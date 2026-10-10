/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\adapter\gorm\connection_quality.go
 * @Description: 连接质量记录仓储 - 与 ConnectionStore 拆分
 *
 * 承载 wsc_connection_qualities 表的 CRUD + batcher 批量更新 + 断开终评
 * models.HeartbeatUpdateEntry/models.StatsIncrementEntry 类型在 connection_repository.go 定义，两 repo 共用
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	sqlbuilder "github.com/kamalyes/go-sqlbuilder/repository"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConnectionQualityStore 连接质量仓储，实现 spi.ConnectionQualityStore 契约
// 承载运行时质量指标(心跳/消息/错误/重连)与评分，随 batcher 高频批量更新
type ConnectionQualityStore struct {
	db         *gorm.DB
	tableName  string // 自定义表名（用于测试隔离）
	logger     logger.ILogger
	cancelFunc context.CancelFunc
}

// NewConnectionQualityStore 创建连接质量仓储实例
// config 复用 ConnectionRecord 配置：质量表随连接记录同窗口清理（按 last_active_at），
// 否则每次连接一行只增不减，高频 UPDATE 会随表膨胀越来越慢
func NewConnectionQualityStore(db *gorm.DB, config *wscconfig.ConnectionRecord, log logger.ILogger) *ConnectionQualityStore {
	ctx, cancel := context.WithCancel(context.Background())

	repo := &ConnectionQualityStore{
		db:         db,
		logger:     log,
		cancelFunc: cancel,
	}

	if config != nil && config.EnableAutoCleanup && config.CleanupDaysAgo > 0 {
		go repo.startCleanupScheduler(ctx, config.CleanupDaysAgo)
	}

	return repo
}

// WithTableName 设置自定义表名（用于测试隔离）
func (r *ConnectionQualityStore) WithTableName(tableName string) *ConnectionQualityStore {
	return &ConnectionQualityStore{
		db:         r.db,
		tableName:  tableName,
		logger:     r.logger,
		cancelFunc: r.cancelFunc,
	}
}

// getDB 获取数据库会话（如果设置了自定义表名则应用）
func (r *ConnectionQualityStore) getDB(ctx context.Context) *gorm.DB {
	db := r.db.WithContext(ctx)
	if r.tableName != "" {
		return db.Table(r.tableName)
	}
	return db.Model(&models.ConnectionQuality{})
}

// newQuery 为批量循环中的每条 UPDATE 构建干净会话
// GORM 复用同一实例时 Where 条件会累积到共享 Statement（曾导致线上
// WHERE connection_id='A' AND connection_id='B' 永不命中、统计静默丢失），必须逐条新建
func (r *ConnectionQualityStore) newQuery(tx *gorm.DB) *gorm.DB {
	db := tx.Session(&gorm.Session{NewDB: true})
	if r.tableName != "" {
		return db.Table(r.tableName)
	}
	return db.Model(&models.ConnectionQuality{})
}

// ========== 核心操作 ==========

// Upsert 创建或更新质量记录
// 首次连接：建初始零值行（QualityScore=100）
// 重连：reconnect_count+1，刷新 last_active_at/user_id
func (r *ConnectionQualityStore) Upsert(ctx context.Context, quality *models.ConnectionQuality) error {
	if quality == nil {
		return fmt.Errorf("quality cannot be nil")
	}
	if quality.ConnectionID == "" {
		return fmt.Errorf("connection_id cannot be empty")
	}

	// 兜底多租户维度（与 connect 表 + Bitmap/ZSET 分桶一致，避免零值导致跨域查询错位）
	quality.AppID = constants.NormalizeAppID(quality.AppID)
	quality.Namespace = constants.NormalizeNamespace(quality.Namespace)

	if quality.QualityScore == 0 {
		quality.QualityScore = 100
	}
	now := time.Now()
	if quality.LastActiveAt == nil {
		quality.LastActiveAt = &now
	}

	// 冲突时递增重连次数并刷新活跃时间（与 ConnectionStore.BatchUpsert 的 reconnect_count 语义对齐）
	// ⚠️ 自引用列必须用表名限定：PostgreSQL 的 ON CONFLICT DO UPDATE 中未限定列名会同时匹配
	// excluded.reconnect_count 与目标表列，触发 SQLSTATE 42702（ambiguous column reference）；
	// 表名限定写法对 MySQL/SQLite 同样兼容（支持测试自定义表名）
	dialect := sqlbuilder.DetectDialect(r.db)
	table := r.tableName
	if table == "" {
		table = models.ConnectionQuality{}.TableName()
	}
	onConflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "connection_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"reconnect_count": gorm.Expr(table + ".reconnect_count + 1"),
			"user_id":         gorm.Expr(dialect.UpsertColumnRef("user_id")),
			"last_active_at":  gorm.Expr(dialect.UpsertColumnRef("last_active_at")),
		}),
	}

	return r.getDB(ctx).
		Clauses(onConflict).
		Omit("").
		Create(quality).Error
}

// BatchUpdateHeartbeats 批量更新 Ping 统计与活跃时间（quality 表）
// 心跳时间戳(last_ping_at)由 ConnectionStore.BatchUpdateHeartbeats 写入
// CASE WHEN 单 SQL 合并（含移动平均的自引用嵌套 CASE），消除逐条 Updates 开销
// （CPU profile 实测占全进程 21%）；单 SQL 自身原子，无需事务包裹；
// 三方言原生兼容（CASE WHEN + 列自引用为 SQL 标准）
func (r *ConnectionQualityStore) BatchUpdateHeartbeats(ctx context.Context, entries []*models.HeartbeatUpdateEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// 批内同连接多条 entry 按字段级合并（活跃时间/PingMs 各取最后有效值），
	// 等价于逐条 UPDATE 的最终状态——entry 级覆盖会在字段互补时丢更新
	type qualityPatch struct {
		activeAt *time.Time
		pingMs   float64
	}
	patches := make(map[string]*qualityPatch, len(entries))
	for _, e := range entries {
		p := patches[e.ConnectionID]
		if p == nil {
			p = &qualityPatch{}
			patches[e.ConnectionID] = p
		}
		if e.PingTime != nil {
			p.activeAt = e.PingTime
		}
		if e.PingMs > 0 {
			p.pingMs = e.PingMs
		}
	}

	var activeCount, pingMsCount int
	for _, p := range patches {
		if p.activeAt != nil {
			activeCount++
		}
		if p.pingMs > 0 {
			pingMsCount++
		}
	}
	if activeCount == 0 && pingMsCount == 0 {
		return nil
	}

	table := r.tableName
	if table == "" {
		table = (&models.ConnectionQuality{}).TableName()
	}

	var sb strings.Builder
	// 参数上限预估：active 2/条 + avg/max/min 各 4/条 + IN 1/条
	args := make([]interface{}, 0, activeCount*2+pingMsCount*12+len(patches))
	sb.WriteString("UPDATE ")
	sb.WriteString(table)
	sb.WriteString(" SET ")

	if activeCount > 0 {
		sb.WriteString("last_active_at = CASE connection_id")
		for cid, p := range patches {
			if p.activeAt != nil {
				sb.WriteString(" WHEN ? THEN ?")
				args = append(args, cid, *p.activeAt)
			}
		}
		sb.WriteString(" ELSE last_active_at END")
	}

	if pingMsCount > 0 {
		if activeCount > 0 {
			sb.WriteByte(',')
		}
		// 三列同一批 patch 的 WHEN 骨架，value 表达式各异（列自引用）；
		// 写作子函数避免三段近似代码散落重复
		appendStatCase := func(col string, valueExpr string, extraArgs func(p *qualityPatch) []interface{}) {
			sb.WriteString(col)
			sb.WriteString(" = CASE connection_id")
			for cid, p := range patches {
				if p.pingMs > 0 {
					sb.WriteString(" WHEN ? THEN ")
					sb.WriteString(valueExpr)
					args = append(args, cid)
					args = append(args, extraArgs(p)...)
				}
			}
			sb.WriteString(" ELSE ")
			sb.WriteString(col)
			sb.WriteString(" END")
		}
		appendStatCase("average_ping_ms",
			"CASE WHEN average_ping_ms > 0 THEN average_ping_ms * 0.7 + ? * 0.3 ELSE ? END",
			func(p *qualityPatch) []interface{} { return []interface{}{p.pingMs, p.pingMs} })
		sb.WriteByte(',')
		appendStatCase("max_ping_ms",
			"CASE WHEN max_ping_ms = 0 OR max_ping_ms < ? THEN ? ELSE max_ping_ms END",
			func(p *qualityPatch) []interface{} { return []interface{}{p.pingMs, p.pingMs} })
		sb.WriteByte(',')
		appendStatCase("min_ping_ms",
			"CASE WHEN min_ping_ms = 0 OR min_ping_ms > ? THEN ? ELSE min_ping_ms END",
			func(p *qualityPatch) []interface{} { return []interface{}{p.pingMs, p.pingMs} })
	}

	sb.WriteString(" WHERE connection_id IN (")
	first := true
	for cid := range patches {
		if !first {
			sb.WriteByte(',')
		}
		sb.WriteByte('?')
		args = append(args, cid)
		first = false
	}
	sb.WriteString(")")

	return r.db.WithContext(ctx).Exec(sb.String(), args...).Error
}

// BatchIncrementStats 批量递增消息/字节统计（CASE WHEN 单 SQL 合并）
// 旧实现为事务内逐条 UPDATE（批 200 = 200 条 SQL + 事务开销），且 5s flush ctx 到期后
// 循环内剩余条目立即失败（ms:0 context deadline exceeded）并逐条打 ERROR 日志，
// 形成超时雪崩与日志风暴；单 SQL 化后整批合并为一条自增语句，整体成功或失败
// 自增语义经「列 + CASE」实现：col = col + CASE connection_id WHEN ? THEN ? ELSE 0 END，
// 无增量的行加 0 不变；批内同连接多条 entry 先合并累加（等价逐条自增的最终状态）
func (r *ConnectionQualityStore) BatchIncrementStats(ctx context.Context, entries []*models.StatsIncrementEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// 批内同连接合并：四列各自累加
	type statPatch struct{ msgSent, msgRecv, bytesSent, bytesRecv int64 }
	patches := make(map[string]*statPatch, len(entries))
	for _, e := range entries {
		p := patches[e.ConnectionID]
		if p == nil {
			p = &statPatch{}
			patches[e.ConnectionID] = p
		}
		p.msgSent += e.MessagesSent
		p.msgRecv += e.MessagesReceived
		p.bytesSent += e.BytesSent
		p.bytesRecv += e.BytesReceived
	}

	// 存在增量的列才进 SET（entry 全为零增量的列跳过）
	var ms, mr, bs, br int
	for _, p := range patches {
		if p.msgSent > 0 {
			ms++
		}
		if p.msgRecv > 0 {
			mr++
		}
		if p.bytesSent > 0 {
			bs++
		}
		if p.bytesRecv > 0 {
			br++
		}
	}
	if ms+mr+bs+br == 0 {
		return nil
	}

	table := r.tableName
	if table == "" {
		table = (&models.ConnectionQuality{}).TableName()
	}

	var sb strings.Builder
	// 参数预估：每列 2/条 + updated_at 1 + IN 1/条
	args := make([]interface{}, 0, (ms+mr+bs+br)*2+len(patches)+1)
	sb.WriteString("UPDATE ")
	sb.WriteString(table)
	// 逐条 Updates 由 gorm 自动维护 updated_at，手写 SQL 显式保留同一行为
	sb.WriteString(" SET updated_at = ?")
	args = append(args, time.Now())

	// 四列共用同一 WHEN 骨架（自增式 CASE，ELSE 0），写作子函数避免四段近似代码散落重复
	appendIncrCase := func(col string, nonzero func(p *statPatch) int64, count int) {
		if count == 0 {
			return
		}
		sb.WriteByte(',')
		sb.WriteString(col)
		sb.WriteString(" = ")
		sb.WriteString(col)
		sb.WriteString(" + CASE connection_id")
		for cid, p := range patches {
			if v := nonzero(p); v > 0 {
				sb.WriteString(" WHEN ? THEN ?")
				args = append(args, cid, v)
			}
		}
		sb.WriteString(" ELSE 0 END")
	}
	appendIncrCase("messages_sent", func(p *statPatch) int64 { return p.msgSent }, ms)
	appendIncrCase("messages_received", func(p *statPatch) int64 { return p.msgRecv }, mr)
	appendIncrCase("bytes_sent", func(p *statPatch) int64 { return p.bytesSent }, bs)
	appendIncrCase("bytes_received", func(p *statPatch) int64 { return p.bytesRecv }, br)

	sb.WriteString(" WHERE connection_id IN (")
	first := true
	for cid := range patches {
		if !first {
			sb.WriteByte(',')
		}
		sb.WriteByte('?')
		args = append(args, cid)
		first = false
	}
	sb.WriteString(")")

	return r.db.WithContext(ctx).Exec(sb.String(), args...).Error
}

// BatchAddErrors 批量记录连接错误（CASE WHEN 单 SQL 合并）
// 同一连接的多次错误在攒批 flush 时合并：ErrorCount 累加，LastError/LastErrorAt 取最新一次；
// error_count 为自增式 CASE（ELSE 0），last_error/last_error_at 为覆盖式 CASE（ELSE 保列值），
// 与 BatchIncrementStats 同模式消除事务内逐条 UPDATE 的超时雪崩与日志风暴
func (r *ConnectionQualityStore) BatchAddErrors(ctx context.Context, entries []*models.ErrorUpdateEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// 批内同连接合并：次数累加，最新错误覆盖
	type errPatch struct {
		count   int64
		lastMsg string
		lastAt  time.Time
	}
	patches := make(map[string]*errPatch, len(entries))
	for _, e := range entries {
		p := patches[e.ConnectionID]
		if p == nil {
			p = &errPatch{}
			patches[e.ConnectionID] = p
		}
		p.count += e.ErrorCount
		p.lastMsg = e.LastError
		p.lastAt = e.LastErrorAt
	}

	table := r.tableName
	if table == "" {
		table = (&models.ConnectionQuality{}).TableName()
	}

	var sb strings.Builder
	// 参数上限预估：error_count 2/条 + last_error 2/条 + last_error_at 2/条 + updated_at 1 + IN 1/条
	args := make([]interface{}, 0, len(patches)*7+1)
	sb.WriteString("UPDATE ")
	sb.WriteString(table)
	sb.WriteString(" SET updated_at = ?, error_count = error_count + CASE connection_id")
	args = append(args, time.Now())
	for cid, p := range patches {
		sb.WriteString(" WHEN ? THEN ?")
		args = append(args, cid, p.count)
	}
	sb.WriteString(" ELSE 0 END, last_error = CASE connection_id")
	for cid, p := range patches {
		sb.WriteString(" WHEN ? THEN ?")
		args = append(args, cid, p.lastMsg)
	}
	sb.WriteString(" ELSE last_error END, last_error_at = CASE connection_id")
	for cid, p := range patches {
		sb.WriteString(" WHEN ? THEN ?")
		args = append(args, cid, p.lastAt)
	}
	sb.WriteString(" ELSE last_error_at END WHERE connection_id IN (")
	first := true
	for cid := range patches {
		if !first {
			sb.WriteByte(',')
		}
		sb.WriteByte('?')
		args = append(args, cid)
		first = false
	}
	sb.WriteString(")")

	return r.db.WithContext(ctx).Exec(sb.String(), args...).Error
}

// BatchFinalizeOnDisconnect 批量断开终评（读质量行算 FinalScore 写 quality_score）
// 旧停机路径逐连接调用（每连接 SELECT quality + SELECT duration + UPDATE score = 3 条 SQL），
// 26w 连接 = 78w 条 SQL 在停机窗口直打 DB，是滚动更新期 DB 过载主源（下游批量 SQL 5s 超时的加害者）；批量化：1 条 IN 读质量行 + Go 内算分 + 1 条 CASE WHEN 单 SQL 写回
// duration 由调用方从 MarkDisconnectedBatch 返回的 entries 直接带入（与 SQL 写入值同源同公式），免去把刚落库的 duration 再读回来的往返
// 与旧单条路径同语义：质量记录不存在（已被清理）的连接自然跳过（Find 不返回）
func (r *ConnectionQualityStore) BatchFinalizeOnDisconnect(ctx context.Context, entries []*models.DisconnectionEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// duration 与 BatchMarkDisconnected 的 SQL 写入公式同源（ConnectedAt 零值 → 0）
	durationOf := func(e *models.DisconnectionEntry) int64 {
		if e.ConnectedAt.IsZero() {
			return 0
		}
		return int64(e.DisconnectedAt.Sub(e.ConnectedAt).Seconds())
	}
	entryByID := make(map[string]*models.DisconnectionEntry, len(entries))
	connectionIDs := make([]string, len(entries))
	for i, e := range entries {
		entryByID[e.ConnectionID] = e
		connectionIDs[i] = e.ConnectionID
	}

	// 读质量行（整行：FinalScore → LiveScore 依赖全部统计列）
	var qualities []*models.ConnectionQuality
	if err := r.getDB(ctx).Where("connection_id IN ?", connectionIDs).Find(&qualities).Error; err != nil {
		return fmt.Errorf("批量查询质量记录失败: %w", err)
	}
	if len(qualities) == 0 {
		return nil
	}

	table := r.tableName
	if table == "" {
		table = (&models.ConnectionQuality{}).TableName()
	}

	var sb strings.Builder
	// 参数预估：CASE 2/条 + IN 1/条
	args := make([]interface{}, 0, len(qualities)*3)
	sb.WriteString("UPDATE ")
	sb.WriteString(table)
	sb.WriteString(" SET quality_score = CASE connection_id")
	for _, q := range qualities {
		e := entryByID[q.ConnectionID]
		score := float64(0)
		if e != nil {
			score = q.FinalScore(durationOf(e))
		}
		sb.WriteString(" WHEN ? THEN ?")
		args = append(args, q.ConnectionID, score)
	}
	sb.WriteString(" ELSE quality_score END WHERE connection_id IN (")
	first := true
	for _, q := range qualities {
		if !first {
			sb.WriteByte(',')
		}
		sb.WriteByte('?')
		args = append(args, q.ConnectionID)
		first = false
	}
	sb.WriteString(")")

	return r.db.WithContext(ctx).Exec(sb.String(), args...).Error
}

// GetByConnectionID 根据连接ID获取质量记录
func (r *ConnectionQualityStore) GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionQuality, error) {
	var quality models.ConnectionQuality
	err := r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		First(&quality).Error
	if err != nil {
		return nil, err
	}
	return &quality, nil
}

// GetByUserID 根据用户ID获取所有质量记录
func (r *ConnectionQualityStore) GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionQuality, error) {
	var records []*models.ConnectionQuality
	err := r.getDB(ctx).
		Where("user_id = ?", userID).
		Find(&records).Error
	return records, err
}

// GetHighErrorRateConnections 获取高错误率连接
func (r *ConnectionQualityStore) GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*models.ConnectionQuality, error) {
	var records []*models.ConnectionQuality

	query := sqlbuilder.NewQuery().
		AddFilter(sqlbuilder.NewGteFilter("error_count", errorThreshold)).
		AddOrder("error_count", "DESC")

	if limit > 0 {
		query.Limit(limit)
	}

	gormDB := r.getDB(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)
	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
	return records, err
}

// GetFrequentReconnectConnections 获取频繁重连的连接
func (r *ConnectionQualityStore) GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold int, limit int) ([]*models.ConnectionQuality, error) {
	var records []*models.ConnectionQuality

	query := sqlbuilder.NewQuery().
		AddFilter(sqlbuilder.NewGteFilter("reconnect_count", reconnectThreshold)).
		AddOrder("reconnect_count", "DESC")

	if limit > 0 {
		query.Limit(limit)
	}

	gormDB := r.getDB(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)
	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
	return records, err
}

// Close 关闭仓库，停止后台清理任务
func (r *ConnectionQualityStore) Close() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
	}
	return nil
}

// CleanupInactiveRecords 清理指定时间前不再活跃的质量记录（硬删：历史质量指标无回溯消费方）
// 断连后心跳停止，last_active_at 停在最后一次活跃时刻，按它清理与连接记录同窗口
func (r *ConnectionQualityStore) CleanupInactiveRecords(ctx context.Context, before time.Time) (int64, error) {
	result := r.getDB(ctx).
		Where("last_active_at < ?", before).
		Delete(&models.ConnectionQuality{})

	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
}

// startCleanupScheduler 启动定时清理任务（使用 EventLoop，每天执行一次，与 ConnectionStore 同模式）
func (r *ConnectionQualityStore) startCleanupScheduler(ctx context.Context, daysAgo int) {
	// 立即执行一次清理
	r.cleanupOldData(ctx, daysAgo)

	syncx.NewEventLoop(ctx).
		OnTicker(24*time.Hour, func() {
			r.cleanupOldData(ctx, daysAgo)
		}).
		OnPanic(func(rec any) {
			r.logger.Errorf("⚠️ 质量记录清理任务 panic: %v, stack: %s", rec, debug.Stack())
		}).
		OnShutdown(func() {
			r.logger.Info("🛑 质量记录清理任务已停止")
		}).
		Run()
}

// cleanupOldData 清理N天前的非活跃质量记录
func (r *ConnectionQualityStore) cleanupOldData(ctx context.Context, daysAgo int) {
	if daysAgo <= 0 {
		return
	}

	before := time.Now().AddDate(0, 0, -daysAgo)

	deleted, err := r.CleanupInactiveRecords(ctx, before)
	if err != nil {
		r.logger.Warnf("⚠️ 清理历史质量记录失败: %v", err)
	} else if deleted > 0 {
		r.logger.Infof("🧹 已清理 %d 天前的非活跃质量记录，删除 %d 条", daysAgo, deleted)
	}
}

// 编译期断言：repository 实现必须满足 spi 契约（Phase 4 迁仓后适配器同样受此约束）
var _ spi.ConnectionQualityStore = (*ConnectionQualityStore)(nil)

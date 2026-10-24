/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:00:16
 * @FilePath: \go-wsc\adapter\gorm\connection_store.go
 * @Description: WebSocket连接记录仓库接口（瘦身版）
 *
 * 拆表后承载 connect 身份+会话生命周期+心跳时间戳(wsc_connection_records)
 * Ping统计/消息/错误/评分等质量指标由 ConnectionQualityStore(wsc_connection_qualities) 承载
 * HeartbeatUpdateEntry/StatsIncrementEntry 契约类型见 models/contract.go，两 repo 共用，供 batcher 提交
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"errors"
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

// ========== 统计结构体定义（两 repo 共用） ==========

// HeartbeatUpdateEntry / StatsIncrementEntry 已迁至 models/contract.go —— 它们是
// spi.ConnectionStore / spi.ConnectionQualityStore 接口签名的一部分。

// ConnectionQueryOptions / ConnectionStats / UserConnectionStats / NodeConnectionStats
// 已迁至 models/contract.go —— 它们是 spi.ConnectionStore 接口签名的一部分，
// 接口层不应反向依赖本包。

// ConnectionStore WebSocket 连接记录仓储，实现 spi.ConnectionStore 契约
// 设计原则：支持多设备登录，每个连接维护独立记录
// 质量指标(Ping统计/消息字节统计/错误/评分)由 ConnectionQualityStore 承载
// 心跳时间戳(last_ping_at/last_pong_at)属会话生命周期语义，由本仓储随心跳批量更新
type ConnectionStore struct {
	db         *gorm.DB
	tableName  string // 自定义表名（用于测试隔离）
	logger     logger.ILogger
	cancelFunc context.CancelFunc
}

// NewConnectionStore 创建连接记录仓储实例
//
// 设计说明：
//   - 支持多设备登录，每个连接维护独立记录
//   - 通过 connection_id 唯一标识每个连接
//   - 通过 is_active 字段区分当前是否在线
//
// 参数:
//   - db: GORM 数据库实例
//   - config: 连接记录配置对象（可选，传 nil 则不启用自动清理）
//   - log: 日志记录器
func NewConnectionStore(db *gorm.DB, config *wscconfig.ConnectionRecord, log logger.ILogger) *ConnectionStore {
	ctx, cancel := context.WithCancel(context.Background())

	repo := &ConnectionStore{
		db:         db,
		logger:     log,
		cancelFunc: cancel,
	}

	// 启动定时清理任务
	if config != nil && config.EnableAutoCleanup && config.CleanupDaysAgo > 0 {
		go repo.startCleanupScheduler(ctx, config.CleanupDaysAgo)
	}

	return repo
}

// WithTableName 设置自定义表名（用于测试隔离）
func (r *ConnectionStore) WithTableName(tableName string) *ConnectionStore {
	return &ConnectionStore{
		db:         r.db,
		tableName:  tableName,
		logger:     r.logger,
		cancelFunc: r.cancelFunc,
	}
}

// getDB 获取数据库会话（如果设置了自定义表名则应用）
func (r *ConnectionStore) getDB(ctx context.Context) *gorm.DB {
	db := r.db.WithContext(ctx)
	if r.tableName != "" {
		return db.Table(r.tableName)
	}
	return db.Model(&models.ConnectionRecord{})
}

// newQuery 为批量循环中的每条 UPDATE 构建干净会话
// GORM 复用同一实例时 Where 条件会累积到共享 Statement，必须逐条新建
func (r *ConnectionStore) newQuery(tx *gorm.DB) *gorm.DB {
	db := tx.Session(&gorm.Session{NewDB: true})
	if r.tableName != "" {
		return db.Table(r.tableName)
	}
	return db.Model(&models.ConnectionRecord{})
}

// ========== 核心操作 ==========

// Upsert 创建或更新连接记录（首次连接创建，重连时更新）
func (r *ConnectionStore) Upsert(ctx context.Context, record *models.ConnectionRecord) error {
	if record == nil {
		return fmt.Errorf("record cannot be nil")
	}
	if record.ConnectionID == "" {
		return fmt.Errorf("connection_id cannot be empty")
	}

	// 兜底多租户维度（与 Bitmap/ZSET 分桶一致，避免零值导致跨域查询错位）
	record.AppID = constants.NormalizeAppID(record.AppID)
	record.Namespace = constants.NormalizeNamespace(record.Namespace)

	// 复用 BatchUpsert 的 ON CONFLICT 单 SQL 路径
	// 替代旧的「前置 SELECT 判存在 + Create/Update」两步（重连场景 2 次 DB 往返）
	return r.BatchUpsert(ctx, []*models.ConnectionRecord{record})
}

// BatchMarkDisconnected 批量标记断连终态（CASE WHEN 单 SQL 合并，单条请求包长度 1 数组复用本方法）
// 断连风暴下逐条 UPDATE 是 DB 写入洪峰主因（26w 断连 = 26w 条独立 UPDATE + 26w 个10s 超时 goroutine），攒批后合并为单条大 SQL；与 BatchUpdateHeartbeats 同模式：
// 单 SQL 原子无需事务、ELSE 保列值、三方言通吃（MySQL/PG/CockroachDB）
// 写 duration/disconnected_at/is_abnormal 等会话终态字段，供 FinalizeOnDisconnect 读 duration 算终评；
// connectedAt 由调用方从内存 Client 快照带入（省前置 SELECT）；IN 影响 0 行（记录已被清理）静默返回。
// is_active 全批统一 false 走普通 SET；其余终态字段逐条不同走 CASE WHEN；
// 批内同连接去重保留最后一条（断连是单次事件，终态以最后一次为准）
func (r *ConnectionStore) BatchMarkDisconnected(ctx context.Context, entries []*models.DisconnectionEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// 去重保留最后：后续 entry 覆盖前者（map 天然后写覆盖）
	deduped := make(map[string]*models.DisconnectionEntry, len(entries))
	for _, e := range entries {
		deduped[e.ConnectionID] = e
	}

	table := r.tableName
	if table == "" {
		table = (&models.ConnectionRecord{}).TableName()
	}

	var sb strings.Builder
	// 参数预估：5 列 CASE WHEN 各 2/条 + IN 1/条
	args := make([]interface{}, 0, len(deduped)*11)
	sb.WriteString("UPDATE ")
	sb.WriteString(table)
	sb.WriteString(" SET is_active = false")

	// 五个终态字段共用同一 WHEN 骨架，写作子函数避免五段近似代码散落重复
	appendTermCase := func(col string, value func(e *models.DisconnectionEntry) interface{}) {
		sb.WriteByte(',')
		sb.WriteString(col)
		sb.WriteString(" = CASE connection_id")
		for cid, e := range deduped {
			sb.WriteString(" WHEN ? THEN ?")
			args = append(args, cid, value(e))
		}
		sb.WriteString(" ELSE ")
		sb.WriteString(col)
		sb.WriteString(" END")
	}
	appendTermCase("disconnected_at", func(e *models.DisconnectionEntry) interface{} { return e.DisconnectedAt })
	appendTermCase("disconnect_reason", func(e *models.DisconnectionEntry) interface{} { return string(e.Reason) })
	appendTermCase("disconnect_code", func(e *models.DisconnectionEntry) interface{} { return e.Code })
	appendTermCase("duration", func(e *models.DisconnectionEntry) interface{} {
		// 与单条 MarkDisconnected 同语义：connectedAt 零值时 duration 为 0
		if e.ConnectedAt.IsZero() {
			return int64(0)
		}
		return int64(e.DisconnectedAt.Sub(e.ConnectedAt).Seconds())
	})
	appendTermCase("is_abnormal", func(e *models.DisconnectionEntry) interface{} {
		// 与单条路径同判定：客户端主动断/服务端正常关停不算异常
		return e.Reason != models.DisconnectReasonClientRequest && e.Reason != models.DisconnectReasonServerShutdown
	})

	sb.WriteString(" WHERE connection_id IN (")
	first := true
	for cid := range deduped {
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

// BatchUpdateHeartbeats 批量更新心跳时间戳（connect 表 last_ping_at）
// CASE WHEN 单 SQL 合并：批内 N 条逐条 UPDATE 压缩为 1 条
// （SET col = CASE connection_id WHEN ? THEN ? ... ELSE col END WHERE connection_id IN (...)），
// 消除逐条 Updates 的 map 解析/SQL 生成/协议往返（1w 连接 × 30s 心跳 = 333 entry/s 稳态下，
// CPU profile 实测本路径占全进程 22%）；单 SQL 自身原子，无需事务包裹；
// ELSE 保列值使无该字段的条目行保持原样；CASE WHEN 为 SQL 标准，MySQL/PG/CockroachDB 通吃
//
// last_pong_at 已停写：全链路只写不读（dashboard 同步但零查询），
// 心跳统计经 per-connection 节流后落库（见 stats.Manager.TrackHeartbeatStats）
func (r *ConnectionStore) BatchUpdateHeartbeats(ctx context.Context, entries []*models.HeartbeatUpdateEntry) error {
	if len(entries) == 0 {
		return nil
	}

	// 批内同连接多条 entry 按字段级合并（PingTime 取最后非 nil），
	// 等价于逐条 UPDATE 的最终状态
	type connectPatch struct{ ping *time.Time }
	patches := make(map[string]*connectPatch, len(entries))
	for _, e := range entries {
		p := patches[e.ConnectionID]
		if p == nil {
			p = &connectPatch{}
			patches[e.ConnectionID] = p
		}
		if e.PingTime != nil {
			p.ping = e.PingTime
		}
	}

	pingCount := 0
	for _, p := range patches {
		if p.ping != nil {
			pingCount++
		}
	}
	if pingCount == 0 {
		return nil
	}

	table := r.tableName
	if table == "" {
		table = (&models.ConnectionRecord{}).TableName()
	}

	var sb strings.Builder
	args := make([]interface{}, 0, pingCount*2+len(patches))
	sb.WriteString("UPDATE ")
	sb.WriteString(table)
	sb.WriteString(" SET last_ping_at = CASE connection_id")
	for cid, p := range patches {
		if p.ping != nil {
			sb.WriteString(" WHEN ? THEN ?")
			args = append(args, cid, *p.ping)
		}
	}
	sb.WriteString(" ELSE last_ping_at END")

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

// GetByConnectionID 根据连接ID获取连接记录
func (r *ConnectionStore) GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionRecord, error) {
	var record models.ConnectionRecord
	err := r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// GetByUserID 根据用户ID获取所有连接记录（支持多设备）
func (r *ConnectionStore) GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error) {
	return r.List(ctx, &models.ConnectionQueryOptions{
		UserID: userID,
	})
}

// GetActiveByUserID 根据用户ID获取所有活跃连接记录
func (r *ConnectionStore) GetActiveByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error) {
	isActive := true
	return r.List(ctx, &models.ConnectionQueryOptions{
		UserID:   userID,
		IsActive: &isActive,
	})
}

// ========== 查询操作 ==========

// List 通用列表查询（支持条件过滤）
func (r *ConnectionStore) List(ctx context.Context, opts *models.ConnectionQueryOptions) ([]*models.ConnectionRecord, error) {
	query := r.getDB(ctx)

	// 应用查询条件
	query = r.applyQueryOptions(query, opts)

	// 排序
	orderBy := "connected_at DESC"
	if opts != nil && opts.OrderBy != "" {
		orderBy = opts.OrderBy
	}
	query = query.Order(orderBy)

	// 分页
	if opts != nil {
		if opts.Limit > 0 {
			query = query.Limit(opts.Limit)
		}
		if opts.Offset > 0 {
			query = query.Offset(opts.Offset)
		}
	}

	var records []*models.ConnectionRecord
	err := query.Find(&records).Error
	return records, err
}

// Count 统计连接数（支持条件过滤）
func (r *ConnectionStore) Count(ctx context.Context, opts *models.ConnectionQueryOptions) (int64, error) {
	query := r.getDB(ctx)
	query = r.applyQueryOptions(query, opts)

	var count int64
	err := query.Count(&count).Error
	return count, err
}

// applyQueryOptions 应用查询条件
func (r *ConnectionStore) applyQueryOptions(query *gorm.DB, opts *models.ConnectionQueryOptions) *gorm.DB {
	if opts == nil {
		return query
	}

	// 使用 go-sqlbuilder 构建过滤条件
	sqlQuery := sqlbuilder.NewQuery().
		AddFilterIfNotEmpty("user_id", opts.UserID).
		AddFilterIfNotEmpty("node_id", opts.NodeID).
		AddFilterIfNotEmpty("client_ip", opts.ClientIP).
		AddFilterIfNotEmpty("is_active", opts.IsActive).
		AddFilterIfNotEmpty("is_abnormal", opts.IsAbnormal)

	// 应用过滤器到 GORM
	query = sqlbuilder.ApplyFilters(query, sqlQuery.Filters)

	return query
}

// ========== 统计分析操作 ==========

// GetConnectionStats 获取连接统计信息
// 拆表后只统计 connect 表维度(total/active/avg_duration/abnormal_rate)
// 质量维度字段(TotalMessages*/TotalBytes*/AveragePingMs/AverageReconnectCount)保持零值，由调用方按需从 qualityRepo 补充
func (r *ConnectionStore) GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*models.ConnectionStats, error) {
	stats := &models.ConnectionStats{}

	err := r.getDB(ctx).
		Where("connected_at BETWEEN ? AND ?", startTime, endTime).
		Select(`
			COUNT(*) as total_connections,
			SUM(CASE WHEN is_active = true THEN 1 ELSE 0 END) as active_connections,
			AVG(CASE WHEN duration > 0 THEN duration ELSE NULL END) as average_duration,
			CASE WHEN COUNT(*) > 0
				THEN SUM(CASE WHEN is_abnormal = true THEN 1 ELSE 0 END) * 100.0 / COUNT(*)
				ELSE 0
			END as abnormal_rate
		`).
		Scan(stats).Error

	if err != nil {
		return nil, err
	}

	return stats, nil
}

// GetConnectionStatsByID 根据连接ID获取单个连接的统计信息
// 质量维度字段保持零值，由调用方按需从 qualityRepo.GetByConnectionID 补充
func (r *ConnectionStore) GetConnectionStatsByID(ctx context.Context, connectionID string) (*models.UserConnectionStats, error) {
	record, err := r.GetByConnectionID(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("获取连接记录失败: %w", err)
	}

	return &models.UserConnectionStats{
		UserID:         record.UserID,
		IsActive:       record.IsActive,
		ConnectedAt:    record.ConnectedAt,
		DisconnectedAt: record.DisconnectedAt,
		Duration:       record.Duration,
	}, nil
}

// GetUserConnectionStats 获取用户所有连接的汇总统计
// 拆表后只汇总 connect 表维度(Duration/ConnectedAt/DisconnectedAt/IsActive)
// 质量维度字段保持零值，由调用方按需从 qualityRepo.GetByUserID 补充
func (r *ConnectionStore) GetUserConnectionStats(ctx context.Context, userID string) (*models.UserConnectionStats, error) {
	records, err := r.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("获取用户连接记录失败: %w", err)
	}
	if len(records) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// 汇总 connect 表维度统计
	stats := &models.UserConnectionStats{
		UserID: userID,
	}

	for _, record := range records {
		if record.IsActive {
			stats.IsActive = true
		}

		// 使用最早的连接时间
		if stats.ConnectedAt.IsZero() || record.ConnectedAt.Before(stats.ConnectedAt) {
			stats.ConnectedAt = record.ConnectedAt
		}

		// 使用最晚的断开时间
		if record.DisconnectedAt != nil {
			if stats.DisconnectedAt == nil || record.DisconnectedAt.After(*stats.DisconnectedAt) {
				stats.DisconnectedAt = record.DisconnectedAt
			}
		}

		stats.Duration += record.Duration
	}

	return stats, nil
}

// GetNodeConnectionStats 获取节点连接统计
// 拆表后只统计 connect 表维度(total/active/disconnected/abnormal/avg_duration)
// 质量维度字段保持零值，由调用方按需从 qualityRepo 补充
func (r *ConnectionStore) GetNodeConnectionStats(ctx context.Context, nodeID string) (*models.NodeConnectionStats, error) {
	stats := &models.NodeConnectionStats{}

	// 查询节点基本信息和汇总统计
	err := r.getDB(ctx).
		Where("node_id = ?", nodeID).
		Select(`
			? as node_id,
			MAX(node_ip) as node_ip,
			MAX(node_port) as node_port,
			COUNT(*) as total_connections,
			SUM(CASE WHEN is_active = true THEN 1 ELSE 0 END) as active_connections,
			SUM(CASE WHEN is_active = false THEN 1 ELSE 0 END) as disconnected_count,
			SUM(CASE WHEN is_abnormal = true THEN 1 ELSE 0 END) as abnormal_count,
			AVG(CASE WHEN duration > 0 THEN duration ELSE NULL END) as average_duration
		`, nodeID).
		Scan(stats).Error

	if err != nil {
		return nil, fmt.Errorf("查询节点统计失败: %w", err)
	}

	// 计算异常率
	if stats.TotalConnections > 0 {
		stats.AbnormalRate = float64(stats.AbnormalCount) / float64(stats.TotalConnections) * 100
	}

	return stats, nil
}

// ========== 批量操作 ==========

// BatchUpsert 批量创建或更新连接记录
// 使用 INSERT ... ON DUPLICATE KEY UPDATE 替代逐条 SELECT + INSERT/UPDATE
// 将 2N 次 DB 调用压缩为 1 次批量 SQL
// 拆表后 OnConflict 只更新 connect 身份+会话生命周期字段，质量指标重置由 qualityRepo 负责
func (r *ConnectionStore) BatchUpsert(ctx context.Context, records []*models.ConnectionRecord) error {
	if len(records) == 0 {
		return nil
	}

	// 冲突时更新重连相关字段（与重连语义一致），metadata 不在冲突分支——
	// 它只随首次 INSERT 写入（同一 connection_id 的重复注册来自同一次握手，metadata 不变），
	// 跳过可避免每次重连全量重写 1-2KB JSON 的写放大
	// 通过 Dialect 引擎兼容 MySQL 的 VALUES(col) 与 SQLite/PostgreSQL 的 excluded.col
	dialect := sqlbuilder.DetectDialect(r.db)
	onConflict := clause.OnConflict{
		Columns: []clause.Column{{Name: "connection_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"node_id":           gorm.Expr(dialect.UpsertColumnRef("node_id")),
			"node_ip":           gorm.Expr(dialect.UpsertColumnRef("node_ip")),
			"node_port":         gorm.Expr(dialect.UpsertColumnRef("node_port")),
			"client_ip":         gorm.Expr(dialect.UpsertColumnRef("client_ip")),
			"client_type":       gorm.Expr(dialect.UpsertColumnRef("client_type")),
			"protocol":          gorm.Expr(dialect.UpsertColumnRef("protocol")),
			"connected_at":      gorm.Expr("CURRENT_TIMESTAMP"),
			"disconnected_at":   nil,
			"duration":          0,
			"last_ping_at":      nil,
			"last_pong_at":      nil,
			"is_active":         true,
			"is_abnormal":       false,
			"is_forced_offline": false,
			"disconnect_reason": "",
			"disconnect_code":   0,
		}),
	}

	return r.getDB(ctx).
		Clauses(onConflict).
		Omit("").
		CreateInBatches(records, 500).Error
}

// ========== 清理操作 ==========

// CleanupInactiveRecords 清理非活跃记录
// 按主键分批删除（deleteInBatches）：百万级连接下断连历史积压量大，
// 无界单条 DELETE 必然超时回滚致 0 行删除（见 batch_delete.go 头注），分批保积压收敛
func (r *ConnectionStore) CleanupInactiveRecords(ctx context.Context, before time.Time) (int64, error) {
	return deleteInBatches(ctx, r.getDB(ctx), &models.ConnectionRecord{},
		"disconnected_at < ? AND is_active = ?", before, false)
}

// startCleanupScheduler 启动定时清理任务（使用 EventLoop，每天执行一次）
func (r *ConnectionStore) startCleanupScheduler(ctx context.Context, daysAgo int) {
	// 立即执行一次清理
	r.cleanupOldData(ctx, daysAgo)

	// 使用 EventLoop 管理定时任务
	syncx.NewEventLoop(ctx).
		// 每天执行一次清理
		OnTicker(24*time.Hour, func() {
			r.cleanupOldData(ctx, daysAgo)
		}).
		// Panic 处理
		OnPanic(func(rec any) {
			r.logger.Errorf("⚠️ 连接记录清理任务 panic: %v, stack: %s", rec, debug.Stack())
		}).
		// 优雅关闭
		OnShutdown(func() {
			r.logger.Info("🛑 连接记录清理任务已停止")
		}).
		Run()
}

// cleanupOldData 清理N天前的非活跃连接记录
// 每轮限时 10 分钟：积压大到删不完时中断本轮，已提交批次保留，下轮调度从断点续删（分批语义）
func (r *ConnectionStore) cleanupOldData(ctx context.Context, daysAgo int) {
	if daysAgo <= 0 {
		return
	}

	before := time.Now().AddDate(0, 0, -daysAgo)

	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	deleted, err := r.CleanupInactiveRecords(cleanupCtx, before)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		r.logger.Warnf("⚠️ 清理历史连接记录失败: %v", err)
	} else if deleted > 0 {
		r.logger.Infof("🧹 已清理 %d 天前的非活跃连接记录，删除 %d 条", daysAgo, deleted)
	}
}

// Close 关闭仓库，停止后台清理任务
func (r *ConnectionStore) Close() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
		r.logger.Info("🛑 ConnectionStore 已关闭")
	}
	return nil
}

// 编译期断言：repository 实现必须满足 spi 契约（Phase 4 迁仓后适配器同样受此约束）
var _ spi.ConnectionStore = (*ConnectionStore)(nil)

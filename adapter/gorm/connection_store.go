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
	"fmt"
	"runtime/debug"
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

	existing, err := r.GetByConnectionID(ctx, record.ConnectionID)
	if err != nil && err != gorm.ErrRecordNotFound {
		return fmt.Errorf("查询连接记录失败: %w", err)
	}

	if existing != nil {
		return r.updateConnectionRecord(ctx, record)
	}

	// 创建新记录时，使用 Omit("") 确保所有字段都被插入（包括零值）
	return r.getDB(ctx).
		Omit("").
		Create(record).Error
}

// updateConnectionRecord 更新现有连接记录（重连场景）
// 拆表后只刷新 connect 身份+会话生命周期字段（含重置心跳时间戳），质量指标重置由 qualityRepo.Upsert 负责
func (r *ConnectionStore) updateConnectionRecord(ctx context.Context, record *models.ConnectionRecord) error {
	now := time.Now()
	updates := map[string]any{
		"node_id":           record.NodeID,
		"node_ip":           record.NodeIP,
		"node_port":         record.NodePort,
		"client_ip":         record.ClientIP,
		"client_type":       record.ClientType,
		"protocol":          record.Protocol,
		"connected_at":      now,
		"disconnected_at":   nil,
		"duration":          0,
		"last_ping_at":      nil,
		"last_pong_at":      nil,
		"is_active":         true,
		"is_abnormal":       false,
		"is_forced_offline": false,
		"metadata":          record.Metadata,
		"disconnect_reason": "",
		"disconnect_code":   0,
	}

	return r.getDB(ctx).
		Where("connection_id = ?", record.ConnectionID).
		Updates(updates).Error
}

// MarkDisconnected 标记连接为已断开
// 写 duration/disconnected_at/is_abnormal 等会话终态字段，供 qualityRepo.FinalizeOnDisconnect 读 duration 算终评
func (r *ConnectionStore) MarkDisconnected(ctx context.Context, connectionID string, reason models.DisconnectReason, code int) error {
	record, err := r.GetByConnectionID(ctx, connectionID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			// 连接记录不存在（可能已被清理），直接返回
			return nil
		}
		return fmt.Errorf("查询连接记录失败: %w", err)
	}

	now := time.Now()
	duration := int64(now.Sub(record.ConnectedAt).Seconds())
	isAbnormal := reason != models.DisconnectReasonClientRequest && reason != models.DisconnectReasonServerShutdown

	updates := map[string]any{
		"disconnected_at":   now,
		"disconnect_reason": string(reason),
		"disconnect_code":   code,
		"duration":          duration,
		"is_active":         false,
		"is_abnormal":       isAbnormal,
	}

	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(updates).Error
}

// BatchUpdateHeartbeats 批量更新心跳时间戳（connect 表 last_ping_at/last_pong_at）
// 使用单事务包裹所有更新，将 N 次 BeginTx/Commit 压缩为 1 次
// 单条失败不影响其他条目（continue 跳过），Ping 统计由 ConnectionQualityStore 写 quality 表
func (r *ConnectionStore) BatchUpdateHeartbeats(ctx context.Context, entries []*models.HeartbeatUpdateEntry) error {
	if len(entries) == 0 {
		return nil
	}

	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx
		if r.tableName != "" {
			query = tx.Table(r.tableName)
		} else {
			query = tx.Model(&models.ConnectionRecord{})
		}

		for _, entry := range entries {
			updates := make(map[string]any)
			if entry.PingTime != nil {
				updates["last_ping_at"] = entry.PingTime
			}
			if entry.PongTime != nil {
				updates["last_pong_at"] = entry.PongTime
			}
			if len(updates) == 0 {
				continue
			}
			if err := query.Where("connection_id = ?", entry.ConnectionID).Updates(updates).Error; err != nil {
				continue // 单条失败不影响其他条目
			}
		}
		return nil // 始终提交事务（单条失败已跳过）
	})
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

	// 冲突时更新重连相关字段（与 updateConnectionRecord 逻辑一致）
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
			"metadata":          gorm.Expr(dialect.UpsertColumnRef("metadata")),
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
func (r *ConnectionStore) CleanupInactiveRecords(ctx context.Context, before time.Time) (int64, error) {
	result := r.getDB(ctx).
		Where("disconnected_at < ? AND is_active = ?", before, false).
		Delete(&models.ConnectionRecord{})

	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
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
func (r *ConnectionStore) cleanupOldData(ctx context.Context, daysAgo int) {
	if daysAgo <= 0 {
		return
	}

	before := time.Now().AddDate(0, 0, -daysAgo)

	deleted, err := r.CleanupInactiveRecords(ctx, before)
	if err != nil {
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

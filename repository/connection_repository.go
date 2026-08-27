/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:00:16
 * @FilePath: \go-wsc\repository\connection_repository.go
 * @Description: WebSocket连接记录仓库接口（瘦身版）
 *
 * 拆表后承载 connect 身份+会话生命周期+心跳时间戳(wsc_connection_records)
 * Ping统计/消息/错误/评分等质量指标已迁移到 ConnectionQualityRepository(wsc_connection_qualities)
 * HeartbeatUpdateEntry/StatsIncrementEntry 类型在此定义，两 repo 共用，供 batcher 提交
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package repository

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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ConnectionRecordRepository WebSocket连接记录仓储接口（瘦身版）
// 设计原则：支持多设备登录，每个连接维护独立记录
// 质量指标(Ping统计/消息字节统计/错误/评分)由 ConnectionQualityRepository 承载
// 心跳时间戳(last_ping_at/last_pong_at)属会话生命周期语义，由本仓储随心跳批量更新
type ConnectionRecordRepository interface {
	// ========== 核心操作 ==========

	// Upsert 创建或更新连接记录（首次连接创建，重连时更新）
	Upsert(ctx context.Context, record *models.ConnectionRecord) error

	// MarkDisconnected 标记连接为已断开（写 duration/disconnected_at/is_abnormal 供质量终评读）
	MarkDisconnected(ctx context.Context, connectionID string, reason models.DisconnectReason, code int) error

	// BatchUpdateHeartbeats 批量更新心跳时间戳（connect 表 last_ping_at/last_pong_at，单事务）
	BatchUpdateHeartbeats(ctx context.Context, entries []*HeartbeatUpdateEntry) error

	// GetByConnectionID 根据连接ID获取连接记录
	GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionRecord, error)

	// GetByUserID 根据用户ID获取所有连接记录（支持多设备）
	GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error)

	// GetActiveByUserID 根据用户ID获取所有活跃连接记录
	GetActiveByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error)

	// ========== 查询操作 ==========

	// List 通用列表查询（支持条件过滤）
	List(ctx context.Context, opts *ConnectionQueryOptions) ([]*models.ConnectionRecord, error)

	// Count 统计连接数（支持条件过滤）
	Count(ctx context.Context, opts *ConnectionQueryOptions) (int64, error)

	// ========== 统计分析操作 ==========

	// GetConnectionStats 获取连接统计信息（仅 connect 身份维度，质量维度由 qualityRepo 补充）
	GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*ConnectionStats, error)

	// GetConnectionStatsByID 根据连接ID获取单个连接的统计信息
	GetConnectionStatsByID(ctx context.Context, connectionID string) (*UserConnectionStats, error)

	// GetUserConnectionStats 获取用户所有连接的汇总统计
	GetUserConnectionStats(ctx context.Context, userID string) (*UserConnectionStats, error)

	// GetNodeConnectionStats 获取节点连接统计
	GetNodeConnectionStats(ctx context.Context, nodeID string) (*NodeConnectionStats, error)

	// ========== 批量操作 ==========

	// BatchUpsert 批量创建或更新连接记录
	BatchUpsert(ctx context.Context, records []*models.ConnectionRecord) error

	// ========== 清理操作 ==========

	// CleanupInactiveRecords 清理非活跃记录
	CleanupInactiveRecords(ctx context.Context, before time.Time) (int64, error)

	// ========== 配置操作 ==========

	// WithTableName 设置自定义表名（用于测试隔离）
	WithTableName(tableName string) ConnectionRecordRepository

	// Close 关闭仓库，停止后台任务
	Close() error
}

// ========== 统计结构体定义（两 repo 共用） ==========

// HeartbeatUpdateEntry 心跳批量更新条目（两 repo 分工消费）
// PingTime/PongTime 由 ConnectionRecordRepository.BatchUpdateHeartbeats 写 connect 表时间戳
// PingMs 由 ConnectionQualityRepository.BatchUpdateHeartbeats 写 quality 表 Ping 统计与活跃时间
type HeartbeatUpdateEntry struct {
	ConnectionID string
	PingTime     *time.Time
	PongTime     *time.Time
	PingMs       float64 // >0 时更新 average/max/min_ping_ms
}

// StatsIncrementEntry 统计递增条目（由 ConnectionQualityRepository.BatchIncrementStats 消费）
type StatsIncrementEntry struct {
	ConnectionID     string
	MessagesSent     int64
	MessagesReceived int64
	BytesSent        int64
	BytesReceived    int64
}

// ConnectionQueryOptions 连接查询选项
type ConnectionQueryOptions struct {
	UserID     string // 用户ID过滤
	NodeID     string // 节点ID过滤
	IsActive   *bool  // 是否活跃（nil表示不过滤）
	IsAbnormal *bool  // 是否异常（nil表示不过滤）
	ClientIP   string // 客户端IP过滤
	Limit      int    // 限制数量
	Offset     int    // 偏移量
	OrderBy    string // 排序字段（默认 connected_at DESC）
}

// ConnectionStats 连接统计信息
// 质量维度字段(TotalMessages*/TotalBytes*/AveragePingMs/AverageReconnectCount)保留兼容调用方
// 拆表后由本方法零填充，跨表补充由调用方按需 JOIN qualityRepo
type ConnectionStats struct {
	TotalConnections      int64   `json:"total_connections"`       // 总连接数
	ActiveConnections     int64   `json:"active_connections"`      // 活跃连接数
	AverageDuration       float64 `json:"average_duration"`        // 平均连接时长(秒)
	TotalMessagesSent     int64   `json:"total_messages_sent"`     // 总发送消息数（拆表后零填充，跨表补充由调用方）
	TotalMessagesReceived int64   `json:"total_messages_received"` // 总接收消息数（拆表后零填充）
	TotalBytesSent        int64   `json:"total_bytes_sent"`        // 总发送字节数（拆表后零填充）
	TotalBytesReceived    int64   `json:"total_bytes_received"`    // 总接收字节数（拆表后零填充）
	AveragePingMs         float64 `json:"average_ping_ms"`         // 平均Ping延迟（拆表后零填充）
	AbnormalRate          float64 `json:"abnormal_rate"`           // 异常断开率
	AverageReconnectCount float64 `json:"average_reconnect_count"` // 平均重连次数（拆表后零填充）
}

// UserConnectionStats 用户连接统计
// 质量维度字段(ReconnectCount/ErrorCount/MessagesSent/MessagesReceived/AveragePingMs/ConnectionQuality)
// 拆表后由本方法零填充，跨表补充由调用方按需从 qualityRepo 取
type UserConnectionStats struct {
	UserID            string     `json:"user_id"`
	IsActive          bool       `json:"is_active"`
	ConnectedAt       time.Time  `json:"connected_at"`
	DisconnectedAt    *time.Time `json:"disconnected_at,omitempty"`
	Duration          int64      `json:"duration"`
	ReconnectCount    int        `json:"reconnect_count"`    // 拆表后零填充
	ErrorCount        int        `json:"error_count"`        // 拆表后零填充
	MessagesSent      int64      `json:"messages_sent"`      // 拆表后零填充
	MessagesReceived  int64      `json:"messages_received"`  // 拆表后零填充
	AveragePingMs     float64    `json:"average_ping_ms"`    // 拆表后零填充
	ConnectionQuality float64    `json:"connection_quality"` // 拆表后零填充
}

// NodeConnectionStats 节点连接统计
// 质量维度字段(TotalMessages*/TotalBytes*/TotalErrors/ErrorRate/AveragePingMs/MaxPingMs/MinPingMs/
// TotalReconnects/AverageReconnectCount/ConnectionQuality)拆表后由本方法零填充，跨表补充由调用方按需从 qualityRepo 取
type NodeConnectionStats struct {
	NodeID                string  `json:"node_id"`                 // 节点ID
	NodeIP                string  `json:"node_ip"`                 // 节点IP
	NodePort              int     `json:"node_port"`               // 节点端口
	TotalConnections      int64   `json:"total_connections"`       // 总连接数
	ActiveConnections     int64   `json:"active_connections"`      // 活跃连接数
	DisconnectedCount     int64   `json:"disconnected_count"`      // 已断开连接数
	AbnormalCount         int64   `json:"abnormal_count"`          // 异常断开数
	AbnormalRate          float64 `json:"abnormal_rate"`           // 异常断开率(%)
	TotalMessagesSent     int64   `json:"total_messages_sent"`     // 拆表后零填充
	TotalMessagesReceived int64   `json:"total_messages_received"` // 拆表后零填充
	TotalBytesSent        int64   `json:"total_bytes_sent"`        // 拆表后零填充
	TotalBytesReceived    int64   `json:"total_bytes_received"`    // 拆表后零填充
	TotalErrors           int64   `json:"total_errors"`            // 拆表后零填充
	ErrorRate             float64 `json:"error_rate"`              // 拆表后零填充
	AveragePingMs         float64 `json:"average_ping_ms"`         // 拆表后零填充
	MaxPingMs             float64 `json:"max_ping_ms"`             // 拆表后零填充
	MinPingMs             float64 `json:"min_ping_ms"`             // 拆表后零填充
	AverageDuration       float64 `json:"average_duration"`        // 平均连接时长(秒)
	TotalReconnects       int64   `json:"total_reconnects"`        // 拆表后零填充
	AverageReconnectCount float64 `json:"average_reconnect_count"` // 拆表后零填充
	ConnectionQuality     float64 `json:"connection_quality"`      // 拆表后零填充
}

// connectionRecordRepositoryImpl WebSocket连接记录仓储实现
type connectionRecordRepositoryImpl struct {
	db         *gorm.DB
	tableName  string // 自定义表名（用于测试隔离）
	logger     logger.ILogger
	cancelFunc context.CancelFunc
}

// NewConnectionRecordRepository 创建连接记录仓储实例
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
func NewConnectionRecordRepository(db *gorm.DB, config *wscconfig.ConnectionRecord, log logger.ILogger) ConnectionRecordRepository {
	ctx, cancel := context.WithCancel(context.Background())

	repo := &connectionRecordRepositoryImpl{
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
func (r *connectionRecordRepositoryImpl) WithTableName(tableName string) ConnectionRecordRepository {
	return &connectionRecordRepositoryImpl{
		db:         r.db,
		tableName:  tableName,
		logger:     r.logger,
		cancelFunc: r.cancelFunc,
	}
}

// getDB 获取数据库会话（如果设置了自定义表名则应用）
func (r *connectionRecordRepositoryImpl) getDB(ctx context.Context) *gorm.DB {
	db := r.db.WithContext(ctx)
	if r.tableName != "" {
		return db.Table(r.tableName)
	}
	return db.Model(&models.ConnectionRecord{})
}

// ========== 核心操作 ==========

// Upsert 创建或更新连接记录（首次连接创建，重连时更新）
func (r *connectionRecordRepositoryImpl) Upsert(ctx context.Context, record *models.ConnectionRecord) error {
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
func (r *connectionRecordRepositoryImpl) updateConnectionRecord(ctx context.Context, record *models.ConnectionRecord) error {
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
func (r *connectionRecordRepositoryImpl) MarkDisconnected(ctx context.Context, connectionID string, reason models.DisconnectReason, code int) error {
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
// 单条失败不影响其他条目（continue 跳过），Ping 统计由 ConnectionQualityRepository 写 quality 表
func (r *connectionRecordRepositoryImpl) BatchUpdateHeartbeats(ctx context.Context, entries []*HeartbeatUpdateEntry) error {
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
func (r *connectionRecordRepositoryImpl) GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionRecord, error) {
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
func (r *connectionRecordRepositoryImpl) GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error) {
	return r.List(ctx, &ConnectionQueryOptions{
		UserID: userID,
	})
}

// GetActiveByUserID 根据用户ID获取所有活跃连接记录
func (r *connectionRecordRepositoryImpl) GetActiveByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error) {
	isActive := true
	return r.List(ctx, &ConnectionQueryOptions{
		UserID:   userID,
		IsActive: &isActive,
	})
}

// ========== 查询操作 ==========

// List 通用列表查询（支持条件过滤）
func (r *connectionRecordRepositoryImpl) List(ctx context.Context, opts *ConnectionQueryOptions) ([]*models.ConnectionRecord, error) {
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
func (r *connectionRecordRepositoryImpl) Count(ctx context.Context, opts *ConnectionQueryOptions) (int64, error) {
	query := r.getDB(ctx)
	query = r.applyQueryOptions(query, opts)

	var count int64
	err := query.Count(&count).Error
	return count, err
}

// applyQueryOptions 应用查询条件
func (r *connectionRecordRepositoryImpl) applyQueryOptions(query *gorm.DB, opts *ConnectionQueryOptions) *gorm.DB {
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
func (r *connectionRecordRepositoryImpl) GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*ConnectionStats, error) {
	stats := &ConnectionStats{}

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
func (r *connectionRecordRepositoryImpl) GetConnectionStatsByID(ctx context.Context, connectionID string) (*UserConnectionStats, error) {
	record, err := r.GetByConnectionID(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("获取连接记录失败: %w", err)
	}

	return &UserConnectionStats{
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
func (r *connectionRecordRepositoryImpl) GetUserConnectionStats(ctx context.Context, userID string) (*UserConnectionStats, error) {
	records, err := r.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("获取用户连接记录失败: %w", err)
	}
	if len(records) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// 汇总 connect 表维度统计
	stats := &UserConnectionStats{
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
func (r *connectionRecordRepositoryImpl) GetNodeConnectionStats(ctx context.Context, nodeID string) (*NodeConnectionStats, error) {
	stats := &NodeConnectionStats{}

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
func (r *connectionRecordRepositoryImpl) BatchUpsert(ctx context.Context, records []*models.ConnectionRecord) error {
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
func (r *connectionRecordRepositoryImpl) CleanupInactiveRecords(ctx context.Context, before time.Time) (int64, error) {
	result := r.getDB(ctx).
		Where("disconnected_at < ? AND is_active = ?", before, false).
		Delete(&models.ConnectionRecord{})

	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
}

// startCleanupScheduler 启动定时清理任务（使用 EventLoop，每天执行一次）
func (r *connectionRecordRepositoryImpl) startCleanupScheduler(ctx context.Context, daysAgo int) {
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
func (r *connectionRecordRepositoryImpl) cleanupOldData(ctx context.Context, daysAgo int) {
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
func (r *connectionRecordRepositoryImpl) Close() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
		r.logger.Info("🛑 ConnectionRecordRepository 已关闭")
	}
	return nil
}

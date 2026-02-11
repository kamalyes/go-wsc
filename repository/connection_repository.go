/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:00:16
 * @FilePath: \go-wsc\repository\connection_repository.go
 * @Description: WebSocket连接记录仓库接口
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"fmt"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	sqlbuilder "github.com/kamalyes/go-sqlbuilder/repository"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"gorm.io/gorm"
)

// ConnectionRecordRepository WebSocket连接记录仓储接口
// 设计原则：支持多设备登录，每个连接维护独立记录
type ConnectionRecordRepository interface {
	// ========== 核心操作 ==========

	// Upsert 创建或更新连接记录（首次连接创建，重连时更新）
	Upsert(ctx context.Context, record *models.ConnectionRecord) error

	// MarkDisconnected 标记连接为已断开
	MarkDisconnected(ctx context.Context, connectionID string, reason models.DisconnectReason, code int, message string) error

	// GetByConnectionID 根据连接ID获取连接记录
	GetByConnectionID(ctx context.Context, connectionID string) (*models.ConnectionRecord, error)

	// GetByUserID 根据用户ID获取所有连接记录（支持多设备）
	GetByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error)

	// GetActiveByUserID 根据用户ID获取所有活跃连接记录
	GetActiveByUserID(ctx context.Context, userID string) ([]*models.ConnectionRecord, error)

	// ========== 统计更新操作 ==========

	// IncrementMessageStats 增加消息统计
	IncrementMessageStats(ctx context.Context, connectionID string, sent, received int64) error

	// IncrementBytesStats 增加字节统计
	IncrementBytesStats(ctx context.Context, connectionID string, sent, received int64) error

	// UpdatePingStats 更新Ping延迟统计
	UpdatePingStats(ctx context.Context, connectionID string, pingMs float64) error

	// AddError 记录错误
	AddError(ctx context.Context, connectionID string, err error) error

	// UpdateHeartbeat 更新心跳时间
	UpdateHeartbeat(ctx context.Context, connectionID string, pingTime, pongTime *time.Time) error

	// ========== 查询操作 ==========

	// List 通用列表查询（支持条件过滤）
	List(ctx context.Context, opts *ConnectionQueryOptions) ([]*models.ConnectionRecord, error)

	// Count 统计连接数（支持条件过滤）
	Count(ctx context.Context, opts *ConnectionQueryOptions) (int64, error)

	// ========== 统计分析操作 ==========

	// GetConnectionStats 获取连接统计信息
	GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*ConnectionStats, error)

	// GetConnectionStatsByID 根据连接ID获取单个连接的统计信息
	GetConnectionStatsByID(ctx context.Context, connectionID string) (*UserConnectionStats, error)

	// GetUserConnectionStats 获取用户所有连接的汇总统计
	GetUserConnectionStats(ctx context.Context, userID string) (*UserConnectionStats, error)

	// GetNodeConnectionStats 获取节点连接统计
	GetNodeConnectionStats(ctx context.Context, nodeID string) (*NodeConnectionStats, error)

	// ========== 异常检测操作 ==========

	// GetHighErrorRateConnections 获取高错误率连接
	GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*models.ConnectionRecord, error)

	// GetFrequentReconnectConnections 获取频繁重连的连接
	GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold int, limit int) ([]*models.ConnectionRecord, error)

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

// ========== 统计结构体定义 ==========

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
type ConnectionStats struct {
	TotalConnections      int64   `json:"total_connections"`       // 总连接数
	ActiveConnections     int64   `json:"active_connections"`      // 活跃连接数
	AverageDuration       float64 `json:"average_duration"`        // 平均连接时长(秒)
	TotalMessagesSent     int64   `json:"total_messages_sent"`     // 总发送消息数
	TotalMessagesReceived int64   `json:"total_messages_received"` // 总接收消息数
	TotalBytesSent        int64   `json:"total_bytes_sent"`        // 总发送字节数
	TotalBytesReceived    int64   `json:"total_bytes_received"`    // 总接收字节数
	AveragePingMs         float64 `json:"average_ping_ms"`         // 平均Ping延迟
	AbnormalRate          float64 `json:"abnormal_rate"`           // 异常断开率
	AverageReconnectCount float64 `json:"average_reconnect_count"` // 平均重连次数
}

// UserConnectionStats 用户连接统计
type UserConnectionStats struct {
	UserID            string     `json:"user_id"`
	IsActive          bool       `json:"is_active"`
	ConnectedAt       time.Time  `json:"connected_at"`
	DisconnectedAt    *time.Time `json:"disconnected_at,omitempty"`
	Duration          int64      `json:"duration"`
	ReconnectCount    int        `json:"reconnect_count"`
	ErrorCount        int        `json:"error_count"`
	MessagesSent      int64      `json:"messages_sent"`
	MessagesReceived  int64      `json:"messages_received"`
	AveragePingMs     float64    `json:"average_ping_ms"`
	ConnectionQuality float64    `json:"connection_quality"`
}

// NodeConnectionStats 节点连接统计
type NodeConnectionStats struct {
	NodeID                string  `json:"node_id"`                 // 节点ID
	NodeIP                string  `json:"node_ip"`                 // 节点IP
	NodePort              int     `json:"node_port"`               // 节点端口
	TotalConnections      int64   `json:"total_connections"`       // 总连接数
	ActiveConnections     int64   `json:"active_connections"`      // 活跃连接数
	DisconnectedCount     int64   `json:"disconnected_count"`      // 已断开连接数
	AbnormalCount         int64   `json:"abnormal_count"`          // 异常断开数
	AbnormalRate          float64 `json:"abnormal_rate"`           // 异常断开率(%)
	TotalMessagesSent     int64   `json:"total_messages_sent"`     // 总发送消息数
	TotalMessagesReceived int64   `json:"total_messages_received"` // 总接收消息数
	TotalBytesSent        int64   `json:"total_bytes_sent"`        // 总发送字节数
	TotalBytesReceived    int64   `json:"total_bytes_received"`    // 总接收字节数
	TotalErrors           int64   `json:"total_errors"`            // 总错误数
	ErrorRate             float64 `json:"error_rate"`              // 错误率(%)
	AveragePingMs         float64 `json:"average_ping_ms"`         // 平均Ping延迟
	MaxPingMs             float64 `json:"max_ping_ms"`             // 最大Ping延迟
	MinPingMs             float64 `json:"min_ping_ms"`             // 最小Ping延迟
	AverageDuration       float64 `json:"average_duration"`        // 平均连接时长(秒)
	TotalReconnects       int64   `json:"total_reconnects"`        // 总重连次数
	AverageReconnectCount float64 `json:"average_reconnect_count"` // 平均重连次数
	ConnectionQuality     float64 `json:"connection_quality"`      // 连接质量评分(0-100)
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
func (r *connectionRecordRepositoryImpl) updateConnectionRecord(ctx context.Context, record *models.ConnectionRecord) error {
	now := time.Now()
	updates := map[string]any{
		"node_id":            record.NodeID,
		"node_ip":            record.NodeIP,
		"node_port":          record.NodePort,
		"client_ip":          record.ClientIP,
		"client_type":        record.ClientType,
		"protocol":           record.Protocol,
		"connected_at":       now,
		"disconnected_at":    nil,
		"duration":           0,
		"is_active":          true,
		"is_abnormal":        false,
		"is_forced_offline":  false,
		"reconnect_count":    gorm.Expr("reconnect_count + ?", 1),
		"metadata":           record.Metadata,
		"last_error":         "",
		"last_error_at":      nil,
		"disconnect_reason":  "",
		"disconnect_code":    0,
		"disconnect_message": "",
	}

	return r.getDB(ctx).
		Where("connection_id = ?", record.ConnectionID).
		Updates(updates).Error
}

// MarkDisconnected 标记连接为已断开
func (r *connectionRecordRepositoryImpl) MarkDisconnected(ctx context.Context, connectionID string, reason models.DisconnectReason, code int, message string) error {
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
		"disconnected_at":    now,
		"disconnect_reason":  string(reason),
		"disconnect_code":    code,
		"disconnect_message": message,
		"duration":           duration,
		"is_active":          false,
		"is_abnormal":        isAbnormal,
	}

	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(updates).Error
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

// ========== 统计更新操作 ==========

// IncrementMessageStats 增加消息统计
func (r *connectionRecordRepositoryImpl) IncrementMessageStats(ctx context.Context, connectionID string, sent, received int64) error {
	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(map[string]any{
			"messages_sent":     gorm.Expr("messages_sent + ?", sent),
			"messages_received": gorm.Expr("messages_received + ?", received),
		}).Error
}

// IncrementBytesStats 增加字节统计
func (r *connectionRecordRepositoryImpl) IncrementBytesStats(ctx context.Context, connectionID string, sent, received int64) error {
	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(map[string]any{
			"bytes_sent":     gorm.Expr("bytes_sent + ?", sent),
			"bytes_received": gorm.Expr("bytes_received + ?", received),
		}).Error
}

// UpdatePingStats 更新Ping延迟统计
func (r *connectionRecordRepositoryImpl) UpdatePingStats(ctx context.Context, connectionID string, pingMs float64) error {
	record, err := r.GetByConnectionID(ctx, connectionID)
	if err != nil {
		// 连接记录不存在（可能已断开），直接返回nil而不是错误
		// 这是正常情况，不需要记录为错误
		return nil
	}

	// 计算新的平均值
	newAverage := pingMs
	if record.AveragePingMs > 0 {
		newAverage = (record.AveragePingMs + pingMs) / 2
	}

	// 更新最大最小值
	newMax := record.MaxPingMs
	if newMax == 0 || pingMs > newMax {
		newMax = pingMs
	}

	newMin := record.MinPingMs
	if newMin == 0 || pingMs < newMin {
		newMin = pingMs
	}

	updates := map[string]any{
		"average_ping_ms": newAverage,
		"max_ping_ms":     newMax,
		"min_ping_ms":     newMin,
	}

	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(updates).Error
}

// AddError 记录错误
func (r *connectionRecordRepositoryImpl) AddError(ctx context.Context, connectionID string, err error) error {
	if err == nil {
		return nil
	}

	now := time.Now()
	updates := map[string]any{
		"error_count":   gorm.Expr("error_count + ?", 1),
		"last_error":    err.Error(),
		"last_error_at": now,
	}

	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(updates).Error
}

// UpdateHeartbeat 更新心跳时间
func (r *connectionRecordRepositoryImpl) UpdateHeartbeat(ctx context.Context, connectionID string, pingTime, pongTime *time.Time) error {
	updates := make(map[string]any)

	if pingTime != nil {
		updates["last_ping_at"] = pingTime
	}
	if pongTime != nil {
		updates["last_pong_at"] = pongTime
	}

	if len(updates) == 0 {
		return nil
	}

	return r.getDB(ctx).
		Where("connection_id = ?", connectionID).
		Updates(updates).Error
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
func (r *connectionRecordRepositoryImpl) GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*ConnectionStats, error) {
	stats := &ConnectionStats{}

	err := r.getDB(ctx).
		Where("connected_at BETWEEN ? AND ?", startTime, endTime).
		Select(`
			COUNT(*) as total_connections,
			SUM(CASE WHEN is_active = true THEN 1 ELSE 0 END) as active_connections,
			AVG(CASE WHEN duration > 0 THEN duration ELSE NULL END) as average_duration,
			SUM(messages_sent) as total_messages_sent,
			SUM(messages_received) as total_messages_received,
			SUM(bytes_sent) as total_bytes_sent,
			SUM(bytes_received) as total_bytes_received,
			AVG(CASE WHEN average_ping_ms > 0 THEN average_ping_ms ELSE NULL END) as average_ping_ms,
			AVG(reconnect_count) as average_reconnect_count
		`).
		Scan(stats).Error

	if err != nil {
		return nil, err
	}

	// 计算异常率
	if stats.TotalConnections > 0 {
		var abnormalCount int64
		r.getDB(ctx).
			Where("connected_at BETWEEN ? AND ? AND is_abnormal = ?", startTime, endTime, true).
			Count(&abnormalCount)
		stats.AbnormalRate = float64(abnormalCount) / float64(stats.TotalConnections) * 100
	}

	return stats, nil
}

// GetConnectionStatsByID 根据连接ID获取单个连接的统计信息
func (r *connectionRecordRepositoryImpl) GetConnectionStatsByID(ctx context.Context, connectionID string) (*UserConnectionStats, error) {
	record, err := r.GetByConnectionID(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("获取连接记录失败: %w", err)
	}

	return &UserConnectionStats{
		UserID:            record.UserID,
		IsActive:          record.IsActive,
		ConnectedAt:       record.ConnectedAt,
		DisconnectedAt:    record.DisconnectedAt,
		Duration:          record.Duration,
		ReconnectCount:    record.ReconnectCount,
		ErrorCount:        record.ErrorCount,
		MessagesSent:      record.MessagesSent,
		MessagesReceived:  record.MessagesReceived,
		AveragePingMs:     record.AveragePingMs,
		ConnectionQuality: record.GetConnectionQuality(),
	}, nil
}

// GetUserConnectionStats 获取用户所有连接的汇总统计
func (r *connectionRecordRepositoryImpl) GetUserConnectionStats(ctx context.Context, userID string) (*UserConnectionStats, error) {
	records, err := r.GetByUserID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("获取用户连接记录失败: %w", err)
	}
	if len(records) == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	// 汇总统计
	stats := &UserConnectionStats{
		UserID: userID,
	}

	var totalPing float64
	var pingCount int
	var activeCount int

	for _, record := range records {
		if record.IsActive {
			activeCount++
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
		stats.ReconnectCount += record.ReconnectCount
		stats.ErrorCount += record.ErrorCount
		stats.MessagesSent += record.MessagesSent
		stats.MessagesReceived += record.MessagesReceived

		if record.AveragePingMs > 0 {
			totalPing += record.AveragePingMs
			pingCount++
		}
	}

	// 计算平均 Ping
	if pingCount > 0 {
		stats.AveragePingMs = totalPing / float64(pingCount)
	}

	// 计算连接质量（基于汇总数据）
	stats.ConnectionQuality = r.calculateAggregatedQuality(stats, activeCount, len(records))

	return stats, nil
}

// calculateAggregatedQuality 计算汇总连接质量评分
func (r *connectionRecordRepositoryImpl) calculateAggregatedQuality(stats *UserConnectionStats, activeCount, totalCount int) float64 {
	score := 100.0

	// 活跃连接占比影响（最多扣20分）
	if totalCount > 0 {
		activeRate := float64(activeCount) / float64(totalCount)
		if activeRate < 0.5 {
			score -= 20
		} else if activeRate < 0.8 {
			score -= 10
		}
	}

	// 延迟影响（最多扣30分）
	if stats.AveragePingMs > 0 {
		if stats.AveragePingMs > 500 {
			score -= 30
		} else if stats.AveragePingMs > 200 {
			score -= 20
		} else if stats.AveragePingMs > 100 {
			score -= 10
		}
	}

	// 错误率影响（最多扣30分）
	totalMessages := stats.MessagesSent + stats.MessagesReceived
	if totalMessages > 0 {
		errorRate := float64(stats.ErrorCount) / float64(totalMessages)
		score -= errorRate * 30
	}

	// 重连次数影响（最多扣20分）
	if stats.ReconnectCount > 0 {
		score -= float64(stats.ReconnectCount) * 2
		if score < 0 {
			score = 0
		}
	}

	if score < 0 {
		score = 0
	}
	return score
}

// GetNodeConnectionStats 获取节点连接统计
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
			SUM(messages_sent) as total_messages_sent,
			SUM(messages_received) as total_messages_received,
			SUM(bytes_sent) as total_bytes_sent,
			SUM(bytes_received) as total_bytes_received,
			SUM(error_count) as total_errors,
			SUM(reconnect_count) as total_reconnects,
			AVG(CASE WHEN duration > 0 THEN duration ELSE NULL END) as average_duration,
			AVG(CASE WHEN average_ping_ms > 0 THEN average_ping_ms ELSE NULL END) as average_ping_ms,
			MAX(CASE WHEN max_ping_ms > 0 THEN max_ping_ms ELSE NULL END) as max_ping_ms,
			MIN(CASE WHEN min_ping_ms > 0 THEN min_ping_ms ELSE NULL END) as min_ping_ms,
			AVG(reconnect_count) as average_reconnect_count
		`, nodeID).
		Scan(stats).Error

	if err != nil {
		return nil, fmt.Errorf("查询节点统计失败: %w", err)
	}

	// 计算异常率
	if stats.TotalConnections > 0 {
		stats.AbnormalRate = float64(stats.AbnormalCount) / float64(stats.TotalConnections) * 100
	}

	// 计算错误率
	totalMessages := stats.TotalMessagesSent + stats.TotalMessagesReceived
	if totalMessages > 0 {
		stats.ErrorRate = float64(stats.TotalErrors) / float64(totalMessages) * 100
	}

	// 计算连接质量评分
	stats.ConnectionQuality = r.calculateNodeQuality(stats)

	return stats, nil
}

// calculateNodeQuality 计算节点连接质量评分
func (r *connectionRecordRepositoryImpl) calculateNodeQuality(stats *NodeConnectionStats) float64 {
	score := 100.0

	// 异常率影响（最多扣30分）
	if stats.AbnormalRate > 0 {
		if stats.AbnormalRate > 20 {
			score -= 30
		} else if stats.AbnormalRate > 10 {
			score -= 20
		} else if stats.AbnormalRate > 5 {
			score -= 10
		}
	}

	// 延迟影响（最多扣30分）
	if stats.AveragePingMs > 0 {
		if stats.AveragePingMs > 500 {
			score -= 30
		} else if stats.AveragePingMs > 200 {
			score -= 20
		} else if stats.AveragePingMs > 100 {
			score -= 10
		}
	}

	// 错误率影响（最多扣25分）
	if stats.ErrorRate > 0 {
		if stats.ErrorRate > 5 {
			score -= 25
		} else if stats.ErrorRate > 2 {
			score -= 15
		} else if stats.ErrorRate > 1 {
			score -= 8
		}
	}

	// 重连次数影响（最多扣15分）
	if stats.AverageReconnectCount > 0 {
		if stats.AverageReconnectCount > 10 {
			score -= 15
		} else if stats.AverageReconnectCount > 5 {
			score -= 10
		} else if stats.AverageReconnectCount > 2 {
			score -= 5
		}
	}

	if score < 0 {
		score = 0
	}
	return score
}

// ========== 异常检测操作 ==========

// GetHighErrorRateConnections 获取高错误率连接
func (r *connectionRecordRepositoryImpl) GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*models.ConnectionRecord, error) {
	var records []*models.ConnectionRecord

	// 使用 go-sqlbuilder 构建查询
	query := sqlbuilder.NewQuery().
		AddFilter(sqlbuilder.NewGteFilter("error_count", errorThreshold)).
		AddOrder("error_count", "DESC")

	if limit > 0 {
		query.Limit(limit)
	}

	// 应用到 GORM
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
func (r *connectionRecordRepositoryImpl) GetFrequentReconnectConnections(ctx context.Context, reconnectThreshold int, limit int) ([]*models.ConnectionRecord, error) {
	var records []*models.ConnectionRecord

	// 使用 go-sqlbuilder 构建查询
	query := sqlbuilder.NewQuery().
		AddFilter(sqlbuilder.NewGteFilter("reconnect_count", reconnectThreshold)).
		AddOrder("reconnect_count", "DESC")

	if limit > 0 {
		query.Limit(limit)
	}

	// 应用到 GORM
	gormDB := r.getDB(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)
	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
	return records, err
}

// ========== 批量操作 ==========

// BatchUpsert 批量创建或更新连接记录
func (r *connectionRecordRepositoryImpl) BatchUpsert(ctx context.Context, records []*models.ConnectionRecord) error {
	if len(records) == 0 {
		return nil
	}

	for _, record := range records {
		if err := r.Upsert(ctx, record); err != nil {
			return fmt.Errorf("批量更新失败 user_id=%s: %w", record.UserID, err)
		}
	}

	return nil
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
			r.logger.Errorf("⚠️ 连接记录清理任务 panic: %v", rec)
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

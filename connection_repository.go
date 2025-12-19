/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 16:12:50
 * @FilePath: \go-wsc\connection_repository.go
 * @Description: WebSocket连接记录仓储接口
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// ConnectionRecordRepository WebSocket连接记录仓储接口
// 用于持久化连接历史记录，支持审计、统计分析等功能
type ConnectionRecordRepository interface {
	// ========== 基础CRUD操作 ==========

	// Create 创建连接记录
	Create(ctx context.Context, record *ConnectionRecord) error

	// Update 更新连接记录
	Update(ctx context.Context, record *ConnectionRecord) error

	// UpdateByConnectionID 根据连接ID更新记录
	UpdateByConnectionID(ctx context.Context, connectionID string, updates map[string]interface{}) error

	// GetByConnectionID 根据连接ID获取记录
	GetByConnectionID(ctx context.Context, connectionID string) (*ConnectionRecord, error)

	// GetByID 根据主键ID获取记录
	GetByID(ctx context.Context, id uint64) (*ConnectionRecord, error)

	// Delete 删除连接记录
	Delete(ctx context.Context, id uint64) error

	// ========== 断开连接操作 ==========

	// MarkDisconnected 标记连接为已断开
	MarkDisconnected(ctx context.Context, connectionID string, reason DisconnectReason, code int, message string) error

	// MarkForcedOffline 标记为强制下线
	MarkForcedOffline(ctx context.Context, connectionID string, reason string) error

	// ========== 统计更新操作 ==========

	// IncrementMessageStats 增加消息统计
	IncrementMessageStats(ctx context.Context, connectionID string, sent, received int64) error

	// IncrementBytesStats 增加字节统计
	IncrementBytesStats(ctx context.Context, connectionID string, sent, received int64) error

	// UpdatePingStats 更新Ping延迟统计
	UpdatePingStats(ctx context.Context, connectionID string, pingMs float64) error

	// IncrementReconnect 增加重连次数
	IncrementReconnect(ctx context.Context, connectionID string) error

	// AddError 记录错误
	AddError(ctx context.Context, connectionID string, err error) error

	// UpdateHeartbeat 更新心跳时间
	UpdateHeartbeat(ctx context.Context, connectionID string, pingTime, pongTime *time.Time) error

	// ========== 查询操作 ==========

	// GetActiveByUserID 获取用户的活跃连接记录
	GetActiveByUserID(ctx context.Context, userID string) ([]*ConnectionRecord, error)

	// GetByUserID 获取用户的所有连接记录
	GetByUserID(ctx context.Context, userID string, limit int, offset int) ([]*ConnectionRecord, error)

	// GetActiveByNodeID 获取节点的活跃连接记录
	GetActiveByNodeID(ctx context.Context, nodeID string) ([]*ConnectionRecord, error)

	// GetByNodeID 获取节点的所有连接记录
	GetByNodeID(ctx context.Context, nodeID string, limit int, offset int) ([]*ConnectionRecord, error)

	// ListActive 获取所有活跃连接
	ListActive(ctx context.Context, limit int, offset int) ([]*ConnectionRecord, error)

	// ListByTimeRange 获取时间范围内的连接记录
	ListByTimeRange(ctx context.Context, startTime, endTime time.Time, limit int, offset int) ([]*ConnectionRecord, error)

	// ========== 统计分析操作 ==========

	// CountActiveConnections 统计活跃连接数
	CountActiveConnections(ctx context.Context) (int64, error)

	// CountByUserID 统计用户的连接次数
	CountByUserID(ctx context.Context, userID string) (int64, error)

	// CountByNodeID 统计节点的连接次数
	CountByNodeID(ctx context.Context, nodeID string) (int64, error)

	// CountByTimeRange 统计时间范围内的连接次数
	CountByTimeRange(ctx context.Context, startTime, endTime time.Time) (int64, error)

	// GetAverageConnectionDuration 获取平均连接时长
	GetAverageConnectionDuration(ctx context.Context, startTime, endTime time.Time) (float64, error)

	// GetConnectionStats 获取连接统计信息
	GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*ConnectionStats, error)

	// GetUserConnectionStats 获取用户连接统计
	GetUserConnectionStats(ctx context.Context, userID string, startTime, endTime time.Time) (*UserConnectionStats, error)

	// GetNodeConnectionStats 获取节点连接统计
	GetNodeConnectionStats(ctx context.Context, nodeID string, startTime, endTime time.Time) (*NodeConnectionStats, error)

	// ========== 异常检测操作 ==========

	// GetAbnormalConnections 获取异常连接记录
	GetAbnormalConnections(ctx context.Context, limit int, offset int) ([]*ConnectionRecord, error)

	// GetHighErrorRateConnections 获取高错误率连接
	GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*ConnectionRecord, error)

	// GetFrequentReconnectUsers 获取频繁重连的用户
	GetFrequentReconnectUsers(ctx context.Context, reconnectThreshold int, timeRange time.Duration) ([]UserReconnectStats, error)

	// ========== 批量操作 ==========

	// BatchCreate 批量创建连接记录
	BatchCreate(ctx context.Context, records []*ConnectionRecord) error

	// BatchUpdateActive 批量更新活跃状态
	BatchUpdateActive(ctx context.Context, connectionIDs []string, isActive bool) error

	// BatchDelete 批量删除记录
	BatchDelete(ctx context.Context, ids []uint64) error

	// ========== 清理操作 ==========

	// CleanupOldRecords 清理旧记录
	CleanupOldRecords(ctx context.Context, before time.Time) (int64, error)

	// ArchiveOldRecords 归档旧记录
	// processor: 处理函数，接收读取到的记录进行自定义处理（如写入归档表、导出文件等）
	// 返回处理的记录数和错误
	ArchiveOldRecords(ctx context.Context, before time.Time, processor func([]*ConnectionRecord) error) (int64, error)
}

// ========== 统计结构体定义 ==========

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
	ReconnectRate         float64 `json:"reconnect_rate"`          // 重连率
}

// UserConnectionStats 用户连接统计
type UserConnectionStats struct {
	UserID                   string    `json:"user_id"`
	TotalConnections         int64     `json:"total_connections"`
	ActiveConnections        int64     `json:"active_connections"`
	AverageDuration          float64   `json:"average_duration"`
	LastConnectedAt          time.Time `json:"last_connected_at"`
	TotalReconnects          int64     `json:"total_reconnects"`
	TotalErrors              int64     `json:"total_errors"`
	AverageConnectionQuality float64   `json:"average_connection_quality"`
}

// NodeConnectionStats 节点连接统计
type NodeConnectionStats struct {
	NodeID                string  `json:"node_id"`
	TotalConnections      int64   `json:"total_connections"`
	ActiveConnections     int64   `json:"active_connections"`
	AverageDuration       float64 `json:"average_duration"`
	TotalMessagesSent     int64   `json:"total_messages_sent"`
	TotalMessagesReceived int64   `json:"total_messages_received"`
	AveragePingMs         float64 `json:"average_ping_ms"`
	ErrorRate             float64 `json:"error_rate"`
}

// UserReconnectStats 用户重连统计
type UserReconnectStats struct {
	UserID          string    `json:"user_id"`
	ReconnectCount  int64     `json:"reconnect_count"`
	LastReconnectAt time.Time `json:"last_reconnect_at"`
}

// connectionRecordRepositoryImpl WebSocket连接记录仓储实现
type connectionRecordRepositoryImpl struct {
	db *gorm.DB
}

// NewConnectionRecordRepository 创建连接记录仓储实例
func NewConnectionRecordRepository(db *gorm.DB) ConnectionRecordRepository {
	return &connectionRecordRepositoryImpl{
		db: db,
	}
}

// ========== 基础CRUD操作 ==========

// Create 创建连接记录
func (r *connectionRecordRepositoryImpl) Create(ctx context.Context, record *ConnectionRecord) error {
	return r.db.WithContext(ctx).Create(record).Error
}

// Update 更新连接记录
func (r *connectionRecordRepositoryImpl) Update(ctx context.Context, record *ConnectionRecord) error {
	return r.db.WithContext(ctx).Save(record).Error
}

// UpdateByConnectionID 根据连接ID更新记录
func (r *connectionRecordRepositoryImpl) UpdateByConnectionID(ctx context.Context, connectionID string, updates map[string]interface{}) error {
	return r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connection_id = ?", connectionID).
		Updates(updates).Error
}

// GetByConnectionID 根据连接ID获取记录
func (r *connectionRecordRepositoryImpl) GetByConnectionID(ctx context.Context, connectionID string) (*ConnectionRecord, error) {
	var record ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("connection_id = ?", connectionID).
		First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// GetByID 根据主键ID获取记录
func (r *connectionRecordRepositoryImpl) GetByID(ctx context.Context, id uint64) (*ConnectionRecord, error) {
	var record ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("id = ?", id).
		First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// Delete 删除连接记录
func (r *connectionRecordRepositoryImpl) Delete(ctx context.Context, id uint64) error {
	return r.db.WithContext(ctx).
		Delete(&ConnectionRecord{}, id).Error
}

// ========== 断开连接操作 ==========

// MarkDisconnected 标记连接为已断开
func (r *connectionRecordRepositoryImpl) MarkDisconnected(ctx context.Context, connectionID string, reason DisconnectReason, code int, message string) error {
	now := time.Now()

	// 获取连接记录以计算持续时长
	record, err := r.GetByConnectionID(ctx, connectionID)
	if err != nil {
		return err
	}

	duration := int64(now.Sub(record.ConnectedAt).Seconds())
	isAbnormal := reason != DisconnectReasonClientRequest && reason != DisconnectReasonServerShutdown

	updates := map[string]interface{}{
		"disconnected_at":    now,
		"disconnect_reason":  string(reason),
		"disconnect_code":    code,
		"disconnect_message": message,
		"duration":           duration,
		"is_active":          false,
		"is_abnormal":        isAbnormal,
	}

	return r.UpdateByConnectionID(ctx, connectionID, updates)
}

// MarkForcedOffline 标记为强制下线
func (r *connectionRecordRepositoryImpl) MarkForcedOffline(ctx context.Context, connectionID string, reason string) error {
	return r.MarkDisconnected(ctx, connectionID, DisconnectReasonForceOffline, 4000, reason)
}

// ========== 统计更新操作 ==========

// IncrementMessageStats 增加消息统计
func (r *connectionRecordRepositoryImpl) IncrementMessageStats(ctx context.Context, connectionID string, sent, received int64) error {
	return r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connection_id = ?", connectionID).
		Updates(map[string]interface{}{
			"messages_sent":     gorm.Expr("messages_sent + ?", sent),
			"messages_received": gorm.Expr("messages_received + ?", received),
		}).Error
}

// IncrementBytesStats 增加字节统计
func (r *connectionRecordRepositoryImpl) IncrementBytesStats(ctx context.Context, connectionID string, sent, received int64) error {
	return r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connection_id = ?", connectionID).
		Updates(map[string]interface{}{
			"bytes_sent":     gorm.Expr("bytes_sent + ?", sent),
			"bytes_received": gorm.Expr("bytes_received + ?", received),
		}).Error
}

// UpdatePingStats 更新Ping延迟统计
func (r *connectionRecordRepositoryImpl) UpdatePingStats(ctx context.Context, connectionID string, pingMs float64) error {
	record, err := r.GetByConnectionID(ctx, connectionID)
	if err != nil {
		return err
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

	updates := map[string]interface{}{
		"average_ping_ms": newAverage,
		"max_ping_ms":     newMax,
		"min_ping_ms":     newMin,
	}

	return r.UpdateByConnectionID(ctx, connectionID, updates)
}

// IncrementReconnect 增加重连次数
func (r *connectionRecordRepositoryImpl) IncrementReconnect(ctx context.Context, connectionID string) error {
	return r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connection_id = ?", connectionID).
		Update("reconnect_count", gorm.Expr("reconnect_count + ?", 1)).Error
}

// AddError 记录错误
func (r *connectionRecordRepositoryImpl) AddError(ctx context.Context, connectionID string, err error) error {
	if err == nil {
		return nil
	}

	now := time.Now()
	updates := map[string]interface{}{
		"error_count":   gorm.Expr("error_count + ?", 1),
		"last_error":    err.Error(),
		"last_error_at": now,
	}

	return r.UpdateByConnectionID(ctx, connectionID, updates)
}

// UpdateHeartbeat 更新心跳时间
func (r *connectionRecordRepositoryImpl) UpdateHeartbeat(ctx context.Context, connectionID string, pingTime, pongTime *time.Time) error {
	updates := make(map[string]interface{})

	if pingTime != nil {
		updates["last_ping_at"] = pingTime
	}
	if pongTime != nil {
		updates["last_pong_at"] = pongTime
	}

	if len(updates) == 0 {
		return nil
	}

	return r.UpdateByConnectionID(ctx, connectionID, updates)
}

// ========== 查询操作 ==========

// GetActiveByUserID 获取用户的活跃连接记录
func (r *connectionRecordRepositoryImpl) GetActiveByUserID(ctx context.Context, userID string) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND is_active = ?", userID, true).
		Order("connected_at DESC").
		Find(&records).Error
	return records, err
}

// GetByUserID 获取用户的所有连接记录
func (r *connectionRecordRepositoryImpl) GetByUserID(ctx context.Context, userID string, limit int, offset int) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("connected_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error
	return records, err
}

// GetActiveByNodeID 获取节点的活跃连接记录
func (r *connectionRecordRepositoryImpl) GetActiveByNodeID(ctx context.Context, nodeID string) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("node_id = ? AND is_active = ?", nodeID, true).
		Order("connected_at DESC").
		Find(&records).Error
	return records, err
}

// GetByNodeID 获取节点的所有连接记录
func (r *connectionRecordRepositoryImpl) GetByNodeID(ctx context.Context, nodeID string, limit int, offset int) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("node_id = ?", nodeID).
		Order("connected_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error
	return records, err
}

// ListActive 获取所有活跃连接
func (r *connectionRecordRepositoryImpl) ListActive(ctx context.Context, limit int, offset int) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("is_active = ?", true).
		Order("connected_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error
	return records, err
}

// ListByTimeRange 获取时间范围内的连接记录
func (r *connectionRecordRepositoryImpl) ListByTimeRange(ctx context.Context, startTime, endTime time.Time, limit int, offset int) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("connected_at BETWEEN ? AND ?", startTime, endTime).
		Order("connected_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error
	return records, err
}

// ========== 统计分析操作 ==========

// CountActiveConnections 统计活跃连接数
func (r *connectionRecordRepositoryImpl) CountActiveConnections(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("is_active = ?", true).
		Count(&count).Error
	return count, err
}

// CountByUserID 统计用户的连接次数
func (r *connectionRecordRepositoryImpl) CountByUserID(ctx context.Context, userID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("user_id = ?", userID).
		Count(&count).Error
	return count, err
}

// CountByNodeID 统计节点的连接次数
func (r *connectionRecordRepositoryImpl) CountByNodeID(ctx context.Context, nodeID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("node_id = ?", nodeID).
		Count(&count).Error
	return count, err
}

// CountByTimeRange 统计时间范围内的连接次数
func (r *connectionRecordRepositoryImpl) CountByTimeRange(ctx context.Context, startTime, endTime time.Time) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connected_at BETWEEN ? AND ?", startTime, endTime).
		Count(&count).Error
	return count, err
}

// GetAverageConnectionDuration 获取平均连接时长
func (r *connectionRecordRepositoryImpl) GetAverageConnectionDuration(ctx context.Context, startTime, endTime time.Time) (float64, error) {
	var avgDuration float64
	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connected_at BETWEEN ? AND ? AND duration > 0", startTime, endTime).
		Select("AVG(duration)").
		Scan(&avgDuration).Error
	return avgDuration, err
}

// GetConnectionStats 获取连接统计信息
func (r *connectionRecordRepositoryImpl) GetConnectionStats(ctx context.Context, startTime, endTime time.Time) (*ConnectionStats, error) {
	stats := &ConnectionStats{}

	// 基础统计查询
	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connected_at BETWEEN ? AND ?", startTime, endTime).
		Select(`
			COUNT(*) as total_connections,
			SUM(CASE WHEN is_active = true THEN 1 ELSE 0 END) as active_connections,
			AVG(CASE WHEN duration > 0 THEN duration ELSE NULL END) as average_duration,
			SUM(messages_sent) as total_messages_sent,
			SUM(messages_received) as total_messages_received,
			SUM(bytes_sent) as total_bytes_sent,
			SUM(bytes_received) as total_bytes_received,
			AVG(CASE WHEN average_ping_ms > 0 THEN average_ping_ms ELSE NULL END) as average_ping_ms
		`).
		Scan(stats).Error

	if err != nil {
		return nil, err
	}

	// 计算异常率
	if stats.TotalConnections > 0 {
		var abnormalCount int64
		r.db.WithContext(ctx).
			Model(&ConnectionRecord{}).
			Where("connected_at BETWEEN ? AND ? AND is_abnormal = ?", startTime, endTime, true).
			Count(&abnormalCount)
		stats.AbnormalRate = float64(abnormalCount) / float64(stats.TotalConnections) * 100
	}

	// 计算重连率
	var totalReconnects int64
	r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connected_at BETWEEN ? AND ?", startTime, endTime).
		Select("SUM(reconnect_count)").
		Scan(&totalReconnects)

	if stats.TotalConnections > 0 {
		stats.ReconnectRate = float64(totalReconnects) / float64(stats.TotalConnections) * 100
	}

	return stats, nil
}

// GetUserConnectionStats 获取用户连接统计
func (r *connectionRecordRepositoryImpl) GetUserConnectionStats(ctx context.Context, userID string, startTime, endTime time.Time) (*UserConnectionStats, error) {
	stats := &UserConnectionStats{
		UserID: userID,
	}

	// 基础统计
	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("user_id = ? AND connected_at BETWEEN ? AND ?", userID, startTime, endTime).
		Select(`
			COUNT(*) as total_connections,
			SUM(CASE WHEN is_active = true THEN 1 ELSE 0 END) as active_connections,
			AVG(CASE WHEN duration > 0 THEN duration ELSE NULL END) as average_duration,
			MAX(connected_at) as last_connected_at,
			SUM(reconnect_count) as total_reconnects,
			SUM(error_count) as total_errors
		`).
		Scan(stats).Error

	if err != nil {
		return nil, err
	}

	// 计算平均连接质量
	var totalQuality float64
	var qualityCount int64
	records, _ := r.GetByUserID(ctx, userID, 100, 0)
	for _, record := range records {
		if record.ConnectedAt.After(startTime) && record.ConnectedAt.Before(endTime) {
			totalQuality += record.GetConnectionQuality()
			qualityCount++
		}
	}
	if qualityCount > 0 {
		stats.AverageConnectionQuality = totalQuality / float64(qualityCount)
	}

	return stats, nil
}

// GetNodeConnectionStats 获取节点连接统计
func (r *connectionRecordRepositoryImpl) GetNodeConnectionStats(ctx context.Context, nodeID string, startTime, endTime time.Time) (*NodeConnectionStats, error) {
	stats := &NodeConnectionStats{
		NodeID: nodeID,
	}

	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("node_id = ? AND connected_at BETWEEN ? AND ?", nodeID, startTime, endTime).
		Select(`
			COUNT(*) as total_connections,
			SUM(CASE WHEN is_active = true THEN 1 ELSE 0 END) as active_connections,
			AVG(CASE WHEN duration > 0 THEN duration ELSE NULL END) as average_duration,
			SUM(messages_sent) as total_messages_sent,
			SUM(messages_received) as total_messages_received,
			AVG(CASE WHEN average_ping_ms > 0 THEN average_ping_ms ELSE NULL END) as average_ping_ms
		`).
		Scan(stats).Error

	if err != nil {
		return nil, err
	}

	// 计算错误率
	if stats.TotalConnections > 0 {
		var totalErrors int64
		r.db.WithContext(ctx).
			Model(&ConnectionRecord{}).
			Where("node_id = ? AND connected_at BETWEEN ? AND ?", nodeID, startTime, endTime).
			Select("SUM(error_count)").
			Scan(&totalErrors)

		totalMessages := stats.TotalMessagesSent + stats.TotalMessagesReceived
		if totalMessages > 0 {
			stats.ErrorRate = float64(totalErrors) / float64(totalMessages) * 100
		}
	}

	return stats, nil
}

// ========== 异常检测操作 ==========

// GetAbnormalConnections 获取异常连接记录
func (r *connectionRecordRepositoryImpl) GetAbnormalConnections(ctx context.Context, limit int, offset int) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("is_abnormal = ?", true).
		Order("connected_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error
	return records, err
}

// GetHighErrorRateConnections 获取高错误率连接
func (r *connectionRecordRepositoryImpl) GetHighErrorRateConnections(ctx context.Context, errorThreshold int, limit int) ([]*ConnectionRecord, error) {
	var records []*ConnectionRecord
	err := r.db.WithContext(ctx).
		Where("error_count >= ?", errorThreshold).
		Order("error_count DESC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

// GetFrequentReconnectUsers 获取频繁重连的用户
func (r *connectionRecordRepositoryImpl) GetFrequentReconnectUsers(ctx context.Context, reconnectThreshold int, timeRange time.Duration) ([]UserReconnectStats, error) {
	var stats []UserReconnectStats

	startTime := time.Now().Add(-timeRange)

	err := r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Select("user_id, SUM(reconnect_count) as reconnect_count, MAX(connected_at) as last_reconnect_at").
		Where("connected_at >= ?", startTime).
		Group("user_id").
		Having("SUM(reconnect_count) >= ?", reconnectThreshold).
		Order("reconnect_count DESC").
		Scan(&stats).Error

	return stats, err
}

// ========== 批量操作 ==========

// BatchCreate 批量创建连接记录
func (r *connectionRecordRepositoryImpl) BatchCreate(ctx context.Context, records []*ConnectionRecord) error {
	if len(records) == 0 {
		return nil
	}

	// 分批处理，每批1000条
	batchSize := 1000
	for i := 0; i < len(records); i += batchSize {
		end := i + batchSize
		if end > len(records) {
			end = len(records)
		}

		if err := r.db.WithContext(ctx).Create(records[i:end]).Error; err != nil {
			return fmt.Errorf("batch create failed at index %d: %w", i, err)
		}
	}

	return nil
}

// BatchUpdateActive 批量更新活跃状态
func (r *connectionRecordRepositoryImpl) BatchUpdateActive(ctx context.Context, connectionIDs []string, isActive bool) error {
	if len(connectionIDs) == 0 {
		return nil
	}

	return r.db.WithContext(ctx).
		Model(&ConnectionRecord{}).
		Where("connection_id IN ?", connectionIDs).
		Update("is_active", isActive).Error
}

// BatchDelete 批量删除记录
func (r *connectionRecordRepositoryImpl) BatchDelete(ctx context.Context, ids []uint64) error {
	if len(ids) == 0 {
		return nil
	}

	return r.db.WithContext(ctx).
		Delete(&ConnectionRecord{}, ids).Error
}

// ========== 清理操作 ==========

// CleanupOldRecords 清理旧记录
func (r *connectionRecordRepositoryImpl) CleanupOldRecords(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("connected_at < ? AND is_active = ?", before, false).
		Delete(&ConnectionRecord{})

	if result.Error != nil {
		return 0, result.Error
	}

	return result.RowsAffected, nil
}

// ArchiveOldRecords 归档旧记录
// 使用游标分页方式分批查询并处理，避免一次性加载过多数据到内存
func (r *connectionRecordRepositoryImpl) ArchiveOldRecords(ctx context.Context, before time.Time, processor func([]*ConnectionRecord) error) (int64, error) {
	if processor == nil {
		return 0, fmt.Errorf("processor function cannot be nil")
	}

	batchSize := 1000
	totalProcessed := int64(0)
	lastID := uint64(0) // 使用ID作为游标，比OFFSET分页高效

	for {
		var records []*ConnectionRecord
		query := r.db.WithContext(ctx).
			Where("connected_at < ? AND is_active = ?", before, false)

		// 使用ID游标分页，避免OFFSET带来的性能问题
		if lastID > 0 {
			query = query.Where("id > ?", lastID)
		}

		err := query.
			Order("id ASC").
			Limit(batchSize).
			Find(&records).Error

		if err != nil {
			return totalProcessed, fmt.Errorf("failed to fetch records: %w", err)
		}

		// 没有更多记录了
		if len(records) == 0 {
			break
		}

		// 调用处理函数处理这批记录
		if err := processor(records); err != nil {
			return totalProcessed, fmt.Errorf("processor failed at last_id %d: %w", lastID, err)
		}

		totalProcessed += int64(len(records))

		// 更新游标为最后一条记录的ID
		lastID = records[len(records)-1].ID

		// 如果返回的记录数少于批次大小，说明已经处理完了
		if len(records) < batchSize {
			break
		}
	}

	return totalProcessed, nil
}

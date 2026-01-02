/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\message_record_repository.go
 * @Description: 消息发送记录管理 - 使用 GORM 数据库持久化
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// MessageRecordRepository 消息记录仓库接口
type MessageRecordRepository interface {
	// Create 创建记录
	Create(ctx context.Context, record *MessageSendRecord) error

	// CreateFromMessage 从 HubMessage 创建记录
	CreateFromMessage(ctx context.Context, msg *HubMessage, maxRetry int, expiresAt *time.Time) (*MessageSendRecord, error)

	// Update 更新记录
	Update(ctx context.Context, record *MessageSendRecord) error

	// FindByID 根据ID查找
	FindByID(ctx context.Context, id uint) (*MessageSendRecord, error)

	// FindByMessageID 根据消息ID查找
	FindByMessageID(ctx context.Context, messageID string) (*MessageSendRecord, error)

	// FindByStatus 根据状态查找
	FindByStatus(ctx context.Context, status MessageSendStatus, limit int) ([]*MessageSendRecord, error)

	// FindBySender 根据发送者查找
	FindBySender(ctx context.Context, sender string, limit int) ([]*MessageSendRecord, error)

	// FindByReceiver 根据接收者查找
	FindByReceiver(ctx context.Context, receiver string, limit int) ([]*MessageSendRecord, error)

	// FindByNodeIP 根据节点IP查找
	FindByNodeIP(ctx context.Context, nodeIP string, limit int) ([]*MessageSendRecord, error)

	// FindByClientIP 根据客户端IP查找
	FindByClientIP(ctx context.Context, clientIP string, limit int) ([]*MessageSendRecord, error)

	// FindRetryable 查找可重试的记录
	FindRetryable(ctx context.Context, limit int) ([]*MessageSendRecord, error)

	// DeleteExpired 删除过期的记录
	DeleteExpired(ctx context.Context) (int64, error)

	// Delete 删除记录
	Delete(ctx context.Context, id uint) error

	// DeleteByMessageID 根据消息ID删除
	DeleteByMessageID(ctx context.Context, messageID string) error

	// UpdateStatus 更新状态
	UpdateStatus(ctx context.Context, messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) error

	// IncrementRetry 增加重试次数
	IncrementRetry(ctx context.Context, messageID string, attempt RetryAttempt) error

	// GetStatistics 获取统计信息
	GetStatistics(ctx context.Context) (map[string]int64, error)

	// CleanupOld 清理旧记录
	CleanupOld(ctx context.Context, before time.Time) (int64, error)

	// GetDB 获取底层 GORM DB（用于复杂查询）
	GetDB() *gorm.DB
}

// MessageRecordGormRepository GORM 实现
type MessageRecordGormRepository struct {
	db *gorm.DB
}

// NewMessageRecordRepository 创建消息记录仓库
func NewMessageRecordRepository(db *gorm.DB) MessageRecordRepository {
	return &MessageRecordGormRepository{db: db}
}

// Create 创建记录
func (r *MessageRecordGormRepository) Create(ctx context.Context, record *MessageSendRecord) error {
	return r.db.WithContext(ctx).Create(record).Error
}

// CreateFromMessage 从 HubMessage 创建记录
func (r *MessageRecordGormRepository) CreateFromMessage(ctx context.Context, msg *HubMessage, maxRetry int, expiresAt *time.Time) (*MessageSendRecord, error) {
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   maxRetry,
		ExpiresAt:  expiresAt,
	}

	// 序列化 HubMessage
	if err := record.SetMessage(msg); err != nil {
		return nil, err
	}

	if err := r.db.WithContext(ctx).Create(record).Error; err != nil {
		return nil, err
	}

	return record, nil
}

// Update 更新记录
func (r *MessageRecordGormRepository) Update(ctx context.Context, record *MessageSendRecord) error {
	return r.db.WithContext(ctx).Save(record).Error
}

// FindByID 根据ID查找
func (r *MessageRecordGormRepository) FindByID(ctx context.Context, id uint) (*MessageSendRecord, error) {
	var record MessageSendRecord
	err := r.db.WithContext(ctx).First(&record, id).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// FindByMessageID 根据消息ID查找
func (r *MessageRecordGormRepository) FindByMessageID(ctx context.Context, messageID string) (*MessageSendRecord, error) {
	var record MessageSendRecord
	err := r.db.WithContext(ctx).Where(QueryMessageIDWhere, messageID).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// FindByStatus 根据状态查找
func (r *MessageRecordGormRepository) FindByStatus(ctx context.Context, status MessageSendStatus, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.WithContext(ctx).Where("status = ?", status).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindBySender 根据发送者查找
func (r *MessageRecordGormRepository) FindBySender(ctx context.Context, sender string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.WithContext(ctx).Where("sender = ?", sender).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindByReceiver 根据接收者查找
func (r *MessageRecordGormRepository) FindByReceiver(ctx context.Context, receiver string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.WithContext(ctx).Where("receiver = ?", receiver).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindByNodeIP 根据节点IP查找
func (r *MessageRecordGormRepository) FindByNodeIP(ctx context.Context, nodeIP string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.WithContext(ctx).Where("node_ip = ?", nodeIP).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindByClientIP 根据客户端IP查找
func (r *MessageRecordGormRepository) FindByClientIP(ctx context.Context, clientIP string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.WithContext(ctx).Where("client_ip = ?", clientIP).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindRetryable 查找可重试的记录
func (r *MessageRecordGormRepository) FindRetryable(ctx context.Context, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	now := time.Now()
	query := r.db.WithContext(ctx).Where("status IN ? AND retry_count < max_retry", []MessageSendStatus{
		MessageSendStatusFailed,
		MessageSendStatusAckTimeout,
	}).Where("expires_at IS NULL OR expires_at > ?", now).
		Order(OrderByCreateTimeAsc)

	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// DeleteExpired 删除过期的记录
func (r *MessageRecordGormRepository) DeleteExpired(ctx context.Context) (int64, error) {
	now := time.Now()
	result := r.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at < ?", now).Delete(&MessageSendRecord{})
	return result.RowsAffected, result.Error
}

// Delete 删除记录
func (r *MessageRecordGormRepository) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&MessageSendRecord{}, id).Error
}

// DeleteByMessageID 根据消息ID删除
func (r *MessageRecordGormRepository) DeleteByMessageID(ctx context.Context, messageID string) error {
	return r.db.WithContext(ctx).Where(QueryMessageIDWhere, messageID).Delete(&MessageSendRecord{}).Error
}

// UpdateStatus 更新状态
func (r *MessageRecordGormRepository) UpdateStatus(ctx context.Context, messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) error {
	now := time.Now()

	updates := map[string]interface{}{
		"status":         status,
		"last_send_time": &now,
	}

	// 设置失败原因和错误信息
	if reason != "" {
		updates["failure_reason"] = reason
	}
	if errorMsg != "" {
		updates["error_message"] = errorMsg
	}

	// 🔥 如果发送成功,设置成功时间
	if status == MessageSendStatusSuccess {
		updates["success_time"] = &now
	}

	// 🔥 直接更新，无需预查询。使用子查询条件：仅在 first_send_time 为 NULL 时设置
	// 注意：GORM 的 Updates 不会更新零值，所以需要显式处理 first_send_time
	result := r.db.WithContext(ctx).Model(&MessageSendRecord{}).
		Where(QueryMessageIDWhere, messageID).
		Updates(updates)

	// 如果记录存在且 first_send_time 还未设置，则单独更新它
	if result.Error == nil && result.RowsAffected > 0 {
		r.db.WithContext(ctx).Model(&MessageSendRecord{}).
			Where(QueryMessageIDWhere, messageID).
			Where("first_send_time IS NULL").
			Update("first_send_time", &now)
	}

	// 🔥 如果没有找到记录（RowsAffected == 0），静默返回（记录可能尚未创建或不需要记录）
	if result.Error != nil {
		return result.Error
	}
	return nil
}

// IncrementRetry 增加重试次数
func (r *MessageRecordGormRepository) IncrementRetry(ctx context.Context, messageID string, attempt RetryAttempt) error {
	var record MessageSendRecord
	err := r.db.WithContext(ctx).Where(QueryMessageIDWhere, messageID).First(&record).Error
	if err != nil {
		return err
	}

	now := time.Now()
	record.RetryHistory = append(record.RetryHistory, attempt)
	record.RetryCount = attempt.AttemptNumber

	updates := map[string]interface{}{
		"retry_count":    record.RetryCount,
		"retry_history":  record.RetryHistory,
		"status":         MessageSendStatusRetrying,
		"last_send_time": &now, // 🔥 每次重试都更新最后发送时间
	}

	// 🔥 如果是首次重试（first_send_time 为 NULL）,设置首次发送时间
	if record.FirstSendTime == nil {
		updates["first_send_time"] = &now
	}

	if attempt.Success {
		// 🔥 重试成功,设置成功时间
		updates["status"] = MessageSendStatusSuccess
		updates["success_time"] = &now
	} else if record.RetryCount >= record.MaxRetry {
		// 🔥 超过最大重试次数,设置失败状态和原因
		updates["status"] = MessageSendStatusFailed
		updates["failure_reason"] = FailureReasonMaxRetry
		if attempt.Error != "" {
			updates["error_message"] = attempt.Error
		}
	} else {
		// 🔥 重试中但未达到最大次数,记录错误信息
		if attempt.Error != "" {
			updates["error_message"] = attempt.Error
		}
	}

	return r.db.WithContext(ctx).Model(&record).Updates(updates).Error
}

// GetStatistics 获取统计信息
func (r *MessageRecordGormRepository) GetStatistics(ctx context.Context) (map[string]int64, error) {
	stats := make(map[string]int64)

	// 总数
	var total int64
	r.db.WithContext(ctx).Model(&MessageSendRecord{}).Count(&total)
	stats["total"] = total

	// 按状态统计
	statuses := []MessageSendStatus{
		MessageSendStatusPending,
		MessageSendStatusSending,
		MessageSendStatusSuccess,
		MessageSendStatusFailed,
		MessageSendStatusRetrying,
		MessageSendStatusAckTimeout,
		MessageSendStatusUserOffline,
		MessageSendStatusExpired,
	}

	for _, status := range statuses {
		var count int64
		r.db.WithContext(ctx).Model(&MessageSendRecord{}).Where("status = ?", status).Count(&count)
		stats[string(status)] = count
	}

	return stats, nil
}

// CleanupOld 清理旧记录
func (r *MessageRecordGormRepository) CleanupOld(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Where("create_time < ? AND status IN ?", before, []MessageSendStatus{
		MessageSendStatusSuccess,
		MessageSendStatusFailed,
		MessageSendStatusExpired,
	}).Delete(&MessageSendRecord{})

	return result.RowsAffected, result.Error
}

// GetDB 获取底层 GORM DB
func (r *MessageRecordGormRepository) GetDB() *gorm.DB {
	return r.db
}

// MessageRecordHooks 消息记录钩子函数接口
type MessageRecordHooks interface {
	// OnRecordCreated 记录创建时调用
	OnRecordCreated(record *MessageSendRecord) error

	// OnRecordUpdated 记录更新时调用
	OnRecordUpdated(record *MessageSendRecord, oldStatus MessageSendStatus, newStatus MessageSendStatus) error

	// OnRetryAttempt 重试尝试时调用
	OnRetryAttempt(record *MessageSendRecord, attempt *RetryAttempt) error

	// OnRecordDeleted 记录删除前调用
	OnRecordDeleted(record *MessageSendRecord) error

	// OnRecordExpired 记录过期时调用
	OnRecordExpired(record *MessageSendRecord) error
}

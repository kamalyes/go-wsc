/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 17:55:16
 * @FilePath: \go-wsc\offline_message_repository.go
 * @Description: 离线消息数据库仓库
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// OfflineMessageRecord 离线消息记录
// 用于存储用户离线时接收到的消息信息，包含发送者、接收者及消息内容等关键数据。
type OfflineMessageRecord struct {
	ID             uint      `gorm:"primaryKey;autoIncrement;comment:主键,唯一标识离线消息记录" json:"id"`
	MessageID      string    `gorm:"column:message_id;size:64;not null;uniqueIndex;comment:消息ID,唯一索引,不能为空" json:"message_id"`
	Sender         string    `gorm:"index;size:255;comment:发送者ID" json:"sender"`
	Receiver       string    `gorm:"index;size:255;comment:接收者ID" json:"receiver"`
	SessionID      string    `gorm:"column:session_id;size:64;not null;index;comment:会话ID" json:"session_id"`
	CompressedData []byte    `gorm:"type:longblob;not null;comment:压缩完整的HubMessage JSON数据" json:"-"`
	ScheduledAt    time.Time `gorm:"column:scheduled_at;not null;comment:消息计划发送的时间" json:"scheduled_at"`
	ExpireAt       time.Time `gorm:"column:expire_at;not null;index;comment:消息过期时间" json:"expire_at"`
	CreatedAt      time.Time `gorm:"autoCreateTime;comment:记录创建时间" json:"created_at"`
	UpdatedAt      time.Time `gorm:"autoUpdateTime;comment:记录最后更新时间" json:"updated_at"`
}

// TableName 指定表名
func (OfflineMessageRecord) TableName() string {
	return "wsc_offline_messages"
}

// TableComment 表注释
func (OfflineMessageRecord) TableComment() string {
	return "WebSocket离线消息记录表-存储用户离线时接收到的消息信息用于消息投递和管理"
}

// OfflineMessageDBRepository 离线消息数据库仓库接口
type OfflineMessageDBRepository interface {
	// Save 保存离线消息到数据库
	Save(ctx context.Context, record *OfflineMessageRecord) error

	// GetByReceiver 获取用户作为接收者的离线消息列表
	GetByReceiver(ctx context.Context, receiverID string, limit int) ([]*OfflineMessageRecord, error)

	// GetBySender 获取用户作为发送者的离线消息列表
	GetBySender(ctx context.Context, senderID string, limit int) ([]*OfflineMessageRecord, error)

	// DeleteByMessageIDs 批量删除离线消息（按接收者）
	DeleteByMessageIDs(ctx context.Context, receiverID string, messageIDs []string) error

	// GetCountByReceiver 获取用户作为接收者的离线消息数量
	GetCountByReceiver(ctx context.Context, receiverID string) (int64, error)

	// GetCountBySender 获取用户作为发送者的离线消息数量
	GetCountBySender(ctx context.Context, senderID string) (int64, error)

	// ClearByReceiver 清空用户作为接收者的所有离线消息
	ClearByReceiver(ctx context.Context, receiverID string) error

	// DeleteExpired 删除过期的离线消息
	DeleteExpired(ctx context.Context) (int64, error)
}

// GormOfflineMessageRepository GORM实现
type GormOfflineMessageRepository struct {
	db *gorm.DB
}

// NewGormOfflineMessageRepository 创建GORM离线消息仓库
func NewGormOfflineMessageRepository(db *gorm.DB) OfflineMessageDBRepository {
	return &GormOfflineMessageRepository{db: db}
}

// Save 保存离线消息到数据库
func (r *GormOfflineMessageRepository) Save(ctx context.Context, record *OfflineMessageRecord) error {
	return r.db.WithContext(ctx).Create(record).Error
}

// GetByReceiver 获取用户作为接收者的离线消息列表
func (r *GormOfflineMessageRepository) GetByReceiver(ctx context.Context, receiverID string, limit int) ([]*OfflineMessageRecord, error) {
	var records []*OfflineMessageRecord
	err := r.db.WithContext(ctx).
		Where("receiver = ? AND expire_at > ?", receiverID, time.Now()).
		Where("pushed_at IS NULL").
		Order("created_at ASC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

// GetBySender 获取用户作为发送者的离线消息列表
func (r *GormOfflineMessageRepository) GetBySender(ctx context.Context, senderID string, limit int) ([]*OfflineMessageRecord, error) {
	var records []*OfflineMessageRecord
	err := r.db.WithContext(ctx).
		Where("sender = ? AND expire_at > ?", senderID, time.Now()).
		Where("pushed_at IS NULL").
		Order("created_at ASC").
		Limit(limit).
		Find(&records).Error
	return records, err
}

// DeleteByMessageIDs 批量删除离线消息（按接收者）
func (r *GormOfflineMessageRepository) DeleteByMessageIDs(ctx context.Context, receiverID string, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("receiver = ? AND message_id IN ?", receiverID, messageIDs).
		Delete(&OfflineMessageRecord{}).Error
}

// GetCountByReceiver 获取用户作为接收者的离线消息数量
func (r *GormOfflineMessageRepository) GetCountByReceiver(ctx context.Context, receiverID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&OfflineMessageRecord{}).
		Where("receiver = ? AND expire_at > ?", receiverID, time.Now()).
		Where("pushed_at IS NULL").
		Count(&count).Error
	return count, err
}

// GetCountBySender 获取用户作为发送者的离线消息数量
func (r *GormOfflineMessageRepository) GetCountBySender(ctx context.Context, senderID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&OfflineMessageRecord{}).
		Where("sender = ? AND expire_at > ?", senderID, time.Now()).
		Where("pushed_at IS NULL").
		Count(&count).Error
	return count, err
}

// ClearByReceiver 清空用户作为接收者的所有离线消息
func (r *GormOfflineMessageRepository) ClearByReceiver(ctx context.Context, receiverID string) error {
	return r.db.WithContext(ctx).
		Where("receiver = ?", receiverID).
		Delete(&OfflineMessageRecord{}).Error
}

// DeleteExpired 删除过期的离线消息
func (r *GormOfflineMessageRepository) DeleteExpired(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("expire_at < ?", time.Now()).
		Delete(&OfflineMessageRecord{})
	return result.RowsAffected, result.Error
}

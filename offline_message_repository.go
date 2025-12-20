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

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"gorm.io/gorm"
)

// OfflineMessageRecord 离线消息记录
// 用于存储用户离线时接收到的消息信息，包含发送者、接收者及消息内容等关键数据。
type OfflineMessageRecord struct {
	ID             uint       `gorm:"primaryKey;autoIncrement;comment:主键,唯一标识离线消息记录" json:"id"`
	MessageID      string     `gorm:"column:message_id;size:64;not null;uniqueIndex;comment:业务消息ID,唯一索引,不能为空" json:"message_id"`
	HubID          string     `gorm:"column:hub_id;size:128;index;comment:Hub内部消息ID,用于ACK确认" json:"hub_id"`
	Sender         string     `gorm:"index;size:255;comment:发送者ID" json:"sender"`
	Receiver       string     `gorm:"index;size:255;comment:接收者ID" json:"receiver"` // 建议创建复合索引：idx_receiver_created_at (receiver, created_at)
	SessionID      string     `gorm:"column:session_id;size:64;not null;index;comment:会话ID" json:"session_id"`
	CompressedData []byte     `gorm:"type:longblob;not null;comment:压缩完整的HubMessage JSON数据" json:"-"`
	ScheduledAt    time.Time  `gorm:"column:scheduled_at;not null;comment:消息计划发送的时间" json:"scheduled_at"`
	ExpireAt       time.Time  `gorm:"column:expire_at;not null;index;comment:消息过期时间" json:"expire_at"`
	PushedAt       *time.Time `gorm:"column:pushed_at;index;comment:消息推送时间,NULL表示未推送" json:"pushed_at,omitempty"`
	CreatedAt      time.Time  `gorm:"autoCreateTime;comment:记录创建时间" json:"created_at"`
	UpdatedAt      time.Time  `gorm:"autoUpdateTime;comment:记录最后更新时间" json:"updated_at"`
}

// TableName 指定表名
func (OfflineMessageRecord) TableName() string {
	return "wsc_offline_messages"
}

// TableComment 表注释
func (OfflineMessageRecord) TableComment() string {
	return "WebSocket离线消息记录表-存储用户离线时接收到的消息信息用于消息投递和管理"
}

// 性能优化建议：
// 1. 创建复合索引以提升查询性能：
//    CREATE INDEX idx_receiver_created_at ON wsc_offline_messages(receiver, created_at);
//    CREATE INDEX idx_receiver_pushed_expire ON wsc_offline_messages(receiver, pushed_at, expire_at);
// 2. message_id 已有唯一索引，用于 cursor 子查询优化

// OfflineMessageDBRepository 离线消息数据库仓库接口
type OfflineMessageDBRepository interface {
	// Save 保存离线消息到数据库
	Save(ctx context.Context, record *OfflineMessageRecord) error

	// BatchSave 批量保存离线消息到数据库
	BatchSave(ctx context.Context, records []*OfflineMessageRecord) error

	// GetByReceiver 获取用户作为接收者的离线消息列表
	// cursor: 可选参数，传入上次返回的最后一条 message_id，空字符串表示从头开始
	GetByReceiver(ctx context.Context, receiverID string, limit int, cursor ...string) ([]*OfflineMessageRecord, error)

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

	// MarkAsPushed 标记消息为已推送
	MarkAsPushed(ctx context.Context, messageIDs []string) error
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

// BatchSave 批量保存离线消息到数据库
// 使用 CreateInBatches 提高批量插入性能
func (r *GormOfflineMessageRepository) BatchSave(ctx context.Context, records []*OfflineMessageRecord) error {
	if len(records) == 0 {
		return nil
	}
	// 每批插入 1000 条
	return r.db.WithContext(ctx).CreateInBatches(records, 1000).Error
}

// GetByReceiver 获取用户作为接收者的离线消息列表
// 按 created_at 升序排列，保证时序一致性
// cursor: 可选参数，传入上次返回的最后一条 message_id 实现分页
func (r *GormOfflineMessageRepository) GetByReceiver(ctx context.Context, receiverID string, limit int, cursor ...string) ([]*OfflineMessageRecord, error) {
	var records []*OfflineMessageRecord
	query := r.db.WithContext(ctx).
		Where("receiver = ? AND expire_at > ?", receiverID, time.Now()).
		Where("pushed_at IS NULL")

	// 如果提供了 cursor，从 cursor 之后的消息开始读取
	// 优化：使用子查询一次性获取 cursor 的 created_at，避免额外查询
	if len(cursor) > 0 && cursor[0] != "" {
		// 使用子查询直接过滤，避免额外的 SELECT 查询
		tableName := OfflineMessageRecord{}.TableName()
		query = query.Where("created_at > (SELECT created_at FROM "+tableName+" WHERE message_id = ? LIMIT 1)", cursor[0])
	}

	// MySQL 查询限制：用户指定 limit 或最多 1 万条
	limit = mathx.IF(limit <= 0, 10000, min(limit, 10000))

	err := query.Order("created_at ASC").
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

// MarkAsPushed 标记消息为已推送
func (r *GormOfflineMessageRepository) MarkAsPushed(ctx context.Context, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&OfflineMessageRecord{}).
		Where("message_id IN ?", messageIDs).
		Update("pushed_at", now).Error
}

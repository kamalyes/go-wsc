/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 23:30:35
 * @FilePath: \go-wsc\repository\offline_message_repository.go
 * @Description: 离线消息数据库仓库
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"gorm.io/gorm"
)

// 性能优化建议：
// 1. 创建复合索引以提升查询性能：
//    CREATE INDEX idx_receiver_created_at ON wsc_offline_messages(receiver, created_at);
//    CREATE INDEX idx_receiver_status_expire ON wsc_offline_messages(receiver, status, expire_at);
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

	// UpdatePushStatus 更新离线消息推送状态
	// status: 消息状态(pending/success/failed)
	// errorMsg: 错误信息(失败时)
	UpdatePushStatus(ctx context.Context, messageIDs []string, status MessageSendStatus, errorMsg string) error

	// CleanupOld 清理旧记录
	CleanupOld(ctx context.Context, before time.Time) (int64, error)

	// Close 关闭仓库，停止后台任务
	Close() error
}

// GormOfflineMessageRepository GORM实现
type GormOfflineMessageRepository struct {
	db         *gorm.DB
	logger     logger.ILogger
	cancelFunc context.CancelFunc
}

// NewGormOfflineMessageRepository 创建GORM离线消息仓库
// 参数:
//   - db: GORM 数据库实例
//   - config: 离线消息配置对象（可选，传 nil 则不启用自动清理）
//   - log: 日志记录器
func NewGormOfflineMessageRepository(db *gorm.DB, config *wscconfig.OfflineMessage, log logger.ILogger) OfflineMessageDBRepository {
	ctx, cancel := context.WithCancel(context.Background())

	repo := &GormOfflineMessageRepository{
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
		Where("status IN ?", PendingOfflineStatuses)

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
		Where("status IN ?", PendingOfflineStatuses).
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
		Where("status IN ?", PendingOfflineStatuses).
		Count(&count).Error
	return count, err
}

// GetCountBySender 获取用户作为发送者的离线消息数量
func (r *GormOfflineMessageRepository) GetCountBySender(ctx context.Context, senderID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&OfflineMessageRecord{}).
		Where("sender = ? AND expire_at > ?", senderID, time.Now()).
		Where("status IN ?", PendingOfflineStatuses).
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

// UpdatePushStatus 更新离线消息推送状态
func (r *GormOfflineMessageRepository) UpdatePushStatus(ctx context.Context, messageIDs []string, status MessageSendStatus, errorMsg string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	now := time.Now()
	updates := map[string]interface{}{
		"status":       status,
		"last_push_at": now,
	}

	// 首次推送时设置 first_push_at (仅当 first_push_at 为空时设置)
	// 使用 COALESCE 确保只在第一次推送时设置
	updates["first_push_at"] = gorm.Expr("COALESCE(first_push_at, ?)", now)

	// 失败时记录错误信息并增加重试次数
	switch status {
	case MessageSendStatusFailed:
		updates["error_message"] = errorMsg
		updates["retry_count"] = gorm.Expr("retry_count + 1")
	case MessageSendStatusSuccess:
		// 成功时清空错误信息
		updates["error_message"] = ""
	}

	return r.db.WithContext(ctx).
		Model(&OfflineMessageRecord{}).
		Where("message_id IN ?", messageIDs).
		Updates(updates).Error
}

// CleanupOld 清理旧记录（已成功推送或已过期的消息）
func (r *GormOfflineMessageRepository) CleanupOld(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("created_at < ? AND (status = ? OR expire_at < ?)", before, MessageSendStatusSuccess, time.Now()).
		Delete(&OfflineMessageRecord{})
	return result.RowsAffected, result.Error
}

// startCleanupScheduler 启动定时清理任务
func (r *GormOfflineMessageRepository) startCleanupScheduler(ctx context.Context, daysAgo int) {
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
			r.logger.Errorf("⚠️ 离线消息清理任务 panic: %v", rec)
		}).
		// 优雅关闭
		OnShutdown(func() {
			r.logger.Info("🛑 离线消息清理任务已停止")
		}).
		Run()
}

// cleanupOldData 清理N天前的历史数据
func (r *GormOfflineMessageRepository) cleanupOldData(ctx context.Context, daysAgo int) {
	if daysAgo <= 0 {
		return
	}

	before := time.Now().AddDate(0, 0, -daysAgo)

	// 清理旧记录
	deleted, err := r.CleanupOld(ctx, before)
	if err != nil {
		r.logger.Warnf("⚠️ 清理历史离线消息失败: %v", err)
	} else if deleted > 0 {
		r.logger.Infof("🧹 已清理 %d 天前的历史离线消息，删除 %d 条", daysAgo, deleted)
	}

	// 同时清理过期消息
	expiredDeleted, err := r.DeleteExpired(ctx)
	if err != nil {
		r.logger.Warnf("⚠️ 清理过期离线消息失败: %v", err)
	} else if expiredDeleted > 0 {
		r.logger.Infof("🧹 已清理过期离线消息，删除 %d 条", expiredDeleted)
	}
}

// Close 关闭仓库，停止后台清理任务
func (r *GormOfflineMessageRepository) Close() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
		r.logger.Info("🛑 OfflineMessageRepository 已关闭")
	}
	return nil
}

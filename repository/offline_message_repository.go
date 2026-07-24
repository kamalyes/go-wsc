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
	sqlbuilder "github.com/kamalyes/go-sqlbuilder/repository"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"gorm.io/gorm"
)

// 性能优化建议：
// 1. 创建复合索引以提升查询性能：
//    CREATE INDEX idx_receiver_created_at ON wsc_offline_messages(receiver, created_at);
//    CREATE INDEX idx_receiver_status_expire ON wsc_offline_messages(receiver, status, expire_at);
// 2. message_id 已有唯一索引，用于 cursor 子查询优化

// MessageRole 消息查询角色
type MessageRole string

const (
	// MessageRoleReceiver 作为接收者查询
	MessageRoleReceiver MessageRole = "receiver"
	// MessageRoleSender 作为发送者查询
	MessageRoleSender MessageRole = "sender"
)

// OfflineMessageDBRepository 离线消息数据库仓库接口
type OfflineMessageDBRepository interface {
	// Save 保存离线消息到数据库
	Save(ctx context.Context, record *OfflineMessageRecord) error

	// BatchSave 批量保存离线消息到数据库
	BatchSave(ctx context.Context, records []*OfflineMessageRecord) error

	// QueryMessages 查询离线消息（支持按接收者/发送者、分页、状态过滤）
	QueryMessages(ctx context.Context, filter *OfflineMessageFilter) ([]*OfflineMessageRecord, error)

	// DeleteByMessageIDs 批量删除离线消息（按命名空间+接收者）
	DeleteByMessageIDs(ctx context.Context, namespace, receiverID string, messageIDs []string) error

	// GetCountByReceiver 获取用户作为接收者的离线消息数量（按命名空间隔离）
	GetCountByReceiver(ctx context.Context, namespace, receiverID string) (int64, error)

	// GetCountBySender 获取用户作为发送者的离线消息数量（按命名空间隔离）
	GetCountBySender(ctx context.Context, namespace, senderID string) (int64, error)

	// ClearByReceiver 清空用户作为接收者的所有离线消息（按命名空间隔离）
	ClearByReceiver(ctx context.Context, namespace, receiverID string) error

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

// OfflineMessageFilter 离线消息查询过滤器
type OfflineMessageFilter struct {
	// UserID 用户ID
	UserID string
	// Role 角色
	Role MessageRole
	// Namespace 命名空间ID（空表示所有命名空间）
	Namespace string
	// GroupID 群组ID（空表示非群组消息或所有消息）
	GroupID string
	// Limit 数量限制
	Limit int
	// Cursor 分页游标（message_id）
	Cursor string
	// Statuses 消息状态列表，为空则待处理状态
	Statuses []MessageSendStatus
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
// namespace 由调用方（注册时归一化）保证，本层不再重复归一化
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

// QueryMessages 查询离线消息（支持按接收者/发送者、分页、状态过滤）
// 按 created_at 升序排列，保证时序一致性
func (r *GormOfflineMessageRepository) QueryMessages(ctx context.Context, filter *OfflineMessageFilter) ([]*OfflineMessageRecord, error) {
	var records []*OfflineMessageRecord

	// 使用 go-sqlbuilder 构建查询
	query := sqlbuilder.NewQuery().
		AddFilter(sqlbuilder.NewGtFilter("expire_at", time.Now()))

	// 根据角色设置用户过滤条件
	switch filter.Role {
	case MessageRoleReceiver:
		query.AddFilterIfNotEmpty("receiver", filter.UserID)
	case MessageRoleSender:
		query.AddFilterIfNotEmpty("sender", filter.UserID)
	}

	// 命名空间过滤：空表示查询所有命名空间（由 AddFilterIfNotEmpty 处理）
	query.AddFilterIfNotEmpty("namespace", filter.Namespace)
	if filter.GroupID != "" {
		query.AddFilterIfNotEmpty("group_id", filter.GroupID)
	}

	// 状态过滤：如果指定了状态则使用指定状态，否则使用默认的待处理状态
	if len(filter.Statuses) > 0 {
		statusesInterface := make([]interface{}, len(filter.Statuses))
		for i, status := range filter.Statuses {
			statusesInterface[i] = status
		}
		query.AddInFilterIfNotEmpty("status", statusesInterface)
	} else {
		statusesInterface := make([]interface{}, len(PendingOfflineStatuses))
		for i, status := range PendingOfflineStatuses {
			statusesInterface[i] = status
		}
		query.AddInFilterIfNotEmpty("status", statusesInterface)
	}

	// 排序
	query.AddOrder("created_at", "ASC")

	// MySQL 查询限制：用户指定 limit 或最多 1 万条
	limit := mathx.IF(filter.Limit <= 0, 10000, min(filter.Limit, 10000))
	query.Limit(limit)

	// 应用到 GORM
	gormDB := r.db.WithContext(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)

	// 分页游标：从 cursor 之后的消息开始读取（使用原生 GORM，因为需要子查询）
	if filter.Cursor != "" {
		tableName := OfflineMessageRecord{}.TableName()
		gormDB = gormDB.Where("created_at > (SELECT created_at FROM "+tableName+" WHERE message_id = ? LIMIT 1)", filter.Cursor)
	}

	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
	return records, err
}

// DeleteByMessageIDs 批量删除离线消息（按命名空间+接收者）
func (r *GormOfflineMessageRepository) DeleteByMessageIDs(ctx context.Context, namespace, receiverID string, messageIDs []string) error {
	if len(messageIDs) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("namespace = ? AND receiver = ? AND message_id IN ?", namespace, receiverID, messageIDs).
		Delete(&OfflineMessageRecord{}).Error
}

// GetCountByReceiver 获取用户作为接收者的离线消息数量（按命名空间隔离）
func (r *GormOfflineMessageRepository) GetCountByReceiver(ctx context.Context, namespace, receiverID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&OfflineMessageRecord{}).
		Where("namespace = ? AND receiver = ? AND expire_at > ?", namespace, receiverID, time.Now()).
		Where("status IN ?", PendingOfflineStatuses).
		Count(&count).Error
	return count, err
}

// GetCountBySender 获取用户作为发送者的离线消息数量（按命名空间隔离）
func (r *GormOfflineMessageRepository) GetCountBySender(ctx context.Context, namespace, senderID string) (int64, error) {	var count int64
	err := r.db.WithContext(ctx).
		Model(&OfflineMessageRecord{}).
		Where("namespace = ? AND sender = ? AND expire_at > ?", namespace, senderID, time.Now()).
		Where("status IN ?", PendingOfflineStatuses).
		Count(&count).Error
	return count, err
}

// ClearByReceiver 清空用户作为接收者的所有离线消息（按命名空间隔离）
func (r *GormOfflineMessageRepository) ClearByReceiver(ctx context.Context, namespace, receiverID string) error {
	return r.db.WithContext(ctx).
		Where("namespace = ? AND receiver = ?", namespace, receiverID).
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

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\repository\message_record_repository.go
 * @Description: 消息发送记录管理 - 使用 GORM 数据库持久化
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	sqlbuilder "github.com/kamalyes/go-sqlbuilder/repository"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"gorm.io/gorm"
)

// MessageRecordFilter 消息记录查询过滤器
type MessageRecordFilter struct {
	// Status 按状态查询（可选）
	Status *MessageSendStatus
	// Sender 按发送者查询（可选）
	Sender string
	// Receiver 按接收者查询（可选）
	Receiver string
	// NodeIP 按节点IP查询（可选）
	NodeIP string
	// ClientIP 按客户端IP查询（可选）
	ClientIP string
	// Limit 查询数量限制
	Limit int
	// OrderDesc 是否降序排序（默认降序，false为升序）
	OrderDesc bool
}

// MessageRecordRepository 消息记录仓库接口
type MessageRecordRepository interface {
	// Create 创建记录
	Create(ctx context.Context, record *MessageSendRecord) error

	// Update 更新记录
	Update(ctx context.Context, record *MessageSendRecord) error

	// FindByID 根据ID查找
	FindByID(ctx context.Context, id uint) (*MessageSendRecord, error)

	// FindByMessageID 根据消息ID查找
	FindByMessageID(ctx context.Context, messageID string) (*MessageSendRecord, error)

	// QueryRecords 查询消息记录（支持按状态、发送者、接收者、节点IP、客户端IP等条件过滤）
	QueryRecords(ctx context.Context, filter *MessageRecordFilter) ([]*MessageSendRecord, error)

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

	// BatchUpdateStatus 批量更新消息状态
	// 相同 status/reason/errorMsg 的消息用一条 SQL 批量更新（WHERE message_id IN (...)）
	// 用于广播场景下大量消息同时成功/失败的状态更新，大幅减少 DB 压力
	BatchUpdateStatus(ctx context.Context, messageIDs []string, status MessageSendStatus, reason FailureReason, errorMsg string) error

	// ClaimStaleSending 原子认领超时的 sending 记录：仅当记录当前状态仍为 sending 时更新为 newStatus
	// 返回实际认领成功的 messageID 列表（状态已被并发方更新则不认领）
	// 多节点并发扫描共享记录表时，状态守卫（WHERE status='sending'）保证每条记录
	// 只被一个节点认领成功，认领者负责后续兜底动作（如转存离线），消除 N Pod 重复处理
	ClaimStaleSending(ctx context.Context, messageIDs []string, newStatus MessageSendStatus, reason FailureReason, errorMsg string) ([]string, error)

	// IncrementRetry 增加重试次数
	IncrementRetry(ctx context.Context, messageID string, attempt RetryAttempt) error

	// GetStatistics 获取统计信息
	GetStatistics(ctx context.Context) (map[string]int64, error)

	// CleanupOld 清理旧记录
	CleanupOld(ctx context.Context, before time.Time) (int64, error)

	// GetDB 获取底层 GORM DB（用于复杂查询）
	GetDB() *gorm.DB

	// Close 关闭仓库，停止后台任务
	Close() error
}

// MessageRecordGormRepository GORM 实现
type MessageRecordGormRepository struct {
	db         *gorm.DB
	logger     logger.ILogger
	cancelFunc context.CancelFunc
}

// NewMessageRecordRepository 创建消息记录仓库
// 参数:
//   - db: GORM 数据库实例
//   - config: 消息记录配置对象（可选，传 nil 则不启用自动清理）
//   - log: 日志记录器
func NewMessageRecordRepository(db *gorm.DB, config *wscconfig.MessageRecord, log logger.ILogger) MessageRecordRepository {
	ctx, cancel := context.WithCancel(context.Background())

	repo := &MessageRecordGormRepository{
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

// Create 创建记录
func (r *MessageRecordGormRepository) Create(ctx context.Context, record *MessageSendRecord) error {
	return r.db.WithContext(ctx).Create(record).Error
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
	err := r.db.WithContext(ctx).Where(models.QueryMessageIDWhere, messageID).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// QueryRecords 查询消息记录（支持按状态、发送者、接收者、节点IP、客户端IP等条件过滤）
func (r *MessageRecordGormRepository) QueryRecords(ctx context.Context, filter *MessageRecordFilter) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord

	query := sqlbuilder.NewQuery().
		AddFilterIfNotEmpty("status", filter.Status).
		AddFilterIfNotEmpty("sender", filter.Sender).
		AddFilterIfNotEmpty("receiver", filter.Receiver).
		AddFilterIfNotEmpty("node_ip", filter.NodeIP).
		AddFilterIfNotEmpty("client_ip", filter.ClientIP).
		AddOrder("create_time", mathx.IF(filter.OrderDesc, "DESC", "ASC"))

	// 限制数量
	if filter.Limit > 0 {
		query.Limit(filter.Limit)
	}

	// 将 Query 应用到 GORM
	gormDB := r.db.WithContext(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)
	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
	return records, err
}

// FindRetryable 查找可重试的记录
func (r *MessageRecordGormRepository) FindRetryable(ctx context.Context, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	now := time.Now()

	// 使用 go-sqlbuilder 构建基础查询
	retryableStatuses := []interface{}{
		models.MessageSendStatusFailed,
		models.MessageSendStatusAckTimeout,
	}

	query := sqlbuilder.NewQuery().
		AddInFilterIfNotEmpty("status", retryableStatuses).
		AddOrder("create_time", "ASC")

	if limit > 0 {
		query.Limit(limit)
	}

	// 应用到 GORM 并添加原始 WHERE 条件
	gormDB := r.db.WithContext(ctx)
	gormDB = sqlbuilder.ApplyFilters(gormDB, query.Filters)
	gormDB = gormDB.Where("retry_count < max_retry")
	gormDB = gormDB.Where("expires_at IS NULL OR expires_at > ?", now)
	gormDB = sqlbuilder.ApplyOrders(gormDB, query.Orders)
	if query.LimitValue != nil {
		gormDB = gormDB.Limit(*query.LimitValue)
	}

	err := gormDB.Find(&records).Error
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
	return r.db.WithContext(ctx).Where(models.QueryMessageIDWhere, messageID).Delete(&MessageSendRecord{}).Error
}

// UpdateStatus 更新状态
//
// 优化说明：原先通过两次 UPDATE 完成 first_send_time 的条件更新，在高并发 ACK 确认场景下
// 会产生瞬时双倍磁盘写入 I/O现合并为单条 UPDATE，使用 CASE WHEN 表达式在数据库侧
// 完成「仅在 first_send_time 为 NULL 时才写入」的条件更新，I/O 开销减半
func (r *MessageRecordGormRepository) UpdateStatus(ctx context.Context, messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) error {
	now := time.Now()

	updates := map[string]interface{}{
		"status":         status,
		"last_send_time": &now,
		// 使用 CASE WHEN 条件更新：仅在 first_send_time 为 NULL 时设置为当前时间，
		// 否则保持原值不变避免额外的 UPDATE 查询带来的磁盘 I/O 开销
		"first_send_time": gorm.Expr("CASE WHEN first_send_time IS NULL THEN ? ELSE first_send_time END", now),
	}

	// 设置失败原因和错误信息
	if reason != "" {
		updates["failure_reason"] = reason
	}
	if errorMsg != "" {
		updates["error_message"] = errorMsg
	}

	// 🔥 如果发送成功,设置成功时间
	if status == models.MessageSendStatusSuccess {
		updates["success_time"] = &now
	}

	// 单次 UPDATE 完成所有字段更新（含 first_send_time 的条件更新）
	result := r.db.WithContext(ctx).Model(&MessageSendRecord{}).
		Where(models.QueryMessageIDWhere, messageID).
		Updates(updates)

	// 🔥 如果没有找到记录（RowsAffected == 0），静默返回（记录可能尚未创建或不需要记录）
	if result.Error != nil {
		return result.Error
	}
	return nil
}

// BatchUpdateStatus 批量更新消息状态
func (r *MessageRecordGormRepository) BatchUpdateStatus(ctx context.Context, messageIDs []string, status MessageSendStatus, reason FailureReason, errorMsg string) error {
	if len(messageIDs) == 0 {
		return nil
	}

	now := time.Now()

	updates := map[string]interface{}{
		"status":          status,
		"last_send_time":  &now,
		"first_send_time": gorm.Expr("CASE WHEN first_send_time IS NULL THEN ? ELSE first_send_time END", now),
	}

	if reason != "" {
		updates["failure_reason"] = reason
	}
	if errorMsg != "" {
		updates["error_message"] = errorMsg
	}

	if status == models.MessageSendStatusSuccess {
		updates["success_time"] = &now
	}

	result := r.db.WithContext(ctx).Model(&MessageSendRecord{}).
		Where("message_id IN ?", messageIDs).
		Updates(updates)

	return result.Error
}

// ClaimStaleSending 原子认领超时的 sending 记录（接口说明见 MessageRecordRepository）
//
// 实现说明：逐条带状态守卫的 UPDATE（WHERE message_id = ? AND status = 'sending'），
// RowsAffected==1 即认领成功。虽是逐条更新（单轮扫描上限 200 条，30s 一次的后台任务），
// 但这是唯一能在多节点并发下精确判定"哪条记录被哪个节点认领"的方式——
// 单条批量 UPDATE 只能返回总命中行数，无法区分每条的认领归属
func (r *MessageRecordGormRepository) ClaimStaleSending(ctx context.Context, messageIDs []string, newStatus MessageSendStatus, reason FailureReason, errorMsg string) ([]string, error) {
	if len(messageIDs) == 0 {
		return nil, nil
	}

	now := time.Now()
	claimed := make([]string, 0, len(messageIDs))

	for _, messageID := range messageIDs {
		updates := map[string]interface{}{
			"status":          newStatus,
			"last_send_time":  &now,
			"first_send_time": gorm.Expr("CASE WHEN first_send_time IS NULL THEN ? ELSE first_send_time END", now),
		}
		if reason != "" {
			updates["failure_reason"] = reason
		}
		if errorMsg != "" {
			updates["error_message"] = errorMsg
		}

		result := r.db.WithContext(ctx).Model(&MessageSendRecord{}).
			Where(models.QueryMessageIDWhere+" AND status = ?", messageID, models.MessageSendStatusSending).
			Updates(updates)
		if result.Error != nil {
			return claimed, result.Error
		}
		if result.RowsAffected > 0 {
			claimed = append(claimed, messageID)
		}
	}

	return claimed, nil
}

// IncrementRetry 增加重试次数
//
// 优化说明：原实现 SELECT + UPDATE 两次数据库往返，且 SELECT 读取整行数据仅为
// 获取 retry_history/first_send_time/max_retry现合并为单条 UPDATE：
//   - retry_history 使用方言感知的 JSON 数组追加（MySQL: JSON_ARRAY_APPEND / SQLite: json_insert / PG: || ）
//   - first_send_time/status/success_time/failure_reason 全部用 CASE WHEN 条件更新
//   - 状态判定依据 attempt.Success 与 retry_count vs max_retry 列
//
// 数据库往返从 2 次降为 1 次，消除 SELECT 整行读取与 Go 侧 retry_history 反序列化
func (r *MessageRecordGormRepository) IncrementRetry(ctx context.Context, messageID string, attempt RetryAttempt) error {
	now := time.Now()

	// 序列化重试记录为 JSON，用于方言感知的 JSON 数组追加
	attemptJSON, err := json.Marshal(attempt)
	if err != nil {
		return fmt.Errorf("序列化重试记录失败: %w", err)
	}

	// 布尔转 0/1 供 SQL CASE WHEN 判定（避免驱动对 bool 参数的歧义）
	successFlag := 0
	if attempt.Success {
		successFlag = 1
	}
	hasError := 0
	if attempt.Error != "" {
		hasError = 1
	}

	// 通过 Dialect 引擎兼容 MySQL/SQLite/PostgreSQL 的 JSON 数组追加语法
	dialect := sqlbuilder.DetectDialect(r.db)
	retryHistoryExpr := dialect.JsonArrayAppend("retry_history", "?")

	// 单条 UPDATE 完成所有更新，WHERE message_id = ? 与 UpdateStatus 保持一致
	result := r.db.WithContext(ctx).Exec(
		`UPDATE `+MessageSendRecord{}.TableName()+` SET
			retry_count = ?,
			retry_history = `+retryHistoryExpr+`,
			last_send_time = ?,
			first_send_time = CASE WHEN first_send_time IS NULL THEN ? ELSE first_send_time END,
			status = CASE
				WHEN ? = 1 THEN ?
				WHEN ? >= max_retry THEN ?
				ELSE ?
			END,
			success_time = CASE WHEN ? = 1 THEN ? ELSE success_time END,
			failure_reason = CASE WHEN ? = 0 AND ? >= max_retry THEN ? ELSE failure_reason END,
			error_message = CASE WHEN ? = 0 AND ? = 1 THEN ? ELSE error_message END,
			updated_at = ?
		WHERE message_id = ?`,
		attempt.AttemptNumber,
		string(attemptJSON),
		now,
		now,
		successFlag, models.MessageSendStatusSuccess,
		attempt.AttemptNumber, models.MessageSendStatusFailed,
		models.MessageSendStatusRetrying,
		successFlag, now,
		successFlag, attempt.AttemptNumber, models.FailureReasonMaxRetry,
		successFlag, hasError, attempt.Error,
		now,
		messageID,
	)

	return result.Error
}

// GetStatistics 获取统计信息
//
// 优化说明：原实现先做 1 次总数 COUNT，再对 8 种状态各做 1 次 COUNT，共 9 次数据库往返
// 现合并为单条 GROUP BY 查询，数据库往返从 9 次降为 1 次
// 所有状态预初始化为 0，保证返回结构与原实现一致（未出现的状态也返回 0）
func (r *MessageRecordGormRepository) GetStatistics(ctx context.Context) (map[string]int64, error) {
	stats := make(map[string]int64)

	// 预初始化所有状态为 0，保持与原实现一致的返回结构
	statuses := []MessageSendStatus{
		models.MessageSendStatusPending,
		models.MessageSendStatusSending,
		models.MessageSendStatusSuccess,
		models.MessageSendStatusFailed,
		models.MessageSendStatusRetrying,
		models.MessageSendStatusAckTimeout,
		models.MessageSendStatusUserOffline,
		models.MessageSendStatusExpired,
	}
	for _, s := range statuses {
		stats[string(s)] = 0
	}

	// 单条 GROUP BY 查询替代 9 次独立 COUNT 查询
	type statusCount struct {
		Status string
		Count  int64
	}
	var results []statusCount

	err := r.db.WithContext(ctx).
		Model(&MessageSendRecord{}).
		Select("status as status, COUNT(*) as count").
		Group("status").
		Scan(&results).Error
	if err != nil {
		return nil, err
	}

	var total int64
	for _, sc := range results {
		stats[sc.Status] = sc.Count
		total += sc.Count
	}
	stats["total"] = total

	return stats, nil
}

// CleanupOld 清理旧记录
func (r *MessageRecordGormRepository) CleanupOld(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Where("create_time < ? AND status IN ?", before, []MessageSendStatus{
		models.MessageSendStatusSuccess,
		models.MessageSendStatusFailed,
		models.MessageSendStatusExpired,
	}).Delete(&MessageSendRecord{})

	return result.RowsAffected, result.Error
}

// GetDB 获取底层 GORM DB
func (r *MessageRecordGormRepository) GetDB() *gorm.DB {
	return r.db
}

// startCleanupScheduler 启动定时清理任务
func (r *MessageRecordGormRepository) startCleanupScheduler(ctx context.Context, daysAgo int) {
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
			r.logger.Errorf("⚠️ 消息发送记录清理任务 panic: %v, stack: %s", rec, debug.Stack())
		}).
		// 优雅关闭
		OnShutdown(func() {
			r.logger.Info("🛑 消息发送记录清理任务已停止")
		}).
		Run()
}

// cleanupOldData 清理N天前的历史数据
func (r *MessageRecordGormRepository) cleanupOldData(ctx context.Context, daysAgo int) {
	if daysAgo <= 0 {
		return
	}

	before := time.Now().AddDate(0, 0, -daysAgo)

	// 清理旧记录
	deleted, err := r.CleanupOld(ctx, before)
	if err != nil {
		r.logger.Warnf("⚠️ 清理历史消息发送记录失败: %v", err)
	} else if deleted > 0 {
		r.logger.Infof("🧹 已清理 %d 天前的历史消息发送记录，删除 %d 条", daysAgo, deleted)
	}
}

// Close 关闭仓库，停止后台清理任务
func (r *MessageRecordGormRepository) Close() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
		r.logger.Info("🛑 MessageRecordRepository 已关闭")
	}
	return nil
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

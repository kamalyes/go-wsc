/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\adapter\gorm\message_sink.go
 * @Description: 消息发送记录管理 - 使用 GORM 数据库持久化
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
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"gorm.io/gorm"
)

// MessageRecordFilter 已迁至 models/contract.go —— 它是 spi.MessageSink 接口
// 签名的一部分，接口层不应反向依赖本包。

// MessageSink GORM 消息记录仓储，实现 spi.MessageSink 契约
type MessageSink struct {
	db         *gorm.DB
	logger     logger.ILogger
	cancelFunc context.CancelFunc
}

// NewMessageSink 创建消息记录仓库
// 参数:
//   - db: GORM 数据库实例
//   - config: 消息记录配置对象（可选，传 nil 则不启用自动清理）
//   - log: 日志记录器
func NewMessageSink(db *gorm.DB, config *wscconfig.MessageRecord, log logger.ILogger) *MessageSink {
	ctx, cancel := context.WithCancel(context.Background())

	repo := &MessageSink{
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

// CreateBatch 批量创建消息发送记录（outbox 攒批 flush，一条 INSERT 写整批）
// 唯一写入路径：单条 Create 已随攒批化移除，写入一律经 MessageRecordOutbox
func (r *MessageSink) CreateBatch(ctx context.Context, records []*models.MessageSendRecord) error {
	if len(records) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).CreateInBatches(records, len(records)).Error
}

// FindByID 根据ID查找
func (r *MessageSink) FindByID(ctx context.Context, id uint) (*models.MessageSendRecord, error) {
	var record models.MessageSendRecord
	err := r.db.WithContext(ctx).First(&record, id).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// FindByMessageID 根据消息ID+接收者查找
func (r *MessageSink) FindByMessageID(ctx context.Context, key models.MessageRecordKey) (*models.MessageSendRecord, error) {
	var record models.MessageSendRecord
	err := r.db.WithContext(ctx).Where(models.QueryMessageIDReceiverWhere, key.MessageID, key.Receiver).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// QueryRecords 查询消息记录（支持按状态、发送者、接收者、节点IP、客户端IP等条件过滤）
func (r *MessageSink) QueryRecords(ctx context.Context, filter *models.MessageRecordFilter) ([]*models.MessageSendRecord, error) {
	var records []*models.MessageSendRecord

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
func (r *MessageSink) FindRetryable(ctx context.Context, limit int) ([]*models.MessageSendRecord, error) {
	var records []*models.MessageSendRecord
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
func (r *MessageSink) DeleteExpired(ctx context.Context) (int64, error) {
	now := time.Now()
	result := r.db.WithContext(ctx).Where("expires_at IS NOT NULL AND expires_at < ?", now).Delete(&models.MessageSendRecord{})
	return result.RowsAffected, result.Error
}

// Delete 删除记录
func (r *MessageSink) Delete(ctx context.Context, id uint) error {
	return r.db.WithContext(ctx).Delete(&models.MessageSendRecord{}, id).Error
}

// DeleteByMessageID 根据消息ID删除
func (r *MessageSink) DeleteByMessageID(ctx context.Context, messageID string) error {
	return r.db.WithContext(ctx).Where(models.QueryMessageIDWhere, messageID).Delete(&models.MessageSendRecord{}).Error
}

// BatchUpdateStatus 批量更新消息状态（按 (message_id, receiver) 复合键批量定位）
// 状态更新唯一路径：单条 UpdateStatus 已随攒批化移除（MessageStatusUpdater 批量调用本方法）
func (r *MessageSink) BatchUpdateStatus(ctx context.Context, keys []models.MessageRecordKey, status models.MessageSendStatus, reason models.FailureReason, errorMsg string) error {
	if len(keys) == 0 {
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

	// 行值 IN：(message_id, receiver) IN ((?,?),(?,?),...)，复合索引 idx_message_id_receiver 可命中
	tuples := make([][]interface{}, len(keys))
	for i, key := range keys {
		tuples[i] = []interface{}{key.MessageID, key.Receiver}
	}

	return execWithSQLRetry(ctx, func() error {
		return r.db.WithContext(ctx).Model(&models.MessageSendRecord{}).
			Where("(message_id, receiver) IN ?", tuples).
			Updates(updates).Error
	})
}

// ClaimStaleSending 原子认领超时的 sending 记录（接口说明见 spi.MessageSink）
//
// 实现说明：逐条带状态守卫的 UPDATE（WHERE message_id = ? AND receiver = ? AND status = 'sending'），
// RowsAffected==1 即认领成功。虽是逐条更新（单轮扫描上限 200 条，30s 一次的后台任务），
// 但这是唯一能在多节点并发下精确判定"哪条记录被哪个节点认领"的方式——
// 单条批量 UPDATE 只能返回总命中行数，无法区分每条的认领归属
func (r *MessageSink) ClaimStaleSending(ctx context.Context, keys []models.MessageRecordKey, newStatus models.MessageSendStatus, reason models.FailureReason, errorMsg string) ([]models.MessageRecordKey, error) {
	if len(keys) == 0 {
		return nil, nil
	}

	now := time.Now()
	claimed := make([]models.MessageRecordKey, 0, len(keys))

	for _, key := range keys {
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

		// CRDB Serializable 下与目标节点状态回报并发更新同一行时（40001）自动重试；
		// RowsAffected 经由闭包外变量传递，避免重试后重复 append 认领键
		var affected int64
		err := execWithSQLRetry(ctx, func() error {
			result := r.db.WithContext(ctx).Model(&models.MessageSendRecord{}).
				Where(models.QueryMessageIDReceiverWhere+" AND status = ?", key.MessageID, key.Receiver, models.MessageSendStatusSending).
				Updates(updates)
			affected = result.RowsAffected
			return result.Error
		})
		if err != nil {
			return claimed, err
		}
		if affected > 0 {
			claimed = append(claimed, key)
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
func (r *MessageSink) IncrementRetry(ctx context.Context, key models.MessageRecordKey, attempt models.RetryAttempt) error {
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

	// 单条 UPDATE 完成所有更新，WHERE message_id = ? AND receiver = ? 与 UpdateStatus 保持一致
	// CRDB Serializable 下与 ACK 状态回报并发更新同一行时（40001）自动重试
	return execWithSQLRetry(ctx, func() error {
		return r.db.WithContext(ctx).Exec(
			`UPDATE `+models.MessageSendRecord{}.TableName()+` SET
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
			WHERE message_id = ? AND receiver = ?`,
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
			key.MessageID, key.Receiver,
		).Error
	})
}

// GetStatistics 获取统计信息
//
// 优化说明：原实现先做 1 次总数 COUNT，再对 8 种状态各做 1 次 COUNT，共 9 次数据库往返
// 现合并为单条 GROUP BY 查询，数据库往返从 9 次降为 1 次
// 所有状态预初始化为 0，保证返回结构与原实现一致（未出现的状态也返回 0）
func (r *MessageSink) GetStatistics(ctx context.Context) (map[string]int64, error) {
	stats := make(map[string]int64)

	// 预初始化所有状态为 0，保持与原实现一致的返回结构
	statuses := []models.MessageSendStatus{
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
		Model(&models.MessageSendRecord{}).
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
func (r *MessageSink) CleanupOld(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Where("create_time < ? AND status IN ?", before, []models.MessageSendStatus{
		models.MessageSendStatusSuccess,
		models.MessageSendStatusFailed,
		models.MessageSendStatusExpired,
	}).Delete(&models.MessageSendRecord{})

	return result.RowsAffected, result.Error
}

// GetDB 获取底层 GORM DB
func (r *MessageSink) GetDB() *gorm.DB {
	return r.db
}

// startCleanupScheduler 启动定时清理任务
func (r *MessageSink) startCleanupScheduler(ctx context.Context, daysAgo int) {
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
func (r *MessageSink) cleanupOldData(ctx context.Context, daysAgo int) {
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
func (r *MessageSink) Close() error {
	if r.cancelFunc != nil {
		r.cancelFunc()
		r.logger.Info("🛑 MessageSink 已关闭")
	}
	return nil
}

// MessageRecordHooks 消息记录钩子函数接口
type MessageRecordHooks interface {
	// OnRecordCreated 记录创建时调用
	OnRecordCreated(record *models.MessageSendRecord) error

	// OnRecordUpdated 记录更新时调用
	OnRecordUpdated(record *models.MessageSendRecord, oldStatus models.MessageSendStatus, newStatus models.MessageSendStatus) error

	// OnRetryAttempt 重试尝试时调用
	OnRetryAttempt(record *models.MessageSendRecord, attempt *models.RetryAttempt) error

	// OnRecordDeleted 记录删除前调用
	OnRecordDeleted(record *models.MessageSendRecord) error

	// OnRecordExpired 记录过期时调用
	OnRecordExpired(record *models.MessageSendRecord) error
}

// 编译期断言：repository 实现必须满足 spi 契约（Phase 4 迁仓后适配器同样受此约束）
var _ spi.MessageSink = (*MessageSink)(nil)

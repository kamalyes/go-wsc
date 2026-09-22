/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 10:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 10:00:00
 * @FilePath: \go-wsc\spi\message_sink.go
 * @Description: 消息归档与统计 SPI - MessageSink 接口契约定义
 *
 * 消息发送记录持久化与 Hub 统计信息的统一契约
 * GORM 实现见 adapter/gorm 包 MessageSink，Redis 统计见 adapter/redis
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// MessageSink 消息归档与统计接口
//
// 职责：
//  1. MessageRecord：消息发送记录的 CRUD + 批量更新 + 统计
//  2. HubStats：节点/集群级别的连接与消息统计
//
// 存储维度：
//   - MessageRecord：按 message_id（全局唯一）主键，支持按 sender/receiver/node_ip/client_ip/status 过滤
//   - HubStats：按 nodeID 隔离，key = "{prefix}node:{nodeID}"，全局汇总 key = "{prefix}nodes"
type MessageSink interface {
	// ========== 消息发送记录（MessageRecord） ==========

	// Create 创建消息发送记录
	Create(ctx context.Context, record *models.MessageSendRecord) error

	// Update 更新消息发送记录
	Update(ctx context.Context, record *models.MessageSendRecord) error

	// FindByID 根据自增ID查找
	FindByID(ctx context.Context, id uint) (*models.MessageSendRecord, error)

	// FindByMessageID 根据消息ID+接收者复合键查找（P2P 同一 message_id 为每个 receiver 各建一条记录）
	FindByMessageID(ctx context.Context, key models.MessageRecordKey) (*models.MessageSendRecord, error)

	// QueryRecords 查询消息记录（支持按状态、发送者、接收者、节点IP、客户端IP等条件过滤）
	QueryRecords(ctx context.Context, filter *models.MessageRecordFilter) ([]*models.MessageSendRecord, error)

	// FindRetryable 查找可重试的记录
	FindRetryable(ctx context.Context, limit int) ([]*models.MessageSendRecord, error)

	// DeleteExpired 删除过期的记录
	DeleteExpired(ctx context.Context) (int64, error)

	// Delete 删除记录
	Delete(ctx context.Context, id uint) error

	// DeleteByMessageID 根据消息ID删除
	DeleteByMessageID(ctx context.Context, messageID string) error

	// UpdateStatus 更新状态（按 message_id + receiver 复合键精确定位，多 receiver 记录互不影响）
	UpdateStatus(ctx context.Context, key models.MessageRecordKey, status models.MessageSendStatus, reason models.FailureReason, errorMsg string) error

	// BatchUpdateStatus 批量更新消息状态（按复合键批量定位；相同 status/reason/errorMsg 用一条 SQL）
	BatchUpdateStatus(ctx context.Context, keys []models.MessageRecordKey, status models.MessageSendStatus, reason models.FailureReason, errorMsg string) error

	// ClaimStaleSending 原子认领超时的 sending 记录（仅当状态仍为 sending 时按复合键更新）
	// 返回实际认领成功的复合键列表（状态已被并发方更新则不认领）
	ClaimStaleSending(ctx context.Context, keys []models.MessageRecordKey, newStatus models.MessageSendStatus, reason models.FailureReason, errorMsg string) ([]models.MessageRecordKey, error)

	// IncrementRetry 增加重试次数（按复合键定位单条记录）
	IncrementRetry(ctx context.Context, key models.MessageRecordKey, attempt models.RetryAttempt) error

	// GetStatistics 获取统计信息
	GetStatistics(ctx context.Context) (map[string]int64, error)

	// CleanupOld 清理旧记录
	CleanupOld(ctx context.Context, before time.Time) (int64, error)

	// Close 关闭仓库，停止后台任务
	Close() error
}

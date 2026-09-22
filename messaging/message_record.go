/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-31 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-31 00:00:00
 * @FilePath: \go-wsc\messaging\message_record.go
 * @Description: Hub 消息记录查询和管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 消息记录查询接口
// ============================================================================

// QueryMessageRecord 根据消息ID+接收者查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - messageID: 消息ID
//   - receiver: 接收者ID（广播类记录传空字符串）
//
// 返回:
//   - *models.MessageSendRecord: 消息记录
//   - error: 错误信息
func (m *Manager) QueryMessageRecord(ctx context.Context, messageID, receiver string) (*models.MessageSendRecord, error) {
	if m.host.GetMessageSink() == nil {
		return nil, models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().FindByMessageID(ctx, models.MessageRecordKey{MessageID: messageID, Receiver: receiver})
}

// QueryMessageRecordsBySender 根据发送者查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - sender: 发送者ID
//   - limit: 返回结果数量限制（0 表示不限制）
func (m *Manager) QueryMessageRecordsBySender(ctx context.Context, sender string, limit int) ([]*models.MessageSendRecord, error) {
	if m.host.GetMessageSink() == nil {
		return nil, models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().QueryRecords(ctx, &models.MessageRecordFilter{Sender: sender, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByReceiver 根据接收者查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - receiver: 接收者ID
//   - limit: 返回结果数量限制（0 表示不限制）
func (m *Manager) QueryMessageRecordsByReceiver(ctx context.Context, receiver string, limit int) ([]*models.MessageSendRecord, error) {
	if m.host.GetMessageSink() == nil {
		return nil, models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().QueryRecords(ctx, &models.MessageRecordFilter{Receiver: receiver, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByNodeIP 根据节点IP查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - nodeIP: 服务器节点IP
//   - limit: 返回结果数量限制（0 表示不限制）
func (m *Manager) QueryMessageRecordsByNodeIP(ctx context.Context, nodeIP string, limit int) ([]*models.MessageSendRecord, error) {
	if m.host.GetMessageSink() == nil {
		return nil, models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().QueryRecords(ctx, &models.MessageRecordFilter{NodeIP: nodeIP, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByClientIP 根据客户端IP查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - clientIP: 客户端IP地址
//   - limit: 返回结果数量限制（0 表示不限制）
func (m *Manager) QueryMessageRecordsByClientIP(ctx context.Context, clientIP string, limit int) ([]*models.MessageSendRecord, error) {
	if m.host.GetMessageSink() == nil {
		return nil, models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().QueryRecords(ctx, &models.MessageRecordFilter{ClientIP: clientIP, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByStatus 根据状态查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - status: 消息状态
//   - limit: 返回结果数量限制（0 表示不限制）
func (m *Manager) QueryMessageRecordsByStatus(ctx context.Context, status models.MessageSendStatus, limit int) ([]*models.MessageSendRecord, error) {
	if m.host.GetMessageSink() == nil {
		return nil, models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().QueryRecords(ctx, &models.MessageRecordFilter{Status: &status, Limit: limit, OrderDesc: true})
}

// QueryRetryableMessageRecords 查询可重试的消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - limit: 返回结果数量限制（0 表示不限制）
func (m *Manager) QueryRetryableMessageRecords(ctx context.Context, limit int) ([]*models.MessageSendRecord, error) {
	if m.host.GetMessageSink() == nil {
		return nil, models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().FindRetryable(ctx, limit)
}

// ============================================================================
// 消息记录删除接口
// ============================================================================

// DeleteMessageRecord 删除消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - id: 记录ID
func (m *Manager) DeleteMessageRecord(ctx context.Context, id uint) error {
	if m.host.GetMessageSink() == nil {
		return models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().Delete(ctx, id)
}

// DeleteMessageRecordByMessageID 根据消息ID删除消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - messageID: 消息ID
func (m *Manager) DeleteMessageRecordByMessageID(ctx context.Context, messageID string) error {
	if m.host.GetMessageSink() == nil {
		return models.ErrRecordRepositoryNotSet
	}
	return m.host.GetMessageSink().DeleteByMessageID(ctx, messageID)
}

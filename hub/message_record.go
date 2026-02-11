/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-31 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-31 00:00:00
 * @FilePath: \go-wsc\hub\message_record.go
 * @Description: Hub 消息记录查询和管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import "context"

// ============================================================================
// 消息记录查询接口
// ============================================================================

// QueryMessageRecord 根据消息ID查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - messageID: 消息ID
//
// 返回:
//   - *MessageSendRecord: 消息记录
//   - error: 错误信息
func (h *Hub) QueryMessageRecord(ctx context.Context, messageID string) (*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindByMessageID(ctx, messageID)
}

// QueryMessageRecordsBySender 根据发送者查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - sender: 发送者ID
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsBySender(ctx context.Context, sender string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.QueryRecords(ctx, &MessageRecordFilter{Sender: sender, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByReceiver 根据接收者查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - receiver: 接收者ID
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByReceiver(ctx context.Context, receiver string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.QueryRecords(ctx, &MessageRecordFilter{Receiver: receiver, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByNodeIP 根据节点IP查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - nodeIP: 服务器节点IP
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByNodeIP(ctx context.Context, nodeIP string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.QueryRecords(ctx, &MessageRecordFilter{NodeIP: nodeIP, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByClientIP 根据客户端IP查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - clientIP: 客户端IP地址
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByClientIP(ctx context.Context, clientIP string, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.QueryRecords(ctx, &MessageRecordFilter{ClientIP: clientIP, Limit: limit, OrderDesc: true})
}

// QueryMessageRecordsByStatus 根据状态查询消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - status: 消息状态
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryMessageRecordsByStatus(ctx context.Context, status MessageSendStatus, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.QueryRecords(ctx, &MessageRecordFilter{Status: &status, Limit: limit, OrderDesc: true})
}

// QueryRetryableMessageRecords 查询可重试的消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - limit: 返回结果数量限制（0 表示不限制）
func (h *Hub) QueryRetryableMessageRecords(ctx context.Context, limit int) ([]*MessageSendRecord, error) {
	if h.messageRecordRepo == nil {
		return nil, ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.FindRetryable(ctx, limit)
}

// ============================================================================
// 消息记录更新接口
// ============================================================================

// UpdateMessageRecordStatus 更新消息记录状态
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - messageID: 消息ID
//   - status: 新状态
//   - reason: 失败原因（可选）
//   - errorMsg: 错误消息（可选）
func (h *Hub) UpdateMessageRecordStatus(ctx context.Context, messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.UpdateStatus(ctx, messageID, status, reason, errorMsg)
}

// UpdateMessageRecord 更新消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - record: 要更新的消息记录
func (h *Hub) UpdateMessageRecord(ctx context.Context, record *MessageSendRecord) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.Update(ctx, record)
}

// ============================================================================
// 消息记录删除接口
// ============================================================================

// DeleteMessageRecord 删除消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - id: 记录ID
func (h *Hub) DeleteMessageRecord(ctx context.Context, id uint) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.Delete(ctx, id)
}

// DeleteMessageRecordByMessageID 根据消息ID删除消息记录
// 参数:
//   - ctx: 上下文（用于超时控制和取消）
//   - messageID: 消息ID
func (h *Hub) DeleteMessageRecordByMessageID(ctx context.Context, messageID string) error {
	if h.messageRecordRepo == nil {
		return ErrRecordRepositoryNotSet
	}
	return h.messageRecordRepo.DeleteByMessageID(ctx, messageID)
}

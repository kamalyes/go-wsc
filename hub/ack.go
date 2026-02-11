/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\ack.go
 * @Description: Hub ACK 确认机制
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// ACK 发送方法
// ============================================================================

// SendToUserWithAck 发送消息并等待ACK确认
func (h *Hub) SendToUserWithAck(ctx context.Context, toUserID string, msg *HubMessage, timeout time.Duration, maxRetry int) (*AckMessage, error) {
	// 检查是否启用ACK
	enableAck := h.config.EnableAck

	if !enableAck {
		result := h.SendToUserWithRetry(ctx, toUserID, msg)
		return nil, result.FinalError
	}
	msg.RequireAck = true // 记录ACK发送开始
	h.logger.InfoKV("ACK消息发送开始",
		"message_id", msg.MessageID,
		"to_user", toUserID,
		"timeout", timeout,
		"max_retry", maxRetry,
		"require_ack", true,
		"enable_ack", enableAck,
	)
	// 检查用户是否在线并处理离线消息
	ackMsg, err, isOnline := h.checkUserOnlineForAck(ctx, toUserID, msg)
	if !isOnline {
		return ackMsg, err
	}

	// 添加到待确认队列
	pm := h.ackManager.AddPendingMessage(msg)
	defer h.ackManager.RemovePendingMessage(msg.MessageID)

	// 创建重试函数
	attemptNum := 0
	retryFunc := h.createAckRetryFunc(ctx, toUserID, msg, &attemptNum)

	// 首次发送
	if err := retryFunc(); err != nil {
		return &AckMessage{
			MessageID: msg.MessageID,
			Status:    AckStatusFailed,
			Timestamp: time.Now(),
			Error:     err.Error(),
		}, err
	}

	// 等待ACK确认并支持重试
	ackMsg, err = pm.WaitForAckWithRetry(retryFunc)

	return ackMsg, err
}

// HandleAck 处理ACK确认消息
func (h *Hub) HandleAck(ackMsg *AckMessage) {
	// 记录ACK消息处理
	h.logger.InfoKV("收到ACK确认",
		"message_id", ackMsg.MessageID,
		"status", ackMsg.Status,
		"timestamp", ackMsg.Timestamp,
	)

	h.ackManager.ConfirmMessage(ackMsg.MessageID, ackMsg)

	// 收到ACK确认，更新消息记录状态
	if ackMsg.Status == AckStatusConfirmed && h.messageRecordRepo != nil {
		go contextx.WithTimeoutOrBackground(h.ctx, 2*time.Second, func(ctx context.Context) error {
			return h.messageRecordRepo.UpdateStatus(ctx, ackMsg.MessageID, models.MessageSendStatusSuccess, "", "")
		})
	}
}

// ============================================================================
// ACK 辅助方法
// ============================================================================

// checkUserOnlineForAck 检查用户是否在线（用于ACK）
func (h *Hub) checkUserOnlineForAck(ctx context.Context, toUserID string, msg *HubMessage) (*AckMessage, error, bool) {
	clientMap, isOnline := h.GetUserClientsMapWithLock(toUserID)

	if !isOnline || len(clientMap) == 0 {
		return h.handleOfflineAckMessage(ctx, toUserID, msg)
	}
	return nil, nil, true
}

// handleOfflineAckMessage 处理离线用户的ACK消息
func (h *Hub) handleOfflineAckMessage(ctx context.Context, toUserID string, msg *HubMessage) (*AckMessage, error, bool) {
	if h.offlineMessageHandler != nil {
		// 存储离线消息
		if err := h.offlineMessageHandler.StoreOfflineMessage(ctx, toUserID, msg); err != nil {
			h.logger.ErrorKV("ACK消息-存储离线消息失败",
				"message_id", msg.MessageID,
				"user_id", toUserID,
				"error", err,
			)
		} else {
			h.logger.InfoKV("ACK消息-用户离线，已存储离线消息",
				"message_id", msg.MessageID,
				"user_id", toUserID,
			)
		}
		return &AckMessage{
			MessageID: msg.MessageID,
			Status:    AckStatusConfirmed,
			Timestamp: time.Now(),
			Error:     "用户离线，消息已存储，将在用户上线时推送",
		}, nil, false
	}

	err := errorx.NewError(ErrTypeUserOffline, toUserID)

	return &AckMessage{
		MessageID: msg.MessageID,
		Status:    AckStatusFailed,
		Timestamp: time.Now(),
		Error:     "用户离线且未配置离线消息处理器",
	}, err, false
}

// createAckRetryFunc 创建ACK重试函数
func (h *Hub) createAckRetryFunc(ctx context.Context, toUserID string, msg *HubMessage, attemptNum *int) func() error {
	return func() error {
		*attemptNum++
		err := h.sendToUser(ctx, toUserID, msg)

		// 记录重试尝试
		if *attemptNum > 1 && h.messageRecordRepo != nil {
			h.recordAckRetryAttempt(msg.MessageID, *attemptNum, err)
		}

		return err
	}
}

// recordAckRetryAttempt 记录ACK重试尝试到数据库
func (h *Hub) recordAckRetryAttempt(messageID string, attemptNum int, err error) {
	retryAttempt := RetryAttempt{
		AttemptNumber: attemptNum,
		Timestamp:     time.Now(),
		Duration:      0,
		Success:       err == nil,
	}
	if err != nil {
		retryAttempt.Error = err.Error()
	}

	syncx.Go().
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("ACK重试记录更新崩溃", "panic", r, "message_id", messageID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.messageRecordRepo.IncrementRetry(ctx, messageID, retryAttempt)
		})
}

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
	"runtime/debug"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// ACK 发送方法
// ============================================================================

// SendToUserWithAck 发送消息并等待ACK确认
func (h *Hub) SendToUserWithAck(ctx context.Context, toUserID string, msg *HubMessage, timeout time.Duration, maxRetry int) (*AckMessage, error) {
	// 🔏 P2P 严格场景：先 EnsureRouteDefaults 归一化 namespace，再 InjectRoute（与 SendToUserWithRetry 一致）
	ctx = routing.EnsureRouteDefaults(ctx)
	ctx = msg.InjectRoute(ctx)

	// 检查是否启用ACK
	enableAck := h.config.EnableAck

	if !enableAck {
		result := h.SendToUserWithRetry(ctx, toUserID, msg)
		return nil, result.FinalError
	}
	msg.RequireAck = true // 记录ACK发送开始
	h.logger.InfoContextKV(ctx, "ACK消息发送开始",
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
	// 🔗 trace 恢复：AckMessage 协议结构不带 trace_id，从 ackManager 的 pending 消息
	// （原始 HubMessage，信封携带 trace_id）恢复 ctx，使 ACK 确认与消息发送链路同一 trace 可查
	ctx := h.ctx
	if pm, ok := h.ackManager.GetPendingMessage(ackMsg.MessageID); ok && pm.Message != nil {
		ctx = pm.Message.ContextFrom(h.ctx)
	}

	// 记录ACK消息处理
	h.logger.InfoContextKV(ctx, "收到ACK确认",
		"message_id", ackMsg.MessageID,
		"status", ackMsg.Status,
		"timestamp", ackMsg.Timestamp,
	)

	h.ackManager.ConfirmMessage(ackMsg.MessageID, ackMsg)

	// 收到ACK确认，更新消息记录状态
	if ackMsg.Status == AckStatusConfirmed && h.messageRecordRepo != nil {
		// ⏰ O(1) 取消跨节点 ACK 超时任务（状态由 sending→success，避免冗余超时检查）
		// 与 updateMessageStatusAsync 的取消语义对齐，详见 ack_timer.go
		h.cancelAckTimeout(ackMsg.MessageID)
		go func() {
			updateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			if err := h.messageRecordRepo.UpdateStatus(updateCtx, ackMsg.MessageID, models.MessageSendStatusSuccess, "", ""); err != nil {
				h.logger.WarnContextKV(ctx, "ACK确认后更新消息状态失败",
					"message_id", ackMsg.MessageID, "error", err)
			}
		}()
	}
}

// ============================================================================
// ACK 辅助方法
// ============================================================================

// checkUserOnlineForAck 检查用户是否在线（用于ACK，按 ctx 路由信封 appID+namespace 隔离）
// 使用 HasUser O(1) 原子检查，替代 GetUserClientsMapWithLock 锁外 len() 读的数据竞争
func (h *Hub) checkUserOnlineForAck(ctx context.Context, toUserID string, msg *HubMessage) (*AckMessage, error, bool) {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	if !h.shardedRegistry.HasUser(toUserID, appID, ns) {
		return h.handleOfflineAckMessage(ctx, toUserID, msg)
	}
	return nil, nil, true
}

// handleOfflineAckMessage 处理离线用户的ACK消息
func (h *Hub) handleOfflineAckMessage(ctx context.Context, toUserID string, msg *HubMessage) (*AckMessage, error, bool) {
	if h.offlineMessageHandler != nil {
		// 存储离线消息
		if err := h.offlineMessageHandler.StoreOfflineMessage(ctx, toUserID, msg); err != nil {
			h.logger.ErrorContextKV(ctx, "ACK消息-存储离线消息失败",
				"message_id", msg.MessageID,
				"user_id", toUserID,
				"error", err,
			)
		} else {
			h.logger.InfoContextKV(ctx, "ACK消息-用户离线，已存储离线消息",
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
			h.recordAckRetryAttempt(ctx, msg, *attemptNum, err)
		}

		return err
	}
}

// recordAckRetryAttempt 记录ACK重试尝试到数据库
func (h *Hub) recordAckRetryAttempt(ctx context.Context, msg *HubMessage, attemptNum int, err error) {
	// nil guard：方法允许直调（测试/外部复用），repo 未注入时静默跳过而非 goroutine 内 panic
	if h.messageRecordRepo == nil || msg == nil {
		return
	}

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
			h.logger.ErrorContextKV(ctx, "ACK重试记录更新崩溃",
				"panic", r, "stack", string(debug.Stack()), "message_id", msg.MessageID)
		}).
		ExecWithContext(func(retryCtx context.Context) error {
			retryCtx = msg.ContextFrom(retryCtx)
			return h.messageRecordRepo.IncrementRetry(retryCtx, msg.MessageID, retryAttempt)
		})
}

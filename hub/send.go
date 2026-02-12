/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 23:08:11
 * @FilePath: \go-wsc\hub\send.go
 * @Description: Hub 消息发送功能
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/retry"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 基础发送方法
// ============================================================================

// ContextKey 上下文键类型
type ContextKey string

const (
	ContextKeyUserID   ContextKey = "user_id"
	ContextKeySenderID ContextKey = "sender_id"
)

// sendToUser 发送消息给指定用户（内部方法）
// 自动支持分布式：如果用户在其他节点，会自动路由过去
func (h *Hub) sendToUser(ctx context.Context, toUserID string, msg *HubMessage) error {
	// 克隆消息以避免并发修改原始消息（特别是在重试场景中）
	msgCopy := msg.Clone()
	msgCopy.ReceiverNode = mathx.IfEmpty(msgCopy.ReceiverNode, h.nodeID)
	msgCopy.CreateAt = mathx.IfNotZero(msgCopy.CreateAt, time.Now())

	// 🌐 分布式路由：检查用户是否在其他节点
	routed, err := h.checkAndRouteToNode(ctx, toUserID, msgCopy)
	if err != nil {
		// 路由失败，记录错误但继续尝试本地发送
		h.logger.WarnKV("跨节点路由失败，尝试本地发送",
			"user_id", toUserID,
			"message_id", msgCopy.MessageID,
			"error", err,
		)
	}
	if routed {
		// 消息已路由到其他节点，本地不需要处理
		h.logger.DebugContextKV(ctx, "消息已路由到其他节点",
			"message_id", msgCopy.MessageID,
			"user_id", toUserID,
		)
		go h.recordMessageToDatabase(msgCopy, nil)
		return nil
	}
	// 用户在本节点或单机模式，正常发送
	// 尝试发送到broadcast队列
	select {
	case h.broadcast <- msgCopy:
		h.logger.DebugContextKV(ctx, "消息已广播", "message_id", msgCopy.MessageID, "from", msgCopy.Sender, "to", msgCopy.Receiver, "type", msgCopy.MessageType)
		// 记录消息到数据库 - 创建时已标记为Sending状态
		go h.recordMessageToDatabase(msgCopy, nil)
		return nil
	default:
		// broadcast队列满，尝试放入待发送队列
		select {
		case h.pendingMessages <- msgCopy:
			h.logger.DebugContextKV(ctx, "消息已放入待发送队列", "message_id", msgCopy.MessageID, "from", msgCopy.Sender, "to", msgCopy.Receiver, "type", msgCopy.MessageType)
			// 记录消息到数据库 - 创建时已标记为Sending状态
			go h.recordMessageToDatabase(msgCopy, nil)
			return nil
		default:
			err := ErrQueueAndPendingFull
			// 记录消息发送失败日志
			h.logger.DebugContextKV(ctx, "消息发送失败", "message_id", msgCopy.MessageID, "from", msgCopy.Sender, "to", msgCopy.Receiver, "type", msgCopy.MessageType, "error", err)
			// 记录失败消息到数据库
			go h.recordMessageToDatabase(msgCopy, err)
			// 通知队列满处理器
			h.notifyQueueFull(msgCopy, toUserID, QueueTypeAllQueues, err)
			return err
		}
	}
}

// ============================================================================
// 重试发送方法
// ============================================================================

// SendToUserWithRetry 带重试机制的发送消息给指定用户
func (h *Hub) SendToUserWithRetry(ctx context.Context, toUserID string, msg *HubMessage) *SendResult {
	result := &SendResult{
		Attempts: make([]SendAttempt, 0, h.config.RetryPolicy.MaxRetries+1),
	}

	startTime := time.Now()

	// 立即创建消息副本，避免并发修改原始消息
	msg = msg.Clone()

	// 修改副本对象
	if msg.Sender == "" {
		if senderID, ok := ctx.Value(ContextKeySenderID).(string); ok {
			msg.Sender = senderID
		} else if userID, ok := ctx.Value(ContextKeyUserID).(string); ok {
			msg.Sender = userID
		}
	}

	msg.Receiver = toUserID
	msg.ReceiverNode = h.nodeID
	msg.CreateAt = mathx.IF(msg.CreateAt.IsZero(), startTime, msg.CreateAt)

	// 设置默认Source为online(如果未设置)
	msg.Source = mathx.IfEmpty(msg.Source, MessageSourceOnline)

	// 确保消息ID存在
	snowflakeId := h.idGenerator.GenerateRequestID()
	msg.ID = mathx.IfNotEmpty(msg.ID, fmt.Sprintf("%s-%s", toUserID, snowflakeId))
	// 若业务消息ID为空，则使用Hub生成的ID
	msg.MessageID = mathx.IfNotEmpty(msg.MessageID, snowflakeId)

	// 检查用户是否在线
	isOnline := h.checkUserOnline(toUserID)
	if !isOnline {
		// 用户离线 - 自动存储到离线队列/数据库
		if h.offlineMessageHandler != nil {
			// 存储离线消息
			if err := h.offlineMessageHandler.StoreOfflineMessage(ctx, toUserID, msg); err != nil {
				h.logger.ErrorKV("存储离线消息失败",
					"user_id", toUserID,
					"message_id", msg.MessageID,
					"error", err,
				)
				result.FinalError = err
				result.TotalDuration = time.Since(startTime)
				h.invokeMessageSendCallback(msg, result)
				return result
			}
			h.logger.InfoKV("用户离线，消息已存储，将在用户上线时推送",
				"user_id", toUserID,
				"message_id", msg.MessageID,
			)
			result.Success = true
			result.TotalDuration = time.Since(startTime)
			h.invokeMessageSendCallback(msg, result)
			return result
		}

		// 未启用自动离线存储或处理器未设置
		err := errorx.NewError(ErrTypeUserOffline, toUserID)
		result.FinalError = err
		result.TotalDuration = time.Since(startTime)
		h.invokeMessageSendCallback(msg, result)
		return result
	}

	// 用户在线 - 执行发送逻辑
	// 创建 go-toolbox retry 实例用于延迟计算和条件判断
	retryInstance := retry.NewRetryWithCtx(ctx).
		SetAttemptCount(h.config.RetryPolicy.MaxRetries + 1).     // +1 因为第一次不是重试
		SetInterval(h.config.RetryPolicy.BaseDelay).              // 基础延迟
		SetMaxInterval(h.config.RetryPolicy.MaxDelay).            // 最大延迟
		SetBackoffMultiplier(h.config.RetryPolicy.BackoffFactor). // 退避倍数
		SetJitter(h.config.RetryPolicy.Jitter).                   // 是否启用抖动
		SetJitterPercent(h.config.RetryPolicy.JitterPercent).     // 抖动百分比
		SetConditionFunc(h.isRetryableError)                      // 重试条件判断

	// 执行带详细记录的重试逻辑
	finalErr := retryInstance.Do(func() error {
		return h.executeSendAttempt(ctx, toUserID, msg, result)
	})

	// 设置最终结果
	h.finalizeSendResult(result, finalErr, startTime)

	// 调用消息发送完成回调
	h.invokeMessageSendCallback(msg, result)

	return result
}

// executeSendAttempt 执行单次发送尝试并记录结果
func (h *Hub) executeSendAttempt(ctx context.Context, toUserID string, msg *HubMessage, result *SendResult) error {
	attemptStart := time.Now()
	attemptNumber := len(result.Attempts) + 1

	err := h.sendToUser(ctx, toUserID, msg)
	duration := time.Since(attemptStart)

	// 记录每次尝试
	sendAttempt := SendAttempt{
		AttemptNumber: attemptNumber,
		StartTime:     attemptStart,
		Duration:      duration,
		Error:         err,
		Success:       err == nil,
	}
	result.Attempts = append(result.Attempts, sendAttempt)

	// 如果是重试（非首次尝试），记录重试信息到数据库
	if attemptNumber > 1 && h.messageRecordRepo != nil {
		h.recordRetryAttemptAsync(msg.MessageID, attemptNumber, attemptStart, duration, err)
	}

	return err
}

// recordRetryAttemptAsync 异步记录重试信息到数据库
func (h *Hub) recordRetryAttemptAsync(messageID string, attemptNumber int, timestamp time.Time, duration time.Duration, err error) {
	retryAttempt := RetryAttempt{
		AttemptNumber: attemptNumber,
		Timestamp:     timestamp,
		Duration:      duration,
		Error:         "",
		Success:       err == nil,
	}
	if err != nil {
		retryAttempt.Error = err.Error()
	}

	syncx.Go().
		OnError(func(err error) {
			h.logger.DebugKV("更新重试记录失败",
				"message_id", messageID,
				"attempt", attemptNumber,
				"error", err,
			)
		}).
		ExecWithContext(func(execCtx context.Context) error {
			return h.messageRecordRepo.IncrementRetry(execCtx, messageID, retryAttempt)
		})
}

// finalizeSendResult 设置发送结果的最终状态
func (h *Hub) finalizeSendResult(result *SendResult, finalErr error, startTime time.Time) {
	result.Success = finalErr == nil
	result.FinalError = finalErr
	result.TotalDuration = time.Since(startTime)
	result.TotalRetries = len(result.Attempts) - 1 // 减1因为第一次不算重试

	// 如果成功发送，设置送达时间
	if result.Success {
		result.DeliveredAt = time.Now()
	}
}

// invokeMessageSendCallback 调用消息发送完成回调
func (h *Hub) invokeMessageSendCallback(msg *HubMessage, result *SendResult) {
	if h.messageSendCallback == nil {
		return
	}

	// 仅对人类用户类型调用回调，忽略系统/机器人消息
	// 如果 ReceiverType 为空，默认为人类用户（向后兼容）
	if msg.ReceiverType != "" && !msg.ReceiverType.IsHumanType() {
		return
	}

	syncx.Go().
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("消息发送回调panic",
				"message_id", msg.MessageID,
				"panic", r,
			)
		}).
		Exec(func() {
			h.messageSendCallback(msg, result)
		})
}

// isRetryableError 判断错误是否可以重试 - 完全基于错误类型
func (h *Hub) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 使用errors包进行类型判断
	return IsRetryableError(err)
}

// ============================================================================
// 批量发送方法
// ============================================================================

// SendToMultipleUsers 发送消息给多个用户
func (h *Hub) SendToMultipleUsers(ctx context.Context, userIDs []string, msg *HubMessage) map[string]error {
	errors := make(map[string]error)
	for _, userID := range userIDs {
		result := h.SendToUserWithRetry(ctx, userID, msg)
		if result.FinalError != nil {
			errors[userID] = result.FinalError
		}
	}
	return errors
}

// SendToGroupMembers 向会话成员批量发送消息（兼容旧版本接口）
// 参数:
//   - ctx: 上下文
//   - memberIDs: 成员ID列表
//   - msg: 要发送的消息
//   - excludeSender: 是否排除发送者本身
//
// 返回:
//   - BroadcastResult: 广播结果，包含成功、失败、离线统计
//
// 示例:
//
//	向会话成员广播，排除发送者自己
//	result := hub.SendToGroupMembers(ctx, memberIDs, msg, true)
//	简单批量发送（不排除发送者）
//	result := hub.SendToGroupMembers(ctx, userIDs, msg, false)
func (h *Hub) SendToGroupMembers(ctx context.Context, memberIDs []string, msg *HubMessage, excludeSender bool) *BroadcastResult {
	// 如果需要排除发送者，从列表中移除
	filteredIDs := memberIDs
	if excludeSender && msg.Sender != "" {
		filteredIDs = mathx.FilterSlice(memberIDs, func(id string) bool {
			return id != msg.Sender
		})
		h.logger.DebugKV("🔄 过滤发送者后的成员列表",
			"original_count", len(memberIDs),
			"filtered_count", len(filteredIDs),
			"excluded_sender", msg.Sender,
		)
	}

	// 并发批量发送
	result := &BroadcastResult{
		Total:      len(filteredIDs),
		Success:    0,
		Offline:    0,
		Failed:     0,
		Errors:     make(map[string]error),
		OfflineIDs: make([]string, 0),
		FailedIDs:  make([]string, 0),
	}

	syncx.NewParallelSliceExecutor[string, *SendResult](filteredIDs).
		OnComplete(func(results []*SendResult, errors []error) {
			for i, sendResult := range results {
				if errors[i] == nil && sendResult.Success {
					result.Success++
				} else if sendResult.FinalError != nil {
					result.Failed++
					result.FailedIDs = append(result.FailedIDs, filteredIDs[i])
					result.Errors[filteredIDs[i]] = sendResult.FinalError
				}
			}
		}).
		Execute(func(idx int, uid string) (*SendResult, error) {
			// SendToUserWithRetry 内部已经处理了在线/离线逻辑
			// - 在线用户：直接发送
			// - 离线用户：自动存储到离线队列，上线后推送
			sendResult := h.SendToUserWithRetry(ctx, uid, msg)
			return sendResult, nil
		})

	h.logger.DebugKV("✅ 会话消息发送完成",
		"session_id", msg.SessionID,
		"message_id", msg.MessageID,
		"total", result.Total,
		"success", result.Success,
		"offline", result.Offline,
		"failed", result.Failed,
	)

	return result
}

// SendToClientsWithRetry 发送消息给多个客户端（带重试）
func (h *Hub) SendToClientsWithRetry(ctx context.Context, clients []*Client, msg *HubMessage, maxRetries int) map[string]*SendResult {
	results := make(map[string]*SendResult, len(clients))
	var resultsMutex sync.Mutex

	syncx.NewParallelSliceExecutor[*Client, *SendResult](clients).
		OnSuccess(func(idx int, client *Client, result *SendResult) {
			resultsMutex.Lock()
			results[client.UserID] = result
			resultsMutex.Unlock()
		}).
		Execute(func(idx int, client *Client) (*SendResult, error) {
			return h.SendToUserWithRetry(ctx, client.UserID, msg), nil
		})

	return results
}

// ============================================================================
// 辅助方法
// ============================================================================

// recordMessageToDatabase 记录消息到数据库
func (h *Hub) recordMessageToDatabase(msg *HubMessage, sendErr error) {
	if h.messageRecordRepo == nil {
		return
	}

	syncx.Go().
		WithTimeout(3 * time.Second).
		OnError(func(err error) {
			h.logger.DebugKV("记录消息到数据库失败",
				"message_id", msg.MessageID,
				"error", err,
			)
		}).
		ExecWithContext(func(ctx context.Context) error {
			now := time.Now()

			// 计算过期时间
			expiresAt := now.Add(mathx.IfNotZero(h.config.MessageRecordTTL, 24*time.Hour))

			// 完整记录所有字段
			record := &MessageSendRecord{
				SessionID:    msg.SessionID,
				MessageID:    msg.MessageID,
				HubID:        msg.ID,
				Sender:       msg.Sender,
				Receiver:     msg.Receiver,
				MessageType:  msg.MessageType,
				Source:       msg.Source,
				NodeIP:       h.nodeID,
				CreateTime:   msg.CreateAt,
				Status:       MessageSendStatusSending, // 消息已入队,标记为sending
				RetryCount:   0,
				MaxRetry:     h.config.RetryPolicy.MaxRetries,
				RetryHistory: []RetryAttempt{},
				ExpiresAt:    &expiresAt,
			}

			// 序列化消息数据
			if msgData, err := json.Marshal(msg); err == nil {
				record.MessageData = string(msgData)
			}

			if sendErr != nil {
				record.Status = MessageSendStatusFailed
				record.ErrorMessage = sendErr.Error()
				record.FailureReason = FailureReason(sendErr.Error())
				record.FirstSendTime = &now
				record.LastSendTime = &now
			}

			return h.messageRecordRepo.Create(ctx, record)
		})
}

// notifyQueueFull 通知队列满处理器
func (h *Hub) notifyQueueFull(msg *HubMessage, recipient string, queueType QueueType, err errorx.BaseError) {
	if h.queueFullCallback == nil {
		return
	}

	syncx.Go().
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("队列满回调panic",
				"message_id", msg.MessageID,
				"panic", r,
			)
		}).
		Exec(func() {
			h.queueFullCallback(msg, recipient, queueType, err)
		})
}

// ============================================================================
// 高级发送方法
// ============================================================================

// SendWithCallback 发送消息并在完成时执行回调
func (h *Hub) SendWithCallback(ctx context.Context, userID string, msg *HubMessage,
	onSuccess func(*SendResult), onError func(error)) {

	syncx.Go().
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("SendWithCallback panic",
				"user_id", userID,
				"message_id", msg.MessageID,
				"panic", r,
			)
		}).
		Exec(func() {
			result := h.SendToUserWithRetry(ctx, userID, msg)
			if result.Success && onSuccess != nil {
				onSuccess(result)
			} else if !result.Success && onError != nil {
				onError(result.FinalError)
			}
		})
}

// SendPriority 根据优先级发送消息
func (h *Hub) SendPriority(ctx context.Context, userID string, msg *HubMessage, priority Priority) {
	msg.Priority = priority

	// 高优先级消息直接发送，不使用队列
	if priority >= PriorityHigh {
		syncx.Go(ctx).Exec(func() {
			h.SendToUserWithRetry(ctx, userID, msg)
		})
		return
	}

	// 普通优先级使用标准流程
	h.SendToUserWithRetry(ctx, userID, msg)
}

// SendConditional 根据条件发送消息给符合条件的客户端
func (h *Hub) SendConditional(ctx context.Context, condition func(*Client) bool, msg *HubMessage) int {
	clients := h.GetClientsCopy()
	matchedClients := mathx.FilterSlice(clients, condition)

	var successCount int32
	syncx.NewParallelSliceExecutor[*Client, *SendResult](matchedClients).
		OnSuccess(func(idx int, client *Client, result *SendResult) {
			if result.Success {
				atomic.AddInt32(&successCount, 1)
			}
		}).
		Execute(func(idx int, client *Client) (*SendResult, error) {
			return h.SendToUserWithRetry(ctx, client.UserID, msg), nil
		})

	return int(successCount)
}

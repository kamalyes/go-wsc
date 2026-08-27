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
	"runtime/debug"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/retry"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 基础发送方法
// ============================================================================

// routeToClusterForOfflineUser 当用户在本地和 Redis 索引中都判定为离线时，
// 仍通过 routeToCluster pubsub 广播到其他节点
//
// 场景：多 Pod 部署中，用户在 Pod A 连接，但 Redis 在线索引因心跳 batch 延迟（2s）、
// channel 满、或 Redis 抖动暂时为空，导致 Pod B 的 checkUserOnline 返回 false。
// 若不广播，消息只存离线不跨节点投递，用户收不到实时消息
//
// 安全性：与离线存储配合使用，不会丢消息也不会重复：
//   - 用户在其他节点在线 → pubsub 投递成功，离线消息不会被推送（用户已在线不触发上线推送）
//   - 用户确实离线 → pubsub 无节点投递，离线消息在上线时推送
func (h *Hub) routeToClusterForOfflineUser(ctx context.Context, userID string, msg *HubMessage) {
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return // 单机模式，无需跨节点
	}
	// 📊 广播兜底触发计数（reportPerformanceMetrics 每 5min 上报后清零）
	// 治本后该值应趋近 0；若持续增长说明索引写入仍有滞后（检查 syncOnlineStatus 是否同步执行、Redis 可达性）
	h.broadcastFallbackCount.Add(1)
	opts := ClusterDispatchOptions{
		Operation:    OperationTypeSendMessage,
		TargetUserID: userID,
	}
	h.logger.InfoContextKV(ctx, "🔍 [跨Pod] 用户本地+Redis索引判定离线，发起pubsub跨节点投递",
		"user_id", userID,
		"message_id", msg.MessageID,
		"sender", msg.Sender,
		"node_id", h.nodeID,
		"grpc_enabled", h.IsGRPCEnabled(),
		"has_pubsub", h.pubsub != nil,
		"trigger_reason", "local_miss+redis_miss",
	)
	if err := h.routeToCluster(ctx, msg, opts); err != nil {
		h.logger.WarnContextKV(ctx, "离线用户跨节点广播失败",
			"user_id", userID,
			"message_id", msg.MessageID,
			"error", err,
		)
	}
}

// sendToUser 发送消息给指定用户（内部方法）
// 自动支持分布式：如果用户在其他节点，会自动路由过去
func (h *Hub) sendToUser(ctx context.Context, toUserID string, msg *HubMessage) error {
	// 深拷贝消息（Clone 持 RLock 与 Set*/With* 互斥；Data map 独立，避免与原 msg 并发写 fatal）
	// （ack 重试 goroutine 写 CreateAt 与 EventLoop 序列化读 CreateAt 并发）
	msgCopy := msg.Clone()
	msgCopy.ReceiverNode = mathx.IfEmpty(msgCopy.ReceiverNode, h.nodeID)
	msgCopy.Receiver = mathx.IfEmpty(msgCopy.Receiver, toUserID) // 确保 Receiver 非空（离线消息反序列化后可能丢失）
	msgCopy.CreateAt = mathx.IfNotZero(msgCopy.CreateAt, time.Now())

	// 🔏 P2P 严格场景：EnsureRouteDefaults 归一化 namespace + InjectRoute 注入信封（防御性，与入口一致）
	// sendToUser 可被 ack 重试/离线推送等路径直接调用，msg 可能未经过入口归一化；
	// EnsureRouteDefaults + InjectRoute 幂等，SendToUserWithRetry 路径再调一次无副作用
	ctx = routing.EnsureRouteDefaults(ctx)
	ctx = msgCopy.InjectRoute(ctx)
	// 🔗 trace 恢复：ctx 无 trace 时从消息信封恢复（如 workerPool/离线回放等异步路径 ctx 已丢失），
	// sendToUser 是所有投递路径的漏斗点，在此恢复保证下游 sendToClientSerialized 的
	// 投递日志与消息原始链路同一 trace_id；ctx 已有 trace 不覆盖（在线链路同源）
	ctx = msgCopy.ContextFrom(ctx)

	// 📝 write-ahead：先落 sending 记录再投递（outbox 模式）
	// 必须先于 checkAndRouteToNode（跨节点 Publish）和 handleBroadcast（本地投递）提交：
	// 投递侧的状态回报走 statusUpdater 批量 UPDATE（扑空静默忽略，见
	// MessageRecordGormRepository.BatchUpdateStatus），若记录后建，UPDATE 命中 0 行
	// → 状态永久停留 sending → 被 ACK 超时兜底误标 ack_timeout 并重复转存离线
	// （用户实际已收到，上线后重复推送）先提交 INSERT 任务使其在投递链路上花费的
	// Publish RTT + 目标节点处理 + statusUpdater flush 间隔内完成落库
	h.recordMessageToDatabase(msgCopy, nil)

	// 🌐 分布式路由：检查用户是否在其他节点
	// 先快照本地在线状态：路由决策与本地投递共用，避免两次查询间的连接抖动造成判定漂移
	// 按 ctx 路由信封(appID+namespace)过滤，避免跨 app/ns 误判在线
	localOnline := h.shardedRegistry.HasUser(toUserID, routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx))
	routed, err := h.checkAndRouteToNode(ctx, toUserID, msgCopy)
	if err != nil {
		// 路由失败，记录错误但继续尝试本地发送
		// 注意：此处不将消息标记为失败，因为会 fallback 到本地发送
		// 如果本地发送也失败，下游的 default 分支会标记为 QueueFull 失败
		h.logger.WarnContextKV(ctx, "跨节点路由失败，尝试本地发送",
			"user_id", toUserID,
			"message_id", msgCopy.MessageID,
			"error", err,
		)
		// 🔥 本地无连接且跨节点路由失败：本地投递必然扑空，返回错误让上层
		// 重试机制（瞬时故障如 Redis 抖动可重试成功）或最终失败离线兜底接管，
		// 避免 fire-and-forget 谎报成功导致消息静默丢失
		if !localOnline {
			return errorx.NewError(models.ErrTypeTemporaryFailure, "跨节点路由失败(用户不在本节点): %v", err)
		}
	}
	if routed && !localOnline {
		// 用户仅在其他节点（本地无连接），消息已路由到其他节点，本地无需处理
		h.logger.DebugContextKV(ctx, "消息已路由到其他节点",
			"message_id", msgCopy.MessageID,
			"user_id", toUserID,
		)
		return nil
	}
	// 用户在本节点或单机模式，正常发送
	// 🔥 多端跨节点：routed=true 仅代表"已投递到其他节点"，用户可能同时在本地和
	// 其他节点有设备（如手机连 Pod A、电脑连 Pod B），本地投递不能被跳过，
	// 否则本地设备永远收不到跨节点发送方的消息（修复前 routed=true 直接 return）
	// 同步执行 handleBroadcast：保证同一发送者顺序发送的消息按序投递到接收方
	// SendToUserWithRetry 本身为阻塞调用，handleBroadcast 内仅做非阻塞的 TrySend
	// （观察者通知走 batcher 异步、数据库记录已先行提交），不会显著拖慢发送路径；
	// 此前用 `go` 异步派发会使并发 goroutine 竞争同一接收方 sendChan 导致消息乱序
	h.handleBroadcast(msgCopy)
	h.logger.DebugContextKV(ctx, "消息已广播", "message_id", msgCopy.MessageID, "from", msgCopy.Sender, "to", msgCopy.Receiver, "type", msgCopy.MessageType)
	return nil
}

// ============================================================================
// 重试发送方法
// ============================================================================

// SendToUserWithRetry 带重试机制的发送消息给指定用户
func (h *Hub) SendToUserWithRetry(ctx context.Context, toUserID string, msg *HubMessage) *SendResult {
	// 立即创建消息副本，避免并发修改原始消息
	msg = msg.Clone()

	// 🔏 P2P 严格场景：先 EnsureRouteDefaults 归一化 namespace（空补 DefaultNamespace），再 InjectRoute
	// InjectRoute 只归一化 appID（namespace 保持 ctx 原值兼容全局广播），故 P2P 入口需显式归一化 namespace
	ctx = routing.EnsureRouteDefaults(ctx)
	ctx = msg.InjectRoute(ctx)

	result := &SendResult{
		Attempts: make([]SendAttempt, 0, h.config.RetryPolicy.MaxRetries+1),
	}

	startTime := time.Now()

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
	msg.ID = mathx.IfNotEmpty(msg.ID, toUserID+"-"+snowflakeId)
	// 若业务消息ID为空，则使用Hub生成的ID
	msg.MessageID = mathx.IfNotEmpty(msg.MessageID, snowflakeId)

	// 检查用户是否在线（按 ctx 路由信封 appID+namespace 隔离，避免跨 app/ns 误判在线）
	isOnline := h.checkUserOnline(ctx, toUserID)
	h.logger.InfoContextKV(ctx, "📍 [投递诊断] 用户在线检查",
		"user_id", toUserID,
		"message_id", msg.MessageID,
		"is_online", isOnline,
		"node_id", h.nodeID,
	)
	if !isOnline {
		// 用户不在本节点且 Redis 全局索引未查到
		// 先通过 pubsub 广播到其他节点：防止 Redis 索引短暂不可用（心跳 batch 延迟/channel 满）
		// 导致消息不跨节点投递 其他节点收到后检查本地是否有该用户，有则投递
		// 若用户确实不在任何节点，下方的离线存储保证消息不丢（上线时推送）
		h.routeToClusterForOfflineUser(ctx, toUserID, msg)

		// 用户离线 - 自动存储到离线队列/数据库
		if h.offlineMessageHandler != nil {
			// 存储离线消息
			if err := h.offlineMessageHandler.StoreOfflineMessage(ctx, toUserID, msg); err != nil {
				h.logger.ErrorContextKV(ctx, "存储离线消息失败",
					"user_id", toUserID,
					"message_id", msg.MessageID,
					"error", err,
				)
				// 🔥 离线存储失败 → 更新 message_record 状态为 Failed
				// 离线存储失败通常因 Redis 队列满或 MySQL 写入异常，消息无法投递也无法暂存
				h.updateMessageStatusAsync(ctx, msg.MessageID, MessageSendStatusFailed, FailureReasonQueueFull, err.Error())
				result.FinalError = err
				result.TotalDuration = time.Since(startTime)
				h.invokeMessageSendCallback(msg, result)
				return result
			}
			h.logger.InfoContextKV(ctx, "用户离线，消息已存储，将在用户上线时推送",
				"user_id", toUserID,
				"message_id", msg.MessageID,
			)
			result.Success = true
			result.StoredOffline = true
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

	// 🔥 在线判定成功但重试耗尽仍失败 → 标记 Failed + 异步转存离线（消息不丢，用户上线时推送）
	// 此前该路径仅依赖 30s 后的 ACK 超时扫描兜底：延迟窗口大、依赖扫描器存活，
	// 且 sendToUser fire-and-forget 谎报成功时连扫描器都捞不到（状态被误报 success 的路径除外）
	// tryStoreOfflineOnDeliveryFailure 内部：转存成功覆盖状态为 UserOffline，失败保持 Failed
	if finalErr != nil && !result.StoredOffline {
		h.updateMessageStatusAsync(ctx, msg.MessageID, MessageSendStatusFailed, models.FailureReasonMaxRetry, finalErr.Error())
		h.tryStoreOfflineOnDeliveryFailure(msg, finalErr)
	}

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
		h.recordRetryAttemptAsync(ctx, msg, attemptNumber, attemptStart, duration, err)
	}

	return err
}

// recordRetryAttemptAsync 异步记录重试信息到数据库
func (h *Hub) recordRetryAttemptAsync(ctx context.Context, msg *HubMessage, attemptNumber int, timestamp time.Time, duration time.Duration, err error) {
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

	// 🔗 trace 恢复：以消息信封 trace_id 为准，重试记录日志与 DB 操作可追溯原始发送链路
	ctx = msg.ContextFrom(ctx)
	syncx.Go().
		OnError(func(err error) {
			h.logger.DebugContextKV(ctx, "更新重试记录失败",
				"message_id", msg.MessageID,
				"attempt", attemptNumber,
				"error", err,
			)
		}).
		ExecWithContext(func(execCtx context.Context) error {
			execCtx = msg.ContextFrom(execCtx)
			return h.messageRecordRepo.IncrementRetry(execCtx, msg.MessageID, retryAttempt)
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
			h.logger.ErrorContextKV(msg.ContextFrom(h.ctx), "消息发送回调panic",
				"message_id", msg.MessageID,
				"panic", r,
				"stack", string(debug.Stack()),
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

// SendToMultipleUsers 并发发送消息给多个用户
// 使用 ParallelSliceExecutor 并行投递 + 预分配 slice + 索引写入，消除 mutex 竞争
func (h *Hub) SendToMultipleUsers(ctx context.Context, userIDs []string, msg *HubMessage) map[string]error {
	errs := make(map[string]error, len(userIDs))
	if len(userIDs) == 0 {
		return errs
	}

	// 预分配结果 slice，每个 goroutine 只写自己的索引（无数据竞争）
	errList := make([]error, len(userIDs))

	syncx.NewParallelSliceExecutor[string, *SendResult](userIDs).
		Execute(func(idx int, userID string) (*SendResult, error) {
			result := h.SendToUserWithRetry(ctx, userID, msg)
			if result.FinalError != nil {
				errList[idx] = result.FinalError // 索引写入，无需锁
			}
			return result, nil
		})

	// Execute 同步返回后，无竞争地转为 map
	for i, userID := range userIDs {
		if errList[i] != nil {
			errs[userID] = errList[i]
		}
	}

	return errs
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
		h.logger.DebugContextKV(ctx, "🔄 过滤发送者后的成员列表",
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
				// 优先判失败（FinalError != nil 即为失败）
				if sendResult.FinalError != nil {
					result.Failed++
					result.FailedIDs = append(result.FailedIDs, filteredIDs[i])
					result.Errors[filteredIDs[i]] = sendResult.FinalError
					continue
				}
				// 成功路径需区分在线送达 vs 离线存储
				if sendResult.Success {
					if sendResult.StoredOffline {
						// 离线存储成功（用户不在线，消息入离线队列）
						result.Offline++
						result.OfflineIDs = append(result.OfflineIDs, filteredIDs[i])
					} else {
						// 在线送达成功
						result.Success++
					}
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

	h.logger.DebugContextKV(ctx, "✅ 会话消息发送完成",
		"session_id", msg.SessionID,
		"message_id", msg.MessageID,
		"total", result.Total,
		"success", result.Success,
		"offline", result.Offline,
		"failed", result.Failed,
	)

	// 🔔 通知观察者（群组级别统一通知，与 SendToGroup 对齐）
	// handleBroadcast 对 GroupIDs 非空的消息跳过观察者通知，此处补齐
	h.notifyObservers(ctx, msg)

	return result
}

// SendToClientsWithRetry 发送消息给多个客户端（带重试）
// 使用预分配 slice + 索引定位写入，消除 mutex（每个 goroutine 写不同索引，无竞争）
func (h *Hub) SendToClientsWithRetry(ctx context.Context, clients []*Client, msg *HubMessage, maxRetries int) map[string]*SendResult {
	results := make(map[string]*SendResult, len(clients))
	if len(clients) == 0 {
		return results
	}

	// 预分配结果 slice，每个 goroutine 只写自己的索引（无数据竞争）
	resultsSlice := make([]*SendResult, len(clients))

	syncx.NewParallelSliceExecutor[*Client, *SendResult](clients).
		OnSuccess(func(idx int, client *Client, result *SendResult) {
			resultsSlice[idx] = result // 各 goroutine 写不同索引，无需锁
		}).
		Execute(func(idx int, client *Client) (*SendResult, error) {
			return h.SendToUserWithRetry(ctx, client.UserID, msg), nil
		})

	// Execute 同步返回后，所有写入已完成，无竞争地转为 map
	for i, client := range clients {
		if resultsSlice[i] != nil {
			results[client.UserID] = resultsSlice[i]
		}
	}

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

	h.workerPool.TrySubmitRecord(func() {
		ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
		defer cancel()
		// 从消息体恢复 trace_id（SendToUserWithRetry 已注入）
		ctx = msg.ContextFrom(ctx)

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

		// SetMessage 序列化消息体并同步 Namespace/GroupID 等路由信封字段到 record
		if err := record.SetMessage(msg); err != nil {
			h.logger.WarnContextKV(ctx, "序列化消息数据失败",
				"message_id", msg.MessageID, "error", err)
		}

		if sendErr != nil {
			record.Status = MessageSendStatusFailed
			record.ErrorMessage = sendErr.Error()
			record.FailureReason = FailureReason(sendErr.Error())
			record.FirstSendTime = &now
			record.LastSendTime = &now
		}

		if err := h.messageRecordRepo.Create(ctx, record); err != nil {
			h.logger.DebugContextKV(ctx, "记录消息到数据库失败",
				"message_id", msg.MessageID,
				"error", err,
			)
		} else if record.Status == MessageSendStatusSending {
			// ⏰ 在时间轮上调度跨节点 ACK 超时任务（per-message，+nodeAckTimeout 触发兜底）
			// 状态由 sending 变更时由 updateMessageStatusAsync O(1) 取消；详见 ack_timer.go
			h.scheduleAckTimeout(record.MessageID)
		}
	})
}

// updateMessageStatusAsync 非阻塞更新消息状态到 DB
func (h *Hub) updateMessageStatusAsync(ctx context.Context, msgID string, status MessageSendStatus, reason FailureReason, errMsg string) {
	if h.messageRecordRepo == nil || h.statusUpdater == nil {
		return
	}

	// ⏰ 状态由 sending 变更时 O(1) 取消跨节点 ACK 超时任务（本地投递即时取消，跨节点目标取消为 no-op）
	// 本节点持有的 timer 被取消后不再触发冗余 ClaimStaleSending 检查；详见 ack_timer.go
	h.cancelAckTimeout(msgID)
	if !h.statusUpdater.Submit(&statusUpdateItem{
		msgID:  msgID,
		status: status,
		reason: reason,
		errMsg: errMsg,
	}) {
		// 🔗 trace 恢复：ctx 由调用方传入（投递路径已恢复消息信封 trace_id）
		h.logger.DebugContextKV(ctx, "消息状态更新队列已满，丢弃",
			"message_id", msgID,
			"status", status,
		)
	}
}

// tryStoreOfflineOnDeliveryFailure 在在线投递失败时异步转存离线消息
//
// 触发条件（全部满足才转存）：
//   - offlineMessageHandler 可用
//   - msg.Receiver 非空（P2P 消息，广播场景不转存）
//   - msg.Source != offline（离线推送本身失败不重新存，避免无限循环）
//
// 状态流转：
//   - 转存成功 → message_record 状态从 Failed 覆盖为 UserOffline（消息已安全暂存，等用户上线推送）
//   - 转存失败 → 保持调用方已标记的 Failed 状态（消息确实丢了）
//
// 注意：多设备场景下若部分设备投递成功部分失败，失败设备仍会触发转存。
// 这不会导致重复推送：用户上线 drain 时 pushAndDeleteOffline 成功后按 message_id 删 MySQL，
// 客户端也可按 message_id 去重。
func (h *Hub) tryStoreOfflineOnDeliveryFailure(msg *HubMessage, deliveryErr error) {
	if h.offlineMessageHandler == nil || msg.Receiver == "" {
		return
	}
	// 离线消息推送失败不重新存（避免无限循环），只标记 Failed
	if msg.Source == MessageSourceOffline {
		return
	}

	syncx.Go().
		OnError(func(storeErr error) {
			h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "在线投递失败后转存离线也失败",
				"message_id", msg.MessageID,
				"user_id", msg.Receiver,
				"delivery_error", deliveryErr.Error(),
				"store_error", storeErr.Error(),
			)
			// 转存失败，保持已有的 Failed 状态，不覆盖
		}).
		ExecWithContext(func(storeCtx context.Context) error {
			storeCtx = msg.ContextFrom(storeCtx)
			if err := h.offlineMessageHandler.StoreOfflineMessage(storeCtx, msg.Receiver, msg); err != nil {
				return err
			}
			// 转存成功 → 覆盖状态为 UserOffline（消息已暂存，等用户上线推送）
			h.updateMessageStatusAsync(storeCtx, msg.MessageID, MessageSendStatusUserOffline, FailureReasonUserOffline, "")
			h.logger.InfoContextKV(storeCtx, "在线投递失败，消息已转存离线队列",
				"message_id", msg.MessageID,
				"user_id", msg.Receiver,
				"delivery_error", deliveryErr.Error(),
			)
			return nil
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
			h.logger.ErrorContextKV(ctx, "SendWithCallback panic",
				"user_id", userID,
				"message_id", msg.MessageID,
				"panic", r,
				"stack", string(debug.Stack()),
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
// 复用 broadcastToFiltered：预序列化一次 + 直接 TrySend，避免逐客户端 Clone/序列化/入队/DB 记录
func (h *Hub) SendConditional(ctx context.Context, condition func(*Client) bool, msg *HubMessage) int {
	return h.broadcastToFiltered(ctx, condition, msg)
}

// SendToAllClientsInMap 发送消息到映射中的所有客户端
// 预序列化一次消息，避免对每个客户端重复 json.Marshal
func (h *Hub) SendToAllClientsInMap(clientMap map[string]*Client, msg *HubMessage) {
	// 复制客户端列表,避免在遍历时map被修改导致竞争
	clients := CopyClientsFromMap(clientMap)
	if len(clients) == 0 {
		return
	}

	// 预序列化一次（WebSocket 客户端共用；SSE 走 TrySendSSE(msg) 不用 []byte）
	// 序列化失败时 preSerialized=nil，由 sendToClientSerialized 内部兜底
	preSerialized, _ := json.Marshal(msg)

	// 遍历复制后的列表发送消息
	for _, client := range clients {
		h.sendToClientSerialized(h.ctx, client, msg, preSerialized)
	}
}

// sendToClient 发送消息到客户端（内部序列化）
func (h *Hub) sendToClient(ctx context.Context, client *Client, msg *HubMessage) {
	h.sendToClientSerialized(ctx, client, msg, nil)
}

// sendToClientSerialized 发送消息到客户端（支持预序列化数据）
// preSerialized 为预序列化的 []byte，为 nil 时内部序列化
// SSE 客户端忽略 preSerialized，直接发送 msg 对象
// 返回是否成功投递到客户端通道（跨节点 PubSub 路径据此统计投递成败）
func (h *Hub) sendToClientSerialized(ctx context.Context, client *Client, msg *HubMessage, preSerialized []byte) bool {
	// 检查客户端是否已关闭
	if client.IsClosed() {
		return false
	}

	// 🔥 如果 MessageID 为空，使用 HubID
	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)

	// SSE 客户端使用专用的消息通道
	if client.ConnectionType == ConnectionTypeSSE {
		if client.TrySendSSE(msg) {
			client.SetLastSeen(time.Now())
			h.logger.InfoContextKV(ctx, "消息已投递到本地客户端",
				"message_id", msgID,
				"user_id", client.UserID,
				"client_id", client.ID,
				"connection_type", "sse",
				"node_id", h.nodeID,
			)
			// SSE消息成功发送，更新为成功状态
			h.updateMessageStatusAsync(ctx, msgID, MessageSendStatusSuccess, "", "")
			return true
		}
		sseErr := fmt.Errorf("SSE channel full or closed")
		h.logger.WarnContextKV(ctx, "SSE客户端消息通道已满或已关闭", "client_id", client.ID, "user_id", client.UserID)
		// SSE通道已满或已关闭，更新为失败状态
		h.updateMessageStatusAsync(ctx, msgID, MessageSendStatusFailed, FailureReasonQueueFull, sseErr.Error())
		// 🔥 在线投递失败 → 异步转存离线（P2P 场景，避免循环）
		h.tryStoreOfflineOnDeliveryFailure(msg, sseErr)
		return false
	}

	// WebSocket 客户端：使用预序列化数据或现场序列化
	var data []byte
	if preSerialized != nil {
		data = preSerialized
	} else {
		var err error
		data, err = json.Marshal(msg)
		if err != nil {
			h.logger.ErrorContextKV(ctx, "消息序列化失败", "error", err)
			// 更新为失败状态
			h.updateMessageStatusAsync(ctx, msgID, MessageSendStatusFailed, FailureReasonUnknown, err.Error())
			// 序列化失败无法转存离线（msg 无法被存储），只标记 Failed
			return false
		}
	}

	if client.TrySend(data) {
		// 消息成功发送到客户端通道，更新为成功状态
		h.updateMessageStatusAsync(ctx, msgID, MessageSendStatusSuccess, "", "")

		// 链路闭环日志：与跨 Pod 路径（distributed.go "[跨Pod] 消息已投递到本地客户端"）统一，
		// trace_id 可从 NotifySend 入口一路串到本节点最终投递（ctx 由 msg.ContextFrom 恢复携带 trace）
		h.logger.InfoContextKV(ctx, "消息已投递到本地客户端",
			"message_id", msgID,
			"user_id", client.UserID,
			"client_id", client.ID,
			"node_id", h.nodeID,
		)

		// 更新接收者的消息统计和字节统计
		h.trackReceiverMessageStats(client.ID, client.UserType, len(data))
		return true
	}

	queueErr := fmt.Errorf("client send channel full or closed")
	h.logger.WarnContextKV(ctx, "客户端发送通道已满或已关闭", "client_id", client.ID)
	// 发送通道已满或已关闭，更新为失败状态
	h.updateMessageStatusAsync(ctx, msgID, MessageSendStatusFailed, FailureReasonQueueFull, queueErr.Error())
	// 🔥 在线投递失败 → 异步转存离线（P2P 场景，避免循环）
	h.tryStoreOfflineOnDeliveryFailure(msg, queueErr)
	return false
}

// syncToSenderDevices 同步消息给发送者的其他设备（多端同步）
// 场景：用户A在设备B、C、D登录，设备B发送消息给用户F，设备C和D应该收到此消息
//
// 性能：使用 ForEachUserClient 零拷贝遍历 + 内联过滤，
// 替代旧版 GetClientsCopyForUser（拷贝1）+ FilterSlice（拷贝2）双重拷贝
func (h *Hub) syncToSenderDevices(ctx context.Context, msg *HubMessage) {
	if msg.Sender == "" {
		return
	}

	// 零拷贝遍历：一次遍历完成计数+收集其他设备，避免中间切片拷贝
	// 过滤：只收集路由匹配（namespace）的设备，且排除当前发送端
	// （发送者多端同步不检查 group，同一 user 在不同 group 的设备都应该同步到）
	var otherDevices []*Client
	deviceCount := 0
	h.shardedRegistry.ForEachUserClientFiltered(msg.Sender, msg.AppID, msg.Namespace, nil, func(_ string, client *Client) bool {
		deviceCount++
		if client.ID != msg.SenderClient {
			otherDevices = append(otherDevices, client)
		}
		return true
	})

	// 只有发送者自己一个设备（或无设备），无需同步
	if deviceCount <= 1 || len(otherDevices) == 0 {
		return
	}

	// 预序列化一次（所有设备复用，消除循环内重复 Marshal）
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "多端同步消息序列化失败", "error", err, "message_id", msg.MessageID)
		return
	}

	h.logger.DebugContextKV(ctx, "🔄 多端同步消息给发送者的其他设备",
		"sender", msg.Sender,
		"sender_client", msg.SenderClient,
		"other_devices_count", len(otherDevices),
		"message_id", msg.MessageID,
	)

	// 发送给发送者的其他设备
	for _, device := range otherDevices {
		h.sendToClientSerialized(ctx, device, msg, data)
	}
}

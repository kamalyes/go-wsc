/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 23:08:11
 * @FilePath: \go-wsc\messaging\send.go
 * @Description: Hub 消息发送功能
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/retry"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
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
func (m *Manager) routeToClusterForOfflineUser(ctx context.Context, userID string, msg *models.HubMessage) {
	if !m.host.HasPubsub() && !m.host.IsGRPCEnabled() {
		return // 单机模式，无需跨节点
	}
	// 批量扇出路径（群广播/批量发送）：不立即广播，聚合到 collector，由扇出入口在 Execute 完成后统一 flush 推送（避免扇出 goroutine 阻塞在 Redis 连接池）
	if collector, ok := ctx.Value(ContextKeyOfflineBroadcastCollector).(*offlineBroadcastCollector); ok && collector != nil {
		collector.add(userID, msg)
		return
	}
	m.doOfflineBroadcast(ctx, userID, msg)
}

// doOfflineBroadcast 执行跨节点离线广播兜底（不经 collector 聚合，flush 与单发路径共用）
func (m *Manager) doOfflineBroadcast(ctx context.Context, userID string, msg *models.HubMessage) {
	// 广播兜底触发计数（reportPerformanceMetrics 每 5min 上报后清零）
	// 治本后该值应趋近 0；若持续增长说明索引写入仍有滞后（检查 syncOnlineStatus 是否同步执行、Redis 可达性）
	m.broadcastFallbackCount.Add(1)
	// 记录广播兜底投递目标（user_not_found 重路由守卫：广播路径离线已预存，回告全拒时不再重复转离线）
	m.host.MarkRerouteAttempted(msg.MessageID, m.host.GetAllClusterNodeIDs(), false)
	opts := cluster.ClusterDispatchOptions{
		Operation:    models.OperationTypeSendMessage,
		TargetUserID: userID,
	}
	m.host.GetLogger().InfoContextKV(ctx, "[跨Pod] 用户本地+Redis索引判定离线，发起pubsub跨节点投递",
		"user_id", userID,
		"message_id", msg.MessageID,
		"sender", msg.Sender,
		"node_id", m.host.GetNodeID(),
		"grpc_enabled", m.host.IsGRPCEnabled(),
		"has_pubsub", m.host.HasPubsub(),
		"trigger_reason", "local_miss+redis_miss",
	)
	if err := m.host.RouteToCluster(ctx, msg, opts); err != nil {
		m.host.GetLogger().WarnContextKV(ctx, "离线用户跨节点广播失败",
			"user_id", userID,
			"message_id", msg.MessageID,
			"error", err,
		)
	}
}

// sendToUser 发送消息给指定用户（内部方法）
// 自动支持分布式：如果用户在其他节点，会自动路由过去
//
// msg 必须为调用方私有副本（入口 SendToUserWithRetry 已 Clone / 测试构造局部 msg），
// 本方法内直接补默认字段（历史双 Clone 冗余：热路径为 ack 重试小众路径买单）
// presetNodes 非空时跳过 Redis 节点查询（SendToUserWithRetry 本地 miss 时已预取，单次往返）
func (m *Manager) sendToUser(ctx context.Context, toUserID string, msg *models.HubMessage, presetNodes []string) error {
	msg.ReceiverNode = mathx.IfEmpty(msg.ReceiverNode, m.host.GetNodeID())
	msg.Receiver = mathx.IfEmpty(msg.Receiver, toUserID) // 确保 Receiver 非空（离线消息反序列化后可能丢失）
	msg.CreateAt = mathx.IfNotZero(msg.CreateAt, time.Now())

	// 信封注入（幂等）：sendToUser 可被 ack 重试/离线推送等路径直接调用，msg 可能未经入口注入；
	// InjectRoute 仅归一化 appID、ns 保留 ctx 原值（P2P 路径公开入口已归一化，
	// 群组可靠投递扇出路径 ns="" 为跨 ns 通配语义，此处不得归一化）
	ctx = msg.InjectRoute(ctx)
	// trace 恢复：ctx 无 trace 时从消息信封恢复（如 workerPool/离线回放等异步路径 ctx 已丢失），
	// sendToUser 是所有投递路径的漏斗点，在此恢复保证下游 SendToClientSerialized 的
	// 投递日志与消息原始链路同一 trace_id；ctx 已有 trace 不覆盖（在线链路同源）
	ctx = msg.ContextFrom(ctx)

	// write-ahead：先落 sending 记录再投递（outbox 模式）
	// 必须先于 checkAndRouteToNode（跨节点 Publish）和 handleBroadcast（本地投递）提交：
	// 投递侧的状态回报走 statusUpdater 批量 UPDATE（扑空静默忽略，见
	// gormadapter.MessageSink.BatchUpdateStatus），若记录后建，UPDATE 命中 0 行
	// → 状态永久停留 sending → 被 ACK 超时兜底误标 ack_timeout 并重复转存离线
	// （用户实际已收到，上线后重复推送）先提交 INSERT 任务使其在投递链路上花费的
	// Publish RTT + 目标节点处理 + statusUpdater flush 间隔内完成落库
	m.recordMessageToDatabase(msg, nil)

	// 分布式路由：检查用户是否在其他节点
	// 先快照本地在线状态：路由决策与本地投递共用，避免两次查询间的连接抖动造成判定漂移
	// 按 ctx 路由信封(appID+namespace)过滤，避免跨 app/ns 误判在线
	localOnline := m.host.GetShardedRegistry().HasUser(toUserID, routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx))
	routed, routeNodes, err := m.host.CheckAndRouteToNode(ctx, toUserID, msg, presetNodes)
	if err != nil {
		// 路由失败，记录错误但继续尝试本地发送
		// 注意：此处不将消息标记为失败，因为会 fallback 到本地发送
		// 如果本地发送也失败，下游的 default 分支会标记为 QueueFull 失败
		m.host.GetLogger().WarnContextKV(ctx, "跨节点路由失败，尝试本地发送",
			"user_id", toUserID,
			"message_id", msg.MessageID,
			"error", err,
		)
		// 本地无连接且跨节点路由失败：本地投递必然扑空，返回错误让上层
		// 重试机制（瞬时故障如 Redis 抖动可重试成功）或最终失败离线兜底接管，
		// 避免 fire-and-forget 谎报成功导致消息静默丢失
		if !localOnline {
			return errorx.NewError(models.ErrTypeTemporaryFailure, "跨节点路由失败(用户不在本节点): %v", err)
		}
	}
	if routed && !localOnline {
		// 用户仅在其他节点（本地无连接），消息已路由到其他节点，本地无需处理
		m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] 用户仅在其他节点，已远程投递，本地跳过",
			"message_id", msg.MessageID,
			"user_id", toUserID,
			"to_nodes", routeNodes,
		)
		return nil
	}
	// 用户在本节点或单机模式，正常发送
	// 多端跨节点：routed=true 仅代表"已投递到其他节点"，用户可能同时在本地和
	// 其他节点有设备（如手机连 Pod A、电脑连 Pod B），本地投递不能被跳过，
	// 否则本地设备永远收不到跨节点发送方的消息（修复前 routed=true 直接 return）
	// 同步执行 handleBroadcast：保证同一发送者顺序发送的消息按序投递到接收方
	// SendToUserWithRetry 本身为阻塞调用，handleBroadcast 内仅做非阻塞的 TrySend
	// （观察者通知走 batcher 异步、数据库记录已先行提交），不会显著拖慢发送路径；
	// 此前用 `go` 异步派发会使并发 goroutine 竞争同一接收方 sendChan 导致消息乱序
	// 🔒 首连门闩：接收方处于离线回放中时暂存本次投递，回放完成后按序补投
	// （闭包捕获的 msg 为 sendToUser 入口克隆的独立副本，延迟执行无并发写风险）
	m.HoldUserDelivery(toUserID, func() { m.handleBroadcast(msg) })
	m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] 本地投递已发起（含多端跨节点双投递场景）",
		"message_id", msg.MessageID,
		"from", msg.Sender,
		"to", msg.Receiver,
		"routed_remote", routed,
		"to_nodes", routeNodes,
		"type", msg.MessageType,
	)
	return nil
}

// ============================================================================
// 重试发送方法
// ============================================================================

// SendToUserWithRetry 带重试机制的发送消息给指定用户
// 公开 P2P 入口：先 EnsureRouteDefaults 归一化 appID/namespace（P2P 严格契约，空补默认值），
// 内部路径（sendToUserWithRetry/sendToUser）不再归一化——群组可靠投递的 per-member 扇出
// 走同一管道，信封 ns="" 为跨 ns 通配语义（见 Deliver 群组分派器），归一化会破坏通配
func (m *Manager) SendToUserWithRetry(ctx context.Context, toUserID string, msg *models.HubMessage) *models.SendResult {
	ctx = routing.EnsureRouteDefaults(ctx)
	return m.sendToUserWithRetry(ctx, toUserID, msg, nil)
}

// sendToUserWithRetry 带重试机制的发送核心
// presetNodes：扇出调用方（群组可靠投递/批量发送）批量预取的节点索引，本地 miss 时零回源复用；
// nil 或未覆盖该用户时回退单元素批查（BatchGetUserNodes 传单元素切片，复用批量端口）
// 仅首次尝试有效：重试时用户可能已迁移节点，仍传 nil 重新查询
func (m *Manager) sendToUserWithRetry(ctx context.Context, toUserID string, msg *models.HubMessage, presetNodes []string) *models.SendResult {
	// 立即创建消息副本，避免并发修改原始消息
	msg = msg.Clone()

	// InjectRoute 注入信封（appID 幂等归一化，ns 保留 ctx 原值：P2P 路径公开入口已归一化，
	// 群组可靠投递扇出路径 ns="" 为跨 ns 通配语义，此处不得归一化）
	ctx = msg.InjectRoute(ctx)

	result := &models.SendResult{
		Attempts: make([]models.SendAttempt, 0, m.host.GetConfig().RetryPolicy.MaxRetries+1),
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
	msg.ReceiverNode = m.host.GetNodeID()
	msg.CreateAt = mathx.IF(msg.CreateAt.IsZero(), startTime, msg.CreateAt)

	// 设置默认Source为online(如果未设置)
	msg.Source = mathx.IfEmpty(msg.Source, models.MessageSourceOnline)

	// 确保消息ID存在
	snowflakeId := m.idGenerator.GenerateRequestID()
	msg.ID = mathx.IfNotEmpty(msg.ID, toUserID+"-"+snowflakeId)
	// 若业务消息ID为空，则使用Hub生成的ID
	msg.MessageID = mathx.IfNotEmpty(msg.MessageID, snowflakeId)

	// 在线判定与路由合并为单次 Redis 往返（热路径优化，消 CheckUserOnline 的 bitmap 查询）：
	// 本地 registry 命中 → 在线，节点列表由 sendToUser 自查（多端跨节点仍需一次查询）；
	// 本地 miss → 一次 BatchGetUserNodes 同时得到在线判定（len>0）与跨节点路由目标
	// （原路径：IsUserOnline bitmap + checkAndRouteToNode 节点单查 = 2 次往返）
	localOnline := m.host.GetShardedRegistry().HasUser(toUserID, routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx))
	isOnline := localOnline
	if !localOnline {
		// presetNodes：扇出调用方批量预取的节点索引（零回源复用）；nil 或批量预取未覆盖该用户时回退单次查询
		if presetNodes == nil {
			presetNodes = m.host.BatchGetUserNodes(ctx, []string{toUserID})[toUserID]
		}
		isOnline = len(presetNodes) > 0
	}
	// 逐消息成功路径日志走 DEBUG（千万连接规模下逐消息 INFO 是吞吐反模式，
	// 异常路径——离线存储失败/投递 0 客户端/终态失败——保持 INFO/WARN 生产可见）
	m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] 用户在线检查",
		"user_id", toUserID,
		"message_id", msg.MessageID,
		"is_online", isOnline,
		"node_id", m.host.GetNodeID(),
	)
	if !isOnline {
		// 用户不在本节点且 Redis 全局索引未查到
		// 先通过 pubsub 广播到其他节点：防止 Redis 索引短暂不可用（心跳 batch 延迟/channel 满）
		// 导致消息不跨节点投递 其他节点收到后检查本地是否有该用户，有则投递
		// 若用户确实不在任何节点，下方的离线存储保证消息不丢（上线时推送）
		m.routeToClusterForOfflineUser(ctx, toUserID, msg)

		// 用户离线 - 自动存储到离线队列/数据库
		if m.offlineHandler != nil {
			// 存储离线消息
			if err := m.offlineHandler.StoreOfflineMessage(ctx, toUserID, msg); err != nil {
				m.host.GetLogger().ErrorContextKV(ctx, "存储离线消息失败",
					"user_id", toUserID,
					"message_id", msg.MessageID,
					"error", err,
				)
				// 离线存储失败 → 更新 message_record 状态为 Failed
				// 离线存储失败通常因 Redis 队列满或 MySQL 写入异常，消息无法投递也无法暂存
				m.updateMessageStatusAsync(ctx, msg.MessageID, msg.Receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, err.Error())
				result.FinalError = err
				result.TotalDuration = time.Since(startTime)
				m.invokeMessageSendCallback(msg, result)
				return result
			}
			m.host.GetLogger().DebugContextKV(ctx, "用户离线，消息已存储，将在用户上线时推送",
				"user_id", toUserID,
				"message_id", msg.MessageID,
			)
			result.Success = true
			result.StoredOffline = true
			result.TotalDuration = time.Since(startTime)
			m.invokeMessageSendCallback(msg, result)
			return result
		}

		// 未启用自动离线存储或处理器未设置
		err := errorx.NewError(models.ErrTypeUserOffline, toUserID)
		result.FinalError = err
		result.TotalDuration = time.Since(startTime)
		m.invokeMessageSendCallback(msg, result)
		return result
	}

	// 用户在线 - 执行发送逻辑
	// 创建 go-toolbox retry 实例用于延迟计算和条件判断
	retryInstance := retry.NewRetryWithCtx(ctx).
		SetAttemptCount(m.host.GetConfig().RetryPolicy.MaxRetries + 1).     // +1 因为第一次不是重试
		SetInterval(m.host.GetConfig().RetryPolicy.BaseDelay).              // 基础延迟
		SetMaxInterval(m.host.GetConfig().RetryPolicy.MaxDelay).            // 最大延迟
		SetBackoffMultiplier(m.host.GetConfig().RetryPolicy.BackoffFactor). // 退避倍数
		SetJitter(m.host.GetConfig().RetryPolicy.Jitter).                   // 是否启用抖动
		SetJitterPercent(m.host.GetConfig().RetryPolicy.JitterPercent).     // 抖动百分比
		SetConditionFunc(m.isRetryableError)                                // 重试条件判断

	// 执行带详细记录的重试逻辑（presetNodes 仅首次有效：重试时用户可能已迁移节点，传 nil 重新查询）
	finalErr := retryInstance.Do(func() error {
		var nodes []string
		if len(result.Attempts) == 0 {
			nodes = presetNodes
		}
		return m.executeSendAttempt(ctx, toUserID, msg, nodes, result)
	})

	// 设置最终结果
	m.finalizeSendResult(result, finalErr, startTime)

	// 在线判定成功但重试耗尽仍失败 → 标记 Failed + 异步转存离线（消息不丢，用户上线时推送）
	// 此前该路径仅依赖 30s 后的 ACK 超时扫描兜底：延迟窗口大、依赖扫描器存活，
	// 且 sendToUser fire-and-forget 谎报成功时连扫描器都捞不到（状态被误报 success 的路径除外）
	// StoreOfflineOnDeliveryFailure 内部：转存成功覆盖状态为 UserOffline，失败保持 Failed
	if finalErr != nil && !result.StoredOffline {
		m.updateMessageStatusAsync(ctx, msg.MessageID, msg.Receiver, models.MessageSendStatusFailed, models.FailureReasonMaxRetry, finalErr.Error())
		m.StoreOfflineOnDeliveryFailure(msg, finalErr)
	}

	// 终态汇总：与入口"[投递诊断] 用户在线检查"首尾呼应，一眼判断消息是否发出/卡在哪一步
	// 成功终态走 DEBUG（逐消息 INFO 是吞吐反模式）；失败终态走 ERROR 已在上方单独记录
	finalErrMsg := ""
	if result.FinalError != nil {
		finalErrMsg = result.FinalError.Error()
	}
	m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] P2P 发送终态",
		"message_id", msg.MessageID,
		"user_id", toUserID,
		"success", result.Success,
		"attempts", len(result.Attempts),
		"stored_offline", result.StoredOffline,
		"total_retries", result.TotalRetries,
		"duration_ms", result.TotalDuration.Milliseconds(),
		"final_error", finalErrMsg,
	)

	// 调用消息发送完成回调
	m.invokeMessageSendCallback(msg, result)

	return result
}

// executeSendAttempt 执行单次发送尝试并记录结果
func (m *Manager) executeSendAttempt(ctx context.Context, toUserID string, msg *models.HubMessage, presetNodes []string, result *models.SendResult) error {
	attemptStart := time.Now()
	attemptNumber := len(result.Attempts) + 1

	err := m.sendToUser(ctx, toUserID, msg, presetNodes)
	duration := time.Since(attemptStart)

	// 记录每次尝试
	sendAttempt := models.SendAttempt{
		AttemptNumber: attemptNumber,
		StartTime:     attemptStart,
		Duration:      duration,
		Error:         err,
		Success:       err == nil,
	}
	result.Attempts = append(result.Attempts, sendAttempt)

	// 如果是重试（非首次尝试），记录重试信息到数据库
	if attemptNumber > 1 && m.host.GetMessageSink() != nil {
		m.recordRetryAttemptAsync(ctx, msg, attemptNumber, attemptStart, duration, err)
	}

	return err
}

// recordRetryAttemptAsync 异步记录重试信息到数据库
func (m *Manager) recordRetryAttemptAsync(ctx context.Context, msg *models.HubMessage, attemptNumber int, timestamp time.Time, duration time.Duration, err error) {
	retryAttempt := models.RetryAttempt{
		AttemptNumber: attemptNumber,
		Timestamp:     timestamp,
		Duration:      duration,
		Error:         "",
		Success:       err == nil,
	}
	if err != nil {
		retryAttempt.Error = err.Error()
	}

	// trace 恢复：以消息信封 trace_id 为准，重试记录日志与 DB 操作可追溯原始发送链路
	ctx = msg.ContextFrom(ctx)
	syncx.Go().
		OnError(func(err error) {
			m.host.GetLogger().DebugContextKV(ctx, "更新重试记录失败",
				"message_id", msg.MessageID,
				"attempt", attemptNumber,
				"error", err,
			)
		}).
		ExecWithContext(func(execCtx context.Context) error {
			execCtx = msg.ContextFrom(execCtx)
			return m.host.GetMessageSink().IncrementRetry(execCtx, models.MessageRecordKey{MessageID: msg.MessageID, Receiver: msg.Receiver}, retryAttempt)
		})
}

// finalizeSendResult 设置发送结果的最终状态
func (m *Manager) finalizeSendResult(result *models.SendResult, finalErr error, startTime time.Time) {
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
func (m *Manager) invokeMessageSendCallback(msg *models.HubMessage, result *models.SendResult) {
	if m.messageSendCallback == nil {
		return
	}

	// 仅对人类用户类型调用回调，忽略系统/机器人消息
	// 如果 ReceiverType 为空，默认为人类用户（向后兼容）
	if msg.ReceiverType != "" && !msg.ReceiverType.IsHumanType() {
		return
	}

	syncx.Go().
		OnPanic(func(r interface{}) {
			m.host.GetLogger().ErrorContextKV(msg.ContextFrom(m.host.Context()), "消息发送回调panic",
				"message_id", msg.MessageID,
				"panic", r,
				"stack", string(debug.Stack()),
			)
		}).
		Exec(func() {
			m.messageSendCallback(msg, result)
		})
}

// isRetryableError 判断错误是否可以重试 - 完全基于错误类型
func (m *Manager) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// 使用errors包进行类型判断
	return models.IsRetryableError(err)
}

// ============================================================================
// 批量发送方法
// ============================================================================

// fanoutConcurrency 解析扇出并发上限
// 复用 WorkerPool.MessageWorkers（默认 64），成员数可达数万的群广播无界扇出会
// 瞬时打爆 Redis 连接池并滞留大量消息副本（OOM 教训），必须限制在途 goroutine 数
func (m *Manager) fanoutConcurrency() int {
	if m.host.GetConfig() != nil && m.host.GetConfig().WorkerPool != nil && m.host.GetConfig().WorkerPool.MessageWorkers > 0 {
		return m.host.GetConfig().WorkerPool.MessageWorkers
	}
	return 64
}

// newFanoutExecutor 创建有界并发的扇出执行器（所有 per-user 批量投递统一入口）
func newFanoutExecutor[T any](items []T, concurrency int) *syncx.ParallelSliceExecutor[T, *models.SendResult] {
	return syncx.NewParallelSliceExecutor[T, *models.SendResult](items).WithConcurrency(concurrency)
}

// offlineBroadcastEntry 聚合收集的离线广播条目
type offlineBroadcastEntry struct {
	userID string
	msg    *models.HubMessage
}

// offlineBroadcastCollector 批量扇出路径的离线广播聚合器
// 扇出 goroutine 内仅收集（O(1) 追加，不碰 Redis），扇出结束后统一 flush 推送：
//   - 索引滞后但实际在线的用户仍能实时收到消息（不跳过、真推送）
//   - 真正离线的用户由离线存储 + 重连上线拉取兜底
//   - 广播移出扇出关键路径，消除 goroutine 阻塞堆积（OOM 根因）
type offlineBroadcastCollector struct {
	mu      sync.Mutex
	entries []offlineBroadcastEntry
}

// add 收集一条离线广播条目（扇出 goroutine 并发调用安全）
func (c *offlineBroadcastCollector) add(userID string, msg *models.HubMessage) {
	c.mu.Lock()
	c.entries = append(c.entries, offlineBroadcastEntry{userID: userID, msg: msg})
	c.mu.Unlock()
}

// withOfflineBroadcastCollector 为批量扇出路径注入聚合器，返回新上下文与聚合器
func withOfflineBroadcastCollector(ctx context.Context) (context.Context, *offlineBroadcastCollector) {
	collector := &offlineBroadcastCollector{}
	return context.WithValue(ctx, ContextKeyOfflineBroadcastCollector, collector), collector
}

// flushOfflineBroadcasts 扇出结束后统一跨节点推送收集到的未命中用户（有界并发，复用扇出并发度）
func (m *Manager) flushOfflineBroadcasts(ctx context.Context, collector *offlineBroadcastCollector) {
	collector.mu.Lock()
	entries := collector.entries
	collector.entries = nil
	collector.mu.Unlock()

	if len(entries) == 0 {
		return
	}
	m.host.GetLogger().InfoContextKV(ctx, "[跨Pod] 批量扇出完成，统一推送聚合的离线兜底广播",
		"pending_count", len(entries),
		"node_id", m.host.GetNodeID(),
	)
	newFanoutExecutor(entries, m.fanoutConcurrency()).
		Execute(func(idx int, entry offlineBroadcastEntry) (*models.SendResult, error) {
			// 直接走底层广播（绕过 collector 检查，避免重新收集造成死循环）
			m.doOfflineBroadcast(ctx, entry.userID, entry.msg)
			return nil, nil
		})
}

// prefetchFanoutNodes 批量预取扇出目标的节点索引（Pipeline 单次往返）
//
// 扇出路径优化：群组可靠投递/批量发送对每个成员都要一次节点查询来判定在线与跨节点路由目标
// （N 个成员 = N 次 Redis 往返）。此前仅预取本地 miss 成员，本地在线成员在 sendToUser 内仍经
// checkAndRouteToNode → queryUserNodes 逐用户 GetUserNodes 判定多端跨节点路由，同样存在 N+1；
// 本方法改为对全部去重成员一次 Pipeline 预取，本地/远端成员统一零回源。
//
// 返回 nil 表示不适用（单机模式/目标过少/批量查询失败），扇出回退原有逐用户查询路径。
// 不修改调用方 msg：信封注入在克隆副本上完成（与 sendToUserWithRetry 内部同序同参）
func (m *Manager) prefetchFanoutNodes(ctx context.Context, msg *models.HubMessage, userIDs []string) map[string][]string {
	// 目标过少批量预取无收益（1 个用户 Pipeline 与单次查询等价）；单机模式无跨节点索引
	if len(userIDs) < 2 || (!m.host.HasPubsub() && !m.host.IsGRPCEnabled()) {
		return nil
	}
	// 路由信封注入（不归一化，保留调用方 ctx 原值）：群组扇出路径信封 ns="" 为跨 ns 通配语义，
	// BatchGetUserNodes 走 unscoped 桶 + appID 过滤定位节点；在克隆副本上执行，不污染调用方原始 msg
	routeCtx := msg.Clone().InjectRoute(ctx)

	// 去重 userID（含本地在线成员：其节点解析用于 sendToUser 的多端跨节点路由，一并预取消除 N+1）
	seen := make(map[string]struct{}, len(userIDs))
	uniques := make([]string, 0, len(userIDs))
	for _, uid := range userIDs {
		if uid == "" {
			continue
		}
		if _, dup := seen[uid]; dup {
			continue
		}
		seen[uid] = struct{}{}
		uniques = append(uniques, uid)
	}
	if len(uniques) == 0 {
		return nil
	}

	nodes := m.host.BatchGetUserNodes(routeCtx, uniques)
	if len(nodes) == 0 {
		return nil // 批量查询失败（含全部无活跃节点），扇出回退逐用户查询
	}
	return nodes
}

// SendToMultipleUsers 并发发送消息给多个用户
// 使用 ParallelSliceExecutor 并行投递 + 预分配 slice + 索引写入，消除 mutex 竞争
func (m *Manager) SendToMultipleUsers(ctx context.Context, userIDs []string, msg *models.HubMessage) map[string]error {
	errs := make(map[string]error, len(userIDs))
	if len(userIDs) == 0 {
		return errs
	}

	// 预分配结果 slice，每个 goroutine 只写自己的索引（无数据竞争）
	errList := make([]error, len(userIDs))

	ctx, collector := withOfflineBroadcastCollector(ctx)
	defer m.flushOfflineBroadcasts(ctx, collector)
	// 批量预取本地 miss 用户的节点索引（1 次 Pipeline 替代扇出内 N 次单查）
	presetNodes := m.prefetchFanoutNodes(ctx, msg, userIDs)
	newFanoutExecutor(userIDs, m.fanoutConcurrency()).
		Execute(func(idx int, userID string) (*models.SendResult, error) {
			result := m.sendToUserWithRetry(ctx, userID, msg, presetNodes[userID])
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
//   - models.BroadcastResult: 广播结果，包含成功、失败、离线统计
//
// 示例:
//
//	向会话成员广播，排除发送者自己
//	result := hub.SendToGroupMembers(ctx, memberIDs, msg, true)
//	简单批量发送（不排除发送者）
//	result := hub.SendToGroupMembers(ctx, userIDs, msg, false)
func (m *Manager) SendToGroupMembers(ctx context.Context, memberIDs []string, msg *models.HubMessage, excludeSender bool) *models.BroadcastResult {
	// 如果需要排除发送者，从列表中移除
	filteredIDs := memberIDs
	if excludeSender && msg.Sender != "" {
		filteredIDs = mathx.FilterSlice(memberIDs, func(id string) bool {
			return id != msg.Sender
		})
		m.host.GetLogger().DebugContextKV(ctx, "过滤发送者后的成员列表",
			"original_count", len(memberIDs),
			"filtered_count", len(filteredIDs),
			"excluded_sender", msg.Sender,
		)
	}

	// 并发批量发送
	result := &models.BroadcastResult{
		Total:      len(filteredIDs),
		Success:    0,
		Offline:    0,
		Failed:     0,
		Errors:     make(map[string]error),
		OfflineIDs: make([]string, 0),
		FailedIDs:  make([]string, 0),
	}

	ctx, collector := withOfflineBroadcastCollector(ctx)
	defer m.flushOfflineBroadcasts(ctx, collector)
	// 批量预取本地 miss 成员的节点索引（1 次 Pipeline 替代扇出内 N 次单查）
	presetNodes := m.prefetchFanoutNodes(ctx, msg, filteredIDs)
	newFanoutExecutor(filteredIDs, m.fanoutConcurrency()).
		OnComplete(func(results []*models.SendResult, errors []error) {
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
		Execute(func(idx int, uid string) (*models.SendResult, error) {
			// sendToUserWithRetry 内部已经处理了在线/离线逻辑
			// - 在线用户：直接发送（presetNodes 批量预取，免逐成员单查）
			// - 离线用户：自动存储到离线队列，上线后推送
			sendResult := m.sendToUserWithRetry(ctx, uid, msg, presetNodes[uid])
			return sendResult, nil
		})

	m.host.GetLogger().DebugContextKV(ctx, "会话消息发送完成",
		"session_id", msg.SessionID,
		"message_id", msg.MessageID,
		"total", result.Total,
		"success", result.Success,
		"offline", result.Offline,
		"failed", result.Failed,
	)

	// 通知观察者（群组级别统一通知，与 SendToGroup 对齐）
	// handleBroadcast 对 GroupIDs 非空的消息跳过观察者通知，此处补齐
	m.NotifyObservers(ctx, msg)

	return result
}

// SendToClientsWithRetry 发送消息给多个客户端（带重试）
// 使用预分配 slice + 索引定位写入，消除 mutex（每个 goroutine 写不同索引，无竞争）
func (m *Manager) SendToClientsWithRetry(ctx context.Context, clients []*models.Client, msg *models.HubMessage, maxRetries int) map[string]*models.SendResult {
	results := make(map[string]*models.SendResult, len(clients))
	if len(clients) == 0 {
		return results
	}

	// 预分配结果 slice，每个 goroutine 只写自己的索引（无数据竞争）
	resultsSlice := make([]*models.SendResult, len(clients))

	ctx, collector := withOfflineBroadcastCollector(ctx)
	defer m.flushOfflineBroadcasts(ctx, collector)
	// 批量预取本地 miss 用户的节点索引（客户端列表按 userID 去重后预取，多端同用户共享单条索引）
	userIDs := make([]string, len(clients))
	for i, client := range clients {
		userIDs[i] = client.UserID
	}
	presetNodes := m.prefetchFanoutNodes(ctx, msg, userIDs)
	newFanoutExecutor(clients, m.fanoutConcurrency()).
		OnSuccess(func(idx int, client *models.Client, result *models.SendResult) {
			resultsSlice[idx] = result // 各 goroutine 写不同索引，无需锁
		}).
		Execute(func(idx int, client *models.Client) (*models.SendResult, error) {
			return m.sendToUserWithRetry(ctx, client.UserID, msg, presetNodes[client.UserID]), nil
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

// recordMessageToDatabase 记录消息到数据库（write-ahead：先落 sending 记录再投递）
//
// 攒批化：经 MessageRecordOutbox 批量 INSERT（满批/50ms flush，高吞吐下写放大
// 降低 2 个数量级）；ACK 超时注册延后到 flush 成功时（ScheduleAckTimeouts 回调）。
// outbox 未注入时降级原 workerPool 单条 Create 路径（本地测试/轻量嵌入场景）
func (m *Manager) recordMessageToDatabase(msg *models.HubMessage, sendErr error) {
	if m.host.GetMessageSink() == nil {
		return
	}

	now := time.Now()

	// 计算过期时间
	expiresAt := now.Add(mathx.IfNotZero(m.host.GetConfig().MessageRecordTTL, 24*time.Hour))

	// 完整记录所有字段
	record := &models.MessageSendRecord{
		SessionID:    msg.SessionID,
		MessageID:    msg.MessageID,
		HubID:        msg.ID,
		Sender:       msg.Sender,
		Receiver:     msg.Receiver,
		MessageType:  msg.MessageType,
		Source:       msg.Source,
		NodeIP:       m.host.GetNodeID(),
		CreateTime:   msg.CreateAt,
		Status:       models.MessageSendStatusSending, // 消息已入队,标记为sending
		RetryCount:   0,
		MaxRetry:     m.host.GetConfig().RetryPolicy.MaxRetries,
		RetryHistory: []models.RetryAttempt{},
		ExpiresAt:    &expiresAt,
	}

	// SetMessage 序列化消息体并同步 Namespace/GroupID 等路由信封字段到 record
	if err := record.SetMessage(msg); err != nil {
		m.host.GetLogger().WarnContextKV(msg.ContextFrom(m.host.Context()), "序列化消息数据失败",
			"message_id", msg.MessageID, "error", err)
	}

	if sendErr != nil {
		record.Status = models.MessageSendStatusFailed
		record.ErrorMessage = sendErr.Error()
		record.FailureReason = models.FailureReason(sendErr.Error())
		record.FirstSendTime = &now
		record.LastSendTime = &now
	}

	// 攒批落库唯一路径：outbox 满批/定时批量 INSERT（发送 goroutine 零阻塞，Submit 仅内存入队）
	// 编排层恒注入 outbox；未注入（测试桩）时跳过落库
	if outbox := m.host.GetMessageRecordOutbox(); outbox != nil {
		outbox.Submit(record)
	}
}

// updateMessageStatusAsync 非阻塞更新消息状态到 DB
// 按 (msgID, receiver) 精确定位记录：P2P 同一 message_id 会为每个 receiver 各建一条记录，
// receiver 缺失会造成多 receiver 状态相互覆盖（如 A 的 success 覆盖 B 的 failed）
func (m *Manager) updateMessageStatusAsync(ctx context.Context, msgID, receiver string, status models.MessageSendStatus, reason models.FailureReason, errMsg string) {
	if m.host.GetMessageSink() == nil || m.host.GetMessageStatusUpdater() == nil {
		return
	}

	// ⏰ 状态由 sending 变更时 O(1) 取消跨节点 ACK 超时任务（本地投递即时取消，跨节点目标取消为 no-op）
	// 本节点持有的 timer 被取消后不再触发冗余 ClaimStaleSending 检查；详见 ack_timer.go
	m.cancelAckTimeout(models.MessageRecordKey{MessageID: msgID, Receiver: receiver})
	// 攒批竞态消除：记录仍在 outbox 未落库时直接合并状态（INSERT 时带终态），
	// 跳过 statusUpdater 的 UPDATE —— 否则 UPDATE 可能先于 INSERT 到库而扑空
	if outbox := m.host.GetMessageRecordOutbox(); outbox != nil &&
		outbox.UpdateStatusIfPending(models.MessageRecordKey{MessageID: msgID, Receiver: receiver}, status, reason, errMsg) {
		return
	}
	if !m.host.GetMessageStatusUpdater().Submit(&batcher.StatusUpdateItem{
		MessageID: msgID,
		Receiver:  receiver,
		Status:    status,
		Reason:    reason,
		ErrMsg:    errMsg,
	}) {
		// trace 恢复：ctx 由调用方传入（投递路径已恢复消息信封 trace_id）
		m.host.GetLogger().DebugContextKV(ctx, "消息状态更新队列已满，丢弃",
			"message_id", msgID,
			"receiver", receiver,
			"status", status,
		)
	}
}

// StoreOfflineOnDeliveryFailure 在在线投递失败时异步转存离线消息
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
func (m *Manager) StoreOfflineOnDeliveryFailure(msg *models.HubMessage, deliveryErr error) {
	if m.offlineHandler == nil || msg.Receiver == "" {
		return
	}
	// 离线消息推送失败不重新存（避免无限循环），只标记 Failed
	if msg.Source == models.MessageSourceOffline {
		return
	}

	syncx.Go().
		OnError(func(storeErr error) {
			m.host.GetLogger().WarnContextKV(msg.ContextFrom(m.host.Context()), "在线投递失败后转存离线也失败",
				"message_id", msg.MessageID,
				"user_id", msg.Receiver,
				"delivery_error", deliveryErr.Error(),
				"store_error", storeErr.Error(),
			)
			// 转存失败，保持已有的 Failed 状态，不覆盖
		}).
		ExecWithContext(func(storeCtx context.Context) error {
			storeCtx = msg.ContextFrom(storeCtx)
			if err := m.offlineHandler.StoreOfflineMessage(storeCtx, msg.Receiver, msg); err != nil {
				return err
			}
			// 转存成功 → 覆盖状态为 UserOffline（消息已暂存，等用户上线推送）
			m.updateMessageStatusAsync(storeCtx, msg.MessageID, msg.Receiver, models.MessageSendStatusUserOffline, models.FailureReasonUserOffline, "")
			m.host.GetLogger().InfoContextKV(storeCtx, "在线投递失败，消息已转存离线队列",
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
func (m *Manager) SendWithCallback(ctx context.Context, userID string, msg *models.HubMessage,
	onSuccess func(*models.SendResult), onError func(error)) {

	syncx.Go().
		OnPanic(func(r interface{}) {
			m.host.GetLogger().ErrorContextKV(ctx, "SendWithCallback panic",
				"user_id", userID,
				"message_id", msg.MessageID,
				"panic", r,
				"stack", string(debug.Stack()),
			)
		}).
		Exec(func() {
			result := m.SendToUserWithRetry(ctx, userID, msg)
			if result.Success && onSuccess != nil {
				onSuccess(result)
			} else if !result.Success && onError != nil {
				onError(result.FinalError)
			}
		})
}

// SendPriority 根据优先级发送消息
func (m *Manager) SendPriority(ctx context.Context, userID string, msg *models.HubMessage, priority models.Priority) {
	msg.Priority = priority

	// 高优先级消息直接发送，不使用队列
	if priority >= models.PriorityHigh {
		syncx.Go(ctx).Exec(func() {
			m.SendToUserWithRetry(ctx, userID, msg)
		})
		return
	}

	// 普通优先级使用标准流程
	m.SendToUserWithRetry(ctx, userID, msg)
}

// SendConditional 根据条件发送消息给符合条件的客户端
// 复用 BroadcastToFiltered：预序列化一次 + 直接 TrySend，避免逐客户端 Clone/序列化/入队/DB 记录
func (m *Manager) SendConditional(ctx context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int {
	return m.BroadcastToFiltered(ctx, condition, msg)
}

// SendToAllClientsInMap 发送消息到映射中的所有客户端
// 预序列化一次消息，避免对每个客户端重复 json.Marshal
func (m *Manager) SendToAllClientsInMap(clientMap map[string]*models.Client, msg *models.HubMessage) {
	// 复制客户端列表,避免在遍历时map被修改导致竞争
	clients := connection.CopyClientsFromMap(clientMap)
	if len(clients) == 0 {
		return
	}

	// 预序列化一次（WebSocket 客户端共用；SSE 走 TrySendSSE(msg) 不用 []byte）
	// 序列化失败时 preSerialized=nil，由 SendToClientSerialized 内部兜底
	preSerialized, _ := json.Marshal(msg)

	// 遍历复制后的列表发送消息
	for _, client := range clients {
		m.SendToClientSerialized(m.host.Context(), client, msg, preSerialized)
	}
}

// SendToClient 发送消息到客户端（内部序列化）
func (m *Manager) SendToClient(ctx context.Context, client *models.Client, msg *models.HubMessage) {
	m.SendToClientSerialized(ctx, client, msg, nil)
}

// SendToClientSerialized 发送消息到客户端（支持预序列化数据）
// preSerialized 为预序列化的 []byte，为 nil 时内部序列化
// SSE 客户端忽略 preSerialized，直接发送 msg 对象
// 返回是否成功投递到客户端通道（跨节点 PubSub 路径据此统计投递成败）
//
// 分级投递接线：
//   - 高频级（Ephemeral）→ Coalescer latest-wins 合并（50ms 周期投递最新值）
//   - Admit 拒绝（Delay/Offline，P2P 场景仅极端水位触发）→ 转离线补发 + 上层重试兜底
//   - 实时路径零改动（TrySend 成功路径）
func (m *Manager) SendToClientSerialized(ctx context.Context, client *models.Client, msg *models.HubMessage, preSerialized []byte) bool {
	// 检查客户端是否已关闭
	if client.IsClosed() {
		return false
	}

	// 如果 MessageID 为空，使用 HubID
	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	// 状态更新按 (msgID, receiver) 定位：P2P 带 receiver、广播为空，各得其所
	receiver := msg.Receiver

	// 高频级：latest-wins 合并（同用户同类型只保最新；50ms drain 周期投递）
	if coalescer := m.host.GetEphemeralCoalescer(); coalescer != nil &&
		msg.ResolveGuarantee() == models.GuaranteeEphemeral &&
		client.ConnectionType != models.ConnectionTypeSSE {
		m.host.GetOverloadMetrics().RecordAdmitted(models.GuaranteeEphemeral)
		if accepted, merged := coalescer.Offer(overload.EphemeralKey(client, msg), msg); accepted {
			if merged {
				// 覆盖同 key 旧消息：latest-wins 合并削峰埋点（漏斗 merged 轨道）
				m.host.GetOverloadMetrics().RecordEphemeralMerged()
			}
			return true // 已并入最新值（将投递）
		}
		// 合并器容量满：语义丢弃（latest-wins 尽头的容量保护）
		m.host.GetOverloadMetrics().RecordEphemeralDrop()
		m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, "coalescer full")
		return false
	}

	// 准入闸门（P2P 场景：L3/L4 极端水位才拒绝；必达级恒放行）
	if verdict := m.host.AdmitMessage(msg, false); verdict != overload.VerdictAdmit {
		// 水位读取（GetAdmissionLevel 内含闸门 nil 防御，热替换窗口安全）
		level := m.host.GetAdmissionLevel()
		rejectErr := fmt.Errorf("admission rejected at %s", level)
		m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, rejectErr.Error())
		// 转离线补发（P2P 离线上线推送 + SendToUserWithRetry/ACK 重试双保险）
		m.StoreOfflineOnDeliveryFailure(msg, rejectErr)
		return false
	}

	// SSE 客户端使用专用的消息通道
	if client.ConnectionType == models.ConnectionTypeSSE {
		if client.TrySendSSE(msg) {
			client.SetLastSeen(time.Now())
			// 逐消息成功路径走 DEBUG（千万连接规模下逐消息 INFO 是吞吐反模式）
			m.host.GetLogger().DebugContextKV(ctx, "消息已投递到本地客户端",
				"message_id", msgID,
				"user_id", client.UserID,
				"client_id", client.ID,
				"connection_type", "sse",
				"node_id", m.host.GetNodeID(),
			)
			// SSE消息成功发送，更新为成功状态
			m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusSuccess, "", "")
			return true
		}
		sseErr := fmt.Errorf("SSE channel full or closed")
		m.host.GetLogger().WarnContextKV(ctx, "SSE客户端消息通道已满或已关闭", "client_id", client.ID, "user_id", client.UserID)
		// SSE通道已满或已关闭，更新为失败状态
		m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, sseErr.Error())
		// 在线投递失败 → 异步转存离线（P2P 场景，避免循环）
		m.StoreOfflineOnDeliveryFailure(msg, sseErr)
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
			m.host.GetLogger().ErrorContextKV(ctx, "消息序列化失败", "error", err)
			// 更新为失败状态
			m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonUnknown, err.Error())
			// 序列化失败无法转存离线（msg 无法被存储），只标记 Failed
			return false
		}
	}

	if client.TrySend(data) {
		// 消息成功发送到客户端通道，更新为成功状态
		m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusSuccess, "", "")
		// 送达漏斗埋点：实时送达 + 在途量出队
		m.host.GetOverloadMetrics().RecordRealtime(msg.ResolveGuarantee())
		m.host.AdmissionOnDelivered()

		// 链路闭环日志：与跨 Pod 路径（distributed.go "[跨Pod] 消息已投递到本地客户端"）统一，
		// trace_id 可从 NotifySend 入口一路串到本节点最终投递（ctx 由 msg.ContextFrom 恢复携带 trace）
		// 逐消息成功路径走 DEBUG（千万连接规模下逐消息 INFO 是吞吐反模式）
		m.host.GetLogger().DebugContextKV(ctx, "消息已投递到本地客户端",
			"message_id", msgID,
			"user_id", client.UserID,
			"client_id", client.ID,
			"node_id", m.host.GetNodeID(),
		)

		// 更新接收者的消息统计和字节统计
		m.host.TrackReceiverMessageStats(client.ID, client.UserType, len(data))
		return true
	}

	queueErr := fmt.Errorf("client send channel full or closed")
	m.host.GetLogger().WarnContextKV(ctx, "客户端发送通道已满或已关闭", "client_id", client.ID)
	// 发送通道已满或已关闭，更新为失败状态
	m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, queueErr.Error())
	// 在线投递失败 → 异步转存离线（P2P 场景，避免循环）
	m.StoreOfflineOnDeliveryFailure(msg, queueErr)
	return false
}

// syncToSenderDevices 同步消息给发送者的其他设备（多端同步）
// 场景：用户A在设备B、C、D登录，设备B发送消息给用户F，设备C和D应该收到此消息
//
// 性能：使用 ForEachUserClient 零拷贝遍历 + 内联过滤，
// 替代旧版 GetClientsCopyForUser（拷贝1）+ FilterSlice（拷贝2）双重拷贝
func (m *Manager) syncToSenderDevices(ctx context.Context, msg *models.HubMessage) {
	if msg.Sender == "" {
		return
	}

	// 零拷贝遍历：一次遍历完成计数+收集其他设备，避免中间切片拷贝
	// 过滤：只收集路由匹配（namespace）的设备，且排除当前发送端
	// （发送者多端同步不检查 group，同一 user 在不同 group 的设备都应该同步到）
	var otherDevices []*models.Client
	deviceCount := 0
	m.host.GetShardedRegistry().ForEachUserClientFiltered(msg.Sender, msg.AppID, msg.Namespace, nil, func(_ string, client *models.Client) bool {
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
		m.host.GetLogger().ErrorContextKV(ctx, "多端同步消息序列化失败", "error", err, "message_id", msg.MessageID)
		return
	}

	m.host.GetLogger().DebugContextKV(ctx, "多端同步消息给发送者的其他设备",
		"sender", msg.Sender,
		"sender_client", msg.SenderClient,
		"other_devices_count", len(otherDevices),
		"message_id", msg.MessageID,
	)

	// 发送给发送者的其他设备
	for _, device := range otherDevices {
		m.SendToClientSerialized(ctx, device, msg, data)
	}
}

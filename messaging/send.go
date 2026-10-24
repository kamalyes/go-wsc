/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-17 15:36:16
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
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/retry"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// P2P 发送主链（入口重试 → 核心体 → 单次尝试 → 跨节点兜底）
// ============================================================================

// routeToClusterForOfflineUser 当用户在本地和 Redis 索引中都判定为离线时，仍通过 routeToCluster pubsub 广播到其他节点
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
// msg 必须为调用方私有副本（入口 SendToUserWithRetry 已 Clone / 测试构造局部 msg）
// 本方法内直接补默认字段（历史双 Clone 冗余：热路径为 ack 重试小众路径买单）
// presetNodes 非空时跳过 Redis 节点查询（SendToUserWithRetry 本地 miss 时已预取，单次往返）
func (m *Manager) sendToUser(ctx context.Context, toUserID string, msg *models.HubMessage, presetNodes []string) error {
	msg.ReceiverNode = mathx.IfEmpty(msg.ReceiverNode, m.host.GetNodeID())
	msg.Receiver = mathx.IfEmpty(msg.Receiver, toUserID) // 确保 Receiver 非空（离线消息反序列化后可能丢失）
	msg.CreateAt = mathx.IfNotZero(msg.CreateAt, time.Now())

	// 信封注入（幂等）：sendToUser 可被 ack 重试/离线推送等路径直接调用，msg 可能未经入口注入；
	// InjectRoute 仅归一化 appID、ns 保留 ctx 原值
	// P2P 路径公开入口已归一化，群组可靠投递扇出路径 ns="" 为跨 ns 通配语义，此处不得归一化
	ctx = msg.InjectRoute(ctx)
	// trace 恢复：ctx 无 trace 时从消息信封恢复（如 workerPool/离线回放等异步路径 ctx 已丢失）
	// sendToUser 是所有投递路径的漏斗点，在此恢复 trace_id 保证下游 SendToClientSerialized 的投递日志与消息原始链路同一 trace_id
	// ctx 已有 trace 不覆盖（在线链路同源）
	ctx = msg.ContextFrom(ctx)

	// write-ahead：先落 sending 记录再投递（outbox 模式）
	// 必须先于 checkAndRouteToNode（跨节点 Publish）和 handleBroadcast（本地投递）提交：
	// 投递侧的状态回报走 statusUpdater 批量 UPDATE（扑空静默忽略，见 gormadapter.MessageSink.BatchUpdateStatus）
	// 若记录后建，UPDATE 命中 0 行，状态永久停留 sending，被 ACK 超时兜底误标 ack_timeout 并重复转存离线，用户实际已收到且上线后重复推送
	// 先提交 INSERT 任务使其在投递链路上花费的 Publish RTT + 目标节点处理 + statusUpdater flush 间隔内完成落库
	// 🔀 express 分道跳过落库：高频流式消息不产生 message_record（防记录表被弹幕/typing 灌爆）
	// 相应地也无状态回报/ACK 对账/离线兜底（全链路轻装，见 sendToUserWithRetryCore express 收尾）
	// 🔀 单次序列化复用：落库记录与本地投递共享同一份编码字节
	// 原路径逐成员两次 json.Marshal（落库 SetMessage 与 handleDirectMessage 各一次），是逐成员可靠扇出的最大单项分配热点
	// express 档不落库，但投递复用同一份
	data, _ := json.Marshal(msg)
	if !msg.ResolveGuarantee().IsExpress() {
		m.recordMessageToDatabase(msg, nil, data)
	}

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
		// 重试机制（瞬时故障如 Redis 抖动可重试成功）或最终失败离线兜底接管
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
	// 其他节点有设备（如手机连 Pod A、电脑连 Pod B），本地投递不能被跳过
	// 否则本地设备永远收不到跨节点发送方的消息（修复前 routed=true 直接 return）
	// 同步执行 handleBroadcast：保证同一发送者顺序发送的消息按序投递到接收方
	// SendToUserWithRetry 本身为阻塞调用，handleBroadcast 内仅做非阻塞的 TrySend
	// （观察者通知走 batcher 异步、数据库记录已先行提交），不会显著拖慢发送路径；
	// 此前用 `go` 异步派发会使并发 goroutine 竞争同一接收方 sendChan 导致消息乱序
	// 🔒 首连门闩：先 holding 预检，闸门全开零锁直判（绝大多数时刻），直接投递零闭包分配（逐成员扇出热路径）
	// 回放中才构造闭包走暂存
	// 闭包捕获的 msg 为 sendToUser 入口克隆的独立副本，延迟执行无并发写风险
	if m.holdingUserDelivery(toUserID) {
		m.HoldUserDelivery(toUserID, func() {
			// 暂存期间已返回调用方，补投失败必须在此接回离线兜底
			if m.handleBroadcast(msg, data) == 0 && !routed {
				failure := errorx.NewError(models.ErrTypeTemporaryFailure, "held delivery rejected")
				m.updateMessageStatusAsync(msg.ContextFrom(m.host.Context()), msg.MessageID, toUserID, models.MessageSendStatusFailed, models.FailureReasonQueueFull, failure.Error())
				m.StoreOfflineOnDeliveryFailure(msg, failure)
			}
		})
	} else {
		if m.handleBroadcast(msg, data) == 0 && !routed {
			return errorx.NewError(models.ErrTypeTemporaryFailure, "no client accepted delivery")
		}
	}
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
// 公开 P2P 入口：先 EnsureRouteDefaults 归一化 appID/namespace（P2P 严格契约，空补默认值）
// 内部路径（sendToUserWithRetry/sendToUser）不再归一化——群组可靠投递的 per-member 扇出走同一管道，信封 ns="" 为跨 ns 通配语义（见 Deliver 群组分派器），归一化会破坏通配
func (m *Manager) SendToUserWithRetry(ctx context.Context, toUserID string, msg *models.HubMessage) *models.SendResult {
	ctx = routing.EnsureRouteDefaults(ctx)
	return m.sendToUserWithRetry(ctx, toUserID, msg, nil)
}

// sendToUserWithRetry 带重试机制的发送入口（Clone + InjectRoute 后委托核心体）
// presetNodes：扇出调用方（群组可靠投递/批量发送/离线回放）批量预取的节点索引，本地 miss 时零回源复用
// nil 或未覆盖该用户时回退单元素批查（BatchGetUserNodes 传单元素切片，复用批量端口）
// 仅首次尝试有效：重试时用户可能已迁移节点，仍传 nil 重新查询
//
// 本方法保留扇出克隆：扇出类调用方（群组可靠投递/批量发送/离线回放）将同一 msg 并发分发给多个成员
// Receiver/ReceiverNode/ID 逐成员不同，必须各持副本（Data/GroupIDs 只读共享，见 CloneForFanout 契约）
// P2P 单一接收方路径（deliverP2P）复用 Deliver 已 Clone 的副本，直接走 sendToUserWithRetryCore 消除二次 Clone
func (m *Manager) sendToUserWithRetry(ctx context.Context, toUserID string, msg *models.HubMessage, presetNodes []string) *models.SendResult {
	// 扇出克隆：仅标量信封字段逐成员改写，Data/GroupIDs 只读共享
	// 深拷贝 map/slice 重建是 10w 级扇出的 GC 热点，全部读方同步序列化，见 CloneForFanout 契约
	msg = msg.CloneForFanout()

	// InjectRoute 注入信封（appID 幂等归一化，ns 保留 ctx 原值）
	// P2P 路径公开入口已归一化，群组可靠投递扇出路径 ns="" 为跨 ns 通配语义，此处不得归一化
	ctx = msg.InjectRoute(ctx)

	return m.sendToUserWithRetryCore(ctx, toUserID, msg, presetNodes)
}

// sendToUserWithRetryCore 发送核心体：msg 必须是调用方私有副本（已 Clone 且已 InjectRoute）
// 本方法不再 Clone（P2P 入口 deliverP2P 复用 Deliver 已 Clone 副本以消除热路径双 Clone）
func (m *Manager) sendToUserWithRetryCore(ctx context.Context, toUserID string, msg *models.HubMessage, presetNodes []string) *models.SendResult {
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

	// 🔀 投递等级解析（一次解析全链路复用）：express 档把解析结果固化到显式字段
	// sendToUser/收尾分支再解析恒命中决策树第 1 优先级（显式设置），全链路仅此一次完整决策树
	guarantee := msg.ResolveGuarantee()
	express := guarantee.IsExpress()
	if express {
		msg.DeliveryGuarantee = guarantee
	}

	isOnline := localOnline
	if !localOnline {
		// presetNodes：扇出调用方批量预取的节点索引（零回源复用）；nil 或批量预取未覆盖该用户时回退单次查询
		if presetNodes == nil {
			presetNodes = m.host.BatchGetUserNodes(ctx, []string{toUserID})[toUserID]
		}
		isOnline = len(presetNodes) > 0
	}

	// 逐消息成功路径日志走 DEBUG（千万连接规模下逐消息 INFO 是吞吐反模式）
	// 异常路径——离线存储失败/投递 0 客户端/终态失败——保持 INFO/WARN 生产可见
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

		// 🔀 express 档不进离线队列：用户离线即放弃本条（高频语义丢弃，下一条同 key 自然覆盖）
		if express {
			m.host.GetOverloadMetrics().RecordEphemeralDrop()
			result.Success = true
			result.TotalDuration = time.Since(startTime)
			m.invokeMessageSendCallback(msg, result)
			return result
		}

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

	// 用户在线 - 执行发送：express 单次直发（fire-and-forget），可靠档走退避重试链
	finalErr := m.deliverOnlineWithPolicy(ctx, toUserID, msg, presetNodes, result, express)

	// 返回前确认离线保存结果，确保成功与已入队标志反映实际处理结果
	if finalErr != nil && !express && m.offlineHandler != nil && msg.Source != models.MessageSourceOffline {
		if storeErr := m.offlineHandler.StoreOfflineMessage(ctx, toUserID, msg); storeErr == nil {
			result.StoredOffline = true
			finalErr = nil
			m.updateMessageStatusAsync(ctx, msg.MessageID, msg.Receiver, models.MessageSendStatusUserOffline, models.FailureReasonUserOffline, "")
		} else {
			finalErr = fmt.Errorf("delivery failed: %w; offline fallback failed: %w", finalErr, storeErr)
		}
	}
	// 设置最终结果
	m.finalizeSendResult(result, finalErr, startTime)

	// 投递终态失败收尾（express 语义丢弃 / 可靠档转离线，不同命）
	if finalErr != nil && !result.StoredOffline {
		if express {
			m.settleDeliveryFailure(ctx, msg, finalErr, true)
		} else {
			m.updateMessageStatusAsync(ctx, msg.MessageID, msg.Receiver, models.MessageSendStatusFailed, models.FailureReasonMaxRetry, finalErr.Error())
		}
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

// deliverOnlineWithPolicy 在线投递执行器：按投递等级选择单发或重试链
//
// express 档：fire-and-forget 单次尝试，不构造 retry 链（退避参数/条件判断零开销）
// latest-wins 语义下同 key 下一条消息自然覆盖本条，重试毫无意义
// 可靠档：首次尝试内联直发（正常扇出成员首次即成功，重试链对象与链内 GetRuntimeCaller 调用者捕获是逐成员纯热路径浪费），仅首次失败且可重试时才构造退避重试链消化剩余次数
// presetNodes 仅首次有效：重试时用户可能已迁移节点，重试传 nil 重新查询
func (m *Manager) deliverOnlineWithPolicy(ctx context.Context, toUserID string, msg *models.HubMessage, presetNodes []string, result *models.SendResult, express bool) error {
	if express {
		return m.executeSendAttempt(ctx, toUserID, msg, presetNodes, result)
	}
	maxAttempts := m.host.GetConfig().RetryPolicy.MaxRetries + 1
	if err := m.executeSendAttempt(ctx, toUserID, msg, presetNodes, result); err == nil || len(result.Attempts) >= maxAttempts || !m.isRetryableError(err) {
		return err
	}
	return retry.NewRetryWithCtx(ctx).
		SetAttemptCount(maxAttempts - len(result.Attempts)).                // 剩余次数（首次已内联消耗）
		SetInterval(m.host.GetConfig().RetryPolicy.BaseDelay).              // 基础延迟
		SetMaxInterval(m.host.GetConfig().RetryPolicy.MaxDelay).            // 最大延迟
		SetBackoffMultiplier(m.host.GetConfig().RetryPolicy.BackoffFactor). // 退避倍数
		SetJitter(m.host.GetConfig().RetryPolicy.Jitter).                   // 是否启用抖动
		SetJitterPercent(m.host.GetConfig().RetryPolicy.JitterPercent).     // 抖动百分比
		SetConditionFunc(m.isRetryableError).                               // 重试条件判断
		Do(func() error {
			var nodes []string
			if len(result.Attempts) == 0 {
				nodes = presetNodes
			}
			return m.executeSendAttempt(ctx, toUserID, msg, nodes, result)
		})
}

// settleDeliveryFailure 投递终态失败收尾（express 与可靠档不同命）
//
// express：无 message_record 可更新、不转离线——语义丢弃计数（高频语义下的最终丢失）
// 可靠档：标记 Failed + 异步转存离线（消息不丢，用户上线时推送）
// 此前可靠档该路径仅依赖 30s 后的 ACK 超时扫描兜底：延迟窗口大、依赖扫描器存活
// 且 sendToUser fire-and-forget 谎报成功时连扫描器都捞不到（状态被误报 success 的路径除外）
// StoreOfflineOnDeliveryFailure 内部：转存成功覆盖状态为 UserOffline，失败保持 Failed
func (m *Manager) settleDeliveryFailure(ctx context.Context, msg *models.HubMessage, finalErr error, express bool) {
	if express {
		m.host.GetOverloadMetrics().RecordEphemeralDrop()
		return
	}
	m.updateMessageStatusAsync(ctx, msg.MessageID, msg.Receiver, models.MessageSendStatusFailed, models.FailureReasonMaxRetry, finalErr.Error())
	m.StoreOfflineOnDeliveryFailure(msg, finalErr)
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

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\hub\broadcast.go
 * @Description: Hub 统一投递入口与广播功能
 *
 * 本文件是消息投递的唯一公开入口（Deliver），以及广播类内部辅助的集中地：
 *   - Deliver：统一投递入口，路由全由 ctx + msg 决定，替代历史 SendToGroup/BroadcastToGroupMembers/
 *     BroadcastToGroup/BroadcastToAllGroups/BroadcastToAllNamespacesAllGroups/BroadcastToGroups/
 *     BroadcastToNamespace/Broadcast 八个割裂方法
 *   - 私有分派器：deliverP2P / deliverToGroupReliable / deliverToGroupFireForget /
 *     deliverToNamespace / deliverGlobally（由 Deliver 按决策树调用）
 *   - 跨节点辅助（仅保留有真实调用者的 2 个）：
 *     · crossNodeGroupBroadcast — deliverToGroupFireForget 调用，单群组跨节点
 *     · batchGetGroupMembers — distributed.go 跨节点接收侧调用，Pipeline 批量取成员
 *     （历史 crossNodeGroupsBroadcast / crossNodeMultiNamespaceGroupsBroadcast / resolveTargetGroups
 *      仅服务于已删除的 BroadcastToAllGroups/BroadcastToGroups，一并清理）
 *   - 内部广播：broadcastToFiltered / broadcastToUserIDs（预序列化 + 直接 TrySend）
 *   - 过滤类广播：BroadcastByUserType / BroadcastToRole / BroadcastToClientType / BroadcastToDepartment
 *     （按客户端属性过滤，与路由正交，保留）
 *   - 高级包装：BroadcastPriority / BroadcastAfterDelay / BroadcastExclude（基于 Deliver）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 统一投递入口
// ============================================================================

// Deliver 统一消息投递入口 — 路由全由 ctx + msg 决定，一套逻辑打通所有场景
//
// 路由元数据来源：ctx（appID/namespace/groupIDs）+ msg（Receiver/RequireAck）
// 调用方通过 routing.NewRoute().WithAppID(appID).WithNamespace(ns).WithGroupIDs(gids).Inject(ctx) 注入路由后调一次 Deliver。
//
// 决策树（按优先级）：
//  1. msg.Receiver != ""                          → P2P（SendToUserWithRetry，在线投递 + 离线存储 + 重试）
//  2. len(groupIDs) > 0:
//     - msg.RequireAck == true                   → 群组可靠投递（per-member SendToUserWithRetry + 离线存储）
//     - msg.RequireAck == false                  → 群组广播（fire-and-forget，broadcastToUserIDs + 跨节点）
//  3. namespace != ""                             → 命名空间广播（broadcastToFiltered + 跨节点 ns 广播）
//  4. namespace == ""                             → 全局广播（handleBroadcast + clusterBatcher）
//
// excludeSender 仅群组场景生效（P2P/广播场景 msg.Sender 不参与过滤）
//
// 返回非 nil *DeliverResult，错误收集到 result.Errors；按 result.Mode + 计数字段判断结果
func (h *Hub) Deliver(ctx context.Context, msg *HubMessage, excludeSender bool) *DeliverResult {
	// 统一入口：Clone + InjectRoute（注入 trace_id + 路由信封，appID 归一化，namespace 保留原值）
	// namespace 不在此归一化：空值在广播分支表示「全局广播」语义，需保留
	// P2P/群组分支需要 ns 严格非空，由各分支内部 EnsureRouteDefaults 兜底
	msg = msg.Clone()
	ctx = msg.InjectRoute(ctx)

	appID := routing.AppIDFromContext(ctx)
	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)

	switch {
	case msg.Receiver != "":
		return h.deliverP2P(ctx, msg, appID)
	case len(groupIDs) > 0:
		if msg.RequireAck {
			return h.deliverToGroupReliable(ctx, msg, excludeSender, appID, namespace, groupIDs)
		}
		return h.deliverToGroupFireForget(ctx, msg, excludeSender, appID, namespace, groupIDs)
	case namespace != "":
		return h.deliverToNamespace(ctx, msg, appID, namespace)
	default:
		return h.deliverGlobally(ctx, msg, appID)
	}
}

// ============================================================================
// 投递私有分派器（由 Deliver 按决策树调用）
// ============================================================================

// deliverP2P 点对点投递（msg.Receiver 非空）
// 委托 SendToUserWithRetry（内部已 EnsureRouteDefaults + InjectRoute，处理在线/离线/重试）
func (h *Hub) deliverP2P(ctx context.Context, msg *HubMessage, appID string) *DeliverResult {
	result := &DeliverResult{
		Mode:   DeliveryModeP2P,
		AppID:  appID,
		Errors: make([]error, 0),
	}

	sr := h.SendToUserWithRetry(ctx, msg.Receiver, msg)
	result.TotalMembers = 1
	if sr.StoredOffline {
		result.OfflineMembers = 1
		if sr.Success {
			result.StoredOffline = 1
		} else {
			result.Failed = 1
		}
	} else {
		result.OnlineMembers = 1
		if sr.Success {
			result.Sent = 1
		} else {
			result.Failed = 1
		}
	}
	if sr.FinalError != nil {
		result.AddError(fmt.Errorf("user %s: %w", msg.Receiver, sr.FinalError))
	}
	return result
}

// deliverToGroupReliable 群组可靠投递（RequireAck=true）
//
// 复用历史 SendToGroup 逻辑：per-member SendToUserWithRetry + 离线存储 + 重试
// 在线成员通过 SendToUserWithRetry 投递（自动支持跨节点路由与重试）
// 离线成员通过离线消息处理器存储，上线后自动推送
func (h *Hub) deliverToGroupReliable(ctx context.Context, msg *HubMessage, excludeSender bool, appID, namespace string, groupIDs []string) *DeliverResult {
	// 群组严格场景：EnsureRouteDefaults 归一化 namespace（空补 DefaultNamespace）
	ctx = routing.EnsureRouteDefaults(ctx)
	namespace = routing.NamespaceFromContext(ctx)

	result := &DeliverResult{
		Mode:      DeliveryModeGroupReliable,
		AppID:     appID,
		Namespace: namespace,
		GroupIDs:  groupIDs,
		Errors:    make([]error, 0),
	}

	if h.groupRepo == nil {
		result.AddError(ErrGroupRepoNotSet)
		return result
	}

	// msg 已 Clone（Deliver 入口），同步路由信封（namespace 已归一化）
	ctx = msg.ContextWithRoute(ctx, appID, namespace, groupIDs)

	// 1. 遍历所有 groupIDs 获取成员列表，合并去重
	seen := make(map[string]struct{}, len(groupIDs)*8)
	members := make([]string, 0, len(groupIDs)*8)
	for _, gid := range groupIDs {
		gMembers, err := h.groupRepo.GetMembers(ctx, appID, namespace, gid)
		if err != nil {
			result.AddError(err)
			h.logger.ErrorContextKV(ctx, "获取群组成员失败",
				"namespace", namespace, "group_id", gid, "error", err)
			continue
		}
		for _, uid := range gMembers {
			if _, ok := seen[uid]; !ok {
				seen[uid] = struct{}{}
				members = append(members, uid)
			}
		}
	}

	result.TotalMembers = len(members)
	if result.TotalMembers == 0 {
		return result
	}

	// 2. 过滤发送者（如需）
	filteredMembers := members
	if excludeSender && msg.Sender != "" {
		filteredMembers = mathx.FilterSlice(members, func(id string) bool {
			return id != msg.Sender
		})
		h.logger.DebugContextKV(ctx, "🔄 过滤发送者后的群组成员列表",
			"namespace", namespace,
			"group_ids", groupIDs,
			"original_count", len(members),
			"filtered_count", len(filteredMembers),
			"excluded_sender", msg.Sender,
		)
	}
	if len(filteredMembers) == 0 {
		return result
	}

	// 3. 并发投递消息 + 原子计数（消除序列化预检查 N 次 Redis 在线探测）
	// SendToUserWithRetry 内部已处理在线/离线逻辑，并通过 StoredOffline 标志返回分类信息
	var (
		sent          int64
		storedOffline int64
		failed        int64
		onlineCount   int64
		offlineCount  int64
		errMu         sync.Mutex
	)

	syncx.NewParallelSliceExecutor[string, *SendResult](filteredMembers).
		Execute(func(idx int, uid string) (*SendResult, error) {
			sendResult := h.SendToUserWithRetry(ctx, uid, msg)

			// 原子分类，无锁开销
			if sendResult.StoredOffline {
				atomic.AddInt64(&offlineCount, 1)
				if sendResult.Success {
					atomic.AddInt64(&storedOffline, 1)
				} else {
					atomic.AddInt64(&failed, 1)
				}
			} else {
				atomic.AddInt64(&onlineCount, 1)
				if sendResult.Success {
					atomic.AddInt64(&sent, 1)
				} else {
					atomic.AddInt64(&failed, 1)
				}
			}

			// 仅在有错误时加锁收集错误信息（错误是少数，锁竞争极低）
			if sendResult.FinalError != nil {
				errMu.Lock()
				result.AddError(fmt.Errorf("user %s: %w", uid, sendResult.FinalError))
				errMu.Unlock()
			}

			return sendResult, nil
		})

	result.OnlineMembers = int(atomic.LoadInt64(&onlineCount))
	result.OfflineMembers = int(atomic.LoadInt64(&offlineCount))
	result.Sent = int(atomic.LoadInt64(&sent))
	result.StoredOffline = int(atomic.LoadInt64(&storedOffline))
	result.Failed = int(atomic.LoadInt64(&failed))

	// � 通知观察者（ctx 已在上方 ContextWithRoute 注入路由，直接使用即可）
	h.notifyObservers(ctx, msg)

	h.logger.InfoContextKV(ctx, "✅ 群组消息投递完成",
		"namespace", namespace,
		"group_ids", groupIDs,
		"message_id", msg.MessageID,
		"total_members", result.TotalMembers,
		"online_members", result.OnlineMembers,
		"offline_members", result.OfflineMembers,
		"sent", result.Sent,
		"stored_offline", result.StoredOffline,
		"failed", result.Failed,
		"duration", time.Since(msg.CreateAt),
	)

	return result
}

// deliverToGroupFireForget 群组广播（RequireAck=false，fire-and-forget）
//
// 复用历史 BroadcastToGroupMembers 逻辑：本地 broadcastToUserIDs + 跨节点 crossNodeGroupBroadcast
// 仅投递当前在线成员，不存储离线消息，无重试，性能最优
func (h *Hub) deliverToGroupFireForget(ctx context.Context, msg *HubMessage, excludeSender bool, appID, namespace string, groupIDs []string) *DeliverResult {
	// 群组严格场景：EnsureRouteDefaults 归一化 namespace（空补 DefaultNamespace）
	ctx = routing.EnsureRouteDefaults(ctx)
	namespace = routing.NamespaceFromContext(ctx)

	result := &DeliverResult{
		Mode:      DeliveryModeGroupBroadcast,
		AppID:     appID,
		Namespace: namespace,
		GroupIDs:  groupIDs,
		Errors:    make([]error, 0),
	}

	if h.groupRepo == nil {
		h.logger.WarnContextKV(ctx, "群组仓库未设置，无法广播",
			"namespace", namespace, "group_ids", groupIDs)
		return result
	}

	// msg 已 Clone，同步路由信封（namespace 已归一化）
	ctx = msg.ContextWithRoute(ctx, appID, namespace, groupIDs)
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 1. 遍历所有 groupIDs 获取成员列表，合并去重
	seen := make(map[string]struct{}, len(groupIDs)*8)
	members := make([]string, 0, len(groupIDs)*8)
	for _, gid := range groupIDs {
		gMembers, err := h.groupRepo.GetMembers(ctx, appID, namespace, gid)
		if err != nil {
			h.logger.ErrorContextKV(ctx, "群组广播：获取群组成员失败",
				"namespace", namespace, "group_id", gid, "error", err)
			continue
		}
		for _, uid := range gMembers {
			if _, ok := seen[uid]; !ok {
				seen[uid] = struct{}{}
				members = append(members, uid)
			}
		}
	}

	result.TotalMembers = len(members)
	if len(members) == 0 {
		return result
	}

	// 2. 排除发送者后得到目标成员列表
	targetMembers := members
	if excludeSender && msg.Sender != "" {
		targetMembers = mathx.FilterSlice(members, func(id string) bool {
			return id != msg.Sender
		})
	}
	if len(targetMembers) == 0 {
		return result
	}

	// 3. 按成员ID查找本地连接并投递（O(m)，m=成员数，不遍历全部连接）
	localCount := h.broadcastToUserIDs(ctx, targetMembers, msg)

	// 🔔 通知观察者（ctx 已注入路由，直接使用）
	h.notifyObservers(ctx, msg)

	// 4. 跨节点广播：优先 gRPC 直连，降级 PubSub（ctx 已含完整路由）
	h.crossNodeGroupBroadcast(ctx, msg, excludeSender)

	h.logger.InfoContextKV(ctx, "📢 群组广播已发起",
		"namespace", namespace,
		"group_ids", groupIDs,
		"message_id", msg.MessageID,
		"total_members", len(members),
		"local_delivered", localCount,
		"grpc_enabled", h.IsGRPCEnabled(),
		"pubsub_enabled", h.pubsub != nil,
	)

	result.LocalDelivered = localCount
	return result
}

// deliverToNamespace 命名空间广播（namespace 非空，无 groupIDs）
//
// 复用历史 BroadcastToNamespace 逻辑：本地 broadcastToFiltered + 跨节点命名空间广播
// 本地按命名空间过滤广播，跨节点提交到 clusterBatcher
func (h *Hub) deliverToNamespace(ctx context.Context, msg *HubMessage, appID, namespace string) *DeliverResult {
	result := &DeliverResult{
		Mode:      DeliveryModeNamespace,
		AppID:     appID,
		Namespace: namespace,
		Errors:    make([]error, 0),
	}

	// msg 已 Clone + InjectRoute，信封已含 ns（非空）；ContextWithRoute 二次同步保证 msg.Namespace 与 ctx 一致
	ctx = msg.ContextWithRoute(ctx, appID, namespace, nil)

	// 本地按命名空间过滤广播（broadcastToFiltered 内部 combinedCondition 会叠加路由信封匹配，此处 condition 仅作业务兜底）
	count := h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.Namespace == namespace
	}, msg)

	// 🔔 通知观察者（命名空间级广播事件）
	h.notifyObservers(ctx, msg)

	// 跨节点命名空间广播（提交到 clusterBatcher 批量处理）
	opts := ClusterDispatchOptions{
		Operation: OperationTypeBroadcast,
		Namespace: namespace,
	}
	if !h.clusterBatcher.Submit(msg, opts) {
		h.logger.WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点命名空间广播",
			"namespace", namespace, "message_id", msg.MessageID)
	}

	result.LocalDelivered = count
	return result
}

// deliverGlobally 全局广播（namespace 为空，无 groupIDs）
//
// 复用历史 Broadcast 逻辑：设置 BroadcastTypeGlobal + 提交 clusterBatcher + 本地 handleBroadcast
// 全命名空间广播（不按命名空间过滤），跨节点通过 clusterBatcher 批量分发
func (h *Hub) deliverGlobally(ctx context.Context, msg *HubMessage, appID string) *DeliverResult {
	result := &DeliverResult{
		Mode:   DeliveryModeGlobal,
		AppID:  appID,
		Errors: make([]error, 0),
	}

	// 自动设置为全局广播类型
	msg.BroadcastType = mathx.IfEmpty(msg.BroadcastType, BroadcastTypeGlobal)
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 增加广播发送统计（原子计数器，由 flushStatsCounters 定时刷写到 Redis）
	if h.statsRepo != nil {
		h.broadcastSentCount.Add(1)
	}

	// 🌐 分布式广播：提交到 clusterBatcher 批量处理（消除 per-message goroutine）
	opts := ClusterDispatchOptions{
		Operation: OperationTypeBroadcast,
		Namespace: "", // 全命名空间广播
	}
	if !h.clusterBatcher.Submit(msg, opts) {
		h.logger.WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点广播",
			"message_id", msg.MessageID)
	}

	// 本地广播（直接异步执行，不经过 EventLoop channel 串行化）
	go h.handleBroadcast(msg)

	// 全局广播为异步，LocalDelivered 不计入（与历史 Broadcast 无返回值语义一致）
	return result
}

// crossNodeGroupBroadcast 跨节点群组广播（单群组）
//
// 统一走 OperationTypeGroupsBroadcast 批量路径，单群组作为 GroupIDs=[groupID] 的特例
// 提交到 clusterBatcher 批量处理，消除 per-message goroutine
// namespace/groupID 从 ctx 提取
func (h *Hub) crossNodeGroupBroadcast(ctx context.Context, msg *HubMessage, excludeSender bool) {
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return // 单机模式，无需跨节点
	}

	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)

	senderID := ""
	if excludeSender {
		senderID = msg.Sender
	}

	opts := ClusterDispatchOptions{
		Operation:     models.OperationTypeGroupBroadcast,
		Namespace:     namespace,
		GroupIDs:      groupIDs,
		ExcludeSender: excludeSender,
		SenderID:      senderID,
	}

	if !h.clusterBatcher.Submit(msg, opts) {
		h.logger.WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点群组广播",
			"namespace", namespace, "group_ids", groupIDs,
			"message_id", msg.MessageID)
	}
}

// batchGetGroupMembers 批量获取多个群组成员并合并去重
// 使用 Redis Pipeline 一次 RTT 获取所有群组成员，O(totalMembers) 去重
// 相比逐群组 N 次 GetMembers（N 次 RTT），降为 1 次 RTT
func (h *Hub) batchGetGroupMembers(ctx context.Context, appID, namespace string, groupIDs []string) map[string]struct{} {
	memberSet := make(map[string]struct{})
	if len(groupIDs) == 0 || h.groupRepo == nil {
		return memberSet
	}

	groupMembers, err := h.groupRepo.GetMultiGroupMembers(ctx, appID, namespace, groupIDs)
	if err != nil {
		h.logger.WarnContextKV(ctx, "批量获取群组成员失败",
			"app_id", appID, "namespace", namespace, "group_count", len(groupIDs), "error", err)
		return memberSet
	}

	for _, members := range groupMembers {
		for _, uid := range members {
			memberSet[uid] = struct{}{}
		}
	}
	return memberSet
}

// ============================================================================
// 内部分组广播辅助（预序列化 + 直接 TrySend）
// ============================================================================

// broadcastToFiltered 预序列化消息并直接发送给符合条件的客户端
// 消除逐客户端 Clone/序列化/入队/DB 记录开销：
//   - 消息只 json.Marshal 1 次（原方案每客户端 1 次）
//   - 不走 SendToUserWithRetry（原方案每客户端 Clone×2 + 在线检查 + 入队 + DB 记录）
//   - 零拷贝遍历（原方案 GetClientsCopy + FilterSlice 双重拷贝）
func (h *Hub) broadcastToFiltered(ctx context.Context, condition func(*Client) bool, msg *HubMessage) int {
	start := time.Now()
	// 🔏 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	// 覆盖所有上层入口：SendConditional / BroadcastByUserType / BroadcastToRole / BroadcastToClientType / Deliver 等
	ctx = msg.InjectRoute(ctx)

	// 预序列化 WebSocket 消息（仅一次）
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "分组广播消息序列化失败", "error", err)
		return 0
	}
	marshalDuration := time.Since(start)

	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	dataLen := len(data)

	// 并发数快照
	totalWSClients := h.shardedRegistry.GetClientCount()
	totalSSEClients := h.shardedRegistry.GetSSEClientCount()

	var successCount int32
	var wsScanned, sseScanned int64

	// 组合过滤条件：业务 condition + appId/namespace 隔离（ClientMatchesEnvelope 做 appId+namespace 严格匹配，
	// 不做 msg.GroupIDs vs client.GroupID 系统组匹配——两者维度不同，详见 ClientMatchesEnvelope 注释）
	// 所有 BroadcastByUserType / BroadcastToRole / BroadcastToClientType 等上层调用自动获得 appId+namespace 隔离
	combinedCondition := func(c *Client) bool {
		return ClientMatchesEnvelope(c, msg.AppID, msg.Namespace, msg.GroupIDs) && condition(c)
	}

	// WebSocket 客户端：直接 TrySend 预序列化数据（并行遍历优化百万级广播）
	wsStart := time.Now()
	h.shardedRegistry.ForEachClientParallel(0, func(_ string, client *Client) {
		atomic.AddInt64(&wsScanned, 1)
		if client.IsClosed() || client.ConnectionType == ConnectionTypeSSE {
			return
		}
		if !combinedCondition(client) {
			return
		}
		if client.TrySend(data) {
			atomic.AddInt32(&successCount, 1)
			h.trackReceiverMessageStats(client.ID, client.UserType, dataLen)
		}
	})
	wsDuration := time.Since(wsStart)

	// SSE 客户端：通过专用通道发送 msg 对象（无需序列化，并行遍历）
	sseStart := time.Now()
	h.shardedRegistry.ForEachSSEClientParallel(0, func(_, _ string, client *Client) {
		atomic.AddInt64(&sseScanned, 1)
		if client.IsClosed() || !combinedCondition(client) {
			return
		}
		if client.TrySendSSE(msg) {
			atomic.AddInt32(&successCount, 1)
		}
	})
	sseDuration := time.Since(sseStart)

	// 消息记录状态只更新一次（同一 msgID）
	totalSuccess := atomic.LoadInt32(&successCount)
	if totalSuccess > 0 {
		h.updateMessageStatusAsync(msgID, MessageSendStatusSuccess, "", "")
	}

	totalDuration := time.Since(start)
	h.logger.DebugContextKV(ctx, "📡 分组广播完成",
		"message_id", msg.MessageID,
		"data_bytes", dataLen,
		"total_ws_clients", totalWSClients,
		"total_sse_clients", totalSSEClients,
		"ws_scanned", atomic.LoadInt64(&wsScanned),
		"sse_scanned", atomic.LoadInt64(&sseScanned),
		"success", totalSuccess,
		"marshal_duration_ms", marshalDuration.Milliseconds(),
		"ws_duration_ms", wsDuration.Milliseconds(),
		"sse_duration_ms", sseDuration.Milliseconds(),
		"total_duration_ms", totalDuration.Milliseconds(),
	)

	return int(totalSuccess)
}

// broadcastToUserIDs 预序列化消息并直接发送给指定用户ID列表的在线客户端
// O(m) 复杂度（m=用户数），按成员ID反查 shardedRegistry，仅锁定相关 shard
// 相比 broadcastToFiltered 的 O(n)（n=总连接数），群组广播场景大幅减少遍历与锁范围
// 适用于已知目标用户ID列表的场景（群组广播、多群组广播）
func (h *Hub) broadcastToUserIDs(ctx context.Context, userIDs []string, msg *HubMessage) int {
	if len(userIDs) == 0 {
		return 0
	}
	// 🔏 路由信封同步：从 ctx 恢复路由到 msg 信封（幂等）
	// 上游 deliverToGroupFireForget 已注入，此处作为二次兜底
	// InjectRoute 同时回写 ctx，保证下游 ctx 与信封一致
	ctx = msg.InjectRoute(ctx)

	// 预序列化 WebSocket 消息（仅一次）
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "群组广播消息序列化失败", "error", err)
		return 0
	}

	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	dataLen := len(data)
	var successCount int32

	// 按用户ID查找客户端（O(m)，仅锁定相关 shard，不遍历全部连接）
	// 使用 ForEachUserClientFiltered 叠加路由信封(appId+namespace)匹配：
	//   - 群组消息：client 必须同 app/ns 且 client.groupID 在 msg.GroupIDs 中
	//   - 避免新加入群的成员通过"旧 userIDs 列表"收到不匹配其 group 的历史消息（与项目约束一致）
	for _, userID := range userIDs {
		h.shardedRegistry.ForEachUserClientFiltered(userID, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *Client) bool {
			if client.IsClosed() {
				return true
			}
			if client.ConnectionType == ConnectionTypeSSE {
				// SSE 客户端发送 msg 对象
				if client.TrySendSSE(msg) {
					atomic.AddInt32(&successCount, 1)
				}
			} else {
				// WebSocket 客户端发送预序列化数据
				if client.TrySend(data) {
					atomic.AddInt32(&successCount, 1)
					h.trackReceiverMessageStats(client.ID, client.UserType, dataLen)
				}
			}
			return true
		})
	}

	totalSuccess := atomic.LoadInt32(&successCount)
	if totalSuccess > 0 {
		h.updateMessageStatusAsync(msgID, MessageSendStatusSuccess, "", "")
	}

	return int(totalSuccess)
}

// ============================================================================
// 过滤类广播方法（按客户端属性过滤，与路由正交，保留）
// ============================================================================

// BroadcastByUserType 发送消息给特定用户类型的所有客户端
func (h *Hub) BroadcastByUserType(ctx context.Context, userType UserType, msg *HubMessage) int {
	return h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.UserType == userType
	}, msg)
}

// BroadcastToRole 发送消息给特定角色的所有用户
func (h *Hub) BroadcastToRole(ctx context.Context, role UserRole, msg *HubMessage) int {
	return h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.Role == role
	}, msg)
}

// BroadcastToClientType 发送消息给特定客户端类型
func (h *Hub) BroadcastToClientType(ctx context.Context, clientType ClientType, msg *HubMessage) int {
	return h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.ClientType == clientType
	}, msg)
}

// BroadcastToDepartment 发送消息给特定部门的所有用户
func (h *Hub) BroadcastToDepartment(ctx context.Context, department Department, msg *HubMessage) int {
	return h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.Department == department
	}, msg)
}

// ============================================================================
// 高级广播包装（基于 Deliver）
// ============================================================================

// BroadcastPriority 根据优先级广播消息（全局广播，走 Deliver）
func (h *Hub) BroadcastPriority(ctx context.Context, msg *HubMessage, priority Priority) {
	msg.Priority = priority
	h.Deliver(ctx, msg, false)
}

// BroadcastAfterDelay 延迟广播消息（全局广播，走 Deliver）
func (h *Hub) BroadcastAfterDelay(ctx context.Context, msg *HubMessage, delay time.Duration) {
	syncx.Go(ctx).
		WithDelay(delay).
		Exec(func() {
			h.Deliver(ctx, msg, false)
		})
}

// BroadcastExclude 广播消息给所有客户端，但排除指定用户
func (h *Hub) BroadcastExclude(ctx context.Context, msg *HubMessage, excludeUserIDs []string) int {
	excludeMap := make(map[string]struct{}, len(excludeUserIDs))
	for _, userID := range excludeUserIDs {
		excludeMap[userID] = struct{}{}
	}

	return h.SendConditional(ctx, func(c *Client) bool {
		_, excluded := excludeMap[c.UserID]
		return !excluded
	}, msg)
}

// ============================================================================
// 获取客户端列表方法
// ============================================================================

// GetClientsByUserType 获取特定用户类型的所有客户端（委托 FilterClients 零拷贝）
func (h *Hub) GetClientsByUserType(userType UserType) []*Client {
	return h.FilterClients(func(c *Client) bool { return c.UserType == userType })
}

// GetClientsByRole 获取特定角色的所有客户端（委托 FilterClients 零拷贝）
func (h *Hub) GetClientsByRole(role UserRole) []*Client {
	return h.FilterClients(func(c *Client) bool { return c.Role == role })
}

// GetClientsByClientType 按客户端类型获取客户端（委托 FilterClients 零拷贝）
func (h *Hub) GetClientsByClientType(clientType ClientType) []*Client {
	return h.FilterClients(func(c *Client) bool { return c.ClientType == clientType })
}

// GetClientsByDepartment 获取特定部门的所有客户端（委托 FilterClients 零拷贝）
func (h *Hub) GetClientsByDepartment(department Department) []*Client {
	return h.FilterClients(func(c *Client) bool { return c.Department == department })
}

// GetClientsByVIPLevel 获取特定VIP等级及以上的客户端（委托 FilterClients 零拷贝）
func (h *Hub) GetClientsByVIPLevel(minVIPLevel VIPLevel) []*Client {
	minLevel := minVIPLevel.GetLevel()
	return h.FilterClients(func(c *Client) bool { return c.GetVIPLevel().GetLevel() >= minLevel })
}

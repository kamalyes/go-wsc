/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:15:30
 * @FilePath: \go-wsc\hub\interfaces.go
 * @Description: Hub 端口方法实现 —— 各域 Host 端口的委托层
 *
 * 隐式满足 messaging/stats/group/overload 四域定义的 Host 端口接口
 * （消费者定义接口原则，不写接口断言）：基础环境/仓储/批处理器为字段直返，
 * 集群/统计/过载/SSE/群组方法转发到对应域管理器或运行时组件。
 * 另隐式满足 spi.StoreTarget 装配能力面（见下方 StoreTarget 区块）。
 * 未注入的组件按端口契约返回 nil / 零值，域内自行判空降级。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync/atomic"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/json"

	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 基础环境（messaging.Host / stats.Host / group.Host / batcher.StorageBatchWriter 共用）
// ============================================================================

// Context 返回 Hub 生命周期上下文
func (h *Hub) Context() context.Context { return h.ctx }

// GetLogger 日志器
func (h *Hub) GetLogger() spi.Logger { return h.logger }

// GetNodeID 当前节点 ID
func (h *Hub) GetNodeID() string { return h.nodeID }

// GetConfig 全局配置
func (h *Hub) GetConfig() *wscconfig.WSC { return h.config }

// GetStartTime Hub 启动时间
func (h *Hub) GetStartTime() time.Time { return h.startTime }

// IsStarted Hub 是否已启动
func (h *Hub) IsStarted() bool { return h.started.Load() }

// IsShuttingDown Hub 是否正在关闭（读泵据此区分服务端主动断开与异常断开）
func (h *Hub) IsShuttingDown() bool { return h.shutdown.Load() }

// IsShutdown 编排层是否正在关闭（transport.Registrar 端口，关闭中拒绝新连接）
func (h *Hub) IsShutdown() bool { return h.shutdown.Load() }

// HasPubsub 是否配置分布式发布订阅（与 gRPC 共同构成跨节点通道开关）
func (h *Hub) HasPubsub() bool { return h.pubsub != nil }

// IsGRPCEnabled 集群 gRPC 通道是否启用（配置开关判空，节点注册表由集群域初始化）
func (h *Hub) IsGRPCEnabled() bool {
	return h.config != nil && h.config.NodeGRPC.IsEnabled()
}

// ============================================================================
// 注册表（messaging.Host / stats.Host / group.Host 共用）
// ============================================================================

// GetShardedRegistry 分片连接注册表（核心运行时结构，恒注入）
func (h *Hub) GetShardedRegistry() *connection.ShardedRegistry { return h.shardedRegistry }

// ============================================================================
// SPI 仓储（未注入即 nil，域内自行判空降级）
// ============================================================================

// GetMessageSink 消息记录仓储
func (h *Hub) GetMessageSink() spi.MessageSink { return h.messageSink }

// GetMessageRecordRepo 消息记录仓储（batcher.StorageBatchWriter 端口名）
func (h *Hub) GetMessageRecordRepo() spi.MessageSink { return h.messageSink }

// GetGroupRepo 群组仓储（messaging.Host 端口名）
func (h *Hub) GetGroupRepo() spi.GroupStore { return h.groupStore }

// GetGroupStore 群组仓储（group.Host 端口名）
func (h *Hub) GetGroupStore() spi.GroupStore { return h.groupStore }

// GetStatsRepo 节点统计仓储（兼作消息计数开关）
func (h *Hub) GetStatsRepo() spi.HubStats { return h.statsRepo }

// GetOnlineStatusRepo 在线状态仓储
func (h *Hub) GetOnlineStatusRepo() spi.OnlineStore { return h.onlineStatusRepo }

// GetConnectionQualityRepository 连接质量仓储
func (h *Hub) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	return h.connectionQualityStore
}

// GetConnectionRecordRepo 连接记录仓储
func (h *Hub) GetConnectionRecordRepo() spi.ConnectionStore { return h.connectionStore }

// GetWorkloadStore 客服负载存储（未注入时调用方须报错而非 no-op）
func (h *Hub) GetWorkloadStore() spi.WorkloadStore { return h.workloadStore }

// ============================================================================
// spi.StoreTarget 装配落点（适配器 hooks 经 spi.Initialize / messaging.InitializeOfflineQueue 注入）
//
// 能力面签名由 spi.StoreTarget 接口约定（无返回值），与 options.go 的链式
// With* 注入等价：仓储 setter 直写 Hub 字段，离线处理器 setter 直通
// messagingMgr.WithOfflineHandler —— 消费者是消息域，持有者职责由其承担。
// ============================================================================

// SetOnlineStatusRepository 注入在线状态仓储（StoreTarget 能力面）
func (h *Hub) SetOnlineStatusRepository(store spi.OnlineStore) { h.onlineStatusRepo = store }

// SetHubStatsRepository 注入 Hub 统计仓储（StoreTarget 能力面）
func (h *Hub) SetHubStatsRepository(store spi.HubStats) { h.statsRepo = store }

// SetGroupRepository 注入群组仓储（StoreTarget 能力面）
func (h *Hub) SetGroupRepository(store spi.GroupStore) { h.groupStore = store }

// SetWorkloadRepository 注入客服负载仓储（StoreTarget 能力面）
func (h *Hub) SetWorkloadRepository(store spi.WorkloadStore) { h.workloadStore = store }

// SetMessageRecordRepository 注入消息记录仓储（StoreTarget 能力面）
func (h *Hub) SetMessageRecordRepository(sink spi.MessageSink) { h.messageSink = sink }

// SetConnectionRecordRepository 注入连接记录仓储（StoreTarget 能力面）
func (h *Hub) SetConnectionRecordRepository(store spi.ConnectionStore) { h.connectionStore = store }

// SetConnectionQualityRepository 注入连接质量仓储（StoreTarget 能力面）
func (h *Hub) SetConnectionQualityRepository(store spi.ConnectionQualityStore) {
	h.connectionQualityStore = store
}

// SetOfflineMessageHandler 注入离线消息处理器（StoreTarget 能力面，直通 messagingMgr）
//
// 处理器实现 spi.OfflineQueue 契约，持久化全部经注入的共享存储
// （Redis 队列 + RDBMS，Deployment 滚动更新下跨 Pod 可见，Pod 本地无状态）；
// 便捷组装见 messaging.InitializeOfflineQueue
func (h *Hub) SetOfflineMessageHandler(queue spi.OfflineQueue) {
	h.messagingMgr.WithOfflineHandler(queue)
}

// ============================================================================
// 批处理器（stats.Host / messaging.Host）
// ============================================================================

// GetMessageStatsBatcher 消息统计批量聚合器
func (h *Hub) GetMessageStatsBatcher() *batcher.MessageStatsBatcher {
	return h.messageStatsBatcher
}

// GetHeartbeatBatcher 心跳统计批量聚合器
func (h *Hub) GetHeartbeatBatcher() *batcher.HeartbeatStatsUpdater {
	return h.heartbeatBatcher
}

// GetMessageStatusUpdater 消息状态批量更新器
func (h *Hub) GetMessageStatusUpdater() *batcher.MessageStatusUpdater { return h.statusUpdater }

// ============================================================================
// 集群域（messaging.Host；unexported 实现由集群域文件提供）
// ============================================================================

// RouteToCluster 路由消息到集群其他节点
func (h *Hub) RouteToCluster(ctx context.Context, msg *models.HubMessage, opts cluster.ClusterDispatchOptions) error {
	return h.routeToCluster(ctx, msg, opts)
}

// CheckAndRouteToNode 检查用户在线节点并按需跨节点投递
// 返回：是否命中跨节点路由、目标节点列表、错误
func (h *Hub) CheckAndRouteToNode(ctx context.Context, userID string, msg *models.HubMessage) (bool, []string, error) {
	return h.checkAndRouteToNode(ctx, userID, msg)
}

// GetAllClusterNodeIDs 获取集群全部节点 ID
func (h *Hub) GetAllClusterNodeIDs() []string { return h.getAllClusterNodeIDs() }

// MarkRerouteAttempted 标记消息已尝试重路由（防循环投递）
func (h *Hub) MarkRerouteAttempted(messageID string, targetNodes []string, p2p bool) {
	h.markRerouteAttempted(messageID, targetNodes, p2p)
}

// DeleteRerouteGuard 清理重路由守卫条目（消息到达终态时调用）
func (h *Hub) DeleteRerouteGuard(messageID string) { h.rerouteGuard.Delete(messageID) }

// SubmitClusterDispatch 提交集群批量分发任务到分布式池（队列满时返回 false）
// 分发沿用消息路由信封上下文，保留 trace_id 实现全链路追踪
func (h *Hub) SubmitClusterDispatch(msg *models.HubMessage, opts cluster.ClusterDispatchOptions) bool {
	if msg == nil {
		return false
	}
	return h.workerPool.TrySubmitDistributed(func() {
		if err := h.routeToCluster(msg.ContextFrom(h.ctx), msg, opts); err != nil {
			h.logger.WarnContextKV(h.ctx, "集群分发任务执行失败",
				"message_id", msg.MessageID,
				"error", err,
			)
		}
	})
}

// ============================================================================
// 统计域服务（messaging.Host）
// ============================================================================

// CheckUserOnline 检查用户是否在线（跨节点汇总判定）
func (h *Hub) CheckUserOnline(ctx context.Context, userID string) bool {
	return h.statsMgr.CheckUserOnline(ctx, userID)
}

// TrackReceiverMessageStats 投递接收方消息统计
func (h *Hub) TrackReceiverMessageStats(connectionID string, receiverType models.UserType, dataSize int) {
	h.statsMgr.TrackReceiverMessageStats(connectionID, receiverType, dataSize)
}

// TrackConnectionError 记录连接错误（异常断开排查）
func (h *Hub) TrackConnectionError(ctx context.Context, connectionID string, userType models.UserType, err error) {
	h.statsMgr.TrackConnectionError(ctx, connectionID, userType, err)
}

// ============================================================================
// 观察者通知（messaging.Host / batcher.ObserverNotifier）
// ============================================================================

// NotifyObservers 通知观察者（观察者未启用时为 no-op）
// 从 ctx 提取 namespace+groupIDs 定位观察范围，提交批量处理器攒批投递
func (h *Hub) NotifyObservers(ctx context.Context, msg *models.HubMessage) {
	if msg == nil || !h.shardedRegistry.ObserverEnabled() {
		return
	}
	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	// msg 会在 Submit 内 Clone，避免调用方修改影响异步 flush
	if !h.observerBatcher.Submit(msg, namespace, groupIDs) {
		h.logger.DebugContextKV(ctx, "观察者通知队列已满，丢弃",
			"message_id", msg.MessageID,
			"namespace", namespace,
			"group_ids", groupIDs,
		)
	}
}

// NotifyObserversDirect 直接通知观察者（不经批处理队列，避免递归入队）
// 由 observerBatcher flush 调用：本地观察者投递 + 跨节点广播，无 per-message goroutine
func (h *Hub) NotifyObserversDirect(msg *models.HubMessage, namespace string, groupIDs []string) {
	if msg == nil {
		return
	}
	ctx := routing.NewRoute().WithAppID(msg.AppID).WithNamespace(namespace).WithGroupIDs(groupIDs).Inject(h.ctx)

	// 快速检查：无观察者时仅跨节点广播 - O(1)
	if h.shardedRegistry.GetObserverUserCount() == 0 {
		h.broadcastObserverNotification(ctx, msg)
		return
	}

	// 三级索引查找：合并所有 groupIDs 的观察者并去重
	observers := h.shardedRegistry.GetObserversForMessage(namespace, groupIDs...)

	// 预构建观察者专用消息（Clone + metadata），所有观察者共享同一份
	observerMsg := msg.Clone()
	observerMsg.WithMetadata("observer_mode", "true")
	observerMsg.WithMetadata("original_sender", msg.Sender)
	observerMsg.WithMetadata("original_receiver", msg.Receiver)

	// 预序列化一次（所有观察者复用，消除逐个 Clone+Marshal 开销）
	msgData, err := json.Marshal(observerMsg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "序列化观察者消息失败",
			"message_id", msg.MessageID,
			"error", err,
		)
		return
	}

	delivered := 0
	for _, observer := range observers {
		if observer.TrySend(msgData) {
			delivered++
		} else {
			h.logger.WarnContextKV(ctx, "观察者缓冲区已满或已关闭，丢弃消息",
				"observer_id", observer.UserID,
				"client_id", observer.ID,
				"message_id", observerMsg.MessageID,
			)
		}
	}

	h.logger.DebugContextKV(ctx, "已通知本地观察者",
		"message_id", observerMsg.MessageID,
		"total_devices", len(observers),
		"delivered", delivered,
	)

	h.broadcastObserverNotification(ctx, msg)
}

// broadcastObserverNotification 广播观察者通知到其他节点
// 统一走 routeToCluster 入口，由其集中决策 gRPC 直连与 PubSub 兜底
func (h *Hub) broadcastObserverNotification(ctx context.Context, msg *models.HubMessage) {
	// 单机模式：无 PubSub 且无 gRPC，不跨节点
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return
	}

	// 从传入 ctx 派生超时 ctx，保留 trace_id 等元数据
	// 如果传入 ctx 已取消（如 Hub 关闭场景），fallback 到 Background 确保 dispatch 能完成
	parentCtx := ctx
	if parentCtx == nil || parentCtx.Err() != nil {
		parentCtx = context.Background()
	}
	dispatchCtx, cancel := context.WithTimeout(parentCtx, 3*time.Second)
	defer cancel()

	opts := cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeObserverNotify,
		Namespace: routing.NamespaceFromContext(ctx),
		GroupIDs:  routing.GroupIDsFromContext(ctx),
	}

	if err := h.routeToCluster(dispatchCtx, msg, opts); err != nil {
		h.logger.WarnContextKV(ctx, "广播观察者通知失败",
			"error", err,
			"message_id", msg.MessageID,
		)
	}
}

// ============================================================================
// 过载保护域（messaging.Host）
// ============================================================================

// AdmitMessage 消息级准入裁决（闸门未启用时恒放行）
// 热路径契约：零分配（枚举返回）；isBroadcast 标识广播路径（L2 阶梯差异化）
func (h *Hub) AdmitMessage(msg *models.HubMessage, isBroadcast bool) overload.AdmitVerdict {
	h.overloadMetrics.RecordAdmitted(msg.ResolveGuarantee())
	gate := h.admission.Load()
	if gate == nil {
		return overload.VerdictAdmit
	}
	verdict := gate.Admit(msg, isBroadcast)
	h.overloadMetrics.RecordAdmissionVerdict(verdict)
	return verdict
}

// AdmissionOnDelivered 投递完成埋点（闸门未启用时 no-op）
func (h *Hub) AdmissionOnDelivered() {
	if gate := h.admission.Load(); gate != nil {
		gate.OnDelivered()
	}
}

// OnWriteBatch 写泵批量埋点（n = 本批消息数，闸门未启用时仅记漏斗计数）
func (h *Hub) OnWriteBatch(n int) {
	h.overloadMetrics.RecordWriteBatch(n)
	if gate := h.admission.Load(); gate != nil {
		gate.OnWriteBatch(n)
	}
}

// GetAdmissionLevel 当前过载水位（闸门未启用时返回 LevelNormal）
func (h *Hub) GetAdmissionLevel() overload.OverloadLevel {
	if gate := h.admission.Load(); gate != nil {
		return gate.Level()
	}
	return overload.LevelNormal
}

// DeferBroadcast 广播延迟重投（延迟队列，拒绝不等于丢弃）
func (h *Hub) DeferBroadcast(ctx context.Context, msg *models.HubMessage, retry func(context.Context, *models.HubMessage)) {
	if h.broadcastDelayQueue == nil || msg == nil {
		return
	}
	if !h.broadcastDelayQueue.Offer(ctx, msg, retry) {
		h.logger.DebugContextKV(ctx, "广播延迟队列已满，丢弃延迟重投",
			"message_id", msg.MessageID,
		)
	}
}

// GetBroadcastShaper 广播出向整形器（未启用时返回 nil）
func (h *Hub) GetBroadcastShaper() *overload.Shaper { return h.broadcastShaper.Load() }

// GetEphemeralCoalescer 高频消息合并器（未启用时返回 nil）
func (h *Hub) GetEphemeralCoalescer() *overload.Coalescer { return h.ephemeralCoalescer.Load() }

// GetOverloadMetrics 过载漏斗指标（编排层恒注入）
func (h *Hub) GetOverloadMetrics() *overload.OverloadMetrics { return &h.overloadMetrics }

// ============================================================================
// SSE 通道（messaging.Host）
// ============================================================================

// SendToUserViaSSE 经 SSE 通道向用户投递（SSE 未启用或用户无订阅时返回 false）
// namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的 SSE 设备，避免跨 ns 串扰
func (h *Hub) SendToUserViaSSE(userID string, msg *models.HubMessage) bool {
	if msg == nil {
		return false
	}
	// 快速检查用户是否有 SSE 连接（O(1)）
	if !h.shardedRegistry.HasSSEUser(userID) {
		return false
	}

	// 持读锁零拷贝遍历发送
	successCount := 0
	totalDevices := 0
	h.shardedRegistry.ForEachSSEUserClient(userID, func(clientID string, client *models.Client) bool {
		// namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的设备
		if msg.Namespace != "" && client.Namespace != msg.Namespace {
			return true
		}
		totalDevices++
		if client.TrySendSSE(msg) {
			client.SetLastSeen(time.Now())
			successCount++
		} else {
			h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "SSE消息队列已满",
				"user_id", userID,
				"client_id", clientID,
				"message_id", msg.MessageID,
				"message_type", msg.MessageType,
			)
		}
		return true
	})

	if successCount > 0 {
		h.logger.InfoContextKV(msg.ContextFrom(h.ctx), "SSE消息发送成功",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
			"success_devices", successCount,
			"total_devices", totalDevices,
		)
		return true
	}
	return false
}

// BroadcastToSSEClients 广播给全部 SSE 客户端（appId/namespace 信封隔离）
// 通过 ForEachSSEClientParallel 并行分片读锁遍历（百万级优化）
func (h *Hub) BroadcastToSSEClients(msg *models.HubMessage) {
	if msg == nil {
		return
	}
	// 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	msg.InjectRoute(h.ctx)

	start := time.Now()
	var sent, skipped int64
	h.shardedRegistry.ForEachSSEClientParallel(0, func(_, clientID string, client *models.Client) {
		if !connection.ClientMatchesEnvelope(client, msg.AppID, msg.Namespace, msg.GroupIDs) {
			return
		}
		if client.TrySendSSE(msg) {
			client.SetLastSeen(time.Now())
			atomic.AddInt64(&sent, 1)
		} else {
			atomic.AddInt64(&skipped, 1)
			h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "SSE客户端消息通道已满，跳过",
				"client_id", clientID,
				"message_id", msg.MessageID,
			)
		}
	})

	h.logger.DebugContextKV(msg.ContextFrom(h.ctx), "SSE广播完成",
		"message_id", msg.MessageID,
		"namespace", msg.Namespace,
		"total_sse_clients", h.shardedRegistry.GetSSEClientCount(),
		"sent", atomic.LoadInt64(&sent),
		"skipped", atomic.LoadInt64(&skipped),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

// ============================================================================
// 群组域（group.Host / overload.UserMessageSender）
// ============================================================================

// GetOnlineUsersByType 按用户类型查询在线用户（负载分配需要在线客服列表）
func (h *Hub) GetOnlineUsersByType(userType models.UserType) ([]string, error) {
	return h.shardedRegistry.GetOnlineUsersByType(userType)
}

// TrySubmitCallback 提交一个异步回调任务（队列满时返回 false）
// 群组生命周期回调走此处，避免每条连接一个 goroutine
func (h *Hub) TrySubmitCallback(task func()) bool {
	return h.workerPool.TrySubmitCallback(task)
}

// GetGroupDisbandCallback 群组解散回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetGroupDisbandCallback() func(ctx context.Context, namespace, groupID string) {
	return h.groupDisbandCallback
}

// GetGroupMemberJoinCallback 群组成员加入回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetGroupMemberJoinCallback() func(ctx context.Context, namespace, groupID string, userIDs []string) {
	return h.groupMemberJoinCallback
}

// GetGroupMemberLeaveCallback 群组成员离开回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetGroupMemberLeaveCallback() func(ctx context.Context, namespace, groupID string, userIDs []string) {
	return h.groupMemberLeaveCallback
}

// SendConditional 条件广播：对满足条件的在线客户端各投递一份，返回投递数
func (h *Hub) SendConditional(ctx context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int {
	return h.messagingMgr.SendConditional(ctx, condition, msg)
}

// SendToUserWithRetry 点对点发送（带重试与路由注入），失败详情在返回值中
func (h *Hub) SendToUserWithRetry(ctx context.Context, toUserID string, msg *models.HubMessage) *models.SendResult {
	return h.messagingMgr.SendToUserWithRetry(ctx, toUserID, msg)
}

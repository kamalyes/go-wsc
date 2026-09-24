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
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"

	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
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
	return h.batcherMgr.MessageStats()
}

// GetHeartbeatBatcher 心跳统计批量聚合器
func (h *Hub) GetHeartbeatBatcher() *batcher.HeartbeatStatsUpdater {
	return h.batcherMgr.HeartbeatStats()
}

// GetMessageStatusUpdater 消息状态批量更新器
func (h *Hub) GetMessageStatusUpdater() *batcher.MessageStatusUpdater {
	return h.batcherMgr.StatusUpdater()
}

// ============================================================================
// 集群域（messaging.Host；unexported 实现由集群域文件提供）
// ============================================================================

// RouteToCluster 路由消息到集群其他节点
func (h *Hub) RouteToCluster(ctx context.Context, msg *models.HubMessage, opts cluster.ClusterDispatchOptions) error {
	return h.routeToCluster(ctx, msg, opts)
}

// GetUserNodes 查询用户所在的全部节点（路由索引，供消息域在线判定与路由共享单次往返）
func (h *Hub) GetUserNodes(ctx context.Context, userID string) []string {
	if h.onlineStatusRepo == nil {
		return nil
	}
	nodes, err := h.queryUserNodes(ctx, userID)
	if err != nil {
		return nil
	}
	return nodes
}

// CheckAndRouteToNode 检查用户在线节点并按需跨节点投递
// presetNodes 非空时跳过节点查询（调用方已预取）
// 返回：是否命中跨节点路由、目标节点列表、错误
func (h *Hub) CheckAndRouteToNode(ctx context.Context, userID string, msg *models.HubMessage, presetNodes []string) (bool, []string, error) {
	return h.checkAndRouteToNode(ctx, userID, msg, presetNodes)
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

// GetMessageRecordOutbox 消息记录攒批 outbox（write-ahead INSERT 攒批化）
func (h *Hub) GetMessageRecordOutbox() *batcher.MessageRecordOutbox {
	return h.batcherMgr.RecordOutbox()
}

// CheckUserOnline 检查用户是否在线（跨节点汇总判定；对外诊断 API，
// 消息发送热路径已合并为 GetUserNodes 单次往返，不再走此方法）
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
// 心跳子系统（messaging.Host 的 HandleHeartbeat + connection.HeartbeatHost 端口）
//
// 心跳逻辑（时间轮 O(1) 超时 + SSE 兜底扫描 + 回调链）在连接域
// HeartbeatManager，此处仅做端口委托；回调 getter 返回 nil 表示
// 业务方未注册，域内跳过（不可假定非空）
// ============================================================================

// HandleHeartbeat 处理心跳消息（messaging.Host 端口委托）
// 协议级 PING 与应用层心跳共用同一保活入口，逻辑在连接域心跳管理器
func (h *Hub) HandleHeartbeat(client *models.Client) {
	h.heartbeatMgr.Handle(client)
}

// TrackHeartbeatStats 投递心跳统计（connection.HeartbeatHost 端口，stats 域漏斗）
func (h *Hub) TrackHeartbeatStats(client *models.Client) {
	h.statsMgr.TrackHeartbeatStats(client)
}

// EnqueueHeartbeatRenew 心跳 Redis 在线索引续期入队（connection.HeartbeatHost 端口）
// 单 goroutine worker 消费 heartbeatRedisCh，满则丢弃（心跳下次还会来）
func (h *Hub) EnqueueHeartbeatRenew(client *models.Client) {
	if h.onlineStatusRepo == nil {
		return
	}
	select {
	case h.heartbeatRedisCh <- client:
	default:
		// channel 满，跳过本次 Redis 更新
	}
}

// GetBeforeHeartbeatCallback 心跳前置回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetBeforeHeartbeatCallback() func(client *models.Client) bool {
	return h.callbacks.BeforeHeartbeat
}

// GetHeartbeatReportCallback 心跳上报回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetHeartbeatReportCallback() func(client *models.Client) {
	return h.callbacks.HeartbeatReport
}

// GetAfterHeartbeatCallback 心跳后置回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetAfterHeartbeatCallback() func(client *models.Client) {
	return h.callbacks.AfterHeartbeat
}

// GetHeartbeatTimeoutCallback 心跳超时回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetHeartbeatTimeoutCallback() func(clientID string, userID string, lastHeartbeat time.Time) {
	return h.callbacks.HeartbeatTimeout
}

// ============================================================================
// 连接生命周期（connection.LifecycleHost 端口 + 对外踢人 API）
//
// 多端登录治理 / 踢出断链 / 精简移除逻辑在连接域 LifecycleManager，
// 此处仅做端口委托：KickClient 的 ForceOffline 通知经 SendToClient 端口
// 走消息域投递，保证踢出消息与普通消息同一写泵有序写出
// ============================================================================

// SendToClient 定向单发（connection.LifecycleHost 端口，委托消息域）
// 踢出通知 / 注册确认等系统消息与业务消息共用同一投递路径
func (h *Hub) SendToClient(ctx context.Context, client *models.Client, msg *models.HubMessage) {
	h.messagingMgr.SendToClient(ctx, client, msg)
}

// Deliver 统一投递入口（P2P / 群组 / 广播决策树，委托消息域）
// 路由维度（appID/namespace/groupIDs）经 routing.NewRoute() 链式构建器注入 ctx：
//   - P2P：msg.Receiver 非空触发（在线投递 + 离线存储 + 重试）
//   - 群组可靠投递：msg.RequireAck=true 触发（per-member 重试 + 离线存储）
//   - 命名空间/全局广播：ctx 信封 namespace 决策（空=全局）
//
// excludeSender：群组/广播场景排除发送者自身连接
func (h *Hub) Deliver(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
	return h.messagingMgr.Deliver(ctx, msg, excludeSender)
}

// KickUser 统一踢出用户全部连接（踢人唯一入口，按 ctx 路由信封 appID+namespace 隔离）
//
// 本地：踢出本节点上该用户信封内全部连接；sendNotification=true 时断链前向全部
// 连接写入 KickOut 通知（Guaranteed 级控制消息，notificationMsg 为通知文案）
//
// 幂等语义：用户已无连接（收集数 0）即"已离线"目标达成，KickedConnections=0
// 不视为失败，调用方据此区分"真踢到"与"本来就不在线"
//
// 跨节点：经在线路由索引查询用户连接所在的远端节点，异步分发 kick 指令
// （gRPC 直连优先、PubSub 兜底）；远端节点按同一信封隔离静默踢出
// （kick 指令不携带通知文案，通知仅由本节点发出）
func (h *Hub) KickUser(ctx context.Context, userID, reason string, sendNotification bool, notificationMsg string) *models.KickUserResult {
	// 本地踢出（连接域统一实现：信封隔离收集 + 可选 KickOut 通知 + 注销）
	result := h.lifecycleMgr.KickUser(ctx, userID, reason, sendNotification, notificationMsg)

	// 跨节点分发：向远端节点的同名用户连接投递 kick 指令（异步，不阻塞调用方）
	h.dispatchKickToRemoteNodes(ctx, userID, reason)

	return result
}

// ============================================================================
// 观察者通知与 SSE 通道（messaging 域实现，hub 对外 API 委托）
//
// 观察者投递（攒批入口 / 本地直投 / 跨节点广播）与 SSE 投递
//（点对点 / 全量广播）逻辑在消息域 observer.go / sse.go，
// 此处仅保留编排层对外入口的端口委托
// ============================================================================

// GetObserverNotifier 观察者通知批量处理器（messaging.Host 端口，
// 消息域 NotifyObservers 攒批入口经此提交）
func (h *Hub) GetObserverNotifier() *batcher.ObserverNotificationBatcher {
	return h.batcherMgr.ObserverNotify()
}

// NotifyObservers 通知观察者（观察者未启用时为 no-op）
// 从 ctx 提取 namespace+groupIDs 定位观察范围，提交批量处理器攒批投递
func (h *Hub) NotifyObservers(ctx context.Context, msg *models.HubMessage) {
	h.messagingMgr.NotifyObservers(ctx, msg)
}

// NotifyObserversDirect 直接通知观察者（不经批处理队列，避免递归入队）
// 由 observerBatcher flush 调用：本地观察者投递 + 跨节点广播，无 per-message goroutine
func (h *Hub) NotifyObserversDirect(msg *models.HubMessage, namespace string, groupIDs []string) {
	h.messagingMgr.NotifyObserversDirect(msg, namespace, groupIDs)
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
// SSE 通道（messaging 域实现，hub 对外 API 委托）
// ============================================================================

// SendToUserViaSSE 经 SSE 通道向用户投递（SSE 未启用或用户无订阅时返回 false）
// namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的 SSE 设备，避免跨 ns 串扰
func (h *Hub) SendToUserViaSSE(userID string, msg *models.HubMessage) bool {
	return h.messagingMgr.SendToUserViaSSE(userID, msg)
}

// BroadcastToSSEClients 广播给全部 SSE 客户端（appId/namespace 信封隔离）
// 通过 ForEachSSEClientParallel 并行分片读锁遍历（百万级优化）
func (h *Hub) BroadcastToSSEClients(msg *models.HubMessage) {
	h.messagingMgr.BroadcastToSSEClients(msg)
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
	return h.callbacks.GroupDisband
}

// GetGroupMemberJoinCallback 群组成员加入回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetGroupMemberJoinCallback() func(ctx context.Context, namespace, groupID string, userIDs []string) {
	return h.callbacks.GroupMemberJoin
}

// GetGroupMemberLeaveCallback 群组成员离开回调（nil 表示业务方未注册，调用方须跳过）
func (h *Hub) GetGroupMemberLeaveCallback() func(ctx context.Context, namespace, groupID string, userIDs []string) {
	return h.callbacks.GroupMemberLeave
}

// SendConditional 条件广播：对满足条件的在线客户端各投递一份，返回投递数
func (h *Hub) SendConditional(ctx context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int {
	return h.messagingMgr.SendConditional(ctx, condition, msg)
}

// SendToUserWithRetry 点对点发送（带重试与路由注入），失败详情在返回值中
func (h *Hub) SendToUserWithRetry(ctx context.Context, toUserID string, msg *models.HubMessage) *models.SendResult {
	return h.messagingMgr.SendToUserWithRetry(ctx, toUserID, msg)
}

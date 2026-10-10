/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\messaging\broadcast.go
 * @Description: Hub 统一投递入口与广播功能
 *
 * 本文件是消息投递的唯一公开入口（Deliver），以及广播类内部辅助的集中地：
 *   - Deliver：统一投递入口，路由全由 ctx + msg 决定
 *   - 私有分派器：deliverP2P / deliverToGroupReliable / deliverToGroupFireForget /
 *     deliverToNamespace / deliverGlobally（由 Deliver 按决策树调用）
 *   - 命名空间本地扇出：BroadcastToNamespace（nsIdx 反向索引定点扇出 + 大命中率自适应回退，
 *     deliverToNamespace 与跨节点消费侧共用）
 *   - 跨节点辅助：crossNodeGroupBroadcast（单群组跨节点）、batchGetGroupMembers（Pipeline 批量取成员）
 *   - 内部广播：BroadcastToFiltered / BroadcastToUserIDs / BroadcastFrameToUserIDs
 *     （预序列化 + 直接 TrySend，末者为聚合 drain 专用的预编码帧直发入口）
 *   - 过滤类广播：BroadcastByUserType / BroadcastToRole / BroadcastToClientType / BroadcastToDepartment
 *     （按客户端属性过滤，与路由正交）
 *   - 高级包装：BroadcastPriority / BroadcastAfterDelay / BroadcastExclude（基于 Deliver）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 统一投递入口
// ============================================================================

// ensureBroadcastID 广播类消息生成 Hub 内部 ID（幂等，已有不覆盖）
// 与 P2P 路径（normalizeMessageFields 生成 userID 前缀 ID 用于 ACK 对账）互补：
// 广播无单一接收者，纯雪花 ID；消息唯一标识是客户端引用与排查链路的基础契约
// idGenerator 未注入时跳过（与 Manager 契约一致：调用方自行保证 msg.ID 非空）
func (m *Manager) ensureBroadcastID(msg *models.HubMessage) {
	if msg.ID == "" && m.idGenerator != nil {
		msg.ID = m.idGenerator.GenerateRequestID()
	}
}

// Deliver 统一消息投递入口 — 路由全由 ctx + msg 决定，一套逻辑打通所有场景
//
// 路由元数据来源：ctx（appID/namespace/groupIDs）+ msg（Receiver/RequireAck）
// 调用方通过 routing.NewRoute().WithAppID(appID).WithNamespace(ns).WithGroupIDs(gids).Inject(ctx) 注入路由后调一次 Deliver。
//
// 决策树（按优先级）：
//  1. msg.Receiver != ""                          → P2P（SendToUserWithRetry，在线投递 + 离线存储 + 重试）
//  2. len(groupIDs) > 0:
//     - msg.RequireAck == true                   → 群组可靠投递（per-member SendToUserWithRetry + 离线存储）
//     - msg.RequireAck == false                  → 群组广播（fire-and-forget，BroadcastToUserIDs + 跨节点）
//  3. namespace != ""                             → 命名空间广播（BroadcastToFiltered + 跨节点 ns 广播）
//  4. namespace == ""                             → 全局广播（handleBroadcast + clusterBatcher）
//
// 群组投递语义：ns 为来源租户记号（必传校验，81407），成员定位按 (appID, groupIDs) 跨 ns
// 聚合（业务侧按租户建组、同 gid 多实例，投递信封 ns 置空=通配，跨 ns 成员实时送达）
//
// excludeSender 仅群组场景生效（P2P/广播场景 msg.Sender 不参与过滤）
//
// 返回非 nil *models.DeliverResult，错误收集到 result.Errors；按 result.Mode + 计数字段判断结果
func (m *Manager) Deliver(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
	// 统一入口：Clone + InjectRoute（注入 trace_id + 路由信封，appID 归一化，namespace 保留原值）
	// namespace 不在此归一化：空值在广播分支表示「全局广播」语义，需保留
	// P2P 分支由 deliverP2P 内部 EnsureRouteDefaults 兜底（复用入口已 Clone 副本，消除二次 Clone）；
	// 群组分支 ns 必传（来源租户记号，缺失报 81407），投递信封 ns 置空通配（见各分派器）
	msg = msg.Clone()
	ctx = msg.InjectRoute(ctx)

	// 路由元数据已随 InjectRoute 注入 ctx 信封，此处提取仅用于模式判定与入口日志；
	// 各分派器内部直接从 ctx 提取所需路由，不再显式传参（单一事实源：ctx）
	appID := routing.AppIDFromContext(ctx)
	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)

	// 入口路由决策：本条消息走哪条投递路径，全链路跟踪的广播侧起点
	var mode models.DeliveryMode
	switch {
	case msg.Receiver != "":
		mode = models.DeliveryModeP2P
	case len(groupIDs) > 0:
		if msg.RequireAck {
			mode = models.DeliveryModeGroupReliable
		} else {
			mode = models.DeliveryModeGroupBroadcast
		}
	case namespace != "":
		mode = models.DeliveryModeNamespace
	default:
		mode = models.DeliveryModeGlobal
	}
	// 路由分支埋点补位（消息级日志降 Debug 后，各分支占比与降级频率经此处量化）
	m.host.GetOverloadMetrics().RecordDeliverMode(mode)

	// 广播类消息在此生成 Hub 内部 ID（P2P 例外：send 路径生成带 userID 前缀的 ID 用于
	// ACK 对账，此处生成会破坏其前缀语义）——广播无单一接收者，纯雪花即可；
	// 消息唯一标识是客户端与排查链路的基础契约，不应因非 ACK 路径而缺席
	if mode != models.DeliveryModeP2P {
		m.ensureBroadcastID(msg)
	}

	// 投递路由决策为每消息必经路径，100w 量级下 Info 级会产生海量日志 IO，降为 Debug（生产环境关闭）
	m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] Deliver 路由决策",
		"message_id", msg.MessageID,
		"mode", mode,
		"receiver", msg.Receiver,
		"group_ids", groupIDs,
		"namespace", namespace,
		"app_id", appID,
	)

	switch mode {
	case models.DeliveryModeP2P:
		return m.deliverP2P(ctx, msg)
	case models.DeliveryModeGroupReliable:
		return m.deliverToGroupReliable(ctx, msg, excludeSender)
	case models.DeliveryModeGroupBroadcast:
		return m.deliverToGroupFireForget(ctx, msg, excludeSender)
	case models.DeliveryModeNamespace:
		return m.deliverToNamespace(ctx, msg)
	default:
		return m.deliverGlobally(ctx, msg)
	}
}

// ============================================================================
// 投递私有分派器（由 Deliver 按决策树调用）
// ============================================================================

// deliverP2P 点对点投递（msg.Receiver 非空）
// msg 已为 Deliver 入口 Clone 的私有副本，此处复用不再二次 Clone：
// 先 EnsureRouteDefaults 归一化 namespace（P2P 严格契约空补默认，等价原 SendToUserWithRetry 公开入口），
// 再 InjectRoute 把归一化后的 ns 写回信封，随后直走 sendToUserWithRetryCore 处理在线/离线/重试
func (m *Manager) deliverP2P(ctx context.Context, msg *models.HubMessage) *models.DeliverResult {
	result := &models.DeliverResult{
		Mode:   models.DeliveryModeP2P,
		AppID:  routing.AppIDFromContext(ctx),
		Errors: make([]error, 0),
	}

	ctx = routing.EnsureRouteDefaults(ctx)
	ctx = msg.InjectRoute(ctx)
	sr := m.sendToUserWithRetryCore(ctx, msg.Receiver, msg, nil)
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
// 在线成员通过 SendToUserWithRetry 投递（自动支持跨节点路由与重试）
// 离线成员通过离线消息处理器存储，上线后自动推送
func (m *Manager) deliverToGroupReliable(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
	// 路由信封已注入 ctx（Deliver 入口 InjectRoute，appID 已归一化非空），直接提取单一事实源
	appID, namespace := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)

	result := &models.DeliverResult{
		Mode:      models.DeliveryModeGroupReliable,
		AppID:     appID,
		Namespace: namespace,
		GroupIDs:  groupIDs,
		Errors:    make([]error, 0),
	}

	// 群组投递 ns 为必要参数（来源租户记号）：缺失说明调用方漏传路由，直接报错不兜底；
	// 成员定位按 (appID, groupIDs) 跨 ns 聚合，ns 不参与定位与过滤（业务侧按租户建组，同 gid 多实例全员送达）
	if namespace == "" {
		result.AddError(models.ErrRouteNamespaceMissing)
		return result
	}

	if m.host.GetGroupRepo() == nil {
		result.AddError(models.ErrGroupRepoNotSet)
		return result
	}

	// msg 已 Clone（Deliver 入口），同步路由信封：ns 置空=投递信封通配（跨 ns 成员的连接匹配
	// 跳过 ns 过滤，实时送达，见 ClientMatchesEnvelope）；result.Namespace 保留原 ns 仅记账
	ctx = msg.ContextWithRoute(ctx, appID, "", groupIDs)
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 1. 两段 Pipeline 跨 ns 聚合所有实例成员并合并去重（N 个实例 → 2 次 RTT）
	memberSet, err := m.batchGetGroupMembers(ctx, groupIDs)
	if err != nil {
		result.AddError(err)
		m.host.GetLogger().ErrorContextKV(ctx, "批量获取群组成员失败",
			"namespace", namespace, "group_ids", groupIDs, "error", err)
		return result
	}
	members := make([]string, 0, len(memberSet))
	for uid := range memberSet {
		members = append(members, uid)
	}

	// 2. 过滤发送者（如需）；TotalMembers 语义为投递目标数，须与下方各计数同口径（过滤后）
	filteredMembers := members
	if excludeSender && msg.Sender != "" {
		filteredMembers = mathx.FilterSlice(members, func(id string) bool {
			return id != msg.Sender
		})
		m.host.GetLogger().DebugContextKV(ctx, "过滤发送者后的群组成员列表",
			"namespace", namespace,
			"group_ids", groupIDs,
			"original_count", len(members),
			"filtered_count", len(filteredMembers),
			"excluded_sender", msg.Sender,
		)
	}
	result.TotalMembers = len(filteredMembers)
	if result.TotalMembers == 0 {
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

	ctx, collector := withOfflineBroadcastCollector(ctx)
	defer m.flushOfflineBroadcasts(ctx, collector)
	// 批量预取本地 miss 成员的节点索引（1 次 Pipeline 替代扇出内 N 次单查，
	// 远端成员为主的群组单条消息 N 次往返 → 1 次）
	presetNodes := m.prefetchFanoutNodes(ctx, msg, filteredMembers)
	newFanoutExecutor(filteredMembers, m.fanoutConcurrency()).
		Execute(func(idx int, uid string) (*models.SendResult, error) {
			sendResult := m.sendToUserWithRetry(ctx, uid, msg, presetNodes[uid])

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

	// 通知观察者（ctx 已在上方 ContextWithRoute 注入路由，直接使用即可）
	m.NotifyObservers(ctx, msg)

	// 每群组消息一条的常规完成统计，降为 Debug 与本地直连投递日志同级别（异常路径仍保留 Info）
	m.host.GetLogger().DebugContextKV(ctx, "群组消息投递完成",
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
// 生产者-消费者模式：源节点只发广播意图（groupIDs+msg），不做成员读也不做全量扇出；
// 本地与远端均由各节点经本地反向索引反查成员后扇出，消除大群跨节点的 Redis 读放大与单点扇出瓶颈
//
// 可聚合档接线：单群组 + 非排除发送者的可聚合消息先 Offer 进本地聚合窗口
// （drain 窗口批量扇出），跨节点广播意图照常发出——各远端节点对本地成员
// 各自聚合，聚合后全集群帧推送数 = 在线连接数（内核 pps 友好）
func (m *Manager) deliverToGroupFireForget(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
	// 路由信封已注入 ctx（Deliver 入口 InjectRoute，appID 已归一化非空），直接提取单一事实源
	appID, namespace := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)

	result := &models.DeliverResult{
		Mode:      models.DeliveryModeGroupBroadcast,
		AppID:     appID,
		Namespace: namespace,
		GroupIDs:  groupIDs,
		Errors:    make([]error, 0),
	}

	// 群组投递 ns 为必要参数（来源租户记号）：缺失说明调用方漏传路由，直接报错不兜底；
	// 成员定位按 (appID, groupIDs) 跨 ns 聚合，ns 不参与定位与过滤（业务侧按租户建组，同 gid 多实例全员送达）
	if namespace == "" {
		result.AddError(models.ErrRouteNamespaceMissing)
		return result
	}

	// msg 已 Clone（Deliver 入口），同步路由信封：ns 置空=投递信封通配（跨 ns 成员的连接匹配
	// 跳过 ns 过滤，实时送达，见 ClientMatchesEnvelope）；result.Namespace 保留原 ns 仅记账
	ctx = msg.ContextWithRoute(ctx, appID, "", groupIDs)
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 0. 可聚合档入窗：本地扇出延后至 drain 窗口（跨节点意图照发，远端节点各自聚合）；
	// 入窗成功即返回（fire-and-forget 语义：本地投递延后一个窗口，result 不计入 LocalDelivered）
	if m.OfferAggregatable(ctx, msg, excludeSender) {
		m.NotifyObservers(ctx, msg)
		m.crossNodeGroupBroadcast(ctx, msg, excludeSender)
		result.Aggregated = true
		return result
	}

	// 1. 本地反向索引取成员（替代两段 Pipeline 全量读 Redis 成员）：只扇出本节点在线成员
	localMembers := m.host.GetShardedRegistry().GetLocalGroupMembers(appID, groupIDs)

	// 2. 排除发送者后得到本地目标成员；本地无成员仍须继续跨节点广播，不可提前返回
	targetMembers := localMembers
	if excludeSender && msg.Sender != "" {
		targetMembers = mathx.FilterSlice(localMembers, func(id string) bool {
			return id != msg.Sender
		})
	}

	// 3. 本地扇出（有本地成员才走，空列表跳过 Admit/整形等无谓开销）
	// 增加广播发送统计（原子计数器，由 flushStatsCounters 定时刷写到 Redis；与全局广播同口径）
	if m.host.GetStatsRepo() != nil {
		m.broadcastSentCount.Add(1)
	}
	localCount := 0
	if len(targetMembers) > 0 {
		localCount = m.BroadcastToUserIDs(ctx, targetMembers, msg)
	}

	// 通知观察者（ctx 已注入路由，直接使用）
	m.NotifyObservers(ctx, msg)

	// 4. 跨节点广播：优先 gRPC 直连，降级 PubSub（各远端节点按本地索引反查投递）
	m.crossNodeGroupBroadcast(ctx, msg, excludeSender)

	// fire-and-forget 每群组消息必经的常规统计，降为 Debug（诊断需要时开 Debug 级即可）
	m.host.GetLogger().DebugContextKV(ctx, "群组广播已发起",
		"namespace", namespace,
		"group_ids", groupIDs,
		"message_id", msg.MessageID,
		"local_members", len(targetMembers),
		"local_delivered", localCount,
		"grpc_enabled", m.host.IsGRPCEnabled(),
		"pubsub_enabled", m.host.HasPubsub(),
	)

	result.LocalDelivered = localCount
	return result
}

// deliverToNamespace 命名空间广播（namespace 非空，无 groupIDs）
//
// 复用历史 BroadcastToNamespace 逻辑：本地 BroadcastToFiltered + 跨节点命名空间广播
// 本地按命名空间过滤广播，跨节点提交到 clusterBatcher
func (m *Manager) deliverToNamespace(ctx context.Context, msg *models.HubMessage) *models.DeliverResult {
	appID, namespace := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	result := &models.DeliverResult{
		Mode:      models.DeliveryModeNamespace,
		AppID:     appID,
		Namespace: namespace,
		Errors:    make([]error, 0),
	}

	// msg 已 Clone + InjectRoute，信封已含 ns（非空）；ContextWithRoute 二次同步保证 msg.Namespace 与 ctx 一致
	ctx = msg.ContextWithRoute(ctx, appID, namespace, nil)
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 增加广播发送统计（原子计数器，由 flushStatsCounters 定时刷写到 Redis；与全局广播同口径）
	if m.host.GetStatsRepo() != nil {
		m.broadcastSentCount.Add(1)
	}

	// 本地按命名空间投递：nsIdx 反向索引定点扇出 + 大命中率自适应回退（统一入口见 BroadcastToNamespace）
	count := m.BroadcastToNamespace(ctx, namespace, msg)

	// 通知观察者（命名空间级广播事件）
	m.NotifyObservers(ctx, msg)

	// 跨节点命名空间广播（提交到分布式池经 routeToCluster 投递）
	opts := cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
		Namespace: namespace,
	}
	// 队列满分道：express 语义丢弃 / 可靠档同步直发兜底（统一见 submitClusterDispatch）
	m.submitClusterDispatch(ctx, msg, opts)

	result.LocalDelivered = count
	return result
}

// deliverGlobally 全局广播（namespace 为空，无 groupIDs）
//
// 复用历史 Broadcast 逻辑：设置 models.BroadcastTypeGlobal + 提交分布式池跨节点投递 + 本地 handleBroadcast
// 全命名空间广播（不按命名空间过滤），跨节点经分布式池 → routeToCluster 分发
func (m *Manager) deliverGlobally(ctx context.Context, msg *models.HubMessage) *models.DeliverResult {
	result := &models.DeliverResult{
		Mode:   models.DeliveryModeGlobal,
		AppID:  routing.AppIDFromContext(ctx),
		Errors: make([]error, 0),
	}

	// 自动设置为全局广播类型
	msg.BroadcastType = mathx.IfEmpty(msg.BroadcastType, models.BroadcastTypeGlobal)
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 增加广播发送统计（原子计数器，由 flushStatsCounters 定时刷写到 Redis）
	if m.host.GetStatsRepo() != nil {
		m.broadcastSentCount.Add(1)
	}

	// 分布式广播：提交到分布式池异步投递（消除 per-message goroutine）
	opts := cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
		Namespace: "", // 全命名空间广播
	}
	// 队列满分道：express 语义丢弃 / 可靠档同步直发兜底（统一见 submitClusterDispatch）
	m.submitClusterDispatch(ctx, msg, opts)

	// 本地广播（直接异步执行，不经过 EventLoop channel 串行化；编码由 handleDirectMessage 兜底）
	go m.handleBroadcast(msg, nil)

	// 全局广播为异步，LocalDelivered 不计入（与历史 Broadcast 无返回值语义一致）
	return result
}

// OfferAggregatable 可聚合档消息入窗尝试（群组广播链路统一接线点）
//
// 返回 true = 已入本地聚合窗口：调用方跳过即时本地扇出（drain 窗口批量扇出，
// 各节点对本地成员各自聚合——源节点 + 跨节点入站流共用本入口）；
// 返回 false = 不聚合或降级直通（调用方走原路径，性能降级不丢消息）：
//   - 聚合器未启用 / 消息非可聚合档 / 多群组 / 排除发送者（窗口扇出无法复现逐成员排除语义）
//   - 窗口满 / 群组数满（Offer 降级直通）
//   - 序列化失败（编码前置一次，失败直通单发）
//
// 编码前置：入窗即完成本消息的序列化（drain 拼装为纯字节拼接，零重编码）
func (m *Manager) OfferAggregatable(ctx context.Context, msg *models.HubMessage, excludeSender bool) bool {
	aggregator := m.host.GetMessageAggregator()
	if aggregator == nil || excludeSender {
		return false
	}

	// 分级解析（读锁 + 决策树）：非可聚合档直通（大多数消息一次字段读即返回）
	if msg.ResolveGuarantee() != models.GuaranteeAggregatable {
		return false
	}

	// 单群组才聚合（多群组语义上无单一聚合 key，直通原路径）
	groupIDs := routing.GroupIDsFromContext(ctx)
	if len(groupIDs) != 1 {
		return false
	}

	// 编码前置一次（数组拼装零重编码；失败直通单发）
	payload, err := json.Marshal(msg)
	if err != nil {
		m.host.GetLogger().ErrorContextKV(ctx, "可聚合消息序列化失败，直通单发",
			"message_id", msg.MessageID, "error", err)
		return false
	}

	return aggregator.Offer(routing.AppIDFromContext(ctx), groupIDs[0], payload)
}

// crossNodeGroupBroadcast 跨节点群组广播（单/多群组统一入口）
//
// 统一走 OperationTypeGroupsBroadcast 复数语义，信封携带全部 GroupIDs，
// 单群组作为 GroupIDs=[groupID] 的特例；提交到分布式池经 routeToCluster
// 投递（gRPC 直连优先 + PubSub 兜底），消除 per-message goroutine
// namespace/groupIDs 从 ctx 提取：群组分派器信封 ns 已置空（通配），
// 原样传递到远端，远端成员聚合与连接匹配同样跨 ns 通配
func (m *Manager) crossNodeGroupBroadcast(ctx context.Context, msg *models.HubMessage, excludeSender bool) {
	if !m.host.HasPubsub() && !m.host.IsGRPCEnabled() {
		return // 单机模式，无需跨节点
	}

	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)

	senderID := ""
	if excludeSender {
		senderID = msg.Sender
	}

	opts := cluster.ClusterDispatchOptions{
		Operation:     models.OperationTypeGroupsBroadcast,
		Namespace:     namespace,
		GroupIDs:      groupIDs,
		ExcludeSender: excludeSender,
		SenderID:      senderID,
	}

	// 队列满分道：express 语义丢弃 / 可靠档同步直发兜底（统一见 submitClusterDispatch）
	m.submitClusterDispatch(ctx, msg, opts)
}

// submitClusterDispatch 分级跨节点分发提交（分布式池队列满分道）
//
// notice 与高频消息不同命（语义化丢弃）：
//   - express 档（高频流式/可聚合）：队列满语义丢弃计数——洪峰下后续消息自然覆盖，丢弃是正确行为
//   - reliable 档：同步直发兜底（调用方承担一次 routeToCluster RTT 作天然反压）——业务 notice
//     不因队列满丢失；降级次数进 stats（cluster_dispatch_syncs，持续增长 = 跨节点消费能力不足）
func (m *Manager) submitClusterDispatch(ctx context.Context, msg *models.HubMessage, opts cluster.ClusterDispatchOptions) bool {
	if m.host.SubmitClusterDispatch(msg, opts) {
		return true
	}
	if msg.ResolveGuarantee().IsExpress() {
		m.host.GetOverloadMetrics().RecordEphemeralDrop()
		m.host.GetLogger().DebugContextKV(ctx, "集群分发队列已满，express 广播语义丢弃",
			"message_id", msg.MessageID)
		return false
	}
	m.host.GetOverloadMetrics().RecordClusterDispatchSync()
	m.host.GetLogger().InfoContextKV(ctx, "集群分发队列已满，可靠档降级同步直发",
		"message_id", msg.MessageID, "operation", opts.Operation)
	if err := m.host.RouteToCluster(ctx, msg, opts); err != nil {
		m.host.GetLogger().WarnContextKV(ctx, "集群分发同步直发失败",
			"message_id", msg.MessageID, "error", err)
		return false
	}
	return true
}

// batchGetGroupMembers 批量获取多个群组成员并合并去重（跨 ns 聚合）
// appID 从 ctx 路由信封提取（上游已注入，不再显式传参）
// 存储侧两段 Pipeline：先查各 gid 的 ns 实例集合（gns 索引），再批量取所有实例的成员，共 2 次 RTT
// O(totalMembers) 合并去重；单个实例失败仅该实例缺失，不影响其他（整体 Pipeline 失败才返回错误）
func (m *Manager) batchGetGroupMembers(ctx context.Context, groupIDs []string) (map[string]struct{}, error) {
	memberSet := make(map[string]struct{})
	if len(groupIDs) == 0 || m.host.GetGroupRepo() == nil {
		return memberSet, nil
	}

	appID := routing.AppIDFromContext(ctx)
	groupMembers, err := m.host.GetGroupRepo().GetMultiGroupMembers(ctx, appID, groupIDs)
	if err != nil {
		return memberSet, err
	}

	for _, members := range groupMembers {
		for _, uid := range members {
			memberSet[uid] = struct{}{}
		}
	}
	return memberSet, nil
}

// ============================================================================
// 内部分组广播辅助（预序列化 + 直接 TrySend）
// ============================================================================

// BroadcastToFiltered 预序列化消息并直接发送给符合条件的客户端
// 消除逐客户端 Clone/序列化/入队/DB 记录开销：
//   - 消息只 json.Marshal 1 次（原方案每客户端 1 次）
//   - 不走 SendToUserWithRetry（原方案每客户端 Clone×2 + 在线检查 + 入队 + DB 记录）
//   - 零拷贝遍历（原方案 GetClientsCopy + FilterSlice 双重拷贝）
//
// 削峰填谷接线（分级准入 + 出向整形）：
//   - Admit 拒绝（VerdictDelay/Offline）→ 延迟队列（填谷重投），队列满走分级兜底
//   - shaper 拒绝（洪峰整形）→ 延迟队列；必达级跳过整形（与 Admit 矩阵的"必达恒放行"一致）
func (m *Manager) BroadcastToFiltered(ctx context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int {
	// 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	ctx = msg.InjectRoute(ctx)

	// 准入闸门：分级×水位裁决（必达级恒放行；普通/高频过载时延迟/离线路由——拒绝≠丢弃）
	if verdict := m.host.AdmitMessage(msg, true); verdict != overload.VerdictAdmit {
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.broadcastToFilteredNow(c, condition, bm)
		})
		return 0 // 本轮不扇出（延迟重投；返回实时成功数 0）
	}

	// ⏱出向整形：每条广播 1 令牌平滑扇出（必达级跳过——控制面不受数据面整形影响）
	if shaper := m.host.GetBroadcastShaper(); shaper != nil &&
		msg.ResolveGuarantee() != models.GuaranteeGuaranteed &&
		!shaper.Allow() {
		m.host.GetOverloadMetrics().RecordShaperDenied()
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.broadcastToFilteredNow(c, condition, bm)
		})
		return 0
	}

	return m.broadcastToFilteredNow(ctx, condition, msg)
}

// broadcastToFilteredNow 预序列化 + 扇出（延迟队列重投的目标函数，不重复准入）
func (m *Manager) broadcastToFilteredNow(ctx context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int {
	start := time.Now()

	// 预序列化 WebSocket 消息（仅一次，所有客户端复用同一份）
	data, err := json.Marshal(msg)
	if err != nil {
		m.host.GetLogger().ErrorContextKV(ctx, "分组广播消息序列化失败", "error", err)
		return 0
	}
	marshalDuration := time.Since(start)

	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	dataLen := len(data)

	// 送达分级在扇出循环外解析一次（循环不变量外提）：过滤广播遍历全量连接，
	// 逐客户端 ResolveGuarantee（读锁 + 决策树）是百万级无谓开销，分级对同一 msg 恒定
	guarantee := msg.ResolveGuarantee()

	// 并发数快照
	totalWSClients := m.host.GetShardedRegistry().GetClientCount()
	totalSSEClients := m.host.GetShardedRegistry().GetSSEClientCount()

	var successCount int32
	var wsScanned, sseScanned int64

	// 组合过滤条件：业务 condition + appId/namespace 隔离（ClientMatchesEnvelope 做 appId+namespace 严格匹配，
	// 不做 msg.GroupIDs vs client.GroupID 系统组匹配——两者维度不同，详见 ClientMatchesEnvelope 注释）
	// 所有 BroadcastByUserType / BroadcastToRole / BroadcastToClientType 等上层调用自动获得 appId+namespace 隔离
	combinedCondition := func(c *models.Client) bool {
		return connection.ClientMatchesEnvelope(c, msg.AppID, msg.Namespace, msg.GroupIDs) && condition(c)
	}

	// WebSocket 客户端：直接 TrySend 预序列化数据（并行遍历优化百万级广播）
	wsStart := time.Now()
	m.host.GetShardedRegistry().ForEachClientParallel(0, func(_ string, client *models.Client) {
		atomic.AddInt64(&wsScanned, 1)
		if client.IsClosed() || client.ConnectionType == models.ConnectionTypeSSE {
			return
		}
		if !combinedCondition(client) {
			return
		}
		if client.TrySend(data) {
			atomic.AddInt32(&successCount, 1)
			m.host.TrackReceiverMessageStats(client.ID, client.UserType, dataLen)
		} else {
			// 成员级分级兜底（修复广播丢弃彻底丢失）：普通/必达转离线，高频语义丢弃
			m.routeDeliveryFallback(msg, client, client.UserID, guarantee)
		}
	})
	wsDuration := time.Since(wsStart)

	// SSE 客户端：通过专用通道发送 msg 对象（无需序列化，并行遍历）
	sseStart := time.Now()
	m.host.GetShardedRegistry().ForEachSSEClientParallel(0, func(_, _ string, client *models.Client) {
		atomic.AddInt64(&sseScanned, 1)
		if client.IsClosed() || !combinedCondition(client) {
			return
		}
		if client.TrySendSSE(msg) {
			atomic.AddInt32(&successCount, 1)
		}
	})
	sseDuration := time.Since(sseStart)

	// 消息记录状态只更新一次（同一 msgID；广播记录 receiver 为空）
	totalSuccess := atomic.LoadInt32(&successCount)
	if totalSuccess > 0 {
		m.updateMessageStatusAsync(ctx, msgID, "", models.MessageSendStatusSuccess, "", "")
	}

	totalDuration := time.Since(start)
	// 广播完成统计：每消息必经的常规统计，降为 Debug（发到多少客户端生产侧由指标观测，日志只做诊断）
	m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] 过滤广播完成",
		"message_id", msg.MessageID,
		"success", totalSuccess,
		"data_bytes", dataLen,
		"total_ws_clients", totalWSClients,
		"total_sse_clients", totalSSEClients,
		"ws_scanned", atomic.LoadInt64(&wsScanned),
		"sse_scanned", atomic.LoadInt64(&sseScanned),
		"marshal_duration_ms", marshalDuration.Milliseconds(),
		"ws_duration_ms", wsDuration.Milliseconds(),
		"sse_duration_ms", sseDuration.Milliseconds(),
		"total_duration_ms", totalDuration.Milliseconds(),
	)

	return int(totalSuccess)
}

// BroadcastToUserIDs 预序列化消息并直接发送给指定用户ID列表的在线客户端
// O(m) 复杂度（m=用户数），按成员ID反查 shardedRegistry，仅锁定相关 shard
// 相比 BroadcastToFiltered 的 O(n)（n=总连接数），群组广播场景大幅减少遍历与锁范围
// 适用于已知目标用户ID列表的场景（群组广播、多群组广播）
//
// 削峰填谷接线：同 BroadcastToFiltered（分级准入 + 出向整形 + 延迟队列路由）
func (m *Manager) BroadcastToUserIDs(ctx context.Context, userIDs []string, msg *models.HubMessage) int {
	return m.broadcastWithFrame(ctx, userIDs, msg, nil)
}

// BroadcastFrameToUserIDs 预编码帧直发扇出（聚合 drain 专用零二次编码入口）
//
// frame 为已编码的 WS 帧字节（如聚合器产出的裸 JSON 数组帧），逐成员 TrySend 直发，
// 消除常规路径的二次 Marshal；msg 仍承担三件事：
//   - 信封匹配键（AppID/Namespace/GroupIDs，per-device 过滤）
//   - SSE 对象通道（TrySendSSE，SSE 无预编码通道）
//   - TrySend 失败时的成员级分级兜底（routeDeliveryFallback）
//
// frame 为空时回退一次 Marshal（与常规路径同语义，防御调用方未编码场景）
// 准入/整形/分片并行/顺序保证与 BroadcastToUserIDs 完全一致
func (m *Manager) BroadcastFrameToUserIDs(ctx context.Context, userIDs []string, msg *models.HubMessage, frame []byte) int {
	return m.broadcastWithFrame(ctx, userIDs, msg, frame)
}

// broadcastWithFrame 准入整形公共壳：admit/shaper 双闸门通过后进入扇出核心
// frame 非空 = 预编码直发（零二次编码）；nil = 常规路径（核心体内 Marshal 一次）
func (m *Manager) broadcastWithFrame(ctx context.Context, userIDs []string, msg *models.HubMessage, frame []byte) int {
	// 路由信封同步：从 ctx 恢复路由到 msg 信封（幂等）
	// 上游 deliverToGroupFireForget 已注入，此处作为二次兜底
	// InjectRoute 同时回写 ctx，保证下游 ctx 与信封一致
	ctx = msg.InjectRoute(ctx)

	// 准入闸门（必达恒放行；普通/高频过载时延迟路由）
	if verdict := m.host.AdmitMessage(msg, true); verdict != overload.VerdictAdmit {
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.broadcastFrameToUserIDsNow(c, userIDs, bm, frame)
		})
		return 0
	}

	// ⏱出向整形（必达级跳过）
	if shaper := m.host.GetBroadcastShaper(); shaper != nil &&
		msg.ResolveGuarantee() != models.GuaranteeGuaranteed &&
		!shaper.Allow() {
		m.host.GetOverloadMetrics().RecordShaperDenied()
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.broadcastFrameToUserIDsNow(c, userIDs, bm, frame)
		})
		return 0
	}

	return m.broadcastFrameToUserIDsNow(ctx, userIDs, msg, frame)
}

// broadcastShardParallelUsers 用户数超过该阈值时启用分片并行扇出（低于则串行，省 goroutine 开销）
const broadcastShardParallelUsers = 512

// broadcastFrameToUserIDsNow 预编码帧/消息 + 按 userIDs 扇出（延迟队列重投目标，不重复准入）
//
// frame 非空直接复用（聚合 drain 零二次编码），nil 时 Marshal msg 一次
//
// 顺序保证（10w 级成员扇出的核心约束）：
//   - 按 userID 均匀分片并行，同一 userID 的全部连接固定落在同一分片内串行投递，
//     配合 client.sendChan 的 FIFO 语义 → 每个用户收到的消息顺序 = 广播调用顺序
//   - 入口同步（WaitGroup 等全部 worker 完成）→ 调用方顺序调用两次广播，
//     各用户 sendChan 的写入顺序与调用顺序一致
func (m *Manager) broadcastFrameToUserIDsNow(ctx context.Context, userIDs []string, msg *models.HubMessage, frame []byte) int {
	if len(userIDs) == 0 {
		return 0
	}

	// 预序列化 WebSocket 消息（仅一次；引擎由 go-toolbox/pkg/json 构建标签决定）
	data := frame
	if len(data) == 0 {
		encoded, err := json.Marshal(msg)
		if err != nil {
			m.host.GetLogger().ErrorContextKV(ctx, "群组广播消息序列化失败", "error", err)
			return 0
		}
		data = encoded
	}

	dataLen := len(data)
	var successCount int32

	// 送达分级在扇出循环外解析一次（循环不变量外提）：群组广播按成员扇出，
	// 逐成员 ResolveGuarantee（读锁 + 决策树）是大群无谓开销，分级对同一 msg 恒定
	guarantee := msg.ResolveGuarantee()

	// 分片扇出（单分片内串行按 userID 投递，保用户内顺序）：
	// 先 holding 预检（live==0 零锁直判，绝大多数时刻）——非回放中直接调
	// deliverFanoutToUser（零闭包分配，10w 级扇出的 GC 主热点）；
	// 回放中才构造闭包走 HoldUserDelivery 暂存（补投可能在任意 goroutine 执行，
	// 成功计数走共享原子）；成功计数用分片局部变量累计、worker 退出一次性汇总
	// （消除逐 client 原子加的 cache line 争用）
	fanout := func(ids []string) {
		var local int32
		for _, userID := range ids {
			if m.holdingUserDelivery(userID) {
				m.HoldUserDelivery(userID, func() {
					if n := m.deliverFanoutToUser(userID, msg, data, dataLen, guarantee); n > 0 {
						atomic.AddInt32(&successCount, int32(n))
					}
				})
				continue
			}
			local += int32(m.deliverFanoutToUser(userID, msg, data, dataLen, guarantee))
		}
		if local > 0 {
			atomic.AddInt32(&successCount, local)
		}
	}

	// 大群分片并行（10w 成员级扇出）；同一 userID 固定在同一分片，用户内顺序不破
	if len(userIDs) >= broadcastShardParallelUsers {
		workers := runtime.NumCPU()
		if workers > len(userIDs) {
			workers = len(userIDs)
		}
		chunk := (len(userIDs) + workers - 1) / workers
		var wg sync.WaitGroup
		for i := 0; i < len(userIDs); i += chunk {
			end := i + chunk
			if end > len(userIDs) {
				end = len(userIDs)
			}
			wg.Add(1)
			go func(ids []string) {
				defer wg.Done()
				fanout(ids)
			}(userIDs[i:end])
		}
		wg.Wait()
	} else {
		fanout(userIDs)
	}

	totalSuccess := atomic.LoadInt32(&successCount)

	// 群组广播本地投递统计：每群组消息必经的常规统计，降为 Debug（成员在线但 0 投递 = 本地无连接，跨节点由 clusterBatcher 负责）
	m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] 群组广播本地投递完成",
		"message_id", msg.MessageID,
		"user_count", len(userIDs),
		"success", totalSuccess,
		"group_ids", msg.GroupIDs,
	)

	return int(totalSuccess)
}

// deliverFanoutToUser 单用户扇出投递：信封匹配过滤（app/ns/group）+ SSE/WS 分道 + 满道分级兜底
//
// 返回成功投递的连接数（多设备用户多条连接逐一计数，与历史扇出计数口径一致）；
// 独立成方法使扇出热路径免闭包分配（原每用户构造的 deliver 闭包是
// 10w 级扇出的 GC 主热点，pprof alloc_space 占比 98%）；
// data 为预序列化不可变字节，msg 仅被 SSE TrySend 只读引用，无并发写
func (m *Manager) deliverFanoutToUser(userID string, msg *models.HubMessage, data []byte, dataLen int, guarantee models.DeliveryGuarantee) int {
	count := 0
	// 按用户ID查找客户端（O(m)，仅锁定相关 shard，不遍历全部连接）
	// ForEachUserClientFiltered 叠加路由信封(appId+namespace)匹配：
	//   - 群组消息：client 必须同 app/ns 且 client.groupID 在 msg.GroupIDs 中
	//   - 避免新加入群的成员通过"旧 userIDs 列表"收到不匹配其 group 的历史消息
	m.host.GetShardedRegistry().ForEachUserClientFiltered(userID, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *models.Client) bool {
		if client.IsClosed() {
			return true
		}
		if client.ConnectionType == models.ConnectionTypeSSE {
			// SSE 客户端发送 msg 对象
			if client.TrySendSSE(msg) {
				count++
			}
			return true
		}
		// WebSocket 客户端发送预序列化数据
		if client.TrySend(data) {
			count++
			m.host.TrackReceiverMessageStats(client.ID, client.UserType, dataLen)
		} else {
			// 成员级分级兜底：普通/必达转离线（用户下次上线/重连时补发），高频语义丢弃
			m.routeDeliveryFallback(msg, client, userID, guarantee)
		}
		return true
	})
	return count
}

// ============================================================================
// 命名空间广播（nsIdx 反向索引定点扇出 + 自适应回退）
// ============================================================================

// nsBroadcastFullScanPercent 命名空间索引回退全量扫描的命中占比阈值（百分比）
// 索引路径按 userID 逐个加 shard 读锁 + map 查找，单价高于全量扫描的批量遍历；
// 命中用户数占本地总连接数的比例达到该阈值（单租户独占 pod 场景）时，
// 全量扫描更优，回退 BroadcastToFiltered 路径
const nsBroadcastFullScanPercent = 50

// BroadcastToNamespace 命名空间广播统一入口（本地投递 deliverToNamespace 与
// 跨节点消费侧 hub.handleDistributedBroadcast 共用，保证两端路径一致）
//
// 经 nsIdx 反向索引定点反查该命名空间的本地在线用户后 BroadcastToUserIDs 扇出，
// 替代 BroadcastToFiltered 对全部连接的 O(N) 全量扫描：
//   - 多租户 pod 上命名空间广播命中率通常极低（如 10w 连接中目标 ns 仅 10 个用户），
//     索引将 O(全部连接) 收敛为 O(该 ns 本地在线用户)
//   - 空命中直接返回（索引与注册表在同一 AddClient/RemoveClient 内事务性维护，
//     无该 ns 用户时全量扫描同样 0 投递，跳过序列化与准入开销）
//
// 自适应回退：命中用户数 ≥ 总连接数的 nsBroadcastFullScanPercent% 时退回全量扫描
// （索引路径逐 userID 查锁的单价更高，大命中率下 64 shard 并行批量遍历反而更快）
//
// msg 信封在本入口统一同步（ContextWithRoute）：appID 归一化 + namespace 显式写入，
// 确保 BroadcastToUserIDs 的 per-device 信封过滤（ClientMatchesEnvelope）与目标 ns 一致
func (m *Manager) BroadcastToNamespace(ctx context.Context, namespace string, msg *models.HubMessage) int {
	if namespace == "" {
		// 空 ns = 全局广播语义，不属于本入口（由 deliverGlobally 全量扫描处理）
		return 0
	}

	appID := mathx.IfEmpty(msg.AppID, routing.AppIDFromContext(ctx))
	ctx = msg.ContextWithRoute(ctx, appID, namespace, nil)

	registry := m.host.GetShardedRegistry()
	users := registry.GetLocalNamespaceUsers(appID, namespace)
	if len(users) == 0 {
		return 0
	}

	// 自适应回退：大命中率时索引路径劣于全量扫描（用户数按设备去重、total 为连接数，
	// 多端登录下 users 偏小于实际连接数，阈值判定偏保守即偏向全量，方向安全）
	total := registry.GetClientCount()
	if int64(len(users))*100 >= total*nsBroadcastFullScanPercent {
		return m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
			return c.Namespace == namespace
		}, msg)
	}
	return m.BroadcastToUserIDs(ctx, users, msg)
}

// ============================================================================
// 过滤类广播方法（按客户端属性过滤，与路由正交）
// ============================================================================

// BroadcastByUserType 发送消息给特定用户类型的所有客户端
func (m *Manager) BroadcastByUserType(ctx context.Context, userType models.UserType, msg *models.HubMessage) int {
	return m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return c.UserType == userType
	}, msg)
}

// BroadcastToRole 发送消息给特定角色的所有用户
func (m *Manager) BroadcastToRole(ctx context.Context, role models.UserRole, msg *models.HubMessage) int {
	return m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return c.Role == role
	}, msg)
}

// BroadcastToClientType 发送消息给特定客户端类型
func (m *Manager) BroadcastToClientType(ctx context.Context, clientType models.ClientType, msg *models.HubMessage) int {
	return m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return c.ClientType == clientType
	}, msg)
}

// BroadcastToDepartment 发送消息给特定部门的所有用户
func (m *Manager) BroadcastToDepartment(ctx context.Context, department models.Department, msg *models.HubMessage) int {
	return m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return c.Department == department
	}, msg)
}

// ============================================================================
// 高级广播包装（基于 Deliver）
// ============================================================================

// BroadcastPriority 根据优先级广播消息（全局广播，走 Deliver）
func (m *Manager) BroadcastPriority(ctx context.Context, msg *models.HubMessage, priority models.Priority) {
	msg.Priority = priority
	m.Deliver(ctx, msg, false)
}

// BroadcastAfterDelay 延迟广播消息（全局广播，走 Deliver）
func (m *Manager) BroadcastAfterDelay(ctx context.Context, msg *models.HubMessage, delay time.Duration) {
	syncx.Go(ctx).
		WithDelay(delay).
		Exec(func() {
			m.Deliver(ctx, msg, false)
		})
}

// BroadcastExclude 广播消息给所有客户端，但排除指定用户
func (m *Manager) BroadcastExclude(ctx context.Context, msg *models.HubMessage, excludeUserIDs []string) int {
	excludeMap := make(map[string]struct{}, len(excludeUserIDs))
	for _, userID := range excludeUserIDs {
		excludeMap[userID] = struct{}{}
	}

	return m.SendConditional(ctx, func(c *models.Client) bool {
		_, excluded := excludeMap[c.UserID]
		return !excluded
	}, msg)
}

// ============================================================================
// 获取客户端列表方法
// ============================================================================

// GetClientsByUserType 获取特定用户类型的所有客户端（委托 FilterClients 零拷贝）
func (m *Manager) GetClientsByUserType(userType models.UserType) []*models.Client {
	return m.host.GetShardedRegistry().FilterClients(func(c *models.Client) bool { return c.UserType == userType })
}

// GetClientsByRole 获取特定角色的所有客户端（委托 FilterClients 零拷贝）
func (m *Manager) GetClientsByRole(role models.UserRole) []*models.Client {
	return m.host.GetShardedRegistry().FilterClients(func(c *models.Client) bool { return c.Role == role })
}

// GetClientsByClientType 按客户端类型获取客户端（委托 FilterClients 零拷贝）
func (m *Manager) GetClientsByClientType(clientType models.ClientType) []*models.Client {
	return m.host.GetShardedRegistry().FilterClients(func(c *models.Client) bool { return c.ClientType == clientType })
}

// GetClientsByDepartment 获取特定部门的所有客户端（委托 FilterClients 零拷贝）
func (m *Manager) GetClientsByDepartment(department models.Department) []*models.Client {
	return m.host.GetShardedRegistry().FilterClients(func(c *models.Client) bool { return c.Department == department })
}

// GetClientsByVIPLevel 获取特定VIP等级及以上的客户端（委托 FilterClients 零拷贝）
func (m *Manager) GetClientsByVIPLevel(minVIPLevel models.VIPLevel) []*models.Client {
	minLevel := minVIPLevel.GetLevel()
	return m.host.GetShardedRegistry().FilterClients(func(c *models.Client) bool { return c.GetVIPLevel().GetLevel() >= minLevel })
}

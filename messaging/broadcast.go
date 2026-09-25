/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\messaging\broadcast.go
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
 *     · batchGetGroupMembers — 两个群组投递分派器调用，Pipeline 批量取成员
 *     （历史 crossNodeGroupsBroadcast / crossNodeMultiNamespaceGroupsBroadcast / resolveTargetGroups
 *      仅服务于已删除的 BroadcastToAllGroups/BroadcastToGroups，一并清理）
 *   - 内部广播：BroadcastToFiltered / BroadcastToUserIDs（预序列化 + 直接 TrySend）
 *   - 过滤类广播：BroadcastByUserType / BroadcastToRole / BroadcastToClientType / BroadcastToDepartment
 *     （按客户端属性过滤，与路由正交，保留）
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
// excludeSender 仅群组场景生效（P2P/广播场景 msg.Sender 不参与过滤）
//
// 返回非 nil *models.DeliverResult，错误收集到 result.Errors；按 result.Mode + 计数字段判断结果
func (m *Manager) Deliver(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
	// 统一入口：Clone + InjectRoute（注入 trace_id + 路由信封，appID 归一化，namespace 保留原值）
	// namespace 不在此归一化：空值在广播分支表示「全局广播」语义，需保留
	// P2P 分支由 sendToUserWithRetry 内部 EnsureRouteDefaults 兜底；
	// 群组分支 ns 是必要参数（群组按 appID+ns 信封分桶定位），缺失直接报错不兜底
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
	m.host.GetLogger().InfoContextKV(ctx, "[投递诊断] Deliver 路由决策",
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
// 委托 SendToUserWithRetry（内部已 EnsureRouteDefaults + InjectRoute，处理在线/离线/重试）
func (m *Manager) deliverP2P(ctx context.Context, msg *models.HubMessage) *models.DeliverResult {
	result := &models.DeliverResult{
		Mode:   models.DeliveryModeP2P,
		AppID:  routing.AppIDFromContext(ctx),
		Errors: make([]error, 0),
	}

	sr := m.SendToUserWithRetry(ctx, msg.Receiver, msg)
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

	// 群组按 appID+namespace 信封分桶隔离，ns 是定位群组成员的必要参数：
	// 缺失说明调用方漏传路由，静默补默认值会投错命名空间维度（跨租户隐患），直接报错
	if namespace == "" {
		result.AddError(models.ErrRouteNamespaceMissing)
		return result
	}

	if m.host.GetGroupRepo() == nil {
		result.AddError(models.ErrGroupRepoNotSet)
		return result
	}

	// msg 已 Clone（Deliver 入口），同步路由信封（namespace 已归一化）
	ctx = msg.ContextWithRoute(ctx, appID, namespace, groupIDs)

	// 1. Pipeline 批量获取所有群组成员并合并去重（多群组 N 次 GetMembers → 1 次 RTT）
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
		m.host.GetLogger().DebugContextKV(ctx, "过滤发送者后的群组成员列表",
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

	m.host.GetLogger().InfoContextKV(ctx, "群组消息投递完成",
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
// 复用历史 BroadcastToGroupMembers 逻辑：本地 BroadcastToUserIDs + 跨节点 crossNodeGroupBroadcast
// 仅投递当前在线成员，不存储离线消息，无重试，性能最优
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

	// 群组按 appID+namespace 信封分桶隔离，ns 是定位群组成员的必要参数：
	// 缺失说明调用方漏传路由，静默补默认值会投错命名空间维度（跨租户隐患），直接报错
	if namespace == "" {
		result.AddError(models.ErrRouteNamespaceMissing)
		return result
	}

	if m.host.GetGroupRepo() == nil {
		m.host.GetLogger().WarnContextKV(ctx, "群组仓库未设置，无法广播",
			"namespace", namespace, "group_ids", groupIDs)
		return result
	}

	// msg 已 Clone，同步路由信封（namespace 已归一化）
	ctx = msg.ContextWithRoute(ctx, appID, namespace, groupIDs)
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 1. Pipeline 批量获取所有群组成员并合并去重（多群组 N 次 GetMembers → 1 次 RTT）
	memberSet, err := m.batchGetGroupMembers(ctx, groupIDs)
	if err != nil {
		m.host.GetLogger().ErrorContextKV(ctx, "群组广播：批量获取群组成员失败",
			"namespace", namespace, "group_ids", groupIDs, "error", err)
		return result
	}
	members := make([]string, 0, len(memberSet))
	for uid := range memberSet {
		members = append(members, uid)
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
	localCount := m.BroadcastToUserIDs(ctx, targetMembers, msg)

	// 通知观察者（ctx 已注入路由，直接使用）
	m.NotifyObservers(ctx, msg)

	// 4. 跨节点广播：优先 gRPC 直连，降级 PubSub（ctx 已含完整路由）
	m.crossNodeGroupBroadcast(ctx, msg, excludeSender)

	m.host.GetLogger().InfoContextKV(ctx, "群组广播已发起",
		"namespace", namespace,
		"group_ids", groupIDs,
		"message_id", msg.MessageID,
		"total_members", len(members),
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

	// 本地按命名空间过滤广播（BroadcastToFiltered 内部 combinedCondition 会叠加路由信封匹配，此处 condition 仅作业务兜底）
	count := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return c.Namespace == namespace
	}, msg)

	// 通知观察者（命名空间级广播事件）
	m.NotifyObservers(ctx, msg)

	// 跨节点命名空间广播（提交到分布式池经 routeToCluster 投递）
	opts := cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
		Namespace: namespace,
	}
	if !m.host.SubmitClusterDispatch(msg, opts) {
		m.host.GetLogger().WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点命名空间广播",
			"namespace", namespace, "message_id", msg.MessageID)
	}

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
	if !m.host.SubmitClusterDispatch(msg, opts) {
		m.host.GetLogger().WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点广播",
			"message_id", msg.MessageID)
	}

	// 本地广播（直接异步执行，不经过 EventLoop channel 串行化）
	go m.handleBroadcast(msg)

	// 全局广播为异步，LocalDelivered 不计入（与历史 Broadcast 无返回值语义一致）
	return result
}

// crossNodeGroupBroadcast 跨节点群组广播（单/多群组统一入口）
//
// 统一走 OperationTypeGroupsBroadcast 复数语义，信封携带全部 GroupIDs，
// 单群组作为 GroupIDs=[groupID] 的特例；提交到分布式池经 routeToCluster
// 投递（gRPC 直连优先 + PubSub 兜底），消除 per-message goroutine
// namespace/groupIDs 从 ctx 提取
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

	if !m.host.SubmitClusterDispatch(msg, opts) {
		m.host.GetLogger().WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点群组广播",
			"namespace", namespace, "group_ids", groupIDs,
			"message_id", msg.MessageID)
	}
}

// batchGetGroupMembers 批量获取多个群组成员并合并去重
// appID/namespace 从 ctx 路由信封提取（上游已注入，不再显式传参）
// 使用 Redis Pipeline 一次 RTT 获取所有群组成员，O(totalMembers) 去重
// 相比逐群组 N 次 GetMembers（N 次 RTT），降为 1 次 RTT（单群组等价）
// 单个群组查询失败仅该 key 缺失，不影响其他群组（与历史逐群组 continue 语义一致）
// 整体 Pipeline 失败才返回错误
func (m *Manager) batchGetGroupMembers(ctx context.Context, groupIDs []string) (map[string]struct{}, error) {
	memberSet := make(map[string]struct{})
	if len(groupIDs) == 0 || m.host.GetGroupRepo() == nil {
		return memberSet, nil
	}

	appID, namespace := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	groupMembers, err := m.host.GetGroupRepo().GetMultiGroupMembers(ctx, appID, namespace, groupIDs)
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
			m.routeDeliveryFallback(msg, client, client.UserID)
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
	// 广播完成统计：Info 级保证生产可见（是否发出、发到多少客户端），分段耗时定位卡点
	m.host.GetLogger().InfoContextKV(ctx, "[投递诊断] 过滤广播完成",
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
	// 路由信封同步：从 ctx 恢复路由到 msg 信封（幂等）
	// 上游 deliverToGroupFireForget 已注入，此处作为二次兜底
	// InjectRoute 同时回写 ctx，保证下游 ctx 与信封一致
	ctx = msg.InjectRoute(ctx)

	// 准入闸门（必达恒放行；普通/高频过载时延迟路由）
	if verdict := m.host.AdmitMessage(msg, true); verdict != overload.VerdictAdmit {
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.broadcastToUserIDsNow(c, userIDs, bm)
		})
		return 0
	}

	// ⏱出向整形（必达级跳过）
	if shaper := m.host.GetBroadcastShaper(); shaper != nil &&
		msg.ResolveGuarantee() != models.GuaranteeGuaranteed &&
		!shaper.Allow() {
		m.host.GetOverloadMetrics().RecordShaperDenied()
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.broadcastToUserIDsNow(c, userIDs, bm)
		})
		return 0
	}

	return m.broadcastToUserIDsNow(ctx, userIDs, msg)
}

// broadcastShardParallelUsers 用户数超过该阈值时启用分片并行扇出（低于则串行，省 goroutine 开销）
const broadcastShardParallelUsers = 512

// broadcastToUserIDsNow 预序列化 + 按 userIDs 扇出（延迟队列重投目标，不重复准入）
//
// 顺序保证（10w 级成员扇出的核心约束）：
//   - 按 userID 均匀分片并行，同一 userID 的全部连接固定落在同一分片内串行投递，
//     配合 client.sendChan 的 FIFO 语义 → 每个用户收到的消息顺序 = 广播调用顺序
//   - 入口同步（WaitGroup 等全部 worker 完成）→ 调用方顺序调用两次广播，
//     各用户 sendChan 的写入顺序与调用顺序一致
func (m *Manager) broadcastToUserIDsNow(ctx context.Context, userIDs []string, msg *models.HubMessage) int {
	if len(userIDs) == 0 {
		return 0
	}

	// 预序列化 WebSocket 消息（仅一次；引擎由 go-toolbox/pkg/json 构建标签决定）
	data, err := json.Marshal(msg)
	if err != nil {
		m.host.GetLogger().ErrorContextKV(ctx, "群组广播消息序列化失败", "error", err)
		return 0
	}

	dataLen := len(data)
	var successCount int32

	// 分片扇出闭包（单分片内串行按 userID 投递，保用户内顺序）
	fanout := func(ids []string) {
		// 按用户ID查找客户端（O(m)，仅锁定相关 shard，不遍历全部连接）
		// 使用 ForEachUserClientFiltered 叠加路由信封(appId+namespace)匹配：
		//   - 群组消息：client 必须同 app/ns 且 client.groupID 在 msg.GroupIDs 中
		//   - 避免新加入群的成员通过"旧 userIDs 列表"收到不匹配其 group 的历史消息（与项目约束一致）
		// 🔒 首连门闩：成员处于离线回放中时暂存本次投递，回放完成后按序补投
		// （闭包捕获 data/msg，data 为预序列化不可变字节；msg 仅被 SSE TrySend 引用
		// 读取，无并发写）
		for _, userID := range ids {
			deliver := func() {
				m.host.GetShardedRegistry().ForEachUserClientFiltered(userID, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *models.Client) bool {
					if client.IsClosed() {
						return true
					}
					if client.ConnectionType == models.ConnectionTypeSSE {
						// SSE 客户端发送 msg 对象
						if client.TrySendSSE(msg) {
							atomic.AddInt32(&successCount, 1)
						}
					} else {
						// WebSocket 客户端发送预序列化数据
						if client.TrySend(data) {
							atomic.AddInt32(&successCount, 1)
							m.host.TrackReceiverMessageStats(client.ID, client.UserType, dataLen)
						} else {
							// 成员级分级兜底：普通/必达转离线（用户下次上线/重连时补发），高频语义丢弃
							m.routeDeliveryFallback(msg, client, userID)
						}
					}
					return true
				})
			}
			m.HoldUserDelivery(userID, deliver)
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

	// 群组广播本地投递统计：Info 级保证生产可见（成员在线但 0 投递 = 本地无连接，跨节点由 clusterBatcher 负责）
	m.host.GetLogger().InfoContextKV(ctx, "[投递诊断] 群组广播本地投递完成",
		"message_id", msg.MessageID,
		"user_count", len(userIDs),
		"success", totalSuccess,
		"group_ids", msg.GroupIDs,
	)

	return int(totalSuccess)
}

// ============================================================================
// 过滤类广播方法（按客户端属性过滤，与路由正交，保留）
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

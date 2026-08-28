/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-28 00:00:00
 * @FilePath: \go-wsc\hub\self_heal.go
 * @Description: 跨节点死索引自愈 — user_not_found 回告重路由 + 幽灵连接回收
 *
 * 修复场景（断线重连跨节点漂移）：
 *   客户端先连 PodA，断开后重连漂移到 PodB，此时：
 *   1. PodA 延迟/失败的清理让在线索引仍指向 PodA（死条目）→ 消息路由到 PodA 扑空
 *   2. PodA 上残留的同 clientID 幽灵连接（TCP 半开）继续占用注册表
 *
 * 三层防线：
 *   ① owner 归属校验（Lua 脚本）：旧节点延迟清理不再误删新节点已接管的索引条目
 *   ② user_not_found 回告：目标节点定向投递扑空时回告发送节点，发送方秒级
 *      重查索引重路由（用户已迁移）/ 立即转离线（用户真实离线），不再干等 30s ACK 超时
 *   ③ client_reclaim 回收：新节点注册时检测同 clientID 归属漂移，通知旧节点踢掉幽灵连接
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// user_not_found 重路由守卫
// ============================================================================

const (
	// rerouteGuardTTL 守卫条目存活时间（覆盖消息发出到 ACK 超时终态的最大窗口）
	rerouteGuardTTL = 2 * time.Minute
	// rerouteGuardSweepInterval 守卫过期条目周期清扫间隔（防长期驻留内存泄漏）
	rerouteGuardSweepInterval = 90 * time.Second
)

// errUserNotFoundAllRejected 所有定向投递节点均回告用户不存在（立即转离线的投递失败原因）
var errUserNotFoundAllRejected = errors.New("所有定向投递节点均回告用户不存在")

// rerouteGuardEntry 单条消息的重路由守卫条目
// 记录该消息已被哪些节点尝试/拒绝，重路由时只投递"从未尝试过的新节点"：
//   - 防重复：已尝试节点（含成功投递的）不再投递，多端场景不产生重复消息
//   - 防丢失：已拒绝节点排除后仅剩新节点时定向补投，用户迁移后消息秒级追达
//   - 防循环：索引抖动时不会在拒绝节点间 ping-pong
type rerouteGuardEntry struct {
	mu        sync.Mutex
	attempted map[string]struct{} // 已尝试投递的节点（初始路由 + 重路由累计）
	rejected  map[string]struct{} // 已回告 user_not_found 的节点
	p2p       bool                // true=P2P 定向路由（离线未预存，全拒时立即转离线）；false=广播兜底（离线已预存）
	expiresAt time.Time
}

// markRerouteAttempted 记录消息的跨节点投递目标（发送侧调用）
// p2p=true 表示 P2P 定向路由（checkAndRouteToNode），离线未预存；
// p2p=false 表示广播兜底（routeToClusterForOfflineUser），离线已由发送路径预存
func (h *Hub) markRerouteAttempted(messageID string, targetNodes []string, p2p bool) {
	if messageID == "" {
		return
	}
	now := time.Now()
	v, _ := h.rerouteGuard.LoadOrStore(messageID, &rerouteGuardEntry{
		attempted: make(map[string]struct{}, len(targetNodes)),
		rejected:  make(map[string]struct{}),
		p2p:       p2p,
		expiresAt: now.Add(rerouteGuardTTL),
	})
	entry := v.(*rerouteGuardEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	// 懒过期：条目已超时视为新消息（重置状态，防止同 messageID 长间隔复用串台）
	if now.After(entry.expiresAt) {
		entry.attempted = make(map[string]struct{}, len(targetNodes))
		entry.rejected = make(map[string]struct{})
		entry.p2p = p2p
		entry.expiresAt = now.Add(rerouteGuardTTL)
	}
	// p2p 标记只增不减（先广播兜底后 P2P 重试的场景以 P2P 语义为准）
	if p2p {
		entry.p2p = true
	}
	for _, nodeID := range targetNodes {
		if nodeID != "" && nodeID != h.nodeID {
			entry.attempted[nodeID] = struct{}{}
		}
	}
}

// markRerouteRejected 记录回告拒绝节点，返回守卫条目（不存在则懒创建）
func (h *Hub) markRerouteRejected(messageID, rejectedNode string) *rerouteGuardEntry {
	now := time.Now()
	v, _ := h.rerouteGuard.LoadOrStore(messageID, &rerouteGuardEntry{
		attempted: make(map[string]struct{}),
		rejected:  make(map[string]struct{}),
		expiresAt: now.Add(rerouteGuardTTL),
	})
	entry := v.(*rerouteGuardEntry)

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if now.After(entry.expiresAt) {
		entry.attempted = make(map[string]struct{})
		entry.rejected = make(map[string]struct{})
		entry.p2p = false // 懒创建/过期条目按保守语义处理（不主动转离线）
		entry.expiresAt = now.Add(rerouteGuardTTL)
	}
	if rejectedNode != "" && rejectedNode != h.nodeID {
		entry.rejected[rejectedNode] = struct{}{}
	}
	return entry
}

// sweepRerouteGuard 周期清扫过期的守卫条目（防内存泄漏；正常路径由 ACK 终态删除）
func (h *Hub) sweepRerouteGuard() {
	now := time.Now()
	h.rerouteGuard.Range(func(key, value any) bool {
		entry, ok := value.(*rerouteGuardEntry)
		if !ok {
			h.rerouteGuard.Delete(key)
			return true
		}
		entry.mu.Lock()
		expired := now.After(entry.expiresAt)
		entry.mu.Unlock()
		if expired {
			h.rerouteGuard.Delete(key)
		}
		return true
	})
}

// ============================================================================
// user_not_found 回告处理（发送节点侧）
// ============================================================================

// handleDistributedUserNotFound 发送节点处理 user_not_found 回告（PubSub 定向投递扑空的目标节点回告）
// 薄包装：提取信封后复用 decideUserNotFoundReroute 核心决策（与 gRPC userMiss 路径共用）
//
// 决策链：
//  1. 守卫记录已拒绝节点（防索引抖动 ping-pong 循环）
//  2. 重查在线索引：出现"从未尝试过的新节点"（用户已迁移）→ 定向重路由补投
//  3. 所有已尝试节点均回告不存在（用户真实离线/索引死条目）→ P2P 路径立即转离线，
//     不再干等 30s ACK 超时；广播兜底路径离线已预存，仅静默结束防重复
func (h *Hub) handleDistributedUserNotFound(ctx context.Context, distMsg *DistributedMessage) error {
	if distMsg.Message == nil {
		return fmt.Errorf("user_not_found 回告缺少原始消息体")
	}
	msg := distMsg.Message
	userID := distMsg.TargetID
	if userID == "" || msg.MessageID == "" {
		return nil
	}

	h.logger.InfoContextKV(ctx, "📥 收到 user_not_found 回告，触发重路由决策",
		"message_id", msg.MessageID,
		"user_id", userID,
		"from_node", distMsg.NodeID,
	)

	// 路由来源优先级与 handleDistributedSendMessage 一致：外层信封 > 内层信封
	appID := mathx.IfEmpty(distMsg.AppID, msg.AppID)
	appID, _ = routing.NormalizeRoute(appID, "")
	namespace := mathx.IfEmpty(distMsg.Namespace, msg.Namespace)

	h.decideUserNotFoundReroute(ctx, msg, userID, distMsg.NodeID, appID, namespace)
	return nil
}

// decideUserNotFoundReroute 用户扑空信号的核心重路由决策（PubSub 回告与 gRPC userMiss 共用）
//
// missNode = 明确声称"用户不在该节点"的节点 ID；appID/namespace 为按投递信封归一化后的查询维度
// 返回 true 表示已向新节点定向补投（调用方应跳过广播兜底，防同一消息重复投递）
func (h *Hub) decideUserNotFoundReroute(ctx context.Context, msg *HubMessage, userID, missNode, appID, namespace string) bool {
	if h.onlineStatusRepo == nil {
		return false
	}

	// 1. 更新守卫（记录已拒绝节点）
	entry := h.markRerouteRejected(msg.MessageID, missNode)

	// 2. 重查在线索引（source of truth，按投递信封过滤）
	queryCtx := routing.NewRoute().WithAppID(appID).WithNamespace(namespace).Inject(ctx)

	nodeIDs, err := h.onlineStatusRepo.GetUserNodes(queryCtx, userID)
	if err != nil {
		h.logger.WarnContextKV(ctx, "user_not_found 重查在线索引失败，保留 ACK 超时兜底",
			"message_id", msg.MessageID,
			"user_id", userID,
			"error", err,
		)
		return false
	}

	// 3. 计算重路由候选：当前索引中"未尝试且未拒绝"的其他节点（用户迁移到的新节点）
	entry.mu.Lock()
	var candidates []string
	for _, nodeID := range nodeIDs {
		if nodeID == "" || nodeID == h.nodeID {
			continue
		}
		if _, tried := entry.attempted[nodeID]; tried {
			continue
		}
		if _, rejected := entry.rejected[nodeID]; rejected {
			continue
		}
		candidates = append(candidates, nodeID)
	}
	// 全拒判定：attempted ⊆ rejected（所有已尝试节点都回告不存在）
	allRejected := len(entry.attempted) > 0
	for n := range entry.attempted {
		if _, ok := entry.rejected[n]; !ok {
			allRejected = false
			break
		}
	}
	p2p := entry.p2p
	if len(candidates) > 0 {
		// 先记账再投递（防止回告先于记账到达引发同节点二次投递）
		for _, n := range candidates {
			entry.attempted[n] = struct{}{}
		}
	}
	entry.mu.Unlock()

	// 4a. 用户已迁移到新节点 → 定向重路由补投
	if len(candidates) > 0 {
		h.logger.InfoContextKV(ctx, "🔄 用户已迁移到新节点，定向重路由补投",
			"message_id", msg.MessageID,
			"user_id", userID,
			"reroute_nodes", candidates,
			"from_node", missNode,
		)
		opts := ClusterDispatchOptions{
			Operation:     OperationTypeSendMessage,
			TargetNodeIDs: candidates,
			TargetUserID:  userID,
		}
		if rErr := h.routeToCluster(ctx, msg, opts); rErr != nil {
			// 重路由失败不转离线：保留 ACK 超时兜底（30s 后超时转离线）
			h.logger.WarnContextKV(ctx, "user_not_found 重路由失败，保留 ACK 超时兜底",
				"message_id", msg.MessageID,
				"user_id", userID,
				"reroute_nodes", candidates,
				"error", rErr,
			)
			return false
		}
		return true
	}

	// 4b. 无新节点可路由：所有已尝试节点均回告不存在 + P2P 路径（离线未预存）→ 立即转离线
	// 复用 tryStoreOfflineOnDeliveryFailure：含离线源防循环、状态覆盖 UserOffline、ACK 超时任务取消
	if allRejected && p2p {
		h.logger.WarnContextKV(ctx, "所有定向节点均回告用户不存在，立即转离线",
			"message_id", msg.MessageID,
			"user_id", userID,
			"rejected_nodes", missNode,
		)
		h.tryStoreOfflineOnDeliveryFailure(msg, errUserNotFoundAllRejected)
		// 终态清理守卫条目
		h.rerouteGuard.Delete(msg.MessageID)
	}
	return false
}

// ============================================================================
// user_not_found 回告发布（目标节点侧）
// ============================================================================

// replyUserNotFound 回告发送节点：用户不在本节点（索引死条目自愈信号）
// 发送方收到后秒级重查索引重路由/转离线，不再干等 30s ACK 超时
// 仅 PubSub 定向投递路径需要回告（gRPC 路径的响应体已即时携带 Success=false）
func (h *Hub) replyUserNotFound(ctx context.Context, distMsg *DistributedMessage) {
	if h.pubsub == nil || distMsg.NodeID == "" || distMsg.NodeID == h.nodeID {
		// gRPC-only 集群无法经 PubSub 回告：gRPC 响应体已即时告知发送方，无需回告
		return
	}
	reply := &DistributedMessage{
		Type:      OperationTypeUserNotFound,
		NodeID:    h.nodeID,
		TargetID:  distMsg.TargetID,
		Message:   distMsg.Message,
		Timestamp: time.Now(),
		AppID:     distMsg.AppID,
		Namespace: distMsg.Namespace,
		Reason:    "target_user_not_on_node",
	}
	reply.InjectContext(ctx)
	// deadNodes 忽略：回告丢失仅退化为 30s ACK 超时兜底（publishToTargetedNodes 已打 Warn 日志）
	if _, err := h.publishToTargetedNodes(ctx, reply, []string{distMsg.NodeID}); err != nil {
		h.logger.WarnContextKV(ctx, "user_not_found 回告发布失败",
			"message_id", distMsg.Message.GetMessageID(),
			"user_id", distMsg.TargetID,
			"from_node", distMsg.NodeID,
			"error", err,
		)
	}
}

// ============================================================================
// 死索引自愈（目标节点侧）
// ============================================================================

// selfHealDeadIndexEntries 自愈清理指向本节点的死索引（异步执行，不阻塞订阅回调）
//
// 死索引成因：本节点连接断开时清理失败（进程崩溃/Redis 抖动/清理在途），
// user_clients ZSET 与 client:<id> 仍指向本节点，其他节点 GetUserNodes 会持续路由消息到本节点扑空
//
// 清理安全性（三重防护）：
//  1. 仅清理 client.NodeID == 本节点 的条目（他节点活跃条目不触碰）
//  2. 离线 Lua 脚本内置 owner 归属校验：条目已被其他节点接管时仅清本节点集合，共享索引不动
//  3. 清理前二次确认本地确实无该用户连接（防御 HasUser 判定后用户恰好重连回本节点的竞态）
func (h *Hub) selfHealDeadIndexEntries(ctx context.Context, userID, appID, namespace string) {
	if h.onlineStatusRepo == nil || userID == "" {
		return
	}
	syncx.Go().
		OnPanic(func(r any) {
			h.logger.ErrorContextKV(ctx, "索引自愈 panic",
				"user_id", userID, "panic", r)
		}).
		Exec(func() {
			h.doSelfHealDeadIndexEntries(ctx, userID, appID, namespace)
		})
}

// doSelfHealDeadIndexEntries 自愈清理实现（在线程池 goroutine 中执行）
func (h *Hub) doSelfHealDeadIndexEntries(ctx context.Context, userID, appID, namespace string) {
	// 按投递信封查询该用户的全部索引条目
	queryCtx := routing.NewRoute().WithAppID(appID).WithNamespace(namespace).Inject(ctx)
	clients, err := h.onlineStatusRepo.GetUserClients(queryCtx, userID)
	if err != nil {
		// 用户无任何索引条目（ErrTypeUserNotFound）无需自愈；其他错误记日志
		if errorx.ClassifyError(err) != models.ErrTypeUserNotFound {
			h.logger.WarnContextKV(ctx, "索引自愈查询用户客户端失败",
				"user_id", userID, "error", err)
		}
		return
	}

	// 二次确认：用户未重连回本节点（自愈是异步的，HasUser 判定可能已过时）
	if h.shardedRegistry.HasUser(userID, appID, namespace) {
		return
	}

	// 仅清理指向本节点的条目（他节点条目不动，防误删活跃连接索引）
	deadClients := make([]*Client, 0, len(clients))
	for _, c := range clients {
		if c != nil && c.NodeID == h.nodeID {
			deadClients = append(deadClients, c)
		}
	}
	if len(deadClients) == 0 {
		return
	}

	if err := h.onlineStatusRepo.BatchSetClientsOfflineWithInfo(ctx, deadClients); err != nil {
		h.logger.WarnContextKV(ctx, "索引自愈清理死索引失败",
			"user_id", userID,
			"cleaned_clients", len(deadClients),
			"error", err,
		)
		return
	}
	h.logger.InfoContextKV(ctx, "🩹 已自愈清理指向本节点的死索引",
		"user_id", userID,
		"cleaned_clients", len(deadClients),
		"app_id", appID,
		"namespace", namespace,
	)
}

// ============================================================================
// 幽灵连接回收（client_reclaim）
// ============================================================================

// detectClientMigration 检测同 clientID 跨节点迁移（必须在 syncOnlineStatus 覆写 owner key 之前调用）
// 返回旧归属节点 ID（空串表示无迁移）；断线重连漂移到本节点时，旧节点可能残留幽灵连接，
// 消息按索引路由到旧节点会扑空 → 需通知旧节点回收
func (h *Hub) detectClientMigration(ctx context.Context, client *Client) string {
	if client == nil || client.ID == "" || h.onlineStatusRepo == nil {
		return ""
	}
	// 单机模式无跨节点迁移
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return ""
	}
	owner, err := h.onlineStatusRepo.GetClientOwner(ctx, client.ID)
	if err != nil {
		h.logger.WarnContextKV(ctx, "查询客户端归属节点失败",
			"client_id", client.ID,
			"user_id", client.UserID,
			"error", err,
		)
		return ""
	}
	return owner
}

// notifyClientReclaim 通知旧节点回收幽灵连接（须在 syncOnlineStatus 写入本节点 owner 之后调用：
// 此时旧节点收到回收后的清理受 Lua 归属校验保护，owner 已是本节点，仅清自身集合不动共享索引）
func (h *Hub) notifyClientReclaim(ctx context.Context, client *Client, ownerNode string) {
	if h.pubsub == nil || client == nil || ownerNode == "" || ownerNode == h.nodeID {
		return
	}
	h.logger.InfoContextKV(ctx, "检测到客户端跨节点迁移，通知旧节点回收幽灵连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"old_node", ownerNode,
		"new_node", h.nodeID,
		"connected_at", client.ConnectedAt,
	)
	dispatch := &DistributedMessage{
		Type:      OperationTypeClientReclaim,
		NodeID:    h.nodeID,
		TargetID:  client.ID,
		Timestamp: client.ConnectedAt, // 新连接建立时间，旧节点据此判定本地连接是否为幽灵
		AppID:     client.AppID,
		Namespace: client.Namespace,
		Reason:    "client_migrated",
	}
	dispatch.InjectContext(ctx)
	// deadNodes 忽略：回收通知丢失由旧节点连接超时清理兜底（publishToTargetedNodes 已打 Warn 日志）
	if _, err := h.publishToTargetedNodes(ctx, dispatch, []string{ownerNode}); err != nil {
		h.logger.WarnContextKV(ctx, "幽灵连接回收通知发布失败",
			"client_id", client.ID,
			"old_node", ownerNode,
			"error", err,
		)
	}
}

// handleDistributedClientReclaim 旧节点回收同 clientID 幽灵连接（断线重连跨节点迁移）
// distMsg.TargetID = clientID，distMsg.Timestamp = 新节点连接建立时间
func (h *Hub) handleDistributedClientReclaim(ctx context.Context, distMsg *DistributedMessage) error {
	clientID := distMsg.TargetID
	if clientID == "" {
		return nil
	}

	client, ok := h.shardedRegistry.GetClient(clientID)
	if !ok || client == nil {
		return nil // 本地无该 clientID 连接，无需回收
	}

	// 时序守卫：本地连接不早于新节点连接（漂移又漂回本节点的场景）→ 本地才是最新连接，不回收
	if !client.ConnectedAt.Before(distMsg.Timestamp) {
		h.logger.InfoContextKV(ctx, "本地连接不早于新节点连接，跳过幽灵回收",
			"client_id", clientID,
			"local_connected_at", client.ConnectedAt,
			"remote_connected_at", distMsg.Timestamp,
			"from_node", distMsg.NodeID,
		)
		return nil
	}

	h.logger.WarnContextKV(ctx, "♻️ 回收幽灵连接：clientID 已迁移到新节点",
		"client_id", clientID,
		"user_id", client.UserID,
		"new_node", distMsg.NodeID,
		"local_connected_at", client.ConnectedAt,
	)
	// kick 内部 Unregister → SetClientOffline 受 Lua 归属校验保护：
	// owner 已是新节点，仅清本节点集合，不动新节点已接管的共享索引
	h.kickClientWithNotification(client, DisconnectReasonKickOut, "连接已迁移到新节点，本地连接已回收")
	return nil
}

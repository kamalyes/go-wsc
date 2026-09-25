/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-25 20:36:01
 * @FilePath: \go-wsc\hub\cluster_dispatch.go
 * @Description: 统一跨节点路由 — 所有跨节点通信的唯一入口
 *
 * 设计理念：一套逻辑，namespace 贯穿，传输透明
 *   - 调用方只关心「发什么、发给谁」，不关心走 gRPC 还是 PubSub
 *   - routeToCluster() 集中决策：gRPC 直连（已知目标）→ PubSub 兜底（广播）
 *   - namespace/group 作为一等公民，空值自动归入 "default"
 *
 * 调用方式：
 *   - 用户消息：routeToCluster(op=SendMessage, targetUserID=xxx)
 *   - 群组广播：routeToCluster(op=GroupsBroadcast, groupIDs=xxx, namespace=xxx)
 *   - 全局广播：routeToCluster(op=Broadcast, namespace="" 表示全命名空间)
 *   - 观察者通知：routeToCluster(op=ObserverNotify, namespace=xxx)
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// 类型定义
// ============================================================================

// ClusterOperation/ClusterDispatchOptions 已迁至 cluster 包（cluster/dispatch_options.go），
// 本文件统一使用 cluster.ClusterDispatchOptions。

// clusterRouteResult 路由结果（内部使用）
type clusterRouteResult struct {
	grpcDelivered  int      // gRPC 成功投递节点数
	pubsubFallback []string // 需 PubSub 兜底的节点列表（地址未知/调用失败）
	userMissNodes  []string // gRPC 目标节点明确返回"用户不在"的节点列表（索引过期/用户已迁移）
}

// grpcDispatchOutcome 单节点 gRPC 投递结果
type grpcDispatchOutcome int

const (
	grpcOutcomeDelivered grpcDispatchOutcome = iota // 投递成功
	grpcOutcomeFallback                             // 调用失败或结果未知 → PubSub 定向兜底重试
	grpcOutcomeUserMiss                             // 目标节点明确用户不在 → 不对该节点兜底（PubSub 定向发布同样会扑空）
)

// deadNodeProbeRetryDelay 定向发布返回 0 且节点心跳正常时的重试等待
// 覆盖订阅断连重连窗口（秒级），避免订阅抖动误判死节点导致消息批量误转离线
const deadNodeProbeRetryDelay = 300 * time.Millisecond

// ============================================================================
// 统一路由入口
// ============================================================================

// routeToCluster 统一跨节点路由入口
//
// 决策链：
//  1. gRPC 直连：已知目标节点 gRPC 地址时，点对点精确投递（低延迟）
//  2. PubSub 兜底：gRPC 未启用/地址未知/调用失败时，降级到 Redis PubSub 广播
//
// 参数：
//   - ctx: 上下文
//   - msg: 消息体（路由元数据由 opts.Namespace 携带，不再写入 msg）
//   - opts: 分发选项
//
// 返回：error（nil 表示至少一个节点投递成功或无需跨节点）
func (h *Hub) routeToCluster(ctx context.Context, msg *models.HubMessage, opts cluster.ClusterDispatchOptions) error {
	// 单机模式：无 PubSub 且无 gRPC，不跨节点
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return nil
	}

	// AppID：msg 信封优先，opts 兜底；空值归一化为 DefaultAppID（入口层策略一致）
	appID := constants.NormalizeAppID(mathx.IfEmpty(msg.AppID, opts.AppID))

	// 🔏 路由信封解析：msg 信封优先（入口层已注入，异步/跨节点链路持久化），opts 仅作兜底
	// - Broadcast 操作：空 Namespace 表示全命名空间广播，不归一化为 "default"
	// - 其他操作：空 Namespace 保持空（由接收端从 msg 内层信封再兜底）
	namespace := mathx.IfEmpty(msg.Namespace, opts.Namespace) // GroupIDs：msg 非空则 clone 一份（避免后续 append 污染原 msg），否则用 opts
	var groupIDs []string
	if len(msg.GroupIDs) > 0 {
		groupIDs = append([]string(nil), msg.GroupIDs...)
	} else {
		groupIDs = opts.GroupIDs
	}

	h.logger.DebugContextKV(ctx, "📨 [投递诊断] 跨节点路由发起",
		"operation", opts.Operation,
		"app_id", appID,
		"namespace", namespace,
		"target_node", opts.TargetNodeID,
		"target_user", opts.TargetUserID,
		"group_ids", groupIDs,
		"grpc_enabled", h.IsGRPCEnabled(),
		"message_id", msg.GetMessageID(),
	)

	// 构建分发信封（路由信封携带 app_id + namespace + group_ids，接收端按此过滤）
	dispatch := &models.DistributedMessage{
		Type:          opts.Operation,
		NodeID:        h.nodeID,
		TargetID:      resolveDispatchTargetID(opts),
		Message:       msg,
		Reason:        opts.Reason,
		Timestamp:     time.Now(),
		AppID:         appID,
		Namespace:     namespace,
		GroupIDs:      groupIDs,
		ExcludeSender: opts.ExcludeSender, // 跨节点群组广播 PubSub 兜底需携带，接收端据此排除发送者
		SenderID:      opts.SenderID,
	}

	// 从 ctx 注入 trace_id（消息已有则复用，跨节点保留源 trace）
	dispatch.InjectContext(ctx)

	// ① 尝试 gRPC 直连
	result := h.dispatchViaGRPC(ctx, msg, opts)
	if result.grpcDelivered > 0 {
		h.overloadMetrics.RecordClusterGRPC(result.grpcDelivered)
	}

	// ①' gRPC 目标节点明确用户不在（user_not_found 的 gRPC 等价信号，无需经 PubSub 回告绕圈）：
	// 触发秒级重路由决策——重查索引发现用户已迁移时向新节点定向补投（返回 rerouted=true），
	// 后续跳过广播兜底（定向补投已覆盖，广播再投会造成同一消息重复投递）。
	// 仅 P2P 消息投递场景（SendMessage + TargetUserID 非空）：KickUser 扑空等场景无需重路由
	rerouted := false
	if opts.Operation == models.OperationTypeSendMessage && opts.TargetUserID != "" && len(result.userMissNodes) > 0 {
		for _, missNode := range result.userMissNodes {
			if h.decideUserNotFoundReroute(ctx, msg, opts.TargetUserID, missNode, appID, namespace) {
				rerouted = true
			}
		}
	}

	// ② 所有节点 gRPC 成功，无需 PubSub
	if len(result.pubsubFallback) == 0 && result.grpcDelivered > 0 {
		h.logger.DebugContextKV(ctx, "📨 [投递诊断] 跨节点路由完成（gRPC 全覆盖）",
			"operation", opts.Operation,
			"grpc_delivered", result.grpcDelivered,
			"message_id", msg.GetMessageID(),
		)
		return nil
	}

	// ③ PubSub 兜底：gRPC 未覆盖的节点走 PubSub
	if h.pubsub != nil {
		var pubErr error
		var deadNodes []string
		if result.grpcDelivered > 0 || len(result.pubsubFallback) > 0 {
			// 已知目标节点（gRPC 部分成功的剩余节点，或 gRPC 全失败的兜底节点）
			// 定向发布到节点专属频道，避免广播频道冗余投递到无关节点
			// 关键修复：gRPC 未启用 + opts.TargetNodeIDs 来自 Redis 在线索引（如 otherNodes=[3iy9vey]）时，
			// 必须走定向发布到 3iy9vey 专属频道，而不是广播频道（广播频道依赖接收端订阅，且无法定向）
			// userMissNodes 不参与定向兜底：目标节点已明确返回用户不在，定向发布必然扑空
			deadNodes, pubErr = h.publishToTargetedNodes(ctx, dispatch, result.pubsubFallback)
		} else if !rerouted {
			// 无目标节点列表（全局广播、群组广播场景）走广播频道；
			// 也覆盖"gRPC 目标全部明确用户不在"（userMissNodes 非空）的场景：
			// Redis 在线索引过期时用户可能已迁移到索引未知的节点，
			// 广播让实际持有该用户连接的节点投递（其余节点 HasUser 扑空自动跳过）
			// rerouted=true 时跳过：重路由已定向补投到新节点，广播再投会重复投递，
			// 索引查询失败等未决场景 rerouted=false 仍走广播兜底
			pubErr = h.publishToCluster(ctx, dispatch)
		}
		if pubErr != nil {
			h.logger.WarnContextKV(ctx, "PubSub 兜底发布失败",
				"operation", opts.Operation,
				"error", pubErr,
				"grpc_delivered", result.grpcDelivered,
				"pubsub_fallback", len(result.pubsubFallback),
				"user_miss", len(result.userMissNodes),
				"message_id", msg.GetMessageID(),
			)
			// gRPC 有部分成功则不算完全失败
			if result.grpcDelivered > 0 {
				return nil
			}
			return pubErr
		}

		// PubSub 兜底成功发布口径计数（gRPC 未覆盖节点时的降级频率，观测降级压力）
		h.overloadMetrics.RecordClusterPubSubFallback()

		// 🚨 死节点秒级兜底：定向频道无人订阅（Pod 挂掉/订阅断连重连中），消息未送达这些节点。
		// P2P 场景重查在线索引：用户所有连接所在节点均失活 → 实时投递无望，立即转离线
		// （不等 30s ACK 超时）；仍有健康节点 → 已通过 gRPC/定向 PubSub 收到，无需处理
		if len(deadNodes) > 0 && opts.Operation == models.OperationTypeSendMessage && opts.TargetUserID != "" {
			h.handleDeadNodesForP2P(ctx, msg, opts.TargetUserID, deadNodes)
		}

		h.logger.InfoContextKV(ctx, "📨 [投递诊断] 跨节点路由完成（PubSub 兜底）",
			"operation", opts.Operation,
			"grpc_delivered", result.grpcDelivered,
			"pubsub_fallback", len(result.pubsubFallback),
			"user_miss", len(result.userMissNodes),
			"dead_nodes", len(deadNodes),
			"message_id", msg.GetMessageID(),
		)
		return nil
	}

	// ④ gRPC 投递 0 节点 + PubSub 未启用 → 路由失败，让上层 fallback 到本地发送 + 离线存储
	// 走到这里说明 IsGRPCEnabled() == true 但 h.pubsub == nil，且 dispatchViaGRPC 没有任何节点投递成功
	// （nodeRegistry 不含其他节点 / opts.TargetNodeIDs 为空 / 目标节点全部返回用户不在）
	// 不返回 error 会让上层误判"已路由成功"→ sendToUser routed=true → 直接 return，消息丢失
	// rerouted=true 例外：userMiss 已触发重路由并定向补投成功，路由未失败
	if result.grpcDelivered == 0 && !rerouted {
		return fmt.Errorf("跨节点路由失败：gRPC 投递 0 节点（投递 %d 个、用户不在 %d 个、兜底 %d 个），PubSub 未启用，无可用投递路径",
			result.grpcDelivered, len(result.userMissNodes), len(result.pubsubFallback))
	}

	h.logger.DebugContextKV(ctx, "📨 [投递诊断] 跨节点路由完成（gRPC-only）",
		"operation", opts.Operation,
		"grpc_delivered", result.grpcDelivered,
		"pubsub_fallback", len(result.pubsubFallback),
		"message_id", msg.GetMessageID(),
	)

	return nil
}

// resolveDispatchTargetID 根据操作类型解析 TargetID
// 广播类操作无特定目标，返回空字符串（群组信息由 GroupIDs 携带）
func resolveDispatchTargetID(opts cluster.ClusterDispatchOptions) string {
	switch opts.Operation {
	case models.OperationTypeSendMessage, models.OperationTypeKickUser:
		return opts.TargetUserID
	default:
		return ""
	}
}

// dispatchKickToRemoteNodes 跨节点踢人分发：向用户连接所在的远端节点发送 kick 指令
//
// 经在线路由索引查询用户连接所在节点，排除本节点后定向分发（Hub.KickUser 调用）：
//   - gRPC 直连：executeGRPCDispatch 的 KickUser 分支 → 专用 KickUser RPC
//   - PubSub 兜底：DistributedMessage 信封（AppID/Namespace/Reason）→ handleDistributedKickUser
//
// 防回环：远端消费端（GRPCServer.KickUser / handleDistributedKickUser）直调
// LifecycleManager.KickUser 仅踢本地，不再跨节点分发
//
// 单机模式（无 PubSub 且无 gRPC）/ 索引未注入 / 用户仅在本节点时为 no-op
func (h *Hub) dispatchKickToRemoteNodes(ctx context.Context, userID, reason string) {
	// 单机模式：无跨节点通道，无需分发
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return
	}
	// 在线路由索引未注入：无法定位远端节点（踢出本节点连接后即结束）
	if h.onlineStatusRepo == nil {
		return
	}

	nodes, err := h.queryUserNodes(ctx, userID)
	if err != nil || len(nodes) == 0 {
		return
	}

	// 排除本节点（本节点连接已由本地踢出路径处理）
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	targetNodes := make([]string, 0, len(nodes))
	for _, nodeID := range nodes {
		if nodeID == "" || nodeID == h.nodeID {
			continue
		}
		targetNodes = append(targetNodes, nodeID)
	}
	if len(targetNodes) == 0 {
		return
	}

	h.logger.InfoContextKV(ctx, "跨节点踢人分发",
		"user_id", userID,
		"reason", reason,
		"app_id", appID,
		"namespace", ns,
		"target_nodes", targetNodes,
	)

	// 空壳消息：路由信封经 opts 携带（routeToCluster 内提取），远端仅按信封踢人不投递消息体
	opts := cluster.ClusterDispatchOptions{
		Operation:     models.OperationTypeKickUser,
		TargetUserID:  userID,
		Reason:        reason,
		TargetNodeIDs: targetNodes,
		AppID:         appID,
		Namespace:     ns,
	}
	h.SubmitClusterDispatch(models.NewHubMessage(), opts)
}

// ============================================================================
// gRPC 直连
// ============================================================================

// dispatchViaGRPC 通过 gRPC 向目标节点直连投递
//
// 返回路由结果：哪些节点成功、哪些需要 PubSub 兜底
func (h *Hub) dispatchViaGRPC(ctx context.Context, msg *models.HubMessage, opts cluster.ClusterDispatchOptions) clusterRouteResult {
	result := clusterRouteResult{}

	// 🔏 路由信封归一化（msg 优先，opts 兜底）：与 routeToCluster 构建 dispatch 信封口径一致
	// 广播类 RPC（BroadcastGroup/NotifyObservers）在服务端反序列化 msg 前就用 AppIDFromContext
	// 查成员/匹配，其路由仅来自 gRPC metadata，此处若 appID 为空会退 DefaultAppID → 跨租户隔离失效
	// 故必须在进入 gRPC 前把归一化信封回填到 opts，保证 gRPC 直连与 PubSub 兜底两条路径信封一致
	opts.AppID = constants.NormalizeAppID(mathx.IfEmpty(msg.AppID, opts.AppID))
	opts.Namespace = mathx.IfEmpty(msg.Namespace, opts.Namespace)
	if len(msg.GroupIDs) > 0 {
		opts.GroupIDs = append([]string(nil), msg.GroupIDs...)
	}

	// 全局/命名空间广播（Broadcast）无定向目标节点：语义为广播到所有节点，走 Redis PubSub
	// 广播频道（publishToCluster，targeted=false），不做 gRPC 定向直连。
	// 历史缺陷：Broadcast 曾复用 BroadcastGroup RPC（groupID 空）→ 服务端查空成员返回 Delivered=0 且
	// 无 error，发送端误判 grpcOutcomeDelivered 跳过 PubSub 兜底，导致跨节点广播静默丢失
	if opts.Operation == models.OperationTypeBroadcast {
		return result
	}

	if !h.IsGRPCEnabled() {
		// gRPC 未启用：优先用调用方已知的目标节点列表（P2P 场景已从 Redis 在线索引查到 otherNodes），
		// 否则 fallback 到 nodeRegistry 中的所有其他节点（群组/全局广播场景，nodeRegistry 由 gRPC 互连注册维护）
		// 关键：gRPC 未启用时 nodeRegistry 通常为空或不含其他节点，必须依赖 opts.TargetNodeIDs 才能定向 PubSub
		if len(opts.TargetNodeIDs) > 0 {
			result.pubsubFallback = opts.TargetNodeIDs
		} else {
			result.pubsubFallback = h.getAllClusterNodeIDs()
		}
		return result
	}

	msgData, err := wscpb.MarshalHubMessage(msg)
	if err != nil {
		h.logger.WarnContextKV(ctx, "gRPC 序列化失败，全部降级 PubSub",
			"operation", opts.Operation, "error", err)
		result.pubsubFallback = h.getAllClusterNodeIDs()
		return result
	}

	// 确定目标节点列表
	targetNodes := h.resolveGRPCTargetNodes(opts)
	if len(targetNodes) == 0 {
		return result // 无目标节点（可能本节点是集群唯一节点）
	}

	grpcCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// 并行投递（并发上限 8，与 grpcBroadcastGroups 对齐）：
	// 串行时单个死节点的 3s 超时会拖慢整批投递（N 节点最坏 3N 秒）；
	// 结果按索引写入无锁竞争，汇总在 wg.Wait 后串行进行
	outcomes := make([]grpcDispatchOutcome, len(targetNodes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, nodeID := range targetNodes {
		addr, ok := h.nodeRegistry.GetNodeAddr(nodeID)
		if !ok {
			outcomes[i] = grpcOutcomeFallback // 地址未知 → PubSub 兜底
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, addr string) {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[i] = h.dispatchNode(grpcCtx, addr, msgData, opts)
		}(i, addr)
	}
	wg.Wait()

	for i, nodeID := range targetNodes {
		switch outcomes[i] {
		case grpcOutcomeDelivered:
			result.grpcDelivered++
		case grpcOutcomeUserMiss:
			result.userMissNodes = append(result.userMissNodes, nodeID)
		default:
			result.pubsubFallback = append(result.pubsubFallback, nodeID)
		}
	}

	return result
}

// resolveGRPCTargetNodes 根据操作类型确定 gRPC 目标节点列表
// 优先级：TargetNodeID（单节点精确）> TargetNodeIDs（多节点定向）> nodeRegistry 所有其他节点（广播）
func (h *Hub) resolveGRPCTargetNodes(opts cluster.ClusterDispatchOptions) []string {
	if opts.TargetNodeID != "" {
		return []string{opts.TargetNodeID}
	}
	// P2P 场景已知目标节点列表时优先用，避免广播到无关节点（gRPC 启用时定向投递更高效）
	if len(opts.TargetNodeIDs) > 0 {
		return opts.TargetNodeIDs
	}
	return h.getAllClusterNodeIDs()
}

// executeGRPCDispatch 执行单次 gRPC 投递（按操作类型分发）
func (h *Hub) executeGRPCDispatch(ctx context.Context, addr string, msgData []byte, opts cluster.ClusterDispatchOptions) grpcDispatchOutcome {
	grpcClient := h.grpcClientPool
	if grpcClient == nil {
		return grpcOutcomeFallback
	}

	var err error
	switch opts.Operation {
	case models.OperationTypeSendMessage:
		resp, derr := grpcClient.SendToUser(ctx, addr, opts.TargetUserID, msgData)
		// 🔥 必须检查响应体的 Success 字段：目标节点用户不在时返回 (Success=false, UserOnline=false, err=nil)，
		// 此前只检查 err 会把"目标节点明确投递失败"误判为投递成功 → 上层 routed=true 直接 return →
		// 消息静默丢失（Redis 在线索引过期/用户已断线迁移的典型场景）
		if derr == nil && resp != nil && !resp.GetSuccess() {
			h.logger.DebugContextKV(ctx, "gRPC 目标节点明确用户不在，跳过该节点",
				"operation", opts.Operation,
				"target_addr", addr,
				"target_user", opts.TargetUserID,
				"user_online", resp.GetUserOnline(),
				"resp_error", resp.GetError(),
			)
			return grpcOutcomeUserMiss
		}
		err = derr

	case models.OperationTypeKickUser:
		// 踢人走专用 KickUser RPC（路由信封经 gRPC metadata 传播，远端按信封隔离踢出）
		// 显式注入 opts 路由：routeToCluster 的 ctx 不含路由（仅 trace），
		// 缺失注入会导致 metadata 为空、远端按 DefaultAppID/空 ns 踢人 → 跨租户误踢
		kickCtx := routing.NewRoute().WithAppID(opts.AppID).WithNamespace(opts.Namespace).Inject(ctx)
		resp, derr := grpcClient.KickUser(kickCtx, addr, opts.TargetUserID, opts.Reason)
		// 扑空（用户不在该节点）与 SendMessage 的 user_miss 同语义：跳过该节点 PubSub 兜底
		if derr == nil && resp != nil && resp.GetKickedConnections() == 0 {
			h.logger.InfoContextKV(ctx, "gRPC 目标节点明确用户不在，跳过该节点踢人兜底",
				"operation", opts.Operation,
				"target_addr", addr,
				"target_user", opts.TargetUserID,
			)
			return grpcOutcomeUserMiss
		}
		err = derr

	case models.OperationTypeGroupBroadcast, models.OperationTypeGroupsBroadcast:
		// 群组广播（两种 op 统一复数语义）：全量 GroupIDs 并行投递，单群组即单元素特例；
		// 任一成功即视为投递成功（失败群组由 PubSub 兜底补齐，保证最终送达）
		return mathx.IF(h.grpcBroadcastGroups(ctx, addr, opts, msgData), grpcOutcomeDelivered, grpcOutcomeFallback)

	case models.OperationTypeObserverNotify:
		// 单次调用注入全部 GroupIDs，服务端合并去重观察者后一次投递
		observerCtx := routing.NewRoute().WithAppID(opts.AppID).WithNamespace(opts.Namespace).WithGroupIDs(opts.GroupIDs).Inject(ctx)
		_, err = grpcClient.NotifyObservers(observerCtx, addr, msgData)

	default:
		h.logger.WarnContextKV(ctx, "未知集群操作类型，跳过 gRPC", "operation", opts.Operation)
		return grpcOutcomeFallback
	}

	if err != nil {
		h.logger.InfoContextKV(ctx, "📨 [投递诊断] gRPC 投递失败，降级 PubSub",
			"operation", opts.Operation,
			"target_addr", addr,
			"error", err,
		)
		return grpcOutcomeFallback
	}

	return grpcOutcomeDelivered
}

// ============================================================================
// gRPC 微批合帧投递（跨消息/跨 goroutine 按节点合帧，RPC 次数 O(消息) → O(批)）
// ============================================================================

// dispatchNode 单节点投递分叉：微批器启用走合帧路径，否则回退单发（历史 unary 行为）
func (h *Hub) dispatchNode(ctx context.Context, addr string, msgData []byte, opts cluster.ClusterDispatchOptions) grpcDispatchOutcome {
	if h.grpcBatchDispatcher == nil || !h.grpcBatchDispatcher.Enabled() {
		return h.executeGRPCDispatch(ctx, addr, msgData, opts)
	}
	return h.dispatchNodeViaBatch(ctx, addr, msgData, opts)
}

// dispatchNodeViaBatch 经微批分派器向单目标节点提交投递指令
//
// 群组广播（GroupBroadcast/GroupsBroadcast）逐 gid 拆分为单群组 item 提交
// （服务端 BatchDispatch 复用单群组 BroadcastGroup 语义，见 dispatchBatchItem）；
// 全部 gid 均提交（不可首条送达即短路，否则漏投后续群组），任一送达即视为该节点送达
func (h *Hub) dispatchNodeViaBatch(ctx context.Context, addr string, msgData []byte, opts cluster.ClusterDispatchOptions) grpcDispatchOutcome {
	if opts.Operation == models.OperationTypeGroupBroadcast || opts.Operation == models.OperationTypeGroupsBroadcast {
		if len(opts.GroupIDs) == 0 {
			return grpcOutcomeFallback
		}
		delivered := false
		for _, gid := range opts.GroupIDs {
			if h.grpcBatchDispatcher.Submit(ctx, addr, h.buildDispatchItem(ctx, opts, msgData, gid)) == cluster.OutcomeDelivered {
				delivered = true
			}
		}
		return mathx.IF(delivered, grpcOutcomeDelivered, grpcOutcomeFallback)
	}

	switch h.grpcBatchDispatcher.Submit(ctx, addr, h.buildDispatchItem(ctx, opts, msgData, "")) {
	case cluster.OutcomeDelivered:
		return grpcOutcomeDelivered
	case cluster.OutcomeUserMiss:
		return grpcOutcomeUserMiss
	default:
		return grpcOutcomeFallback
	}
}

// buildDispatchItem 组装单条微批指令（路由信封取自 opts，与服务端 unary 等价维度）
// gid 非空时 groupIDs 收敛为该单群组；否则透传 opts.GroupIDs（P2P/广播为 nil 或空合法）
// traceID 从调用方 ctx 提取（微批逐条 item 各自携带，远端按 item 恢复全链路 trace）
func (h *Hub) buildDispatchItem(ctx context.Context, opts cluster.ClusterDispatchOptions, msgData []byte, gid string) cluster.DispatchItem {
	groupIDs := opts.GroupIDs
	if gid != "" {
		groupIDs = []string{gid}
	}
	return cluster.DispatchItem{
		Operation:     opts.Operation,
		AppID:         opts.AppID,
		Namespace:     opts.Namespace,
		GroupIDs:      groupIDs,
		TargetUserID:  opts.TargetUserID,
		Reason:        opts.Reason,
		MessageData:   msgData,
		ExcludeSender: opts.ExcludeSender,
		SenderID:      opts.SenderID,
		TraceID:       logger.ExtractTraceID(ctx),
	}
}

// ============================================================================
// PubSub 兜底
// ============================================================================

// publishToCluster 通过 Redis PubSub 发布到集群广播频道
// 统一替换历史上的 broadcastToAllNodes + broadcastGroupToAllNodes
func (h *Hub) publishToCluster(ctx context.Context, dispatch *models.DistributedMessage) error {
	if h.pubsub == nil {
		return nil
	}

	// 控制消息（如 client_reclaim）无 Message 体，messageID 安全为空串
	messageID := ""
	if dispatch.Message != nil {
		messageID = dispatch.Message.GetMessageID()
	}

	channel := h.config.RedisRepository.PubSub.GetBroadcastChannel()
	data := h.marshalDistributedMessage(ctx, dispatch)
	h.logger.DebugContextKV(ctx, "📡 PubSub 广播频道发布",
		"channel", channel,
		"payload_size", len(data),
		"message_id", messageID,
	)
	err := h.pubsub.Publish(ctx, channel, string(data))
	if err != nil {
		h.logger.WarnContextKV(ctx, "📡 PubSub 广播频道发布失败",
			"channel", channel,
			"payload_size", len(data),
			"error", err,
			"message_id", messageID,
		)
	}
	return err
}

// publishToTargetedNodes 向指定节点的专属频道精准发布（避免全量广播导致 gRPC 已成功节点重复处理）
// 用于 gRPC 部分成功的 PubSub 兜底场景：仅失败节点需要收到消息
// 也是 gRPC 未启用 + opts.TargetNodeIDs 已知目标节点场景的定向发布主路径
//
// 性能：Redis Pipeline 批量发布，N 个目标节点 N 次 RTT → 1 次
// 可靠性：利用 PUBLISH 返回值（收到消息的订阅者数）秒级感知死节点——
// 返回 deadNodes = 频道无人订阅的节点列表（Pod 挂掉/订阅断连重连中，消息未送达，
// 原先要干等 30s ACK 超时才能发现）
func (h *Hub) publishToTargetedNodes(ctx context.Context, dispatch *models.DistributedMessage, nodeIDs []string) ([]string, error) {
	if h.pubsub == nil || len(nodeIDs) == 0 {
		return nil, nil
	}

	// 控制消息（如 client_reclaim）无 Message 体，messageID 安全为空串
	messageID := ""
	if dispatch.Message != nil {
		messageID = dispatch.Message.GetMessageID()
	}

	data := h.marshalDistributedMessage(ctx, dispatch)
	prefix := h.config.RedisRepository.PubSub.GetNodeChannelPrefix()
	h.logger.DebugContextKV(ctx, "📡 PubSub 定向发布",
		"channel_prefix", prefix,
		"target_nodes", nodeIDs,
		"target_count", len(nodeIDs),
		"payload_size", len(data),
		"message_id", messageID,
	)

	// 防御：过滤自身（不应出现，但避免意外循环投递）
	targets := make([]string, 0, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		if nodeID != h.nodeID {
			targets = append(targets, nodeID)
		}
	}
	if len(targets) == 0 {
		return nil, nil
	}

	// Pipeline 批量发布：逐节点 cachex.Publish 是 N 次串行 RTT（内含 retry 包装开销），
	// 且丢弃 PUBLISH 返回值无法感知死节点；直接走底层 client 等价（data 已序列化，
	// cachex.Publish 对 string 仅透传压缩语义）
	// 🔥 频道必须经 ResolveChannel 解析为物理频道（含 namespace 前缀）：
	// 订阅侧（SubscribeNodeMessages → cachex.Subscribe）会自动拼接 namespace 前缀，
	// 此前直接用 prefix+nodeID 裸频道发布，造成"订阅带前缀、发布不带前缀"的频道错配，
	// PUBLISH 永远返回 0（定向频道无人订阅）
	client := h.pubsub.GetClient()
	pipe := client.Pipeline()
	cmds := make([]*redis.IntCmd, len(targets))
	channels := make([]string, len(targets))
	for i, nodeID := range targets {
		channels[i] = h.pubsub.ResolveChannel(prefix + nodeID)
		cmds[i] = pipe.Publish(ctx, channels[i], data)
	}
	_, _ = pipe.Exec(ctx) // 网络/命令错误统一在下方逐命令检查（Exec 聚合错误不区分粒度）

	// PUBLISH 返回值 = 收到消息的订阅者数（节点正常 = 1 条订阅连接）；
	// 0 或命令错误 = 频道无人订阅（死节点），消息未送达
	var deadNodes []string
	var lastErr error
	for i, cmd := range cmds {
		if err := cmd.Err(); err != nil {
			lastErr = err
			deadNodes = append(deadNodes, targets[i])
			h.logger.WarnContextKV(ctx, "📡 PubSub 定向发布失败（单节点）",
				"target_node", targets[i],
				"channel", channels[i],
				"error", err,
				"message_id", messageID)
			continue
		}
		if cmd.Val() == 0 {
			// 订阅失活 ≠ 节点死亡：订阅断连重连窗口（秒级）内 PUBLISH 同样返回 0。
			// 交叉验证节点心跳：心跳正常 → 等待重连窗口后重试一次，仍无人订阅才判死，
			// 避免订阅抖动导致消息被批量误转离线
			// 适配说明：cluster.NodeRegistry 未暴露 IsNodeAlive，以节点在注册表缓存中
			// 存在作为心跳新鲜代理（refreshNodes 按 TTL 清理心跳过期节点）
			if h.nodeRegistry != nil {
				if _, alive := h.nodeRegistry.GetNodeAddr(targets[i]); alive {
					time.Sleep(deadNodeProbeRetryDelay)
					if retry, rerr := client.Publish(ctx, channels[i], data).Result(); rerr == nil && retry > 0 {
						h.logger.InfoContextKV(ctx, "📡 [死节点探测] 心跳正常+订阅恢复，重试投递成功",
							"target_node", targets[i],
							"message_id", messageID)
						continue
					}
				}
			}
			deadNodes = append(deadNodes, targets[i])
		}
	}
	if len(deadNodes) > 0 {
		h.logger.WarnContextKV(ctx, "📡 [死节点感知] 定向频道无人订阅，消息未送达（Pod 挂掉或订阅断连重连中）",
			"dead_nodes", deadNodes,
			"total_targets", len(targets),
			"message_id", messageID,
		)
	}
	return deadNodes, lastErr
}

// handleDeadNodesForP2P P2P 消息的死节点秒级兜底（publishToTargetedNodes 检测到定向频道无人订阅时调用）
//
// 重查在线索引：用户所有连接所在节点均失活（Pod 挂掉/订阅断连重连中）→ 实时投递无望，
// 立即转离线（复用 StoreOfflineOnDeliveryFailure：含离线源防循环、状态覆盖、ACK 超时任务取消），
// 不再干等 30s ACK 超时；仍有健康节点（已通过 gRPC/定向 PubSub 收到消息）或索引查询失败 →
// 不处理，保留 ACK 超时兜底
func (h *Hub) handleDeadNodesForP2P(ctx context.Context, msg *models.HubMessage, userID string, deadNodes []string) {
	if h.onlineStatusRepo == nil {
		return
	}
	deadSet := make(map[string]struct{}, len(deadNodes))
	for _, n := range deadNodes {
		deadSet[n] = struct{}{}
	}

	// 按投递信封归一化查询维度（与 decideUserNotFoundReroute 一致）
	appID := constants.NormalizeAppID(msg.AppID)
	queryCtx := routing.NewRoute().WithAppID(appID).WithNamespace(msg.Namespace).Inject(ctx)
	nodeIDs, err := h.onlineStatusRepo.GetUserNodes(queryCtx, userID)
	if err != nil || len(nodeIDs) == 0 {
		// 索引查询失败或用户无索引：未决场景，保留 30s ACK 超时兜底
		return
	}

	for _, nodeID := range nodeIDs {
		if nodeID == "" || nodeID == h.nodeID {
			continue // 本地节点由 sendToUser 本地投递路径负责
		}
		if _, dead := deadSet[nodeID]; !dead {
			// 用户仍有健康节点（该节点已收到消息），多端部分送达即成功，不转离线
			return
		}
	}

	h.logger.WarnContextKV(ctx, "🚨 [死节点兜底] 用户所有连接所在节点订阅均失活，立即转离线（不等 30s ACK 超时）",
		"message_id", msg.MessageID,
		"user_id", userID,
		"dead_nodes", deadNodes,
	)
	h.messagingMgr.StoreOfflineOnDeliveryFailure(msg, fmt.Errorf("目标节点 %v 订阅失活（Pod 挂掉或订阅断连），消息未送达", deadNodes))
}

// ============================================================================
// 辅助方法
// ============================================================================

// getAllClusterNodeIDs 获取集群中所有其他节点的 ID 列表（不含本节点）
func (h *Hub) getAllClusterNodeIDs() []string {
	if h.nodeRegistry == nil {
		return nil
	}
	allNodes := h.nodeRegistry.GetAllNodes()
	nodeIDs := make([]string, 0, len(allNodes))
	for nodeID := range allNodes {
		if nodeID != "" && nodeID != h.nodeID { // 排除自身，避免 gRPC 自调用 + PubSub 循环
			nodeIDs = append(nodeIDs, nodeID)
		}
	}
	return nodeIDs
}

// grpcBroadcastGroups 通过 gRPC 批量广播到多个群组（并行复用 BroadcastGroup RPC）
//
// 设计权衡：
//   - 无 .proto 源文件无法新增批量 RPC，因此复用现有 BroadcastGroup 单群组 RPC
//   - 对每个 groupID 并行调用（并发上限 8），单群组场景仅 1 次调用零损耗
//   - gRPC 点对点直连仍优于 PubSub 广播：精准路由、无冗余投递
//   - 任一成功即返回 true（失败群组由 PubSub 兜底补齐，保证最终送达）
func (h *Hub) grpcBroadcastGroups(ctx context.Context, addr string, opts cluster.ClusterDispatchOptions, msgData []byte) bool {
	// 调用方统一通过 GroupIDs 传群组列表（单元素=单群组）
	if len(opts.GroupIDs) == 0 || h.grpcClientPool == nil {
		return false
	}
	groupIDs := opts.GroupIDs

	var (
		success int64
		wg      sync.WaitGroup
		sem     = make(chan struct{}, 8) // 并发上限，避免打满 gRPC 连接
	)
	for _, gid := range groupIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func(groupID string) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, err := h.grpcClientPool.BroadcastGroup(routing.NewRoute().WithAppID(opts.AppID).WithNamespace(opts.Namespace).WithGroup(groupID).Inject(ctx), addr, msgData, opts.ExcludeSender, opts.SenderID); err == nil {
				atomic.AddInt64(&success, 1)
			} else {
				h.logger.DebugContextKV(ctx, "gRPC 批量群组广播：单个群组投递失败",
					"target_addr", addr, "group_id", groupID, "error", err)
			}
		}(gid)
	}
	wg.Wait()

	if atomic.LoadInt64(&success) == 0 {
		return false
	}
	return true
}

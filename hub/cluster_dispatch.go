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

	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 类型定义
// ============================================================================

// ClusterOperation 集群操作类型（统一别名，消除 DistributedMessage 历史命名歧义）
type ClusterOperation = models.OperationType

// ClusterDispatchOptions 跨节点分发选项
// 封装所有跨节点通信的路由参数，由 routeToCluster 统一消费
// 设计原则：调用方爱传什么传什么，单/多元素统一走切片，不做单值字段冗余
type ClusterDispatchOptions struct {
	Operation     ClusterOperation // 操作类型（SendMessage/GroupsBroadcast/Broadcast/ObserverNotify/KickUser）
	Namespace     string           // 命名空间ID（空="default"；Broadcast 时空表示全命名空间）
	TargetNodeID  string           // 目标节点ID（精确路由，空=所有已知节点广播）
	TargetNodeIDs []string         // 已知目标节点列表（P2P 跨节点路由用，gRPC 未启用时优先定向 PubSub 而非广播频道）
	TargetUserID  string           // 目标用户ID（Operation=SendMessage 时使用）
	GroupIDs      []string         // 群组ID列表（len==1 单群组广播，len>1 批量广播，len==0 不广播）
	ExcludeSender bool             // 是否排除发送者（群组广播时使用）
	SenderID      string           // 发送者ID（排除发送者时使用）
	Reason        string           // 辅助信息（踢人原因等）
}

// clusterRouteResult 路由结果（内部使用）
type clusterRouteResult struct {
	grpcDelivered  int      // gRPC 成功投递节点数
	pubsubFallback []string // 需 PubSub 兜底的节点列表
}

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
func (h *Hub) routeToCluster(ctx context.Context, msg *HubMessage, opts ClusterDispatchOptions) error {
	// 单机模式：无 PubSub 且无 gRPC，不跨节点
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return nil
	}

	// 🔏 路由信封解析：msg 信封优先（入口层已注入，异步/跨节点链路持久化），opts 仅作兜底
	// - Broadcast 操作：空 Namespace 表示全命名空间广播，不归一化为 "default"
	// - 其他操作：空 Namespace 保持空（由接收端从 msg 内层信封再兜底）
	namespace := msg.Namespace
	if namespace == "" {
		namespace = opts.Namespace
	}
	// GroupIDs：msg 非空则 clone 一份（避免后续 append 污染原 msg），否则用 opts
	var groupIDs []string
	if len(msg.GroupIDs) > 0 {
		groupIDs = append([]string(nil), msg.GroupIDs...)
	} else {
		groupIDs = opts.GroupIDs
	}

	h.logger.DebugContextKV(ctx, "集群路由",
		"operation", opts.Operation,
		"namespace", namespace,
		"target_node", opts.TargetNodeID,
		"target_user", opts.TargetUserID,
		"group_ids", groupIDs,
		"grpc_enabled", h.IsGRPCEnabled(),
		"message_id", msg.GetMessageID(),
	)

	// 构建分发信封（路由信封携带 namespace + group_ids，接收端按此过滤）
	dispatch := &models.DistributedMessage{
		Type:          opts.Operation,
		NodeID:        h.nodeID,
		TargetID:      resolveDispatchTargetID(opts),
		Message:       msg,
		Reason:        opts.Reason,
		Timestamp:     time.Now(),
		Namespace:     namespace,
		GroupIDs:      groupIDs,
		ExcludeSender: opts.ExcludeSender, // 跨节点群组广播 PubSub 兜底需携带，接收端据此排除发送者
		SenderID:      opts.SenderID,
	}

	// 从 ctx 注入 trace_id（消息已有则复用，跨节点保留源 trace）
	dispatch.InjectContext(ctx)

	// ① 尝试 gRPC 直连
	result := h.dispatchViaGRPC(ctx, msg, opts)

	// ② 所有节点 gRPC 成功，无需 PubSub
	if len(result.pubsubFallback) == 0 && result.grpcDelivered > 0 {
		h.logger.DebugContextKV(ctx, "集群路由完成（gRPC 全覆盖）",
			"operation", opts.Operation,
			"grpc_delivered", result.grpcDelivered,
			"message_id", msg.GetMessageID(),
		)
		return nil
	}

	// ③ PubSub 兜底：gRPC 未覆盖的节点走 PubSub
	if h.pubsub != nil {
		var pubErr error
		if result.grpcDelivered > 0 || len(result.pubsubFallback) > 0 {
			// 已知目标节点（gRPC 部分成功的剩余节点，或 gRPC 全失败的兜底节点）
			// 定向发布到节点专属频道，避免广播频道冗余投递到无关节点
			// 关键修复：gRPC 未启用 + opts.TargetNodeIDs 来自 Redis 在线索引（如 otherNodes=[3iy9vey]）时，
			// 必须走定向发布到 3iy9vey 专属频道，而不是广播频道（广播频道依赖接收端订阅，且无法定向）
			pubErr = h.publishToTargetedNodes(ctx, dispatch, result.pubsubFallback)
		} else {
			// 无目标节点列表（全局广播、群组广播场景），走广播频道
			pubErr = h.publishToCluster(ctx, dispatch)
		}
		if pubErr != nil {
			h.logger.WarnContextKV(ctx, "PubSub 兜底发布失败",
				"operation", opts.Operation,
				"error", pubErr,
				"grpc_delivered", result.grpcDelivered,
				"pubsub_fallback", len(result.pubsubFallback),
				"message_id", msg.GetMessageID(),
			)
			// gRPC 有部分成功则不算完全失败
			if result.grpcDelivered > 0 {
				return nil
			}
			return pubErr
		}

		h.logger.DebugContextKV(ctx, "集群路由完成",
			"operation", opts.Operation,
			"grpc_delivered", result.grpcDelivered,
			"pubsub_fallback", len(result.pubsubFallback),
			"message_id", msg.GetMessageID(),
		)
		return nil
	}

	// ④ gRPC 投递 0 节点 + PubSub 未启用 → 路由失败，让上层 fallback 到本地发送 + 离线存储
	// 走到这里说明 IsGRPCEnabled() == true 但 h.pubsub == nil，且 dispatchViaGRPC 没有任何节点投递成功
	// （nodeRegistry 不含其他节点，且 opts.TargetNodeIDs 也为空，无任何可用投递路径）
	// 不返回 error 会让上层误判"已路由成功"→ sendToUser L97 routed=true → 直接 return，消息丢失
	if result.grpcDelivered == 0 {
		return fmt.Errorf("跨节点路由失败：gRPC 投递 0 节点（nodeRegistry 无其他节点），PubSub 未启用，无任何投递路径")
	}

	h.logger.DebugContextKV(ctx, "集群路由完成",
		"operation", opts.Operation,
		"grpc_delivered", result.grpcDelivered,
		"pubsub_fallback", len(result.pubsubFallback),
		"message_id", msg.GetMessageID(),
	)

	return nil
}

// resolveDispatchTargetID 根据操作类型解析 TargetID
// 广播类操作无特定目标，返回空字符串（群组信息由 GroupIDs 携带）
func resolveDispatchTargetID(opts ClusterDispatchOptions) string {
	switch opts.Operation {
	case models.OperationTypeSendMessage, models.OperationTypeKickUser:
		return opts.TargetUserID
	default:
		return ""
	}
}

// ============================================================================
// gRPC 直连
// ============================================================================

// dispatchViaGRPC 通过 gRPC 向目标节点直连投递
//
// 返回路由结果：哪些节点成功、哪些需要 PubSub 兜底
func (h *Hub) dispatchViaGRPC(ctx context.Context, msg *HubMessage, opts ClusterDispatchOptions) clusterRouteResult {
	result := clusterRouteResult{}

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

	for _, nodeID := range targetNodes {
		addr, ok := h.nodeRegistry.GetNodeAddr(nodeID)
		if !ok {
			result.pubsubFallback = append(result.pubsubFallback, nodeID)
			continue
		}

		if h.executeGRPCDispatch(grpcCtx, addr, msgData, opts) {
			result.grpcDelivered++
		} else {
			result.pubsubFallback = append(result.pubsubFallback, nodeID)
		}
	}

	return result
}

// resolveGRPCTargetNodes 根据操作类型确定 gRPC 目标节点列表
// 优先级：TargetNodeID（单节点精确）> TargetNodeIDs（多节点定向）> nodeRegistry 所有其他节点（广播）
func (h *Hub) resolveGRPCTargetNodes(opts ClusterDispatchOptions) []string {
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
func (h *Hub) executeGRPCDispatch(ctx context.Context, addr string, msgData []byte, opts ClusterDispatchOptions) bool {
	grpcClient := h.grpcClientPool
	if grpcClient == nil {
		return false
	}

	var err error
	switch opts.Operation {
	case models.OperationTypeSendMessage, models.OperationTypeKickUser:
		_, err = grpcClient.SendToUser(ctx, addr, opts.TargetUserID, msgData)

	case models.OperationTypeGroupBroadcast:
		// 单群组广播（GroupIDs[0]）：一次 BroadcastGroup RPC
		return h.grpcBroadcastGroup(ctx, addr, opts, msgData)

	case models.OperationTypeGroupsBroadcast:
		// 批量群组广播（len(GroupIDs)>1）：并行复用 BroadcastGroup RPC
		return h.grpcBroadcastGroups(ctx, addr, opts, msgData)

	case models.OperationTypeObserverNotify:
		// 单次调用注入全部 GroupIDs，服务端合并去重观察者后一次投递
		observerCtx := routing.WithNamespaceGroupIDs(ctx, opts.Namespace, opts.GroupIDs)
		_, err = grpcClient.NotifyObservers(observerCtx, addr, msgData)

	case models.OperationTypeBroadcast:
		// 全局广播通过 gRPC SendToUser 的变体：向所有节点发送
		// 复用 BroadcastGroup 的 namespace 过滤能力，groupID 留空表示全命名空间
		broadcastCtx := routing.WithNamespaceGroupIDs(ctx, opts.Namespace, nil)
		_, err = grpcClient.BroadcastGroup(broadcastCtx, addr, msgData, false, "")

	default:
		h.logger.WarnContextKV(ctx, "未知集群操作类型，跳过 gRPC", "operation", opts.Operation)
		return false
	}

	if err != nil {
		h.logger.DebugContextKV(ctx, "gRPC 投递失败，降级 PubSub",
			"operation", opts.Operation,
			"target_addr", addr,
			"error", err,
		)
		return false
	}

	return true
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

	channel := h.config.RedisRepository.PubSub.GetBroadcastChannel()
	data := h.marshalDistributedMessage(dispatch, dispatch.Message.GetMessageID())
	h.logger.InfoContextKV(ctx, "📡 PubSub 广播频道发布",
		"channel", channel,
		"payload_size", len(data),
		"message_id", dispatch.Message.GetMessageID(),
	)
	err := h.pubsub.Publish(ctx, channel, string(data))
	if err != nil {
		h.logger.WarnContextKV(ctx, "📡 PubSub 广播频道发布失败",
			"channel", channel,
			"payload_size", len(data),
			"error", err,
			"message_id", dispatch.Message.GetMessageID(),
		)
	}
	return err
}

// publishToTargetedNodes 向指定节点的专属频道精准发布（避免全量广播导致 gRPC 已成功节点重复处理）
// 用于 gRPC 部分成功的 PubSub 兜底场景：仅失败节点需要收到消息
// 也是 gRPC 未启用 + opts.TargetNodeIDs 已知目标节点场景的定向发布主路径
func (h *Hub) publishToTargetedNodes(ctx context.Context, dispatch *models.DistributedMessage, nodeIDs []string) error {
	if h.pubsub == nil || len(nodeIDs) == 0 {
		return nil
	}
	data := h.marshalDistributedMessage(dispatch, dispatch.Message.GetMessageID())
	prefix := h.config.RedisRepository.PubSub.GetNodeChannelPrefix()
	h.logger.InfoContextKV(ctx, "📡 PubSub 定向发布",
		"channel_prefix", prefix,
		"target_nodes", nodeIDs,
		"target_count", len(nodeIDs),
		"payload_size", len(data),
		"message_id", dispatch.Message.GetMessageID(),
	)
	var lastErr error
	for _, nodeID := range nodeIDs {
		// 防御：跳过自身（不应出现，但避免意外循环投递）
		if nodeID == h.nodeID {
			continue
		}
		if err := h.pubsub.Publish(ctx, prefix+nodeID, string(data)); err != nil {
			lastErr = err
			h.logger.WarnContextKV(ctx, "📡 PubSub 定向发布失败",
				"target_node", nodeID,
				"channel", prefix+nodeID,
				"payload_size", len(data),
				"error", err,
				"message_id", dispatch.Message.GetMessageID())
		}
	}
	return lastErr
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

// grpcBroadcastGroup 通过 gRPC 广播到单个群组（GroupIDs[0]）
func (h *Hub) grpcBroadcastGroup(ctx context.Context, addr string, opts ClusterDispatchOptions, msgData []byte) bool {
	if len(opts.GroupIDs) == 0 || h.grpcClientPool == nil {
		return false
	}

	_, err := h.grpcClientPool.BroadcastGroup(routing.WithNamespaceGroupIDs(ctx, opts.Namespace, opts.GroupIDs[:1]), addr, msgData, opts.ExcludeSender, opts.SenderID)
	if err != nil {
		h.logger.DebugContextKV(ctx, "gRPC 群组广播投递失败",
			"target_addr", addr, "group_id", opts.GroupIDs[0], "error", err)
		return false
	}
	return true
}

// grpcBroadcastGroups 通过 gRPC 批量广播到多个群组（并行复用 BroadcastGroup RPC）
//
// 设计权衡：
//   - 无 .proto 源文件无法新增批量 RPC，因此复用现有 BroadcastGroup 单群组 RPC
//   - 对每个 groupID 并行调用（并发上限 8），单群组场景仅 1 次调用零损耗
//   - gRPC 点对点直连仍优于 PubSub 广播：精准路由、无冗余投递
//   - 任一成功即返回 true（失败群组由 PubSub 兜底补齐，保证最终送达）
func (h *Hub) grpcBroadcastGroups(ctx context.Context, addr string, opts ClusterDispatchOptions, msgData []byte) bool {
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
			if _, err := h.grpcClientPool.BroadcastGroup(routing.WithNamespaceGroupIDs(ctx, opts.Namespace, []string{groupID}), addr, msgData, opts.ExcludeSender, opts.SenderID); err == nil {
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

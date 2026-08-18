/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 01:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-30 11:20:15
 * @FilePath: \go-wsc\hub\distributed.go
 * @Description: Hub 分布式消息订阅与处理
 *
 * 本文件职责单一：订阅跨节点消息并分发到本地处理函数
 * 跨节点路由（gRPC 直连 + PubSub 兜底）统一由 cluster_dispatch.go::routeToCluster 处理，
 * 节点注册与发现由 node_registry.go::NodeRegistry 负责，
 * 消除了历史上分散在此的 RegisterNode/DiscoverNodes/broadcastToAllNodes 等重复实现
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	pb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 用户消息跨节点路由
// ============================================================================

// checkAndRouteToNode 检查用户是否在其他节点，如果是则路由过去
//
// 统一走 routeToCluster 入口，由其集中决策 gRPC 直连与 PubSub 兜底，
// 消除历史上分散在各方法中的重复路由逻辑
//
// 返回: (是否在其他节点, 错误)
//   - routed=true:  消息已路由到其他节点，调用方无需本地发送
//   - routed=false: 用户在本节点或离线，调用方应本地发送
func (h *Hub) checkAndRouteToNode(ctx context.Context, userID string, msg *HubMessage) (bool, error) {
	// 单机模式：无 PubSub 且无 gRPC，不跨节点
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return false, nil
	}
	if h.onlineStatusRepo == nil {
		return false, nil
	}

	// 1. 查询用户所在节点（优先走 routerCache 三层兜底）
	var nodeIDs []string
	var err error
	if h.routerCache != nil {
		nodeIDs, err = h.routerCache.GetUserNodes(ctx, userID)
	} else {
		nodeIDs, err = h.onlineStatusRepo.GetUserNodes(ctx, userID)
	}
	if err != nil {
		// 查询失败，假设用户在本节点或离线，继续本地发送流程
		return false, err
	}

	// 2. 过滤掉本节点，只保留其他节点
	var otherNodes []string
	for _, nodeID := range nodeIDs {
		if nodeID != "" && nodeID != h.nodeID {
			otherNodes = append(otherNodes, nodeID)
		}
	}

	// 3. 没有其他节点 → 本地发送
	if len(otherNodes) == 0 {
		h.logger.InfoContextKV(ctx, "📍 [投递诊断] 用户仅在本节点，走本地投递",
			"user_id", userID,
			"message_id", msg.MessageID,
			"node_id", h.nodeID,
			"all_nodes", nodeIDs,
		)
		return false, nil
	}

	h.logger.InfoContextKV(ctx, "📍 [投递诊断] 用户在其他节点，发起跨节点路由",
		"message_id", msg.MessageID,
		"user_id", userID,
		"from_node", h.nodeID,
		"to_nodes", otherNodes,
		"all_nodes", nodeIDs,
		"grpc_enabled", h.IsGRPCEnabled(),
	)

	// 4. 统一跨节点路由：gRPC 直连优先，PubSub 兜底（由 routeToCluster 集中决策）
	opts := ClusterDispatchOptions{
		Operation:     OperationTypeSendMessage,
		Namespace:     "", // namespace 由 routeToCluster 从 msg 信封提取（msg.Namespace），此处留空不覆盖
		TargetNodeID:  "", // 不再依赖单节点精确路由（老路径：nodeRegistry 自动发现，gRPC 未启用时会落空）
		TargetNodeIDs: otherNodes, // 已知目标节点列表，传给 routeToCluster：gRPC 未启用时优先定向 PubSub 而非广播频道
		TargetUserID:  userID,
	}
	if err := h.routeToCluster(ctx, msg, opts); err != nil {
		// 路由失败，返回 false 让上层 fallback 到本地发送
		return false, err
	}
	return true, nil
}

// ============================================================================
// 节点间消息订阅
// ============================================================================

// unmarshalDistributedMessage 反序列化分布式消息
// 优先使用 protobuf（高性能、低体积），失败时降级到 JSON（兼容旧节点）
// 三处订阅回调（节点消息/广播/观察者）共用此方法，避免逻辑重复
func (h *Hub) unmarshalDistributedMessage(data []byte) (*DistributedMessage, error) {
	// 🚀 优先尝试 protobuf 反序列化
	distMsg, pErr := pb.UnmarshalDistributedMessage(data)
	if pErr == nil {
		return distMsg, nil
	}

	// 降级到 JSON（兼容旧节点或非 protobuf 消息）
	var jsonMsg DistributedMessage
	if jErr := json.Unmarshal(data, &jsonMsg); jErr != nil {
		h.logger.ErrorKV("解析分布式消息失败",
			"protobuf_error", pErr,
			"json_error", jErr,
		)
		return nil, jErr
	}
	return &jsonMsg, nil
}

// marshalDistributedMessage 序列化分布式消息
// 优先使用 protobuf（高性能、低体积），失败时降级到 JSON
func (h *Hub) marshalDistributedMessage(distMsg *DistributedMessage, messageID string) []byte {
	// 🚀 使用 protobuf 序列化（高性能、低体积）
	data, mErr := pb.MarshalDistributedMessage(distMsg)
	if mErr == nil {
		return data
	}

	// protobuf 序列化失败，降级到 JSON
	h.logger.WarnKV("protobuf 序列化失败，降级到 JSON",
		"error", mErr,
		"message_id", messageID,
	)
	data, _ = json.Marshal(distMsg)
	return data
}

// SubscribeNodeMessages 订阅本节点的消息通道
func (h *Hub) SubscribeNodeMessages(ctx context.Context) error {
	if h.pubsub == nil {
		return ErrPubSubNotSet
	}

	channel := h.config.RedisRepository.PubSub.GetNodeChannelPrefix() + h.nodeID

	h.logger.InfoContextKV(ctx, "订阅节点消息通道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorContextKV(ctx, "节点消息订阅 panic", "panic", r, "stack", string(debug.Stack()), "channel", channel)
		}).
		Exec(func() {
			_, err := h.pubsub.Subscribe([]string{channel}, func(subCtx context.Context, ch string, msg string) error {
				distMsg, err := h.unmarshalDistributedMessage([]byte(msg))
				if err != nil {
					return err
				}

				// 防御：忽略自己发出的消息（publishToTargetedNodes 已跳过自身频道，
				// 此处二次防御避免 registry 误含自身或发布逻辑变更导致的循环投递）
				if distMsg.NodeID == h.nodeID {
					return nil
				}

				// 使用订阅回调提供的 subCtx，而不是外层的 ctx
				return h.handleDistributedMessage(subCtx, distMsg)
			})

			if err != nil {
				h.logger.ErrorContextKV(ctx, "订阅节点消息失败", "error", err, "channel", channel)
			}

			// 使用 EventLoop 保持订阅活跃，直到 context 取消
			syncx.NewEventLoop(ctx).
				OnShutdown(func() {
					h.logger.InfoContextKV(ctx, "节点消息订阅已停止", "channel", channel)
				}).
				Run()
		})

	return nil
}

// handleDistributedMessage 处理从其他节点转发来的消息
func (h *Hub) handleDistributedMessage(ctx context.Context, distMsg *DistributedMessage) error {
	// 参数验证
	if distMsg == nil {
		return fmt.Errorf("distributed message is nil")
	}

	// 从分布式消息恢复 trace_id 到 ctx（PubSub 跨节点链路串联）
	// 确保 logger.DebugContextKV 等日志自动输出 trace_id
	ctx = distMsg.ContextFrom(ctx)

	// 🔏 路由信封同步：将 DistributedMessage 外层路由信封同步写入内层 HubMessage
	// 兼容两类发送路径：
	//   1. 新节点：HubMessage 自身信封已带路由（pb 序列化），此调用为幂等（已有不覆盖）
	//   2. 旧节点/历史路径：仅 DistributedMessage 外层信封带路由，此处补齐 HubMessage 内部信封
	// 下游所有本地投递过滤（broadcastToUserIDs / broadcastToFiltered / handleBroadcast）统一从 msg 取路由
	if distMsg.Message != nil {
		distMsg.Message.ContextWithRoute(ctx, distMsg.Namespace, distMsg.GroupIDs)
	}

	h.logger.DebugContextKV(ctx, "收到分布式消息",
		"type", distMsg.Type,
		"from_node", distMsg.NodeID,
		"target_id", distMsg.TargetID,
		"msg_ns", distMsg.Namespace,
		"msg_group_count", len(distMsg.GroupIDs),
	)

	switch distMsg.Type {
	case OperationTypeSendMessage:
		return h.handleDistributedSendMessage(ctx, distMsg)

	case OperationTypeKickUser:
		return h.handleDistributedKickUser(ctx, distMsg)

	case OperationTypeBroadcast:
		return h.handleDistributedBroadcast(ctx, distMsg)

	case OperationTypeGroupBroadcast, OperationTypeGroupsBroadcast:
		// 单群组（group_broadcast）与批量群组（groups_broadcast）统一走同一处理函数：
		// handleDistributedGroupsBroadcast 接收 GroupIDs 列表，len==1 即单群组场景。
		// 历史遗漏：switch 曾只有复数 case，单群组 PubSub 兜底消息会进 default 丢失。
		return h.handleDistributedGroupsBroadcast(ctx, distMsg)

	case OperationTypeObserverNotify:
		return h.handleDistributedObserverNotify(ctx, distMsg)

	default:
		h.logger.WarnContextKV(ctx, "未知的分布式消息类型", "type", distMsg.Type)
		return fmt.Errorf("unknown message type: %s", distMsg.Type)
	}
}

// handleDistributedSendMessage 处理跨节点发送消息
// 使用 ForEachUserClient 零拷贝遍历 + 预序列化，替代 CopyClientsFromMap 双重拷贝
func (h *Hub) handleDistributedSendMessage(ctx context.Context, distMsg *DistributedMessage) error {
	if distMsg.Message == nil {
		return fmt.Errorf("message data not found")
	}

	// 快速检查用户是否存在（避免无用户时序列化开销）
	if !h.shardedRegistry.HasUser(distMsg.TargetID) {
		h.logger.DebugContextKV(ctx, "[跨Pod] 用户不在本节点，跳过",
			"user_id", distMsg.TargetID,
			"message_id", distMsg.Message.MessageID,
			"from_node", distMsg.NodeID,
			"node_id", h.nodeID,
		)
		return fmt.Errorf("user not found on this node: %s", distMsg.TargetID)
	}

	h.logger.InfoContextKV(ctx, "✅ [跨Pod] 消息命中本节点，准备投递给本地客户端",
		"user_id", distMsg.TargetID,
		"message_id", distMsg.Message.MessageID,
		"from_node", distMsg.NodeID,
		"node_id", h.nodeID,
	)

	// 序列化消息为字节（预序列化一次，多设备复用）
	msgData, err := json.Marshal(distMsg.Message)
	if err != nil {
		return fmt.Errorf("marshal message failed: %w", err)
	}

	// 零拷贝遍历：ForEachUserClientFiltered 持读锁遍历 + namespace 严格匹配，TrySend 非阻塞安全
	// ⚠️ 必须按 namespace 过滤：同一 userID 可能跨 namespace 多端登录（如 ns1 客服 + ns2 用户），
	//    不过滤会导致跨租户消息泄露。与本地 P2P 路径 handleDirectMessage 保持一致。
	// distMsg.Namespace 来自发送端 msg 信封（routeToCluster 从 msg.Namespace 提取），为空时退化为不隔离（兼容旧节点）
	//
	// 复用 sendToClientSerialized（与本地/gRPC 路径对齐）：
	// 统一状态回报（Success/Failed → wsc_message_send_records）、接收者统计、失败转存离线、SSE 客户端支持。
	// 🔥 历史遗漏：此前裸调 client.TrySend，跨节点消息状态永远停留 sending，且 SSE 客户端收不到跨节点消息
	successCount := 0
	h.shardedRegistry.ForEachUserClientFiltered(distMsg.TargetID, distMsg.Namespace, nil, func(_ string, client *Client) bool {
		if h.sendToClientSerialized(ctx, client, distMsg.Message, msgData) {
			successCount++
		}

		// 检查上下文是否取消
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	})

	if successCount == 0 {
		h.handleSendFailure(ctx, distMsg.TargetID, distMsg.Message, "all clients unavailable")
		return fmt.Errorf("failed to send to any client: %s", distMsg.TargetID)
	}

	h.logger.InfoContextKV(ctx, "✅ [跨Pod] 消息已投递到本地客户端",
		"message_id", distMsg.Message.MessageID,
		"user_id", distMsg.TargetID,
		"success_count", successCount,
		"from_node", distMsg.NodeID,
		"node_id", h.nodeID,
	)

	// 🔔 通知观察者（跨节点消息也需要通知观察者）
	// 点对点消息的 groupIDs 由路由信封携带（群组消息场景），观察者按 namespace+groupIDs 三级索引匹配
	h.notifyObservers(routing.WithNamespaceGroupIDs(ctx, distMsg.Namespace, distMsg.GroupIDs), distMsg.Message)

	return nil
}

// handleSendFailure 处理跨节点消息发送失败
func (h *Hub) handleSendFailure(ctx context.Context, userID string, msg *HubMessage, reason string) {
	h.logger.WarnContextKV(ctx, "跨节点消息发送失败",
		"user_id", userID,
		"message_id", msg.MessageID,
		"source", msg.Source,
		"reason", reason,
	)
}

// handleDistributedKickUser 处理跨节点踢人
func (h *Hub) handleDistributedKickUser(ctx context.Context, distMsg *DistributedMessage) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	default:
		h.KickUserSimple(distMsg.TargetID, distMsg.Reason)
		return nil
	}
}

// handleDistributedBroadcast 处理跨节点广播
// 命名空间隔离：distMsg.Namespace 为空表示全命名空间广播，非空仅广播给同命名空间客户端
func (h *Hub) handleDistributedBroadcast(ctx context.Context, distMsg *DistributedMessage) error {
	if distMsg.Message == nil {
		return fmt.Errorf("message data not found")
	}

	namespace := distMsg.Namespace

	if namespace == "" {
		// 全命名空间广播（Namespace 为空）→ 直接调用 handleBroadcastMessage 投递给所有客户端
		//
		// 🔥 不走 h.broadcast → handleBroadcast 路径：
		//   handleBroadcast 会调用 notifyObservers → broadcastObserverNotification，
		//   而源节点 Broadcast 已通过 notifyObservers → broadcastObserverNotification 通知了所有节点的观察者。
		//   若目标节点再次走 handleBroadcast，会导致 N 个节点各自广播观察者通知 → 每个节点收到 N-1 份重复（N² 总通知量）。
		//   直接调用 handleBroadcastMessage 跳过观察者通知，仅做本地客户端投递。
		h.handleBroadcastMessage(ctx, distMsg.Message)
		return nil
	}

	// 命名空间广播 → 仅发送给同命名空间客户端
	// broadcastToFiltered 不调用 notifyObservers（源节点 BroadcastToNamespace 已统一通知观察者）
	count := h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.Namespace == namespace
	}, distMsg.Message)

	h.logger.DebugContextKV(ctx, "跨节点命名空间广播已处理",
		"namespace", namespace,
		"message_id", distMsg.Message.MessageID,
		"local_delivered", count,
	)
	return nil
}

// ============================================================================
// 分布式锁
// ============================================================================

// AcquireDistributedLock 获取分布式锁
func (h *Hub) AcquireDistributedLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if h.pubsub == nil {
		return false, ErrPubSubNotSet
	}

	lockKey := h.config.RedisRepository.PubSub.GetLockKeyPrefix() + key
	lockValue := h.nodeID

	// 使用 Lua 脚本实现 SETNX + EXPIRE 原子操作
	script := `
		if redis.call("exists", KEYS[1]) == 0 then
			redis.call("set", KEYS[1], ARGV[1])
			redis.call("expire", KEYS[1], ARGV[2])
			return 1
		else
			return 0
		end
	`

	result, err := h.pubsub.GetClient().Eval(ctx, script, []string{lockKey}, lockValue, int(ttl.Seconds())).Result()
	if err != nil {
		return false, err
	}

	acquired, ok := result.(int64)
	if !ok {
		return false, fmt.Errorf("unexpected result type from lua script")
	}

	return acquired == 1, nil
}

// ReleaseDistributedLock 释放分布式锁
func (h *Hub) ReleaseDistributedLock(ctx context.Context, key string) error {
	if h.pubsub == nil {
		return ErrPubSubNotSet
	}

	lockKey := h.config.RedisRepository.PubSub.GetLockKeyPrefix() + key

	// Lua 脚本确保只删除自己的锁
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("del", KEYS[1])
		else
			return 0
		end
	`

	return h.pubsub.GetClient().Eval(ctx, script, []string{lockKey}, h.nodeID).Err()
}

// ============================================================================
// 跨节点群组广播与观察者通知处理
// ============================================================================

// handleDistributedGroupsBroadcast 处理跨节点群组广播（单群组与批量统一入口）
//
// 高性能：一次 Pipeline 获取所有群组成员 → 合并去重 → 一次本地过滤广播
// 相比逐群组处理，N 个群组从 N 次 GetMembers + N 次 broadcastToFiltered 降为 1 + 1
//
// 兼容旧消息：GroupIDs 为空时回退到 TargetID（旧版单群组消息）
func (h *Hub) handleDistributedGroupsBroadcast(ctx context.Context, distMsg *DistributedMessage) error {
	// SubscribeBroadcastChannel 已过滤自身消息，此处二次防御
	if distMsg.NodeID == h.nodeID {
		return nil
	}

	if distMsg.Message == nil {
		return fmt.Errorf("message data not found")
	}

	if h.groupRepo == nil {
		return fmt.Errorf("group repository is not set")
	}

	// 群组ID列表：优先 GroupIDs，回退 TargetID（兼容旧版单群组消息）
	groupIDs := distMsg.GroupIDs
	if len(groupIDs) == 0 && distMsg.TargetID != "" {
		groupIDs = []string{distMsg.TargetID}
	}
	if len(groupIDs) == 0 {
		return fmt.Errorf("groupIDs is empty in distributed message")
	}

	namespace := distMsg.Namespace

	// Pipeline 批量获取所有群组成员并合并去重（用户跨群组只收一条）
	memberSet := h.batchGetGroupMembers(ctx, namespace, groupIDs)
	if len(memberSet) == 0 {
		return nil
	}

	// 转为成员列表，按需排除发送者（跨节点 PubSub 兜底场景，与 gRPC BroadcastGroup 对齐）
	// ⚠️ 历史遗漏：distMsg 未携带 ExcludeSender/SenderID 时，发送者在其他节点的设备会收到自己的群组消息
	members := make([]string, 0, len(memberSet))
	for uid := range memberSet {
		if distMsg.ExcludeSender && distMsg.SenderID != "" && uid == distMsg.SenderID {
			continue
		}
		members = append(members, uid)
	}

	// 按成员ID查找本地连接并投递
	count := h.broadcastToUserIDs(ctx, members, distMsg.Message)

	h.logger.DebugContextKV(ctx, "跨节点群组广播已处理",
		"namespace", namespace,
		"group_ids", groupIDs,
		"unique_members", len(members),
		"from_node", distMsg.NodeID,
		"message_id", distMsg.Message.MessageID,
		"local_delivered", count,
	)

	return nil
}

// handleDistributedObserverNotify 处理跨节点观察者通知
// 三级索引查找：全局 + 命名空间 + 命名空间+群组（观察者可订阅多个组，按 groupIDs 合并去重）
func (h *Hub) handleDistributedObserverNotify(ctx context.Context, distMsg *DistributedMessage) error {
	// 忽略自己发出的通知（本地观察者已经在 notifyObservers 中收到了）
	if distMsg.NodeID == h.nodeID {
		h.logger.DebugContextKV(ctx, "忽略自己发出的观察者通知",
			"from_node", distMsg.NodeID,
		)
		return nil
	}

	if distMsg.Message == nil {
		h.logger.WarnContextKV(ctx, "观察者通知缺少消息数据",
			"from_node", distMsg.NodeID,
		)
		return fmt.Errorf("message data not found")
	}

	namespace := distMsg.Namespace
	groupIDs := distMsg.GroupIDs

	// 三级索引查找：全局 + 命名空间 + 各群组，按 clientID 去重
	// 观察者可订阅多个组，传入所有 groupIDs 合并匹配
	observers := h.GetObserversForMessage(namespace, groupIDs...)
	if len(observers) == 0 {
		h.logger.DebugContextKV(ctx, "本节点无匹配观察者，跳过通知",
			"message_id", distMsg.Message.MessageID,
			"namespace", namespace,
			"group_ids", groupIDs,
			"from_node", distMsg.NodeID,
		)
		return nil
	}

	h.logger.DebugContextKV(ctx, "开始处理跨节点观察者通知",
		"message_id", distMsg.Message.MessageID,
		"namespace", namespace,
		"group_ids", groupIDs,
		"from_node", distMsg.NodeID,
		"observer_count", len(observers),
	)

	// 预构建观察者专用消息（Clone + metadata），所有观察者共享同一份
	observerMsg := distMsg.Message.Clone()
	observerMsg.WithMetadata("observer_mode", "true")
	observerMsg.WithMetadata("original_sender", distMsg.Message.Sender)
	observerMsg.WithMetadata("original_receiver", distMsg.Message.Receiver)

	// 预序列化一次（所有观察者复用同一份 msgData，消除逐个 Clone+Marshal）
	msgData, err := json.Marshal(observerMsg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "跨节点观察者消息序列化失败",
			"message_id", distMsg.Message.MessageID, "error", err)
		return err
	}
	msgID := observerMsg.MessageID

	// 通知本节点的所有观察者
	var successCount atomic.Int32
	syncx.NewParallelSliceExecutor[*Client, error](observers).
		OnSuccess(func(idx int, client *Client, result error) {
			successCount.Add(1)
		}).
		OnError(func(idx int, client *Client, err error) {
			h.logger.WarnContextKV(ctx, "跨节点通知观察者失败",
				"observer_id", client.UserID,
				"client_id", client.ID,
				"message_id", msgID,
				"error", err,
			)
		}).
		OnPanic(func(idx int, client *Client, panicVal any) {
			h.logger.WarnContextKV(ctx, "跨节点通知观察者时发生 panic(通道可能已关闭)",
				"observer_id", client.UserID,
				"client_id", client.ID,
				"message_id", msgID,
				"panic", panicVal,
				"stack", string(debug.Stack()),
			)
		}).
		Execute(func(idx int, observer *Client) (error, error) {
			return h.sendToObserver(ctx, observer, msgID, msgData), nil
		})

	h.logger.DebugContextKV(ctx, "已处理跨节点观察者通知",
		"message_id", distMsg.Message.MessageID,
		"from_node", distMsg.NodeID,
		"total_observers", len(observers),
		"success_count", successCount.Load(),
	)

	return nil
}

// SubscribeBroadcastChannel 订阅全局广播频道
func (h *Hub) SubscribeBroadcastChannel(ctx context.Context) error {
	if h.pubsub == nil {
		return ErrPubSubNotSet
	}

	channel := h.config.RedisRepository.PubSub.GetBroadcastChannel()

	h.logger.InfoContextKV(ctx, "订阅全局广播频道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorContextKV(ctx, "广播频道订阅 panic", "panic", r, "stack", string(debug.Stack()), "channel", channel)
		}).
		Exec(func() {
			_, err := h.pubsub.Subscribe([]string{channel}, func(subCtx context.Context, ch string, msg string) error {
				distMsg, err := h.unmarshalDistributedMessage([]byte(msg))
				if err != nil {
					return err
				}

				// 忽略自己发出的广播
				if distMsg.NodeID == h.nodeID {
					return nil
				}

				// 使用订阅回调提供的 subCtx，而不是外层的 ctx
				return h.handleDistributedMessage(subCtx, distMsg)
			})

			if err != nil {
				h.logger.ErrorContextKV(ctx, "订阅广播频道失败", "error", err, "channel", channel)
			}

			// 使用 EventLoop 保持订阅活跃，直到 context 取消
			syncx.NewEventLoop(ctx).
				OnShutdown(func() {
					h.logger.InfoContextKV(ctx, "广播频道订阅已停止", "channel", channel)
				}).
				Run()
		})

	return nil
}

// SubscribeObserverChannel 订阅观察者通知频道
func (h *Hub) SubscribeObserverChannel(ctx context.Context) error {
	if h.pubsub == nil {
		return ErrPubSubNotSet
	}

	channel := h.config.RedisRepository.PubSub.GetObserverChannel()

	h.logger.InfoContextKV(ctx, "订阅观察者通知频道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorContextKV(ctx, "观察者频道订阅 panic", "panic", r, "stack", string(debug.Stack()), "channel", channel)
		}).
		Exec(func() {
			_, err := h.pubsub.Subscribe([]string{channel}, func(subCtx context.Context, ch string, msg string) error {
				distMsg, err := h.unmarshalDistributedMessage([]byte(msg))
				if err != nil {
					return err
				}

				// 使用订阅回调提供的 subCtx，而不是外层的 ctx
				return h.handleDistributedMessage(subCtx, distMsg)
			})

			if err != nil {
				h.logger.ErrorContextKV(ctx, "订阅观察者频道失败", "error", err, "channel", channel)
			}

			// 使用 EventLoop 保持订阅活跃，直到 context 取消
			syncx.NewEventLoop(ctx).
				OnShutdown(func() {
					h.logger.InfoContextKV(ctx, "观察者频道订阅已停止", "channel", channel)
				}).
				Run()
		})

	return nil
}

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
		return false, nil
	}

	h.logger.DebugKV("跨节点路由消息",
		"message_id", msg.MessageID,
		"user_id", userID,
		"from_node", h.nodeID,
		"to_nodes", otherNodes,
		"grpc_enabled", h.IsGRPCEnabled(),
	)

	// 4. 统一跨节点路由：gRPC 直连优先，PubSub 兜底（由 routeToCluster 集中决策）
	opts := ClusterDispatchOptions{
		Operation:     OperationTypeSendMessage,
		Namespace:     "", // 点对点消息不需要命名空间隔离路由
		TargetNodeIDs: otherNodes,
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

	h.logger.InfoKV("订阅节点消息通道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("节点消息订阅 panic", "panic", r, "stack", string(debug.Stack()), "channel", channel)
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
				h.logger.ErrorKV("订阅节点消息失败", "error", err, "channel", channel)
			}

			// 使用 EventLoop 保持订阅活跃，直到 context 取消
			syncx.NewEventLoop(ctx).
				OnShutdown(func() {
					h.logger.InfoKV("节点消息订阅已停止", "channel", channel)
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

	h.logger.DebugKV("收到分布式消息",
		"type", distMsg.Type,
		"from_node", distMsg.NodeID,
		"target_id", distMsg.TargetID,
	)

	switch distMsg.Type {
	case OperationTypeSendMessage:
		return h.handleDistributedSendMessage(ctx, distMsg)

	case OperationTypeKickUser:
		return h.handleDistributedKickUser(ctx, distMsg)

	case OperationTypeBroadcast:
		return h.handleDistributedBroadcast(ctx, distMsg)

	case OperationTypeGroupsBroadcast:
		return h.handleDistributedGroupsBroadcast(ctx, distMsg)

	case OperationTypeObserverNotify:
		return h.handleDistributedObserverNotify(ctx, distMsg)

	default:
		h.logger.WarnKV("未知的分布式消息类型", "type", distMsg.Type)
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
		h.logger.DebugKV("用户不在本节点", "user_id", distMsg.TargetID)
		return fmt.Errorf("user not found on this node: %s", distMsg.TargetID)
	}

	// 序列化消息为字节（预序列化一次，多设备复用）
	msgData, err := json.Marshal(distMsg.Message)
	if err != nil {
		return fmt.Errorf("marshal message failed: %w", err)
	}

	// 零拷贝遍历：ForEachUserClient 持读锁遍历，TrySend 非阻塞安全
	successCount := 0
	h.shardedRegistry.ForEachUserClient(distMsg.TargetID, func(_ string, client *Client) bool {
		if client.TrySend(msgData) {
			successCount++
		} else {
			h.logger.WarnKV("跨节点消息发送失败：发送缓冲区满或已关闭",
				"client_id", client.ID,
				"user_id", distMsg.TargetID,
				"message_id", distMsg.Message.MessageID,
			)
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

	h.logger.DebugKV("跨节点消息已发送到本地客户端",
		"message_id", distMsg.Message.MessageID,
		"user_id", distMsg.TargetID,
		"success_count", successCount,
	)

	// 🔔 通知观察者（跨节点消息也需要通知观察者）
	// 从 GroupIDs 提取 groupID（群组级观察者通知）
	observerGroupID := ""
	if len(distMsg.GroupIDs) > 0 {
		observerGroupID = distMsg.GroupIDs[0]
	}
	h.notifyObservers(ctx, distMsg.Message, distMsg.Namespace, observerGroupID)

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

	// 全命名空间广播（Namespace 为空）→ 走 broadcast 队列广播给所有客户端
	if namespace == "" {
		select {
		case h.broadcast <- distMsg.Message:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
			h.logger.WarnKV("广播队列已满", "message_id", distMsg.Message.MessageID)
			return nil
		}
	}

	// 命名空间广播 → 仅发送给同命名空间客户端
	count := h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.Namespace == namespace
	}, distMsg.Message)

	h.logger.DebugKV("跨节点命名空间广播已处理",
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
// 兼容两种来源：
//   - 批量广播：distMsg.GroupIDs 携带多个群组ID
//   - 单群组广播：distMsg.GroupIDs 为空时回退到 distMsg.TargetID（单群组场景）
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

	// 统一群组ID列表：优先 GroupIDs，回退 TargetID（单群组兼容）
	groupIDs := distMsg.GroupIDs
	if len(groupIDs) == 0 && distMsg.TargetID != "" {
		groupIDs = []string{distMsg.TargetID}
	}
	if len(groupIDs) == 0 {
		return fmt.Errorf("groupIDs is empty in distributed message")
	}

	namespace := distMsg.Namespace

	// Pipeline 批量获取所有群组成员（1 次 RTT，单群组场景等价于单次 SMEMBERS）
	groupMembers, err := h.groupRepo.GetMultiGroupMembers(ctx, namespace, groupIDs)
	if err != nil {
		h.logger.WarnKV("跨节点群组广播：获取群组成员失败",
			"namespace", namespace, "group_count", len(groupIDs), "error", err)
		return err
	}

	// 合并去重为一个 memberSet（用户在多个群组只收一条消息）
	memberSet := make(map[string]struct{})
	for _, members := range groupMembers {
		for _, uid := range members {
			memberSet[uid] = struct{}{}
		}
	}

	if len(memberSet) == 0 {
		return nil
	}

	// 按成员ID查找本地连接并投递（O(m) 替代 O(n) 全连接扫描）
	memberList := make([]string, 0, len(memberSet))
	for uid := range memberSet {
		memberList = append(memberList, uid)
	}
	count := h.broadcastToUserIDs(ctx, memberList, distMsg.Message)

	h.logger.DebugKV("跨节点群组广播已处理",
		"namespace", namespace,
		"group_count", len(groupIDs),
		"unique_members", len(memberSet),
		"from_node", distMsg.NodeID,
		"message_id", distMsg.Message.MessageID,
		"local_delivered", count,
	)

	return nil
}

// handleDistributedObserverNotify 处理跨节点观察者通知
// 命名空间隔离：仅通知全局观察者和同命名空间观察者
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

	// 获取消息命名空间ID用于过滤
	namespace := distMsg.Namespace

	// 获取本节点同命名空间的观察者（含全局观察者）
	observers := h.GetObserverClientsByNamespace(namespace)
	if len(observers) == 0 {
		h.logger.DebugContextKV(ctx, "本节点无匹配观察者，跳过通知",
			"message_id", distMsg.Message.MessageID,
			"namespace", namespace,
			"from_node", distMsg.NodeID,
		)
		return nil
	}

	h.logger.DebugContextKV(ctx, "开始处理跨节点观察者通知",
		"message_id", distMsg.Message.MessageID,
		"namespace", namespace,
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
			h.logger.WarnKV("跨节点通知观察者时发生 panic(通道可能已关闭)",
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

	h.logger.InfoKV("订阅全局广播频道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("广播频道订阅 panic", "panic", r, "stack", string(debug.Stack()), "channel", channel)
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
				h.logger.ErrorKV("订阅广播频道失败", "error", err, "channel", channel)
			}

			// 使用 EventLoop 保持订阅活跃，直到 context 取消
			syncx.NewEventLoop(ctx).
				OnShutdown(func() {
					h.logger.InfoKV("广播频道订阅已停止", "channel", channel)
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

	h.logger.InfoKV("订阅观察者通知频道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("观察者频道订阅 panic", "panic", r, "stack", string(debug.Stack()), "channel", channel)
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
				h.logger.ErrorKV("订阅观察者频道失败", "error", err, "channel", channel)
			}

			// 使用 EventLoop 保持订阅活跃，直到 context 取消
			syncx.NewEventLoop(ctx).
				OnShutdown(func() {
					h.logger.InfoKV("观察者频道订阅已停止", "channel", channel)
				}).
				Run()
		})

	return nil
}

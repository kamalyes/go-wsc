/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 01:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-30 11:20:15
 * @FilePath: \go-wsc\hub\distributed.go
 * @Description: Hub 分布式功能 - 跨节点消息路由
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 分布式消息路由
// ============================================================================

// checkAndRouteToNode 检查用户是否在其他节点，如果是则路由过去
// 返回: (是否在其他节点, 错误)
func (h *Hub) checkAndRouteToNode(ctx context.Context, userID string, msg *HubMessage) (bool, error) {
	// 如果没有启用 PubSub，说明是单机模式
	if h.pubsub == nil || h.onlineStatusRepo == nil {
		return false, nil
	}

	// 1. 查询用户在哪个节点
	nodeID, err := h.onlineStatusRepo.GetUserNode(ctx, userID)
	if err != nil {
		// 查询失败，假设用户在本节点或离线
		return false, nil
	}

	// 2. 如果在本节点，返回 false (不需要路由)
	if nodeID == "" || nodeID == h.nodeID {
		return false, nil
	}

	// 3. 用户在其他节点，通过 PubSub 转发
	h.logger.DebugKV("跨节点路由消息",
		"message_id", msg.MessageID,
		"user_id", userID,
		"from_node", h.nodeID,
		"to_node", nodeID,
	)

	distMsg := &DistributedMessage{
		Type:      OperationTypeSendMessage,
		NodeID:    h.nodeID,
		TargetID:  userID,
		Message:   msg,
		Timestamp: time.Now(),
	}

	channel := fmt.Sprintf("wsc:node:%s", nodeID)
	data, _ := json.Marshal(distMsg)

	if err := h.pubsub.Publish(ctx, channel, string(data)); err != nil {
		h.logger.ErrorKV("跨节点消息发布失败",
			"error", err,
			"target_node", nodeID,
			"message_id", msg.MessageID,
		)
		return true, ErrPubSubPublishFailed
	}

	return true, nil
}

// ============================================================================
// 节点间消息订阅
// ============================================================================

// SubscribeNodeMessages 订阅本节点的消息通道
func (h *Hub) SubscribeNodeMessages(ctx context.Context) error {
	if h.pubsub == nil {
		return ErrPubSubNotSet
	}

	channel := fmt.Sprintf("wsc:node:%s", h.nodeID)

	h.logger.InfoKV("订阅节点消息通道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("节点消息订阅 panic", "panic", r, "channel", channel)
		}).
		Exec(func() {
			_, err := h.pubsub.Subscribe([]string{channel}, func(subCtx context.Context, ch string, msg string) error {
				var distMsg DistributedMessage
				if err := json.Unmarshal([]byte(msg), &distMsg); err != nil {
					h.logger.ErrorKV("解析分布式消息失败", "error", err)
					return err
				}

				// 使用订阅回调提供的 subCtx，而不是外层的 ctx
				return h.handleDistributedMessage(subCtx, &distMsg)
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

	case OperationTypeObserverNotify:
		return h.handleDistributedObserverNotify(ctx, distMsg)

	default:
		h.logger.WarnKV("未知的分布式消息类型", "type", distMsg.Type)
		return fmt.Errorf("unknown message type: %s", distMsg.Type)
	}
}

// handleDistributedSendMessage 处理跨节点发送消息
func (h *Hub) handleDistributedSendMessage(ctx context.Context, distMsg *DistributedMessage) error {
	if distMsg.Message == nil {
		return fmt.Errorf("message data not found")
	}

	// 查找用户的所有客户端（使用 userToClients 而不是 clients）
	h.mutex.RLock()
	userClients, exists := h.userToClients[distMsg.TargetID]
	h.mutex.RUnlock()

	if !exists || len(userClients) == 0 {
		h.logger.DebugKV("用户不在本节点", "user_id", distMsg.TargetID)
		return fmt.Errorf("user not found on this node: %s", distMsg.TargetID)
	}

	// 序列化消息为字节
	msgData, err := json.Marshal(distMsg.Message)
	if err != nil {
		return fmt.Errorf("marshal message failed: %w", err)
	}

	// 发送到用户的所有客户端（支持多端登录）
	successCount := 0
	for _, client := range userClients {
		if client.IsClosed() {
			continue
		}

		if client.SendChan == nil {
			continue
		}

		select {
		case client.SendChan <- msgData:
			successCount++
		case <-ctx.Done():
			return fmt.Errorf("context cancelled: %w", ctx.Err())
		default:
			h.logger.WarnKV("跨节点消息发送失败：发送缓冲区满",
				"client_id", client.ID,
				"user_id", distMsg.TargetID,
				"message_id", distMsg.Message.MessageID,
			)
		}
	}

	if successCount == 0 {
		h.handleSendFailure(ctx, distMsg.TargetID, distMsg.Message, "all clients unavailable")
		return fmt.Errorf("failed to send to any client: %s", distMsg.TargetID)
	}

	h.logger.DebugKV("跨节点消息已发送到本地客户端",
		"message_id", distMsg.Message.MessageID,
		"user_id", distMsg.TargetID,
		"success_count", successCount,
		"total_clients", len(userClients),
	)

	// 🔔 通知观察者（跨节点消息也需要通知观察者）
	h.notifyObservers(distMsg.Message)

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
func (h *Hub) handleDistributedBroadcast(ctx context.Context, distMsg *DistributedMessage) error {
	if distMsg.Message == nil {
		return fmt.Errorf("message data not found")
	}

	// 广播给本节点的所有客户端
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

// ============================================================================
// 节点注册与健康检查
// ============================================================================

// RegisterNode 注册节点到 Redis
func (h *Hub) RegisterNode(ctx context.Context) error {
	if h.pubsub == nil {
		return ErrPubSubNotSet
	}

	nodeInfo := &NodeInfo{
		ID:          h.nodeID,
		IPAddress:   h.config.NodeIP,
		Port:        h.config.NodePort,
		Status:      NodeStatusActive,
		LastSeen:    time.Now(),
		Connections: int(h.activeClientsCount.Load()),
	}

	key := fmt.Sprintf("wsc:nodes:%s", h.nodeID)
	data, _ := json.Marshal(nodeInfo)

	h.logger.InfoKV("注册节点", "key", key, "nodeID", h.nodeID, "data", string(data))

	// 使用 Lua 脚本设置节点信息和过期时间
	script := `
		redis.call("set", KEYS[1], ARGV[1])
		redis.call("expire", KEYS[1], ARGV[2])
		return 1
	`

	err := h.pubsub.GetClient().Eval(ctx, script, []string{key}, string(data), 30).Err()
	if err != nil {
		h.logger.ErrorKV("注册节点失败", "error", err, "key", key)
	} else {
		h.logger.InfoKV("注册节点成功", "key", key)
	}
	return err
}

// StartNodeHeartbeat 启动节点心跳 (在 Hub.Run 中调用)
func (h *Hub) StartNodeHeartbeat(ctx context.Context) {
	if h.pubsub == nil {
		return
	}

	syncx.NewEventLoop(ctx).
		OnTicker(10*time.Second, func() {
			if err := h.RegisterNode(ctx); err != nil {
				h.logger.ErrorKV("节点心跳失败", "error", err)
			}
		}).
		OnPanic(func(r any) {
			h.logger.ErrorKV("节点心跳 panic", "panic", r)
		}).
		Run()
}

// DiscoverNodes 发现其他节点（使用 Lua 脚本）
func (h *Hub) DiscoverNodes(ctx context.Context) ([]*NodeInfo, error) {
	if h.pubsub == nil {
		return nil, ErrPubSubNotSet
	}

	// 使用 Lua 脚本扫描并获取所有节点信息
	script := `
		local pattern = ARGV[1]
		local cursor = "0"
		local nodes = {}
		
		repeat
			local result = redis.call("SCAN", cursor, "MATCH", pattern, "COUNT", 100)
			cursor = result[1]
			local keys = result[2]
			
			for i, key in ipairs(keys) do
				local data = redis.call("GET", key)
				if data then
					table.insert(nodes, data)
				end
			end
		until cursor == "0"
		
		return nodes
	`

	pattern := "wsc:nodes:*"
	h.logger.InfoKV("开始发现节点", "pattern", pattern, "currentNodeID", h.nodeID)

	result, err := h.pubsub.GetClient().Eval(ctx, script, []string{}, pattern).Result()
	if err != nil {
		h.logger.ErrorKV("发现节点失败", "error", err)
		return nil, fmt.Errorf("failed to discover nodes: %w", err)
	}

	// 解析结果
	nodeDataList, ok := result.([]any)
	if !ok {
		h.logger.ErrorKV("Lua 脚本返回类型错误", "type", fmt.Sprintf("%T", result))
		return nil, fmt.Errorf("unexpected result type from lua script")
	}

	h.logger.InfoKV("Lua 脚本返回节点数量", "count", len(nodeDataList))

	nodes := make([]*NodeInfo, 0, len(nodeDataList))
	for _, nodeData := range nodeDataList {
		dataStr, ok := nodeData.(string)
		if !ok {
			h.logger.WarnKV("节点数据类型错误", "type", fmt.Sprintf("%T", nodeData))
			continue
		}

		var node NodeInfo
		if err := json.Unmarshal([]byte(dataStr), &node); err != nil {
			h.logger.WarnKV("解析节点信息失败", "error", err, "data", dataStr)
			continue
		}

		h.logger.InfoKV("解析到节点", "nodeID", node.ID, "currentNodeID", h.nodeID)

		// 排除自己
		if node.ID != h.nodeID {
			nodes = append(nodes, &node)
		}
	}

	h.logger.InfoKV("发现节点完成", "totalFound", len(nodeDataList), "excludeSelf", len(nodes))

	return nodes, nil
}

// ============================================================================
// 分布式锁
// ============================================================================

// AcquireDistributedLock 获取分布式锁
func (h *Hub) AcquireDistributedLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if h.pubsub == nil {
		return false, ErrPubSubNotSet
	}

	lockKey := fmt.Sprintf("wsc:lock:%s", key)
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

	lockKey := fmt.Sprintf("wsc:lock:%s", key)

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
// 跨节点广播
// ============================================================================

// broadcastToAllNodes 广播消息到所有节点
func (h *Hub) broadcastToAllNodes(ctx context.Context, msg *HubMessage) error {
	if h.pubsub == nil {
		return nil // 单机模式，不需要跨节点广播
	}

	distMsg := &DistributedMessage{
		Type:      OperationTypeBroadcast,
		NodeID:    h.nodeID,
		Message:   msg,
		Timestamp: time.Now(),
	}

	// 发布到全局广播频道
	channel := "wsc:broadcast"
	data, _ := json.Marshal(distMsg)

	return h.pubsub.Publish(ctx, channel, string(data))
}

// handleDistributedObserverNotify 处理跨节点观察者通知
func (h *Hub) handleDistributedObserverNotify(ctx context.Context, distMsg *DistributedMessage) error {
	// 忽略自己发出的通知（本地观察者已经在 notifyObservers 中收到了）
	if distMsg.NodeID == h.nodeID {
		h.logger.DebugContextKV(ctx, "忽略自己发出的观察者通知",
			"from_node", distMsg.NodeID,
		)
		return nil
	}

	if distMsg.Message == nil {
		h.logger.ErrorContextKV(ctx, "观察者通知缺少消息数据",
			"from_node", distMsg.NodeID,
		)
		return fmt.Errorf("message data not found")
	}

	// 获取本节点的所有观察者
	observers := h.GetObserverClients()
	if len(observers) == 0 {
		h.logger.DebugContextKV(ctx, "本节点无观察者，跳过通知",
			"message_id", distMsg.Message.MessageID,
			"from_node", distMsg.NodeID,
		)
		return nil
	}

	h.logger.DebugContextKV(ctx, "开始处理跨节点观察者通知",
		"message_id", distMsg.Message.MessageID,
		"from_node", distMsg.NodeID,
		"observer_count", len(observers),
	)

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
				"message_id", distMsg.Message.MessageID,
				"error", err,
			)
		}).
		OnPanic(func(idx int, client *Client, panicVal any) {
			h.logger.WarnKV("跨节点通知观察者时发生 panic(通道可能已关闭)",
				"observer_id", client.UserID,
				"client_id", client.ID,
				"message_id", distMsg.Message.MessageID,
				"panic", panicVal,
			)
		}).
		Execute(func(idx int, observer *Client) (error, error) {
			return h.sendToObserver(observer, distMsg.Message), nil
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

	channel := "wsc:broadcast"

	h.logger.InfoKV("订阅全局广播频道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("广播频道订阅 panic", "panic", r, "channel", channel)
		}).
		Exec(func() {
			_, err := h.pubsub.Subscribe([]string{channel}, func(subCtx context.Context, ch string, msg string) error {
				var distMsg DistributedMessage
				if err := json.Unmarshal([]byte(msg), &distMsg); err != nil {
					h.logger.ErrorKV("解析广播消息失败", "error", err)
					return err
				}

				// 忽略自己发出的广播
				if distMsg.NodeID == h.nodeID {
					return nil
				}

				// 使用订阅回调提供的 subCtx，而不是外层的 ctx
				return h.handleDistributedMessage(subCtx, &distMsg)
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

	channel := "wsc:observers"

	h.logger.InfoKV("订阅观察者通知频道", "channel", channel)

	// 使用 EventLoop 包装订阅过程，提供 panic 恢复和优雅关闭
	syncx.Go(ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("观察者频道订阅 panic", "panic", r, "channel", channel)
		}).
		Exec(func() {
			_, err := h.pubsub.Subscribe([]string{channel}, func(subCtx context.Context, ch string, msg string) error {
				var distMsg DistributedMessage
				if err := json.Unmarshal([]byte(msg), &distMsg); err != nil {
					h.logger.ErrorKV("解析观察者通知失败", "error", err)
					return err
				}

				// 使用订阅回调提供的 subCtx，而不是外层的 ctx
				return h.handleDistributedMessage(subCtx, &distMsg)
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

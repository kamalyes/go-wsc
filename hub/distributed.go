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
		"message_id", msg.ID,
		"user_id", userID,
		"from_node", h.nodeID,
		"to_node", nodeID,
	)

	distMsg := &DistributedMessage{
		Type:     OperationTypeSendMessage,
		NodeID:   h.nodeID,
		TargetID: userID,
		Data: map[string]any{
			"message": msg,
		},
		Timestamp: time.Now(),
	}

	channel := fmt.Sprintf("wsc:node:%s", nodeID)
	data, _ := json.Marshal(distMsg)

	if err := h.pubsub.Publish(ctx, channel, string(data)); err != nil {
		h.logger.ErrorKV("跨节点消息发布失败",
			"error", err,
			"target_node", nodeID,
			"message_id", msg.ID,
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

				return h.handleDistributedMessage(ctx, &distMsg)
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

	default:
		h.logger.WarnKV("未知的分布式消息类型", "type", distMsg.Type)
		return fmt.Errorf("unknown message type: %s", distMsg.Type)
	}
}

// handleDistributedSendMessage 处理跨节点发送消息
func (h *Hub) handleDistributedSendMessage(ctx context.Context, distMsg *DistributedMessage) error {
	msgData, ok := distMsg.Data["message"]
	if !ok {
		return fmt.Errorf("message data not found")
	}

	// 反序列化消息
	msgBytes, _ := json.Marshal(msgData)
	var msg HubMessage
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		return fmt.Errorf("unmarshal message failed: %w", err)
	}

	// 直接发送到本地用户，跳过分布式路由检查（避免循环）
	h.mutex.RLock()
	client, exists := h.clients[distMsg.TargetID]
	h.mutex.RUnlock()

	if !exists {
		return fmt.Errorf("user not found on this node: %s", distMsg.TargetID)
	}

	// 序列化消息为字节
	msgData2, err := json.Marshal(&msg)
	if err != nil {
		return fmt.Errorf("marshal message failed: %w", err)
	}

	// 直接发送到客户端，不经过 sendToUser（避免再次路由）
	select {
	case client.SendChan <- msgData2:
		h.logger.DebugKV("跨节点消息已发送到本地客户端",
			"message_id", msg.ID,
			"user_id", distMsg.TargetID,
		)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	default:
		return fmt.Errorf("client send buffer full: %s", distMsg.TargetID)
	}
}

// handleDistributedKickUser 处理跨节点踢人
func (h *Hub) handleDistributedKickUser(ctx context.Context, distMsg *DistributedMessage) error {
	select {
	case <-ctx.Done():
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	default:
		reason, _ := distMsg.Data["reason"].(string)
		h.KickUserSimple(distMsg.TargetID, reason)
		return nil
	}
}

// handleDistributedBroadcast 处理跨节点广播
func (h *Hub) handleDistributedBroadcast(ctx context.Context, distMsg *DistributedMessage) error {
	msgData, ok := distMsg.Data["message"]
	if !ok {
		return fmt.Errorf("message data not found")
	}

	msgBytes, _ := json.Marshal(msgData)
	var msg HubMessage
	if err := json.Unmarshal(msgBytes, &msg); err != nil {
		return fmt.Errorf("unmarshal message failed: %w", err)
	}

	// 广播给本节点的所有客户端
	select {
	case h.broadcast <- &msg:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("context cancelled: %w", ctx.Err())
	default:
		h.logger.WarnKV("广播队列已满", "message_id", msg.ID)
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
		Type:     OperationTypeBroadcast,
		NodeID:   h.nodeID,
		TargetID: "",
		Data: map[string]any{
			"message": msg,
		},
		Timestamp: time.Now(),
	}

	// 发布到全局广播频道
	channel := "wsc:broadcast"
	data, _ := json.Marshal(distMsg)

	return h.pubsub.Publish(ctx, channel, string(data))
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

				return h.handleDistributedMessage(ctx, &distMsg)
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

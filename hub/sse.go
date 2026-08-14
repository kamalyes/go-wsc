/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:37:26
 * @FilePath: \go-wsc\hub\sse.go
 * @Description: Hub SSE 连接支持（重构版，统一使用 Client 结构）
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// ============================================================================
// SSE 注册/注销方法
// ============================================================================

// RegisterSSE 注册SSE连接（统一使用 Client 结构）
func (h *Hub) RegisterSSE(userID string, w http.ResponseWriter, userType UserType) (*Client, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// 创建 SSE 客户端（使用统一的 Client 结构）
	client := &Client{
		ID:             "sse-" + userID + "-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		UserID:         userID,
		UserType:       userType,
		ConnectionType: ConnectionTypeSSE,
		Status:         UserStatusOnline,
		NodeID:         h.nodeID,
		LastSeen:       time.Now(),
		LastHeartbeat:  time.Now(),
		Context:        context.Background(),
		Metadata:       make(map[string]interface{}),

		// SSE 专用字段
		SSEWriter:    w,
		SSEFlusher:   flusher,
		SSEMessageCh: make(chan *HubMessage, h.config.MessageBufferSize),
		SSECloseCh:   make(chan struct{}),
	}

	// 使用统一的注册通道
	h.register <- client

	h.logger.InfoKV("SSE连接已创建",
		"user_id", userID,
		"client_id", client.ID,
		"client_type", "sse",
	)

	return client, nil
}

// UnregisterSSE 注销SSE连接
func (h *Hub) UnregisterSSE(clientID string) {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if exists && client.ConnectionType == ConnectionTypeSSE {
		h.unregister <- client
		h.logger.InfoKV("SSE连接已注销",
			"user_id", client.UserID,
			"client_id", clientID,
		)
	}
}

// ============================================================================
// SSE 消息发送方法
// ============================================================================

// SendToUserViaSSE 通过SSE发送消息给指定用户（支持多设备，按 namespace 隔离）
// 使用 ForEachSSEUserClient 持读锁零拷贝遍历，替代 GetSSEUserClients 锁外遍历的数据竞争
// 🔏 namespace 隔离：与 ForEachUserClientFiltered 保持一致，
// msg.Namespace 非空时仅投递给同 ns 的 SSE 设备，避免同一 userID 跨 ns 串扰
func (h *Hub) SendToUserViaSSE(userID string, msg *HubMessage) bool {
	// 快速检查用户是否有 SSE 连接（O(1)）
	if !h.shardedRegistry.HasSSEUser(userID) {
		h.logger.WarnKV("SSE用户不存在",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
		)
		return false
	}

	// 持读锁零拷贝遍历发送
	successCount := 0
	totalDevices := 0
	h.shardedRegistry.ForEachSSEUserClient(userID, func(clientID string, client *Client) bool {
		// 🔏 namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的设备
		if msg.Namespace != "" && client.Namespace != msg.Namespace {
			return true
		}
		totalDevices++
		select {
		case client.SSEMessageCh <- msg:
			client.SetLastSeen(time.Now())
			successCount++
			h.logger.DebugContextKV(h.ctx, "SSE消息发送",
				"message_id", msg.MessageID,
				"from", msg.Sender,
				"to", userID,
				"client_id", clientID,
				"type", msg.MessageType,
			)
		default:
			// SSE消息队列满
			h.logger.WarnKV("SSE消息队列已满",
				"user_id", userID,
				"client_id", clientID,
				"message_id", msg.MessageID,
				"message_type", msg.MessageType,
			)
		}
		return true
	})

	if successCount > 0 {
		h.logger.InfoKV("SSE消息发送成功",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
			"success_devices", successCount,
			"total_devices", totalDevices,
		)
		return true
	}

	return false
}

// broadcastToSSEClients 广播消息到所有SSE客户端（按 namespace 隔离）
// 通过 shardedRegistry.ForEachSSEClient 分片读锁遍历，无外置锁
// 🔏 namespace 隔离：与 WebSocket 路径 ForEachClientFiltered 保持一致，
// msg.Namespace 非空时仅投递给同 ns 的 SSE 客户端，避免跨租户串扰
func (h *Hub) broadcastToSSEClients(msg *HubMessage) {
	h.shardedRegistry.ForEachSSEClient(func(userID, clientID string, client *Client) bool {
		if !ClientMatchesEnvelope(client, msg.Namespace, msg.GroupIDs) {
			return true
		}
		select {
		case client.SSEMessageCh <- msg:
			client.SetLastSeen(time.Now())
		default:
			// 消息通道满，跳过
			h.logger.WarnKV("SSE客户端消息通道已满，跳过",
				"user_id", userID,
				"client_id", clientID,
			)
		}
		return true
	})
}

// ============================================================================
// SSE 查询方法
// ============================================================================

// GetSSEClientCount 获取SSE客户端数量（原子计数器，零锁开销）
func (h *Hub) GetSSEClientCount() int {
	return int(h.shardedRegistry.GetSSEClientCount())
}

// GetSSEClients 获取所有SSE客户端列表
// 通过 shardedRegistry.ForEachSSEClient 收集（分片读锁粒度细）
func (h *Hub) GetSSEClients() []*Client {
	clients := make([]*Client, 0)
	h.shardedRegistry.ForEachSSEClient(func(_, _ string, client *Client) bool {
		clients = append(clients, client)
		return true
	})
	return clients
}

// IsSSEClientOnline 检查SSE客户端是否在线 - O(1)
func (h *Hub) IsSSEClientOnline(userID string) bool {
	return h.shardedRegistry.HasSSEUser(userID)
}

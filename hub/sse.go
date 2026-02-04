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
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
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
		ID:             fmt.Sprintf("sse-%s-%d", userID, time.Now().UnixNano()),
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
	h.mutex.RLock()
	client, exists := h.clients[clientID]
	h.mutex.RUnlock()

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

// SendToUserViaSSE 通过SSE发送消息给指定用户（支持多设备）
func (h *Hub) SendToUserViaSSE(userID string, msg *HubMessage) bool {
	h.sseMutex.RLock()
	clientMap, exists := h.sseClients[userID]
	h.sseMutex.RUnlock()

	if !exists || len(clientMap) == 0 {
		h.logger.WarnKV("SSE用户不存在",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
		)
		return false
	}

	// 发送到该用户的所有 SSE 设备
	successCount := 0
	for clientID, client := range clientMap {
		select {
		case client.SSEMessageCh <- msg:
			client.LastSeen = time.Now()
			successCount++
			h.logger.DebugKV("SSE消息发送",
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
	}

	if successCount > 0 {
		h.logger.InfoKV("SSE消息发送成功",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
			"success_devices", successCount,
			"total_devices", len(clientMap),
		)
		return true
	}

	return false
}

// broadcastToSSEClients 广播消息到所有SSE客户端
func (h *Hub) broadcastToSSEClients(msg *HubMessage) {
	syncx.WithRLock(&h.sseMutex, func() {
		for userID, clientMap := range h.sseClients {
			for clientID, client := range clientMap {
				select {
				case client.SSEMessageCh <- msg:
					client.LastSeen = time.Now()
				default:
					// 消息通道满，跳过
					h.logger.WarnKV("SSE客户端消息通道已满，跳过",
						"user_id", userID,
						"client_id", clientID,
					)
				}
			}
		}
	})
}

// ============================================================================
// SSE 查询方法
// ============================================================================

// GetSSEClientCount 获取SSE客户端数量
func (h *Hub) GetSSEClientCount() int {
	h.sseMutex.RLock()
	defer h.sseMutex.RUnlock()
	count := 0
	for _, clientMap := range h.sseClients {
		count += len(clientMap)
	}
	return count
}

// GetSSEClients 获取所有SSE客户端列表
func (h *Hub) GetSSEClients() []*Client {
	h.sseMutex.RLock()
	defer h.sseMutex.RUnlock()
	clients := make([]*Client, 0)
	for _, clientMap := range h.sseClients {
		for _, client := range clientMap {
			clients = append(clients, client)
		}
	}
	return clients
}

// IsSSEClientOnline 检查SSE客户端是否在线
func (h *Hub) IsSSEClientOnline(userID string) bool {
	h.sseMutex.RLock()
	defer h.sseMutex.RUnlock()
	clientMap, exists := h.sseClients[userID]
	return exists && len(clientMap) > 0
}

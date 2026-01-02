/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-31 00:55:15
 * @FilePath: \engine-im-service\go-wsc\hub\context.go
 * @Description: Hub 上下文和辅助方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 客户端查询方法
// ============================================================================

// GetMostRecentClient 获取用户对应的客户端（返回最近活跃的客户端）
func (h *Hub) GetMostRecentClient(userID string) *Client {
	return syncx.WithRLockReturnValue(&h.mutex, func() *Client {
		clientMap := h.userToClients[userID]
		if len(clientMap) == 0 {
			return nil
		}
		// 在锁内完成查找操作
		return findMostRecentClient(clientMap)
	})
}

// findMostRecentClient 从客户端map中找到最近活跃的客户端
func findMostRecentClient(clientMap map[string]*Client) *Client {
	if len(clientMap) == 0 {
		return nil
	}
	var mostRecent *Client
	for _, client := range clientMap {
		if mostRecent == nil || client.LastSeen.After(mostRecent.LastSeen) {
			mostRecent = client
		}
	}
	return mostRecent
}

// HasClient 检查是否存在指定ID的客户端
func (h *Hub) HasClient(clientID string) bool {
	return syncx.WithRLockReturnValue(&h.mutex, func() bool {
		_, exists := h.clients[clientID]
		return exists
	})
}

// HasUserClient 检查是否存在指定用户ID的客户端
func (h *Hub) HasUserClient(userID string) bool {
	return syncx.WithRLockReturnValue(&h.mutex, func() bool {
		clientMap, exists := h.userToClients[userID]
		return exists && len(clientMap) > 0
	})
}

// HasSSEClient 检查是否存在指定用户ID的SSE连接
func (h *Hub) HasSSEClient(userID string) bool {
	return syncx.WithRLockReturnValue(&h.sseMutex, func() bool {
		_, exists := h.sseClients[userID]
		return exists
	})
}

// HasAgentClient 检查是否存在指定用户ID的代理客户端
func (h *Hub) HasAgentClient(userID string) bool {
	return syncx.WithRLockReturnValue(&h.mutex, func() bool {
		clientMap, exists := h.agentClients[userID]
		return exists && len(clientMap) > 0
	})
}

// ============================================================================
// 用户在线信息查询
// ============================================================================

// GetUserOnlineInfo 获取用户在线信息（统一返回 Client 结构体）
// 先查本地连接（包括 WebSocket 和 SSE），再查 Redis
func (h *Hub) GetUserOnlineInfo(userID string) (*Client, error) {
	// 优先检查本地所有客户端连接（WebSocket 和 SSE 统一存储在 clients map）
	client := syncx.WithRLockReturnValue(&h.mutex, func() *Client {
		// 遍历所有客户端，找到匹配的用户ID
		for _, client := range h.clients {
			if client.UserID == userID {
				return client
			}
		}
		return nil
	})

	if client != nil {
		return client, nil
	}

	// 如果本地没有，查询 Redis 中其他节点的在线信息
	if h.onlineStatusRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return h.onlineStatusRepo.GetOnlineInfo(ctx, userID)
	}

	return nil, nil
}

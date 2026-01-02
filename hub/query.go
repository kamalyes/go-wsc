/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 19:26:30
 * @FilePath: \go-wsc\hub\query.go
 * @Description: Hub 查询和统计功能
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 统计方法
// ============================================================================

// GetStats 获取Hub统计信息
func (h *Hub) GetStats() *HubStats {
	wsCount, agentCount := syncx.WithRLockReturnWithE(&h.mutex, func() (int, int) {
		return len(h.clients), len(h.agentClients)
	})

	sseCount := syncx.WithRLockReturnValue(&h.sseMutex, func() int {
		return len(h.sseClients)
	})

	stats := &HubStats{
		TotalClients:     wsCount + sseCount,
		WebSocketClients: wsCount,
		SSEClients:       sseCount,
		AgentConnections: agentCount,
		QueuedMessages:   len(h.pendingMessages),
		OnlineUsers:      h.GetOnlineUsersCount(),
		Uptime:           h.GetUptime(),
	}

	// 从 statsRepo 获取更详细的统计信息
	if h.statsRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()
		if nodeStats, err := h.statsRepo.GetNodeStats(ctx, h.nodeID); err == nil && nodeStats != nil {
			stats.MessagesSent = nodeStats.MessagesSent
			stats.MessagesReceived = nodeStats.MessagesReceived
			stats.BroadcastsSent = nodeStats.BroadcastsSent
		}
	}

	return stats
}

// GetUptime 获取Hub运行时长
func (h *Hub) GetUptime() int64 {
	return time.Since(h.startTime).Milliseconds()
}

// GetOnlineUsers 获取所有在线用户ID列表
func (h *Hub) GetOnlineUsers() []string {
	wsUsers := syncx.WithRLockReturnValue(&h.mutex, func() []string {
		users := make([]string, 0, len(h.userToClients))
		for userID := range h.userToClients {
			users = append(users, userID)
		}
		return users
	})

	sseUsers := syncx.WithRLockReturnValue(&h.sseMutex, func() []string {
		users := make([]string, 0, len(h.sseClients))
		for userID := range h.sseClients {
			users = append(users, userID)
		}
		return users
	})

	// 合并并去重
	return mathx.SliceUniq(append(wsUsers, sseUsers...))
}

// GetOnlineUsersCount 获取在线用户数量
func (h *Hub) GetOnlineUsersCount() int {
	return len(h.GetOnlineUsers())
}

// GetOnlineUserCountByType 根据用户类型获取在线用户数量
func (h *Hub) GetOnlineUserCountByType(userType UserType) (int64, error) {
	clients := h.GetClientsByUserType(userType)

	// 按用户ID去重计数（同一用户多个连接只计数一次）
	userSet := make(map[string]struct{})
	for _, client := range clients {
		userSet[client.UserID] = struct{}{}
	}

	return int64(len(userSet)), nil
}

// GetClientsCount 获取客户端连接总数
func (h *Hub) GetClientsCount() int {
	return syncx.WithRLockReturnValue(&h.mutex, func() int {
		return len(h.clients)
	})
}

// GetClientCount 获取客户端总数（别名方法）
func (h *Hub) GetClientCount() int {
	return h.GetClientsCount()
}

// ============================================================================
// 查询方法
// ============================================================================

// IsUserOnline 检查用户是否在线
func (h *Hub) IsUserOnline(userID string) (bool, error) {
	// 优先检查本地 WebSocket 连接
	isOnline := syncx.WithRLockReturnValue(&h.mutex, func() bool {
		for _, client := range h.clients {
			if client.UserID == userID {
				return true
			}
		}
		return false
	})

	if isOnline {
		return true, nil
	}

	// 检查 SSE 连接
	sseExists := syncx.WithRLockReturnValue(&h.sseMutex, func() bool {
		_, exists := h.sseClients[userID]
		return exists
	})

	if sseExists {
		return true, nil
	}

	// 如果有 Redis repository，检查其他节点
	if h.onlineStatusRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return h.onlineStatusRepo.IsOnline(ctx, userID)
	}

	return false, nil
}

// GetClientByID 根据客户端ID获取客户端
func (h *Hub) GetClientByID(clientID string) *Client {
	client, _ := h.GetClientByIDWithLock(clientID)
	return client
}

// GetClientsByUserID 根据用户ID获取所有客户端
func (h *Hub) GetClientsByUserID(userID string) []*Client {
	clientMap := syncx.WithRLockReturnValue(&h.mutex, func() map[string]*Client {
		return h.userToClients[userID]
	})

	if len(clientMap) == 0 {
		return nil
	}

	return CopyClientsFromMap(clientMap)
}

// GetUserStatus 获取用户状态
func (h *Hub) GetUserStatus(userID string) UserStatus {
	clients := h.GetClientsByUserID(userID)
	if len(clients) == 0 {
		return UserStatusOffline
	}

	// 返回最后活跃的客户端状态
	var mostRecent *Client
	for _, client := range clients {
		if mostRecent == nil || client.LastSeen.After(mostRecent.LastSeen) {
			mostRecent = client
		}
	}
	if mostRecent != nil {
		return mostRecent.Status
	}
	return UserStatusOffline
}

// GetClientIPs 获取用户所有客户端的IP地址列表
func (h *Hub) GetClientIPs(userID string) []string {
	clients := h.GetClientsByUserID(userID)
	if len(clients) == 0 {
		return nil
	}

	ips := make([]string, 0, len(clients))
	for _, client := range clients {
		if ip := h.getClientIPFromClient(client); ip != "" {
			ips = append(ips, ip)
		}
	}
	return ips
}

// getClientIPFromClient 从客户端获取IP地址
func (h *Hub) getClientIPFromClient(client *Client) string {
	if client.ClientIP != "" {
		return client.ClientIP
	}
	if client.Metadata != nil {
		if ip, ok := client.Metadata["client_ip"].(string); ok {
			return ip
		}
	}
	return ""
}

// ============================================================================
// 元数据操作
// ============================================================================

// GetClientMetadata 获取客户端元数据
func (h *Hub) GetClientMetadata(clientID string, key string) (interface{}, bool) {
	client, exists := syncx.WithRLockReturnWithE(&h.mutex, func() (*Client, bool) {
		client, exists := h.clients[clientID]
		return client, exists
	})

	if !exists || client.Metadata == nil {
		return nil, false
	}
	val, ok := client.Metadata[key]
	return val, ok
}

// UpdateClientMetadata 更新客户端元数据
func (h *Hub) UpdateClientMetadata(clientID string, key string, value interface{}) error {
	client, exists := syncx.WithRLockReturnWithE(&h.mutex, func() (*Client, bool) {
		client, exists := h.clients[clientID]
		return client, exists
	})

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	if client.Metadata == nil {
		client.Metadata = make(map[string]interface{})
	}
	client.Metadata[key] = value
	return nil
}

// ============================================================================
// 分组查询方法
// ============================================================================

// GetClientsByDepartmentGrouped 按部门分组获取客户端
func (h *Hub) GetClientsByDepartmentGrouped() map[Department][]*Client {
	result := make(map[Department][]*Client)
	allClients := h.GetClientsCopy()

	for _, client := range allClients {
		result[client.Department] = append(result[client.Department], client)
	}
	return result
}

// GetClientsByUserTypeGrouped 按用户类型分组获取客户端
func (h *Hub) GetClientsByUserTypeGrouped() map[UserType][]*Client {
	result := make(map[UserType][]*Client)
	allClients := h.GetClientsCopy()

	for _, client := range allClients {
		result[client.UserType] = append(result[client.UserType], client)
	}
	return result
}

// GetClientsByStatusGrouped 按状态分组获取客户端
func (h *Hub) GetClientsByStatusGrouped() map[UserStatus][]*Client {
	result := make(map[UserStatus][]*Client)
	allClients := h.GetClientsCopy()

	for _, client := range allClients {
		result[client.Status] = append(result[client.Status], client)
	}
	return result
}

// GetClientsWithStatus 获取指定状态的所有客户端
func (h *Hub) GetClientsWithStatus(status UserStatus) []*Client {
	allClients := h.GetClientsCopy()
	return mathx.FilterSlice(allClients, func(client *Client) bool {
		return client.Status == status
	})
}

// ============================================================================
// 连接信息方法
// ============================================================================

// GetConnectionDetail 获取连接详细信息（返回 Client 结构体副本）
func (h *Hub) GetConnectionDetail(clientID string) *Client {
	client, exists := syncx.WithRLockReturnWithE(&h.mutex, func() (*Client, bool) {
		client, exists := h.clients[clientID]
		return client, exists
	})

	if !exists {
		return nil
	}

	// 返回副本，避免外部修改
	return client
}

// GetClientStats 获取客户端统计信息
func (h *Hub) GetClientStats(clientID string) map[string]interface{} {
	client := h.GetConnectionDetail(clientID)
	if client == nil {
		return nil
	}

	return map[string]interface{}{
		"connection_info":     client,
		"connection_duration": time.Since(client.LastSeen),
	}
}

// ============================================================================
// 过滤和搜索方法
// ============================================================================

// FilterClients 按条件过滤客户端
func (h *Hub) FilterClients(predicate func(*Client) bool) []*Client {
	allClients := h.GetClientsCopy()
	return mathx.FilterSlice(allClients, predicate)
}

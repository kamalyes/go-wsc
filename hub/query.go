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
)

// ============================================================================
// 统计方法
// ============================================================================

// GetStats 获取Hub统计信息
func (h *Hub) GetStats() *HubStats {
	// shardedRegistry 原子计数（主存储 + 分类索引）
	totalCount := h.shardedRegistry.GetClientCount()
	sseCount := h.shardedRegistry.GetSSEClientCount()
	agentCount := int64(h.shardedRegistry.GetAgentUserCount())

	stats := &HubStats{
		TotalClients:     totalCount,
		WebSocketClients: totalCount - sseCount, // WS 连接数 = 总连接数 - SSE 连接数
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
// 主存储已包含所有连接类型（WS + SSE），直接返回即可
func (h *Hub) GetOnlineUsers() []string {
	return h.shardedRegistry.GetOnlineUserIDs()
}

// GetOnlineUsersCount 获取在线用户数量
// 使用原子计数器 O(1)，替代 GetOnlineUsers() 的 O(n) Keys() 遍历
func (h *Hub) GetOnlineUsersCount() int {
	return int(h.shardedRegistry.GetUserCount())
}

// GetOnlineUserCountByType 根据用户类型获取在线用户数量
// 使用 ForEachClient 零拷贝遍历 + 用户去重，替代 GetClientsByUserType 切片拷贝
func (h *Hub) GetOnlineUserCountByType(userType UserType) (int64, error) {
	userSet := make(map[string]struct{})
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		if client.UserType == userType {
			userSet[client.UserID] = struct{}{}
		}
		return true
	})
	return int64(len(userSet)), nil
}

// GetClientsCount 获取客户端连接总数
func (h *Hub) GetClientsCount() int64 {
	return int64(h.shardedRegistry.GetClientCount())
}

// GetClientCount 获取客户端总数（别名方法）
func (h *Hub) GetClientCount() int64 {
	return h.GetClientsCount()
}

// ============================================================================
// 查询方法
// ============================================================================

// IsUserOnline 检查用户是否在线
func (h *Hub) IsUserOnline(userID string) (bool, error) {
	// 检查 shardedRegistry 主存储（原子读，包含 WS + SSE）
	if h.shardedRegistry.HasUser(userID) {
		return true, nil
	}

	// 如果有 Redis repository，检查其他节点
	if h.onlineStatusRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return h.onlineStatusRepo.IsUserOnline(ctx, userID)
	}

	return false, nil
}

// GetClientByID 根据客户端ID获取客户端
func (h *Hub) GetClientByID(clientID string) *Client {
	client, _ := h.GetClientByIDWithLock(clientID)
	return client
}

// GetClientsByUserID 根据用户ID获取所有客户端
// 使用 ForEachUserClient 持读锁遍历，替代 GetUserClients 锁外 CopyClientsFromMap 的数据竞争
func (h *Hub) GetClientsByUserID(userID string) []*Client {
	clients := make([]*Client, 0, 4)
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
		clients = append(clients, client)
		return true
	})
	if len(clients) == 0 {
		return nil
	}
	return clients
}

// GetUserStatus 获取用户状态
// 使用 ForEachUserClient 零拷贝遍历，替代 GetClientsByUserID 切片拷贝
func (h *Hub) GetUserStatus(userID string) UserStatus {
	var mostRecent *Client
	var mostRecentSeen time.Time
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
		seen := client.GetLastSeen()
		if mostRecent == nil || seen.After(mostRecentSeen) {
			mostRecent = client
			mostRecentSeen = seen
		}
		return true
	})
	if mostRecent != nil {
		return mostRecent.GetStatus()
	}
	return UserStatusOffline
}

// GetClientIPs 获取用户所有客户端的IP地址列表
// 使用 ForEachUserClient 零拷贝遍历，替代 GetClientsByUserID 切片拷贝
func (h *Hub) GetClientIPs(userID string) []string {
	// 先计数预估容量
	count := h.shardedRegistry.GetUserClientCount(userID)
	if count == 0 {
		return nil
	}

	ips := make([]string, 0, count)
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
		if ip := h.getClientIPFromClient(client); ip != "" {
			ips = append(ips, ip)
		}
		return true
	})
	return ips
}

// getClientIPFromClient 从客户端获取IP地址
func (h *Hub) getClientIPFromClient(client *Client) string {
	if client.ClientIP != "" {
		return client.ClientIP
	}
	if val, ok := client.GetMetadataValue("client_ip"); ok {
		if ip, ok := val.(string); ok && ip != "" {
			return ip
		}
	}
	return ""
}

// ============================================================================
// 元数据操作
// ============================================================================

// GetClientMetadata 获取客户端元数据（线程安全）
func (h *Hub) GetClientMetadata(clientID string, key string) (interface{}, bool) {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if !exists {
		return nil, false
	}
	return client.GetMetadataValue(key)
}

// UpdateClientMetadata 更新客户端元数据（线程安全）
func (h *Hub) UpdateClientMetadata(clientID string, key string, value interface{}) error {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}
	client.SetMetadataValue(key, value)
	return nil
}

// ============================================================================
// 分组查询方法
// ============================================================================

// GetClientsByDepartmentGrouped 按部门分组获取客户端（零拷贝遍历）
func (h *Hub) GetClientsByDepartmentGrouped() map[Department][]*Client {
	result := make(map[Department][]*Client)
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		result[client.Department] = append(result[client.Department], client)
		return true
	})
	return result
}

// GetClientsByUserTypeGrouped 按用户类型分组获取客户端（零拷贝遍历）
func (h *Hub) GetClientsByUserTypeGrouped() map[UserType][]*Client {
	result := make(map[UserType][]*Client)
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		result[client.UserType] = append(result[client.UserType], client)
		return true
	})
	return result
}

// GetClientsByStatusGrouped 按状态分组获取客户端（零拷贝遍历，原子读 Status）
func (h *Hub) GetClientsByStatusGrouped() map[UserStatus][]*Client {
	result := make(map[UserStatus][]*Client)
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		status := client.GetStatus()
		result[status] = append(result[status], client)
		return true
	})
	return result
}

// GetClientsWithStatus 获取指定状态的所有客户端（委托 FilterClients 零拷贝，原子读 Status）
func (h *Hub) GetClientsWithStatus(status UserStatus) []*Client {
	return h.FilterClients(func(c *Client) bool { return c.GetStatus() == status })
}

// ============================================================================
// 连接信息方法
// ============================================================================

// GetConnectionDetail 获取连接详细信息（返回 Client 结构体副本）
func (h *Hub) GetConnectionDetail(clientID string) *Client {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if !exists {
		return nil
	}
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
		"connection_duration": time.Since(client.GetLastSeen()),
	}
}

// ============================================================================
// 过滤和搜索方法
// ============================================================================

// FilterClients 按条件过滤客户端（零拷贝：ForEachClient 遍历 + 条件收集，避免 GetClientsCopy 全量拷贝）
func (h *Hub) FilterClients(predicate func(*Client) bool) []*Client {
	if predicate == nil {
		return []*Client{}
	}
	result := make([]*Client, 0, 16)
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		if predicate(client) {
			result = append(result, client)
		}
		return true
	})
	return result
}

// ============================================================================
// 客户端存在性检查（原 context.go，合并至此避免文件碎片化）
// ============================================================================

// GetMostRecentClient 获取用户对应的客户端（返回最近活跃的客户端）
// 使用 ForEachUserClient 零拷贝遍历（持读锁），替代 GetUserClients 锁外遍历内部 map 的数据竞争
func (h *Hub) GetMostRecentClient(userID string) *Client {
	var mostRecent *Client
	var mostRecentSeen time.Time
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
		seen := client.GetLastSeen()
		if mostRecent == nil || seen.After(mostRecentSeen) {
			mostRecent = client
			mostRecentSeen = seen
		}
		return true
	})
	return mostRecent
}

// findMostRecentClient 从客户端map中找到最近活跃的客户端
func findMostRecentClient(clientMap map[string]*Client) *Client {
	if len(clientMap) == 0 {
		return nil
	}
	var mostRecent *Client
	var mostRecentSeen time.Time
	for _, client := range clientMap {
		seen := client.GetLastSeen()
		if mostRecent == nil || seen.After(mostRecentSeen) {
			mostRecent = client
			mostRecentSeen = seen
		}
	}
	return mostRecent
}

// HasClient 检查是否存在指定ID的客户端
func (h *Hub) HasClient(clientID string) bool {
	_, exists := h.shardedRegistry.GetClient(clientID)
	return exists
}

// HasUserClient 检查是否存在指定用户ID的客户端
func (h *Hub) HasUserClient(userID string) bool {
	return h.shardedRegistry.HasUser(userID)
}

// HasSSEClient 检查是否存在指定用户ID的SSE连接 - O(1)
func (h *Hub) HasSSEClient(userID string) bool {
	return h.shardedRegistry.HasSSEUser(userID)
}

// HasAgentClient 检查是否存在指定用户ID的代理客户端 - O(1)
func (h *Hub) HasAgentClient(userID string) bool {
	return h.shardedRegistry.HasAgent(userID)
}

// GetClientsCopy 获取所有客户端的副本
// 使用 shardedRegistry 的批量查询接口，分片读锁粒度细
func (h *Hub) GetClientsCopy() []*Client {
	return h.shardedRegistry.GetAllClients()
}

// GetUserClientsCopy 获取每个用户最活跃的客户端副本列表
// 遍历所有 shard，每个用户取最近活跃的客户端（复用 findMostRecentClient）
func (h *Hub) GetUserClientsCopy() []*Client {
	result := make([]*Client, 0, h.shardedRegistry.GetUserCount())
	h.shardedRegistry.ForEachUser(func(_ string, clientMap map[string]*Client) bool {
		if mostRecent := findMostRecentClient(clientMap); mostRecent != nil {
			result = append(result, mostRecent)
		}
		return true
	})
	return result
}

// GetUserClientsMapWithLock 获取指定用户的所有客户端映射(带锁)
// 返回的是 shardedRegistry 内部 map 的引用（持有读锁时获取的快照）
// 注意：调用方不应长时间持有该引用，建议立即复制使用
func (h *Hub) GetUserClientsMapWithLock(userID string) (map[string]*Client, bool) {
	return h.shardedRegistry.GetUserClients(userID)
}

// GetClientsCopyForUser 获取用户的客户端列表副本（线程安全）
// 如果指定了 clientID，只返回该客户端；否则返回用户的所有客户端
// 使用 ForEachUserClient 持读锁遍历，替代 GetUserClients 锁外遍历的数据竞争
func (h *Hub) GetClientsCopyForUser(userID, clientID string) []*Client {
	// 指定 clientID 时用 O(1) 查找
	if clientID != "" {
		if client, exists := h.shardedRegistry.GetClient(clientID); exists && client.UserID == userID {
			return []*Client{client}
		}
		return nil
	}

	// 未指定 clientID 时零拷贝遍历收集
	clients := make([]*Client, 0, 4)
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
		clients = append(clients, client)
		return true
	})
	if len(clients) == 0 {
		return nil
	}
	return clients
}

// GetConnectionsByUserID 获取用户的所有连接
// 使用 ForEachUserClient 持读锁遍历，替代 GetUserClients 锁外 CopyClientsFromMap 的数据竞争
func (h *Hub) GetConnectionsByUserID(userID string) []*Client {
	clients := make([]*Client, 0, 4)
	h.shardedRegistry.ForEachUserClient(userID, func(_ string, client *Client) bool {
		clients = append(clients, client)
		return true
	})
	if len(clients) == 0 {
		return nil
	}
	return clients
}

// checkUserOnline 检查用户是否在线（支持分布式）
func (h *Hub) checkUserOnline(userID string) bool {
	// 1. 先检查本地 shardedRegistry 是否在线（原子读，零锁开销）
	if h.shardedRegistry.HasUser(userID) {
		return true
	}

	// 2. 如果本地不在线，且启用了分布式，则查询Redis
	if h.onlineStatusRepo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		nodes, err := h.onlineStatusRepo.GetUserNodes(ctx, userID)
		if err == nil && len(nodes) > 0 {
			// 用户在其他节点在线
			return true
		}
	}

	// 3. 本地和Redis都没有，用户离线
	return false
}

// GetClientByIDWithLock 获取客户端(返回是否存在)
// 使用 shardedRegistry 的 clientID→userID 索引定位 shard
func (h *Hub) GetClientByIDWithLock(clientID string) (*Client, bool) {
	return h.shardedRegistry.GetClient(clientID)
}

// GetHubHealth 获取Hub健康状态
// 使用 shardedRegistry 原子计数器
func (h *Hub) GetHubHealth() *HubHealthInfo {
	totalCount := int(h.shardedRegistry.GetClientCount())
	sseCount := int(h.shardedRegistry.GetSSEClientCount())

	return &HubHealthInfo{
		Status:           "healthy",
		IsRunning:        h.IsStarted(),
		WebSocketCount:   totalCount - sseCount,
		SSECount:         sseCount,
		TotalConnections: totalCount,
		NodeID:           h.nodeID,
	}
}

// GetOnlineUsersByType 按用户类型获取在线用户列表
// 使用 ForEachClient 零拷贝遍历 + 用户去重，替代 FilterClients 切片拷贝
func (h *Hub) GetOnlineUsersByType(userType UserType) ([]string, error) {
	seen := make(map[string]struct{})
	h.shardedRegistry.ForEachClient(func(_ string, client *Client) bool {
		if client.UserType == userType {
			seen[client.UserID] = struct{}{}
		}
		return true
	})

	userIDs := make([]string, 0, len(seen))
	for uid := range seen {
		userIDs = append(userIDs, uid)
	}
	return userIDs, nil
}

// CopyClientsFromMap 从客户端映射中复制客户端列表
// 用于避免在遍历时map被修改导致的数据竞争
func CopyClientsFromMap(clientMap map[string]*Client) []*Client {
	clients := make([]*Client, 0, len(clientMap))
	for _, client := range clientMap {
		clients = append(clients, client)
	}
	return clients
}

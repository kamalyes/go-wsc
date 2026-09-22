/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 02:20:00
 * @FilePath: \go-wsc\connection\query.go
 * @Description: 连接域 —— 注册表查询门面（数据贴数据）
 *
 * 从 hub/query.go 拆解归位：与 ShardedRegistry 原语纯重复的门面别名已删除
 * （调用方直用原语），此处仅保留带附加价值的查询：信封过滤遍历、
 * 分组聚合、谓词收集、元数据操作。跨节点在线查询归 stats 域。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// GetClientsByUserID 根据用户ID获取所有客户端（按 ctx 路由信封的 appID+namespace 隔离）
// 路由信封通过 routing 从 ctx 注入，调用方不需传 appID/namespace 参数
// 使用 ForEachUserClientFiltered 持读锁遍历（复用 ClientMatchesEnvelope 单一真相源）
func (r *ShardedRegistry) GetClientsByUserID(ctx context.Context, userID string) []*models.Client {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	clients := make([]*models.Client, 0, 4)
	r.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
		clients = append(clients, client)
		return true
	})
	if len(clients) == 0 {
		return nil
	}
	return clients
}

// GetUserStatus 获取用户状态（按 ctx 路由信封的 appID+namespace 隔离）
// 路由信封通过 routing 从 ctx 注入，调用方不需传 appID/namespace 参数
// 使用 ForEachUserClientFiltered 零拷贝遍历（复用 ClientMatchesEnvelope 单一真相源）
func (r *ShardedRegistry) GetUserStatus(ctx context.Context, userID string) models.UserStatus {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	var mostRecent *models.Client
	var mostRecentSeen time.Time
	r.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
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
	return models.UserStatusOffline
}

// GetClientIPs 获取用户所有客户端的IP地址列表（按 ctx 路由信封的 appID+namespace 隔离）
// 路由信封通过 routing 从 ctx 注入，调用方不需传 appID/namespace 参数
// 使用 ForEachUserClientFiltered 零拷贝遍历（复用 ClientMatchesEnvelope 单一真相源）
func (r *ShardedRegistry) GetClientIPs(ctx context.Context, userID string) []string {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	// 先计数预估容量（不过滤的计数，仅用于预分配）
	count := r.GetUserClientCount(userID)
	if count == 0 {
		return nil
	}

	ips := make([]string, 0, count)
	r.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
		if ip := GetClientIP(client); ip != "" {
			ips = append(ips, ip)
		}
		return true
	})
	return ips
}

// GetClientIP 从客户端获取IP地址
func GetClientIP(client *models.Client) string {
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
func (r *ShardedRegistry) GetClientMetadata(clientID string, key string) (interface{}, bool) {
	client, exists := r.GetClient(clientID)
	if !exists {
		return nil, false
	}
	return client.GetMetadataValue(key)
}

// UpdateClientMetadata 更新客户端元数据（线程安全）
func (r *ShardedRegistry) UpdateClientMetadata(clientID string, key string, value interface{}) error {
	client, exists := r.GetClient(clientID)
	if !exists {
		return errorx.NewError(models.ErrTypeClientNotFound, "client_id: %s", clientID)
	}
	client.SetMetadataValue(key, value)
	return nil
}

// ============================================================================
// 分组查询方法
// ============================================================================

// GetClientsByDepartmentGrouped 按部门分组获取客户端（零拷贝遍历）
func (r *ShardedRegistry) GetClientsByDepartmentGrouped() map[models.Department][]*models.Client {
	result := make(map[models.Department][]*models.Client)
	r.ForEachClient(func(_ string, client *models.Client) bool {
		result[client.Department] = append(result[client.Department], client)
		return true
	})
	return result
}

// GetClientsByUserTypeGrouped 按用户类型分组获取客户端（零拷贝遍历）
func (r *ShardedRegistry) GetClientsByUserTypeGrouped() map[models.UserType][]*models.Client {
	result := make(map[models.UserType][]*models.Client)
	r.ForEachClient(func(_ string, client *models.Client) bool {
		result[client.UserType] = append(result[client.UserType], client)
		return true
	})
	return result
}

// GetClientsByStatusGrouped 按状态分组获取客户端（零拷贝遍历，原子读 Status）
func (r *ShardedRegistry) GetClientsByStatusGrouped() map[models.UserStatus][]*models.Client {
	result := make(map[models.UserStatus][]*models.Client)
	r.ForEachClient(func(_ string, client *models.Client) bool {
		status := client.GetStatus()
		result[status] = append(result[status], client)
		return true
	})
	return result
}

// GetClientsWithStatus 获取指定状态的所有客户端（委托 FilterClients 零拷贝，原子读 Status）
func (r *ShardedRegistry) GetClientsWithStatus(status models.UserStatus) []*models.Client {
	return r.FilterClients(func(c *models.Client) bool { return c.GetStatus() == status })
}

// ============================================================================
// 连接信息方法
// ============================================================================

// GetClientStats 获取客户端统计信息
func (r *ShardedRegistry) GetClientStats(clientID string) map[string]interface{} {
	client, _ := r.GetClient(clientID)
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
func (r *ShardedRegistry) FilterClients(predicate func(*models.Client) bool) []*models.Client {
	if predicate == nil {
		return []*models.Client{}
	}
	result := make([]*models.Client, 0, 16)
	r.ForEachClient(func(_ string, client *models.Client) bool {
		if predicate(client) {
			result = append(result, client)
		}
		return true
	})
	return result
}

// GetMostRecentClient 获取用户对应的客户端（返回最近活跃的客户端，按 ctx 路由信封 appID+namespace 隔离）
// 使用 ForEachUserClientFiltered 零拷贝遍历（持读锁），替代 GetUserClients 锁外遍历内部 map 的数据竞争
func (r *ShardedRegistry) GetMostRecentClient(ctx context.Context, userID string) *models.Client {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	var mostRecent *models.Client
	var mostRecentSeen time.Time
	r.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
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
func findMostRecentClient(clientMap map[string]*models.Client) *models.Client {
	if len(clientMap) == 0 {
		return nil
	}
	var mostRecent *models.Client
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

// HasUserClient 检查是否存在指定用户ID的客户端（按 ctx 路由信封的 appID+namespace 隔离）
// 路由信封通过 routing 从 ctx 注入，调用方不需传 appID/namespace 参数
func (r *ShardedRegistry) HasUserClient(ctx context.Context, userID string) bool {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	return r.HasUser(userID, appID, ns)
}

// GetUserClientsCopy 获取每个用户最活跃的客户端副本列表
// 遍历所有 shard，每个用户取最近活跃的客户端（复用 findMostRecentClient）
func (r *ShardedRegistry) GetUserClientsCopy() []*models.Client {
	result := make([]*models.Client, 0, r.GetUserCount())
	r.ForEachUser(func(_ string, clientMap map[string]*models.Client) bool {
		if mostRecent := findMostRecentClient(clientMap); mostRecent != nil {
			result = append(result, mostRecent)
		}
		return true
	})
	return result
}

// GetClientsCopyForUser 获取用户的客户端列表副本（线程安全，按 ctx 路由信封 appID+namespace 隔离）
// 如果指定了 clientID，只返回该客户端（O(1) 查找，仍校验 client 的 appID+namespace 信封匹配）
// 否则返回用户匹配信封的所有客户端
// 使用 ForEachUserClientFiltered 持读锁遍历，替代 GetUserClients 锁外遍历的数据竞争
func (r *ShardedRegistry) GetClientsCopyForUser(ctx context.Context, userID, clientID string) []*models.Client {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	// 指定 clientID 时用 O(1) 查找（仍校验信封匹配，避免跨 app/ns 返回）
	if clientID != "" {
		client, exists := r.GetClient(clientID)
		if !exists || client.UserID != userID {
			return nil
		}
		// appID 为空时退化（兼容无路由 ctx），否则校验信封
		if appID != "" && !ClientMatchesEnvelope(client, appID, ns, nil) {
			return nil
		}
		return []*models.Client{client}
	}

	// 未指定 clientID 时零拷贝遍历收集（按信封过滤）
	clients := make([]*models.Client, 0, 4)
	r.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *models.Client) bool {
		clients = append(clients, client)
		return true
	})
	if len(clients) == 0 {
		return nil
	}
	return clients
}

// GetOnlineUserCountByType 根据用户类型获取在线用户数量
// 使用 ForEachClient 零拷贝遍历 + 用户去重，替代 GetClientsByUserType 切片拷贝
func (r *ShardedRegistry) GetOnlineUserCountByType(userType models.UserType) (int64, error) {
	userSet := make(map[string]struct{})
	r.ForEachClient(func(_ string, client *models.Client) bool {
		if client.UserType == userType {
			userSet[client.UserID] = struct{}{}
		}
		return true
	})
	return int64(len(userSet)), nil
}

// GetOnlineUsersByType 按用户类型获取在线用户列表
// 使用 ForEachClient 零拷贝遍历 + 用户去重，替代 FilterClients 切片拷贝
func (r *ShardedRegistry) GetOnlineUsersByType(userType models.UserType) ([]string, error) {
	seen := make(map[string]struct{})
	r.ForEachClient(func(_ string, client *models.Client) bool {
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
func CopyClientsFromMap(clientMap map[string]*models.Client) []*models.Client {
	clients := make([]*models.Client, 0, len(clientMap))
	for _, client := range clientMap {
		clients = append(clients, client)
	}
	return clients
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:02:15
 * @FilePath: \go-wsc\hub\sharded_registry.go
 * @Description: 分片注册表（基于 go-toolbox 的 syncx.ShardedMap）
 *
 * 主存储（userShards）：
 *   - userID → (clientID → *Client)
 *   - 按 userID 的 FNV-1a hash 分散到 64 个 shard
 *   - AddClient/RemoveClient 通过 WithShardLock 在同一 shard 锁内原子操作
 *
 * 分类分片索引（sseShards/observerShards/agentShards）：
 *   - 替代 Hub 上的 sseClients/observerClients/agentClients 外置 map
 *   - 结构与主存储一致：userID → (clientID → *Client)
 *   - 仅在客户端匹配对应类型/连接类型时写入，nil 表示该模块未启用
 *   - 与主存储写入/删除原子完成（同一 client 同一 userID 落同一 shard）
 *
 * 性能提升：
 *   - 64 个 shard 将锁竞争降低 64 倍
 *   - 不同用户的注册/注销/发送操作完全并行
 *   - 原子计数器零锁开销
 */

package hub

import (
	"sync"
	"sync/atomic"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
)

// defaultShardCount 默认分片数量
// 64 个分片在 8-32 核 CPU 上能充分并行，且内存开销可控
const defaultShardCount = 64

// ShardedRegistry 分片注册表
// 基于 syncx.ShardedMap 实现，替代 Hub 的单一 mutex + map 结构
// 同时承担原本由 Hub 维护的 sseClients/observerClients/agentClients 分类索引
type ShardedRegistry struct {
	// userShards 主存储：userID → (clientID → Client)
	// 使用 syncx.ShardedMap 的分片锁，粒度细
	userShards *syncx.ShardedMap[string, map[string]*Client]

	// clientIDToUserID 客户端ID → 用户ID 反向索引
	// 用于 GetClient(clientID) 时定位所在 shard
	// sync.Map 读多写少场景性能优异
	clientIDToUserID sync.Map

	// clientCount 客户端总数（原子计数器，避免读锁）
	clientCount atomic.Int64

	// userCount 用户总数（原子计数器）
	userCount atomic.Int64

	// sseShards SSE 连接分类索引：userID → (clientID → Client)
	// 替代 Hub.sseClients，nil 表示未启用（当前实现下总是启用）
	sseShards *syncx.ShardedMap[string, map[string]*Client]

	// observerShards 观察者分类索引：userID → (clientID → Client)
	// 替代 Hub.observerClients，nil 表示该模块未启用（读写均跳过）
	observerShards *syncx.ShardedMap[string, map[string]*Client]

	// agentShards 客服/Bot 分类索引：userID → (clientID → Client)
	// 替代 Hub.agentClients，nil 表示该模块未启用（读写均跳过）
	agentShards *syncx.ShardedMap[string, map[string]*Client]

	// sseCount SSE 连接数（原子计数器，替代 Hub.sseClientsCount）
	// WS 连接数 = clientCount - sseCount，无需单独维护
	sseCount atomic.Int64
}

// NewShardedRegistry 创建分片注册表
// agentEnabled / observerEnabled 控制是否启用对应分类分片；
// 未启用的分类 shards 保持 nil，相关读写直接跳过，避免无谓内存与锁开销
func NewShardedRegistry(agentEnabled, observerEnabled bool) *ShardedRegistry {
	r := &ShardedRegistry{
		// 主存储与 SSE 索引始终启用
		userShards:       syncx.NewShardedMap[string, map[string]*Client](defaultShardCount),
		sseShards:        syncx.NewShardedMap[string, map[string]*Client](defaultShardCount),
		clientIDToUserID: sync.Map{},
	}
	if agentEnabled {
		r.agentShards = syncx.NewShardedMap[string, map[string]*Client](defaultShardCount)
	}
	if observerEnabled {
		r.observerShards = syncx.NewShardedMap[string, map[string]*Client](defaultShardCount)
	}
	return r
}

// ============================================================================
// 客户端管理
// ============================================================================

// AddClient 添加客户端到注册表
// 主存储 + 分类索引在同一 userID 的 shard 锁内原子完成（ShardedMap 保证同一 key 落同一 shard）
func (r *ShardedRegistry) AddClient(client *Client) {
	if client == nil {
		return
	}

	// 1. 主存储：userID → (clientID → Client)
	r.userShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			userClients = make(map[string]*Client)
			data[client.UserID] = userClients
			r.userCount.Add(1)
		}
		userClients[client.ID] = client
	})

	// 2. 反向索引
	r.clientIDToUserID.Store(client.ID, client.UserID)

	// 3. 分类索引（按类型写入对应分片）
	if client.ConnectionType == models.ConnectionTypeSSE {
		r.addSSEClient(client)
		r.sseCount.Add(1)
	}

	if client.UserType == models.UserTypeObserver {
		r.addObserverClient(client)
	}

	if client.UserType == models.UserTypeAgent || client.UserType == models.UserTypeBot {
		r.addAgentClient(client)
	}

	// 4. 总计数器
	r.clientCount.Add(1)
}

// RemoveClient 从注册表移除客户端
// 返回被移除的客户端（nil 表示不存在）
func (r *ShardedRegistry) RemoveClient(clientID, userID string) *Client {
	var removed *Client

	// 1. 主存储移除
	r.userShards.WithShardLock(userID, func(data map[string]map[string]*Client) {
		userClients, exists := data[userID]
		if !exists {
			return
		}
		removed = userClients[clientID]
		if removed == nil {
			return
		}
		delete(userClients, clientID)
		if len(userClients) == 0 {
			delete(data, userID)
			r.userCount.Add(-1)
		}
	})

	if removed == nil {
		return nil
	}

	// 2. 分类索引移除（按客户端类型走对应分片）
	if removed.ConnectionType == models.ConnectionTypeSSE {
		r.removeSSEClient(removed)
		r.sseCount.Add(-1)
	}
	if removed.UserType == models.UserTypeObserver {
		r.removeObserverClient(removed)
	}
	if removed.UserType == models.UserTypeAgent || removed.UserType == models.UserTypeBot {
		r.removeAgentClient(removed)
	}

	// 3. 反向索引与计数器
	r.clientIDToUserID.Delete(clientID)
	r.clientCount.Add(-1)

	return removed
}

// GetClient 根据 clientID 获取客户端
// 先查 clientIDToUserID 反向索引定位 shard，再读锁查找
func (r *ShardedRegistry) GetClient(clientID string) (*Client, bool) {
	userIDVal, ok := r.clientIDToUserID.Load(clientID)
	if !ok {
		return nil, false
	}
	userID := userIDVal.(string)

	var client *Client
	var exists bool

	r.userShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		userClients, ok := data[userID]
		if !ok {
			return
		}
		client, exists = userClients[clientID]
	})

	return client, exists
}

// GetUserClients 获取用户的所有客户端
// 返回的是内部 map 引用（读锁期间获取的快照），调用方不应长时间持有
func (r *ShardedRegistry) GetUserClients(userID string) (map[string]*Client, bool) {
	var clients map[string]*Client
	var exists bool

	r.userShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		clients, exists = data[userID]
	})

	return clients, exists
}

// GetUserClientCount 获取用户的客户端数量
func (r *ShardedRegistry) GetUserClientCount(userID string) int {
	var count int
	r.userShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		clients := data[userID]
		count = len(clients)
	})
	return count
}

// HasUser 检查用户是否在线（主存储，任意连接类型）
func (r *ShardedRegistry) HasUser(userID string) bool {
	var exists bool
	r.userShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		_, exists = data[userID]
	})
	return exists
}

// ============================================================================
// 批量查询与遍历
// ============================================================================

// GetAllClients 获取所有客户端列表
// 通过 ShardedMap.Range 遍历所有 shard（分片读锁，粒度细）
func (r *ShardedRegistry) GetAllClients() []*Client {
	result := make([]*Client, 0, r.clientCount.Load())

	r.userShards.Range(func(_ string, userClients map[string]*Client) bool {
		for _, client := range userClients {
			result = append(result, client)
		}
		return true
	})

	return result
}

// GetOnlineUserIDs 获取所有在线用户ID列表
// 使用 ShardedMap.Keys 直接获取所有 userID
func (r *ShardedRegistry) GetOnlineUserIDs() []string {
	return r.userShards.Keys()
}

// ForEachClient 遍历所有客户端
// 回调返回 false 时停止遍历
// 注意：回调在持有读锁时执行，不应执行耗时操作
func (r *ShardedRegistry) ForEachClient(fn func(clientID string, client *Client) bool) {
	r.userShards.Range(func(_ string, userClients map[string]*Client) bool {
		for clientID, client := range userClients {
			if !fn(clientID, client) {
				return false
			}
		}
		return true
	})
}

// ForEachUser 遍历所有在线用户
// 回调返回 false 时停止遍历
// 注意：回调在持有读锁时执行，不应执行耗时操作
func (r *ShardedRegistry) ForEachUser(fn func(userID string, clients map[string]*Client) bool) {
	r.userShards.Range(func(userID string, userClients map[string]*Client) bool {
		return fn(userID, userClients)
	})
}

// ============================================================================
// SSE 分类索引 API（替代 Hub.sseClients 的所有访问点）
// ============================================================================

// GetSSEUserClients 获取指定用户的所有 SSE 客户端（用于按 userID 推送）
// 返回内部 map 引用，调用方应在读锁释放后立即使用或拷贝
func (r *ShardedRegistry) GetSSEUserClients(userID string) (map[string]*Client, bool) {
	var clients map[string]*Client
	var exists bool

	r.sseShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		clients, exists = data[userID]
	})

	return clients, exists
}

// ForEachSSEClient 遍历所有 SSE 客户端（用于广播）
// 回调返回 false 时停止遍历
func (r *ShardedRegistry) ForEachSSEClient(fn func(userID string, clientID string, client *Client) bool) {
	r.sseShards.Range(func(userID string, userClients map[string]*Client) bool {
		for clientID, client := range userClients {
			if !fn(userID, clientID, client) {
				return false
			}
		}
		return true
	})
}

// GetSSEUserCount 获取 SSE 在线用户数
func (r *ShardedRegistry) GetSSEUserCount() int {
	return r.sseShards.Len()
}

// GetSSEUserIDs 获取所有 SSE 在线用户ID
func (r *ShardedRegistry) GetSSEUserIDs() []string {
	return r.sseShards.Keys()
}

// HasSSEUser 检查指定用户是否有 SSE 连接
func (r *ShardedRegistry) HasSSEUser(userID string) bool {
	var exists bool
	r.sseShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		clients, ok := data[userID]
		exists = ok && len(clients) > 0
	})
	return exists
}

// addSSEClient 添加 SSE 客户端到分类索引（私有，由 AddClient 调用）
func (r *ShardedRegistry) addSSEClient(client *Client) {
	r.sseShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			userClients = make(map[string]*Client)
			data[client.UserID] = userClients
		}
		userClients[client.ID] = client
	})
}

// removeSSEClient 从分类索引移除 SSE 客户端（私有，由 RemoveClient 调用）
func (r *ShardedRegistry) removeSSEClient(client *Client) {
	r.sseShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			return
		}
		delete(userClients, client.ID)
		if len(userClients) == 0 {
			delete(data, client.UserID)
		}
	})
}

// ============================================================================
// Observer 分类索引 API（替代 Hub.observerClients 的所有访问点）
// ============================================================================

// ObserverEnabled 返回观察者模块是否启用（shards 非 nil）
func (r *ShardedRegistry) ObserverEnabled() bool {
	return r.observerShards != nil
}

// GetObserverUserClients 获取指定观察者的所有客户端
func (r *ShardedRegistry) GetObserverUserClients(userID string) (map[string]*Client, bool) {
	if r.observerShards == nil {
		return nil, false
	}
	var clients map[string]*Client
	var exists bool

	r.observerShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		clients, exists = data[userID]
	})

	return clients, exists
}

// ForEachObserver 遍历所有观察者客户端
func (r *ShardedRegistry) ForEachObserver(fn func(userID string, clientID string, client *Client) bool) {
	if r.observerShards == nil {
		return
	}
	r.observerShards.Range(func(userID string, userClients map[string]*Client) bool {
		for clientID, client := range userClients {
			if !fn(userID, clientID, client) {
				return false
			}
		}
		return true
	})
}

// GetObserverUserCount 获取观察者用户数（设备去重）
func (r *ShardedRegistry) GetObserverUserCount() int {
	if r.observerShards == nil {
		return 0
	}
	return r.observerShards.Len()
}

// GetObserverDeviceCount 获取观察者设备总数
func (r *ShardedRegistry) GetObserverDeviceCount() int {
	if r.observerShards == nil {
		return 0
	}
	count := 0
	r.observerShards.Range(func(_ string, userClients map[string]*Client) bool {
		count += len(userClients)
		return true
	})
	return count
}

// HasObserver 检查用户是否为观察者 - O(1)
func (r *ShardedRegistry) HasObserver(userID string) bool {
	if r.observerShards == nil {
		return false
	}
	var exists bool
	r.observerShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		_, exists = data[userID]
	})
	return exists
}

// addObserverClient 添加观察者到分类索引（私有，由 AddClient 调用）
func (r *ShardedRegistry) addObserverClient(client *Client) {
	if r.observerShards == nil {
		return
	}
	r.observerShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			userClients = make(map[string]*Client)
			data[client.UserID] = userClients
		}
		userClients[client.ID] = client
	})
}

// removeObserverClient 从分类索引移除观察者（私有，由 RemoveClient 调用）
func (r *ShardedRegistry) removeObserverClient(client *Client) {
	if r.observerShards == nil {
		return
	}
	r.observerShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			return
		}
		delete(userClients, client.ID)
		if len(userClients) == 0 {
			delete(data, client.UserID)
		}
	})
}

// ============================================================================
// Agent 分类索引 API（替代 Hub.agentClients 的所有访问点）
// ============================================================================

// AgentEnabled 返回客服模块是否启用（shards 非 nil）
func (r *ShardedRegistry) AgentEnabled() bool {
	return r.agentShards != nil
}

// GetAgentUserClients 获取指定客服的所有客户端
func (r *ShardedRegistry) GetAgentUserClients(userID string) (map[string]*Client, bool) {
	if r.agentShards == nil {
		return nil, false
	}
	var clients map[string]*Client
	var exists bool

	r.agentShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		clients, exists = data[userID]
	})

	return clients, exists
}

// GetAgentUserCount 获取客服用户数 - O(1)
func (r *ShardedRegistry) GetAgentUserCount() int {
	if r.agentShards == nil {
		return 0
	}
	return r.agentShards.Len()
}

// HasAgent 检查用户是否为客服 - O(1)
func (r *ShardedRegistry) HasAgent(userID string) bool {
	if r.agentShards == nil {
		return false
	}
	var exists bool
	r.agentShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		clients, ok := data[userID]
		exists = ok && len(clients) > 0
	})
	return exists
}

// addAgentClient 添加客服到分类索引（私有，由 AddClient 调用）
func (r *ShardedRegistry) addAgentClient(client *Client) {
	if r.agentShards == nil {
		return
	}
	r.agentShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			userClients = make(map[string]*Client)
			data[client.UserID] = userClients
		}
		userClients[client.ID] = client
	})
}

// removeAgentClient 从分类索引移除客服（私有，由 RemoveClient 调用）
func (r *ShardedRegistry) removeAgentClient(client *Client) {
	if r.agentShards == nil {
		return
	}
	r.agentShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			return
		}
		delete(userClients, client.ID)
		if len(userClients) == 0 {
			delete(data, client.UserID)
		}
	})
}

// ============================================================================
// 统计信息
// ============================================================================

// GetClientCount 获取客户端总数（原子读取，零锁开销）
func (r *ShardedRegistry) GetClientCount() int64 {
	return r.clientCount.Load()
}

// GetUserCount 获取在线用户总数（原子读取，零锁开销）
func (r *ShardedRegistry) GetUserCount() int64 {
	return r.userCount.Load()
}

// GetSSEClientCount 获取 SSE 客户端总数（原子读取，零锁开销）
func (r *ShardedRegistry) GetSSEClientCount() int64 {
	return r.sseCount.Load()
}

// GetActiveClientCount 获取活跃（非 SSE）客户端数 = 总连接数 - SSE 连接数
func (r *ShardedRegistry) GetActiveClientCount() int64 {
	return r.clientCount.Load() - r.sseCount.Load()
}

// Clear 清空注册表
func (r *ShardedRegistry) Clear() {
	r.userShards.Clear()
	r.sseShards.Clear()

	if r.observerShards != nil {
		r.observerShards.Clear()
	}
	if r.agentShards != nil {
		r.agentShards.Clear()
	}

	// 清空反向索引
	r.clientIDToUserID.Range(func(key, value any) bool {
		r.clientIDToUserID.Delete(key)
		return true
	})

	r.clientCount.Store(0)
	r.userCount.Store(0)
	r.sseCount.Store(0)
}

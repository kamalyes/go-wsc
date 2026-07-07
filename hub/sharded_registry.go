/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:02:15
 * @FilePath: \go-wsc\hub\sharded_registry.go
 * @Description: 分片注册表（基于 go-cachex 的 ShardedMap）
 *
 * 底层存储使用 cachex.ShardedMap[string, map[string]*Client]
 *   - key: userID
 *   - value: map[clientID]*Client（同一用户的所有连接）
 *   - 分片：按 userID 的 FNV-1a hash 分散到 64 个 shard
 *
 * 双索引原子性：
 *   - AddClient/RemoveClient 通过 ShardedMap.WithShardLock 在同一 shard 锁内
 *     原子操作 userID→clients 和 clientID→Client 两个层级
 *   - GetClient 通过 clientID→userID 反向索引定位 shard，再读锁查找
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
)

// defaultShardCount 默认分片数量
// 64 个分片在 8-32 核 CPU 上能充分并行，且内存开销可控
const defaultShardCount = 64

// ShardedRegistry 分片注册表
// 基于 cachex.ShardedMap 实现，替代 Hub 的单一 mutex + map 结构
type ShardedRegistry struct {
	// userShards 用户分片存储：userID → (clientID → Client)
	// 使用 cachex.ShardedMap 的分片锁，粒度细
	userShards *syncx.ShardedMap[string, map[string]*Client]

	// clientIDToUserID 客户端ID → 用户ID 反向索引
	// 用于 GetClient(clientID) 时定位所在 shard
	// sync.Map 读多写少场景性能优异
	clientIDToUserID sync.Map

	// clientCount 客户端总数（原子计数器，避免读锁）
	clientCount atomic.Int64

	// userCount 用户总数（原子计数器）
	userCount atomic.Int64
}

// NewShardedRegistry 创建分片注册表
func NewShardedRegistry() *ShardedRegistry {
	return &ShardedRegistry{
		// 使用 FNV-1a hash 的 string 分片 map（64 分片）
		userShards: syncx.NewShardedMap[string, map[string]*Client](defaultShardCount),
	}
}

// ============================================================================
// 客户端管理
// ============================================================================

// AddClient 添加客户端到注册表
// 通过 WithShardLock 在同一 shard 锁内原子更新 userID→clients 和 clientID→Client
func (r *ShardedRegistry) AddClient(client *Client) {
	if client == nil {
		return
	}

	// 在 userID 对应的 shard 锁内原子操作
	r.userShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		// 获取或创建用户的 client map
		userClients, exists := data[client.UserID]
		if !exists {
			userClients = make(map[string]*Client)
			data[client.UserID] = userClients
			r.userCount.Add(1)
		}
		userClients[client.ID] = client
	})

	// 更新 clientID → userID 反向索引
	r.clientIDToUserID.Store(client.ID, client.UserID)

	// 更新计数器
	r.clientCount.Add(1)
}

// RemoveClient 从注册表移除客户端
// 返回被移除的客户端（nil 表示不存在）
func (r *ShardedRegistry) RemoveClient(clientID, userID string) *Client {
	var removed *Client

	// 在 userID 对应的 shard 锁内原子移除
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
		// 如果用户没有更多客户端，移除整个用户的条目
		if len(userClients) == 0 {
			delete(data, userID)
			r.userCount.Add(-1)
		}
	})

	if removed != nil {
		// 移除 clientID → userID 反向索引
		r.clientIDToUserID.Delete(clientID)
		// 更新计数器
		r.clientCount.Add(-1)
	}

	return removed
}

// GetClient 根据 clientID 获取客户端
// 先查 clientIDToUserID 反向索引定位 shard，再读锁查找
func (r *ShardedRegistry) GetClient(clientID string) (*Client, bool) {
	// 查 clientID → userID 索引
	userIDVal, ok := r.clientIDToUserID.Load(clientID)
	if !ok {
		return nil, false
	}
	userID := userIDVal.(string)

	var client *Client
	var exists bool

	// 在 userID 对应的 shard 读锁内查找
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

// HasUser 检查用户是否在线
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

// Clear 清空注册表
func (r *ShardedRegistry) Clear() {
	r.userShards.Clear()

	// 清空反向索引
	r.clientIDToUserID.Range(func(key, value any) bool {
		r.clientIDToUserID.Delete(key)
		return true
	})

	r.clientCount.Store(0)
	r.userCount.Store(0)
}

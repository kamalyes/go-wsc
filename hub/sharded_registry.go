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

// RegistryCapacity 容量提示（用于预分配各 ShardedMap 的每 shard 内部 map 容量）
//
// 设计思路：
//   - 总容量除以 shardCount 得到每 shard 提示，减少 map 扩容次数
//   - 各分类索引（SSE/Observer/Agent）按预估占比独立分配，避免过度预分配
//   - 0 表示不预分配，由 map 自适应扩容
type RegistryCapacity struct {
	// TotalClients 总客户端连接数预估（主存储 userShards）
	TotalClients int
	// SSEClients SSE 客户端连接数预估（sseShards）
	SSEClients int
	// ObserverClients 观察者连接数预估（observerShards）
	ObserverClients int
	// AgentClients 客服/Bot 连接数预估（agentShards）
	AgentClients int
}

// perShardHint 总容量除以分片数，向上取整
// 总容量 <=0 时返回 0（不预分配）
func (c RegistryCapacity) perShardHint(total int) int {
	if total <= 0 {
		return 0
	}
	return (total + defaultShardCount - 1) / defaultShardCount
}

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

	// observerUserCount 观察者用户数（设备去重，原子计数器）
	// observerShards 通过 WithShardLock 写入，其内部 count 不会被更新，
	// 因此单独维护原子计数器保证 GetObserverUserCount 的 O(1) 且准确
	observerUserCount atomic.Int64

	// observerIdx 观察者三级二级索引（加速 namespace+group 级查找，消除 O(n) 全量扫描）
	// observerShards 为主存储（userID 分片），observerIdx 为按观察范围的二级索引
	// 三级：global（所有命名空间）/ byNamespace（指定命名空间）/ byGroup（指定命名空间+群组）
	observerIdx observerIndex

	// agentShards 客服/Bot 分类索引：userID → (clientID → Client)
	// 替代 Hub.agentClients，nil 表示该模块未启用（读写均跳过）
	agentShards *syncx.ShardedMap[string, map[string]*Client]

	// agentUserCount 客服用户数（设备去重，原子计数器）
	// 与 observerUserCount 同理，agentShards 通过 WithShardLock 写入，
	// 单独维护原子计数器保证 GetAgentUserCount 的 O(1) 且准确
	agentUserCount atomic.Int64

	// sseCount SSE 连接数（原子计数器，替代 Hub.sseClientsCount）
	// WS 连接数 = clientCount - sseCount，无需单独维护
	sseCount atomic.Int64
}

// NewShardedRegistry 创建分片注册表
// agentEnabled / observerEnabled 控制是否启用对应分类分片；
// 未启用的分类 shards 保持 nil，相关读写直接跳过，避免无谓内存与锁开销
//
// capacity 提供总容量预估，用于预分配各 ShardedMap 的每 shard 内部 map 容量；
// 传入零值 RegistryCapacity 时退化为不预分配（与旧版兼容）
func NewShardedRegistry(agentEnabled, observerEnabled bool, capacity RegistryCapacity) *ShardedRegistry {
	r := &ShardedRegistry{
		userShards:       newClientShardedMap(capacity.TotalClients),
		sseShards:        newClientShardedMap(capacity.SSEClients),
		clientIDToUserID: sync.Map{},
	}
	if agentEnabled {
		r.agentShards = newClientShardedMap(capacity.AgentClients)
	}
	if observerEnabled {
		r.observerShards = newClientShardedMap(capacity.ObserverClients)
		r.observerIdx = newObserverIndex()
	}
	return r
}

// newClientShardedMap 创建统一类型的客户端分片映射表（userID → (clientID → *Client)）
// 封装默认 shardCount + WithPerShardHint 选项，避免每个分类索引重复书写
// totalHint 为该索引预估总容量，内部按 defaultShardCount 平均分摊
func newClientShardedMap(totalHint int) *syncx.ShardedMap[string, map[string]*Client] {
	return syncx.NewShardedMapWithOptions(
		defaultShardCount,
		syncx.WithPerShardHint[string, map[string]*Client](RegistryCapacity{}.perShardHint(totalHint)),
	)
}

// ============================================================================
// 客户端管理
// ============================================================================

// AddClient 添加客户端到注册表
// 主存储 + 分类索引在同一 userID 的 shard 锁内原子完成（ShardedMap 保证同一 key 落同一 shard）
//
// 覆盖语义（断线重连场景）：当相同 clientID 已存在时，仅更新主存储与分类索引的指针，
// 不重复累加 clientCount/sseCount，否则计数器会随每次重连持续膨胀、与实际连接数脱节
// observerUserCount/agentUserCount 在各自 add 方法内已按"用户是否新增"判定，无需此处处理
func (r *ShardedRegistry) AddClient(client *Client) {
	if client == nil {
		return
	}

	// 1. 主存储：userID → (clientID → Client）
	isNew := false
	r.userShards.WithShardLock(client.UserID, func(data map[string]map[string]*Client) {
		userClients, exists := data[client.UserID]
		if !exists {
			userClients = make(map[string]*Client)
			data[client.UserID] = userClients
			r.userCount.Add(1)
		}
		if _, dup := userClients[client.ID]; !dup {
			isNew = true
		}
		userClients[client.ID] = client
	})

	// 2. 反向索引
	r.clientIDToUserID.Store(client.ID, client.UserID)

	// 3. 分类索引（按类型写入对应分片）
	//    始终调用以刷新指针（断线重连覆盖时分类索引可能持有已关闭的旧客户端指针）；
	//    计数器仅在真正新增时累加，避免覆盖场景重复计数
	if client.ConnectionType == models.ConnectionTypeSSE {
		r.addSSEClient(client)
		if isNew {
			r.sseCount.Add(1)
		}
	}

	if client.UserType == models.UserTypeObserver {
		r.addObserverClient(client)
	}

	if client.UserType == models.UserTypeAgent || client.UserType == models.UserTypeBot {
		r.addAgentClient(client)
	}

	// 4. 总计数器（仅新增时累加，覆盖场景不重复计数）
	if isNew {
		r.clientCount.Add(1)
	}
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

// ForEachUserClient 遍历指定用户的所有客户端（在读锁内执行，并发安全）
// 回调返回 false 时停止遍历回调不应执行阻塞操作（TrySend 等非阻塞操作可安全调用）
func (r *ShardedRegistry) ForEachUserClient(userID string, fn func(clientID string, client *Client) bool) {
	r.userShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		userClients, ok := data[userID]
		if !ok {
			return
		}
		for clientID, client := range userClients {
			if !fn(clientID, client) {
				return
			}
		}
	})
}

// MoveClientGroup 更新客户端的 GroupID 并迁移观察者索引
// 用于 MoveGroupMember 场景：普通客户仅更新字段；观察者需从旧 group 索引迁移到新 group 索引
func (r *ShardedRegistry) MoveClientGroup(client *Client, newGroupID string) {
	if client.UserType == UserTypeObserver {
		// 观察者：先从旧索引移除（用当前 GroupID），再更新字段，再加入新索引
		r.observerIdx.remove(client)
		client.SetGroupID(newGroupID)
		r.observerIdx.add(client)
	} else {
		// 普通客户：仅更新字段（群组投递走 groupRepo 成员关系，不依赖该索引）
		client.SetGroupID(newGroupID)
	}
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

// ForEachSSEUserClient 遍历指定用户的所有 SSE 客户端（持读锁，零拷贝）
// 回调返回 false 时停止遍历
// 替代 GetSSEUserClients 锁外遍历内部 map 的数据竞争
func (r *ShardedRegistry) ForEachSSEUserClient(userID string, fn func(clientID string, client *Client) bool) {
	r.sseShards.WithShardRLock(userID, func(data map[string]map[string]*Client) {
		userClients, ok := data[userID]
		if !ok {
			return
		}
		for clientID, client := range userClients {
			if !fn(clientID, client) {
				return
			}
		}
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

// GetObserverUserCount 获取观察者用户数（设备去重）- O(1)
func (r *ShardedRegistry) GetObserverUserCount() int {
	if r.observerShards == nil {
		return 0
	}
	return int(r.observerUserCount.Load())
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
			r.observerUserCount.Add(1)
		}
		userClients[client.ID] = client
	})
	// 同步写入三级二级索引（O(1)，消除后续查找的全量扫描）
	r.observerIdx.add(client)
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
			r.observerUserCount.Add(-1)
		}
	})
	// 同步从三级二级索引移除
	r.observerIdx.remove(client)
}

// ============================================================================
// 路由信封投递过滤 helper（避免跨 namespace/group 串扰）
// ============================================================================

// ClientMatchesEnvelope 判断某个 client 是否应该接收携带指定路由信封的消息
// 投递匹配规则（严格但符合"没有就是没有"原则，无默认归一化兜底）：
//  1. namespace 维度：
//     - msgNamespace 非空（业务指定了 ns） → client.Namespace 必须严格相等才匹配
//     - msgNamespace == ""  （全局广播/系统通知/没指定 ns）→ 跳过 ns 过滤，所有 ns client 都匹配
//
// ⚠️ 不做 msgGroupIDs vs client.GroupID 匹配（与 ForEachUserClientFiltered 对称设计）：
//   - msg.GroupIDs 存放的是"业务群组ID"（如 g-broadcast），而 client.GroupID 是"连接级系统组"
//     （如 __default_gp__），两者完全两个维度，强行匹配导致客户端全部被过滤（delivered=0）
//   - 群组成员过滤已由 groupRepo.GetMembers + memberSet 完成（broadcastToUserIDs 路径），
//     全局广播（broadcastToFiltered/ForEachClientFiltered）无需也不应做系统组匹配
//   - msgGroupIDs 参数仅作签名兼容，不参与匹配
//
// 本函数为热路径（每次投递遍历 client 都调用）：零分配、内联友好、短路优先
func ClientMatchesEnvelope(client *models.Client, msgNamespace string, msgGroupIDs []string) bool {
	if client == nil {
		return false
	}
	// namespace 匹配：非空才强制严格相等；空=全局广播不隔离
	if msgNamespace != "" && client.Namespace != msgNamespace {
		return false
	}
	return true
}

// ForEachUserClientFiltered 遍历指定 userID 的所有在线设备，仅对匹配路由信封的 client 调用 fn
// fn 返回 false 时中止遍历（与 ForEachUserClient 语义一致）
// 用于 P2P/群组的定向投递：即使同名 userID 跨 namespace 同时在线，也只投递给路由匹配的设备
// ForEachUserClientFiltered 遍历指定 userID 的所有在线设备，仅对匹配 namespace 的设备调用 fn
// 设计说明：只做 namespace 隔离（避免同名 userID 跨 ns 串扰），**不对 msgGroupIDs vs client.GroupID 做系统组匹配**
//   - 场景1（P2P 点对点）：调用方已明确指定 userID 为接收者，msg.GroupIDs=nil → 无 group 维度
//   - 场景2（群组消息）：调用方已通过 group_repo.GetMembers() 获取业务群组成员 userIDs，
//     msg.GroupIDs 存放的是"业务群组ID"（如 "g-broadcast"），而 client.GroupID 是"连接级系统组"
//     （如 __default_gp__），两者完全两个维度，强行匹配导致群成员 device 全部被过滤（delivered=0）
//
// namespace 匹配规则（与 ClientMatchesEnvelope 一致，对称设计）：
//   - msgNamespace 非空（业务指定 ns） → 仅投递给同 ns 的 client 设备，避免串扰
//   - msgNamespace == ""（未指定 ns/全局）→ 跳过 ns 过滤，投递给 userID 的所有设备
func (r *ShardedRegistry) ForEachUserClientFiltered(userID, msgNamespace string, msgGroupIDs []string, fn func(clientID string, client *models.Client) bool) {
	r.ForEachUserClient(userID, func(clientID string, client *models.Client) bool {
		// msgNamespace 非空时强制同 ns 匹配（为空=全局不隔离，跳过 ns 检查）
		// msgGroupIDs 仅作签名兼容（不参与系统组 vs 业务群匹配）
		if client == nil {
			return true
		}
		if msgNamespace != "" && client.Namespace != msgNamespace {
			return true // 不匹配，跳过继续
		}
		return fn(clientID, client)
	})
}

// ForEachClientFiltered 遍历注册表所有 client，仅对匹配路由信封的 client 调用 fn
// 用于全局广播等场景：ns1 的广播只投递给 ns1 的所有在线设备
func (r *ShardedRegistry) ForEachClientFiltered(msgNamespace string, msgGroupIDs []string, fn func(clientID string, client *models.Client) bool) {
	r.ForEachClient(func(clientID string, client *models.Client) bool {
		if !ClientMatchesEnvelope(client, msgNamespace, msgGroupIDs) {
			return true
		}
		return fn(clientID, client)
	})
}

func (r *ShardedRegistry) GetObserversForMessage(namespace string, groupIDs ...string) []*Client {
	if r.observerShards == nil {
		return nil
	}
	return r.observerIdx.getForMessage(namespace, groupIDs...)
}

// ============================================================================
// observerIndex 观察者三级二级索引
// 按观察范围分三级，O(1) 查找替代 ForEachObserver 的 O(n) 全量扫描
// ============================================================================

// observerIndex 观察者三级索引
type observerIndex struct {
	mu          sync.RWMutex
	global      map[string]*Client            // clientID → Client（Namespace=="" 全局观察者）
	byNamespace map[string]map[string]*Client // namespace → (clientID → Client)
	byGroup     map[string]map[string]*Client // "namespace:groupID" → (clientID → Client)
}

// newObserverIndex 创建观察者三级索引
func newObserverIndex() observerIndex {
	return observerIndex{
		global:      make(map[string]*Client),
		byNamespace: make(map[string]map[string]*Client),
		byGroup:     make(map[string]map[string]*Client),
	}
}

// groupIndexKey 拼接命名空间+群组的索引键
func groupIndexKey(namespace, groupID string) string {
	return namespace + ":" + groupID
}

// add 添加观察者到对应级别的索引
func (idx *observerIndex) add(client *Client) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if client.Namespace == "" {
		// 全局观察者：观察所有命名空间
		idx.global[client.ID] = client
	} else if gid := client.GetGroupIDRaw(); gid != "" {
		// 群组级观察者：观察指定命名空间+群组
		key := groupIndexKey(client.Namespace, gid)
		group, ok := idx.byGroup[key]
		if !ok {
			group = make(map[string]*Client)
			idx.byGroup[key] = group
		}
		group[client.ID] = client
	} else {
		// 命名空间级观察者：观察指定命名空间（所有群组）
		ns, ok := idx.byNamespace[client.Namespace]
		if !ok {
			ns = make(map[string]*Client)
			idx.byNamespace[client.Namespace] = ns
		}
		ns[client.ID] = client
	}
}

// remove 从索引中移除观察者
func (idx *observerIndex) remove(client *Client) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if client.Namespace == "" {
		delete(idx.global, client.ID)
	} else if gid := client.GetGroupIDRaw(); gid != "" {
		key := groupIndexKey(client.Namespace, gid)
		if group, ok := idx.byGroup[key]; ok {
			delete(group, client.ID)
			if len(group) == 0 {
				delete(idx.byGroup, key)
			}
		}
	} else {
		if ns, ok := idx.byNamespace[client.Namespace]; ok {
			delete(ns, client.ID)
			if len(ns) == 0 {
				delete(idx.byNamespace, client.Namespace)
			}
		}
	}
}

// getForMessage 合并三级索引查找观察者（全局 + 命名空间 + 多群组），按 clientID 去重
func (idx *observerIndex) getForMessage(namespace string, groupIDs ...string) []*Client {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// 预估容量：全局 + 命名空间 + 各群组
	estimate := len(idx.global)
	if ns, ok := idx.byNamespace[namespace]; ok {
		estimate += len(ns)
	}
	for _, gid := range groupIDs {
		if gid != "" {
			key := groupIndexKey(namespace, gid)
			if group, ok := idx.byGroup[key]; ok {
				estimate += len(group)
			}
		}
	}

	result := make([]*Client, 0, estimate)
	seen := make(map[string]struct{}, estimate) // 去重

	// 1. 全局观察者
	for _, c := range idx.global {
		if !c.IsClosed() {
			if _, ok := seen[c.ID]; !ok {
				seen[c.ID] = struct{}{}
				result = append(result, c)
			}
		}
	}

	// 2. 命名空间级观察者
	if ns, ok := idx.byNamespace[namespace]; ok {
		for _, c := range ns {
			if !c.IsClosed() {
				if _, ok := seen[c.ID]; !ok {
					seen[c.ID] = struct{}{}
					result = append(result, c)
				}
			}
		}
	}

	// 3. 各群组级观察者（遍历所有 groupIDs，去重）
	for _, gid := range groupIDs {
		if gid == "" {
			continue
		}
		key := groupIndexKey(namespace, gid)
		if group, ok := idx.byGroup[key]; ok {
			for _, c := range group {
				if !c.IsClosed() {
					if _, ok := seen[c.ID]; !ok {
						seen[c.ID] = struct{}{}
						result = append(result, c)
					}
				}
			}
		}
	}

	return result
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
	return int(r.agentUserCount.Load())
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
			r.agentUserCount.Add(1)
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
			r.agentUserCount.Add(-1)
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
		// 同步清空 observerIdx 三级索引，避免内存泄漏
		// （observerIdx 是独立 map，不会被 observerShards.Clear 联动清理）
		r.observerIdx.mu.Lock()
		r.observerIdx.global = make(map[string]*Client)
		r.observerIdx.byNamespace = make(map[string]map[string]*Client)
		r.observerIdx.byGroup = make(map[string]map[string]*Client)
		r.observerIdx.mu.Unlock()
	}
	if r.agentShards != nil {
		r.agentShards.Clear()
	}

	// 清空反向索引
	// 性能：用新实例替换比逐个 Delete 更快（旧 map 由 GC 回收）
	r.clientIDToUserID = sync.Map{}

	r.clientCount.Store(0)
	r.userCount.Store(0)
	r.sseCount.Store(0)
}

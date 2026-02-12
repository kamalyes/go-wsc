/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 18:16:26
 * @FilePath: \go-wsc\repository\online_status_repository.go
 * @Description: 客户端在线状态管理 - 支持 Redis 分布式存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"context"
	"fmt"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/redis/go-redis/v9"
)

// luaBatchSetClientsOnline Lua 脚本：批量设置客户端在线（使用 ZSET 存储过期时间）
//
// KEYS[1] = keyPrefix (用于构建其他 key)
//
// ARGV[1] = ttl (秒)
// ARGV[2] = currentTime (当前时间戳，用于清理过期数据)
// ARGV[3] = clientCount (客户端数量)
// ARGV[4..] = 每个客户端的数据，格式：clientID|userID|nodeID|userType|expireTime|clientData
//
// 返回值: 成功处理的客户端数量
const luaBatchSetClientsOnline = `
local keyPrefix = KEYS[1]
local ttl = tonumber(ARGV[1])
local currentTime = tonumber(ARGV[2])
local clientCount = tonumber(ARGV[3])
local successCount = 0

for i = 1, clientCount do
    local idx = 3 + i
    local data = ARGV[idx]
    
    -- 解析数据: clientID|userID|nodeID|userType|expireTime|clientData
    local sep = string.byte("|")
    local parts = {}
    local lastPos = 1
    
    -- 手动分割字符串（只分割前5个字段）
    for j = 1, 5 do
        local pos = string.find(data, "|", lastPos, true)
        if pos then
            table.insert(parts, string.sub(data, lastPos, pos - 1))
            lastPos = pos + 1
        end
    end
    -- 剩余部分是 clientData
    table.insert(parts, string.sub(data, lastPos))
    
    if #parts == 6 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local expireTime = tonumber(parts[5])
        local clientData = parts[6]
        
        local clientKey = keyPrefix .. "client:" .. clientID
        local userClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local allUsersKey = keyPrefix .. "all_users"
        local typeKey = keyPrefix .. "type:" .. userType
        
        -- 清理该用户的过期客户端（在添加新客户端之前）
        redis.call('ZREMRANGEBYSCORE', userClientsKey, '-inf', currentTime)
        
        -- 存储客户端信息
        redis.call('SETEX', clientKey, ttl, clientData)
        
        -- 添加到集合（全部使用 ZADD 存储过期时间）
        redis.call('ZADD', userClientsKey, expireTime, clientID)
        redis.call('EXPIRE', userClientsKey, ttl)
        redis.call('ZADD', nodeClientsKey, expireTime, clientID)
        redis.call('ZADD', allUsersKey, expireTime, userID)
        redis.call('ZADD', typeKey, expireTime, userID)
        
        successCount = successCount + 1
    end
end

return successCount
`

// luaBatchSetClientsOffline Lua 脚本：批量设置客户端离线（使用 ZSET）
//
// KEYS[1] = keyPrefix (用于构建其他 key)
//
// ARGV[1] = currentTime (当前时间戳，用于清理过期数据)
// ARGV[2] = clientCount (客户端数量)
// ARGV[3..] = 每个客户端的数据，格式：clientID|userID|nodeID|userType
//
// 返回值: 成功处理的客户端数量
const luaBatchSetClientsOffline = `
local keyPrefix = KEYS[1]
local currentTime = tonumber(ARGV[1])
local clientCount = tonumber(ARGV[2])
local successCount = 0

for i = 1, clientCount do
    local idx = 2 + i
    local data = ARGV[idx]
    
    -- 解析数据: clientID|userID|nodeID|userType
    local parts = {}
    for part in string.gmatch(data, "([^|]+)") do
        table.insert(parts, part)
    end
    
    if #parts == 4 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        
        -- 构建 keys
        local clientKey = keyPrefix .. "client:" .. clientID
        local userClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local allUsersKey = keyPrefix .. "all_users"
        local typeKey = keyPrefix .. "type:" .. userType
        
        -- 删除客户端信息
        redis.call('DEL', clientKey)
        
        -- 从集合中移除（ZREM 用于 ZSET）
        redis.call('ZREM', userClientsKey, clientID)
        redis.call('ZREM', nodeClientsKey, clientID)
        
        -- 清理该用户的过期客户端
        redis.call('ZREMRANGEBYSCORE', userClientsKey, '-inf', currentTime)
        
        -- 检查用户是否还有其他未过期的客户端（使用 ZCOUNT 而不是 ZCARD）
        local remainingCount = redis.call('ZCOUNT', userClientsKey, currentTime, '+inf')
        if remainingCount == 0 then
            redis.call('DEL', userClientsKey)
            redis.call('ZREM', allUsersKey, userID)
            redis.call('ZREM', typeKey, userID)
        end
        
        successCount = successCount + 1
    end
end

return successCount
`

// luaCleanupExpiredClients Lua 脚本：清理当前节点的过期客户端（使用 ZSET）
//
// KEYS[1] = keyPrefix (用于构建其他 key)
// KEYS[2] = nodeClientsKey (当前节点的客户端集合)
//
// ARGV[1] = nodeID (当前节点ID)
// ARGV[2] = currentTime (当前时间戳，用于清理 ZSET 中的过期数据)
//
// 返回值: 清理的客户端数量
const luaCleanupExpiredClients = `
local keyPrefix = KEYS[1]
local nodeClientsKey = KEYS[2]
local nodeID = ARGV[1]
local currentTime = tonumber(ARGV[2])

-- 清理 node_clients ZSET 中的过期数据
local cleaned = redis.call('ZREMRANGEBYSCORE', nodeClientsKey, '-inf', currentTime)

-- 清理 all_users 和 type ZSET 中的过期数据
local allUsersKey = keyPrefix .. "all_users"
redis.call('ZREMRANGEBYSCORE', allUsersKey, '-inf', currentTime)

-- 清理所有 type ZSET（这里需要知道所有可能的 userType）
-- 简化处理：只清理常见的两种类型
local typeCustomerKey = keyPrefix .. "type:customer"
local typeAgentKey = keyPrefix .. "type:agent"
redis.call('ZREMRANGEBYSCORE', typeCustomerKey, '-inf', currentTime)
redis.call('ZREMRANGEBYSCORE', typeAgentKey, '-inf', currentTime)

return cleaned
`

// OnlineStatusRepository 在线状态仓库接口
type OnlineStatusRepository interface {
	// ========== 客户端连接管理 ==========

	// SetClientOnline 设置客户端在线（支持多设备）
	SetClientOnline(ctx context.Context, client *Client) error

	// SetClientOffline 设置指定客户端离线
	SetClientOffline(ctx context.Context, client *Client) error

	// SetOffline 设置用户所有客户端离线
	SetOffline(ctx context.Context, userID string) error

	// GetClient 获取客户端信息
	GetClient(ctx context.Context, clientID string) (*Client, error)

	// GetUserClients 获取用户的所有在线客户端
	GetUserClients(ctx context.Context, userID string) ([]*Client, error)

	// UpdateClientHeartbeat 更新客户端心跳
	UpdateClientHeartbeat(ctx context.Context, clientID string) error

	// ========== 用户在线状态查询 ==========

	// IsUserOnline 检查用户是否在线（任意设备）
	IsUserOnline(ctx context.Context, userID string) (bool, error)

	// GetAllOnlineUsers 获取所有在线用户ID列表
	GetAllOnlineUsers(ctx context.Context) ([]string, error)

	// GetOnlineCount 获取在线用户总数
	GetOnlineCount(ctx context.Context) (int64, error)

	// GetOnlineUsersByType 根据用户类型获取在线用户
	GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error)

	// ========== 分布式节点查询 ==========

	// GetUserNodes 获取用户所在的所有节点（支持多设备）
	GetUserNodes(ctx context.Context, userID string) ([]string, error)

	// GetNodeClients 获取节点的所有在线客户端
	GetNodeClients(ctx context.Context, nodeID string) ([]*Client, error)

	// GetNodeUsers 获取节点的所有在线用户ID
	GetNodeUsers(ctx context.Context, nodeID string) ([]string, error)

	// ========== 批量操作 ==========

	// BatchSetClientsOnline 批量设置客户端在线
	BatchSetClientsOnline(ctx context.Context, clients []*Client) error

	// BatchSetClientsOffline 批量设置客户端离线
	BatchSetClientsOffline(ctx context.Context, clientIDs []string) error

	// ========== 维护清理 ==========

	// CleanupExpired 清理当前节点的过期客户端
	CleanupExpired(ctx context.Context, nodeID string) (int64, error)
}

// RedisOnlineStatusRepository Redis 实现
type RedisOnlineStatusRepository struct {
	client             *redis.Client
	keyPrefix          string        // key 前缀
	ttl                time.Duration // 过期时间
	enableCompression  bool          // 是否启用压缩
	compressionMinSize int           // 压缩阈值（字节）
}

// NewRedisOnlineStatusRepository 创建 Redis 在线状态仓库
func NewRedisOnlineStatusRepository(client *redis.Client, config *wscconfig.OnlineStatus) OnlineStatusRepository {
	return &RedisOnlineStatusRepository{
		client:             client,
		keyPrefix:          mathx.IfNotEmpty(config.KeyPrefix, DefaultOnlineKeyPrefix),
		ttl:                config.TTL,
		enableCompression:  config.EnableCompression,
		compressionMinSize: mathx.IfNotZero(config.CompressionMinSize, 512),
	}
}

// ============================================================================
// Redis Key 生成方法
// ============================================================================

// GetClientKey 获取客户端详细信息的 key
func (r *RedisOnlineStatusRepository) GetClientKey(clientID string) string {
	return fmt.Sprintf("%sclient:%s", r.keyPrefix, clientID)
}

// GetUserClientsKey 获取用户客户端集合的 key
func (r *RedisOnlineStatusRepository) GetUserClientsKey(userID string) string {
	return fmt.Sprintf("%suser_clients:%s", r.keyPrefix, userID)
}

// GetNodeClientsKey 获取节点客户端集合的 key
func (r *RedisOnlineStatusRepository) GetNodeClientsKey(nodeID string) string {
	return fmt.Sprintf("%snode_clients:%s", r.keyPrefix, nodeID)
}

// GetUserTypeSetKey 获取用户类型集合的 key
func (r *RedisOnlineStatusRepository) GetUserTypeSetKey(userType models.UserType) string {
	return fmt.Sprintf("%stype:%s", r.keyPrefix, userType)
}

// GetAllUsersSetKey 获取所有在线用户集合的 key
func (r *RedisOnlineStatusRepository) GetAllUsersSetKey() string {
	return fmt.Sprintf("%sall_users", r.keyPrefix)
}

// ============================================================================
// 客户端连接管理
// ============================================================================

// SetClientOnline 设置客户端在线（调用批量方法）
func (r *RedisOnlineStatusRepository) SetClientOnline(ctx context.Context, client *Client) error {
	return r.BatchSetClientsOnline(ctx, []*Client{client})
}

// SetClientOffline 设置指定客户端离线（调用批量方法）
func (r *RedisOnlineStatusRepository) SetClientOffline(ctx context.Context, client *Client) error {
	if client == nil {
		return errorx.WrapError("client cannot be nil")
	}

	currentTime := time.Now().Unix()

	// 直接使用提供的客户端信息构建参数，避免从 Redis 查询
	batchData := fmt.Sprintf("%s|%s|%s|%s",
		client.ID,
		client.UserID,
		client.NodeID,
		string(client.UserType),
	)

	args := []any{currentTime, 1, batchData}

	// 使用 Lua 脚本删除
	keys := []string{r.keyPrefix}
	_, err := r.client.Eval(ctx, luaBatchSetClientsOffline, keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute lua script", err)
	}

	return nil
}

// SetOffline 设置用户所有客户端离线
func (r *RedisOnlineStatusRepository) SetOffline(ctx context.Context, userID string) error {
	if userID == "" {
		return errorx.WrapError("userID cannot be empty")
	}

	// 获取用户所有客户端ID（使用 ZRANGE 获取 ZSET 中的所有成员）
	clientIDs, err := r.client.ZRange(ctx, r.GetUserClientsKey(userID), 0, -1).Result()
	if err != nil {
		return err
	}

	if len(clientIDs) == 0 {
		return nil
	}

	// 批量下线
	return r.BatchSetClientsOffline(ctx, clientIDs)
}

// GetClient 获取客户端信息
func (r *RedisOnlineStatusRepository) GetClient(ctx context.Context, clientID string) (*Client, error) {
	data, err := r.client.Get(ctx, r.GetClientKey(clientID)).Result()
	if err != nil {
		return nil, err
	}

	return zipx.ZlibSmartDecompressObject[*Client]([]byte(data))
}

// GetUserClients 获取用户的所有在线客户端
func (r *RedisOnlineStatusRepository) GetUserClients(ctx context.Context, userID string) ([]*Client, error) {
	clientIDs, err := r.client.ZRange(ctx, r.GetUserClientsKey(userID), 0, -1).Result()
	if err != nil {
		return nil, err
	}

	if len(clientIDs) == 0 {
		return nil, ErrUserNotFound
	}

	// 批量获取客户端信息
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(clientIDs))
	for i, clientID := range clientIDs {
		cmds[i] = pipe.Get(ctx, r.GetClientKey(clientID))
	}

	_, err = pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, err
	}

	clients := make([]*Client, 0, len(clientIDs))
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}

		var client Client
		if err := json.Unmarshal([]byte(data), &client); err != nil {
			continue
		}
		clients = append(clients, &client)
	}

	return clients, nil
}

// UpdateClientHeartbeat 更新客户端心跳
func (r *RedisOnlineStatusRepository) UpdateClientHeartbeat(ctx context.Context, clientID string) error {
	client, err := r.GetClient(ctx, clientID)
	if err != nil {
		// redis.Nil 表示客户端不存在（可能已过期或被清理），这是正常情况
		if err == redis.Nil {
			return nil
		}
		return err
	}

	now := time.Now()
	client.LastHeartbeat = now
	client.LastSeen = now

	return r.SetClientOnline(ctx, client)
}

// ============================================================================
// 用户在线状态查询
// ============================================================================

// IsUserOnline 检查用户是否在线（任意设备）
func (r *RedisOnlineStatusRepository) IsUserOnline(ctx context.Context, userID string) (bool, error) {
	// 使用 ZCOUNT 统计未过期的客户端（score > 当前时间）
	currentTime := time.Now().Unix()
	count, err := r.client.ZCount(ctx, r.GetUserClientsKey(userID), fmt.Sprintf("%d", currentTime), "+inf").Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetAllOnlineUsers 获取所有在线用户ID列表（使用 ZSET，自动过滤过期数据）
func (r *RedisOnlineStatusRepository) GetAllOnlineUsers(ctx context.Context) ([]string, error) {
	// 使用 ZRangeArgs 只获取未过期的用户（score > 当前时间）
	currentTime := time.Now().Unix()
	return r.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     r.GetAllUsersSetKey(),
		ByScore: true,
		Start:   fmt.Sprintf("%d", currentTime),
		Stop:    "+inf",
	}).Result()
}

// GetOnlineCount 获取在线用户总数（使用 ZCOUNT 统计未过期的用户）
func (r *RedisOnlineStatusRepository) GetOnlineCount(ctx context.Context) (int64, error) {
	currentTime := time.Now().Unix()
	return r.client.ZCount(ctx, r.GetAllUsersSetKey(), fmt.Sprintf("%d", currentTime), "+inf").Result()
}

// GetOnlineUsersByType 根据用户类型获取在线用户（使用 ZSET，自动过滤过期数据）
func (r *RedisOnlineStatusRepository) GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error) {
	// 使用 ZRangeArgs 只获取未过期的用户（score > 当前时间）
	currentTime := time.Now().Unix()
	return r.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     r.GetUserTypeSetKey(userType),
		ByScore: true,
		Start:   fmt.Sprintf("%d", currentTime),
		Stop:    "+inf",
	}).Result()
}

// ============================================================================
// 分布式节点查询
// ============================================================================

// GetUserNodes 获取用户所在的所有节点（支持多设备）
func (r *RedisOnlineStatusRepository) GetUserNodes(ctx context.Context, userID string) ([]string, error) {
	clients, err := r.GetUserClients(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 去重节点ID
	nodeSet := make(map[string]struct{})
	for _, client := range clients {
		if client.NodeID != "" {
			nodeSet[client.NodeID] = struct{}{}
		}
	}

	nodes := make([]string, 0, len(nodeSet))
	for nodeID := range nodeSet {
		nodes = append(nodes, nodeID)
	}

	return nodes, nil
}

// GetNodeClients 获取节点的所有在线客户端
func (r *RedisOnlineStatusRepository) GetNodeClients(ctx context.Context, nodeID string) ([]*Client, error) {
	// 使用 ZRANGE 获取所有客户端（ZSET 存储）
	clientIDs, err := r.client.ZRange(ctx, r.GetNodeClientsKey(nodeID), 0, -1).Result()
	if err != nil {
		return nil, err
	}

	if len(clientIDs) == 0 {
		return []*Client{}, nil
	}

	// 批量获取客户端信息
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(clientIDs))
	for i, clientID := range clientIDs {
		cmds[i] = pipe.Get(ctx, r.GetClientKey(clientID))
	}

	_, err = pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, err
	}

	// 解析客户端信息
	clients := make([]*Client, 0, len(clientIDs))
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}

		var client Client
		if err := json.Unmarshal([]byte(data), &client); err != nil {
			continue
		}
		clients = append(clients, &client)
	}

	return clients, nil
}

// GetNodeUsers 获取节点的所有在线用户ID
func (r *RedisOnlineStatusRepository) GetNodeUsers(ctx context.Context, nodeID string) ([]string, error) {
	clients, err := r.GetNodeClients(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	// 去重用户ID
	userSet := make(map[string]struct{})
	for _, client := range clients {
		userSet[client.UserID] = struct{}{}
	}

	users := make([]string, 0, len(userSet))
	for userID := range userSet {
		users = append(users, userID)
	}

	return users, nil
}

// ============================================================================
// 批量操作（使用 Lua 脚本）
// ============================================================================

// BatchSetClientsOnline 批量设置客户端在线（使用 Lua 脚本）
func (r *RedisOnlineStatusRepository) BatchSetClientsOnline(ctx context.Context, clients []*Client) error {
	if len(clients) == 0 {
		return nil
	}

	now := time.Now()
	currentTime := now.Unix()

	// 准备批量数据
	validClients := make([]string, 0, len(clients))

	for _, client := range clients {
		if client.ID == "" || client.UserID == "" || client.NodeID == "" {
			continue
		}

		data, err := json.Marshal(client)
		if err != nil {
			continue
		}

		// 如果启用压缩且数据大小超过阈值，则压缩
		clientData := data
		if r.enableCompression && len(data) >= r.compressionMinSize {
			compressed, err := zipx.ZlibCompressWithPrefix(data)
			if err == nil && len(compressed) < len(data) {
				clientData = compressed
			}
		}

		// 计算过期时间戳（当前时间 + TTL）
		expireTime := now.Add(r.ttl).Unix()

		// 格式：clientID|userID|nodeID|userType|expireTime|clientData
		batchData := fmt.Sprintf("%s|%s|%s|%s|%d|%s",
			client.ID,
			client.UserID,
			client.NodeID,
			string(client.UserType),
			expireTime,
			string(clientData),
		)
		validClients = append(validClients, batchData)
	}

	if len(validClients) == 0 {
		return nil
	}

	// 构建参数：ttl, currentTime, clientCount, ...clientData
	args := make([]any, 0, 3+len(validClients))
	args = append(args, int(r.ttl.Seconds()))
	args = append(args, currentTime)
	args = append(args, len(validClients))
	for _, clientData := range validClients {
		args = append(args, clientData)
	}

	// 使用 Lua 脚本批量设置
	keys := []string{r.keyPrefix}
	_, err := r.client.Eval(ctx, luaBatchSetClientsOnline, keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute batch lua script", err)
	}

	return nil
}

// BatchSetClientsOffline 批量设置客户端离线（使用 Lua 脚本）
func (r *RedisOnlineStatusRepository) BatchSetClientsOffline(ctx context.Context, clientIDs []string) error {
	if len(clientIDs) == 0 {
		return nil
	}

	currentTime := time.Now().Unix()

	// 先批量获取客户端信息
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(clientIDs))
	for i, clientID := range clientIDs {
		cmds[i] = pipe.Get(ctx, r.GetClientKey(clientID))
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return err
	}

	// 准备批量数据：currentTime, clientCount, ...clientData
	args := []any{currentTime, 0} // 先占位，后面更新数量

	validCount := 0
	for i, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}

		var client Client
		if err := json.Unmarshal([]byte(data), &client); err != nil {
			continue
		}

		// 格式：clientID|userID|nodeID|userType
		batchData := fmt.Sprintf("%s|%s|%s|%s",
			clientIDs[i],
			client.UserID,
			client.NodeID,
			string(client.UserType),
		)
		args = append(args, batchData)
		validCount++
	}

	if validCount == 0 {
		return nil
	}

	// 更新客户端数量
	args[1] = validCount

	// 使用 Lua 脚本批量删除
	keys := []string{r.keyPrefix}
	_, err = r.client.Eval(ctx, luaBatchSetClientsOffline, keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute batch lua script", err)
	}

	return nil
}

// BatchSetClientsOfflineWithInfo 批量设置客户端离线（使用已知的客户端信息）
// 当客户端信息已知时使用此方法，避免从 Redis 查询，确保即使客户端key已被删除也能清理ZSET
func (r *RedisOnlineStatusRepository) BatchSetClientsOfflineWithInfo(ctx context.Context, clients []*Client) error {
	if len(clients) == 0 {
		return nil
	}

	currentTime := time.Now().Unix()
	args := []any{currentTime, len(clients)}

	for _, client := range clients {
		batchData := fmt.Sprintf("%s|%s|%s|%s",
			client.ID,
			client.UserID,
			client.NodeID,
			string(client.UserType),
		)
		args = append(args, batchData)
	}

	// 使用 Lua 脚本批量删除
	keys := []string{r.keyPrefix}
	_, err := r.client.Eval(ctx, luaBatchSetClientsOffline, keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute batch lua script", err)
	}

	return nil
}

// ============================================================================
// 维护清理
// ============================================================================

// CleanupExpired 清理当前节点的过期客户端（使用 ZSET 自动清理过期数据）
//
// 清理策略：
// 1. 清理 node_clients 中不存在的客户端
// 2. 使用 ZREMRANGEBYSCORE 清理 all_users 和 type ZSET 中的过期数据
//
// 注意：
// - user_clients 使用 ZSET 存储，设置了 TTL 会自动过期
// - all_users 和 type 使用 ZSET，通过 score（过期时间戳）清理
func (r *RedisOnlineStatusRepository) CleanupExpired(ctx context.Context, nodeID string) (int64, error) {
	if nodeID == "" {
		return 0, fmt.Errorf("nodeID cannot be empty")
	}

	nodeClientsKey := r.GetNodeClientsKey(nodeID)
	currentTime := time.Now().Unix()

	result, err := r.client.Eval(ctx, luaCleanupExpiredClients, []string{r.keyPrefix, nodeClientsKey}, nodeID, currentTime).Result()
	if err != nil {
		return 0, fmt.Errorf("执行清理脚本失败: %w", err)
	}

	cleaned, ok := result.(int64)
	if !ok {
		return 0, fmt.Errorf("脚本返回值类型错误")
	}

	return cleaned, nil
}

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
	"github.com/kamalyes/go-wsc/models"
	"github.com/redis/go-redis/v9"
)

// luaBatchSetClientsOnline Lua 脚本：批量设置客户端在线
//
// KEYS[1] = keyPrefix (用于构建其他 key)
//
// ARGV[1] = ttl (秒)
// ARGV[2] = clientCount (客户端数量)
// ARGV[3..] = 每个客户端的数据，格式：clientID|userID|nodeID|userType|clientData
//
// 返回值: 成功处理的客户端数量
const luaBatchSetClientsOnline = `
local keyPrefix = KEYS[1]
local ttl = tonumber(ARGV[1])
local clientCount = tonumber(ARGV[2])
local successCount = 0

for i = 1, clientCount do
    local idx = 2 + i
    local data = ARGV[idx]
    
    -- 解析数据: clientID|userID|nodeID|userType|clientData
    local sep = string.byte("|")
    local parts = {}
    local lastPos = 1
    
    -- 手动分割字符串（只分割前4个字段）
    for j = 1, 4 do
        local pos = string.find(data, "|", lastPos, true)
        if pos then
            table.insert(parts, string.sub(data, lastPos, pos - 1))
            lastPos = pos + 1
        end
    end
    -- 剩余部分是 clientData
    table.insert(parts, string.sub(data, lastPos))
    
    if #parts == 5 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local clientData = parts[5]
        
        -- 构建 keys
        local clientKey = keyPrefix .. "client:" .. clientID
        local userClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local allUsersKey = keyPrefix .. "all_users"
        local typeKey = keyPrefix .. "type:" .. userType
        
        -- 存储客户端信息
        redis.call('SETEX', clientKey, ttl, clientData)
        
        -- 添加到集合
        redis.call('SADD', userClientsKey, clientID)
        redis.call('EXPIRE', userClientsKey, ttl)
        redis.call('SADD', nodeClientsKey, clientID)
        redis.call('SADD', allUsersKey, userID)
        redis.call('SADD', typeKey, userID)
        
        successCount = successCount + 1
    end
end

return successCount
`

// luaBatchSetClientsOffline Lua 脚本：批量设置客户端离线
//
// KEYS[1] = keyPrefix (用于构建其他 key)
//
// ARGV[1] = clientCount (客户端数量)
// ARGV[2..] = 每个客户端的数据，格式：clientID|userID|nodeID|userType
//
// 返回值: 成功处理的客户端数量
const luaBatchSetClientsOffline = `
local keyPrefix = KEYS[1]
local clientCount = tonumber(ARGV[1])
local successCount = 0

for i = 1, clientCount do
    local idx = 1 + i
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
        
        -- 从集合中移除
        redis.call('SREM', userClientsKey, clientID)
        redis.call('SREM', nodeClientsKey, clientID)
        
        -- 检查用户是否还有其他客户端
        local remainingCount = redis.call('SCARD', userClientsKey)
        if remainingCount == 0 then
            redis.call('SREM', allUsersKey, userID)
            redis.call('SREM', typeKey, userID)
        end
        
        successCount = successCount + 1
    end
end

return successCount
`

// OnlineStatusRepository 在线状态仓库接口
type OnlineStatusRepository interface {
	// ========== 客户端连接管理 ==========

	// SetClientOnline 设置客户端在线（支持多设备）
	SetClientOnline(ctx context.Context, client *Client) error

	// SetClientOffline 设置指定客户端离线
	SetClientOffline(ctx context.Context, clientID string) error

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

	// CleanupExpired 清理过期的在线状态
	CleanupExpired(ctx context.Context) (int64, error)
}

// RedisOnlineStatusRepository Redis 实现
type RedisOnlineStatusRepository struct {
	client    *redis.Client
	keyPrefix string        // key 前缀
	ttl       time.Duration // 过期时间
}

// NewRedisOnlineStatusRepository 创建 Redis 在线状态仓库
func NewRedisOnlineStatusRepository(client *redis.Client, config *wscconfig.OnlineStatus) OnlineStatusRepository {
	return &RedisOnlineStatusRepository{
		client:    client,
		keyPrefix: mathx.IF(config.KeyPrefix == "", DefaultOnlineKeyPrefix, config.KeyPrefix),
		ttl:       mathx.IF(config.TTL == 0, 5*time.Minute, config.TTL),
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
func (r *RedisOnlineStatusRepository) SetClientOffline(ctx context.Context, clientID string) error {
	return r.BatchSetClientsOffline(ctx, []string{clientID})
}

// SetOffline 设置用户所有客户端离线
func (r *RedisOnlineStatusRepository) SetOffline(ctx context.Context, userID string) error {
	if userID == "" {
		return errorx.WrapError("userID cannot be empty")
	}

	// 获取用户所有客户端ID
	clientIDs, err := r.client.SMembers(ctx, r.GetUserClientsKey(userID)).Result()
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

	var client Client
	if err := json.Unmarshal([]byte(data), &client); err != nil {
		return nil, errorx.WrapError("failed to unmarshal client info", err)
	}

	return &client, nil
}

// GetUserClients 获取用户的所有在线客户端
func (r *RedisOnlineStatusRepository) GetUserClients(ctx context.Context, userID string) ([]*Client, error) {
	clientIDs, err := r.client.SMembers(ctx, r.GetUserClientsKey(userID)).Result()
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

// UpdateClientHeartbeat 更新客户端心跳
func (r *RedisOnlineStatusRepository) UpdateClientHeartbeat(ctx context.Context, clientID string) error {
	client, err := r.GetClient(ctx, clientID)
	if err != nil {
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
	count, err := r.client.SCard(ctx, r.GetUserClientsKey(userID)).Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetAllOnlineUsers 获取所有在线用户ID列表
func (r *RedisOnlineStatusRepository) GetAllOnlineUsers(ctx context.Context) ([]string, error) {
	return r.client.SMembers(ctx, r.GetAllUsersSetKey()).Result()
}

// GetOnlineCount 获取在线用户总数
func (r *RedisOnlineStatusRepository) GetOnlineCount(ctx context.Context) (int64, error) {
	return r.client.SCard(ctx, r.GetAllUsersSetKey()).Result()
}

// GetOnlineUsersByType 根据用户类型获取在线用户
func (r *RedisOnlineStatusRepository) GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error) {
	return r.client.SMembers(ctx, r.GetUserTypeSetKey(userType)).Result()
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
	clientIDs, err := r.client.SMembers(ctx, r.GetNodeClientsKey(nodeID)).Result()
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

	// 准备批量数据
	args := []any{
		int(r.ttl.Seconds()),
		len(clients),
	}

	for _, client := range clients {
		if client.ID == "" || client.UserID == "" || client.NodeID == "" {
			continue
		}

		data, err := json.Marshal(client)
		if err != nil {
			continue
		}

		// 格式：clientID|userID|nodeID|userType|clientData
		batchData := fmt.Sprintf("%s|%s|%s|%s|%s",
			client.ID,
			client.UserID,
			client.NodeID,
			string(client.UserType),
			string(data),
		)
		args = append(args, batchData)
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

	// 准备批量数据
	args := []any{0} // 先占位，后面更新数量

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
	args[0] = validCount

	// 使用 Lua 脚本批量删除
	keys := []string{r.keyPrefix}
	_, err = r.client.Eval(ctx, luaBatchSetClientsOffline, keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute batch lua script", err)
	}

	return nil
}

// ============================================================================
// 维护清理
// ============================================================================

// CleanupExpired 清理过期的在线状态
func (r *RedisOnlineStatusRepository) CleanupExpired(ctx context.Context) (int64, error) {
	var cleaned int64

	allUsers, err := r.GetAllOnlineUsers(ctx)
	if err != nil {
		return 0, err
	}

	for _, userID := range allUsers {
		clientIDs, err := r.client.SMembers(ctx, r.GetUserClientsKey(userID)).Result()
		if err != nil {
			continue
		}

		// 检查每个客户端是否存在
		pipe := r.client.Pipeline()
		existsCmds := make([]*redis.IntCmd, len(clientIDs))
		for i, clientID := range clientIDs {
			existsCmds[i] = pipe.Exists(ctx, r.GetClientKey(clientID))
		}

		_, err = pipe.Exec(ctx)
		if err != nil && err != redis.Nil {
			continue
		}

		// 收集已过期的客户端
		expiredClientIDs := make([]string, 0)
		for i, cmd := range existsCmds {
			exists, _ := cmd.Result()
			if exists == 0 {
				expiredClientIDs = append(expiredClientIDs, clientIDs[i])
			}
		}

		// 清理过期客户端
		if len(expiredClientIDs) > 0 {
			_ = r.BatchSetClientsOffline(ctx, expiredClientIDs)
			cleaned += int64(len(expiredClientIDs))
		}
	}

	return cleaned, nil
}

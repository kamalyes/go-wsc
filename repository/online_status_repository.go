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
	"strings"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/redis/go-redis/v9"
)

// OnlineStatusRepository 在线状态仓库接口
type OnlineStatusRepository interface {
	// SetOnline 设置用户在线（直接传入 Client 指针）
	SetOnline(ctx context.Context, client *Client) error

	// SetOffline 设置用户离线
	SetOffline(ctx context.Context, userID string) error

	// IsOnline 检查用户是否在线
	IsOnline(ctx context.Context, userID string) (bool, error)

	// GetOnlineInfo 获取在线客户端信息
	GetOnlineInfo(ctx context.Context, userID string) (*Client, error)

	// GetAllOnlineUsers 获取所有在线用户ID列表
	GetAllOnlineUsers(ctx context.Context) ([]string, error)

	// GetOnlineUsersByNode 获取指定节点的在线用户
	GetOnlineUsersByNode(ctx context.Context, nodeID string) ([]string, error)

	// GetOnlineCount 获取在线用户总数
	GetOnlineCount(ctx context.Context) (int64, error)

	// UpdateHeartbeat 更新心跳时间
	UpdateHeartbeat(ctx context.Context, userID string) error

	// BatchSetOnline 批量设置用户在线
	BatchSetOnline(ctx context.Context, clients map[string]*Client) error

	// BatchSetOffline 批量设置用户离线
	BatchSetOffline(ctx context.Context, userIDs []string) error

	// GetOnlineUsersByType 根据用户类型获取在线用户
	GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error)

	// CleanupExpired 清理过期的在线状态（可选，Redis 会自动过期）
	CleanupExpired(ctx context.Context) (int64, error)

	// ========== 分布式节点相关方法 ==========

	// SetUserNode 设置用户所在节点
	SetUserNode(ctx context.Context, userID string, nodeID string) error

	// GetUserNode 获取用户所在节点
	GetUserNode(ctx context.Context, userID string) (string, error)

	// GetNodeUsers 获取节点的所有在线用户
	GetNodeUsers(ctx context.Context, nodeID string) ([]string, error)
}

// RedisOnlineStatusRepository Redis 实现
type RedisOnlineStatusRepository struct {
	client    *redis.Client
	keyPrefix string        // key 前缀
	ttl       time.Duration // 过期时间
}

// NewRedisOnlineStatusRepository 创建 Redis 在线状态仓库
// 参数:
//   - client: Redis 客户端 (github.com/redis/go-redis/v9)
//   - config: 在线状态配置对象
func NewRedisOnlineStatusRepository(client *redis.Client, config *wscconfig.OnlineStatus) OnlineStatusRepository {
	return &RedisOnlineStatusRepository{
		client:    client,
		keyPrefix: mathx.IF(config.KeyPrefix == "", DefaultOnlineKeyPrefix, config.KeyPrefix),
		ttl:       mathx.IF(config.TTL == 0, 5*time.Minute, config.TTL),
	}
}

// GetUserKey 获取用户在线状态的 key
func (r *RedisOnlineStatusRepository) GetUserKey(userID string) string {
	return fmt.Sprintf("%suser:%s", r.keyPrefix, userID)
}

// GetNodeSetKey 获取节点在线用户集合的 key
func (r *RedisOnlineStatusRepository) GetNodeSetKey(nodeID string) string {
	return fmt.Sprintf("%snode:%s", r.keyPrefix, nodeID)
}

// GetUserTypeSetKey 获取用户类型在线用户集合的 key
func (r *RedisOnlineStatusRepository) GetUserTypeSetKey(userType models.UserType) string {
	return fmt.Sprintf("%s%s", r.keyPrefix, userType)
}

// GetAllUsersSetKey 获取所有在线用户集合的 key
func (r *RedisOnlineStatusRepository) GetAllUsersSetKey() string {
	return fmt.Sprintf("%sall", r.keyPrefix)
}

// SetOnline 设置用户在线
func (r *RedisOnlineStatusRepository) SetOnline(ctx context.Context, client *Client) error {
	// 序列化客户端信息
	data, err := json.Marshal(client)
	if err != nil {
		return errorx.WrapError("failed to marshal client info", err)
	}

	// 使用 pipeline 批量执行
	pipe := r.client.Pipeline()

	// 1. 设置用户在线信息
	pipe.Set(ctx, r.GetUserKey(client.UserID), data, r.ttl)

	// 2. 添加到全局在线用户集合
	pipe.SAdd(ctx, r.GetAllUsersSetKey(), client.UserID)

	// 3. 添加到节点在线用户集合
	if client.NodeID != "" {
		pipe.SAdd(ctx, r.GetNodeSetKey(client.NodeID), client.UserID)
	}

	// 4. 添加到用户类型集合
	pipe.SAdd(ctx, r.GetUserTypeSetKey(client.UserType), client.UserID)

	_, err = pipe.Exec(ctx)
	return err
}

// SetOffline 设置用户离线
func (r *RedisOnlineStatusRepository) SetOffline(ctx context.Context, userID string) error {
	// 先获取用户信息，以便从相关集合中移除
	info, err := r.GetOnlineInfo(ctx, userID)
	if err != nil && err != redis.Nil {
		// 如果获取失败但不是 key 不存在，继续删除
	}

	pipe := r.client.Pipeline()

	// 1. 删除用户在线信息
	pipe.Del(ctx, r.GetUserKey(userID))

	// 2. 从全局在线用户集合中移除
	pipe.SRem(ctx, r.GetAllUsersSetKey(), userID)

	// 3. 从节点集合中移除
	if info != nil && info.NodeID != "" {
		pipe.SRem(ctx, r.GetNodeSetKey(info.NodeID), userID)
	}

	// 4. 从用户类型集合中移除
	if info != nil {
		pipe.SRem(ctx, r.GetUserTypeSetKey(info.UserType), userID)
	}

	_, err = pipe.Exec(ctx)
	return err
}

// IsOnline 检查用户是否在线
func (r *RedisOnlineStatusRepository) IsOnline(ctx context.Context, userID string) (bool, error) {
	exists, err := r.client.Exists(ctx, r.GetUserKey(userID)).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

// GetOnlineInfo 获取在线客户端信息
func (r *RedisOnlineStatusRepository) GetOnlineInfo(ctx context.Context, userID string) (*Client, error) {
	data, err := r.client.Get(ctx, r.GetUserKey(userID)).Result()
	if err != nil {
		return nil, err
	}

	var client Client
	if err := json.Unmarshal([]byte(data), &client); err != nil {
		return nil, errorx.WrapError("failed to unmarshal client info", err)
	}

	return &client, nil
}

// GetAllOnlineUsers 获取所有在线用户ID列表
func (r *RedisOnlineStatusRepository) GetAllOnlineUsers(ctx context.Context) ([]string, error) {
	return r.client.SMembers(ctx, r.GetAllUsersSetKey()).Result()
}

// GetOnlineUsersByNode 获取指定节点的在线用户
func (r *RedisOnlineStatusRepository) GetOnlineUsersByNode(ctx context.Context, nodeID string) ([]string, error) {
	return r.client.SMembers(ctx, r.GetNodeSetKey(nodeID)).Result()
}

// GetOnlineCount 获取在线用户总数
func (r *RedisOnlineStatusRepository) GetOnlineCount(ctx context.Context) (int64, error) {
	return r.client.SCard(ctx, r.GetAllUsersSetKey()).Result()
}

// UpdateHeartbeat 更新心跳时间
func (r *RedisOnlineStatusRepository) UpdateHeartbeat(ctx context.Context, userID string) error {
	// 获取当前信息
	client, err := r.GetOnlineInfo(ctx, userID)
	if err != nil {
		return err
	}

	// 更新心跳时间
	client.LastHeartbeat = time.Now()
	client.LastSeen = time.Now()

	// 重新设置（会刷新 TTL）
	return r.SetOnline(ctx, client)
}

// BatchSetOnline 批量设置用户在线
func (r *RedisOnlineStatusRepository) BatchSetOnline(ctx context.Context, clients map[string]*Client) error {
	pipe := r.client.Pipeline()

	for userID, client := range clients {
		data, err := json.Marshal(client)
		if err != nil {
			return errorx.WrapError(fmt.Sprintf("failed to marshal client info for user %s", userID), err)
		}

		pipe.Set(ctx, r.GetUserKey(userID), data, r.ttl)
		pipe.SAdd(ctx, r.GetAllUsersSetKey(), userID)

		if client.NodeID != "" {
			pipe.SAdd(ctx, r.GetNodeSetKey(client.NodeID), userID)
		}

		pipe.SAdd(ctx, r.GetUserTypeSetKey(client.UserType), userID)
	}

	// 注意：不对共享集合设置过期时间，集合成员通过定期清理维护

	_, err := pipe.Exec(ctx)
	return err
}

// BatchSetOffline 批量设置用户离线
func (r *RedisOnlineStatusRepository) BatchSetOffline(ctx context.Context, userIDs []string) error {
	pipe := r.client.Pipeline()

	for _, userID := range userIDs {
		// 获取用户信息以便从集合中移除
		info, _ := r.GetOnlineInfo(ctx, userID)

		pipe.Del(ctx, r.GetUserKey(userID))
		pipe.SRem(ctx, r.GetAllUsersSetKey(), userID)

		if info != nil {
			if info.NodeID != "" {
				pipe.SRem(ctx, r.GetNodeSetKey(info.NodeID), userID)
			}
			pipe.SRem(ctx, r.GetUserTypeSetKey(info.UserType), userID)
		}
	}

	_, err := pipe.Exec(ctx)
	return err
}

// GetOnlineUsersByType 根据用户类型获取在线用户
func (r *RedisOnlineStatusRepository) GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error) {
	return r.client.SMembers(ctx, r.GetUserTypeSetKey(userType)).Result()
}

// CleanupExpired 清理过期的在线状态
// 注意：Redis 会自动清理过期的 key，此方法主要用于清理集合中的无效成员
func (r *RedisOnlineStatusRepository) CleanupExpired(ctx context.Context) (int64, error) {
	var cleaned int64

	// 获取所有在线用户
	allUsers, err := r.GetAllOnlineUsers(ctx)
	if err != nil {
		return 0, err
	}

	pipe := r.client.Pipeline()
	var toRemove []string
	userTypeMap := make(map[models.UserType][]string) // 记录需要从各类型集合中移除的用户
	nodeMap := make(map[string][]string)              // 记录需要从各节点集合中移除的用户

	// 检查每个用户是否真的在线
	for _, userID := range allUsers {
		exists, err := r.client.Exists(ctx, r.GetUserKey(userID)).Result()
		if err != nil {
			continue
		}
		if exists == 0 {
			// 用户信息已过期，但还在集合中
			// 先获取用户信息，确定需要从哪些集合中移除
			info, _ := r.GetOnlineInfo(ctx, userID)

			toRemove = append(toRemove, userID)
			pipe.SRem(ctx, r.GetAllUsersSetKey(), userID)

			// 如果能获取到用户信息，从对应的类型和节点集合中移除
			if info != nil {
				userTypeMap[info.UserType] = append(userTypeMap[info.UserType], userID)
				if info.NodeID != "" {
					nodeMap[info.NodeID] = append(nodeMap[info.NodeID], userID)
				}
			}
		}
	}

	// 从用户类型集合中移除过期用户
	for userType, users := range userTypeMap {
		for _, userID := range users {
			pipe.SRem(ctx, r.GetUserTypeSetKey(userType), userID)
		}
	}

	// 从节点集合中移除过期用户
	for nodeID, users := range nodeMap {
		for _, userID := range users {
			pipe.SRem(ctx, r.GetNodeSetKey(nodeID), userID)
		}
	}

	if len(toRemove) > 0 {
		_, err = pipe.Exec(ctx)
		if err != nil {
			return 0, err
		}
		cleaned = int64(len(toRemove))
	}

	return cleaned, nil
}

// ============================================================================
// 分布式节点相关方法实现
// ============================================================================

// SetUserNode 设置用户所在节点
func (r *RedisOnlineStatusRepository) SetUserNode(ctx context.Context, userID string, nodeID string) error {
	key := fmt.Sprintf("%suser_node:%s", r.keyPrefix, userID)
	return r.client.Set(ctx, key, nodeID, r.ttl).Err()
}

// GetUserNode 获取用户所在节点
func (r *RedisOnlineStatusRepository) GetUserNode(ctx context.Context, userID string) (string, error) {
	key := fmt.Sprintf("%suser_node:%s", r.keyPrefix, userID)
	result, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil // 用户不在线或未记录节点
	}
	return result, err
}

// GetNodeUsers 获取节点的所有在线用户
func (r *RedisOnlineStatusRepository) GetNodeUsers(ctx context.Context, nodeID string) ([]string, error) {
	pattern := fmt.Sprintf("%suser_node:*", r.keyPrefix)

	var users []string
	iter := r.client.Scan(ctx, 0, pattern, 0).Iterator()

	for iter.Next(ctx) {
		key := iter.Val()
		node, err := r.client.Get(ctx, key).Result()
		if err != nil || node != nodeID {
			continue
		}

		// 从 key 中提取 userID
		// key 格式: wsc:online:user_node:user123
		parts := strings.Split(key, ":")
		if len(parts) >= 3 {
			userID := parts[len(parts)-1]
			users = append(users, userID)
		}
	}

	if err := iter.Err(); err != nil {
		return nil, err
	}

	return users, nil
}

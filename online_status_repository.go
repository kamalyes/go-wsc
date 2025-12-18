/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-01
 * @FilePath: \go-wsc\online_status_repository.go
 * @Description: 客户端在线状态管理 - 支持 Redis 分布式存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/redis/go-redis/v9"
)

// OnlineClientInfo 在线客户端信息
type OnlineClientInfo struct {
	ClientID      string                 `json:"client_id"`      // 客户端ID
	UserID        string                 `json:"user_id"`        // 用户ID
	UserType      UserType               `json:"user_type"`      // 用户类型
	NodeID        string                 `json:"node_id"`        // 所在节点ID
	NodeIP        string                 `json:"node_ip"`        // 节点IP
	ClientIP      string                 `json:"client_ip"`      // 客户端IP
	ConnectTime   time.Time              `json:"connect_time"`   // 连接时间
	LastSeen      time.Time              `json:"last_seen"`      // 最后活跃时间
	LastHeartbeat time.Time              `json:"last_heartbeat"` // 最后心跳时间
	ClientType    ClientType             `json:"client_type"`    // 客户端类型
	Status        UserStatus             `json:"status"`         // 状态
	Metadata      map[string]interface{} `json:"metadata"`       // 元数据
}

// OnlineStatusRepository 在线状态仓库接口
type OnlineStatusRepository interface {
	// SetOnline 设置用户在线
	SetOnline(ctx context.Context, userID string, info *OnlineClientInfo, ttl time.Duration) error

	// SetOffline 设置用户离线
	SetOffline(ctx context.Context, userID string) error

	// IsOnline 检查用户是否在线
	IsOnline(ctx context.Context, userID string) (bool, error)

	// GetOnlineInfo 获取在线用户信息
	GetOnlineInfo(ctx context.Context, userID string) (*OnlineClientInfo, error)

	// GetAllOnlineUsers 获取所有在线用户ID列表
	GetAllOnlineUsers(ctx context.Context) ([]string, error)

	// GetOnlineUsersByNode 获取指定节点的在线用户
	GetOnlineUsersByNode(ctx context.Context, nodeID string) ([]string, error)

	// GetOnlineCount 获取在线用户总数
	GetOnlineCount(ctx context.Context) (int64, error)

	// UpdateHeartbeat 更新心跳时间
	UpdateHeartbeat(ctx context.Context, userID string) error

	// BatchSetOnline 批量设置用户在线
	BatchSetOnline(ctx context.Context, users map[string]*OnlineClientInfo, ttl time.Duration) error

	// BatchSetOffline 批量设置用户离线
	BatchSetOffline(ctx context.Context, userIDs []string) error

	// GetOnlineUsersByType 根据用户类型获取在线用户
	GetOnlineUsersByType(ctx context.Context, userType UserType) ([]string, error)

	// CleanupExpired 清理过期的在线状态（可选，Redis 会自动过期）
	CleanupExpired(ctx context.Context) (int64, error)
}

// RedisOnlineStatusRepository Redis 实现
type RedisOnlineStatusRepository struct {
	client     *redis.Client
	keyPrefix  string        // key 前缀
	defaultTTL time.Duration // 默认过期时间
}

// NewRedisOnlineStatusRepository 创建 Redis 在线状态仓库
// 参数:
//   - client: Redis 客户端 (github.com/redis/go-redis/v9)
//   - keyPrefix: key 前缀，默认为 "wsc:online:"
//   - ttl: 默认过期时间，建议设置为心跳间隔的 2-3 倍
func NewRedisOnlineStatusRepository(client *redis.Client, keyPrefix string, ttl time.Duration) OnlineStatusRepository {
	return &RedisOnlineStatusRepository{
		client:     client,
		keyPrefix:  mathx.IF(keyPrefix == "", "wsc:online:", keyPrefix),
		defaultTTL: mathx.IF(ttl == 0, 5*time.Minute, ttl),
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
func (r *RedisOnlineStatusRepository) GetUserTypeSetKey(userType UserType) string {
	return fmt.Sprintf("%s%s", r.keyPrefix, userType)
}

// GetAllUsersSetKey 获取所有在线用户集合的 key
func (r *RedisOnlineStatusRepository) GetAllUsersSetKey() string {
	return fmt.Sprintf("%sall", r.keyPrefix)
}

// SetOnline 设置用户在线
func (r *RedisOnlineStatusRepository) SetOnline(ctx context.Context, userID string, info *OnlineClientInfo, ttl time.Duration) error {
	if ttl == 0 {
		ttl = r.defaultTTL
	}

	// 序列化用户信息
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("failed to marshal online info: %w", err)
	}

	// 使用 pipeline 批量执行
	pipe := r.client.Pipeline()

	// 1. 设置用户在线信息
	pipe.Set(ctx, r.GetUserKey(userID), data, ttl)

	// 2. 添加到全局在线用户集合
	pipe.SAdd(ctx, r.GetAllUsersSetKey(), userID)
	pipe.Expire(ctx, r.GetAllUsersSetKey(), ttl)

	// 3. 添加到节点在线用户集合
	if info.NodeID != "" {
		pipe.SAdd(ctx, r.GetNodeSetKey(info.NodeID), userID)
		pipe.Expire(ctx, r.GetNodeSetKey(info.NodeID), ttl)
	}

	// 4. 添加到用户类型集合
	pipe.SAdd(ctx, r.GetUserTypeSetKey(info.UserType), userID)
	pipe.Expire(ctx, r.GetUserTypeSetKey(info.UserType), ttl)

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

// GetOnlineInfo 获取在线用户信息
func (r *RedisOnlineStatusRepository) GetOnlineInfo(ctx context.Context, userID string) (*OnlineClientInfo, error) {
	data, err := r.client.Get(ctx, r.GetUserKey(userID)).Result()
	if err != nil {
		return nil, err
	}

	var info OnlineClientInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, fmt.Errorf("failed to unmarshal online info: %w", err)
	}

	return &info, nil
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
	info, err := r.GetOnlineInfo(ctx, userID)
	if err != nil {
		return err
	}

	// 更新心跳时间
	info.LastHeartbeat = time.Now()
	info.LastSeen = time.Now()

	// 重新设置（会刷新 TTL）
	return r.SetOnline(ctx, userID, info, r.defaultTTL)
}

// BatchSetOnline 批量设置用户在线
func (r *RedisOnlineStatusRepository) BatchSetOnline(ctx context.Context, users map[string]*OnlineClientInfo, ttl time.Duration) error {
	if ttl == 0 {
		ttl = r.defaultTTL
	}

	pipe := r.client.Pipeline()

	for userID, info := range users {
		data, err := json.Marshal(info)
		if err != nil {
			return fmt.Errorf("failed to marshal online info for user %s: %w", userID, err)
		}

		pipe.Set(ctx, r.GetUserKey(userID), data, ttl)
		pipe.SAdd(ctx, r.GetAllUsersSetKey(), userID)

		if info.NodeID != "" {
			pipe.SAdd(ctx, r.GetNodeSetKey(info.NodeID), userID)
		}

		pipe.SAdd(ctx, r.GetUserTypeSetKey(info.UserType), userID)
	}

	// 设置集合过期时间
	pipe.Expire(ctx, r.GetAllUsersSetKey(), ttl)

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
func (r *RedisOnlineStatusRepository) GetOnlineUsersByType(ctx context.Context, userType UserType) ([]string, error) {
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

	// 检查每个用户是否真的在线
	for _, userID := range allUsers {
		exists, err := r.client.Exists(ctx, r.GetUserKey(userID)).Result()
		if err != nil {
			continue
		}
		if exists == 0 {
			// 用户信息已过期，但还在集合中
			toRemove = append(toRemove, userID)
			pipe.SRem(ctx, r.GetAllUsersSetKey(), userID)
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

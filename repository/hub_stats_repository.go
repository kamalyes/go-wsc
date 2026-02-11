/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 01:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 18:15:59
 * @FilePath: \go-wsc\repository\hub_stats_repository.go
 * @Description: Hub 统计信息 Redis 存储 - 支持分布式多节点部署
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"fmt"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/convert"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/redis/go-redis/v9"
)

// Redis Hash 字段常量
const (
	FieldTotalConnections  = "total_connections"
	FieldActiveConnections = "active_connections"
	FieldMessagesSent      = "messages_sent"
	FieldMessagesReceived  = "messages_received"
	FieldBroadcastsSent    = "broadcasts_sent"
	FieldStartTime         = "start_time"
)

// Redis Key 前缀常量
const (
	KeyPrefixNode      = "node:"
	KeyPrefixHeartbeat = "heartbeat:"
	KeySuffixNodes     = "nodes"
)

// HubStatsRepository Hub 统计信息仓库接口
type HubStatsRepository interface {
	// UpdateConnectionStats 批量更新连接统计(总连接数+1,活跃连接数,心跳时间)
	UpdateConnectionStats(ctx context.Context, nodeID string, activeCount int64) error

	// IncrementTotalConnections 增加总连接数
	IncrementTotalConnections(ctx context.Context, nodeID string, delta int64) error

	// SetActiveConnections 设置当前活跃连接数
	SetActiveConnections(ctx context.Context, nodeID string, count int64) error

	// IncrementMessagesSent 增加已发送消息数
	IncrementMessagesSent(ctx context.Context, nodeID string, delta int64) error

	// IncrementMessagesReceived 增加已接收消息数
	IncrementMessagesReceived(ctx context.Context, nodeID string, delta int64) error

	// IncrementBroadcastsSent 增加已发送广播数
	IncrementBroadcastsSent(ctx context.Context, nodeID string, delta int64) error

	// RegisterNode 注册节点并初始化统计信息（设置启动时间、添加到节点集合）
	RegisterNode(ctx context.Context, nodeID string, startTime int64) error

	// GetNodeStats 获取指定节点的统计信息
	GetNodeStats(ctx context.Context, nodeID string) (*NodeStats, error)

	// GetAllNodesStats 获取所有节点的统计信息
	GetAllNodesStats(ctx context.Context) (map[string]*NodeStats, error)

	// GetTotalStats 获取集群总统计信息（所有节点汇总）
	GetTotalStats(ctx context.Context) (*ClusterStats, error)

	// CleanupNodeStats 清理已下线节点的统计数据
	CleanupNodeStats(ctx context.Context, nodeID string) error

	// UpdateNodeHeartbeat 更新节点心跳时间
	UpdateNodeHeartbeat(ctx context.Context, nodeID string) error

	// GetActiveNodes 获取活跃的节点列表（基于心跳）
	GetActiveNodes(ctx context.Context, timeout time.Duration) ([]string, error)
}

// NodeStats 节点统计信息
type NodeStats struct {
	NodeID            string    `json:"node_id" redis:"-"`
	TotalConnections  int64     `json:"total_connections" redis:"total_connections"`
	ActiveConnections int64     `json:"active_connections" redis:"active_connections"`
	MessagesSent      int64     `json:"messages_sent" redis:"messages_sent"`
	MessagesReceived  int64     `json:"messages_received" redis:"messages_received"`
	BroadcastsSent    int64     `json:"broadcasts_sent" redis:"broadcasts_sent"`
	StartTime         int64     `json:"start_time" redis:"start_time"`
	LastHeartbeat     time.Time `json:"last_heartbeat" redis:"-"`
	Uptime            int64     `json:"uptime" redis:"-"` // 运行时间(秒),计算字段
}

// ClusterStats 集群统计信息
type ClusterStats struct {
	TotalNodes        int          `json:"total_nodes"`
	ActiveNodes       int          `json:"active_nodes"`
	TotalConnections  int64        `json:"total_connections"`
	ActiveConnections int64        `json:"active_connections"`
	MessagesSent      int64        `json:"messages_sent"`
	MessagesReceived  int64        `json:"messages_received"`
	BroadcastsSent    int64        `json:"broadcasts_sent"`
	NodesStats        []*NodeStats `json:"nodes_stats"`
	UpdateTime        time.Time    `json:"update_time"`
}

// RedisHubStatsRepository Redis 实现的 Hub 统计仓库
type RedisHubStatsRepository struct {
	client      *redis.Client
	keyPrefix   string        // Redis key 前缀，例如 "wsc:stats:"
	statsExpire time.Duration // 统计数据过期时间，默认 7 天
}

// NewRedisHubStatsRepository 创建 Redis Hub 统计仓库
// 参数:
//   - client: Redis 客户端 (github.com/redis/go-redis/v9)
//   - config: 统计配置对象
func NewRedisHubStatsRepository(client *redis.Client, config *wscconfig.Stats) *RedisHubStatsRepository {
	keyPrefix := mathx.IF(config.KeyPrefix == "", DefaultStatsKeyPrefix, config.KeyPrefix)
	ttl := mathx.IF(config.TTL == 0, 7*24*time.Hour, config.TTL)

	return &RedisHubStatsRepository{
		client:      client,
		keyPrefix:   keyPrefix,
		statsExpire: ttl,
	}
}

// GetNodeKey 获取节点统计的 Redis key
func (r *RedisHubStatsRepository) GetNodeKey(nodeID string) string {
	return r.keyPrefix + KeyPrefixNode + nodeID
}

// GetHeartbeatKey 获取节点心跳的 Redis key
func (r *RedisHubStatsRepository) GetHeartbeatKey(nodeID string) string {
	return r.keyPrefix + KeyPrefixHeartbeat + nodeID
}

// GetNodesSetKey 获取节点集合的 Redis key
func (r *RedisHubStatsRepository) GetNodesSetKey() string {
	return r.keyPrefix + KeySuffixNodes
}

// UpdateConnectionStats 批量更新连接统计(总连接数+1,活跃连接数,心跳时间)
// 这个方法将3个Redis操作合并到一个Pipeline中,提高性能
func (r *RedisHubStatsRepository) UpdateConnectionStats(ctx context.Context, nodeID string, activeCount int64) error {
	key := r.GetNodeKey(nodeID)
	heartbeatKey := r.GetHeartbeatKey(nodeID)
	nodesSetKey := r.GetNodesSetKey()

	pipe := r.client.Pipeline()
	// 增加总连接数
	pipe.HIncrBy(ctx, key, FieldTotalConnections, 1)
	// 设置活跃连接数
	pipe.HSet(ctx, key, FieldActiveConnections, activeCount)
	// 设置过期时间
	pipe.Expire(ctx, key, r.statsExpire)
	// 添加到节点集合
	pipe.SAdd(ctx, nodesSetKey, nodeID)
	// 更新心跳时间
	pipe.Set(ctx, heartbeatKey, time.Now().Unix(), r.statsExpire)

	_, err := pipe.Exec(ctx)
	return err
}

// IncrementTotalConnections 增加总连接数
func (r *RedisHubStatsRepository) IncrementTotalConnections(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, FieldTotalConnections, delta)
	pipe.Expire(ctx, key, r.statsExpire)
	pipe.SAdd(ctx, r.GetNodesSetKey(), nodeID)
	_, err := pipe.Exec(ctx)
	return err
}

// SetActiveConnections 设置当前活跃连接数
func (r *RedisHubStatsRepository) SetActiveConnections(ctx context.Context, nodeID string, count int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HSet(ctx, key, FieldActiveConnections, count)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// IncrementMessagesSent 增加已发送消息数
func (r *RedisHubStatsRepository) IncrementMessagesSent(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, FieldMessagesSent, delta)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// IncrementMessagesReceived 增加已接收消息数
func (r *RedisHubStatsRepository) IncrementMessagesReceived(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, FieldMessagesReceived, delta)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// IncrementBroadcastsSent 增加已发送广播数
func (r *RedisHubStatsRepository) IncrementBroadcastsSent(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, FieldBroadcastsSent, delta)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// RegisterNode 注册节点并初始化统计信息
func (r *RedisHubStatsRepository) RegisterNode(ctx context.Context, nodeID string, startTime int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HSet(ctx, key, FieldStartTime, startTime)
	pipe.Expire(ctx, key, r.statsExpire)
	pipe.SAdd(ctx, r.GetNodesSetKey(), nodeID)
	_, err := pipe.Exec(ctx)
	return err
}

// UpdateNodeHeartbeat 更新节点心跳时间
func (r *RedisHubStatsRepository) UpdateNodeHeartbeat(ctx context.Context, nodeID string) error {
	key := r.GetHeartbeatKey(nodeID)
	return r.client.Set(ctx, key, time.Now().Unix(), r.statsExpire).Err()
}

// GetNodeStats 获取指定节点的统计信息
func (r *RedisHubStatsRepository) GetNodeStats(ctx context.Context, nodeID string) (*NodeStats, error) {
	key := r.GetNodeKey(nodeID)

	// 使用Scan直接映射到结构体
	stats := &NodeStats{NodeID: nodeID}
	if err := r.client.HGetAll(ctx, key).Scan(stats); err != nil {
		return nil, err
	}

	// 检查是否找到数据
	if stats.TotalConnections == 0 && stats.ActiveConnections == 0 && stats.StartTime == 0 {
		return nil, fmt.Errorf("node stats not found: %s", nodeID)
	}

	// 计算运行时间
	if stats.StartTime > 0 {
		stats.Uptime = time.Now().Unix() - stats.StartTime
	}

	// 获取心跳时间
	heartbeatKey := r.GetHeartbeatKey(nodeID)
	if heartbeatVal, err := r.client.Get(ctx, heartbeatKey).Result(); err == nil {
		if ts, err := convert.MustIntT[int64](heartbeatVal, nil); err == nil {
			stats.LastHeartbeat = time.Unix(ts, 0)
		}
	}

	return stats, nil
}

// GetAllNodesStats 获取所有节点的统计信息
func (r *RedisHubStatsRepository) GetAllNodesStats(ctx context.Context) (map[string]*NodeStats, error) {
	// 获取所有节点ID
	nodeIDs, err := r.client.SMembers(ctx, r.GetNodesSetKey()).Result()
	if err != nil {
		return nil, err
	}

	statsMap := make(map[string]*NodeStats)
	for _, nodeID := range nodeIDs {
		stats, err := r.GetNodeStats(ctx, nodeID)
		if err != nil {
			continue // 跳过获取失败的节点
		}
		statsMap[nodeID] = stats
	}

	return statsMap, nil
}

// GetTotalStats 获取集群总统计信息（所有节点汇总）
func (r *RedisHubStatsRepository) GetTotalStats(ctx context.Context) (*ClusterStats, error) {
	allStats, err := r.GetAllNodesStats(ctx)
	if err != nil {
		return nil, err
	}

	clusterStats := &ClusterStats{
		TotalNodes: len(allStats),
		NodesStats: make([]*NodeStats, 0, len(allStats)),
		UpdateTime: time.Now(),
	}

	// 汇总所有节点的统计数据
	for _, stats := range allStats {
		clusterStats.TotalConnections += stats.TotalConnections
		clusterStats.ActiveConnections += stats.ActiveConnections
		clusterStats.MessagesSent += stats.MessagesSent
		clusterStats.MessagesReceived += stats.MessagesReceived
		clusterStats.BroadcastsSent += stats.BroadcastsSent
		clusterStats.NodesStats = append(clusterStats.NodesStats, stats)

		// 检查节点是否活跃（心跳在5分钟内）
		if time.Since(stats.LastHeartbeat) < 5*time.Minute {
			clusterStats.ActiveNodes++
		}
	}

	return clusterStats, nil
}

// GetActiveNodes 获取活跃的节点列表（基于心跳）
func (r *RedisHubStatsRepository) GetActiveNodes(ctx context.Context, timeout time.Duration) ([]string, error) {
	timeout = mathx.IfEmpty(timeout, 5*time.Minute)

	nodeIDs, err := r.client.SMembers(ctx, r.GetNodesSetKey()).Result()
	if err != nil {
		return nil, err
	}

	activeNodes := make([]string, 0)
	now := time.Now()

	for _, nodeID := range nodeIDs {
		heartbeatKey := r.GetHeartbeatKey(nodeID)
		heartbeatVal, err := r.client.Get(ctx, heartbeatKey).Result()
		if err != nil {
			continue
		}

		ts, err := convert.MustIntT[int64](heartbeatVal, nil)
		if err != nil {
			continue
		}

		lastHeartbeat := time.Unix(ts, 0)
		if now.Sub(lastHeartbeat) <= timeout {
			activeNodes = append(activeNodes, nodeID)
		}
	}

	return activeNodes, nil
}

// CleanupNodeStats 清理已下线节点的统计数据
func (r *RedisHubStatsRepository) CleanupNodeStats(ctx context.Context, nodeID string) error {
	pipe := r.client.Pipeline()
	pipe.Del(ctx, r.GetNodeKey(nodeID))
	pipe.Del(ctx, r.GetHeartbeatKey(nodeID))
	pipe.SRem(ctx, r.GetNodesSetKey(), nodeID)
	_, err := pipe.Exec(ctx)
	return err
}

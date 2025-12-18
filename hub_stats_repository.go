/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 01:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 01:05:00
 * @FilePath: \go-wsc\hub_stats_repository.go
 * @Description: Hub 统计信息 Redis 存储 - 支持分布式多节点部署
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/redis/go-redis/v9"
)

// HubStatsRepository Hub 统计信息仓库接口
type HubStatsRepository interface {
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

	// SetStartTime 设置 Hub 启动时间
	SetStartTime(ctx context.Context, nodeID string, startTime int64) error

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
	NodeID            string    `json:"node_id"`
	TotalConnections  int64     `json:"total_connections"`
	ActiveConnections int64     `json:"active_connections"`
	MessagesSent      int64     `json:"messages_sent"`
	MessagesReceived  int64     `json:"messages_received"`
	BroadcastsSent    int64     `json:"broadcasts_sent"`
	StartTime         int64     `json:"start_time"`
	LastHeartbeat     time.Time `json:"last_heartbeat"`
	Uptime            int64     `json:"uptime"` // 运行时间（秒）
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
//   - keyPrefix: key 前缀，默认为 "wsc:stats:"
//   - ttl: 统计数据过期时间，默认 7 天
func NewRedisHubStatsRepository(client *redis.Client, keyPrefix string, ttl time.Duration) *RedisHubStatsRepository {
	keyPrefix = mathx.IF(keyPrefix == "", "wsc:stats:", keyPrefix)
	ttl = mathx.IF(ttl == 0, 7*24*time.Hour, ttl)

	return &RedisHubStatsRepository{
		client:      client,
		keyPrefix:   keyPrefix,
		statsExpire: ttl,
	}
}

// GetNodeKey 获取节点统计的 Redis key
func (r *RedisHubStatsRepository) GetNodeKey(nodeID string) string {
	return r.keyPrefix + "node:" + nodeID
}

// GetHeartbeatKey 获取节点心跳的 Redis key
func (r *RedisHubStatsRepository) GetHeartbeatKey(nodeID string) string {
	return r.keyPrefix + "heartbeat:" + nodeID
}

// GetNodesSetKey 获取节点集合的 Redis key
func (r *RedisHubStatsRepository) GetNodesSetKey() string {
	return r.keyPrefix + "nodes"
}

// IncrementTotalConnections 增加总连接数
func (r *RedisHubStatsRepository) IncrementTotalConnections(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, "total_connections", delta)
	pipe.Expire(ctx, key, r.statsExpire)
	pipe.SAdd(ctx, r.GetNodesSetKey(), nodeID)
	_, err := pipe.Exec(ctx)
	return err
}

// SetActiveConnections 设置当前活跃连接数
func (r *RedisHubStatsRepository) SetActiveConnections(ctx context.Context, nodeID string, count int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HSet(ctx, key, "active_connections", count)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// IncrementMessagesSent 增加已发送消息数
func (r *RedisHubStatsRepository) IncrementMessagesSent(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, "messages_sent", delta)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// IncrementMessagesReceived 增加已接收消息数
func (r *RedisHubStatsRepository) IncrementMessagesReceived(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, "messages_received", delta)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// IncrementBroadcastsSent 增加已发送广播数
func (r *RedisHubStatsRepository) IncrementBroadcastsSent(ctx context.Context, nodeID string, delta int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HIncrBy(ctx, key, "broadcasts_sent", delta)
	pipe.Expire(ctx, key, r.statsExpire)
	_, err := pipe.Exec(ctx)
	return err
}

// SetStartTime 设置 Hub 启动时间
func (r *RedisHubStatsRepository) SetStartTime(ctx context.Context, nodeID string, startTime int64) error {
	key := r.GetNodeKey(nodeID)
	pipe := r.client.Pipeline()
	pipe.HSet(ctx, key, "start_time", startTime)
	pipe.Expire(ctx, key, r.statsExpire)
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
	result, err := r.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("node stats not found: %s", nodeID)
	}

	stats := &NodeStats{
		NodeID: nodeID,
	}

	if val, ok := result["total_connections"]; ok {
		stats.TotalConnections, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := result["active_connections"]; ok {
		stats.ActiveConnections, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := result["messages_sent"]; ok {
		stats.MessagesSent, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := result["messages_received"]; ok {
		stats.MessagesReceived, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := result["broadcasts_sent"]; ok {
		stats.BroadcastsSent, _ = strconv.ParseInt(val, 10, 64)
	}
	if val, ok := result["start_time"]; ok {
		stats.StartTime, _ = strconv.ParseInt(val, 10, 64)
		if stats.StartTime > 0 {
			stats.Uptime = time.Now().Unix() - stats.StartTime
		}
	}

	// 获取心跳时间
	heartbeatKey := r.GetHeartbeatKey(nodeID)
	if heartbeatVal, err := r.client.Get(ctx, heartbeatKey).Result(); err == nil {
		if ts, err := strconv.ParseInt(heartbeatVal, 10, 64); err == nil {
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
	if timeout <= 0 {
		timeout = 5 * time.Minute // 默认 5 分钟
	}

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

		ts, err := strconv.ParseInt(heartbeatVal, 10, 64)
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

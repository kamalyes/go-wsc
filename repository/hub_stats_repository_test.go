/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-03 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-03 00:00:00
 * @FilePath: \go-wsc\repository\hub_stats_repository_test.go
 * @Description: Hub 统计仓库 Lua 脚本改造验证测试（仅依赖 Redis）
 *
 * 验证要点：
 *  1. 各方法从 Pipeline 改为 Lua 后的功能等价性
 *  2. 条件续期逻辑：TTL 高于阈值时跳过 EXPIRE，低于阈值时续期
 *  3. 首次创建（key 不存在 TTL=-2）时正确设置 EXPIRE
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"context"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStatsRepo 创建基于 miniredis（纯内存 Redis mock）的统计仓库
// 优势：无需外部 Redis 服务，支持 Lua EVAL 脚本，每个测试用例独立隔离
// statsExpire=24h，故 expireRefreshThreshold=12h
func newTestStatsRepo(t *testing.T) (*RedisHubStatsRepository, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	repo := NewRedisHubStatsRepository(client, &wscconfig.Stats{
		KeyPrefix: "wsc:test:lua:",
		TTL:       24 * time.Hour,
	})
	return repo, client
}

// TestLua_RegisterNodeAndUpdateConnection 验证 RegisterNode + UpdateConnectionStats 的 Lua 脚本
func TestLua_RegisterNodeAndUpdateConnection(t *testing.T) {
	repo, client := newTestStatsRepo(t)
	ctx := context.Background()
	nodeID := "node-1"
	startTime := time.Now().Unix()

	// 注册节点
	require.NoError(t, repo.RegisterNode(ctx, nodeID, startTime))

	// 验证 start_time 已设置
	stats, err := repo.GetNodeStats(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, startTime, stats.StartTime)

	// 验证已 SADD 到 nodes set
	members, err := client.SMembers(ctx, repo.GetNodesSetKey()).Result()
	require.NoError(t, err)
	assert.Contains(t, members, nodeID)

	// 验证首次创建设置了 EXPIRE
	ttl, err := client.TTL(ctx, repo.GetNodeKey(nodeID)).Result()
	require.NoError(t, err)
	assert.Greater(t, ttl.Seconds(), float64(0), "首次创建应设置 EXPIRE")

	// UpdateConnectionStats：5 操作合并为 1 次 Lua
	require.NoError(t, repo.UpdateConnectionStats(ctx, nodeID, 10))

	stats, err = repo.GetNodeStats(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), stats.TotalConnections, "total_connections 应 +1")
	assert.Equal(t, int64(10), stats.ActiveConnections, "active_connections 应被设置")

	// 验证 heartbeat key 已写入
	hb, err := client.Get(ctx, repo.GetHeartbeatKey(nodeID)).Result()
	require.NoError(t, err)
	assert.NotEmpty(t, hb, "心跳 key 应已写入")
}

// TestLua_IncrementMessageStats 验证各 Increment* 方法的增量正确性
func TestLua_IncrementMessageStats(t *testing.T) {
	repo, _ := newTestStatsRepo(t)
	ctx := context.Background()
	nodeID := "node-2"
	require.NoError(t, repo.RegisterNode(ctx, nodeID, time.Now().Unix()))

	for i := 0; i < 5; i++ {
		require.NoError(t, repo.IncrementMessagesSent(ctx, nodeID, 1))
		require.NoError(t, repo.IncrementMessagesReceived(ctx, nodeID, 2))
		require.NoError(t, repo.IncrementBroadcastsSent(ctx, nodeID, 3))
		require.NoError(t, repo.IncrementTotalConnections(ctx, nodeID, 1))
	}

	stats, err := repo.GetNodeStats(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, int64(5), stats.MessagesSent, "messages_sent 累加应为 5")
	assert.Equal(t, int64(10), stats.MessagesReceived, "messages_received 累加应为 10")
	assert.Equal(t, int64(15), stats.BroadcastsSent, "broadcasts_sent 累加应为 15")
	assert.Equal(t, int64(5), stats.TotalConnections, "total_connections 累加应为 5")
}

// TestLua_SetActiveConnections 验证 SetActiveConnections 的 HSET
func TestLua_SetActiveConnections(t *testing.T) {
	repo, _ := newTestStatsRepo(t)
	ctx := context.Background()
	nodeID := "node-3"
	require.NoError(t, repo.RegisterNode(ctx, nodeID, time.Now().Unix()))

	require.NoError(t, repo.SetActiveConnections(ctx, nodeID, 42))
	stats, err := repo.GetNodeStats(ctx, nodeID)
	require.NoError(t, err)
	assert.Equal(t, int64(42), stats.ActiveConnections)
}

// TestLua_ConditionalExpire_SkipWhenTTLHigh 验证 TTL 高于阈值时不续期（减少冗余 EXPIRE 写）
// statsExpire=24h，threshold=12h。设置 TTL=20h > 12h，Increment 后 TTL 不应被重置
func TestLua_ConditionalExpire_SkipWhenTTLHigh(t *testing.T) {
	repo, client := newTestStatsRepo(t)
	ctx := context.Background()
	nodeID := "node-4"
	require.NoError(t, repo.RegisterNode(ctx, nodeID, time.Now().Unix()))

	// 将 TTL 设为 20h（高于阈值 12h）
	require.NoError(t, client.Expire(ctx, repo.GetNodeKey(nodeID), 20*time.Hour).Err())
	ttlBefore, err := client.TTL(ctx, repo.GetNodeKey(nodeID)).Result()
	require.NoError(t, err)

	// 触发 IncrementMessagesSent
	require.NoError(t, repo.IncrementMessagesSent(ctx, nodeID, 1))

	ttlAfter, err := client.TTL(ctx, repo.GetNodeKey(nodeID)).Result()
	require.NoError(t, err)

	// TTL 应基本不变（未触发续期），允许 5 秒误差
	assert.InDelta(t, ttlBefore.Seconds(), ttlAfter.Seconds(), 5,
		"TTL 高于阈值时不应续期，TTL 应保持不变")
}

// TestLua_ConditionalExpire_RefreshWhenTTLLow 验证 TTL 低于阈值时续期
// 设置 TTL=1h < 12h，Increment 后应续期到 24h
func TestLua_ConditionalExpire_RefreshWhenTTLLow(t *testing.T) {
	repo, client := newTestStatsRepo(t)
	ctx := context.Background()
	nodeID := "node-5"
	require.NoError(t, repo.RegisterNode(ctx, nodeID, time.Now().Unix()))

	// 将 TTL 设为 1h（低于阈值 12h）
	require.NoError(t, client.Expire(ctx, repo.GetNodeKey(nodeID), 1*time.Hour).Err())

	// 触发 IncrementMessagesSent，应续期
	require.NoError(t, repo.IncrementMessagesSent(ctx, nodeID, 1))

	ttlAfter, err := client.TTL(ctx, repo.GetNodeKey(nodeID)).Result()
	require.NoError(t, err)

	// TTL 应被续期到接近 24h（大于 23h 即可）
	assert.Greater(t, ttlAfter.Seconds(), (23 * time.Hour).Seconds(),
		"TTL 低于阈值时应续期到 statsExpire")
}

// TestLua_ConditionalExpire_FirstCreate 验证首次创建（key 不存在 TTL=-2）时设置 EXPIRE
func TestLua_ConditionalExpire_FirstCreate(t *testing.T) {
	repo, client := newTestStatsRepo(t)
	ctx := context.Background()
	nodeID := "node-6"

	// 不先 RegisterNode，直接 Increment（key 不存在）
	require.NoError(t, repo.IncrementMessagesSent(ctx, nodeID, 1))

	ttl, err := client.TTL(ctx, repo.GetNodeKey(nodeID)).Result()
	require.NoError(t, err)
	// 首次创建应设置 EXPIRE（TTL > 0）
	assert.Greater(t, ttl.Seconds(), float64(0), "首次创建应设置 EXPIRE")
	// 且不应超过 statsExpire（24h）
	assert.LessOrEqual(t, ttl.Seconds(), (24*time.Hour).Seconds()+10,
		"TTL 不应超过 statsExpire")
}

// TestLua_CleanupNodeStats 验证清理脚本（DEL stats + DEL heartbeat + SREM nodes）
func TestLua_CleanupNodeStats(t *testing.T) {
	repo, client := newTestStatsRepo(t)
	ctx := context.Background()
	nodeID := "node-7"
	require.NoError(t, repo.RegisterNode(ctx, nodeID, time.Now().Unix()))
	require.NoError(t, repo.UpdateConnectionStats(ctx, nodeID, 5))

	// 清理
	require.NoError(t, repo.CleanupNodeStats(ctx, nodeID))

	// stats key 应被删除
	exists, err := client.Exists(ctx, repo.GetNodeKey(nodeID)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "stats key 应被删除")

	// heartbeat key 应被删除
	exists, err = client.Exists(ctx, repo.GetHeartbeatKey(nodeID)).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "heartbeat key 应被删除")

	// 应从 nodes set 移除
	members, err := client.SMembers(ctx, repo.GetNodesSetKey()).Result()
	require.NoError(t, err)
	assert.NotContains(t, members, nodeID, "应从 nodes set 移除")
}

// TestLua_GetTotalStats 验证汇总查询在 Lua 改造后依然正确
func TestLua_GetTotalStats(t *testing.T) {
	repo, _ := newTestStatsRepo(t)
	ctx := context.Background()

	// 注册两个节点并产生统计
	for _, nodeID := range []string{"node-a", "node-b"} {
		require.NoError(t, repo.RegisterNode(ctx, nodeID, time.Now().Unix()))
		require.NoError(t, repo.IncrementMessagesSent(ctx, nodeID, 10))
		require.NoError(t, repo.IncrementMessagesReceived(ctx, nodeID, 5))
	}

	total, err := repo.GetTotalStats(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, total.TotalNodes)
	assert.Equal(t, int64(20), total.MessagesSent, "两节点 messages_sent 汇总应为 20")
	assert.Equal(t, int64(10), total.MessagesReceived, "两节点 messages_received 汇总应为 10")
}

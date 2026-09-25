/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-25 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-25 00:00:00
 * @FilePath: \go-wsc\adapter\redis\online_store_nodes_test.go
 * @Description: 节点桶（nodes:{app}:{uid}）语义测试
 *
 * 节点桶是跨节点定位的直查索引（GetUserNodes/BatchGetUserNodes 的唯一数据源），
 * 核心契约：
 *   - member="<ns>:<nodeID>"、score=expireTime，scoped 按 ns 前缀过滤、通配取后段
 *   - 注销不 ZREM（同 (ns,node) 多端共用条目，盲删会误删同节点其他活跃端）
 *   - 死条目靠 score 过期自愈：读取侧 ZRangeArgs 按 score 下界过滤
 *
 * 复用 online_store_isolation_test.go 的 miniredis + 真实 OnlineStore 基建。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package redisadapter

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
)

// TestOnlineUserNodes_ScopedAndWildcard 验证单桶双语义：scoped 前缀过滤 / 跨 ns 通配聚合
func TestOnlineUserNodes_ScopedAndWildcard(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	// 同一用户同 app 跨 ns 双端：ns-A/node-1 + ns-B/node-2
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1", "user-x", "app-A", "ns-A", "node-1")))
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-2", "user-x", "app-A", "ns-B", "node-2")))

	// scoped 信封 → 只返回该 ns 的节点
	nodes, err := repo.GetUserNodes(scopedCtx("app-A", "ns-A"), "user-x")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "scoped 信封应只返回 ns-A 的节点")

	// 信封 ns=""（群组投递通配）→ 跨 ns 全部节点
	nodes, err = repo.GetUserNodes(scopedCtx("app-A", ""), "user-x")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1", "node-2"}, nodes, "通配信封应返回该 app 全部 ns 的节点")

	// 批量路径同语义：scoped 只含该 ns，通配全量聚合，无在线用户 key 缺失
	result, err := repo.BatchGetUserNodes(scopedCtx("app-A", "ns-B"), []string{"user-x", "user-none"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-2"}, result["user-x"], "批量 scoped 应只返回 ns-B 的节点")
	_, exists := result["user-none"]
	assert.False(t, exists, "无在线用户应不在结果中")

	result, err = repo.BatchGetUserNodes(scopedCtx("app-A", ""), []string{"user-x"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1", "node-2"}, result["user-x"], "批量通配应聚合全部 ns 节点")
}

// TestOnlineUserNodes_ScoreExpirySelfHeal 验证死条目自愈：注销不 ZREM，score 过期后读取过滤
func TestOnlineUserNodes_ScoreExpirySelfHeal(t *testing.T) {
	t.Parallel()
	repo, mr := setupOnlineIsolationRepo(t)
	ctx := context.Background()
	bucketKey := "wsc:iso:online:nodes:app-A:user-y"

	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = redisClient.Close() })

	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-y", "user-y", "app-A", "ns-A", "node-1")))
	scoped := scopedCtx("app-A", "ns-A")

	// 活跃条目可查
	nodes, err := repo.GetUserNodes(scoped, "user-y")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "注册后应可查到节点")

	// 注销不 ZREM：自愈窗口内（score 未过期）条目仍在、仍可查（同节点多端盲删保护）
	require.NoError(t, repo.SetClientOffline(ctx, makeIsolationClient("c-y", "user-y", "app-A", "ns-A", "node-1")))
	nodes, err = repo.GetUserNodes(scoped, "user-y")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "注销后 score 窗口内条目应保留（不盲删）")

	// score 推进到过去（模拟全部端断开后无人续命的死条目）→ 读取自动过滤
	past := time.Now().Add(-time.Hour).Unix()
	require.NoError(t, redisClient.ZAdd(ctx, bucketKey, redis.Z{Score: float64(past), Member: "ns-A:node-1"}).Err())
	nodes, err = repo.GetUserNodes(scoped, "user-y")
	require.NoError(t, err)
	assert.Empty(t, nodes, "score 已过期的死条目应被读取侧过滤（自愈）")

	// score 前移到未来（模拟心跳续期）→ 条目复活
	future := time.Now().Add(time.Minute).Unix()
	require.NoError(t, redisClient.ZAdd(ctx, bucketKey, redis.Z{Score: float64(future), Member: "ns-A:node-1"}).Err())
	nodes, err = repo.GetUserNodes(scoped, "user-y")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "score 前移后条目应恢复可见")
}

// TestOnlineUserNodes_MultiDeviceSameNodeDedup 验证同 (ns,node) 多端共用条目、单次返回去重
func TestOnlineUserNodes_MultiDeviceSameNodeDedup(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	// 同用户同节点双端（如手机+PC 落到同一节点）
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1", "user-m", "app-A", "ns-A", "node-1")))
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-2", "user-m", "app-A", "ns-A", "node-1")))

	nodes, err := repo.GetUserNodes(scopedCtx("app-A", "ns-A"), "user-m")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "同节点多端应共用单条目，节点列表天然去重")

	// 断开其中一端：另一端的 member 不受影响（注销不 ZREM 的盲删保护）
	require.NoError(t, repo.SetClientOffline(ctx, makeIsolationClient("c-1", "user-m", "app-A", "ns-A", "node-1")))
	nodes, err = repo.GetUserNodes(scopedCtx("app-A", "ns-A"), "user-m")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "断开一端后另一活跃端的节点条目应保留")
}

// TestOnlineUserNodes_DefaultAppNoRouteCtx 验证无信封查询收敛 DefaultAppID 域
func TestOnlineUserNodes_DefaultAppNoRouteCtx(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	// appID 留空 → 注册归一化 DefaultAppID，落入 Default 桶
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-d", "user-d", "", "ns-A", "node-1")))
	// 显式 app-B 的连接不落在 Default 桶
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-b", "user-d", "app-B", "ns-B", "node-2")))

	// 无信封 → 收敛 DefaultAppID 域：可查到 Default 连接，不返回 app-B 连接（隔离收紧）
	nodes, err := repo.GetUserNodes(context.Background(), "user-d")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "无信封应查询 DefaultAppID 桶")

	result, err := repo.BatchGetUserNodes(context.Background(), []string{"user-d"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, result["user-d"], "批量无信封同收敛 DefaultAppID 桶")

	// 信封显式指定 app-B 仍精确隔离
	nodes, err = repo.GetUserNodes(scopedCtx("app-B", "ns-B"), "user-d")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-2"}, nodes, "app-B 信封应返回 app-B 的节点")
	assert.NotEqual(t, constants.DefaultAppID, "app-B", "前置自检：app-B 不应为默认值")
}

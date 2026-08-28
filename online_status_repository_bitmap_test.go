/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-22 00:00:00
 * @LastEditTime: 2026-08-22 00:00:00
 * @FilePath: \go-wsc\online_status_repository_bitmap_test.go
 * @Description: Bitmap 分层在线状态测试
 *
 * 覆盖：
 *   - IsUserOnline 命中/未命中/miss 兜底（含 L1 offset 缓存回填）
 *   - 路由信封隔离（同名 userID 跨 app/ns）
 *   - offset 首次分配（INCR/HSETNX 原子性）
 *   - BatchIsUserOnline Pipeline 批量判定 / BatchSetClientsOfflineWithInfo
 *   - GetUserClients / BatchGetUserNodes 走 scoped key
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/random"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newBitmapTestRepoSetup 创建 bitmap 单一路径的测试仓库
//
// bitmap 为唯一实现路径（无开关），此处仅显式设大 TTL 避免慢 CI 机器误判。
// 使用独立前缀避免与其他测试共享 uid_map（offset 一旦分配永久保留，跨测试复用会污染断言）。
func newBitmapTestRepoSetup(t *testing.T, ttl time.Duration) *testRepoSetup {
	t.Helper()
	t.Setenv("WSC_ONLINE_BITMAP_TTL", "60s")

	redisClient := GetTestRedisClientWithFlush(t)
	prefix := getTestIDGenerator().GenerateRequestID()
	return &testRepoSetup{
		repo:   NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", prefix), TTL: ttl}),
		ctx:    context.Background(),
		prefix: prefix,
	}
}

// withRoute 注入路由信封到 ctx，返回带 appID+namespace 的 ctx
func withRoute(ctx context.Context, appID, ns string) context.Context {
	return routing.NewRoute().WithAppID(appID).WithNamespace(ns).Inject(ctx)
}

// newScopedTestClient 创建带指oss定路由信封字段的测试客户端
// client.AppID / client.Namespace 必须与 ctx 注入的路由信封一致，
// 否则 BatchSetClientsOnline 写入的 scoped key 与 IsUserOnline 查询的 scoped key 不匹配
func newScopedTestClient(userType UserType, appID, ns string) *Client {
	client := createTestClientWithIDGen(userType)
	client.AppID = appID
	client.Namespace = ns
	return client
}

// assertOnline 断言 IsUserOnline 返回预期值
func (s *testRepoSetup) assertOnline(t *testing.T, ctx context.Context, userID string, expected bool) {
	t.Helper()
	online, err := s.repo.IsUserOnline(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, expected, online)
}

// TestBitmapIsUserOnline_Basic 基础场景：bitmap 启用 + 路由信封
//
// 验证：
//   - SetClientOnline 后 IsUserOnline=true（GETBIT 命中）
//   - SetClientOffline 后 IsUserOnline=false（uid_map 仍在但 bit=0，回退 ZCount 兜底返回 false）
//   - 首次上线 offset 自动分配（uid_map Hash 有该 userID 字段）
func TestBitmapIsUserOnline_Basic(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	client := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	defer setup.cleanup(client)

	ctx := withRoute(setup.ctx, client.AppID, client.Namespace)

	// 上线前：uid_map 未分配 offset → IsUserOnline 返回 0（确定离线）
	setup.assertOnline(t, ctx, client.UserID, false)

	// 上线：bitmap SETBIT + scoped/global ZSET 双写
	require.NoError(t, setup.repo.SetClientOnline(ctx, client))

	// 上线后：GETBIT 命中 → 在线
	setup.assertOnline(t, ctx, client.UserID, true)

	// 下线：bitmap SETBIT 0 + ZREM scoped/unscoped
	require.NoError(t, setup.repo.SetClientOffline(ctx, client))

	// 下线后：bit=0 → 回退 ZCount 兜底 → scoped ZSET 空 → false
	setup.assertOnline(t, ctx, client.UserID, false)
}

// TestBitmap_RouteEnvelopeIsolation 路由信封隔离：同名 userID 跨 app/ns 不互相误判
//
// 验证：
//   - app-A/ns-1 在线，查 app-B/ns-1 = false（跨 app 隔离）
//   - app-A/ns-1 在线，查 app-A/ns-2 = false（跨 ns 隔离）
//   - 全局广播 ns="" 查询命中（用户在 app-A 任意 ns 在线）
func TestBitmap_RouteEnvelopeIsolation(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	client := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	defer setup.cleanup(client)

	ctxA1 := withRoute(setup.ctx, "app-A", "ns-1")
	require.NoError(t, setup.repo.SetClientOnline(ctxA1, client))

	// 同信封查询：在线
	setup.assertOnline(t, ctxA1, client.UserID, true)

	// 跨 app 查询：app-B/ns-1 → 不同 scoped bitmap，bit=0 → 回退 scoped ZCount 也是空 → false
	ctxB1 := withRoute(setup.ctx, "app-B", "ns-1")
	setup.assertOnline(t, ctxB1, client.UserID, false)

	// 跨 ns 查询：app-A/ns-2 → 不同 scoped bitmap，bit=0 → 回退 scoped ZCount 也是空 → false
	ctxA2 := withRoute(setup.ctx, "app-A", "ns-2")
	setup.assertOnline(t, ctxA2, client.UserID, false)

	// 全局广播查询：ns="" → 命中 global bitmap（SetClientOnline 同时写 global bitmap）→ 在线
	ctxABroadcast := withRoute(setup.ctx, "app-A", "")
	setup.assertOnline(t, ctxABroadcast, client.UserID, true)
}

// TestBitmap_MissFallbackZCount bitmap miss 兜底：bitmap 过期/淘汰后回退 scoped ZCount
//
// 验证 IsUserOnline 在 bitmap 不命中时（bit=0 或 bitmap key 被淘汰），
// 回退 zcountScoped 兜底，返回 ZSET 的真实在线状态，不误判离线。
// 场景：bitmap TTL 默认 8s，过期后 GETBIT=0，但 scoped ZSET 仍在线（TTL 5min 未过期）
func TestBitmap_MissFallbackZCount(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	client := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	defer setup.cleanup(client)

	ctx := withRoute(setup.ctx, client.AppID, client.Namespace)
	require.NoError(t, setup.repo.SetClientOnline(ctx, client))

	// 模拟 bitmap 过期/淘汰：直接 DEL scoped 和 global bitmap key
	redisClient := GetTestRedisClient(t)
	scopedBitmap := setup.repo.(*RedisOnlineStatusRepository).GetScopedBitmapKey("app-A", "ns-1")
	globalBitmap := setup.repo.(*RedisOnlineStatusRepository).GetGlobalBitmapKey("app-A")
	require.NoError(t, redisClient.Del(setup.ctx, scopedBitmap, globalBitmap).Err())

	// bitmap miss → 回退 zcountScoped → scoped ZSET 仍在线 → true（不误判离线）
	setup.assertOnline(t, ctx, client.UserID, true)
}

// TestBitmap_OffsetFirstAllocation offset 首次分配
//
// 验证 SetClientOnline 首次写入时 INCR/HSETNX 原子分配 offset，
// uid_map Hash 中应包含该 userID 字段，且值 < maxBitmapOffset。
// 同一 userID 多次上线 offset 不变（永久复用，避免 INCR 空洞累积）。
func TestBitmap_OffsetFirstAllocation(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	client := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	defer setup.cleanup(client)

	ctx := withRoute(setup.ctx, client.AppID, client.Namespace)
	require.NoError(t, setup.repo.SetClientOnline(ctx, client))

	redisClient := GetTestRedisClient(t)
	uidMapKey := setup.repo.(*RedisOnlineStatusRepository).GetUIDMapKey(client.UserID)
	offset, err := redisClient.HGet(setup.ctx, uidMapKey, client.UserID).Int64()
	require.NoError(t, err, "uid_map 应包含首次分配的 offset")
	assert.GreaterOrEqual(t, offset, int64(0))
	assert.Less(t, offset, int64(10_000_000), "offset 应在上限内")

	// 再次上线 offset 不变（永久复用）
	firstOffset := offset
	require.NoError(t, setup.repo.SetClientOffline(ctx, client))
	require.NoError(t, setup.repo.SetClientOnline(ctx, client))
	offset2, err := redisClient.HGet(setup.ctx, uidMapKey, client.UserID).Int64()
	require.NoError(t, err)
	assert.Equal(t, firstOffset, offset2, "同 userID 再次上线 offset 应不变")
}

// TestBitmap_BatchIsUserOnline 批量快速在线判定
//
// 验证 BatchIsUserOnline 返回 map 含全部查询的 userID，
// 在线=true，离线=false，未上线 userID 也在 map 中（值为 false）。
func TestBitmap_BatchIsUserOnline(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	c1 := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	c2 := newScopedTestClient(UserTypeAgent, "app-A", "ns-1")
	defer setup.cleanup(c1, c2)

	ctx := withRoute(setup.ctx, "app-A", "ns-1")
	require.NoError(t, setup.repo.SetClientOnline(ctx, c1))
	require.NoError(t, setup.repo.SetClientOnline(ctx, c2))

	// 查询：c1 在线、c2 在线、c3（陌生人）离线
	result, err := setup.repo.BatchIsUserOnline(ctx, []string{c1.UserID, c2.UserID, "non-existent-user"})
	require.NoError(t, err)
	assert.Len(t, result, 3, "全部查询的 userID 都应在 map 中")
	assert.True(t, result[c1.UserID])
	assert.True(t, result[c2.UserID])
	assert.False(t, result["non-existent-user"])
}

// TestBitmap_BatchSetClientsOfflineWithInfo 已知客户端信息批量下线
//
// 验证 BatchSetClientsOfflineWithInfo 不依赖 client:<id> key（即使 client 已过期也能清理 ZSET+bitmap）。
// 场景：SetClientOnline 后 DEL client:<id> key（模拟过期），再调 BatchSetClientsOfflineWithInfo，
// 应正确清理 scoped/unscoped ZSET + scoped/global bitmap，IsUserOnline 返回 false。
func TestBitmap_BatchSetClientsOfflineWithInfo(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	client := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	defer setup.cleanup(client)

	ctx := withRoute(setup.ctx, client.AppID, client.Namespace)
	require.NoError(t, setup.repo.SetClientOnline(ctx, client))
	setup.assertOnline(t, ctx, client.UserID, true)

	// 模拟 client:<id> key 已过期：直接 DEL
	redisClient := GetTestRedisClient(t)
	clientKey := setup.repo.(*RedisOnlineStatusRepository).GetClientKey(client.ID)
	require.NoError(t, redisClient.Del(setup.ctx, clientKey).Err())

	// BatchSetClientsOffline（依赖 client key 解压）会因 key 不存在而跳过该 client，
	// 无法清理 ZSET；BatchSetClientsOfflineWithInfo 用已知 client 信息直接清理，应成功
	require.NoError(t, setup.repo.BatchSetClientsOfflineWithInfo(ctx, []*Client{client}))
	setup.assertOnline(t, ctx, client.UserID, false)
}

// TestBitmap_GetUserClientsScopedKey scoped key 命中（无逐客户端过滤）
//
// 验证 bitmap 启用 + 路由信封时，GetUserClients 直接走 scoped ZSET，
// 无需逐客户端按 appID+ns 过滤（ZSET 已分桶）。
// 同名 userID 跨 ns 同时在线时，scoped 查询只返回当前信封的客户端。
func TestBitmap_GetUserClientsScopedKey(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	// 同名 userID 在 ns-1 和 ns-2 各一个客户端
	c1 := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	c2 := newScopedTestClient(UserTypeCustomer, "app-A", "ns-2")
	c2.UserID = c1.UserID // 强制同名
	defer setup.cleanup(c1, c2)

	ctx1 := withRoute(setup.ctx, "app-A", "ns-1")
	ctx2 := withRoute(setup.ctx, "app-A", "ns-2")
	require.NoError(t, setup.repo.SetClientOnline(ctx1, c1))
	require.NoError(t, setup.repo.SetClientOnline(ctx2, c2))

	// 查 ns-1：只返回 c1
	got1, err := setup.repo.GetUserClients(ctx1, c1.UserID)
	require.NoError(t, err)
	require.Len(t, got1, 1, "scoped key 命中应只返回 ns-1 的客户端")
	assert.Equal(t, c1.ID, got1[0].ID)

	// 查 ns-2：只返回 c2
	got2, err := setup.repo.GetUserClients(ctx2, c1.UserID)
	require.NoError(t, err)
	require.Len(t, got2, 1, "scoped key 命中应只返回 ns-2 的客户端")
	assert.Equal(t, c2.ID, got2[0].ID)
}

// TestBitmap_GetUserNodesScopedKey GetUserNodes 走 scoped key
//
// 验证 bitmap 启用 + 路由信封时，GetUserNodes 继承 GetUserClients 的 scoped 路径，
// 只返回当前信封的节点。
func TestBitmap_GetUserNodesScopedKey(t *testing.T) {
	setup := newBitmapTestRepoSetup(t, 5*time.Minute)
	c1 := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	c2 := newScopedTestClient(UserTypeCustomer, "app-A", "ns-2")
	c2.UserID = c1.UserID
	c2.NodeID = "node-ns-2-" + random.FRandAlphaString(10)
	defer setup.cleanup(c1, c2)

	ctx1 := withRoute(setup.ctx, "app-A", "ns-1")
	ctx2 := withRoute(setup.ctx, "app-A", "ns-2")
	require.NoError(t, setup.repo.SetClientOnline(ctx1, c1))
	require.NoError(t, setup.repo.SetClientOnline(ctx2, c2))

	// ns-1 查询：只返回 c1 的节点
	nodes1, err := setup.repo.GetUserNodes(ctx1, c1.UserID)
	require.NoError(t, err)
	require.Len(t, nodes1, 1)
	assert.Contains(t, nodes1, c1.NodeID)

	// ns-2 查询：只返回 c2 的节点
	nodes2, err := setup.repo.GetUserNodes(ctx2, c1.UserID)
	require.NoError(t, err)
	require.Len(t, nodes2, 1)
	assert.Contains(t, nodes2, c2.NodeID)
}

// ============================================================================
// Benchmark：bitmap 快速路径（L1 缓存 + GETBIT）
//
// 重要说明：
//   - 测试后端是 miniredis（进程内内存模拟），无网络往返，
//     且其 Lua 引擎为 gopher-lua（解释执行），远慢于生产 Redis 的 c-Lua。
//   - 生产 Redis 的核心收益在"网络往返次数"：首次查询 1 次 Lua（HGET+GETBIT），
//     L1 回填后仅 1 次 GETBIT，与该用户的客户端数 N 无关（恒定）。
//   - 验证方法：对比 N=1 与 N=50 两组，两组数据应基本持平（bitmap 只记 1 位）。
// ============================================================================

// setupBitmapBenchmark 准备 benchmark 数据
// clientCount 为同 userID+信封下的客户端数（模拟多设备）
func setupBitmapBenchmark(b *testing.B, clientCount int) (OnlineStatusRepository, context.Context, string) {
	b.Helper()
	b.Setenv("WSC_ONLINE_BITMAP_TTL", "60s")
	redisClient := GetTestRedisClientWithFlush(b)
	prefix := getTestIDGenerator().GenerateRequestID()
	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: fmt.Sprintf("%s:", prefix),
		TTL:       5 * time.Minute,
	})
	// 第一个客户端定义 userID + 路由信封，其余复用同 userID + 信封（模拟多设备）
	c1 := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
	ctx := withRoute(context.Background(), c1.AppID, c1.Namespace)
	clients := make([]*Client, clientCount)
	for i := range clients {
		if i == 0 {
			clients[i] = c1
		} else {
			c := newScopedTestClient(UserTypeCustomer, c1.AppID, c1.Namespace)
			c.UserID = c1.UserID // 同名 userID，不同 clientID/NodeID
			clients[i] = c
		}
	}
	if err := repo.BatchSetClientsOnline(ctx, clients); err != nil {
		b.Fatalf("BatchSetClientsOnline failed: %v", err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	return repo, ctx, c1.UserID
}

// BenchmarkIsUserOnline_1Client 1 客户端（首次查询走 Lua 回填 L1，后续 L1 命中直接 GETBIT）
func BenchmarkIsUserOnline_1Client(b *testing.B) {
	repo, ctx, userID := setupBitmapBenchmark(b, 1)
	for i := 0; i < b.N; i++ {
		if _, err := repo.IsUserOnline(ctx, userID); err != nil {
			b.Fatalf("IsUserOnline failed: %v", err)
		}
	}
}

// BenchmarkIsUserOnline_50Clients 50 客户端（同 userID 多设备）
// L1 命中后仅 1 次 GETBIT（bitmap 只记 1 位，与客户端数无关）
// 预期：与 1Client 组基本持平（恒定开销）
func BenchmarkIsUserOnline_50Clients(b *testing.B) {
	repo, ctx, userID := setupBitmapBenchmark(b, 50)
	for i := 0; i < b.N; i++ {
		if _, err := repo.IsUserOnline(ctx, userID); err != nil {
			b.Fatalf("IsUserOnline failed: %v", err)
		}
	}
}

// BenchmarkBatchIsUserOnline_50Users 批量快速判定 50 用户
// Pipeline 实现：L1 命中零网络（稳态 1 次 GETBIT 往返覆盖 50 用户）
func BenchmarkBatchIsUserOnline_50Users(b *testing.B) {
	b.Setenv("WSC_ONLINE_BITMAP_TTL", "60s")
	redisClient := GetTestRedisClientWithFlush(b)
	prefix := getTestIDGenerator().GenerateRequestID()
	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: fmt.Sprintf("%s:", prefix),
		TTL:       5 * time.Minute,
	})
	userIDs := make([]string, 50)
	for i := range userIDs {
		c := newScopedTestClient(UserTypeCustomer, "app-A", "ns-1")
		ctx := withRoute(context.Background(), c.AppID, c.Namespace)
		if err := repo.SetClientOnline(ctx, c); err != nil {
			b.Fatalf("SetClientOnline failed: %v", err)
		}
		userIDs[i] = c.UserID
	}
	ctx := withRoute(context.Background(), "app-A", "ns-1")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := repo.BatchIsUserOnline(ctx, userIDs); err != nil {
			b.Fatalf("BatchIsUserOnline failed: %v", err)
		}
	}
}

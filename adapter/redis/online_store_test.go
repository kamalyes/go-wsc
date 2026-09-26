/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 10:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 10:30:00
 * @FilePath: \go-wsc\adapter\redis\online_store_test.go
 * @Description: OnlineStore 在线仓库回归测试（三大主题合并）
 *
 *   1. 路由信封隔离：IsUserOnline/GetUserClients/GetUserNodes/BatchGetUserNodes
 *      按 ctx 路由信封 appID+namespace 过滤，避免同名 userID 跨租户误判。
 *
 *   2. 节点桶语义：nodes:{app}:{uid} 跨节点定位直查，单 key 双语义
 *      （scoped 前缀过滤 / 通配聚合），注销不 ZREM、死条目靠 score 过期自愈。
 *
 *   3. 全局聚合查询：GetAllOnlineUsers/GetOnlineCount/GetOnlineUsersByType/
 *      GetNodeClients 分桶遍历与 ZCOUNT 求和，ZRANGEBYSCORE 服务端过滤过期死条目。
 *
 * 刻意跑真实 miniredis 实现：这组方法断言的是 ZSET 分桶聚合、score 过期过滤与
 * 信封隔离语义，用内存替身只会测替身自身，拿不到实现正确性证据。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package redisadapter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 测试基建（共享 helper）
// ============================================================================

// setupOnlineIsolationRepo 创建 miniredis + 真实 OnlineStore，返回 repo 与关闭函数
//
// 刻意跑真实 Redis 实现而非内存替身：隔离/节点桶断言的是实现正确性，
// 用替身只会测出替身自身的行为。
func setupOnlineIsolationRepo(t *testing.T) (repo spi.OnlineStore, mr *miniredis.Miniredis) {
	t.Helper()
	mr = miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		DialTimeout:  5 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { _ = redisClient.Close() })
	repo = NewOnlineStore(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:iso:online:",
		TTL:       60 * time.Second,
	})
	return repo, mr
}

// setupOnlineStore 创建 miniredis + 具体 *OnlineStore（非接口），返回 repo 与 redis 客户端
//
// 返回具体类型以便访问私有 key 构造方法（同包），并用 client 手动注入过期死条目。
func setupOnlineStore(t *testing.T) (repo *OnlineStore, client *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	client = redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		DialTimeout:  5 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { _ = client.Close() })
	repo = NewOnlineStore(client, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:q:online:",
		TTL:       60 * time.Second,
	})
	return repo, client
}

// makeIsolationClient 创建带 appID/namespace/nodeID 的客户端
func makeIsolationClient(clientID, userID, appID, namespace, nodeID string) *models.Client {
	c := models.NewClient(clientID, userID, models.UserTypeCustomer)
	c.WithAppID(appID)
	c.WithNamespace(namespace)
	c.WithNodeInfo(nodeID, "127.0.0.1", 18080)
	c.ConnectionType = models.ConnectionTypeWebSocket
	return c
}

// scopedCtx 构建带路由信封的 ctx
func scopedCtx(appID, namespace string) context.Context {
	return routing.NewRoute().WithAppID(appID).WithNamespace(namespace).Inject(context.Background())
}

// clientIDOf 生成唯一 clientID（测试数据用）
func clientIDOf(i int) string {
	return fmt.Sprintf("client-%d", i)
}

// ============================================================================
// 主题一：路由信封隔离
// ============================================================================

// TestOnlineRepoIsolation_IsUserOnline 验证 IsUserOnline 按路由信封隔离
func TestOnlineRepoIsolation_IsUserOnline(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	// 两个相同 userID 的客户端，不同 appID+namespace
	clientA := makeIsolationClient("c-a", "shared-user", "app-A", "ns-A", "node-1")
	clientB := makeIsolationClient("c-b", "shared-user", "app-B", "ns-B", "node-2")
	require.NoError(t, repo.SetClientOnline(ctx, clientA))
	require.NoError(t, repo.SetClientOnline(ctx, clientB))

	// 按 app-A/ns-A 查询 → 在线
	online, err := repo.IsUserOnline(scopedCtx("app-A", "ns-A"), "shared-user")
	require.NoError(t, err)
	assert.True(t, online, "app-A/ns-A 信封下 shared-user 应在线")

	// 按 app-B/ns-B 查询 → 在线
	online, err = repo.IsUserOnline(scopedCtx("app-B", "ns-B"), "shared-user")
	require.NoError(t, err)
	assert.True(t, online, "app-B/ns-B 信封下 shared-user 应在线")

	// 按 app-A/ns-X 查询（namespace 不匹配）→ 离线
	online, err = repo.IsUserOnline(scopedCtx("app-A", "ns-X"), "shared-user")
	require.NoError(t, err)
	assert.False(t, online, "app-A/ns-X 信封下 shared-user 应离线（namespace 不匹配）")

	// 按 app-C/ns-C 查询（appID 不匹配）→ 离线
	online, err = repo.IsUserOnline(scopedCtx("app-C", "ns-C"), "shared-user")
	require.NoError(t, err)
	assert.False(t, online, "app-C/ns-C 信封下 shared-user 应离线（appID 不匹配）")
}

// TestOnlineRepoIsolation_GetUserClients 验证 GetUserClients 按路由信封过滤
func TestOnlineRepoIsolation_GetUserClients(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	clientA := makeIsolationClient("c-a", "shared-user", "app-A", "ns-A", "node-1")
	clientB := makeIsolationClient("c-b", "shared-user", "app-B", "ns-B", "node-2")
	require.NoError(t, repo.SetClientOnline(ctx, clientA))
	require.NoError(t, repo.SetClientOnline(ctx, clientB))

	// app-A/ns-A 信封 → 只返回 clientA
	clients, err := repo.GetUserClients(scopedCtx("app-A", "ns-A"), "shared-user")
	require.NoError(t, err)
	require.Len(t, clients, 1, "app-A/ns-A 信封应只返回 1 个客户端")
	assert.Equal(t, "c-a", clients[0].ID)

	// app-B/ns-B 信封 → 只返回 clientB
	clients, err = repo.GetUserClients(scopedCtx("app-B", "ns-B"), "shared-user")
	require.NoError(t, err)
	require.Len(t, clients, 1, "app-B/ns-B 信封应只返回 1 个客户端")
	assert.Equal(t, "c-b", clients[0].ID)

	// app-A/ns-X 信封（namespace 不匹配）→ 空列表
	clients, err = repo.GetUserClients(scopedCtx("app-A", "ns-X"), "shared-user")
	require.NoError(t, err)
	assert.Empty(t, clients, "app-A/ns-X 信封应返回空（namespace 不匹配）")
}

// TestOnlineRepoIsolation_GetUserNodes 验证 GetUserNodes 按路由信封返回正确节点
func TestOnlineRepoIsolation_GetUserNodes(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	// 同一 userID 在 node-1（app-A/ns-A）和 node-2（app-B/ns-B）
	clientA := makeIsolationClient("c-a", "shared-user", "app-A", "ns-A", "node-1")
	clientB := makeIsolationClient("c-b", "shared-user", "app-B", "ns-B", "node-2")
	require.NoError(t, repo.SetClientOnline(ctx, clientA))
	require.NoError(t, repo.SetClientOnline(ctx, clientB))

	// app-A/ns-A 信封 → 只返回 node-1
	nodes, err := repo.GetUserNodes(scopedCtx("app-A", "ns-A"), "shared-user")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1"}, nodes, "app-A/ns-A 信封应只返回 node-1")

	// app-B/ns-B 信封 → 只返回 node-2
	nodes, err = repo.GetUserNodes(scopedCtx("app-B", "ns-B"), "shared-user")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-2"}, nodes, "app-B/ns-B 信封应只返回 node-2")

	// app-C/ns-C 信封 → 空（无匹配）
	nodes, err = repo.GetUserNodes(scopedCtx("app-C", "ns-C"), "shared-user")
	require.NoError(t, err)
	assert.Empty(t, nodes, "app-C/ns-C 信封应返回空")
}

// TestOnlineRepoIsolation_BatchGetUserNodes 验证批量查询按路由信封隔离
func TestOnlineRepoIsolation_BatchGetUserNodes(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	// user1: app-A/ns-A on node-1, app-B/ns-B on node-2
	// user2: app-A/ns-A on node-3
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1a", "user1", "app-A", "ns-A", "node-1")))
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1b", "user1", "app-B", "ns-B", "node-2")))
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-2a", "user2", "app-A", "ns-A", "node-3")))

	// app-A/ns-A 信封批量查询
	result, err := repo.BatchGetUserNodes(scopedCtx("app-A", "ns-A"), []string{"user1", "user2", "user-nonexist"})
	require.NoError(t, err)

	// user1 → node-1（不含 node-2，因为 node-2 属于 app-B/ns-B）
	assert.ElementsMatch(t, []string{"node-1"}, result["user1"], "user1 在 app-A/ns-A 信封下应只返回 node-1")
	// user2 → node-3
	assert.ElementsMatch(t, []string{"node-3"}, result["user2"], "user2 在 app-A/ns-A 信封下应返回 node-3")
	// user-nonexist → 空切片（缓存空结果防止击穿）
	assert.Empty(t, result["user-nonexist"], "不存在的用户应返回空切片")

	// app-B/ns-B 信封 → user1 → node-2，user2 不在 map 中（app-B/ns-B 下无连接）
	result, err = repo.BatchGetUserNodes(scopedCtx("app-B", "ns-B"), []string{"user1", "user2"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-2"}, result["user1"], "user1 在 app-B/ns-B 信封下应只返回 node-2")
	_, exists := result["user2"]
	assert.False(t, exists, "user2 在 app-B/ns-B 信封下应不在结果中（无匹配连接）")
}

// TestOnlineRepoIsolation_NoRouteCtx_BackwardCompat 验证无路由信封时退化为不过滤（向后兼容）
func TestOnlineRepoIsolation_NoRouteCtx_BackwardCompat(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	clientA := makeIsolationClient("c-a", "shared-user", "app-A", "ns-A", "node-1")
	clientB := makeIsolationClient("c-b", "shared-user", "app-B", "ns-B", "node-2")
	require.NoError(t, repo.SetClientOnline(ctx, clientA))
	require.NoError(t, repo.SetClientOnline(ctx, clientB))

	// 无路由信封（普通 context.Background()）→ 返回全部客户端（向后兼容）
	clients, err := repo.GetUserClients(context.Background(), "shared-user")
	require.NoError(t, err)
	assert.Len(t, clients, 2, "无路由信封应返回全部客户端（向后兼容）")

	// IsUserOnline 无路由信封 → true
	online, err := repo.IsUserOnline(context.Background(), "shared-user")
	require.NoError(t, err)
	assert.True(t, online, "无路由信封应返回在线（向后兼容）")

	// GetUserNodes 无路由信封 → 收敛 DefaultAppID 域（两个连接均非 Default app，返回空）
	// 节点桶按 app 维度编码后天然收紧：旧语义返回跨 app 全部节点是隔离泄漏面，生产调用方均有信封
	nodes, err := repo.GetUserNodes(context.Background(), "shared-user")
	require.NoError(t, err)
	assert.Empty(t, nodes, "无路由信封应收敛 DefaultAppID 域，非 Default app 连接不返回（隔离收紧）")
}

// TestOnlineRepoIsolation_NamespaceBroadcast 验证 namespace 空值=全局广播语义
func TestOnlineRepoIsolation_NamespaceBroadcast(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineIsolationRepo(t)
	ctx := context.Background()

	// app-A 下两个不同 namespace 的客户端
	clientA := makeIsolationClient("c-a", "user-x", "app-A", "ns-A", "node-1")
	clientB := makeIsolationClient("c-b", "user-x", "app-A", "ns-B", "node-2")
	require.NoError(t, repo.SetClientOnline(ctx, clientA))
	require.NoError(t, repo.SetClientOnline(ctx, clientB))

	// app-A + namespace="" （全局广播）→ 匹配 app-A 下所有 namespace
	clients, err := repo.GetUserClients(scopedCtx("app-A", ""), "user-x")
	require.NoError(t, err)
	assert.Len(t, clients, 2, "app-A + 空 namespace（广播）应返回 app-A 下全部客户端")
}

// ============================================================================
// 主题二：节点桶（nodes:{app}:{uid}）语义
// ============================================================================

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

// ============================================================================
// 主题三：全局聚合查询
// ============================================================================

// TestGetAllOnlineUsers 验证分桶遍历返回全部在线 userID
func TestGetAllOnlineUsers(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineStore(t)
	ctx := context.Background()

	// 多个不同 userID（天然散落到不同桶，验证跨桶合并）
	userIDs := []string{"user-alice", "user-bob", "user-carol", "user-dave"}
	for i, uid := range userIDs {
		require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient(
			clientIDOf(i), uid, "app-x", "ns-x", "node-1",
		)))
	}

	users, err := repo.GetAllOnlineUsers(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, userIDs, users, "应返回全部在线 userID")
}

// TestGetAllOnlineUsers_FiltersExpired 验证 score 已过期的死条目被 ZRANGEBYSCORE 过滤
func TestGetAllOnlineUsers_FiltersExpired(t *testing.T) {
	t.Parallel()
	repo, client := setupOnlineStore(t)
	ctx := context.Background()

	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1", "user-alive", "app-x", "ns-x", "node-1")))

	// 手动向 all_users 桶注入 score 已过期的死条目（模拟心跳停止后未清理的残留）
	bucket := repo.keyBucket("user-alive")
	require.NoError(t, client.ZAdd(ctx, repo.allUsersBucketKey(bucket), redis.Z{
		Score:  float64(time.Now().Unix() - 100),
		Member: "user-stale",
	}).Err())

	users, err := repo.GetAllOnlineUsers(ctx)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user-alive"}, users, "过期死条目应被过滤")
}

// TestGetOnlineCount 验证分桶 ZCOUNT 求和返回正确总数
func TestGetOnlineCount(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineStore(t)
	ctx := context.Background()

	userIDs := []string{"user-1", "user-2", "user-3"}
	for i, uid := range userIDs {
		require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient(
			clientIDOf(i), uid, "app-x", "ns-x", "node-1",
		)))
	}

	count, err := repo.GetOnlineCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(len(userIDs)), count, "在线总数应与上线的唯一 userID 数一致")

	// 同一 userID 多设备在线不重复计数（all_users 桶 member=userID，ZSET 天然去重）
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-extra", "user-1", "app-x", "ns-x", "node-1")))
	count, err = repo.GetOnlineCount(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(len(userIDs)), count, "同 userID 多设备不应重复计数")
}

// TestGetOnlineUsersByType 验证按类型分桶查询只返回对应类型的在线用户
func TestGetOnlineUsersByType(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineStore(t)
	ctx := context.Background()

	// 两个 customer、一个 agent
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1", "user-cs-1", "app-x", "ns-x", "node-1")))
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-2", "user-cs-2", "app-x", "ns-x", "node-1")))
	agent := makeIsolationClient("c-3", "user-ag-1", "app-x", "ns-x", "node-1")
	agent.UserType = models.UserTypeAgent
	require.NoError(t, repo.SetClientOnline(ctx, agent))

	customers, err := repo.GetOnlineUsersByType(ctx, models.UserTypeCustomer)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user-cs-1", "user-cs-2"}, customers, "只应返回 customer 用户")

	agents, err := repo.GetOnlineUsersByType(ctx, models.UserTypeAgent)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"user-ag-1"}, agents, "只应返回 agent 用户")

	// 无该类型在线用户 → 空列表
	admins, err := repo.GetOnlineUsersByType(ctx, models.UserTypeAdmin)
	require.NoError(t, err)
	assert.Empty(t, admins, "无 admin 在线应返回空")
}

// TestGetNodeClients 验证按节点查询返回该节点全部客户端
func TestGetNodeClients(t *testing.T) {
	t.Parallel()
	repo, _ := setupOnlineStore(t)
	ctx := context.Background()

	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1", "user-1", "app-x", "ns-x", "node-1")))
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-2", "user-2", "app-x", "ns-x", "node-1")))
	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-3", "user-3", "app-x", "ns-x", "node-2")))

	clients, err := repo.GetNodeClients(ctx, "node-1")
	require.NoError(t, err)
	require.Len(t, clients, 2, "node-1 应返回 2 个客户端")
	assert.ElementsMatch(t, []string{"c-1", "c-2"}, []string{clients[0].ID, clients[1].ID})

	clients2, err := repo.GetNodeClients(ctx, "node-2")
	require.NoError(t, err)
	require.Len(t, clients2, 1, "node-2 应返回 1 个客户端")
	assert.Equal(t, "c-3", clients2[0].ID)

	// 无客户端节点 → 空列表
	clients3, err := repo.GetNodeClients(ctx, "node-empty")
	require.NoError(t, err)
	assert.Empty(t, clients3, "无客户端节点应返回空")
}

// TestGetNodeClients_FiltersExpired 验证 score 已过期的死条目被过滤、不再触发批量 GET
func TestGetNodeClients_FiltersExpired(t *testing.T) {
	t.Parallel()
	repo, client := setupOnlineStore(t)
	ctx := context.Background()

	require.NoError(t, repo.SetClientOnline(ctx, makeIsolationClient("c-1", "user-1", "app-x", "ns-x", "node-1")))

	// 手动向 node_clients 桶注入 score 已过期的死条目（其 client 详情 key 也不存在，若未被过滤会 GET 扑空）
	require.NoError(t, client.ZAdd(ctx, repo.nodeClientsKey("node-1"), redis.Z{
		Score:  float64(time.Now().Unix() - 100),
		Member: "stale-client",
	}).Err())

	clients, err := repo.GetNodeClients(ctx, "node-1")
	require.NoError(t, err)
	require.Len(t, clients, 1, "过期死条目应被过滤")
	assert.Equal(t, "c-1", clients[0].ID)
}
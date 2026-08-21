/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-22 00:00:00
 * @FilePath: \go-wsc\hub\online_status_isolation_test.go
 * @Description: 在线状态查询 appID+namespace 隔离回归测试
 *
 * 验证 onlineStatusRepo 的查询方法（IsUserOnline/GetUserClients/GetUserNodes/BatchGetUserNodes）
 * 按 ctx 路由信封的 appID+namespace 过滤，避免同名 userID 跨 app/ns 误判在线或返回错误节点。
 *
 * 核心场景：两个相同 userID 的客户端分别属于 app-A/ns-A 和 app-B/ns-B，
 * 查询时只应返回当前路由信封下的客户端/节点。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// setupOnlineIsolationRepo 创建 miniredis + onlineStatusRepo，返回 repo 与关闭函数
func setupOnlineIsolationRepo(t *testing.T) (repo OnlineStatusRepository, mr *miniredis.Miniredis) {
	t.Helper()
	mr = miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{
		Addr:         mr.Addr(),
		DialTimeout:  5 * time.Second,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	})
	t.Cleanup(func() { _ = redisClient.Close() })
	repo = newRedisOnlineStatusRepo(redisClient, "wsc:iso:online:", 60*time.Second)
	return repo, mr
}

// makeIsolationClient 创建带 appID/namespace/nodeID 的客户端
func makeIsolationClient(clientID, userID, appID, namespace, nodeID string) *Client {
	c := NewClient(clientID, userID, models.UserTypeCustomer)
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

	// IsUserOnline 无路由信封 → true（ZCount 快速路径，不过滤）
	online, err := repo.IsUserOnline(context.Background(), "shared-user")
	require.NoError(t, err)
	assert.True(t, online, "无路由信封应返回在线（向后兼容）")

	// GetUserNodes 无路由信封 → 返回全部节点
	nodes, err := repo.GetUserNodes(context.Background(), "shared-user")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"node-1", "node-2"}, nodes, "无路由信封应返回全部节点（向后兼容）")
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

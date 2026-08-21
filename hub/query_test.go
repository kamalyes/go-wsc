/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-08 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-08 00:09:16
 * @FilePath: \go-wsc\hub\query_test.go
 * @Description: Hub 查询/统计白盒单元测试（覆盖 hub/query.go）
 *
 * 复用 group_test.go 中的 setupGroupTestHub / makeTestClient / makeGroupMessage。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 共享测试 helper（SSE/Observer/Agent 客户端构造，供 query/sse/observer/broadcast 测试复用）
// ============================================================================

// makeSSEClient 构造 SSE 连接客户端（ConnectionType=SSE，含 SSEMessageCh）
func makeSSEClient(clientID, userID string) *Client {
	c := makeTestClient(clientID, userID)
	c.ConnectionType = ConnectionTypeSSE
	c.SSEMessageCh = make(chan *HubMessage, 16)
	return c
}

// makeObserverClient 构造观察者客户端（UserType=Observer）
// 默认创建全局观察者（Namespace="" 匹配任意命名空间），传 opts[0] 可指定命名空间级观察者
func makeObserverClient(clientID, userID string, opts ...string) *Client {
	c := makeTestClient(clientID, userID, opts...)
	c.UserType = UserTypeObserver
	if len(opts) == 0 {
		c.Namespace = "" // 全局观察者：匹配任意 namespace
	}
	return c
}

// makeAgentClient 构造客服客户端（UserType=Agent）
func makeAgentClient(clientID, userID string) *Client {
	c := makeTestClient(clientID, userID)
	c.UserType = UserTypeAgent
	return c
}

// ============================================================================
// 统计方法测试
// ============================================================================

func TestGetStats(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-s1", "u-s1")
	c2 := makeTestClient("c-s2", "u-s2")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	stats := hub.GetStats()
	require.NotNil(t, stats)
	assert.Equal(t, int64(2), stats.TotalClients)
	assert.Equal(t, 2, stats.OnlineUsers)
	// statsRepo 为 nil，详细计数为 0
	assert.Equal(t, int64(0), stats.MessagesSent)
	assert.GreaterOrEqual(t, stats.Uptime, int64(0))
}

func TestGetUptime(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// startTime 在 NewHub 时设置，uptime 应非负
	assert.GreaterOrEqual(t, hub.GetUptime(), int64(0))
}

func TestGetOnlineUsers(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-ou1", "u-ou1"))
	hub.shardedRegistry.AddClient(makeTestClient("c-ou2", "u-ou2"))

	users := hub.GetOnlineUsers()
	assert.ElementsMatch(t, []string{"u-ou1", "u-ou2"}, users)
}

func TestGetOnlineUsersCount(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-cnt1", "u-cnt1"))
	hub.shardedRegistry.AddClient(makeTestClient("c-cnt2", "u-cnt2"))
	hub.shardedRegistry.AddClient(makeTestClient("c-cnt3", "u-cnt1")) // 同用户多设备

	assert.Equal(t, 2, hub.GetOnlineUsersCount())
}

func TestGetOnlineUserCountByType(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	agent := makeAgentClient("c-agent", "u-agent")
	cust := makeTestClient("c-cust", "u-cust")
	cust2 := makeTestClient("c-cust2", "u-cust2")
	hub.shardedRegistry.AddClient(agent)
	hub.shardedRegistry.AddClient(cust)
	hub.shardedRegistry.AddClient(cust2)

	n, err := hub.GetOnlineUserCountByType(UserTypeCustomer)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	nAgent, err := hub.GetOnlineUserCountByType(UserTypeAgent)
	require.NoError(t, err)
	assert.Equal(t, int64(1), nAgent)
}

func TestGetClientsCount(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-cc1", "u-cc1"))
	hub.shardedRegistry.AddClient(makeTestClient("c-cc2", "u-cc2"))

	assert.Equal(t, int64(2), hub.GetClientsCount())
	assert.Equal(t, int64(2), hub.GetClientCount())
}

// ============================================================================
// 查询方法测试
// ============================================================================

func TestIsUserOnline(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-online", "u-online"))

	online, err := hub.IsUserOnline(context.Background(), "u-online")
	require.NoError(t, err)
	assert.True(t, online)

	offline, err := hub.IsUserOnline(context.Background(), "u-not-exist")
	require.NoError(t, err)
	assert.False(t, offline)
}

func TestGetClientByID(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c := makeTestClient("c-byid", "u-byid")
	hub.shardedRegistry.AddClient(c)

	got := hub.GetClientByID("c-byid")
	require.NotNil(t, got)
	assert.Equal(t, "u-byid", got.UserID)

	assert.Nil(t, hub.GetClientByID("nope"))
}

func TestGetClientsByUserID(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-m1", "u-multi"))
	hub.shardedRegistry.AddClient(makeTestClient("c-m2", "u-multi"))

	clients := hub.GetClientsByUserID(context.Background(), "u-multi")
	require.Len(t, clients, 2)
	assert.Nil(t, hub.GetClientsByUserID(context.Background(), "u-none"))
}

func TestGetUserStatus(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c := makeTestClient("c-st", "u-st")
	c.SetStatus(UserStatusBusy)
	hub.shardedRegistry.AddClient(c)

	assert.Equal(t, UserStatusBusy, hub.GetUserStatus(context.Background(), "u-st"))
	assert.Equal(t, UserStatusOffline, hub.GetUserStatus(context.Background(), "u-none"))
}

func TestGetClientIPs(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 客户端直接带 ClientIP
	c1 := makeTestClient("c-ip1", "u-ip")
	c1.ClientIP = "10.0.0.1"
	// 客户端通过 metadata 带 client_ip
	c2 := makeTestClient("c-ip2", "u-ip")
	c2.SetMetadataValue("client_ip", "10.0.0.2")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	ips := hub.GetClientIPs(context.Background(), "u-ip")
	assert.ElementsMatch(t, []string{"10.0.0.1", "10.0.0.2"}, ips)
	assert.Nil(t, hub.GetClientIPs(context.Background(), "u-none"))
}

func TestGetClientMetadata(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c := makeTestClient("c-md", "u-md")
	c.SetMetadataValue("k", "v")
	hub.shardedRegistry.AddClient(c)

	val, ok := hub.GetClientMetadata("c-md", "k")
	require.True(t, ok)
	assert.Equal(t, "v", val)

	_, ok = hub.GetClientMetadata("nope", "k")
	assert.False(t, ok)
}

func TestUpdateClientMetadata(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c := makeTestClient("c-umd", "u-umd")
	hub.shardedRegistry.AddClient(c)

	require.NoError(t, hub.UpdateClientMetadata("c-umd", "k2", "v2"))
	val, ok := hub.GetClientMetadata("c-umd", "k2")
	require.True(t, ok)
	assert.Equal(t, "v2", val)

	// 不存在的客户端返回错误
	err := hub.UpdateClientMetadata("nope", "k", "v")
	require.Error(t, err)
}

// ============================================================================
// 分组查询方法测试
// ============================================================================

func TestGetClientsByDepartmentGrouped(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-d1", "u-d1")
	c1.Department = Department("tech")
	c2 := makeTestClient("c-d2", "u-d2")
	c2.Department = Department("sales")
	c3 := makeTestClient("c-d3", "u-d3")
	c3.Department = Department("tech")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)
	hub.shardedRegistry.AddClient(c3)

	grouped := hub.GetClientsByDepartmentGrouped()
	assert.Len(t, grouped[Department("tech")], 2)
	assert.Len(t, grouped[Department("sales")], 1)
}

func TestGetClientsByUserTypeGrouped(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeAgentClient("c-ut1", "u-ut1"))
	hub.shardedRegistry.AddClient(makeTestClient("c-ut2", "u-ut2"))
	hub.shardedRegistry.AddClient(makeAgentClient("c-ut3", "u-ut3"))

	grouped := hub.GetClientsByUserTypeGrouped()
	assert.Len(t, grouped[UserTypeAgent], 2)
	assert.Len(t, grouped[UserTypeCustomer], 1)
}

func TestGetClientsByStatusGrouped(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-st1", "u-st1")
	c1.SetStatus(UserStatusBusy)
	c2 := makeTestClient("c-st2", "u-st2")
	c2.SetStatus(UserStatusOnline)
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	grouped := hub.GetClientsByStatusGrouped()
	assert.Len(t, grouped[UserStatusBusy], 1)
	assert.Len(t, grouped[UserStatusOnline], 1)
}

func TestGetClientsWithStatus(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-ws1", "u-ws1")
	c1.SetStatus(UserStatusAway)
	c2 := makeTestClient("c-ws2", "u-ws2")
	c2.SetStatus(UserStatusAway)
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	assert.Len(t, hub.GetClientsWithStatus(UserStatusAway), 2)
}

// ============================================================================
// 连接信息方法测试
// ============================================================================

func TestGetConnectionDetail(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c := makeTestClient("c-cd", "u-cd")
	hub.shardedRegistry.AddClient(c)

	require.NotNil(t, hub.GetConnectionDetail("c-cd"))
	assert.Nil(t, hub.GetConnectionDetail("nope"))
}

func TestGetClientStats(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c := makeTestClient("c-cst", "u-cst")
	hub.shardedRegistry.AddClient(c)

	stats := hub.GetClientStats("c-cst")
	require.NotNil(t, stats)
	assert.NotNil(t, stats["connection_info"])
	assert.Nil(t, hub.GetClientStats("nope"))
}

// ============================================================================
// 过滤和搜索方法测试
// ============================================================================

func TestFilterClients(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeAgentClient("c-f1", "u-f1"))
	hub.shardedRegistry.AddClient(makeTestClient("c-f2", "u-f2"))

	// nil predicate 返回空切片
	assert.Empty(t, hub.FilterClients(nil))

	agents := hub.FilterClients(func(c *Client) bool { return c.UserType == UserTypeAgent })
	assert.Len(t, agents, 1)
}

func TestGetMostRecentClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-mr1", "u-mr")
	c2 := makeTestClient("c-mr2", "u-mr")
	c1.SetLastSeen(time.Now().Add(-time.Minute))
	c2.SetLastSeen(time.Now())
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	got := hub.GetMostRecentClient(context.Background(), "u-mr")
	require.NotNil(t, got)
	assert.Equal(t, "c-mr2", got.ID)
	assert.Nil(t, hub.GetMostRecentClient(context.Background(), "u-none"))
}

func TestFindMostRecentClient(t *testing.T) {
	t.Run("空map返回nil", func(t *testing.T) {
		assert.Nil(t, findMostRecentClient(map[string]*Client{}))
	})
	t.Run("返回最近活跃客户端", func(t *testing.T) {
		c1 := makeTestClient("c1", "u1")
		c2 := makeTestClient("c2", "u2")
		c1.SetLastSeen(time.Now().Add(-time.Hour))
		c2.SetLastSeen(time.Now())
		got := findMostRecentClient(map[string]*Client{"c1": c1, "c2": c2})
		require.NotNil(t, got)
		assert.Equal(t, "c2", got.ID)
	})
}

func TestHasClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-hc", "u-hc"))
	assert.True(t, hub.HasClient("c-hc"))
	assert.False(t, hub.HasClient("nope"))
}

func TestHasUserClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-hu", "u-hu"))
	assert.True(t, hub.HasUserClient(context.Background(), "u-hu"))
	assert.False(t, hub.HasUserClient(context.Background(), "nope"))
}

func TestHasSSEClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeSSEClient("c-sse", "u-sse"))
	assert.True(t, hub.HasSSEClient("u-sse"))
	assert.False(t, hub.HasSSEClient("u-none"))
}

func TestHasAgentClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeAgentClient("c-ag", "u-ag"))
	assert.True(t, hub.HasAgentClient("u-ag"))
	assert.False(t, hub.HasAgentClient("u-none"))
}

func TestGetClientsCopy(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-cc1", "u-cc1"))
	hub.shardedRegistry.AddClient(makeTestClient("c-cc2", "u-cc2"))
	assert.Len(t, hub.GetClientsCopy(), 2)
}

func TestGetUserClientsCopy(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-uc1", "u-uc1"))
	hub.shardedRegistry.AddClient(makeTestClient("c-uc2", "u-uc2"))
	// 同用户两设备，取最近活跃
	c3 := makeTestClient("c-uc3", "u-uc1")
	c3.SetLastSeen(time.Now().Add(time.Hour))
	hub.shardedRegistry.AddClient(c3)

	clients := hub.GetUserClientsCopy()
	assert.Len(t, clients, 2)
}

func TestGetUserClientsMapWithLock(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-ml", "u-ml"))

	m, ok := hub.GetUserClientsMapWithLock("u-ml")
	require.True(t, ok)
	assert.Contains(t, m, "c-ml")

	_, ok = hub.GetUserClientsMapWithLock("u-none")
	assert.False(t, ok)
}

func TestGetClientsCopyForUser(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-cp1", "u-cp")
	c2 := makeTestClient("c-cp2", "u-cp")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	t.Run("指定clientID仅返回该客户端", func(t *testing.T) {
		clients := hub.GetClientsCopyForUser(context.Background(), "u-cp", "c-cp1")
		require.Len(t, clients, 1)
		assert.Equal(t, "c-cp1", clients[0].ID)
	})
	t.Run("clientID不匹配返回nil", func(t *testing.T) {
		assert.Nil(t, hub.GetClientsCopyForUser(context.Background(), "u-cp", "nope"))
	})
	t.Run("未指定clientID返回所有", func(t *testing.T) {
		assert.Len(t, hub.GetClientsCopyForUser(context.Background(), "u-cp", ""), 2)
	})
	t.Run("不存在用户返回nil", func(t *testing.T) {
		assert.Nil(t, hub.GetClientsCopyForUser(context.Background(), "u-none", ""))
	})
}

func TestGetConnectionsByUserID(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-cb1", "u-cb"))
	hub.shardedRegistry.AddClient(makeTestClient("c-cb2", "u-cb"))

	assert.Len(t, hub.GetConnectionsByUserID(context.Background(), "u-cb"), 2)
	assert.Nil(t, hub.GetConnectionsByUserID(context.Background(), "u-none"))
}

func TestCheckUserOnline(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-co", "u-co"))
	assert.True(t, hub.checkUserOnline(context.Background(), "u-co"))
	// 无 onlineStatusRepo，离线用户返回 false
	assert.False(t, hub.checkUserOnline(context.Background(), "u-none"))
}

func TestGetClientByIDWithLock(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-cl", "u-cl"))
	c, ok := hub.GetClientByIDWithLock("c-cl")
	require.True(t, ok)
	assert.NotNil(t, c)
	_, ok = hub.GetClientByIDWithLock("nope")
	assert.False(t, ok)
}

func TestGetHubHealth(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c-hh", "u-hh"))
	hub.shardedRegistry.AddClient(makeSSEClient("c-hh-sse", "u-hh-sse"))

	health := hub.GetHubHealth()
	require.NotNil(t, health)
	assert.Equal(t, "healthy", health.Status)
	assert.Equal(t, 2, health.TotalConnections)
	assert.Equal(t, 1, health.SSECount)
	assert.Equal(t, 1, health.WebSocketCount)
	assert.Equal(t, hub.nodeID, health.NodeID)
}

func TestGetOnlineUsersByType(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeAgentClient("c-ob1", "u-ob1"))
	hub.shardedRegistry.AddClient(makeAgentClient("c-ob2", "u-ob2"))
	hub.shardedRegistry.AddClient(makeTestClient("c-ob3", "u-ob3"))

	users, err := hub.GetOnlineUsersByType(UserTypeAgent)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u-ob1", "u-ob2"}, users)
}

func TestCopyClientsFromMap(t *testing.T) {
	c1 := makeTestClient("c-cm1", "u-cm1")
	c2 := makeTestClient("c-cm2", "u-cm2")
	clients := CopyClientsFromMap(map[string]*Client{"c-cm1": c1, "c-cm2": c2})
	assert.Len(t, clients, 2)
	assert.Empty(t, CopyClientsFromMap(map[string]*Client{}))
}

// 避免 unused import（context 在其它测试中使用）
var _ = context.Background
var _ = models.DefaultNamespace

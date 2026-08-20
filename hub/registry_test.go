/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-01 17:26:30
 * @FilePath: \go-wsc\hub\registry_test.go
 * @Description: 客户端注册测试（含节点总连接数硬限制）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWSConnPair 建立一对真实 WebSocket 连接（服务端 + 客户端），用于测试
// 返回的 serverConn 通常作为 Client.Conn；clientConn 用于验证连接行为（如关闭）
func newWSConnPair(t *testing.T) (serverConn, clientConn *websocket.Conn) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverConnCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	t.Cleanup(srv.Close)

	var err error
	clientConn, _, err = websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http://", "ws://", 1), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case serverConn = <-serverConnCh:
		require.NotNil(t, serverConn)
	case <-time.After(2 * time.Second):
		t.Fatal("等待服务端 WebSocket 升级超时")
	}
	t.Cleanup(func() { _ = serverConn.Close() })
	return serverConn, clientConn
}

// newTestClient 构造一个用于测试的 Client（带真实 WebSocket 连接）
func newTestClient(id, userID string, conn *websocket.Conn) *Client {
	return &Client{
		ID:             id,
		UserID:         userID,
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		Context:        context.Background(),
		Metadata:       make(map[string]interface{}),
		Conn:           conn,
	}
}

// TestHandleRegisterMaxConnectionsLimit 测试节点总连接数硬限制
// MaxConnectionsPerNode=2 时：前 2 个注册成功，第 3 个被拒绝（发 Close 帧 1013 并关闭连接，未入注册表）
func TestHandleRegisterMaxConnectionsLimit(t *testing.T) {
	config := wscconfig.Default()
	if config.Performance == nil {
		config.Performance = &wscconfig.Performance{}
	}
	config.Performance.MaxConnectionsPerNode = 2

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	hub.WaitForStart()

	// 注册 2 个客户端（达到上限，使用不同 userID 避免触发单用户连接数限制）
	for i := 0; i < 2; i++ {
		sConn, _ := newWSConnPair(t)
		client := newTestClient(fmt.Sprintf("client-%d", i), fmt.Sprintf("user-%d", i), sConn)
		hub.Register(client)
		require.Eventually(t, func() bool {
			return hub.HasClient(client.ID)
		}, 2*time.Second, 20*time.Millisecond, "第 %d 个客户端应注册成功", i+1)
	}
	require.Equal(t, int64(2), hub.GetClientCount(), "应已注册 2 个客户端")

	// 第 3 个应被节点总连接数硬限制拒绝
	sConn3, cConn3 := newWSConnPair(t)
	rejected := newTestClient("client-rejected", "user-rejected", sConn3)
	hub.Register(rejected)

	// 验证连接被关闭：对端应收到 Close 帧(1013 Try Again Later) 或连接错误
	cConn3.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := cConn3.ReadMessage()
	require.Error(t, err, "被拒绝的连接应被关闭")
	if ce, ok := err.(*websocket.CloseError); ok {
		assert.Equal(t, websocket.CloseTryAgainLater, ce.Code, "应收到 1013 Try Again Later close 帧")
	}

	// 验证被拒绝的客户端未入注册表，连接数仍为 2
	assert.False(t, hub.HasClient(rejected.ID), "被拒绝的客户端不应进入注册表")
	assert.Equal(t, int64(2), hub.GetClientCount(), "连接数应保持为 2")
}

// TestHandleRegisterMaxConnectionsZeroUnlimited 测试 MaxConnectionsPerNode=0 时不限制
func TestHandleRegisterMaxConnectionsZeroUnlimited(t *testing.T) {
	config := wscconfig.Default()
	if config.Performance == nil {
		config.Performance = &wscconfig.Performance{}
	}
	config.Performance.MaxConnectionsPerNode = 0

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	hub.WaitForStart()

	// 注册 5 个客户端（不同用户），0 表示不限制，应全部成功
	for i := 0; i < 5; i++ {
		sConn, _ := newWSConnPair(t)
		client := newTestClient(fmt.Sprintf("client-%d", i), fmt.Sprintf("user-%d", i), sConn)
		hub.Register(client)
	}
	require.Eventually(t, func() bool {
		return hub.GetClientCount() == 5
	}, 2*time.Second, 20*time.Millisecond, "0 表示不限制,5 个客户端应全部注册成功")
}

// TestGetMaxConnectionsPerNode 测试辅助方法对 nil 配置的安全性
func TestGetMaxConnectionsPerNode(t *testing.T) {
	t.Run("正常配置", func(t *testing.T) {
		config := wscconfig.Default()
		if config.Performance == nil {
			config.Performance = &wscconfig.Performance{}
		}
		config.Performance.MaxConnectionsPerNode = 3000
		hub := NewHub(config)
		assert.Equal(t, 3000, hub.GetMaxConnectionsPerNode())
	})

	t.Run("Performance 为 nil", func(t *testing.T) {
		config := wscconfig.Default()
		config.Performance = nil
		hub := NewHub(config)
		assert.Equal(t, 0, hub.GetMaxConnectionsPerNode(), "Performance 为 nil 时应返回 0（不限制）")
	})
}

// TestShardedRegistryAddClientOverwriteNoDoubleCount 验证断线重连覆盖场景下计数器不重复累加。
//
// 背景：相同 ClientID 的新客户端覆盖旧客户端时，旧客户端的 map 条目被覆盖（map 大小不变），
// 若 clientCount/sseCount 仍无条件 +1，会随每次重连持续膨胀、与实际连接数脱节
// （表现为"活跃连接数"远大于 netstat ESTABLISHED 连接数）
// 旧客户端读协程退出后，removeClientUnsafe 会执行"删除当前条目-回填新客户端"流程，
// 该流程后计数器必须与实际 map 条目数一致
func TestShardedRegistryAddClientOverwriteNoDoubleCount(t *testing.T) {
	makeClient := func(id, userID string, connType ConnectionType, userType UserType) *Client {
		return &Client{
			ID:             id,
			UserID:         userID,
			UserType:       userType,
			Status:         UserStatusOnline,
			ConnectionType: connType,
			Context:        context.Background(),
			Metadata:       make(map[string]interface{}),
		}
	}

	t.Run("WebSocket 覆盖不重复计数", func(t *testing.T) {
		reg := NewShardedRegistry(true, true, RegistryCapacity{})

		c1 := makeClient("client-X", "user-U", ConnectionTypeWebSocket, UserTypeCustomer)
		reg.AddClient(c1)
		assert.Equal(t, int64(1), reg.GetClientCount(), "首次注册后 clientCount 应为 1")
		assert.Equal(t, int64(1), reg.GetUserCount(), "userCount 应为 1")

		// 断线重连：相同 ClientID 的新客户端覆盖 c1
		c2 := makeClient("client-X", "user-U", ConnectionTypeWebSocket, UserTypeCustomer)
		reg.AddClient(c2)
		assert.Equal(t, int64(1), reg.GetClientCount(), "覆盖场景 clientCount 不应重复累加")
		assert.Equal(t, int64(1), reg.GetUserCount(), "userCount 应保持为 1")

		// 模拟 removeClientUnsafe 的"删除-回填"流程
		removed := reg.RemoveClient(c2.ID, c2.UserID)
		require.Equal(t, c2, removed, "应返回当前注册的新客户端 c2")
		assert.Equal(t, int64(0), reg.GetClientCount(), "删除后 clientCount 应为 0")
		reg.AddClient(removed) // 回填新客户端
		assert.Equal(t, int64(1), reg.GetClientCount(), "回填后 clientCount 应恢复为 1")

		// 多轮重连后计数器仍应准确
		for i := 0; i < 10; i++ {
			reg.AddClient(makeClient("client-X", "user-U", ConnectionTypeWebSocket, UserTypeCustomer))
		}
		assert.Equal(t, int64(1), reg.GetClientCount(), "10 轮重连后 clientCount 仍应为 1")
		assert.Equal(t, int64(1), reg.GetUserCount(), "10 轮重连后 userCount 仍应为 1")
	})

	t.Run("SSE 覆盖不重复计数", func(t *testing.T) {
		reg := NewShardedRegistry(true, true, RegistryCapacity{})

		s1 := makeClient("sse-X", "user-S", ConnectionTypeSSE, UserTypeCustomer)
		reg.AddClient(s1)
		assert.Equal(t, int64(1), reg.GetSSEClientCount(), "SSE 首次注册 sseCount 应为 1")
		assert.Equal(t, int64(1), reg.GetClientCount(), "SSE 首次注册 clientCount 应为 1")

		// 相同 ClientID 的 SSE 客户端覆盖
		s2 := makeClient("sse-X", "user-S", ConnectionTypeSSE, UserTypeCustomer)
		reg.AddClient(s2)
		assert.Equal(t, int64(1), reg.GetSSEClientCount(), "SSE 覆盖场景 sseCount 不应重复累加")
		assert.Equal(t, int64(1), reg.GetClientCount(), "SSE 覆盖场景 clientCount 不应重复累加")
	})
}

// ============================================================================
// 注册 / 重连 / 注销 / 踢人 核心路径白盒测试
// ============================================================================

// newTestHubConfig 构造测试用 Hub 配置（节点 127.0.0.1:18080，30s 心跳，256 消息缓冲）
func newTestHubConfig() *wscconfig.WSC {
	return wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
}

// waitForConnClosed 轮询等待 WebSocket 连接被对端关闭（读到非超时错误即视为已关闭）
// 用于断言 handleRegister/kick/Disconnect 等路径关闭了底层连接，避免裸 sleep
func waitForConnClosed(t *testing.T, conn *websocket.Conn, timeout time.Duration) {
	t.Helper()
	require.Eventually(t, func() bool {
		_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, _, err := conn.ReadMessage()
		if err == nil {
			return false
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			return false
		}
		return true
	}, timeout, 10*time.Millisecond)
}

// startTestHub 构造并启动一个测试 Hub，返回 Hub 与关闭函数
func startTestHub(t *testing.T, config *wscconfig.WSC) (*Hub, func()) {
	t.Helper()
	hub := NewHub(config)
	go hub.Run()
	hub.WaitForStart()
	return hub, func() { _ = hub.SafeShutdown() }
}

// TestRegisterRejectedWhenShutdown 验证 Hub 已 shutdown 时 handleRegister 拒绝注册：关闭 conn 且不进注册表
func TestRegisterRejectedWhenShutdown(t *testing.T) {
	hub := NewHub(newTestHubConfig())
	go hub.Run()
	hub.WaitForStart()
	// 先关闭 Hub，再尝试注册
	require.NoError(t, hub.SafeShutdown())

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("rejected-client", "rejected-user", sConn)

	// EventLoop 已停止，直接调用 handleRegister 验证 shutdown 拒绝分支
	require.NotPanics(t, func() { hub.handleRegister(client) })

	assert.False(t, hub.HasClient(client.ID), "shutdown 后注册的客户端不应进入注册表")
	assert.Equal(t, int64(0), hub.GetClientCount(), "计数应保持为 0")
	// 连接应被关闭
	waitForConnClosed(t, cConn, 2*time.Second)
}

// TestRegisterNormalPath 验证正常注册后客户端入表（同指针）、计数增加且日志路径不 panic
func TestRegisterNormalPath(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newTestClient("normal-client", "normal-user", nil)
	require.NotPanics(t, func() { hub.Register(client) })

	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	got, ok := hub.GetClientByIDWithLock(client.ID)
	require.True(t, ok, "客户端应已注册")
	assert.Same(t, client, got, "注册表中的客户端应为同一指针")
	assert.Equal(t, int64(1), hub.GetClientCount(), "计数应为 1")
}

// TestMultiLoginDisallowKicksOld 验证 AllowMultiLogin=false 时同 userID 新连接挤掉旧连接（旧 conn 关闭、从注册表移除）
func TestMultiLoginDisallowKicksOld(t *testing.T) {
	config := newTestHubConfig()
	config.AllowMultiLogin = false
	hub, shutdown := startTestHub(t, config)
	defer shutdown()

	sConnA, cConnA := newWSConnPair(t)
	clientA := newTestClient("client-A", "user-U", sConnA)
	hub.Register(clientA)
	// -race 全量套件下 EventLoop 可能被拖慢，放宽到 5s
	require.Eventually(t, func() bool { return hub.HasClient(clientA.ID) }, 5*time.Second, 10*time.Millisecond)

	sConnB, _ := newWSConnPair(t)
	clientB := newTestClient("client-B", "user-U", sConnB)
	hub.Register(clientB)

	// 旧连接被踢出，新连接入表，计数不膨胀
	require.Eventually(t, func() bool {
		return hub.HasClient(clientB.ID) && !hub.HasClient(clientA.ID) && hub.GetClientCount() == 1
	}, 5*time.Second, 10*time.Millisecond)

	// 旧连接应被关闭
	waitForConnClosed(t, cConnA, 5*time.Second)
}

// TestMultiLoginMaxConnectionsPerUserKicksOldest 验证 MaxConnectionsPerUser 限制下超出时踢掉最旧连接
func TestMultiLoginMaxConnectionsPerUserKicksOldest(t *testing.T) {
	config := newTestHubConfig()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 2
	hub, shutdown := startTestHub(t, config)
	defer shutdown()

	now := time.Now()
	mk := func(id string, hb time.Time) *Client {
		c := newTestClient(id, "user-U", nil)
		c.SetLastHeartbeat(hb)
		return c
	}

	c1 := mk("c1", now.Add(-2*time.Hour)) // 最旧
	hub.Register(c1)
	require.Eventually(t, func() bool { return hub.HasClient("c1") }, 2*time.Second, 10*time.Millisecond)

	c2 := mk("c2", now.Add(-1*time.Hour))
	hub.Register(c2)
	require.Eventually(t, func() bool { return hub.HasClient("c2") }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(2), hub.GetClientCount())

	c3 := mk("c3", now) // 最新，触发踢最旧
	hub.Register(c3)

	// c3 触发踢最旧：c1 被踢，c2/c3 保留，计数仍为 2
	require.Eventually(t, func() bool {
		return !hub.HasClient("c1") && hub.HasClient("c2") && hub.HasClient("c3") && hub.GetClientCount() == 2
	}, 2*time.Second, 10*time.Millisecond)
}

// TestReconnectSameClientIDReplacesOld 验证相同 ClientID 断线重连替换旧连接（旧 conn 关闭、新 client 覆盖 map、计数不膨胀）
func TestReconnectSameClientIDReplacesOld(t *testing.T) {
	config := newTestHubConfig()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0 // 无限制，避免触发 max-conn 踢人
	hub, shutdown := startTestHub(t, config)
	defer shutdown()

	sConn1, cConn1 := newWSConnPair(t)
	oldClient := newTestClient("client-X", "user-U", sConn1)
	hub.Register(oldClient)
	require.Eventually(t, func() bool { return hub.HasClient("client-X") }, 2*time.Second, 10*time.Millisecond)

	sConn2, _ := newWSConnPair(t)
	newClient := newTestClient("client-X", "user-U", sConn2)
	hub.Register(newClient)

	// 新 client 覆盖 map 条目
	require.Eventually(t, func() bool {
		got, ok := hub.GetClientByIDWithLock("client-X")
		return ok && got == newClient
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, int64(1), hub.GetClientCount(), "重连替换后计数不应膨胀")
	// 旧 client 通道应被关闭，新 client 通道保持开启
	require.Eventually(t, func() bool { return oldClient.IsClosed() }, 2*time.Second, 10*time.Millisecond)
	assert.False(t, newClient.IsClosed(), "新 client 通道不应被关闭")
	// 旧连接应被关闭
	waitForConnClosed(t, cConn1, 2*time.Second)
}

// TestUnregisterNormal 验证正常注销后计数减少、客户端通道关闭
func TestUnregisterNormal(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newTestClient("unreg-client", "unreg-user", nil)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(1), hub.GetClientCount())

	hub.Unregister(client)

	require.Eventually(t, func() bool {
		return !hub.HasClient(client.ID) && hub.GetClientCount() == 0 && client.IsClosed()
	}, 2*time.Second, 10*time.Millisecond, "注销后客户端应移除、计数归零且通道关闭")
	assert.True(t, client.IsClosed(), "客户端发送通道应被关闭")
}

// TestRemoveClientUnsafeNotRegistered 验证对未注册 client 调用 removeClientUnsafe（Unregister 的同步核心）早返回、计数不变
func TestRemoveClientUnsafeNotRegistered(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	registered := makeTestClient("registered", "user-A")
	hub.shardedRegistry.AddClient(registered)
	require.Equal(t, int64(1), hub.GetClientCount())

	unregistered := makeTestClient("ghost", "user-B")
	require.NotPanics(t, func() { hub.removeClientUnsafe(unregistered) })

	assert.Equal(t, int64(1), hub.GetClientCount(), "未注册 client 注销不应改变计数")
	assert.True(t, hub.HasClient("registered"), "已注册客户端应仍在线")
}

// TestRemoveClientUnsafeReconnectRaceBackfill 验证重连竞争：旧 client 注销时 map 已是新 client，回填后新 client 仍在、计数不变
func TestRemoveClientUnsafeReconnectRaceBackfill(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	oldClient := makeTestClient("client-X", "user-U")
	hub.shardedRegistry.AddClient(oldClient)

	newClient := makeTestClient("client-X", "user-U") // 相同 ClientID，覆盖
	hub.shardedRegistry.AddClient(newClient)

	require.Equal(t, int64(1), hub.GetClientCount())
	got, ok := hub.GetClientByIDWithLock("client-X")
	require.True(t, ok)
	require.Same(t, newClient, got, "覆盖后注册表应持有新 client")

	// 旧 client 的读协程退出时调用 removeClientUnsafe(oldClient)
	require.NotPanics(t, func() { hub.removeClientUnsafe(oldClient) })

	// 回填后新 client 仍在，计数不变
	got2, ok2 := hub.GetClientByIDWithLock("client-X")
	require.True(t, ok2, "回填后新 client 应仍在注册表")
	require.Same(t, newClient, got2, "注册表仍应持有新 client")
	assert.Equal(t, int64(1), hub.GetClientCount(), "回填后计数应保持为 1")
	assert.False(t, newClient.IsClosed(), "新 client 通道不应被关闭")
}

// TestKickUserUserNotOnline 验证 KickUser 对不在线用户返回错误
func TestKickUserUserNotOnline(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	result := hub.KickUser("ghost-user", "test reason", false, "")
	assert.False(t, result.Success, "不在线用户踢出应失败")
	require.Error(t, result.Error, "应返回错误")
	assert.Equal(t, 0, result.KickedConnections, "不应有连接被踢")
}

// TestKickUserOnlineRemoved 验证 KickUser 在线用户被踢出注册表
func TestKickUserOnlineRemoved(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newTestClient("kick-client", "kick-user", nil)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	result := hub.KickUser("kick-user", "force", false, "")
	assert.True(t, result.Success, "在线用户踢出应成功")
	assert.Equal(t, 1, result.KickedConnections, "应踢出 1 个连接")
	require.NoError(t, result.Error)

	require.Eventually(t, func() bool {
		return !hub.HasClient(client.ID) && hub.GetClientCount() == 0 && client.IsClosed()
	}, 2*time.Second, 10*time.Millisecond, "被踢客户端应从注册表移除且通道关闭")
	assert.True(t, client.IsClosed(), "被踢客户端通道应被关闭")
}

// TestKickUserSimpleOnlineReturnsCount 验证 KickUserSimple 返回被踢连接数且客户端移除
func TestKickUserSimpleOnlineReturnsCount(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newTestClient("simple-client", "simple-user", nil)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	kicked := hub.KickUserSimple("simple-user", "simple reason")
	assert.Equal(t, 1, kicked, "应踢出 1 个连接")

	require.Eventually(t, func() bool { return !hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)
}

// TestKickUserWithMessageOnlineReturnsNil 验证 KickUserWithMessage 在线用户返回 nil 错误、投递踢出通知并移除
func TestKickUserWithMessageOnlineReturnsNil(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newTestClient("msg-client", "msg-user", nil)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)
	require.NotNil(t, client.SendChan, "注册后 SendChan 应已初始化")

	err := hub.KickUserWithMessage("msg-user", "by msg", "you are kicked")
	require.NoError(t, err, "在线用户踢出应返回 nil")

	// 应向 SendChan 投递 kick_out 通知（通知在 Unregister 关闭通道前同步投递，缓冲消息可读取）
	select {
	case data := <-client.SendChan:
		assert.Contains(t, string(data), "kick_out", "应收到 kick_out 通知消息")
	case <-time.After(2 * time.Second):
		t.Fatal("未收到踢出通知消息")
	}

	require.Eventually(t, func() bool { return !hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)
}

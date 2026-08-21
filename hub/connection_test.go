/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-06-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-25 10:56:20
 * @FilePath: \go-wsc\hub\connection_test.go
 * @Description: Hub 连接管理白盒测试（hub/connection.go）
 *   - closeClientChannel / closeClientConnection 幂等与 nil 安全
 *   - DisconnectClient / DisconnectUser 主动断开
 *   - ResetClientStatus 状态重置
 *   - kickExistingClients / kickOldestConnection / kickClientWithNotification 踢人内部路径
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
)

// TestCloseClientChannelIdempotent 验证 closeClientChannel 幂等：多次调用不 panic、不重复关闭通道
func TestCloseClientChannelIdempotent(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	client := makeTestClient("c1", "u1")
	require.False(t, client.IsClosed())

	require.NotPanics(t, func() { hub.closeClientChannel(client) })
	assert.True(t, client.IsClosed(), "首次关闭后应标记为已关闭")

	// 通道应已关闭：读取返回零值与 false
	_, ok := <-client.SendChan
	assert.False(t, ok, "SendChan 应已关闭")

	// 重复关闭不应 panic、不应二次关闭
	require.NotPanics(t, func() {
		hub.closeClientChannel(client)
		hub.closeClientChannel(client)
	})
	assert.True(t, client.IsClosed())
}

// TestCloseClientConnectionNilConn 验证 closeClientConnection 在 conn 为 nil 时不 panic
func TestCloseClientConnectionNilConn(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	client := makeTestClient("c1", "u1")
	require.Nil(t, client.Conn, "makeTestClient 不应设置 Conn")

	require.NotPanics(t, func() { hub.closeClientConnection(client) })
}

// TestDisconnectClientOnlineThenRemoved 验证 DisconnectClient 关闭在线 client 连接后其被移除
func TestDisconnectClientOnlineThenRemoved(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("disc-client", "disc-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	require.NoError(t, hub.DisconnectClient(client.ID, "test disconnect"))

	// 连接关闭后读协程退出并触发 Unregister，最终从注册表移除
	waitForConnClosed(t, cConn, 2*time.Second)
	require.Eventually(t, func() bool {
		return !hub.HasClient(client.ID) && hub.GetClientCount() == 0
	}, 2*time.Second, 10*time.Millisecond, "DisconnectClient 后客户端应被移除")
}

// TestDisconnectClientNotFound 验证 DisconnectClient 对不存在的 client 返回错误且不改变计数
func TestDisconnectClientNotFound(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	err := hub.DisconnectClient("nonexistent-client", "reason")
	require.Error(t, err, "不存在的 client 应返回错误")
	assert.Equal(t, int64(0), hub.GetClientCount())
}

// TestDisconnectUserRemovesAll 验证 DisconnectUser 移除该 userID 的所有 client
func TestDisconnectUserRemovesAll(t *testing.T) {
	config := newTestHubConfig()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0
	hub, shutdown := startTestHub(t, config)
	defer shutdown()

	sConn1, cConn1 := newWSConnPair(t)
	c1 := newTestClient("du-1", "du-user", sConn1)
	hub.Register(c1)
	require.Eventually(t, func() bool { return hub.HasClient("du-1") }, 2*time.Second, 10*time.Millisecond)

	sConn2, cConn2 := newWSConnPair(t)
	c2 := newTestClient("du-2", "du-user", sConn2)
	hub.Register(c2)
	require.Eventually(t, func() bool { return hub.HasClient("du-2") }, 2*time.Second, 10*time.Millisecond)
	require.Equal(t, int64(2), hub.GetClientCount())

	require.NoError(t, hub.DisconnectUser(context.Background(), "du-user", "shutdown user"))

	waitForConnClosed(t, cConn1, 2*time.Second)
	waitForConnClosed(t, cConn2, 2*time.Second)
	require.Eventually(t, func() bool {
		return hub.GetClientCount() == 0 && !hub.HasClient("du-1") && !hub.HasClient("du-2")
	}, 2*time.Second, 10*time.Millisecond, "该用户所有 client 应被移除")
}

// TestDisconnectUserNotFound 验证 DisconnectUser 对不存在用户返回错误
func TestDisconnectUserNotFound(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	err := hub.DisconnectUser(context.Background(), "ghost-user", "reason")
	require.Error(t, err, "不存在的用户应返回错误")
}

// TestResetClientStatusChanges 验证 ResetClientStatus 重置客户端状态，且对不存在 client 返回错误
func TestResetClientStatusChanges(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newTestClient("reset-client", "reset-user", nil)
	client.SetStatus(UserStatusBusy)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	got, ok := hub.GetClientByIDWithLock(client.ID)
	require.True(t, ok)
	assert.Equal(t, UserStatusBusy, got.GetStatus(), "初始状态应为 Busy")

	require.NoError(t, hub.ResetClientStatus(client.ID, UserStatusOnline))
	assert.Equal(t, UserStatusOnline, got.GetStatus(), "重置后状态应为 Online")

	// 不存在的 client 应返回错误
	require.Error(t, hub.ResetClientStatus("nonexistent", UserStatusOnline))
}

// TestKickExistingClientsRemovesAll 验证 kickExistingClients 踢掉给定切片中的所有客户端
func TestKickExistingClientsRemovesAll(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	clients := []*Client{
		makeTestClient("ke-1", "ke-user"),
		makeTestClient("ke-2", "ke-user"),
		makeTestClient("ke-3", "ke-user"),
	}
	for _, c := range clients {
		hub.shardedRegistry.AddClient(c)
	}
	require.Equal(t, int64(3), hub.GetClientCount())

	hub.kickExistingClients(clients, DisconnectReasonForceOffline)

	require.Eventually(t, func() bool {
		return hub.GetClientCount() == 0 &&
			!hub.HasClient("ke-1") && !hub.HasClient("ke-2") && !hub.HasClient("ke-3")
	}, 2*time.Second, 10*time.Millisecond, "所有客户端应被踢出")
}

// TestKickOldestConnectionKicksOldest 验证 kickOldestConnection 踢掉心跳最旧的客户端
func TestKickOldestConnectionKicksOldest(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	now := time.Now()
	oldest := makeTestClient("oldest", "ko-user")
	oldest.SetLastHeartbeat(now.Add(-2 * time.Hour))
	mid := makeTestClient("mid", "ko-user")
	mid.SetLastHeartbeat(now.Add(-1 * time.Hour))
	newest := makeTestClient("newest", "ko-user")
	newest.SetLastHeartbeat(now)

	hub.shardedRegistry.AddClient(oldest)
	hub.shardedRegistry.AddClient(mid)
	hub.shardedRegistry.AddClient(newest)
	require.Equal(t, int64(3), hub.GetClientCount())

	hub.kickOldestConnection("ko-user")

	require.Eventually(t, func() bool {
		return !hub.HasClient("oldest") && hub.HasClient("mid") && hub.HasClient("newest") &&
			hub.GetClientCount() == 2
	}, 2*time.Second, 10*time.Millisecond, "应踢掉最旧的连接，保留其余两个")
}

// TestKickClientWithNotificationDelivers 验证 kickClientWithNotification 向被踢 client 投递 force_offline 消息后关闭并移除
func TestKickClientWithNotificationDelivers(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	// 直接入表（不经 handleRegister），避免启动读写协程与测试抢占 SendChan
	client := makeTestClient("kn-client", "kn-user")
	client.Conn = sConn
	client.ConnectionType = ConnectionTypeWebSocket
	hub.shardedRegistry.AddClient(client)
	require.True(t, hub.HasClient(client.ID))

	require.NotPanics(t, func() {
		hub.kickClientWithNotification(client, DisconnectReasonForceOffline, "force offline")
	})

	// 应收到 force_offline 通知（kickClientWithNotification 在 Unregister 前同步投递到 SendChan）
	select {
	case data := <-client.SendChan:
		assert.Contains(t, string(data), "force_offline", "应收到 force_offline 通知")
	case <-time.After(2 * time.Second):
		t.Fatal("未收到强制下线通知消息")
	}

	// 连接随后被关闭、客户端被移除且通道关闭
	waitForConnClosed(t, cConn, 2*time.Second)
	require.Eventually(t, func() bool {
		return !hub.HasClient(client.ID) && client.IsClosed()
	}, 2*time.Second, 10*time.Millisecond, "被踢客户端应被移除且通道关闭")
}

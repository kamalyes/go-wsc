/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 18:35:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 18:35:00
 * @FilePath: \go-wsc\connection\lifecycle_test.go
 * @Description: 连接域生命周期管理器测试 - 多端登录治理 / 踢出断链 / 精简移除幂等
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// fakeLifecycleHost 生命周期端口测试桩：记录注销与强制下线通知投递
type fakeLifecycleHost struct {
	shuttingDown bool

	unregistered []*models.Client
	sent         map[string]*models.HubMessage // clientID → 最后一条投递消息
}

func newFakeLifecycleHost() *fakeLifecycleHost {
	return &fakeLifecycleHost{sent: make(map[string]*models.HubMessage)}
}

// GetLogger 返回 nil（构造器内部兜底默认日志器）
func (f *fakeLifecycleHost) GetLogger() spi.Logger { return nil }

func (f *fakeLifecycleHost) IsShuttingDown() bool { return f.shuttingDown }

func (f *fakeLifecycleHost) Unregister(client *models.Client) {
	f.unregistered = append(f.unregistered, client)
}

func (f *fakeLifecycleHost) SendToClient(_ context.Context, client *models.Client, msg *models.HubMessage) {
	f.sent[client.ID] = msg
}

// newLifecycleManagerFixture 构造生命周期管理器（内含真实心跳管理器供断链原语取消超时任务）
func newLifecycleManagerFixture(t *testing.T, registry *ShardedRegistry, policy MultiLoginPolicy) (*LifecycleManager, *fakeLifecycleHost) {
	t.Helper()
	host := newFakeLifecycleHost()
	heartbeat := NewHeartbeatManager(&fakeHeartbeatHost{}, registry, time.Minute)
	t.Cleanup(heartbeat.Stop)
	return NewLifecycleManager(host, registry, heartbeat, policy), host
}

// TestLifecycleEnforceMultiLoginDisallowed 不允许多端登录：同用户新连接注册后踢掉全部旧连接
// （旧连接收到 ForceOffline 通知 + 注销，新连接自身不受影响）
func TestLifecycleEnforceMultiLoginDisallowed(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{AllowMultiLogin: false})

	// 旧连接持有真实底层连接：踢出时应先收到 ForceOffline 通知再被注销
	old1Server, _ := newWSConnPair(t)
	defer old1Server.Close()
	old2Server, _ := newWSConnPair(t)
	defer old2Server.Close()

	old1 := models.NewClient("lc-old-1", "u-1000", models.UserTypeCustomer)
	old1.Conn = old1Server
	old2 := models.NewClient("lc-old-2", "u-1000", models.UserTypeCustomer)
	old2.Conn = old2Server
	fresh := models.NewClient("lc-new", "u-1000", models.UserTypeCustomer)
	registry.AddClient(old1)
	registry.AddClient(old2)
	registry.AddClient(fresh)

	manager.EnforceMultiLoginPolicy(fresh)

	require.Len(t, host.unregistered, 2, "两条旧连接均应被注销")
	assert.ElementsMatch(t, []*models.Client{old1, old2}, host.unregistered)
	for _, old := range []*models.Client{old1, old2} {
		msg, ok := host.sent[old.ID]
		require.True(t, ok, "旧连接应收到强制下线通知")
		assert.Equal(t, models.MessageTypeForceOffline, msg.MessageType)
	}
	_, notified := host.sent[fresh.ID]
	assert.False(t, notified, "新连接不应收到强制下线通知")
}

// TestLifecycleEnforceMultiLoginKicksOldest 允许多端 + 连接数上限：超限时仅踢最久未心跳的旧连接
func TestLifecycleEnforceMultiLoginKicksOldest(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{
		AllowMultiLogin:       true,
		MaxConnectionsPerUser: 2,
	})

	stale := models.NewClient("lc-stale", "u-2000", models.UserTypeCustomer)
	stale.SetLastHeartbeat(time.Now().Add(-30 * time.Minute)) // 最久未心跳
	active := models.NewClient("lc-active", "u-2000", models.UserTypeCustomer)
	fresh := models.NewClient("lc-fresh", "u-2000", models.UserTypeCustomer)
	registry.AddClient(stale)
	registry.AddClient(active)
	registry.AddClient(fresh)

	// 已有 3 条连接（含新），上限 2 → 踢掉 1 条最旧的
	manager.EnforceMultiLoginPolicy(fresh)

	require.Len(t, host.unregistered, 1, "仅应踢掉一条最旧连接")
	assert.Equal(t, stale.ID, host.unregistered[0].ID, "应踢掉心跳最早的连接")
}

// TestLifecycleEnforceMultiLoginWithinLimit 允许多端 + 未达上限：不做任何踢出
func TestLifecycleEnforceMultiLoginWithinLimit(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{
		AllowMultiLogin:       true,
		MaxConnectionsPerUser: 3,
	})

	clientA := models.NewClient("lc-a", "u-3000", models.UserTypeCustomer)
	clientB := models.NewClient("lc-b", "u-3000", models.UserTypeCustomer)
	registry.AddClient(clientA)
	registry.AddClient(clientB)

	manager.EnforceMultiLoginPolicy(clientB)

	assert.Empty(t, host.unregistered, "未达上限不应踢出任何连接")
	assert.Empty(t, host.sent, "不应有任何强制下线通知")
}

// TestLifecycleEnforceMultiLoginNoExistingUser 用户无既有连接：快速路径零开销返回
func TestLifecycleEnforceMultiLoginNoExistingUser(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{AllowMultiLogin: false})

	fresh := models.NewClient("lc-single", "u-5000", models.UserTypeCustomer)
	registry.AddClient(fresh)

	// 新连接是用户唯一连接（无旧连接可踢）
	manager.EnforceMultiLoginPolicy(fresh)

	assert.Empty(t, host.unregistered)
	assert.Empty(t, host.sent)
}

// TestLifecycleKickUser 踢出用户全部连接：不发送通知，返回触发注销的连接数
func TestLifecycleKickUser(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})

	clientA := models.NewClient("lc-ka", "u-6000", models.UserTypeCustomer)
	clientB := models.NewClient("lc-kb", "u-6000", models.UserTypeCustomer)
	registry.AddClient(clientA)
	registry.AddClient(clientB)

	kicked := manager.KickUser(context.Background(), "u-6000", "test-kick")

	assert.Equal(t, 2, kicked)
	require.Len(t, host.unregistered, 2)
	assert.Empty(t, host.sent, "KickUser 不发送强制下线通知")
}

// TestLifecycleKickUserOffline 用户不在线：返回 0 且无副作用
func TestLifecycleKickUserOffline(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})

	kicked := manager.KickUser(context.Background(), "u-not-exist", "test-kick")

	assert.Zero(t, kicked)
	assert.Empty(t, host.unregistered)
}

// TestLifecycleKickClientNotifiesBeforeUnregister 踢出单连接：先投递 ForceOffline 通知再注销；
// 无底层连接（Conn 为 nil，如 SSE 半注册）时跳过通知直接注销
func TestLifecycleKickClientNotifiesBeforeUnregister(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})

	serverConn, _ := newWSConnPair(t)
	defer serverConn.Close()

	connected := models.NewClient("lc-kick-conn", "u-7000", models.UserTypeCustomer)
	connected.Conn = serverConn
	bare := models.NewClient("lc-kick-bare", "u-7000", models.UserTypeCustomer) // Conn 为 nil

	manager.KickClient(connected, models.DisconnectReasonKickOut, "连接已迁移到新节点")
	manager.KickClient(bare, models.DisconnectReasonKickOut, "连接已迁移到新节点")

	require.Len(t, host.unregistered, 2, "两种连接均应被注销")
	msg, ok := host.sent[connected.ID]
	require.True(t, ok, "有底层连接的客户端应收到强制下线通知")
	assert.Equal(t, models.MessageTypeForceOffline, msg.MessageType)
	assert.Equal(t, "连接已迁移到新节点", msg.Content)
	_, ok = host.sent[bare.ID]
	assert.False(t, ok, "Conn 为 nil 的客户端应跳过通知")
}

// TestLifecycleRemoveUnsafeCloses 精简移除：注册表条目清除 + 生命周期信号与底层连接关闭
func TestLifecycleRemoveUnsafeCloses(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, _ := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})

	serverConn, _ := newWSConnPair(t)
	defer serverConn.Close()

	client := models.NewClient("lc-remove", "u-8000", models.UserTypeCustomer)
	client.Conn = serverConn
	registry.AddClient(client)

	manager.RemoveUnsafe(client)

	_, exists := registry.GetClient(client.ID)
	assert.False(t, exists, "注册表条目应被清除")
	assert.True(t, client.IsClosed(), "生命周期信号应已关闭")
}

// TestLifecycleRemoveUnsafePointerConsistency 指针一致性：断线重连后新连接已覆盖同 clientID 条目，
// 旧客户端的精简移除不误删新连接（removed != client 时回填注册表）
func TestLifecycleRemoveUnsafePointerConsistency(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, _ := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})

	old := models.NewClient("lc-same-id", "u-9000", models.UserTypeCustomer)
	newer := models.NewClient("lc-same-id", "u-9000", models.UserTypeCustomer)
	registry.AddClient(old)
	registry.AddClient(newer) // 同 clientID 覆盖注册表条目

	manager.RemoveUnsafe(old) // 旧客户端读协程退出的兜底清理

	current, exists := registry.GetClient(newer.ID)
	require.True(t, exists, "新连接的注册表条目不应被误删")
	assert.Same(t, newer, current, "注册表应保留新连接指针")
	assert.False(t, newer.IsClosed(), "新连接不应被关闭")
}

// TestLifecycleCleanupHalfRegisteredIdempotent 半注册清理幂等：重复调用与未注册连接均无副作用
func TestLifecycleCleanupHalfRegisteredIdempotent(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, _ := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})

	client := models.NewClient("lc-half", "u-10000", models.UserTypeCustomer)
	registry.AddClient(client)

	require.NotPanics(t, func() {
		manager.CleanupHalfRegistered(client)
		manager.CleanupHalfRegistered(client) // 重复调用：各步骤幂等
		manager.CleanupHalfRegistered(nil)    // nil 防御
	})

	_, exists := registry.GetClient(client.ID)
	assert.False(t, exists, "半注册条目应被清除")
	assert.True(t, client.IsClosed(), "生命周期信号应已关闭")
}

// TestLifecycleCloseChannelIdempotent 通道关闭幂等：重复 CloseChannel 不 double-close；
// SSE 客户端额外关闭 SSECloseCh
func TestLifecycleCloseChannelIdempotent(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, _ := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})

	ws := models.NewClient("lc-close-ws", "u-11000", models.UserTypeCustomer)
	sse := models.NewClient("lc-close-sse", "u-11000", models.UserTypeCustomer)
	sse.ConnectionType = models.ConnectionTypeSSE
	sse.SSECloseCh = make(chan struct{})

	require.NotPanics(t, func() {
		manager.CloseChannel(ws)
		manager.CloseChannel(ws) // 重复调用：IsClosed 守卫拦截
		manager.CloseChannel(sse)
	})

	assert.True(t, ws.IsClosed())
	select {
	case <-ws.DoneCh:
	default:
		t.Fatal("WS 客户端 DoneCh 应已关闭")
	}
	select {
	case <-sse.SSECloseCh:
	default:
		t.Fatal("SSE 客户端 SSECloseCh 应已关闭")
	}
}

// TestLifecycleCloseConnectionGoingAway 停机期关闭连接：先发 1001 GoingAway 控制帧再关闭底层连接
func TestLifecycleCloseConnectionGoingAway(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newLifecycleManagerFixture(t, registry, MultiLoginPolicy{})
	host.shuttingDown = true

	serverConn, clientConn := newWSConnPair(t)
	defer clientConn.Close()

	client := models.NewClient("lc-goingaway", "u-12000", models.UserTypeCustomer)
	client.Conn = serverConn

	manager.CloseConnection(client)

	// 客户端侧应收到 1001 GoingAway 关闭帧（而非裸断开）
	_ = clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := clientConn.ReadMessage()
	require.Error(t, err, "底层连接应已关闭")
	closeErr, ok := err.(*websocket.CloseError)
	require.True(t, ok, "应收到 close 帧，实际错误: %v", err)
	assert.Equal(t, websocket.CloseGoingAway, closeErr.Code, "应携带 1001 GoingAway 关闭码")
}

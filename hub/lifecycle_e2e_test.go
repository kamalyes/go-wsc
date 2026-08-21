/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-21 00:00:00
 * @FilePath: \go-wsc\hub\lifecycle_e2e_test.go
 * @Description: 真实连接/断联/重连 端到端集成测试
 *
 * 用真实 WebSocket 连接（newWSConnPair）+ 真实 Hub + 内存离线 handler，
 * 验证完整生命周期场景：
 *   1. 连接 → 在线发消息 → 收到消息
 *   2. 连接 → 断联 → 断联期间发消息 → 离线存储
 *   3. 断联后重连 → 上线推送积压离线消息
 *   4. 重连后新消息正常在线投递
 *   5. 异常断联（关闭连接）→ 触发 Unregister → 重连
 *   6. 多设备同 userID（AllowMultiLogin）
 *
 * ⚠️ 真实连接走 hub.Register 会启动 handleClientWrite 写循环：
 *    sendToClientSerialized → TrySend 写入 SendChan → handleClientWrite 从 SendChan 读 → 写到 WebSocket conn
 *    因此测试从 clientConn（WebSocket 对端）读消息，而不是从 SendChan 读
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lifecycleTestEnv 真实连接生命周期测试环境
type lifecycleTestEnv struct {
	hub     *Hub
	offline *memoryOfflineHandler
	cleanup func()
	t       *testing.T
	mu      sync.Mutex
	connSeq int
}

// newLifecycleEnv 构造真实连接测试环境：真实 Hub + 内存离线 handler + 心跳超时关闭
func newLifecycleEnv(t *testing.T, opts ...func(*wscconfig.WSC)) *lifecycleTestEnv {
	t.Helper()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second). // 关闭心跳超时干扰
		WithMessageBufferSize(256)
	config.AllowMultiLogin = true // 默认允许多端，避免误踢
	for _, opt := range opts {
		opt(config)
	}

	hub := NewHub(config)
	offline := newMemoryOfflineHandler()
	hub.SetOfflineMessageHandler(offline)

	go hub.Run()
	hub.WaitForStart()

	return &lifecycleTestEnv{
		hub:     hub,
		offline: offline,
		cleanup: func() { _ = hub.SafeShutdown() },
		t:       t,
	}
}

// dial 真实 WebSocket 拨号并注册到 Hub，返回 (client, clientConn)
// 老系统风格：不显式传 namespace/groupID（注册时归一化）
// clientID 唯一递增，避免同 userID 多设备时 clientID 重复触发重连替换
func (e *lifecycleTestEnv) dial(userID string) (*Client, *websocket.Conn) {
	e.mu.Lock()
	e.connSeq++
	clientID := fmt.Sprintf("c-%s-%d", userID, e.connSeq)
	e.mu.Unlock()

	sConn, cConn := newWSConnPair(e.t)
	client := newTestClient(clientID, userID, sConn)
	e.hub.Register(client)

	// 等待注册完成
	require.Eventually(e.t, func() bool { return e.hub.HasClient(clientID) }, 2*time.Second, 10*time.Millisecond)
	return client, cConn
}

// readMsg 从 WebSocket 对端（clientConn）读一条消息，带超时
// 真实连接下消息经 handleClientWrite 写到 conn，故从 conn 读而非 SendChan
func readMsg(t *testing.T, conn *websocket.Conn, timeout time.Duration) (*HubMessage, bool) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, data, err := conn.ReadMessage()
	if err != nil {
		return nil, false
	}
	var m HubMessage
	if err := json.Unmarshal(data, &m); err == nil {
		return &m, true
	}
	return nil, false
}

// readMsgs 从 WebSocket 对端连续读消息直到超时，返回读到的所有消息
// 用于离线消息批量推送场景（重连后一次性收到多条积压消息）
func readMsgs(t *testing.T, conn *websocket.Conn, timeout time.Duration) []*HubMessage {
	t.Helper()
	var msgs []*HubMessage
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m, ok := readMsg(t, conn, 200*time.Millisecond)
		if !ok {
			break
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// ============================================================================
// 群组测试扩展：带群组仓库的环境 + 群组连接 + 群组消息投递 helper
// ============================================================================

// newLifecycleEnvWithGroup 构造带群组仓库（miniredis）的真实连接测试环境
// 群组广播/可靠投递/离线成员存储 均依赖 groupRepo
func newLifecycleEnvWithGroup(t *testing.T) *lifecycleTestEnv {
	t.Helper()
	env := newLifecycleEnv(t)

	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	groupRepo := repository.NewRedisGroupRepository(redisClient, "wsc:test:lifecycle:")
	env.hub.SetGroupRepository(groupRepo)

	// 包装 cleanup：先关 hub 再关 redis
	oldCleanup := env.cleanup
	env.cleanup = func() {
		oldCleanup()
		_ = redisClient.Close()
	}
	return env
}

// dialWithGroup 真实连接并指定群组（注册时 joinMemberGroupOnConnect 自动入群）
func (e *lifecycleTestEnv) dialWithGroup(userID, groupID string) (*Client, *websocket.Conn) {
	e.mu.Lock()
	e.connSeq++
	clientID := fmt.Sprintf("c-%s-%d", userID, e.connSeq)
	e.mu.Unlock()

	sConn, cConn := newWSConnPair(e.t)
	client := newTestClient(clientID, userID, sConn)
	client.WithGroupID(groupID)
	e.hub.Register(client)

	require.Eventually(e.t, func() bool { return e.hub.HasClient(clientID) }, 2*time.Second, 10*time.Millisecond)
	// 等待群组成员关系写入 Redis（joinMemberGroupOnConnect 异步于 HasClient）
	e.waitForGroupMember(groupID, userID)
	return client, cConn
}

// waitForGroupMember 等待 userID 出现在 groupID 的成员列表中（Redis 最终一致）
func (e *lifecycleTestEnv) waitForGroupMember(groupID, userID string) {
	ctx := routing.NewRoute().
		WithAppID(models.DefaultAppID).
		WithNamespace(models.DefaultNamespace).
		WithGroup(groupID).
		Inject(context.Background())
	require.Eventually(e.t, func() bool {
		members, err := e.hub.GetGroupMembers(ctx)
		if err != nil {
			return false
		}
		for _, m := range members {
			if m == userID {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond)
}

// sendGroupMsg 向指定群组发送消息（注入路由后调 Deliver）
// requireAck=true → 可靠投递（离线成员存储）；excludeSender=true → 发送者不收自己消息
func sendGroupMsg(t *testing.T, hub *Hub, senderID, groupID, content string, requireAck, excludeSender bool) *DeliverResult {
	t.Helper()
	ctx := routing.NewRoute().
		WithAppID(models.DefaultAppID).
		WithNamespace(models.DefaultNamespace).
		WithGroup(groupID).
		Inject(context.Background())
	msg := makeGroupMessage(senderID)
	msg.Content = content
	msg.RequireAck = requireAck
	return hub.Deliver(ctx, msg, excludeSender)
}

// assertMsgReceived 断言从 WebSocket 连接收到指定 content 的消息（持续读取直到找到或超时）
// ⚠️ gorilla/websocket 限制：ReadMessage 返回错误后（含超时）连接进入 failed 状态，
//
//	后续 ReadMessage 会 panic。因此本函数用「剩余时间」作为单次读取 deadline，
//	而非多次短超时重试。成功读取后可继续读（跳过不匹配消息）。
func assertMsgReceived(t *testing.T, conn *websocket.Conn, expectedContent string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var received []string
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(remaining))
		_, data, err := conn.ReadMessage()
		if err != nil {
			break // 超时或连接错误，无法继续读取
		}
		var m HubMessage
		if json.Unmarshal(data, &m) == nil {
			received = append(received, m.Content)
			if m.Content == expectedContent {
				return
			}
		}
	}
	assert.Fail(t, fmt.Sprintf("应收到内容为 %q 的消息（超时 %v，共读到 %d 条：%v）",
		expectedContent, timeout, len(received), received))
}

// ============================================================================
// 场景 1：连接 → 在线发消息 → 收到消息（真实 WS 收发）
// ============================================================================

// TestE2E_Connect_SendOnline_Receive 真实连接后在线 P2P 投递
func TestE2E_Connect_SendOnline_Receive(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// A 真实连接（A 是发送方）
	_, cConnA := env.dial("user-A")
	defer cConnA.Close()

	// B 真实连接（B 是接收方）
	clientB, cConnB := env.dial("user-B")
	defer cConnB.Close()

	// 验证注册后字段归一化（真实连接路径也走 handleRegister 归一化）
	assert.Equal(t, models.DefaultAppID, clientB.AppID, "老系统 AppID 应归一化")
	assert.Equal(t, models.DefaultNamespace, clientB.Namespace, "老系统 Namespace 应归一化")

	// A 通过 Hub 向 B 发 P2P 消息（老系统 ctx：context.Background()）
	msg := makeGroupMessage("user-A")
	msg.Receiver = "user-B"
	msg.Content = "hello from A"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-B", msg)

	assert.True(t, result.Success, "在线投递应成功")
	assert.False(t, result.StoredOffline, "在线用户不应走离线存储")

	// B 应从 WebSocket 连接收到消息（handleClientWrite 把 SendChan 消息写到 conn）
	got, ok := readMsg(t, cConnB, 3*time.Second)
	require.True(t, ok, "B 应从 WebSocket 收到消息")
	assert.Equal(t, "hello from A", got.Content)
	assert.Equal(t, "user-A", got.Sender)
}

// ============================================================================
// 场景 2：连接 → 断联 → 断联期间发消息 → 离线存储
// ============================================================================

// TestE2E_Disconnect_OfflineStore 离线期间消息存储到离线队列
func TestE2E_Disconnect_OfflineStore(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// C 真实连接后断联
	clientC, cConnC := env.dial("user-C")
	cConnC.Close() // 客户端关闭连接 → 服务端 ReadMessage 报错 → handleClientRead 返回 → Unregister

	// 等待服务端感知断联并完成 Unregister
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientC.ID) }, 3*time.Second, 20*time.Millisecond,
		"断联后 client 应被移除")

	// 此时 C 离线，向 C 发消息应走离线存储
	msg := makeGroupMessage("sender")
	msg.Receiver = "user-C"
	msg.Content = "offline-msg-1"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-C", msg)

	assert.True(t, result.StoredOffline, "离线用户消息应走离线存储")
	assert.NoError(t, result.FinalError)

	// 验证离线队列有 1 条
	count, err := env.offline.GetOfflineMessageCount(context.Background(), "user-C")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "离线存储应有1条消息")
}

// ============================================================================
// 场景 3：断联后重连 → 上线推送积压离线消息
// ============================================================================

// TestE2E_Reconnect_PushOfflineMessages 重连后收到断联期间积压的离线消息
func TestE2E_Reconnect_PushOfflineMessages(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// D 连接后断联
	clientD1, cConnD1 := env.dial("user-D")
	cConnD1.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientD1.ID) }, 3*time.Second, 20*time.Millisecond)

	// 断联期间发 2 条消息
	for i := 0; i < 2; i++ {
		msg := makeGroupMessage("sender")
		msg.Receiver = "user-D"
		msg.Content = "offline-msg"
		env.hub.SendToUserWithRetry(context.Background(), "user-D", msg)
	}
	count, _ := env.offline.GetOfflineMessageCount(context.Background(), "user-D")
	require.Equal(t, int64(2), count, "断联期间应存储 2 条离线消息")

	// D 重连（新 client，同 userID）
	_, cConnD2 := env.dial("user-D")
	defer cConnD2.Close()

	// 重连后 pushOfflineMessagesOnConnect 异步推送积压离线消息到新连接
	msgs := readMsgs(t, cConnD2, 5*time.Second)
	assert.GreaterOrEqual(t, len(msgs), 2, "重连后应收到至少 2 条离线消息，实际收到 %d 条", len(msgs))
}

// ============================================================================
// 场景 4：重连后新消息正常在线投递
// ============================================================================

// TestE2E_AfterReconnect_NewMessageDelivered 重连并消费离线消息后，新消息仍能在线投递
func TestE2E_AfterReconnect_NewMessageDelivered(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// E 连接后断联
	clientE1, cConnE1 := env.dial("user-E")
	cConnE1.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientE1.ID) }, 3*time.Second, 20*time.Millisecond)

	// 离线期间存 1 条
	msg1 := makeGroupMessage("sender")
	msg1.Receiver = "user-E"
	msg1.Content = "offline-backlog"
	env.hub.SendToUserWithRetry(context.Background(), "user-E", msg1)

	// E 重连
	_, cConnE2 := env.dial("user-E")
	defer cConnE2.Close()

	// 现在发新消息，应在线投递（离线消息可能同时被推送，一起读到 conn）
	msg2 := makeGroupMessage("sender")
	msg2.Receiver = "user-E"
	msg2.Content = "new-online-msg"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-E", msg2)
	assert.True(t, result.Success, "重连后新消息应在线投递成功")
	assert.False(t, result.StoredOffline)

	// 连续读消息，找到新在线消息（可能夹杂离线积压消息）
	msgs := readMsgs(t, cConnE2, 3*time.Second)
	var foundNew bool
	for _, m := range msgs {
		if m.Content == "new-online-msg" {
			foundNew = true
			break
		}
	}
	assert.True(t, foundNew, "E 应收到新在线消息（共读到 %d 条消息）", len(msgs))
}

// ============================================================================
// 场景 5：异常断联（连接错误）→ 触发 Unregister → 重连
// ============================================================================

// TestE2E_AbnormalDisconnect_TriggersUnregister 连接异常断开（非正常 Close）后 Hub 自动清理
func TestE2E_AbnormalDisconnect_TriggersUnregister(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// F 真实连接
	clientF, cConnF := env.dial("user-F")

	// 强制关闭底层连接（模拟网络中断，非正常 Close 帧）
	_ = cConnF.Close()

	// handleClientRead 的 ReadMessage 报错 → defer Unregister 触发
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientF.ID) }, 3*time.Second, 20*time.Millisecond,
		"异常断联后 Hub 应自动 Unregister")
	assert.Equal(t, int64(0), env.hub.GetClientCount(), "断联后在线计数应归零")

	// F 重连
	clientF2, cConnF2 := env.dial("user-F")
	defer cConnF2.Close()
	assert.True(t, env.hub.HasClient(clientF2.ID), "重连后应在线")
}

// ============================================================================
// 场景 6：多设备同 userID（AllowMultiLogin=true，不互踢）
// ============================================================================

// TestE2E_MultiDevice_SameUser_AllOnline 多设备同 userID 并存，消息全投递
func TestE2E_MultiDevice_SameUser_AllOnline(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// 同一 userID 的 2 个设备同时连接
	dev1, cConn1 := env.dial("user-G")
	defer cConn1.Close()
	dev2, cConn2 := env.dial("user-G")
	defer cConn2.Close()

	// 两设备都应在线（AllowMultiLogin=true）
	assert.True(t, env.hub.HasClient(dev1.ID), "设备1 应在线")
	assert.True(t, env.hub.HasClient(dev2.ID), "设备2 应在线")

	// 向 user-G 发消息，两设备都应收到
	msg := makeGroupMessage("sender")
	msg.Receiver = "user-G"
	msg.Content = "multi-device-msg"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-G", msg)
	assert.True(t, result.Success)

	got1, ok1 := readMsg(t, cConn1, 2*time.Second)
	got2, ok2 := readMsg(t, cConn2, 2*time.Second)
	assert.True(t, ok1, "设备1 应收到消息")
	assert.True(t, ok2, "设备2 应收到消息")
	if ok1 {
		assert.Equal(t, "multi-device-msg", got1.Content)
	}
	if ok2 {
		assert.Equal(t, "multi-device-msg", got2.Content)
	}
}

// ============================================================================
// 场景 7：多设备中一个断联，另一个仍在线收消息
// ============================================================================

// TestE2E_MultiDevice_PartialDisconnect 一个设备断联不影响另一个
func TestE2E_MultiDevice_PartialDisconnect(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	dev1, cConn1 := env.dial("user-H")
	dev2, cConn2 := env.dial("user-H")
	defer cConn2.Close()

	// 设备1 断联
	cConn1.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(dev1.ID) }, 3*time.Second, 20*time.Millisecond)
	// 设备2 仍在线
	assert.True(t, env.hub.HasClient(dev2.ID), "设备2 应仍在线")

	// 发消息，设备2 应收到，不应走离线（仍有在线设备）
	msg := makeGroupMessage("sender")
	msg.Receiver = "user-H"
	msg.Content = "still-online-msg"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-H", msg)
	assert.True(t, result.Success)
	assert.False(t, result.StoredOffline, "有在线设备时不应走离线存储")

	got, ok := readMsg(t, cConn2, 2*time.Second)
	require.True(t, ok, "设备2 应收到消息")
	assert.Equal(t, "still-online-msg", got.Content)
}

// ============================================================================
// 场景 8：快速重连（断联后立即重连），clientID 复用
// ============================================================================

// TestE2E_QuickReconnect_SameClientID 断联后立即用同 clientID 重连，旧 client 被替换
func TestE2E_QuickReconnect_SameClientID(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// I 连接
	clientI1, _ := env.dial("user-I")
	// 立即用同 clientID 重连（旧连接仍在注册表里）
	sConn2, cConn2 := newWSConnPair(t)
	clientI2 := newTestClient(clientI1.ID, "user-I", sConn2)
	env.hub.Register(clientI2)

	// 新 client 覆盖 map，旧 client 被关闭
	require.Eventually(t, func() bool { return clientI1.IsClosed() }, 2*time.Second, 10*time.Millisecond,
		"同 clientID 重连应关闭旧 client")
	assert.True(t, env.hub.HasClient(clientI2.ID))
	defer cConn2.Close()
}

// ============================================================================
// 场景 9：连接后发心跳刷新，不超时
// ============================================================================

// TestE2E_HeartbeatRefresh_KeepsOnline 真实连接后刷新心跳，client 保持在线不超时
func TestE2E_HeartbeatRefresh_KeepsOnline(t *testing.T) {
	// 用短心跳间隔验证心跳刷新
	env := newLifecycleEnv(t, func(c *wscconfig.WSC) {
		c.HeartbeatInterval = 200 * time.Millisecond
		c.ClientTimeout = 400 * time.Millisecond
	})
	defer env.cleanup()

	clientJ, cConnJ := env.dial("user-J")
	defer cConnJ.Close()

	// 持续刷新心跳（模拟客户端发 PING）
	stop := make(chan struct{})
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				clientJ.SetLastHeartbeat(time.Now())
				clientJ.SetLastSeen(time.Now())
				env.hub.RefreshHeartbeatTimeout(clientJ)
			case <-stop:
				return
			}
		}
	}()

	// 等待足够长时间（超过 ClientTimeout 多倍），client 应仍在线
	time.Sleep(1 * time.Second)
	close(stop)

	assert.True(t, env.hub.HasClient(clientJ.ID), "持续刷新心跳的 client 应保持在线")
}

// ============================================================================
// 场景 10：老系统不传维度，真实连接 + P2P 消息收发全链路
// ============================================================================

// TestE2E_Legacy_NoDimensions_RealConn_P2P 老系统风格：context.Background() 全链路 P2P
// 验证注册归一化 + 在线投递在真实连接下都正常
func TestE2E_Legacy_NoDimensions_RealConn_P2P(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// 老系统 K 连接（newTestClient 不设 AppID/Namespace，注册时归一化）
	clientK, cConnK := env.dial("user-K")
	defer cConnK.Close()

	// 验证注册后字段归一化（真实连接路径也走 handleRegister 归一化）
	assert.Equal(t, models.DefaultAppID, clientK.AppID, "老系统 AppID 应归一化")
	assert.Equal(t, models.DefaultNamespace, clientK.Namespace, "老系统 Namespace 应归一化")

	// context.Background() 发 P2P 消息给在线的 K
	msg := makeGroupMessage("sender")
	msg.Receiver = "user-K"
	msg.Content = "legacy-p2p"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-K", msg)
	assert.True(t, result.Success, "老系统 ctx 在线 P2P 应成功")

	got, ok := readMsg(t, cConnK, 2*time.Second)
	require.True(t, ok, "老系统 K 应收到消息")
	assert.Equal(t, "legacy-p2p", got.Content)
}

// ============================================================================
// 场景 11：群组广播真实收发（3 人群组，排除发送者）
// ============================================================================

// TestE2E_GroupBroadcast_RealConn_ExcludeSender 群组广播：发送者不收到自己消息，其他成员收到
func TestE2E_GroupBroadcast_RealConn_ExcludeSender(t *testing.T) {
	env := newLifecycleEnvWithGroup(t)
	defer env.cleanup()

	const gid = "team-alpha"
	// 3 人加入群组
	_, cConnM1 := env.dialWithGroup("user-M1", gid)
	defer cConnM1.Close()
	_, cConnM2 := env.dialWithGroup("user-M2", gid)
	defer cConnM2.Close()
	_, cConnM3 := env.dialWithGroup("user-M3", gid)
	defer cConnM3.Close()

	// M1 发群组消息，excludeSender=true → M1 不收，M2/M3 收
	sendGroupMsg(t, env.hub, "user-M1", gid, "group-hello-exclude", false, true)

	assertMsgReceived(t, cConnM2, "group-hello-exclude", 2*time.Second)
	assertMsgReceived(t, cConnM3, "group-hello-exclude", 2*time.Second)

	// M1 不应收到（排除发送者）
	m1Msgs := readMsgs(t, cConnM1, 500*time.Millisecond)
	for _, m := range m1Msgs {
		assert.NotEqual(t, "group-hello-exclude", m.Content, "发送者被排除时不应收到自己消息")
	}
}

// ============================================================================
// 场景 12：群组广播不排除发送者（发送者也收到，多端同步场景）
// ============================================================================

// TestE2E_GroupBroadcast_IncludeSender 群组广播不排除发送者：所有成员含发送者都收到
func TestE2E_GroupBroadcast_IncludeSender(t *testing.T) {
	env := newLifecycleEnvWithGroup(t)
	defer env.cleanup()

	const gid = "team-beta"
	_, cConnN1 := env.dialWithGroup("user-N1", gid)
	defer cConnN1.Close()
	_, cConnN2 := env.dialWithGroup("user-N2", gid)
	defer cConnN2.Close()

	// N1 发群组消息，excludeSender=false → N1 也收到（多端同步语义）
	sendGroupMsg(t, env.hub, "user-N1", gid, "group-include-sender", false, false)

	assertMsgReceived(t, cConnN1, "group-include-sender", 2*time.Second)
	assertMsgReceived(t, cConnN2, "group-include-sender", 2*time.Second)
}

// ============================================================================
// 场景 13：群组离线成员存储 + 重连推送（RequireAck=true 可靠投递）
// ============================================================================

// TestE2E_GroupOfflineMember_StoredAndPushOnReconnect 群组可靠投递：离线成员消息存储，重连后推送
func TestE2E_GroupOfflineMember_StoredAndPushOnReconnect(t *testing.T) {
	env := newLifecycleEnvWithGroup(t)
	defer env.cleanup()

	const gid = "team-gamma"
	// P1, P2 在线
	_, cConnP1 := env.dialWithGroup("user-P1", gid)
	defer cConnP1.Close()
	_, cConnP2 := env.dialWithGroup("user-P2", gid)
	defer cConnP2.Close()
	// P3 连接后断联（离线）
	clientP3, cConnP3 := env.dialWithGroup("user-P3", gid)
	cConnP3.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientP3.ID) }, 3*time.Second, 20*time.Millisecond)

	// P1 发可靠群组消息（RequireAck=true），P3 离线 → 消息应存储到 P3 的离线队列
	sendGroupMsg(t, env.hub, "user-P1", gid, "reliable-group-msg", true, true)

	// P2 在线应即时收到（P1 被排除）
	assertMsgReceived(t, cConnP2, "reliable-group-msg", 2*time.Second)
	// P1 被排除，不应收到自己的消息
	for _, m := range readMsgs(t, cConnP1, 300*time.Millisecond) {
		assert.NotEqual(t, "reliable-group-msg", m.Content, "发送者被排除时不应收到自己消息")
	}

	// P3 重连
	_, cConnP3New := env.dialWithGroup("user-P3", gid)
	defer cConnP3New.Close()

	// P3 应从离线队列收到群组消息
	assertMsgReceived(t, cConnP3New, "reliable-group-msg", 5*time.Second)
}

// ============================================================================
// 场景 14：多用户并发 P2P（5 人环形发送，同时收发）
// ============================================================================

// TestE2E_ConcurrentP2P_RingSend 5 个用户同时连接，环形发送 P2P，各自收到
func TestE2E_ConcurrentP2P_RingSend(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	const n = 5
	conns := make([]*websocket.Conn, n)
	userIDs := make([]string, n)
	for i := 0; i < n; i++ {
		userIDs[i] = fmt.Sprintf("user-ring-%d", i)
		_, conn := env.dial(userIDs[i])
		conns[i] = conn
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	// 环形发送：i → (i+1)%n
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			next := (i + 1) % n
			msg := makeGroupMessage(userIDs[i])
			msg.Receiver = userIDs[next]
			msg.Content = fmt.Sprintf("ring-%d-to-%d", i, next)
			env.hub.SendToUserWithRetry(context.Background(), userIDs[next], msg)
		}(i)
	}
	wg.Wait()

	// 每个用户应收到前一个发来的消息
	for i := 0; i < n; i++ {
		prev := (i - 1 + n) % n
		expected := fmt.Sprintf("ring-%d-to-%d", prev, i)
		assertMsgReceived(t, conns[i], expected, 3*time.Second)
	}
}

// ============================================================================
// 场景 15：消息顺序保证（同一发送者连续发 5 条，接收方按序收到）
// ============================================================================

// TestE2E_MessageOrder_SequentialSend 同一发送者连续发 5 条 P2P，接收方按序收到
func TestE2E_MessageOrder_SequentialSend(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	_, cConnA := env.dial("user-ord-A")
	defer cConnA.Close()
	_, cConnB := env.dial("user-ord-B")
	defer cConnB.Close()

	// A 连续发 5 条消息给 B
	const count = 5
	for i := 0; i < count; i++ {
		msg := makeGroupMessage("user-ord-A")
		msg.Receiver = "user-ord-B"
		msg.Content = fmt.Sprintf("order-msg-%d", i)
		env.hub.SendToUserWithRetry(context.Background(), "user-ord-B", msg)
	}

	// B 应按序收到 5 条
	msgs := readMsgs(t, cConnB, 3*time.Second)
	require.GreaterOrEqual(t, len(msgs), count, "B 应至少收到 %d 条消息", count)

	// 验证前 count 条按序到达
	for i := 0; i < count; i++ {
		expected := fmt.Sprintf("order-msg-%d", i)
		assert.Equal(t, expected, msgs[i].Content, "第 %d 条消息顺序不符", i)
	}
}

// ============================================================================
// 场景 16：反复断联重连（3 次连接→断联→重连循环）
// ============================================================================

// TestE2E_RepeatedReconnect_3Cycles 3 次断联重连循环，每次都能正常收发
func TestE2E_RepeatedReconnect_3Cycles(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	const cycles = 3
	for c := 0; c < cycles; c++ {
		// 连接
		_, cConn := env.dial("user-reconnect")
		// 立即发消息验证在线
		msg := makeGroupMessage("sender")
		msg.Receiver = "user-reconnect"
		msg.Content = fmt.Sprintf("cycle-%d", c)
		result := env.hub.SendToUserWithRetry(context.Background(), "user-reconnect", msg)
		assert.True(t, result.Success, "第 %d 轮在线投递应成功", c)
		assertMsgReceived(t, cConn, fmt.Sprintf("cycle-%d", c), 2*time.Second)

		// 断联
		cConn.Close()
	}

	// 最后一次重连后保持在线
	_, cConnFinal := env.dial("user-reconnect")
	defer cConnFinal.Close()
	msg := makeGroupMessage("sender")
	msg.Receiver = "user-reconnect"
	msg.Content = "final-after-cycles"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-reconnect", msg)
	assert.True(t, result.Success)
	assertMsgReceived(t, cConnFinal, "final-after-cycles", 2*time.Second)
}

// ============================================================================
// 场景 17：连接超时自动清理（不刷新心跳 → 超时移除）
// ============================================================================

// TestE2E_HeartbeatTimeout_AutoRemove 不刷新心跳的 client 超时后自动移除
func TestE2E_HeartbeatTimeout_AutoRemove(t *testing.T) {
	env := newLifecycleEnv(t, func(c *wscconfig.WSC) {
		c.HeartbeatInterval = 100 * time.Millisecond
		c.ClientTimeout = 300 * time.Millisecond
	})
	defer env.cleanup()

	clientQ, _ := env.dial("user-Q")
	// 不刷新心跳，等待超时
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientQ.ID) }, 3*time.Second, 50*time.Millisecond,
		"不刷新心跳的 client 应在超时后被自动移除")
}

// ============================================================================
// 场景 18：大批量离线消息（20 条积压 → 重连全推送）
// ============================================================================

// TestE2E_LargeOfflineBatch_20Messages 离线期间存储 20 条消息，重连后全部推送
func TestE2E_LargeOfflineBatch_20Messages(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// R 连接后断联
	clientR, cConnR := env.dial("user-R")
	cConnR.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientR.ID) }, 3*time.Second, 20*time.Millisecond)

	// 离线期间发 20 条
	const batch = 20
	for i := 0; i < batch; i++ {
		msg := makeGroupMessage("sender")
		msg.Receiver = "user-R"
		msg.Content = fmt.Sprintf("batch-%d", i)
		env.hub.SendToUserWithRetry(context.Background(), "user-R", msg)
	}

	// 验证离线存储
	count, _ := env.offline.GetOfflineMessageCount(context.Background(), "user-R")
	require.Equal(t, int64(batch), count, "离线应存储 %d 条", batch)

	// R 重连
	_, cConnRNew := env.dial("user-R")
	defer cConnRNew.Close()

	// 读取所有推送的离线消息
	msgs := readMsgs(t, cConnRNew, 10*time.Second)
	assert.GreaterOrEqual(t, len(msgs), batch, "重连后应收到全部 %d 条离线消息，实际 %d", batch, len(msgs))
}

// ============================================================================
// 场景 19：群组动态加入后收到新消息
// ============================================================================

// TestE2E_GroupJoin_ThenReceive 新用户加入群组后能收到后续群组消息
func TestE2E_GroupJoin_ThenReceive(t *testing.T) {
	env := newLifecycleEnvWithGroup(t)
	defer env.cleanup()

	const gid = "team-delta"
	// S1 先入群
	_, cConnS1 := env.dialWithGroup("user-S1", gid)
	defer cConnS1.Close()

	// S1 发一条群组消息（此时只有 S1 在群里）
	sendGroupMsg(t, env.hub, "user-S1", gid, "before-join", false, true)

	// S2 后入群
	_, cConnS2 := env.dialWithGroup("user-S2", gid)
	defer cConnS2.Close()

	// S1 再发一条群组消息
	sendGroupMsg(t, env.hub, "user-S1", gid, "after-join", false, true)

	// S2 应收到入群后的消息（assertMsgReceived 会跳过不匹配的消息）
	assertMsgReceived(t, cConnS2, "after-join", 2*time.Second)
}

// ============================================================================
// 场景 20：群组混合在线离线（3 人群组，1 离线 → 在线者即时收，离线者存储）
// ============================================================================

// TestE2E_GroupMixed_OnlineOffline 群组可靠投递：2 在线即时收 + 1 离线存储后重连收
func TestE2E_GroupMixed_OnlineOffline(t *testing.T) {
	env := newLifecycleEnvWithGroup(t)
	defer env.cleanup()

	const gid = "team-epsilon"
	// T1, T2 在线
	_, cConnT1 := env.dialWithGroup("user-T1", gid)
	defer cConnT1.Close()
	_, cConnT2 := env.dialWithGroup("user-T2", gid)
	defer cConnT2.Close()
	// T3 连接后断联
	clientT3, cConnT3 := env.dialWithGroup("user-T3", gid)
	cConnT3.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientT3.ID) }, 3*time.Second, 20*time.Millisecond)

	// T1 发可靠群组消息
	sendGroupMsg(t, env.hub, "user-T1", gid, "mixed-group-msg", true, true)

	// T2 在线即时收到
	assertMsgReceived(t, cConnT2, "mixed-group-msg", 2*time.Second)
	// T1 被排除，不收到
	readMsgs(t, cConnT1, 300*time.Millisecond)

	// T3 重连
	_, cConnT3New := env.dialWithGroup("user-T3", gid)
	defer cConnT3New.Close()
	// T3 从离线队列收到
	assertMsgReceived(t, cConnT3New, "mixed-group-msg", 5*time.Second)
}

// ============================================================================
// 场景 21：断联期间多发送者发消息 → 重连后全部收到
// ============================================================================

// TestE2E_Offline_MultipleSenders 多个发送者在 R 离线期间各发消息，R 重连后全部收到
func TestE2E_Offline_MultipleSenders(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// 3 个发送者在线
	_, cConnSA := env.dial("user-SA")
	defer cConnSA.Close()
	_, cConnSB := env.dial("user-SB")
	defer cConnSB.Close()
	_, cConnSC := env.dial("user-SC")
	defer cConnSC.Close()

	// R 离线
	clientR, cConnR := env.dial("user-R-offline")
	cConnR.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientR.ID) }, 3*time.Second, 20*time.Millisecond)

	// 3 个发送者各发一条给 R
	senders := []string{"user-SA", "user-SB", "user-SC"}
	contents := []string{"from-A", "from-B", "from-C"}
	for i, s := range senders {
		msg := makeGroupMessage(s)
		msg.Receiver = "user-R-offline"
		msg.Content = contents[i]
		env.hub.SendToUserWithRetry(context.Background(), "user-R-offline", msg)
	}

	// R 重连
	_, cConnRNew := env.dial("user-R-offline")
	defer cConnRNew.Close()

	// R 应收到 3 个发送者的消息
	msgs := readMsgs(t, cConnRNew, 5*time.Second)
	gotContents := make(map[string]bool)
	for _, m := range msgs {
		gotContents[m.Content] = true
	}
	for _, c := range contents {
		assert.True(t, gotContents[c], "R 应收到 %q", c)
	}
}

// ============================================================================
// 场景 22：多设备群组消息（同 userID 2 设备在同一群组，都收到群组消息）
// ============================================================================

// TestE2E_GroupMultiDevice_BothReceive 同 userID 的 2 设备同在一群组，群组消息都收到
func TestE2E_GroupMultiDevice_BothReceive(t *testing.T) {
	env := newLifecycleEnvWithGroup(t)
	defer env.cleanup()

	const gid = "team-zeta"
	// 同一 userID 的 2 个设备加入群组
	_, cConnDev1 := env.dialWithGroup("user-multi", gid)
	defer cConnDev1.Close()
	_, cConnDev2 := env.dialWithGroup("user-multi", gid)
	defer cConnDev2.Close()

	// 另一个用户发群组消息
	_, cConnOther := env.dialWithGroup("user-other", gid)
	defer cConnOther.Close()

	sendGroupMsg(t, env.hub, "user-other", gid, "multi-dev-group", false, true)

	// 两个设备都应收到（assertMsgReceived 会跳过不匹配的注册阶段消息）
	assertMsgReceived(t, cConnDev1, "multi-dev-group", 2*time.Second)
	assertMsgReceived(t, cConnDev2, "multi-dev-group", 2*time.Second)
}

// ============================================================================
// 场景 23：SSE 连接在线投递（SSE 连接类型不走时间轮心跳）
// ============================================================================

// TestE2E_SSE_Connection_P2P SSE 连接类型在线收 P2P 消息（经 SSEMessageCh 通道）
func TestE2E_SSE_Connection_P2P(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// 构造 SSE 类型客户端（SSE 不走时间轮心跳，消息经 SSEMessageCh 投递）
	sConn, cConn := newWSConnPair(t)
	defer cConn.Close()
	clientSSE := newTestClient("c-sse-1", "user-sse", sConn)
	clientSSE.ConnectionType = ConnectionTypeSSE
	clientSSE.SSEMessageCh = make(chan *HubMessage, 16) // SSE 专用消息通道
	env.hub.Register(clientSSE)
	require.Eventually(t, func() bool { return env.hub.HasClient("c-sse-1") }, 2*time.Second, 10*time.Millisecond)

	// 发 P2P 消息给 SSE 客户端
	msg := makeGroupMessage("sender")
	msg.Receiver = "user-sse"
	msg.Content = "sse-p2p-msg"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-sse", msg)
	assert.True(t, result.Success, "SSE 客户端在线 P2P 应成功")

	// SSE 客户端从 SSEMessageCh 读消息（非 WebSocket conn）
	select {
	case got := <-clientSSE.SSEMessageCh:
		assert.Equal(t, "sse-p2p-msg", got.Content)
	case <-time.After(2 * time.Second):
		t.Fatal("SSE 客户端应从 SSEMessageCh 收到消息")
	}
}

// ============================================================================
// 场景 24：断联→离线存储→重连→在线新消息→离线消息混合到达
// ============================================================================

// TestE2E_Mixed_OnlineAndOfflineMessages 重连后离线积压和在线新消息混合到达
func TestE2E_Mixed_OnlineAndOfflineMessages(t *testing.T) {
	env := newLifecycleEnv(t)
	defer env.cleanup()

	// U 连接后断联
	clientU, cConnU := env.dial("user-U")
	cConnU.Close()
	require.Eventually(t, func() bool { return !env.hub.HasClient(clientU.ID) }, 3*time.Second, 20*time.Millisecond)

	// 离线存 1 条
	msg1 := makeGroupMessage("sender")
	msg1.Receiver = "user-U"
	msg1.Content = "offline-backlog-mixed"
	env.hub.SendToUserWithRetry(context.Background(), "user-U", msg1)

	// U 重连
	_, cConnU2 := env.dial("user-U")
	defer cConnU2.Close()

	// 立即发新在线消息（离线推送可能正在进行）
	msg2 := makeGroupMessage("sender")
	msg2.Receiver = "user-U"
	msg2.Content = "new-online-mixed"
	result := env.hub.SendToUserWithRetry(context.Background(), "user-U", msg2)
	assert.True(t, result.Success)

	// U 应收到两条（离线 + 在线），内容均存在
	msgs := readMsgs(t, cConnU2, 5*time.Second)
	gotSet := make(map[string]bool)
	for _, m := range msgs {
		gotSet[m.Content] = true
	}
	assert.True(t, gotSet["offline-backlog-mixed"], "应收到离线积压消息")
	assert.True(t, gotSet["new-online-mixed"], "应收到新在线消息")
}

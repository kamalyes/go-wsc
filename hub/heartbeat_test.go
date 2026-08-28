/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-25 10:56:20
 * @FilePath: \go-wsc\hub\heartbeat_test.go
 * @Description: Hub 心跳核心路径白盒单元测试
 *   - heartbeat.go: sendPongResponse / handleHeartbeatMessage / SetHeartbeatConfig
 *   - message_handler.go: checkHeartbeat
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// sendPongResponse
// ============================================================================

// TestPongResponseSuccess 验证向 client 投递 pong 帧：SendChan 收到 Pong 消息且 lastPong 更新
func TestPongResponseSuccess(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("pong-c", "pong-user")
	// Conn 为 nil（makeTestClient 默认），sendPongResponse 不依赖 Conn
	now := time.Now()

	err := hub.sendPongResponse(client, now)
	require.NoError(t, err, "向有效 client 发送 pong 应成功")

	select {
	case data := <-client.SendChan:
		require.NotNil(t, data, "应收到 pong 帧数据")
		var pongMsg HubMessage
		require.NoError(t, json.Unmarshal(data, &pongMsg))
		assert.Equal(t, MessageTypePong, pongMsg.MessageType, "消息类型应为 Pong")
		assert.Equal(t, client.UserID, pongMsg.Receiver, "接收者应为客户端 userID")
		assert.Equal(t, UserTypeSystem, pongMsg.SenderType, "发送者类型应为系统")
		assert.Equal(t, now.Unix(), pongMsg.CreateAt.Unix(), "CreateAt 应为传入时间")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：未收到 pong 消息")
	}

	// 验证 lastPong 已更新
	assert.Equal(t, now.UnixNano(), client.GetLastPong().UnixNano(), "lastPong 应被更新为传入时间")
}

// TestPongResponseNilClient 验证 nil client 不 panic 且返回 error
func TestPongResponseNilClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	err := hub.sendPongResponse(nil, time.Now())
	require.Error(t, err, "nil client 应返回 error")
	assert.Contains(t, err.Error(), "nil")
}

// TestPongResponseClosedClient 验证向已关闭的 client 发送 pong 返回 error 不 panic
func TestPongResponseClosedClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("pong-closed", "pong-closed-user")
	client.CloseMu.Lock()
	client.MarkClosed()
	close(client.SendChan)
	client.CloseMu.Unlock()

	err := hub.sendPongResponse(client, time.Now())
	require.Error(t, err, "向已关闭 client 发送应返回 error")
}

// ============================================================================
// handleHeartbeatMessage
// ============================================================================

// TestHeartbeatHandleMessage 验证心跳消息处理：更新 lastHeartbeat/lastSeen、回复 pong
func TestHeartbeatHandleMessage(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("hb-c", "hb-user")
	// 记录旧的心跳时间（为零值）
	oldHB := client.GetLastHeartbeat()
	assert.True(t, oldHB.IsZero(), "初始 lastHeartbeat 应为零值")

	// 处理心跳消息
	hub.handleHeartbeatMessage(client)

	// 验证 lastHeartbeat 已更新为当前时间附近
	newHB := client.GetLastHeartbeat()
	assert.False(t, newHB.IsZero(), "处理后 lastHeartbeat 不应为零值")
	assert.True(t, newHB.After(oldHB), "lastHeartbeat 应被更新为更晚的时间")

	// 验证 lastSeen 也被更新
	newSeen := client.GetLastSeen()
	assert.False(t, newSeen.IsZero(), "处理后 lastSeen 不应为零值")

	// 验证 pong 响应已投递到 SendChan
	select {
	case data := <-client.SendChan:
		require.NotNil(t, data)
		var pongMsg HubMessage
		require.NoError(t, json.Unmarshal(data, &pongMsg))
		assert.Equal(t, MessageTypePong, pongMsg.MessageType, "应回复 Pong 消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：未收到 pong 响应")
	}

	// 验证 lastPong 被更新
	assert.False(t, client.GetLastPong().IsZero(), "lastPong 应被更新")
}

// TestHeartbeatHandleMessageClosedClient 验证已关闭客户端的心跳被跳过
func TestHeartbeatHandleMessageClosedClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("hb-closed", "hb-closed-user")
	client.MarkClosed()

	oldHB := client.GetLastHeartbeat()

	// 处理心跳消息（应被跳过）
	assert.NotPanics(t, func() {
		hub.handleHeartbeatMessage(client)
	})

	// lastHeartbeat 不应被更新（仍为零值）
	assert.Equal(t, oldHB, client.GetLastHeartbeat(), "已关闭客户端的心跳不应更新 lastHeartbeat")

	// SendChan 不应收到任何消息
	select {
	case <-client.SendChan:
		t.Fatal("已关闭客户端不应收到 pong 响应")
	case <-time.After(100 * time.Millisecond):
		// 预期超时，无消息
	}
}

// TestHeartbeatHandleMessageBeforeCallback 验证前置回调返回 false 时跳过后续处理
func TestHeartbeatHandleMessageBeforeCallback(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("hb-cb", "hb-cb-user")
	called := false
	hub.beforeHeartbeatCallback = func(c *Client) bool {
		called = true
		return false // 阻止后续处理
	}
	defer func() { hub.beforeHeartbeatCallback = nil }()

	oldHB := client.GetLastHeartbeat()
	hub.handleHeartbeatMessage(client)

	assert.True(t, called, "前置回调应被调用")
	assert.Equal(t, oldHB, client.GetLastHeartbeat(), "前置回调返回 false 时不应更新 lastHeartbeat")

	// 不应收到 pong
	select {
	case <-client.SendChan:
		t.Fatal("前置回调返回 false 时不应发送 pong")
	case <-time.After(100 * time.Millisecond):
	}
}

// TestHeartbeatHandleMessageAfterCallbacks 验证心跳上报/后置回调被触发
func TestHeartbeatHandleMessageAfterCallbacks(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("hb-after", "hb-after-user")
	reportCalled := false
	afterCalled := false
	hub.heartbeatReportCallback = func(c *Client) {
		reportCalled = true
		assert.Equal(t, client.ID, c.ID)
	}
	hub.afterHeartbeatCallback = func(c *Client) {
		afterCalled = true
		assert.Equal(t, client.ID, c.ID)
	}
	defer func() {
		hub.heartbeatReportCallback = nil
		hub.afterHeartbeatCallback = nil
	}()

	hub.handleHeartbeatMessage(client)

	assert.True(t, reportCalled, "心跳上报回调应被触发")
	assert.True(t, afterCalled, "后置回调应被触发")
}

// ============================================================================
// SetHeartbeatConfig
// ============================================================================

// TestSetHeartbeatConfig 验证心跳配置更新生效
func TestSetHeartbeatConfig(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	newInterval := 15 * time.Second
	newTimeout := 45 * time.Second
	hub.SetHeartbeatConfig(newInterval, newTimeout)

	assert.Equal(t, newInterval, hub.config.HeartbeatInterval, "心跳间隔应更新")
	assert.Equal(t, newTimeout, hub.config.ClientTimeout, "心跳超时应更新")
}

// ============================================================================
// checkHeartbeat（定义在 message_handler.go）
// ============================================================================

// TestCheckHeartbeatTimeout 验证超时未心跳的客户端被注销清理
func TestCheckHeartbeatTimeout(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	// 设置较短的超时时间便于测试
	config.ClientTimeout = 200 * time.Millisecond

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	require.NoError(t, hub.WaitForStartWithTimeout(5*time.Second))

	// 创建一个 LastSeen 为过去时间的 SSE 客户端（超时）
	// SSE 客户端由 checkHeartbeat 扫描兜底（WebSocket 由 heartbeatTimer 时间轮 O(1) 管理）
	timeoutClient := makeTestClient("chk-timeout", "chk-timeout-user")
	timeoutClient.ConnectionType = ConnectionTypeSSE
	timeoutClient.SetLastSeen(time.Now().Add(-10 * time.Second))
	hub.shardedRegistry.AddClient(timeoutClient)

	// 创建一个 LastSeen 为当前时间的 SSE 客户端（未超时）
	activeClient := makeTestClient("chk-active", "chk-active-user")
	activeClient.ConnectionType = ConnectionTypeSSE
	activeClient.SetLastSeen(time.Now())
	hub.shardedRegistry.AddClient(activeClient)

	require.True(t, hub.HasClient("chk-timeout"), "超时客户端应已注册")
	require.True(t, hub.HasClient("chk-active"), "活跃客户端应已注册")

	// 执行心跳检查
	hub.checkHeartbeat()

	// 超时客户端应被注销（通过 EventLoop 异步处理）
	require.Eventually(t, func() bool {
		return !hub.HasClient("chk-timeout")
	}, 3*time.Second, 20*time.Millisecond, "超时客户端应被注销")

	// 活跃客户端应仍然存在
	assert.True(t, hub.HasClient("chk-active"), "活跃客户端不应被注销")
}

// TestCheckHeartbeatNormal 验证未超时的客户端不受心跳检查影响
func TestCheckHeartbeatNormal(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// setupGroupTestHub 默认 ClientTimeout=90s

	// 创建最近心跳的客户端
	client1 := makeTestClient("chk-n1", "chk-u1")
	client1.SetLastHeartbeat(time.Now())
	hub.shardedRegistry.AddClient(client1)

	client2 := makeTestClient("chk-n2", "chk-u2")
	client2.SetLastHeartbeat(time.Now())
	hub.shardedRegistry.AddClient(client2)

	require.Equal(t, int64(2), hub.shardedRegistry.GetClientCount())

	// 执行心跳检查（不运行 Hub，checkHeartbeat 直接遍历 registry）
	// 由于 lastHeartbeat 为当前时间，不会被判定为超时
	hub.checkHeartbeat()

	// 两个客户端应仍然存在
	assert.True(t, hub.HasClient("chk-n1"), "未超时客户端不应被注销")
	assert.True(t, hub.HasClient("chk-n2"), "未超时客户端不应被注销")
	assert.Equal(t, int64(2), hub.shardedRegistry.GetClientCount())
}

// TestCheckHeartbeatSSEUsesLastSeen 验证 SSE 客户端使用 lastSeen 而非 lastHeartbeat 判断超时
func TestCheckHeartbeatSSEUsesLastSeen(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	config.ClientTimeout = 200 * time.Millisecond

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	require.NoError(t, hub.WaitForStartWithTimeout(5*time.Second))

	// SSE 客户端：lastHeartbeat 很久以前，但 lastSeen 是最近时间
	sseClient := &Client{
		ID:             "sse-chk",
		UserID:         "sse-chk-user",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeSSE,
		SendChan:       make(chan []byte, 16),
		Context:        context.Background(),
	}
	sseClient.SetLastHeartbeat(time.Now().Add(-10 * time.Second)) // 旧心跳
	sseClient.SetLastSeen(time.Now())                             // 最近活跃
	hub.shardedRegistry.AddClient(sseClient)

	// 执行心跳检查（SSE 应使用 lastSeen，未超时）
	hub.checkHeartbeat()

	// SSE 客户端应仍然存在（因为 lastSeen 是最近时间）
	assert.True(t, hub.HasClient("sse-chk"), "SSE 客户端应使用 lastSeen 判断，未超时不应被注销")
}

// ============================================================================
// 协议级 PING 单写者路径（setupPingHandler / PongCh / 写泵 pong 写出）
// ============================================================================

// TestProtocolPingSingleWriterPong 验证协议级 PING 的单写者 pong 路径：
// 1. PING 刷新客户端心跳（协议级 PING 与应用层心跳等效保活）
// 2. pong 控制帧由写泵统一写出（客户端可正确收到带相同 appData 的 pong）
func TestProtocolPingSingleWriterPong(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.SafeShutdown()
	go hub.Run()
	require.NoError(t, hub.WaitForStartWithTimeout(5*time.Second))

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("ping-sw-client", "ping-sw-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool {
		return hub.HasClient(client.ID)
	}, 2*time.Second, 20*time.Millisecond, "客户端应注册成功")

	// 记录初始心跳（注册时已设置，PING 后应更新）
	before := client.GetLastHeartbeat()

	// 客户端设置 pong handler（gorilla 控制帧不会从 ReadMessage 返回，需 handler 捕获）
	pongCh := make(chan string, 1)
	cConn.SetPongHandler(func(appData string) error {
		select {
		case pongCh <- appData:
		default:
		}
		return nil
	})

	// 客户端发协议级 PING
	require.NoError(t, cConn.WriteControl(websocket.PingMessage, []byte("hb-1"), time.Now().Add(time.Second)))

	// 驱动客户端读循环分发控制帧（gorilla 要求持续读才能触发 pong handler）
	go func() {
		for {
			if _, _, err := cConn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// 断言收到 pong（appData 回显）
	select {
	case appData := <-pongCh:
		assert.Equal(t, "hb-1", appData, "pong 应回显 PING 的 appData")
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 pong 控制帧（单写者路径：写泵应统一写出 pong）")
	}

	// 断言心跳已刷新（协议级 PING 走 touchHeartbeat 保活路径）
	require.Eventually(t, func() bool {
		return client.GetLastHeartbeat().After(before)
	}, 2*time.Second, 20*time.Millisecond, "协议级 PING 应刷新 LastHeartbeat")
}

// TestProtocolPingWithDataConcurrentWrites 数据帧与 PING 并发时连接不损坏（单写者冒烟测试）
// 修复前：gorilla 默认 ping handler 在读协程内直接 WriteControl 写 pong，
// 与写泵 WriteMessage 的数据帧并发写同一 TCP 连接，帧交错会导致连接损坏
func TestProtocolPingWithDataConcurrentWrites(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.SafeShutdown()
	go hub.Run()
	require.NoError(t, hub.WaitForStartWithTimeout(5*time.Second))

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("ping-sw-stress", "ping-sw-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool {
		return hub.HasClient(client.ID)
	}, 2*time.Second, 20*time.Millisecond, "客户端应注册成功")

	// 客户端持续读：分发 pong 控制帧 + 消费数据帧，统计收到的数据帧与读错误
	var received int32
	var readErr atomic.Value
	go func() {
		for {
			mt, _, err := cConn.ReadMessage()
			if err != nil {
				readErr.Store(err)
				return
			}
			if mt == websocket.TextMessage {
				atomic.AddInt32(&received, 1)
			}
		}
	}()

	// 客户端高频发 PING（触发 pong 与数据帧的并发写窗口）
	pingDone := make(chan struct{})
	go func() {
		defer close(pingDone)
		for i := 0; i < 200; i++ {
			_ = cConn.WriteControl(websocket.PingMessage, []byte("p"), time.Now().Add(time.Second))
			time.Sleep(2 * time.Millisecond)
		}
	}()

	// 同时写泵持续写数据帧（TrySend 直接入队，写泵批量写出）
	payload, err := json.Marshal(NewHubMessage().SetMessageType(MessageTypeText).SetContent("single-writer-stress"))
	require.NoError(t, err)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client.TrySend(payload)
		select {
		case <-pingDone:
			// PING 发完即可停止数据帧写入
		default:
		}
	}
	<-pingDone

	// 等待写泵排空积压
	time.Sleep(300 * time.Millisecond)

	// 无读错误（连接未损坏）且数据帧正常送达
	assert.Nil(t, readErr.Load(), "数据帧与 pong 并发写时连接不应损坏")
	assert.Greater(t, atomic.LoadInt32(&received), int32(0), "应收到数据帧")
}

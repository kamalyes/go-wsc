/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-23 00:00:00
 * @FilePath: \go-wsc\hub\handler_test.go
 * @Description: 处理器测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// ============================================================================
// 心跳重连机制测试
// ============================================================================

// TestClosedClientHeartbeatCount 测试已关闭客户端心跳计数
func TestClosedClientHeartbeatCount(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "closed-hb-test",
		UserID:        "closed-hb-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 标记客户端为已关闭
	client.MarkClosed()

	// 发送心跳，计数应该递增
	assert.Equal(t, int32(0), client.GetClosedHeartbeatCount())

	hub.handleHeartbeatMessage(client)
	assert.Equal(t, int32(1), client.GetClosedHeartbeatCount())

	hub.handleHeartbeatMessage(client)
	assert.Equal(t, int32(2), client.GetClosedHeartbeatCount())

	hub.handleHeartbeatMessage(client)
	// 第3次达到阈值，触发重连后计数器重置
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), client.GetClosedHeartbeatCount(), "重连后计数器应重置")
}

// TestClosedClientAutoRecoverAndPong 测试超过阈值自动恢复并发送 pong
func TestClosedClientAutoRecoverAndPong(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "auto-recover-test",
		UserID:        "auto-recover-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 标记客户端为已关闭
	client.MarkClosed()
	assert.True(t, client.IsClosed())

	// 发送3次心跳（默认阈值3），触发自动恢复
	hub.handleHeartbeatMessage(client)
	hub.handleHeartbeatMessage(client)
	hub.handleHeartbeatMessage(client)
	time.Sleep(100 * time.Millisecond)

	// 客户端应已恢复
	assert.False(t, client.IsClosed(), "客户端应已恢复为活跃状态")

	// SendChan 中应有 pong 响应消息
	assert.Greater(t, len(client.SendChan), 0, "应有 pong 响应消息")
}

// TestClosedClientReconnectPreventDuplicate 测试防重事件
func TestClosedClientReconnectPreventDuplicate(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	var callbackCount atomic.Int32

	hub.OnClosedClientHeartbeat(func(client *Client, heartbeatCount int32) {
		callbackCount.Add(1)
	})

	client := &Client{
		ID:            "dedup-test",
		UserID:        "dedup-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	client.MarkClosed()

	// 连续发送6次心跳（2倍阈值），回调只应触发1次
	// 因为第一次达到阈值后客户端已恢复，后续心跳走正常流程
	for i := 0; i < 6; i++ {
		hub.handleHeartbeatMessage(client)
		time.Sleep(20 * time.Millisecond)
	}

	assert.Equal(t, int32(1), callbackCount.Load(), "回调只应触发1次（防重）")
}

// TestClosedClientReconnectWithNilSendChan 测试 SendChan 为 nil 时自动重建
func TestClosedClientReconnectWithNilSendChan(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "nil-chan-test",
		UserID:        "nil-chan-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 模拟 SendChan 被回收（与 initClientSendChan 使用同一把锁）
	client.CloseMu.Lock()
	client.MarkClosed()
	client.SendChan = nil
	client.CloseMu.Unlock()

	// 发送3次心跳触发恢复
	hub.handleHeartbeatMessage(client)
	hub.handleHeartbeatMessage(client)
	hub.handleHeartbeatMessage(client)
	time.Sleep(100 * time.Millisecond)

	// 客户端应已恢复，SendChan 应已重建
	assert.False(t, client.IsClosed(), "客户端应已恢复")
	assert.NotNil(t, client.SendChan, "SendChan 应已重建")
}

// TestClosedClientCustomThreshold 测试自定义阈值
func TestClosedClientCustomThreshold(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	hub.SetClosedHeartbeatThreshold(5)

	var callbackCount atomic.Int32

	hub.OnClosedClientHeartbeat(func(client *Client, heartbeatCount int32) {
		callbackCount.Add(1)
	})

	client := &Client{
		ID:            "custom-threshold-test",
		UserID:        "custom-threshold-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	client.MarkClosed()

	// 发送4次心跳，不应触发恢复
	for i := 0; i < 4; i++ {
		hub.handleHeartbeatMessage(client)
	}
	assert.Equal(t, int32(0), callbackCount.Load(), "4次心跳不应触发阈值5的回调")
	assert.True(t, client.IsClosed(), "客户端应仍为关闭状态")

	// 第5次心跳，触发恢复
	hub.handleHeartbeatMessage(client)
	time.Sleep(100 * time.Millisecond)

	assert.Equal(t, int32(1), callbackCount.Load(), "5次心跳应触发回调")
	assert.False(t, client.IsClosed(), "客户端应已恢复")
}

// TestActiveClientHeartbeatNoCount 测试活跃客户端心跳不影响计数器
func TestActiveClientHeartbeatNoCount(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "active-test",
		UserID:        "active-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 客户端未关闭，发送心跳
	hub.handleHeartbeatMessage(client)

	assert.Equal(t, int32(0), client.GetClosedHeartbeatCount(), "活跃客户端不应增加关闭心跳计数")
}

// TestTryStartReconnectPreventDuplicate 测试 TryStartReconnect 防重
func TestTryStartReconnectPreventDuplicate(t *testing.T) {
	client := &Client{
		ID:       "reconnect-dedup",
		UserID:   "reconnect-dedup-user",
		UserType: UserTypeCustomer,
	}

	// 第一次应成功
	assert.True(t, client.TryStartReconnect())
	assert.True(t, client.IsReconnecting())

	// 第二次应失败（防重）
	assert.False(t, client.TryStartReconnect())

	// 结束重连后可以再次进入
	client.FinishReconnect()
	assert.False(t, client.IsReconnecting())
	assert.True(t, client.TryStartReconnect())
	client.FinishReconnect()
}

// ============================================================================
// Overflow Buffer 测试
// ============================================================================

// TestOverflowBufferBasic 测试 overflow buffer 基本功能
func TestOverflowBufferBasic(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	client := &Client{
		ID:                 "overflow-test",
		UserID:             "overflow-user",
		UserType:           UserTypeCustomer,
		Status:             UserStatusOnline,
		LastSeen:           time.Now(),
		LastHeartbeat:      time.Now(),
		SendChan:           make(chan []byte, 1), // 容量为1
		Context:            context.Background(),
		OverflowBufferSize: 4,
	}

	// 第一次发送应成功（填满 SendChan）
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg1")))

	// SendChan 已满，消息应进入 overflow buffer
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg2")))
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg3")))

	// overflow 应有2条消息
	assert.Equal(t, 2, client.OverflowLen())

	client.StopOverflowDrain()
}

// TestOverflowBufferFull 测试 overflow buffer 满时丢弃旧消息
func TestOverflowBufferFull(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	client := &Client{
		ID:                 "overflow-full-test",
		UserID:             "overflow-full-user",
		UserType:           UserTypeCustomer,
		Status:             UserStatusOnline,
		LastSeen:           time.Now(),
		LastHeartbeat:      time.Now(),
		SendChan:           make(chan []byte, 1),
		Context:            context.Background(),
		OverflowBufferSize: 2,
	}

	// 填满 SendChan
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg1")))

	// 写入 overflow
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg2")))
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg3")))
	assert.Equal(t, 2, client.OverflowLen())

	// overflow 满了，丢弃最早的 msg2，保留 msg3
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg4")))
	assert.Equal(t, 2, client.OverflowLen())

	client.StopOverflowDrain()
}

// TestOverflowBufferDrain 测试 overflow buffer drain 回灌
func TestOverflowBufferDrain(t *testing.T) {
	client := &Client{
		ID:                 "overflow-drain-test",
		UserID:             "overflow-drain-user",
		UserType:           UserTypeCustomer,
		Status:             UserStatusOnline,
		LastSeen:           time.Now(),
		LastHeartbeat:      time.Now(),
		SendChan:           make(chan []byte, 10), // 足够大
		Context:            context.Background(),
		OverflowBufferSize: 64,
	}

	// 直接发送应成功
	assert.True(t, client.TrySend([]byte("direct-msg")))
	assert.Equal(t, 0, client.OverflowLen())

	// 清空 SendChan
	<-client.SendChan

	client.StopOverflowDrain()
}

// TestSSEOverflowBufferBasic 测试 SSE overflow buffer 基本功能
func TestSSEOverflowBufferBasic(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	client := &Client{
		ID:                 "sse-overflow-test",
		UserID:             "sse-overflow-user",
		UserType:           UserTypeCustomer,
		Status:             UserStatusOnline,
		LastSeen:           time.Now(),
		LastHeartbeat:      time.Now(),
		ConnectionType:     ConnectionTypeSSE,
		SSEMessageCh:       make(chan *HubMessage, 1),
		Context:            context.Background(),
		OverflowBufferSize: 4,
	}

	msg1 := &HubMessage{ID: "msg1", Content: "hello"}
	msg2 := &HubMessage{ID: "msg2", Content: "world"}
	msg3 := &HubMessage{ID: "msg3", Content: "overflow"}

	assert.True(t, hub.TrySendSSEWithOverflow(client, msg1))
	assert.True(t, hub.TrySendSSEWithOverflow(client, msg2))
	assert.True(t, hub.TrySendSSEWithOverflow(client, msg3))

	assert.Equal(t, 2, client.SSEOverflowLen())

	client.StopSSEOverflowDrain()
}

// TestClosedClientOverflowDrainStop 测试客户端关闭时停止 drain goroutine
func TestClosedClientOverflowDrainStop(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	client := &Client{
		ID:                 "drain-stop-test",
		UserID:             "drain-stop-user",
		UserType:           UserTypeCustomer,
		Status:             UserStatusOnline,
		LastSeen:           time.Now(),
		LastHeartbeat:      time.Now(),
		SendChan:           make(chan []byte, 1),
		Context:            context.Background(),
		OverflowBufferSize: 64,
	}

	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg1")))
	assert.True(t, hub.TrySendWithOverflow(client, []byte("msg2")))

	client.MarkClosed()
	client.StopOverflowDrain()
}

// ============================================================================
// Pong 失败计数器阈值测试
// ============================================================================

// TestPongFailThreshold_UnregisterAfterThreshold 连续 pong 失败超过阈值后注销客户端
func TestPongFailThreshold_UnregisterAfterThreshold(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "pong-fail-client",
		UserID:        "pong-fail-user",
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 1),
		Context:       context.Background(),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 填满 SendChan 使 TrySend 失败，同时不启动消费者让阻塞重试也超时
	client.SendChan <- []byte("filler")

	// 模拟 2 次心跳（默认阈值 3，未达阈值）
	for i := 0; i < 2; i++ {
		hub.handleHeartbeatMessage(client)
		assert.Equal(t, int32(i+1), client.GetPongFailCount())
	}

	// 客户端应仍存在（未达阈值）
	_, exists := hub.GetClientByIDWithLock(client.ID)
	assert.True(t, exists, "未达阈值，客户端应仍存在")

	// 第 3 次心跳，达到阈值，应被注销
	hub.handleHeartbeatMessage(client)

	// 等待注销完成
	time.Sleep(100 * time.Millisecond)

	_, exists = hub.GetClientByIDWithLock(client.ID)
	assert.False(t, exists, "达到阈值后客户端应被注销")
}

// TestPongFailThreshold_ResetOnSuccess pong 成功后重置失败计数器
func TestPongFailThreshold_ResetOnSuccess(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "pong-reset-client",
		UserID:        "pong-reset-user",
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 先手动设置失败计数
	client.IncrPongFailCount()
	client.IncrPongFailCount()
	assert.Equal(t, int32(2), client.GetPongFailCount())

	// 启动消费者
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-client.SendChan:
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	// 发送心跳，pong 成功，计数器应重置
	hub.handleHeartbeatMessage(client)
	assert.Equal(t, int32(0), client.GetPongFailCount(), "pong 成功后计数器应重置为 0")
}

// TestPongFailThreshold_CustomThreshold 自定义 pong 失败阈值
func TestPongFailThreshold_CustomThreshold(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	hub.PongFailThreshold = 2 // 自定义阈值为 2
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "pong-custom-threshold",
		UserID:        "pong-custom-user",
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 1),
		Context:       context.Background(),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 填满 SendChan 使 pong 发送失败
	client.SendChan <- []byte("filler")

	// 第 1 次心跳
	hub.handleHeartbeatMessage(client)
	_, exists := hub.GetClientByIDWithLock(client.ID)
	assert.True(t, exists, "第 1 次失败，客户端应仍存在")

	// 第 2 次心跳，达到阈值 2
	hub.handleHeartbeatMessage(client)
	time.Sleep(100 * time.Millisecond)

	_, exists = hub.GetClientByIDWithLock(client.ID)
	assert.False(t, exists, "达到自定义阈值 2 后客户端应被注销")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-23 15:32:49
 * @FilePath: \go-wsc\models\client_test.go
 * @Description: 客户端发送测试（纯 channel 操作，overflow 逻辑在 hub 包测试）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// ============================================================================
// TrySend 基础测试
// ============================================================================

// TestTrySend_DirectSuccess SendChan 有空间时直接发送成功
func TestTrySend_DirectSuccess(t *testing.T) {
	client := &Client{
		ID:            "try-send-direct",
		UserID:        "user1",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	assert.True(t, client.TrySend([]byte("hello")))
	assert.Equal(t, 1, len(client.SendChan))

	// 读取验证内容
	msg := <-client.SendChan
	assert.Equal(t, "hello", string(msg))
}

// TestTrySend_ClosedClient 已关闭客户端发送失败
func TestTrySend_ClosedClient(t *testing.T) {
	client := &Client{
		ID:            "try-send-closed",
		UserID:        "user1",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	client.MarkClosed()
	assert.False(t, client.TrySend([]byte("hello")))
}

// TestTrySend_NilSendChan SendChan 为 nil 时发送失败
func TestTrySend_NilSendChan(t *testing.T) {
	client := &Client{
		ID:            "try-send-nil",
		UserID:        "user1",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      nil,
		Context:       context.Background(),
	}

	assert.False(t, client.TrySend([]byte("hello")))
}

// TestTrySend_ChannelFull SendChan 满时返回 false
func TestTrySend_ChannelFull(t *testing.T) {
	client := &Client{
		ID:            "try-send-full",
		UserID:        "user1",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 1),
		Context:       context.Background(),
	}

	// 填满 SendChan
	assert.True(t, client.TrySend([]byte("first")))
	// 通道满，返回 false
	assert.False(t, client.TrySend([]byte("second")))
}

// ============================================================================
// TrySendSSE 基础测试
// ============================================================================

// TestTrySendSSE_DirectSuccess SSEMessageCh 有空间时直接发送成功
func TestTrySendSSE_DirectSuccess(t *testing.T) {
	client := &Client{
		ID:             "sse-direct",
		UserID:         "user1",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		LastSeen:       time.Now(),
		LastHeartbeat:  time.Now(),
		ConnectionType: ConnectionTypeSSE,
		SSEMessageCh:   make(chan *HubMessage, 10),
		Context:        context.Background(),
	}

	msg := &HubMessage{ID: "m1", Content: "hello"}
	assert.True(t, client.TrySendSSE(msg))

	received := <-client.SSEMessageCh
	assert.Equal(t, "m1", received.ID)
}

// TestTrySendSSE_ClosedClient 已关闭客户端发送失败
func TestTrySendSSE_ClosedClient(t *testing.T) {
	client := &Client{
		ID:             "sse-closed",
		UserID:         "user1",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		LastSeen:       time.Now(),
		LastHeartbeat:  time.Now(),
		ConnectionType: ConnectionTypeSSE,
		SSEMessageCh:   make(chan *HubMessage, 10),
		Context:        context.Background(),
	}

	client.MarkClosed()
	assert.False(t, client.TrySendSSE(&HubMessage{ID: "m1"}))
}

// TestTrySendSSE_NilChannel SSEMessageCh 为 nil 时发送失败
func TestTrySendSSE_NilChannel(t *testing.T) {
	client := &Client{
		ID:             "sse-nil",
		UserID:         "user1",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		LastSeen:       time.Now(),
		LastHeartbeat:  time.Now(),
		ConnectionType: ConnectionTypeSSE,
		SSEMessageCh:   nil,
		Context:        context.Background(),
	}

	assert.False(t, client.TrySendSSE(&HubMessage{ID: "m1"}))
}

// TestTrySendSSE_ChannelFull SSEMessageCh 满时返回 false
func TestTrySendSSE_ChannelFull(t *testing.T) {
	client := &Client{
		ID:             "sse-full",
		UserID:         "user1",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		LastSeen:       time.Now(),
		LastHeartbeat:  time.Now(),
		ConnectionType: ConnectionTypeSSE,
		SSEMessageCh:   make(chan *HubMessage, 1),
		Context:        context.Background(),
	}

	assert.True(t, client.TrySendSSE(&HubMessage{ID: "m1"}))
	assert.False(t, client.TrySendSSE(&HubMessage{ID: "m2"}))
}

// ============================================================================
// 并发安全测试
// ============================================================================

// TestTrySend_ConcurrentSafety 多 goroutine 并发 TrySend
func TestTrySend_ConcurrentSafety(t *testing.T) {
	client := &Client{
		ID:            "concurrent-send",
		UserID:        "user1",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 256),
		Context:       context.Background(),
	}

	var wg sync.WaitGroup
	var successCount atomic.Int32
	const goroutines = 50
	const messagesPerGoroutine = 20

	// 消费者：持续读取 SendChan
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

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerGoroutine; j++ {
				if client.TrySend([]byte("msg")) {
					successCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	close(done)

	t.Logf("并发发送完成: 成功 %d/%d", successCount.Load(), goroutines*messagesPerGoroutine)
}

// TestTrySendSSE_ConcurrentSafety 多 goroutine 并发 TrySendSSE
func TestTrySendSSE_ConcurrentSafety(t *testing.T) {
	client := &Client{
		ID:             "concurrent-sse",
		UserID:         "user1",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		LastSeen:       time.Now(),
		LastHeartbeat:  time.Now(),
		ConnectionType: ConnectionTypeSSE,
		SSEMessageCh:   make(chan *HubMessage, 256),
		Context:        context.Background(),
	}

	var wg sync.WaitGroup
	var successCount atomic.Int32
	const goroutines = 50
	const messagesPerGoroutine = 20

	// 消费者
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-client.SSEMessageCh:
			case <-done:
				return
			}
		}
	}()

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerGoroutine; j++ {
				if client.TrySendSSE(&HubMessage{ID: "msg"}) {
					successCount.Add(1)
				}
			}
		}(i)
	}

	wg.Wait()
	close(done)

	t.Logf("SSE 并发发送完成: 成功 %d/%d", successCount.Load(), goroutines*messagesPerGoroutine)
}

// ============================================================================
// 边界条件测试
// ============================================================================

// TestTrySend_EmptyData 发送空数据
func TestTrySend_EmptyData(t *testing.T) {
	client := &Client{
		ID:            "empty-data",
		UserID:        "user1",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	assert.True(t, client.TrySend([]byte{}))
	msg := <-client.SendChan
	assert.Equal(t, 0, len(msg))
}

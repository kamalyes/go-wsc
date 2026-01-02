/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 09:26:00
 * @FilePath: \go-wsc\ack_test.go
 * @Description: ACK消息确认机制测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// 测试常量
const (
	testMessage              = "Test message"
	testUser1                = "user-1"
	testUser2                = "user-2"
	testUser3                = "user-3"
	testClient1              = "client-1"
	testClient2              = "client-2"
	testClient3              = "client-3"
	testSender1              = "sender-1"
	testSender2              = "sender-2"
	testSender3              = "sender-3"
	msgClientRegisterTimeout = "客户端注册超时"
)

// TestAckManagerCreate 测试创建ACK管理器
func TestAckManagerCreate(t *testing.T) {
	am := NewAckManager(5*time.Second, 3)
	assert.NotNil(t, am)
	assert.Equal(t, 5*time.Second, am.GetTimeout())
	assert.Equal(t, 3, am.GetMaxRetry())
	assert.Equal(t, 0, am.GetPendingCount())
}

// TestAckManagerAddPendingMessage 测试添加待确认消息
func TestAckManagerAddPendingMessage(t *testing.T) {
	am := NewAckManager(5*time.Second, 3)
	msg := &HubMessage{
		ID:          "test-msg-1",
		MessageType: MessageTypeText,
		Content:     testMessage,
	}

	pm := am.AddPendingMessageWithExpire(msg, 2*time.Second, 2)
	assert.NotNil(t, pm)
	assert.Equal(t, msg, pm.Message)
	assert.Equal(t, 2*time.Second, pm.Timeout)
	assert.Equal(t, 2, pm.MaxRetry)
	assert.Equal(t, 1, am.GetPendingCount())
}

// TestAckManagerConfirmSuccess 测试确认消息成功
func TestAckManagerConfirmSuccess(t *testing.T) {
	am := NewAckManager(5*time.Second, 3)
	msg := &HubMessage{
		ID:          "test-msg-2",
		MessageType: MessageTypeText,
		Content:     testMessage,
	}

	// 使用较短的超时避免测试等待太久
	pm := am.AddPendingMessageWithExpire(msg, 200*time.Millisecond, 2)

	// 模拟ACK确认 - 在超时前发送
	go func() {
		time.Sleep(50 * time.Millisecond)
		ack := &AckMessage{
			MessageID: msg.ID,
			Status:    AckStatusConfirmed,
			Timestamp: time.Now(),
		}
		am.ConfirmMessage(msg.ID, ack)
	}()

	// 等待ACK
	ack, err := pm.WaitForAck()
	assert.NoError(t, err)
	assert.NotNil(t, ack)
	assert.Equal(t, AckStatusConfirmed, ack.Status)
	assert.Equal(t, 0, am.GetPendingCount())
}

// TestAckManagerTimeout 测试ACK超时
func TestAckManagerTimeout(t *testing.T) {
	am := NewAckManager(5*time.Second, 3)
	msg := &HubMessage{
		ID:          "test-msg-3",
		MessageType: MessageTypeText,
		Content:     testMessage,
	}

	// 使用较短的超时避免测试超时
	pm := am.AddPendingMessageWithExpire(msg, 100*time.Millisecond, 0)

	// 不发送ACK，等待超时
	ack, err := pm.WaitForAck()
	assert.Error(t, err)
	assert.NotNil(t, ack)
	assert.Equal(t, AckStatusTimeout, ack.Status)
}

// TestAckManagerCleanupExpired 测试清理过期消息
func TestAckManagerCleanupExpired(t *testing.T) {
	am := NewAckManager(5*time.Second, 3)

	// 添加多个消息,使用较短的超时
	// timeout=50ms, maxRetry=0, contextTimeout = 50ms * (0+1) + 1s = 1.05s
	for i := 0; i < 5; i++ {
		msg := &HubMessage{
			ID:          string(rune('a' + i)),
			MessageType: MessageTypeText,
			Content:     testMessage,
		}
		// 设置 maxRetry=0 以缩短 context 超时时间
		am.AddPendingMessageWithExpire(msg, 50*time.Millisecond, 0)
	}

	assert.Equal(t, 5, am.GetPendingCount())

	// 等待所有消息的 context 过期
	// contextTimeout = 50ms * (0+1) + 1s = 1.05s
	// 等待 1.2s 确保所有消息都过期
	time.Sleep(1200 * time.Millisecond)

	// 清理过期消息
	cleaned := am.CleanupExpired()
	assert.Equal(t, 5, cleaned, "应该清理所有5个过期消息")
	assert.Equal(t, 0, am.GetPendingCount(), "清理后应该没有待确认消息")
}

// TestHubSendWithAckEnabled 测试启用ACK的消息发送
func TestHubSendWithAckEnabled(t *testing.T) {
	config := wscconfig.Default().
		Enable().
		WithAck(2000 * time.Millisecond)

	t.Logf("配置创建后 EnableAck: %v, AckTimeout: %v", config.EnableAck, config.AckTimeout)

	hub := NewHub(config)
	go hub.Run()
	defer hub.Shutdown()

	// 注册测试客户端
	now := time.Now()
	client := &Client{
		ID:            testClient1,
		UserID:        testUser1,
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
		LastSeen:      now,
		LastHeartbeat: now,
	}
	hub.Register(client)

	// 可靠地等待注册完成，通过检查用户是否在线
	registered := false
	for i := 0; i < 50; i++ { // 最多等待5秒
		if isOnline, _ := hub.IsUserOnline(testUser1); isOnline {
			registered = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !registered {
		t.Fatal(msgClientRegisterTimeout)
	}
	t.Log("客户端注册成功")

	// 模拟客户端处理消息并发送ACK
	go func() {
		// 监听客户端SendChan,收到消息后立即回复ACK
		select {
		case msg := <-client.SendChan:
			// 收到消息,立即发送ACK
			msgStr := "<empty message>"
			if len(msg) > 0 {
				msgStr = string(msg)
			}
			t.Logf("收到消息: %s", msgStr)
			ack := &AckMessage{
				MessageID: "test-msg-with-ack",
				Status:    AckStatusConfirmed,
				Timestamp: time.Now(),
			}
			hub.HandleAck(ack)
			t.Log("已发送ACK")
		case <-time.After(5 * time.Second):
			// 超时,测试失败
			t.Error("未收到消息")
		}
	}()

	// 发送带ACK的消息
	ctx := context.WithValue(context.Background(), ContextKeySenderID, testSender1)
	msg := &HubMessage{
		ID:          "test-msg-with-ack",
		MessageType: MessageTypeText,
		Content:     "Test message with ACK",
	}

	ackMsg, err := hub.SendToUserWithAck(ctx, testUser1, msg, 0, 0)
	t.Logf("EnableAck配置: %v, AckTimeout: %v", hub.GetConfig().EnableAck, hub.GetConfig().AckTimeout)
	assert.NoError(t, err)
	assert.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusConfirmed, ackMsg.Status)

	// 等待ACK处理完成再shutdown
	time.Sleep(100 * time.Millisecond)
}

// TestHubSendWithAckDisabled 测试未启用ACK的消息发送
func TestHubSendWithAckDisabled(t *testing.T) {
	config := wscconfig.Default().Enable()
	// 不调用WithAck，保持EnableAck=false

	hub := NewHub(config)

	// 启动hub
	go hub.Run()
	defer hub.Shutdown()
	// 注册测试客户端 - 在hub.Run()之前注册避免竞争
	now := time.Now()
	client := &Client{
		ID:            testClient1,
		UserID:        testUser1,
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
		LastSeen:      now,
		LastHeartbeat: now,
	}
	hub.Register(client)

	// 可靠地等待注册完成
	registered := false
	for i := 0; i < 50; i++ {
		if isOnline, _ := hub.IsUserOnline(testUser1); isOnline {
			registered = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !registered {
		t.Fatal(msgClientRegisterTimeout)
	}

	// 发送消息（无ACK）
	ctx := context.WithValue(context.Background(), ContextKeySenderID, testSender2)
	msg := &HubMessage{
		MessageType: MessageTypeText,
		Content:     "Test message without ACK",
	}

	ackMsg, err := hub.SendToUserWithAck(ctx, testUser1, msg, 0, 0)
	assert.NoError(t, err)
	assert.Nil(t, ackMsg) // 未启用ACK时返回nil
}

// TestHubSendWithAckRetry 测试启用ACK的消息重试
func TestHubSendWithAckRetry(t *testing.T) {
	config := wscconfig.Default().
		Enable().
		WithAck(500 * time.Millisecond) // 减少超时时间到500ms

	t.Logf("配置创建后 EnableAck: %v, AckTimeout: %v", config.EnableAck, config.AckTimeout)

	hub := NewHub(config)
	go hub.Run()

	// 注册测试客户端
	client := &Client{
		ID:       testClient3,
		UserID:   testUser3,
		SendChan: make(chan []byte, 10),
		Context:  context.Background(),
		LastSeen: time.Now(), // 设置最后活跃时间防止被清理
	}
	hub.Register(client)

	// 可靠地等待注册完成
	registered := false
	for i := 0; i < 50; i++ {
		if isOnline, _ := hub.IsUserOnline(testUser3); isOnline {
			registered = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !registered {
		t.Fatal(msgClientRegisterTimeout)
	}

	// 模拟在第2次重试后返回ACK
	done := make(chan struct{})
	messageCount := 0
	go func() {
		defer close(done)
		for {
			select {
			case msg := <-client.SendChan:
				messageCount++
				var msgStr string
				if len(msg) > 0 {
					msgStr = string(msg)
					if len(msgStr) > 50 {
						msgStr = msgStr[:50] + "..."
					}
				} else {
					msgStr = "<empty or nil message>"
				}
				t.Logf("收到第%d次消息: %s", messageCount, msgStr)
				// 第1次和第2次忽略,第3次(第2次重试)回复ACK
				if messageCount >= 3 {
					// 稍微延迟一下,确保消息处理完成
					time.Sleep(50 * time.Millisecond)
					ack := &AckMessage{
						MessageID: "test-msg-retry",
						Status:    AckStatusConfirmed,
						Timestamp: time.Now(),
					}
					hub.HandleAck(ack)
					t.Log("已在第3次消息后发送ACK")
					return
				}
			case <-time.After(5 * time.Second):
				t.Errorf("超时,只收到%d次消息", messageCount)
				return
			}
		}
	}()

	// 发送带ACK的消息
	ctx := context.WithValue(context.Background(), ContextKeySenderID, testSender3)
	msg := &HubMessage{
		ID:          "test-msg-retry",
		MessageType: MessageTypeText,
		Content:     "Test message with retry",
	}

	ackMsg, err := hub.SendToUserWithAck(ctx, testUser3, msg, 0, 2) // 明确设置maxRetry为2
	assert.NoError(t, err)
	assert.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusConfirmed, ackMsg.Status)

	// 等待ACK处理完成
	<-done
	time.Sleep(100 * time.Millisecond)

	hub.Shutdown()
}

// TestAckRetrySuccess 测试重试成功
func TestAckRetrySuccess(t *testing.T) {
	am := NewAckManager(5*time.Second, 3)
	msg := &HubMessage{
		ID:          "test-retry-msg",
		MessageType: MessageTypeText,
		Content:     "Test retry message",
	}

	pm := am.AddPendingMessageWithExpire(msg, 150*time.Millisecond, 2)

	// 记录重试次数
	var retryCount int32

	// 重试函数
	retryFunc := func() error {
		current := atomic.AddInt32(&retryCount, 1)
		t.Logf("重试发送消息，第 %d 次", current-1)

		// 在第2次重试时发送ACK
		if current == 2 {
			go func() {
				time.Sleep(10 * time.Millisecond)
				ack := &AckMessage{
					MessageID: msg.ID,
					Status:    AckStatusConfirmed,
					Timestamp: time.Now(),
				}
				am.ConfirmMessage(msg.ID, ack)
			}()
		}
		return nil
	}

	// 等待ACK并重试
	ack, err := pm.WaitForAckWithRetry(retryFunc)
	assert.NoError(t, err)
	assert.NotNil(t, ack)
	assert.Equal(t, AckStatusConfirmed, ack.Status)
}

// TestAckRetryExhausted 测试重试次数耗尽
func TestAckRetryExhausted(t *testing.T) {
	am := NewAckManager(5*time.Second, 3)
	msg := &HubMessage{
		ID:          "test-exhaust-msg",
		MessageType: MessageTypeText,
		Content:     "Test exhaust message",
	}

	pm := am.AddPendingMessageWithExpire(msg, 100*time.Millisecond, 1)

	// 重试函数
	retryCount := 0
	retryFunc := func() error {
		retryCount++
		t.Logf("重试发送消息，第 %d 次", retryCount)
		return nil
	}

	// 等待ACK并重试（不发送ACK，等待超时）
	ack, err := pm.WaitForAckWithRetry(retryFunc)
	assert.Error(t, err)
	assert.NotNil(t, ack)
	assert.Equal(t, AckStatusTimeout, ack.Status)
	assert.Equal(t, 1, retryCount) // 应该重试了1次
}

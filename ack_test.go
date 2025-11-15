/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-15
 * @FilePath: \go-wsc\ack_test.go
 * @Description: ACK消息确认机制测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestAckManager 测试ACK管理器基本功能
func TestAckManager(t *testing.T) {
	t.Run("创建ACK管理器", func(t *testing.T) {
		am := NewAckManager(5*time.Second, 3)
		assert.NotNil(t, am)
		assert.Equal(t, 5*time.Second, am.defaultAckTimeout)
		assert.Equal(t, 3, am.maxRetry)
		assert.Equal(t, 0, am.GetPendingCount())
	})

	t.Run("添加待确认消息", func(t *testing.T) {
		am := NewAckManager(5*time.Second, 3)
		msg := &HubMessage{
			ID:      "test-msg-1",
			Type:    MessageTypeText,
			Content: "Test message",
		}

		pm := am.AddPendingMessage(msg, 2*time.Second, 2)
		assert.NotNil(t, pm)
		assert.Equal(t, msg, pm.Message)
		assert.Equal(t, 2*time.Second, pm.Timeout)
		assert.Equal(t, 2, pm.MaxRetry)
		assert.Equal(t, 1, am.GetPendingCount())
	})

	t.Run("确认消息成功", func(t *testing.T) {
		am := NewAckManager(5*time.Second, 3)
		msg := &HubMessage{
			ID:      "test-msg-2",
			Type:    MessageTypeText,
			Content: "Test message",
		}

		pm := am.AddPendingMessage(msg, 5*time.Second, 2)

		// 模拟ACK确认
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
	})

	t.Run("ACK超时", func(t *testing.T) {
		am := NewAckManager(5*time.Second, 3)
		msg := &HubMessage{
			ID:      "test-msg-3",
			Type:    MessageTypeText,
			Content: "Test message",
		}

		// 使用较短的expireDuration避免测试超时
		pm := am.AddPendingMessageWithExpire(msg, 200*time.Millisecond, 0, 1*time.Second)

		// 不发送ACK，等待超时
		ack, err := pm.WaitForAck()
		assert.Error(t, err)
		assert.NotNil(t, ack)
		assert.Equal(t, AckStatusTimeout, ack.Status)
	})

	t.Run("清理过期消息", func(t *testing.T) {
		am := NewAckManager(5*time.Second, 3)
		
		// 添加多个消息,使用较短的expireDuration
		for i := 0; i < 5; i++ {
			msg := &HubMessage{
				ID:      string(rune('a' + i)),
				Type:    MessageTypeText,
				Content: "Test message",
			}
			am.AddPendingMessageWithExpire(msg, 100*time.Millisecond, 0, 300*time.Millisecond)
		}

		assert.Equal(t, 5, am.GetPendingCount())

		// 等待消息过期
		time.Sleep(400 * time.Millisecond)

		// 清理过期消息
		cleaned := am.CleanupExpired()
		assert.Equal(t, 5, cleaned)
		assert.Equal(t, 0, am.GetPendingCount())
	})
}

// TestHubWithAck 测试Hub的ACK功能
func TestHubWithAck(t *testing.T) {
	t.Run("启用ACK的消息发送", func(t *testing.T) {
		config := DefaultHubConfig()
		config.EnableAck = true
		config.AckTimeout = 2 * time.Second
		config.MaxRetry = 2

		hub := NewHub(config)
		go hub.Run()
		defer hub.Shutdown()

		// 注册测试客户端
		client := &Client{
			ID:       "client-1",
			UserID:   "user-1",
			SendChan: make(chan []byte, 10),
			Context:  context.Background(),
		}
		hub.Register(client)

		// 等待注册完成
		time.Sleep(200 * time.Millisecond)

		// 模拟客户端处理消息并发送ACK
		go func() {
			// 监听客户端SendChan,收到消息后立即回复ACK
			select {
			case msg := <-client.SendChan:
				// 收到消息,立即发送ACK
				t.Logf("收到消息: %s", string(msg))
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
		ctx := context.WithValue(context.Background(), ContextKeySenderID, "sender-1")
		msg := &HubMessage{
			ID:      "test-msg-with-ack",
			Type:    MessageTypeText,
			Content: "Test message with ACK",
		}

		ackMsg, err := hub.SendToUserWithAck(ctx, "user-1", msg, 0, 0)
		assert.NoError(t, err)
		assert.NotNil(t, ackMsg)
		assert.Equal(t, AckStatusConfirmed, ackMsg.Status)
		
		// 等待ACK处理完成再shutdown
		time.Sleep(100 * time.Millisecond)
	})

	t.Run("未启用ACK的消息发送", func(t *testing.T) {
		config := DefaultHubConfig()
		config.EnableAck = false

		hub := NewHub(config)
		go hub.Run()
		defer hub.Shutdown()

		// 注册测试客户端
		client := &Client{
			ID:       "client-2",
			UserID:   "user-2",
			SendChan: make(chan []byte, 10),
			Context:  context.Background(),
		}
		hub.Register(client)

		// 等待注册完成
		time.Sleep(100 * time.Millisecond)

		// 发送消息（无ACK）
		ctx := context.WithValue(context.Background(), ContextKeySenderID, "sender-2")
		msg := &HubMessage{
			Type:    MessageTypeText,
			Content: "Test message without ACK",
		}

		ackMsg, err := hub.SendToUserWithAck(ctx, "user-2", msg, 0, 0)
		assert.NoError(t, err)
		assert.Nil(t, ackMsg) // 未启用ACK时返回nil
	})

	t.Run("ACK超时重试", func(t *testing.T) {
		config := DefaultHubConfig()
		config.EnableAck = true
		config.AckTimeout = 300 * time.Millisecond  // 增加超时时间
		config.MaxRetry = 2

		hub := NewHub(config)
		go hub.Run()
		
		// 注册测试客户端
		client := &Client{
			ID:       "client-3",
			UserID:   "user-3",
			SendChan: make(chan []byte, 10),
			Context:  context.Background(),
		}
		hub.Register(client)

		// 等待注册完成
		time.Sleep(100 * time.Millisecond)

		// 模拟在第2次重试后返回ACK
		done := make(chan struct{})
		messageCount := 0
		go func() {
			defer close(done)
			for {
				select {
				case msg := <-client.SendChan:
					messageCount++
					t.Logf("收到第%d次消息: %s", messageCount, string(msg)[:50])
					// 第1次和第2次忽略,第3次(第2次重试)回复ACK
					if messageCount >= 3 {
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
		ctx := context.WithValue(context.Background(), ContextKeySenderID, "sender-3")
		msg := &HubMessage{
			ID:      "test-msg-retry",
			Type:    MessageTypeText,
			Content: "Test message with retry",
		}

		ackMsg, err := hub.SendToUserWithAck(ctx, "user-3", msg, 0, -1)  // maxRetry=-1表示使用默认值
		assert.NoError(t, err)
		assert.NotNil(t, ackMsg)
		assert.Equal(t, AckStatusConfirmed, ackMsg.Status)
		
		hub.Shutdown()
		time.Sleep(100 * time.Millisecond)
	})
}

// TestAckWithRetry 测试重试机制
func TestAckWithRetry(t *testing.T) {
	t.Run("重试成功", func(t *testing.T) {
		am := NewAckManager(5*time.Second, 3)
		msg := &HubMessage{
			ID:      "test-retry-msg",
			Type:    MessageTypeText,
			Content: "Test retry message",
		}

		pm := am.AddPendingMessage(msg, 200*time.Millisecond, 2)

		// 模拟第2次重试后返回ACK
		retryCount := 0
		go func() {
			for {
				time.Sleep(250 * time.Millisecond)
				retryCount++
				if retryCount >= 2 {
					ack := &AckMessage{
						MessageID: msg.ID,
						Status:    AckStatusConfirmed,
						Timestamp: time.Now(),
					}
					am.ConfirmMessage(msg.ID, ack)
					return
				}
			}
		}()

		// 重试函数
		retryFunc := func() error {
			t.Logf("重试发送消息，第 %d 次", retryCount)
			return nil
		}

		// 等待ACK并重试
		ack, err := pm.WaitForAckWithRetry(retryFunc)
		assert.NoError(t, err)
		assert.NotNil(t, ack)
		assert.Equal(t, AckStatusConfirmed, ack.Status)
	})

	t.Run("重试次数耗尽", func(t *testing.T) {
		am := NewAckManager(5*time.Second, 3)
		msg := &HubMessage{
			ID:      "test-exhaust-msg",
			Type:    MessageTypeText,
			Content: "Test exhaust message",
		}

		pm := am.AddPendingMessage(msg, 100*time.Millisecond, 1)

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
	})
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 08:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:30:00
 * @FilePath: \go-wsc\hub_multi_client_test.go
 * @Description: 多端登录消息发送测试
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
	"github.com/stretchr/testify/require"
)

// TestMultiClientMessageSend 测试多端登录时的消息发送
func TestMultiClientMessageSend(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true // 允许多端登录
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	const (
		testUserID   = "multi-user-001"
		testClient1  = "client-001"
		testClient2  = "client-002"
		testClient3  = "client-003"
		testMsgID1   = "msg-to-all"
		testMsgID2   = "msg-to-specific"
		testContent1 = "消息发送给所有客户端"
		testContent2 = "消息发送给指定客户端"
	)

	// 创建3个客户端连接到同一个用户
	client1 := &Client{
		ID:       testClient1,
		UserID:   testUserID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
		Context:  context.WithValue(context.Background(), ContextKeyUserID, testUserID),
	}

	client2 := &Client{
		ID:       testClient2,
		UserID:   testUserID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
		Context:  context.WithValue(context.Background(), ContextKeyUserID, testUserID),
	}

	client3 := &Client{
		ID:       testClient3,
		UserID:   testUserID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
		Context:  context.WithValue(context.Background(), ContextKeyUserID, testUserID),
	}

	// 注册所有客户端
	hub.Register(client1)
	hub.Register(client2)
	hub.Register(client3)
	time.Sleep(100 * time.Millisecond)

	// 验证所有客户端都已注册
	clientCount := hub.GetClientsCount()
	assert.Equal(t, 3, clientCount, "应该有3个活跃连接")

	t.Run("SendToAllClients_WhenReceiverClientNotSpecified", func(t *testing.T) {
		// 计数器：跟踪每个客户端收到的消息数
		var count1, count2, count3 int32

		// 启动goroutine监听消息
		stopChan := make(chan struct{})
		defer close(stopChan)

		go func() {
			for {
				select {
				case <-client1.SendChan:
					atomic.AddInt32(&count1, 1)
				case <-stopChan:
					return
				}
			}
		}()

		go func() {
			for {
				select {
				case <-client2.SendChan:
					atomic.AddInt32(&count2, 1)
				case <-stopChan:
					return
				}
			}
		}()

		go func() {
			for {
				select {
				case <-client3.SendChan:
					atomic.AddInt32(&count3, 1)
				case <-stopChan:
					return
				}
			}
		}()

		// 发送消息，不指定 ReceiverClient
		idGen := hub.GetIDGenerator()
		msg := &HubMessage{
			ID:           idGen.GenerateTraceID(),
			MessageID:    idGen.GenerateRequestID(),
			MessageType:  MessageTypeText,
			Content:      testContent1,
			Receiver:     testUserID,
			ReceiverType: UserTypeCustomer,
			// ReceiverClient 为空，应该发送给所有客户端
		}

		result := hub.SendToUserWithRetry(context.Background(), testUserID, msg)
		require.NoError(t, result.FinalError)

		// 等待消息处理
		time.Sleep(200 * time.Millisecond)

		// 验证所有客户端都收到消息
		assert.Equal(t, int32(1), atomic.LoadInt32(&count1), "客户端1应该收到1条消息")
		assert.Equal(t, int32(1), atomic.LoadInt32(&count2), "客户端2应该收到1条消息")
		assert.Equal(t, int32(1), atomic.LoadInt32(&count3), "客户端3应该收到1条消息")
	})

	t.Run("SendToSpecificClient_WhenReceiverClientSpecified", func(t *testing.T) {
		// 清空所有客户端的消息队列
		drainChannel(client1.SendChan)
		drainChannel(client2.SendChan)
		drainChannel(client3.SendChan)

		// 计数器
		var count1, count2, count3 int32

		// 启动goroutine监听消息
		stopChan := make(chan struct{})
		defer close(stopChan)

		go func() {
			for {
				select {
				case <-client1.SendChan:
					atomic.AddInt32(&count1, 1)
				case <-stopChan:
					return
				}
			}
		}()

		go func() {
			for {
				select {
				case <-client2.SendChan:
					atomic.AddInt32(&count2, 1)
				case <-stopChan:
					return
				}
			}
		}()

		go func() {
			for {
				select {
				case <-client3.SendChan:
					atomic.AddInt32(&count3, 1)
				case <-stopChan:
					return
				}
			}
		}()

		// 发送消息，指定 ReceiverClient 为 client2
		idGen := hub.GetIDGenerator()
		msg := &HubMessage{
			ID:             idGen.GenerateTraceID(),
			MessageID:      idGen.GenerateRequestID(),
			MessageType:    MessageTypeText,
			Content:        testContent2,
			Receiver:       testUserID,
			ReceiverClient: testClient2, // 指定只发送给 client2
			ReceiverType:   UserTypeCustomer,
		}

		result := hub.SendToUserWithRetry(context.Background(), testUserID, msg)
		require.NoError(t, result.FinalError) // 等待消息处理
		time.Sleep(200 * time.Millisecond)

		// 验证只有 client2 收到消息
		assert.Equal(t, int32(0), atomic.LoadInt32(&count1), "客户端1不应该收到消息")
		assert.Equal(t, int32(1), atomic.LoadInt32(&count2), "客户端2应该收到1条消息")
		assert.Equal(t, int32(0), atomic.LoadInt32(&count3), "客户端3不应该收到消息")
	})

	t.Run("NoMessageSent_WhenSpecifiedClientNotExists", func(t *testing.T) {
		// 清空所有客户端的消息队列
		drainChannel(client1.SendChan)
		drainChannel(client2.SendChan)
		drainChannel(client3.SendChan)

		// 计数器
		var count1, count2, count3 int32

		// 启动goroutine监听消息
		stopChan := make(chan struct{})
		defer close(stopChan)

		go func() {
			for {
				select {
				case <-client1.SendChan:
					atomic.AddInt32(&count1, 1)
				case <-stopChan:
					return
				}
			}
		}()

		go func() {
			for {
				select {
				case <-client2.SendChan:
					atomic.AddInt32(&count2, 1)
				case <-stopChan:
					return
				}
			}
		}()

		go func() {
			for {
				select {
				case <-client3.SendChan:
					atomic.AddInt32(&count3, 1)
				case <-stopChan:
					return
				}
			}
		}()

		// 发送消息，指定一个不存在的 ReceiverClient
		idGen := hub.GetIDGenerator()
		msg := &HubMessage{
			ID:             idGen.GenerateTraceID(),
			MessageID:      idGen.GenerateRequestID(),
			MessageType:    MessageTypeText,
			Content:        "消息发送给不存在的客户端",
			Receiver:       testUserID,
			ReceiverClient: "non-existent-client", // 不存在的客户端
			ReceiverType:   UserTypeCustomer,
		}

		result := hub.SendToUserWithRetry(context.Background(), testUserID, msg)
		require.NoError(t, result.FinalError) // 等待消息处理
		time.Sleep(200 * time.Millisecond)

		// 验证所有客户端都没有收到消息
		assert.Equal(t, int32(0), atomic.LoadInt32(&count1), "客户端1不应该收到消息")
		assert.Equal(t, int32(0), atomic.LoadInt32(&count2), "客户端2不应该收到消息")
		assert.Equal(t, int32(0), atomic.LoadInt32(&count3), "客户端3不应该收到消息")
	})

	// 清理
	hub.Unregister(client1)
	hub.Unregister(client2)
	hub.Unregister(client3)
}

// drainChannel 清空channel中的所有消息
func drainChannel(ch chan []byte) {
	for {
		select {
		case <-ch:
			// 继续清空
		default:
			return
		}
	}
}

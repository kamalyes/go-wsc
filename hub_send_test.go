/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 09:54:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:53:00
 * @FilePath: \go-wsc\hub_send_test.go
 * @Description: Hub消息发送功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendToMultipleUsers 测试发送消息到多个用户
func TestSendToMultipleUsers(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册多个客户端
	clients := make([]*Client, 3)
	userIDs := []string{}

	for i := 0; i < 3; i++ {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		userIDs = append(userIDs, clients[i].UserID)
		hub.Register(clients[i])
	}
	time.Sleep(100 * time.Millisecond)

	msg := &HubMessage{
		ID:           "multi-msg-1",
		MessageType:  MessageTypeText,
		Content:      "群发消息测试",
		ReceiverType: UserTypeCustomer,
	}

	// 发送到多个用户
	errors := hub.SendToMultipleUsers(context.Background(), userIDs, msg)

	// 验证没有错误
	assert.Equal(t, 0, len(errors), "不应该有发送错误")

	// 清理
	for _, client := range clients {
		hub.Unregister(client)
	}
}

// TestBroadcastToGroup 测试广播到用户组
func TestBroadcastToGroup(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册不同类型的客户端
	customer := createTestClientWithIDGen(UserTypeCustomer)
	agent := createTestClientWithIDGen(UserTypeAgent)

	hub.Register(customer)
	hub.Register(agent)
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:           "broadcast-group-1",
		MessageType:  MessageTypeText,
		Content:      "发送给customer组",
		ReceiverType: UserTypeCustomer,
	}

	// 广播到customer组
	count := hub.BroadcastToGroup(context.Background(), UserTypeCustomer, msg)
	assert.Equal(t, 1, count, "应该发送给1个customer")

	hub.Unregister(customer)
	hub.Unregister(agent)
}

// TestBroadcast 测试全局广播
func TestBroadcast(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册多个客户端
	clients := make([]*Client, 2)
	for i := 0; i < 2; i++ {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		hub.Register(clients[i])
	}
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:           "broadcast-all-1",
		MessageType:  MessageTypeAnnouncement,
		Content:      "全局广播消息",
		ReceiverType: UserTypeCustomer,
	}

	// 全局广播
	hub.Broadcast(context.Background(), msg)
	time.Sleep(100 * time.Millisecond)

	// 清理
	for _, client := range clients {
		hub.Unregister(client)
	}
}

// TestSendConditional 测试条件发送
func TestSendConditional(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册多个客户端
	for i := 1; i <= 3; i++ {
		client := createTestClientWithIDGen(UserTypeCustomer)
		client.UserID = "cond-user-" + string(rune('0'+i))
		hub.Register(client)
	}
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:           "conditional-1",
		MessageType:  MessageTypeText,
		Content:      "条件发送",
		ReceiverType: UserTypeCustomer,
	}

	// 只发送给特定用户
	count := hub.SendConditional(context.Background(), func(c *Client) bool {
		return c.UserID == "cond-user-1" || c.UserID == "cond-user-2"
	}, msg)

	assert.Equal(t, 2, count, "应该发送给2个用户")
}

// TestSendPriority 测试优先级发送
func TestSendPriority(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	client := createTestClientWithIDGen(UserTypeCustomer)
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:          "priority-msg-1",
		MessageType: MessageTypeText,
		Content:     "高优先级消息",
	}

	// 发送高优先级消息
	hub.SendPriority(context.Background(), client.UserID, msg, PriorityHigh)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, PriorityHigh, msg.Priority, "消息优先级应该被设置")

	hub.Unregister(client)
}

// TestBroadcastAfterDelay 测试延迟广播
func TestBroadcastAfterDelay(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	client := createTestClientWithIDGen(UserTypeCustomer)
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:           "delay-msg-1",
		MessageType:  MessageTypeText,
		Content:      "延迟广播",
		ReceiverType: UserTypeCustomer,
	}

	start := time.Now()
	hub.BroadcastAfterDelay(context.Background(), msg, 100*time.Millisecond)

	// 等待延迟完成
	time.Sleep(200 * time.Millisecond)
	elapsed := time.Since(start)

	assert.GreaterOrEqual(t, elapsed, 100*time.Millisecond, "应该至少延迟100ms")

	hub.Unregister(client)
}

// TestSendToUserViaSSE 测试通过SSE发送
func TestSendToUserViaSSE(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册SSE客户端
	w := httptest.NewRecorder()
	_, err := hub.RegisterSSE("sse-user-1", w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:          "sse-msg-1",
		MessageType: MessageTypeText,
		Content:     "SSE消息",
	}

	// 通过SSE发送
	success := hub.SendToUserViaSSE("sse-user-1", msg)
	assert.True(t, success, "SSE发送应该成功")

	hub.UnregisterSSE("sse-user-1")
}

// TestSendPongResponse 测试Pong响应
func TestSendPongResponse(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	client := createTestClientWithIDGen(UserTypeCustomer)
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 发送Pong响应
	err := hub.SendPongResponse(client)
	assert.NoError(t, err, "发送Pong应该成功")

	// 验证消息是否被发送到SendChan
	select {
	case msg := <-client.SendChan:
		assert.NotNil(t, msg, "应该收到Pong消息")
		// 验证消息内容
		var pongMsg HubMessage
		err := json.Unmarshal(msg, &pongMsg)
		assert.NoError(t, err, "消息应该能反序列化")
		assert.Equal(t, MessageTypePong, pongMsg.MessageType, "消息类型应该是Pong")
		assert.Equal(t, client.UserID, pongMsg.Receiver, "接收者应该是客户端用户ID")
	case <-time.After(1 * time.Second):
		t.Fatal("超时:没有收到Pong消息")
	}

	hub.Unregister(client)
}

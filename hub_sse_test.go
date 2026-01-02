/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 12:07:59
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:52:04
 * @FilePath: \go-wsc\hub_sse_test.go
 * @Description: Hub SSE相关功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRegisterSSE 测试注册SSE客户端
func TestRegisterSSE(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册SSE客户端
	w := httptest.NewRecorder()
	client, err := hub.RegisterSSE("sse-reg-user", w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 验证客户端已注册
	assert.True(t, hub.HasSSEClient("sse-reg-user"), "SSE客户端应该已注册")

	hub.UnregisterSSE(client.ID)
}

// TestUnregisterSSE 测试注销SSE客户端
func TestUnregisterSSE(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册并注销SSE客户端
	w := httptest.NewRecorder()
	client, err := hub.RegisterSSE("sse-unreg-user", w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	hub.UnregisterSSE(client.ID)
	time.Sleep(50 * time.Millisecond)

	// 验证客户端已注销
	assert.False(t, hub.HasSSEClient("sse-unreg-user"), "SSE客户端应该已注销")
}

// TestHasSSEClient 测试检查SSE客户端是否存在
func TestHasSSEClient(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 客户端不存在
	exists := hub.HasSSEClient("non-existent-sse")
	assert.False(t, exists, "SSE客户端不应该存在")

	// 注册客户端
	w := httptest.NewRecorder()
	client, err := hub.RegisterSSE("sse-exists-user", w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 客户端存在
	exists = hub.HasSSEClient("sse-exists-user")
	assert.True(t, exists, "SSE客户端应该存在")

	hub.UnregisterSSE(client.ID)
}

// TestGetSSEClientsByUserID 测试根据UserID获取SSE客户端
func TestGetSSEClientsByUserID(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "sse-multi-user"

	// 注册多个SSE客户端
	w1 := httptest.NewRecorder()
	client1, err := hub.RegisterSSE(userID, w1, UserTypeCustomer)
	require.NoError(t, err)
	w2 := httptest.NewRecorder()
	client2, err := hub.RegisterSSE(userID, w2, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 验证SSE客户端已注册
	assert.True(t, hub.HasSSEClient(userID), "SSE客户端应该已注册")

	// 清理
	hub.UnregisterSSE(client1.ID)
	hub.UnregisterSSE(client2.ID)
}

// TestSendToSSEClients 测试向SSE客户端发送消息
func TestSendToSSEClients(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册SSE客户端
	w := httptest.NewRecorder()
	client, err := hub.RegisterSSE("sse-send-user", w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:          "sse-msg-1",
		MessageType: MessageTypeText,
		Content:     "SSE消息测试",
	}

	// 发送消息
	success := hub.SendToUserViaSSE("sse-send-user", msg)
	assert.True(t, success, "SSE消息发送应该成功")

	hub.UnregisterSSE(client.ID)
}

// TestBroadcastToSSEClients 测试向所有SSE客户端广播
func TestBroadcastToSSEClients(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册多个SSE客户端
	var clientIDs []string
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		client, err := hub.RegisterSSE(fmt.Sprintf("sse-user-%d", i+1), w, UserTypeCustomer)
		require.NoError(t, err)
		clientIDs = append(clientIDs, client.ID)
	}
	time.Sleep(50 * time.Millisecond)

	msg := &HubMessage{
		ID:           "sse-broadcast-1",
		MessageType:  MessageTypeAnnouncement,
		Content:      "SSE广播消息",
		ReceiverType: UserTypeCustomer,
	}

	// 广播消息
	hub.Broadcast(context.Background(), msg)
	time.Sleep(100 * time.Millisecond)

	// 清理
	for _, clientID := range clientIDs {
		hub.UnregisterSSE(clientID)
	}
}

// TestSSEClientCount 测试SSE客户端计数
func TestSSEClientCount(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册SSE客户端
	w := httptest.NewRecorder()
	client, err := hub.RegisterSSE("sse-count-user", w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 获取统计信息
	stats := hub.GetStats()
	assert.GreaterOrEqual(t, stats.TotalClients, 1, "应该至少有1个连接")

	hub.UnregisterSSE(client.ID)
}

// TestMixedClientsWebSocketAndSSE 测试WebSocket和SSE混合客户端
func TestMixedClientsWebSocketAndSSE(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册WebSocket客户端
	wsClient := &Client{
		ID:       "ws-mixed-client",
		UserID:   "mixed-user-1",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(wsClient)

	// 注册SSE客户端
	w := httptest.NewRecorder()
	sseClient, err := hub.RegisterSSE("mixed-user-2", w, UserTypeCustomer)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)

	// 验证两种客户端都已注册
	assert.True(t, hub.HasClient("ws-mixed-client"), "WebSocket客户端应该存在")
	assert.True(t, hub.HasSSEClient("mixed-user-2"), "SSE客户端应该存在")

	// 清理
	hub.Unregister(wsClient)
	hub.UnregisterSSE(sseClient.ID)
}

// TestSSEClientHeartbeat 测试SSE客户端心跳
func TestSSEClientHeartbeat(t *testing.T) {
	config := wscconfig.Default().
		WithHeartbeatInterval(1 * time.Second).
		WithClientTimeout(3 * time.Second)
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册SSE客户端
	w := httptest.NewRecorder()
	client, err := hub.RegisterSSE("sse-hb-user", w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 更新心跳
	hub.UpdateHeartbeat("sse-hb-user")
	time.Sleep(50 * time.Millisecond)

	// 验证客户端仍在线
	assert.True(t, hub.HasSSEClient("sse-hb-user"), "SSE客户端应该仍在线")

	hub.UnregisterSSE(client.ID)
}

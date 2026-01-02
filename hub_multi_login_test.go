/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 13:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:20:15
 * @FilePath: \go-wsc\hub_multi_login_test.go
 * @Description: Hub 多端登录测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
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

// TestMultiLoginAllowed 测试允许多端登录
func TestMultiLoginAllowed(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0 // 无限制

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "multi-user-001"

	// 注册第一个 WebSocket 客户端
	client1 := &Client{
		ID:       "ws-client-1",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client1)
	time.Sleep(50 * time.Millisecond)

	// 注册第二个 WebSocket 客户端
	client2 := &Client{
		ID:       "ws-client-2",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client2)
	time.Sleep(50 * time.Millisecond)

	// 注册 SSE 客户端
	w := httptest.NewRecorder()
	sseClient, err := hub.RegisterSSE(userID, w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 验证所有三个客户端都在线
	assert.True(t, hub.HasClient(client1.ID), "WebSocket客户端1应该在线")
	assert.True(t, hub.HasClient(client2.ID), "WebSocket客户端2应该在线")
	assert.True(t, hub.HasClient(sseClient.ID), "SSE客户端应该在线")

	// 验证用户的所有客户端
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 3, len(clientMap), "应该有3个客户端")
}

// TestMultiLoginDisabled 测试禁止多端登录（强制单端）
func TestMultiLoginDisabled(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = false

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "single-user-001"

	// 注册第一个客户端
	client1 := &Client{
		ID:       "ws-client-1",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client1)
	time.Sleep(100 * time.Millisecond)

	assert.True(t, hub.HasClient(client1.ID), "第一个客户端应该在线")

	// 注册第二个客户端（应该踢掉第一个）
	client2 := &Client{
		ID:       "ws-client-2",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client2)
	time.Sleep(100 * time.Millisecond)

	// 验证只有第二个客户端在线
	assert.False(t, hub.HasClient(client1.ID), "第一个客户端应该被踢下线")
	assert.True(t, hub.HasClient(client2.ID), "第二个客户端应该在线")

	// 验证用户只有一个客户端
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 1, len(clientMap), "应该只有1个客户端")
}

// TestMultiLoginWithLimit 测试连接数限制
func TestMultiLoginWithLimit(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 2

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "limited-user-001"

	// 注册两个客户端
	client1 := &Client{
		ID:       "ws-client-1",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client1)
	time.Sleep(50 * time.Millisecond)

	client2 := &Client{
		ID:       "ws-client-2",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client2)
	time.Sleep(50 * time.Millisecond)

	// 验证两个客户端都在线
	assert.True(t, hub.HasClient(client1.ID), "客户端1应该在线")
	assert.True(t, hub.HasClient(client2.ID), "客户端2应该在线")

	// 注册第三个客户端（应该踢掉最早的）
	client3 := &Client{
		ID:       "ws-client-3",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client3)
	time.Sleep(100 * time.Millisecond)

	// 验证最早的客户端被踢，保留最近的两个
	assert.False(t, hub.HasClient(client1.ID), "最早的客户端1应该被踢")
	assert.True(t, hub.HasClient(client2.ID), "客户端2应该在线")
	assert.True(t, hub.HasClient(client3.ID), "客户端3应该在线")

	// 验证用户只有两个客户端
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 2, len(clientMap), "应该只有2个客户端")
}

// TestMultiLoginMessageBroadcast 测试消息发送到所有客户端
func TestMultiLoginMessageBroadcast(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "broadcast-user-001"

	// 注册两个 WebSocket 客户端
	client1 := &Client{
		ID:       "ws-client-1",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client1)
	time.Sleep(50 * time.Millisecond)

	client2 := &Client{
		ID:       "ws-client-2",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(client2)
	time.Sleep(50 * time.Millisecond)

	// 注册 SSE 客户端
	w := httptest.NewRecorder()
	sseClient, err := hub.RegisterSSE(userID, w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 发送消息给用户
	message := &HubMessage{
		MessageType: MessageTypeText,
		Receiver:    userID,
		Content:     "测试多端消息",
		CreateAt:    time.Now(),
	}

	_ = hub.SendToUserWithRetry(context.Background(), userID, message)
	time.Sleep(100 * time.Millisecond)

	// 验证所有客户端都收到消息
	select {
	case msg1 := <-client1.SendChan:
		assert.Contains(t, string(msg1), "测试多端消息", "WebSocket客户端1应该收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("WebSocket客户端1未收到消息")
	}

	select {
	case msg2 := <-client2.SendChan:
		assert.Contains(t, string(msg2), "测试多端消息", "WebSocket客户端2应该收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("WebSocket客户端2未收到消息")
	}

	select {
	case sseMsg := <-sseClient.SSEMessageCh:
		assert.Equal(t, "测试多端消息", sseMsg.Content, "SSE客户端应该收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("SSE客户端未收到消息")
	}
}

// TestMultiLoginMixedTypes 测试同一用户混合连接类型（WebSocket + SSE）
func TestMultiLoginMixedTypes(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "mixed-user-001"

	// 先注册 WebSocket
	wsClient := &Client{
		ID:             "ws-client",
		UserID:         userID,
		UserType:       UserTypeCustomer,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, 10),
	}
	hub.Register(wsClient)
	time.Sleep(50 * time.Millisecond)

	// 再注册 SSE
	w := httptest.NewRecorder()
	sseClient, err := hub.RegisterSSE(userID, w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	// 验证两种类型的客户端都在线
	assert.True(t, hub.HasClient(wsClient.ID), "WebSocket客户端应该在线")
	assert.True(t, hub.HasClient(sseClient.ID), "SSE客户端应该在线")
	assert.True(t, hub.HasSSEClient(userID), "应该有SSE连接")

	// 验证用户有两个客户端
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 2, len(clientMap), "应该有2个客户端（WebSocket + SSE）")

	// 验证连接类型
	var hasWebSocket, hasSSE bool
	for _, client := range clientMap {
		if client.ConnectionType == ConnectionTypeWebSocket {
			hasWebSocket = true
		} else if client.ConnectionType == ConnectionTypeSSE {
			hasSSE = true
		}
	}
	assert.True(t, hasWebSocket, "应该有WebSocket连接")
	assert.True(t, hasSSE, "应该有SSE连接")
}

// TestMultiLoginKickUser 测试踢出用户的所有连接
func TestMultiLoginKickUser(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "kick-user-001"

	// 注册多个客户端
	for i := 1; i <= 3; i++ {
		client := &Client{
			ID:       fmt.Sprintf("client-%d", i),
			UserID:   userID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 10),
		}
		hub.Register(client)
		time.Sleep(50 * time.Millisecond)
	}

	// 验证所有客户端在线
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 3, len(clientMap), "应该有3个客户端")

	// 踢出用户
	result := hub.KickUser(userID, "管理员踢出", false, "")
	assert.True(t, result.Success, "踢出应该成功")
	assert.Equal(t, 3, result.KickedConnections, "应该断开3个连接")

	time.Sleep(100 * time.Millisecond)

	// 验证所有客户端都被踢出
	for i := 1; i <= 3; i++ {
		assert.False(t, hub.HasClient(fmt.Sprintf("client-%d", i)), fmt.Sprintf("客户端%d应该被踢出", i))
	}
}

// TestMultiLoginAgentClients 测试客服多端登录
func TestMultiLoginAgentClients(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	agentID := "agent-001"

	// 注册两个客服客户端
	agent1 := &Client{
		ID:       "agent-client-1",
		UserID:   agentID,
		UserType: UserTypeAgent,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(agent1)
	time.Sleep(50 * time.Millisecond)

	agent2 := &Client{
		ID:       "agent-client-2",
		UserID:   agentID,
		UserType: UserTypeAgent,
		SendChan: make(chan []byte, 10),
	}
	hub.Register(agent2)
	time.Sleep(50 * time.Millisecond)

	// 验证两个客服客户端都在线
	assert.True(t, hub.HasClient(agent1.ID), "客服客户端1应该在线")
	assert.True(t, hub.HasClient(agent2.ID), "客服客户端2应该在线")

	// 验证客服特定的存储
	assert.True(t, hub.HasAgentClient(agentID), "应该有客服连接")

	// 验证客服有两个客户端
	clientMap, exists := hub.GetUserClientsMapWithLock(agentID)
	assert.True(t, exists, "客服应该有客户端")
	assert.Equal(t, 2, len(clientMap), "应该有2个客服客户端")
}

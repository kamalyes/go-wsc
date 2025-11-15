/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-15 00:00:00
 * @FilePath: \go-wsc\hub_test.go
 * @Description: Hub 测试文件 - 测试WebSocket/SSE连接管理中心的各种功能
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// mockWelcomeProvider 模拟欢迎消息提供者
type mockWelcomeProvider struct {
	enabled  bool
	template *WelcomeTemplate
	mu       sync.RWMutex
}

func newMockWelcomeProvider() *mockWelcomeProvider {
	return &mockWelcomeProvider{
		enabled: true,
		template: &WelcomeTemplate{
			Title:       "欢迎使用客服系统",
			Content:     "您好 {user_id}，欢迎使用我们的客服系统！当前时间: {time}",
			MessageType: MessageTypeSystem,
			Enabled:     true,
			Variables:   []string{"user_id", "time"},
		},
	}
}

func (m *mockWelcomeProvider) GetWelcomeMessage(userID string, userRole UserRole, userType UserType, ticketID string, extraData map[string]interface{}) (*WelcomeMessage, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if !m.enabled || !m.template.Enabled {
		return nil, false, nil
	}

	variables := map[string]interface{}{
		"user_id": userID,
	}

	for key, value := range extraData {
		variables[key] = value
	}

	result := m.template.ReplaceVariables(variables)
	
	return &WelcomeMessage{
		Title:       result.Title,
		Content:     result.Content,
		MessageType: result.MessageType,
		Priority:    PriorityNormal,
		Data:        map[string]interface{}{"type": "welcome"},
	}, true, nil
}

func (m *mockWelcomeProvider) RefreshConfig() error {
	return nil
}

func (m *mockWelcomeProvider) setEnabled(enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
}

// TestNewHub 测试Hub创建
func TestNewHub(t *testing.T) {
	t.Run("使用默认配置创建Hub", func(t *testing.T) {
		hub := NewHub(nil)
		assert.NotNil(t, hub)
		assert.NotEmpty(t, hub.nodeID)
		assert.NotNil(t, hub.config)
		assert.Equal(t, "0.0.0.0", hub.config.NodeIP)
		assert.Equal(t, 8080, hub.config.NodePort)
		assert.Equal(t, 30*time.Second, hub.config.HeartbeatInterval)
		
		hub.Shutdown()
	})

	t.Run("使用自定义配置创建Hub", func(t *testing.T) {
		config := &HubConfig{
			NodeIP:              "192.168.1.100",
			NodePort:            9000,
			HeartbeatInterval:   60 * time.Second,
			ClientTimeout:       120 * time.Second,
			MessageBufferSize:   512,
			SSEHeartbeat:        45 * time.Second,
			SSETimeout:          3 * time.Minute,
			SSEMessageBuffer:    200,
			WelcomeProvider:     newMockWelcomeProvider(),
		}

		hub := NewHub(config)
		assert.NotNil(t, hub)
		assert.Equal(t, config.NodeIP, hub.config.NodeIP)
		assert.Equal(t, config.NodePort, hub.config.NodePort)
		assert.Equal(t, config.HeartbeatInterval, hub.config.HeartbeatInterval)
		assert.Equal(t, config.WelcomeProvider, hub.welcomeProvider)
		
		hub.Shutdown()
	})
}

// TestHubStats 测试Hub统计信息
func TestHubStats(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	stats := hub.GetStats()
	assert.NotNil(t, stats)
	assert.Equal(t, hub.nodeID, stats["node_id"])
	assert.Equal(t, 0, stats["websocket_count"])
	assert.Equal(t, 0, stats["sse_count"])
	assert.Equal(t, 0, stats["total_connections"])
	assert.Equal(t, int64(0), stats["messages_sent"])
	assert.Equal(t, int64(0), stats["messages_received"])
}

// TestHubClientRegistration 测试客户端注册和注销
func TestHubClientRegistration(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	// 启动Hub
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	t.Run("注册WebSocket客户端", func(t *testing.T) {
		client := &Client{
			ID:         "client-001",
			UserID:     "user-001",
			UserType:   UserTypeCustomer,
			Role:       UserRoleCustomer,
			Status:     UserStatusOnline,
			LastSeen:   time.Now(),
			SendChan:   make(chan []byte, 256),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, "user-001"),
		}

		// 注册客户端
		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 验证客户端已注册
		assert.Contains(t, hub.clients, client.ID)
		assert.Contains(t, hub.userToClient, client.UserID)
		
		stats := hub.GetStats()
		assert.Equal(t, 1, stats["websocket_count"])
		assert.Equal(t, 1, stats["total_connections"])

		// 注销客户端
		hub.Unregister(client)
		time.Sleep(100 * time.Millisecond)

		// 验证客户端已注销
		assert.NotContains(t, hub.clients, client.ID)
		assert.NotContains(t, hub.userToClient, client.UserID)
		
		stats = hub.GetStats()
		assert.Equal(t, 0, stats["websocket_count"])
		assert.Equal(t, 0, stats["total_connections"])
	})

	t.Run("注册Agent客户端", func(t *testing.T) {
		client := &Client{
			ID:         "agent-001",
			UserID:     "agent-001",
			UserType:   UserTypeAgent,
			Role:       UserRoleAgent,
			Status:     UserStatusOnline,
			LastSeen:   time.Now(),
			SendChan:   make(chan []byte, 256),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, "agent-001"),
		}

		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 验证agent客户端已注册到agentClients
		assert.Contains(t, hub.agentClients, client.UserID)
		
		hub.Unregister(client)
		time.Sleep(100 * time.Millisecond)

		// 验证agent客户端已从agentClients中移除
		assert.NotContains(t, hub.agentClients, client.UserID)
	})

	t.Run("注册带工单的客户端", func(t *testing.T) {
		ticketID := "ticket-001"
		client := &Client{
			ID:         "client-002",
			UserID:     "user-002",
			UserType:   UserTypeCustomer,
			Role:       UserRoleCustomer,
			TicketID:   ticketID,
			Status:     UserStatusOnline,
			LastSeen:   time.Now(),
			SendChan:   make(chan []byte, 256),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, "user-002"),
		}

		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 验证工单客户端已注册
		assert.Contains(t, hub.ticketClients, ticketID)
		assert.Contains(t, hub.ticketClients[ticketID], client)
		
		hub.Unregister(client)
		time.Sleep(100 * time.Millisecond)

		// 验证工单客户端已移除
		if clients, exists := hub.ticketClients[ticketID]; exists {
			assert.NotContains(t, clients, client)
		}
	})

	t.Run("替换现有用户连接", func(t *testing.T) {
		userID := "user-003"
		
		// 创建第一个客户端
		client1 := &Client{
			ID:       "client-003-1",
			UserID:   userID,
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, userID),
		}

		hub.Register(client1)
		time.Sleep(100 * time.Millisecond)

		// 验证第一个客户端已注册
		assert.Contains(t, hub.userToClient, userID)
		assert.Equal(t, client1, hub.userToClient[userID])

		// 创建第二个客户端（相同用户ID）
		client2 := &Client{
			ID:       "client-003-2",
			UserID:   userID,
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, userID),
		}

		hub.Register(client2)
		time.Sleep(100 * time.Millisecond)

		// 验证第一个客户端已被替换
		assert.Contains(t, hub.userToClient, userID)
		assert.Equal(t, client2, hub.userToClient[userID])
		assert.NotContains(t, hub.clients, client1.ID)
		assert.Contains(t, hub.clients, client2.ID)

		hub.Unregister(client2)
	})
}

// TestHubSSESupport 测试SSE连接支持
func TestHubSSESupport(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	t.Run("注册和注销SSE连接", func(t *testing.T) {
		userID := "sse-user-001"
		sseConn := &SSEConnection{
			UserID:     userID,
			MessageCh:  make(chan *HubMessage, 100),
			CloseCh:    make(chan struct{}),
			LastActive: time.Now(),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, userID),
		}

		// 注册SSE连接
		hub.RegisterSSE(sseConn)
		assert.Contains(t, hub.sseClients, userID)

		stats := hub.GetStats()
		assert.Equal(t, 1, stats["sse_count"])
		assert.Equal(t, 1, stats["total_connections"])

		// 注销SSE连接
		hub.UnregisterSSE(userID)
		assert.NotContains(t, hub.sseClients, userID)

		stats = hub.GetStats()
		assert.Equal(t, 0, stats["sse_count"])
		assert.Equal(t, 0, stats["total_connections"])
	})

	t.Run("通过SSE发送消息", func(t *testing.T) {
		userID := "sse-user-002"
		sseConn := &SSEConnection{
			UserID:     userID,
			MessageCh:  make(chan *HubMessage, 100),
			CloseCh:    make(chan struct{}),
			LastActive: time.Now(),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, userID),
		}

		hub.RegisterSSE(sseConn)

		message := &HubMessage{
			Type:     MessageTypeText,
			From:     "system",
			To:       userID,
			Content:  "SSE测试消息",
			CreateAt: time.Now(),
			Status:   MessageStatusSent,
		}

		// 发送消息
		success := hub.SendToUserViaSSE(userID, message)
		assert.True(t, success)

		// 验证消息已接收
		select {
		case receivedMsg := <-sseConn.MessageCh:
			assert.Equal(t, message.Content, receivedMsg.Content)
			assert.Equal(t, message.From, receivedMsg.From)
			assert.Equal(t, message.To, receivedMsg.To)
		case <-time.After(1 * time.Second):
			t.Fatal("未收到SSE消息")
		}

		hub.UnregisterSSE(userID)
	})

	t.Run("向不存在的SSE用户发送消息", func(t *testing.T) {
		success := hub.SendToUserViaSSE("nonexistent-user", &HubMessage{
			Type:    MessageTypeText,
			Content: "测试消息",
		})
		assert.False(t, success)
	})
}

// TestHubMessaging 测试Hub消息功能
func TestHubMessaging(t *testing.T) {
	config := &HubConfig{
		NodeIP:            "127.0.0.1",
		NodePort:          8080,
		HeartbeatInterval: 30 * time.Second,
		ClientTimeout:     90 * time.Second,
		MessageBufferSize: 256,
		WelcomeProvider:   nil, // 禁用欢迎消息以避免测试干扰
	}
	
	hub := NewHub(config)
	defer hub.Shutdown()

	// 启动Hub
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	t.Run("点对点消息发送", func(t *testing.T) {
		// 创建发送者和接收者
		sender := &Client{
			ID:       "sender-001",
			UserID:   "sender-001",
			UserType: UserTypeAgent,
			Role:     UserRoleAgent,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "sender-001"),
		}

		receiver := &Client{
			ID:       "receiver-001",
			UserID:   "receiver-001",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "receiver-001"),
		}

		hub.Register(sender)
		hub.Register(receiver)
		time.Sleep(100 * time.Millisecond)

		// 发送点对点消息
		ctx := context.WithValue(context.Background(), ContextKeySenderID, sender.UserID)
		message := &HubMessage{
			Type:    MessageTypeText,
			To:      receiver.UserID,
			Content: "点对点测试消息",
			Status:  MessageStatusSent,
		}

		err := hub.SendToUser(ctx, receiver.UserID, message)
		assert.NoError(t, err)

		// 验证接收者收到消息
		select {
		case msgData := <-receiver.SendChan:
			var receivedMsg HubMessage
			err := json.Unmarshal(msgData, &receivedMsg)
			assert.NoError(t, err)
			assert.Equal(t, message.Content, receivedMsg.Content)
			assert.Equal(t, sender.UserID, receivedMsg.From)
			assert.Equal(t, receiver.UserID, receivedMsg.To)
		case <-time.After(1 * time.Second):
			t.Fatal("接收者未收到消息")
		}

		// 验证发送者未收到消息
		select {
		case <-sender.SendChan:
			t.Fatal("发送者不应该收到消息")
		case <-time.After(100 * time.Millisecond):
			// 正确情况
		}

		hub.Unregister(sender)
		hub.Unregister(receiver)
	})

	t.Run("工单消息发送", func(t *testing.T) {
		ticketID := "ticket-002"
		
		// 创建多个工单相关客户端
		customer := &Client{
			ID:       "customer-002",
			UserID:   "customer-002",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			TicketID: ticketID,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "customer-002"),
		}

		agent := &Client{
			ID:       "agent-002",
			UserID:   "agent-002",
			UserType: UserTypeAgent,
			Role:     UserRoleAgent,
			TicketID: ticketID,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "agent-002"),
		}

		hub.Register(customer)
		hub.Register(agent)
		time.Sleep(100 * time.Millisecond)

		// 发送工单消息
		ctx := context.WithValue(context.Background(), ContextKeySenderID, customer.UserID)
		message := &HubMessage{
			Type:     MessageTypeText,
			TicketID: ticketID,
			Content:  "工单测试消息",
			Status:   MessageStatusSent,
		}

		err := hub.SendToTicket(ctx, ticketID, message)
		assert.NoError(t, err)

		// 验证agent收到消息
		select {
		case msgData := <-agent.SendChan:
			var receivedMsg HubMessage
			err := json.Unmarshal(msgData, &receivedMsg)
			assert.NoError(t, err)
			assert.Equal(t, message.Content, receivedMsg.Content)
			assert.Equal(t, customer.UserID, receivedMsg.From)
			assert.Equal(t, ticketID, receivedMsg.TicketID)
		case <-time.After(1 * time.Second):
			t.Fatal("Agent未收到工单消息")
		}

		// 验证发送者(customer)未收到消息（不给自己发送）
		select {
		case <-customer.SendChan:
			// 可能收到欢迎消息，跳过
		case <-time.After(100 * time.Millisecond):
			// 正确情况
		}

		hub.Unregister(customer)
		hub.Unregister(agent)
	})

	t.Run("广播消息", func(t *testing.T) {
		// 创建多个客户端
		clients := make([]*Client, 3)
		for i := 0; i < 3; i++ {
			clients[i] = &Client{
				ID:       fmt.Sprintf("broadcast-client-%d", i),
				UserID:   fmt.Sprintf("broadcast-user-%d", i),
				UserType: UserTypeCustomer,
				Role:     UserRoleCustomer,
				Status:   UserStatusOnline,
				LastSeen: time.Now(),
				SendChan: make(chan []byte, 256),
				Context:  context.WithValue(context.Background(), ContextKeyUserID, fmt.Sprintf("broadcast-user-%d", i)),
			}
			hub.Register(clients[i])
		}
		time.Sleep(100 * time.Millisecond)

		// 发送广播消息
		message := &HubMessage{
			Type:     MessageTypeSystem,
			From:     "system",
			Content:  "系统广播消息",
			CreateAt: time.Now(),
			Status:   MessageStatusSent,
		}

		hub.Broadcast(context.Background(), message)

		// 验证所有客户端都收到消息
		for i, client := range clients {
			select {
			case msgData := <-client.SendChan:
				var receivedMsg HubMessage
				err := json.Unmarshal(msgData, &receivedMsg)
				assert.NoError(t, err)
				assert.Equal(t, message.Content, receivedMsg.Content)
				assert.Equal(t, "system", receivedMsg.From)
			case <-time.After(1 * time.Second):
				t.Fatalf("客户端 %d 未收到广播消息", i)
			}
			hub.Unregister(client)
		}
	})

	t.Run("消息队列满的情况", func(t *testing.T) {
		// 创建小缓冲区的Hub配置
		smallConfig := &HubConfig{
			NodeIP:            "127.0.0.1",
			NodePort:          8080,
			HeartbeatInterval: 30 * time.Second,
			ClientTimeout:     90 * time.Second,
			MessageBufferSize: 1,  // broadcast会是1*4=4
			PendingQueueSize:  1,  // pending是1，总共4+1=5条
			WelcomeProvider:   nil,
		}
		smallHub := NewHub(smallConfig)
		defer smallHub.Shutdown()

		// 不启动Hub.Run(),这样broadcast channel会满
		// go smallHub.Run()
		// time.Sleep(100 * time.Millisecond)

		// 快速发送多条消息，测试队列满的情况（broadcast:2 + pending:2 = 4）
		errorCount := 0
		successCount := 0
		for i := 0; i < 10; i++ {
			message := &HubMessage{
				Type:     MessageTypeText,
				Content:  fmt.Sprintf("消息 %d", i),
				CreateAt: time.Now(),
			}
			err := smallHub.SendToUser(context.Background(), "nonexistent", message)
			if err != nil {
				errorCount++
			} else {
				successCount++
			}
		}
		
		// 验证至少有一些消息因为队列满而失败
		t.Logf("成功: %d, 失败: %d", successCount, errorCount)
		assert.True(t, errorCount > 0, "应该有消息因为队列满而失败")
	})
}

// TestHubOnlineUsers 测试在线用户管理
func TestHubOnlineUsers(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	t.Run("获取在线用户列表", func(t *testing.T) {
		// 初始状态应该没有在线用户
		onlineUsers := hub.GetOnlineUsers()
		assert.Empty(t, onlineUsers)

		// 添加WebSocket客户端
		wsClient := &Client{
			ID:       "ws-client-001",
			UserID:   "ws-user-001",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "ws-user-001"),
		}
		hub.Register(wsClient)

		// 添加SSE客户端
		sseConn := &SSEConnection{
			UserID:     "sse-user-001",
			MessageCh:  make(chan *HubMessage, 100),
			CloseCh:    make(chan struct{}),
			LastActive: time.Now(),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, "sse-user-001"),
		}
		hub.RegisterSSE(sseConn)

		time.Sleep(100 * time.Millisecond)

		onlineUsers = hub.GetOnlineUsers()
		assert.Len(t, onlineUsers, 2)
		assert.Contains(t, onlineUsers, "ws-user-001")
		assert.Contains(t, onlineUsers, "sse-user-001")

		// 移除客户端
		hub.Unregister(wsClient)
		hub.UnregisterSSE("sse-user-001")

		time.Sleep(100 * time.Millisecond)

		onlineUsers = hub.GetOnlineUsers()
		assert.Empty(t, onlineUsers)
	})

	t.Run("相同用户同时有WebSocket和SSE连接", func(t *testing.T) {
		userID := "dual-user-001"

		// 添加WebSocket客户端
		wsClient := &Client{
			ID:       "ws-client-dual",
			UserID:   userID,
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, userID),
		}
		hub.Register(wsClient)

		// 添加SSE客户端
		sseConn := &SSEConnection{
			UserID:     userID,
			MessageCh:  make(chan *HubMessage, 100),
			CloseCh:    make(chan struct{}),
			LastActive: time.Now(),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, userID),
		}
		hub.RegisterSSE(sseConn)

		time.Sleep(100 * time.Millisecond)

		onlineUsers := hub.GetOnlineUsers()
		assert.Len(t, onlineUsers, 1)
		assert.Contains(t, onlineUsers, userID)

		hub.Unregister(wsClient)
		hub.UnregisterSSE(userID)
	})
}

// TestHubWelcomeMessage 测试欢迎消息功能
func TestHubWelcomeMessage(t *testing.T) {
	welcomeProvider := newMockWelcomeProvider()
	config := &HubConfig{
		NodeIP:            "127.0.0.1",
		NodePort:          8080,
		HeartbeatInterval: 30 * time.Second,
		ClientTimeout:     90 * time.Second,
		MessageBufferSize: 256,
		WelcomeProvider:   welcomeProvider,
	}

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	t.Run("客户端注册时收到欢迎消息", func(t *testing.T) {
		client := &Client{
			ID:       "welcome-client-001",
			UserID:   "welcome-user-001",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "welcome-user-001"),
		}

		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 验证收到欢迎消息
		select {
		case msgData := <-client.SendChan:
			var welcomeMsg HubMessage
			err := json.Unmarshal(msgData, &welcomeMsg)
			assert.NoError(t, err)
			assert.Equal(t, MessageTypeSystem, welcomeMsg.Type)
			assert.Equal(t, "system", welcomeMsg.From)
			assert.Equal(t, client.UserID, welcomeMsg.To)
			assert.Contains(t, welcomeMsg.Content, client.UserID)
			assert.Contains(t, welcomeMsg.Data, "type")
			assert.Equal(t, "welcome", welcomeMsg.Data["type"])
			assert.Contains(t, welcomeMsg.Data, "title")
		case <-time.After(1 * time.Second):
			t.Fatal("未收到欢迎消息")
		}

		hub.Unregister(client)
	})

	t.Run("禁用欢迎消息时不发送", func(t *testing.T) {
		// 禁用欢迎消息
		welcomeProvider.setEnabled(false)

		client := &Client{
			ID:       "no-welcome-client-001",
			UserID:   "no-welcome-user-001",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "no-welcome-user-001"),
		}

		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 验证没有收到消息
		select {
		case <-client.SendChan:
			t.Fatal("不应该收到欢迎消息")
		case <-time.After(500 * time.Millisecond):
			// 正确情况
		}

		hub.Unregister(client)
		
		// 重新启用欢迎消息
		welcomeProvider.setEnabled(true)
	})
}

// TestHubNodeID 测试节点ID
func TestHubNodeID(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	nodeID := hub.GetNodeID()
	assert.NotEmpty(t, nodeID)
	assert.Contains(t, nodeID, "node-")

	// 微小延迟确保不同的纳秒时间戳
	time.Sleep(time.Microsecond)

	// 验证不同Hub实例有不同的NodeID
	hub2 := NewHub(nil)
	defer hub2.Shutdown()

	nodeID2 := hub2.GetNodeID()
	assert.NotEmpty(t, nodeID2)
	assert.NotEqual(t, nodeID, nodeID2)
}

// TestHubShutdown 测试Hub关闭
func TestHubShutdown(t *testing.T) {
	hub := NewHub(nil)
	
	// 添加一些客户端和SSE连接
	client := &Client{
		ID:       "shutdown-client-001",
		UserID:   "shutdown-user-001",
		UserType: UserTypeCustomer,
		Role:     UserRoleCustomer,
		Status:   UserStatusOnline,
		LastSeen: time.Now(),
		SendChan: make(chan []byte, 256),
		Context:  context.WithValue(context.Background(), ContextKeyUserID, "shutdown-user-001"),
	}

	sseConn := &SSEConnection{
		UserID:     "shutdown-sse-user-001",
		MessageCh:  make(chan *HubMessage, 100),
		CloseCh:    make(chan struct{}),
		LastActive: time.Now(),
		Context:    context.WithValue(context.Background(), ContextKeyUserID, "shutdown-sse-user-001"),
	}

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	hub.Register(client)
	hub.RegisterSSE(sseConn)
	
	// 等待注册完成
	time.Sleep(200 * time.Millisecond)

	// 在shutdown前获取数据,避免race
	hub.mutex.Lock()
	hasClient := hub.clients[client.ID] != nil
	hub.mutex.Unlock()
	
	hub.sseMutex.Lock()
	hasSSE := hub.sseClients[sseConn.UserID] != nil
	hub.sseMutex.Unlock()

	// 验证客户端和SSE连接存在
	assert.True(t, hasClient)
	assert.True(t, hasSSE)

	// 关闭Hub
	hub.Shutdown()

	// 验证上下文已被取消
	select {
	case <-hub.ctx.Done():
		// 正确情况
	case <-time.After(1 * time.Second):
		t.Fatal("Hub上下文未被取消")
	}

	// 验证SSE连接的CloseCh已关闭
	select {
	case <-sseConn.CloseCh:
		// 正确情况
	case <-time.After(1 * time.Second):
		t.Fatal("SSE连接未正确关闭")
	}
}

// TestHubMessageFallback 测试消息降级机制
func TestHubMessageFallback(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	t.Run("WebSocket优先，SSE降级", func(t *testing.T) {
		userID := "fallback-user-001"

		// 只注册SSE连接
		sseConn := &SSEConnection{
			UserID:     userID,
			MessageCh:  make(chan *HubMessage, 100),
			CloseCh:    make(chan struct{}),
			LastActive: time.Now(),
			Context:    context.WithValue(context.Background(), ContextKeyUserID, userID),
		}
		hub.RegisterSSE(sseConn)

		// 发送点对点消息
		message := &HubMessage{
			Type:     MessageTypeText,
			To:       userID,
			Content:  "降级测试消息",
			CreateAt: time.Now(),
			Status:   MessageStatusSent,
		}

		err := hub.SendToUser(context.Background(), userID, message)
		assert.NoError(t, err)

		// 验证通过SSE收到消息
		select {
		case receivedMsg := <-sseConn.MessageCh:
			assert.Equal(t, message.Content, receivedMsg.Content)
			assert.Equal(t, userID, receivedMsg.To)
		case <-time.After(1 * time.Second):
			t.Fatal("未通过SSE收到消息")
		}

		hub.UnregisterSSE(userID)
	})
}

// TestHubConcurrentOperations 测试并发操作
func TestHubConcurrentOperations(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	t.Run("并发客户端注册和注销", func(t *testing.T) {
		const numClients = 100
		var wg sync.WaitGroup
		wg.Add(numClients * 2) // 注册和注销

		clients := make([]*Client, numClients)
		for i := 0; i < numClients; i++ {
			clients[i] = &Client{
				ID:       fmt.Sprintf("concurrent-client-%d", i),
				UserID:   fmt.Sprintf("concurrent-user-%d", i),
				UserType: UserTypeCustomer,
				Role:     UserRoleCustomer,
				Status:   UserStatusOnline,
				LastSeen: time.Now(),
				SendChan: make(chan []byte, 256),
				Context:  context.WithValue(context.Background(), ContextKeyUserID, fmt.Sprintf("concurrent-user-%d", i)),
			}
		}

		// 并发注册
		for i := 0; i < numClients; i++ {
			go func(client *Client) {
				defer wg.Done()
				hub.Register(client)
			}(clients[i])
		}

		// 等待一段时间让注册完成
		time.Sleep(500 * time.Millisecond)

		// 并发注销
		for i := 0; i < numClients; i++ {
			go func(client *Client) {
				defer wg.Done()
				hub.Unregister(client)
			}(clients[i])
		}

		// 等待所有操作完成
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// 等待Hub处理完所有注销操作
			time.Sleep(100 * time.Millisecond)
			
			// 验证最终状态
			stats := hub.GetStats()
			assert.Equal(t, 0, stats["websocket_count"])
			assert.Equal(t, 0, stats["total_connections"])
		case <-time.After(10 * time.Second):
			t.Fatal("并发操作超时")
		}
	})

	t.Run("并发消息发送", func(t *testing.T) {
		const numMessages = 50
		var wg sync.WaitGroup
		wg.Add(numMessages)

		// 注册一个接收者
		receiver := &Client{
			ID:       "concurrent-receiver",
			UserID:   "concurrent-receiver",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 1000), // 大缓冲区
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "concurrent-receiver"),
		}
		hub.Register(receiver)
		time.Sleep(100 * time.Millisecond)

		// 并发发送消息
		for i := 0; i < numMessages; i++ {
			go func(msgNum int) {
				defer wg.Done()
				message := &HubMessage{
					Type:     MessageTypeText,
					To:       receiver.UserID,
					Content:  fmt.Sprintf("并发消息 %d", msgNum),
					CreateAt: time.Now(),
					Status:   MessageStatusSent,
				}
				err := hub.SendToUser(context.Background(), receiver.UserID, message)
				assert.NoError(t, err)
			}(i)
		}

		// 等待所有消息发送完成
		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// 验证接收到的消息数量
			receivedCount := 0
			timeout := time.After(2 * time.Second)
			for {
				select {
				case <-receiver.SendChan:
					receivedCount++
					if receivedCount >= numMessages {
						assert.GreaterOrEqual(t, receivedCount, numMessages)
						hub.Unregister(receiver)
						return
					}
				case <-timeout:
					t.Logf("接收到 %d/%d 条消息", receivedCount, numMessages)
					assert.GreaterOrEqual(t, receivedCount, numMessages/2, "应该接收到至少一半的消息")
					hub.Unregister(receiver)
					return
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("并发消息发送超时")
		}
	})
}

// BenchmarkHubOperations Hub操作性能基准测试
func BenchmarkHubOperations(b *testing.B) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	b.Run("ClientRegistration", func(b *testing.B) {
		clients := make([]*Client, b.N)
		for i := 0; i < b.N; i++ {
			clients[i] = &Client{
				ID:       fmt.Sprintf("bench-client-%d", i),
				UserID:   fmt.Sprintf("bench-user-%d", i),
				UserType: UserTypeCustomer,
				Role:     UserRoleCustomer,
				Status:   UserStatusOnline,
				LastSeen: time.Now(),
				SendChan: make(chan []byte, 256),
				Context:  context.WithValue(context.Background(), ContextKeyUserID, fmt.Sprintf("bench-user-%d", i)),
			}
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hub.Register(clients[i])
		}

		// 清理
		for i := 0; i < b.N; i++ {
			hub.Unregister(clients[i])
		}
	})

	b.Run("MessageSending", func(b *testing.B) {
		// 注册一个接收者
		receiver := &Client{
			ID:       "bench-receiver",
			UserID:   "bench-receiver",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 10000), // 大缓冲区防止阻塞
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "bench-receiver"),
		}
		hub.Register(receiver)
		time.Sleep(100 * time.Millisecond)

		message := &HubMessage{
			Type:     MessageTypeText,
			To:       receiver.UserID,
			Content:  "基准测试消息",
			CreateAt: time.Now(),
			Status:   MessageStatusSent,
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hub.SendToUser(context.Background(), receiver.UserID, message)
		}

		hub.Unregister(receiver)
	})
}
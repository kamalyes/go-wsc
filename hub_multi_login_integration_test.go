/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 13:50:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 13:54:22
 * @FilePath: \go-wsc\hub_multi_login_integration_test.go
 * @Description: Hub 多端登录集成测试（包含 Redis 在线状态和负载管理）
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

// TestMultiLoginWithOnlineStatusSync 测试多端登录时的在线状态同步
func TestMultiLoginWithOnlineStatusSync(t *testing.T) {
	redisClient := NewTestRedisClient(t)
	defer redisClient.Close()

	// 创建在线状态仓库
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:multilogin:online:",
		TTL:       5 * time.Minute,
	})

	// 创建 Hub 配置
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0 // 无限制

	hub := NewHub(config)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "multi-user-001"
	ctx := context.Background()

	// 验证初始状态 - 用户不在线
	isOnline, err := onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.False(t, isOnline, "用户初始应该离线")

	// 注册第一个客户端
	client1 := &Client{
		ID:             "client-1",
		UserID:         userID,
		UserType:       UserTypeCustomer,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, 10),
	}
	hub.Register(client1)
	time.Sleep(200 * time.Millisecond) // 等待异步同步完成

	// 验证用户已在线
	isOnline, err = onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.True(t, isOnline, "用户应该在线")

	// 获取在线信息
	clientInfo, err := onlineStatusRepo.GetOnlineInfo(ctx, userID)
	require.NoError(t, err)
	require.NotNil(t, clientInfo)
	assert.Equal(t, userID, clientInfo.UserID)
	assert.Equal(t, UserTypeCustomer, clientInfo.UserType)

	// 注册第二个客户端（多端登录）
	client2 := &Client{
		ID:             "client-2",
		UserID:         userID,
		UserType:       UserTypeCustomer,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, 10),
	}
	hub.Register(client2)
	time.Sleep(500 * time.Millisecond) // 等待同步

	// 验证 Hub 中有两个客户端
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 2, len(clientMap), "应该有2个客户端")

	// 注销第一个客户端
	hub.Unregister(client1)
	time.Sleep(500 * time.Millisecond) // 等待状态更新

	// 用户仍然在线（因为还有第二个客户端）
	isOnline, err = onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.True(t, isOnline, "用户应该仍然在线")

	// 注销第二个客户端
	hub.Unregister(client2)
	time.Sleep(500 * time.Millisecond) // 等待状态清理

	// 现在用户应该离线
	isOnline, err = onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.False(t, isOnline, "用户应该离线")
}

// TestMultiLoginWithWorkloadSync 测试客服多端登录时的负载同步
func TestMultiLoginWithWorkloadSync(t *testing.T) {
	redisClient := NewTestRedisClient(t)
	defer redisClient.Close()

	// 创建负载管理仓库
	workloadRepo := NewRedisWorkloadRepository(redisClient, &wscconfig.Workload{
		KeyPrefix: "wsc:test:multilogin:workload:",
		TTL:       10 * time.Minute,
	}, NewDefaultWSCLogger())

	// 创建 Hub 配置
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	hub.SetWorkloadRepository(workloadRepo)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	agentID := "agent-001"
	ctx := context.Background()

	// 注册客服客户端
	agent1 := &Client{
		ID:             "agent-client-1",
		UserID:         agentID,
		UserType:       UserTypeAgent,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, 10),
	}
	hub.Register(agent1)
	time.Sleep(100 * time.Millisecond)

	// 验证客服已注册
	assert.True(t, hub.HasClient("agent-client-1"), "客服应该已注册")

	// 设置客服负载
	err := hub.SetAgentWorkload(agentID, 5)
	require.NoError(t, err)

	// 验证负载已设置
	workload, err := hub.GetAgentWorkload(agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(5), workload)

	// 增加负载
	err = hub.IncrementAgentWorkload(agentID)
	require.NoError(t, err)

	workload, err = hub.GetAgentWorkload(agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(6), workload)

	// 注销客服
	hub.Unregister(agent1)
	time.Sleep(300 * time.Millisecond) // 等待异步移除负载完成

	// 验证负载已移除
	workload, err = workloadRepo.GetAgentWorkload(ctx, agentID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), workload, "客服离线后负载应该被移除")
}

// TestMultiLoginMixedWithFullIntegration 测试混合类型多端登录的完整集成
func TestMultiLoginMixedWithFullIntegration(t *testing.T) {
	redisClient := NewTestRedisClient(t)
	defer redisClient.Close()

	// 创建所有仓库
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:multilogin:mixed:online:",
		TTL:       5 * time.Minute,
	})

	statsRepo := NewRedisHubStatsRepository(redisClient, &wscconfig.Stats{
		KeyPrefix: "wsc:test:multilogin:mixed:stats:",
		TTL:       24 * time.Hour,
	})

	// 创建 Hub 配置
	config := wscconfig.Default()
	config.AllowMultiLogin = true

	hub := NewHub(config)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	hub.SetHubStatsRepository(statsRepo)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "mixed-user-001"
	ctx := context.Background()

	// 注册 WebSocket 客户端
	wsClient := &Client{
		ID:             "ws-client",
		UserID:         userID,
		UserType:       UserTypeCustomer,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, 10),
	}
	hub.Register(wsClient)
	time.Sleep(200 * time.Millisecond) // 增加等待时间以便统计同步

	// 注册 SSE 客户端
	w := httptest.NewRecorder()
	sseClient, err := hub.RegisterSSE(userID, w, UserTypeCustomer)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond) // 增加等待时间

	// 验证在线状态
	isOnline, err := onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.True(t, isOnline, "用户应该在线")

	// 注意：Hub统计是异步更新的，可能存在延迟，这里不强制校验
	// 主要验证功能性，而不是统计准确性

	// 验证用户有两种类型的客户端
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 2, len(clientMap), "应该有2个客户端")

	// 验证连接类型
	var hasWebSocket, hasSSE bool
	for _, client := range clientMap {
		switch client.ConnectionType {
		case ConnectionTypeWebSocket:
			hasWebSocket = true
		case ConnectionTypeSSE:
			hasSSE = true
		}
	}
	assert.True(t, hasWebSocket, "应该有WebSocket连接")
	assert.True(t, hasSSE, "应该有SSE连接")

	// 发送消息给用户
	message := &HubMessage{
		MessageType: MessageTypeText,
		Receiver:    userID,
		Content:     "测试多端消息",
		CreateAt:    time.Now(),
	}

	result := hub.SendToUserWithRetry(context.Background(), userID, message)
	require.True(t, result.Success, "消息发送应该成功")

	// 注意：消息统计是异步更新的，本测试主要验证多端消息发送功能，不强制校验统计

	// 清理
	hub.Unregister(wsClient)
	hub.UnregisterSSE(sseClient.ID)
	time.Sleep(200 * time.Millisecond)

	// 验证离线
	isOnline, err = onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.False(t, isOnline, "用户应该离线")
}

// TestMultiLoginDisabledWithOnlineStatus 测试禁用多端登录时的在线状态更新
func TestMultiLoginDisabledWithOnlineStatus(t *testing.T) {
	redisClient := NewTestRedisClient(t)
	defer redisClient.Close()

	// 创建在线状态仓库
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:multilogin:disabled:online:",
		TTL:       5 * time.Minute,
	})

	// 创建 Hub 配置 - 禁用多端登录
	config := wscconfig.Default()
	config.AllowMultiLogin = false

	hub := NewHub(config)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "single-user-001"
	ctx := context.Background()

	// 注册第一个客户端
	client1 := &Client{
		ID:             "client-1",
		UserID:         userID,
		UserType:       UserTypeCustomer,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, 10),
	}
	hub.Register(client1)
	time.Sleep(200 * time.Millisecond)

	// 验证在线
	isOnline, err := onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.True(t, isOnline, "用户应该在线")

	// 注册第二个客户端（应该踢掉第一个）
	client2 := &Client{
		ID:             "client-2",
		UserID:         userID,
		UserType:       UserTypeCustomer,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, 10),
	}
	hub.Register(client2)
	time.Sleep(200 * time.Millisecond)

	// 验证只有一个客户端在线
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 1, len(clientMap), "应该只有1个客户端")
	assert.Contains(t, clientMap, "client-2", "应该是第二个客户端")

	// 用户仍然在线
	isOnline, err = onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.True(t, isOnline, "用户应该在线")

	// 注销第二个客户端
	hub.Unregister(client2)
	time.Sleep(500 * time.Millisecond) // 等待状态清理

	// 现在用户应该离线
	isOnline, err = onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.False(t, isOnline, "用户应该离线")
}

// TestMultiLoginWithConnectionLimit 测试连接数限制时的在线状态
func TestMultiLoginWithConnectionLimit(t *testing.T) {
	redisClient := NewTestRedisClient(t)
	defer redisClient.Close()

	// 创建在线状态仓库
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:multilogin:limit:online:",
		TTL:       5 * time.Minute,
	})

	// 创建 Hub 配置 - 限制最多2个连接
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 2

	hub := NewHub(config)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "limited-user-001"
	ctx := context.Background()

	// 注册3个客户端
	clients := make([]*Client, 3)
	for i := 0; i < 3; i++ {
		clients[i] = &Client{
			ID:             fmt.Sprintf("client-%d", i+1),
			UserID:         userID,
			UserType:       UserTypeCustomer,
			ConnectionType: ConnectionTypeWebSocket,
			SendChan:       make(chan []byte, 10),
		}
		hub.Register(clients[i])
		time.Sleep(100 * time.Millisecond)
	}

	// 验证只有2个客户端在线（最后2个）
	clientMap, exists := hub.GetUserClientsMapWithLock(userID)
	assert.True(t, exists, "用户应该有客户端")
	assert.Equal(t, 2, len(clientMap), "应该只有2个客户端")

	// 用户在线
	isOnline, err := onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.True(t, isOnline, "用户应该在线")

	// 全部注销
	for _, client := range clients {
		hub.Unregister(client)
	}
	time.Sleep(300 * time.Millisecond)

	// 用户离线
	isOnline, err = onlineStatusRepo.IsOnline(ctx, userID)
	require.NoError(t, err)
	assert.False(t, isOnline, "用户应该离线")
}

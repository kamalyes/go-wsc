/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 09:54:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 11:54:02
 * @FilePath: \go-wsc\hub_query_test.go
 * @Description: Hub查询和统计功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"fmt"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetStats 测试获取统计信息
func TestGetStats(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册客户端
	client := &Client{
		ID:       "stats-client",
		UserID:   "stats-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 获取统计信息
	stats := hub.GetStats()
	
	assert.NotNil(t, stats, "统计信息不应为空")
	assert.GreaterOrEqual(t, stats.TotalClients, 1, "应该至少有1个连接")
	assert.NotEmpty(t, hub.GetNodeID(), "NodeID不应为空")
	
	hub.Unregister(client)
}

// TestIsUserOnline 测试检查用户在线状态
func TestIsUserOnline(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 用户离线
	online, err := hub.IsUserOnline("offline-user")
	require.NoError(t, err)
	assert.False(t, online, "用户应该离线")

	// 注册客户端
	client := &Client{
		ID:       "online-client",
		UserID:   "online-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 用户在线
	online, err = hub.IsUserOnline("online-user")
	require.NoError(t, err)
	assert.True(t, online, "用户应该在线")

	hub.Unregister(client)
}

// TestHasClient 测试检查客户端是否存在
func TestHasClient(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 客户端不存在
	exists := hub.HasClient("non-existent-client")
	assert.False(t, exists, "客户端不应该存在")

	// 注册客户端
	client := &Client{
		ID:       "exists-client",
		UserID:   "exists-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 客户端存在
	exists = hub.HasClient("exists-client")
	assert.True(t, exists, "客户端应该存在")

	hub.Unregister(client)
}

// TestGetClientsByUserID 测试根据UserID获取客户端
func TestGetClientsByUserID(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	userID := "multi-user"

	// 注册多个客户端
	client1 := &Client{
		ID:       "user-client-1",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	client2 := &Client{
		ID:       "user-client-2",
		UserID:   userID,
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client1)
	hub.Register(client2)
	time.Sleep(50 * time.Millisecond)

	// 获取客户端列表
	clients := hub.GetClientsByUserID(userID)
	assert.Equal(t, 2, len(clients), "应该有2个客户端")

	// 清理
	hub.Unregister(client1)
	hub.Unregister(client2)
}

// TestGetClientsByUserType 测试根据用户类型获取客户端
func TestGetClientsByUserType(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册不同类型的客户端
	customer := &Client{
		ID:       "type-customer",
		UserID:   "user-c",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	agent := &Client{
		ID:       "type-agent",
		UserID:   "user-a",
		UserType: UserTypeAgent,
		SendChan: make(chan []byte, 100),
	}

	hub.Register(customer)
	hub.Register(agent)
	time.Sleep(50 * time.Millisecond)

	// 获取customer类型的客户端
	customers := hub.GetClientsByUserType(UserTypeCustomer)
	assert.Equal(t, 1, len(customers), "应该有1个customer")

	// 获取agent类型的客户端
	agents := hub.GetClientsByUserType(UserTypeAgent)
	assert.Equal(t, 1, len(agents), "应该有1个agent")

	hub.Unregister(customer)
	hub.Unregister(agent)
}

// TestGetAllClients 测试获取所有客户端
func TestGetAllClients(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册多个客户端
	for i := 1; i <= 3; i++ {
		client := &Client{
			ID:       fmt.Sprintf("all-client-%d", i),
			UserID:   fmt.Sprintf("all-user-%d", i),
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
		}
		hub.Register(client)
	}
	time.Sleep(50 * time.Millisecond)

	// 获取所有客户端
	allClients := hub.GetClientsCopy()
	assert.GreaterOrEqual(t, len(allClients), 3, "应该至少有3个客户端")
}

// TestGetNodeID 测试获取节点ID
func TestGetNodeID(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("192.168.1.100", 8080)
	hub := NewHub(config)
	defer hub.Shutdown()

	nodeID := hub.GetNodeID()
	assert.NotEmpty(t, nodeID, "NodeID不应为空")
	assert.Contains(t, nodeID, "192.168.1.100", "NodeID应该包含IP")
	assert.Contains(t, nodeID, "8080", "NodeID应该包含端口")
}

// TestGetWorkerID 测试获取WorkerID
func TestGetWorkerID(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	workerID := hub.GetWorkerID()
	assert.Greater(t, workerID, int64(0), "WorkerID应该大于0")
}

// TestGetIDGenerator 测试获取ID生成器
func TestGetIDGenerator(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	generator := hub.GetIDGenerator()
	assert.NotNil(t, generator, "ID生成器不应为空")

	// 生成ID测试
	id := generator.GenerateRequestID()
	assert.NotEmpty(t, id, "生成的ID不应为空")
}

// TestWaitForStart 测试等待启动完成
func TestWaitForStart(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	// 启动Hub
	go hub.Run()

	// 等待启动完成
	start := time.Now()
	hub.WaitForStart()
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 5*time.Second, "启动应该在5秒内完成")
}

// TestGetOnlineUserCount 测试获取在线用户数
func TestGetOnlineUserCount(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 初始应该为0
	stats := hub.GetStats()
	initialCount := stats.TotalClients

	// 注册客户端
	client := &Client{
		ID:       "count-client",
		UserID:   "count-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 应该增加1
	stats = hub.GetStats()
	assert.Equal(t, initialCount+1, stats.TotalClients, "连接数应该增加1")

	hub.Unregister(client)
	time.Sleep(50 * time.Millisecond)

	// 应该恢复原值
	stats = hub.GetStats()
	assert.Equal(t, initialCount, stats.TotalClients, "连接数应该恢复")
}

// TestGetClientByID 测试根据ID获取客户端
func TestGetClientByID(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	// 注册客户端
	client := &Client{
		ID:       "getid-client",
		UserID:   "getid-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, 100),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 获取客户端
	found := hub.GetClientByID("getid-client")
	assert.NotNil(t, found, "应该找到客户端")
	assert.Equal(t, "getid-client", found.ID, "客户端ID应该匹配")

	// 获取不存在的客户端
	notFound := hub.GetClientByID("non-existent")
	assert.Nil(t, notFound, "不应该找到客户端")

	hub.Unregister(client)
}

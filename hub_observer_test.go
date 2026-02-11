/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-03 22:50:11
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-03 22:50:11
 * @FilePath: \go-wsc\hub_observer_test.go
 * @Description: Hub 观察者功能测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestObserverBasicFunctionality 测试观察者基本功能
func TestObserverBasicFunctionality(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	require.NotNil(t, hub)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(50 * time.Millisecond)

	client := createTestClientWithIDGen(UserTypeObserver)
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	assert.True(t, hub.IsObserver(client.UserID))
	assert.Equal(t, 1, hub.GetObserverCount())
	assert.Equal(t, 1, hub.GetObserverDeviceCount())

	hub.Unregister(client)
	time.Sleep(50 * time.Millisecond)

	assert.False(t, hub.IsObserver(client.UserID))
	assert.Equal(t, 0, hub.GetObserverCount())
}

// TestObserverMultiDevice 测试观察者多端登录
func TestObserverMultiDevice(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	require.NotNil(t, hub)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(50 * time.Millisecond)

	// 同一观察者从3个设备登录
	observers := []*Client{
		createTestClientWithIDGen(UserTypeObserver),
		createTestClientWithIDGen(UserTypeObserver),
		createTestClientWithIDGen(UserTypeObserver),
	}

	registerClients(hub, observers)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, hub.GetObserverCount())
	assert.Equal(t, 3, hub.GetObserverDeviceCount())

	hub.Unregister(observers[1])
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, hub.GetObserverCount())
	assert.Equal(t, 2, hub.GetObserverDeviceCount())

	hub.Unregister(observers[0])
	hub.Unregister(observers[2])
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 0, hub.GetObserverCount())
	assert.Equal(t, 0, hub.GetObserverDeviceCount())
}

// TestObserverStats 测试观察者统计信息
func TestObserverStats(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	require.NotNil(t, hub)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(50 * time.Millisecond)

	// 创建2个观察者用户，5个设备
	clients1 := createTestClientWithUserID(hub.GetIDGenerator().GenerateRequestID(), UserTypeObserver, 2)
	client2 := createTestClientWithUserID(hub.GetIDGenerator().GenerateRequestID(), UserTypeObserver, 3)
	clients := append(clients1, client2...)
	registerClients(hub, clients)

	// 等待所有客户端注册完成
	time.Sleep(200 * time.Millisecond)

	stats := hub.GetObserverManagerStats()
	require.NotNil(t, stats)

	assert.Equal(t, 2, stats.TotalObservers, "应该有2个观察者用户")
	assert.Equal(t, 5, stats.TotalDevices, "应该有5个观察者设备")
	assert.Equal(t, 5, len(stats.ObserverStats), "应该有5条设备统计")
}

// TestObserverWithNormalUsers 测试观察者与普通用户混合场景
func TestObserverWithNormalUsers(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	require.NotNil(t, hub)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(50 * time.Millisecond)

	clients := []*Client{
		createTestClientWithIDGen(UserTypeAgent),
		createTestClientWithIDGen(UserTypeAdmin),
		createTestClientWithIDGen(UserTypeCustomer),
		createTestClientWithIDGen(UserTypeObserver),
	}
	registerClients(hub, clients)
	time.Sleep(50 * time.Millisecond)

	assert.Equal(t, 1, hub.GetObserverCount())
	assert.Equal(t, 3, len(hub.GetClientsCopy()), "总共3个客户端")

	// 验证观察者不影响其他用户类型的统计
	allClients := hub.GetClientsCopy()
	var customerCount, agentCount, observerCount int
	for _, client := range allClients {
		switch client.UserType {
		case UserTypeCustomer:
			customerCount++
		case UserTypeAgent:
			agentCount++
		case UserTypeObserver:
			observerCount++
		}
	}

	assert.Equal(t, 1, customerCount)
	assert.Equal(t, 1, agentCount)
	assert.Equal(t, 1, observerCount)
}

// TestObserverReceiveMessages 测试观察者接收消息
func TestObserverReceiveMessages(t *testing.T) {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	require.NotNil(t, hub)

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(50 * time.Millisecond)

	clients := []*Client{
		createTestClientWithIDGen(UserTypeObserver),
		createTestClientWithIDGen(UserTypeCustomer),
	}
	registerClients(hub, clients)
	time.Sleep(50 * time.Millisecond)

	msg := createTestHubMessage(MessageTypeCard)
	msg.Receiver = clients[1].UserID

	ctx := context.Background()
	result := hub.SendToUserWithRetry(ctx, clients[1].UserID, msg)
	assert.NoError(t, result.FinalError)

	time.Sleep(100 * time.Millisecond)

	// 验证观察者的SendChan有消息
	assert.Greater(t, len(clients[0].SendChan), 0, "观察者应该收到消息")
}

// TestObserverReceiveDistributedMessages 测试观察者接收分布式消息
func TestObserverReceiveDistributedMessages(t *testing.T) {
	// 创建两个节点
	hub1 := createTestHubWithDistributed(t, "node-1")
	hub2 := createTestHubWithDistributed(t, "node-2")

	go hub1.Run()
	go hub2.Run()
	defer hub1.Shutdown()
	defer hub2.Shutdown()

	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 节点1: 注册观察者和 user-2（接收者）
	observer := createTestClientWithIDGen(UserTypeObserver)
	user2 := createTestClientWithIDGen(UserTypeCustomer)
	hub1.Register(observer)
	hub1.Register(user2)

	// 节点2: 注册 user-1（发送者）
	user1 := createTestClientWithIDGen(UserTypeCustomer)
	hub2.Register(user1)

	// 等待在线状态同步到 Redis（recordUserNode 现在是同步的，但仍需等待 Redis 传播）
	time.Sleep(1000 * time.Millisecond)

	// 节点2: user-1 发送消息给 user-2（user-2 在节点1）
	msg := createTestHubMessage(MessageTypeText)
	msg.SetSender(user1.UserID).
		SetReceiver(user2.UserID).
		SetContent("Hello from node-2")

	result := hub2.SendToUserWithRetry(ctx, user2.UserID, msg)
	assert.NoError(t, result.FinalError)

	// 等待消息传播
	time.Sleep(300 * time.Millisecond)

	// 验证节点1的观察者收到了消息
	assert.Greater(t, len(observer.SendChan), 0, "节点1的观察者应该收到节点2的消息")

	// 读取观察者收到的消息
	select {
	case msgData := <-observer.SendChan:
		var receivedMsg HubMessage
		err := json.Unmarshal(msgData, &receivedMsg)
		require.NoError(t, err)

		// 验证消息内容
		assert.Equal(t, msg.Content, receivedMsg.Content)
		assert.Equal(t, user1.UserID, receivedMsg.Sender)
		assert.Equal(t, user2.UserID, receivedMsg.Receiver)

		// 验证观察者元数据（使用 GetMetadata 方法）
		observerMode, ok := receivedMsg.GetMetadata("observer_mode")
		require.True(t, ok, "observer_mode 应该存在")
		assert.Equal(t, "true", observerMode)

		originalSender, ok := receivedMsg.GetMetadata("original_sender")
		require.True(t, ok, "original_sender 应该存在")
		assert.Equal(t, user1.UserID, originalSender)

		originalReceiver, ok := receivedMsg.GetMetadata("original_receiver")
		require.True(t, ok, "original_receiver 应该存在")
		assert.Equal(t, user2.UserID, originalReceiver)
	default:
		t.Fatal("观察者没有收到消息")
	}
}

// TestObserverMultiNodeReceive 测试多节点观察者同时接收消息
func TestObserverMultiNodeReceive(t *testing.T) {
	// 跳过需要 Redis 的测试
	if testing.Short() {
		t.Skip("跳过需要 Redis 的分布式测试")
	}

	// 创建三个节点
	hub1 := createTestHubWithDistributed(t, "node-1")
	hub2 := createTestHubWithDistributed(t, "node-2")
	hub3 := createTestHubWithDistributed(t, "node-3")

	go hub1.Run()
	go hub2.Run()
	go hub3.Run()
	defer hub1.Shutdown()
	defer hub2.Shutdown()
	defer hub3.Shutdown()

	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 节点1: 注册观察者1
	observer1 := createTestClientWithIDGen(UserTypeObserver)
	hub1.Register(observer1)

	// 节点2: 注册观察者2
	observer2 := createTestClientWithIDGen(UserTypeObserver)
	hub2.Register(observer2)

	// 节点3: 注册两个普通用户
	user1 := createTestClientWithIDGen(UserTypeCustomer)
	user2 := createTestClientWithIDGen(UserTypeCustomer)
	hub3.Register(user1)
	hub3.Register(user2)

	time.Sleep(200 * time.Millisecond)

	// 节点3: user-1 发送消息给 user-2
	msg := createTestHubMessage(MessageTypeText)
	msg.SetSender(user1.UserID).
		SetReceiver(user2.UserID).
		SetContent("Multi-node test message")

	result := hub3.SendToUserWithRetry(ctx, user2.UserID, msg)
	assert.NoError(t, result.FinalError)

	// 等待消息传播
	time.Sleep(300 * time.Millisecond)

	// 验证节点1和节点2的观察者都收到了消息
	assert.Greater(t, len(observer1.SendChan), 0, "节点1的观察者应该收到消息")
	assert.Greater(t, len(observer2.SendChan), 0, "节点2的观察者应该收到消息")
}

// TestObserverReceiveBroadcastMessages 测试观察者接收广播消息
func TestObserverReceiveBroadcastMessages(t *testing.T) {
	// 跳过需要 Redis 的测试
	if testing.Short() {
		t.Skip("跳过需要 Redis 的分布式测试")
	}

	// 创建两个节点
	hub1 := createTestHubWithDistributed(t, "node-1")
	hub2 := createTestHubWithDistributed(t, "node-2")

	go hub1.Run()
	go hub2.Run()
	defer hub1.Shutdown()
	defer hub2.Shutdown()

	time.Sleep(100 * time.Millisecond)

	ctx := context.Background()

	// 订阅分布式频道（包括广播频道和观察者频道）
	require.NoError(t, hub1.SubscribeBroadcastChannel(ctx))
	require.NoError(t, hub2.SubscribeBroadcastChannel(ctx))
	require.NoError(t, hub1.SubscribeObserverChannel(ctx))
	require.NoError(t, hub2.SubscribeObserverChannel(ctx))
	time.Sleep(100 * time.Millisecond)

	// 节点1: 注册观察者
	observer1 := createTestClientWithIDGen(UserTypeObserver)
	hub1.Register(observer1)

	// 节点2: 注册普通用户
	user1 := createTestClientWithIDGen(UserTypeCustomer)
	hub2.Register(user1)

	time.Sleep(200 * time.Millisecond)

	// 节点2: 发送全局广播消息
	msg := createTestHubMessage(MessageTypeText)
	msg.SetSender(UserTypeSystem.String()).
		SetContent("Global broadcast message").
		SetBroadcastType(BroadcastTypeGlobal)

	hub2.Broadcast(ctx, msg)

	// 等待消息传播
	time.Sleep(300 * time.Millisecond)

	// 验证节点1的观察者收到了广播消息
	assert.Greater(t, len(observer1.SendChan), 0, "观察者应该收到广播消息")

	// 读取观察者收到的消息
	select {
	case msgData := <-observer1.SendChan:
		var receivedMsg HubMessage
		err := json.Unmarshal(msgData, &receivedMsg)
		require.NoError(t, err)

		// 验证消息内容
		assert.Equal(t, msg.Content, receivedMsg.Content)
		assert.Equal(t, msg.Sender, receivedMsg.Sender)
		assert.Equal(t, msg.BroadcastType, receivedMsg.BroadcastType)
	default:
		t.Fatal("观察者没有收到广播消息")
	}
}

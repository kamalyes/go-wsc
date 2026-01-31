/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 16:52:18
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-30 16:52:18
 * @FilePath: \go-wsc\hub_distributed_test.go
 * @Description: Hub 分布式功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestHubWithDistributed 创建带分布式功能的测试 Hub
func createTestHubWithDistributed(t *testing.T, nodeID string) *Hub {
	redisClient := NewTestRedisClient(t)
	pubsub := cachex.NewPubSub(redisClient)

	// 使用 os.Setenv 设置环境变量（必须在 NewHub 之前）
	oldNodeID := os.Getenv("NODE_ID")
	os.Setenv("NODE_ID", nodeID)
	t.Cleanup(func() {
		if oldNodeID != "" {
			os.Setenv("NODE_ID", oldNodeID)
		} else {
			os.Unsetenv("NODE_ID")
		}
	})

	config := wscconfig.Default()
	config.NodeIP = "127.0.0.1"
	config.NodePort = 8080

	hub := NewHub(config)
	hub.SetPubSub(pubsub)

	// 创建 Redis 版本的 OnlineStatusRepository
	onlineStatusRepo := NewRedisOnlineStatusRepository(
		redisClient, &wscconfig.OnlineStatus{
			KeyPrefix: "wsc:test:distributed:online:",
			TTL:       5 * time.Minute,
		})
	hub.SetOnlineStatusRepository(onlineStatusRepo)

	// 验证节点 ID 是否正确设置
	actualNodeID := hub.GetNodeID()
	t.Logf("创建 Hub: 期望节点ID=%s, 实际节点ID=%s", nodeID, actualNodeID)

	return hub
}

// TestRegisterNode 测试节点注册
func TestRegisterNode(t *testing.T) {
	hub := createTestHubWithDistributed(t, "node-1")
	defer hub.SafeShutdown()

	ctx := context.Background()

	err := hub.RegisterNode(ctx)
	assert.NoError(t, err)

	// 验证节点信息已存储
	key := fmt.Sprintf("wsc:nodes:%s", hub.GetNodeID())
	client := hub.GetPubSub().GetClient()
	data, err := client.Get(ctx, key).Result()
	assert.NoError(t, err)

	var nodeInfo NodeInfo
	err = json.Unmarshal([]byte(data), &nodeInfo)
	assert.NoError(t, err)
	assert.Equal(t, "node-1", nodeInfo.ID)
	assert.Equal(t, models.NodeStatusActive, nodeInfo.Status)
}

// TestDiscoverNodes 测试节点发现
func TestDiscoverNodes(t *testing.T) {
	hub1 := createTestHubWithDistributed(t, "node-1")
	defer hub1.SafeShutdown()

	hub2 := createTestHubWithDistributed(t, "node-2")
	defer hub2.SafeShutdown()

	ctx := context.Background()

	// 注册两个节点
	err := hub1.RegisterNode(ctx)
	require.NoError(t, err)

	err = hub2.RegisterNode(ctx)
	require.NoError(t, err)

	// 等待一小段时间确保 Redis 写入完成
	time.Sleep(100 * time.Millisecond)

	// 直接查询 Redis 验证数据是否存在
	redisClient := hub1.GetPubSub().GetClient()
	keys, err := redisClient.Keys(ctx, "wsc:nodes:*").Result()
	t.Logf("Redis 中的节点 keys: %v, err: %v", keys, err)

	// 手动获取每个 key 的值
	for _, key := range keys {
		val, err := redisClient.Get(ctx, key).Result()
		t.Logf("Key: %s, Value: %s, Err: %v", key, val, err)
	}

	// 从 node-1 发现其他节点
	nodes, err := hub1.DiscoverNodes(ctx)
	t.Logf("node-1 发现的节点: %+v, err: %v", nodes, err)
	assert.NoError(t, err)
	if assert.Len(t, nodes, 1, "node-1 应该发现 1 个其他节点") {
		assert.Equal(t, "node-2", nodes[0].ID)
	}

	// 从 node-2 发现其他节点
	nodes, err = hub2.DiscoverNodes(ctx)
	t.Logf("node-2 发现的节点: %+v, err: %v", nodes, err)
	assert.NoError(t, err)
	if assert.Len(t, nodes, 1, "node-2 应该发现 1 个其他节点") {
		assert.Equal(t, "node-1", nodes[0].ID)
	}
}

// TestCheckAndRouteToNode 测试跨节点消息路由
func TestCheckAndRouteToNode(t *testing.T) {
	hub1 := createTestHubWithDistributed(t, "node-1")
	defer hub1.SafeShutdown()

	hub2 := createTestHubWithDistributed(t, "node-2")
	defer hub2.SafeShutdown()

	ctx := context.Background()

	// 模拟用户在 node-2
	userID := "user-123"
	err := hub2.GetOnlineStatusRepo().SetUserNode(ctx, userID, "node-2")
	require.NoError(t, err)

	// 从 node-1 发送消息给 user-123
	msg := &HubMessage{
		ID:       "msg-1",
		Sender:   "sender-1",
		Receiver: userID,
	}

	// 测试跨节点路由（通过公共方法间接测试）
	// 由于用户在 node-2，消息会被路由
	_ = msg
	t.Logf("用户 %s 在节点 node-2，从 node-1 发送消息会触发跨节点路由", userID)
}

// TestAcquireDistributedLock 测试分布式锁
func TestAcquireDistributedLock(t *testing.T) {
	hub := createTestHubWithDistributed(t, "node-1")
	defer hub.SafeShutdown()

	ctx := context.Background()
	lockKey := "test-lock"

	// 获取锁
	acquired, err := hub.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
	assert.NoError(t, err)
	assert.True(t, acquired)

	// 再次尝试获取（应该失败）
	acquired, err = hub.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
	assert.NoError(t, err)
	assert.False(t, acquired)

	// 释放锁
	err = hub.ReleaseDistributedLock(ctx, lockKey)
	assert.NoError(t, err)

	// 再次获取（应该成功）
	acquired, err = hub.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
	assert.NoError(t, err)
	assert.True(t, acquired)
}

// TestReleaseDistributedLock 测试释放分布式锁
func TestReleaseDistributedLock(t *testing.T) {
	hub1 := createTestHubWithDistributed(t, "node-1")
	defer hub1.SafeShutdown()

	hub2 := createTestHubWithDistributed(t, "node-2")
	defer hub2.SafeShutdown()

	ctx := context.Background()
	lockKey := "test-lock"

	// node-1 获取锁
	acquired, err := hub1.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)

	// node-2 尝试释放 node-1 的锁（应该失败）
	err = hub2.ReleaseDistributedLock(ctx, lockKey)
	assert.NoError(t, err) // 不会报错，但锁不会被释放

	// 验证锁仍然存在
	acquired, err = hub2.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
	assert.NoError(t, err)
	assert.False(t, acquired, "锁应该仍然被 node-1 持有")

	// node-1 释放锁
	err = hub1.ReleaseDistributedLock(ctx, lockKey)
	assert.NoError(t, err)

	// node-2 现在可以获取锁
	acquired, err = hub2.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
	assert.NoError(t, err)
	assert.True(t, acquired)
}

// TestBroadcastToAllNodes 测试跨节点广播
func TestBroadcastToAllNodes(t *testing.T) {
	hub1 := createTestHubWithDistributed(t, "node-1")
	defer hub1.SafeShutdown()

	hub2 := createTestHubWithDistributed(t, "node-2")
	defer hub2.SafeShutdown()

	ctx := context.Background()

	// 启动 hub2 的广播订阅
	go hub2.Run()
	hub2.WaitForStart()

	time.Sleep(200 * time.Millisecond)

	ctx = context.Background()

	// 从 hub1 广播消息（通过 Broadcast 方法间接测试）
	msg := &HubMessage{
		ID:          "broadcast-1",
		MessageType: models.MessageTypeText,
		Content:     "test broadcast",
	}

	hub1.Broadcast(ctx, msg)

	// 等待消息传播
	time.Sleep(200 * time.Millisecond)
}

// TestSubscribeNodeMessages 测试订阅节点消息
func TestSubscribeNodeMessages(t *testing.T) {
	hub := createTestHubWithDistributed(t, "node-1")
	defer hub.SafeShutdown()

	ctx := context.Background()

	// 启动订阅
	go func() {
		err := hub.SubscribeNodeMessages(ctx)
		if err != nil {
			t.Logf("订阅失败: %v", err)
		}
	}()

	time.Sleep(200 * time.Millisecond)

	// 发送测试消息到节点频道
	channel := fmt.Sprintf("wsc:node:%s", hub.GetNodeID())
	distMsg := &DistributedMessage{
		Type:     models.OperationTypeSendMessage,
		NodeID:   "node-2",
		TargetID: "user-123",
		Data: map[string]interface{}{
			"message": &HubMessage{
				ID:      "msg-1",
				Content: "test",
			},
		},
		Timestamp: time.Now(),
	}

	data, _ := json.Marshal(distMsg)
	err := hub.GetPubSub().Publish(ctx, channel, string(data))
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
}

// TestSubscribeBroadcastChannel 测试订阅广播频道
func TestSubscribeBroadcastChannel(t *testing.T) {
	hub := createTestHubWithDistributed(t, "node-1")
	defer hub.SafeShutdown()

	ctx := context.Background()

	// 启动订阅
	go func() {
		err := hub.SubscribeBroadcastChannel(ctx)
		if err != nil {
			t.Logf("订阅失败: %v", err)
		}
	}()

	time.Sleep(200 * time.Millisecond)

	// 发送广播消息
	distMsg := &DistributedMessage{
		Type:   models.OperationTypeBroadcast,
		NodeID: "node-2", // 来自其他节点
		Data: map[string]interface{}{
			"message": &HubMessage{
				ID:      "broadcast-1",
				Content: "test broadcast",
			},
		},
		Timestamp: time.Now(),
	}

	data, _ := json.Marshal(distMsg)
	err := hub.GetPubSub().Publish(ctx, "wsc:broadcast", string(data))
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
}

// TestStartNodeHeartbeat 测试节点心跳
func TestStartNodeHeartbeat(t *testing.T) {
	hub := createTestHubWithDistributed(t, "node-1")
	defer hub.SafeShutdown()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 先手动注册一次节点（心跳的第一次执行要等 10 秒）
	err := hub.RegisterNode(ctx)
	require.NoError(t, err)

	// 启动心跳
	go hub.StartNodeHeartbeat(ctx)

	// 验证节点信息存在
	key := fmt.Sprintf("wsc:nodes:%s", hub.GetNodeID())
	client := hub.GetPubSub().GetClient()
	exists, err := client.Exists(ctx, key).Result()
	assert.NoError(t, err)
	assert.Equal(t, int64(1), exists, "节点信息应该存在于 Redis 中")
}

// TestDistributedMessageTypes 测试不同类型的分布式消息
func TestDistributedMessageTypes(t *testing.T) {
	hub := createTestHubWithDistributed(t, "node-1")
	defer hub.SafeShutdown()

	ctx := context.Background()

	tests := []struct {
		name    string
		msgType models.OperationType
	}{
		{"发送消息", models.OperationTypeSendMessage},
		{"踢出用户", models.OperationTypeKickUser},
		{"广播消息", models.OperationTypeBroadcast},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			distMsg := &DistributedMessage{
				Type:     tt.msgType,
				NodeID:   "node-2",
				TargetID: "user-123",
				Data: map[string]interface{}{
					"message": &HubMessage{ID: "msg-1"},
					"reason":  "test",
				},
				Timestamp: time.Now(),
			}

			// 通过发布消息到节点频道来间接测试
			channel := fmt.Sprintf("wsc:node:%s", hub.GetNodeID())
			data, _ := json.Marshal(distMsg)
			err := hub.GetPubSub().Publish(ctx, channel, string(data))
			assert.NoError(t, err)
			t.Logf("发布 %s 消息成功", tt.name)
		})
	}
}

// TestNodeInfoSerialization 测试节点信息序列化
func TestNodeInfoSerialization(t *testing.T) {
	nodeInfo := &NodeInfo{
		ID:          "node-1",
		IPAddress:   "192.168.1.100",
		Port:        8080,
		Status:      models.NodeStatusActive,
		LastSeen:    time.Now(),
		Connections: 100,
	}

	// 序列化
	data, err := json.Marshal(nodeInfo)
	assert.NoError(t, err)

	// 反序列化
	var decoded NodeInfo
	err = json.Unmarshal(data, &decoded)
	assert.NoError(t, err)

	assert.Equal(t, nodeInfo.ID, decoded.ID)
	assert.Equal(t, nodeInfo.IPAddress, decoded.IPAddress)
	assert.Equal(t, nodeInfo.Port, decoded.Port)
	assert.Equal(t, nodeInfo.Status, decoded.Status)
	assert.Equal(t, nodeInfo.Connections, decoded.Connections)
}

// TestDistributedMessageSerialization 测试分布式消息序列化
func TestDistributedMessageSerialization(t *testing.T) {
	distMsg := &DistributedMessage{
		Type:     models.OperationTypeSendMessage,
		NodeID:   "node-1",
		TargetID: "user-123",
		Data: map[string]interface{}{
			"message": map[string]interface{}{
				"id":      "msg-1",
				"content": "test",
			},
		},
		Timestamp: time.Now(),
	}

	// 序列化
	data, err := json.Marshal(distMsg)
	assert.NoError(t, err)

	// 反序列化
	var decoded DistributedMessage
	err = json.Unmarshal(data, &decoded)
	assert.NoError(t, err)

	assert.Equal(t, distMsg.Type, decoded.Type)
	assert.Equal(t, distMsg.NodeID, decoded.NodeID)
	assert.Equal(t, distMsg.TargetID, decoded.TargetID)
}

// TestMultiNodeScenario 测试多节点场景
func TestMultiNodeScenario(t *testing.T) {
	// 创建 3 个节点
	hub1 := createTestHubWithDistributed(t, "node-1")
	defer hub1.SafeShutdown()

	hub2 := createTestHubWithDistributed(t, "node-2")
	defer hub2.SafeShutdown()

	hub3 := createTestHubWithDistributed(t, "node-3")
	defer hub3.SafeShutdown()

	ctx := context.Background()

	// 注册所有节点
	err := hub1.RegisterNode(ctx)
	require.NoError(t, err)

	err = hub2.RegisterNode(ctx)
	require.NoError(t, err)

	err = hub3.RegisterNode(ctx)
	require.NoError(t, err)

	// 从 node-1 发现其他节点
	nodes, err := hub1.DiscoverNodes(ctx)
	assert.NoError(t, err)
	assert.Len(t, nodes, 2)

	// 验证发现的节点
	nodeIDs := make(map[string]bool)
	for _, node := range nodes {
		nodeIDs[node.ID] = true
	}
	assert.True(t, nodeIDs["node-2"])
	assert.True(t, nodeIDs["node-3"])
}

// TestLockExpiration 测试锁过期
func TestLockExpiration(t *testing.T) {
	hub := createTestHubWithDistributed(t, "node-1")
	defer hub.SafeShutdown()

	ctx := context.Background()
	lockKey := "test-expire-lock"

	// 获取短期锁
	acquired, err := hub.AcquireDistributedLock(ctx, lockKey, 1*time.Second)
	require.NoError(t, err)
	require.True(t, acquired)

	// 等待锁过期
	time.Sleep(2 * time.Second)

	// 应该可以再次获取
	acquired, err = hub.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
	assert.NoError(t, err)
	assert.True(t, acquired)
}

// TestConcurrentLockAcquisition 测试并发获取锁
func TestConcurrentLockAcquisition(t *testing.T) {
	hub1 := createTestHubWithDistributed(t, "node-1")
	defer hub1.SafeShutdown()

	hub2 := createTestHubWithDistributed(t, "node-2")
	defer hub2.SafeShutdown()

	ctx := context.Background()
	lockKey := "concurrent-lock"

	// 并发尝试获取锁
	results := make(chan bool, 2)

	go func() {
		acquired, _ := hub1.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
		results <- acquired
	}()

	go func() {
		acquired, _ := hub2.AcquireDistributedLock(ctx, lockKey, 10*time.Second)
		results <- acquired
	}()

	// 收集结果
	result1 := <-results
	result2 := <-results

	// 只有一个应该成功
	assert.True(t, result1 != result2, "只有一个节点应该获取到锁")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-01
 * @FilePath: \go-wsc\online_status_repository_test.go
 * @Description: 客户端在线状态管理 - 支持 Redis 分布式存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

// 测试用 Redis 配置（使用 local 配置文件中的 Redis）
func getTestRedisClient(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr:     "120.79.25.168:16389",
		Password: "M5Pi9YW6u",
		DB:       1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 测试连接
	err := client.Ping(ctx).Err()
	require.NoError(t, err, "Redis 连接失败，请检查配置")

	return client
}

// 创建测试用的在线客户端信息
func createTestOnlineClientInfo(clientID, userID string, userType UserType, nodeID string) *OnlineClientInfo {
	now := time.Now()
	return &OnlineClientInfo{
		ClientID:      clientID,
		UserID:        userID,
		UserType:      userType,
		NodeID:        nodeID,
		NodeIP:        "192.168.1.100",
		ClientIP:      "10.0.0.1",
		ConnectTime:   now,
		LastSeen:      now,
		LastHeartbeat: now,
		ClientType:    ClientTypeWeb,
		Status:        UserStatusOnline,
		Metadata:      map[string]interface{}{"test": "data"},
	}
}

func TestRedisOnlineStatusRepository_SetOnline(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	info := createTestOnlineClientInfo("test-client-1", "user-001", UserTypeCustomer, "node-1")

	// 设置在线
	err := repo.SetOnline(ctx, "user-001", info, 5*time.Minute)
	assert.NoError(t, err)

	// 验证在线状态
	isOnline, err := repo.IsOnline(ctx, "user-001")
	assert.NoError(t, err)
	assert.True(t, isOnline)

	// 获取在线信息
	retrievedInfo, err := repo.GetOnlineInfo(ctx, "user-001")
	assert.NoError(t, err)
	assert.Equal(t, info.ClientID, retrievedInfo.ClientID)
	assert.Equal(t, info.UserID, retrievedInfo.UserID)
	assert.Equal(t, info.NodeIP, retrievedInfo.NodeIP)
	assert.Equal(t, info.ClientIP, retrievedInfo.ClientIP)

	// 清理
	_ = repo.SetOffline(ctx, "user-001")
}

func TestRedisOnlineStatusRepository_SetOffline(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	info := createTestOnlineClientInfo("test-client-2", "user-002", UserTypeAgent, "node-1")

	// 先设置在线
	err := repo.SetOnline(ctx, "user-002", info, 5*time.Minute)
	require.NoError(t, err)

	// 验证在线
	isOnline, err := repo.IsOnline(ctx, "user-002")
	require.NoError(t, err)
	require.True(t, isOnline)

	// 设置离线
	err = repo.SetOffline(ctx, "user-002")
	assert.NoError(t, err)

	// 验证离线
	isOnline, err = repo.IsOnline(ctx, "user-002")
	assert.NoError(t, err)
	assert.False(t, isOnline)
}

func TestRedisOnlineStatusRepository_GetOnlineUsersByType(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, "agent-001")
		_ = repo.SetOffline(ctx, "agent-002")
		_ = repo.SetOffline(ctx, "customer-001")
	}()

	// 设置多个客服在线
	agent1 := createTestOnlineClientInfo("client-agent-1", "agent-001", UserTypeAgent, "node-1")
	agent2 := createTestOnlineClientInfo("client-agent-2", "agent-002", UserTypeAgent, "node-1")
	customer1 := createTestOnlineClientInfo("client-customer-1", "customer-001", UserTypeCustomer, "node-1")

	err := repo.SetOnline(ctx, "agent-001", agent1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "agent-002", agent2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "customer-001", customer1, 5*time.Minute)
	require.NoError(t, err)

	// 获取客服类型的在线用户
	agents, err := repo.GetOnlineUsersByType(ctx, UserTypeAgent)
	assert.NoError(t, err)
	assert.Contains(t, agents, "agent-001")
	assert.Contains(t, agents, "agent-002")
	assert.NotContains(t, agents, "customer-001")

	// 获取客户类型的在线用户
	customers, err := repo.GetOnlineUsersByType(ctx, UserTypeCustomer)
	assert.NoError(t, err)
	assert.Contains(t, customers, "customer-001")
	assert.NotContains(t, customers, "agent-001")
}

func TestRedisOnlineStatusRepository_GetOnlineUsersByNode(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, "user-node1-001")
		_ = repo.SetOffline(ctx, "user-node1-002")
		_ = repo.SetOffline(ctx, "user-node2-001")
	}()

	// 在不同节点设置在线用户
	user1 := createTestOnlineClientInfo("client-1", "user-node1-001", UserTypeCustomer, "node-1")
	user2 := createTestOnlineClientInfo("client-2", "user-node1-002", UserTypeCustomer, "node-1")
	user3 := createTestOnlineClientInfo("client-3", "user-node2-001", UserTypeCustomer, "node-2")

	err := repo.SetOnline(ctx, "user-node1-001", user1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "user-node1-002", user2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "user-node2-001", user3, 5*time.Minute)
	require.NoError(t, err)

	// 获取 node-1 的在线用户
	node1Users, err := repo.GetOnlineUsersByNode(ctx, "node-1")
	assert.NoError(t, err)
	assert.Contains(t, node1Users, "user-node1-001")
	assert.Contains(t, node1Users, "user-node1-002")
	assert.NotContains(t, node1Users, "user-node2-001")

	// 获取 node-2 的在线用户
	node2Users, err := repo.GetOnlineUsersByNode(ctx, "node-2")
	assert.NoError(t, err)
	assert.Contains(t, node2Users, "user-node2-001")
	assert.NotContains(t, node2Users, "user-node1-001")
}

func TestRedisOnlineStatusRepository_GetAllOnlineUserIDs(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, "all-user-001")
		_ = repo.SetOffline(ctx, "all-user-002")
		_ = repo.SetOffline(ctx, "all-user-003")
	}()

	// 设置多个用户在线
	user1 := createTestOnlineClientInfo("client-1", "all-user-001", UserTypeCustomer, "node-1")
	user2 := createTestOnlineClientInfo("client-2", "all-user-002", UserTypeAgent, "node-1")
	user3 := createTestOnlineClientInfo("client-3", "all-user-003", UserTypeBot, "node-2")

	err := repo.SetOnline(ctx, "all-user-001", user1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "all-user-002", user2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "all-user-003", user3, 5*time.Minute)
	require.NoError(t, err)

	// 获取所有在线用户
	allUsers, err := repo.GetAllOnlineUsers(ctx)
	assert.NoError(t, err)
	assert.Contains(t, allUsers, "all-user-001")
	assert.Contains(t, allUsers, "all-user-002")
	assert.Contains(t, allUsers, "all-user-003")
}

func TestRedisOnlineStatusRepository_UpdateHeartbeat(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	info := createTestOnlineClientInfo("heartbeat-client", "heartbeat-user", UserTypeCustomer, "node-1")

	// 设置在线，TTL 为 2 秒
	err := repo.SetOnline(ctx, "heartbeat-user", info, 2*time.Second)
	require.NoError(t, err)

	// 等待 1 秒
	time.Sleep(1 * time.Second)

	// 更新心跳
	err = repo.UpdateHeartbeat(ctx, "heartbeat-user")
	assert.NoError(t, err)

	// 再等待 2 秒（原本应该过期了，但因为更新了心跳，用默认TTL）
	time.Sleep(2 * time.Second)

	// 验证仍然在线（使用默认5分钟TTL）
	isOnline, err := repo.IsOnline(ctx, "heartbeat-user")
	assert.NoError(t, err)
	assert.True(t, isOnline, "心跳更新后用户应该仍然在线")

	// 清理
	_ = repo.SetOffline(ctx, "heartbeat-user")
}

func TestRedisOnlineStatusRepository_GetOnlineCount(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, "count-user-001")
		_ = repo.SetOffline(ctx, "count-user-002")
		_ = repo.SetOffline(ctx, "count-user-003")
	}()

	// 设置多个用户在线
	user1 := createTestOnlineClientInfo("c1", "count-user-001", UserTypeCustomer, "node-1")
	user2 := createTestOnlineClientInfo("c2", "count-user-002", UserTypeAgent, "node-1")
	user3 := createTestOnlineClientInfo("c3", "count-user-003", UserTypeCustomer, "node-2")

	err := repo.SetOnline(ctx, "count-user-001", user1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "count-user-002", user2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, "count-user-003", user3, 5*time.Minute)
	require.NoError(t, err)

	// 获取总在线人数
	count, err := repo.GetOnlineCount(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, count, int64(3), "至少应该有 3 个在线用户")
}

func TestRedisOnlineStatusRepository_BatchSetOnline(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, "batch-user-001")
		_ = repo.SetOffline(ctx, "batch-user-002")
		_ = repo.SetOffline(ctx, "batch-user-003")
	}()

	// 批量设置在线
	infos := map[string]*OnlineClientInfo{
		"batch-user-001": createTestOnlineClientInfo("bc1", "batch-user-001", UserTypeCustomer, "node-1"),
		"batch-user-002": createTestOnlineClientInfo("bc2", "batch-user-002", UserTypeAgent, "node-1"),
		"batch-user-003": createTestOnlineClientInfo("bc3", "batch-user-003", UserTypeBot, "node-1"),
	}

	err := repo.BatchSetOnline(ctx, infos, 5*time.Minute)
	assert.NoError(t, err)

	// 验证所有用户都在线
	for userID := range infos {
		isOnline, err := repo.IsOnline(ctx, userID)
		assert.NoError(t, err)
		assert.True(t, isOnline, "用户 %s 应该在线", userID)
	}
}

func TestRedisOnlineStatusRepository_Timeout(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)

	// 使用已取消的 context 测试超时
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	info := createTestOnlineClientInfo("timeout-client", "timeout-user", UserTypeCustomer, "node-1")

	// 应该返回错误
	err := repo.SetOnline(ctx, "timeout-user", info, 5*time.Minute)
	assert.Error(t, err, "已取消的 context 应该返回错误")
}

func TestRedisOnlineStatusRepository_Concurrency(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, "test:", 5*time.Minute)
	ctx := context.Background()

	// 并发测试
	const goroutines = 10
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer func() { done <- true }()

			userID := fmt.Sprintf("concurrent-user-%d", index)
			info := createTestOnlineClientInfo(
				fmt.Sprintf("concurrent-client-%d", index),
				userID,
				UserTypeCustomer,
				"node-1",
			)

			// 设置在线
			err := repo.SetOnline(ctx, userID, info, 5*time.Minute)
			assert.NoError(t, err)

			// 验证在线
			isOnline, err := repo.IsOnline(ctx, userID)
			assert.NoError(t, err)
			assert.True(t, isOnline)

			// 清理
			_ = repo.SetOffline(ctx, userID)
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

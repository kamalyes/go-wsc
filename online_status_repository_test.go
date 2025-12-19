/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-01 17:15:17
 * @FilePath: \go-wsc\online_status_repository_test.go
 * @Description: 客户端在线状态管理 - 支持 Redis 分布式存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	testUser001       = "user-001"
	testAgent001      = "agent-001"
	testAgent002      = "agent-002"
	testUserNode1001  = "user-node1-001"
	testUserNode2001  = "user-node2-001"
	testUserNode1002  = "user-node1-002"
	testNode1         = "node-1"
	testNode2         = "node-2"
	testAllUser001    = "all-user-001"
	testAllUser002    = "all-user-002"
	testAllUser003    = "all-user-003"
	testHeartbeatUser = "heartbeat-user"
	testCountUser001  = "count-user-001"
	testCountUser002  = "count-user-002"
	testCountUser003  = "count-user-003"
	testBatchUser001  = "batch-user-001"
	testBatchUser002  = "batch-user-002"
	testBatchUser003  = "batch-user-003"
	testCustomer001   = "customer-001"
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

	// 清空测试数据
	client.FlushDB(ctx)

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

func TestRedisOnlineStatusRepositorySetOnline(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	info := createTestOnlineClientInfo("test-client-1", testUser001, UserTypeCustomer, testNode1)

	// 设置在线
	err := repo.SetOnline(ctx, testUser001, info, 5*time.Minute)
	assert.NoError(t, err)

	// 验证在线状态
	isOnline, err := repo.IsOnline(ctx, testUser001)
	assert.NoError(t, err)
	assert.True(t, isOnline)

	// 获取在线信息
	retrievedInfo, err := repo.GetOnlineInfo(ctx, testUser001)
	assert.NoError(t, err)
	assert.Equal(t, info.ClientID, retrievedInfo.ClientID)
	assert.Equal(t, info.UserID, retrievedInfo.UserID)
	assert.Equal(t, info.NodeIP, retrievedInfo.NodeIP)
	assert.Equal(t, info.ClientIP, retrievedInfo.ClientIP)

	// 清理
	_ = repo.SetOffline(ctx, testUser001)
}

func TestRedisOnlineStatusRepositorySetOffline(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	info := createTestOnlineClientInfo("test-client-2", testCountUser002, UserTypeAgent, testNode1)

	// 先设置在线
	err := repo.SetOnline(ctx, testCountUser002, info, 5*time.Minute)
	require.NoError(t, err)

	// 验证在线
	isOnline, err := repo.IsOnline(ctx, testCountUser002)
	require.NoError(t, err)
	require.True(t, isOnline)

	// 设置离线
	err = repo.SetOffline(ctx, testCountUser002)
	assert.NoError(t, err)

	// 验证离线
	isOnline, err = repo.IsOnline(ctx, testCountUser002)
	assert.NoError(t, err)
	assert.False(t, isOnline)
}

func TestRedisOnlineStatusRepositoryGetOnlineUsersByType(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, testAgent001)
		_ = repo.SetOffline(ctx, testAgent002)
		_ = repo.SetOffline(ctx, testCustomer001)
	}()

	// 设置多个客服在线
	agent1 := createTestOnlineClientInfo("client-agent-1", testAgent001, UserTypeAgent, testNode1)
	agent2 := createTestOnlineClientInfo("client-agent-2", testAgent002, UserTypeAgent, testNode1)
	customer1 := createTestOnlineClientInfo("client-customer-1", testCustomer001, UserTypeCustomer, testNode1)

	err := repo.SetOnline(ctx, testAgent001, agent1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testAgent002, agent2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testCustomer001, customer1, 5*time.Minute)
	require.NoError(t, err)

	// 获取客服类型的在线用户
	agents, err := repo.GetOnlineUsersByType(ctx, UserTypeAgent)
	assert.NoError(t, err)
	assert.Contains(t, agents, testAgent001)
	assert.Contains(t, agents, testAgent002)
	assert.NotContains(t, agents, testCustomer001)

	// 获取客户类型的在线用户
	customers, err := repo.GetOnlineUsersByType(ctx, UserTypeCustomer)
	assert.NoError(t, err)
	assert.Contains(t, customers, testCustomer001)
	assert.NotContains(t, customers, testAgent001)
}

func TestRedisOnlineStatusRepositoryGetOnlineUsersByNode(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, testUserNode1001)
		_ = repo.SetOffline(ctx, testUserNode1002)
		_ = repo.SetOffline(ctx, testUserNode2001)
	}()

	// 在不同节点设置在线用户
	user1 := createTestOnlineClientInfo("client-1", testUserNode1001, UserTypeCustomer, testNode1)
	user2 := createTestOnlineClientInfo("client-2", testUserNode1002, UserTypeCustomer, testNode1)
	user3 := createTestOnlineClientInfo("client-3", testUserNode2001, UserTypeCustomer, testNode2)

	err := repo.SetOnline(ctx, testUserNode1001, user1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testUserNode1002, user2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testUserNode2001, user3, 5*time.Minute)
	require.NoError(t, err)

	// 获取 node-1 的在线用户
	node1Users, err := repo.GetOnlineUsersByNode(ctx, testNode1)
	assert.NoError(t, err)
	assert.Contains(t, node1Users, testUserNode1001)
	assert.Contains(t, node1Users, testUserNode1002)
	assert.NotContains(t, node1Users, testUserNode2001)

	// 获取 node-2 的在线用户
	node2Users, err := repo.GetOnlineUsersByNode(ctx, testNode2)
	assert.NoError(t, err)
	assert.Contains(t, node2Users, testUserNode2001)
	assert.NotContains(t, node2Users, testUserNode1001)
}

func TestRedisOnlineStatusRepositoryGetAllOnlineUserIDs(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, testAllUser001)
		_ = repo.SetOffline(ctx, testAllUser002)
		_ = repo.SetOffline(ctx, testAllUser003)
	}()

	// 设置多个用户在线
	user1 := createTestOnlineClientInfo("client-1", testAllUser001, UserTypeCustomer, testNode1)
	user2 := createTestOnlineClientInfo("client-2", testAllUser002, UserTypeAgent, testNode1)
	user3 := createTestOnlineClientInfo("client-3", testAllUser003, UserTypeBot, testNode2)

	err := repo.SetOnline(ctx, testAllUser001, user1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testAllUser002, user2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testAllUser003, user3, 5*time.Minute)
	require.NoError(t, err)

	// 获取所有在线用户
	allUsers, err := repo.GetAllOnlineUsers(ctx)
	assert.NoError(t, err)
	assert.Contains(t, allUsers, testAllUser001)
	assert.Contains(t, allUsers, testAllUser002)
	assert.Contains(t, allUsers, testAllUser003)
}

func TestRedisOnlineStatusRepositoryUpdateHeartbeat(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	info := createTestOnlineClientInfo("heartbeat-client", testHeartbeatUser, UserTypeCustomer, testNode1)

	// 设置在线，TTL 为 2 秒
	err := repo.SetOnline(ctx, testHeartbeatUser, info, 2*time.Second)
	require.NoError(t, err)

	// 等待 1 秒
	time.Sleep(1 * time.Second)

	// 更新心跳
	err = repo.UpdateHeartbeat(ctx, testHeartbeatUser)
	assert.NoError(t, err)

	// 再等待 2 秒（原本应该过期了，但因为更新了心跳，用默认TTL）
	time.Sleep(2 * time.Second)

	// 验证仍然在线（使用默认5分钟TTL）
	isOnline, err := repo.IsOnline(ctx, testHeartbeatUser)
	assert.NoError(t, err)
	assert.True(t, isOnline, "心跳更新后用户应该仍然在线")

	// 清理
	_ = repo.SetOffline(ctx, testHeartbeatUser)
}

func TestRedisOnlineStatusRepositoryGetOnlineCount(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, testCountUser001)
		_ = repo.SetOffline(ctx, testCountUser002)
		_ = repo.SetOffline(ctx, testCountUser003)
	}()

	// 设置多个用户在线
	user1 := createTestOnlineClientInfo("c1", testCountUser001, UserTypeCustomer, testNode1)
	user2 := createTestOnlineClientInfo("c2", testCountUser002, UserTypeAgent, testNode1)
	user3 := createTestOnlineClientInfo("c3", testCountUser003, UserTypeCustomer, testNode2)

	err := repo.SetOnline(ctx, testCountUser001, user1, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testCountUser002, user2, 5*time.Minute)
	require.NoError(t, err)
	err = repo.SetOnline(ctx, testCountUser003, user3, 5*time.Minute)
	require.NoError(t, err)

	// 获取总在线人数
	count, err := repo.GetOnlineCount(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, count, int64(3), "至少应该有 3 个在线用户")
}

func TestRedisOnlineStatusRepositoryBatchSetOnline(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
	ctx := context.Background()

	// 清理测试数据
	defer func() {
		_ = repo.SetOffline(ctx, testBatchUser001)
		_ = repo.SetOffline(ctx, testBatchUser002)
		_ = repo.SetOffline(ctx, testBatchUser003)
	}()

	// 批量设置在线
	infos := map[string]*OnlineClientInfo{
		testBatchUser001: createTestOnlineClientInfo("bc1", testBatchUser001, UserTypeCustomer, testNode1),
		testBatchUser002: createTestOnlineClientInfo("bc2", testBatchUser002, UserTypeAgent, testNode1),
		testBatchUser003: createTestOnlineClientInfo("bc3", testBatchUser003, UserTypeBot, testNode1),
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

func TestRedisOnlineStatusRepositoryTimeout(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})

	// 使用已取消的 context 测试超时
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	info := createTestOnlineClientInfo("timeout-client", "timeout-user", UserTypeCustomer, testNode1)

	// 应该返回错误
	err := repo.SetOnline(ctx, "timeout-user", info, 5*time.Minute)
	assert.Error(t, err, "已取消的 context 应该返回错误")
}

func TestRedisOnlineStatusRepositoryConcurrency(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	repo := NewRedisOnlineStatusRepository(client, &wscconfig.OnlineStatus{KeyPrefix: "test:", TTL: 5 * time.Minute})
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
				testNode1,
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

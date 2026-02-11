/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:57:38
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
	"github.com/kamalyes/go-toolbox/pkg/random"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisOnlineStatusRepositorySetOnline(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	client := createTestClientWithIDGen(UserTypeCustomer)

	// 设置在线
	err := repo.SetOnline(ctx, client)
	assert.NoError(t, err)

	// 验证在线状态
	isOnline, err := repo.IsOnline(ctx, client.UserID)
	assert.NoError(t, err)
	assert.True(t, isOnline)

	// 获取在线信息
	retrievedInfo, err := repo.GetOnlineInfo(ctx, client.UserID)
	assert.NoError(t, err)
	require.NotNil(t, retrievedInfo, "在线信息不应为空")
	assert.Equal(t, client.ID, retrievedInfo.ID)
	assert.Equal(t, client.UserID, retrievedInfo.UserID)
	assert.Equal(t, client.NodeID, retrievedInfo.NodeID)
	assert.Equal(t, client.ClientIP, retrievedInfo.ClientIP)

	// 清理
	_ = repo.SetOffline(ctx, client.UserID)
}

func TestRedisOnlineStatusRepositorySetOffline(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	client := createTestClientWithIDGen(UserTypeAgent)

	// 先设置在线
	err := repo.SetOnline(ctx, client)
	require.NoError(t, err)

	// 验证在线
	isOnline, err := repo.IsOnline(ctx, client.UserID)
	require.NoError(t, err)
	require.True(t, isOnline)

	// 设置离线
	err = repo.SetOffline(ctx, client.UserID)
	assert.NoError(t, err)

	// 验证离线
	isOnline, err = repo.IsOnline(ctx, client.UserID)
	assert.NoError(t, err)
	assert.False(t, isOnline)
}

func TestRedisOnlineStatusRepositoryUpdateHeartbeat(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 2 * time.Second})
	ctx := context.Background()

	info := createTestClientWithIDGen(UserTypeCustomer)

	// 设置在线，TTL 为 2 秒
	err := repo.SetOnline(ctx, info)
	require.NoError(t, err)

	// 等待 1 秒
	time.Sleep(200 * time.Millisecond)

	// 更新心跳
	err = repo.UpdateHeartbeat(ctx, info.UserID)
	assert.NoError(t, err)

	// 再等待 2 秒（原本应该过期了，但因为更新了心跳，用默认TTL）
	time.Sleep(300 * time.Millisecond)

	// 验证仍然在线（使用默认5分钟TTL）
	isOnline, err := repo.IsOnline(ctx, info.UserID)
	assert.NoError(t, err)
	assert.True(t, isOnline, "心跳更新后用户应该仍然在线")

	// 清理
	_ = repo.SetOffline(ctx, info.UserID)
}

func TestRedisOnlineStatusRepositoryTimeout(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})

	// 使用已取消的 context 测试超时
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	info := createTestClientWithIDGen(UserTypeAgent)

	// 应该返回错误
	err := repo.SetOnline(ctx, info)
	assert.Error(t, err, "已取消的 context 应该返回错误")
}

func TestRedisOnlineStatusRepositoryConcurrency(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 并发测试
	const goroutines = 10
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer func() { done <- true }()

			client := createTestClientWithIDGen(UserTypeCustomer)

			// 设置在线
			err := repo.SetOnline(ctx, client)
			assert.NoError(t, err)

			// 验证在线
			isOnline, err := repo.IsOnline(ctx, client.UserID)
			assert.NoError(t, err)
			assert.True(t, isOnline)

			// 清理
			_ = repo.SetOffline(ctx, client.UserID)
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func TestRedisOnlineStatusRepositoryGetAllOnlineUsers(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 创建多个在线用户
	clients := make([]*Client, 5)
	for i := range 5 {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		err := repo.SetOnline(ctx, clients[i])
		require.NoError(t, err)
	}

	// 获取所有在线用户
	onlineUsers, err := repo.GetAllOnlineUsers(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(onlineUsers), 5, "应至少有5个在线用户")

	// 验证所有用户都在列表中
	userIDMap := make(map[string]bool)
	for _, userID := range onlineUsers {
		userIDMap[userID] = true
	}
	for _, client := range clients {
		assert.True(t, userIDMap[client.UserID], "用户 %s 应在在线列表中", client.UserID)
	}

	// 清理
	for _, client := range clients {
		_ = repo.SetOffline(ctx, client.UserID)
	}
}

func TestRedisOnlineStatusRepositoryGetOnlineUsersByNode(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	nodeID1 := random.FRandAlphaString(30)
	nodeID2 := random.FRandAlphaString(30)

	// 节点1的用户
	client1 := createTestClientWithIDGen(UserTypeCustomer)
	client1.NodeID = nodeID1
	err := repo.SetOnline(ctx, client1)
	require.NoError(t, err)

	client2 := createTestClientWithIDGen(UserTypeAgent)
	client2.NodeID = nodeID1
	err = repo.SetOnline(ctx, client2)
	require.NoError(t, err)

	// 节点2的用户
	client3 := createTestClientWithIDGen(UserTypeCustomer)
	client3.NodeID = nodeID2
	err = repo.SetOnline(ctx, client3)
	require.NoError(t, err)

	// 获取节点1的用户
	node1Users, err := repo.GetOnlineUsersByNode(ctx, nodeID1)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(node1Users), "节点1应有2个用户")

	// 获取节点2的用户
	node2Users, err := repo.GetOnlineUsersByNode(ctx, nodeID2)
	assert.NoError(t, err)
	assert.Equal(t, 1, len(node2Users), "节点2应有1个用户")

	// 清理
	_ = repo.SetOffline(ctx, client1.UserID)
	_ = repo.SetOffline(ctx, client2.UserID)
	_ = repo.SetOffline(ctx, client3.UserID)
}

func TestRedisOnlineStatusRepositoryGetOnlineCount(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 初始计数
	initialCount, err := repo.GetOnlineCount(ctx)
	require.NoError(t, err)

	// 添加用户
	clients := make([]*Client, 3)
	for i := range 3 {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		err = repo.SetOnline(ctx, clients[i])
		require.NoError(t, err)
	}

	// 验证计数增加
	newCount, err := repo.GetOnlineCount(ctx)
	assert.NoError(t, err)
	assert.Equal(t, initialCount+3, newCount, "在线用户数应增加3")

	// 清理
	for _, client := range clients {
		_ = repo.SetOffline(ctx, client.UserID)
	}
}

func TestRedisOnlineStatusRepositoryBatchSetOnline(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 创建多个客户端
	clients := make(map[string]*Client)
	for range 5 {
		client := createTestClientWithIDGen(UserTypeCustomer)
		clients[client.UserID] = client
	}

	// 批量设置在线
	err := repo.BatchSetOnline(ctx, clients)
	assert.NoError(t, err)

	// 验证所有用户都在线
	for userID := range clients {
		isOnline, err := repo.IsOnline(ctx, userID)
		assert.NoError(t, err)
		assert.True(t, isOnline, "用户 %s 应该在线", userID)
	}

	// 清理
	userIDs := make([]string, 0, len(clients))
	for userID := range clients {
		userIDs = append(userIDs, userID)
	}
	_ = repo.BatchSetOffline(ctx, userIDs)
}

func TestRedisOnlineStatusRepositoryBatchSetOffline(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 创建多个在线用户
	userIDs := make([]string, 5)
	for i := range 5 {
		client := createTestClientWithIDGen(UserTypeAgent)
		userIDs[i] = client.UserID
		err := repo.SetOnline(ctx, client)
		require.NoError(t, err)
	}

	// 验证都在线
	for _, userID := range userIDs {
		isOnline, err := repo.IsOnline(ctx, userID)
		require.NoError(t, err)
		require.True(t, isOnline)
	}

	// 批量设置离线
	err := repo.BatchSetOffline(ctx, userIDs)
	assert.NoError(t, err)

	// 验证都离线
	for _, userID := range userIDs {
		isOnline, err := repo.IsOnline(ctx, userID)
		assert.NoError(t, err)
		assert.False(t, isOnline, "用户 %s 应该离线", userID)
	}
}

func TestRedisOnlineStatusRepositoryGetOnlineUsersByType(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 创建不同类型的用户
	customerClients := make([]*Client, 3)
	for i := range 3 {
		customerClients[i] = createTestClientWithIDGen(UserTypeCustomer)
		err := repo.SetOnline(ctx, customerClients[i])
		require.NoError(t, err)
	}

	agentClients := make([]*Client, 2)
	for i := range 2 {
		agentClients[i] = createTestClientWithIDGen(UserTypeAgent)
		err := repo.SetOnline(ctx, agentClients[i])
		require.NoError(t, err)
	}

	// 获取客服类型用户
	customerUsers, err := repo.GetOnlineUsersByType(ctx, UserTypeCustomer)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(customerUsers), 3, "应至少有3个客服用户在线")

	// 获取坐席类型用户
	agentUsers, err := repo.GetOnlineUsersByType(ctx, UserTypeAgent)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(agentUsers), 2, "应至少有2个坐席用户在线")

	// 清理
	for _, client := range customerClients {
		_ = repo.SetOffline(ctx, client.UserID)
	}
	for _, client := range agentClients {
		_ = repo.SetOffline(ctx, client.UserID)
	}
}

func TestRedisOnlineStatusRepositorySetAndGetUserNode(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 创建测试客户端（SetOnline 会自动设置节点映射）
	client := createTestClientWithIDGen(UserTypeCustomer)
	client.NodeID = random.FRandAlphaString(30)

	// 通过 SetOnline 设置用户在线（会自动设置节点映射）
	err := repo.SetOnline(ctx, client)
	assert.NoError(t, err)

	// 获取用户节点
	retrievedNodeID, err := repo.GetUserNode(ctx, client.UserID)
	assert.NoError(t, err)
	assert.Equal(t, client.NodeID, retrievedNodeID, "应返回正确的节点ID")

	// 测试不存在的用户
	_, err = repo.GetUserNode(ctx, "non-existent-user")
	assert.Error(t, err, "不存在的用户应返回错误")

	// 清理
	_ = repo.SetOffline(ctx, client.UserID)
}

func TestRedisOnlineStatusRepositoryGetNodeUsers(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	nodeID := random.FRandAlphaString(30)

	// 创建多个用户并设置到同一节点
	clients := make([]*Client, 4)
	for i := range 4 {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		clients[i].NodeID = nodeID
		err := repo.SetOnline(ctx, clients[i])
		require.NoError(t, err)
	}

	// 获取节点的所有用户
	nodeUsers, err := repo.GetNodeUsers(ctx, nodeID)
	assert.NoError(t, err)
	assert.Equal(t, 4, len(nodeUsers), "节点应有4个用户")

	// 验证所有用户都在列表中
	userIDMap := make(map[string]bool)
	for _, userID := range nodeUsers {
		userIDMap[userID] = true
	}
	for _, client := range clients {
		assert.True(t, userIDMap[client.UserID], "用户 %s 应在节点用户列表中", client.UserID)
	}

	// 清理
	for _, client := range clients {
		_ = repo.SetOffline(ctx, client.UserID)
	}
}

func TestRedisOnlineStatusRepositoryCleanupExpired(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	// 使用较短的TTL便于测试
	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 1 * time.Second})
	ctx := context.Background()

	// 创建在线用户
	client := createTestClientWithIDGen(UserTypeCustomer)
	err := repo.SetOnline(ctx, client)
	require.NoError(t, err)

	// 验证在线
	isOnline, err := repo.IsOnline(ctx, client.UserID)
	require.NoError(t, err)
	require.True(t, isOnline)

	// 等待过期
	time.Sleep(2 * time.Second)

	// 调用清理（虽然Redis会自动过期，但测试清理方法）
	count, err := repo.CleanupExpired(ctx)
	assert.NoError(t, err)
	t.Logf("清理了 %d 个过期条目", count)

	// 验证已离线
	isOnline, err = repo.IsOnline(ctx, client.UserID)
	assert.NoError(t, err)
	assert.False(t, isOnline, "用户应该已过期离线")
}

func TestRedisOnlineStatusRepositoryGetOnlineInfoNotFound(t *testing.T) {
	redisClient := getTestRedisClient(t)
	redisKeyPrefix := getTestIDGenerator().GenerateRequestID()

	repo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", redisKeyPrefix), TTL: 5 * time.Minute})
	ctx := context.Background()

	// 获取不存在的用户信息
	info, err := repo.GetOnlineInfo(ctx, "non-existent-user")
	assert.Error(t, err, "不存在的用户应返回错误")
	assert.Nil(t, info, "不存在的用户应返回nil")
}

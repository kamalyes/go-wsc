/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 12:00:00
 * @FilePath: \go-wsc\online_status_repository_test.go
 * @Description: 客户端在线状态管理测试 - 支持多设备
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

// testRepoSetup 测试仓库设置辅助结构
type testRepoSetup struct {
	repo   OnlineStatusRepository
	ctx    context.Context
	prefix string
}

// newTestRepoSetup 创建测试仓库设置
func newTestRepoSetup(t *testing.T, ttl time.Duration) *testRepoSetup {
	redisClient := getTestRedisClient(t)
	prefix := getTestIDGenerator().GenerateRequestID()

	return &testRepoSetup{
		repo:   NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{KeyPrefix: fmt.Sprintf("%s:", prefix), TTL: ttl}),
		ctx:    context.Background(),
		prefix: prefix,
	}
}

// cleanup 清理测试数据
func (s *testRepoSetup) cleanup(clients ...*Client) {
	for _, client := range clients {
		_ = s.repo.SetOffline(s.ctx, client.UserID)
	}
}

// assertUserOnline 断言用户在线
func (s *testRepoSetup) assertUserOnline(t *testing.T, userID string, expected bool) {
	isOnline, err := s.repo.IsUserOnline(s.ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, expected, isOnline)
}

func TestRedisOnlineStatusRepositorySetClientOnline(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	client := createTestClientWithIDGen(UserTypeCustomer)
	defer setup.cleanup(client)

	err := setup.repo.SetClientOnline(setup.ctx, client)
	assert.NoError(t, err)

	setup.assertUserOnline(t, client.UserID, true)

	clients, err := setup.repo.GetUserClients(setup.ctx, client.UserID)
	assert.NoError(t, err)
	require.NotEmpty(t, clients)
	assert.Equal(t, client.ID, clients[0].ID)
	assert.Equal(t, client.UserID, clients[0].UserID)
	assert.Equal(t, client.NodeID, clients[0].NodeID)
	assert.Equal(t, client.ClientIP, clients[0].ClientIP)
}

func TestRedisOnlineStatusRepositorySetClientOffline(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	client := createTestClientWithIDGen(UserTypeAgent)

	err := setup.repo.SetClientOnline(setup.ctx, client)
	require.NoError(t, err)

	setup.assertUserOnline(t, client.UserID, true)

	err = setup.repo.SetClientOffline(setup.ctx, client)
	assert.NoError(t, err)

	setup.assertUserOnline(t, client.UserID, false)
}

func TestRedisOnlineStatusRepositoryUpdateClientHeartbeat(t *testing.T) {
	setup := newTestRepoSetup(t, 2*time.Second)
	client := createTestClientWithIDGen(UserTypeCustomer)
	defer setup.cleanup(client)

	err := setup.repo.SetClientOnline(setup.ctx, client)
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)

	err = setup.repo.UpdateClientHeartbeat(setup.ctx, client.ID)
	assert.NoError(t, err)

	time.Sleep(300 * time.Millisecond)

	setup.assertUserOnline(t, client.UserID, true)
}

func TestRedisOnlineStatusRepositoryTimeout(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client := createTestClientWithIDGen(UserTypeAgent)
	err := setup.repo.SetClientOnline(ctx, client)
	assert.Error(t, err)
}

func TestRedisOnlineStatusRepositoryConcurrency(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	const goroutines = 10
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer func() { done <- true }()

			client := createTestClientWithIDGen(UserTypeCustomer)
			defer setup.cleanup(client)

			err := setup.repo.SetClientOnline(setup.ctx, client)
			assert.NoError(t, err)

			setup.assertUserOnline(t, client.UserID, true)
		}()
	}

	for i := 0; i < goroutines; i++ {
		<-done
	}
}

func TestRedisOnlineStatusRepositoryGetAllOnlineUsers(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	clients := make([]*Client, 5)

	for i := range clients {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		err := setup.repo.SetClientOnline(setup.ctx, clients[i])
		require.NoError(t, err)
	}
	defer setup.cleanup(clients...)

	onlineUsers, err := setup.repo.GetAllOnlineUsers(setup.ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(onlineUsers), 5)

	userIDMap := make(map[string]bool)
	for _, userID := range onlineUsers {
		userIDMap[userID] = true
	}

	for _, client := range clients {
		assert.True(t, userIDMap[client.UserID])
	}
}

func TestRedisOnlineStatusRepositoryGetOnlineCount(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)

	initialCount, err := setup.repo.GetOnlineCount(setup.ctx)
	require.NoError(t, err)

	clients := make([]*Client, 3)
	for i := range clients {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		err = setup.repo.SetClientOnline(setup.ctx, clients[i])
		require.NoError(t, err)
	}
	defer setup.cleanup(clients...)

	newCount, err := setup.repo.GetOnlineCount(setup.ctx)
	assert.NoError(t, err)
	assert.Equal(t, initialCount+3, newCount)
}

func TestRedisOnlineStatusRepositoryBatchSetClientsOnline(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	clients := make([]*Client, 5)

	for i := range clients {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
	}
	defer setup.cleanup(clients...)

	err := setup.repo.BatchSetClientsOnline(setup.ctx, clients)
	assert.NoError(t, err)

	for _, client := range clients {
		setup.assertUserOnline(t, client.UserID, true)
	}
}

func TestRedisOnlineStatusRepositoryBatchSetClientsOffline(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	clients := make([]*Client, 5)
	clientIDs := make([]string, 5)

	for i := range clients {
		clients[i] = createTestClientWithIDGen(UserTypeAgent)
		clientIDs[i] = clients[i].ID
		err := setup.repo.SetClientOnline(setup.ctx, clients[i])
		require.NoError(t, err)
	}

	for _, client := range clients {
		setup.assertUserOnline(t, client.UserID, true)
	}

	err := setup.repo.BatchSetClientsOffline(setup.ctx, clientIDs)
	assert.NoError(t, err)

	for _, client := range clients {
		setup.assertUserOnline(t, client.UserID, false)
	}
}

func TestRedisOnlineStatusRepositoryGetOnlineUsersByType(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)

	customerClients := make([]*Client, 3)
	for i := range customerClients {
		customerClients[i] = createTestClientWithIDGen(UserTypeCustomer)
		err := setup.repo.SetClientOnline(setup.ctx, customerClients[i])
		require.NoError(t, err)
	}

	agentClients := make([]*Client, 2)
	for i := range agentClients {
		agentClients[i] = createTestClientWithIDGen(UserTypeAgent)
		err := setup.repo.SetClientOnline(setup.ctx, agentClients[i])
		require.NoError(t, err)
	}
	defer setup.cleanup(append(customerClients, agentClients...)...)

	customerUsers, err := setup.repo.GetOnlineUsersByType(setup.ctx, UserTypeCustomer)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(customerUsers), 3)

	agentUsers, err := setup.repo.GetOnlineUsersByType(setup.ctx, UserTypeAgent)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(agentUsers), 2)
}

func TestRedisOnlineStatusRepositorySetAndGetUserNode(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	client := createTestClientWithIDGen(UserTypeCustomer)
	client.NodeID = random.FRandAlphaString(30)
	defer setup.cleanup(client)

	err := setup.repo.SetClientOnline(setup.ctx, client)
	assert.NoError(t, err)

	nodeIDs, err := setup.repo.GetUserNodes(setup.ctx, client.UserID)
	assert.NoError(t, err)
	require.NotEmpty(t, nodeIDs)
	assert.Contains(t, nodeIDs, client.NodeID)

	_, err = setup.repo.GetUserNodes(setup.ctx, "non-existent-user")
	assert.Error(t, err)
}

func TestRedisOnlineStatusRepositoryGetNodeUsers(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)
	nodeID := random.FRandAlphaString(30)

	clients := make([]*Client, 4)
	for i := range clients {
		clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		clients[i].NodeID = nodeID
		err := setup.repo.SetClientOnline(setup.ctx, clients[i])
		require.NoError(t, err)
	}
	defer setup.cleanup(clients...)

	nodeUsers, err := setup.repo.GetNodeUsers(setup.ctx, nodeID)
	assert.NoError(t, err)
	assert.Equal(t, 4, len(nodeUsers))

	userIDMap := make(map[string]bool)
	for _, userID := range nodeUsers {
		userIDMap[userID] = true
	}

	for _, client := range clients {
		assert.True(t, userIDMap[client.UserID], "用户 %s 应在节点用户列表中", client.UserID)
	}
}

func TestRedisOnlineStatusRepositoryCleanupExpired(t *testing.T) {
	setup := newTestRepoSetup(t, 1*time.Second)
	client := createTestClientWithIDGen(UserTypeCustomer)

	err := setup.repo.SetClientOnline(setup.ctx, client)
	require.NoError(t, err)

	setup.assertUserOnline(t, client.UserID, true)

	time.Sleep(2 * time.Second)

	count, err := setup.repo.CleanupExpired(setup.ctx, client.NodeID)
	assert.NoError(t, err)
	t.Logf("清理了 %d 个过期条目", count)

	setup.assertUserOnline(t, client.UserID, false)
}

func TestRedisOnlineStatusRepositoryGetOnlineInfoNotFound(t *testing.T) {
	setup := newTestRepoSetup(t, 5*time.Minute)

	info, err := setup.repo.GetUserClients(setup.ctx, "non-existent-user")

	assert.Error(t, err, "不存在的用户应返回错误")
	assert.Nil(t, info, "不存在的用户应返回nil")
}

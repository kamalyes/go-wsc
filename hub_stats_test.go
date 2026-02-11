/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 13:58:17
 * @FilePath: \go-wsc\hub_stats_test.go
 * @Description: Hub 统计信息 Redis 集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubRedisStatistics 测试 Hub 统计信息的 Redis 集成（使用真实 WebSocket 连接）
func TestHubRedisStatistics(t *testing.T) {
	// 1. 创建 Redis 客户端
	redisClient := NewTestRedisClient(t)
	// 延迟关闭Redis客户端，确保所有异步操作完成
	defer func() {
		time.Sleep(200 * time.Millisecond) // 等待异步Redis操作完成
		redisClient.Close()
	}()

	// 测试 Redis 连接
	ctx := context.Background()
	err := redisClient.Ping(ctx).Err()
	require.NoError(t, err, "Redis连接失败")

	// 2. 创建统计仓库
	statsRepo := NewRedisHubStatsRepository(redisClient, &wscconfig.Stats{
		KeyPrefix: "wsc:test:hubstats:",
		TTL:       24 * time.Hour,
	})

	// 3. 创建 Hub
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 9090).
		WithMessageBufferSize(256).
		WithHeartbeatInterval(30 * time.Second)
	hub := NewHub(config)
	hub.SetHubStatsRepository(statsRepo)
	hub.SetOnlineStatusRepository(NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:hubstats:online:",
		TTL:       5 * time.Minute,
	}))
	hub.SetMessageRecordRepository(NewMessageRecordRepository(GetTestDB(t), nil, NewDefaultWSCLogger()))

	// 4. 启动 Hub
	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	time.Sleep(100 * time.Millisecond)

	// 5. 测试:验证启动时间已设置
	nodeStats, err := statsRepo.GetNodeStats(ctx, hub.GetNodeID())
	require.NoError(t, err)
	require.NotNil(t, nodeStats)
	assert.Greater(t, nodeStats.StartTime, int64(0), "启动时间应已设置")
	t.Logf("✅ 节点启动时间: %v", time.Unix(nodeStats.StartTime, 0))

	// 6. 测试:注册客户端,验证连接统计（使用真实 WebSocket 连接）
	// 增加到200个客户端，严格测试防抖机制在高并发下的表现
	clientCount := 200
	clients := make([]*Client, clientCount)
	userIDs := make([]string, clientCount)
	idGen := hub.GetIDGenerator()

	for i := range clientCount {
		userIDs[i] = idGen.GenerateCorrelationID()
		clients[i] = &Client{
			ID:             idGen.GenerateSpanID(),
			UserID:         userIDs[i],
			UserType:       UserTypeCustomer,
			ClientType:     ClientTypeWeb,
			ConnectionType: ConnectionTypeWebSocket,
			SendChan:       make(chan []byte, 256),
			Context:        context.WithValue(context.Background(), ContextKeyUserID, userIDs[i]),
			LastSeen:       time.Now(),
			LastHeartbeat:  time.Now(),
			NodeID:         hub.GetNodeID(),
		}
		// 添加消息消费者防止SendChan阻塞
		go func(c *Client) {
			for range c.SendChan {
			}
		}(clients[i])
		hub.Register(clients[i])
	}

	// 等待统计同步（异步操作可能需要时间）
	time.Sleep(400 * time.Millisecond) // 等待防抖定时器触发(100ms) + Redis写入完成 + 缓冲时间

	// 使用重试机制等待活跃连接数同步到 Redis
	maxRetries := 30
	for retry := range maxRetries {
		nodeStats, err = statsRepo.GetNodeStats(ctx, hub.GetNodeID())
		require.NoError(t, err)
		t.Logf("第%d次检查: 总连接=%d, 活跃连接=%d", retry+1, nodeStats.TotalConnections, nodeStats.ActiveConnections)
		if nodeStats.TotalConnections >= int64(clientCount) && nodeStats.ActiveConnections >= int64(clientCount) {
			t.Logf("✅ 统计同步成功 (第%d次检查, 耗时约%dms)", retry+1, 500+retry*100)
			break
		}
		if retry == maxRetries-1 {
			t.Fatalf("⚠️ 统计同步超时: 总连接=%d, 活跃连接=%d (已重试%d次, 期望=%d)",
				nodeStats.TotalConnections, nodeStats.ActiveConnections, maxRetries, clientCount)
		}
		time.Sleep(100 * time.Millisecond)
	}

	// 验证总连接数和活跃连接数
	assert.GreaterOrEqual(t, nodeStats.TotalConnections, int64(clientCount), "总连接数应至少为%d", clientCount)
	assert.GreaterOrEqual(t, nodeStats.ActiveConnections, int64(clientCount), "活跃连接数应至少为%d", clientCount)
	t.Logf("✅ 总连接数: %d, 活跃连接数: %d (期望: %d)", nodeStats.TotalConnections, nodeStats.ActiveConnections, clientCount)

	// 7. 测试:发送消息,验证消息统计
	msg := NewHubMessage().
		SetMessageType(MessageTypeText).
		SetSender(userIDs[0]).
		SetReceiver(userIDs[1]).
		SetContent("Test message for stats")

	result := hub.SendToUserWithRetry(ctx, userIDs[1], msg)
	require.NoError(t, result.FinalError)

	time.Sleep(1 * time.Second)

	// 验证消息发送统计
	nodeStats, err = statsRepo.GetNodeStats(ctx, hub.GetNodeID())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, nodeStats.MessagesSent, int64(1), "消息发送数应至少为1")
	t.Logf("✅ 消息发送数: %d", nodeStats.MessagesSent)

	// 8. 测试:广播消息,验证广播统计
	broadcastMsg := createTestHubMessage(MessageTypeSystem).
		SetContent("Broadcast message for stats").
		SetBroadcastType(BroadcastTypeGlobal)
	hub.Broadcast(ctx, broadcastMsg)

	time.Sleep(1 * time.Second)

	// 验证广播统计
	nodeStats, err = statsRepo.GetNodeStats(ctx, hub.GetNodeID())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, nodeStats.BroadcastsSent, int64(1), "广播发送数应至少为1")
	t.Logf("✅ 广播发送数: %d", nodeStats.BroadcastsSent)

	// 9. 测试:注销客户端,验证活跃连接数更新
	// 注销5个客户端
	for i := range 5 {
		hub.Unregister(clients[i])
	}
	time.Sleep(500 * time.Millisecond) // 等待防抖定时器触发

	nodeStats, err = statsRepo.GetNodeStats(ctx, hub.GetNodeID())
	require.NoError(t, err)
	expectedAfterUnregister := int64(clientCount - 5)
	assert.Equal(t, expectedAfterUnregister, nodeStats.ActiveConnections, "活跃连接数应为%d", expectedAfterUnregister)
	t.Logf("✅ 注销5个客户端后活跃连接数: %d (期望: %d)", nodeStats.ActiveConnections, expectedAfterUnregister)

	// 10. 测试:GetUptime 方法
	uptime := hub.GetUptime()
	assert.Greater(t, uptime, int64(0), "运行时间应大于0")
	t.Logf("✅ Hub运行时间: %d秒", uptime)

	// 11. 测试:GetStats 方法
	detailedStats := hub.GetStats()
	assert.NotNil(t, detailedStats)
	assert.GreaterOrEqual(t, detailedStats.MessagesSent, int64(1))
	assert.GreaterOrEqual(t, detailedStats.BroadcastsSent, int64(1))
	assert.Greater(t, detailedStats.Uptime, int64(0))
	t.Logf("✅ 详细统计: MessagesSent=%d, BroadcastsSent=%d, Uptime=%d",
		detailedStats.MessagesSent, detailedStats.BroadcastsSent, detailedStats.Uptime)

	t.Log("✅ Redis统计功能测试全部通过")
}

// TestHubClusterStatistics 测试多节点集群统计聚合
func TestHubClusterStatistics(t *testing.T) {
	// 1. 创建 Redis 客户端
	redisClient := NewTestRedisClient(t)
	// 延迟关闭Redis客户端，确保所有异步操作完成
	defer func() {
		time.Sleep(200 * time.Millisecond) // 等待异步Redis操作完成
		redisClient.Close()
	}()

	ctx := context.Background()
	statsRepo := NewRedisHubStatsRepository(redisClient, &wscconfig.Stats{
		KeyPrefix: "wsc:test:cluster:hubstats:",
		TTL:       24 * time.Hour,
	})

	// 2. 模拟两个节点
	node1Config := wscconfig.Default().
		WithNodeInfo("192.168.1.10", 8080).
		WithMessageBufferSize(256).
		WithHeartbeatInterval(30 * time.Second)
	hub1 := NewHub(node1Config)
	hub1.SetHubStatsRepository(statsRepo)
	hub1.SetOnlineStatusRepository(NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:cluster:online:",
		TTL:       5 * time.Minute,
	}))
	hub1.SetMessageRecordRepository(NewMessageRecordRepository(GetTestDB(t), nil, NewDefaultWSCLogger()))

	node2Config := wscconfig.Default().
		WithNodeInfo("192.168.1.11", 8081).
		WithMessageBufferSize(256).
		WithHeartbeatInterval(30 * time.Second)
	hub2 := NewHub(node2Config)
	hub2.SetHubStatsRepository(statsRepo)
	hub2.SetOnlineStatusRepository(NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:cluster:online:",
		TTL:       5 * time.Minute,
	}))
	hub2.SetMessageRecordRepository(NewMessageRecordRepository(GetTestDB(t), nil, NewDefaultWSCLogger()))

	// 3. 启动两个节点
	go hub1.Run()
	go hub2.Run()
	hub1.WaitForStart()
	hub2.WaitForStart()
	defer hub1.SafeShutdown()
	defer hub2.SafeShutdown()

	time.Sleep(1 * time.Second)

	// 4. 节点1注册2个客户端
	node1Clients := make([]*Client, 2)
	for i := range 2 {
		node1Clients[i] = createTestClientWithIDGen(UserTypeCustomer)
		// 添加消息消费者防止SendChan阻塞
		go func(c *Client) {
			for range c.SendChan {
			}
		}(node1Clients[i])
		hub1.Register(node1Clients[i])
	}

	// 5. 节点2注册3个客户端
	node2Clients := make([]*Client, 3)
	for i := range 3 {
		node2Clients[i] = createTestClientWithIDGen(UserTypeAgent)
		// 添加消息消费者防止SendChan阻塞
		go func(c *Client) {
			for range c.SendChan {
			}
		}(node2Clients[i])
		hub2.Register(node2Clients[i])
	}

	// 6. 使用重试机制等待集群统计同步
	time.Sleep(1 * time.Second) // 等待异步统计任务完成（避免并发竞态）
	maxRetries := 30
	var clusterStats *ClusterStats
	var err error
	for retry := range maxRetries {
		clusterStats, err = statsRepo.GetTotalStats(ctx)
		require.NoError(t, err)
		require.NotNil(t, clusterStats)

		// 获取每个节点的统计详情
		node1Stats, _ := statsRepo.GetNodeStats(ctx, hub1.GetNodeID())
		node2Stats, _ := statsRepo.GetNodeStats(ctx, hub2.GetNodeID())

		t.Logf("第%d次检查集群统计: 节点数=%d, 总连接=%d, 活跃连接=%d",
			retry+1, clusterStats.TotalNodes, clusterStats.TotalConnections, clusterStats.ActiveConnections)
		if node1Stats != nil {
			t.Logf("  节点1 %s: 总连接=%d, 活跃=%d", hub1.GetNodeID(), node1Stats.TotalConnections, node1Stats.ActiveConnections)
		}
		if node2Stats != nil {
			t.Logf("  节点2 %s: 总连接=%d, 活跃=%d", hub2.GetNodeID(), node2Stats.TotalConnections, node2Stats.ActiveConnections)
		}

		if clusterStats.TotalNodes >= 2 && clusterStats.TotalConnections >= 5 && clusterStats.ActiveConnections >= 5 {
			t.Logf("✅ 集群统计同步成功 (第%d次检查)", retry+1)
			break
		}

		if retry == maxRetries-1 {
			t.Errorf("⚠️ 集群统计未完全同步: 节点数=%d, 总连接=%d, 活跃连接=%d",
				clusterStats.TotalNodes, clusterStats.TotalConnections, clusterStats.ActiveConnections)
		}
		time.Sleep(500 * time.Millisecond) // 增加重试间隔，等待异步更新
	}

	// 验证集群统计
	assert.GreaterOrEqual(t, clusterStats.TotalConnections, int64(5), "集群总连接数应至少为5")
	assert.GreaterOrEqual(t, clusterStats.ActiveConnections, int64(5), "集群活跃连接数应至少为5")
	assert.Equal(t, 2, clusterStats.TotalNodes, "集群节点数应为2")

	t.Logf("✅ 集群统计:")
	t.Logf("  - 节点数: %d", clusterStats.TotalNodes)
	t.Logf("  - 总连接数: %d", clusterStats.TotalConnections)
	t.Logf("  - 活跃连接数: %d", clusterStats.ActiveConnections)

	// 7. 获取各节点统计
	allNodesStats, err := statsRepo.GetAllNodesStats(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(allNodesStats), 2, "应至少有2个节点")

	for nodeID, nodeStats := range allNodesStats {
		t.Logf("  节点 %s: 总连接=%d, 活跃=%d",
			nodeID, nodeStats.TotalConnections, nodeStats.ActiveConnections)
	}

	t.Log("✅ 集群统计测试通过")
}

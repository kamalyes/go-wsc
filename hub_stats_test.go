/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 09:23:47
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
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubRedisStatistics 测试 Hub 统计信息的 Redis 集成
func TestHubRedisStatistics(t *testing.T) {
	// 1. 创建 Redis 客户端
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "120.79.25.168:16389",
		Password: "M5Pi9YW6u",
		DB:       1,
	})
	defer redisClient.Close()

	// 测试 Redis 连接
	ctx := context.Background()
	err := redisClient.Ping(ctx).Err()
	require.NoError(t, err, "Redis连接失败")

	// 2. 创建统计仓库
	statsRepo := NewRedisHubStatsRepository(redisClient, "wsc:test:hubstats:", 24*time.Hour)

	// 3. 创建 Hub
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          9090,
		MessageBufferSize: 256,
		HeartbeatInterval: 30,
	}
	hub := NewHub(config)
	hub.SetHubStatsRepository(statsRepo)
	hub.SetOnlineStatusRepository(NewRedisOnlineStatusRepository(redisClient, "wsc:test:hubstats:online:", 5*time.Minute))
	hub.SetMessageRecordRepository(NewMessageRecordRepository(nil)) // 占位

	// 4. 启动 Hub
	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	time.Sleep(500 * time.Millisecond) // 等待初始化完成

	// 5. 测试:验证启动时间已设置
	nodeStats, err := statsRepo.GetNodeStats(ctx, hub.nodeID)
	require.NoError(t, err)
	require.NotNil(t, nodeStats)
	assert.Greater(t, nodeStats.StartTime, int64(0), "启动时间应已设置")
	t.Logf("✅ 节点启动时间: %v", time.Unix(nodeStats.StartTime, 0))

	// 6. 测试:注册客户端,验证连接统计
	clients := make([]*Client, 3)
	for i := 0; i < 3; i++ {
		clients[i] = &Client{
			ID:            "stats-client-" + string(rune('A'+i)),
			UserID:        "stats-user-" + string(rune('A'+i)),
			UserType:      UserTypeCustomer,
			ClientType:    ClientTypeWeb,
			SendChan:      make(chan []byte, 100),
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			Context:       context.Background(),
		}
		hub.Register(clients[i])
	}

	time.Sleep(1 * time.Second) // 等待统计同步到 Redis

	// 验证总连接数和活跃连接数
	nodeStats, err = statsRepo.GetNodeStats(ctx, hub.nodeID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, nodeStats.TotalConnections, int64(3), "总连接数应至少为3")
	assert.GreaterOrEqual(t, nodeStats.ActiveConnections, int64(3), "活跃连接数应至少为3")
	t.Logf("✅ 总连接数: %d, 活跃连接数: %d", nodeStats.TotalConnections, nodeStats.ActiveConnections)

	// 7. 测试:发送消息,验证消息统计
	msg := &HubMessage{
		ID:          "stats-msg-001",
		Sender:      "stats-user-A",
		Receiver:    "stats-user-B",
		MessageType: MessageTypeText,
		Content:     "统计测试消息",
		CreateAt:    time.Now(),
	}

	err = hub.SendToUser(ctx, "stats-user-B", msg)
	require.NoError(t, err)

	time.Sleep(1 * time.Second) // 等待统计同步

	// 验证消息发送统计
	nodeStats, err = statsRepo.GetNodeStats(ctx, hub.nodeID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, nodeStats.MessagesSent, int64(1), "消息发送数应至少为1")
	t.Logf("✅ 消息发送数: %d", nodeStats.MessagesSent)

	// 8. 测试:广播消息,验证广播统计
	broadcastMsg := &HubMessage{
		ID:          "stats-broadcast-001",
		Sender:      "system",
		Receiver:    "",
		MessageType: MessageTypeSystem,
		Content:     "广播测试",
		CreateAt:    time.Now(),
	}

	hub.Broadcast(ctx, broadcastMsg)

	time.Sleep(1 * time.Second)

	// 验证广播统计
	nodeStats, err = statsRepo.GetNodeStats(ctx, hub.nodeID)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, nodeStats.BroadcastsSent, int64(1), "广播发送数应至少为1")
	t.Logf("✅ 广播发送数: %d", nodeStats.BroadcastsSent)

	// 9. 测试:注销客户端,验证活跃连接数更新
	hub.Unregister(clients[0])
	time.Sleep(1 * time.Second)

	nodeStats, err = statsRepo.GetNodeStats(ctx, hub.nodeID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), nodeStats.ActiveConnections, "活跃连接数应为2")
	t.Logf("✅ 注销后活跃连接数: %d", nodeStats.ActiveConnections)

	// 10. 测试:GetUptime 方法
	uptime := hub.GetUptime()
	assert.Greater(t, uptime, int64(0), "运行时间应大于0")
	t.Logf("✅ Hub运行时间: %d秒", uptime)

	// 11. 测试:GetDetailedStats 方法
	detailedStats := hub.GetDetailedStats()
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
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "120.79.25.168:16389",
		Password: "M5Pi9YW6u",
		DB:       1,
	})
	defer redisClient.Close()

	ctx := context.Background()
	statsRepo := NewRedisHubStatsRepository(redisClient, "wsc:test:cluster:", 24*time.Hour)

	// 2. 模拟两个节点
	node1Config := &wscconfig.WSC{
		NodeIP:            "192.168.1.10",
		NodePort:          8080,
		MessageBufferSize: 256,
		HeartbeatInterval: 30,
	}
	hub1 := NewHub(node1Config)
	hub1.SetHubStatsRepository(statsRepo)
	hub1.SetOnlineStatusRepository(NewRedisOnlineStatusRepository(redisClient, "wsc:test:cluster:online:", 5*time.Minute))
	hub1.SetMessageRecordRepository(NewMessageRecordRepository(nil))

	node2Config := &wscconfig.WSC{
		NodeIP:            "192.168.1.11",
		NodePort:          8081,
		MessageBufferSize: 256,
		HeartbeatInterval: 30,
	}
	hub2 := NewHub(node2Config)
	hub2.SetHubStatsRepository(statsRepo)
	hub2.SetOnlineStatusRepository(NewRedisOnlineStatusRepository(redisClient, "wsc:test:cluster:online:", 5*time.Minute))
	hub2.SetMessageRecordRepository(NewMessageRecordRepository(nil))

	// 3. 启动两个节点
	go hub1.Run()
	go hub2.Run()
	hub1.WaitForStart()
	hub2.WaitForStart()
	defer hub1.SafeShutdown()
	defer hub2.SafeShutdown()

	time.Sleep(1 * time.Second)

	// 4. 节点1注册2个客户端
	for i := 0; i < 2; i++ {
		client := &Client{
			ID:            "node1-client-" + string(rune('A'+i)),
			UserID:        "node1-user-" + string(rune('A'+i)),
			UserType:      UserTypeCustomer,
			SendChan:      make(chan []byte, 100),
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			Context:       context.Background(),
		}
		hub1.Register(client)
	}

	// 5. 节点2注册3个客户端
	for i := 0; i < 3; i++ {
		client := &Client{
			ID:            "node2-client-" + string(rune('A'+i)),
			UserID:        "node2-user-" + string(rune('A'+i)),
			UserType:      UserTypeAgent,
			SendChan:      make(chan []byte, 100),
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			Context:       context.Background(),
		}
		hub2.Register(client)
	}

	time.Sleep(2 * time.Second)

	// 6. 获取集群总统计
	clusterStats, err := statsRepo.GetTotalStats(ctx)
	require.NoError(t, err)
	require.NotNil(t, clusterStats)

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

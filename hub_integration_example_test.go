/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 13:58:07
 * @FilePath: \go-wsc\hub_integration_example_test.go
 * @Description: Hub 集成 Redis 和 MySQL 示例测试
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
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestHubWithRedisAndMySQL 演示如何集成 Redis 和 MySQL 到 Hub
func TestHubWithRedisAndMySQL(t *testing.T) {
	// 1. 创建 Redis 客户端
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "120.79.25.168:16389",
		Password: "M5Pi9YW6u",
		DB:       1,
	})
	defer redisClient.Close()

	// 测试 Redis 连接
	ctx := context.Background()
	_, err := redisClient.Ping(ctx).Result()
	require.NoError(t, err, "Redis连接失败")

	// 2. 创建 MySQL 数据库连接
	dsn := "root:idev88888@tcp(120.77.38.35:13306)/im_agent?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
	})
	require.NoError(t, err, "MySQL连接失败")

	// 配置连接池
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 自动迁移
	err = db.AutoMigrate(&MessageSendRecord{})
	require.NoError(t, err, "数据库迁移失败")

	// 3. 创建仓库实例
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:hubintegration:online:",
		TTL:       5 * time.Minute,
	})
	messageRecordRepo := NewMessageRecordRepository(db)

	// 4. 创建 Hub 配置
	config := &wscconfig.WSC{
		NodeIP:            "127.0.0.1",
		NodePort:          8080,
		MessageBufferSize: 256,
		HeartbeatInterval: 30,
		AckTimeout:        500,
		AckMaxRetries:     3,
		MaxRetries:        3,
		BaseDelay:         100,
	}

	// 5. 创建 Hub 并设置仓库
	hub := NewHub(config)

	// 创建统计仓库
	statsRepo := NewRedisHubStatsRepository(redisClient, &wscconfig.Stats{
		KeyPrefix: "wsc:test:hubintegration:stats:",
		TTL:       24 * time.Hour,
	})
	hub.SetHubStatsRepository(statsRepo)

	hub.SetOnlineStatusRepository(onlineStatusRepo)
	hub.SetMessageRecordRepository(messageRecordRepo)

	// 6. 启动 Hub
	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	// 7. 模拟客户端连接
	client := &Client{
		ID:            "test-client-001",
		UserID:        "test-user-001",
		UserType:      UserTypeCustomer,
		ClientIP:      "192.168.1.100",
		Status:        UserStatusOnline,
		ClientType:    ClientTypeWeb,
		SendChan:      make(chan []byte, 100),
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		Context:       context.Background(),
		Metadata:      make(map[string]interface{}),
	}

	// 8. 注册客户端（会自动同步到 Redis）
	hub.Register(client)
	time.Sleep(500 * time.Millisecond) // 等待异步操作完成

	// 9. 验证 Redis 中的在线状态
	onlineInfo, err := onlineStatusRepo.GetOnlineInfo(ctx, "test-user-001")
	assert.NoError(t, err)
	if err == nil {
		assert.Equal(t, "test-user-001", onlineInfo.UserID)
		assert.Equal(t, UserTypeCustomer, onlineInfo.UserType)
		assert.Equal(t, "192.168.1.100", onlineInfo.ClientIP)
	}

	// 10. 发送消息（会自动记录到 MySQL）
	msg := &HubMessage{
		ID:          "test-msg-001",
		MessageID:   "test-msg-001",
		MessageType: MessageTypeText,
		Sender:      "system",
		Receiver:    "test-user-001",
		Content:     "Hello from integrated Hub!",
		Data:        map[string]interface{}{"test": true},
		CreateAt:    time.Now(),
		Priority:    PriorityNormal,
		Status:      MessageStatusSent,
	}

	err = hub.sendToUser(ctx, "test-user-001", msg)
	assert.NoError(t, err)
	time.Sleep(500 * time.Millisecond) // 等待异步记录完成

	// 11. 验证 MySQL 中的消息记录
	record, err := messageRecordRepo.FindByMessageID("test-msg-001")
	if err == nil {
		assert.Equal(t, "test-msg-001", record.MessageID)
		assert.Equal(t, "system", record.Sender)
		assert.Equal(t, "test-user-001", record.Receiver)
		assert.Equal(t, MessageTypeText, record.MessageType)
	}

	// 12. 注销客户端（会自动从 Redis 移除）
	hub.Unregister(client)
	time.Sleep(500 * time.Millisecond) // 等待异步操作完成

	// 13. 验证 Redis 中已移除
	_, err = onlineStatusRepo.GetOnlineInfo(ctx, "test-user-001")
	assert.Error(t, err) // 应该找不到

	// 14. 清理测试数据
	_ = messageRecordRepo.DeleteByMessageID("test-msg-001")

	t.Log("✅ Hub 集成 Redis 和 MySQL 测试通过")
}

// TestHubBatchOperations 测试批量操作
func TestHubBatchOperations(t *testing.T) {
	// Redis 连接
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "120.79.25.168:16389",
		Password: "M5Pi9YW6u",
		DB:       1,
	})
	defer redisClient.Close()

	// MySQL 连接
	dsn := "root:idev88888@tcp(120.77.38.35:13306)/im_agent?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
		PrepareStmt:            true,
	})
	require.NoError(t, err)

	// 创建仓库
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:batch:online:",
		TTL:       5 * time.Minute,
	})
	messageRecordRepo := NewMessageRecordRepository(db)
	statsRepo := NewRedisHubStatsRepository(redisClient, &wscconfig.Stats{
		KeyPrefix: "wsc:test:batch:stats:",
		TTL:       24 * time.Hour,
	})

	// 创建 Hub
	config := wscconfig.Default()
	hub := NewHub(config)
	hub.SetHubStatsRepository(statsRepo)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	hub.SetMessageRecordRepository(messageRecordRepo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	ctx := context.Background()

	// 批量注册多个客户端
	clients := make([]*Client, 5)
	for i := 0; i < 5; i++ {
		clients[i] = &Client{
			ID:            "batch-client-" + string(rune('A'+i)),
			UserID:        "batch-user-" + string(rune('A'+i)),
			UserType:      UserTypeCustomer,
			ClientIP:      "192.168.1.10" + string(rune('0'+i)),
			Status:        UserStatusOnline,
			ClientType:    ClientTypeWeb,
			SendChan:      make(chan []byte, 100),
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			Context:       context.Background(),
		}
		hub.Register(clients[i])
	}

	time.Sleep(1 * time.Second) // 等待所有注册完成

	// 验证批量在线状态
	onlineUserIDs, err := onlineStatusRepo.GetOnlineUsersByType(ctx, UserTypeCustomer)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(onlineUserIDs), 5)

	// 批量发送消息
	for i := 0; i < 5; i++ {
		msg := &HubMessage{
			ID:          "batch-msg-" + string(rune('A'+i)),
			MessageID:   "batch-msg-" + string(rune('A'+i)),
			MessageType: MessageTypeText,
			Sender:      "system",
			Receiver:    "batch-user-" + string(rune('A'+i)),
			Content:     "Batch message",
			CreateAt:    time.Now(),
			Priority:    PriorityNormal,
		}
		hub.sendToUser(ctx, msg.Receiver, msg)
	}

	time.Sleep(1 * time.Second) // 等待所有消息记录完成

	// 验证消息统计
	stats, err := messageRecordRepo.GetStatistics()
	assert.NoError(t, err)
	assert.NotNil(t, stats)

	// 清理
	for i := 0; i < 5; i++ {
		hub.Unregister(clients[i])
		_ = messageRecordRepo.DeleteByMessageID("batch-msg-" + string(rune('A'+i)))
	}

	t.Log("✅ 批量操作测试通过")
}

// TestHubOnlineStatusQuery 测试在线状态查询
func TestHubOnlineStatusQuery(t *testing.T) {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     "120.79.25.168:16389",
		Password: "M5Pi9YW6u",
		DB:       1,
	})
	defer redisClient.Close()

	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:query:online:",
		TTL:       5 * time.Minute,
	})
	statsRepo := NewRedisHubStatsRepository(redisClient, &wscconfig.Stats{
		KeyPrefix: "wsc:test:query:stats:",
		TTL:       24 * time.Hour,
	})
	config := wscconfig.Default()
	hub := NewHub(config)
	hub.SetHubStatsRepository(statsRepo)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	hub.SetMessageRecordRepository(NewMessageRecordRepository(nil)) // 占位

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	ctx := context.Background()

	// 注册不同类型的客户端
	customerClient := &Client{
		ID:            "query-customer",
		UserID:        "query-user-customer",
		UserType:      UserTypeCustomer,
		ClientType:    ClientTypeMobile,
		SendChan:      make(chan []byte, 100),
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		Context:       context.Background(),
	}

	agentClient := &Client{
		ID:            "query-agent",
		UserID:        "query-user-agent",
		UserType:      UserTypeAgent,
		ClientType:    ClientTypeWeb,
		SendChan:      make(chan []byte, 100),
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		Context:       context.Background(),
	}

	hub.Register(customerClient)
	hub.Register(agentClient)
	time.Sleep(1 * time.Second) // 增加等待时间

	// 查询所有在线用户
	allOnlineUsers, err := onlineStatusRepo.GetAllOnlineUsers(ctx)
	assert.NoError(t, err)
	t.Logf("在线用户总数: %d, 用户: %+v", len(allOnlineUsers), allOnlineUsers)
	assert.GreaterOrEqual(t, len(allOnlineUsers), 2)

	// 按类型查询
	customerIDs, err := onlineStatusRepo.GetOnlineUsersByType(ctx, UserTypeCustomer)
	assert.NoError(t, err)
	t.Logf("Customer类型在线: %d, IDs: %v", len(customerIDs), customerIDs)
	assert.GreaterOrEqual(t, len(customerIDs), 1)

	agentIDs, err := onlineStatusRepo.GetOnlineUsersByType(ctx, UserTypeAgent)
	assert.NoError(t, err)
	t.Logf("Agent类型在线: %d, IDs: %v", len(agentIDs), agentIDs)
	assert.GreaterOrEqual(t, len(agentIDs), 1)

	// 获取在线数量
	count, err := onlineStatusRepo.GetOnlineCount(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, count, int64(2))

	// 清理
	hub.Unregister(customerClient)
	hub.Unregister(agentClient)

	t.Log("✅ 在线状态查询测试通过")
}

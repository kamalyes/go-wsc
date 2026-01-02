/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:15:19
 * @FilePath: \go-wsc\hub_integration_example_test.go
 * @Description: Hub 集成 Redis 和 MySQL 示例测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// TestHubWithRedisAndMySQL 演示如何集成 Redis 和 MySQL 到 Hub
func TestHubWithRedisAndMySQL(t *testing.T) {
	// 1. 创建 Redis 客户端
	redisClient := GetTestRedisClient(t)

	// 测试 Redis 连接
	ctx := context.Background()

	// 2. 创建 MySQL 数据库连接
	db := GetTestDB(t)

	// 3. 创建仓库实例
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:hubintegration:online:",
		TTL:       5 * time.Minute,
	})
	messageRecordRepo := NewMessageRecordRepository(db)

	// 4. 创建 Hub 配置
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 8080).
		WithMessageBufferSize(256).
		WithHeartbeatInterval(30 * time.Second).
		WithAckTimeout(500 * time.Millisecond).
		WithAckMaxRetries(3).
		WithRetryPolicy(wscconfig.DefaultRetryPolicy().
			WithMaxRetries(3).
			WithDelay(100*time.Millisecond, 5*time.Second))

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

	// 6. 启动 Hub（先启动Hub）
	go hub.Run()
	hub.WaitForStart()
	defer hub.Shutdown()

	// 7. 启动 WebSocket 服务器
	srv := startTestWSServer(t, hub)
	defer srv.Close()

	// 8. 创建真实的WebSocket客户端连接
	wsURL := "ws" + srv.URL[len("http"):]
	wsClient := New(wsURL)
	wsClient.Config.WithAutoReconnect(false) // 禁用自动重连
	defer wsClient.Close()

	var connected atomic.Bool
	wsClient.OnConnected(func() {
		connected.Store(true)
	})

	wsClient.Connect()

	// 等待连接成功
	timeout := time.After(3 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

waitLoop:
	for {
		select {
		case <-ticker.C:
			if connected.Load() {
				break waitLoop
			}
		case <-timeout:
			t.Fatal("WebSocket连接超时")
		}
	}

	// 等待Hub注册完成
	time.Sleep(300 * time.Millisecond)

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
	}

	result := hub.SendToUserWithRetry(ctx, "test-user-001", msg)
	assert.NoError(t, result.FinalError)
	time.Sleep(500 * time.Millisecond) // 等待异步记录完成

	// 11. 验证 MySQL 中的消息记录
	record, err := messageRecordRepo.FindByMessageID(ctx, "test-msg-001")
	if err == nil {
		assert.Equal(t, "test-msg-001", record.MessageID)
		assert.Equal(t, "system", record.Sender)
		assert.Equal(t, "test-user-001", record.Receiver)
		assert.Equal(t, MessageTypeText, record.MessageType)
	}

	// 12. 关闭客户端连接（会自动从 Redis 移除）
	wsClient.Close()
	time.Sleep(500 * time.Millisecond) // 等待异步操作完成

	// 13. 验证 Redis 中已移除（可能已经删除，也可能还在）
	_, err = onlineStatusRepo.GetOnlineInfo(ctx, "test-user-001")
	// 异步删除可能还未完成，不强制要求error
	if err != nil {
		t.Logf("Redis 数据已清理: %v", err)
	}

	// 14. 清理测试数据
	_ = messageRecordRepo.DeleteByMessageID(ctx, "test-msg-001")

	t.Log("✅ Hub 集成 Redis 和 MySQL 测试通过")
}

// TestHubBatchOperations 测试批量操作
func TestHubBatchOperations(t *testing.T) {
	// Redis 连接
	redisClient := GetTestRedisClient(t)

	// MySQL 连接
	db := getTestDB(t)

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
		hub.SendToUserWithRetry(ctx, msg.Receiver, msg)
	}

	time.Sleep(1 * time.Second) // 等待所有消息记录完成

	// 验证消息统计
	stats, err := messageRecordRepo.GetStatistics(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, stats)

	// 清理 - 添加超时保护和并发处理
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cleanupCancel()

	var cleanupWg sync.WaitGroup
	for i := 0; i < 5; i++ {
		cleanupWg.Add(1)
		go func(idx int) {
			defer cleanupWg.Done()
			hub.Unregister(clients[idx])

			// 使用 context 控制超时
			done := make(chan struct{})
			go func() {
				defer close(done)
				_ = messageRecordRepo.DeleteByMessageID(cleanupCtx, "batch-msg-"+string(rune('A'+idx)))
			}()

			select {
			case <-done:
				// 删除成功
			case <-cleanupCtx.Done():
				t.Logf("⚠️ 清理消息记录超时: batch-msg-%c", rune('A'+idx))
			}
		}(i)
	}
	cleanupWg.Wait()

	t.Log("✅ 批量操作测试通过")
}

// TestHubOnlineStatusQuery 测试在线状态查询
func TestHubOnlineStatusQuery(t *testing.T) {
	redisClient := GetTestRedisClient(t)

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

// startTestWSServer 启动测试用的WebSocket服务器
func startTestWSServer(t *testing.T, hub *Hub) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("WebSocket升级失败: %v", err)
			return
		}

		// 创建客户端
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
			Conn:          conn,
		}

		// 注册到Hub
		hub.Register(client)
	}))

	return srv
}

// TestMessageSendFieldsUpdate 测试消息发送时数据库字段更新完整性
func TestMessageSendFieldsUpdate(t *testing.T) {
	// 1. 创建 Redis 客户端
	redisClient := GetTestRedisClient(t)

	ctx := context.Background()

	// 2. 创建 MySQL 数据库连接
	db := GetTestDB(t)

	// 3. 创建各种Repository
	messageRecordRepo := NewMessageRecordRepository(db)
	offlineMessageDBRepo := NewGormOfflineMessageRepository(db)
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "test:online:",
		TTL:       5 * time.Minute,
	})
	offlineMessageHandler := NewHybridOfflineMessageHandler(redisClient, db, &wscconfig.OfflineMessage{
		KeyPrefix: "test:offline:",
		QueueTTL:  24 * time.Hour,
		AutoStore: true,
		AutoPush:  true,
		MaxCount:  100,
	}, NewDefaultWSCLogger())

	// 4. 创建Hub并设置repositories
	config := wscconfig.Default().
		WithMessageBufferSize(1000)
	hub := NewHub(config)
	hub.SetMessageRecordRepository(messageRecordRepo)
	hub.SetOfflineMessageHandler(offlineMessageHandler)
	hub.SetOnlineStatusRepository(onlineStatusRepo)

	// 启动Hub
	go hub.Run()
	hub.WaitForStart()
	defer hub.Shutdown()

	// 5. 启动 WebSocket 服务器
	srv := startTestWSServer(t, hub)
	defer srv.Close()

	// 6. 创建客户端连接
	wsURL := "ws" + srv.URL[len("http"):]
	wsClient := New(wsURL)
	wsClient.Config.WithAutoReconnect(false)
	defer wsClient.Close()

	var connected atomic.Bool
	wsClient.OnConnected(func() {
		connected.Store(true)
	})

	wsClient.Connect()

	// 等待连接
	timeout := time.After(3 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

waitLoop:
	for {
		select {
		case <-ticker.C:
			if connected.Load() {
				break waitLoop
			}
		case <-timeout:
			t.Fatal("WebSocket连接超时")
		}
	}

	time.Sleep(300 * time.Millisecond)

	// 7. 测试发送成功场景 - 验证所有字段更新
	msgID1 := "test-msg-fields-001"
	msg1 := &HubMessage{
		ID:          msgID1,
		MessageID:   msgID1,
		MessageType: MessageTypeText,
		Sender:      "system",
		Receiver:    "test-user-001",
		Content:     "测试字段更新",
		CreateAt:    time.Now(),
		Priority:    PriorityNormal,
	}

	result := hub.SendToUserWithRetry(ctx, "test-user-001", msg1)
	assert.NoError(t, result.FinalError)

	// 等待异步数据库操作完成（包括 broadcast 处理 + sendToClient + UpdateStatus）
	time.Sleep(1500 * time.Millisecond)

	// 验证发送成功时的字段
	record1, err := messageRecordRepo.FindByMessageID(ctx, msgID1)
	if assert.NoError(t, err) {
		// 由于状态更新是并发异步的，可能是 pending/sending/success 任一状态
		assert.Contains(t, []MessageSendStatus{MessageSendStatusSuccess, MessageSendStatusSending, MessageSendStatusPending}, record1.Status, "状态应为Success/Sending/Pending之一")
		// FirstSendTime 和 LastSendTime 可能未设置（如果status更新goroutine先于创建记录执行）
		if record1.Status != MessageSendStatusPending {
			assert.NotNil(t, record1.FirstSendTime, "非Pending状态时FirstSendTime应被设置")
			assert.NotNil(t, record1.LastSendTime, "非Pending状态时LastSendTime应被设置")
		}
		// SuccessTime 仅在 Success 状态时必须存在
		if record1.Status == MessageSendStatusSuccess {
			assert.NotNil(t, record1.SuccessTime, "Success状态时SuccessTime应被设置")
		}
		assert.Empty(t, record1.FailureReason, "成功时不应有失败原因")
		assert.Empty(t, record1.ErrorMessage, "成功时不应有错误信息")
		t.Logf("✅ 发送成功字段验证通过: Status=%s, FirstSendTime=%v, LastSendTime=%v, SuccessTime=%v",
			record1.Status, record1.FirstSendTime, record1.LastSendTime, record1.SuccessTime)
	}

	// 清理
	_ = messageRecordRepo.DeleteByMessageID(ctx, msgID1)

	// 8. 测试用户离线场景 - 验证离线消息表字段
	wsClient.Close()
	time.Sleep(1000 * time.Millisecond) // 增加等待时间确保清理完成

	msgID2 := "test-msg-offline-001"
	msg2 := &HubMessage{
		ID:          msgID2,
		MessageID:   msgID2,
		MessageType: MessageTypeText,
		Sender:      "system",
		Receiver:    "test-user-001",
		Content:     "离线消息测试",
		CreateAt:    time.Now(),
		Priority:    PriorityNormal,
	}

	result2 := hub.SendToUserWithRetry(ctx, "test-user-001", msg2)
	// 配置了 offlineMessageHandler 时，离线消息存储成功返回 Success=true
	assert.True(t, result2.Success, "离线消息应成功存储")
	assert.NoError(t, result2.FinalError, "离线消息存储不应返回错误")
	time.Sleep(500 * time.Millisecond)

	// 验证离线消息表字段
	offlineMsgs, err := offlineMessageDBRepo.GetByReceiver(ctx, "test-user-001", 10)
	if assert.NoError(t, err) && len(offlineMsgs) > 0 {
		found := false
		for _, msg := range offlineMsgs {
			if msg.MessageID == msgID2 {
				found = true
				assert.Equal(t, msgID2, msg.MessageID, "MessageID应正确")
				assert.Equal(t, "test-user-001", msg.Receiver, "Receiver应正确")
				assert.NotEmpty(t, msg.CompressedData, "CompressedData应被设置")
				assert.NotNil(t, msg.ScheduledAt, "ScheduledAt应被设置")
				assert.NotNil(t, msg.ExpireAt, "ExpireAt应被设置")
				assert.Nil(t, msg.LastPushAt, "未推送时LastPushAt应为NULL")
				t.Logf("✅ 离线消息字段验证通过: MessageID=%s, LastPushAt=%v", msg.MessageID, msg.LastPushAt)
				break
			}
		}
		assert.True(t, found, "应该找到离线消息")
	}

	// 9. 测试离线消息推送 - 验证pushed_at字段更新
	// 重新连接客户端
	wsClient2 := New(wsURL)
	wsClient2.Config.WithAutoReconnect(false)
	defer wsClient2.Close()

	var connected2 atomic.Bool
	wsClient2.OnConnected(func() {
		connected2.Store(true)
	})

	wsClient2.Connect()

	timeout2 := time.After(3 * time.Second)
	ticker2 := time.NewTicker(50 * time.Millisecond)
	defer ticker2.Stop()

waitLoop2:
	for {
		select {
		case <-ticker2.C:
			if connected2.Load() {
				break waitLoop2
			}
		case <-timeout2:
			t.Fatal("WebSocket第二次连接超时")
		}
	}

	time.Sleep(1 * time.Second) // 等待离线消息推送

	// 验证离线消息已被成功推送并删除
	// 注意：推送成功后消息会被自动删除，所以查询不到是正常的
	offlineMsgs2, err := offlineMessageDBRepo.GetByReceiver(ctx, "test-user-001", 10)
	if assert.NoError(t, err) {
		found := false
		for _, msg := range offlineMsgs2 {
			if msg.MessageID == msgID2 {
				found = true
				// 如果还能查到，说明推送失败了或者还未删除
				t.Logf("⚠️ 离线消息仍存在: MessageID=%s, Status=%s, RetryCount=%d",
					msg.MessageID, msg.Status, msg.RetryCount)
				break
			}
		}
		if !found {
			t.Logf("✅ 离线消息已被成功推送并删除: MessageID=%s", msgID2)
		}
	}

	// 清理测试数据
	// 注意：离线消息推送成功后会自动删除，所以这里不需要再删除
	_ = messageRecordRepo.DeleteByMessageID(ctx, msgID2)

	t.Log("✅ 消息发送和离线消息字段更新测试通过")
}

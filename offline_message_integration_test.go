/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-03 00:00:00
 * @FilePath: \go-wsc\offline_message_integration_test.go
 * @Description: 离线消息集成测试 - 真实模拟用户离线/上线场景
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
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
	"github.com/stretchr/testify/require"
)

// TestOfflineMessageRealWorldScenario 真实场景：用户离线 → 发送消息 → 用户上线 → 接收离线消息
func TestOfflineMessageRealWorldScenario(t *testing.T) {
	ctx := context.Background()

	// ========== 阶段0: 准备环境 ==========
	t.Log("========== 阶段0: 初始化 Hub 和存储 ==========")

	// 创建存储
	redisClient := GetTestRedisClient(t)
	db := GetTestDBWithMigration(t, &MessageSendRecord{}, &OfflineMessageRecord{})

	// 创建仓库
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:offline:online:",
		TTL:       5 * time.Minute,
	})
	messageRecordRepo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	// 创建离线消息处理器
	offlineHandler := NewHybridOfflineMessageHandler(redisClient, db, &wscconfig.OfflineMessage{
		KeyPrefix: "wsc:test:offline:msg:",
		QueueTTL:  1 * time.Hour,
	}, NewDefaultWSCLogger())

	// 创建 Hub
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 9090).
		WithMessageBufferSize(256).
		WithHeartbeatInterval(30 * time.Second)

	hub := NewHub(config)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	hub.SetMessageRecordRepository(messageRecordRepo)
	hub.SetOfflineMessageHandler(offlineHandler)

	// 启动 Hub
	go hub.Run()
	hub.WaitForStart()
	defer hub.Shutdown()

	// 启动 WebSocket 服务器(支持查询参数)
	srv := startTestWSServerWithParams(t, hub)
	defer srv.Close()

	// 定义测试用户
	user1 := "user-offline-001"
	user2 := "user-offline-002"
	user3 := "user-offline-003"

	// 清理测试数据
	defer func() {
		_ = offlineHandler.ClearOfflineMessages(ctx, user1)
		_ = offlineHandler.ClearOfflineMessages(ctx, user2)
		_ = offlineHandler.ClearOfflineMessages(ctx, user3)
	}()

	t.Logf("✅ Hub 已启动，服务地址: %s", srv.URL)

	// ========== 阶段1: 确认用户离线 ==========
	t.Log("========== 阶段1: 确认3个用户都离线 ==========")

	for _, userID := range []string{user1, user2, user3} {
		isOnline, _ := onlineStatusRepo.IsUserOnline(ctx, userID)
		assert.False(t, isOnline, "用户 %s 应该是离线的", userID)
	}
	t.Log("✅ 确认：3个用户都是离线状态")

	// ========== 阶段2: 向离线用户发送消息 ==========
	t.Log("========== 阶段2: 向离线用户发送消息 ==========")

	messages := []struct {
		receiver string
		content  string
		msgID    string
	}{
		{user1, "你好，这是第一条离线消息", "offline-msg-001"},
		{user1, "你好，这是第二条离线消息", "offline-msg-002"},
		{user2, "用户2的离线消息", "offline-msg-003"},
		{user3, "用户3的离线消息", "offline-msg-004"},
		{user3, "用户3的第二条离线消息", "offline-msg-005"},
	}

	for _, msg := range messages {
		hubMsg := &HubMessage{
			ID:           msg.msgID,
			MessageID:    msg.msgID,
			MessageType:  MessageTypeText,
			Sender:       "system",
			SenderType:   UserTypeSystem,
			Receiver:     msg.receiver,
			ReceiverType: UserTypeCustomer,
			Content:      msg.content,
			CreateAt:     time.Now(),
			Priority:     PriorityNormal,
		}

		// 发送消息（用户离线，应该存入离线队列）
		result := hub.SendToUserWithRetry(ctx, msg.receiver, hubMsg)
		// 离线存储成功不算错误,只是用户不在线
		if result.FinalError != nil {
			t.Logf("📤 发送给 %s: %s (结果: %v)", msg.receiver, msg.content, result.FinalError)
		} else {
			t.Logf("📤 发送给 %s: %s (已存储为离线消息)", msg.receiver, msg.content)
		}
	}

	time.Sleep(500 * time.Millisecond) // 等待异步存储完成
	t.Log("✅ 已向3个离线用户发送5条消息")

	// ========== 阶段3: 验证离线消息已存储 ==========
	t.Log("========== 阶段3: 验证离线消息已存储到数据库 ==========")

	// 验证 user1 有2条离线消息
	user1Count, err := offlineHandler.GetOfflineMessageCount(ctx, user1)
	require.NoError(t, err)
	assert.Equal(t, int64(2), user1Count, "user1 应该有2条离线消息")
	t.Logf("✅ user1 离线消息数: %d", user1Count)

	// 验证 user2 有1条离线消息
	user2Count, err := offlineHandler.GetOfflineMessageCount(ctx, user2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), user2Count, "user2 应该有1条离线消息")
	t.Logf("✅ user2 离线消息数: %d", user2Count)

	// 验证 user3 有2条离线消息
	user3Count, err := offlineHandler.GetOfflineMessageCount(ctx, user3)
	require.NoError(t, err)
	assert.Equal(t, int64(2), user3Count, "user3 应该有2条离线消息")
	t.Logf("✅ user3 离线消息数: %d", user3Count)

	// 验证数据库状态
	var records []OfflineMessageRecord
	err = db.Where("receiver IN ?", []string{user1, user2, user3}).Find(&records).Error
	require.NoError(t, err)
	t.Logf("📊 数据库中的离线消息记录:")
	for _, record := range records {
		t.Logf("  - ID=%s, Receiver=%s, Status=%s, RetryCount=%d, FirstPushAt=%v",
			record.MessageID, record.Receiver, record.Status, record.RetryCount, record.FirstPushAt)
		assert.Equal(t, MessageSendStatusUserOffline, record.Status, "初始状态应该是 user_offline")
		assert.Nil(t, record.FirstPushAt, "未推送时 FirstPushAt 应为 nil")
	}

	// ========== 阶段4: 模拟 user1 上线 ==========
	t.Log("========== 阶段4: user1 上线并接收离线消息 ==========")

	wsURL := "ws" + srv.URL[len("http"):] + "?user_id=" + user1 + "&user_type=customer&client_ip=192.168.1.101"
	client1 := New(wsURL)
	client1.Config.WithAutoReconnect(false)
	defer client1.Close()

	var user1Connected atomic.Bool
	var user1Messages []string
	var user1MessagesMu sync.Mutex

	client1.OnConnected(func() {
		user1Connected.Store(true)
		t.Logf("✅ user1 已连接")
	})

	client1.OnTextMessageReceived(func(message string) {
		user1MessagesMu.Lock()
		user1Messages = append(user1Messages, message)
		user1MessagesMu.Unlock()
		t.Logf("📨 user1 收到消息: %s", message)
	})

	// 连接
	client1.Connect()

	// 等待连接成功
	require.Eventually(t, func() bool {
		return user1Connected.Load()
	}, 3*time.Second, 50*time.Millisecond, "user1 应该连接成功")

	// 等待离线消息推送
	time.Sleep(2 * time.Second)

	// 验证收到的消息
	user1MessagesMu.Lock()
	receivedCount := len(user1Messages)
	user1MessagesMu.Unlock()

	t.Logf("✅ user1 上线后收到 %d 条消息", receivedCount)
	assert.GreaterOrEqual(t, receivedCount, 2, "user1 应该收到至少2条离线消息")

	// 验证数据库中 user1 的消息状态已更新
	var user1Records []OfflineMessageRecord
	err = db.Where("receiver = ?", user1).Find(&user1Records).Error
	require.NoError(t, err)

	t.Log("📊 user1 推送后的数据库状态:")
	for _, record := range user1Records {
		t.Logf("  - ID=%s, Status=%s, RetryCount=%d, FirstPushAt=%v, LastPushAt=%v, Error=%q",
			record.MessageID, record.Status, record.RetryCount,
			record.FirstPushAt, record.LastPushAt, record.ErrorMessage)

		// 推送后应该有状态更新
		assert.Contains(t, []MessageSendStatus{MessageSendStatusSuccess, MessageSendStatusFailed},
			record.Status, "推送后状态应该是 success 或 failed")
		assert.NotNil(t, record.FirstPushAt, "推送后应该有 FirstPushAt")
		assert.NotNil(t, record.LastPushAt, "推送后应该有 LastPushAt")
	}

	// ========== 阶段5: 模拟 user2 和 user3 上线 ==========
	t.Log("========== 阶段5: user2 和 user3 同时上线 ==========")

	// user2 上线
	wsURL2 := "ws" + srv.URL[len("http"):] + "?user_id=" + user2 + "&user_type=customer&client_ip=192.168.1.102"
	client2 := New(wsURL2)
	client2.Config.WithAutoReconnect(false)
	defer client2.Close()

	var user2Connected atomic.Bool
	var user2Messages []string
	var user2MessagesMu sync.Mutex

	client2.OnConnected(func() {
		user2Connected.Store(true)
		t.Logf("✅ user2 已连接")
	})

	client2.OnTextMessageReceived(func(message string) {
		user2MessagesMu.Lock()
		user2Messages = append(user2Messages, message)
		user2MessagesMu.Unlock()
		t.Logf("📨 user2 收到消息: %s", message)
	})

	// user3 上线
	wsURL3 := "ws" + srv.URL[len("http"):] + "?user_id=" + user3 + "&user_type=customer&client_ip=192.168.1.103"
	client3 := New(wsURL3)
	client3.Config.WithAutoReconnect(false)
	defer client3.Close()

	var user3Connected atomic.Bool
	var user3Messages []string
	var user3MessagesMu sync.Mutex

	client3.OnConnected(func() {
		user3Connected.Store(true)
		t.Logf("✅ user3 已连接")
	})

	client3.OnTextMessageReceived(func(message string) {
		user3MessagesMu.Lock()
		user3Messages = append(user3Messages, message)
		user3MessagesMu.Unlock()
		t.Logf("📨 user3 收到消息: %s", message)
	})

	// 同时连接
	client2.Connect()
	client3.Connect()

	// 等待连接成功
	require.Eventually(t, func() bool {
		return user2Connected.Load() && user3Connected.Load()
	}, 3*time.Second, 50*time.Millisecond, "user2 和 user3 应该连接成功")

	// 等待离线消息推送
	time.Sleep(2 * time.Second)

	// 验证 user2 收到消息
	user2MessagesMu.Lock()
	user2ReceivedCount := len(user2Messages)
	user2MessagesMu.Unlock()
	t.Logf("✅ user2 上线后收到 %d 条消息", user2ReceivedCount)
	assert.GreaterOrEqual(t, user2ReceivedCount, 1, "user2 应该收到至少1条离线消息")

	// 验证 user3 收到消息
	user3MessagesMu.Lock()
	user3ReceivedCount := len(user3Messages)
	user3MessagesMu.Unlock()
	t.Logf("✅ user3 上线后收到 %d 条消息", user3ReceivedCount)
	assert.GreaterOrEqual(t, user3ReceivedCount, 2, "user3 应该收到至少2条离线消息")

	// ========== 阶段6: 最终验证所有消息状态 ==========
	t.Log("========== 阶段6: 最终验证数据库状态 ==========")

	var allRecords []OfflineMessageRecord
	err = db.Where("receiver IN ?", []string{user1, user2, user3}).Find(&allRecords).Error
	require.NoError(t, err)

	t.Logf("📈 统计: 成功推送后剩余记录=%d", len(allRecords))
	// 所有离线消息推送成功后应该被删除
	assert.Equal(t, 0, len(allRecords), "所有离线消息推送成功后应被删除，数据库中应无记录")

	t.Log("========== 测试完成：完整验证了离线消息的真实推送流程 ==========")
}

// startTestWSServerWithParams 启动支持URL参数的WebSocket测试服务器
func startTestWSServerWithParams(t *testing.T, hub *Hub) *httptest.Server {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("WebSocket升级失败: %v", err)
			return
		}

		// 从查询参数读取客户端信息
		query := r.URL.Query()
		userID := query.Get("user_id")
		if userID == "" {
			userID = "test-user-default"
		}

		userTypeStr := query.Get("user_type")
		var userType UserType
		switch userTypeStr {
		case "customer":
			userType = UserTypeCustomer
		case "agent":
			userType = UserTypeAgent
		case "system":
			userType = UserTypeSystem
		default:
			userType = UserTypeCustomer
		}

		clientIP := query.Get("client_ip")
		if clientIP == "" {
			clientIP = "127.0.0.1"
		}

		// 创建客户端
		client := &Client{
			ID:            "test-client-" + userID,
			UserID:        userID,
			UserType:      userType,
			ClientIP:      clientIP,
			Status:        UserStatusOnline,
			ClientType:    ClientTypeWeb,
			SendChan:      make(chan []byte, 256),
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			Context:       context.Background(),
			Metadata:      make(map[string]interface{}),
			Conn:          conn,
		}

		t.Logf("📝 创建客户端: ID=%s, UserID=%s, UserType=%s, IP=%s",
			client.ID, client.UserID, client.UserType, client.ClientIP)

		// 注册到Hub
		hub.Register(client)
	}))

	return srv
}

// TestOfflineMessage30MessagesStressTest 压力测试：用户有30条离线消息，上线后能否正常接收
func TestOfflineMessage30MessagesStressTest(t *testing.T) {
	ctx := context.Background()

	// ========== 阶段0: 准备环境 ==========
	t.Log("========== 阶段0: 初始化 Hub 和存储 ==========")

	// 创建存储
	redisClient := GetTestRedisClient(t)
	db := GetTestDBWithMigration(t, &MessageSendRecord{}, &OfflineMessageRecord{})

	// 创建仓库
	onlineStatusRepo := NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:stress:online:",
		TTL:       5 * time.Minute,
	})
	messageRecordRepo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	// 创建离线消息处理器
	offlineHandler := NewHybridOfflineMessageHandler(redisClient, db, &wscconfig.OfflineMessage{
		KeyPrefix: "wsc:test:stress:msg:",
		QueueTTL:  1 * time.Hour,
		MaxCount:  300, // 允许更多离线消息
	}, NewDefaultWSCLogger())

	// 创建 Hub
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 9091).
		WithMessageBufferSize(512). // 增大缓冲区以处理大量消息
		WithHeartbeatInterval(30 * time.Second)

	hub := NewHub(config)
	hub.SetOnlineStatusRepository(onlineStatusRepo)
	hub.SetMessageRecordRepository(messageRecordRepo)
	hub.SetOfflineMessageHandler(offlineHandler)

	// 启动 Hub
	go hub.Run()
	hub.WaitForStart()
	defer hub.Shutdown()

	// 启动 WebSocket 服务器
	srv := startTestWSServerWithParams(t, hub)
	defer srv.Close()

	// 定义测试用户
	userID := "user-stress-30"

	// 清理测试数据
	defer func() {
		_ = offlineHandler.ClearOfflineMessages(ctx, userID)
	}()

	t.Logf("✅ Hub 已启动，服务地址: %s", srv.URL)

	// ========== 阶段1: 确认用户离线 ==========
	t.Log("========== 阶段1: 确认用户离线 ==========")

	isOnline, _ := onlineStatusRepo.IsUserOnline(ctx, userID)
	assert.False(t, isOnline, "用户应该是离线的")
	t.Log("✅ 确认：用户处于离线状态")

	// ========== 阶段2: 发送30条离线消息 ==========
	t.Log("========== 阶段2: 向离线用户发送30条消息 ==========")

	totalMessages := 30
	startTime := time.Now()

	for i := 1; i <= totalMessages; i++ {
		msgID := time.Now().Format("20060102150405.000000") + "-" + userID
		hubMsg := &HubMessage{
			ID:           msgID,
			MessageID:    msgID,
			MessageType:  MessageTypeText,
			Sender:       "system",
			SenderType:   UserTypeSystem,
			Receiver:     userID,
			ReceiverType: UserTypeCustomer,
			Content:      time.Now().Format("这是第 " + string(rune(i)) + " 条离线消息 - 时间戳: 306-01-02 15:04:05.000"),
			CreateAt:     time.Now(),
			Priority:     PriorityNormal,
		}

		result := hub.SendToUserWithRetry(ctx, userID, hubMsg)
		if result.FinalError != nil && i%50 == 0 {
			t.Logf("⚠️ 第 %d 条消息发送失败: %v", i, result.FinalError)
		}

		// 每50条输出一次进度
		if i%50 == 0 {
			t.Logf("📤 已发送 %d/%d 条消息", i, totalMessages)
		}
	}

	sendDuration := time.Since(startTime)
	t.Logf("✅ 完成发送 %d 条消息，耗时: %v (平均 %.2f ms/条)",
		totalMessages, sendDuration, float64(sendDuration.Milliseconds())/float64(totalMessages))

	// 等待异步存储完成
	time.Sleep(2 * time.Second)

	// ========== 阶段3: 验证离线消息已存储 ==========
	t.Log("========== 阶段3: 验证离线消息已存储到数据库 ==========")

	count, err := offlineHandler.GetOfflineMessageCount(ctx, userID)
	require.NoError(t, err)
	t.Logf("📊 数据库中的离线消息数: %d", count)
	assert.Equal(t, int64(totalMessages), count, "应该有30条离线消息")

	// ========== 阶段4: 用户上线并接收所有离线消息 ==========
	t.Log("========== 阶段4: 用户上线并接收30条离线消息 ==========")

	wsURL := "ws" + srv.URL[len("http"):] + "?user_id=" + userID + "&user_type=customer&client_ip=192.168.1.30"
	client := New(wsURL)
	client.Config.WithAutoReconnect(false)
	defer client.Close()

	var connected atomic.Bool
	var receivedMessages []string
	var messagesMu sync.Mutex
	var firstMessageTime time.Time
	var lastMessageTime time.Time

	client.OnConnected(func() {
		connected.Store(true)
		t.Logf("✅ 用户已连接")
	})

	client.OnTextMessageReceived(func(message string) {
		messagesMu.Lock()
		if len(receivedMessages) == 0 {
			firstMessageTime = time.Now()
		}
		receivedMessages = append(receivedMessages, message)
		lastMessageTime = time.Now()

		// 每50条输出一次进度
		if len(receivedMessages)%50 == 0 {
			t.Logf("📨 已接收 %d 条消息", len(receivedMessages))
		}
		messagesMu.Unlock()
	})

	// 连接
	connectStart := time.Now()
	client.Connect()

	// 等待连接成功
	require.Eventually(t, func() bool {
		return connected.Load()
	}, 5*time.Second, 100*time.Millisecond, "用户应该连接成功")

	connectDuration := time.Since(connectStart)
	t.Logf("✅ 连接建立成功，耗时: %v", connectDuration)

	// 等待离线消息推送完成（给足够的时间）
	t.Log("⏳ 等待离线消息推送...")
	time.Sleep(10 * time.Second)

	// ========== 阶段5: 验证接收结果 ==========
	t.Log("========== 阶段5: 验证接收结果 ==========")

	messagesMu.Lock()
	receivedCount := len(receivedMessages)
	messagesMu.Unlock()

	if !firstMessageTime.IsZero() && !lastMessageTime.IsZero() {
		receiveDuration := lastMessageTime.Sub(firstMessageTime)
		t.Logf("📊 接收统计:")
		t.Logf("  - 总共接收: %d 条消息", receivedCount)
		t.Logf("  - 预期数量: %d 条", totalMessages)
		t.Logf("  - 接收成功率: %.2f%%", float64(receivedCount)/float64(totalMessages)*100)
		t.Logf("  - 接收耗时: %v", receiveDuration)
		t.Logf("  - 平均速率: %.2f 条/秒", float64(receivedCount)/receiveDuration.Seconds())
	}

	// 验证接收到的消息数量
	assert.GreaterOrEqual(t, receivedCount, int(float64(totalMessages)*0.95),
		"应该接收到至少95%%的消息（%d/%d）", receivedCount, totalMessages)

	// ========== 阶段6: 验证数据库状态 ==========
	t.Log("========== 阶段6: 验证数据库最终状态 ==========")

	var remainingRecords []OfflineMessageRecord
	err = db.Where("receiver = ?", userID).Find(&remainingRecords).Error
	require.NoError(t, err)

	t.Logf("📊 数据库状态:")
	t.Logf("  - 剩余未推送记录: %d 条", len(remainingRecords))

	if len(remainingRecords) > 0 {
		// 统计各状态的消息数
		statusCount := make(map[MessageSendStatus]int)
		for _, record := range remainingRecords {
			statusCount[record.Status]++
		}
		t.Logf("  - 状态分布:")
		for status, count := range statusCount {
			t.Logf("    * %s: %d 条", status, count)
		}
	}

	// 验证大部分消息已成功推送
	assert.LessOrEqual(t, len(remainingRecords), int(float64(totalMessages)*0.1),
		"应该有不超过10%%的消息未推送成功")

	// ========== 阶段7: 性能统计 ==========
	t.Log("========== 阶段7: 性能统计总结 ==========")
	t.Logf("📈 整体性能:")
	t.Logf("  - 发送30条消息耗时: %v", sendDuration)
	t.Logf("  - 连接建立耗时: %v", connectDuration)
	t.Logf("  - 消息接收成功率: %.2f%% (%d/%d)",
		float64(receivedCount)/float64(totalMessages)*100, receivedCount, totalMessages)

	t.Log("========== 压力测试完成：成功验证30条离线消息的推送能力 ==========")
}

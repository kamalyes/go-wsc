/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-20 10:29:55
 * @FilePath: \go-wsc\offline_message_handler_test.go
 * @Description: 离线消息处理器测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	testHandlerDBInstance *gorm.DB
	testHandlerRedis      redis.UniversalClient
	testHandlerOnce       sync.Once
)

// 获取测试用数据库连接（单例）
func getTestHandlerDB(t *testing.T) *gorm.DB {
	testHandlerOnce.Do(func() {
		dsn := "root:idev88888@tcp(120.77.38.35:13306)/im_agent?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Silent),
			SkipDefaultTransaction: true,
			PrepareStmt:            true,
		})
		require.NoError(t, err, "数据库连接失败")

		// 自动迁移
		err = db.AutoMigrate(&OfflineMessageRecord{})
		require.NoError(t, err, "数据库迁移失败")

		// 配置连接池
		sqlDB, err := db.DB()
		require.NoError(t, err, "获取底层DB失败")
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetConnMaxLifetime(time.Hour)

		testHandlerDBInstance = db
	})
	return testHandlerDBInstance
}

// 获取测试用 Redis 连接
func getTestHandlerRedis(t *testing.T) redis.UniversalClient {
	if testHandlerRedis == nil {
		testHandlerRedis = redis.NewClient(&redis.Options{
			Addr:     "120.79.25.168:16389",
			Password: "M5Pi9YW6u",
			DB:       1,
		})

		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		_, err := testHandlerRedis.Ping(ctx).Result()
		require.NoError(t, err, "Redis 连接失败")
	}
	return testHandlerRedis
}

// 创建测试用混合处理器
func createTestHybridHandler(t *testing.T) OfflineMessageRepository {
	db := getTestHandlerDB(t)
	redisClient := getTestHandlerRedis(t)

	config := &wscconfig.OfflineMessage{
		KeyPrefix: "test:wsc:offline:",
		QueueTTL:  time.Hour,
	}

	return NewHybridOfflineMessageHandler(redisClient, db, config)
}

// 创建测试消息
func createTestHubOfflineMessage(messageID, sender, receiver, sessionID string) *HubMessage {
	return &HubMessage{
		ID:           messageID,
		MessageID:    messageID,
		MessageType:  MessageTypeText,
		Sender:       sender,
		SenderType:   UserTypeCustomer,
		Receiver:     receiver,
		ReceiverType: UserTypeAgent,
		SessionID:    sessionID,
		Content:      "Test offline message content",
		Data:         map[string]interface{}{"test": "data"},
		CreateAt:     time.Now(),
	}
}

func TestHybridOfflineMessageHandlerStoreAndRetrieve(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-001"
	messageID := osx.HashUnixMicroCipherText()
	msg := createTestHubOfflineMessage(messageID, "sender-001", userID, "session-001")

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 清理测试数据
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	// 存储离线消息
	err := handler.StoreOfflineMessage(ctx, userID, msg)
	assert.NoError(t, err)

	// 获取离线消息
	messages, _, err := handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 1)
	assert.Equal(t, messageID, messages[0].ID)
	assert.Equal(t, msg.Content, messages[0].Content)
}

func TestHybridOfflineMessageHandlerGetOfflineMessageCount(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-002"

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 清理测试数据
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	// 初始数量应该为0
	count, err := handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// 存储3条消息
	for i := 0; i < 3; i++ {
		messageID := osx.HashUnixMicroCipherText()
		msg := createTestHubOfflineMessage(messageID, "sender-001", userID, "session-001")
		err := handler.StoreOfflineMessage(ctx, userID, msg)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	// 验证数量
	count, err = handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestHybridOfflineMessageHandlerDeleteOfflineMessages(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-003"
	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 清理测试数据
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	// 存储2条消息
	for _, msgID := range messageIDs {
		msg := createTestHubOfflineMessage(msgID, "sender-001", userID, "session-001")
		err := handler.StoreOfflineMessage(ctx, userID, msg)
		require.NoError(t, err)
	}

	// 从 Redis 获取消息(出队)
	messages, _, err := handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 2, "应该获取到2条消息")

	// 删除第一条消息(从 MySQL)
	err = handler.DeleteOfflineMessages(ctx, userID, []string{messageIDs[0]})
	assert.NoError(t, err)

	// 从 MySQL 获取剩余消息
	messages, _, err = handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 1, "应该剩余1条消息")
	assert.Equal(t, messageIDs[1], messages[0].ID)
}

func TestHybridOfflineMessageHandlerMarkAsPushed(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-004"
	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 清理测试数据
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	// 存储3条消息到 Redis + MySQL
	for _, msgID := range messageIDs {
		msg := createTestHubOfflineMessage(msgID, "sender-001", userID, "session-001")
		err := handler.StoreOfflineMessage(ctx, userID, msg)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	// 第一次获取：从 Redis 出队所有消息
	messages, _, err := handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 3, "第一次从 Redis 获取3条消息")

	// 模拟推送：前2条成功,最后1条失败
	// 标记前2条为已推送
	err = handler.MarkAsPushed(ctx, messageIDs[:2])
	assert.NoError(t, err)

	// 第二次获取：Redis 已空,从 MySQL 读取,应该只返回未推送的消息
	messages, _, err = handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 1, "第二次从 MySQL 获取,只有1条未推送的消息")
	assert.Equal(t, messageIDs[2], messages[0].ID)

	// 统计数量：MySQL 中只有1条未推送的消息
	count, err := handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count, "统计数量为1(只包含未推送的消息)")
}

func TestHybridOfflineMessageHandlerMarkAsPushedAll(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-005"
	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 清理测试数据
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	// 存储消息
	for _, msgID := range messageIDs {
		msg := createTestHubOfflineMessage(msgID, "sender-001", userID, "session-001")
		err := handler.StoreOfflineMessage(ctx, userID, msg)
		require.NoError(t, err)
	}

	// 第一次获取：从 Redis 出队
	messages, _, err := handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 2, "从 Redis 获取2条消息")

	// 标记所有消息为已推送
	err = handler.MarkAsPushed(ctx, messageIDs)
	assert.NoError(t, err)

	// 第二次获取：从 MySQL 读取,所有消息都已推送,返回空
	messages, _, err = handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 0, "所有消息都已推送，应该返回空列表")

	// 验证统计数量为0
	count, err := handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestHybridOfflineMessageHandlerMarkAsPushedEmptyList(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	// 空列表不应该报错
	err := handler.MarkAsPushed(ctx, []string{})
	assert.NoError(t, err)
}

func TestHybridOfflineMessageHandlerClearOfflineMessages(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-006"

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 存储多条消息
	for i := 0; i < 3; i++ {
		messageID := osx.HashUnixMicroCipherText()
		msg := createTestHubOfflineMessage(messageID, "sender-001", userID, "session-001")
		err := handler.StoreOfflineMessage(ctx, userID, msg)
		require.NoError(t, err)
	}

	// 验证存储成功
	count, err := handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, count, int64(3))

	// 清空消息
	err = handler.ClearOfflineMessages(ctx, userID)
	assert.NoError(t, err)

	// 验证清空后数量为0
	count, err = handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestHybridOfflineMessageHandlerConcurrentStoreAndMarkAsPushed(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-007"
	concurrency := 10

	messageIDs := make([]string, concurrency)
	for i := 0; i < concurrency; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 清理测试数据
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	// 并发存储消息
	var wg sync.WaitGroup
	errChan := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := createTestHubOfflineMessage(messageIDs[idx], "sender-001", userID, "session-001")
			if err := handler.StoreOfflineMessage(ctx, userID, msg); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// 检查是否有错误
	for err := range errChan {
		assert.NoError(t, err)
	}

	// 第一次获取：从 Redis 出队所有消息
	messages, _, err := handler.GetOfflineMessages(ctx, userID, 100, "")
	assert.NoError(t, err)
	assert.Len(t, messages, concurrency, "应该获取到所有消息")

	// 并发标记为已推送
	errChan2 := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := handler.MarkAsPushed(ctx, []string{messageIDs[idx]}); err != nil {
				errChan2 <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan2)

	// 检查是否有错误
	for err := range errChan2 {
		assert.NoError(t, err)
	}

	// 第二次获取：从 MySQL 读取,所有消息都已推送
	count, err := handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count, "所有消息都应该已推送")
}

func TestHybridOfflineMessageHandlerMarkAsPushedNonExistent(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	// 标记不存在的消息不应该报错
	err := handler.MarkAsPushed(ctx, []string{"non-existent-msg-id"})
	assert.NoError(t, err)
}

func TestHybridOfflineMessageHandlerPartialMarkAsPushed(t *testing.T) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-008"
	messageIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 先清理历史测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)

	// 清理测试数据
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	// 存储5条消息
	for _, msgID := range messageIDs {
		msg := createTestHubOfflineMessage(msgID, "sender-001", userID, "session-001")
		err := handler.StoreOfflineMessage(ctx, userID, msg)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	// 第一次获取：从 Redis 出队所有消息
	messages, _, err := handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 5, "从 Redis 获取5条消息")

	// 标记中间3条为已推送
	err = handler.MarkAsPushed(ctx, messageIDs[1:4])
	assert.NoError(t, err)

	// 第二次获取：从 MySQL 读取,应该只有2条未推送的消息
	messages, _, err = handler.GetOfflineMessages(ctx, userID, 10, "")
	assert.NoError(t, err)
	assert.Len(t, messages, 2, "从 MySQL 获取2条未推送的消息")

	// 验证未推送的消息ID
	unpushedIDs := []string{messages[0].ID, messages[1].ID}
	assert.Contains(t, unpushedIDs, messageIDs[0])
	assert.Contains(t, unpushedIDs, messageIDs[4])
}

// TestHybridOfflineMessageHandlerCursorMultipleScales 测试不同数据量的游标分页
func TestHybridOfflineMessageHandlerCursorMultipleScales(t *testing.T) {
	// 测试场景配置
	testCases := map[string]struct {
		totalMessages int
		batchSize     int
	}{
		"100": {totalMessages: 100, batchSize: 30},
		"300": {totalMessages: 300, batchSize: 50},
		"1K":  {totalMessages: 1000, batchSize: 100},
		// "3K":  {totalMessages: 3000, batchSize: 100},
		// "10K": {totalMessages: 10000, batchSize: 500},
		// "20K": {totalMessages: 20000, batchSize: 1000},
		// "50K": {totalMessages: 50000, batchSize: 2000},
		// "100K": {totalMessages: 100000, batchSize: 5000},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			testCursorPagination(t, tc.totalMessages, tc.batchSize)
		})
	}
}

// testCursorPagination 通用的游标分页测试函数
func testCursorPagination(t *testing.T, totalMessages, batchSize int) {
	handler := createTestHybridHandler(t)
	ctx := context.Background()

	userID := "test-user-cursor-" + osx.HashUnixMicroCipherText()[:8]

	// 清理测试数据
	_ = handler.ClearOfflineMessages(ctx, userID)
	defer func() {
		_ = handler.ClearOfflineMessages(ctx, userID)
	}()

	t.Logf("开始批量存储 %d 条消息...", totalMessages)
	startTime := time.Now()

	// 批量存储消息（使用批量插入优化）
	messageIDs := make([]string, totalMessages)
	batchInsertSize := 1000 // 每次批量插入1000条

	for batchStart := 0; batchStart < totalMessages; batchStart += batchInsertSize {
		batchEnd := batchStart + batchInsertSize
		if batchEnd > totalMessages {
			batchEnd = totalMessages
		}

		records := make([]*OfflineMessageRecord, 0, batchEnd-batchStart)

		for i := batchStart; i < batchEnd; i++ {
			messageIDs[i] = osx.HashUnixMicroCipherText()
			msg := createTestHubOfflineMessage(messageIDs[i], "sender-001", userID, "session-001")

			// 使用 zipx 压缩数据
			compressedData, _, _ := zipx.ZlibCompressObjectWithSize(msg)

			record := &OfflineMessageRecord{
				MessageID:      messageIDs[i],
				Sender:         "sender-001",
				Receiver:       userID,
				SessionID:      "session-001",
				CompressedData: compressedData,
				ScheduledAt:    msg.CreateAt,
				ExpireAt:       msg.CreateAt.Add(7 * 24 * time.Hour),
				CreatedAt:      time.Now(),
			}
			records = append(records, record)

			// 同时加入Redis
			_ = handler.(*HybridOfflineMessageHandler).queueRepo.Enqueue(ctx, userID, msg)
		}

		// 批量插入MySQL
		if len(records) > 0 {
			db := getTestHandlerDB(t)
			err := db.CreateInBatches(records, 1000).Error
			require.NoError(t, err)
		}

		t.Logf("已批量存储 %d/%d 条消息", batchEnd, totalMessages)
	}

	storeTime := time.Since(startTime)
	t.Logf("存储完成，耗时: %v, 平均每条: %v", storeTime, storeTime/time.Duration(totalMessages))

	// 验证总数
	count, err := handler.GetOfflineMessageCount(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(totalMessages), count)

	// 分批读取所有消息
	t.Logf("开始分批读取消息，每批 %d 条...", batchSize)
	readStartTime := time.Now()

	allMessages := make([]*HubMessage, 0, totalMessages)
	cursor := ""
	batchCount := 0

	for {
		messages, nextCursor, err := handler.GetOfflineMessages(ctx, userID, batchSize, cursor)
		assert.NoError(t, err)

		if len(messages) == 0 {
			break
		}

		allMessages = append(allMessages, messages...)
		batchCount++

		if batchCount%10 == 0 {
			t.Logf("已读取 %d 批次，累计 %d 条消息", batchCount, len(allMessages))
		}

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	readTime := time.Since(readStartTime)
	t.Logf("读取完成，共 %d 批次，获取 %d 条消息，耗时: %v", batchCount, len(allMessages), readTime)

	// 验证读取的消息数量
	assert.Equal(t, totalMessages, len(allMessages), "应该读取到所有消息")

	// 验证没有重复消息
	messageIDSet := make(map[string]bool)
	for _, msg := range allMessages {
		assert.False(t, messageIDSet[msg.ID], "不应该有重复消息: %s", msg.ID)
		messageIDSet[msg.ID] = true
	}

	// 验证消息顺序（按 created_at 升序）
	for i := 1; i < len(allMessages); i++ {
		assert.True(t, !allMessages[i].CreateAt.Before(allMessages[i-1].CreateAt),
			"消息应该按时间升序排列")
	}

	t.Logf("性能统计: 存储耗时=%v, 读取耗时=%v, 总耗时=%v",
		storeTime, readTime, storeTime+readTime)
}

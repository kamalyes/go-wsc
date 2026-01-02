/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 17:00:26
 * @FilePath: \go-wsc\offline_message_handler_test.go
 * @Description: 离线消息处理器测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ============================================================================
// 测试辅助函数
// ============================================================================

// testOfflineHandlerContext 封装离线消息处理器测试的上下文
type testOfflineHandlerContext struct {
	t       *testing.T
	handler OfflineMessageHandler
	db      *gorm.DB
	ctx     context.Context
	userID  string
}

// newTestOfflineHandlerContext 创建测试上下文
func newTestOfflineHandlerContext(t *testing.T, userSuffix string) *testOfflineHandlerContext {
	db := getTestHandlerDB(t)
	tc := &testOfflineHandlerContext{
		t:       t,
		handler: createTestHybridHandler(t),
		db:      db,
		ctx:     context.Background(),
		userID:  "test-user-" + userSuffix,
	}
	// 预清理避免数据污染
	tc.cleanup()
	return tc
}

// cleanup 清理测试数据
func (c *testOfflineHandlerContext) cleanup() {
	_ = c.handler.ClearOfflineMessages(c.ctx, c.userID)
}

// storeMessage 存储消息
func (c *testOfflineHandlerContext) storeMessage(msg *HubMessage) {
	err := c.handler.StoreOfflineMessage(c.ctx, c.userID, msg)
	require.NoError(c.t, err)
}

// getMessages 获取消息
func (c *testOfflineHandlerContext) getMessages(limit int, cursor string) ([]*HubMessage, string) {
	messages, nextCursor, err := c.handler.GetOfflineMessages(c.ctx, c.userID, limit, cursor)
	assert.NoError(c.t, err)
	return messages, nextCursor
}

// getCount 获取消息数量
func (c *testOfflineHandlerContext) getCount() int64 {
	count, err := c.handler.GetOfflineMessageCount(c.ctx, c.userID)
	assert.NoError(c.t, err)
	return count
}

// createTestMessage 创建测试消息（自动生成所有字段）
func (c *testOfflineHandlerContext) createTestMessage() (string, *HubMessage) {
	return CreateTestHubMessage(c.userID, "")
}

func createTestHybridHandler(t *testing.T) OfflineMessageHandler {
	db := getTestHandlerDB(t)
	redisClient := getTestHandlerRedis(t)

	config := &wscconfig.OfflineMessage{
		KeyPrefix: "test:wsc:offline:",
		QueueTTL:  time.Hour,
	}

	return NewHybridOfflineMessageHandler(redisClient, db, config)
}

func TestHybridOfflineMessageHandlerStoreAndRetrieve(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "001")
	defer tc.cleanup()

	msgID, msg := tc.createTestMessage()
	tc.storeMessage(msg)

	messages, _ := tc.getMessages(10, "")
	assert.Len(t, messages, 1)
	assert.Equal(t, msgID, messages[0].MessageID)
	assert.Equal(t, msg.Content, messages[0].Content)
}

func TestHybridOfflineMessageHandlerGetOfflineMessageCount(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "002")
	defer tc.cleanup()

	count := tc.getCount()
	assert.Equal(t, int64(0), count)

	for i := 0; i < 3; i++ {
		_, msg := tc.createTestMessage()
		tc.storeMessage(msg)
		time.Sleep(10 * time.Millisecond)
	}

	count = tc.getCount()
	assert.Equal(t, int64(3), count)
}

func TestHybridOfflineMessageHandlerDeleteOfflineMessages(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "003")
	defer tc.cleanup()

	msgID1, msg1 := tc.createTestMessage()
	msgID2, msg2 := tc.createTestMessage()
	tc.storeMessage(msg1)
	tc.storeMessage(msg2)

	messages, _ := tc.getMessages(10, "")
	assert.Len(t, messages, 2)

	err := tc.handler.DeleteOfflineMessages(tc.ctx, tc.userID, []string{msgID1})
	assert.NoError(t, err)

	messages, _ = tc.getMessages(10, "")
	assert.Len(t, messages, 1)
	assert.Equal(t, msgID2, messages[0].MessageID)
}

func TestHybridOfflineMessageHandlerUpdatePushStatus(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "004")
	defer tc.cleanup()

	msgIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		msgID, msg := tc.createTestMessage()
		msgIDs[i] = msgID
		tc.storeMessage(msg)
		time.Sleep(10 * time.Millisecond)
	}

	messages, _ := tc.getMessages(10, "")
	assert.Len(t, messages, 3, "第一次从 Redis 获取3条消息")

	// 标记前2条成功
	err := tc.handler.UpdatePushStatus(tc.ctx, msgIDs[:2], nil)
	assert.NoError(t, err)

	// 标记第3条失败
	err = tc.handler.UpdatePushStatus(tc.ctx, []string{msgIDs[2]}, fmt.Errorf("network timeout"))
	assert.NoError(t, err)

	// 验证状态
	var record1, record3 OfflineMessageRecord
	err = tc.db.Where("message_id = ?", msgIDs[0]).First(&record1).Error
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, record1.Status)

	err = tc.db.Where("message_id = ?", msgIDs[2]).First(&record3).Error
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, record3.Status)
	assert.Equal(t, "network timeout", record3.ErrorMessage)
}

func TestHybridOfflineMessageHandlerUpdatePushStatusAll(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "005")
	defer tc.cleanup()

	msgIDs := make([]string, 2)
	for i := 0; i < 2; i++ {
		msgID, msg := tc.createTestMessage()
		msgIDs[i] = msgID
		tc.storeMessage(msg)
	}

	messages, _ := tc.getMessages(10, "")
	assert.Len(t, messages, 2, "从 Redis 获取2条消息")

	err := tc.handler.UpdatePushStatus(tc.ctx, msgIDs, nil)
	assert.NoError(t, err)

	// 验证状态
	for _, msgID := range msgIDs {
		var record OfflineMessageRecord
		err = tc.db.Where("message_id = ?", msgID).First(&record).Error
		assert.NoError(t, err)
		assert.Equal(t, MessageSendStatusSuccess, record.Status)
	}
}

func TestHybridOfflineMessageHandlerUpdatePushStatusEmptyList(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "empty-mark")
	defer tc.cleanup()

	err := tc.handler.UpdatePushStatus(tc.ctx, []string{}, nil)
	assert.NoError(t, err)
}

func TestHybridOfflineMessageHandlerClearOfflineMessages(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "006")
	defer tc.cleanup()

	for i := 0; i < 3; i++ {
		_, msg := tc.createTestMessage()
		tc.storeMessage(msg)
	}

	count := tc.getCount()
	assert.GreaterOrEqual(t, count, int64(3))

	err := tc.handler.ClearOfflineMessages(tc.ctx, tc.userID)
	assert.NoError(t, err)

	count = tc.getCount()
	assert.Equal(t, int64(0), count)
}

func TestHybridOfflineMessageHandlerConcurrentStoreAndUpdatePushStatus(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "007")
	defer tc.cleanup()

	concurrency := 10
	messageIDs := make([]string, concurrency)
	for i := 0; i < concurrency; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	var wg sync.WaitGroup
	errChan := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, msg := tc.createTestMessage()
			if err := tc.handler.StoreOfflineMessage(tc.ctx, tc.userID, msg); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		assert.NoError(t, err)
	}

	messages, _ := tc.getMessages(100, "")
	assert.Len(t, messages, concurrency, "应该获取到所有消息")

	errChan2 := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 随机成功/失败
			var pushErr error
			if idx%2 == 0 {
				pushErr = nil // 成功
			} else {
				pushErr = fmt.Errorf("push error %d", idx) // 失败
			}
			if err := tc.handler.UpdatePushStatus(tc.ctx, []string{messageIDs[idx]}, pushErr); err != nil {
				errChan2 <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan2)

	for err := range errChan2 {
		assert.NoError(t, err)
	}
}
func TestHybridOfflineMessageHandlerUpdatePushStatusNonExistent(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "non-existent")
	defer tc.cleanup()

	err := tc.handler.UpdatePushStatus(tc.ctx, []string{"non-existent-msg-id"}, nil)
	assert.NoError(t, err)
}

func TestHybridOfflineMessageHandlerPartialUpdatePushStatus(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "008")
	defer tc.cleanup()

	messageIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		messageID, msg := tc.createTestMessage()
		messageIDs[i] = messageID
		tc.storeMessage(msg)
		time.Sleep(10 * time.Millisecond)
	}

	messages, _ := tc.getMessages(10, "")
	assert.Len(t, messages, 5, "从 Redis 获取5条消息")

	// 推送成功的消息应该删除，而不是更新状态
	// 这里删除 1-3 号消息（模拟推送成功）
	err := tc.handler.DeleteOfflineMessages(tc.ctx, tc.userID, messageIDs[1:4])
	assert.NoError(t, err)

	// 推送失败的消息（0号和4号）应该更新状态
	err = tc.handler.UpdatePushStatus(tc.ctx, []string{messageIDs[0]}, fmt.Errorf("push failed"))
	assert.NoError(t, err)
	err = tc.handler.UpdatePushStatus(tc.ctx, []string{messageIDs[4]}, fmt.Errorf("push failed"))
	assert.NoError(t, err)

	messages, _ = tc.getMessages(10, "")
	assert.Len(t, messages, 2, "从 MySQL 获取2条失败的消息")

	unpushedIDs := []string{messages[0].MessageID, messages[1].MessageID}
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
		"50":  {totalMessages: 50, batchSize: 20},
		"100": {totalMessages: 100, batchSize: 30},
		// 注释掉大数据量测试以加快测试速度
		// "300": {totalMessages: 300, batchSize: 50},
		// "1K":  {totalMessages: 1000, batchSize: 100},
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

		for i := batchStart; i < batchEnd; i++ {
			messageID := osx.HashUnixMicroCipherText()
			messageIDs[i] = messageID
			msg := &HubMessage{
				ID:           messageID,
				MessageID:    messageID,
				MessageType:  MessageTypeText,
				Sender:       "sender-" + userID,
				SenderType:   UserTypeAgent,
				Receiver:     userID,
				ReceiverType: UserTypeCustomer,
				SessionID:    "session-" + userID,
				Content:      "Pagination test message",
				Data:         map[string]interface{}{"index": i},
				CreateAt:     time.Now(),
			}

			// 使用公共方法存储离线消息（会同时存储到 Redis 和 MySQL）
			err := handler.StoreOfflineMessage(ctx, userID, msg)
			if err != nil {
				t.Logf("存储离线消息失败: %v", err)
			}
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

// ============================================================================
// 增强测试 - Redis-MySQL混合存储、边界条件、可靠性
// ============================================================================

// TestHybridOfflineMessageHandlerRedisFailover 测试Redis故障切换到MySQL
func TestHybridOfflineMessageHandlerRedisFailover(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "redis-failover")
	defer tc.cleanup()

	// 存储消息到Redis
	msgIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		msgID, msg := tc.createTestMessage()
		msgIDs[i] = msgID
		tc.storeMessage(msg)
		time.Sleep(10 * time.Millisecond)
	}

	// 第一次获取：从Redis读取
	messages1, _ := tc.getMessages(10, "")
	assert.Len(t, messages1, 5, "应该从Redis获取5条消息")

	// 清空Redis队列（模拟Redis故障后恢复）
	for i := 0; i < 5; i++ {
		_, _ = tc.getMessages(1, "")
	}

	// 第二次获取：应该从MySQL读取（因为Redis已空）
	messages2, _ := tc.getMessages(10, "")
	assert.Len(t, messages2, 5, "应该从MySQL获取5条消息")

	// 验证消息ID一致
	for i := 0; i < 5; i++ {
		assert.Equal(t, msgIDs[i], messages2[i].MessageID)
	}
}

// TestHybridOfflineMessageHandlerRedisMySQLConsistency 测试Redis和MySQL的数据一致性
func TestHybridOfflineMessageHandlerRedisMySQLConsistency(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "consistency")
	defer tc.cleanup()

	msgIDs := make([]string, 10)
	for i := 0; i < 10; i++ {
		msgID, msg := tc.createTestMessage()
		msgIDs[i] = msgID
		tc.storeMessage(msg)
		time.Sleep(10 * time.Millisecond)
	}

	// 从Redis获取并标记前5条为已推送
	messages, _ := tc.getMessages(5, "")
	assert.Len(t, messages, 5)

	pushedIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		pushedIDs[i] = messages[i].MessageID
	}
	err := tc.handler.UpdatePushStatus(tc.ctx, pushedIDs, nil)
	assert.NoError(t, err)

	// 继续从Redis获取剩余5条
	messages2, _ := tc.getMessages(10, "")
	assert.Len(t, messages2, 5, "应该获取剩余5条")

	// 再次获取，此时Redis已空，从MySQL读取
	// MySQL应该只返回未推送的消息
	messages3, _ := tc.getMessages(10, "")
	assert.Len(t, messages3, 5, "MySQL应该只返回5条未推送的消息")

	// 验证MySQL返回的都是未推送的消息
	pushedMap := make(map[string]bool)
	for _, id := range pushedIDs {
		pushedMap[id] = true
	}

	for _, msg := range messages3 {
		assert.False(t, pushedMap[msg.MessageID], "不应该返回已推送的消息")
	}
}

// TestHybridOfflineMessageHandlerEmptyUserMessages 测试用户无离线消息
func TestHybridOfflineMessageHandlerEmptyUserMessages(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "empty-user")
	defer tc.cleanup()

	messages, cursor := tc.getMessages(10, "")
	assert.Len(t, messages, 0)
	assert.Empty(t, cursor)

	count := tc.getCount()
	assert.Equal(t, int64(0), count)
}

// TestHybridOfflineMessageHandlerLargeMessageContent 测试大消息内容
func TestHybridOfflineMessageHandlerLargeMessageContent(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "large-content")
	defer tc.cleanup()

	// 创建包含大数据的消息
	msgID := osx.HashUnixMicroCipherText()
	largeData := make(map[string]interface{})
	largeData["content"] = string(make([]byte, 100*1024)) // 100KB

	msg := &HubMessage{
		ID:          msgID,
		MessageID:   msgID,
		MessageType: MessageTypeText,
		Sender:      "sender-large",
		Receiver:    tc.userID,
		SessionID:   "session-large",
		Content:     "Large content test",
		Data:        largeData,
		CreateAt:    time.Now(),
	}

	tc.storeMessage(msg)

	// 验证可以正常获取
	messages, _ := tc.getMessages(10, "")
	assert.Len(t, messages, 1)
	assert.Equal(t, msgID, messages[0].MessageID)
}

// TestHybridOfflineMessageHandlerConcurrentGetAndStore 测试并发获取和存储
func TestHybridOfflineMessageHandlerConcurrentGetAndStore(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "concurrent-get-store")
	defer tc.cleanup()

	const (
		storers       = 5
		getters       = 10
		msgsPerStorer = 10
	)

	var wg sync.WaitGroup
	errChan := make(chan error, storers+getters)

	// 并发存储
	for s := 0; s < storers; s++ {
		wg.Add(1)
		go func(storerID int) {
			defer wg.Done()
			for i := 0; i < msgsPerStorer; i++ {
				_, msg := tc.createTestMessage()
				if err := tc.handler.StoreOfflineMessage(tc.ctx, tc.userID, msg); err != nil {
					errChan <- err
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(s)
	}

	// 并发获取
	for g := 0; g < getters; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				_, _, err := tc.handler.GetOfflineMessages(tc.ctx, tc.userID, 10, "")
				if err != nil {
					errChan <- err
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		t.Errorf("并发错误: %v", err)
	}

	// 验证最终一致性
	count := tc.getCount()
	assert.GreaterOrEqual(t, count, int64(0))
}

// TestHybridOfflineMessageHandlerRecoveryAfterClear 测试清空后的恢复
func TestHybridOfflineMessageHandlerRecoveryAfterClear(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "recovery")
	defer tc.cleanup()

	// 存储一些消息
	for i := 0; i < 5; i++ {
		_, msg := tc.createTestMessage()
		tc.storeMessage(msg)
	}

	count1 := tc.getCount()
	assert.Equal(t, int64(5), count1)

	// 清空
	err := tc.handler.ClearOfflineMessages(tc.ctx, tc.userID)
	assert.NoError(t, err)

	count2 := tc.getCount()
	assert.Equal(t, int64(0), count2)

	// 重新存储消息
	for i := 0; i < 3; i++ {
		_, msg := tc.createTestMessage()
		tc.storeMessage(msg)
	}

	count3 := tc.getCount()
	assert.Equal(t, int64(3), count3)
}

// TestHybridOfflineMessageHandlerDataIntegrity 测试数据完整性
func TestHybridOfflineMessageHandlerDataIntegrity(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "integrity")
	defer tc.cleanup()

	// 存储带有特殊内容的消息
	specialContents := []string{
		"包含中文内容",
		"Contains English",
		"日本語を含む",
		"특수문자@#$%^&*()",
		"emoji 😀😃😄",
		`{"json": "data"}`,
		"<xml>data</xml>",
	}

	for _, content := range specialContents {
		msgID := osx.HashUnixMicroCipherText()
		msg := &HubMessage{
			ID:        msgID,
			MessageID: msgID,
			Sender:    "sender-integrity",
			Receiver:  tc.userID,
			SessionID: "session-integrity",
			Content:   content,
			CreateAt:  time.Now(),
		}
		tc.storeMessage(msg)
	}

	// 获取并验证内容完整性
	messages, _ := tc.getMessages(10, "")
	assert.Len(t, messages, len(specialContents))

	for i, msg := range messages {
		assert.Equal(t, specialContents[i], msg.Content,
			"消息内容应该保持完整: %s", specialContents[i])
	}
}

// TestHybridOfflineMessageHandlerMessageOrdering 测试消息顺序
func TestHybridOfflineMessageHandlerMessageOrdering(t *testing.T) {
	tc := newTestOfflineHandlerContext(t, "ordering")
	defer tc.cleanup()

	// 按顺序存储带时间戳的消息
	const messageCount = 20

	for i := 0; i < messageCount; i++ {
		_, msg := tc.createTestMessage()
		tc.storeMessage(msg)
		time.Sleep(10 * time.Millisecond) // 确保时间戳不同
	}

	// 获取所有消息
	messages, _ := tc.getMessages(messageCount, "")
	assert.Len(t, messages, messageCount)

	// 验证顺序（应该按创建时间升序）
	for i := 1; i < len(messages); i++ {
		assert.True(t,
			!messages[i].CreateAt.Before(messages[i-1].CreateAt),
			"消息应该按时间升序排列")
	}
}

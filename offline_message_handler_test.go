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
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 测试上下文
// ============================================================================

// testOfflineHandlerContext 离线消息处理器测试上下文
type testOfflineHandlerContext struct {
	t              *testing.T
	handler        OfflineMessageHandler
	ctx            context.Context
	idGen          IDGenerator
	cleanupUserIDs []string
}

// newTestOfflineHandlerContext 创建离线消息处理器测试上下文
func newTestOfflineHandlerContext(t *testing.T) *testOfflineHandlerContext {
	redisClient := GetTestRedisClient(t)
	db := GetTestDBWithMigration(t, &OfflineMessageRecord{})

	config := &wscconfig.OfflineMessage{
		KeyPrefix: "wsc:test:offline:handler:",
		QueueTTL:  1 * time.Hour,
	}

	handler := NewHybridOfflineMessageHandler(redisClient, db, config, NewDefaultWSCLogger())

	return &testOfflineHandlerContext{
		t:              t,
		handler:        handler,
		ctx:            context.Background(),
		idGen:          idgen.NewIDGenerator(idgen.GeneratorTypeNanoID),
		cleanupUserIDs: make([]string, 0),
	}
}

// cleanup 清理测试数据
func (c *testOfflineHandlerContext) cleanup() {
	for _, userID := range c.cleanupUserIDs {
		_ = c.handler.ClearOfflineMessages(c.ctx, userID)
	}
}

// createTestMessage 创建测试消息
func (c *testOfflineHandlerContext) createTestMessage(receiver string) *HubMessage {
	msg := createTestHubMessage(MessageTypeText)
	msg.Receiver = receiver
	msg.ReceiverType = UserTypeCustomer
	msg.CreateAt = time.Now()
	return msg
}

// ============================================================================
// 存储离线消息测试
// ============================================================================

// TestOfflineMessageHandler_StoreOfflineMessage 测试存储离线消息
func TestOfflineMessageHandler_StoreOfflineMessage(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("成功存储离线消息", func(t *testing.T) {
		msg := tc.createTestMessage(userID)

		err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
		assert.NoError(t, err)

		// 验证消息已存储
		count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		assert.NoError(t, err)
		assert.Greater(t, count, int64(0))
	})

	t.Run("存储多条离线消息", func(t *testing.T) {
		userID2 := tc.idGen.GenerateCorrelationID()
		tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID2)

		// 存储5条消息
		for i := 0; i < 5; i++ {
			msg := tc.createTestMessage(userID2)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID2, msg)
			require.NoError(t, err)
		}

		// 验证数量
		count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID2)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, count, int64(5))
	})

	t.Run("拒绝存储系统消息", func(t *testing.T) {
		userID3 := tc.idGen.GenerateCorrelationID()
		tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID3)

		// 创建系统消息
		msg := tc.createTestMessage(userID3)
		msg.MessageType = MessageTypeSystem

		err := tc.handler.StoreOfflineMessage(tc.ctx, userID3, msg)
		assert.NoError(t, err) // 系统消息会被跳过，不报错

		// 验证系统消息未存储
		count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID3)
		assert.NoError(t, err)
		assert.Zero(t, count)
	})

	t.Run("处理nil消息", func(t *testing.T) {
		err := tc.handler.StoreOfflineMessage(tc.ctx, userID, nil)
		assert.Error(t, err)
	})
}

// ============================================================================
// 获取离线消息测试
// ============================================================================

// TestOfflineMessageHandler_GetOfflineMessages 测试获取离线消息
func TestOfflineMessageHandler_GetOfflineMessages(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("获取离线消息-无消息", func(t *testing.T) {
		messages, cursor, err := tc.handler.GetOfflineMessages(tc.ctx, userID, 10, "")
		assert.NoError(t, err)
		assert.Empty(t, messages)
		assert.Empty(t, cursor)
	})

	t.Run("获取离线消息-有消息", func(t *testing.T) {
		userID2 := tc.idGen.GenerateCorrelationID()
		tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID2)

		// 存储3条消息
		for i := 0; i < 3; i++ {
			msg := tc.createTestMessage(userID2)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID2, msg)
			require.NoError(t, err)
		}

		// 获取消息
		messages, cursor, err := tc.handler.GetOfflineMessages(tc.ctx, userID2, 10, "")
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(messages), 3)
		t.Logf("获取到 %d 条消息, cursor: %s", len(messages), cursor)
	})

	t.Run("获取离线消息-限制数量", func(t *testing.T) {
		userID3 := tc.idGen.GenerateCorrelationID()
		tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID3)

		// 存储10条消息
		for i := 0; i < 10; i++ {
			msg := tc.createTestMessage(userID3)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID3, msg)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond) // 确保顺序
		}

		// 限制获取5条
		messages, cursor, err := tc.handler.GetOfflineMessages(tc.ctx, userID3, 5, "")
		assert.NoError(t, err)
		assert.LessOrEqual(t, len(messages), 5)

		// 如果有游标，获取下一页
		if cursor != "" {
			nextMessages, _, err := tc.handler.GetOfflineMessages(tc.ctx, userID3, 5, cursor)
			assert.NoError(t, err)
			assert.NotEmpty(t, nextMessages)
			t.Logf("第二页获取到 %d 条消息", len(nextMessages))
		}
	})

	t.Run("获取离线消息-使用游标分页", func(t *testing.T) {
		userID4 := tc.idGen.GenerateCorrelationID()
		tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID4)

		// 存储20条消息
		for i := 0; i < 20; i++ {
			msg := tc.createTestMessage(userID4)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID4, msg)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond)
		}

		allMessages := make([]*HubMessage, 0)
		cursor := ""
		pageCount := 0

		// 循环分页获取
		for {
			messages, nextCursor, err := tc.handler.GetOfflineMessages(tc.ctx, userID4, 5, cursor)
			assert.NoError(t, err)

			if len(messages) == 0 {
				break
			}

			allMessages = append(allMessages, messages...)
			pageCount++

			if nextCursor == "" {
				break
			}

			cursor = nextCursor

			// 防止无限循环
			if pageCount > 10 {
				break
			}
		}

		t.Logf("分页获取完成: 共 %d 页, %d 条消息", pageCount, len(allMessages))
		assert.GreaterOrEqual(t, len(allMessages), 20)
	})
}

// ============================================================================
// 删除离线消息测试
// ============================================================================

// TestOfflineMessageHandler_DeleteOfflineMessages 测试删除离线消息
func TestOfflineMessageHandler_DeleteOfflineMessages(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("删除指定的离线消息", func(t *testing.T) {
		// 存储5条消息
		var messageIDs []string
		for i := 0; i < 5; i++ {
			msg := tc.createTestMessage(userID)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
			require.NoError(t, err)
			messageIDs = append(messageIDs, msg.MessageID)
		}

		// 删除前3条
		err := tc.handler.DeleteOfflineMessages(tc.ctx, userID, messageIDs[:3])
		assert.NoError(t, err)

		// 验证数量减少
		count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		assert.NoError(t, err)
		t.Logf("删除后剩余消息数量: %d", count)
	})

	t.Run("删除空列表", func(t *testing.T) {
		err := tc.handler.DeleteOfflineMessages(tc.ctx, userID, []string{})
		assert.NoError(t, err)
	})

	t.Run("删除不存在的消息ID", func(t *testing.T) {
		err := tc.handler.DeleteOfflineMessages(tc.ctx, userID, []string{"non-existent-id"})
		assert.NoError(t, err)
	})
}

// ============================================================================
// 统计测试
// ============================================================================

// TestOfflineMessageHandler_GetOfflineMessageCount 测试获取离线消息数量
func TestOfflineMessageHandler_GetOfflineMessageCount(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("统计离线消息数量", func(t *testing.T) {
		// 初始为0
		count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		assert.NoError(t, err)
		assert.Zero(t, count)

		// 存储8条消息
		for i := 0; i < 8; i++ {
			msg := tc.createTestMessage(userID)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
			require.NoError(t, err)
		}

		// 验证数量
		count, err = tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, count, int64(8))
		t.Logf("离线消息数量: %d", count)
	})
}

// ============================================================================
// 清空测试
// ============================================================================

// TestOfflineMessageHandler_ClearOfflineMessages 测试清空离线消息
func TestOfflineMessageHandler_ClearOfflineMessages(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("清空所有离线消息", func(t *testing.T) {
		// 存储10条消息
		for i := 0; i < 10; i++ {
			msg := tc.createTestMessage(userID)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
			require.NoError(t, err)
		}

		// 验证有消息
		before, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		require.NoError(t, err)
		assert.Greater(t, before, int64(0))

		// 清空
		err = tc.handler.ClearOfflineMessages(tc.ctx, userID)
		assert.NoError(t, err)

		// 验证已清空
		after, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		assert.NoError(t, err)
		assert.Zero(t, after)
	})

	t.Run("清空不存在的用户", func(t *testing.T) {
		err := tc.handler.ClearOfflineMessages(tc.ctx, "non-existent-user")
		assert.NoError(t, err)
	})
}

// ============================================================================
// 推送状态更新测试
// ============================================================================

// TestOfflineMessageHandler_UpdatePushStatus 测试更新推送状态
func TestOfflineMessageHandler_UpdatePushStatus(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("更新推送状态为成功", func(t *testing.T) {
		// 存储消息
		msg := tc.createTestMessage(userID)
		err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
		require.NoError(t, err)

		// 更新为成功
		err = tc.handler.UpdatePushStatus(tc.ctx, []string{msg.MessageID}, nil)
		assert.NoError(t, err)
	})

	t.Run("更新推送状态为失败", func(t *testing.T) {
		// 存储消息
		msg := tc.createTestMessage(userID)
		err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
		require.NoError(t, err)

		// 更新为失败
		pushErr := assert.AnError
		err = tc.handler.UpdatePushStatus(tc.ctx, []string{msg.MessageID}, pushErr)
		assert.NoError(t, err)
	})

	t.Run("批量更新推送状态", func(t *testing.T) {
		userID2 := tc.idGen.GenerateCorrelationID()
		tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID2)

		// 存储多条消息
		var messageIDs []string
		for i := 0; i < 5; i++ {
			msg := tc.createTestMessage(userID2)
			err := tc.handler.StoreOfflineMessage(tc.ctx, userID2, msg)
			require.NoError(t, err)
			messageIDs = append(messageIDs, msg.MessageID)
		}

		// 批量更新
		err := tc.handler.UpdatePushStatus(tc.ctx, messageIDs, nil)
		assert.NoError(t, err)
	})
}

// ============================================================================
// 混合存储测试
// ============================================================================

// TestOfflineMessageHandler_HybridStorage 测试Redis和MySQL混合存储
func TestOfflineMessageHandler_HybridStorage(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("Redis和MySQL同时存储", func(t *testing.T) {
		msg := tc.createTestMessage(userID)

		// 存储消息
		err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
		require.NoError(t, err)

		// 等待异步写入完成
		time.Sleep(100 * time.Millisecond)

		// 从Redis获取（应该优先从Redis读取）
		messages, _, err := tc.handler.GetOfflineMessages(tc.ctx, userID, 1, "")
		assert.NoError(t, err)
		assert.NotEmpty(t, messages, "应该从Redis读取到消息")

		// 验证MySQL也有数据
		count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		assert.NoError(t, err)
		assert.Greater(t, count, int64(0), "MySQL应该也有数据")
	})
}

// ============================================================================
// 并发测试
// ============================================================================

// TestOfflineMessageHandler_Concurrent 测试并发操作
func TestOfflineMessageHandler_Concurrent(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	t.Run("并发存储离线消息", func(t *testing.T) {
		const goroutines = 10
		const messagesPerGoroutine = 5

		done := make(chan bool, goroutines)

		for i := 0; i < goroutines; i++ {
			go func() {
				defer func() { done <- true }()

				for j := 0; j < messagesPerGoroutine; j++ {
					msg := tc.createTestMessage(userID)
					err := tc.handler.StoreOfflineMessage(tc.ctx, userID, msg)
					assert.NoError(t, err)
				}
			}()
		}

		// 等待所有协程完成
		for i := 0; i < goroutines; i++ {
			<-done
		}

		// 验证消息数量
		count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
		assert.NoError(t, err)
		expected := int64(goroutines * messagesPerGoroutine)
		assert.GreaterOrEqual(t, count, expected)
		t.Logf("并发存储完成: 期望 %d 条, 实际 %d 条", expected, count)
	})
}

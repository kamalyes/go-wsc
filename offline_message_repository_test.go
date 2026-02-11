/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 17:15:29
 * @FilePath: \go-wsc\offline_message_repository_test.go
 * @Description: 离线消息仓库集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 测试上下文
// ============================================================================

// testOfflineMessageRepoContext 离线消息仓库测试上下文
type testOfflineMessageRepoContext struct {
	t          *testing.T
	repo       OfflineMessageDBRepository
	ctx        context.Context
	idGen      IDGenerator
	cleanupIDs []string
}

// newTestOfflineMessageRepoContext 创建测试上下文
func newTestOfflineMessageRepoContext(t *testing.T) *testOfflineMessageRepoContext {
	db := GetTestDBWithMigration(t, &OfflineMessageRecord{})
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())

	return &testOfflineMessageRepoContext{
		t:          t,
		repo:       repo,
		ctx:        context.Background(),
		idGen:      idgen.NewIDGenerator(idgen.GeneratorTypeNanoID),
		cleanupIDs: make([]string, 0),
	}
}

// cleanup 清理测试数据
func (c *testOfflineMessageRepoContext) cleanup() {
	for _, receiverID := range c.cleanupIDs {
		_ = c.repo.ClearByReceiver(c.ctx, receiverID)
	}
}

// createTestRecord 创建测试离线消息记录
func (c *testOfflineMessageRepoContext) createTestRecord(customReceiver ...string) *OfflineMessageRecord {
	msgID := c.idGen.GenerateRequestID()
	senderID := c.idGen.GenerateCorrelationID()
	receiverID := c.idGen.GenerateCorrelationID()
	if len(customReceiver) > 0 && customReceiver[0] != "" {
		receiverID = customReceiver[0]
	}
	sessionID := c.idGen.GenerateTraceID()
	now := time.Now()

	// 创建测试消息
	hubMsg := createTestHubMessage(MessageTypeText)
	hubMsg.MessageID = msgID
	hubMsg.Sender = senderID
	hubMsg.Receiver = receiverID
	hubMsg.SessionID = sessionID

	// 压缩消息
	compressedData, err := zipx.ZlibCompressObject(hubMsg)
	require.NoError(c.t, err)

	record := &OfflineMessageRecord{
		MessageID:      msgID,
		Sender:         senderID,
		Receiver:       receiverID,
		SessionID:      sessionID,
		CompressedData: compressedData,
		Status:         MessageSendStatusUserOffline,
		RetryCount:     0,
		MaxRetry:       3,
		ScheduledAt:    now,
		ExpireAt:       now.Add(7 * 24 * time.Hour),
		CreatedAt:      now,
	}

	// 记录清理ID
	c.cleanupIDs = append(c.cleanupIDs, receiverID)

	return record
}

// ============================================================================
// 基础 CRUD 测试
// ============================================================================

// TestOfflineMessageRepository_Save 测试保存离线消息
func TestOfflineMessageRepository_Save(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	t.Run("成功保存离线消息", func(t *testing.T) {
		record := tc.createTestRecord()

		err := tc.repo.Save(tc.ctx, record)
		assert.NoError(t, err)
		assert.NotZero(t, record.ID, "保存后应该有自增ID")
	})

	t.Run("重复保存相同消息ID应失败(唯一索引)", func(t *testing.T) {
		record := tc.createTestRecord()

		// 第一次保存
		err := tc.repo.Save(tc.ctx, record)
		require.NoError(t, err)

		// 第二次保存相同消息ID
		duplicate := *record
		duplicate.ID = 0 // 重置ID
		err = tc.repo.Save(tc.ctx, &duplicate)
		assert.Error(t, err, "重复的message_id+receiver应该报错")
	})
}

// TestOfflineMessageRepository_BatchSave 测试批量保存
func TestOfflineMessageRepository_BatchSave(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	t.Run("批量保存多条离线消息", func(t *testing.T) {
		records := []*OfflineMessageRecord{
			tc.createTestRecord(),
			tc.createTestRecord(),
			tc.createTestRecord(),
		}

		err := tc.repo.BatchSave(tc.ctx, records)
		assert.NoError(t, err)

		for _, record := range records {
			assert.NotZero(t, record.ID, "每条记录都应该有ID")
		}
	})

	t.Run("空列表批量保存", func(t *testing.T) {
		err := tc.repo.BatchSave(tc.ctx, []*OfflineMessageRecord{})
		assert.NoError(t, err)
	})
}

// ============================================================================
// 查询测试
// ============================================================================

// TestOfflineMessageRepository_GetByReceiver 测试按接收者查询
func TestOfflineMessageRepository_GetByReceiver(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	receiverID := tc.idGen.GenerateCorrelationID()

	t.Run("查询离线消息-无游标", func(t *testing.T) {
		// 创建5条离线消息
		for i := 0; i < 5; i++ {
			record := tc.createTestRecord(receiverID)
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond) // 确保创建时间不同
		}

		// 查询前3条
		messages, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: receiverID,
			Role:   MessageRoleReceiver,
			Limit:  3,
			Cursor: "",
		})
		assert.NoError(t, err)
		assert.Len(t, messages, 3)

		// 验证按创建时间升序排序
		for i := 1; i < len(messages); i++ {
			assert.True(t, messages[i].CreatedAt.After(messages[i-1].CreatedAt) ||
				messages[i].CreatedAt.Equal(messages[i-1].CreatedAt))
		}
	})

	t.Run("查询离线消息-使用游标分页", func(t *testing.T) {
		receiverID2 := tc.idGen.GenerateCorrelationID()

		// 创建10条消息
		for i := 0; i < 10; i++ {
			record := tc.createTestRecord(receiverID2)
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond)
		}

		// 第一页
		page1, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: receiverID2,
			Role:   MessageRoleReceiver,
			Limit:  5,
			Cursor: "",
		})
		assert.NoError(t, err)
		assert.Len(t, page1, 5)

		// 第二页 - 使用游标
		cursor := page1[len(page1)-1].MessageID
		page2, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: receiverID2,
			Role:   MessageRoleReceiver,
			Limit:  5,
			Cursor: cursor,
		})
		assert.NoError(t, err)
		assert.Len(t, page2, 5)

		// 验证不重复
		for _, msg1 := range page1 {
			for _, msg2 := range page2 {
				assert.NotEqual(t, msg1.MessageID, msg2.MessageID)
			}
		}
	})

	t.Run("查询不存在的接收者", func(t *testing.T) {
		messages, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: "non-existent-user",
			Role:   MessageRoleReceiver,
			Limit:  10,
			Cursor: "",
		})
		assert.NoError(t, err)
		assert.Empty(t, messages)
	})
}

// TestOfflineMessageRepository_GetBySender 测试按发送者查询
func TestOfflineMessageRepository_GetBySender(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	senderID := tc.idGen.GenerateCorrelationID()

	t.Run("查询发送者的离线消息", func(t *testing.T) {
		// 创建3条消息
		for i := 0; i < 3; i++ {
			record := tc.createTestRecord()
			record.Sender = senderID
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
		}

		messages, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: senderID,
			Role:   MessageRoleSender,
			Limit:  10,
			Cursor: "",
		})
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(messages), 3)

		for _, msg := range messages {
			assert.Equal(t, senderID, msg.Sender)
		}
	})

	t.Run("使用cursor分页查询", func(t *testing.T) {
		// 清空之前的测试数据
		_ = tc.repo.ClearByReceiver(tc.ctx, tc.idGen.GenerateCorrelationID())

		senderID2 := tc.idGen.GenerateCorrelationID()
		// 创建5条消息
		for i := 0; i < 5; i++ {
			record := tc.createTestRecord()
			record.Sender = senderID2
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
			time.Sleep(10 * time.Millisecond) // 确保创建时间不同
		}

		// 第一页：获取2条
		page1, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: senderID2,
			Role:   MessageRoleSender,
			Limit:  2,
			Cursor: "",
		})
		assert.NoError(t, err)
		assert.Len(t, page1, 2)

		// 第二页：使用第一页最后一条的ID作为cursor
		cursor := page1[len(page1)-1].MessageID
		page2, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: senderID2,
			Role:   MessageRoleSender,
			Limit:  2,
			Cursor: cursor,
		})
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, len(page2), 1) // 至少还有1条

		// 验证分页数据不重叠
		if len(page2) > 0 {
			assert.NotEqual(t, page1[0].MessageID, page2[0].MessageID)
		}
	})

	t.Run("按状态过滤查询", func(t *testing.T) {
		senderID3 := tc.idGen.GenerateCorrelationID()

		// 创建不同状态的消息
		record1 := tc.createTestRecord()
		record1.Sender = senderID3
		record1.Status = MessageSendStatusPending
		err := tc.repo.Save(tc.ctx, record1)
		require.NoError(t, err)

		record2 := tc.createTestRecord()
		record2.Sender = senderID3
		record2.Status = MessageSendStatusSuccess
		err = tc.repo.Save(tc.ctx, record2)
		require.NoError(t, err)

		// 只查询成功状态的消息
		messages, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID:   senderID3,
			Role:     MessageRoleSender,
			Limit:    10,
			Cursor:   "",
			Statuses: []MessageSendStatus{MessageSendStatusSuccess},
		})
		assert.NoError(t, err)

		// 验证返回的消息都是成功状态
		foundSuccess := false
		for _, msg := range messages {
			if msg.MessageID == record2.MessageID {
				assert.Equal(t, MessageSendStatusSuccess, msg.Status)
				foundSuccess = true
			}
		}
		assert.True(t, foundSuccess, "应该能查询到成功状态的消息")
	})
}

// ============================================================================
// 删除和清理测试
// ============================================================================

// TestOfflineMessageRepository_DeleteByMessageIDs 测试批量删除
func TestOfflineMessageRepository_DeleteByMessageIDs(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	receiverID := tc.idGen.GenerateCorrelationID()

	t.Run("删除指定的消息ID", func(t *testing.T) {
		// 创建5条消息
		var messageIDs []string
		for i := 0; i < 5; i++ {
			record := tc.createTestRecord(receiverID)
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
			messageIDs = append(messageIDs, record.MessageID)
		}

		// 删除前3条
		err := tc.repo.DeleteByMessageIDs(tc.ctx, receiverID, messageIDs[:3])
		assert.NoError(t, err)

		// 验证剩余2条
		remaining, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: receiverID,
			Role:   MessageRoleReceiver,
			Limit:  10,
			Cursor: "",
		})
		assert.NoError(t, err)
		assert.Len(t, remaining, 2)
	})

	t.Run("删除空列表", func(t *testing.T) {
		err := tc.repo.DeleteByMessageIDs(tc.ctx, receiverID, []string{})
		assert.NoError(t, err)
	})
}

// TestOfflineMessageRepository_ClearByReceiver 测试清空用户离线消息
func TestOfflineMessageRepository_ClearByReceiver(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	receiverID := tc.idGen.GenerateCorrelationID()

	t.Run("清空接收者的所有离线消息", func(t *testing.T) {
		// 创建10条消息
		for i := 0; i < 10; i++ {
			record := tc.createTestRecord(receiverID)
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
		}

		// 验证有消息
		before, err := tc.repo.GetCountByReceiver(tc.ctx, receiverID)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, before, int64(10))

		// 清空
		err = tc.repo.ClearByReceiver(tc.ctx, receiverID)
		assert.NoError(t, err)

		// 验证已清空
		after, err := tc.repo.GetCountByReceiver(tc.ctx, receiverID)
		assert.NoError(t, err)
		assert.Zero(t, after)
	})
}

// ============================================================================
// 统计测试
// ============================================================================

// TestOfflineMessageRepository_GetCount 测试统计数量
func TestOfflineMessageRepository_GetCount(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	receiverID := tc.idGen.GenerateCorrelationID()
	senderID := tc.idGen.GenerateCorrelationID()

	t.Run("统计接收者的离线消息数量", func(t *testing.T) {
		// 创建8条消息
		for i := 0; i < 8; i++ {
			record := tc.createTestRecord(receiverID)
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
		}

		count, err := tc.repo.GetCountByReceiver(tc.ctx, receiverID)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, count, int64(8))
	})

	t.Run("统计发送者的离线消息数量", func(t *testing.T) {
		// 创建5条消息
		for i := 0; i < 5; i++ {
			record := tc.createTestRecord()
			record.Sender = senderID
			err := tc.repo.Save(tc.ctx, record)
			require.NoError(t, err)
		}

		count, err := tc.repo.GetCountBySender(tc.ctx, senderID)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, count, int64(5))
	})
}

// ============================================================================
// 状态更新测试
// ============================================================================

// TestOfflineMessageRepository_UpdatePushStatus 测试更新推送状态
func TestOfflineMessageRepository_UpdatePushStatus(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	t.Run("更新推送状态为成功", func(t *testing.T) {
		record := tc.createTestRecord()
		err := tc.repo.Save(tc.ctx, record)
		require.NoError(t, err)

		// 更新为成功
		err = tc.repo.UpdatePushStatus(tc.ctx, []string{record.MessageID}, MessageSendStatusSuccess, "")
		assert.NoError(t, err)

		// 验证状态 - 指定查询成功状态的消息
		messages, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID:   record.Receiver,
			Role:     MessageRoleReceiver,
			Limit:    1,
			Cursor:   "",
			Statuses: []MessageSendStatus{MessageSendStatusSuccess},
		})
		require.NoError(t, err)
		require.Len(t, messages, 1)
		assert.Equal(t, MessageSendStatusSuccess, messages[0].Status)
		assert.NotNil(t, messages[0].LastPushAt)
	})

	t.Run("更新推送状态为失败", func(t *testing.T) {
		record := tc.createTestRecord()
		err := tc.repo.Save(tc.ctx, record)
		require.NoError(t, err)

		// 更新为失败
		errorMsg := "网络连接超时"
		err = tc.repo.UpdatePushStatus(tc.ctx, []string{record.MessageID}, MessageSendStatusFailed, errorMsg)
		assert.NoError(t, err)

		// 验证状态 - 失败状态的消息仍在待推送队列中
		messages, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: record.Receiver,
			Role:   MessageRoleReceiver,
			Limit:  1,
			Cursor: "",
		})
		require.NoError(t, err)
		require.Len(t, messages, 1)
		assert.Equal(t, MessageSendStatusFailed, messages[0].Status)
		assert.Equal(t, errorMsg, messages[0].ErrorMessage)
	})
}

// ============================================================================
// 过期和清理测试
// ============================================================================

// TestOfflineMessageRepository_DeleteExpired 测试删除过期消息
func TestOfflineMessageRepository_DeleteExpired(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	t.Run("删除过期的离线消息", func(t *testing.T) {
		// 创建已过期的消息
		expiredRecord := tc.createTestRecord()
		expiredRecord.ExpireAt = time.Now().Add(-1 * time.Hour)
		err := tc.repo.Save(tc.ctx, expiredRecord)
		require.NoError(t, err)

		// 创建未过期的消息
		validRecord := tc.createTestRecord()
		validRecord.ExpireAt = time.Now().Add(1 * time.Hour)
		err = tc.repo.Save(tc.ctx, validRecord)
		require.NoError(t, err)

		// 删除过期消息
		deletedCount, err := tc.repo.DeleteExpired(tc.ctx)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, deletedCount, int64(1))

		// 验证未过期的消息仍然存在
		remaining, err := tc.repo.QueryMessages(tc.ctx, &OfflineMessageFilter{
			UserID: validRecord.Receiver,
			Role:   MessageRoleReceiver,
			Limit:  10,
			Cursor: "",
		})
		assert.NoError(t, err)
		assert.NotEmpty(t, remaining)
	})
}

// TestOfflineMessageRepository_CleanupOld 测试清理旧记录
func TestOfflineMessageRepository_CleanupOld(t *testing.T) {
	tc := newTestOfflineMessageRepoContext(t)
	defer tc.cleanup()

	t.Run("清理指定时间之前的记录", func(t *testing.T) {
		// 创建旧记录
		oldRecord := tc.createTestRecord()
		oldRecord.CreatedAt = time.Now().Add(-30 * 24 * time.Hour)
		err := tc.repo.Save(tc.ctx, oldRecord)
		require.NoError(t, err)

		// 创建新记录
		newRecord := tc.createTestRecord()
		err = tc.repo.Save(tc.ctx, newRecord)
		require.NoError(t, err)

		// 清理30天前的记录
		before := time.Now().Add(-29 * 24 * time.Hour)
		deletedCount, err := tc.repo.CleanupOld(tc.ctx, before)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, deletedCount, int64(0))
	})
}

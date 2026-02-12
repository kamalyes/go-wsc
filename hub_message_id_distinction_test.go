/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-20 12:00:00
 * @FilePath: \go-wsc\hub_message_id_distinction_test.go
 * @Description: Hub ID 和 MessageID 区分测试 - 确保不会混用
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubMessageIDVsHubIDDistinction 验证 Hub ID 和 MessageID 不会混淆
func TestHubMessageIDVsHubIDDistinction(t *testing.T) {
	ctx := context.Background()
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	// 创建消息，Hub ID 和 MessageID 应该不同
	msg := createTestHubMessage(MessageTypeText)
	businessMsgID := msg.MessageID // 使用消息自带的 MessageID
	defer func() {
		_ = repo.DeleteByMessageID(ctx, businessMsgID)
	}()

	// 🔥 断言：Hub ID 和 MessageID 必须不同
	assert.NotEqual(t, msg.ID, msg.MessageID, "Hub ID 和 MessageID 不应该相同")
	assert.Contains(t, msg.ID, "msg_test_node_", "Hub ID 应该包含节点前缀")
	assert.NotContains(t, msg.MessageID, "msg_test_node_", "MessageID 不应该包含节点前缀")

	// 创建记录
	created, err := repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)

	// 🔥 验证数据库记录保存了两个不同的ID
	assert.Equal(t, businessMsgID, created.MessageID, "数据库应该保存业务消息ID到 message_id 字段")
	assert.Equal(t, msg.ID, created.HubID, "数据库应该保存 Hub 内部ID到 hub_id 字段")
	assert.NotEqual(t, created.MessageID, created.HubID, "数据库的两个ID字段值应该不同")

	// 🔥 验证只能通过 MessageID 查询（而不是 HubID）
	found, err := repo.FindByMessageID(ctx, businessMsgID)
	require.NoError(t, err)
	assert.Equal(t, created.ID, found.ID, "应该能通过 MessageID 查询到记录")

	// 🔥 尝试用 Hub ID 查询应该找不到（因为查询用的是 message_id 字段）
	notFound, err := repo.FindByMessageID(ctx, msg.ID) // 这里传入的是 Hub ID
	assert.Error(t, err, "用 Hub ID 查询应该报错")
	assert.Nil(t, notFound, "用 Hub ID 不应该查到记录")

	// 🔥 验证更新操作使用的是 MessageID
	err = repo.UpdateStatus(ctx, businessMsgID, MessageSendStatusSuccess, "", "")
	assert.NoError(t, err, "用 MessageID 更新应该成功")

	updated, err := repo.FindByMessageID(ctx, businessMsgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, updated.Status, "状态应该已更新")

	// 🔥 尝试用 Hub ID 更新应该静默失败（UpdateStatus 会忽略不存在的记录）
	err = repo.UpdateStatus(ctx, msg.ID, MessageSendStatusFailed, FailureReasonNetworkError, "test")
	assert.NoError(t, err, "UpdateStatus 对不存在的记录应该静默返回")

	// 验证状态没有被错误更新
	final, err := repo.FindByMessageID(ctx, businessMsgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, final.Status, "状态不应该被 Hub ID 更新影响")
}

// TestMessageRecordIDFields 测试 MessageSendRecord 的 ID 字段正确性
func TestMessageRecordIDFields(t *testing.T) {
	businessMsgID := "biz_msg_12345"
	hubInternalID := "msg_node01_67890"

	msg := &HubMessage{
		ID:          hubInternalID, // Hub 内部ID
		MessageID:   businessMsgID, // 业务消息ID
		Sender:      "user-a",
		Receiver:    "user-b",
		MessageType: MessageTypeText,
		Content:     "test",
	}

	record := &MessageSendRecord{}
	err := record.SetMessage(msg)
	require.NoError(t, err)

	// 🔥 验证 SetMessage 正确分离两个ID
	assert.Equal(t, businessMsgID, record.MessageID, "MessageID 字段应该存储业务消息ID")
	assert.Equal(t, hubInternalID, record.HubID, "HubID 字段应该存储 Hub 内部ID")
	assert.NotEqual(t, record.MessageID, record.HubID, "两个ID字段的值必须不同")

	// 🔥 验证 GetMessage 正确还原两个ID
	retrieved, err := record.GetMessage()
	require.NoError(t, err)
	assert.Equal(t, hubInternalID, retrieved.ID, "还原的消息应该有正确的 Hub ID")
	assert.Equal(t, businessMsgID, retrieved.MessageID, "还原的消息应该有正确的业务消息ID")
}

// TestRetryWithCorrectMessageID 测试重试时使用正确的 MessageID
func TestRetryWithCorrectMessageID(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	ctx := context.Background()

	msg := createTestHubMessage(MessageTypeText)
	businessMsgID := msg.MessageID // 使用消息自带的 MessageID
	defer func() {
		_ = repo.DeleteByMessageID(ctx, businessMsgID)
	}()

	_, err := repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)

	// 🔥 重试应该使用 MessageID 而不是 Hub ID
	attempt := RetryAttempt{
		AttemptNumber: 1,
		Success:       false,
		Error:         "timeout",
	}

	// 使用业务消息ID进行重试记录
	err = repo.IncrementRetry(ctx, businessMsgID, attempt)
	assert.NoError(t, err, "使用 MessageID 记录重试应该成功")

	// 验证重试记录已保存
	record, err := repo.FindByMessageID(ctx, businessMsgID)
	require.NoError(t, err)
	assert.Equal(t, 1, record.RetryCount, "重试次数应该增加")
	assert.Len(t, record.RetryHistory, 1, "重试历史应该有一条记录")

	// 🔥 尝试用 Hub ID 记录重试应该找不到记录（静默失败）
	err = repo.IncrementRetry(ctx, msg.ID, attempt) // 使用 Hub ID
	assert.Error(t, err, "使用 Hub ID 应该报错（找不到记录）")

	// 验证重试次数没有被错误增加
	final, err := repo.FindByMessageID(ctx, businessMsgID)
	require.NoError(t, err)
	assert.Equal(t, 1, final.RetryCount, "重试次数不应该被 Hub ID 操作影响")
}

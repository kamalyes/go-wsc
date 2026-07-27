/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 16:52:03
 * @FilePath: \go-wsc\message_record_test.go
 * @Description: 消息记录仓库集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 测试辅助函数
// ============================================================================

// testMessageRecordContext 封装消息记录测试的上下文
type testMessageRecordContext struct {
	t          *testing.T
	repo       MessageRecordRepository
	cleanupIDs []string
}

// newTestMessageRecordContext 创建测试上下文
func newTestMessageRecordContext(t *testing.T) *testMessageRecordContext {
	return &testMessageRecordContext{
		t:          t,
		repo:       NewMessageRecordRepository(GetTestDBWithMigration(t, &MessageSendRecord{}), nil, NewDefaultWSCLogger()),
		cleanupIDs: make([]string, 0),
	}
}

// cleanup 清理测试数据
func (c *testMessageRecordContext) cleanup() {
	for _, msgID := range c.cleanupIDs {
		ctx := context.Background()
		_ = c.repo.DeleteByMessageID(ctx, msgID)
	}
}

// TestMessageRecordStatusUpdateFields 测试消息状态更新时字段是否正确更新
func TestMessageRecordStatusUpdateFields(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()
	ctx := context.Background()

	// 1. 创建Pending状态的记录
	msg := createTestHubMessage(MessageTypeText)
	tc.cleanupIDs = append(tc.cleanupIDs, msg.MessageID)

	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.1.100",
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)
	err = tc.repo.Create(ctx, record)
	require.NoError(t, err)

	// 2. 更新为Sending状态，验证first_send_time被设置
	err = tc.repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSending, "", "")
	assert.NoError(t, err)

	record1, err := tc.repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusSending, record1.Status)
	assert.NotNil(t, record1.FirstSendTime, "更新为Sending状态时应设置FirstSendTime")
	assert.NotNil(t, record1.LastSendTime, "更新为Sending状态时应设置LastSendTime")
	firstSendTime := record1.FirstSendTime
	lastSendTime := record1.LastSendTime

	time.Sleep(100 * time.Millisecond)

	// 3. 更新为Success状态，验证success_time被设置
	err = tc.repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
	assert.NoError(t, err)

	record2, err := tc.repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, record2.Status)
	assert.NotNil(t, record2.SuccessTime, "更新为Success状态时应设置SuccessTime")
	assert.NotNil(t, record2.LastSendTime, "LastSendTime应被更新")
	assert.Equal(t, firstSendTime.Unix(), record2.FirstSendTime.Unix(), "FirstSendTime不应该被修改")
	assert.True(t, record2.LastSendTime.After(*lastSendTime), "LastSendTime应该更新为更新的时间")

	t.Log("✅ 消息发送成功状态字段更新验证通过")
}

// TestMessageRecordFailureFields 测试消息发送失败时字段是否正确更新
func TestMessageRecordFailureFields(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()
	ctx := context.Background()

	msgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, msgID)

	// 1. 创建记录
	msg := &HubMessage{
		ID:                  "msg_test_node_" + msgID,
		MessageID:           msgID,
		MessageType:         MessageTypeText,
		Sender:              "sender-001",
		SenderType:          UserTypeCustomer,
		Receiver:            "receiver-001",
		ReceiverType:        UserTypeAgent,
		SessionID:           "session_" + msgID,
		Content:             "test message",
		Data:                map[string]any{"test": "data"},
		CreateAt:            time.Now(),
		SeqNo:               time.Now().UnixMicro(),
		Priority:            PriorityNormal,
		RequireAck:          true,
		PushType:            PushTypeDirect,
		BroadcastType:       BroadcastTypeNone,
		SkipDatabaseStorage: false,
	}
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.1.100",
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)
	err = tc.repo.Create(ctx, record)
	require.NoError(t, err)

	// 2. 更新为Sending状态
	err = tc.repo.UpdateStatus(ctx, msgID, MessageSendStatusSending, "", "")
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// 3. 更新为Failed状态，验证failure_reason和error_message被设置
	testReason := FailureReasonConnError
	testError := "connection timeout"
	err = tc.repo.UpdateStatus(ctx, msgID, MessageSendStatusFailed, testReason, testError)
	assert.NoError(t, err)

	record1, err := tc.repo.FindByMessageID(ctx, msgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, record1.Status)
	assert.Equal(t, testReason, record1.FailureReason, "失败时应设置FailureReason")
	assert.Equal(t, testError, record1.ErrorMessage, "失败时应设置ErrorMessage")
	assert.NotNil(t, record1.LastSendTime, "LastSendTime应被更新")

	t.Log("✅ 消息发送失败状态字段更新验证通过")
}

// TestMessageRecordRetryFields 测试消息重试时字段是否正确更新
func TestMessageRecordRetryFields(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()
	ctx := context.Background()

	// 1. 创建记录
	msg := createTestHubMessage(MessageTypeText)
	tc.cleanupIDs = append(tc.cleanupIDs, msg.MessageID)

	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.1.100",
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)
	err = tc.repo.Create(ctx, record)
	require.NoError(t, err)

	// 2. 第一次重试（失败）
	attempt1 := RetryAttempt{
		AttemptNumber: 1,
		Timestamp:     time.Now(),
		Duration:      100 * time.Millisecond,
		Error:         "first retry error",
		Success:       false,
	}
	err = tc.repo.IncrementRetry(ctx, msg.MessageID, attempt1)
	assert.NoError(t, err)

	record1, err := tc.repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Equal(t, 1, record1.RetryCount, "重试次数应为1")
	assert.Equal(t, MessageSendStatusRetrying, record1.Status, "状态应为Retrying")
	assert.NotNil(t, record1.FirstSendTime, "首次重试应设置FirstSendTime")
	assert.NotNil(t, record1.LastSendTime, "重试应更新LastSendTime")
	assert.NotEmpty(t, record1.RetryHistory, "重试历史应被记录")
	assert.Equal(t, "first retry error", record1.ErrorMessage, "错误信息应被记录")

	time.Sleep(100 * time.Millisecond)

	// 3. 第二次重试（成功）
	attempt2 := RetryAttempt{
		AttemptNumber: 2,
		Timestamp:     time.Now(),
		Duration:      50 * time.Millisecond,
		Error:         "",
		Success:       true,
	}
	err = tc.repo.IncrementRetry(ctx, msg.MessageID, attempt2)
	assert.NoError(t, err)

	record2, err := tc.repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Equal(t, 2, record2.RetryCount, "重试次数应为2")
	assert.Equal(t, MessageSendStatusSuccess, record2.Status, "重试成功状态应为Success")
	assert.NotNil(t, record2.SuccessTime, "重试成功应设置SuccessTime")

	t.Log("✅ 消息重试字段更新验证通过")
}

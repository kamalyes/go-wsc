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

	"github.com/kamalyes/go-sqlbuilder"
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
		repo:       NewMessageRecordRepository(getTestDB(t),nil, NewDefaultWSCLogger()),
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

// createRecord 创建消息记录
func (c *testMessageRecordContext) createRecord(msg *HubMessage, maxRetry int, expiresAt *time.Time) *MessageSendRecord {
	ctx := context.Background()
	record, err := c.repo.CreateFromMessage(ctx, msg, maxRetry, expiresAt)
	require.NoError(c.t, err)
	return record
}

// createTestHubMessage 创建测试消息并返回 (msgID, msg)
func (c *testMessageRecordContext) createTestHubMessage() (string, *HubMessage) {
	msgID := osx.HashUnixMicroCipherText()
	c.cleanupIDs = append(c.cleanupIDs, msgID)

	hubID := "msg_test_node_" + msgID
	sessionID := "session_" + msgID

	msg := &HubMessage{
		ID:                  hubID,
		MessageID:           msgID,
		MessageType:         MessageTypeText,
		Sender:              "test_sender_" + msgID[:8],
		SenderType:          UserTypeCustomer,
		Receiver:            "test_receiver_" + msgID[:8],
		ReceiverType:        UserTypeAgent,
		ReceiverClient:      "client_" + msgID[:8],
		ReceiverNode:        "node_test_01",
		SessionID:           sessionID,
		Content:             "Test message: " + msgID[:16],
		Data:                map[string]interface{}{"test": "data"},
		CreateAt:            time.Now(),
		SeqNo:               time.Now().UnixMicro(),
		Priority:            PriorityNormal,
		RequireAck:          true,
		PushType:            PushTypeDirect,
		BroadcastType:       BroadcastTypeNone,
		SkipDatabaseStorage: false,
	}
	return msgID, msg
}

// createTestHubMessageStandalone 创建独立测试消息（用于不需要cleanup的场景）
func createTestHubMessage(messageID, sender, receiver string, msgType MessageType) *HubMessage {
	hubID := "msg_test_node_" + messageID
	sessionID := "session_" + messageID

	return &HubMessage{
		ID:                  hubID,
		MessageID:           messageID,
		MessageType:         msgType,
		Sender:              sender,
		SenderType:          UserTypeCustomer,
		Receiver:            receiver,
		ReceiverType:        UserTypeAgent,
		ReceiverClient:      "client_" + messageID[:8],
		ReceiverNode:        "node_test_01",
		SessionID:           sessionID,
		Content:             "Test message for " + sender,
		Data:                map[string]interface{}{"content": "test data"},
		CreateAt:            time.Now(),
		SeqNo:               time.Now().UnixMicro(),
		Priority:            PriorityNormal,
		RequireAck:          true,
		PushType:            PushTypeDirect,
		BroadcastType:       BroadcastTypeNone,
		SkipDatabaseStorage: false,
	}
}

func TestMessageSendRecordSetAndGetMessage(t *testing.T) {
	record := &MessageSendRecord{}
	msgID := "test-business-msg-001"
	msg := createTestHubMessage(msgID, "user-001", "user-002", MessageTypeText)

	// 设置消息
	err := record.SetMessage(msg)
	assert.NoError(t, err)

	// 🔥 校验：MessageID 应该是业务消息ID
	assert.Equal(t, msgID, record.MessageID, "MessageID 应该是业务消息ID")
	// 🔥 校验：HubID 应该是 Hub 内部ID
	assert.Equal(t, msg.ID, record.HubID, "HubID 应该是 Hub 内部ID")
	assert.NotEqual(t, record.MessageID, record.HubID, "MessageID 和 HubID 应该不同")

	assert.Equal(t, "user-001", record.Sender)
	assert.Equal(t, "user-002", record.Receiver)
	assert.Equal(t, MessageTypeText, record.MessageType)
	assert.NotEmpty(t, record.MessageData)

	// 获取消息
	retrievedMsg, err := record.GetMessage()
	assert.NoError(t, err)
	assert.NotNil(t, retrievedMsg)

	// 🔥 校验：反序列化后 ID 和 MessageID 应该保持正确
	assert.Equal(t, msg.ID, retrievedMsg.ID, "Hub ID 应该一致")
	assert.Equal(t, msgID, retrievedMsg.MessageID, "业务消息ID 应该一致")
	assert.Equal(t, msg.Sender, retrievedMsg.Sender)
	assert.Equal(t, msg.Receiver, retrievedMsg.Receiver)
	assert.Equal(t, msg.MessageType, retrievedMsg.MessageType)
}

func TestMessageRecordRepositoryCreate(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()
	ctx := context.Background()

	msgID, msg := tc.createTestHubMessage()

	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.1.100",
		ClientIP:   "10.0.0.1",
	}

	err := record.SetMessage(msg)
	require.NoError(t, err)

	err = tc.repo.Create(ctx, record)
	assert.NoError(t, err)
	assert.NotZero(t, record.ID)
	_ = msgID
}

func TestMessageRecordRepositoryCreateFromMessage(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()

	msgID, msg := tc.createTestHubMessage()

	expiresAt := time.Now().Add(1 * time.Hour)

	record := tc.createRecord(msg, 5, &expiresAt)
	assert.NotNil(t, record)
	assert.NotZero(t, record.ID)
	assert.Equal(t, MessageSendStatusPending, record.Status)
	assert.Equal(t, 5, record.MaxRetry)
	assert.Equal(t, msgID, record.MessageID)
}

func TestMessageRecordRepositoryFindByMessageID(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()
	ctx := context.Background()

	msgID, msg := tc.createTestHubMessage()

	created := tc.createRecord(msg, 3, nil)

	// 🔥 校验创建的记录有正确的ID
	assert.Equal(t, msgID, created.MessageID, "创建的记录应该有业务消息ID")
	assert.Equal(t, msg.ID, created.HubID, "创建的记录应该有Hub内部ID")
	assert.NotEqual(t, created.MessageID, created.HubID, "MessageID 和 HubID 应该不同")

	// 🔥 查找记录：使用业务消息ID查询
	found, err := tc.repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, created.ID, found.ID)
	assert.Equal(t, msgID, found.MessageID, "查询应该使用业务消息ID")
	assert.Equal(t, msg.ID, found.HubID, "记录应该保存Hub内部ID")
}

func TestMessageRecordRepositoryUpdateStatus(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()
	ctx := context.Background()

	msgID, msg := tc.createTestHubMessage()

	created := tc.createRecord(msg, 3, nil)

	// 🔥 更新为发送中：使用业务消息ID
	err := tc.repo.UpdateStatus(ctx, msgID, MessageSendStatusSending, "", "")
	assert.NoError(t, err)

	// 验证更新
	updated, err := tc.repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusSending, updated.Status)
	assert.Equal(t, msgID, updated.MessageID, "更新后 MessageID 应该保持不变")

	// 🔥 更新为成功：使用业务消息ID
	err = tc.repo.UpdateStatus(ctx, msgID, MessageSendStatusSuccess, "", "")
	assert.NoError(t, err)

	// 验证成功状态
	success, err := tc.repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, success.Status)
	assert.NotNil(t, success.SuccessTime)
	assert.Equal(t, msgID, success.MessageID, "MessageID 应该始终是业务消息ID")

	// 🔥 更新为失败：使用业务消息ID
	err = tc.repo.UpdateStatus(ctx, msgID, MessageSendStatusFailed, FailureReasonNetworkError, "Network timeout")
	assert.NoError(t, err)

	// 验证失败状态
	failed, err := tc.repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, failed.Status)
	assert.Equal(t, FailureReasonNetworkError, failed.FailureReason)
	assert.Equal(t, "Network timeout", failed.ErrorMessage)
	assert.Equal(t, msgID, failed.MessageID, "失败后 MessageID 应该仍是业务消息ID")

	_ = created
}

func TestMessageRecordRepositoryIncrementRetry(t *testing.T) {
	tc := newTestMessageRecordContext(t)
	defer tc.cleanup()
	ctx := context.Background()

	msgID, msg := tc.createTestHubMessage()

	_ = tc.createRecord(msg, 3, nil)

	// 🔥 第一次重试：使用业务消息ID
	attempt1 := RetryAttempt{
		AttemptNumber: 1,
		Timestamp:     time.Now(),
		Duration:      100 * time.Millisecond,
		Error:         "connection timeout",
		Success:       false,
	}
	err := tc.repo.IncrementRetry(ctx, msgID, attempt1)
	assert.NoError(t, err)

	// 验证重试次数
	record, err := tc.repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.Equal(t, 1, record.RetryCount)
	assert.Len(t, record.RetryHistory, 1)
	assert.Equal(t, MessageSendStatusRetrying, record.Status)
	assert.Equal(t, msgID, record.MessageID, "重试时 MessageID 应该保持不变")

	// 第二次重试
	attempt2 := RetryAttempt{
		AttemptNumber: 2,
		Timestamp:     time.Now(),
		Duration:      150 * time.Millisecond,
		Error:         "queue full",
		Success:       false,
	}
	err = tc.repo.IncrementRetry(ctx, msgID, attempt2)
	assert.NoError(t, err)

	record, err = tc.repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.Equal(t, 2, record.RetryCount)
	assert.Len(t, record.RetryHistory, 2)

	// 第三次重试成功
	attempt3 := RetryAttempt{
		AttemptNumber: 3,
		Timestamp:     time.Now(),
		Duration:      80 * time.Millisecond,
		Error:         "",
		Success:       true,
	}
	err = tc.repo.IncrementRetry(ctx, msgID, attempt3)
	assert.NoError(t, err)

	record, err = tc.repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.Equal(t, 3, record.RetryCount)
	assert.Len(t, record.RetryHistory, 3)
	assert.Equal(t, MessageSendStatusSuccess, record.Status)
	assert.NotNil(t, record.SuccessTime)
}

func TestMessageRecordRepositoryFindByStatus(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db,nil, NewDefaultWSCLogger())

	ctx := context.Background()

	// 生成唯一ID
	msgIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		for _, msgID := range msgIDs {
			_ = repo.DeleteByMessageID(ctx, msgID)
		}
	}()

	// 创建不同状态的记录
	statuses := []MessageSendStatus{
		MessageSendStatusPending,
		MessageSendStatusSuccess,
		MessageSendStatusFailed,
	}

	for i, status := range statuses {
		msg := createTestHubMessage(msgIDs[i], "sender", "receiver", MessageTypeText)
		record, err := repo.CreateFromMessage(ctx, msg, 3, nil)
		require.NoError(t, err)

		if status != MessageSendStatusPending {
			err = repo.UpdateStatus(ctx, record.MessageID, status, "", "")
			require.NoError(t, err)
		}
	}

	// 查找待发送的记录
	pending, err := repo.FindByStatus(ctx, MessageSendStatusPending, 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(pending), 1)

	// 查找成功的记录
	success, err := repo.FindByStatus(ctx, MessageSendStatusSuccess, 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(success), 1)

	// 查找失败的记录
	failed, err := repo.FindByStatus(ctx, MessageSendStatusFailed, 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(failed), 1)
}

func TestMessageRecordRepositoryFindBySenderAndReceiver(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	ctx := context.Background()
	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID)
	}()

	msg := createTestHubMessage(msgID, "alice", "bob", MessageTypeText)
	_, err := repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)

	// 按发送者查找
	senderRecords, err := repo.FindBySender(ctx, "alice", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(senderRecords), 1)
	found := false
	for _, r := range senderRecords {
		if r.MessageID == msgID {
			found = true
			break
		}
	}
	assert.True(t, found)

	// 按接收者查找
	receiverRecords, err := repo.FindByReceiver(ctx, "bob", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(receiverRecords), 1)
	found = false
	for _, r := range receiverRecords {
		if r.MessageID == msgID {
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestMessageRecordRepositoryFindByNodeIPAndClientIP(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	ctx := context.Background()

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-ip", "receiver-ip", MessageTypeText)
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.100.50",
		ClientIP:   "10.20.30.40",
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)
	err = repo.Create(ctx, record)
	require.NoError(t, err)

	// 按节点IP查找
	nodeRecords, err := repo.FindByNodeIP(ctx, "192.168.100.50", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(nodeRecords), 1)
	found := false
	for _, r := range nodeRecords {
		if r.MessageID == msgID {
			found = true
			assert.Equal(t, "192.168.100.50", r.NodeIP)
			break
		}
	}
	assert.True(t, found)

	// 按客户端IP查找
	clientRecords, err := repo.FindByClientIP(ctx, "10.20.30.40", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(clientRecords), 1)
	found = false
	for _, r := range clientRecords {
		if r.MessageID == msgID {
			found = true
			assert.Equal(t, "10.20.30.40", r.ClientIP)
			break
		}
	}
	assert.True(t, found)
}

func TestMessageRecordRepositoryFindRetryable(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	ctx := context.Background()

	msgID1 := osx.HashUnixMicroCipherText()
	msgID2 := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID1)
		_ = repo.DeleteByMessageID(ctx, msgID2)
	}()

	// 创建可重试的失败记录
	msg1 := createTestHubMessage(msgID1, "sender", "receiver", MessageTypeText)
	record1, err := repo.CreateFromMessage(ctx, msg1, 3, nil)
	require.NoError(t, err)
	err = repo.UpdateStatus(ctx, record1.MessageID, MessageSendStatusFailed, FailureReasonNetworkError, "timeout")
	require.NoError(t, err)

	// 创建超过最大重试次数的记录
	msg2 := createTestHubMessage(msgID2, "sender", "receiver", MessageTypeText)
	record2, err := repo.CreateFromMessage(ctx, msg2, 1, nil)
	require.NoError(t, err)
	err = repo.IncrementRetry(ctx, record2.MessageID, RetryAttempt{
		AttemptNumber: 1,
		Timestamp:     time.Now(),
		Success:       false,
	})
	require.NoError(t, err)

	// 查找可重试的记录
	retryable, err := repo.FindRetryable(ctx, 10)
	assert.NoError(t, err)

	// 验证第一条记录在可重试列表中
	found := false
	for _, r := range retryable {
		if r.MessageID == msgID1 {
			found = true
			assert.Less(t, r.RetryCount, r.MaxRetry)
			break
		}
	}
	assert.True(t, found)
}

func TestMessageRecordRepositoryDeleteExpired(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	ctx := context.Background()

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID)
	}()

	// 创建过期的记录
	msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
	pastTime := time.Now().Add(-1 * time.Hour) // 1小时前过期
	record, err := repo.CreateFromMessage(ctx, msg, 3, &pastTime)
	require.NoError(t, err)

	// 删除过期的记录
	deletedCount, err := repo.DeleteExpired(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, deletedCount, int64(1), "应该至少删除1条过期记录")

	// 验证记录已被删除
	_, err = repo.FindByMessageID(ctx, msgID)
	assert.Error(t, err, "过期记录应该已被删除")

	_ = record
}

func TestMessageRecordRepositoryGetStatistics(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	ctx := context.Background()
	stats, err := repo.GetStatistics(ctx)
	assert.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "total")
	assert.Contains(t, stats, string(MessageSendStatusPending))
	assert.Contains(t, stats, string(MessageSendStatusSuccess))
	assert.Contains(t, stats, string(MessageSendStatusFailed))
}

func TestMessageRecordRepositoryCleanupOld(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	msgID := osx.HashUnixMicroCipherText()
	ctx := context.Background()
	// 创建旧记录
	msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
	record, err := repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)

	// 更新为成功状态
	err = repo.UpdateStatus(ctx, record.MessageID, MessageSendStatusSuccess, "", "")
	require.NoError(t, err)

	// 手动设置创建时间为很久以前
	oldTime := time.Now().Add(-365 * 24 * time.Hour) // 1年前
	err = repo.GetDB().Model(&MessageSendRecord{}).
		Where("message_id = ?", record.MessageID).
		Update("create_time", oldTime).Error
	require.NoError(t, err)

	// 清理30天前的记录
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	deleted, err := repo.CleanupOld(ctx, cutoff)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, int64(1))

	// 验证记录已删除
	_, err = repo.FindByMessageID(ctx, msgID)
	assert.Error(t, err) // 应该找不到
}

func TestMessageSendRecordJSONFields(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	msgID := osx.HashUnixMicroCipherText()
	ctx := context.Background()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID)
	}()

	msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		Metadata: sqlbuilder.MapAny{
			"priority": "high",
			"source":   "mobile_app",
			"version":  1.5,
		},
		CustomFields: sqlbuilder.MapAny{
			"user_level": "vip",
			"region":     "asia",
		},
		Tags: sqlbuilder.StringSlice{"urgent", "support", "vip"},
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)

	err = repo.Create(ctx, record)
	require.NoError(t, err)

	// 重新查询验证JSON字段
	found, err := repo.FindByMessageID(ctx, msgID)
	assert.NoError(t, err)
	assert.NotNil(t, found.Metadata)
	assert.Equal(t, "high", found.Metadata["priority"])
	assert.Equal(t, "mobile_app", found.Metadata["source"])
	assert.NotNil(t, found.CustomFields)
	assert.Equal(t, "vip", found.CustomFields["user_level"])
	assert.Len(t, found.Tags, 3)
	assert.Contains(t, found.Tags, "urgent")
}

func TestMessageRecordRepositoryConcurrency(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	// 并发创建记录
	const goroutines = 10
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer func() { done <- true }()

			ctx := context.Background()
			msgID := osx.HashUnixMicroCipherText()
			msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
			_, err := repo.CreateFromMessage(ctx, msg, 3, nil)
			assert.NoError(t, err)

			// 清理
			defer func() {
				_ = repo.DeleteByMessageID(ctx, msgID)
			}()
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

// TestMessageRecordStatusUpdateFields 测试消息状态更新时字段是否正确更新
func TestMessageRecordStatusUpdateFields(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	msgID := osx.HashUnixMicroCipherText()
	ctx := context.Background()
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID)
	}()

	// 1. 创建Pending状态的记录
	msg := createTestHubMessage(msgID, "sender-001", "receiver-001", MessageTypeText)
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.1.100",
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)
	err = repo.Create(ctx, record)
	require.NoError(t, err)

	// 2. 更新为Sending状态，验证first_send_time被设置
	err = repo.UpdateStatus(ctx, msgID, MessageSendStatusSending, "", "")
	assert.NoError(t, err)

	record1, err := repo.FindByMessageID(ctx, msgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusSending, record1.Status)
	assert.NotNil(t, record1.FirstSendTime, "更新为Sending状态时应设置FirstSendTime")
	assert.NotNil(t, record1.LastSendTime, "更新为Sending状态时应设置LastSendTime")
	firstSendTime := record1.FirstSendTime
	lastSendTime := record1.LastSendTime

	time.Sleep(100 * time.Millisecond)

	// 3. 更新为Success状态，验证success_time被设置
	err = repo.UpdateStatus(ctx, msgID, MessageSendStatusSuccess, "", "")
	assert.NoError(t, err)

	record2, err := repo.FindByMessageID(ctx, msgID)
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
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	msgID := osx.HashUnixMicroCipherText()
	ctx := context.Background()
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID)
	}()

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
		Data:                map[string]interface{}{"test": "data"},
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
	err = repo.Create(ctx, record)
	require.NoError(t, err)

	// 2. 更新为Sending状态
	err = repo.UpdateStatus(ctx, msgID, MessageSendStatusSending, "", "")
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// 3. 更新为Failed状态，验证failure_reason和error_message被设置
	testReason := FailureReasonConnError
	testError := "connection timeout"
	err = repo.UpdateStatus(ctx, msgID, MessageSendStatusFailed, testReason, testError)
	assert.NoError(t, err)

	record1, err := repo.FindByMessageID(ctx, msgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, record1.Status)
	assert.Equal(t, testReason, record1.FailureReason, "失败时应设置FailureReason")
	assert.Equal(t, testError, record1.ErrorMessage, "失败时应设置ErrorMessage")
	assert.NotNil(t, record1.LastSendTime, "LastSendTime应被更新")

	t.Log("✅ 消息发送失败状态字段更新验证通过")
}

// TestMessageRecordRetryFields 测试消息重试时字段是否正确更新
func TestMessageRecordRetryFields(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())

	msgID := osx.HashUnixMicroCipherText()
	ctx := context.Background()
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msgID)
	}()

	// 1. 创建记录
	msg := createTestHubMessage(msgID, "sender-001", "receiver-001", MessageTypeText)
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.1.100",
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)
	err = repo.Create(ctx, record)
	require.NoError(t, err)

	// 2. 第一次重试（失败）
	attempt1 := RetryAttempt{
		AttemptNumber: 1,
		Timestamp:     time.Now(),
		Duration:      100 * time.Millisecond,
		Error:         "first retry error",
		Success:       false,
	}
	err = repo.IncrementRetry(ctx, msgID, attempt1)
	assert.NoError(t, err)

	record1, err := repo.FindByMessageID(ctx, msgID)
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
	err = repo.IncrementRetry(ctx, msgID, attempt2)
	assert.NoError(t, err)

	record2, err := repo.FindByMessageID(ctx, msgID)
	require.NoError(t, err)
	assert.Equal(t, 2, record2.RetryCount, "重试次数应为2")
	assert.Equal(t, MessageSendStatusSuccess, record2.Status, "重试成功状态应为Success")
	assert.NotNil(t, record2.SuccessTime, "重试成功应设置SuccessTime")

	t.Log("✅ 消息重试字段更新验证通过")
}

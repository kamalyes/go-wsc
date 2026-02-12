/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:19:53
 * @FilePath: \go-wsc\hub_message_status_update_test.go
 * @Description: Hub消息状态更新测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubUpdateMessageSendStatusSuccess 测试消息状态更新成功
func TestHubUpdateMessageSendStatusSuccess(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	ctx := context.Background()
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())
	hub := NewHub(wscconfig.Default())
	hub.SetMessageRecordRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	msg := createTestHubMessage(MessageTypeText)
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msg.MessageID)
	}()
	created, err := repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)

	// 🔥 校验创建的记录有正确的ID
	assert.Equal(t, msg.MessageID, created.MessageID, "创建的记录应该使用业务消息ID")
	assert.Equal(t, msg.ID, created.HubID, "创建的记录应该保存Hub内部ID")

	// 🔥 Hub 内部通过 MessageID 更新状态
	err = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
	require.NoError(t, err)

	// 🔥 使用业务消息ID查询
	record, err := repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, record.Status)
	assert.NotNil(t, record.SuccessTime)
	assert.Equal(t, msg.MessageID, record.MessageID, "状态更新后 MessageID 应该保持不变")
}

// TestHubUpdateMessageSendStatusFailed 测试消息状态更新为失败
func TestHubUpdateMessageSendStatusFailed(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	ctx := context.Background()
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())
	hub := NewHub(wscconfig.Default())
	hub.SetMessageRecordRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	msg := createTestHubMessage(MessageTypeText)
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msg.MessageID)
	}()

	created, err := repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)

	// 🔥 校验 ID 正确性
	assert.Equal(t, msg.MessageID, created.MessageID, "业务消息ID")
	assert.Equal(t, msg.ID, created.HubID, "Hub内部ID")

	errorMsg := "network timeout"
	// 🔥 使用 MessageID 更新状态
	err = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusFailed, FailureReasonNetworkError, errorMsg)
	require.NoError(t, err)

	// 🔥 使用业务消息ID查询
	record, err := repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, record.Status)
	assert.Equal(t, FailureReasonNetworkError, record.FailureReason)
	assert.Equal(t, errorMsg, record.ErrorMessage)
	assert.Equal(t, msg.MessageID, record.MessageID, "失败状态下 MessageID 应该不变")
}

// TestHubUpdateMessageSendStatusRecordNotExist 测试记录不存在时的处理
func TestHubUpdateMessageSendStatusRecordNotExist(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	ctx := context.Background()
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())
	hub := NewHub(wscconfig.Default())
	hub.SetMessageRecordRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	msg := createTestHubMessage(MessageTypeText)
	_, err := json.Marshal(msg)
	require.NoError(t, err)

	err = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
	require.NoError(t, err) // 记录不存在时静默返回

	_, err = repo.FindByMessageID(ctx, msg.MessageID)
	assert.Error(t, err)
}

// TestHubUpdateMessageSendStatusRetryMechanism 测试重试机制
func TestHubUpdateMessageSendStatusRetryMechanism(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	ctx := context.Background()
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())
	hub := NewHub(wscconfig.Default())
	hub.SetMessageRecordRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	msg := createTestHubMessage(MessageTypeText)
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msg.MessageID)
	}()

	_, err := json.Marshal(msg)
	require.NoError(t, err)

	err = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusPending, "", "")
	time.Sleep(50 * time.Millisecond)

	_, err = repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	record, err := repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusPending, record.Status)
}

// TestHubUpdateMessageSendStatusConcurrent 测试并发更新
func TestHubUpdateMessageSendStatusConcurrent(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	ctx := context.Background()
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())
	hub := NewHub(wscconfig.Default())
	hub.SetMessageRecordRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	msg := createTestHubMessage(MessageTypeText)
	defer func() {
		_ = repo.DeleteByMessageID(ctx, msg.MessageID)
	}()

	_, err := repo.CreateFromMessage(ctx, msg, 3, nil)
	require.NoError(t, err)

	var wg sync.WaitGroup
	concurrency := 10
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(index int) {
			defer wg.Done()
			if index%2 == 0 {
				_ = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
			} else {
				_ = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusFailed, FailureReasonNetworkError, "test error")
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(300 * time.Millisecond)

	record, err := repo.FindByMessageID(ctx, msg.MessageID)
	require.NoError(t, err)
	assert.NotNil(t, record)
	assert.Contains(t, []MessageSendStatus{MessageSendStatusSuccess, MessageSendStatusFailed}, record.Status)
}

// TestHubUpdateMessageSendStatusMultipleMessages 测试批量更新多条消息
func TestHubUpdateMessageSendStatusMultipleMessages(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	ctx := context.Background()
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())
	hub := NewHub(wscconfig.Default())
	hub.SetMessageRecordRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	messageCount := 20
	messageIDs := make([]string, messageCount)

	defer func() {
		for _, msgID := range messageIDs {
			_ = repo.DeleteByMessageID(ctx, msgID)
		}
	}()

	for i := 0; i < messageCount; i++ {
		msg := createTestHubMessage(MessageTypeText)
		messageIDs[i] = msg.MessageID
		_, err := repo.CreateFromMessage(ctx, msg, 3, nil)
		require.NoError(t, err)

		err = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
		require.NoError(t, err)
	}

	time.Sleep(500 * time.Millisecond)

	successCount := 0
	for _, msgID := range messageIDs {
		record, err := repo.FindByMessageID(ctx, msgID)
		if err == nil && record.Status == MessageSendStatusSuccess {
			successCount++
		}
	}

	assert.GreaterOrEqual(t, successCount, messageCount-2, "Most messages should be updated successfully")
}

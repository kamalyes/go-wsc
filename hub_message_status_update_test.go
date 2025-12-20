/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 11:40:00
 * @FilePath: \go-wsc\hub_message_status_update_test.go
 * @Description: Hub消息状态更新测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubUpdateMessageSendStatusSuccess 测试消息状态更新成功
func TestHubUpdateMessageSendStatusSuccess(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)
	hub := NewHub(nil)
	hub.messageRecordRepo = repo

	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	defer hub.cancel()

	msgID := osx.HashUnixMicroCipherText()
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-001", "receiver-001", MessageTypeText)
	created, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	// 🔥 校验创建的记录有正确的ID
	assert.Equal(t, msgID, created.MessageID, "创建的记录应该使用业务消息ID")
	assert.Equal(t, msg.ID, created.HubID, "创建的记录应该保存Hub内部ID")

	messageData, err := json.Marshal(msg)
	require.NoError(t, err)

	// 🔥 Hub 内部通过 MessageID 更新状态
	hub.updateMessageSendStatus(messageData, MessageSendStatusSuccess, "", "")
	time.Sleep(200 * time.Millisecond)

	// 🔥 使用业务消息ID查询
	record, err := repo.FindByMessageID(msgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, record.Status)
	assert.NotNil(t, record.SuccessTime)
	assert.Equal(t, msgID, record.MessageID, "状态更新后 MessageID 应该保持不变")
}

// TestHubUpdateMessageSendStatusFailed 测试消息状态更新为失败
func TestHubUpdateMessageSendStatusFailed(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)
	hub := NewHub(nil)
	hub.messageRecordRepo = repo

	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	defer hub.cancel()

	msgID := osx.HashUnixMicroCipherText()
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-002", "receiver-002", MessageTypeText)
	created, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	// 🔥 校验 ID 正确性
	assert.Equal(t, msgID, created.MessageID, "业务消息ID")
	assert.Equal(t, msg.ID, created.HubID, "Hub内部ID")

	messageData, err := json.Marshal(msg)
	require.NoError(t, err)

	errorMsg := "network timeout"
	// 🔥 使用 MessageID 更新状态
	hub.updateMessageSendStatus(messageData, MessageSendStatusFailed, FailureReasonNetworkError, errorMsg)
	time.Sleep(200 * time.Millisecond)

	// 🔥 使用业务消息ID查询
	record, err := repo.FindByMessageID(msgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, record.Status)
	assert.Equal(t, FailureReasonNetworkError, record.FailureReason)
	assert.Equal(t, errorMsg, record.ErrorMessage)
	assert.Equal(t, msgID, record.MessageID, "失败状态下 MessageID 应该不变")
} 

// TestHubUpdateMessageSendStatusRecordNotExist 测试记录不存在时的处理
func TestHubUpdateMessageSendStatusRecordNotExist(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)
	hub := NewHub(nil)
	hub.messageRecordRepo = repo

	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	defer hub.cancel()

	msgID := osx.HashUnixMicroCipherText()
	msg := createTestHubMessage(msgID, "sender-003", "receiver-003", MessageTypeText)
	messageData, err := json.Marshal(msg)
	require.NoError(t, err)

	hub.updateMessageSendStatus(messageData, MessageSendStatusSuccess, "", "")
	time.Sleep(200 * time.Millisecond)

	_, err = repo.FindByMessageID(msgID)
	assert.Error(t, err)
}

// TestHubUpdateMessageSendStatusRetryMechanism 测试重试机制
func TestHubUpdateMessageSendStatusRetryMechanism(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)
	hub := NewHub(nil)
	hub.messageRecordRepo = repo

	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	defer hub.cancel()

	msgID := osx.HashUnixMicroCipherText()
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-004", "receiver-004", MessageTypeText)
	messageData, err := json.Marshal(msg)
	require.NoError(t, err)

	hub.updateMessageSendStatus(messageData, MessageSendStatusPending, "", "")
	time.Sleep(50 * time.Millisecond)

	_, err = repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)
	time.Sleep(200 * time.Millisecond)

	record, err := repo.FindByMessageID(msgID)
	require.NoError(t, err)
	assert.Equal(t, MessageSendStatusPending, record.Status)
}

// TestHubUpdateMessageSendStatusConcurrent 测试并发更新
func TestHubUpdateMessageSendStatusConcurrent(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)
	hub := NewHub(nil)
	hub.messageRecordRepo = repo

	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	defer hub.cancel()

	msgID := osx.HashUnixMicroCipherText()
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-005", "receiver-005", MessageTypeText)
	_, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	messageData, err := json.Marshal(msg)
	require.NoError(t, err)

	var wg sync.WaitGroup
	concurrency := 10
	wg.Add(concurrency)

	for i := 0; i < concurrency; i++ {
		go func(index int) {
			defer wg.Done()
			if index%2 == 0 {
				hub.updateMessageSendStatus(messageData, MessageSendStatusSuccess, "", "")
			} else {
				hub.updateMessageSendStatus(messageData, MessageSendStatusFailed, FailureReasonNetworkError, "test error")
			}
		}(i)
	}

	wg.Wait()
	time.Sleep(300 * time.Millisecond)

	record, err := repo.FindByMessageID(msgID)
	require.NoError(t, err)
	assert.NotNil(t, record)
	assert.Contains(t, []MessageSendStatus{MessageSendStatusSuccess, MessageSendStatusFailed}, record.Status)
}

// TestHubUpdateMessageSendStatusMultipleMessages 测试批量更新多条消息
func TestHubUpdateMessageSendStatusMultipleMessages(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)
	hub := NewHub(nil)
	hub.messageRecordRepo = repo

	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	defer hub.cancel()

	messageCount := 20
	messageIDs := make([]string, messageCount)

	defer func() {
		for _, msgID := range messageIDs {
			_ = repo.DeleteByMessageID(msgID)
		}
	}()

	for i := 0; i < messageCount; i++ {
		msgID := osx.HashUnixMicroCipherText()
		messageIDs[i] = msgID

		msg := createTestHubMessage(msgID, "sender-bulk", "receiver-bulk", MessageTypeText)
		_, err := repo.CreateFromMessage(msg, 3, nil)
		require.NoError(t, err)

		messageData, err := json.Marshal(msg)
		require.NoError(t, err)

		hub.updateMessageSendStatus(messageData, MessageSendStatusSuccess, "", "")
	}

	time.Sleep(500 * time.Millisecond)

	successCount := 0
	for _, msgID := range messageIDs {
		record, err := repo.FindByMessageID(msgID)
		if err == nil && record.Status == MessageSendStatusSuccess {
			successCount++
		}
	}

	assert.GreaterOrEqual(t, successCount, messageCount-2, "Most messages should be updated successfully")
}

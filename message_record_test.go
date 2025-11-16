/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-15
 * @FilePath: \go-wsc\message_record_test.go
 * @Description: 消息记录功能测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

// TestMessageRecordManager 测试消息记录管理器
func TestMessageRecordManager(t *testing.T) {
	t.Run("创建消息记录", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		assert.NotNil(t, mrm)
		assert.Equal(t, 0, mrm.GetRecordCount())

		msg := &HubMessage{
			ID:      "test-msg-1",
			Type:    MessageTypeText,
			Content: "Test message",
			To:      "user-1",
		}

		record := mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		assert.NotNil(t, record)
		assert.Equal(t, "test-msg-1", record.MessageID)
		assert.Equal(t, MessageSendStatusPending, record.Status)
		assert.Equal(t, 3, record.MaxRetry)
		assert.Equal(t, 1, mrm.GetRecordCount())
	})

	t.Run("更新记录状态", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		msg := &HubMessage{
			ID:      "test-msg-2",
			Type:    MessageTypeText,
			Content: "Test message",
		}

		record := mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		assert.Equal(t, MessageSendStatusPending, record.Status)

		mrm.UpdateRecordStatus("test-msg-2", MessageSendStatusSending, "", "")
		updatedRecord, _ := mrm.GetRecord("test-msg-2")
		assert.Equal(t, MessageSendStatusSending, updatedRecord.Status)

		mrm.UpdateRecordStatus("test-msg-2", MessageSendStatusSuccess, "", "")
		updatedRecord, _ = mrm.GetRecord("test-msg-2")
		assert.Equal(t, MessageSendStatusSuccess, updatedRecord.Status)
		assert.False(t, updatedRecord.SuccessTime.IsZero())
	})

	t.Run("记录重试尝试", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		msg := &HubMessage{
			ID:      "test-msg-3",
			Type:    MessageTypeText,
			Content: "Test message",
		}

		record := mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		assert.Equal(t, 0, len(record.RetryHistory))

		// 第一次重试失败
		mrm.RecordRetryAttempt("test-msg-3", 1, 100*time.Millisecond, assert.AnError, false)
		updatedRecord, _ := mrm.GetRecord("test-msg-3")
		assert.Equal(t, 1, len(updatedRecord.RetryHistory))
		assert.Equal(t, 1, updatedRecord.RetryCount)
		assert.Equal(t, MessageSendStatusRetrying, updatedRecord.Status)

		// 第二次重试成功
		mrm.RecordRetryAttempt("test-msg-3", 2, 120*time.Millisecond, nil, true)
		updatedRecord, _ = mrm.GetRecord("test-msg-3")
		assert.Equal(t, 2, len(updatedRecord.RetryHistory))
		assert.Equal(t, 2, updatedRecord.RetryCount)
		assert.Equal(t, MessageSendStatusSuccess, updatedRecord.Status)
	})

	t.Run("标记用户离线", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)
		msg := &HubMessage{
			ID:      "test-msg-4",
			Type:    MessageTypeText,
			Content: "Test message",
			To:      "offline-user",
		}

		mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		mrm.MarkUserOffline("test-msg-4")

		record, _ := mrm.GetRecord("test-msg-4")
		assert.True(t, record.UserOffline)
		assert.Equal(t, MessageSendStatusUserOffline, record.Status)
		assert.Equal(t, FailureReasonUserOffline, record.FailureReason)
	})

	t.Run("获取失败的记录", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		// 创建多条记录
		for i := 0; i < 5; i++ {
			msg := &HubMessage{
				ID:      string(rune('a' + i)),
				Type:    MessageTypeText,
				Content: "Test message",
			}
			mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
		}

		// 设置不同状态
		mrm.UpdateRecordStatus("a", MessageSendStatusSuccess, "", "")
		mrm.UpdateRecordStatus("b", MessageSendStatusFailed, FailureReasonQueueFull, "Queue full")
		mrm.UpdateRecordStatus("c", MessageSendStatusAckTimeout, FailureReasonAckTimeout, "Timeout")
		mrm.UpdateRecordStatus("d", MessageSendStatusSuccess, "", "")
		mrm.UpdateRecordStatus("e", MessageSendStatusFailed, FailureReasonNetworkError, "Network error")

		failed := mrm.GetFailedRecords()
		assert.Equal(t, 3, len(failed)) // b, c, e
	})

	t.Run("获取可重试的记录", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		// 失败但未达到最大重试次数
		msg1 := &HubMessage{ID: "retry-1", Type: MessageTypeText}
		record1 := mrm.CreateRecord(msg1, 3, time.Now().Add(time.Hour))
		mrm.UpdateRecordStatus("retry-1", MessageSendStatusFailed, FailureReasonNetworkError, "error")
		record1.RetryCount = 1

		// 失败且已达到最大重试次数
		msg2 := &HubMessage{ID: "retry-2", Type: MessageTypeText}
		record2 := mrm.CreateRecord(msg2, 3, time.Now().Add(time.Hour))
		mrm.UpdateRecordStatus("retry-2", MessageSendStatusFailed, FailureReasonMaxRetry, "max retry")
		record2.RetryCount = 3

		// 已过期
		msg3 := &HubMessage{ID: "retry-3", Type: MessageTypeText}
		mrm.CreateRecord(msg3, 3, time.Now().Add(-time.Hour))
		mrm.UpdateRecordStatus("retry-3", MessageSendStatusFailed, FailureReasonExpired, "expired")

		retryable := mrm.GetRetryableRecords()
		assert.Equal(t, 1, len(retryable)) // 只有retry-1可重试
		assert.Equal(t, "retry-1", retryable[0].MessageID)
	})

	t.Run("清理过期记录", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, 100*time.Millisecond, nil)

		// 创建记录
		msg := &HubMessage{ID: "expire-1", Type: MessageTypeText}
		mrm.CreateRecord(msg, 3, time.Now().Add(-time.Hour))

		// 等待过期
		time.Sleep(200 * time.Millisecond)

		cleaned := mrm.CleanupExpiredRecords()
		assert.Equal(t, 1, cleaned)
		assert.Equal(t, 0, mrm.GetRecordCount())
	})

	t.Run("统计信息", func(t *testing.T) {
		mrm := NewMessageRecordManager(100, time.Hour, nil)

		// 创建不同状态的记录
		statuses := []MessageSendStatus{
			MessageSendStatusPending,
			MessageSendStatusSending,
			MessageSendStatusSuccess,
			MessageSendStatusFailed,
			MessageSendStatusRetrying,
		}

		for i, status := range statuses {
			msg := &HubMessage{ID: string(rune('a' + i)), Type: MessageTypeText}
			mrm.CreateRecord(msg, 3, time.Now().Add(time.Hour))
			mrm.UpdateRecordStatus(string(rune('a'+i)), status, "", "")
		}

		stats := mrm.GetStatistics()
		assert.Equal(t, 5, stats["total"])
		assert.Equal(t, 1, stats["pending"])
		assert.Equal(t, 1, stats["sending"])
		assert.Equal(t, 1, stats["success"])
		assert.Equal(t, 1, stats["failed"])
		assert.Equal(t, 1, stats["retrying"])
	})
}

// TestHubWithMessageRecord 测试Hub集成消息记录
func TestHubWithMessageRecord(t *testing.T) {
	t.Run("启用消息记录的Hub", func(t *testing.T) {
		config := wscconfig.Default().
			WithGroup(wscconfig.DefaultGroup().
				Enable().
				WithMessageRecord(true))

		hub := NewHub(config)
		assert.NotNil(t, hub.recordManager)

		go hub.Run()
		defer hub.Shutdown()

		// 注册客户端
		client := &Client{
			ID:       "client-1",
			UserID:   "user-1",
			SendChan: make(chan []byte, 10),
			Context:  context.Background(),
		}
		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 发送消息
		ctx := context.WithValue(context.Background(), ContextKeySenderID, "sender-1")
		msg := &HubMessage{
			Type:    MessageTypeText,
			Content: "Test message with record",
		}

		err := hub.SendToUser(ctx, "user-1", msg)
		assert.NoError(t, err)
	})

	t.Run("查询消息统计", func(t *testing.T) {
		config := wscconfig.Default().
			WithGroup(wscconfig.DefaultGroup().
				Enable().
				WithMessageRecord(true))

		hub := NewHub(config)
		go hub.Run()

		// 等待Hub启动
		time.Sleep(100 * time.Millisecond)

		defer hub.Shutdown()

		stats := hub.GetMessageStatistics()
		assert.Equal(t, 1, stats["enabled"])
		assert.Equal(t, 0, stats["total"])
	})

	t.Run("未启用消息记录", func(t *testing.T) {
		config := wscconfig.Default().
			WithGroup(wscconfig.DefaultGroup().
				Enable().
				WithMessageRecord(false))

		hub := NewHub(config)
		assert.Nil(t, hub.recordManager)

		stats := hub.GetMessageStatistics()
		assert.Equal(t, 0, stats["enabled"])
	})
}

// TestMessageRecordRetry 测试消息重试功能
func TestMessageRecordRetry(t *testing.T) {
	t.Run("重试失败消息", func(t *testing.T) {
		config := wscconfig.Default().
			WithGroup(wscconfig.DefaultGroup().
				Enable().
				WithMessageRecord(true)).
			WithTicket(wscconfig.DefaultTicket().
				Enable().
				WithAck(true, 200, 2))

		hub := NewHub(config)
		go hub.Run()
		defer hub.Shutdown()

		// 注册客户端
		client := &Client{
			ID:       "client-1",
			UserID:   "user-1",
			SendChan: make(chan []byte, 10),
			Context:  context.Background(),
		}
		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 发送消息（会超时）
		ctx := context.WithValue(context.Background(), ContextKeySenderID, "sender-1")
		msg := &HubMessage{
			ID:      "retry-test-msg",
			Type:    MessageTypeText,
			Content: "Test retry message",
		}

		_, _ = hub.SendToUserWithAck(ctx, "user-1", msg, 0, 0)
		time.Sleep(500 * time.Millisecond)

		// 检查记录
		record, exists := hub.GetMessageRecord("retry-test-msg")
		assert.True(t, exists)
		assert.NotNil(t, record)
	})

	t.Run("批量重试失败消息", func(t *testing.T) {
		config := wscconfig.Default().
			WithGroup(wscconfig.DefaultGroup().
				Enable().
				WithMessageRecord(true))

		hub := NewHub(config)
		go hub.Run()

		// 等待Hub启动
		time.Sleep(100 * time.Millisecond)

		defer hub.Shutdown()

		// 创建一些失败记录
		for i := 0; i < 3; i++ {
			msg := &HubMessage{
				ID:      string(rune('a' + i)),
				Type:    MessageTypeText,
				Content: "Test message",
				To:      "user-1",
			}
			hub.recordManager.CreateRecord(msg, 3, time.Now().Add(time.Hour))
			hub.recordManager.UpdateRecordStatus(string(rune('a'+i)), MessageSendStatusFailed, FailureReasonNetworkError, "test")
		}

		retryable := hub.GetRetryableMessages()
		assert.Equal(t, 3, len(retryable))
	})
}

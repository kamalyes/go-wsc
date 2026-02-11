/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-29 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 00:00:00
 * @FilePath: \go-wsc\batch_sender_test.go
 * @Description: BatchSender 批量发送测试
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
	"github.com/stretchr/testify/assert"
)

// TestBatchSender_Basic 测试批量发送基础功能
func TestBatchSender_Basic(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	// 创建批量发送器
	sender := hub.NewBatchSender(context.Background())

	// 为 user-1 添加 5 条消息
	idGen := hub.GetIDGenerator()
	for i := 0; i < 5; i++ {
		sender.AddMessage("user-1", &HubMessage{
			ID:          idGen.GenerateTraceID(),
			MessageID:   idGen.GenerateRequestID(),
			MessageType: MessageTypeText,
			Content:     fmt.Sprintf("Message %d for user-1", i),
		})
	}

	// 为 user-2 添加 3 条消息
	sender.AddMessages("user-2",
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 1 for user-2"},
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 2 for user-2"},
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 3 for user-2"},
	)

	// 为 user-3 添加 2 条消息
	sender.AddUserMessages(map[string][]*HubMessage{
		"user-3": {
			{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 1 for user-3"},
			{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 2 for user-3"},
		},
	})

	// 检查统计
	users, messages := sender.Count()
	assert.Equal(t, 3, users)
	assert.Equal(t, 10, messages)

	// 执行发送（用户不在线，会失败但不影响测试逻辑）
	result := sender.Execute()

	// 验证结果
	assert.Equal(t, 3, result.TotalUsers)
	assert.Equal(t, 10, result.TotalMessages)

	t.Logf("批量发送完成: 成功=%d, 失败=%d", result.SuccessCount, result.FailureCount)
}

// TestBatchSender_ChainCall 测试链式调用
func TestBatchSender_ChainCall(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	// 测试链式调用
	idGen := hub.GetIDGenerator()
	result := hub.NewBatchSender(context.Background()).
		AddMessage("test-user", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 1"}).
		AddMessage("test-user", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 2"}).
		AddMessage("test-user", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 3"}).
		Execute()

	assert.Equal(t, 1, result.TotalUsers)
	assert.Equal(t, 3, result.TotalMessages)
}

// TestBatchSender_EmptyExecution 测试空执行
func TestBatchSender_EmptyExecution(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()

	sender := hub.NewBatchSender(context.Background())
	result := sender.Execute()

	assert.Equal(t, 0, result.TotalUsers)
	assert.Equal(t, 0, result.TotalMessages)
	assert.Equal(t, int32(0), result.SuccessCount)
}

// TestBatchSender_NonExistentUser 测试不存在的用户
func TestBatchSender_NonExistentUser(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	sender := hub.NewBatchSender(context.Background())
	idGen := hub.GetIDGenerator()
	sender.AddMessage("non-existent-user", &HubMessage{
		ID:          idGen.GenerateTraceID(),
		MessageID:   idGen.GenerateRequestID(),
		MessageType: MessageTypeText,
		Content:     "Test message",
	})

	result := sender.Execute()

	assert.Equal(t, 1, result.TotalUsers)
	assert.Equal(t, 1, result.TotalMessages)
	// 不存在的用户发送会失败
	assert.Greater(t, result.FailureCount, int32(0))
}

// TestBatchSender_Clear 测试清空功能
func TestBatchSender_Clear(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()

	sender := hub.NewBatchSender(context.Background())
	idGen := hub.GetIDGenerator()
	sender.AddMessage("user-1", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Test"})
	sender.AddMessage("user-2", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Test"})

	users, messages := sender.Count()
	assert.Equal(t, 2, users)
	assert.Equal(t, 2, messages)

	sender.Clear()
	users, messages = sender.Count()
	assert.Equal(t, 0, users)
	assert.Equal(t, 0, messages)
}

// TestBatchSender_Concurrent 测试并发批量发送
func TestBatchSender_Concurrent(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	sender := hub.NewBatchSender(context.Background())

	// 为每个用户添加50条消息
	idGen := hub.GetIDGenerator()
	for i := 0; i < 10; i++ {
		userID := fmt.Sprintf("user-%d", i)
		msgs := make([]*HubMessage, 50)
		for j := 0; j < 50; j++ {
			msgs[j] = &HubMessage{
				ID:          idGen.GenerateTraceID(),
				MessageID:   idGen.GenerateRequestID(),
				MessageType: MessageTypeText,
				Content:     fmt.Sprintf("Message %d for %s", j, userID),
			}
		}
		sender.AddMessages(userID, msgs...)
	}

	users, messages := sender.Count()
	assert.Equal(t, 10, users)
	assert.Equal(t, 500, messages)

	// 执行批量发送
	start := time.Now()
	result := sender.Execute()
	duration := time.Since(start)

	t.Logf("批量发送 %d 个用户的 %d 条消息耗时: %v", result.TotalUsers, result.TotalMessages, duration)

	assert.Equal(t, 10, result.TotalUsers)
	assert.Equal(t, 500, result.TotalMessages)
}

// TestBatchSender_ExecuteAsync 测试异步执行
func TestBatchSender_ExecuteAsync(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	sender := hub.NewBatchSender(context.Background())
	idGen := hub.GetIDGenerator()
	sender.AddMessage("test-user", &HubMessage{
		ID:          idGen.GenerateTraceID(),
		MessageID:   idGen.GenerateRequestID(),
		MessageType: MessageTypeText,
		Content:     "Async message",
	})

	// 异步执行
	done := make(chan bool)
	sender.ExecuteAsync(func(result *BatchSendResult) {
		assert.Equal(t, 1, result.TotalMessages)
		done <- true
	})

	select {
	case <-done:
		// 成功
	case <-time.After(5 * time.Second):
		t.Fatal("异步执行超时")
	}
}

// TestBatchSender_MixedSuccess 测试混合成功失败
func TestBatchSender_MixedSuccess(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	sender := hub.NewBatchSender(context.Background())

	// 添加多个用户的消息（都不在线，会失败）
	idGen := hub.GetIDGenerator()
	sender.AddMessage("user-1", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "For user-1"})
	sender.AddMessage("user-1", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "For user-1"})
	sender.AddMessage("user-2", &HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "For user-2"})

	result := sender.Execute()

	assert.Equal(t, 2, result.TotalUsers)
	assert.Equal(t, 3, result.TotalMessages)
	t.Logf("成功=%d, 失败=%d", result.SuccessCount, result.FailureCount)
}

// TestBatchSender_FailureCallback 测试批量发送失败回调
func TestBatchSender_FailureCallback(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	// 记录失败的消息
	var failedMessages []struct {
		UserID string
		MsgID  string
		Error  error
	}
	var mu sync.Mutex

	// 注册批量发送失败回调
	hub.OnBatchSendFailure(func(userID string, msg *HubMessage, err error) {
		mu.Lock()
		defer mu.Unlock()
		failedMessages = append(failedMessages, struct {
			UserID string
			MsgID  string
			Error  error
		}{
			UserID: userID,
			MsgID:  msg.ID,
			Error:  err,
		})
		t.Logf("批量发送失败回调触发: userID=%s, msgID=%s, error=%v", userID, msg.ID, err)
	})

	// 不连接任何客户端，所有发送都应该失败
	sender := hub.NewBatchSender(context.Background())

	// 添加多个用户的消息
	idGen := hub.GetIDGenerator()
	sender.AddMessages("user-1",
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 1"},
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 2"},
	)
	sender.AddMessages("user-2",
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 3"},
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 4"},
		&HubMessage{ID: idGen.GenerateTraceID(), MessageID: idGen.GenerateRequestID(), MessageType: MessageTypeText, Content: "Message 5"},
	)

	result := sender.Execute()

	// 验证发送结果
	assert.Equal(t, 2, result.TotalUsers)
	assert.Equal(t, 5, result.TotalMessages)
	assert.Greater(t, result.FailureCount, int32(0), "应该有失败的消息")

	// 等待回调执行完成
	time.Sleep(300 * time.Millisecond)

	// 验证失败回调被触发
	mu.Lock()
	defer mu.Unlock()
	assert.Greater(t, len(failedMessages), 0, "失败回调应该被触发")

	t.Logf("总共触发了 %d 次失败回调", len(failedMessages))
	for _, fm := range failedMessages {
		t.Logf("失败消息: userID=%s, msgID=%s, error=%v", fm.UserID, fm.MsgID, fm.Error)
	}
}

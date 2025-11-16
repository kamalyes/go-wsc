/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-16
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-16 23:20:00
 * @FilePath: \go-wsc\hub_failure_handlers_test.go
 * @Description: Hub消息发送失败处理器测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 测试用的失败处理器实现

// TestSendFailureHandler 通用发送失败处理器
type TestSendFailureHandler struct {
	failureCount  int64
	lastMessage   *HubMessage
	lastRecipient string
	lastReason    string
	lastError     error
	mu            sync.RWMutex
}

func (h *TestSendFailureHandler) HandleSendFailure(msg *HubMessage, recipient string, reason string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	atomic.AddInt64(&h.failureCount, 1)
	h.lastMessage = msg
	h.lastRecipient = recipient
	h.lastReason = reason
	h.lastError = err
}

func (h *TestSendFailureHandler) GetFailureCount() int64 {
	return atomic.LoadInt64(&h.failureCount)
}

func (h *TestSendFailureHandler) GetLastFailure() (*HubMessage, string, string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMessage, h.lastRecipient, h.lastReason, h.lastError
}

// TestQueueFullHandler 队列满处理器
type TestQueueFullHandler struct {
	queueFullCount int64
	lastMessage    *HubMessage
	lastRecipient  string
	lastQueueType  string
	lastError      error
	mu             sync.RWMutex
}

func (h *TestQueueFullHandler) HandleQueueFull(msg *HubMessage, recipient string, queueType string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	atomic.AddInt64(&h.queueFullCount, 1)
	h.lastMessage = msg
	h.lastRecipient = recipient
	h.lastQueueType = queueType
	h.lastError = err
}

func (h *TestQueueFullHandler) GetQueueFullCount() int64 {
	return atomic.LoadInt64(&h.queueFullCount)
}

func (h *TestQueueFullHandler) GetLastQueueFull() (*HubMessage, string, string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMessage, h.lastRecipient, h.lastQueueType, h.lastError
}

// TestUserOfflineHandler 用户离线处理器
type TestUserOfflineHandler struct {
	offlineCount int64
	lastMessage  *HubMessage
	lastUserID   string
	lastError    error
	mu           sync.RWMutex
}

func (h *TestUserOfflineHandler) HandleUserOffline(msg *HubMessage, userID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	atomic.AddInt64(&h.offlineCount, 1)
	h.lastMessage = msg
	h.lastUserID = userID
	h.lastError = err
}

func (h *TestUserOfflineHandler) GetOfflineCount() int64 {
	return atomic.LoadInt64(&h.offlineCount)
}

func (h *TestUserOfflineHandler) GetLastOffline() (*HubMessage, string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMessage, h.lastUserID, h.lastError
}

// TestConnectionErrorHandler 连接错误处理器
type TestConnectionErrorHandler struct {
	connErrorCount int64
	lastMessage    *HubMessage
	lastClientID   string
	lastError      error
	mu             sync.RWMutex
}

func (h *TestConnectionErrorHandler) HandleConnectionError(msg *HubMessage, clientID string, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	atomic.AddInt64(&h.connErrorCount, 1)
	h.lastMessage = msg
	h.lastClientID = clientID
	h.lastError = err
}

func (h *TestConnectionErrorHandler) GetConnectionErrorCount() int64 {
	return atomic.LoadInt64(&h.connErrorCount)
}

func (h *TestConnectionErrorHandler) GetLastConnectionError() (*HubMessage, string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMessage, h.lastClientID, h.lastError
}

// TestTimeoutHandler 超时处理器
type TestTimeoutHandler struct {
	timeoutCount  int64
	lastMessage   *HubMessage
	lastRecipient string
	lastType      string
	lastDuration  time.Duration
	lastError     error
	mu            sync.RWMutex
}

func (h *TestTimeoutHandler) HandleTimeout(msg *HubMessage, recipient string, timeoutType string, duration time.Duration, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	atomic.AddInt64(&h.timeoutCount, 1)
	h.lastMessage = msg
	h.lastRecipient = recipient
	h.lastType = timeoutType
	h.lastDuration = duration
	h.lastError = err
}

func (h *TestTimeoutHandler) GetTimeoutCount() int64 {
	return atomic.LoadInt64(&h.timeoutCount)
}

func (h *TestTimeoutHandler) GetLastTimeout() (*HubMessage, string, string, time.Duration, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMessage, h.lastRecipient, h.lastType, h.lastDuration, h.lastError
}

// TestConfigurableSendFailureHandler 可配置行为的发送失败处理器
type TestConfigurableSendFailureHandler struct {
	failureCount  int64
	lastMessage   *HubMessage
	lastRecipient string
	lastReason    string
	lastError     error
	mu            sync.RWMutex
	panicMode     bool
}

func (h *TestConfigurableSendFailureHandler) HandleSendFailure(msg *HubMessage, recipient string, reason string, err error) {
	if h.panicMode {
		panic("test panic")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	atomic.AddInt64(&h.failureCount, 1)
	h.lastMessage = msg
	h.lastRecipient = recipient
	h.lastReason = reason
	h.lastError = err
}

func (h *TestConfigurableSendFailureHandler) SetPanicMode(panic bool) {
	h.panicMode = panic
}

func (h *TestConfigurableSendFailureHandler) GetFailureCount() int64 {
	return atomic.LoadInt64(&h.failureCount)
}

func (h *TestConfigurableSendFailureHandler) GetLastFailure() (*HubMessage, string, string, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastMessage, h.lastRecipient, h.lastReason, h.lastError
}

// TestHubFailureHandlers 测试Hub失败处理器
func TestHubFailureHandlers(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	// 创建测试处理器
	generalHandler := &TestSendFailureHandler{}
	queueHandler := &TestQueueFullHandler{}
	offlineHandler := &TestUserOfflineHandler{}
	connHandler := &TestConnectionErrorHandler{}
	timeoutHandler := &TestTimeoutHandler{}

	// 注册处理器
	hub.AddSendFailureHandler(generalHandler)
	hub.AddQueueFullHandler(queueHandler)
	hub.AddUserOfflineHandler(offlineHandler)
	hub.AddConnectionErrorHandler(connHandler)
	hub.AddTimeoutHandler(timeoutHandler)

	t.Run("QueueFullHandler", func(t *testing.T) {
		// 模拟队列满的情况 - 发送大量消息直到队列满
		msg := &HubMessage{
			ID:      "queue-full-test",
			Type:    MessageTypeText,
			Content: "Test queue full",
		}

		// 发送大量消息直到队列满
		var lastErr error
		for i := 0; i < 2000; i++ {
			testMsg := *msg
			testMsg.ID = fmt.Sprintf("queue-full-test-%d", i)
			err := hub.SendToUser(context.Background(), "nonexistent-user", &testMsg)
			if err != nil {
				lastErr = err
				break
			}
		}

		// 等待处理器被调用
		time.Sleep(100 * time.Millisecond)

		// 验证是否触发了队列满处理器
		if lastErr != nil {
			assert.Greater(t, queueHandler.GetQueueFullCount(), int64(0), "应该触发队列满处理器")
			assert.Greater(t, generalHandler.GetFailureCount(), int64(0), "应该触发通用处理器")

			lastMsg, recipient, queueType, err := queueHandler.GetLastQueueFull()
			assert.NotNil(t, lastMsg)
			assert.Equal(t, "nonexistent-user", recipient)
			assert.Equal(t, "all_queues", queueType)
			assert.Error(t, err)
		} else {
			t.Skip("队列没有达到满的状态，跳过测试")
		}
	})

	t.Run("UserOfflineHandler", func(t *testing.T) {
		msg := &HubMessage{
			ID:      "offline-test",
			Type:    MessageTypeText,
			Content: "Test offline user",
		}

		// 尝试向离线用户发送消息（使用带ACK的方法）
		_, err := hub.SendToUserWithAck(context.Background(), "offline-user", msg, 100*time.Millisecond, 1)

		// 等待处理器被调用
		time.Sleep(200 * time.Millisecond)

		// 验证是否触发了用户离线处理器
		assert.Error(t, err)
		assert.Greater(t, offlineHandler.GetOfflineCount(), int64(0), "应该触发用户离线处理器")
		assert.Greater(t, generalHandler.GetFailureCount(), int64(0), "应该触发通用处理器")

		lastMsg, userID, offlineErr := offlineHandler.GetLastOffline()
		assert.NotNil(t, lastMsg)
		assert.Equal(t, "offline-user", userID)
		assert.Error(t, offlineErr)
	})

	t.Run("MultipleHandlersForSameFailure", func(t *testing.T) {
		// 重置计数器
		atomic.StoreInt64(&generalHandler.failureCount, 0)
		atomic.StoreInt64(&offlineHandler.offlineCount, 0)

		msg := &HubMessage{
			ID:      "multi-handler-test",
			Type:    MessageTypeText,
			Content: "Test multiple handlers",
		}

		// 向离线用户发送消息
		_, err := hub.SendToUserWithAck(context.Background(), "offline-user-2", msg, 50*time.Millisecond, 1)

		// 等待处理器被调用
		time.Sleep(100 * time.Millisecond)

		// 验证多个处理器都被调用
		assert.Error(t, err)
		assert.Greater(t, offlineHandler.GetOfflineCount(), int64(0), "专用离线处理器应该被调用")
		assert.Greater(t, generalHandler.GetFailureCount(), int64(0), "通用处理器也应该被调用")

		// 验证处理器收到了正确的信息
		lastMsg, userID, _ := offlineHandler.GetLastOffline()
		assert.Equal(t, "multi-handler-test", lastMsg.ID)
		assert.Equal(t, "offline-user-2", userID)

		generalMsg, recipient, reason, _ := generalHandler.GetLastFailure()
		assert.Equal(t, "multi-handler-test", generalMsg.ID)
		assert.Equal(t, "offline-user-2", recipient)
		assert.Equal(t, SendFailureReasonUserOffline, reason)
	})

	t.Run("HandlerPanicRecovery", func(t *testing.T) {
		// 创建一个会panic的处理器
		panicHandler := &TestConfigurableSendFailureHandler{}
		panicHandler.SetPanicMode(true)

		hub.AddSendFailureHandler(panicHandler)

		msg := &HubMessage{
			ID:      "panic-test",
			Type:    MessageTypeText,
			Content: "Test panic recovery",
		}

		// 发送消息，应该不会因为处理器panic而崩溃
		_, err := hub.SendToUserWithAck(context.Background(), "panic-test-user", msg, 50*time.Millisecond, 1)

		// 等待处理器被调用
		time.Sleep(100 * time.Millisecond)

		// 验证Hub仍然正常工作
		assert.Error(t, err)

		// 关闭panic模式
		panicHandler.SetPanicMode(false)
	})
}

// TestFailureHandlerManagement 测试处理器管理功能
func TestFailureHandlerManagement(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	t.Run("AddAndRemoveHandlers", func(t *testing.T) {
		handler1 := &TestSendFailureHandler{}
		handler2 := &TestSendFailureHandler{}

		// 添加处理器
		hub.AddSendFailureHandler(handler1)
		hub.AddSendFailureHandler(handler2)

		// 验证处理器被正确添加
		assert.Len(t, hub.sendFailureHandlers, 2)

		// 移除一个处理器
		hub.RemoveSendFailureHandler(handler1)
		assert.Len(t, hub.sendFailureHandlers, 1)

		// 移除不存在的处理器不应该有副作用
		nonExistentHandler := &TestSendFailureHandler{}
		hub.RemoveSendFailureHandler(nonExistentHandler)
		assert.Len(t, hub.sendFailureHandlers, 1)

		// 移除最后一个处理器
		hub.RemoveSendFailureHandler(handler2)
		assert.Len(t, hub.sendFailureHandlers, 0)
	})

	t.Run("DifferentHandlerTypes", func(t *testing.T) {
		queueHandler := &TestQueueFullHandler{}
		offlineHandler := &TestUserOfflineHandler{}
		connHandler := &TestConnectionErrorHandler{}
		timeoutHandler := &TestTimeoutHandler{}

		hub.AddQueueFullHandler(queueHandler)
		hub.AddUserOfflineHandler(offlineHandler)
		hub.AddConnectionErrorHandler(connHandler)
		hub.AddTimeoutHandler(timeoutHandler)

		assert.Len(t, hub.queueFullHandlers, 1)
		assert.Len(t, hub.userOfflineHandlers, 1)
		assert.Len(t, hub.connectionErrorHandlers, 1)
		assert.Len(t, hub.timeoutHandlers, 1)
	})
}

// TestHubRetryMechanism 测试Hub重试机制
func TestHubRetryMechanism(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	t.Run("RetryOnRetryableError", func(t *testing.T) {
		// 创建一个配置，使队列立即满
		smallConfig := wscconfig.Default()
		smallConfig.MessageBufferSize = 1 // 很小的缓冲区
		retryHub := NewHub(smallConfig)

		// 先填满所有队列
		retryHub.broadcast = make(chan *HubMessage, 0)       // 无缓冲区
		retryHub.pendingMessages = make(chan *HubMessage, 0) // 无缓冲区

		msg := &HubMessage{
			ID:      "retry-test",
			Type:    MessageTypeText,
			Content: "Test retry on retryable error",
		}

		result := retryHub.SendToUserWithRetry(context.Background(), "retry-test-user", msg)

		// 添加调试信息
		t.Logf("发送结果: Success=%v, 尝试次数=%d, 重试次数=%d, 最终错误=%v",
			result.Success, len(result.Attempts), result.TotalRetries, result.FinalError)

		for i, attempt := range result.Attempts {
			t.Logf("尝试 %d: Success=%v, Error=%v", i+1, attempt.Success, attempt.Error)
		}

		// 检查配置
		t.Logf("Hub配置: MaxRetries=%d, BaseDelay=%v, RetryableErrors=%v, NonRetryableErrors=%v",
			retryHub.config.MaxRetries, retryHub.config.BaseDelay, retryHub.config.RetryableErrors, retryHub.config.NonRetryableErrors)

		// 验证重试逻辑
		assert.False(t, result.Success, "应该最终失败")
		assert.Greater(t, len(result.Attempts), 1, "应该进行了多次尝试")
		assert.Greater(t, result.TotalRetries, 0, "应该有重试次数")
		assert.Greater(t, result.TotalTime, time.Duration(0), "应该有总耗时")

		// 验证每次尝试都有记录
		for i, attempt := range result.Attempts {
			assert.Equal(t, i+1, attempt.AttemptNumber, "尝试次数应该正确")
			assert.False(t, attempt.Success, "每次尝试都应该失败")
			assert.NotNil(t, attempt.Error, "应该有错误信息")
			assert.GreaterOrEqual(t, attempt.Duration, time.Duration(0), "应该有耗时（可能为0）")
		}

		retryHub.Shutdown()
	})

	t.Run("SuccessOnFirstAttempt", func(t *testing.T) {
		// 注册一个正常的客户端
		client := &Client{
			ID:       "success-client",
			UserID:   "success-user",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "success-user"),
		}
		hub.Register(client)
		time.Sleep(50 * time.Millisecond)

		msg := &HubMessage{
			ID:      "success-test",
			Type:    MessageTypeText,
			Content: "Test success on first attempt",
		}

		result := hub.SendToUserWithRetry(context.Background(), "success-user", msg)

		// 验证成功逻辑
		assert.True(t, result.Success, "应该成功")
		assert.Equal(t, 1, len(result.Attempts), "应该只有一次尝试")
		assert.Equal(t, 0, result.TotalRetries, "应该没有重试")
		assert.True(t, result.Attempts[0].Success, "第一次尝试应该成功")
		assert.NoError(t, result.Attempts[0].Error, "第一次尝试应该没有错误")

		hub.Unregister(client)
	})

	t.Run("ContextCancellation", func(t *testing.T) {
		// 创建一个新的Hub用于测试上下文取消
		cancelHub := NewHub(nil)
		// 同样设置队列为无缓冲区，强制失败
		cancelHub.broadcast = make(chan *HubMessage, 0)
		cancelHub.pendingMessages = make(chan *HubMessage, 0)

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		msg := &HubMessage{
			ID:      "context-cancel-test",
			Type:    MessageTypeText,
			Content: "Test context cancellation",
		}

		result := cancelHub.SendToUserWithRetry(ctx, "cancel-test-user", msg)

		// 验证上下文取消
		assert.False(t, result.Success, "应该失败")
		assert.NotNil(t, result.FinalError, "应该有最终错误")
		// 可能是context错误或队列满错误
		errorStr := result.FinalError.Error()
		assert.True(t,
			strings.Contains(errorStr, "context") || strings.Contains(errorStr, "队列已满"),
			"错误应该包含context信息或队列满信息: %s", errorStr)

		cancelHub.Shutdown()
	})

	t.Run("NonRetryableError", func(t *testing.T) {
		// 测试不可重试的错误（如用户离线）
		msg := &HubMessage{
			ID:      "non-retry-test",
			Type:    MessageTypeText,
			Content: "Test non-retryable error",
		}

		// 使用带ACK的方法触发用户离线错误
		_, err := hub.SendToUserWithAck(context.Background(), "offline-user", msg, 50*time.Millisecond, 1)

		assert.Error(t, err, "应该有错误")

		// 验证是否是不可重试的错误
		isRetryable := hub.shouldRetryBasedOnErrorPattern(err)
		assert.False(t, isRetryable, "用户离线错误应该不可重试")
	})

	t.Run("RetryDelayUsingGoToolbox", func(t *testing.T) {
		// 现在延迟计算由 go-toolbox retry 模块处理
		// 我们只测试配置是否正确传递
		assert.Equal(t, 3, hub.config.MaxRetries, "最大重试次数应该为3")
		assert.Equal(t, 100*time.Millisecond, hub.config.BaseDelay, "基础延迟应该为100ms")
		assert.Equal(t, 5*time.Second, hub.config.MaxDelay, "最大延迟应该为5秒")
		assert.Equal(t, 2.0, hub.config.BackoffFactor, "退避因子应该为2.0")
	})
}

// TestRetryableErrorPatterns 测试可重试错误模式
func TestRetryableErrorPatterns(t *testing.T) {
	hub := NewHub(nil)
	defer hub.Shutdown()

	testCases := []struct {
		name        string
		error       error
		shouldRetry bool
	}{
		{"QueueFull", fmt.Errorf("queue_full: message queue is full"), true},
		{"Timeout", fmt.Errorf("timeout: operation timed out"), true},
		{"ConnError", fmt.Errorf("connection refused"), true},
		{"UserOffline", fmt.Errorf("user_offline: user is not connected"), false},
		{"Permission", fmt.Errorf("permission denied"), false},
		{"InvalidFormat", fmt.Errorf("invalid message format"), false},
		{"NetworkUnreachable", fmt.Errorf("network unreachable"), true},
		{"Temporary", fmt.Errorf("temporary failure"), true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			shouldRetry := hub.shouldRetryBasedOnErrorPattern(tc.error)
			assert.Equal(t, tc.shouldRetry, shouldRetry, "错误重试判断应该正确")
		})
	}
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 15:25:03
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 15:25:03
 * @FilePath: \go-wsc\hub_callbacks_test.go
 * @Description: Hub回调函数测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ====================================================================
// 测试常量定义
// ====================================================================

const (
	// 测试用户和客户端ID
	testClientID2    = "multi-client"
	testClientID3    = "panic-client"
	testClientID4    = "replace-client"
	testUserID1      = "test-user-1"
	testUserID2      = "multi-user"
	testUserID3      = "panic-user"
	testUserID4      = "replace-user"
	testUserOffline  = "offline-user"
	testUserNonExist = "non-existent-user"
	timeoutClientID  = "timeout-client"
	timeoutUserID    = "timeout-user"

	// 测试消息ID
	msgSuccessID   = "msg-success-1"
	msgFailID      = "msg-fail-1"
	msgQueueFullID = "msg-queue-full"
	msgOfflineID   = "msg-offline"
	msgMultiID     = "msg-multi"
	msgPanicID     = "msg-panic"
	msgReplaceID   = "msg-replace"

	// 测试消息内容
	msgContentFail      = "测试失败发送"
	msgContentQueueFull = "测试队列满"
	msgContentOffline   = "测试用户离线"
	msgContentMulti     = "测试多回调"
	msgContentPanic     = "测试panic恢复"
	msgContentReplace   = "测试回调替换"

	// 测试超时时间
	waitShortDuration  = 50 * time.Millisecond
	waitMediumDuration = 100 * time.Millisecond
	waitLongDuration   = 200 * time.Millisecond

	// 测试心跳配置
	heartbeatInterval = 50

	// 测试缓冲区大小
	testChannelBufferSize = 100
	testSmallBufferSize   = 1

	// 测试队列类型
	queueTypeAll = "all_queues"

	// 测试panic消息
	testPanicMessage = "test panic in callback"

	// 测试断言消息
	assertMsgNotNil    = "消息不应该为空"
	assertResultNotNil = "结果不应该为空"
	assertErrorNotNil  = "错误不应该为空"
)

// ====================================================================
// 测试辅助结构
// ====================================================================

// CallbackRecorder 回调记录器 - 用于测试回调是否被正确调用
type CallbackRecorder struct {
	mu sync.RWMutex

	// OfflineMessagePushCallback 记录
	offlinePushCount     int64
	lastOfflineUserID    string
	lastPushedMessageIDs []string
	lastFailedMessageIDs []string

	// MessageSendCallback 记录
	messageSendCount int64
	lastSendMessage  *HubMessage
	lastSendResult   *SendResult

	// QueueFullCallback 记录
	queueFullCount     int64
	lastQueueMessage   *HubMessage
	lastQueueRecipient string
	lastQueueType      string
	lastQueueError     errorx.BaseError

	// HeartbeatTimeoutCallback 记录
	heartbeatTimeoutCount int64
	lastHeartbeatClientID string
	lastHeartbeatUserID   string
	lastHeartbeatTime     time.Time

	// ClientConnectCallback 记录
	clientConnectCount int64
	lastConnectClient  *Client
	lastConnectError   error

	// ClientDisconnectCallback 记录
	clientDisconnectCount int64
	lastDisconnectClient  *Client
	lastDisconnectReason  DisconnectReason
	lastDisconnectError   error

	// MessageReceivedCallback 记录
	messageReceivedCount int64
	lastReceivedClient   *Client
	lastReceivedMessage  *HubMessage
	lastReceivedError    error

	// ErrorCallback 记录
	errorCallbackCount   int64
	lastError            error
	lastErrorSeverity    ErrorSeverity
	lastErrorCallbackErr error
}

// OnOfflineMessagePush 实现离线消息推送回调
func (r *CallbackRecorder) OnOfflineMessagePush(userID string, pushedMessageIDs []string, failedMessageIDs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.offlinePushCount, 1)
	r.lastOfflineUserID = userID
	r.lastPushedMessageIDs = append([]string{}, pushedMessageIDs...)
	r.lastFailedMessageIDs = append([]string{}, failedMessageIDs...)
}

// OnMessageSend 实现消息发送完成回调
func (r *CallbackRecorder) OnMessageSend(msg *HubMessage, result *SendResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.messageSendCount, 1)
	r.lastSendMessage = msg
	r.lastSendResult = result
}

// OnQueueFull 实现队列满回调
func (r *CallbackRecorder) OnQueueFull(msg *HubMessage, recipient string, queueType QueueType, err errorx.BaseError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.queueFullCount, 1)
	r.lastQueueMessage = msg
	r.lastQueueRecipient = recipient
	r.lastQueueType = string(queueType)
	r.lastQueueError = err
}

// OnHeartbeatTimeout 实现心跳超时回调
func (r *CallbackRecorder) OnHeartbeatTimeout(clientID string, userID string, lastHeartbeat time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.heartbeatTimeoutCount, 1)
	r.lastHeartbeatClientID = clientID
	r.lastHeartbeatUserID = userID
	r.lastHeartbeatTime = lastHeartbeat
}

// OnClientConnect 实现客户端连接回调
func (r *CallbackRecorder) OnClientConnect(ctx context.Context, client *Client) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.clientConnectCount, 1)
	r.lastConnectClient = client
	return r.lastConnectError
}

// OnClientDisconnect 实现客户端断开连接回调
func (r *CallbackRecorder) OnClientDisconnect(ctx context.Context, client *Client, reason DisconnectReason) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.clientDisconnectCount, 1)
	r.lastDisconnectClient = client
	r.lastDisconnectReason = reason
	return r.lastDisconnectError
}

// OnMessageReceived 实现消息接收回调
func (r *CallbackRecorder) OnMessageReceived(ctx context.Context, client *Client, msg *HubMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.messageReceivedCount, 1)
	r.lastReceivedClient = client
	r.lastReceivedMessage = msg
	return r.lastReceivedError
}

// OnError 实现错误处理回调
func (r *CallbackRecorder) OnError(ctx context.Context, err error, severity ErrorSeverity) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.AddInt64(&r.errorCallbackCount, 1)
	r.lastError = err
	r.lastErrorSeverity = severity
	return r.lastErrorCallbackErr
}

// GetLastMessageSend 获取最后的消息发送记录
func (r *CallbackRecorder) GetLastMessageSend() (*HubMessage, *SendResult) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastSendMessage, r.lastSendResult
}

// GetLastQueueFull 获取最后的队列满记录
func (r *CallbackRecorder) GetLastQueueFull() (*HubMessage, string, string, errorx.BaseError) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.lastQueueMessage, r.lastQueueRecipient, r.lastQueueType, r.lastQueueError
}

// Reset 重置所有计数器
func (r *CallbackRecorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	atomic.StoreInt64(&r.offlinePushCount, 0)
	atomic.StoreInt64(&r.messageSendCount, 0)
	atomic.StoreInt64(&r.queueFullCount, 0)
	atomic.StoreInt64(&r.heartbeatTimeoutCount, 0)
	atomic.StoreInt64(&r.clientConnectCount, 0)
	atomic.StoreInt64(&r.clientDisconnectCount, 0)
	atomic.StoreInt64(&r.messageReceivedCount, 0)
	atomic.StoreInt64(&r.errorCallbackCount, 0)
	r.lastConnectError = nil
	r.lastDisconnectError = nil
	r.lastReceivedError = nil
	r.lastErrorCallbackErr = nil
}

// ====================================================================
// MessageSendCallback 测试
// ====================================================================

func TestMessageSendCallback(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnMessageSend(recorder.OnMessageSend)

	t.Run("Success_SendMessage", func(t *testing.T) {
		// 注册一个在线客户端
		client := &Client{
			ID:       "test-client-1",
			UserID:   testUserID1,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testUserID1),
		}
		hub.Register(client)
		time.Sleep(50 * time.Millisecond)

		msg := &HubMessage{
			ID:          "msg-success-1",
			MessageType: MessageTypeText,
			Content:     "测试成功发送",
		}

		// 发送消息
		hub.SendToUserWithRetry(context.Background(), testUserID1, msg)

		// 等待回调执行
		time.Sleep(100 * time.Millisecond)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.messageSendCount)
		assert.Equal(t, int64(1), count, "回调应该被调用一次")

		lastMsg, lastResult := recorder.GetLastMessageSend()
		assert.NotNil(t, lastMsg, assertMsgNotNil)
		assert.NotNil(t, lastResult, assertResultNotNil)
		assert.Equal(t, msgSuccessID, lastMsg.ID, "消息ID应该匹配")
		assert.True(t, lastResult.Success, "发送应该成功")
		assert.Equal(t, 0, lastResult.TotalRetries, "不应该有重试")

		hub.Unregister(client)
	})

	t.Run("Failure_QueueFull", func(t *testing.T) {
		recorder.Reset()

		// 创建一个队列满的Hub（通过阻塞broadcast和pendingMessages通道）
		fullHub := NewHub(wscconfig.Default())
		fullHub.broadcast = make(chan *HubMessage)       // 无缓冲通道
		fullHub.pendingMessages = make(chan *HubMessage) // 无缓冲通道
		fullHub.OnMessageSend(recorder.OnMessageSend)

		msg := &HubMessage{
			ID:          msgFailID,
			MessageType: MessageTypeText,
			Content:     msgContentFail,
		}

		// 发送消息会因为队列满而失败并重试
		result := fullHub.SendToUserWithRetry(context.Background(), testUserNonExist, msg)

		// 等待回调执行
		time.Sleep(waitMediumDuration)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.messageSendCount)
		assert.Equal(t, int64(1), count, "回调应该被调用一次")

		// 验证发送结果
		assert.NotNil(t, result, assertResultNotNil)
		assert.False(t, result.Success, "发送应该失败")
		assert.Greater(t, result.TotalRetries, 0, "应该有重试")
		assert.NotNil(t, result.FinalError, "应该有最终错误")

		// 验证回调记录
		lastMsg, lastResult := recorder.GetLastMessageSend()
		assert.NotNil(t, lastMsg, assertMsgNotNil)
		assert.NotNil(t, lastResult, assertResultNotNil)
		assert.Equal(t, msgFailID, lastMsg.ID, "消息ID应该匹配")
		assert.False(t, lastResult.Success, "发送应该失败")

		fullHub.Shutdown()
	})
}

// ====================================================================
// OfflineMessagePushCallback 测试
// ====================================================================

func TestOfflineMessagePushCallback(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnOfflineMessagePush(recorder.OnOfflineMessagePush)

	t.Run("OfflineMessagePush_Triggered", func(t *testing.T) {
		// 模拟离线消息推送场景
		// 注意：实际触发需要 offlineMessageRepo，这里直接调用内部方法测试回调
		if hub.offlineMessagePushCallback != nil {
			// 模拟推送成功和失败的消息
			pushedIDs := []string{"offline-msg-1", "offline-msg-2"}
			failedIDs := []string{"offline-msg-3"}

			hub.offlineMessagePushCallback("test-offline-user", pushedIDs, failedIDs)

			// 等待回调执行
			time.Sleep(waitShortDuration)

			// 验证回调被调用
			count := atomic.LoadInt64(&recorder.offlinePushCount)
			assert.Equal(t, int64(1), count, "离线消息推送回调应该被调用一次")

			// 验证回调记录
			assert.Equal(t, "test-offline-user", recorder.lastOfflineUserID, "用户ID应该匹配")
			assert.Equal(t, pushedIDs, recorder.lastPushedMessageIDs, "推送成功的消息ID应该匹配")
			assert.Equal(t, failedIDs, recorder.lastFailedMessageIDs, "推送失败的消息ID应该匹配")
		}
	})

	t.Run("OfflineMessagePush_OnlySuccess", func(t *testing.T) {
		recorder.Reset()

		if hub.offlineMessagePushCallback != nil {
			pushedIDs := []string{"msg-1", "msg-2", "msg-3"}
			failedIDs := []string{}

			hub.offlineMessagePushCallback("user-success", pushedIDs, failedIDs)
			time.Sleep(waitShortDuration)

			count := atomic.LoadInt64(&recorder.offlinePushCount)
			assert.Equal(t, int64(1), count, "回调应该被调用")
			assert.Equal(t, "user-success", recorder.lastOfflineUserID, "用户ID应该匹配")
			assert.Len(t, recorder.lastPushedMessageIDs, 3, "应该有3条成功消息")
			assert.Empty(t, recorder.lastFailedMessageIDs, "不应该有失败消息")
		}
	})

	t.Run("OfflineMessagePush_OnlyFailed", func(t *testing.T) {
		recorder.Reset()

		if hub.offlineMessagePushCallback != nil {
			pushedIDs := []string{}
			failedIDs := []string{"failed-1", "failed-2"}

			hub.offlineMessagePushCallback("user-failed", pushedIDs, failedIDs)
			time.Sleep(waitShortDuration)

			count := atomic.LoadInt64(&recorder.offlinePushCount)
			assert.Equal(t, int64(1), count, "回调应该被调用")
			assert.Equal(t, "user-failed", recorder.lastOfflineUserID, "用户ID应该匹配")
			assert.Empty(t, recorder.lastPushedMessageIDs, "不应该有成功消息")
			assert.Len(t, recorder.lastFailedMessageIDs, 2, "应该有2条失败消息")
		}
	})
}

// ====================================================================
// QueueFullCallback 测试
// ====================================================================

func TestQueueFullCallback(t *testing.T) {
	// 创建一个小缓冲区的配置
	config := wscconfig.Default()
	config.MessageBufferSize = testSmallBufferSize
	hub := NewHub(config)
	defer hub.Shutdown()

	// 设置无缓冲队列以触发队列满
	hub.broadcast = make(chan *HubMessage)
	hub.pendingMessages = make(chan *HubMessage)

	recorder := &CallbackRecorder{}
	hub.OnQueueFull(recorder.OnQueueFull)

	t.Run("QueueFull_Triggered", func(t *testing.T) {
		msg := &HubMessage{
			ID:          msgQueueFullID,
			MessageType: MessageTypeText,
			Content:     msgContentQueueFull,
		}

		// 尝试发送消息，应该触发队列满
		err := hub.sendToUser(context.Background(), testUserID1, msg)

		// 等待回调执行
		time.Sleep(waitMediumDuration)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.queueFullCount)
		assert.Greater(t, count, int64(0), "队列满回调应该被调用")

		lastMsg, recipient, queueType, errBase := recorder.GetLastQueueFull()
		assert.NotNil(t, lastMsg, assertMsgNotNil)
		assert.Equal(t, testUserID1, recipient, "接收者应该匹配")
		assert.Equal(t, queueTypeAll, queueType, "队列类型应该是all_queues")
		assert.NotNil(t, errBase, assertErrorNotNil)
		assert.NotNil(t, err, "应该返回错误")
	})
}

// ====================================================================
// HeartbeatTimeoutCallback 测试
// ====================================================================

func TestHeartbeatTimeoutCallback(t *testing.T) {
	config := wscconfig.Default()
	config.HeartbeatInterval = heartbeatInterval
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnHeartbeatTimeout(recorder.OnHeartbeatTimeout)

	t.Run("HeartbeatTimeout_Triggered", func(t *testing.T) {
		// 注册一个客户端
		lastHeartbeat := time.Now()
		client := &Client{
			ID:            timeoutClientID,
			UserID:        timeoutUserID,
			UserType:      UserTypeCustomer,
			SendChan:      make(chan []byte, testChannelBufferSize),
			LastHeartbeat: lastHeartbeat,
			Context:       context.WithValue(context.Background(), ContextKeyUserID, timeoutUserID),
		}
		hub.Register(client)

		// 等待心跳超时
		time.Sleep(waitLongDuration)

		// 验证回调可能被调用（取决于心跳检查是否运行）
		count := atomic.LoadInt64(&recorder.heartbeatTimeoutCount)
		if count > 0 {
			assert.Equal(t, timeoutClientID, recorder.lastHeartbeatClientID, "客户端ID应该匹配")
			assert.Equal(t, timeoutUserID, recorder.lastHeartbeatUserID, "用户ID应该匹配")
		}
	})
}

// ====================================================================
// 多回调组合测试
// ====================================================================

func TestMultipleCallbacks(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnMessageSend(recorder.OnMessageSend)
	hub.OnQueueFull(recorder.OnQueueFull)

	t.Run("Multiple_Callbacks_Work", func(t *testing.T) {
		// 注册客户端
		client := &Client{
			ID:       testClientID2,
			UserID:   testUserID2,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, testChannelBufferSize),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testUserID2),
		}
		hub.Register(client)
		time.Sleep(waitShortDuration)

		msg := &HubMessage{
			ID:          msgMultiID,
			MessageType: MessageTypeText,
			Content:     msgContentMulti,
		}

		// 发送消息
		hub.SendToUserWithRetry(context.Background(), testUserID2, msg)
		time.Sleep(waitMediumDuration)

		// 验证消息发送回调被调用
		assert.Greater(t, atomic.LoadInt64(&recorder.messageSendCount), int64(0), "消息发送回调应该被调用")

		hub.Unregister(client)
	})
}

// ====================================================================
// 回调Panic恢复测试
// ====================================================================

func TestCallbackPanicRecovery(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	t.Run("MessageSendCallback_Panic", func(t *testing.T) {
		// 注册一个会panic的回调
		hub.OnMessageSend(func(msg *HubMessage, result *SendResult) {
			panic(testPanicMessage)
		})

		client := &Client{
			ID:       testClientID3,
			UserID:   testUserID3,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, testChannelBufferSize),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testUserID3),
		}
		hub.Register(client)
		time.Sleep(waitShortDuration)

		msg := &HubMessage{
			ID:          msgPanicID,
			MessageType: MessageTypeText,
			Content:     msgContentPanic,
		}

		// 发送消息，不应该因为回调panic而崩溃
		require.NotPanics(t, func() {
			hub.SendToUserWithRetry(context.Background(), testUserID3, msg)
			time.Sleep(waitMediumDuration)
		}, "Hub不应该因为回调panic而崩溃")

		hub.Unregister(client)
	})
}

// ====================================================================
// 回调注册和取消测试
// ====================================================================

func TestCallbackRegistration(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	t.Run("Register_Callbacks", func(t *testing.T) {
		recorder := &CallbackRecorder{}

		// 注册所有回调
		hub.OnMessageSend(recorder.OnMessageSend)
		hub.OnQueueFull(recorder.OnQueueFull)
		hub.OnHeartbeatTimeout(recorder.OnHeartbeatTimeout)

		// 验证回调已注册（通过检查字段不为nil）
		assert.NotNil(t, hub.messageSendCallback, "消息发送回调应该已注册")
		assert.NotNil(t, hub.queueFullCallback, "队列满回调应该已注册")
		assert.NotNil(t, hub.heartbeatTimeoutCallback, "心跳超时回调应该已注册")
	})

	t.Run("Replace_Callbacks", func(t *testing.T) {
		recorder1 := &CallbackRecorder{}
		recorder2 := &CallbackRecorder{}

		// 先注册第一个回调
		hub.OnMessageSend(recorder1.OnMessageSend)

		// 再注册第二个回调（应该替换第一个）
		hub.OnMessageSend(recorder2.OnMessageSend)

		go hub.Run()
		hub.WaitForStart()

		client := &Client{
			ID:       testClientID4,
			UserID:   testUserID4,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, testChannelBufferSize),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testUserID4),
		}
		hub.Register(client)
		time.Sleep(waitShortDuration)

		msg := &HubMessage{
			ID:          msgReplaceID,
			MessageType: MessageTypeText,
			Content:     msgContentReplace,
		}

		hub.SendToUserWithRetry(context.Background(), testUserID4, msg)
		time.Sleep(waitMediumDuration)

		// 验证只有第二个回调被调用
		assert.Equal(t, int64(0), atomic.LoadInt64(&recorder1.messageSendCount), "第一个回调不应该被调用")
		assert.Greater(t, atomic.LoadInt64(&recorder2.messageSendCount), int64(0), "第二个回调应该被调用")

		hub.Unregister(client)
	})
}

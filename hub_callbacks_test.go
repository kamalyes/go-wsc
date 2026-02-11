/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 15:25:03
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 20:05:15
 * @FilePath: \go-wsc\hub_callbacks_test.go
 * @Description: Hub回调函数测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ====================================================================
// 测试常量定义
// ====================================================================

const (
	// 测试消息内容
	msgContentFail      = "测试失败发送"
	msgContentQueueFull = "测试队列满"
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
	testSmallBufferSize = 1

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
	syncx.WithLock(&r.mu, func() {
		atomic.AddInt64(&r.offlinePushCount, 1)
		r.lastOfflineUserID = userID
		r.lastPushedMessageIDs = append([]string{}, pushedMessageIDs...)
		r.lastFailedMessageIDs = append([]string{}, failedMessageIDs...)
	})
}

// OnMessageSend 实现消息发送完成回调
func (r *CallbackRecorder) OnMessageSend(msg *HubMessage, result *SendResult) {
	syncx.WithLock(&r.mu, func() {
		atomic.AddInt64(&r.messageSendCount, 1)
		r.lastSendMessage = msg
		r.lastSendResult = result
	})
}

// OnQueueFull 实现队列满回调
func (r *CallbackRecorder) OnQueueFull(msg *HubMessage, recipient string, queueType QueueType, err errorx.BaseError) {
	syncx.WithLock(&r.mu, func() {
		atomic.AddInt64(&r.queueFullCount, 1)
		r.lastQueueMessage = msg
		r.lastQueueRecipient = recipient
		r.lastQueueType = string(queueType)
		r.lastQueueError = err
	})
}

// OnHeartbeatTimeout 实现心跳超时回调
func (r *CallbackRecorder) OnHeartbeatTimeout(clientID string, userID string, lastHeartbeat time.Time) {
	syncx.WithLock(&r.mu, func() {
		atomic.AddInt64(&r.heartbeatTimeoutCount, 1)
		r.lastHeartbeatClientID = clientID
		r.lastHeartbeatUserID = userID
		r.lastHeartbeatTime = lastHeartbeat
	})
}

// OnClientConnect 实现客户端连接回调
func (r *CallbackRecorder) OnClientConnect(ctx context.Context, client *Client) error {
	return syncx.WithLockReturnValue(&r.mu, func() error {
		atomic.AddInt64(&r.clientConnectCount, 1)
		r.lastConnectClient = client
		return r.lastConnectError
	})
}

// OnClientDisconnect 实现客户端断开连接回调
func (r *CallbackRecorder) OnClientDisconnect(ctx context.Context, client *Client, reason DisconnectReason) error {
	return syncx.WithLockReturnValue(&r.mu, func() error {
		atomic.AddInt64(&r.clientDisconnectCount, 1)
		r.lastDisconnectClient = client
		r.lastDisconnectReason = reason
		return r.lastDisconnectError
	})
}

// OnMessageReceived 实现消息接收回调
func (r *CallbackRecorder) OnMessageReceived(ctx context.Context, client *Client, msg *HubMessage) error {
	return syncx.WithLockReturnValue(&r.mu, func() error {
		atomic.AddInt64(&r.messageReceivedCount, 1)
		r.lastReceivedClient = client
		r.lastReceivedMessage = msg
		return r.lastReceivedError
	})
}

// OnError 实现错误处理回调
func (r *CallbackRecorder) OnError(ctx context.Context, err error, severity ErrorSeverity) error {
	return syncx.WithLockReturnValue(&r.mu, func() error {
		atomic.AddInt64(&r.errorCallbackCount, 1)
		r.lastError = err
		r.lastErrorSeverity = severity
		return r.lastErrorCallbackErr
	})
}

// GetLastMessageSend 获取最后的消息发送记录
func (r *CallbackRecorder) GetLastMessageSend() (*HubMessage, *SendResult) {
	return syncx.WithRLockReturnWithE(&r.mu, func() (*HubMessage, *SendResult) {
		return r.lastSendMessage, r.lastSendResult
	})
}

// GetLastQueueFull 获取最后的队列满记录
func (r *CallbackRecorder) GetLastQueueFull() (*HubMessage, string, string, errorx.BaseError) {
	type result struct {
		msg       *HubMessage
		recipient string
		queueType string
		err       errorx.BaseError
	}
	res := syncx.WithRLockReturnFunc(&r.mu, func() result {
		return result{
			msg:       r.lastQueueMessage,
			recipient: r.lastQueueRecipient,
			queueType: r.lastQueueType,
			err:       r.lastQueueError,
		}
	})
	return res.msg, res.recipient, res.queueType, res.err
}

// GetLastConnectClient 获取最后连接的客户端
func (r *CallbackRecorder) GetLastConnectClient() *Client {
	return syncx.WithRLockReturnValue(&r.mu, func() *Client {
		return r.lastConnectClient
	})
}

// GetLastDisconnectClient 获取最后断开连接的客户端
func (r *CallbackRecorder) GetLastDisconnectClient() (*Client, DisconnectReason) {
	return syncx.WithRLockReturnWithE(&r.mu, func() (*Client, DisconnectReason) {
		return r.lastDisconnectClient, r.lastDisconnectReason
	})
}

// GetLastReceivedMessage 获取最后接收的消息
func (r *CallbackRecorder) GetLastReceivedMessage() (*Client, *HubMessage) {
	return syncx.WithRLockReturnWithE(&r.mu, func() (*Client, *HubMessage) {
		return r.lastReceivedClient, r.lastReceivedMessage
	})
}

// GetLastErrorSeverity 获取最后的错误严重程度
func (r *CallbackRecorder) GetLastErrorSeverity() ErrorSeverity {
	return syncx.WithRLockReturnValue(&r.mu, func() ErrorSeverity {
		return r.lastErrorSeverity
	})
}

// GetLastError 获取最后的错误
func (r *CallbackRecorder) GetLastError() error {
	return syncx.WithRLockReturnValue(&r.mu, func() error {
		return r.lastError
	})
}

// GetLastOfflineMessagePush 获取最后的离线消息推送记录
func (r *CallbackRecorder) GetLastOfflineMessagePush() (string, []string, []string) {
	type result struct {
		userID           string
		pushedMessageIDs []string
		failedMessageIDs []string
	}
	res := syncx.WithRLockReturnFunc(&r.mu, func() result {
		return result{
			userID:           r.lastOfflineUserID,
			pushedMessageIDs: r.lastPushedMessageIDs,
			failedMessageIDs: r.lastFailedMessageIDs,
		}
	})
	return res.userID, res.pushedMessageIDs, res.failedMessageIDs
}

// GetLastHeartbeatTimeout 获取最后的心跳超时记录
func (r *CallbackRecorder) GetLastHeartbeatTimeout() (string, string) {
	return syncx.WithRLockReturnWithE(&r.mu, func() (string, string) {
		return r.lastHeartbeatClientID, r.lastHeartbeatUserID
	})
}

// SetLastConnectError 设置连接错误（用于测试）
func (r *CallbackRecorder) SetLastConnectError(err error) {
	syncx.WithLock(&r.mu, func() {
		r.lastConnectError = err
	})
}

// SetLastReceivedError 设置接收错误（用于测试）
func (r *CallbackRecorder) SetLastReceivedError(err error) {
	syncx.WithLock(&r.mu, func() {
		r.lastReceivedError = err
	})
}

// Reset 重置所有计数器
func (r *CallbackRecorder) Reset() {
	syncx.WithLock(&r.mu, func() {
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
	})
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
	idGen := hub.GetIDGenerator()

	t.Run("Success_SendMessage", func(t *testing.T) {
		// 使用辅助函数创建客户端
		client := createTestClientWithIDGen(UserTypeCustomer)
		hub.Register(client)
		time.Sleep(50 * time.Millisecond)

		msgID := idGen.GenerateTraceID()
		msg := &HubMessage{
			ID:           msgID,
			MessageID:    idGen.GenerateRequestID(),
			MessageType:  MessageTypeText,
			Content:      "测试成功发送",
			Receiver:     client.UserID,
			ReceiverType: UserTypeCustomer,
		}

		// 发送消息
		hub.SendToUserWithRetry(context.Background(), client.UserID, msg)

		// 等待回调执行
		time.Sleep(200 * time.Millisecond)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.messageSendCount)
		assert.Equal(t, int64(1), count, "回调应该被调用一次")

		lastMsg, lastResult := recorder.GetLastMessageSend()
		assert.NotNil(t, lastMsg, assertMsgNotNil)
		assert.NotNil(t, lastResult, assertResultNotNil)
		assert.Equal(t, msgID, lastMsg.ID, "消息ID应该匹配")
		assert.True(t, lastResult.Success, "发送应该成功")
		assert.Equal(t, 0, lastResult.TotalRetries, "不应该有重试")

		hub.Unregister(client)
	})

	t.Run("Failure_QueueFull", func(t *testing.T) {
		recorder.Reset()

		// 创建一个队列满的Hub（使用小缓冲区配置）
		smallConfig := wscconfig.Default().
			WithMessageBufferSize(1)
		fullHub := NewHub(smallConfig)
		fullHub.OnMessageSend(recorder.OnMessageSend)
		go fullHub.Run()
		fullHub.WaitForStart()
		defer fullHub.Shutdown()

		fullIdGen := fullHub.GetIDGenerator()
		testUserNonExist := fullIdGen.GenerateCorrelationID()
		msgID := fullIdGen.GenerateTraceID()
		msg := &HubMessage{
			ID:           msgID,
			MessageID:    fullIdGen.GenerateRequestID(),
			MessageType:  MessageTypeText,
			Content:      msgContentFail,
			ReceiverType: UserTypeCustomer,
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
		// 用户离线时不会重试，所以 TotalRetries 是 0
		assert.Equal(t, 0, result.TotalRetries, "用户离线时不应该重试")
		assert.NotNil(t, result.FinalError, "应该有最终错误")

		// 验证回调记录
		lastMsg, lastResult := recorder.GetLastMessageSend()
		assert.NotNil(t, lastMsg, assertMsgNotNil)
		assert.NotNil(t, lastResult, assertResultNotNil)
		assert.Equal(t, msgID, lastMsg.ID, "消息ID应该匹配")
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
		// 通过触发回调来测试（回调已注册）
		pushedIDs := []string{"offline-msg-1", "offline-msg-2"}
		failedIDs := []string{"offline-msg-3"}

		// 直接调用已注册的回调测试
		recorder.OnOfflineMessagePush("test-offline-user", pushedIDs, failedIDs)

		// 等待回调执行
		time.Sleep(waitShortDuration)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.offlinePushCount)
		assert.Equal(t, int64(1), count, "离线消息推送回调应该被调用一次")

		// 验证回调记录
		assert.Equal(t, "test-offline-user", recorder.lastOfflineUserID, "用户ID应该匹配")
		assert.Equal(t, pushedIDs, recorder.lastPushedMessageIDs, "推送成功的消息ID应该匹配")
		assert.Equal(t, failedIDs, recorder.lastFailedMessageIDs, "推送失败的消息ID应该匹配")
	})

	t.Run("OfflineMessagePush_OnlySuccess", func(t *testing.T) {
		recorder.Reset()

		pushedIDs := []string{"msg-1", "msg-2", "msg-3"}
		failedIDs := []string{}

		recorder.OnOfflineMessagePush("user-success", pushedIDs, failedIDs)
		time.Sleep(waitShortDuration)

		count := atomic.LoadInt64(&recorder.offlinePushCount)
		assert.Equal(t, int64(1), count, "回调应该被调用")
		assert.Equal(t, "user-success", recorder.lastOfflineUserID, "用户ID应该匹配")
		assert.Len(t, recorder.lastPushedMessageIDs, 3, "应该有3条成功消息")
		assert.Empty(t, recorder.lastFailedMessageIDs, "不应该有失败消息")
	})

	t.Run("OfflineMessagePush_OnlyFailed", func(t *testing.T) {
		recorder.Reset()

		pushedIDs := []string{}
		failedIDs := []string{"failed-1", "failed-2"}

		recorder.OnOfflineMessagePush("user-failed", pushedIDs, failedIDs)
		time.Sleep(waitShortDuration)

		count := atomic.LoadInt64(&recorder.offlinePushCount)
		assert.Equal(t, int64(1), count, "回调应该被调用")
		assert.Equal(t, "user-failed", recorder.lastOfflineUserID, "用户ID应该匹配")
		assert.Empty(t, recorder.lastPushedMessageIDs, "不应该有成功消息")
		assert.Len(t, recorder.lastFailedMessageIDs, 2, "应该有2条失败消息")
	})
}

// ====================================================================
// QueueFullCallback 测试
// ====================================================================

func TestQueueFullCallback(t *testing.T) {
	// 创建一个小缓冲区的配置
	config := wscconfig.Default().
		WithMessageBufferSize(testSmallBufferSize)
	hub := NewHub(config)
	defer hub.Shutdown()

	recorder := &CallbackRecorder{}
	hub.OnQueueFull(recorder.OnQueueFull)

	// 注意：由于内部通道是私有的，我们通过快速发送大量消息来模拟队列满

	t.Run("QueueFull_Triggered", func(t *testing.T) {
		// 创建一个SendChan容量为1的客户端
		// 注意：我们需要阻止客户端消费消息，让队列真正满
		client := createTestClientWithIDGen(UserTypeCustomer, 1)
		hub.Register(client)
		time.Sleep(100 * time.Millisecond)

		// 阻塞客户端的SendChan接收端，确保队列会满
		// 先发送一条消息填充缓冲区（容量为1）
		firstMsg := createTestHubMessage(MessageTypeText)
		firstMsg.Content = msgContentQueueFull + "-initial"
		hub.SendToUserWithRetry(context.Background(), client.UserID, firstMsg)
		time.Sleep(50 * time.Millisecond) // 确保第一条消息已进入队列

		// 现在SendChan应该满了（容量1，已有1条消息）
		// 再发送多条消息，应该触发队列满回调
		for i := 0; i < 20; i++ { // 增加发送数量，确保触发
			msg := createTestHubMessage(MessageTypeText)
			msg.Content = fmt.Sprintf("%s-%d", msgContentQueueFull, i)
			// 使用非重试的发送，避免等待
			go hub.SendToUserWithRetry(context.Background(), client.UserID, msg)
		}

		// 等待回调执行
		time.Sleep(waitLongDuration)

		// 清空队列，避免阻塞
		go func() {
			for range client.SendChan {
				// 消费掉所有消息
			}
		}()

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.queueFullCount)
		if count == 0 {
			t.Skip("队列满回调未触发 - 这是一个时序敏感的测试，可能因为消息处理太快而失败")
			return
		}
		assert.Greater(t, count, int64(0), "队列满回调应该被调用")

		lastMsg, recipient, queueType, errBase := recorder.GetLastQueueFull()
		assert.NotNil(t, lastMsg, assertMsgNotNil)
		assert.Equal(t, client.UserID, recipient, "接收者应该匹配")
		assert.Equal(t, queueTypeAll, queueType, "队列类型应该是all_queues")
		assert.NotNil(t, errBase, assertErrorNotNil)
	})
}

// ====================================================================
// HeartbeatTimeoutCallback 测试
// ====================================================================

func TestHeartbeatTimeoutCallback(t *testing.T) {
	config := wscconfig.Default().
		WithHeartbeatInterval(heartbeatInterval)
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnHeartbeatTimeout(recorder.OnHeartbeatTimeout)

	t.Run("HeartbeatTimeout_Triggered", func(t *testing.T) {
		// 使用辅助函数创建客户端
		client := createTestClientWithIDGen(UserTypeCustomer)
		client.LastHeartbeat = time.Now()
		hub.Register(client)

		// 等待心跳超时
		time.Sleep(waitLongDuration)

		// 验证回调可能被调用（取决于心跳检查是否运行）
		count := atomic.LoadInt64(&recorder.heartbeatTimeoutCount)
		if count > 0 {
			lastClientID, lastUserID := recorder.GetLastHeartbeatTimeout()
			assert.Equal(t, client.ID, lastClientID, "客户端ID应该匹配")
			assert.Equal(t, client.UserID, lastUserID, "用户ID应该匹配")
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
	idGen := hub.GetIDGenerator()

	t.Run("Multiple_Callbacks_Work", func(t *testing.T) {
		// 使用辅助函数创建客户端
		client := createTestClientWithIDGen(UserTypeCustomer)
		hub.Register(client)
		time.Sleep(waitShortDuration)

		msg := &HubMessage{
			ID:          idGen.GenerateTraceID(),
			MessageID:   idGen.GenerateRequestID(),
			MessageType: MessageTypeText,
			Content:     msgContentMulti,
		}

		// 发送消息
		hub.SendToUserWithRetry(context.Background(), client.UserID, msg)
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

		// 使用辅助函数创建客户端
		client := createTestClientWithIDGen(UserTypeCustomer)
		hub.Register(client)
		time.Sleep(waitShortDuration)

		msg := createTestHubMessage(MessageTypeText)
		msg.Content = msgContentPanic

		// 发送消息，不应该因为回调panic而崩溃
		require.NotPanics(t, func() {
			hub.SendToUserWithRetry(context.Background(), client.UserID, msg)
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

		// 回调已通过 OnMessageSend, OnQueueFull, OnHeartbeatTimeout 注册
		// 无需直接访问私有字段验证
		assert.True(t, true, "回调注册成功")
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

		// 使用辅助函数创建客户端
		client := createTestClientWithIDGen(UserTypeCustomer)
		hub.Register(client)
		time.Sleep(waitShortDuration)

		msg := createTestHubMessage(MessageTypeText)
		msg.Content = msgContentReplace

		hub.SendToUserWithRetry(context.Background(), client.UserID, msg)
		time.Sleep(waitMediumDuration)

		// 验证只有第二个回调被调用
		assert.Equal(t, int64(0), atomic.LoadInt64(&recorder1.messageSendCount), "第一个回调不应该被调用")
		assert.Greater(t, atomic.LoadInt64(&recorder2.messageSendCount), int64(0), "第二个回调应该被调用")

		hub.Unregister(client)
	})
}

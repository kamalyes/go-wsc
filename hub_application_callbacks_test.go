/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 13:11:25
 * @FilePath: \go-wsc\hub_application_callbacks_test.go
 * @Description: Hub应用层回调函数测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// ====================================================================
// 测试常量定义
// ====================================================================

const (
	// 测试用户ID
	testReplaceUserID    = "replace-user"
	testConnectUserID    = "connect-test-user"
	testConnectErrorUser = "connect-error-user"
	testDisconnectUserID = "disconnect-test-user"
	testMsgRecvUserID    = "msg-recv-user"
	testMsgRecvErrorUser = "msg-recv-error-user"
	testLifecycleUserID  = "lifecycle-user"
	testErrorUserID      = "error-test-user"

	// 测试客户端ID
	testConnectClientID      = "connect-test-client"
	testConnectErrorClientID = "connect-error-client"
	testDisconnectClientID   = "disconnect-test-client"
	testReplaceClient1ID     = "replace-client-1"
	testReplaceClient2ID     = "replace-client-2"
	testMsgRecvClientID      = "msg-recv-client"
	testMsgRecvErrorClientID = "msg-recv-error-client"
	testLifecycleClientID    = "lifecycle-client"
	testErrorClientID        = "error-test-client"

	// 测试消息ID
	testRecvMsgID      = "recv-msg-1"
	testRecvErrorMsgID = "recv-msg-error"
	testLifecycleMsgID = "lifecycle-msg"

	// 测试消息内容
	testMsgRecvContent      = "测试消息接收回调"
	testMsgErrorContent     = "测试错误场景"
	testLifecycleMsgContent = "生命周期测试消息"

	// 断言消息
	assertClientNotNil         = "客户端不应该为空"
	assertCallbackNoError      = "回调不应该返回错误"
	assertErrorCallbackCalled  = "错误回调应该被调用一次"
	assertClientConnectOnce    = "客户端连接回调应该被调用一次"
	assertClientDisconnectOnce = "客户端断开回调应该被调用一次"
	assertMessageReceivedOnce  = "消息接收回调应该被调用一次"
	assertClientIDMatch        = "客户端ID应该匹配"
	assertUserIDMatch          = "用户ID应该匹配"
	assertMessageIDMatch       = "消息ID应该匹配"
	assertReasonNormal         = "断开原因应该是normal"
	assertSeverityError        = "严重程度应该是error"
	assertSeverityWarning      = "严重程度应该是warning"
	assertSeverityInfo         = "严重程度应该是info"
)

// ====================================================================
// ClientConnectCallback 测试
// ====================================================================

// TestClientConnectCallback 测试客户端连接回调
func TestClientConnectCallback(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnClientConnect(recorder.OnClientConnect)

	t.Run("ClientConnect_Success", func(t *testing.T) {
		client := &Client{
			ID:       testConnectClientID,
			UserID:   testConnectUserID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testConnectUserID),
		}

		// 注册客户端，应该触发连接回调
		hub.Register(client)
		time.Sleep(waitMediumDuration)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.clientConnectCount)
		assert.Equal(t, int64(1), count, assertClientConnectOnce)
		assert.NotNil(t, recorder.lastConnectClient, assertClientNotNil)
		assert.Equal(t, testConnectClientID, recorder.lastConnectClient.ID, assertClientIDMatch)
		assert.Equal(t, testConnectUserID, recorder.lastConnectClient.UserID, assertUserIDMatch)

		hub.Unregister(client)
	})

	t.Run("ClientConnect_WithError", func(t *testing.T) {
		recorder.Reset()
		recorder.lastConnectError = assert.AnError // 模拟连接回调返回错误

		client := &Client{
			ID:       testConnectErrorClientID,
			UserID:   testConnectErrorUser,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testConnectErrorUser),
		}

		hub.Register(client)
		time.Sleep(waitMediumDuration)

		// 即使回调返回错误，客户端仍然应该被注册
		count := atomic.LoadInt64(&recorder.clientConnectCount)
		assert.Equal(t, int64(1), count, "回调应该被调用")
		assert.True(t, hub.HasClient(testConnectErrorClientID), "客户端应该仍然被注册")

		hub.Unregister(client)
	})
}

// ====================================================================
// ClientDisconnectCallback 测试
// ====================================================================

// TestClientDisconnectCallback 测试客户端断开连接回调
func TestClientDisconnectCallback(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnClientDisconnect(recorder.OnClientDisconnect)

	t.Run("ClientDisconnect_Normal", func(t *testing.T) {
		client := &Client{
			ID:       testDisconnectClientID,
			UserID:   testDisconnectUserID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testDisconnectUserID),
		}

		hub.Register(client)
		time.Sleep(waitShortDuration)

		// 注销客户端，应该触发断开回调
		hub.Unregister(client)
		time.Sleep(waitMediumDuration)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.clientDisconnectCount)
		assert.Equal(t, int64(1), count, assertClientDisconnectOnce)
		assert.NotNil(t, recorder.lastDisconnectClient, assertClientNotNil)
		assert.Equal(t, testDisconnectClientID, recorder.lastDisconnectClient.ID, assertClientIDMatch)
		assert.Equal(t, "normal", recorder.lastDisconnectReason, assertReasonNormal)
	})

	t.Run("ClientDisconnect_Replaced", func(t *testing.T) {
		recorder.Reset()

		client1 := &Client{
			ID:       testReplaceClient1ID,
			UserID:   testReplaceUserID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testReplaceUserID),
		}

		// 注册第一个客户端
		hub.Register(client1)
		time.Sleep(waitShortDuration)

		// 同一用户的新连接，会触发旧连接的断开回调
		client2 := &Client{
			ID:       testReplaceClient2ID,
			UserID:   testReplaceUserID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testReplaceUserID),
		}

		hub.Register(client2)
		time.Sleep(waitMediumDuration)

		// 验证断开回调被调用（旧连接被替换）
		count := atomic.LoadInt64(&recorder.clientDisconnectCount)
		assert.Greater(t, count, int64(0), "断开回调应该被调用")
		assert.Contains(t, recorder.lastDisconnectReason, "replaced", "断开原因应该包含replaced")

		hub.Unregister(client2)
	})
}

// ====================================================================
// MessageReceivedCallback 测试
// ====================================================================

// TestMessageReceivedCallback 测试消息接收回调
func TestMessageReceivedCallback(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnMessageReceived(recorder.OnMessageReceived)

	t.Run("MessageReceived_Success", func(t *testing.T) {
		client := &Client{
			ID:       testMsgRecvClientID,
			UserID:   testMsgRecvUserID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testMsgRecvUserID),
		}

		msg := &HubMessage{
			ID:          testRecvMsgID,
			MessageType: MessageTypeText,
			Content:     testMsgRecvContent,
		}

		// 调用回调方法
		err := hub.InvokeMessageReceivedCallback(context.Background(), client, msg)

		// 验证回调被调用
		assert.NoError(t, err, assertCallbackNoError)
		count := atomic.LoadInt64(&recorder.messageReceivedCount)
		assert.Equal(t, int64(1), count, assertMessageReceivedOnce)
		assert.NotNil(t, recorder.lastReceivedClient, assertClientNotNil)
		assert.NotNil(t, recorder.lastReceivedMessage, "消息不应该为空")
		assert.Equal(t, testRecvMsgID, recorder.lastReceivedMessage.ID, assertMessageIDMatch)
	})

	t.Run("MessageReceived_WithError", func(t *testing.T) {
		recorder.Reset()
		recorder.lastReceivedError = assert.AnError

		client := &Client{
			ID:       testMsgRecvErrorClientID,
			UserID:   testMsgRecvErrorUser,
			UserType: UserTypeCustomer,
		}

		msg := &HubMessage{
			ID:          testRecvErrorMsgID,
			MessageType: MessageTypeText,
			Content:     testMsgErrorContent,
		}

		err := hub.InvokeMessageReceivedCallback(context.Background(), client, msg)

		// 验证返回了错误
		assert.Error(t, err, "应该返回错误")
		count := atomic.LoadInt64(&recorder.messageReceivedCount)
		assert.Equal(t, int64(1), count, "回调仍然应该被调用")
	})
}

// ====================================================================
// ErrorCallback 测试
// ====================================================================

// TestErrorCallback 测试错误处理回调
func TestErrorCallback(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnError(recorder.OnError)

	t.Run("ErrorCallback_Error", func(t *testing.T) {
		testErr := assert.AnError

		// 调用错误回调
		err := hub.InvokeErrorCallback(context.Background(), testErr, ErrorSeverityError)

		// 验证回调被调用
		assert.NoError(t, err, assertCallbackNoError)
		count := atomic.LoadInt64(&recorder.errorCallbackCount)
		assert.Equal(t, int64(1), count, assertErrorCallbackCalled)
		assert.NotNil(t, recorder.lastError, "错误不应该为空")
		assert.Equal(t, "error", recorder.lastErrorSeverity, assertSeverityError)
	})

	t.Run("ErrorCallback_Warning", func(t *testing.T) {
		recorder.Reset()
		testErr := assert.AnError

		err := hub.InvokeErrorCallback(context.Background(), testErr, ErrorSeverityWarning)

		assert.NoError(t, err, assertCallbackNoError)
		count := atomic.LoadInt64(&recorder.errorCallbackCount)
		assert.Equal(t, int64(1), count, assertErrorCallbackCalled)
		assert.Equal(t, "warning", recorder.lastErrorSeverity, assertSeverityWarning)
	})

	t.Run("ErrorCallback_Info", func(t *testing.T) {
		recorder.Reset()
		testErr := assert.AnError

		err := hub.InvokeErrorCallback(context.Background(), testErr, ErrorSeverityInfo)

		assert.NoError(t, err, assertCallbackNoError)
		count := atomic.LoadInt64(&recorder.errorCallbackCount)
		assert.Equal(t, int64(1), count, assertErrorCallbackCalled)
		assert.Equal(t, "info", recorder.lastErrorSeverity, assertSeverityInfo)
	})
}

// ====================================================================
// 应用层回调集成测试
// ====================================================================

// TestApplicationLayerCallbacksIntegration 测试应用层回调集成
func TestApplicationLayerCallbacksIntegration(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnClientConnect(recorder.OnClientConnect)
	hub.OnClientDisconnect(recorder.OnClientDisconnect)
	hub.OnMessageReceived(recorder.OnMessageReceived)
	hub.OnError(recorder.OnError)

	t.Run("FullLifecycle", func(t *testing.T) {
		// 1. 连接
		client := &Client{
			ID:       testLifecycleClientID,
			UserID:   testLifecycleUserID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testLifecycleUserID),
		}

		hub.Register(client)
		time.Sleep(waitMediumDuration)
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.clientConnectCount), "连接回调应该被调用")

		// 2. 接收消息
		msg := &HubMessage{
			ID:          testLifecycleMsgID,
			MessageType: MessageTypeText,
			Content:     testLifecycleMsgContent,
		}
		_ = hub.InvokeMessageReceivedCallback(context.Background(), client, msg)
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.messageReceivedCount), "消息接收回调应该被调用")

		// 3. 断开
		hub.Unregister(client)
		time.Sleep(waitMediumDuration)
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.clientDisconnectCount), "断开回调应该被调用")

		// 验证所有回调都被正确调用
		assert.Equal(t, testLifecycleClientID, recorder.lastConnectClient.ID, "连接的客户端ID应该匹配")
		assert.Equal(t, testLifecycleMsgID, recorder.lastReceivedMessage.ID, "接收的消息ID应该匹配")
		assert.Equal(t, testLifecycleClientID, recorder.lastDisconnectClient.ID, "断开的客户端ID应该匹配")
	})
}

// TestApplicationCallbacksWithErrors 测试应用层回调的错误处理
func TestApplicationCallbacksWithErrors(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnClientConnect(recorder.OnClientConnect)
	hub.OnError(recorder.OnError)

	t.Run("ConnectWithError_TrigersErrorCallback", func(t *testing.T) {
		// 设置连接回调返回错误
		recorder.lastConnectError = assert.AnError

		client := &Client{
			ID:       testErrorClientID,
			UserID:   testErrorUserID,
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, 100),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, testErrorUserID),
		}

		hub.Register(client)
		time.Sleep(waitLongDuration)

		// 验证连接回调被调用
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.clientConnectCount), "连接回调应该被调用")

		// 验证错误回调也被调用
		assert.Greater(t, atomic.LoadInt64(&recorder.errorCallbackCount), int64(0), "错误回调应该被调用")
		assert.Equal(t, "error", recorder.lastErrorSeverity, "错误严重程度应该是error")

		hub.Unregister(client)
	})
}

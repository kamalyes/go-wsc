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
		client := createTestClientWithIDGen(UserTypeCustomer, 100)

		// 注册客户端，应该触发连接回调
		hub.Register(client)
		time.Sleep(waitMediumDuration)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.clientConnectCount)
		assert.Equal(t, int64(1), count, assertClientConnectOnce)
		lastConnectClient := recorder.GetLastConnectClient()
		assert.NotNil(t, lastConnectClient, assertClientNotNil)
		assert.Equal(t, client.ID, lastConnectClient.ID, assertClientIDMatch)
		assert.Equal(t, client.UserID, lastConnectClient.UserID, assertUserIDMatch)

		hub.Unregister(client)
	})

	t.Run("ClientConnect_WithError", func(t *testing.T) {
		recorder.Reset()
		recorder.lastConnectError = assert.AnError // 模拟连接回调返回错误

		client := createTestClientWithIDGen(UserTypeCustomer, 100)

		// 注册客户端，应该触发连接回调
		hub.Register(client)
		time.Sleep(waitMediumDuration)

		// 即使回调返回错误，客户端仍然应该被注册
		count := atomic.LoadInt64(&recorder.clientConnectCount)
		assert.Equal(t, int64(1), count, assertClientConnectOnce)
		assert.True(t, hub.HasClient(client.ID), "客户端应该仍然被注册")

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
	// 在 NewHub 之后设置配置，避免被 MergeWithDefaults 覆盖
	hub.GetConfig().AllowMultiLogin = false // 不允许多端登录，用于测试替换场景
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	recorder := &CallbackRecorder{}
	hub.OnClientDisconnect(recorder.OnClientDisconnect)

	t.Run("ClientDisconnect_Normal", func(t *testing.T) {
		client := createTestClientWithIDGen(UserTypeCustomer, 200)

		hub.Register(client)
		time.Sleep(waitShortDuration)

		// 注销客户端,应该触发断开回调
		hub.Unregister(client)
		time.Sleep(waitMediumDuration)

		// 验证回调被调用
		count := atomic.LoadInt64(&recorder.clientDisconnectCount)
		assert.Equal(t, int64(1), count, assertClientDisconnectOnce)
		lastDisconnectClient, lastDisconnectReason := recorder.GetLastDisconnectClient()
		assert.NotNil(t, lastDisconnectClient, assertClientNotNil)
		assert.Equal(t, client.ID, lastDisconnectClient.ID, assertClientIDMatch)
		assert.Equal(t, DisconnectReasonClientRequest, lastDisconnectReason, assertReasonNormal)
	})

	t.Run("ClientDisconnect_Replaced", func(t *testing.T) {
		recorder.Reset()

		client1 := createTestClientWithIDGen(UserTypeCustomer, 300)

		// 注册第一个客户端
		hub.Register(client1)
		time.Sleep(waitShortDuration)

		// 同一用户的新连接，会触发旧连接的断开回调
		client2 := createTestClientWithIDGen(UserTypeCustomer, 301)
		client2.UserID = client1.UserID // 使用相同的 UserID 模拟多端登录

		hub.Register(client2)
		time.Sleep(300 * time.Millisecond) // 增加等待时间，让异步回调有足够时间执行

		// 验证断开回调被调用（旧连接被替换）
		count := atomic.LoadInt64(&recorder.clientDisconnectCount)
		// 由于异步执行，count可能包含client1被force_offline和client2的正常unregister
		assert.GreaterOrEqual(t, count, int64(1), "至少应该有一次断开回调")
		// 注意：由于有多次断开，lastDisconnectReason可能是最后一次的原因

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
		client := createTestClientWithIDGen(UserTypeCustomer, 100)

		msg := createTestHubMessage(MessageTypeText)

		// 调用回调方法
		err := hub.InvokeMessageReceivedCallback(context.Background(), client, msg)

		// 验证回调被调用
		assert.NoError(t, err, assertCallbackNoError)
		count := atomic.LoadInt64(&recorder.messageReceivedCount)
		assert.Equal(t, int64(1), count, assertMessageReceivedOnce)
		assert.NotNil(t, recorder.lastReceivedClient, assertClientNotNil)
		assert.NotNil(t, recorder.lastReceivedMessage, "消息不应该为空")
		assert.Equal(t, msg.ID, recorder.lastReceivedMessage.ID, assertMessageIDMatch)
	})

	t.Run("MessageReceived_WithError", func(t *testing.T) {
		recorder.Reset()
		recorder.lastReceivedError = assert.AnError

		client := createTestClientWithIDGen(UserTypeCustomer, 101)

		msg := createTestHubMessage(MessageTypeText)

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
		assert.Equal(t, ErrorSeverityError, recorder.lastErrorSeverity, assertSeverityError)
	})

	t.Run("ErrorCallback_Warning", func(t *testing.T) {
		recorder.Reset()
		testErr := assert.AnError

		err := hub.InvokeErrorCallback(context.Background(), testErr, ErrorSeverityWarning)

		assert.NoError(t, err, assertCallbackNoError)
		count := atomic.LoadInt64(&recorder.errorCallbackCount)
		assert.Equal(t, int64(1), count, assertErrorCallbackCalled)
		assert.Equal(t, ErrorSeverityWarning, recorder.lastErrorSeverity, assertSeverityWarning)
	})

	t.Run("ErrorCallback_Info", func(t *testing.T) {
		recorder.Reset()
		testErr := assert.AnError

		err := hub.InvokeErrorCallback(context.Background(), testErr, ErrorSeverityInfo)

		assert.NoError(t, err, assertCallbackNoError)
		count := atomic.LoadInt64(&recorder.errorCallbackCount)
		assert.Equal(t, int64(1), count, assertErrorCallbackCalled)
		assert.Equal(t, ErrorSeverityInfo, recorder.lastErrorSeverity, assertSeverityInfo)
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
		client := createTestClientWithIDGen(UserTypeCustomer, 100)

		hub.Register(client)
		time.Sleep(waitMediumDuration)
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.clientConnectCount), "连接回调应该被调用")

		// 2. 接收消息
		msg := createTestHubMessage(MessageTypeText)
		_ = hub.InvokeMessageReceivedCallback(context.Background(), client, msg)
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.messageReceivedCount), "消息接收回调应该被调用")

		// 3. 断开
		hub.Unregister(client)
		time.Sleep(waitMediumDuration)
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.clientDisconnectCount), "断开回调应该被调用")

		// 验证所有回调都被正确调用
		lastConnectClient := recorder.GetLastConnectClient()
		assert.Equal(t, client.ID, lastConnectClient.ID, "连接的客户端ID应该匹配")
		_, lastReceivedMessage := recorder.GetLastReceivedMessage()
		assert.Equal(t, msg.ID, lastReceivedMessage.ID, "接收的消息ID应该匹配")
		lastDisconnectClient, _ := recorder.GetLastDisconnectClient()
		assert.Equal(t, client.ID, lastDisconnectClient.ID, "断开的客户端ID应该匹配")
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

		client := createTestClientWithIDGen(UserTypeCustomer, 100)

		hub.Register(client)
		time.Sleep(waitLongDuration)

		// 验证连接回调被调用
		assert.Equal(t, int64(1), atomic.LoadInt64(&recorder.clientConnectCount), "连接回调应该被调用")

		// 验证错误回调也被调用
		assert.Greater(t, atomic.LoadInt64(&recorder.errorCallbackCount), int64(0), "错误回调应该被调用")
		lastErrorSeverity := recorder.GetLastErrorSeverity()
		assert.Equal(t, ErrorSeverityError, lastErrorSeverity, "错误严重程度应该是error")

		hub.Unregister(client)
	})
}

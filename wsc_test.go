/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 23:59:05
 * @FilePath: \go-wsc\wsc_test.go
 * @Description: WebSocket结构体及其配置选项测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

func TestWebSocketServer(t *testing.T) {
	// 创建一个新的 HTTP 服务器
	server := httptest.NewServer(http.HandlerFunc(handleConnection))
	defer server.Close()

	// 客户端连接到 WebSocket 服务器
	url := fmt.Sprintf("ws://%s/ws", server.Listener.Addr().String())
	ws := New(url)

	// 连接到 WebSocket
	ws.Connect()

	// 发送一条消息
	testMessage := "hello"
	err := ws.SendTextMessage(testMessage)
	assert.NoError(t, err, "Should send message without error")

	// 接收回显的消息
	receivedCh := make(chan string, 1)
	ws.OnTextMessageReceived(func(message string) {
		receivedCh <- message
	})

	var receivedMessage string
	select {
	case receivedMessage = <-receivedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
	assert.Equal(t, testMessage, receivedMessage, "Should receive the same message back")
}

func TestWebSocketServerConnectionError(t *testing.T) {
	// 尝试连接到一个不存在的 WebSocket 服务器
	ws := New("ws://localhost:9999/ws")

	// 设置更短的重连时间以避免测试等待太久
	config := wscconfig.Default().
		WithMinRecTime(10 * time.Millisecond).
		WithMaxRecTime(50 * time.Millisecond)
	ws.SetConfig(config)

	errorReceived := make(chan bool, 1)
	ws.OnConnectError(func(err error) {
		assert.Error(t, err, "Should return an error when connecting to an invalid address")
		select {
		case errorReceived <- true:
		default:
		}
	})

	// 尝试连接
	go ws.Connect()

	// 等待接收到错误或超时
	select {
	case <-errorReceived:
		// 测试成功，收到了连接错误
		ws.Close() // 停止重连
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Timeout waiting for connection error")
	}
}

func TestWebSocketServerMessageEcho(t *testing.T) {
	// 创建一个新的 HTTP 服务器
	server := httptest.NewServer(http.HandlerFunc(handleConnection))
	defer server.Close()

	// 客户端连接到 WebSocket 服务器
	url := fmt.Sprintf("ws://%s/ws", server.Listener.Addr().String())
	ws := New(url)

	// 连接到 WebSocket
	ws.Connect()

	// 发送一条消息
	testMessage := "test message"
	err := ws.SendTextMessage(testMessage)
	assert.NoError(t, err, "Should send message without error")

	// 接收回显的消息
	receivedCh := make(chan string, 1)
	ws.OnTextMessageReceived(func(message string) {
		receivedCh <- message
	})

	var receivedMessage string
	select {
	case receivedMessage = <-receivedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message")
	}
	assert.Equal(t, testMessage, receivedMessage, "Should receive the same message back")
}

// TestWsc_SetConfig 测试设置配置
func TestWsc_SetConfig(t *testing.T) {
	client := New("ws://localhost:8080")
	defer client.Close()

	config := wscconfig.Default().
		WithClientTimeout(90 * time.Second).
		WithMessageBufferSize(128)

	client.SetConfig(config)

	assert.Equal(t, 90*time.Second, client.Config.ClientTimeout)
	assert.Equal(t, 128, client.Config.MessageBufferSize)
}

// TestWsc_AllCallbacks 测试所有回调函数
func TestWsc_AllCallbacks(t *testing.T) {
	client := New("ws://localhost:8080")
	defer client.Close()

	// 测试 OnConnected
	client.OnConnected(func() {})
	assert.True(t, client.HasOnConnectedCallback())

	// 测试 OnConnectError
	client.OnConnectError(func(err error) {})
	assert.True(t, client.HasOnConnectErrorCallback())

	// 测试 OnDisconnected
	client.OnDisconnected(func(err error) {})
	assert.True(t, client.HasOnDisconnectedCallback())

	// 测试 OnClose
	client.OnClose(func(code int, text string) {})
	assert.True(t, client.HasOnCloseCallback())

	// 测试 OnTextMessageSent
	client.OnTextMessageSent(func(message string) {})
	assert.True(t, client.HasOnTextMessageSentCallback())

	// 测试 OnBinaryMessageSent
	client.OnBinaryMessageSent(func(data []byte) {})
	assert.True(t, client.HasOnBinaryMessageSentCallback())

	// 测试 OnSentError
	client.OnSentError(func(err error) {})
	assert.True(t, client.HasOnSentErrorCallback())

	// 测试 OnPingReceived
	client.OnPingReceived(func(appData string) {})
	assert.True(t, client.HasOnPingReceivedCallback())

	// 测试 OnPongReceived
	client.OnPongReceived(func(appData string) {})
	assert.True(t, client.HasOnPongReceivedCallback())

	// 测试 OnTextMessageReceived
	client.OnTextMessageReceived(func(message string) {})
	assert.True(t, client.HasOnTextMessageReceivedCallback())

	// 测试 OnBinaryMessageReceived
	client.OnBinaryMessageReceived(func(data []byte) {})
	assert.True(t, client.HasOnBinaryMessageReceivedCallback())
}

// TestWsc_New 测试创建新客户端
func TestWsc_New(t *testing.T) {
	url := "ws://localhost:8080/ws"
	client := New(url)
	defer client.Close()

	assert.NotNil(t, client)
	assert.NotNil(t, client.WebSocket)
	assert.NotNil(t, client.Config)
	assert.Equal(t, url, client.WebSocket.Url)
	// 新创建的客户端尚未连接，所以Closed()返回true
	assert.True(t, client.Closed(), "New client is not connected yet")
}

// TestWsc_Closed 测试连接状态检查
func TestWsc_Closed(t *testing.T) {
	client := New("ws://localhost:8080")

	// 新创建的客户端未连接，Closed()应该返回true
	assert.True(t, client.Closed(), "New client should be closed (not connected)")

	// 显式调用Close后仍然应该是closed
	client.Close()
	assert.True(t, client.Closed(), "Closed client should return true")
}

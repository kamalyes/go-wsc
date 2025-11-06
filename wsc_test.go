package wsc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

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

	ws.OnConnectError(func(err error) {
		assert.Error(t, err, "Should return an error when connecting to an invalid address")
	})

	// 尝试连接
	go ws.Connect()

	// 等待一段时间以确保连接尝试完成
	time.Sleep(1 * time.Second)
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

	config := NewDefaultConfig()
	config.WriteWait = 5 * time.Second
	config.MaxMessageSize = 1024
	config.MessageBufferSize = 128

	client.SetConfig(config)

	assert.Equal(t, 5*time.Second, client.Config.WriteWait)
	assert.Equal(t, int64(1024), client.Config.MaxMessageSize)
	assert.Equal(t, 128, client.Config.MessageBufferSize)
}

// TestWsc_AllCallbacks 测试所有回调函数
func TestWsc_AllCallbacks(t *testing.T) {
	client := New("ws://localhost:8080")
	defer client.Close()

	// 测试 OnConnected
	client.OnConnected(func() {})
	assert.NotNil(t, client.onConnected.Load())

	// 测试 OnConnectError
	client.OnConnectError(func(err error) {})
	assert.NotNil(t, client.onConnectError.Load())

	// 测试 OnDisconnected
	client.OnDisconnected(func(err error) {})
	assert.NotNil(t, client.onDisconnected.Load())

	// 测试 OnClose
	client.OnClose(func(code int, text string) {})
	assert.NotNil(t, client.onClose.Load())

	// 测试 OnTextMessageSent
	client.OnTextMessageSent(func(message string) {})
	assert.NotNil(t, client.onTextMessageSent.Load())

	// 测试 OnBinaryMessageSent
	client.OnBinaryMessageSent(func(data []byte) {})
	assert.NotNil(t, client.onBinaryMessageSent.Load())

	// 测试 OnSentError
	client.OnSentError(func(err error) {})
	assert.NotNil(t, client.onSentError.Load())

	// 测试 OnPingReceived
	client.OnPingReceived(func(appData string) {})
	assert.NotNil(t, client.onPingReceived.Load())

	// 测试 OnPongReceived
	client.OnPongReceived(func(appData string) {})
	assert.NotNil(t, client.onPongReceived.Load())

	// 测试 OnTextMessageReceived
	client.OnTextMessageReceived(func(message string) {})
	assert.NotNil(t, client.onTextMessageReceived.Load())

	// 测试 OnBinaryMessageReceived
	client.OnBinaryMessageReceived(func(data []byte) {})
	assert.NotNil(t, client.onBinaryMessageReceived.Load())
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

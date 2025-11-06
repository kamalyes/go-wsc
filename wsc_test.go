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
	var receivedMessage string
	ws.OnTextMessageReceived(func(message string) {
		receivedMessage = message
	})

	// 等待一段时间以确保消息被接收
	time.Sleep(1 * time.Second)

	// 断言接收到的消息是我们发送的消息
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
	var receivedMessage string
	ws.OnTextMessageReceived(func(message string) {
		receivedMessage = message
	})

	// 等待一段时间以确保消息被接收
	time.Sleep(1 * time.Second)

	// 断言接收到的消息是我们发送的消息
	assert.Equal(t, testMessage, receivedMessage, "Should receive the same message back")
}

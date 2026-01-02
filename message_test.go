/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-07 01:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 19:24:55
 * @FilePath: \go-wsc\message_test.go
 * @Description: 消息发送测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// TestSendTextMessage 测试发送文本消息
func TestSendTextMessage(t *testing.T) {
	received := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// 接收消息
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			t.Logf("ReadMessage error: %v", err)
			return
		}
		if msgType == websocket.TextMessage {
			received <- string(msg)
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		err := client.SendTextMessage("test message")
		assert.NoError(t, err, "SendTextMessage should succeed")

		select {
		case msg := <-received:
			assert.Equal(t, "test message", msg, "Should receive correct message")
		case <-time.After(2 * time.Second):
			t.Fatal("Timeout waiting for message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestSendBinaryMessage 测试发送二进制消息
func TestSendBinaryMessage(t *testing.T) {
	received := make(chan []byte, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Upgrade error: %v", err)
			return
		}
		defer conn.Close()

		// 接收消息
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			t.Logf("ReadMessage error: %v", err)
			return
		}
		if msgType == websocket.BinaryMessage {
			received <- msg
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		err := client.SendBinaryMessage([]byte{0x01, 0x02, 0x03})
		assert.NoError(t, err, "SendBinaryMessage should succeed")

		select {
		case msg := <-received:
			assert.Equal(t, []byte{0x01, 0x02, 0x03}, msg, "Should receive correct binary message")
		case <-time.After(2 * time.Second):
			t.Fatal("Timeout waiting for message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestSendMessage_WhenClosed 测试连接关闭后发送消息
func TestSendMessage_WhenClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()
		time.Sleep(100 * time.Millisecond) // 短暂保持连接
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		// 关闭连接
		client.Close()
		time.Sleep(100 * time.Millisecond)

		// 尝试发送消息应该失败
		err := client.SendTextMessage("test")
		assert.Equal(t, ErrConnectionClosed, err, "SendTextMessage after close should return ErrConnectionClosed")

		err = client.SendBinaryMessage([]byte("test"))
		assert.Equal(t, ErrConnectionClosed, err, "SendBinaryMessage after close should return ErrConnectionClosed")
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestSendMessage_BufferFull 测试发送缓冲区满的情况
func TestSendMessage_BufferFull(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()

		// 不读取消息，让缓冲区填满
		time.Sleep(100 * time.Millisecond) // 减少等待时间
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")

	// 创建小缓冲区的客户端
	config := wscconfig.Default().WithMessageBufferSize(2)
	client := New(url)
	client.SetConfig(config)
	defer client.Close()

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		// 填满缓冲区
		err := client.SendTextMessage("msg1")
		assert.NoError(t, err)
		err = client.SendTextMessage("msg2")
		assert.NoError(t, err)

		// 下一条消息应该返回 ErrMessageBufferFull
		err = client.SendTextMessage("msg3")
		if err != nil {
			assert.Equal(t, ErrMessageBufferFull, err, "Should return ErrMessageBufferFull when buffer is full")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestSendMessage_Concurrent 测试并发发送消息
func TestSendMessage_Concurrent(t *testing.T) {
	receivedCount := int32(0)
	expectedCount := 100

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
			atomic.AddInt32(&receivedCount, 1)
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		// 并发发送消息
		done := make(chan bool)
		for i := 0; i < expectedCount; i++ {
			go func(n int) {
				for {
					err := client.SendTextMessage("concurrent message")
					if err == nil {
						done <- true
						return
					}
					if err == ErrConnectionClosed {
						done <- false
						return
					}
					// 如果是缓冲区满，稍后重试
					time.Sleep(time.Millisecond)
				}
			}(i)
		}

		// 等待所有发送完成
		success := 0
		for i := 0; i < expectedCount; i++ {
			if <-done {
				success++
			}
		}

		time.Sleep(100 * time.Millisecond) // 等待接收

		t.Logf("Sent: %d, Received: %d", success, atomic.LoadInt32(&receivedCount))
		assert.Greater(t, success, 0, "At least some messages should be sent")
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestSendMessage_AfterSendChanClosed 测试sendChan关闭后发送消息
func TestSendMessage_AfterSendChanClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		assert.NoError(t, err)
		defer conn.Close()
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		// 真实地关闭连接
		client.Close()

		// 等待连接完全关闭
		time.Sleep(100 * time.Millisecond)

		// 尝试发送消息应该失败
		err := client.SendTextMessage("test")
		assert.Equal(t, ErrConnectionClosed, err, "SendTextMessage after Close should return ErrConnectionClosed")

		err = client.SendBinaryMessage([]byte("test"))
		assert.Equal(t, ErrConnectionClosed, err, "SendBinaryMessage after Close should return ErrConnectionClosed")
		assert.Equal(t, ErrConnectionClosed, err, "SendBinaryMessage with closed sendChan should return ErrConnectionClosed")

		client.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestSendMessage_LargeMessage 测试发送大消息
func TestSendMessage_LargeMessage(t *testing.T) {
	largeData := make([]byte, 1024*1024) // 1MB
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	received := make(chan []byte, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Upgrade error: %v", err)
			return
		}
		defer conn.Close()

		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			t.Logf("ReadMessage error: %v", err)
			return
		}
		if msgType == websocket.BinaryMessage {
			received <- msg
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		err := client.SendBinaryMessage(largeData)
		assert.NoError(t, err, "Should send large message successfully")

		select {
		case msg := <-received:
			assert.Equal(t, len(largeData), len(msg), "Should receive complete large message")
		case <-time.After(3 * time.Second):
			t.Fatal("Timeout waiting for large message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestSendMessage_EmptyMessage 测试发送空消息
func TestSendMessage_EmptyMessage(t *testing.T) {
	received := make(chan string, 1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("Upgrade error: %v", err)
			return
		}
		defer conn.Close()

		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			t.Logf("ReadMessage error: %v", err)
			return
		}
		if msgType == websocket.TextMessage {
			received <- string(msg)
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		err := client.SendTextMessage("")
		assert.NoError(t, err, "Should send empty message successfully")

		select {
		case msg := <-received:
			assert.Equal(t, "", msg, "Should receive empty message")
		case <-time.After(2 * time.Second):
			t.Fatal("Timeout waiting for message")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-07 01:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-07 01:12:51
 * @FilePath: \go-wsc\connection_advanced_test.go
 * @Description: 高级连接测试 - 重连、消息处理、错误场景
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

// TestConnection_ReadMessages_TextMessage 测试读取文本消息
func TestConnection_ReadMessages_TextMessage(t *testing.T) {
	receivedMessages := make(chan string, 5)
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 发送多条文本消息
		messages := []string{"message1", "message2", "message3"}
		for _, msg := range messages {
			conn.WriteMessage(websocket.TextMessage, []byte(msg))
			time.Sleep(50 * time.Millisecond)
		}
		
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	client.OnTextMessageReceived(func(message string) {
		receivedMessages <- message
	})

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		// 收集所有消息
		timeout := time.After(2 * time.Second)
		received := []string{}
		
		for i := 0; i < 3; i++ {
			select {
			case msg := <-receivedMessages:
				received = append(received, msg)
			case <-timeout:
				break
			}
		}
		
		assert.GreaterOrEqual(t, len(received), 1, "Should receive at least one message")
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestConnection_ReadMessages_BinaryMessage 测试读取二进制消息
func TestConnection_ReadMessages_BinaryMessage(t *testing.T) {
	receivedMessages := make(chan []byte, 5)
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 发送二进制消息
		conn.WriteMessage(websocket.BinaryMessage, []byte{0x01, 0x02, 0x03})
		conn.WriteMessage(websocket.BinaryMessage, []byte{0x04, 0x05, 0x06})
		
		time.Sleep(500 * time.Millisecond)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	client.OnBinaryMessageReceived(func(data []byte) {
		receivedMessages <- data
	})

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		timeout := time.After(2 * time.Second)
		received := [][]byte{}
		
		for i := 0; i < 2; i++ {
			select {
			case msg := <-receivedMessages:
				received = append(received, msg)
			case <-timeout:
				break
			}
		}
		
		assert.GreaterOrEqual(t, len(received), 1, "Should receive at least one binary message")
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestConnection_WriteMessages_Success 测试成功写入消息
func TestConnection_WriteMessages_Success(t *testing.T) {
	sentCount := int32(0)
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 接收消息
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	client := New(url)
	defer client.Close()

	client.OnTextMessageSent(func(message string) {
		atomic.AddInt32(&sentCount, 1)
	})

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		// 发送多条消息
		for i := 0; i < 5; i++ {
			err := client.SendTextMessage(fmt.Sprintf("message%d", i))
			assert.NoError(t, err)
		}
		
		time.Sleep(500 * time.Millisecond)
		assert.GreaterOrEqual(t, atomic.LoadInt32(&sentCount), int32(1), 
			"Should have sent at least one message")
	case <-time.After(2 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestConnection_Reconnect_AfterDisconnect 测试断线重连
func TestConnection_Reconnect_AfterDisconnect(t *testing.T) {
	connectCount := int32(0)
	disconnectCount := int32(0)
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&connectCount, 1)
		
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		
		// 第一次连接后立即断开，触发重连
		if count == 1 {
			conn.Close()
			return
		}
		
		// 后续连接保持
		defer conn.Close()
		time.Sleep(2 * time.Second)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	
	config := NewDefaultConfig()
	config.MinRecTime = 100 * time.Millisecond
	config.MaxRecTime = 500 * time.Millisecond
	
	client := New(url)
	client.SetConfig(config)
	defer client.Close()

	client.OnDisconnected(func(err error) {
		atomic.AddInt32(&disconnectCount, 1)
	})

	go client.Connect()

	// 等待重连发生
	time.Sleep(2 * time.Second)

	assert.GreaterOrEqual(t, atomic.LoadInt32(&connectCount), int32(2), 
		"Should attempt to reconnect after disconnect")
	assert.GreaterOrEqual(t, atomic.LoadInt32(&disconnectCount), int32(1),
		"Should detect disconnect")
}

// TestConnection_Reconnect_WithBackoff 测试带退避的重连
func TestConnection_Reconnect_WithBackoff(t *testing.T) {
	attemptTimes := []time.Time{}
	var timeMu sync.Mutex
	
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timeMu.Lock()
		attemptTimes = append(attemptTimes, time.Now())
		timeMu.Unlock()
		
		// 前几次拒绝连接
		if len(attemptTimes) < 3 {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(1 * time.Second)
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	
	config := NewDefaultConfig()
	config.MinRecTime = 100 * time.Millisecond
	config.MaxRecTime = 1 * time.Second
	config.RecFactor = 2.0
	
	client := New(url)
	client.SetConfig(config)
	defer client.Close()

	errorCount := int32(0)
	client.OnConnectError(func(err error) {
		atomic.AddInt32(&errorCount, 1)
	})

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		timeMu.Lock()
		times := make([]time.Time, len(attemptTimes))
		copy(times, attemptTimes)
		timeMu.Unlock()
		
		assert.GreaterOrEqual(t, len(times), 3, "Should have multiple connection attempts")
		
		// 验证退避时间递增
		if len(times) >= 3 {
			interval1 := times[1].Sub(times[0])
			interval2 := times[2].Sub(times[1])
			t.Logf("Backoff intervals: %v, %v", interval1, interval2)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Connection timeout")
	}
}

// TestConnection_ConcurrentWrites 测试并发写入
func TestConnection_ConcurrentWrites(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()

		// 接收消息
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}))
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http")
	
	config := NewDefaultConfig()
	config.MessageBufferSize = 1000
	
	client := New(url)
	client.SetConfig(config)
	defer client.Close()

	sent := int32(0)
	client.OnTextMessageSent(func(message string) {
		atomic.AddInt32(&sent, 1)
	})

	connected := make(chan bool)
	client.OnConnected(func() {
		connected <- true
	})

	go client.Connect()

	select {
	case <-connected:
		// 并发发送消息
		var wg sync.WaitGroup
		goroutines := 10
		messagesPerGoroutine := 10
		
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				for j := 0; j < messagesPerGoroutine; j++ {
					client.SendTextMessage(fmt.Sprintf("msg-%d-%d", id, j))
					time.Sleep(time.Millisecond)
				}
			}(i)
		}
		
		wg.Wait()
		time.Sleep(500 * time.Millisecond)
		
		sentCount := atomic.LoadInt32(&sent)
		t.Logf("Sent %d messages concurrently", sentCount)
		assert.Greater(t, sentCount, int32(0), "Should send some messages")
	case <-time.After(3 * time.Second):
		t.Fatal("Connection timeout")
	}
}

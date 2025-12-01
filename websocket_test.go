/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 09:25:15
 * @FilePath: \go-wsc\websocket_test.go
 * @Description: WebSocket结构体及其配置选项测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"net/http"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

// 定义常量以避免重复的 URL 字符串
const testURL = "ws://localhost:8080/ws"

func TestNewWebSocket(t *testing.T) {
	ws := NewWebSocket(testURL)

	assert.Equal(t, testURL, ws.Url, "Expected URL should match")
	assert.Equal(t, websocket.DefaultDialer, ws.Dialer, "Expected default dialer should match")
	assert.False(t, ws.isConnected, "Expected isConnected to be false")
}

func TestWithDialer(t *testing.T) {
	ws := NewWebSocket(testURL)
	customDialer := &websocket.Dialer{}
	ws.WithDialer(customDialer)

	assert.Equal(t, customDialer, ws.Dialer, "Expected custom dialer should be set")
}

func TestWithRequestHeader(t *testing.T) {
	ws := NewWebSocket(testURL)
	header := http.Header{}
	header.Set("Authorization", "Bearer token")
	ws.WithRequestHeader(header)

	assert.Equal(t, "Bearer token", ws.RequestHeader.Get("Authorization"), "Expected Authorization header to be set")
}

func TestWithSendBufferSize(t *testing.T) {
	ws := NewWebSocket(testURL)
	ws.WithSendBufferSize(512)

	assert.Equal(t, 512, cap(ws.sendChan), "Expected send channel capacity to be 512")
}

func TestWithCustomURL(t *testing.T) {
	ws := NewWebSocket("ws://localhost:8080/ws")
	newURL := "ws://new-url.com/ws"
	ws.WithCustomURL(newURL)

	assert.Equal(t, newURL, ws.Url, "Expected URL should match the new URL")
}

// TestWithDialer_ChainedCalls 测试链式调用
func TestWithDialer_ChainedCalls(t *testing.T) {
	customDialer := &websocket.Dialer{
		HandshakeTimeout: 10,
	}

	ws := NewWebSocket(testURL).
		WithDialer(customDialer).
		WithSendBufferSize(1024).
		WithCustomURL("ws://chained.com/ws")

	assert.Equal(t, customDialer, ws.Dialer)
	assert.Equal(t, 1024, cap(ws.sendChan))
	assert.Equal(t, "ws://chained.com/ws", ws.Url)
}

// TestWithRequestHeader_MultipleHeaders 测试多个请求头
func TestWithRequestHeader_MultipleHeaders(t *testing.T) {
	ws := NewWebSocket(testURL)
	header := http.Header{}
	header.Set("Authorization", "Bearer token123")
	header.Set("X-Custom-Header", "custom-value")
	header.Set("User-Agent", "test-client/1.0")

	ws.WithRequestHeader(header)

	assert.Equal(t, "Bearer token123", ws.RequestHeader.Get("Authorization"))
	assert.Equal(t, "custom-value", ws.RequestHeader.Get("X-Custom-Header"))
	assert.Equal(t, "test-client/1.0", ws.RequestHeader.Get("User-Agent"))
}

// TestWithSendBufferSize_DifferentSizes 测试不同的缓冲区大小
func TestWithSendBufferSize_DifferentSizes(t *testing.T) {
	testCases := []struct {
		name string
		size int
	}{
		{"Minimal", 1},
		{"Small", 10},
		{"Medium", 256},
		{"Large", 1024},
		{"VeryLarge", 10000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ws := NewWebSocket(testURL)
			ws.WithSendBufferSize(tc.size)
			assert.Equal(t, tc.size, cap(ws.sendChan),
				"Send buffer size should be %d", tc.size)
		})
	}
}

// TestWithCustomURL_Various 测试各种URL格式
func TestWithCustomURL_Various(t *testing.T) {
	testCases := []struct {
		name string
		url  string
	}{
		{"Simple", "ws://localhost:8080"},
		{"WithPath", "ws://localhost:8080/custom/path"},
		{"WithQuery", "ws://localhost:8080/ws?param=value"},
		{"SecureWS", "wss://secure.example.com/ws"},
		{"WithPort", "ws://example.com:9090/ws"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ws := NewWebSocket(testURL)
			ws.WithCustomURL(tc.url)
			assert.Equal(t, tc.url, ws.Url)
		})
	}
}

// TestWebSocket_InitialState 测试WebSocket初始状态
func TestWebSocket_InitialState(t *testing.T) {
	ws := NewWebSocket(testURL)

	assert.NotNil(t, ws)
	assert.Equal(t, testURL, ws.Url)
	assert.NotNil(t, ws.Dialer)
	assert.NotNil(t, ws.RequestHeader)
	assert.NotNil(t, ws.sendChan)
	assert.False(t, ws.isConnected)
	assert.Nil(t, ws.Conn)
	assert.Nil(t, ws.HttpResponse)
	assert.Equal(t, int32(0), ws.sendChanClosed)
}

// TestWebSocket_DefaultValues 测试默认值
func TestWebSocket_DefaultValues(t *testing.T) {
	ws := NewWebSocket(testURL)

	assert.Equal(t, websocket.DefaultDialer, ws.Dialer, "Should use default dialer")
	assert.Equal(t, 256, cap(ws.sendChan), "Default buffer size should be 256")
	assert.Equal(t, 0, len(ws.RequestHeader), "Request header should be empty initially")
}

// TestWithDialer_NilDialer 测试nil拨号器
func TestWithDialer_NilDialer(t *testing.T) {
	ws := NewWebSocket(testURL)
	ws.WithDialer(nil)

	// 即使设置为nil，也应该被接受（调用者负责确保有效性）
	assert.Nil(t, ws.Dialer)
}

// TestWithRequestHeader_EmptyHeader 测试空请求头
func TestWithRequestHeader_EmptyHeader(t *testing.T) {
	ws := NewWebSocket(testURL)
	emptyHeader := http.Header{}
	ws.WithRequestHeader(emptyHeader)

	assert.Equal(t, 0, len(ws.RequestHeader))
}

// TestWithSendBufferSize_ZeroSize 测试零缓冲区大小
func TestWithSendBufferSize_ZeroSize(t *testing.T) {
	ws := NewWebSocket(testURL)
	originalCap := cap(ws.sendChan)
	ws.WithSendBufferSize(0)

	// 零大小不会改变channel，保持原值
	assert.Equal(t, originalCap, cap(ws.sendChan))
}

// TestWithCustomURL_EmptyURL 测试空URL
func TestWithCustomURL_EmptyURL(t *testing.T) {
	ws := NewWebSocket(testURL)
	ws.WithCustomURL("")

	assert.Equal(t, "", ws.Url)
}

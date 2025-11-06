/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 09:50:55
 * @FilePath: \go-wsc\websocket_test.go
 * @Description:
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

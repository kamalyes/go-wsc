/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:02:15
 * @FilePath: \go-wsc\hub\sse_test.go
 * @Description: Hub SSE 连接支持白盒单元测试（覆盖 hub/sse.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendToUserViaSSE_Success 验证向已注册 SSE 用户发送消息成功
func TestSendToUserViaSSE_Success(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c := makeSSEClient("c-sse1", "u-sse1")
	hub.shardedRegistry.AddClient(c)

	msg := makeGroupMessage("sender")
	ok := hub.SendToUserViaSSE("u-sse1", msg)
	assert.True(t, ok)

	select {
	case got := <-c.SSEMessageCh:
		assert.Same(t, msg, got)
	case <-time.After(time.Second):
		t.Fatal("SSE 消息未投递到 SSEMessageCh")
	}
}

// TestSendToUserViaSSE_NoSSEUser 验证用户无 SSE 连接时返回 false
func TestSendToUserViaSSE_NoSSEUser(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ok := hub.SendToUserViaSSE("u-no-sse", makeGroupMessage("sender"))
	assert.False(t, ok)
}

// TestSendToUserViaSSE_QueueFull 验证 SSE 消息队列满时返回 false
func TestSendToUserViaSSE_QueueFull(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// SSEMessageCh 缓冲为 1，先填满
	c := makeSSEClient("c-sse-full", "u-sse-full")
	c.SSEMessageCh = make(chan *HubMessage, 1)
	c.SSEMessageCh <- makeGroupMessage("fill")
	hub.shardedRegistry.AddClient(c)

	// 队列已满，SendToUserViaSSE 应返回 false（successCount=0）
	ok := hub.SendToUserViaSSE("u-sse-full", makeGroupMessage("sender"))
	assert.False(t, ok)
}

// TestGetSSEClientCount 验证获取 SSE 客户端数量
func TestGetSSEClientCount(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.Equal(t, 0, hub.GetSSEClientCount())

	hub.shardedRegistry.AddClient(makeSSEClient("c1", "u1"))
	hub.shardedRegistry.AddClient(makeSSEClient("c2", "u2"))
	assert.Equal(t, 2, hub.GetSSEClientCount())
}

// TestGetSSEClients 验证获取所有 SSE 客户端列表
func TestGetSSEClients(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeSSEClient("c1", "u1"))
	hub.shardedRegistry.AddClient(makeSSEClient("c2", "u2"))

	clients := hub.GetSSEClients()
	assert.Len(t, clients, 2)
}

// TestIsSSEClientOnline 验证检查 SSE 客户端是否在线
func TestIsSSEClientOnline(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.False(t, hub.IsSSEClientOnline("u-sse-chk"))

	hub.shardedRegistry.AddClient(makeSSEClient("c-chk", "u-sse-chk"))
	assert.True(t, hub.IsSSEClientOnline("u-sse-chk"))
}

// TestBroadcastToSSEClients 验证广播消息到所有 SSE 客户端
func TestBroadcastToSSEClients(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeSSEClient("c1", "u1")
	c2 := makeSSEClient("c2", "u2")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	msg := makeGroupMessage("sender")
	hub.broadcastToSSEClients(msg)

	for _, c := range []*Client{c1, c2} {
		select {
		case got := <-c.SSEMessageCh:
			assert.Same(t, msg, got)
		case <-time.After(time.Second):
			t.Fatal("广播消息未投递到 SSE 客户端")
		}
	}
}

// TestRegisterSSE 验证注册 SSE 连接（需 Run 处理异步注册）
func TestRegisterSSE(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// httptest.ResponseRecorder 实现了 http.Flusher
	rec := httptest.NewRecorder()
	client, err := hub.RegisterSSE("u-reg", rec, UserTypeCustomer)
	require.NoError(t, err)
	require.NotNil(t, client)
	assert.Equal(t, ConnectionTypeSSE, client.ConnectionType)
	assert.Equal(t, "u-reg", client.UserID)

	// 等待 Run 完成 register
	require.Eventually(t, func() bool { return hub.IsSSEClientOnline("u-reg") }, time.Second, 10*time.Millisecond)

	// 验证 SSE 响应头已设置
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
}

// TestRegisterSSE_NoFlusher 验证 ResponseWriter 不支持 Flusher 时返回错误
func TestRegisterSSE_NoFlusher(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 构造一个不实现 http.Flusher 的 ResponseWriter
	var w http.ResponseWriter = noFlusherWriter{}
	client, err := hub.RegisterSSE("u-noflush", w, UserTypeCustomer)
	require.Error(t, err)
	assert.Nil(t, client)
	assert.Contains(t, err.Error(), "streaming not supported")
}

// noFlusherWriter 仅实现 http.ResponseWriter，不实现 http.Flusher
type noFlusherWriter struct{}

func (noFlusherWriter) Header() http.Header       { return http.Header{} }
func (noFlusherWriter) Write([]byte) (int, error) { return 0, nil }
func (noFlusherWriter) WriteHeader(int)           {}

// TestUnregisterSSE 验证注销 SSE 连接
func TestUnregisterSSE(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	rec := httptest.NewRecorder()
	client, err := hub.RegisterSSE("u-unreg", rec, UserTypeCustomer)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return hub.IsSSEClientOnline("u-unreg") }, time.Second, 10*time.Millisecond)

	// 注销
	hub.UnregisterSSE(client.ID)
	// 等待 Run 处理 unregister
	require.Eventually(t, func() bool { return !hub.IsSSEClientOnline("u-unreg") }, time.Second, 10*time.Millisecond)
}

// TestUnregisterSSE_NotExist 验证注销不存在的客户端不 panic
func TestUnregisterSSE_NotExist(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.NotPanics(t, func() {
		hub.UnregisterSSE("no-such-client")
	})
}

// TestUnregisterSSE_NotSSE 验证注销非 SSE 客户端不触发注销
func TestUnregisterSSE_NotSSE(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 注册一个 WebSocket 客户端（非 SSE）
	c := makeTestClient("c-ws", "u-ws")
	hub.shardedRegistry.AddClient(c)

	// 注销非 SSE 客户端不应触发异步注销
	assert.NotPanics(t, func() {
		hub.UnregisterSSE("c-ws")
	})
}

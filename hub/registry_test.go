/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-01 17:26:30
 * @FilePath: \go-wsc\hub\registry_test.go
 * @Description: 客户端注册测试（含节点总连接数硬限制）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWSConnPair 建立一对真实 WebSocket 连接（服务端 + 客户端），用于测试
// 返回的 serverConn 通常作为 Client.Conn；clientConn 用于验证连接行为（如关闭）
func newWSConnPair(t *testing.T) (serverConn, clientConn *websocket.Conn) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverConnCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	t.Cleanup(srv.Close)

	var err error
	clientConn, _, err = websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http://", "ws://", 1), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case serverConn = <-serverConnCh:
		require.NotNil(t, serverConn)
	case <-time.After(2 * time.Second):
		t.Fatal("等待服务端 WebSocket 升级超时")
	}
	t.Cleanup(func() { _ = serverConn.Close() })
	return serverConn, clientConn
}

// newTestClient 构造一个用于测试的 Client（带真实 WebSocket 连接）
func newTestClient(id, userID string, conn *websocket.Conn) *Client {
	return &Client{
		ID:             id,
		UserID:         userID,
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		Context:        context.Background(),
		Metadata:       make(map[string]interface{}),
		Conn:           conn,
	}
}

// TestHandleRegisterMaxConnectionsLimit 测试节点总连接数硬限制
// MaxConnectionsPerNode=2 时：前 2 个注册成功，第 3 个被拒绝（发 Close 帧 1013 并关闭连接，未入注册表）
func TestHandleRegisterMaxConnectionsLimit(t *testing.T) {
	config := wscconfig.Default()
	if config.Performance == nil {
		config.Performance = &wscconfig.Performance{}
	}
	config.Performance.MaxConnectionsPerNode = 2

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	hub.WaitForStart()

	// 注册 2 个客户端（达到上限，使用不同 userID 避免触发单用户连接数限制）
	for i := 0; i < 2; i++ {
		sConn, _ := newWSConnPair(t)
		client := newTestClient(fmt.Sprintf("client-%d", i), fmt.Sprintf("user-%d", i), sConn)
		hub.Register(client)
		require.Eventually(t, func() bool {
			return hub.HasClient(client.ID)
		}, 2*time.Second, 20*time.Millisecond, "第 %d 个客户端应注册成功", i+1)
	}
	require.Equal(t, int64(2), hub.GetClientCount(), "应已注册 2 个客户端")

	// 第 3 个应被节点总连接数硬限制拒绝
	sConn3, cConn3 := newWSConnPair(t)
	rejected := newTestClient("client-rejected", "user-rejected", sConn3)
	hub.Register(rejected)

	// 验证连接被关闭：对端应收到 Close 帧(1013 Try Again Later) 或连接错误
	cConn3.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := cConn3.ReadMessage()
	require.Error(t, err, "被拒绝的连接应被关闭")
	if ce, ok := err.(*websocket.CloseError); ok {
		assert.Equal(t, websocket.CloseTryAgainLater, ce.Code, "应收到 1013 Try Again Later close 帧")
	}

	// 验证被拒绝的客户端未入注册表，连接数仍为 2
	assert.False(t, hub.HasClient(rejected.ID), "被拒绝的客户端不应进入注册表")
	assert.Equal(t, int64(2), hub.GetClientCount(), "连接数应保持为 2")
}

// TestHandleRegisterMaxConnectionsZeroUnlimited 测试 MaxConnectionsPerNode=0 时不限制
func TestHandleRegisterMaxConnectionsZeroUnlimited(t *testing.T) {
	config := wscconfig.Default()
	if config.Performance == nil {
		config.Performance = &wscconfig.Performance{}
	}
	config.Performance.MaxConnectionsPerNode = 0

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	hub.WaitForStart()

	// 注册 5 个客户端（不同用户），0 表示不限制，应全部成功
	for i := 0; i < 5; i++ {
		sConn, _ := newWSConnPair(t)
		client := newTestClient(fmt.Sprintf("client-%d", i), fmt.Sprintf("user-%d", i), sConn)
		hub.Register(client)
	}
	require.Eventually(t, func() bool {
		return hub.GetClientCount() == 5
	}, 2*time.Second, 20*time.Millisecond, "0 表示不限制,5 个客户端应全部注册成功")
}

// TestGetMaxConnectionsPerNode 测试辅助方法对 nil 配置的安全性
func TestGetMaxConnectionsPerNode(t *testing.T) {
	t.Run("正常配置", func(t *testing.T) {
		config := wscconfig.Default()
		if config.Performance == nil {
			config.Performance = &wscconfig.Performance{}
		}
		config.Performance.MaxConnectionsPerNode = 3000
		hub := NewHub(config)
		assert.Equal(t, 3000, hub.GetMaxConnectionsPerNode())
	})

	t.Run("Performance 为 nil", func(t *testing.T) {
		config := wscconfig.Default()
		config.Performance = nil
		hub := NewHub(config)
		assert.Equal(t, 0, hub.GetMaxConnectionsPerNode(), "Performance 为 nil 时应返回 0（不限制）")
	})
}

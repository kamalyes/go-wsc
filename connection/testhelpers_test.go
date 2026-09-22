/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 15:00:22
 * @FilePath: \go-wsc\connection\testhelpers_test.go
 * @Description: 连接域测试基建 - 真 WebSocket 连接对构造
 *
 * 迁移自 hub/registry_test.go 的 newWSConnPair（P2 批1 测试随源归位）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"
)

// newWSConnPair 建立真 WebSocket 连接对（服务端/客户端各持一端）
// 用于 frame_writer 合帧写出与 control_lane 断链降级等需要真实连接语义的测试
func newWSConnPair(t testing.TB) (serverConn, clientConn *websocket.Conn) {
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

	// 阻塞等待服务端就绪：Dial 返回（收到 101）与 handler 投递 conn 之间存在调度窗口，
	// 非阻塞 select 在并行负载下会偶发取空导致测试假失败
	select {
	case serverConn = <-serverConnCh:
	case <-time.After(2 * time.Second):
		t.Fatal("服务端连接未就绪")
	}
	return serverConn, clientConn
}

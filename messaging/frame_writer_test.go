/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 16:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 16:30:00
 * @FilePath: \go-wsc\messaging\frame_writer_test.go
 * @Description: writev 合帧写出测试 —— 控制通道批量写的顺序与 lane 隔离
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestWSPair 启动本地 WS 服务端并返回（服务端连接, 客户端连接）
// 服务端连接即写泵的写出目标（models.Client.Conn 侧）
func newTestWSPair(t *testing.T) (*websocket.Conn, *websocket.Conn) {
	t.Helper()
	up := websocket.Upgrader{}
	srvCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		srvCh <- conn
	}))
	t.Cleanup(srv.Close)

	dialURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	clientConn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	var serverConn *websocket.Conn
	select {
	case serverConn = <-srvCh:
	case <-time.After(3 * time.Second):
		t.Fatal("等待服务端连接超时")
	}
	t.Cleanup(func() { _ = serverConn.Close() })
	return serverConn, clientConn
}

// TestWriteControlBatchOrderAndLaneIsolation 控制通道批量写：
// 首条直写 + CtrlCh 积压合帧一次写出，帧序 = 接收序（FIFO），数据 lane 不被触碰
func TestWriteControlBatchOrderAndLaneIsolation(t *testing.T) {
	m, _ := newTestManager()
	serverConn, clientConn := newTestWSPair(t)

	client := makeTestClient("c-ctrl", "u-ctrl")
	client.Conn = serverConn
	client.CtrlCh = make(chan []byte, 16)

	// 首条直写 + CtrlCh 预填 3 条积压 = 一批 4 条控制帧
	first := []byte(`{"seq":0}`)
	for i := 1; i <= 3; i++ {
		require.True(t, client.TrySendControl([]byte(fmt.Sprintf(`{"seq":%d}`, i))),
			"CtrlCh 容量 16，预填 3 条应全部成功")
	}
	// 数据 lane 预填：控制批量写不得消费
	require.True(t, client.TrySend([]byte(`{"lane":"data"}`)))

	require.NoError(t, m.writeControlBatch(client, first))

	// 客户端侧按接收序收到 4 条控制帧（writev 合帧不改帧内顺序）
	for i := 0; i <= 3; i++ {
		mt, data, err := clientConn.ReadMessage()
		require.NoError(t, err)
		require.Equal(t, websocket.TextMessage, mt)
		assert.Equal(t, fmt.Sprintf(`{"seq":%d}`, i), string(data), "控制帧应按 FIFO 顺序到达")
	}

	// CtrlCh 已排空、数据 lane 未被触碰（lane 隔离）
	assert.Len(t, client.CtrlCh, 0)
	assert.Len(t, client.SendChan, 1)
}

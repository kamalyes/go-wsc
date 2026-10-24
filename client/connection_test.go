/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-24 18:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-24 18:30:00
 * @FilePath: \go-wsc\client\connection_test.go
 * @Description: 连接生命周期回归测试 —— 重连循环可中断、服务端优雅关闭触发重连、
 * 回调内关闭不死锁（对应 connection.go 三处并发缺陷修复）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reconnectOnceWSServer 首次连接由服务端主动优雅关闭（模拟滚动发布），
// 第二次起正常保活——用于验证客户端对服务端 close frame 的重连行为
func reconnectOnceWSServer(t *testing.T) (string, func() int32) {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := DefaultUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if count.Add(1) == 1 {
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, "rolling deploy"),
				time.Now().Add(time.Second))
			_ = conn.Close()
			return
		}
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), count.Load
}

// pushOnceWSServer 升级后向客户端推送一条文本消息（用于触发消息回调）
func pushOnceWSServer(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := DefaultUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"hello":"world"}`))
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// TestConnectLoopExitsAfterClose 主动关闭后重连循环必须退出（可中断退避等待）
// 回归场景：Connect 对不可达地址无限重试，time.Sleep 不可中断且每轮无关闭检查——
// Close 后循环继续拨号（连接复活）或服务器不可达时 goroutine 永久泄漏。
// 修复后退避等待 select closeSignal，Close 即退出
func TestConnectLoopExitsAfterClose(t *testing.T) {
	c := New("ws://127.0.0.1:1") // 不可达地址：拨号必然失败进入重试循环
	c.Config.AutoReconnect = true

	connectDone := make(chan struct{})
	go func() {
		defer close(connectDone)
		c.Connect()
	}()

	// 等循环进入退避等待（首轮拨号失败即进入）
	time.Sleep(200 * time.Millisecond)
	c.Close()

	select {
	case <-connectDone:
		// 循环已退出
	case <-time.After(3 * time.Second):
		t.Fatal("Close 后 Connect 重试循环未退出（退避等待不可中断，goroutine 泄漏）")
	}
	assert.True(t, c.Closed(), "关闭后状态应为 Disconnected")
}

// TestServerGracefulCloseTriggersReconnect 服务端优雅关闭（close frame）触发重连
// 回归场景：CloseHandler 内提前 clean 置 Disconnected，重连被 Closed() 拦截——
// 服务端滚动发布/踢人后 AutoReconnect 客户端永久掉线。
// 修复后清理决策收敛到 handleReconnectOrClean，close frame 走正常重连路径
func TestServerGracefulCloseTriggersReconnect(t *testing.T) {
	url, count := reconnectOnceWSServer(t)

	c := New(url)
	c.Config.AutoReconnect = true
	c.Connect()
	defer c.Close()

	// 服务端首次连接即优雅关闭；客户端应自动重连（第二次连接正常保活）
	require.Eventually(t, func() bool {
		return count() >= 2
	}, 5*time.Second, 50*time.Millisecond, "服务端优雅关闭后客户端应重连")
}

// TestCallbackCloseNoDeadlock 消息回调内调用 Close 不死锁
// 回归场景：processReceivedMessage/writeMessages 持 c.mu 执行用户回调，
// 回调内 Close→clean 需同一把 mu——读/写协程持锁等回调、回调等锁，自死锁。
// 修复后回调在锁外分发
func TestCallbackCloseNoDeadlock(t *testing.T) {
	url := pushOnceWSServer(t)

	c := New(url)
	closed := make(chan struct{})
	c.OnTextMessageReceived(func(string) {
		// 常见用法：收到特定消息即断开
		c.Close()
		close(closed)
	})
	c.Connect()

	select {
	case <-closed:
		// Close 在回调内完成，无死锁
	case <-time.After(3 * time.Second):
		t.Fatal("消息回调内 Close 死锁（回调与 clean 争用同一把 mu）")
	}
	assert.True(t, c.Closed())
}

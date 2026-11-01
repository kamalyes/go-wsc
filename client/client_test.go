/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 17:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 17:05:00
 * @FilePath: \go-wsc\client\client_test.go
 * @Description: LB 直连模式测试 —— 多地址随机分布、失败轮转与单地址回退
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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCountingWSServer 启动计数 WS 服务端（升级即计数，读循环保活直至客户端断开）
func newCountingWSServer(t *testing.T) (string, func() int32) {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := DefaultUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		count.Add(1)
		for {
			if _, _, rerr := conn.ReadMessage(); rerr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), count.Load
}

// waitCount 轮询等到计数达到 want，超时才判红
//
// count.Add(1) 发生在服务端 handler 的协程里，而客户端的 Connect 在自己这一侧握手完成
// 就返回了，两者之间没有任何同步 —— 于是「连上了」与「服务端已计数」是两件事
// 立刻读计数在快机器上碰巧为真，在 CI 上读到 0：实测 Go 1.27 / ubuntu-latest 上
// TestWithServersFailover 就是这么红的，断言 1 实际 0，而连接本身是成功的
//
// 轮询把「等够了没」交给实际计数判断，既不会因为机器慢而误判，也不会在机器快时白等
//
// [EN] count.Add(1) runs in the server handler's goroutine while the client's Connect returns
// once its own side of the handshake is done, with no synchronisation between them, so "the
// client is connected" and "the server has counted it" are two different facts. Reading the
// counter at once happens to work on a fast machine and read zero in CI, which is exactly how
// this failed on Go 1.27 with ubuntu-latest: one expected, zero seen, while the connection
// itself had succeeded. Polling lets the actual count decide when the wait is over
func waitCount(t *testing.T, count func() int32, want int32) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if count() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等了 3s 计数只到 %d，期望至少 %d", count(), want)
}

// TestWithServersFailover 失败轮转：起始地址不可达时同轮转至可用地址，
// ConnectedUrl 记录实际连上的节点
func TestWithServersFailover(t *testing.T) {
	dead := "ws://127.0.0.1:1" // 特权端口，几乎不会有服务监听，保证拨号失败
	aliveURL, aliveCount := newCountingWSServer(t)

	c := NewWithServers([]string{dead, dead, aliveURL})
	c.Connect()
	defer c.Close()

	assert.True(t, c.IsConnected(), "同轮轮转应最终连上可用地址")
	assert.Equal(t, aliveURL, c.ConnectedUrl(), "应记录实际连上的地址")

	// 先等服务端把这一次连接计上，再断言「恰好一次」——
	// 后半句才是这个测试要守住的：轮转过程中不该把可用地址拨多次
	// [EN] Wait for the server to count this connection, then assert it happened exactly once,
	// which is the part that matters: rotation must not dial the live address more than once
	waitCount(t, aliveCount, 1)
	assert.Equal(t, int32(1), aliveCount(), "可用服务器应恰被连接一次")
}

// TestWithServersRandomDistribution 随机分布：多客户端随机挑选地址，
// 连接应均匀落到各服务器（20 客户端全落一侧的概率 2^-19，可安全断言双侧均有）
func TestWithServersRandomDistribution(t *testing.T) {
	urlA, countA := newCountingWSServer(t)
	urlB, countB := newCountingWSServer(t)

	const total = 20
	clients := make([]*Client, 0, total)
	for i := 0; i < total; i++ {
		c := NewWithServers([]string{urlA, urlB})
		c.Connect()
		clients = append(clients, c)
	}
	defer func() {
		for _, c := range clients {
			c.Close()
		}
	}()

	// 20 个客户端各自的连接都要等服务端计上，否则读到的是一个还在增长的中间值
	// [EN] All twenty connections have to be counted server-side, or the sum read here is an
	// intermediate value still growing
	waitCount(t, func() int32 { return countA() + countB() }, total)
	assert.Equal(t, int32(total), countA()+countB(), "全部客户端应完成连接")
	assert.True(t, countA() > 0 && countB() > 0,
		"随机挑选应将连接分布到双侧服务器（A=%d B=%d）", countA(), countB())
}

// TestWithServersSingleFallback 单地址回退：列表仅一个地址时保持固定拨号行为
func TestWithServersSingleFallback(t *testing.T) {
	url, count := newCountingWSServer(t)

	c := New("").WithServers([]string{"", url, url}) // 空地址与重复项被清理，剩单地址
	require.Len(t, c.WebSocket.servers, 1)
	c.Connect()
	defer c.Close()

	assert.True(t, c.IsConnected())
	assert.Equal(t, url, c.ConnectedUrl())

	waitCount(t, count, 1)
	assert.Equal(t, int32(1), count())
}

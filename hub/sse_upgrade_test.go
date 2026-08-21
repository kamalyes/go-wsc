/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-06-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-06-25 10:56:20
 * @FilePath: \go-wsc\hub\sse_upgrade_test.go
 * @Description: SSE 网关接入端到端测试（HandleSSEUpgrade 真实 HTTP + text/event-stream 流读取）
 *
 * 用 httptest.NewServer(HandleSSEUpgrade) + http.Get + bufio.Scanner 读取 SSE 流，
 * 验证完整 SSE 生命周期：连接 → 消息投递 → 断联 → 重连 → 心跳 → 隔离 → token 鉴权 → goroutine 不泄漏。
 *
 * 与 sse_test.go（白盒单元测试，直接操作 SSEMessageCh）不同，本文件走真实 HTTP 链路，
 * 验证 HandleSSEUpgrade → createSSEClient → handleRegister → handleSSEWriteLoop 的完整闭环。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runtimeNumGoroutine 包装 runtime.NumGoroutine，便于测试
func runtimeNumGoroutine() int {
	// 触发 GC 让已结束的 goroutine 计数回收
	runtime.GC()
	return runtime.NumGoroutine()
}

// sseUpgradeTestEnv SSE 升级测试环境
type sseUpgradeTestEnv struct {
	hub     *Hub
	server  *httptest.Server
	cleanup func()
}

// newSSEUpgradeEnv 构造 SSE 升级测试环境：真实 Hub + httptest.NewServer(HandleSSEUpgrade)
func newSSEUpgradeEnv(t *testing.T, opts ...func(*wscconfig.WSC)) *sseUpgradeTestEnv {
	t.Helper()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second). // 关闭心跳超时干扰
		WithMessageBufferSize(256)
	config.AllowMultiLogin = true
	for _, opt := range opts {
		opt(config)
	}

	hub := NewHub(config)
	go hub.Run()
	hub.WaitForStart()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleSSEUpgrade))
	cleanup := func() {
		_ = hub.SafeShutdown()
		server.Close()
	}
	return &sseUpgradeTestEnv{hub: hub, server: server, cleanup: cleanup}
}

// sseStreamReader 单 goroutine 持续读取 SSE 流，事件/心跳写入 channel 供消费
// 解决旧 readSSEEvent 每次调用都起一个 goroutine 读同一 scanner 的泄漏与数据竞争：
// 超时返回后旧 goroutine 仍阻塞在 scanner.Scan()，后续调用的 goroutine 与之争抢数据，
// 导致消息被泄漏 goroutine 吞掉（NamespaceIsolation 失败的根因）
type sseStreamReader struct {
	events chan string // data 事件内容 或 心跳注释行（: ping）
	done   chan struct{}
	once   sync.Once
}

// newSSEStreamReader 启动唯一读取 goroutine 消费 body，事件投递到 events channel
func newSSEStreamReader(body io.ReadCloser) *sseStreamReader {
	scanner := bufio.NewScanner(body)
	r := &sseStreamReader{
		events: make(chan string, 64),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(r.events)
		var data strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			// 心跳注释行（: 开头）：直接转发，浏览器 EventSource 自动忽略
			if strings.HasPrefix(line, ":") {
				select {
				case r.events <- line:
				case <-r.done:
					return
				}
				continue
			}
			// data 行：累积（SSE 协议多行 data 每行都要前缀）
			if strings.HasPrefix(line, "data: ") {
				data.WriteString(strings.TrimPrefix(line, "data: "))
			}
			// 空行：事件结束，投递累积内容
			if line == "" && data.Len() > 0 {
				select {
				case r.events <- data.String():
				case <-r.done:
					return
				}
				data.Reset()
			}
		}
	}()
	return r
}

// readEvent 从事件 channel 读一条，超时返回 ""
func (r *sseStreamReader) readEvent(timeout time.Duration) string {
	select {
	case s, ok := <-r.events:
		if !ok {
			return ""
		}
		return s
	case <-time.After(timeout):
		return ""
	}
}

// close 停止读取 goroutine（幂等）
func (r *sseStreamReader) close() {
	r.once.Do(func() { close(r.done) })
}

// dialSSE 拨 SSE 连接，返回 (resp, reader, cancelFn)
// cancelFn 调用会关闭 reader + 连接（触发服务端写循环退出 → Unregister）
func (e *sseUpgradeTestEnv) dialSSE(t *testing.T, userID string, opts ...string) (*http.Response, *sseStreamReader, func()) {
	t.Helper()
	url := e.server.URL + "?user_id=" + userID + "&user_type=customer"
	for _, o := range opts {
		url += "&" + o
	}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "SSE 拨号失败")
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"), "应返回 text/event-stream")
	reader := newSSEStreamReader(resp.Body)
	return resp, reader, func() {
		reader.close()
		cancel()
		_ = resp.Body.Close()
	}
}

// ============================================================================
// 场景 1：SSE 连接 → SendToUserViaSSE → 从流读到 data: <json>
// ============================================================================

func TestHandleSSEUpgrade_BasicDelivery(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	resp, reader, cancel := env.dialSSE(t, "user-sse-1")
	defer cancel()
	defer resp.Body.Close()

	// 等待注册完成
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond)

	// 向 SSE 用户发消息
	msg := makeGroupMessage("sender")
	msg.Content = "sse-hello"
	msg.Receiver = "user-sse-1"
	require.True(t, env.hub.SendToUserViaSSE("user-sse-1", msg), "SendToUserViaSSE 应成功")

	// 从 SSE 流读到消息
	data := reader.readEvent(2 * time.Second)
	require.NotEmpty(t, data, "应从 SSE 流读到 data 事件")
	assert.Contains(t, data, "sse-hello", "消息内容应匹配")
}

// ============================================================================
// 场景 2：客户端断开 → 服务端感知 → Unregister
// ============================================================================

func TestHandleSSEUpgrade_ClientDisconnect(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	_, _, cancel := env.dialSSE(t, "user-sse-2")
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond)

	// 客户端主动断开
	cancel()

	// 服务端写循环应通过 r.Context().Done() 感知并 Unregister
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 0 }, 3*time.Second, 50*time.Millisecond,
		"客户端断开后 SSE 连接应被清理")
}

// ============================================================================
// 场景 3：断开 → 重连同 user_id → 连接数维持 1 → 新消息仍可投递
// ============================================================================

func TestHandleSSEUpgrade_Reconnect(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	// 首次连接
	_, reader1, cancel1 := env.dialSSE(t, "user-sse-3")
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond)

	// 断开
	cancel1()
	reader1.readEvent(200 * time.Millisecond) // 触发 reader 退出
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 0 }, 3*time.Second, 50*time.Millisecond)

	// 重连
	_, reader2, cancel2 := env.dialSSE(t, "user-sse-3")
	defer cancel2()
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond,
		"重连后 SSE 客户端数应恢复为 1")

	// 新消息仍可投递
	msg := makeGroupMessage("sender")
	msg.Content = "after-reconnect"
	msg.Receiver = "user-sse-3"
	require.True(t, env.hub.SendToUserViaSSE("user-sse-3", msg))
	data := reader2.readEvent(2 * time.Second)
	assert.Contains(t, data, "after-reconnect")
}

// ============================================================================
// 场景 4：心跳（配置 SSEHeartbeat=50ms，静置后读到 : ping）
// ============================================================================

func TestHandleSSEUpgrade_Heartbeat(t *testing.T) {
	env := newSSEUpgradeEnv(t, func(c *wscconfig.WSC) {
		c.SSEHeartbeat = 50 * time.Millisecond
	})
	defer env.cleanup()

	resp, reader, cancel := env.dialSSE(t, "user-sse-4")
	defer cancel()
	defer resp.Body.Close()

	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond)

	// 读到心跳注释行（: ping）—— reader 已将 : 开头行转发到 events channel
	line := reader.readEvent(2 * time.Second)
	require.NotEmpty(t, line, "应读到心跳注释行")
	assert.True(t, strings.HasPrefix(line, ":"), "心跳应为注释行（: 开头），实际: %q", line)
}

// ============================================================================
// 场景 5：多设备广播（同 user_id 2 条 SSE，都收到）
// ============================================================================

func TestHandleSSEUpgrade_MultiDeviceBroadcast(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	// 同一 userID 两条 SSE 连接（传不同 device_id 避免同 clientID 替换）
	_, reader1, cancel1 := env.dialSSE(t, "user-sse-5", "device_id=dev1")
	defer cancel1()
	_, reader2, cancel2 := env.dialSSE(t, "user-sse-5", "device_id=dev2")
	defer cancel2()

	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 2 }, 2*time.Second, 20*time.Millisecond)

	// 发 P2P 消息给 user-sse-5，两个设备都应收到
	msg := makeGroupMessage("sender")
	msg.Content = "broadcast-test"
	msg.Receiver = "user-sse-5"
	env.hub.SendToUserViaSSE("user-sse-5", msg)

	data1 := reader1.readEvent(2 * time.Second)
	data2 := reader2.readEvent(2 * time.Second)
	assert.Contains(t, data1, "broadcast-test", "设备1 应收到")
	assert.Contains(t, data2, "broadcast-test", "设备2 应收到")
}

// ============================================================================
// 场景 6：namespace 隔离（ns1 连接收不到 ns2 消息，ns1 消息能收到）
// ============================================================================

func TestHandleSSEUpgrade_NamespaceIsolation(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	// 拨 SSE 连接，namespace=ns1（通过 query 参数传递）
	// 注意：extractClientAttributes 从 ClientAttributes.NamespaceSources 提取，默认包含 "namespace"
	_, reader, cancel := env.dialSSE(t, "user-sse-6", "namespace=ns1")
	defer cancel()

	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond)

	// 发 msg.Namespace=ns2 的消息，不应收到
	msg2 := makeGroupMessage("sender")
	msg2.Content = "ns2-msg"
	msg2.Receiver = "user-sse-6"
	msg2.Namespace = "ns2"
	env.hub.SendToUserViaSSE("user-sse-6", msg2)
	assert.Empty(t, reader.readEvent(300*time.Millisecond), "跨 namespace 消息不应收到")

	// 发 msg.Namespace=ns1 的消息，应收到
	msg1 := makeGroupMessage("sender")
	msg1.Content = "ns1-msg"
	msg1.Receiver = "user-sse-6"
	msg1.Namespace = "ns1"
	env.hub.SendToUserViaSSE("user-sse-6", msg1)
	data := reader.readEvent(2 * time.Second)
	assert.Contains(t, data, "ns1-msg", "同 namespace 消息应收到")
}

// ============================================================================
// 场景 7：老系统兼容——不传 token 传明文 user_id，连接成功
// ============================================================================

func TestHandleSSEUpgrade_LegacyNoToken(t *testing.T) {
	// 默认配置未启用 ConnectionToken，走明文参数提取
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	resp, reader, cancel := env.dialSSE(t, "user-legacy")
	defer cancel()
	defer resp.Body.Close()

	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond,
		"老系统不传 token 应能连接")

	// 验证归一化：AppID/Namespace 应为默认值
	clients := env.hub.GetSSEClients()
	require.Len(t, clients, 1)
	assert.Equal(t, constants.DefaultAppID, clients[0].AppID)
	assert.Equal(t, constants.DefaultNamespace, clients[0].Namespace)

	// 消息投递正常
	msg := makeGroupMessage("sender")
	msg.Content = "legacy-ok"
	msg.Receiver = "user-legacy"
	env.hub.SendToUserViaSSE("user-legacy", msg)
	data := reader.readEvent(2 * time.Second)
	assert.Contains(t, data, "legacy-ok")
}

// ============================================================================
// 场景 8：Hub 优雅关闭 → 写循环退出，SafeShutdown 不超时
// ============================================================================

func TestHandleSSEUpgrade_HubShutdown(t *testing.T) {
	env := newSSEUpgradeEnv(t)

	_, _, cancel := env.dialSSE(t, "user-shutdown")
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 2*time.Second, 20*time.Millisecond)

	// 必须先 cancel() 关闭客户端连接，触发服务端 r.Context().Done() 让写循环退出、handler 返回；
	// 若先 server.Close() 会死锁：server.Close() 阻塞等 handler 返回，而 handler 阻塞在写循环等 cancel()
	cancel()

	// SafeShutdown 触发 h.ctx.Done()，写循环退出（即便客户端未断开也能退出），handler 返回
	done := make(chan struct{})
	go func() {
		_ = env.hub.SafeShutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SafeShutdown 超时，写循环可能泄漏")
	}
	// 此时 handler 已返回（h.ctx.Done() 退出写循环），server.Close() 不再阻塞
	env.server.Close()
}

// ============================================================================
// 场景 9：goroutine 不泄漏（拨 + 断开 N 条后 goroutine 数回落）
// ============================================================================

func TestHandleSSEUpgrade_GoroutineLeak(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	// 先等 Hub 启动稳定
	time.Sleep(100 * time.Millisecond)
	baseGoroutines := runtimeNumGoroutine()

	const n = 5
	for i := 0; i < n; i++ {
		_, _, cancel := env.dialSSE(t, "user-leak")
		require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 1*time.Second, 20*time.Millisecond)
		cancel()
		require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 0 }, 3*time.Second, 50*time.Millisecond)
	}

	// 等待 goroutine 回收
	require.Eventually(t, func() bool {
		return runtimeNumGoroutine() <= baseGoroutines+2 // 容忍小幅波动
	}, 5*time.Second, 100*time.Millisecond, "goroutine 数应回落（base=%d current=%d）", baseGoroutines, runtimeNumGoroutine())
}

// ============================================================================
// 场景 10：SSEClients 指标真实反映连接数
// ============================================================================

func TestHandleSSEUpgrade_SSEClientsMetric(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	_, _, cancel1 := env.dialSSE(t, "user-metric-1")
	defer cancel1()
	_, _, cancel2 := env.dialSSE(t, "user-metric-2")
	defer cancel2()

	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 2 }, 2*time.Second, 20*time.Millisecond)
	assert.Equal(t, 2, env.hub.GetSSEClientCount(), "应有 2 个 SSE 客户端")

	// 通过 GetStats 验证（与监控层 gateway_wsc_sse_clients 指标同源）
	stats := env.hub.GetStats()
	assert.Equal(t, int64(2), stats.SSEClients, "GetStats().SSEClients 应为 2")

	cancel1()
	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == 1 }, 3*time.Second, 50*time.Millisecond)
	stats = env.hub.GetStats()
	assert.Equal(t, int64(1), stats.SSEClients, "断开一个后应为 1")
}

// ============================================================================
// 场景 11：并发多用户 SSE 消息投递（5 个用户各自收到）
// ============================================================================

func TestHandleSSEUpgrade_ConcurrentMultiUser(t *testing.T) {
	env := newSSEUpgradeEnv(t)
	defer env.cleanup()

	const n = 5
	readers := make([]*sseStreamReader, n)
	cancels := make([]func(), n)
	for i := 0; i < n; i++ {
		_, r, c := env.dialSSE(t, "user-concurrent-"+string(rune('A'+i)))
		readers[i] = r
		cancels[i] = c
	}
	defer func() {
		for _, c := range cancels {
			c()
		}
	}()

	require.Eventually(t, func() bool { return env.hub.GetSSEClientCount() == n }, 2*time.Second, 20*time.Millisecond)

	// 并发向每个用户发消息
	var wg sync.WaitGroup
	contents := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			userID := "user-concurrent-" + string(rune('A'+i))
			contents[i] = "msg-to-" + userID
			msg := makeGroupMessage("sender")
			msg.Content = contents[i]
			msg.Receiver = userID
			env.hub.SendToUserViaSSE(userID, msg)
		}(i)
	}
	wg.Wait()

	// 每个用户应收到自己的消息
	for i := 0; i < n; i++ {
		data := readers[i].readEvent(3 * time.Second)
		assert.Contains(t, data, contents[i], "用户 %d 应收到 %q", i, contents[i])
	}
}

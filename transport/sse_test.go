/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 15:56:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 18:22:00
 * @FilePath: \go-wsc\transport\sse_test.go
 * @Description: SSE 接入处理器测试 - 客户端构造 / 协议写 / 写循环退出 / 升级编排（含端到端）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/messaging"
	"github.com/kamalyes/go-wsc/models"
)

// newSSEClientAttrs 构造 SSE 客户端属性样例
func newSSEClientAttrs() *ClientAttributes {
	return &ClientAttributes{
		ClientID:  "sse-1",
		UserID:    "u-1",
		UserType:  models.UserTypeVisitor,
		AppID:     "app-1",
		Namespace: "ns-1",
		GroupIDs:  []string{"g1"},
	}
}

// newSSEHandler 快速构造 SSE 处理器（默认配置 + 独立 Registrar 桩）
func newSSEHandler(cfg *wscconfig.WSC, reg *fakeRegistrar) *SSEHandler {
	return NewSSEHandler(cfg, reg, NewUpgrader(cfg, reg)).WithNodeID("node-sse")
}

// ============================================================================
// 构造防御
// ============================================================================

// TestNewSSEHandlerPanic nil 配置 / nil 升级器 fail-fast
func TestNewSSEHandlerPanic(t *testing.T) {
	cfg := newUpgraderTestConfig()
	assert.Panics(t, func() { NewSSEHandler(nil, &fakeRegistrar{}, NewUpgrader(cfg, &fakeRegistrar{})) })
	assert.Panics(t, func() { NewSSEHandler(cfg, &fakeRegistrar{}, nil) })
}

// ============================================================================
// 客户端构造
// ============================================================================

// TestCreateSSEClientHeaders SSE 响应头 + 专用通道容量
func TestCreateSSEClientHeaders(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.SSEMessageBuffer = 32
	h := newSSEHandler(cfg, &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rec := httptest.NewRecorder()

	client, err := h.CreateSSEClient(req, rec, newSSEClientAttrs())
	require.NoError(t, err)

	// SSE 响应头四件套
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "keep-alive", rec.Header().Get("Connection"))
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))

	// 链式构造与专用通道
	assert.Equal(t, models.ConnectionTypeSSE, client.ConnectionType)
	assert.NotNil(t, client.SSEWriter)
	assert.NotNil(t, client.SSEFlusher)
	assert.Equal(t, 32, cap(client.SSEMessageCh), "SSEMessageCh 容量应取 SSEMessageBuffer 配置")
	assert.NotNil(t, client.SSECloseCh)
	// 节点与隔离维度
	assert.Equal(t, "node-sse", client.NodeID)
	assert.Equal(t, "app-1", client.AppID)
	assert.Equal(t, "ns-1", client.Namespace)
}

// TestCreateSSEClientBufferFallback SSEMessageBuffer 未配置 → 回退 MessageBufferSize
func TestCreateSSEClientBufferFallback(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.SSEMessageBuffer = 0
	cfg.MessageBufferSize = 128
	h := newSSEHandler(cfg, &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	client, err := h.CreateSSEClient(req, httptest.NewRecorder(), newSSEClientAttrs())
	require.NoError(t, err)
	assert.Equal(t, 128, cap(client.SSEMessageCh), "未配置 SSE 缓冲时应回退 MessageBufferSize")
}

// TestCreateSSEClientNotFlusher ResponseWriter 不支持 Flusher → 报错
func TestCreateSSEClientNotFlusher(t *testing.T) {
	h := newSSEHandler(newUpgraderTestConfig(), &fakeRegistrar{})

	// 只嵌入 http.ResponseWriter，不实现 Flusher
	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	_, err := h.CreateSSEClient(req, nonFlusher{httptest.NewRecorder()}, newSSEClientAttrs())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "streaming not supported")
}

// nonFlusher 剥离 Flusher 能力的 ResponseWriter 桩
type nonFlusher struct {
	http.ResponseWriter
}

// TestCreateSSEClientContext 连接级 ctx：SenderID 注入 + 请求 trace_id 透传
func TestCreateSSEClientContext(t *testing.T) {
	h := newSSEHandler(newUpgraderTestConfig(), &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	req = req.WithContext(logger.ContextWithTraceID(req.Context(), "trace-sse-1"))
	client, err := h.CreateSSEClient(req, httptest.NewRecorder(), newSSEClientAttrs())
	require.NoError(t, err)

	require.NotNil(t, client.Context)
	assert.Equal(t, "u-1", client.Context.Value(messaging.ContextKeySenderID))
	assert.Equal(t, "trace-sse-1", logger.ExtractTraceID(client.Context))
}

// ============================================================================
// 协议写
// ============================================================================

// TestWriteSSEEvent 消息事件写出：data: 前缀 + 事件结束符
func TestWriteSSEEvent(t *testing.T) {
	h := newSSEHandler(newUpgraderTestConfig(), &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rec := httptest.NewRecorder()
	client, err := h.CreateSSEClient(req, rec, newSSEClientAttrs())
	require.NoError(t, err)

	msg := models.NewHubMessage().SetSender("system").SetReceiver("u-1")
	require.NoError(t, h.writeSSEEvent(client, msg))

	body := rec.Body.String()
	assert.Contains(t, body, "data: ", "SSE 数据行应有 data: 前缀")
	assert.True(t, strings.HasSuffix(body, "\n\n"), "事件应以空行结束")
	assert.Contains(t, body, `"sender":"system"`, "消息 JSON 应写出")
}

// TestWriteSSEEventMarshalFailure 序列化失败：记 WARN 跳过单条，不断链
func TestWriteSSEEventMarshalFailure(t *testing.T) {
	h := newSSEHandler(newUpgraderTestConfig(), &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rec := httptest.NewRecorder()
	client, err := h.CreateSSEClient(req, rec, newSSEClientAttrs())
	require.NoError(t, err)

	// Data 塞入不可序列化的 channel → Marshal 失败
	msg := models.NewHubMessage()
	msg.Data["ch"] = make(chan int)

	assert.NoError(t, h.writeSSEEvent(client, msg), "序列化失败不应杀连接")
	assert.Empty(t, rec.Body.String(), "序列化失败不应写出任何字节")
}

// TestWriteSSEHeartbeat 心跳注释行写出
func TestWriteSSEHeartbeat(t *testing.T) {
	h := newSSEHandler(newUpgraderTestConfig(), &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rec := httptest.NewRecorder()
	client, err := h.CreateSSEClient(req, rec, newSSEClientAttrs())
	require.NoError(t, err)

	require.NoError(t, h.writeSSEHeartbeat(client))
	assert.Equal(t, ": ping\n\n", rec.Body.String(), "心跳应为注释行格式")
}

// ============================================================================
// 写循环退出路径
// ============================================================================

// TestWriteLoopExitOnChannelClose SSEMessageCh 关闭 → 退出 + 注销
func TestWriteLoopExitOnChannelClose(t *testing.T) {
	reg := &fakeRegistrar{}
	h := newSSEHandler(newUpgraderTestConfig(), reg)

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rec := httptest.NewRecorder()
	client, err := h.CreateSSEClient(req, rec, newSSEClientAttrs())
	require.NoError(t, err)

	close(client.SSEMessageCh)
	h.WriteLoop(client, req) // 应立即返回

	_, _, unreg, _ := reg.counts()
	assert.Equal(t, 1, unreg, "写循环退出应触发注销")
}

// TestWriteLoopExitOnCloseCh SSECloseCh 关闭（踢人/优雅关闭）→ 退出 + 注销
func TestWriteLoopExitOnCloseCh(t *testing.T) {
	reg := &fakeRegistrar{}
	h := newSSEHandler(newUpgraderTestConfig(), reg)

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	client, err := h.CreateSSEClient(req, httptest.NewRecorder(), newSSEClientAttrs())
	require.NoError(t, err)

	close(client.SSECloseCh)
	h.WriteLoop(client, req)

	_, _, unreg, _ := reg.counts()
	assert.Equal(t, 1, unreg)
}

// TestWriteLoopExitOnCtxCancel 客户端断开（请求 ctx 取消）→ 退出 + 注销
func TestWriteLoopExitOnCtxCancel(t *testing.T) {
	reg := &fakeRegistrar{}
	h := newSSEHandler(newUpgraderTestConfig(), reg)

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	client, err := h.CreateSSEClient(req, httptest.NewRecorder(), newSSEClientAttrs())
	require.NoError(t, err)

	cancel()
	h.WriteLoop(client, req)

	_, _, unreg, _ := reg.counts()
	assert.Equal(t, 1, unreg)
}

// TestWriteLoopConsumesMessage 消费一条消息后由 SSECloseCh 退出（写出语义 + 退出语义串联）
func TestWriteLoopConsumesMessage(t *testing.T) {
	reg := &fakeRegistrar{}
	h := newSSEHandler(newUpgraderTestConfig(), reg)

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	rec := httptest.NewRecorder()
	client, err := h.CreateSSEClient(req, rec, newSSEClientAttrs())
	require.NoError(t, err)

	// 先投递一条消息（带缓冲通道非阻塞），再关闭 SSECloseCh 触发退出。
	// select 存在竞态（消息/关闭都可能先选中），故此用例只断言退出与注销语义；
	// 若消息先被消费则写出 body，若关闭先选中则 body 为空——两者皆为合法行为
	client.SSEMessageCh <- models.NewHubMessage().SetSender("system")
	close(client.SSECloseCh)
	h.WriteLoop(client, req)

	_, _, unreg, _ := reg.counts()
	assert.Equal(t, 1, unreg, "写循环退出应触发注销")
}

// ============================================================================
// 升级编排
// ============================================================================

// TestHandleSSEUpgradeHealthCheck 健康检查：200 + JSON（不升级协议、不注册）
func TestHandleSSEUpgradeHealthCheck(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.HealthCheck.Enabled = true
	reg := &fakeRegistrar{}
	h := newSSEHandler(cfg, reg)

	req := httptest.NewRequest(http.MethodGet, "/sse?health=true", nil)
	rec := httptest.NewRecorder()
	h.HandleSSEUpgrade(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `"status":"ok"`)
	assert.Contains(t, rec.Body.String(), "node-sse")

	_, syncReg, _, _ := reg.counts()
	assert.Zero(t, syncReg, "健康检查不应注册客户端")
}

// TestHandleSSEUpgradeRejected 连接验证失败 → 拒绝（无 SSE 响应头）
func TestHandleSSEUpgradeRejected(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.ConnectionValidation.Enabled = true
	cfg.ConnectionValidation.RequireUserID = true
	reg := &fakeRegistrar{}
	h := newSSEHandler(cfg, reg)

	req := httptest.NewRequest(http.MethodGet, "/sse", nil) // 无 user_id
	rec := httptest.NewRecorder()
	h.HandleSSEUpgrade(rec, req)

	assert.NotEqual(t, "text/event-stream", rec.Header().Get("Content-Type"), "被拒连接不应设置 SSE 头")
	_, syncReg, _, _ := reg.counts()
	assert.Zero(t, syncReg)
}

// TestHandleSSEUpgradeShutdown 编排层关闭中 → 拒绝新连接
func TestHandleSSEUpgradeShutdown(t *testing.T) {
	cfg := newUpgraderTestConfig()
	reg := &fakeRegistrar{}
	reg.shutdown.Store(true)
	h := newSSEHandler(cfg, reg)

	req := httptest.NewRequest(http.MethodGet, "/sse?user_id=u-off&user_type=visitor", nil)
	rec := httptest.NewRecorder()
	h.HandleSSEUpgrade(rec, req)

	_, syncReg, _, _ := reg.counts()
	assert.Zero(t, syncReg, "关闭中不应注册")
}

// TestHandleSSEUpgradeSyncRegister 成功链路：SSE 头 → 同步注册 → 写循环退出后注销
func TestHandleSSEUpgradeSyncRegister(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.ConnectionValidation.Enabled = true
	reg := &fakeRegistrar{}
	h := newSSEHandler(cfg, reg)

	// 预取消的请求 ctx：HandleSSEUpgrade 完成同步注册后写循环立即退出
	req := httptest.NewRequest(http.MethodGet, "/sse?user_id=u-sse&user_type=visitor", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	h.HandleSSEUpgrade(rec, req)

	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))

	_, syncReg, unreg, _ := reg.counts()
	assert.Equal(t, 1, syncReg, "SSE 应同步注册（注册完成才进写循环）")
	assert.Equal(t, 1, unreg, "写循环退出应触发注销")
}

// TestSSEUpgradeEndToEnd 端到端：注册钩子投递消息 → 客户端流式读到 data: 事件 → 断开注销
func TestSSEUpgradeEndToEnd(t *testing.T) {
	cfg := newUpgraderTestConfig()
	reg := &fakeRegistrar{}
	h := newSSEHandler(cfg, reg)

	// 注册完成后由测试侧向专用通道投递一条消息（模拟消息域推送）
	reg.onRegisterSync = func(client *models.Client) {
		client.SSEMessageCh <- models.NewHubMessage().
			SetSender("system").
			SetReceiver(client.UserID).
			SetContent("hello-sse")
	}

	srv := httptest.NewServer(http.HandlerFunc(h.HandleSSEUpgrade))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sse?user_id=u-e2e&user_type=visitor")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	// 流式读取：首行应为 data: 前缀的事件数据
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	assert.Contains(t, line, "data: ", "端到端应读到 data: 事件行")
	assert.Contains(t, line, "hello-sse")

	// 断开连接 → 写循环退出 → 注销
	resp.Body.Close()
	require.Eventually(t, func() bool {
		_, _, unreg, _ := reg.counts()
		return unreg == 1
	}, 2*time.Second, 10*time.Millisecond, "客户端断开后应触发注销")
}

// TestSSEHandlerWait 等待在途写循环（优雅关闭语义）
func TestSSEHandlerWait(t *testing.T) {
	cfg := newUpgraderTestConfig()
	reg := &fakeRegistrar{}
	h := newSSEHandler(cfg, reg)

	req := httptest.NewRequest(http.MethodGet, "/sse", nil)
	client, err := h.CreateSSEClient(req, httptest.NewRecorder(), newSSEClientAttrs())
	require.NoError(t, err)

	// 写循环阻塞运行中，另起 goroutine 稍后关闭触发退出
	done := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		close(client.SSECloseCh)
	}()
	go func() {
		h.WriteLoop(client, req)
		close(done)
	}()

	h.Wait() // 不死锁即通过（写循环已在途）
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("写循环应在 SSECloseCh 关闭后退出")
	}
}

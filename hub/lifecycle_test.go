/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 03:55:50
 * @FilePath: \go-wsc\hub\lifecycle_test.go
 * @Description:
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/repository"
)

// ============================================================================
// fakeOnlineStatusRepo — 最小化 fake，仅实现 CleanupExpired
// ============================================================================

type fakeOnlineStatusRepo struct {
	repository.OnlineStatusRepository // 嵌入 nil interface，仅覆盖 CleanupExpired
	cleanupExpiredCount               int64
	cleanupExpiredErr                 error
}

func (f *fakeOnlineStatusRepo) CleanupExpired(_ context.Context, _ string) (int64, error) {
	return f.cleanupExpiredCount, f.cleanupExpiredErr
}

// ============================================================================
// cleanupExpiredMessageRecords
// ============================================================================

func TestCleanupExpiredMessageRecords_NilRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// messageRecordRepo 为 nil → 直接 return，不 panic
	assert.NotPanics(t, func() {
		hub.cleanupExpiredMessageRecords()
	})
}

func TestCleanupExpiredMessageRecords_SuccessWithDeleted(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{deleteExpiredCount: 5}
	hub.SetMessageRecordRepository(repo)

	assert.NotPanics(t, func() {
		hub.cleanupExpiredMessageRecords()
	})
}

func TestCleanupExpiredMessageRecords_SuccessWithZeroDeleted(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{deleteExpiredCount: 0}
	hub.SetMessageRecordRepository(repo)

	assert.NotPanics(t, func() {
		hub.cleanupExpiredMessageRecords()
	})
}

func TestCleanupExpiredMessageRecords_Error(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{deleteExpiredErr: assertError("db error")}
	hub.SetMessageRecordRepository(repo)

	assert.NotPanics(t, func() {
		hub.cleanupExpiredMessageRecords()
	})
}

// ============================================================================
// cleanupExpiredOnlineStatus
// ============================================================================

func TestCleanupExpiredOnlineStatus_SuccessWithCleaned(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeOnlineStatusRepo{cleanupExpiredCount: 3}
	hub.SetOnlineStatusRepository(repo)

	assert.NotPanics(t, func() {
		hub.cleanupExpiredOnlineStatus()
	})
}

func TestCleanupExpiredOnlineStatus_SuccessWithZeroCleaned(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeOnlineStatusRepo{cleanupExpiredCount: 0}
	hub.SetOnlineStatusRepository(repo)

	assert.NotPanics(t, func() {
		hub.cleanupExpiredOnlineStatus()
	})
}

func TestCleanupExpiredOnlineStatus_Error(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeOnlineStatusRepo{cleanupExpiredErr: assertError("redis error")}
	hub.SetOnlineStatusRepository(repo)

	assert.NotPanics(t, func() {
		hub.cleanupExpiredOnlineStatus()
	})
}

// ============================================================================
// cleanupExpiredAck
// ============================================================================

func TestCleanupExpiredAck_NilAckManager(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// ackManager 为 nil → 直接 return
	original := hub.ackManager
	hub.ackManager = nil
	defer func() { hub.ackManager = original }()

	assert.NotPanics(t, func() {
		hub.cleanupExpiredAck()
	})
}

func TestCleanupExpiredAck_NoExpired(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// ackManager 已由 NewHub 创建，CleanupExpired 返回 0
	assert.NotPanics(t, func() {
		hub.cleanupExpiredAck()
	})
}

func TestCleanupExpiredAck_WithExpired(t *testing.T) {
	t.Parallel()
	// 极短 AckTimeout 让 ACK 快速过期
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	config.AckTimeout = 10 * time.Millisecond
	hub := NewHub(config)
	defer hub.Shutdown()

	// 添加一个需要 ACK 的消息（会进入 ackManager 等待表）
	msg := NewHubMessage()
	msg.SetID("ack-expire-test")
	hub.ackManager.AddPendingMessage(msg)

	// 等待 ACK 超时
	time.Sleep(50 * time.Millisecond)

	// cleanupExpiredAck 应清理过期的 ACK 记录
	assert.NotPanics(t, func() {
		hub.cleanupExpiredAck()
	})
}

// ============================================================================
// reportPerformanceMetrics
// ============================================================================

func TestReportPerformanceMetrics_NilStatsRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// statsRepo 为 nil → 直接 return
	assert.NotPanics(t, func() {
		hub.reportPerformanceMetrics()
	})
}

func TestReportPerformanceMetrics_StatsError(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeHubStatsRepository{nodeStatsErr: assertError("redis error")}
	hub.SetHubStatsRepository(repo)

	assert.NotPanics(t, func() {
		hub.reportPerformanceMetrics()
	})
}

func TestReportPerformanceMetrics_Success(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeHubStatsRepository{
		nodeStats: &repository.NodeStats{
			NodeID:            hub.nodeID,
			TotalConnections:  100,
			ActiveConnections: 50,
			MessagesSent:      200,
			BroadcastsSent:    30,
			Uptime:            3600,
		},
	}
	hub.SetHubStatsRepository(repo)

	assert.NotPanics(t, func() {
		hub.reportPerformanceMetrics()
	})
}

// ============================================================================
// flushStatsCounters
// ============================================================================

func TestFlushStatsCounters_NilStatsRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// statsRepo 为 nil → 直接 return
	assert.NotPanics(t, func() {
		hub.flushStatsCounters()
	})
}

func TestFlushStatsCounters_WithMsgsAndBroadcasts(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeHubStatsRepository{}
	hub.SetHubStatsRepository(repo)

	// 累积消息和广播计数
	hub.msgSentCount.Add(10)
	hub.broadcastSentCount.Add(5)

	assert.NotPanics(t, func() {
		hub.flushStatsCounters()
	})

	// flush 后计数器应归零
	assert.Equal(t, int64(0), hub.msgSentCount.Load())
	assert.Equal(t, int64(0), hub.broadcastSentCount.Load())
}

func TestFlushStatsCounters_ZeroCounts(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeHubStatsRepository{}
	hub.SetHubStatsRepository(repo)

	// 计数为 0 → 不调用 IncrementXxx
	assert.NotPanics(t, func() {
		hub.flushStatsCounters()
	})
}

// ============================================================================
// sendClientRegisteredMessage
// ============================================================================

func TestSendClientRegisteredMessage_WithCustomHeaders(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	// 启用自定义响应头
	config.ResponseHeaders.Enabled = true
	config.ResponseHeaders.SendRegisteredMessage = true
	config.ResponseHeaders.RegisteredMessageContent = "welcome"
	config.ResponseHeaders.CustomHeaders = map[string]string{
		"server": "go-wsc-test",
		"ver":    "1.0",
	}

	hub := NewHub(config)
	defer hub.Shutdown()

	client := makeTestClient("c-reg", "u-reg")
	hub.shardedRegistry.AddClient(client)

	// 调用 sendClientRegisteredMessage
	hub.sendClientRegisteredMessage(client)

	// 验证客户端收到消息
	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "客户端应收到注册确认消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：客户端未收到注册确认消息")
	}
}

func TestSendClientRegisteredMessage_DefaultContent(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)

	hub := NewHub(config)
	defer hub.Shutdown()

	client := makeTestClient("c-reg2", "u-reg2")
	hub.shardedRegistry.AddClient(client)

	// ResponseHeaders.Enabled = false → 不添加自定义头
	hub.sendClientRegisteredMessage(client)

	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(1 * time.Second):
		t.Fatal("超时：客户端未收到注册确认消息")
	}
}

// ============================================================================
// handleHealthCheck
// ============================================================================

func TestHandleHealthCheck_SendResponseAndClose(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default()
	config.HealthCheck.Enabled = true
	config.HealthCheck.CloseImmediately = true
	config.HealthCheck.SendResponseMessage = true

	hub := NewHub(config)
	defer hub.Shutdown()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWebSocketUpgrade))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?health=true"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "WebSocket 连接失败")
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	// 读取健康检查响应消息
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err, "应收到健康检查响应消息")
	assert.NotEmpty(t, msg, "响应消息不应为空")

	// CloseImmediately=true → 服务端关闭连接，ReadMessage 应返回错误
	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "CloseImmediately=true 时连接应被服务端关闭")
}

func TestHandleHealthCheck_NoResponseKeepOpen(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default()
	config.HealthCheck.Enabled = true
	config.HealthCheck.CloseImmediately = false
	config.HealthCheck.SendResponseMessage = false

	hub := NewHub(config)
	defer hub.Shutdown()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWebSocketUpgrade))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?health=true"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "WebSocket 连接失败")
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	// SendResponseMessage=false → 设置短超时读取，不应收到消息
	conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err = conn.ReadMessage()
	assert.Error(t, err, "SendResponseMessage=false 时不应收到消息")
}

func TestHandleHealthCheck_SendResponseKeepOpen(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default()
	config.HealthCheck.Enabled = true
	config.HealthCheck.CloseImmediately = false
	config.HealthCheck.SendResponseMessage = true

	hub := NewHub(config)
	defer hub.Shutdown()

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWebSocketUpgrade))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?health=true"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	require.NoError(t, err, "WebSocket 连接失败")
	defer conn.Close()

	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	// 读取健康检查响应消息
	_, msg, err := conn.ReadMessage()
	require.NoError(t, err, "应收到健康检查响应消息")
	assert.NotEmpty(t, msg)
}

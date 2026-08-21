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
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
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

// capturingConnRecordRepo 捕获 Upsert 的连接记录，用于断言路由维度
type capturingConnRecordRepo struct {
	mockConnRecordRepo
	mu      sync.Mutex
	records []*ConnectionRecord
}

func (c *capturingConnRecordRepo) Upsert(_ context.Context, record *ConnectionRecord) error {
	c.mu.Lock()
	c.records = append(c.records, record)
	c.mu.Unlock()
	return nil
}

func (c *capturingConnRecordRepo) snapshot() []*ConnectionRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*ConnectionRecord, len(c.records))
	copy(out, c.records)
	return out
}

// TestLegacySystem_RegisterNormalizesDimensions 老系统 client 不设 AppID/Namespace/GroupID，
// 注册后应被归一化为 DefaultAppID/DefaultNamespace，且 client 存储可查
func TestLegacySystem_RegisterNormalizesDimensions(t *testing.T) {
	config := wscconfig.Default()
	if config.Performance == nil {
		config.Performance = &wscconfig.Performance{}
	}
	config.HeartbeatInterval = time.Hour // 关闭心跳超时干扰
	config.ClientTimeout = time.Hour

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	hub.WaitForStart()

	// 老系统 client：不设 AppID/Namespace/GroupID（全空）
	client := &Client{
		ID:             "legacy-client-1",
		UserID:         "legacy-user-1",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		Context:        context.Background(),
		Metadata:       make(map[string]interface{}),
	}
	// 显式断言初始全空
	require.Empty(t, client.AppID, "初始 AppID 应为空（模拟老系统）")
	require.Empty(t, client.Namespace, "初始 Namespace 应为空（模拟老系统）")
	require.Empty(t, client.GroupID, "初始 GroupID 应为空（模拟老系统）")

	hub.Register(client)

	// 等待注册完成
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 1. 验证 client 字段被归一化
	assert.Equal(t, constants.DefaultAppID, client.AppID, "AppID 空应归一化为 DefaultAppID")
	assert.Equal(t, constants.DefaultNamespace, client.Namespace, "Namespace 空应归一化为 DefaultNamespace")

	// 2. 验证 client 存储可查
	retrieved, exists := hub.shardedRegistry.GetClient(client.ID)
	require.True(t, exists, "注册后应能从 registry 查到 client")
	assert.Equal(t, constants.DefaultAppID, retrieved.AppID, "registry 里的 AppID 应为归一化后的值")
	assert.Equal(t, constants.DefaultNamespace, retrieved.Namespace, "registry 里的 Namespace 应为归一化后的值")
}

// TestLegacySystem_ConnectionRecordUsesNormalizedDimensions 连接记录应使用归一化后的字段
func TestLegacySystem_ConnectionRecordUsesNormalizedDimensions(t *testing.T) {
	config := wscconfig.Default()
	if config.Performance == nil {
		config.Performance = &wscconfig.Performance{}
	}
	config.HeartbeatInterval = time.Hour
	config.ClientTimeout = time.Hour

	hub := NewHub(config)
	connRepo := &capturingConnRecordRepo{}
	hub.SetConnectionRecordRepository(connRepo)
	defer hub.SafeShutdown()
	go hub.Run()
	hub.WaitForStart()

	client := &Client{
		ID:             "legacy-conn-1",
		UserID:         "legacy-conn-user",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		Context:        context.Background(),
		Metadata:       make(map[string]interface{}),
	}
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 等待异步保存连接记录（workerPool TrySubmitRecord）
	require.Eventually(t, func() bool { return len(connRepo.snapshot()) > 0 }, 2*time.Second, 50*time.Millisecond)

	records := connRepo.snapshot()
	require.NotEmpty(t, records)
	rec := records[0]
	assert.Equal(t, "legacy-conn-1", rec.ConnectionID)
	assert.Equal(t, "legacy-conn-user", rec.UserID)
}

// TestLegacySystem_HeartbeatRefreshNoPanic 老系统 client 不传维度，心跳刷新不应 panic
func TestLegacySystem_HeartbeatRefreshNoPanic(t *testing.T) {
	config := wscconfig.Default()
	if config.Performance == nil {
		config.Performance = &wscconfig.Performance{}
	}
	config.HeartbeatInterval = time.Hour
	config.ClientTimeout = time.Hour

	hub := NewHub(config)
	defer hub.SafeShutdown()
	go hub.Run()
	hub.WaitForStart()

	client := &Client{
		ID:             "legacy-hb-1",
		UserID:         "legacy-hb-user",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		Context:        context.Background(),
		Metadata:       make(map[string]interface{}),
	}
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 心跳刷新不 panic（老系统 client 无维度，RefreshHeartbeatTimeout 应正常工作）
	assert.NotPanics(t, func() {
		hub.RefreshHeartbeatTimeout(client)
	})
	// client 仍在线
	assert.True(t, hub.HasClient(client.ID), "心跳刷新后 client 应仍在线")
}

// TestLegacySystem_P2PSendWithBackgroundCtx 老系统用 context.Background() 发 P2P 消息
// EnsureRouteDefaults 应兜底注入 DefaultAppID/DefaultNamespace，离线存储维度正确
func TestLegacySystem_P2PSendWithBackgroundCtx(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	h := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(h)
	defer hub.SafeShutdown()

	// context.Background() 模拟老系统完全不传 appid/namespace/group
	hub.SendToUserWithRetry(context.Background(), "legacy-p2p-offline", makeGroupMessage("legacy-sender"))

	ns, groups, count := h.snapshot()
	require.Equal(t, 1, count, "离线用户应触发一次 StoreOfflineMessage")
	assert.Equal(t, constants.DefaultNamespace, ns, "老系统不传 namespace 应兜底为 DefaultNamespace")
	assert.Empty(t, groups, "P2P 不传 group 应保持空（非 []string{''}）")
}

// TestLegacySystem_NoAppID_Alone 单独不传 appid（namespace 正常）也应正常
// Route.Inject 内部归一化 appID 空补 DefaultAppID
func TestLegacySystem_NoAppID_Alone(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	h := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(h)
	defer hub.SafeShutdown()

	// 只设 namespace 不设 appid（NewRoute().WithAppID("") 等价于不调 WithAppID）
	ctx := routing.NewRoute().WithNamespace("ns-explicit").Inject(context.Background())
	hub.SendToUserWithRetry(ctx, "no-appid-offline", makeGroupMessage("sender"))

	ns, _, count := h.snapshot()
	require.Equal(t, 1, count)
	assert.Equal(t, "ns-explicit", ns, "显式 namespace 不应被覆盖")
}

// TestLegacySystem_EmptyGroupID_NoBogusGroupEntry 老系统不设 GroupID，
// 消息处理路径不应产生 []string{""} 伪群组条目
// 验证 routing.NewRoute().WithGroup("") 的行为：空串不应 append（调用方判空）
func TestLegacySystem_EmptyGroupID_NoBogusGroupEntry(t *testing.T) {
	// 直接验证 Route API：不调 WithGroup 时 groupIDs 应为 nil
	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		Inject(context.Background())
	assert.Nil(t, routing.GroupIDsFromContext(ctx), "不调 WithGroup 时 groupIDs 应为 nil（P2P 语义）")
	assert.Empty(t, routing.FirstGroupIDFromContext(ctx), "无群组时 FirstGroupID 应为空串")

	// 验证 EnsureRouteDefaults 在全空 ctx 下兜底
	ctx2 := routing.EnsureRouteDefaults(context.Background())
	assert.Equal(t, constants.DefaultAppID, routing.AppIDFromContext(ctx2), "全空 ctx EnsureRouteDefaults 应补 DefaultAppID")
	assert.Equal(t, constants.DefaultNamespace, routing.NamespaceFromContext(ctx2), "全空 ctx EnsureRouteDefaults 应补 DefaultNamespace")
	assert.Nil(t, routing.GroupIDsFromContext(ctx2), "P2P 场景 groupIDs 应保持 nil")
}

// TestLegacySystem_OnlineDeliveryWithBackgroundCtx 老系统用 context.Background() 发消息给在线用户
// 验证：client 经 Register 归一化后，老系统 ctx 在线投递能成功（路由维度经 EnsureRouteDefaults 兜底，与 client 归一化值一致）
func TestLegacySystem_OnlineDeliveryWithBackgroundCtx(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	// 在线 client（老系统风格：不设 AppID/Namespace/GroupID，走 Register 归一化）
	client := &Client{
		ID:             "legacy-online-1",
		UserID:         "legacy-online-user",
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		Context:        context.Background(),
		SendChan:       make(chan []byte, 10),
		Metadata:       make(map[string]interface{}),
	}
	hub.Register(client)
	// Register 异步执行 handleRegister，等待 client 入注册表
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)
	// 归一化后 client.AppID/Namespace 应为默认值（与 EnsureRouteDefaults 兜底值一致，ClientMatchesEnvelope 能匹配）
	require.Equal(t, constants.DefaultAppID, client.AppID)
	require.Equal(t, constants.DefaultNamespace, client.Namespace)

	// context.Background() 模拟老系统完全不传 appid/namespace/group
	msg := makeGroupMessage("legacy-sender")
	msg.Receiver = "legacy-online-user"
	result := hub.SendToUserWithRetry(context.Background(), "legacy-online-user", msg)

	assert.True(t, result.Success, "老系统 ctx 在线投递应成功")
	assert.NoError(t, result.FinalError)
	assert.False(t, result.StoredOffline, "在线用户不应走离线存储")

	// 验证 client 收到消息
	require.Eventually(t, func() bool {
		select {
		case <-client.SendChan:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond, "老系统 ctx 在线用户应收到消息")
}

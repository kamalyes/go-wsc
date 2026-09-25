/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 18:23:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 18:23:00
 * @FilePath: \go-wsc\connection\heartbeat_test.go
 * @Description: 连接域心跳管理器测试 - 回调链时序 / 前置拦截 / 时间轮超时注销 / SSE 兜底扫描
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// fakeHeartbeatHost 心跳端口测试桩：记录端口调用并回放注册的回调
// 记录字段由 mu 保护——超时任务在时间轮 goroutine 异步执行，
// 与测试 goroutine 的断言/轮询读并发（-race 检测要求）
type fakeHeartbeatHost struct {
	mu           sync.Mutex
	unregistered []*models.Client
	tracked      []*models.Client
	renewed      []*models.Client

	before  func(*models.Client) bool
	report  func(*models.Client)
	after   func(*models.Client)
	timeout func(clientID, userID string, lastHeartbeat time.Time)
}

func (f *fakeHeartbeatHost) Context() context.Context { return context.Background() }

// GetLogger 返回 nil（构造器内部兜底默认日志器）
func (f *fakeHeartbeatHost) GetLogger() spi.Logger { return nil }

func (f *fakeHeartbeatHost) Unregister(client *models.Client) {
	f.mu.Lock()
	f.unregistered = append(f.unregistered, client)
	f.mu.Unlock()
}

// unregisteredCount 线程安全读取注销记录数（时间轮 goroutine 写、测试 goroutine 轮询读）
func (f *fakeHeartbeatHost) unregisteredCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.unregistered)
}

func (f *fakeHeartbeatHost) TrackHeartbeatStats(client *models.Client) {
	f.mu.Lock()
	f.tracked = append(f.tracked, client)
	f.mu.Unlock()
}

func (f *fakeHeartbeatHost) EnqueueHeartbeatRenew(client *models.Client) {
	f.mu.Lock()
	f.renewed = append(f.renewed, client)
	f.mu.Unlock()
}

func (f *fakeHeartbeatHost) GetBeforeHeartbeatCallback() func(*models.Client) bool {
	return f.before
}

func (f *fakeHeartbeatHost) GetHeartbeatReportCallback() func(*models.Client) {
	return f.report
}

func (f *fakeHeartbeatHost) GetAfterHeartbeatCallback() func(*models.Client) {
	return f.after
}

func (f *fakeHeartbeatHost) GetHeartbeatTimeoutCallback() func(string, string, time.Time) {
	return f.timeout
}

// newHeartbeatManagerFixture 构造管理器与端口桩（clientTimeout 由用例覆盖）
func newHeartbeatManagerFixture(t *testing.T, registry *ShardedRegistry, clientTimeout time.Duration) (*HeartbeatManager, *fakeHeartbeatHost) {
	t.Helper()
	host := &fakeHeartbeatHost{}
	manager := NewHeartbeatManager(host, registry, clientTimeout)
	return manager, host
}

// TestHeartbeatHandleCallbackChain 心跳链路：前置 → 时间戳续期 → Redis 续期入队 → 上报 → 后置 → 统计
func TestHeartbeatHandleCallbackChain(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newHeartbeatManagerFixture(t, registry, time.Minute)

	var order []string
	host.before = func(*models.Client) bool { order = append(order, "before"); return true }
	host.report = func(*models.Client) { order = append(order, "report") }
	host.after = func(*models.Client) { order = append(order, "after") }

	client := models.NewClient("hb-1", "u-1000", models.UserTypeCustomer)
	client.Context = context.Background()
	before := time.Now().Add(-time.Second)
	client.SetLastHeartbeat(before)
	client.SetLastSeen(before)

	manager.Handle(client)

	assert.Equal(t, []string{"before", "report", "after"}, order)
	require.Len(t, host.renewed, 1, "心跳应入队 Redis 续期")
	require.Len(t, host.tracked, 1, "心跳应投递统计追踪")
	assert.True(t, client.GetLastHeartbeat().After(before), "内存心跳时间戳应被刷新")
	assert.True(t, client.GetLastSeen().After(before), "内存活跃时间戳应被刷新")
}

// TestHeartbeatBeforeIntercepts 前置回调返回 false：整条链路跳过（拦截语义）
func TestHeartbeatBeforeIntercepts(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newHeartbeatManagerFixture(t, registry, time.Minute)

	host.before = func(*models.Client) bool { return false }

	client := models.NewClient("hb-2", "u-2000", models.UserTypeCustomer)
	client.Context = context.Background()

	manager.Handle(client)

	assert.Empty(t, host.renewed, "拦截后不应入队 Redis 续期")
	assert.Empty(t, host.tracked, "拦截后不应投递统计追踪")
}

// TestHeartbeatClosedClientIgnored 已断开客户端心跳：入口直接忽略
func TestHeartbeatClosedClientIgnored(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newHeartbeatManagerFixture(t, registry, time.Minute)

	client := models.NewClient("hb-3", "u-3000", models.UserTypeCustomer)
	client.Context = context.Background()
	client.MarkClosed()

	manager.Handle(client)
	manager.Handle(nil)

	assert.Empty(t, host.renewed)
	assert.Empty(t, host.tracked)
}

// TestHeartbeatTimeoutTaskUnregisters 时间轮超时：触发超时回调并异步注销
func TestHeartbeatTimeoutTaskUnregisters(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newHeartbeatManagerFixture(t, registry, 30*time.Millisecond)

	// timeoutClientID 由 mu 保护：超时回调在时间轮 goroutine 写、Eventually 在测试 goroutine 轮询读
	var mu sync.Mutex
	timeoutClientID := ""
	host.timeout = func(clientID, _ string, _ time.Time) {
		mu.Lock()
		timeoutClientID = clientID
		mu.Unlock()
	}

	client := models.NewClient("hb-4", "u-4000", models.UserTypeCustomer)
	client.Context = context.Background()
	manager.ScheduleTimeout(client)
	defer manager.CancelTimeout(client.ID)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return host.unregisteredCount() == 1 && timeoutClientID == client.ID
	}, 3*time.Second, 20*time.Millisecond, "超时任务应触发回调并注销客户端")
}

// TestHeartbeatTimeoutClosedClientSkips 超时触发时客户端已正常断开：跳过注销
func TestHeartbeatTimeoutClosedClientSkips(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newHeartbeatManagerFixture(t, registry, 30*time.Millisecond)

	client := models.NewClient("hb-5", "u-5000", models.UserTypeCustomer)
	client.Context = context.Background()
	manager.ScheduleTimeout(client)
	client.MarkClosed()

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, host.unregisteredCount(), "已关闭客户端的超时任务应跳过注销")
	manager.CancelTimeout(client.ID)
}

// TestHeartbeatScanSSETimeouts SSE 兜底扫描：仅超时客户端被注销并触发回调
func TestHeartbeatScanSSETimeouts(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newHeartbeatManagerFixture(t, registry, time.Minute)

	var timeoutIDs []string
	host.timeout = func(clientID, _ string, _ time.Time) { timeoutIDs = append(timeoutIDs, clientID) }

	stale := models.NewClient("sse-stale", "u-6000", models.UserTypeCustomer)
	stale.Context = context.Background()
	stale.ConnectionType = models.ConnectionTypeSSE
	stale.SetLastSeen(time.Now().Add(-2 * time.Hour)) // 超时 2 小时未活跃

	fresh := models.NewClient("sse-fresh", "u-7000", models.UserTypeCustomer)
	fresh.Context = context.Background()
	fresh.ConnectionType = models.ConnectionTypeSSE
	fresh.SetLastSeen(time.Now()) // 刚活跃

	registry.AddClient(stale)
	registry.AddClient(fresh)

	manager.ScanSSETimeouts()

	require.Len(t, host.unregistered, 1, "仅超时的 SSE 客户端应被注销")
	assert.Equal(t, stale.ID, host.unregistered[0].ID)
	assert.Equal(t, []string{stale.ID}, timeoutIDs, "超时客户端应触发超时回调")
}

// TestHeartbeatSSEBypassesWheel SSE 客户端不进时间轮（由扫描兜底）
func TestHeartbeatSSEBypassesWheel(t *testing.T) {
	registry := NewShardedRegistry(false, false, RegistryCapacity{TotalClients: 64})
	manager, host := newHeartbeatManagerFixture(t, registry, 30*time.Millisecond)

	sse := models.NewClient("sse-1", "u-8000", models.UserTypeCustomer)
	sse.Context = context.Background()
	sse.ConnectionType = models.ConnectionTypeSSE

	// ScheduleTimeout 对 SSE 客户端是 no-op：短暂等待不应触发任何注销
	manager.ScheduleTimeout(sse)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 0, host.unregisteredCount(), "SSE 客户端不应进入时间轮超时管理")
}

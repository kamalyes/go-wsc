/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-06-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-06-25 10:56:20
 * @FilePath: \go-wsc\hub\stats_test.go
 * @Description: Hub 监控核心路径白盒单元测试
 *   - stats.go: shouldTrackUserStats / track* / logClientConnection / logWithClient / syncOnlineStatus
 *   - lifecycle.go: syncClientStats / FlushStats / cleanupExpiredAck / reportPerformanceMetrics
 *                   WaitForStartWithTimeout / GetMaxConnectionsPerNode
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 测试用 mock：ConnectionRecordRepository
// 仅记录 BatchIncrementStats / BatchUpdateHeartbeats / AddError 调用，
// 其余方法 no-op，用于验证 track* 函数的统计计数路径
// ============================================================================

type fakeConnRecordRepo struct {
	mu               sync.Mutex
	incrementEntries []*repository.StatsIncrementEntry
	heartbeatEntries []*repository.HeartbeatUpdateEntry
	addErrorCount    atomic.Int64
}

func (f *fakeConnRecordRepo) BatchIncrementStats(_ context.Context, entries []*repository.StatsIncrementEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.incrementEntries = append(f.incrementEntries, entries...)
	return nil
}

func (f *fakeConnRecordRepo) BatchUpdateHeartbeats(_ context.Context, entries []*repository.HeartbeatUpdateEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeatEntries = append(f.heartbeatEntries, entries...)
	return nil
}

func (f *fakeConnRecordRepo) AddError(_ context.Context, _ string, _ error) error {
	f.addErrorCount.Add(1)
	return nil
}

func (f *fakeConnRecordRepo) getIncrementEntries() []*repository.StatsIncrementEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]*repository.StatsIncrementEntry, len(f.incrementEntries))
	copy(cp, f.incrementEntries)
	return cp
}

func (f *fakeConnRecordRepo) getHeartbeatEntries() []*repository.HeartbeatUpdateEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]*repository.HeartbeatUpdateEntry, len(f.heartbeatEntries))
	copy(cp, f.heartbeatEntries)
	return cp
}

// 其余方法 no-op
func (f *fakeConnRecordRepo) Upsert(context.Context, *ConnectionRecord) error { return nil }
func (f *fakeConnRecordRepo) MarkDisconnected(context.Context, string, DisconnectReason, int, string) error {
	return nil
}
func (f *fakeConnRecordRepo) GetByConnectionID(context.Context, string) (*ConnectionRecord, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) GetByUserID(context.Context, string) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) GetActiveByUserID(context.Context, string) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) List(context.Context, *repository.ConnectionQueryOptions) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) Count(context.Context, *repository.ConnectionQueryOptions) (int64, error) {
	return 0, nil
}
func (f *fakeConnRecordRepo) GetConnectionStats(context.Context, time.Time, time.Time) (*repository.ConnectionStats, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) GetConnectionStatsByID(context.Context, string) (*repository.UserConnectionStats, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) GetUserConnectionStats(context.Context, string) (*repository.UserConnectionStats, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) GetNodeConnectionStats(context.Context, string) (*repository.NodeConnectionStats, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) GetHighErrorRateConnections(context.Context, int, int) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) GetFrequentReconnectConnections(context.Context, int, int) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (f *fakeConnRecordRepo) BatchUpsert(context.Context, []*ConnectionRecord) error { return nil }
func (f *fakeConnRecordRepo) CleanupInactiveRecords(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeConnRecordRepo) WithTableName(string) ConnectionRecordRepository { return f }
func (f *fakeConnRecordRepo) Close() error                                    { return nil }

// replaceBatchersForTest 用短 flush 间隔替换 Hub 的批量处理器，便于测试中快速验证 flush
func replaceBatchersForTest(hub *Hub) {
	hub.messageStatsBatcher.Stop()
	hub.messageStatsBatcher = NewMessageStatsBatcher(hub, 100, 1, 50*time.Millisecond)
	hub.heartbeatBatcher.Stop()
	hub.heartbeatBatcher = NewHeartbeatStatsUpdater(hub, 100, 1, 50*time.Millisecond)
}

// ============================================================================
// shouldTrackUserStats
// ============================================================================

// TestShouldTrackUserStats 验证系统/机器人/观察者被排除，customer/agent 被追踪
func TestShouldTrackUserStats(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	cases := []struct {
		name     string
		ut       UserType
		expected bool
	}{
		{"系统用户不追踪", UserTypeSystem, false},
		{"机器人不追踪", UserTypeBot, false},
		{"观察者不追踪", UserTypeObserver, false},
		{"普通客户追踪", UserTypeCustomer, true},
		{"人工客服追踪", UserTypeAgent, true},
		{"管理员追踪", UserTypeAdmin, true},
		{"VIP追踪", UserTypeVIP, true},
		{"访客追踪", UserTypeVisitor, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.expected, hub.shouldTrackUserStats(c.ut))
		})
	}
}

// ============================================================================
// trackSenderMessageStats / trackReceiverMessageStats
// ============================================================================

// TestTrackSenderMessageStats 验证发送者统计追踪：nil repo 安全、系统/机器人排除、正常用户计数
func TestTrackSenderMessageStats(t *testing.T) {
	t.Run("nil_repo不panic", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		// setupGroupTestHub 未设置 connectionRecordRepo，为 nil
		assert.NotPanics(t, func() {
			hub.trackSenderMessageStats("conn-1", UserTypeCustomer)
		})
	})

	t.Run("空connectionID不计数", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)
		hub.trackSenderMessageStats("", UserTypeCustomer)
		time.Sleep(150 * time.Millisecond) // 等待 flush 周期
		assert.Empty(t, fake.getIncrementEntries(), "空 connectionID 不应提交统计")
	})

	t.Run("系统用户和机器人被排除", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)

		hub.trackSenderMessageStats("conn-sys", UserTypeSystem)
		hub.trackSenderMessageStats("conn-bot", UserTypeBot)
		hub.trackSenderMessageStats("conn-obs", UserTypeObserver)
		time.Sleep(150 * time.Millisecond)
		assert.Empty(t, fake.getIncrementEntries(), "系统/机器人/观察者不应产生统计")
	})

	t.Run("正常用户计数增加", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)

		hub.trackSenderMessageStats("conn-cust", UserTypeCustomer)
		require.Eventually(t, func() bool {
			return len(fake.getIncrementEntries()) > 0
		}, 2*time.Second, 20*time.Millisecond, "正常用户应产生统计")

		entries := fake.getIncrementEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, "conn-cust", entries[0].ConnectionID)
		assert.Equal(t, int64(1), entries[0].MessagesSent)
		assert.Equal(t, int64(0), entries[0].MessagesReceived)
	})
}

// TestTrackReceiverMessageStats 验证接收者统计追踪：nil repo 安全、系统/机器人排除、正常用户计数含字节数
func TestTrackReceiverMessageStats(t *testing.T) {
	t.Run("nil_repo不panic", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		assert.NotPanics(t, func() {
			hub.trackReceiverMessageStats("conn-1", UserTypeCustomer, 128)
		})
	})

	t.Run("系统用户被排除", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)

		hub.trackReceiverMessageStats("conn-sys", UserTypeSystem, 100)
		hub.trackReceiverMessageStats("conn-bot", UserTypeBot, 100)
		time.Sleep(150 * time.Millisecond)
		assert.Empty(t, fake.getIncrementEntries(), "系统/机器人不应产生接收统计")
	})

	t.Run("正常用户计数增加", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)

		hub.trackReceiverMessageStats("conn-recv", UserTypeAgent, 256)
		require.Eventually(t, func() bool {
			return len(fake.getIncrementEntries()) > 0
		}, 2*time.Second, 20*time.Millisecond)

		entries := fake.getIncrementEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, "conn-recv", entries[0].ConnectionID)
		assert.Equal(t, int64(1), entries[0].MessagesReceived)
		assert.Equal(t, int64(256), entries[0].BytesReceived)
		assert.Equal(t, int64(0), entries[0].MessagesSent)
	})
}

// ============================================================================
// trackConnectionError
// ============================================================================

// TestTrackConnectionError 验证连接错误追踪：nil/空 error 不计数、nil repo 安全、有 error 计数
func TestTrackConnectionError(t *testing.T) {
	t.Run("nil_repo不panic", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		assert.NotPanics(t, func() {
			hub.trackConnectionError("conn-1", UserTypeCustomer, errors.New("test"))
		})
	})

	t.Run("nil_error不计数", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake

		hub.trackConnectionError("conn-1", UserTypeCustomer, nil)
		time.Sleep(200 * time.Millisecond)
		assert.Equal(t, int64(0), fake.addErrorCount.Load(), "nil error 不应计数")
	})

	t.Run("空connectionID不计数", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake

		hub.trackConnectionError("", UserTypeCustomer, errors.New("err"))
		time.Sleep(200 * time.Millisecond)
		assert.Equal(t, int64(0), fake.addErrorCount.Load(), "空 connectionID 不应计数")
	})

	t.Run("系统用户被排除", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake

		hub.trackConnectionError("conn-sys", UserTypeSystem, errors.New("err"))
		hub.trackConnectionError("conn-bot", UserTypeBot, errors.New("err"))
		time.Sleep(200 * time.Millisecond)
		assert.Equal(t, int64(0), fake.addErrorCount.Load(), "系统/机器人不应记录错误")
	})

	t.Run("有error正常计数", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake

		hub.trackConnectionError("conn-err", UserTypeCustomer, errors.New("connection reset"))
		require.Eventually(t, func() bool {
			return fake.addErrorCount.Load() > 0
		}, 2*time.Second, 20*time.Millisecond, "正常用户的连接错误应被记录")
		assert.Equal(t, int64(1), fake.addErrorCount.Load())
	})
}

// ============================================================================
// trackHeartbeatStats
// ============================================================================

// TestTrackHeartbeatStats 验证心跳统计追踪：nil repo 安全、零值心跳 pingMs=0、非零心跳 pingMs>0
func TestTrackHeartbeatStats(t *testing.T) {
	t.Run("nil_repo不panic", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		client := makeTestClient("c-hb-nil", "u-hb-nil")
		assert.NotPanics(t, func() {
			hub.trackHeartbeatStats(client)
		})
	})

	t.Run("nil_client不panic", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)
		assert.NotPanics(t, func() {
			hub.trackHeartbeatStats(nil)
		})
		time.Sleep(150 * time.Millisecond)
		assert.Empty(t, fake.getHeartbeatEntries(), "nil client 不应产生心跳统计")
	})

	t.Run("系统用户被排除", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)

		sysClient := makeTestClient("c-sys", "u-sys")
		sysClient.UserType = UserTypeSystem
		hub.trackHeartbeatStats(sysClient)
		time.Sleep(150 * time.Millisecond)
		assert.Empty(t, fake.getHeartbeatEntries(), "系统用户不应产生心跳统计")
	})

	t.Run("零值心跳pingMs为零", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)

		client := makeTestClient("c-hb-zero", "u-hb-zero")
		// lastHeartbeatUnix 为 0 → GetLastHeartbeat 返回 time.Time{}
		hub.trackHeartbeatStats(client)
		require.Eventually(t, func() bool {
			return len(fake.getHeartbeatEntries()) > 0
		}, 2*time.Second, 20*time.Millisecond)

		entries := fake.getHeartbeatEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, "c-hb-zero", entries[0].ConnectionID)
		assert.Equal(t, float64(0), entries[0].PingMs, "零值心跳 pingMs 应为 0")
	})

	t.Run("非零心跳pingMs大于零", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		fake := &fakeConnRecordRepo{}
		hub.connectionRecordRepo = fake
		replaceBatchersForTest(hub)

		client := makeTestClient("c-hb-ok", "u-hb-ok")
		client.SetLastHeartbeat(time.Now().Add(-100 * time.Millisecond))
		client.SetLastPong(time.Now())
		hub.trackHeartbeatStats(client)
		require.Eventually(t, func() bool {
			return len(fake.getHeartbeatEntries()) > 0
		}, 2*time.Second, 20*time.Millisecond)

		entries := fake.getHeartbeatEntries()
		require.Len(t, entries, 1)
		assert.Equal(t, "c-hb-ok", entries[0].ConnectionID)
		assert.Greater(t, entries[0].PingMs, float64(0), "非零心跳 pingMs 应 > 0")
	})
}

// ============================================================================
// logClientConnection
// ============================================================================

// TestLogClientConnection 验证连接日志渲染不 panic，且输出包含 clientID/userID
func TestLogClientConnection(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("log-client-id", "log-user-id")
	client.ClientIP = "10.0.0.1"
	hub.shardedRegistry.AddClient(client)

	// 将 logger 输出重定向到 buffer 以便断言内容
	var buf bytes.Buffer
	if l, ok := hub.logger.(*logger.Logger); ok {
		l.WithOutput(&buf)
	}

	assert.NotPanics(t, func() {
		hub.logClientConnection(client)
	})

	output := buf.String()
	assert.Contains(t, output, "log-client-id", "日志应包含客户端ID")
	assert.Contains(t, output, "log-user-id", "日志应包含用户ID")
}

// ============================================================================
// syncClientStats / syncOnlineStatus
// ============================================================================

// TestSyncClientStats 验证同步客户端统计：nil statsRepo 安全不 panic
func TestSyncClientStats(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// statsRepo 为 nil（setupGroupTestHub 未设置）
	assert.NotPanics(t, func() {
		hub.syncClientStats()
	})
	// 给异步任务一点时间确认不 panic
	time.Sleep(100 * time.Millisecond)
}

// TestSyncOnlineStatus 验证同步在线状态：nil onlineStatusRepo 安全不 panic
func TestSyncOnlineStatus(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	client := makeTestClient("sync-online", "sync-user")
	// onlineStatusRepo 为 nil
	assert.NotPanics(t, func() {
		hub.syncOnlineStatus(client)
	})
}

// ============================================================================
// logWithClient
// ============================================================================

// TestStatsLogWithClient 验证 logWithClient 各级别不 panic
func TestStatsLogWithClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	client := makeTestClient("log-wc-id", "log-wc-user")

	levels := []logger.LogLevel{logger.DEBUG, logger.INFO, logger.WARN, logger.ERROR}
	for _, level := range levels {
		assert.NotPanics(t, func() {
			hub.logWithClient(level, "测试日志消息", client, "extra_key", "extra_val")
		})
	}
}

// ============================================================================
// WaitForStartWithTimeout
// ============================================================================

// TestWaitForStartWithTimeout 验证带超时的启动等待：正常启动返回 nil，超时返回 ErrHubStartupTimeout
func TestWaitForStartWithTimeout(t *testing.T) {
	t.Run("正常启动", func(t *testing.T) {
		hub, _, _, cleanup := setupGroupTestHub(t)
		defer cleanup()

		go hub.Run()
		err := hub.WaitForStartWithTimeout(5 * time.Second)
		assert.NoError(t, err, "正常启动应在超时前返回 nil")
	})

	t.Run("超时返回错误", func(t *testing.T) {
		config := wscconfig.Default().
			WithNodeInfo("127.0.0.1", 18080).
			WithHeartbeatInterval(30 * time.Second).
			WithMessageBufferSize(256)
		hub := NewHub(config)
		defer hub.SafeShutdown()

		// 不启动 Hub，直接等待超时
		err := hub.WaitForStartWithTimeout(100 * time.Millisecond)
		assert.Equal(t, ErrHubStartupTimeout, err, "未启动的 Hub 应返回 ErrHubStartupTimeout")
	})
}

// ============================================================================
// FlushStats
// ============================================================================

// TestFlushStats 验证刷写统计计数器：nil statsRepo 安全不 panic
func TestFlushStats(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// statsRepo 为 nil
	assert.NotPanics(t, func() {
		hub.FlushStats()
	})
}

// ============================================================================
// cleanupExpiredAck
// ============================================================================

// TestStatsCleanupExpiredAck 验证清理过期 ACK：nil ackManager 安全不 panic
func TestStatsCleanupExpiredAck(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// ackManager 由 NewHub 创建，手动置 nil 测试安全路径
	originalAck := hub.ackManager
	hub.ackManager = nil
	defer func() { hub.ackManager = originalAck }()

	assert.NotPanics(t, func() {
		hub.cleanupExpiredAck()
	})
}

// ============================================================================
// reportPerformanceMetrics
// ============================================================================

// TestReportPerformanceMetrics 验证性能指标报告：nil statsRepo 安全不 panic
func TestReportPerformanceMetrics(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// statsRepo 为 nil
	assert.NotPanics(t, func() {
		hub.reportPerformanceMetrics()
	})
}

// ============================================================================
// GetMaxConnectionsPerNode
// ============================================================================

// TestStatsGetMaxConnectionsPerNode 验证节点最大连接数读取：nil Performance 返回 0，正常配置返回设定值
func TestStatsGetMaxConnectionsPerNode(t *testing.T) {
	t.Run("正常配置返回设定值", func(t *testing.T) {
		config := wscconfig.Default()
		if config.Performance == nil {
			config.Performance = &wscconfig.Performance{}
		}
		config.Performance.MaxConnectionsPerNode = 5000
		hub := NewHub(config)
		defer hub.SafeShutdown()
		assert.Equal(t, 5000, hub.GetMaxConnectionsPerNode())
	})

	t.Run("Performance为nil返回0", func(t *testing.T) {
		config := wscconfig.Default()
		config.Performance = nil
		hub := NewHub(config)
		defer hub.SafeShutdown()
		assert.Equal(t, 0, hub.GetMaxConnectionsPerNode(), "Performance 为 nil 时应返回 0")
	})
}

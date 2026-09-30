/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-30 03:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-30 03:05:00
 * @FilePath: \go-wsc\hub\shutdown_cleanup_test.go
 * @Description: 停机批量清理测试（batchCleanupOnShutdown 编排逻辑）
 *
 * 覆盖：三段清理全链路（Redis 下线/断连终态/质量终评）、块间并行正确性、
 * Redis 故障隔离、终评超时熔断、依赖未注入降级、空客户端短路
 * 测试数据按生产规模构造（多租户混合 + 跨块规模），块间并行下到达顺序不定，
 * 断言一律按总量/集合语义而非块序
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ========== 测试桩（embed 接口法：未覆盖方法调用即 panic，只覆写停机清理链路用到的方法） ==========

// shutdownOnlineRepo 在线状态仓储桩：收集批量下线块，可注入错误
type shutdownOnlineRepo struct {
	spi.OnlineStore
	mu     sync.Mutex
	chunks [][]*models.Client
	err    error
}

func (f *shutdownOnlineRepo) BatchSetClientsOfflineWithInfo(_ context.Context, clients []*models.Client) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.chunks = append(f.chunks, append([]*models.Client(nil), clients...))
	return nil
}

func (f *shutdownOnlineRepo) offlineChunks() [][]*models.Client {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]*models.Client(nil), f.chunks...)
}

// shutdownQualityStore 质量仓储桩：收集批量终评块
type shutdownQualityStore struct {
	spi.ConnectionQualityStore
	mu     sync.Mutex
	chunks [][]*models.DisconnectionEntry
}

func (f *shutdownQualityStore) BatchFinalizeOnDisconnect(_ context.Context, entries []*models.DisconnectionEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, append([]*models.DisconnectionEntry(nil), entries...))
	return nil
}

func (f *shutdownQualityStore) finalizeChunks() [][]*models.DisconnectionEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]*models.DisconnectionEntry(nil), f.chunks...)
}

// shutdownStatsRepo 节点统计桩：记录 SetActiveConnections 调用
type shutdownStatsRepo struct {
	spi.HubStats
	mu     sync.Mutex
	counts []int64
}

func (f *shutdownStatsRepo) SetActiveConnections(_ context.Context, _ string, count int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.counts = append(f.counts, count)
	return nil
}

func (f *shutdownStatsRepo) activeCounts() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.counts...)
}

// shutdownRecordStore 连接记录仓储桩：收集批量断连终态块（经 RecordManager 分块直调）
type shutdownRecordStore struct {
	spi.ConnectionStore
	mu     sync.Mutex
	chunks [][]*models.DisconnectionEntry
}

func (f *shutdownRecordStore) BatchMarkDisconnected(_ context.Context, entries []*models.DisconnectionEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, append([]*models.DisconnectionEntry(nil), entries...))
	return nil
}

func (f *shutdownRecordStore) markChunks() [][]*models.DisconnectionEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]*models.DisconnectionEntry(nil), f.chunks...)
}

// shutdownRecordHost 连接记录端口桩：仓储动态返回（GetLogger 返回 nil，构造器内部兜底默认日志器）
type shutdownRecordHost struct {
	store spi.ConnectionStore
}

func (h *shutdownRecordHost) GetLogger() spi.Logger                        { return nil }
func (h *shutdownRecordHost) GetConnectionRecordRepo() spi.ConnectionStore { return h.store }
func (h *shutdownRecordHost) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	return nil
}
func (h *shutdownRecordHost) GetDisconnectionBatcher() connection.DisconnectionSubmitter {
	return nil
}

// newShutdownCleanupHub 构造仅装配停机清理链路依赖的 Hub（其余字段零值）
func newShutdownCleanupHub(recordStore spi.ConnectionStore) *Hub {
	return &Hub{
		nodeID:    "node-shutdown-test",
		logger:    spi.InitLogger(nil),
		recordMgr: connection.NewRecordManager(&shutdownRecordHost{store: recordStore}),
	}
}

// newShutdownClients 生产规模多租户客户端集：3 租户 × 3 命名空间混合，
// total 跨块（500/块）覆盖整除与余数两种边界
func newShutdownClients(total int) []*models.Client {
	apps := []string{"app-game", "app-shop", "__default_app__"}
	nss := []string{"", "ns-vip", "ns-eu"}
	clients := make([]*models.Client, total)
	for i := 0; i < total; i++ {
		c := models.NewClient(
			fmt.Sprintf("conn-shut-%d", i),
			fmt.Sprintf("u-shut-%d", i),
			models.UserTypeCustomer,
		)
		c.AppID = apps[i%len(apps)]
		c.Namespace = nss[i%len(nss)]
		clients[i] = c
	}
	return clients
}

// assertChunksTotal 断言块集合的总量与单块上限（块间并行到达顺序不定，按集合语义校验）
func assertChunksTotal[T any](t *testing.T, chunks [][]T, total int, maxChunk int, what string) {
	t.Helper()
	require.NotEmpty(t, chunks, "%s 不应为空", what)
	got := 0
	for _, chunk := range chunks {
		assert.LessOrEqual(t, len(chunk), maxChunk, "%s 单块不应超过分块上限", what)
		got += len(chunk)
	}
	assert.Equal(t, total, got, "%s 总量应覆盖全部连接", what)
}

// TestShutdownBatchCleanupFullPipeline 三段清理全链路：1237 连接（3 块：500+500+237），
// stats 归零 / Redis 批量下线 / 断连终态 / 质量终评全部触达且无遗漏
func TestShutdownBatchCleanupFullPipeline(t *testing.T) {
	total := shutdownChunkSize*2 + 237
	clients := newShutdownClients(total)

	online := &shutdownOnlineRepo{}
	quality := &shutdownQualityStore{}
	stats := &shutdownStatsRepo{}
	recordStore := &shutdownRecordStore{}

	h := newShutdownCleanupHub(recordStore)
	h.onlineStatusRepo = online
	h.connectionQualityStore = quality
	h.statsRepo = stats

	h.batchCleanupOnShutdown(clients)

	// stats 只调一次归零
	assert.Equal(t, []int64{0}, stats.activeCounts(), "停机应且仅应将活跃连接数归零一次")

	// Redis 批量下线：3 块覆盖 1237 连接，无重复
	assertChunksTotal(t, online.offlineChunks(), total, shutdownChunkSize, "Redis 批量下线")
	seen := make(map[string]bool, total)
	for _, chunk := range online.offlineChunks() {
		for _, c := range chunk {
			assert.False(t, seen[c.ID], "连接 %s 不应重复下线", c.ID)
			seen[c.ID] = true
		}
	}
	assert.Len(t, seen, total, "Redis 下线应覆盖全部连接且去重")

	// 断连终态：3 块覆盖 1237 连接
	assertChunksTotal(t, recordStore.markChunks(), total, shutdownChunkSize, "断连终态")

	// 质量终评：块总量与断终一致（entries 由断终返回复用），终态语义正确
	finalizeChunks := quality.finalizeChunks()
	assertChunksTotal(t, finalizeChunks, total, shutdownChunkSize, "质量终评")
	for _, chunk := range finalizeChunks {
		for _, e := range chunk {
			assert.Equal(t, models.DisconnectReasonServerShutdown, e.Reason, "停机路径 Reason 应为 ServerShutdown")
			assert.Equal(t, 1001, e.Code, "停机路径应携带 1001 GoingAway 关闭码")
		}
	}
}

// TestShutdownCleanupRedisFailureIsolated Redis 下线失败不阻断后续清理：
// 断连终态与质量终评仍全量执行（各段失败独立记日志）
func TestShutdownCleanupRedisFailureIsolated(t *testing.T) {
	total := shutdownChunkSize + 88
	clients := newShutdownClients(total)

	online := &shutdownOnlineRepo{err: errors.New("redis unavailable")}
	quality := &shutdownQualityStore{}
	stats := &shutdownStatsRepo{}
	recordStore := &shutdownRecordStore{}

	h := newShutdownCleanupHub(recordStore)
	h.onlineStatusRepo = online
	h.connectionQualityStore = quality
	h.statsRepo = stats

	require.NotPanics(t, func() {
		h.batchCleanupOnShutdown(clients)
	})

	assert.Empty(t, online.offlineChunks(), "注入错误后不应有成功下线的块")
	assert.Equal(t, []int64{0}, stats.activeCounts(), "stats 归零不受 Redis 故障影响")
	assertChunksTotal(t, recordStore.markChunks(), total, shutdownChunkSize, "断连终态")
	assertChunksTotal(t, quality.finalizeChunks(), total, shutdownChunkSize, "质量终评")
}

// TestShutdownCleanupFinalizeCircuitBreaker 终评超时熔断：清理段耗时超阈值时
// 跳过质量终评（审计让位于按时退出），断连终态不受影响
func TestShutdownCleanupFinalizeCircuitBreaker(t *testing.T) {
	origSkip := shutdownFinalizeSkipAfter
	defer func() { shutdownFinalizeSkipAfter = origSkip }()
	// 阈值归零使 elapsed >= 0 恒成立，稳定触发熔断分支（免去真实等待）
	shutdownFinalizeSkipAfter = 0

	total := shutdownChunkSize + 12
	clients := newShutdownClients(total)

	quality := &shutdownQualityStore{}
	recordStore := &shutdownRecordStore{}

	h := newShutdownCleanupHub(recordStore)
	h.onlineStatusRepo = &shutdownOnlineRepo{}
	h.connectionQualityStore = quality
	h.statsRepo = &shutdownStatsRepo{}

	h.batchCleanupOnShutdown(clients)

	assert.Empty(t, quality.finalizeChunks(), "清理超阈值时应跳过质量终评")
	assertChunksTotal(t, recordStore.markChunks(), total, shutdownChunkSize, "断连终态")
}

// TestShutdownCleanupNilReposNoPanic 依赖未注入降级：stats/online/quality 均 nil
// 时仅执行断连终态，不 panic（域内判空降级约定）
func TestShutdownCleanupNilReposNoPanic(t *testing.T) {
	total := 66
	clients := newShutdownClients(total)
	recordStore := &shutdownRecordStore{}

	h := newShutdownCleanupHub(recordStore)

	require.NotPanics(t, func() {
		h.batchCleanupOnShutdown(clients)
	})

	assertChunksTotal(t, recordStore.markChunks(), total, shutdownChunkSize, "断连终态")
}

// TestShutdownCleanupEmptyClients 空客户端短路：不触达任何仓储
func TestShutdownCleanupEmptyClients(t *testing.T) {
	online := &shutdownOnlineRepo{}
	quality := &shutdownQualityStore{}
	stats := &shutdownStatsRepo{}
	recordStore := &shutdownRecordStore{}

	h := newShutdownCleanupHub(recordStore)
	h.onlineStatusRepo = online
	h.connectionQualityStore = quality
	h.statsRepo = stats

	require.NotPanics(t, func() {
		h.batchCleanupOnShutdown(nil)
	})

	assert.Empty(t, stats.activeCounts(), "空客户端不应触达 stats")
	assert.Empty(t, online.offlineChunks(), "空客户端不应触达 Redis 下线")
	assert.Empty(t, recordStore.markChunks(), "空客户端不应触达断连终态")
	assert.Empty(t, quality.finalizeChunks(), "空客户端不应触达质量终评")
}

// TestShutdownCleanupEntriesEmptySkipsFinalize 断终产物为空时跳过终评：
// 零连接产出零 entries，终评不应被空块调用
func TestShutdownCleanupEntriesEmptySkipsFinalize(t *testing.T) {
	// recordMgr 未注入仓储（store 为 nil）时 MarkDisconnectedBatch 返回空 entries
	h := newShutdownCleanupHub(nil)
	quality := &shutdownQualityStore{}
	h.connectionQualityStore = quality
	h.onlineStatusRepo = &shutdownOnlineRepo{}
	h.statsRepo = &shutdownStatsRepo{}

	require.NotPanics(t, func() {
		h.batchCleanupOnShutdown(newShutdownClients(30))
	})

	assert.Empty(t, quality.finalizeChunks(), "entries 为空时不应调用终评")
}

// TestShutdownFinalizeSkipAfterThreshold 语义守护：默认阈值应容纳正常清理耗时
// （Redis 最坏 10s + 断终 30s ctx 截止 = 40s 打满即熔断）
func TestShutdownFinalizeSkipAfterThreshold(t *testing.T) {
	assert.Equal(t, 40*time.Second, shutdownFinalizeSkipAfter,
		"默认熔断阈值应为 40s（Redis 10s + 断终 30s 截止的打满上界）")
	assert.Equal(t, 8, shutdownWorkers, "停机并行块数应为 8")
	assert.Equal(t, 500, shutdownChunkSize, "停机分块大小应为 500")
}

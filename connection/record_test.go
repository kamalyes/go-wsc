/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:08:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-30 09:21:17
 * @FilePath: \go-wsc\connection\record_test.go
 * @Description: 连接域连接记录管理器测试 - 快照构造 / 异步落库 / 断连攒批 / 停机批量终态

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// fakeRecordHost 连接记录端口测试桩：仓储与攒批器可运行期切换（未注入 → 注入）
type fakeRecordHost struct {
	mu           sync.Mutex
	store        spi.ConnectionStore
	qualityStore spi.ConnectionQualityStore
	batcher      DisconnectionSubmitter
}

// GetLogger 返回 nil（构造器内部兜底默认日志器）
func (f *fakeRecordHost) GetLogger() spi.Logger { return nil }

func (f *fakeRecordHost) GetConnectionRecordRepo() spi.ConnectionStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.store
}

func (f *fakeRecordHost) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.qualityStore
}

func (f *fakeRecordHost) GetDisconnectionBatcher() DisconnectionSubmitter {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.batcher
}

// fakeDisconnectionBatcher 断连终态攒批桩：同步捕获提交的条目（便于断言快照语义）
type fakeDisconnectionBatcher struct {
	mu      sync.Mutex
	entries []*models.DisconnectionEntry
	accept  bool // Submit 返回值（false 模拟队列满丢弃）
}

func (f *fakeDisconnectionBatcher) Submit(entry *models.DisconnectionEntry) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries = append(f.entries, entry)
	return f.accept
}

func (f *fakeDisconnectionBatcher) submitted() []*models.DisconnectionEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*models.DisconnectionEntry(nil), f.entries...)
}

// fakeRecordStore 连接记录仓储桩：只覆盖记录语义相关方法（未覆盖方法 panic）
type fakeRecordStore struct {
	spi.ConnectionStore // 未覆盖的方法调用即 panic

	mu      sync.Mutex
	upserts []*models.ConnectionRecord
	// chunks 每次 BatchMarkDisconnected 调用记为一个块（停机路径分块直调）
	chunks [][]*models.DisconnectionEntry
	// markErr 注入批量断连错误（验证块失败不阻断其余块，错误仅记日志）
	markErr error
}

func (f *fakeRecordStore) Upsert(_ context.Context, record *models.ConnectionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, record)
	return nil
}

func (f *fakeRecordStore) BatchMarkDisconnected(_ context.Context, entries []*models.DisconnectionEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chunks = append(f.chunks, append([]*models.DisconnectionEntry(nil), entries...))
	return f.markErr
}

func (f *fakeRecordStore) upsertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upserts)
}

func (f *fakeRecordStore) disconnectChunks() [][]*models.DisconnectionEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]*models.DisconnectionEntry(nil), f.chunks...)
}

// fakeQualityStore 连接质量仓储桩：只覆盖注册落库路径用到的 Upsert（未覆盖方法 panic）
type fakeQualityStore struct {
	spi.ConnectionQualityStore // 未覆盖的方法调用即 panic

	mu      sync.Mutex
	upserts []*models.ConnectionQuality
	// upsertErr 注入 Upsert 错误（验证 quality 失败不阻断 connect 行写入）
	upsertErr error
}

func (f *fakeQualityStore) Upsert(_ context.Context, quality *models.ConnectionQuality) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, quality)
	return f.upsertErr
}

func (f *fakeQualityStore) seeded() []*models.ConnectionQuality {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*models.ConnectionQuality(nil), f.upserts...)
}

func (f *fakeQualityStore) upsertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upserts)
}

// newRecordManagerFixture 构造记录管理器与端口桩（仓储 + 质量仓储 + 攒批器均已注入）
func newRecordManagerFixture(t *testing.T) (*RecordManager, *fakeRecordHost, *fakeRecordStore, *fakeQualityStore, *fakeDisconnectionBatcher) {
	t.Helper()
	store := &fakeRecordStore{}
	quality := &fakeQualityStore{}
	batcher := &fakeDisconnectionBatcher{accept: true}
	host := &fakeRecordHost{store: store, qualityStore: quality, batcher: batcher}
	return NewRecordManager(host), host, store, quality, batcher
}

// TestRecordCreateSnapshot 记录构造：Client 关键字段快照到 ConnectionRecord
func TestRecordCreateSnapshot(t *testing.T) {
	manager, _, _, _, _ := newRecordManagerFixture(t)

	client := models.NewClient("rec-1", "u-13000", models.UserTypeCustomer)
	client.NodeID = "node-a"
	client.NodeIP = "10.0.0.1"
	client.NodePort = 8080
	client.ConnectionType = models.ConnectionTypeWebSocket
	client.SetMetadataValue("device", "ios")

	record := manager.Create(client)

	assert.Equal(t, client.ID, record.ConnectionID)
	assert.Equal(t, client.UserID, record.UserID)
	assert.Equal(t, "node-a", record.NodeID)
	assert.Equal(t, "10.0.0.1", record.NodeIP)
	assert.Equal(t, 8080, record.NodePort)
	assert.Equal(t, models.ConnectionTypeWebSocket, record.Protocol)
	assert.True(t, record.IsActive, "新建记录应为活跃状态")
	assert.Equal(t, client.ConnectedAt, record.ConnectedAt)
	assert.NotNil(t, record.Metadata, "metadata 快照应写入记录")
}

// TestRecordSaveUpsertsAsync 异步保存：经 syncx.Go 异步 Upsert（Eventually 等待）
func TestRecordSaveUpsertsAsync(t *testing.T) {
	manager, _, store, _, _ := newRecordManagerFixture(t)

	client := models.NewClient("rec-2", "u-13001", models.UserTypeCustomer)
	record := manager.Create(client)

	manager.Save(context.Background(), record)

	require.Eventually(t, func() bool {
		return store.upsertCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "记录应被异步 Upsert")
}

// TestRecordSaveSeedsQualityRow 质量初始行同生：Save 落 connect 行的同时派生
// quality 初始行（关联标识四元组来自 record；指标零值 + 评分兜底由仓储 Upsert 承担），
// 否则 batcher 的心跳/统计/错误批量 UPDATE 全部空转影响 0 行
func TestRecordSaveSeedsQualityRow(t *testing.T) {
	manager, _, store, quality, _ := newRecordManagerFixture(t)

	client := models.NewClient("rec-q1", "u-13008", models.UserTypeCustomer)
	client.AppID = "app-1001"
	client.Namespace = "ns-2001"
	record := manager.Create(client)
	record.AppID = client.AppID
	record.Namespace = client.Namespace

	manager.Save(context.Background(), record)

	require.Eventually(t, func() bool {
		return quality.upsertCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "质量初始行应与连接记录同批写入")

	seeded := quality.seeded()
	require.Len(t, seeded, 1)
	q := seeded[0]
	assert.Equal(t, record.ConnectionID, q.ConnectionID)
	assert.Equal(t, record.UserID, q.UserID)
	assert.Equal(t, record.AppID, q.AppID)
	assert.Equal(t, record.Namespace, q.Namespace)
	assert.Equal(t, store.upsertCount(), quality.upsertCount(), "connect 行与 quality 行应成对落库")
}

// TestRecordSaveQualityStoreNilNoOp 质量仓储未注入：Save 降级只写 connect 行，不 panic
func TestRecordSaveQualityStoreNilNoOp(t *testing.T) {
	store := &fakeRecordStore{}
	host := &fakeRecordHost{store: store, qualityStore: nil}
	manager := NewRecordManager(host)

	client := models.NewClient("rec-qnil", "u-13009", models.UserTypeCustomer)
	record := manager.Create(client)

	require.NotPanics(t, func() {
		manager.Save(context.Background(), record)
	})
	require.Eventually(t, func() bool {
		return store.upsertCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "质量仓储缺失不应拖垮连接记录落库")
}

// TestRecordSaveQualityErrorNotBlocking quality 写入失败互不阻断：connect 行仍正常落库
func TestRecordSaveQualityErrorNotBlocking(t *testing.T) {
	manager, _, store, quality, _ := newRecordManagerFixture(t)
	quality.upsertErr = errors.New("quality table unavailable")

	client := models.NewClient("rec-qerr", "u-13010", models.UserTypeCustomer)
	record := manager.Create(client)

	manager.Save(context.Background(), record)

	require.Eventually(t, func() bool {
		return store.upsertCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "quality 失败不应阻断 connect 行写入")
}

// TestRecordMarkDisconnectedSubmitsSnapshot 攒批路径：断连终态以快照提交到攒批器，
// reason=ClientRequest、code=0，ConnectedAt 从内存 Client 带入，DisconnectedAt 在提交瞬间冻结
func TestRecordMarkDisconnectedSubmitsSnapshot(t *testing.T) {
	manager, _, _, _, batcher := newRecordManagerFixture(t)

	client := models.NewClient("rec-3", "u-13002", models.UserTypeCustomer)
	client.NodeID = "node-b"
	before := time.Now()

	manager.MarkDisconnected(client)

	entries := batcher.submitted()
	require.Len(t, entries, 1, "断连终态应提交到攒批器")
	e := entries[0]
	assert.Equal(t, client.ID, e.ConnectionID)
	assert.Equal(t, client.ConnectedAt, e.ConnectedAt, "ConnectedAt 应从内存 Client 快照带入")
	assert.Equal(t, models.DisconnectReasonClientRequest, e.Reason)
	assert.Zero(t, e.Code)
	assert.False(t, e.DisconnectedAt.Before(before), "DisconnectedAt 不应早于提交时刻")
}

// TestRecordMarkDisconnectedQueueFullNoOp 队列满丢弃：Submit 返回 false 仅记日志不 panic
func TestRecordMarkDisconnectedQueueFullNoOp(t *testing.T) {
	store := &fakeRecordStore{}
	batcher := &fakeDisconnectionBatcher{accept: false}
	host := &fakeRecordHost{store: store, batcher: batcher}
	manager := NewRecordManager(host)

	client := models.NewClient("rec-full", "u-13005", models.UserTypeCustomer)
	require.NotPanics(t, func() {
		manager.MarkDisconnected(client)
	})
	assert.Len(t, batcher.submitted(), 1, "条目仍被记录（桩语义），真实队列满时丢弃")
}

// TestRecordMarkDisconnectedBatch 停机批量终态：单块内全部条目 reason=ServerShutdown、code=1001，
// DisconnectedAt 取同一时间戳快照（同批停机语义一致）
func TestRecordMarkDisconnectedBatch(t *testing.T) {
	manager, _, store, _, _ := newRecordManagerFixture(t)

	clients := []*models.Client{
		models.NewClient("rec-b1", "u-13003", models.UserTypeCustomer),
		models.NewClient("rec-b2", "u-13003", models.UserTypeCustomer),
		models.NewClient("rec-b3", "u-13007", models.UserTypeCustomer),
	}

	manager.MarkDisconnectedBatch(clients)

	// 同步直调：调用返回时已全部完成且落在同一块（未超分块上限）
	chunks := store.disconnectChunks()
	require.Len(t, chunks, 1, "批量不超过分块上限时应合并为单块")
	entries := chunks[0]
	require.Len(t, entries, len(clients), "每个连接应产生一条断连终态")
	ids := make(map[string]bool, len(entries))
	for _, e := range entries {
		assert.Equal(t, models.DisconnectReasonServerShutdown, e.Reason)
		assert.Equal(t, 1001, e.Code, "停机路径应携带 1001 GoingAway 关闭码")
		ids[e.ConnectionID] = true
	}
	for _, client := range clients {
		assert.True(t, ids[client.ID], "连接 %s 应被标记", client.ID)
	}
}

// TestRecordMarkDisconnectedBatchChunks 停机超分块上限：按 disconnectionShutdownChunkSize
// 切块直调，块边界覆盖整除与余数两种情况
func TestRecordMarkDisconnectedBatchChunks(t *testing.T) {
	manager, _, store, _, _ := newRecordManagerFixture(t)

	total := disconnectionShutdownChunkSize*2 + 37 // 2 个满块 + 37 条余数块
	clients := make([]*models.Client, 0, total)
	for i := 0; i < total; i++ {
		clients = append(clients, models.NewClient(
			"rec-chunk-"+strconv.Itoa(i),
			"u-13"+strconv.Itoa(9000+i),
			models.UserTypeCustomer,
		))
	}

	manager.MarkDisconnectedBatch(clients)

	chunks := store.disconnectChunks()
	require.Len(t, chunks, 3, "超上限应按块拆分")
	// 块间并行提交，各块到达顺序不确定，按块大小排序后校验边界
	sizes := make([]int, len(chunks))
	for i, chunk := range chunks {
		sizes[i] = len(chunk)
	}
	sort.Ints(sizes)
	assert.Equal(t, []int{37, disconnectionShutdownChunkSize, disconnectionShutdownChunkSize}, sizes,
		"应为 2 个满块 + 37 条余数块")

	seen := make(map[string]bool, total)
	for _, chunk := range chunks {
		for _, e := range chunk {
			assert.Equal(t, models.DisconnectReasonServerShutdown, e.Reason)
			assert.False(t, seen[e.ConnectionID], "连接 %s 不应重复标记", e.ConnectionID)
			seen[e.ConnectionID] = true
		}
	}
	assert.Len(t, seen, total, "全部连接应被标记且去重")
}

// TestRecordMarkDisconnectedBatchStoreError 块失败不阻断：仓储持续返回错误时
// 全部块仍被尝试（每块独立记日志），entries 仍全量返回供终评复用
func TestRecordMarkDisconnectedBatchStoreError(t *testing.T) {
	manager, _, store, _, _ := newRecordManagerFixture(t)
	store.markErr = errors.New("db unavailable")

	total := disconnectionShutdownChunkSize + 45 // 2 块：500 + 45
	clients := make([]*models.Client, 0, total)
	for i := 0; i < total; i++ {
		clients = append(clients, models.NewClient(
			"rec-err-"+strconv.Itoa(i),
			"u-13"+strconv.Itoa(9000+i),
			models.UserTypeCustomer,
		))
	}

	var entries []*models.DisconnectionEntry
	require.NotPanics(t, func() {
		entries = manager.MarkDisconnectedBatch(clients)
	})

	chunks := store.disconnectChunks()
	require.Len(t, chunks, 2, "错误不应中断分块：全部块均被尝试")
	seen := make(map[string]bool, total)
	for _, chunk := range chunks {
		for _, e := range chunk {
			seen[e.ConnectionID] = true
		}
	}
	assert.Len(t, seen, total, "全部连接应被尝试标记")
	require.Len(t, entries, total, "entries 返回不受仓储错误影响（终评复用契约）")
	for _, e := range entries {
		assert.Equal(t, models.DisconnectReasonServerShutdown, e.Reason)
		assert.Equal(t, 1001, e.Code)
	}
}

// TestRecordMarkDisconnectedBatchEmpty 空客户端列表：直调零次不 panic
func TestRecordMarkDisconnectedBatchEmpty(t *testing.T) {
	manager, _, store, _, _ := newRecordManagerFixture(t)

	manager.MarkDisconnectedBatch(nil)
	manager.MarkDisconnectedBatch([]*models.Client{})

	assert.Empty(t, store.disconnectChunks(), "空列表不应触发任何批量调用")
}

// TestRecordWithoutStoreNoOp 仓储/攒批器未注入：全路径 no-op 降级不 panic
func TestRecordWithoutStoreNoOp(t *testing.T) {
	host := &fakeRecordHost{store: nil, batcher: nil} // 未注入仓储与攒批器
	manager := NewRecordManager(host)
	client := models.NewClient("rec-noop", "u-13004", models.UserTypeCustomer)
	record := manager.Create(client)

	require.NotPanics(t, func() {
		manager.Save(context.Background(), record)
		manager.MarkDisconnected(client)
		manager.MarkDisconnectedBatch([]*models.Client{client})
	})
}

// TestRecordStoreOnlyNoOp 攒批器未注入但仓储在：单连接路径 no-op（停机批量路径仍可用）
func TestRecordStoreOnlyNoOp(t *testing.T) {
	store := &fakeRecordStore{}
	host := &fakeRecordHost{store: store, batcher: nil}
	manager := NewRecordManager(host)
	client := models.NewClient("rec-storeonly", "u-13006", models.UserTypeCustomer)

	require.NotPanics(t, func() {
		manager.MarkDisconnected(client)
	})
	assert.Empty(t, store.disconnectChunks(), "攒批器未注入时单连接路径不应直调仓储")

	manager.MarkDisconnectedBatch([]*models.Client{client})
	chunks := store.disconnectChunks()
	require.Len(t, chunks, 1, "停机批量路径不依赖攒批器")
	require.Len(t, chunks[0], 1)
}

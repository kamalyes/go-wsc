/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-02 16:20:01
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-02 16:20:01
 * @FilePath: \go-wsc\batcher\disconnection_batcher_test.go
 * @Description: 断连终态批量更新器测试
 *   - 创建 / 非阻塞 Submit / 空 batch flush 不落库
 *   - flush 批量透传（批内去重归仓储层，本层保持批次原样）
 *   - 满批 200 自动 flush（凑批语义）
 *   - 队列满丢弃 + DroppedCount（8192 固定容量下的确定性构造）
 *   - Stop flush 剩余（停机不丢账）且幂等
 *   - 仓储缺失 / 仓储报错时不 panic
 *   - 并发 Submit 全量落库
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// 基准结果 sink（包级变量防编译器死代码消除，七个组件的基准共用）
var (
	benchSinkBool   bool
	benchSinkInt    int
	benchSinkInt64  int64
	benchSinkFloat  float64
	benchSinkString string
)

// newDisconnectionTestHub 创建带连接记录仓储替身的端口替身
func newDisconnectionTestHub(t *testing.T) (*fakeBatchWriter, *fakeConnStore) {
	t.Helper()
	w := newFakeBatchWriter()
	return w, w.records
}

// newDisconnectionEntry 构造一条断连终态条目
func newDisconnectionEntry(connID string, at time.Time) *models.DisconnectionEntry {
	return &models.DisconnectionEntry{
		ConnectionID:   connID,
		ConnectedAt:    at.Add(-time.Minute),
		DisconnectedAt: at,
		Reason:         models.DisconnectReasonClientRequest,
		Code:           1000,
	}
}

// blockingDisconnectionStore 可阻塞的连接仓储替身：
// 首次 BatchMarkDisconnected 入口阻塞（卡住 worker flush 的确定性手段），
// release 关闭后放行并委托内层替身记录
type blockingDisconnectionStore struct {
	*fakeConnStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingDisconnectionStore) BatchMarkDisconnected(ctx context.Context, entries []*models.DisconnectionEntry) error {
	f.once.Do(func() { close(f.started) })
	<-f.release
	return f.fakeConnStore.BatchMarkDisconnected(ctx, entries)
}

// disconnectionBlockingWriter 返回阻塞式连接仓储的端口替身
type disconnectionBlockingWriter struct {
	*fakeBatchWriter
	records *blockingDisconnectionStore
}

func (f *disconnectionBlockingWriter) GetConnectionRecordRepo() spi.ConnectionStore { return f.records }

// nilConnRecordWriter 连接仓储返回 nil 接口的替身（真实 Hub 未配置存储后端的形态，
// 纯 WS 部署场景）
type nilConnRecordWriter struct {
	*fakeBatchWriter
}

func (f *nilConnRecordWriter) GetConnectionRecordRepo() spi.ConnectionStore { return nil }

// errorDisconnectionStore 批量落库恒定失败的连接仓储替身（测 flush 错误日志路径）
type errorDisconnectionStore struct {
	*fakeConnStore
	err error
}

func (f *errorDisconnectionStore) BatchMarkDisconnected(_ context.Context, _ []*models.DisconnectionEntry) error {
	return f.err
}

// disconnectionErrorWriter 返回错误注入连接仓储的端口替身
type disconnectionErrorWriter struct {
	*fakeBatchWriter
	records *errorDisconnectionStore
}

func (f *disconnectionErrorWriter) GetConnectionRecordRepo() spi.ConnectionStore { return f.records }

// TestNewDisconnectionBatcher 验证创建后字段正确
func TestNewDisconnectionBatcher(t *testing.T) {
	w, _ := newDisconnectionTestHub(t)

	b := NewDisconnectionBatcher(w)

	require.NotNil(t, b, "创建的 batcher 不应为 nil")
	assert.Same(t, w, b.hub, "batcher.hub 应指向传入的端口")
	assert.NotNil(t, b.processor, "batcher.processor 不应为 nil")
}

// TestDisconnectionBatcher_SubmitSuccess 验证非阻塞提交成功
func TestDisconnectionBatcher_SubmitSuccess(t *testing.T) {
	w, _ := newDisconnectionTestHub(t)

	b := NewDisconnectionBatcher(w)
	defer b.Stop()

	ok := b.Submit(newDisconnectionEntry("conn-1", time.Now()))
	assert.True(t, ok, "提交断连终态条目应成功")
}

// TestDisconnectionBatcher_FlushEmptyBatch 验证 flush 空 batch 不 panic 且不落库
func TestDisconnectionBatcher_FlushEmptyBatch(t *testing.T) {
	w, fakeRepo := newDisconnectionTestHub(t)

	b := NewDisconnectionBatcher(w)

	assert.NotPanics(t, func() {
		b.flush(nil)
		b.flush([]*models.DisconnectionEntry{})
	}, "flush 空 batch 不应 panic")

	assert.Empty(t, fakeRepo.disconnectionsSnapshot(), "空 batch 不应调用 BatchMarkDisconnected")
}

// TestDisconnectionBatcher_FlushPassesBatchVerbatim 验证 flush 原样透传批次
// （批内同连接去重由仓储层 BatchMarkDisconnected 统一处理，本层不做变换）
func TestDisconnectionBatcher_FlushPassesBatchVerbatim(t *testing.T) {
	w, fakeRepo := newDisconnectionTestHub(t)

	b := NewDisconnectionBatcher(w)

	base := time.Now()
	batch := []*models.DisconnectionEntry{
		newDisconnectionEntry("conn-a", base),
		newDisconnectionEntry("conn-b", base.Add(time.Second)),
		newDisconnectionEntry("conn-a", base.Add(2*time.Second)), // 同连接重复：本层透传不去重
	}

	b.flush(batch)

	require.Equal(t, batch, fakeRepo.disconnectionsSnapshot(), "flush 应原样透传整批条目")
}

// TestDisconnectionBatcher_BatchFullTriggersFlush 验证满批自动触发 flush（凑批语义）
// 构造参数为包级常量（batch 200 / 1s），满 200 条即 flush，不等定时器
func TestDisconnectionBatcher_BatchFullTriggersFlush(t *testing.T) {
	w, fakeRepo := newDisconnectionTestHub(t)

	b := NewDisconnectionBatcher(w)
	defer b.Stop()

	for i := 0; i < disconnectionBatchSize; i++ {
		require.True(t, b.Submit(newDisconnectionEntry(fmt.Sprintf("conn-%d", i), time.Now())),
			"队列容量 8192 下提交应全部成功")
	}

	require.Eventually(t, func() bool {
		return len(fakeRepo.disconnectionsSnapshot()) == disconnectionBatchSize
	}, 2*time.Second, 5*time.Millisecond, "满 200 条应自动 flush")
}

// TestDisconnectionBatcher_StopFlushesPending 验证 Stop 前置 flush 剩余数据（停机不丢账）
func TestDisconnectionBatcher_StopFlushesPending(t *testing.T) {
	w, fakeRepo := newDisconnectionTestHub(t)

	b := NewDisconnectionBatcher(w)

	for i := 0; i < 5; i++ {
		require.True(t, b.Submit(newDisconnectionEntry("conn-shutdown", time.Now())))
	}

	b.Stop()

	require.Len(t, fakeRepo.disconnectionsSnapshot(), 5, "Stop 应 flush 全部 5 条残留条目")
}

// TestDisconnectionBatcher_StopMultipleTimes 验证多次调用 Stop 不 panic（停机幂等）
func TestDisconnectionBatcher_StopMultipleTimes(t *testing.T) {
	b := NewDisconnectionBatcher(newFakeBatchWriter())

	assert.NotPanics(t, func() {
		b.Stop()
		b.Stop()
		b.Stop()
	}, "多次调用 Stop 不应 panic")
}

// TestDisconnectionBatcher_QueueFullDrops 验证队列满背压：Submit 丢弃并计数
// 确定性构造：卡住 worker 首次 flush（batchSize=200 恰好消费完首批），
// 队列恢复空闲后恰好填满 8192 容量，第 8193 条必然丢弃
func TestDisconnectionBatcher_QueueFullDrops(t *testing.T) {
	w := newFakeBatchWriter()
	blockStore := &blockingDisconnectionStore{
		fakeConnStore: w.records,
		started:       make(chan struct{}),
		release:       make(chan struct{}),
	}
	b := NewDisconnectionBatcher(&disconnectionBlockingWriter{fakeBatchWriter: w, records: blockStore})

	// 首批 200 条：worker 收满触发 flush 并阻塞，此刻队列已排空
	for i := 0; i < disconnectionBatchSize; i++ {
		require.True(t, b.Submit(newDisconnectionEntry(fmt.Sprintf("conn-first-%d", i), time.Now())))
	}
	<-blockStore.started

	// worker 卡在 flush：队列恢复 8192 空闲容量，恰好填满
	for i := 0; i < disconnectionQueueSize; i++ {
		require.True(t, b.Submit(newDisconnectionEntry(fmt.Sprintf("conn-overflow-%d", i), time.Now())),
			"队列空闲容量内提交应成功")
	}
	// 第 8193 条：队列满，非阻塞 Submit 丢弃
	assert.False(t, b.Submit(newDisconnectionEntry("conn-dropped", time.Now())),
		"队列满时 Submit 应返回 false")
	assert.EqualValues(t, 1, b.processor.DroppedCount(), "丢弃应被计数")

	// 放行 worker 并停机：首批 200 + 队列 8192 全部落库，被丢弃的 1 条不落库
	close(blockStore.release)
	b.Stop()

	require.Len(t, blockStore.disconnectionsSnapshot(),
		disconnectionBatchSize+disconnectionQueueSize,
		"Stop 后落库条数应为首批 + 溢出队列之和")
}

// TestDisconnectionBatcher_NilRepoSkipsFlush 验证连接仓储未注入时 flush 跳过且不 panic
func TestDisconnectionBatcher_NilRepoSkipsFlush(t *testing.T) {
	w := &nilConnRecordWriter{newFakeBatchWriter()}

	b := NewDisconnectionBatcher(w)

	assert.NotPanics(t, func() {
		b.flush([]*models.DisconnectionEntry{newDisconnectionEntry("conn-1", time.Now())})
	}, "仓储为 nil 时 flush 不应 panic")

	assert.Empty(t, w.records.disconnectionsSnapshot(), "仓储为 nil 时不应有任何落库")
}

// TestDisconnectionBatcher_FlushStoreErrorNotPanics 验证仓储报错时 flush 走错误日志且不 panic
func TestDisconnectionBatcher_FlushStoreErrorNotPanics(t *testing.T) {
	w := newFakeBatchWriter()
	b := NewDisconnectionBatcher(&disconnectionErrorWriter{
		fakeBatchWriter: w,
		records:         &errorDisconnectionStore{fakeConnStore: w.records, err: errors.New("db down")},
	})

	assert.NotPanics(t, func() {
		b.flush([]*models.DisconnectionEntry{newDisconnectionEntry("conn-1", time.Now())})
	}, "落库失败时 flush 不应 panic")
}

// TestDisconnectionBatcher_ConcurrentSubmit 验证并发 Submit 安全且 Stop 后全量落库
func TestDisconnectionBatcher_ConcurrentSubmit(t *testing.T) {
	w, fakeRepo := newDisconnectionTestHub(t)

	b := NewDisconnectionBatcher(w)

	var accepted int64
	var wg sync.WaitGroup
	for g := 0; g < 25; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if b.Submit(newDisconnectionEntry(fmt.Sprintf("conn-%d-%d", g, i), time.Now())) {
					atomic.AddInt64(&accepted, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	b.Stop()

	require.EqualValues(t, atomic.LoadInt64(&accepted), int64(len(fakeRepo.disconnectionsSnapshot())),
		"Stop 后落库条数应等于被接受的提交条数")
}

// BenchmarkDisconnectionBatcher_SubmitFlush 断连终态提交+flush 管线基准
// 构造参数为包级常量（queue 8192 / batch 200 / 1s）：满 200 条自动 flush，
// µs 级提交循环不会等到 1s 定时器，全部 flush 由凑批触发，无需手动介入
func BenchmarkDisconnectionBatcher_SubmitFlush(b *testing.B) {
	w := newFakeBatchWriter()
	batcher := NewDisconnectionBatcher(w)

	clients := make([]string, disconnectionBatchSize)
	for i := range clients {
		clients[i] = fmt.Sprintf("bench-conn-%d", i)
	}
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchSinkBool = batcher.Submit(&models.DisconnectionEntry{
			ConnectionID:   clients[i%disconnectionBatchSize],
			ConnectedAt:    now,
			DisconnectedAt: now,
			Reason:         models.DisconnectReasonClientRequest,
			Code:           1000,
		})
	}
	b.StopTimer()
	batcher.Stop()
	benchSinkInt = len(w.records.disconnectionsSnapshot())
}

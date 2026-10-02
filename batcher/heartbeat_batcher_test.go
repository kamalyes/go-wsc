/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-02 16:20:01
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-02 16:20:01
 * @FilePath: \go-wsc\batcher\heartbeat_batcher_test.go
 * @Description: 心跳统计批量更新器测试
 *   - 创建 / 非阻塞 Submit / 空 batch flush 不落库
 *   - flush 按 clientID 去重（保留批内最后一条）并双表落库
 *     （时间戳落 connect 表、Ping 统计落 quality 表）
 *   - 满批自动 flush（凑批语义）
 *   - 队列满丢弃 + DroppedCount（卡住 quality 写入的确定性构造）
 *   - Stop flush 剩余（停机不丢账）
 *   - 双仓储缺失时不 panic
 *   - 并发 Submit 全量落库
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"context"
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

// newHeartbeatTestHub 创建带双仓储替身的端口替身（connect/quality 两 repo 均记录心跳）
func newHeartbeatTestHub(t *testing.T) (*fakeBatchWriter, *fakeConnStore, *fakeQualityStore) {
	t.Helper()
	w := newFakeBatchWriter()
	return w, w.records, w.quality
}

// blockingHeartbeatQualityStore 可阻塞的质量仓储替身：
// 首次 BatchUpdateHeartbeats 入口阻塞（卡住 worker flush 的确定性手段），
// release 关闭后放行并委托内层替身记录
type blockingHeartbeatQualityStore struct {
	*fakeQualityStore
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (f *blockingHeartbeatQualityStore) BatchUpdateHeartbeats(ctx context.Context, entries []*models.HeartbeatUpdateEntry) error {
	f.once.Do(func() { close(f.started) })
	<-f.release
	return f.fakeQualityStore.BatchUpdateHeartbeats(ctx, entries)
}

// heartbeatBlockingWriter 返回阻塞式质量仓储的端口替身（connect 仓储走原替身）
type heartbeatBlockingWriter struct {
	*fakeBatchWriter
	quality *blockingHeartbeatQualityStore
}

func (f *heartbeatBlockingWriter) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	return f.quality
}

// nilHeartbeatReposWriter 双仓储均返回 nil 接口的替身（未配置存储后端的形态）
type nilHeartbeatReposWriter struct {
	*fakeBatchWriter
}

func (f *nilHeartbeatReposWriter) GetConnectionRecordRepo() spi.ConnectionStore {
	return nil
}

func (f *nilHeartbeatReposWriter) GetConnectionQualityRepository() spi.ConnectionQualityStore {
	return nil
}

// TestNewHeartbeatStatsUpdater 验证创建后字段正确
func TestNewHeartbeatStatsUpdater(t *testing.T) {
	w, _, _ := newHeartbeatTestHub(t)

	u := NewHeartbeatStatsUpdater(w, 64, 8, 50*time.Millisecond)

	require.NotNil(t, u, "创建的更新器不应为 nil")
	assert.Same(t, w, u.hub, "updater.hub 应指向传入的端口")
	assert.NotNil(t, u.processor, "updater.processor 不应为 nil")
}

// TestHeartbeatStatsUpdater_SubmitSuccess 验证非阻塞提交成功
func TestHeartbeatStatsUpdater_SubmitSuccess(t *testing.T) {
	w, _, _ := newHeartbeatTestHub(t)

	u := NewHeartbeatStatsUpdater(w, 100, 100, 10*time.Second)
	defer u.Stop()

	ok := u.Submit(&HeartbeatStatsEntry{ClientID: "conn-1", PingTime: time.Now(), PingMs: 12.5})
	assert.True(t, ok, "提交心跳统计应成功")
}

// TestHeartbeatStatsUpdater_FlushEmptyBatch 验证 flush 空 batch 不 panic 且双表均不落库
func TestHeartbeatStatsUpdater_FlushEmptyBatch(t *testing.T) {
	w, recordsRepo, qualityRepo := newHeartbeatTestHub(t)

	u := NewHeartbeatStatsUpdater(w, 10, 10, 10*time.Second)

	assert.NotPanics(t, func() {
		u.flush(nil)
		u.flush([]*HeartbeatStatsEntry{})
	}, "flush 空 batch 不应 panic")

	assert.Empty(t, recordsRepo.heartbeatsSnapshot(), "空 batch 不应调用 connect 表写入")
	assert.Empty(t, qualityRepo.heartbeatsSnapshot(), "空 batch 不应调用 quality 表写入")
}

// TestHeartbeatStatsUpdater_FlushDeduplicatesByClientID 验证 flush 去重与双表落库正确性
// 语义为「同 clientID 保留批内最后一条」（后出现覆盖先出现），非按 PingTime 取最新；
// 去重结果应同时写入 connect 表（时间戳）与 quality 表（Ping 统计）
func TestHeartbeatStatsUpdater_FlushDeduplicatesByClientID(t *testing.T) {
	w, recordsRepo, qualityRepo := newHeartbeatTestHub(t)

	u := NewHeartbeatStatsUpdater(w, 100, 100, 10*time.Second)

	base := time.Now()
	batch := []*HeartbeatStatsEntry{
		{ClientID: "conn-a", PingTime: base.Add(1 * time.Second), PingMs: 10},
		{ClientID: "conn-b", PingTime: base, PingMs: 99},
		{ClientID: "conn-a", PingTime: base.Add(2 * time.Second), PingMs: 20},
		{ClientID: "conn-a", PingTime: base, PingMs: 15}, // 批内最后一条覆盖前两条
	}

	u.flush(batch)

	snapshots := map[string][]*models.HeartbeatUpdateEntry{
		"connect 表": recordsRepo.heartbeatsSnapshot(),
		"quality 表": qualityRepo.heartbeatsSnapshot(),
	}
	for name, snap := range snapshots {
		require.Len(t, snap, 2, "%s 应收到去重后的 2 条", name)

		agg := make(map[string]*models.HeartbeatUpdateEntry, len(snap))
		for _, e := range snap {
			agg[e.ConnectionID] = e
		}

		a := agg["conn-a"]
		require.NotNil(t, a, "%s 应包含 conn-a", name)
		assert.Equal(t, 15.0, a.PingMs, "%s: conn-a 应保留批内最后一条（PingMs=15）", name)
		require.NotNil(t, a.PingTime, "%s: conn-a 的 PingTime 不应为 nil", name)
		assert.Equal(t, base, *a.PingTime, "%s: conn-a 的 PingTime 应为最后一条的时间", name)

		bEntry := agg["conn-b"]
		require.NotNil(t, bEntry, "%s 应包含 conn-b", name)
		assert.Equal(t, 99.0, bEntry.PingMs)
		assert.NotNil(t, bEntry.PingTime)
	}
}

// TestHeartbeatStatsUpdater_BatchFullTriggersFlush 验证满批自动触发 flush（凑批语义）
func TestHeartbeatStatsUpdater_BatchFullTriggersFlush(t *testing.T) {
	w, recordsRepo, qualityRepo := newHeartbeatTestHub(t)

	// flushInterval=1h 关闭定时 flush，只有满 2 条才触发
	u := NewHeartbeatStatsUpdater(w, 100, 2, time.Hour)
	defer u.Stop()

	base := time.Now()
	require.True(t, u.Submit(&HeartbeatStatsEntry{ClientID: "conn-1", PingTime: base, PingMs: 11}))
	require.True(t, u.Submit(&HeartbeatStatsEntry{ClientID: "conn-2", PingTime: base, PingMs: 22}))

	require.Eventually(t, func() bool {
		return len(qualityRepo.heartbeatsSnapshot()) == 2
	}, 2*time.Second, 5*time.Millisecond, "满 2 条应自动 flush 到 quality 表")

	require.Len(t, recordsRepo.heartbeatsSnapshot(), 2, "connect 表应同步收到 2 条")
}

// TestHeartbeatStatsUpdater_StopFlushesPending 验证 Stop 前置 flush 剩余数据（停机不丢账）
func TestHeartbeatStatsUpdater_StopFlushesPending(t *testing.T) {
	w, recordsRepo, qualityRepo := newHeartbeatTestHub(t)

	// 长 flushInterval 确保只能靠 Stop 触发 flush
	u := NewHeartbeatStatsUpdater(w, 100, 10, time.Hour)

	for i := 0; i < 3; i++ {
		require.True(t, u.Submit(&HeartbeatStatsEntry{
			ClientID: fmt.Sprintf("conn-stop-%d", i),
			PingTime: time.Now(),
			PingMs:   float64(i),
		}))
	}

	u.Stop()

	require.Len(t, recordsRepo.heartbeatsSnapshot(), 3, "Stop 应 flush 残留到 connect 表")
	require.Len(t, qualityRepo.heartbeatsSnapshot(), 3, "Stop 应 flush 残留到 quality 表")
}

// TestHeartbeatStatsUpdater_QueueFullDrops 验证队列满背压：Submit 丢弃并计数
// 确定性构造：queueSize=1 / batchSize=1，卡住 worker 的 quality 写入，
// 队列恢复 1 格空闲后第 2 条入队、第 3 条必然丢弃
func TestHeartbeatStatsUpdater_QueueFullDrops(t *testing.T) {
	w := newFakeBatchWriter()
	blockQuality := &blockingHeartbeatQualityStore{
		fakeQualityStore: w.quality,
		started:          make(chan struct{}),
		release:          make(chan struct{}),
	}

	u := NewHeartbeatStatsUpdater(&heartbeatBlockingWriter{fakeBatchWriter: w, quality: blockQuality}, 1, 1, time.Hour)

	// 第 1 条：worker 取出即触发 flush，connect 表先写、quality 写入阻塞，队列排空
	require.True(t, u.Submit(&HeartbeatStatsEntry{ClientID: "conn-block", PingTime: time.Now(), PingMs: 1}))
	<-blockQuality.started

	// 队列空闲容量=1：第 2 条入队，第 3 条丢弃
	require.True(t, u.Submit(&HeartbeatStatsEntry{ClientID: "conn-2", PingTime: time.Now(), PingMs: 2}))
	assert.False(t, u.Submit(&HeartbeatStatsEntry{ClientID: "conn-3", PingTime: time.Now(), PingMs: 3}),
		"队列满时 Submit 应返回 false")
	assert.EqualValues(t, 1, u.processor.DroppedCount(), "丢弃应被计数")

	close(blockQuality.release)
	u.Stop()

	// 首条（阻塞的那次）+ Stop drain 的第 2 条：双表各 2 条，被丢弃的第 3 条不落库
	require.Len(t, blockQuality.heartbeatsSnapshot(), 2, "quality 表最终应有 2 条")
	require.Len(t, w.records.heartbeatsSnapshot(), 2, "connect 表最终应有 2 条")
}

// TestHeartbeatStatsUpdater_NilReposSkipsFlush 验证双仓储缺失时 flush 跳过且不 panic
func TestHeartbeatStatsUpdater_NilReposSkipsFlush(t *testing.T) {
	u := NewHeartbeatStatsUpdater(&nilHeartbeatReposWriter{newFakeBatchWriter()}, 10, 10, time.Hour)

	assert.NotPanics(t, func() {
		u.flush([]*HeartbeatStatsEntry{{ClientID: "conn-1", PingTime: time.Now(), PingMs: 1}})
	}, "双仓储为 nil 时 flush 不应 panic")
}

// TestHeartbeatStatsUpdater_ConcurrentSubmit 验证并发 Submit 安全且 Stop 后全量落库
func TestHeartbeatStatsUpdater_ConcurrentSubmit(t *testing.T) {
	w, recordsRepo, qualityRepo := newHeartbeatTestHub(t)

	u := NewHeartbeatStatsUpdater(w, 4096, 100, time.Hour)

	var accepted int64
	var wg sync.WaitGroup
	for g := 0; g < 25; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				if u.Submit(&HeartbeatStatsEntry{
					ClientID: fmt.Sprintf("conn-%d-%d", g, i),
					PingTime: time.Now(),
					PingMs:   float64(i),
				}) {
					atomic.AddInt64(&accepted, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	u.Stop()

	require.EqualValues(t, atomic.LoadInt64(&accepted), int64(len(qualityRepo.heartbeatsSnapshot())),
		"Stop 后 quality 表落库条数应等于被接受条数")
	require.EqualValues(t, atomic.LoadInt64(&accepted), int64(len(recordsRepo.heartbeatsSnapshot())),
		"Stop 后 connect 表落库条数应等于被接受条数")
}

// BenchmarkHeartbeatStatsUpdater_SubmitFlush 心跳提交+去重 flush 摊销成本基准
// batchSize=65536 / interval=1h：worker 不自动 flush、不等定时器，
// 由基准循环每 200 条手动调同包 flush（200 个不同客户端去重后双表落库）
func BenchmarkHeartbeatStatsUpdater_SubmitFlush(b *testing.B) {
	w := newFakeBatchWriter()
	u := NewHeartbeatStatsUpdater(w, 1<<14, 65536, time.Hour)

	clients := make([]string, 200)
	for i := range clients {
		clients[i] = fmt.Sprintf("bench-hb-%d", i)
	}
	pingAt := time.Now()

	const benchBatch = 200
	batch := make([]*HeartbeatStatsEntry, 0, benchBatch)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		entry := &HeartbeatStatsEntry{ClientID: clients[i%benchBatch], PingTime: pingAt, PingMs: 12.5}
		benchSinkBool = u.Submit(entry)
		batch = append(batch, entry)
		if len(batch) == benchBatch {
			u.flush(batch)
			batch = batch[:0]
		}
	}
	b.StopTimer()
	if len(batch) > 0 {
		u.flush(batch)
	}
	u.Stop()
	benchSinkInt = len(w.records.heartbeatsSnapshot())
}

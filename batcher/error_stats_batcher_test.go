/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-29 10:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-02 16:20:01
 * @FilePath: \go-wsc\batcher\error_stats_batcher_test.go
 * @Description: 连接错误统计批量更新器测试
 *   - NewErrorStatsBatcher 创建
 *   - Submit 提交多条
 *   - flush 按 connectionID 聚合（count 累加、LastError/LastErrorAt 保留最新）
 *   - Stop 前置 flush 剩余数据（停机不丢账）
 *   - 质量仓储未注入时 flush 跳过不 panic
 *   使用 fakeQualityStore 的 BatchAddErrors 验证聚合结果
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// TestNewErrorStatsBatcher 验证 NewErrorStatsBatcher 创建后字段正确
func TestNewErrorStatsBatcher(t *testing.T) {
	w, _ := newStatsTestHub(t)

	b := NewErrorStatsBatcher(w, 64, 8, 50*time.Millisecond)

	require.NotNil(t, b, "创建的 batcher 不应为 nil")
	assert.Same(t, w, b.hub, "batcher.hub 应指向传入的端口")
	assert.NotNil(t, b.processor, "batcher.processor 不应为 nil")
}

// TestErrorStatsBatcher_SubmitSuccess 验证 Submit 成功提交非空 item
func TestErrorStatsBatcher_SubmitSuccess(t *testing.T) {
	w, _ := newStatsTestHub(t)

	b := NewErrorStatsBatcher(w, 100, 100, 10*time.Second)

	ok := b.Submit(&ErrorStatsItem{
		ConnectionID: "conn-1",
		Error:        errors.New("read tcp 10.0.0.1:443: connection reset by peer"),
		ErrorAt:      time.Now(),
	})
	assert.True(t, ok, "提交 item 应成功")
}

// TestErrorStatsBatcher_FlushEmptyBatch 验证 flush 空 batch 不 panic 且不调用仓储
func TestErrorStatsBatcher_FlushEmptyBatch(t *testing.T) {
	w, fakeRepo := newStatsTestHub(t)

	b := NewErrorStatsBatcher(w, 10, 10, 10*time.Second)

	assert.NotPanics(t, func() {
		b.flush(nil)
		b.flush([]*ErrorStatsItem{})
	}, "flush 空 batch 不应 panic")

	assert.Empty(t, fakeRepo.errUpdatesSnapshot(), "空 batch 不应调用 BatchAddErrors")
}

// TestErrorStatsBatcher_FlushAggregatesByConnectionID 验证 flush 按 connectionID 聚合：
// 同连接多次错误合并为一条（ErrorCount 累加），LastError/LastErrorAt 保留时间最新的一次
func TestErrorStatsBatcher_FlushAggregatesByConnectionID(t *testing.T) {
	w, fakeRepo := newStatsTestHub(t)

	b := NewErrorStatsBatcher(w, 100, 100, 10*time.Second)

	base := time.Now()
	errOld := errors.New("旧错误：write deadline exceeded")
	errNew := errors.New("新错误：unexpected EOF")

	// 场景：conn-a 两次错误（时间序错乱提交，聚合应按 ErrorAt 取最新而非提交序）、
	// conn-b 一次、conn-a 与 conn-c 各一次 —— 断连风暴的典型批次形态
	batch := []*ErrorStatsItem{
		{ConnectionID: "conn-a", Error: errOld, ErrorAt: base},
		{ConnectionID: "conn-b", Error: errors.New("err-b1"), ErrorAt: base.Add(1 * time.Second)},
		{ConnectionID: "conn-a", Error: errNew, ErrorAt: base.Add(2 * time.Second)},
		{ConnectionID: "conn-c", Error: errors.New("err-c1"), ErrorAt: base.Add(3 * time.Second)},
		{ConnectionID: "conn-b", Error: errors.New("err-b2"), ErrorAt: base.Add(4 * time.Second)},
	}

	b.flush(batch)

	entries := fakeRepo.errUpdatesSnapshot()
	require.Len(t, entries, 3, "flush 后应聚合为 3 个 connectionID")

	agg := make(map[string]*models.ErrorUpdateEntry, len(entries))
	for _, e := range entries {
		agg[e.ConnectionID] = e
	}

	// conn-a：两次错误合并，count=2，保留 ErrorAt 更新的 errNew
	a := agg["conn-a"]
	require.NotNil(t, a, "conn-a 应存在聚合条目")
	assert.EqualValues(t, 2, a.ErrorCount, "conn-a 两次错误应合并为 count=2")
	assert.Equal(t, errNew.Error(), a.LastError, "conn-a 应保留时间最新的错误消息")
	assert.Equal(t, base.Add(2*time.Second), a.LastErrorAt, "conn-a 的 LastErrorAt 应为最新一次错误时间")

	// conn-b：两次错误合并，count=2，保留后提交的 err-b2
	bEntry := agg["conn-b"]
	require.NotNil(t, bEntry, "conn-b 应存在聚合条目")
	assert.EqualValues(t, 2, bEntry.ErrorCount)
	assert.Equal(t, "err-b2", bEntry.LastError)
	assert.Equal(t, base.Add(4*time.Second), bEntry.LastErrorAt)

	// conn-c：单次错误，count=1
	cEntry := agg["conn-c"]
	require.NotNil(t, cEntry, "conn-c 应存在聚合条目")
	assert.EqualValues(t, 1, cEntry.ErrorCount)
	assert.Equal(t, "err-c1", cEntry.LastError)
}

// TestErrorStatsBatcher_StopFlushesPending 验证 Stop 停机前 flush 队列中的残留条目（不丢账）
func TestErrorStatsBatcher_StopFlushesPending(t *testing.T) {
	w, fakeRepo := newStatsTestHub(t)

	// 长 flushInterval 确保只能靠 Stop 触发 flush
	b := NewErrorStatsBatcher(w, 100, 100, time.Hour)

	for i := 0; i < 3; i++ {
		require.True(t, b.Submit(&ErrorStatsItem{
			ConnectionID: "conn-shutdown",
			Error:        errors.New("shutdown flush"),
			ErrorAt:      time.Now(),
		}), "提交应成功")
	}

	b.Stop()

	entries := fakeRepo.errUpdatesSnapshot()
	require.Len(t, entries, 1, "Stop 应 flush 残留并按连接聚合为 1 条")
	assert.EqualValues(t, 3, entries[0].ErrorCount, "3 条残留应合并 count=3")
}

// TestErrorStatsBatcher_NilRepoSkipsFlush 验证质量仓储未注入时 flush 跳过且不 panic
func TestErrorStatsBatcher_NilRepoSkipsFlush(t *testing.T) {
	// 覆盖 GetConnectionQualityRepository 返回 nil 接口（真实 Hub 未注入存储后端时的形态，
	// 纯 WS 部署场景）；注意不可用零值 fakeBatchWriter 模拟 —— 其 quality 为具体指针类型，
	// 包装成接口后是 typed-nil，`repo != nil` 无法识别，超出 flush 的防御契约
	w := &nilQualityWriter{newFakeBatchWriter()}

	b := NewErrorStatsBatcher(w, 10, 10, 10*time.Second)

	assert.NotPanics(t, func() {
		b.flush([]*ErrorStatsItem{
			{ConnectionID: "conn-1", Error: errors.New("boom"), ErrorAt: time.Now()},
		})
	}, "仓储为 nil 时 flush 不应 panic")
}

// nilQualityWriter 质量仓储返回 nil 接口的替身（模拟未配置存储后端）
type nilQualityWriter struct {
	*fakeBatchWriter
}

func (f *nilQualityWriter) GetConnectionQualityRepository() spi.ConnectionQualityStore { return nil }

// BenchmarkErrorStatsBatcher_SubmitFlush 提交+聚合 flush 摊销成本基准
// batchSize=65536 / interval=1h：worker 不自动 flush、不等定时器，
// 由基准循环每 200 条手动调同包 flush（200 个不同连接聚合为 ≤200 条 UPDATE）
func BenchmarkErrorStatsBatcher_SubmitFlush(b *testing.B) {
	w := newFakeBatchWriter()
	u := NewErrorStatsBatcher(w, 1<<14, 65536, time.Hour)

	conns := make([]string, 200)
	for i := range conns {
		conns[i] = fmt.Sprintf("bench-err-%d", i)
	}
	benchErr := errors.New("bench: connection reset by peer")
	errAt := time.Now()

	const benchBatch = 200
	batch := make([]*ErrorStatsItem, 0, benchBatch)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		item := &ErrorStatsItem{ConnectionID: conns[i%benchBatch], Error: benchErr, ErrorAt: errAt}
		benchSinkBool = u.Submit(item)
		batch = append(batch, item)
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
	benchSinkInt = len(w.quality.errUpdatesSnapshot())
}

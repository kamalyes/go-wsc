/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 00:00:00
 * @FilePath: \go-wsc\hub\message_stats_batcher_test.go
 * @Description: 消息统计批量更新器测试
 *   - NewMessageStatsBatcher 创建
 *   - Submit 提交多条
 *   - flush 按 connectionID 聚合
 *   - Stop 停止
 *   使用 fakeConnRecordRepo 的 BatchIncrementStats 验证聚合结果
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/repository"
)

// newStatsTestHub 创建带 fakeConnRecordRepo 的测试 Hub
func newStatsTestHub(t *testing.T) (*Hub, *fakeConnRecordRepo, func()) {
	t.Helper()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)

	hub := NewHub(config)
	fakeRepo := &fakeConnRecordRepo{}
	hub.connectionRecordRepo = fakeRepo

	cleanup := func() {
		hub.Shutdown()
	}
	return hub, fakeRepo, cleanup
}

// TestNewMessageStatsBatcher 验证 NewMessageStatsBatcher 创建后字段正确
func TestNewMessageStatsBatcher(t *testing.T) {
	hub, _, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 64, 8, 50*time.Millisecond)

	require.NotNil(t, batcher, "创建的 batcher 不应为 nil")
	assert.Same(t, hub, batcher.hub, "batcher.hub 应指向传入的 hub")
	assert.NotNil(t, batcher.processor, "batcher.processor 不应为 nil")
}

// TestMessageStatsBatcher_SubmitSuccess 验证 Submit 成功提交非空 item
func TestMessageStatsBatcher_SubmitSuccess(t *testing.T) {
	hub, _, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 100, 100, 10*time.Second)

	item := &statsIncrementItem{
		ConnectionID:     "conn-1",
		MessagesSent:     1,
		MessagesReceived: 0,
		BytesSent:        128,
		BytesReceived:    0,
	}
	ok := batcher.Submit(item)
	assert.True(t, ok, "提交 item 应成功")
}

// TestMessageStatsBatcher_FlushEmptyBatch 验证 flush 空 batch 不 panic 且不调用 repo
func TestMessageStatsBatcher_FlushEmptyBatch(t *testing.T) {
	hub, fakeRepo, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 10, 10, 10*time.Second)

	assert.NotPanics(t, func() {
		batcher.flush(nil)
		batcher.flush([]*statsIncrementItem{})
	}, "flush 空 batch 不应 panic")

	assert.Empty(t, fakeRepo.getIncrementEntries(), "空 batch 不应调用 BatchIncrementStats")
}

// TestMessageStatsBatcher_FlushAggregatesByConnectionID 验证 flush 按 connectionID 正确聚合
func TestMessageStatsBatcher_FlushAggregatesByConnectionID(t *testing.T) {
	hub, fakeRepo, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 100, 100, 10*time.Second)

	batch := []*statsIncrementItem{
		{ConnectionID: "conn-a", MessagesSent: 2, MessagesReceived: 1, BytesSent: 200, BytesReceived: 100},
		{ConnectionID: "conn-b", MessagesSent: 1, MessagesReceived: 0, BytesSent: 50, BytesReceived: 0},
		{ConnectionID: "conn-a", MessagesSent: 3, MessagesReceived: 2, BytesSent: 300, BytesReceived: 200},
		{ConnectionID: "conn-c", MessagesSent: 0, MessagesReceived: 5, BytesSent: 0, BytesReceived: 500},
		{ConnectionID: "conn-b", MessagesSent: 4, MessagesReceived: 1, BytesSent: 400, BytesReceived: 150},
	}

	batcher.flush(batch)

	entries := fakeRepo.getIncrementEntries()
	require.Len(t, entries, 3, "flush 后应聚合为 3 个 connectionID")

	aggMap := make(map[string]*repository.StatsIncrementEntry, len(entries))
	for _, e := range entries {
		aggMap[e.ConnectionID] = e
	}

	require.Contains(t, aggMap, "conn-a")
	require.Contains(t, aggMap, "conn-b")
	require.Contains(t, aggMap, "conn-c")

	a := aggMap["conn-a"]
	assert.Equal(t, int64(5), a.MessagesSent, "conn-a: MessagesSent 应聚合 2+3=5")
	assert.Equal(t, int64(3), a.MessagesReceived, "conn-a: MessagesReceived 应聚合 1+2=3")
	assert.Equal(t, int64(500), a.BytesSent, "conn-a: BytesSent 应聚合 200+300=500")
	assert.Equal(t, int64(300), a.BytesReceived, "conn-a: BytesReceived 应聚合 100+200=300")

	b := aggMap["conn-b"]
	assert.Equal(t, int64(5), b.MessagesSent, "conn-b: MessagesSent 应聚合 1+4=5")
	assert.Equal(t, int64(1), b.MessagesReceived, "conn-b: MessagesReceived 应聚合 0+1=1")
	assert.Equal(t, int64(450), b.BytesSent, "conn-b: BytesSent 应聚合 50+400=450")
	assert.Equal(t, int64(150), b.BytesReceived, "conn-b: BytesReceived 应聚合 0+150=150")

	c := aggMap["conn-c"]
	assert.Equal(t, int64(0), c.MessagesSent)
	assert.Equal(t, int64(5), c.MessagesReceived)
	assert.Equal(t, int64(0), c.BytesSent)
	assert.Equal(t, int64(500), c.BytesReceived)
}

// TestMessageStatsBatcher_FlushDoesNotMutateOriginal 验证 flush 不修改原始 batch 中的 item
func TestMessageStatsBatcher_FlushDoesNotMutateOriginal(t *testing.T) {
	hub, _, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 100, 100, 10*time.Second)

	orig := &statsIncrementItem{
		ConnectionID:     "conn-x",
		MessagesSent:     1,
		MessagesReceived: 1,
		BytesSent:        10,
		BytesReceived:    20,
	}
	batch := []*statsIncrementItem{orig, orig}

	batcher.flush(batch)

	assert.Equal(t, int64(1), orig.MessagesSent, "原始 item 不应被 flush 修改")
	assert.Equal(t, int64(1), orig.MessagesReceived)
	assert.Equal(t, int64(10), orig.BytesSent)
	assert.Equal(t, int64(20), orig.BytesReceived)
}

// TestMessageStatsBatcher_SubmitAndAutoFlush 验证 Submit 后自动 flush 写入 repo
func TestMessageStatsBatcher_SubmitAndAutoFlush(t *testing.T) {
	hub, fakeRepo, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 100, 2, 50*time.Millisecond)

	batcher.Submit(&statsIncrementItem{
		ConnectionID: "conn-auto-1", MessagesSent: 1, BytesSent: 100,
	})
	batcher.Submit(&statsIncrementItem{
		ConnectionID: "conn-auto-2", MessagesSent: 1, BytesSent: 200,
	})
	batcher.Submit(&statsIncrementItem{
		ConnectionID: "conn-auto-1", MessagesSent: 1, BytesSent: 300,
	})

	require.Eventually(t, func() bool {
		entries := fakeRepo.getIncrementEntries()
		total := int64(0)
		for _, e := range entries {
			if e.ConnectionID == "conn-auto-1" {
				total += e.MessagesSent
			}
		}
		return total >= 2
	}, 2*time.Second, 20*time.Millisecond, "conn-auto-1 的两次提交应最终被写入 repo（可能分多次 flush）")

	entries := fakeRepo.getIncrementEntries()
	aggMap := make(map[string]*repository.StatsIncrementEntry)
	for _, e := range entries {
		existing, ok := aggMap[e.ConnectionID]
		if !ok {
			cp := *e
			aggMap[e.ConnectionID] = &cp
		} else {
			existing.MessagesSent += e.MessagesSent
			existing.MessagesReceived += e.MessagesReceived
			existing.BytesSent += e.BytesSent
			existing.BytesReceived += e.BytesReceived
		}
	}
	require.Contains(t, aggMap, "conn-auto-1")
	assert.Equal(t, int64(2), aggMap["conn-auto-1"].MessagesSent, "conn-auto-1 跨多次 flush 汇总应为 1+1=2")
	assert.Equal(t, int64(400), aggMap["conn-auto-1"].BytesSent, "conn-auto-1 BytesSent 跨多次 flush 汇总应为 100+300=400")
	require.Contains(t, aggMap, "conn-auto-2")
	assert.Equal(t, int64(1), aggMap["conn-auto-2"].MessagesSent)
	assert.Equal(t, int64(200), aggMap["conn-auto-2"].BytesSent)
}

// TestMessageStatsBatcher_StopFlushesRemaining 验证 Stop 时 flush 剩余未处理数据
func TestMessageStatsBatcher_StopFlushesRemaining(t *testing.T) {
	hub, fakeRepo, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 1000, 1000, 10*time.Second)

	for i := 0; i < 5; i++ {
		batcher.Submit(&statsIncrementItem{
			ConnectionID:     "conn-stop",
			MessagesSent:     1,
			MessagesReceived: 1,
			BytesSent:        10,
			BytesReceived:    20,
		})
	}

	done := make(chan struct{})
	go func() {
		batcher.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 应在 3s 内完成")
	}

	entries := fakeRepo.getIncrementEntries()
	require.Len(t, entries, 1, "Stop 应 flush 剩余数据并聚合成 1 条")
	assert.Equal(t, "conn-stop", entries[0].ConnectionID)
	assert.Equal(t, int64(5), entries[0].MessagesSent)
	assert.Equal(t, int64(5), entries[0].MessagesReceived)
	assert.Equal(t, int64(50), entries[0].BytesSent)
	assert.Equal(t, int64(100), entries[0].BytesReceived)
}

// TestMessageStatsBatcher_StopMultipleTimes 验证多次调用 Stop 不 panic
func TestMessageStatsBatcher_StopMultipleTimes(t *testing.T) {
	hub, _, cleanup := newStatsTestHub(t)
	defer cleanup()

	batcher := NewMessageStatsBatcher(hub, 10, 10, 10*time.Second)

	assert.NotPanics(t, func() {
		batcher.Stop()
		batcher.Stop()
		batcher.Stop()
	}, "多次调用 Stop 不应 panic")
}

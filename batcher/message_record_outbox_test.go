/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 11:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-02 16:20:01
 * @FilePath: \go-wsc\batcher\message_record_outbox_test.go
 * @Description: 消息记录攒批 outbox 测试 —— flush 成功/失败路径的 pending 清理与 ACK 注册
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

// outboxTestProvider outbox 最小依赖替身（sink + logger 两方法端口）
// sink 为接口类型：除 fakeMessageSink 外，测试可用自定义 sink 注入时序钩子
type outboxTestProvider struct {
	sink   spi.MessageSink
	logger spi.Logger
}

func (p *outboxTestProvider) GetMessageSink() spi.MessageSink { return p.sink }
func (p *outboxTestProvider) GetLogger() spi.Logger           { return p.logger }

// newOutboxForTest 构造小参数 outbox（5ms flush 间隔，测试快速触发攒批路径）
func newOutboxForTest(t *testing.T, sink *fakeMessageSink) *MessageRecordOutbox {
	t.Helper()
	return NewMessageRecordOutbox(
		&outboxTestProvider{sink: sink, logger: spi.NewDefaultLogger()},
		16, 8, 5*time.Millisecond,
	)
}

// newOutboxRecord 构造一条 sending 状态的待落库记录
func newOutboxRecord(id, receiver string) *models.MessageSendRecord {
	return &models.MessageSendRecord{
		MessageID: id,
		Receiver:  receiver,
		Status:    models.MessageSendStatusSending,
	}
}

// outboxPendingLen 读取 pending 当前条数（白盒）
func outboxPendingLen(o *MessageRecordOutbox) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending)
}

// TestOutboxFlushFailureClearsPending 落库失败批次必须从 pending 移除
// 回归场景：DB 故障期间持续 flush 失败，残留 pending 让 UpdateStatusIfPending
// 永远命中已死记录，且 map 无界增长（每条含序列化 MessageData）
func TestOutboxFlushFailureClearsPending(t *testing.T) {
	sink := &fakeMessageSink{createErr: errors.New("db down")}
	o := newOutboxForTest(t, sink)
	defer o.Stop()

	rec := newOutboxRecord("m-fail", "u-1")
	require.True(t, o.Submit(rec))

	require.Eventually(t, func() bool {
		return outboxPendingLen(o) == 0
	}, 2*time.Second, 5*time.Millisecond, "落库失败批次应从 pending 移除")

	// 已丢弃记录不再被状态合并命中（修复前会永远命中死记录）
	key := models.MessageRecordKey{MessageID: rec.MessageID, Receiver: rec.Receiver}
	assert.False(t, o.UpdateStatusIfPending(key, models.MessageSendStatusSuccess, "", ""),
		"已随失败批次丢弃的记录不应再命中 pending 合并")
}

// TestOutboxFlushSuccessRegistersAckTimeout 成功路径：pending 清理 + 仅 sending 记录注册 ACK 超时
func TestOutboxFlushSuccessRegistersAckOnlySending(t *testing.T) {
	sink := &fakeMessageSink{}
	o := newOutboxForTest(t, sink)
	defer o.Stop()

	var mu sync.Mutex
	var ackKeys []models.MessageRecordKey
	o.OnFlushed(func(keys []models.MessageRecordKey) {
		mu.Lock()
		ackKeys = append(ackKeys, keys...)
		mu.Unlock()
	})

	sending := newOutboxRecord("m-send", "u-1")
	done := newOutboxRecord("m-done", "u-2")
	done.Status = models.MessageSendStatusSuccess
	require.True(t, o.Submit(sending))
	require.True(t, o.Submit(done))

	require.Eventually(t, func() bool {
		return len(sink.createdSnapshot()) == 2
	}, 2*time.Second, 5*time.Millisecond, "两条记录应成功落库")

	assert.Equal(t, 0, outboxPendingLen(o), "成功 flush 后 pending 应清空")

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t,
		[]models.MessageRecordKey{{MessageID: "m-send", Receiver: "u-1"}},
		ackKeys, "仅 sending 状态记录注册 ACK 超时",
	)
}

// hookedCreateBatchSink 在 CreateBatch 入口逐字段读取记录（模拟 GORM 序列化）
// 并在读取完成后阻塞，供测试在"序列化进行中"注入并发状态合并
type hookedCreateBatchSink struct {
	fakeMessageSink
	inCreate chan struct{} // CreateBatch 完成字段读取后通知
	release  chan struct{} // CreateBatch 阻塞至此放行
}

func (s *hookedCreateBatchSink) CreateBatch(ctx context.Context, records []*models.MessageSendRecord) error {
	// 模拟序列化：遍历读取记录字段——修复前此处与并发 UpdateStatusIfPending
	// 写同一 record 指针构成数据竞争（-race 可复现）
	for _, r := range records {
		_ = r.MessageID + "|" + r.Receiver + "|" + string(r.Status) + "|" + r.ErrorMessage
	}
	s.inCreate <- struct{}{}
	<-s.release
	return s.fakeMessageSink.CreateBatch(ctx, records)
}

// TestOutboxFlushSnapshotIsolatesConcurrentUpdate 落库序列化与并发状态合并的数据竞争
// 回归场景：CreateBatch 在锁外执行（pending 清理在落库后），序列化期间并发
// UpdateStatusIfPending 命中 pending 写同一 record 指针——修复前 -race 报告竞争，
// 修复后 flush 入口锁内快照，落库内容与并发合并互不干扰，语义不变
func TestOutboxFlushSnapshotIsolatesConcurrentUpdate(t *testing.T) {
	sink := &hookedCreateBatchSink{
		inCreate: make(chan struct{}),
		release:  make(chan struct{}),
	}
	// flush 间隔取 1 小时：阻止后台定时 flush 干扰，手动调用 o.flush 控制时序
	o := NewMessageRecordOutbox(
		&outboxTestProvider{sink: sink, logger: spi.NewDefaultLogger()},
		16, 8, time.Hour,
	)
	defer o.Stop()

	var ackRegistered int32
	o.OnFlushed(func(keys []models.MessageRecordKey) {
		atomic.AddInt32(&ackRegistered, int32(len(keys)))
	})

	rec := newOutboxRecord("m-race", "u-1")
	// 白盒注入 pending（等价 Submit 后未 flush 的状态）：不经过 processor.Submit 入队，
	// 避免 Stop drain 时后台 worker 对同一记录二次 flush、与手动 flush 的
	// CreateBatch 时序钩子（inCreate 单次接收）形成交叉死锁
	key := models.MessageRecordKey{MessageID: rec.MessageID, Receiver: rec.Receiver}
	o.mu.Lock()
	o.pending[key] = rec
	o.mu.Unlock()

	flushDone := make(chan struct{})
	go func() {
		defer close(flushDone)
		o.flush([]*models.MessageSendRecord{rec})
	}()

	// 等 CreateBatch 字段读取完成（序列化进行中），此刻 pending 尚未清理
	<-sink.inCreate
	require.True(t, o.UpdateStatusIfPending(key, models.MessageSendStatusSuccess, "", ""),
		"序列化期间记录仍在 pending，状态合并应命中")

	close(sink.release)
	<-flushDone

	// 落库内容 = flush 入口快照（sending）；原指针保留并发合并的终态（供 ACK 注册判定）
	created := sink.createdSnapshot()
	require.Len(t, created, 1)
	assert.Equal(t, models.MessageSendStatusSending, created[0].Status,
		"落库内容应为 flush 入口快照，不受序列化期间的并发状态合并影响")
	assert.Equal(t, models.MessageSendStatusSuccess, rec.Status,
		"原指针保留并发合并的终态")
	assert.Equal(t, 0, outboxPendingLen(o), "flush 完成后 pending 应清空")
	assert.Equal(t, int32(0), atomic.LoadInt32(&ackRegistered),
		"落库时指针已变终态，不应注册 ACK 超时")
}

// BenchmarkMessageRecordOutbox_SubmitFlush 提交+批量落库 flush 摊销成本基准
// batchSize=65536 / interval=1h：worker 不自动 flush、不等定时器，
// 由基准循环每 200 条手动调同包 flush（快照拷贝 + CreateBatch + pending 清理）
func BenchmarkMessageRecordOutbox_SubmitFlush(b *testing.B) {
	sink := &fakeMessageSink{}
	o := NewMessageRecordOutbox(
		&outboxTestProvider{sink: sink, logger: spi.NewDefaultLogger()},
		1<<14, 65536, time.Hour,
	)

	msgIDs := make([]string, 200)
	for i := range msgIDs {
		msgIDs[i] = fmt.Sprintf("bench-msg-%d", i)
	}

	const benchBatch = 200
	batch := make([]*models.MessageSendRecord, 0, benchBatch)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		record := &models.MessageSendRecord{
			MessageID: msgIDs[i%benchBatch],
			Receiver:  "bench-user",
			Status:    models.MessageSendStatusSuccess, // 终态：flush 后不触发 onFlushed 回调
		}
		benchSinkBool = o.Submit(record)
		batch = append(batch, record)
		if len(batch) == benchBatch {
			o.flush(batch)
			batch = batch[:0]
		}
	}
	b.StopTimer()
	if len(batch) > 0 {
		o.flush(batch)
	}
	o.Stop()
	benchSinkInt = len(sink.createdSnapshot())
}

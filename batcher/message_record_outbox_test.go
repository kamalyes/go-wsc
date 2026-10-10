/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 11:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 11:30:00
 * @FilePath: \go-wsc\batcher\message_record_outbox_test.go
 * @Description: 消息记录攒批 outbox 测试 —— flush 成功/失败路径的 pending 清理与 ACK 注册
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// outboxTestProvider outbox 最小依赖替身（sink + logger 两方法端口）
type outboxTestProvider struct {
	sink   *fakeMessageSink
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

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 11:26:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 11:26:00
 * @FilePath: \go-wsc\batcher\message_record_outbox.go
 * @Description: 消息记录攒批 outbox（write-ahead INSERT 攒批化）
 *
 * 写放大治理：P2P 每条消息一次 DB INSERT，高吞吐下 MySQL 先于一切熔断
 * 本组件将逐条 Create 攒批（满 batchSize 或每 flushInterval 一次
 * CreateBatch 批量 INSERT，200 条/50ms 量级下写放大降低 2 个数量级），
 * 攒批调度复用 syncx.BatchProcessor（满批即时 flush / 定时 flush / Stop drain）
 *
 * 早于 flush 的状态更新直接合并进内存记录（UpdateStatusIfPending 命中返回 true，
 * 调用方跳过 statusUpdater 的批量 UPDATE），消除「UPDATE 先于 INSERT 到库 →
 * UPDATE 扑空 → 记录停留 sending → ACK 超时误标」的攒批竞态：
 * 记录以指针入队，flush 时同一指针已携带终态，INSERT 一步到位
 *
 * ACK 超时注册延后到 flush 成功后（onFlushed 回调，仅 sending 状态记录）：
 * 超时兜底本来就是秒级语义，50ms 攒批延迟无害
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"context"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// MessageRecordSinkProvider outbox 依赖端口（窄接口：sink + logger，hub 与测试桩均可实现）
type MessageRecordSinkProvider interface {
	GetMessageSink() spi.MessageSink
	GetLogger() spi.Logger
}

// MessageRecordOutbox 消息发送记录攒批 outbox
type MessageRecordOutbox struct {
	provider  MessageRecordSinkProvider
	processor *syncx.BatchProcessor[*models.MessageSendRecord]

	mu        sync.Mutex
	pending   map[models.MessageRecordKey]*models.MessageSendRecord // 未落库记录（供状态合并查找）
	onFlushed func(keys []models.MessageRecordKey)                  // flush 成功回调（ACK 超时注册，仅 sending 记录）
}

// NewMessageRecordOutbox 创建消息记录攒批 outbox 并启动后台 flush 协程
func NewMessageRecordOutbox(provider MessageRecordSinkProvider, queueSize, batchSize int, flushInterval time.Duration) *MessageRecordOutbox {
	if queueSize <= 0 {
		queueSize = 4096
	}
	if batchSize <= 0 {
		batchSize = 200
	}
	if flushInterval <= 0 {
		flushInterval = 50 * time.Millisecond
	}
	o := &MessageRecordOutbox{
		provider: provider,
		pending:  make(map[models.MessageRecordKey]*models.MessageSendRecord),
	}
	o.processor = syncx.NewBatchProcessor[*models.MessageSendRecord](
		queueSize, batchSize, flushInterval, o.flush,
		syncx.WithBatchProcessorName[*models.MessageSendRecord]("message-record-outbox"),
	)
	return o
}

// OnFlushed 注册 flush 成功回调（每批仅对状态仍为 sending 的记录回调，供 ACK 超时注册）
func (o *MessageRecordOutbox) OnFlushed(fn func(keys []models.MessageRecordKey)) {
	o.onFlushed = fn
}

// Submit 提交一条待落库记录（同 key 重复提交以最新为准：重试路径复用同一 messageID）
// 队列满时该条丢弃并告警（极端背压下的最终一致性语义，与 statusUpdater 对齐）
func (o *MessageRecordOutbox) Submit(record *models.MessageSendRecord) bool {
	if record == nil {
		return false
	}
	key := models.MessageRecordKey{MessageID: record.MessageID, Receiver: record.Receiver}
	o.mu.Lock()
	o.pending[key] = record
	o.mu.Unlock()

	if !o.processor.Submit(record) {
		// 队列满丢弃：从 pending 移除（否则 UpdateStatusIfPending 谎报命中，状态静默丢失）
		o.mu.Lock()
		if o.pending[key] == record {
			delete(o.pending, key)
		}
		o.mu.Unlock()
		o.provider.GetLogger().WarnContextKV(context.Background(), "outbox 队列满，消息记录丢弃",
			"message_id", record.MessageID,
			"receiver", record.Receiver,
			"dropped_total", o.processor.DroppedCount(),
		)
		return false
	}
	return true
}

// UpdateStatusIfPending 记录尚未落库时直接合并状态更新（消除攒批竞态）
// 返回 true 表示已合并（调用方跳过 statusUpdater 的 UPDATE 提交）
func (o *MessageRecordOutbox) UpdateStatusIfPending(key models.MessageRecordKey, status models.MessageSendStatus, reason models.FailureReason, errMsg string) bool {
	o.mu.Lock()
	record, ok := o.pending[key]
	if ok {
		now := time.Now()
		record.Status = status
		record.FailureReason = reason
		record.ErrorMessage = errMsg
		record.FirstSendTime = &now
		record.LastSendTime = &now
	}
	o.mu.Unlock()
	return ok
}

// DroppedCount 返回累计丢弃条目数（队列满时丢弃，监控背压用）
func (o *MessageRecordOutbox) DroppedCount() int64 {
	return o.processor.DroppedCount()
}

// Stop 停止后台协程并 flush 剩余记录（SafeShutdown 在 Hub cancel 前调用）
func (o *MessageRecordOutbox) Stop() {
	o.processor.Stop()
}

// flush 批量落库（BatchProcessor 回调，满批/定时/Stop drain 三种触发共用）
func (o *MessageRecordOutbox) flush(records []*models.MessageSendRecord) {
	if len(records) == 0 {
		return
	}
	// 独立超时 context：Stop drain 在 Hub cancel 前后均可能触发，用 Background 避免被截断
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := o.provider.GetMessageSink().CreateBatch(ctx, records); err != nil {
		o.provider.GetLogger().WarnContextKV(context.Background(), "outbox 批量落库失败，本批记录丢弃",
			"count", len(records),
			"error", err,
		)
		return
	}

	// 仅状态仍为 sending 的记录注册 ACK 超时（终态记录已被合并进 INSERT，无需兜底）；
	// pending 清理按指针比对：与 flush 并发的同 key 重新 Submit（重试路径）不受影响
	var sendingKeys []models.MessageRecordKey
	o.mu.Lock()
	for _, record := range records {
		key := models.MessageRecordKey{MessageID: record.MessageID, Receiver: record.Receiver}
		if o.pending[key] == record {
			delete(o.pending, key)
		}
		if record.Status == models.MessageSendStatusSending {
			sendingKeys = append(sendingKeys, key)
		}
	}
	o.mu.Unlock()

	if o.onFlushed != nil && len(sendingKeys) > 0 {
		o.onFlushed(sendingKeys)
	}
}

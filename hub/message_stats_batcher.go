/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-15 00:06:53
 * @FilePath: \go-wsc\hub\message_stats_batcher.go
 * @Description: 消息统计批量更新器
 *   基于 syncx.BatchProcessor 泛型批量处理器实现
 *   收集消息/字节统计增量，按 batch flush 到 DB（单事务批量 UPDATE）
 *   同一 connectionID 的增量在 flush 时聚合，减少事务内 UPDATE 条数
 *
 *   优化前：每条消息 → syncx.Go() → 2 次 DB UPDATE（IncrementMessageStats + IncrementBytesStats）
 *   广播 939 人 = 939 个 goroutine + 1878 次 DB 调用
 *   优化后：每条消息 → Submit（非阻塞） → BatchProcessor 收集 → 单事务批量 UPDATE
 *   广播 939 人 = 939 次 Submit（channel 写入）+ 1 次事务（聚合后 ≤939 条 UPDATE）
 *
 * 工作流程：
 *   1. 消息发送路径调用 Submit（非阻塞，队列满时丢弃）
 *   2. BatchProcessor 后台 worker 收集，满 batchSize 或每 flushInterval 触发 flush
 *   3. flush 时按 connectionID 聚合增量，合并成一次 BatchIncrementStats 调用（单事务）
 *   4. SafeShutdown 时调用 Stop，flush 剩余数据后退出
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/repository"
)

// statsIncrementItem 统计增量条目（内部使用）
type statsIncrementItem struct {
	ConnectionID     string
	MessagesSent     int64
	MessagesReceived int64
	BytesSent        int64
	BytesReceived    int64
}

// MessageStatsBatcher 消息统计批量更新器
type MessageStatsBatcher struct {
	hub       *Hub
	processor *syncx.BatchProcessor[*statsIncrementItem]
}

// NewMessageStatsBatcher 创建消息统计批量更新器
func NewMessageStatsBatcher(hub *Hub, queueSize, batchSize int, flushInterval time.Duration) *MessageStatsBatcher {
	u := &MessageStatsBatcher{hub: hub}
	u.processor = syncx.NewBatchProcessor(queueSize, batchSize, flushInterval, u.flush)
	return u
}

// Submit 非阻塞提交统计增量
// 队列满时返回 false（统计数据可丢失，非核心路径）
func (u *MessageStatsBatcher) Submit(item *statsIncrementItem) bool {
	return u.processor.Submit(item)
}

// Stop 停止更新器，flush 剩余数据后退出
func (u *MessageStatsBatcher) Stop() {
	u.processor.Stop()
}

// flush 批量更新 DB
// 按 connectionID 聚合增量（同一连接的多条增量合并），然后单事务批量写入
func (u *MessageStatsBatcher) flush(batch []*statsIncrementItem) {
	if len(batch) == 0 {
		return
	}

	// 按 connectionID 聚合增量
	aggregated := make(map[string]*statsIncrementItem, len(batch))
	for _, item := range batch {
		existing, ok := aggregated[item.ConnectionID]
		if !ok {
			// 复制到 map，避免修改原始 item
			aggregated[item.ConnectionID] = &statsIncrementItem{
				ConnectionID:     item.ConnectionID,
				MessagesSent:     item.MessagesSent,
				MessagesReceived: item.MessagesReceived,
				BytesSent:        item.BytesSent,
				BytesReceived:    item.BytesReceived,
			}
		} else {
			existing.MessagesSent += item.MessagesSent
			existing.MessagesReceived += item.MessagesReceived
			existing.BytesSent += item.BytesSent
			existing.BytesReceived += item.BytesReceived
		}
	}

	// 转换为仓储层条目
	entries := make([]*repository.StatsIncrementEntry, 0, len(aggregated))
	for _, item := range aggregated {
		entries = append(entries, &repository.StatsIncrementEntry{
			ConnectionID:     item.ConnectionID,
			MessagesSent:     item.MessagesSent,
			MessagesReceived: item.MessagesReceived,
			BytesSent:        item.BytesSent,
			BytesReceived:    item.BytesReceived,
		})
	}

	// 用 context.Background() 而非 h.ctx
	// SafeShutdown 中 Stop() 在 h.cancel() 之前调用，但 flush 可能耗时较长
	// 用独立 context 避免被 Hub cancel 截断
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := u.hub.connectionRecordRepo.BatchIncrementStats(ctx, entries); err != nil {
		u.hub.logger.ErrorKV("批量更新消息统计失败",
			"error", err,
			"batch_size", len(entries),
		)
	}
}

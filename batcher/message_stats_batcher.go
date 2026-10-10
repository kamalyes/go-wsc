/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-21 12:57:22
 * @FilePath: \go-wsc\batcher\message_stats_batcher.go
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

package batcher

import (
	"context"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
)

// StatsIncrementItem 统计增量条目（Hub 侧构造）
type StatsIncrementItem struct {
	ConnectionID     string
	MessagesSent     int64
	MessagesReceived int64
	BytesSent        int64
	BytesReceived    int64
}

// MessageStatsBatcher 消息统计批量更新器
type MessageStatsBatcher struct {
	hub       StorageBatchWriter
	processor *syncx.BatchProcessor[*StatsIncrementItem]

	// itemPool 统计条目对象池：广播扇出等单消息多接收者场景消除逐条小对象分配
	// （100w 接收者广播：100w 次堆分配 → 池内循环复用，GC 标记对象数骤降）
	// 生命周期：Acquire（清零）→ 填充 → Submit 入队 → flush 聚合落库 → Put 归还；
	// 队列满丢弃路径由 Submit 内部即时归还，归还不触碰字段（清零延迟到下一次 Acquire）
	itemPool sync.Pool
}

// NewMessageStatsBatcher 创建消息统计批量更新器
func NewMessageStatsBatcher(hub StorageBatchWriter, queueSize, batchSize int, flushInterval time.Duration) *MessageStatsBatcher {
	u := &MessageStatsBatcher{hub: hub}
	u.itemPool = sync.Pool{New: func() any { return &StatsIncrementItem{} }}
	u.processor = syncx.NewBatchProcessor(queueSize, batchSize, flushInterval, u.flush)
	return u
}

// Acquire 获取池化统计条目（已清零，填充字段后经 Submit 入队）
// 高频路径（广播扇出逐接收者统计）以 Acquire+Submit 替代字面量构造，消除逐条堆分配；
// 清零在 Acquire 时执行（归还时不触碰字段），防上一轮残留计数串扰
// 所有权契约：Submit 后条目归批量器所有（flush 后回池复用），调用方不得继续持有使用，
// 同一指针仅可 Submit 一次（重复入队会破坏池内唯一性）
func (u *MessageStatsBatcher) Acquire() *StatsIncrementItem {
	item := u.itemPool.Get().(*StatsIncrementItem)
	*item = StatsIncrementItem{}
	return item
}

// Submit 非阻塞提交统计增量
// 队列满时返回 false（统计数据可丢失，非核心路径），条目即时归还池
func (u *MessageStatsBatcher) Submit(item *StatsIncrementItem) bool {
	if u.processor.Submit(item) {
		return true
	}
	u.itemPool.Put(item)
	return false
}

// Stop 停止更新器，flush 剩余数据后退出
func (u *MessageStatsBatcher) Stop() {
	u.processor.Stop()
}

// flush 批量更新 DB
// 按 connectionID 聚合增量（同一连接的多条增量合并），然后单事务批量写入
func (u *MessageStatsBatcher) flush(batch []*StatsIncrementItem) {
	if len(batch) == 0 {
		return
	}

	// 按 connectionID 聚合增量
	aggregated := make(map[string]*StatsIncrementItem, len(batch))
	for _, item := range batch {
		existing, ok := aggregated[item.ConnectionID]
		if !ok {
			// 复制到 map，避免修改原始 item
			aggregated[item.ConnectionID] = &StatsIncrementItem{
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
	entries := make([]*models.StatsIncrementEntry, 0, len(aggregated))
	for _, item := range aggregated {
		entries = append(entries, &models.StatsIncrementEntry{
			ConnectionID:     item.ConnectionID,
			MessagesSent:     item.MessagesSent,
			MessagesReceived: item.MessagesReceived,
			BytesSent:        item.BytesSent,
			BytesReceived:    item.BytesReceived,
		})
	}

	// 聚合完成（值已拷贝进 entries），源条目归还对象池供下一轮 Acquire 复用
	for _, item := range batch {
		u.itemPool.Put(item)
	}

	// 用 context.Background() 而非 h.ctx
	// SafeShutdown 中 Stop() 在 h.cancel() 之前调用，但 flush 可能耗时较长
	// 用独立 context 避免被 Hub cancel 截断
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := u.hub.GetConnectionQualityRepository().BatchIncrementStats(ctx, entries); err != nil {
		u.hub.GetLogger().ErrorContextKV(u.hub.Context(), "批量更新消息统计失败",
			"error", err,
			"batch_size", len(entries),
		)
	}
}

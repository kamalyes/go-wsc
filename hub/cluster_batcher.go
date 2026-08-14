/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-31 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-31 00:00:00
 * @FilePath: \go-wsc\hub\cluster_batcher.go
 * @Description: 跨节点分发批量处理器
 *   基于 syncx.BatchProcessor 泛型批量处理器实现
 *   收集跨节点路由请求（全局广播/命名空间广播/群组广播），按 batch flush
 *   消除每条消息一个 goroutine 的 routeToCluster 调用开销
 *
 *   优化前：每条广播消息 → go func() { routeToCluster(...) }()（1 goroutine/消息）
 *   高频广播 = N 个 goroutine 竞争 gRPC 连接池与 Redis 连接
 *   优化后：每条广播消息 → Submit（非阻塞）→ BatchProcessor 单 worker 串行 flush
 *   高频广播 = N 次 Submit（channel 写入）+ 1 个 worker 串行 routeToCluster
 *
 *   稳定性保障（由 syncx.BatchProcessor 提供）：
 *   - WithClone：Submit 时深拷贝 msg + GroupIDs 切片，防止调用方修改影响异步 flush
 *   - WithPanicHandler：flush panic 时恢复，单次失败不崩溃 worker
 *   - DroppedCount：队列满时丢弃计数，便于监控背压
 *
 * 工作流程：
 *   1. 广播路径调用 Submit（非阻塞，队列满时丢弃）
 *   2. BatchProcessor 后台 worker 收集，满 batchSize 或每 flushInterval 触发 flush
 *   3. flush 时逐条调用 routeToCluster（无 per-message goroutine）
 *   4. SafeShutdown 时调用 Stop，flush 剩余数据后退出
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// clusterDispatchItem 跨节点分发条目（内部使用）
type clusterDispatchItem struct {
	msg  *HubMessage
	opts ClusterDispatchOptions
}

// cloneClusterDispatchItem 深拷贝跨节点分发条目
// Clone msg 防止数据竞争，复制 GroupIDs 切片防止调用方修改底层数组
func cloneClusterDispatchItem(item *clusterDispatchItem) *clusterDispatchItem {
	opts := item.opts
	if len(opts.GroupIDs) > 0 {
		opts.GroupIDs = append([]string(nil), opts.GroupIDs...)
	}
	return &clusterDispatchItem{
		msg:  item.msg.Clone(),
		opts: opts,
	}
}

// ClusterDispatchBatcher 跨节点分发批量处理器
type ClusterDispatchBatcher struct {
	hub       *Hub
	processor *syncx.BatchProcessor[*clusterDispatchItem]
}

// NewClusterDispatchBatcher 创建跨节点分发批量处理器
func NewClusterDispatchBatcher(hub *Hub, queueSize, batchSize int, flushInterval time.Duration) *ClusterDispatchBatcher {
	b := &ClusterDispatchBatcher{hub: hub}
	b.processor = syncx.NewBatchProcessor(
		queueSize, batchSize, flushInterval, b.flush,
		syncx.WithBatchProcessorClone(cloneClusterDispatchItem),
		syncx.WithBatchProcessorName[*clusterDispatchItem]("cluster-dispatch"),
	)
	return b
}

// Submit 非阻塞提交跨节点分发请求
// msg + GroupIDs 由 BatchProcessor.WithClone 自动深拷贝，调用方无需关心数据隔离
// 队列满时返回 false（广播消息可丢失，非核心路径）
func (b *ClusterDispatchBatcher) Submit(msg *HubMessage, opts ClusterDispatchOptions) bool {
	if b == nil || msg == nil {
		return false
	}
	return b.processor.Submit(&clusterDispatchItem{
		msg:  msg,
		opts: opts,
	})
}

// Stop 停止处理器，flush 剩余数据后退出
func (b *ClusterDispatchBatcher) Stop() {
	if b == nil {
		return
	}
	b.processor.Stop()
}

// DroppedCount 返回累计丢弃的跨节点分发数（队列满时丢弃）
func (b *ClusterDispatchBatcher) DroppedCount() int64 {
	if b == nil {
		return 0
	}
	return b.processor.DroppedCount()
}

// flush 批量执行跨节点分发
// 逐条调用 routeToCluster，消除 per-message goroutine
func (b *ClusterDispatchBatcher) flush(items []*clusterDispatchItem) {
	if len(items) == 0 {
		return
	}
	for _, item := range items {
		ctx, cancel := context.WithTimeout(b.hub.ctx, 3*time.Second)
		// 从消息体恢复 trace_id（Broadcast/SendToUserWithRetry 已通过 InjectContext 注入）
		ctx = item.msg.ContextFrom(ctx)
		if err := b.hub.routeToCluster(ctx, item.msg, item.opts); err != nil {
			b.hub.logger.WarnContextKV(ctx, "批量集群分发失败",
				"error", err,
				"operation", item.opts.Operation,
				"message_id", item.msg.GetMessageID(),
			)
		}
		cancel()
	}
}

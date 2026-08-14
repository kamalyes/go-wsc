/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-31 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-31 00:00:00
 * @FilePath: \go-wsc\hub\observer_batcher.go
 * @Description: 观察者通知批量处理器
 *   基于 syncx.BatchProcessor 泛型批量处理器实现
 *   收集观察者通知请求，按 batch flush（本地投递 + 跨节点广播）
 *   消除每条消息一个 goroutine 的开销，提供 channel 背压与丢弃保护
 *
 *   优化前：每条消息 → syncx.Go()（本地观察者投递）+ syncx.Go()（跨节点广播）
 *   高频群组消息 = N 个 goroutine + 2N 次 routeToCluster 调用
 *   优化后：每条消息 → Submit（非阻塞）→ BatchProcessor 收集 → 单 worker flush
 *   高频群组消息 = N 次 Submit（channel 写入）+ 1 个 worker 串行处理
 *
 *   稳定性保障（由 syncx.BatchProcessor 提供）：
 *   - WithClone：Submit 时深拷贝 msg，防止调用方修改影响异步 flush
 *   - WithPanicHandler：flush panic 时恢复，单次失败不崩溃 worker
 *   - DroppedCount：队列满时丢弃计数，便于监控背压
 *
 * 工作流程：
 *   1. 消息路径调用 Submit（非阻塞，队列满时丢弃）
 *   2. BatchProcessor 后台 worker 收集，满 batchSize 或每 flushInterval 触发 flush
 *   3. flush 时逐条执行本地观察者投递 + 跨节点广播（无 per-message goroutine）
 *   4. SafeShutdown 时调用 Stop，flush 剩余数据后退出
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// observerNotifyItem 观察者通知条目（内部使用）
type observerNotifyItem struct {
	msg       *HubMessage
	namespace string
	groupIDs  []string
}

// cloneObserverNotifyItem 深拷贝观察者通知条目（Clone msg 防止数据竞争，复制 groupIDs 切片）
func cloneObserverNotifyItem(item *observerNotifyItem) *observerNotifyItem {
	groupIDs := item.groupIDs
	if len(groupIDs) > 0 {
		groupIDs = append([]string(nil), groupIDs...)
	}
	return &observerNotifyItem{
		msg:       item.msg.Clone(),
		namespace: item.namespace,
		groupIDs:  groupIDs,
	}
}

// ObserverNotificationBatcher 观察者通知批量处理器
type ObserverNotificationBatcher struct {
	hub       *Hub
	processor *syncx.BatchProcessor[*observerNotifyItem]
}

// NewObserverNotificationBatcher 创建观察者通知批量处理器
func NewObserverNotificationBatcher(hub *Hub, queueSize, batchSize int, flushInterval time.Duration) *ObserverNotificationBatcher {
	b := &ObserverNotificationBatcher{hub: hub}
	b.processor = syncx.NewBatchProcessor(
		queueSize, batchSize, flushInterval, b.flush,
		syncx.WithBatchProcessorClone(cloneObserverNotifyItem),
		syncx.WithBatchProcessorName[*observerNotifyItem]("observer-notify"),
	)
	return b
}

// Submit 非阻塞提交观察者通知
// msg 由 BatchProcessor.WithClone 自动深拷贝，调用方无需关心数据隔离
// 队列满时返回 false（观察者通知可丢失，非核心路径）
func (b *ObserverNotificationBatcher) Submit(msg *HubMessage, namespace string, groupIDs []string) bool {
	if b == nil || msg == nil {
		return false
	}
	return b.processor.Submit(&observerNotifyItem{
		msg:       msg,
		namespace: namespace,
		groupIDs:  groupIDs,
	})
}

// Stop 停止处理器，flush 剩余数据后退出
func (b *ObserverNotificationBatcher) Stop() {
	if b == nil {
		return
	}
	b.processor.Stop()
}

// DroppedCount 返回累计丢弃的观察者通知数（队列满时丢弃）
func (b *ObserverNotificationBatcher) DroppedCount() int64 {
	if b == nil {
		return 0
	}
	return b.processor.DroppedCount()
}

// flush 批量处理观察者通知
// 逐条执行本地观察者投递 + 跨节点广播，消除 per-message goroutine
func (b *ObserverNotificationBatcher) flush(items []*observerNotifyItem) {
	if len(items) == 0 {
		return
	}
	for _, item := range items {
		b.hub.notifyObserversDirect(item.msg, item.namespace, item.groupIDs)
	}
}

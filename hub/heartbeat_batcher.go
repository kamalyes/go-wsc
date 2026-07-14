/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-11 15:15:17
 * @FilePath: \go-wsc\hub\heartbeat_batcher.go
 * @Description: 心跳统计批量更新器
 *   基于 syncx.BatchProcessor 泛型批量处理器实现
 *   收集心跳数据，按 batch flush 到 DB（单事务批量 UPDATE）
 *   同一 clientID 的多条心跳在 flush 时去重，仅保留最新
 *
 * 工作流程：
 *   1. 心跳路径调用 Submit（非阻塞，队列满时丢弃）
 *   2. BatchProcessor 后台 worker 收集，满 batchSize 或每 flushInterval 触发 flush
 *   3. flush 时按 clientID 去重，合并成一次 BatchUpdateHeartbeats 调用（单事务）
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

// heartbeatStatsEntry 心跳统计条目
type heartbeatStatsEntry struct {
	ClientID string
	PingTime time.Time
	PongTime time.Time
	PingMs   float64
}

// HeartbeatStatsUpdater 心跳统计批量更新器
type HeartbeatStatsUpdater struct {
	hub       *Hub
	processor *syncx.BatchProcessor[*heartbeatStatsEntry]
}

// NewHeartbeatStatsUpdater 创建心跳统计批量更新器
func NewHeartbeatStatsUpdater(hub *Hub, queueSize, batchSize int, flushInterval time.Duration) *HeartbeatStatsUpdater {
	u := &HeartbeatStatsUpdater{hub: hub}
	u.processor = syncx.NewBatchProcessor(queueSize, batchSize, flushInterval, u.flush)
	return u
}

// Submit 非阻塞提交心跳统计
// 队列满时返回 false（心跳数据可丢弃，下次心跳会补上）
func (u *HeartbeatStatsUpdater) Submit(entry *heartbeatStatsEntry) bool {
	return u.processor.Submit(entry)
}

// Stop 停止更新器，flush 剩余数据后退出
func (u *HeartbeatStatsUpdater) Stop() {
	u.processor.Stop()
}

// flush 批量更新 DB
// 按 clientID 去重（同一客户端多条心跳仅保留最新），然后单事务批量写入
func (u *HeartbeatStatsUpdater) flush(batch []*heartbeatStatsEntry) {
	if len(batch) == 0 {
		return
	}

	// 按 clientID 去重，保留最新条目（后出现的覆盖先出现的）
	deduped := make(map[string]*heartbeatStatsEntry, len(batch))
	for _, entry := range batch {
		deduped[entry.ClientID] = entry
	}

	// 转换为仓储层条目
	entries := make([]*repository.HeartbeatUpdateEntry, 0, len(deduped))
	for _, entry := range deduped {
		// 复制局部变量避免循环变量逃逸问题
		pingTime := entry.PingTime
		pongTime := entry.PongTime
		entries = append(entries, &repository.HeartbeatUpdateEntry{
			ConnectionID: entry.ClientID,
			PingTime:     &pingTime,
			PongTime:     &pongTime,
			PingMs:       entry.PingMs,
		})
	}

	// 用 context.Background() 而非 h.ctx
	// SafeShutdown 中 Stop() 在 h.cancel() 之前调用，但 flush 可能耗时较长
	// 用独立 context 避免被 Hub cancel 截断
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := u.hub.connectionRecordRepo.BatchUpdateHeartbeats(ctx, entries); err != nil {
		u.hub.logger.ErrorKV("批量更新心跳统计失败",
			"error", err,
			"batch_size", len(entries),
		)
	}
}

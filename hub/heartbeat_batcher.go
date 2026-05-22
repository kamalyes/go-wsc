/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-22 00:00:00
 * @FilePath: \go-wsc\hub\heartbeat_batcher.go
 * @Description: 心跳统计批量聚合器
 *
 * 优化目标：减少 trackHeartbeatStats 的 CPU 占用（从 22.54% 降至 5-8%）
 * - 每次心跳不再启动独立 goroutine 写数据库
 * - 聚合心跳数据到本地缓存，定时批量刷写
 * - 减少 goroutine 数量和数据库写入频率
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

const (
	// heartbeatFlushInterval 心跳统计刷写间隔
	heartbeatFlushInterval = 10 * time.Second
	// heartbeatMaxBatchSize 单次刷写最大批量大小
	heartbeatMaxBatchSize = 200
)

// heartbeatStatsEntry 心跳统计条目
type heartbeatStatsEntry struct {
	ClientID      string
	PingTime      time.Time
	PongTime      time.Time
	PingMs        float64
	LastHeartbeat time.Time
}

// heartbeatStatsBatcher 心跳统计批量聚合器
type heartbeatStatsBatcher struct {
	hub    *Hub
	mu     sync.Mutex
	buffer map[string]*heartbeatStatsEntry // key: clientID
	stopCh chan struct{}
}

// newHeartbeatStatsBatcher 创建心跳统计批量聚合器
func newHeartbeatStatsBatcher(hub *Hub) *heartbeatStatsBatcher {
	b := &heartbeatStatsBatcher{
		hub:    hub,
		buffer: make(map[string]*heartbeatStatsEntry, 256),
		stopCh: make(chan struct{}),
	}
	return b
}

// Start 启动定时刷写协程
func (b *heartbeatStatsBatcher) Start() {
	go b.flushLoop()
}

// Stop 停止聚合器，刷写剩余数据
func (b *heartbeatStatsBatcher) Stop() {
	close(b.stopCh)
	// 最后一次刷写
	b.flush()
}

// Add 添加心跳统计条目（非阻塞，线程安全）
func (b *heartbeatStatsBatcher) Add(clientID string, pingTime, pongTime time.Time, pingMs float64, lastHeartbeat time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 同一客户端只保留最新的统计（覆盖旧值）
	b.buffer[clientID] = &heartbeatStatsEntry{
		ClientID:      clientID,
		PingTime:      pingTime,
		PongTime:      pongTime,
		PingMs:        pingMs,
		LastHeartbeat: lastHeartbeat,
	}
}

// flushLoop 定时刷写循环
func (b *heartbeatStatsBatcher) flushLoop() {
	ticker := time.NewTicker(heartbeatFlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			b.flush()
		case <-b.stopCh:
			return
		case <-b.hub.ctx.Done():
			return
		}
	}
}

// flush 批量刷写心跳统计到数据库
func (b *heartbeatStatsBatcher) flush() {
	b.mu.Lock()
	if len(b.buffer) == 0 {
		b.mu.Unlock()
		return
	}

	// 取出所有数据，清空 buffer
	entries := make([]*heartbeatStatsEntry, 0, len(b.buffer))
	for _, entry := range b.buffer {
		entries = append(entries, entry)
	}
	b.buffer = make(map[string]*heartbeatStatsEntry, 256)
	b.mu.Unlock()

	// 分批写入数据库
	for i := 0; i < len(entries); i += heartbeatMaxBatchSize {
		end := i + heartbeatMaxBatchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]
		b.writeBatch(batch)
	}
}

// writeBatch 写入一批心跳统计
func (b *heartbeatStatsBatcher) writeBatch(entries []*heartbeatStatsEntry) {
	repo := b.hub.connectionRecordRepo
	if repo == nil {
		return
	}

	syncx.Go().
		WithTimeout(5 * time.Second).
		OnPanic(func(r any) {
			b.hub.logger.ErrorKV("批量更新心跳统计崩溃", "panic", r, "batch_size", len(entries))
		}).
		ExecWithContext(func(ctx context.Context) error {
			for _, entry := range entries {
				if err := repo.UpdateHeartbeat(ctx, entry.ClientID, &entry.PingTime, &entry.PongTime); err != nil {
					// 单条失败不影响其他条目
					continue
				}
				if entry.PingMs > 0 {
					_ = repo.UpdatePingStats(ctx, entry.ClientID, entry.PingMs)
				}
			}
			return nil
		})
}

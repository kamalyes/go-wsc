/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-07 21:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-10 21:00:00
 * @FilePath: \go-wsc\overload\delay_queue.go
 * @Description: 广播延迟队列 —— 填谷填充器（削峰的"谷"侧）
 *
 * 整形拒绝（shaper.Allow() == false）的广播消息进入此队列延迟投递：
 *   - 有界容量（BroadcastDelayQueueCapacity），满则走分级兜底路由（转离线/高频计数），不丢弃必达
 *   - 单 drain goroutine：每轮取 1 条 + 按 shaper 速率节拍投递
 *     （洪峰时排队削峰；水位回落后 AIMD 提速 → drain 节拍加快 → 填谷加速）
 *
 * 性能：入队 1 次 chan send（非阻塞，满即 fallback）；drain 单协程无锁竞争
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// BroadcastDelayQueueCapacity 延迟队列容量（有界；超过走分级兜底路由）
const BroadcastDelayQueueCapacity = 1024

// broadcastDelayEntry 延迟投递条目（闭包捕获完整投递上下文）
type broadcastDelayEntry struct {
	ctx   context.Context
	msg   *models.HubMessage
	retry func(ctx context.Context, msg *models.HubMessage) // 重投函数（只做投递，不重新 Admit）
}

// broadcastDelayQueue 有界延迟队列
type BroadcastDelayQueue struct {
	entries chan broadcastDelayEntry
	// queueFull 队列满时走兜底路由的次数（观测：洪峰强度信号）
	queueFull atomic.Int64
	// enqueued / drained 入队/出队计数（观测：队列积压 = enqueued - drained）
	enqueued atomic.Int64
	drained  atomic.Int64

	// shaper 广播整形器（取自 Hub；nil 表示未启用整形，按原始速率投递）
	// 依赖倒置：此处只需 interval 一个能力，不持有 *Hub
	shaper func() ShaperInterval
}

// NewBroadcastDelayQueue 创建延迟队列（drain 循环由调用方启动）
func NewBroadcastDelayQueue(shaper func() ShaperInterval) *BroadcastDelayQueue {
	return &BroadcastDelayQueue{
		entries: make(chan broadcastDelayEntry, BroadcastDelayQueueCapacity),
		shaper:  shaper,
	}
}

// offer 入队一条延迟投递（非阻塞；返回 false = 队列满，调用方走分级兜底路由）
func (q *BroadcastDelayQueue) Offer(ctx context.Context, msg *models.HubMessage, retry func(ctx context.Context, msg *models.HubMessage)) bool {
	select {
	case q.entries <- broadcastDelayEntry{ctx: ctx, msg: msg, retry: retry}:
		q.enqueued.Add(1)
		return true
	default:
		q.queueFull.Add(1)
		return false
	}
}

// drainLoop 投递循环（每轮 1 条 + 按 shaper 速率节拍等待）
//
// 节拍：每投递 1 条 sleep 一个令牌间隔（interval = 1s / rate）；
// AIMD 恢复时 SetRate 提升 → interval 缩短 → 填谷加速（闭环联动）
func (q *BroadcastDelayQueue) DrainLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			// Hub 关闭：flush 剩余条目（尽力投递，投递函数内部自带 hub 生命周期检查）
			for {
				select {
				case entry := <-q.entries:
					entry.retry(entry.ctx, entry.msg)
					q.drained.Add(1)
				default:
					return
				}
			}
		case entry := <-q.entries:
			// ⏱️ 节拍等待：按当前整形速率休眠一个令牌间隔（atomic load 热替换即刻生效）
			if shaper := q.shaper(); shaper != nil {
				if interval := shaper.Interval(); interval > 0 {
					select {
					case <-time.After(time.Duration(interval)):
					case <-ctx.Done():
						entry.retry(entry.ctx, entry.msg) // 关闭时不再等待，直接投递
						q.drained.Add(1)
						continue
					}
				}
			}
			entry.retry(entry.ctx, entry.msg)
			q.drained.Add(1)
		}
	}
}

// Backlog 队列当前积压（观测用）
func (q *BroadcastDelayQueue) Backlog() int64 {
	return q.enqueued.Load() - q.drained.Load()
}

// Stats 快照（观测用）
func (q *BroadcastDelayQueue) Stats() map[string]int64 {
	return map[string]int64{
		"capacity":   BroadcastDelayQueueCapacity,
		"backlog":    q.Backlog(),
		"enqueued":   q.enqueued.Load(),
		"drained":    q.drained.Load(),
		"queue_full": q.queueFull.Load(),
	}
}

// deferBroadcast 广播延迟路由（Admit 拒绝 / 整形拒绝的统一入口）
//
// 路由优先级：延迟队列（有界）→ 队列满时分级兜底：
//   - 高频级：语义丢弃（latest-wins，计数）
//   - 普通/必达级：广播无单点接收者，记 unrecoverable + Error 日志
//     （必达级广播经 Admit 矩阵恒放行，实际不会走到兜底；此处为极端洪峰下的最后防线）

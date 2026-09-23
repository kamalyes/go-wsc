/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 00:00:00
 * @FilePath: \go-wsc\hub\ack_log_window.go
 * @Description: 跨节点 ACK 超时日志聚合窗口（messageID 维度）
 *
 * 问题背景（生产实测单日 1.6GB 日志洪水）：
 *   广播消息发往订阅失活的目标节点时，N 个 receiver 各自的 ACK 超时定时器
 *   在 30s 后同波次集中触发（抖动扩散 ~3s），每个 receiver 各打 WARN+INFO 各一行，
 *   产生 2N 行/消息的重复日志。行为本身正确（每个 receiver 需各存一份离线），
 *   错在逐 receiver 打日志。
 *
 * 聚合策略：
 *   - 按 messageID 开窗（ackTimeoutLogWindowTTL），窗口内仅首条 WARN/INFO 放行，
 *     其余仅计数（离线转存、状态标记不受影响，仅日志聚合）
 *   - 窗口滚动（同 messageID 再有超时波次）时，放行首条并携带上一窗口聚合数
 *   - 窗口条目由 sweepAckTimeoutLogWindows 周期清扫（EventLoop IfTicker，见 lifecycle.go），
 *     条目量 = 窗口保留期内活跃 messageID 数，极小
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"sync"
	"time"
)

const (
	// ackTimeoutLogWindowTTL 单条消息的日志聚合窗口时长
	// 取 30s（与 nodeAckTimeout 同量级）：完整覆盖一个广播波次内全部 receiver
	// 定时器的抖动扩散（nodeAckTimeoutJitter=3s，实测波次跨度 ~17s）
	ackTimeoutLogWindowTTL = 30 * time.Second
	// ackLogWindowSweepInterval 聚合窗口清扫间隔（由 Hub EventLoop IfTicker 触发，见 lifecycle.go）
	ackLogWindowSweepInterval = 90 * time.Second
	// ackLogWindowRetention 窗口条目空闲保留时长（超过即可清扫）
	ackLogWindowRetention = 10 * time.Minute
)

// ackTimeoutLogWindow 单条消息的 ACK 超时日志聚合窗口
// 窗口语义：windowStart 起的 TTL 内非首条一律抑制并计数，窗口过期后下一条放行并滚动
type ackTimeoutLogWindow struct {
	mu          sync.Mutex
	windowStart time.Time // 当前窗口起点
	suppressed  int64     // 当前窗口内被抑制的日志条数（≈ 同消息被聚合的 receiver 数）
}

// allow 判定本条日志是否放行
// 返回：是否放行、上一窗口被抑制的条数（仅窗口滚动放行时非零，供日志观测聚合量）
func (w *ackTimeoutLogWindow) allow(now time.Time, ttl time.Duration) (allowed bool, suppressedPrevWindow int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if now.Sub(w.windowStart) < ttl {
		w.suppressed++
		return false, 0
	}
	suppressedPrevWindow = w.suppressed
	w.windowStart = now
	w.suppressed = 0
	return true, suppressedPrevWindow
}

// allowAckTimeoutLog ACK 超时日志聚合网关（messageID 维度，见文件头注释）
// 调用方：makeAckTimeoutCallback 的"已标记待重试"WARN 与配套转存 INFO（quiet 参数联动）
// 首条放行、窗口内抑制并计数、过期滚动放行并返回上窗口聚合数
func (h *Hub) allowAckTimeoutLog(messageID string) (allowed bool, suppressedPrevWindow int64) {
	if messageID == "" {
		return true, 0 // 无法聚合的键直接放行（防御性，正常不会出现）
	}
	now := time.Now()
	v, loaded := h.ackTimeoutLogWindows.LoadOrStore(messageID, &ackTimeoutLogWindow{windowStart: now})
	w := v.(*ackTimeoutLogWindow)
	if !loaded {
		return true, 0 // 窗口创建者即本波次首条，直接放行
	}
	return w.allow(now, ackTimeoutLogWindowTTL)
}

// sweepAckTimeoutLogWindows 周期清扫空闲过期的聚合窗口条目（防 sync.Map 泄漏）
// 由 Hub EventLoop IfTicker 触发（见 lifecycle.go）
func (h *Hub) sweepAckTimeoutLogWindows() {
	now := time.Now()
	h.ackTimeoutLogWindows.Range(func(key, value any) bool {
		w, ok := value.(*ackTimeoutLogWindow)
		if !ok {
			h.ackTimeoutLogWindows.Delete(key)
			return true
		}
		w.mu.Lock()
		idle := now.Sub(w.windowStart)
		w.mu.Unlock()
		if idle > ackLogWindowRetention {
			h.ackTimeoutLogWindows.Delete(key)
		}
		return true
	})
}

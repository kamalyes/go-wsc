/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 00:00:00
 * @FilePath: \go-wsc\hub\ack_log_window_test.go
 * @Description: ACK 超时日志聚合窗口单测（窗口抑制/滚动聚合/并发放行/周期清扫）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAckTimeoutLogWindowAllow_WindowSuppressionAndRollup(t *testing.T) {
	ttl := 30 * time.Second
	base := time.Now()
	w := &ackTimeoutLogWindow{windowStart: base}

	// 窗口内：全部抑制并计数
	for i := 0; i < 3; i++ {
		allowed, prev := w.allow(base.Add(time.Duration(i)*100*time.Millisecond), ttl)
		assert.False(t, allowed, "窗口内第 %d 条应抑制", i+1)
		assert.Zero(t, prev)
	}

	// 窗口过期滚动：放行并携带上一窗口聚合数
	rolled := base.Add(ttl + time.Second)
	allowed, prev := w.allow(rolled, ttl)
	require.True(t, allowed, "窗口滚动后应放行")
	require.EqualValues(t, 3, prev, "应携带上一窗口聚合数 3")

	// 新窗口内恢复抑制
	allowed, prev = w.allow(rolled.Add(time.Second), ttl)
	assert.False(t, allowed, "新窗口内应抑制")
	assert.Zero(t, prev)
}

func TestAllowAckTimeoutLog_ConcurrentWaveOnlyFirstAllowed(t *testing.T) {
	h := &Hub{}
	const receivers = 25 // 复刻生产广播场景：同波次 25 个 receiver 定时器并发触发
	var allowedCount atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < receivers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 对齐起跑线，最大化并发窗口竞争
			if allowed, _ := h.allowAckTimeoutLog("msg-broadcast-wave"); allowed {
				allowedCount.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.EqualValues(t, 1, allowedCount.Load(), "同波次并发 %d 条仅应放行 1 条（窗口创建者）", receivers)
}

func TestAllowAckTimeoutLog_EmptyMessageIDAlwaysAllowed(t *testing.T) {
	h := &Hub{}
	for i := 0; i < 3; i++ {
		allowed, prev := h.allowAckTimeoutLog("")
		assert.True(t, allowed, "空 messageID 应直接放行（无法聚合）")
		assert.Zero(t, prev)
	}
}

func TestSweepAckTimeoutLogWindows(t *testing.T) {
	h := &Hub{}
	now := time.Now()

	h.ackTimeoutLogWindows.Store("stale-msg", &ackTimeoutLogWindow{
		windowStart: now.Add(-ackLogWindowRetention - time.Minute)})
	h.ackTimeoutLogWindows.Store("fresh-msg", &ackTimeoutLogWindow{windowStart: now})
	h.ackTimeoutLogWindows.Store("bad-type", "not-a-window") // 防御性清理

	h.sweepAckTimeoutLogWindows()

	assert.False(t, hasKey(&h.ackTimeoutLogWindows, "stale-msg"), "过期条目应被清扫")
	assert.False(t, hasKey(&h.ackTimeoutLogWindows, "bad-type"), "非法类型条目应被清扫")
	assert.True(t, hasKey(&h.ackTimeoutLogWindows, "fresh-msg"), "新鲜条目不应被清扫")
}

// hasKey 避免引入 golang.org/x/sync 之外的工具函数（保持单测自包含）
func hasKey(m *sync.Map, key string) bool {
	_, ok := m.Load(key)
	return ok
}

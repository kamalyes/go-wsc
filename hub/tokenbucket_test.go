/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 10:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 10:30:00
 * @FilePath: \go-wsc\hub\tokenbucket_test.go
 * @Description: GCRA 令牌桶测试 —— 速率节流 + 突发容忍 + 热调整 + 并发安全
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
)

// TestShaperRateLimiting 稳态速率节流：200/s 在 100ms 窗口内放行数 ≈ rate*window + burst
func TestShaperRateLimiting(t *testing.T) {
	rate := int64(200)
	s := NewShaper(rate)

	// 先耗尽突发容忍（burst = 100ms × 200/s = 20 条）
	window := 100 * time.Millisecond
	// 允许放行上限：burst(20) + rate*window(20) + 抖动余量(5)
	allowed := 0
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if s.Allow() {
			allowed++
		}
	}
	assert.LessOrEqual(t, allowed, int(float64(rate)*window.Seconds())+22,
		"100ms 窗口放行数不应超过 burst+rate*window")
	assert.Greater(t, allowed, 10, "节流不应误杀为接近 0")
}

// TestShaperBurstTolerance 突发容忍：新建桶瞬间允许 burst 数量级通过
// GCRA 判定含等号（now+burst >= tat 放行）：burst=100ms/interval=10ms → 11 条通过，第 12 条拒绝
func TestShaperBurstTolerance(t *testing.T) {
	rate := int64(100) // burst = 100ms * 100/s = 10 个 interval，边界 +1 = 11 条
	s := NewShaper(rate)

	burstPassed := 0
	for i := 0; i < 11; i++ {
		if s.Allow() {
			burstPassed++
		}
	}
	assert.Equal(t, 11, burstPassed, "前 11 条应全部通过（100ms 突发容忍含边界）")

	// 突发耗尽后应被拒（tat 已推进到 now+110ms > now+100ms）
	assert.False(t, s.Allow(), "突发耗尽后下一条应被拒绝")
}

// TestShaperRecovery 空闲恢复：tat 与 now 的距离缩回 burst 窗口内后重新放行
func TestShaperRecovery(t *testing.T) {
	rate := int64(100)
	s := NewShaper(rate)

	// 耗尽突发并推进 tat 至 now+120ms（超出 burst 窗口）
	for i := 0; i < 12; i++ {
		s.Allow()
	}
	assert.False(t, s.Allow())

	// tat - now = 120ms - 25ms = 95ms < burst 100ms → 放行
	time.Sleep(25 * time.Millisecond)
	assert.True(t, s.Allow(), "tat 距离缩回 burst 窗口内后应恢复放行")
}

// TestShaperSetRateHot 热调整速率：SetRate 提速后放行密度上升
func TestShaperSetRateHot(t *testing.T) {
	rate := int64(100)
	s := NewShaper(rate)

	// 耗尽突发
	for i := 0; i < 10; i++ {
		s.Allow()
	}

	// 提速到 1000/s：令牌间隔 1ms，1ms 后即放行
	s.SetRate(1000)
	assert.Equal(t, int64(1000), s.Rate())
	time.Sleep(2 * time.Millisecond)
	assert.True(t, s.Allow(), "提速后应快速恢复放行")
}

// TestShaperSetRateMinClamp SetRate 下限钳制（min 1，不允许 0 关闭语义冲突）
func TestShaperSetRateMinClamp(t *testing.T) {
	s := NewShaper(100)
	s.SetRate(0)
	assert.Equal(t, int64(1), s.Rate(), "SetRate(0) 应钳制为 1")
	s.SetRate(-5)
	assert.Equal(t, int64(1), s.Rate(), "负速率应钳制为 1")
}

// TestShaperConcurrent 并发安全：多 goroutine 同时 Allow 不 panic，计数合理
func TestShaperConcurrent(t *testing.T) {
	rate := int64(1000)
	s := NewShaper(rate)

	var allowed, denied atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if s.Allow() {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// 800 次请求：burst(100) + 少量稳态放行，其余被拒
	assert.LessOrEqual(t, allowed.Load(), int64(150), "并发下放行总数不应超突发+稳态")
	assert.Greater(t, allowed.Load(), int64(50), "并发下应仍有多数突发通过")
	assert.Equal(t, int64(800), allowed.Load()+denied.Load(), "每次请求必须有确定裁决")
}

// TestShaperDefaultRate 默认速率兜底（0 → constants.DefaultBroadcastShaperRate）
func TestShaperDefaultRate(t *testing.T) {
	s := NewShaper(0)
	assert.NotZero(t, s.Rate(), "零值速率应用默认值")
}

// TestShaperFairnessLongRun 长跑守恒：1s 窗口总放行数 ≈ rate（GCRA 稳态收敛）
func TestShaperFairnessLongRun(t *testing.T) {
	rate := int64(500)
	s := NewShaper(rate)

	// 持续请求 1s，统计放行数（首 100ms 有 burst 红利 +22）
	allowed := 0
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if s.Allow() {
			allowed++
		} else {
			time.Sleep(100 * time.Microsecond) // 被拒时微睡，避免空转 CPU
		}
	}
	// 允许 ±10% 误差 + burst 红利
	assert.InDelta(t, rate+20, int64(allowed), float64(rate)*0.1+10,
		"1s 稳态放行数应收敛到速率附近")
}

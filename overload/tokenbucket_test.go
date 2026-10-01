/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-01 09:05:08
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-01 09:05:08
 * @FilePath: \go-wsc\overload\tokenbucket_test.go
 * @Description: GCRA 令牌桶测试与基准（放行/拒绝边界、突发容量、时间补充、并发争抢）
 *
 * 覆盖范围：
 *   - 单元测试：白盒构造 tat 验证 GCRA 判定转换点与虚调度推进语义；
 *     黑盒验证突发容量（burstTime/interval）、拒绝后随时间恢复、SetRate 热调整
 *   - 基准：稳态放行热路径（time.Now + CAS 成功）与拒绝快速路径（CAS 前返回），
 *     并行形态测量 tat 单点 CAS 争抢下的吞吐退化
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/stretchr/testify/require"
)

// benchShaperSink 基准结果汇（包级 atomic：防死码消除，并行写入也不引入伪 data race）
var benchShaperSink atomic.Bool

// TestShaperAllowDenyBoundary 白盒：直接构造 tat，验证允许/拒绝精确转换点与虚调度推进语义
func TestShaperAllowDenyBoundary(t *testing.T) {
	s := NewShaper(1000) // interval = 1ms
	interval := s.Interval()
	require.EqualValues(t, int64(time.Millisecond), interval)

	// tat 领先 now 半个突发容忍量：仍在放行边界内，必放行
	now := time.Now().UnixNano()
	s.tat.Store(now + s.burstTime/2)
	require.True(t, s.Allow())

	// 放行后 tat 从 max(旧tat, now) 起恰好推进一个 interval（追赶式推进：空闲期不累积令牌）
	got := s.tat.Load()
	require.GreaterOrEqual(t, got, now+s.burstTime/2+interval)
	require.LessOrEqual(t, got, now+s.burstTime/2+interval+int64(10*time.Millisecond))

	// tat 领先超过突发容忍量（留 10ms 时钟漂移余量）：必拒绝
	now = time.Now().UnixNano()
	s.tat.Store(now + s.burstTime + int64(10*time.Millisecond))
	require.False(t, s.Allow())

	// 拒绝路径不推进 tat（无副作用：被拒消息不占用虚调度队列）
	require.Equal(t, now+s.burstTime+int64(10*time.Millisecond), s.tat.Load())
}

// TestShaperBurstCapacity 黑盒：新桶可一次性突发 burstTime/interval 条，随后转为拒绝
func TestShaperBurstCapacity(t *testing.T) {
	s := NewShaper(10000) // interval = 100µs，突发容量 = 100ms/100µs = 1000
	start := time.Now()
	allowed := 0
	for i := 0; i < 5000; i++ {
		if !s.Allow() {
			break
		}
		allowed++
	}
	elapsed := time.Since(start)

	// 下界：GCRA 数学保证前 burstTime/interval+1 = 1001 次必放行（与执行时钟快慢无关）
	require.GreaterOrEqual(t, int64(allowed), int64(1000))
	// 上界：突发容量 + 执行期时钟补充（按令牌间隔折算），留少量漂移余量
	upper := int64(1001) + elapsed.Nanoseconds()/s.Interval() + 16
	require.LessOrEqual(t, int64(allowed), upper)
}

// TestShaperRefillOverTime 黑盒：突发打满拒绝后，等待超过一个令牌间隔即恢复放行
func TestShaperRefillOverTime(t *testing.T) {
	s := NewShaper(50) // interval = 20ms，突发容量 = 5
	burst := 0
	rejected := false
	for i := 0; i < 200; i++ {
		if !s.Allow() {
			rejected = true
			break
		}
		burst++
	}
	require.True(t, rejected, "突发容量（≈5）耗尽后应出现拒绝")
	require.GreaterOrEqual(t, int64(burst), int64(5))

	// 等待超过一个令牌间隔（30ms > 20ms）：时钟追上虚调度队头，恢复放行
	time.Sleep(30 * time.Millisecond)
	require.True(t, s.Allow())
}

// TestShaperConcurrentAllow 并发安全：多 goroutine 交替 Allow（-race 下验证 CAS 循环无竞争）
// 同时验证 GCRA 全局速率语义：总放行数受 全局突发容量 + 执行期时钟补充 上界约束
func TestShaperConcurrentAllow(t *testing.T) {
	const (
		rate      = 10000 // interval = 100µs
		workers   = 8
		perWorker = 4000
	)
	s := NewShaper(rate)
	var total atomic.Int64

	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for i := 0; i < perWorker; i++ {
				if s.Allow() {
					local++
				}
			}
			total.Add(int64(local))
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	require.NotZero(t, total.Load())
	// 全局上界：突发容量 + 时钟补充 + 少量漂移余量（所有调用方共享同一 GCRA 速率）
	upper := int64(1001) + elapsed.Nanoseconds()/s.Interval() + int64(workers)
	require.LessOrEqual(t, total.Load(), upper)
	// 8×4000 次调用远超容量：必然出现过拒绝，验证争抢下无"全放行"异常
	require.Less(t, total.Load(), int64(workers*perWorker))
}

// TestShaperZeroValueFailOpen 零值 Shaper：rate 未设置（interval=0）时 fail-open 不整形
// 与限流器出错放行语义一致：整形器异常不阻断广播链路
func TestShaperZeroValueFailOpen(t *testing.T) {
	var s Shaper
	require.Zero(t, s.Rate())
	require.Zero(t, s.Interval())
	require.True(t, s.Allow())
	require.True(t, s.Allow())
}

// TestNewShaperInvalidParams 非法构造：rate<=0 回退默认广播整形速率
func TestNewShaperInvalidParams(t *testing.T) {
	for _, rate := range []int64{0, -1, -100} {
		s := NewShaper(rate)
		require.Equal(t, int64(constants.DefaultBroadcastShaperRate), s.Rate(), "rate=%d 应回退默认速率", rate)
		require.Positive(t, s.Interval())
	}

	// 合法速率原样生效：interval = 1s/rate（整数纳秒）
	s := NewShaper(12345)
	require.EqualValues(t, 12345, s.Rate())
	require.Equal(t, int64(time.Second)/12345, s.Interval())
}

// TestShaperSetRateHotAdjust SetRate 热调整（AIMD 联动）：立即生效于后续 Allow，非法值钳位到 1
func TestShaperSetRateHotAdjust(t *testing.T) {
	s := NewShaper(1000) // interval = 1ms
	s.SetRate(100)
	require.EqualValues(t, 100, s.Rate())
	require.Equal(t, int64(10*time.Millisecond), s.Interval())
	s.SetRate(100000)
	require.Equal(t, int64(10*time.Microsecond), s.Interval())

	// 钳位：<1 钳到 1（0 表示关闭整形，SetRate 不承担关闭语义）
	s.SetRate(0)
	require.EqualValues(t, 1, s.Rate())
	s.SetRate(-50)
	require.EqualValues(t, 1, s.Rate())
	require.Equal(t, int64(time.Second), s.Interval())

	// 行为验证：骤降到 interval >> 突发容忍的低速率后，放行一条即持续拒绝
	fast := NewShaper(100000) // interval = 10µs
	require.True(t, fast.Allow())
	fast.SetRate(1)                // interval = 1s >> 突发容忍 100ms
	require.True(t, fast.Allow())  // 仍处容忍边界内：放行一条
	require.False(t, fast.Allow()) // tat 领先约 1s：立即拒绝
}

// BenchmarkTokenBucketAllow 单 goroutine 热路径（稳态放行形态）
//
// 形态说明：高速率（interval=1ns）下时钟推进恒大于令牌间隔，每次 Allow 走完整
// 放行路径（time.Now + tat Load + CAS 成功推进），即生产"填谷"时的连续放行成本
func BenchmarkTokenBucketAllow(b *testing.B) {
	s := NewShaper(1_000_000_000) // interval = 1ns：热路径恒放行
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchShaperSink.Store(s.Allow())
	}
}

// BenchmarkTokenBucketAllowParallel 并行争抢（b.RunParallel 模拟多写泵并发扇出）
//
// 形态说明：与单 goroutine 同为稳态放行形态，争抢点在 tat 单点 CAS——
// GCRA 无锁化的代价集中于此（CAS 失败重读重试），测量争抢下的吞吐退化
func BenchmarkTokenBucketAllowParallel(b *testing.B) {
	s := NewShaper(1_000_000_000)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			benchShaperSink.Store(s.Allow())
		}
	})
}

// BenchmarkTokenBucketAllowRejected 拒绝快速路径（洪峰削峰态）
//
// 形态说明：tat 拨远 1 小时（远超任何基准时长），每次 Allow 在 CAS 前判定即
// 返回 false——生产洪峰时被拒消息的成本下界（拒绝不推进 tat、零副作用）
func BenchmarkTokenBucketAllowRejected(b *testing.B) {
	s := NewShaper(10000)
	s.tat.Store(time.Now().Add(time.Hour).UnixNano())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchShaperSink.Store(s.Allow())
	}
}

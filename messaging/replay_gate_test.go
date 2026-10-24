/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 10:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 10:05:00
 * @FilePath: \go-wsc\messaging\replay_gate_test.go
 * @Description: 首连离线回放门闩单元测试（暂存顺序 / 超限直通 / 未回放直通 / panic 开闸 / 并发竞态 / 免锁快路径基准）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package messaging

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReplayGate_HoldUntilEnd(t *testing.T) {
	g := NewReplayGate()
	g.begin("u-1")

	var mu sync.Mutex
	var order []int
	held := g.hold("u-1", func() { mu.Lock(); order = append(order, 1); mu.Unlock() })
	assert.True(t, held, "回放中应暂存")
	held = g.hold("u-1", func() { mu.Lock(); order = append(order, 2); mu.Unlock() })
	assert.True(t, held, "回放中应暂存")

	assert.Empty(t, order, "回放未结束不应执行")

	g.end("u-1")
	assert.Equal(t, []int{1, 2}, order, "end 后应按暂存顺序补投")
}

func TestReplayGate_DirectWhenNotReplaying(t *testing.T) {
	g := NewReplayGate()

	executed := false
	held := g.hold("u-2", func() { executed = true })
	assert.False(t, held, "未回放应直通")
	assert.True(t, executed, "直通应立即执行")
}

func TestReplayGate_HoldoutOverflow(t *testing.T) {
	g := NewReplayGate()
	g.begin("u-3")

	executed := 0
	for i := 0; i < maxReplayHoldout+5; i++ {
		g.hold("u-3", func() { executed++ })
	}
	assert.Equal(t, 5, executed, "超出暂存上限后应直接投递")
	g.end("u-3")
	assert.Equal(t, 5+maxReplayHoldout, executed, "end 应补投上限内的全部暂存")
}

func TestReplayGate_EndWithoutBegin(t *testing.T) {
	g := NewReplayGate()
	assert.NotPanics(t, func() { g.end("u-4") }, "未 begin 的 end 应幂等无操作")
}

func TestReplayGate_ReplayAfterEnd(t *testing.T) {
	g := NewReplayGate()
	g.begin("u-5")

	var order []int
	g.hold("u-5", func() { order = append(order, 1) })
	g.end("u-5")

	// 开闸后恢复直通
	executed := false
	g.hold("u-5", func() { executed = true })
	assert.True(t, executed)
	assert.Equal(t, []int{1}, order)
}

func TestReplayGate_FastPathCounterLifecycle(t *testing.T) {
	g := NewReplayGate()

	// 闸门全开：计数为 0，快路径直投
	assert.Zero(t, g.live.Load(), "初始应无回放用户")
	executed := false
	assert.False(t, g.hold("u-6", func() { executed = true }), "闸门全开应直通")
	assert.True(t, executed)

	// begin/end 幂等约束下计数守恒：重复 begin 只计一次
	g.begin("u-7")
	g.begin("u-7")
	assert.Equal(t, int64(1), g.live.Load(), "重复 begin 计数应保持一次")
	g.end("u-7")
	g.end("u-7")
	assert.Zero(t, g.live.Load(), "end 后计数应归零")

	// 多用户并存：全部回放结束后归零
	g.begin("u-8")
	g.begin("u-9")
	assert.Equal(t, int64(2), g.live.Load())
	g.end("u-8")
	g.end("u-9")
	assert.Zero(t, g.live.Load())
}

func TestReplayGate_ConcurrentHoldDuringReplay(t *testing.T) {
	g := NewReplayGate()

	const broadcasts = 200

	// 生产端：一半投递在回放中（应全部补投），一半在开闸后（应直投）
	var mu sync.Mutex
	heldTotal := 0
	delivered := make([]bool, broadcasts*2)
	next := 0

	g.begin("u-stress")
	for i := 0; i < broadcasts; i++ {
		idx := next
		next++
		held := g.hold("u-stress", func() { delivered[idx] = true })
		if held {
			mu.Lock()
			heldTotal++
			mu.Unlock()
		}
	}
	g.end("u-stress")
	for i := 0; i < broadcasts; i++ {
		idx := next
		next++
		g.hold("u-stress", func() { delivered[idx] = true })
	}

	// 守恒：全部投递恰好执行一次（暂存补投 + 直投无重复无丢失）
	deliveredCount := 0
	for _, ok := range delivered {
		if ok {
			deliveredCount++
		}
	}
	assert.Equal(t, broadcasts*2, deliveredCount, "投递应恰好执行一次")
	assert.Equal(t, broadcasts, heldTotal, "回放中投递应全部暂存（未触上限）")
	assert.Zero(t, g.live.Load(), "结束后计数应归零")
}

// TestTryBeginUserReplay_SingleFlight 速断速连窗口同一用户叠加首连触发时回放单飞
func TestTryBeginUserReplay_SingleFlight(t *testing.T) {
	m, _ := newTestManager()
	m.WithReplayGate(NewReplayGate())

	// 首个首连触发获得回放权
	assert.True(t, m.TryBeginUserReplay("u-1"), "首个触发应获得回放权")
	// inflight 回放未结束：速断速连的重复触发必须放弃（不重复 drain、不提前开闸）
	assert.False(t, m.TryBeginUserReplay("u-1"), "inflight 期间的重复触发应放弃回放权")

	// 回放结束：标记释放，门闩开闸
	m.EndUserReplay("u-1")
	assert.True(t, m.TryBeginUserReplay("u-1"), "回放结束后应可再次获得回放权")
	m.EndUserReplay("u-1")

	// 门闩计数守恒：单飞下 begin/end 严格配对，多次触发后归零
	assert.Zero(t, m.replayGate.live.Load(), "全部回放结束后门闩计数应归零")

	// 不同用户互不影响
	assert.True(t, m.TryBeginUserReplay("u-2"))
	assert.True(t, m.TryBeginUserReplay("u-3"))
	m.EndUserReplay("u-2")
	m.EndUserReplay("u-3")
}

// TestTryBeginUserReplay_ReplayGatedUntilEnd 单飞获权后门闩保持关闭直到回放结束
func TestTryBeginUserReplay_ReplayGatedUntilEnd(t *testing.T) {
	m, _ := newTestManager()
	m.WithReplayGate(NewReplayGate())

	require := assert.New(t)
	require.True(m.TryBeginUserReplay("u-gated"))

	held := m.replayGate.hold("u-gated", func() {})
	require.True(held, "回放进行中实时投递应被暂存")

	m.EndUserReplay("u-gated")
	executed := false
	held = m.replayGate.hold("u-gated", func() { executed = true })
	require.False(held, "回放结束后应恢复直通")
	require.True(executed)
}

func BenchmarkReplayGate_HoldGateOpen(b *testing.B) {
	// 闸门全开（绝大多数时刻）：快路径仅一次原子读
	g := NewReplayGate()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.hold("u-bench", func() {})
	}
}

func BenchmarkReplayGate_HoldDuringReplay(b *testing.B) {
	// 回放进行中：锁路径全流程（RLock 探测 + 写锁暂存）
	g := NewReplayGate()
	g.begin("u-bench")
	defer g.end("u-bench")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.hold("u-bench", func() {})
	}
}

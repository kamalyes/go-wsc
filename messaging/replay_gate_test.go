/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 10:05:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 10:05:00
 * @FilePath: \go-wsc\messaging\replay_gate_test.go
 * @Description: 首连离线回放门闩单元测试（暂存顺序 / 超限直通 / 未回放直通 / panic 开闸）
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

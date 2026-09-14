/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 21:52:19
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 11:00:00
 * @FilePath: \go-wsc\hub\coalescer_test.go
 * @Description: 高频合并器测试 —— latest-wins 覆盖 + 容量保护 + Drain 换出
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"fmt"
	"sync"
	"testing"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newEphemeralMsg 构造高频测试消息（seq 模拟流式版本）
func newEphemeralMsg(key string, seq int) *models.HubMessage {
	return models.NewHubMessage().
		SetMessageType(models.MessageTypeTyping).
		WithGuarantee(models.GuaranteeEphemeral).
		SetSender(key).
		SetContent(fmt.Sprintf("typing-%d", seq))
}

// TestCoalescerLatestWins 同 key 覆盖：只保留最新值（覆盖≠丢弃，merged 计数）
func TestCoalescerLatestWins(t *testing.T) {
	c := NewCoalescer(100)

	// 同 key 写 5 条（版本递增）：首次插入不合并，后续 4 次覆盖均报告 merged
	for i := 1; i <= 5; i++ {
		accepted, merged := c.Offer("u1|typing", newEphemeralMsg("u1", i))
		assert.True(t, accepted)
		assert.Equal(t, i > 1, merged, "仅覆盖（i>1）应报告 merged")
	}

	assert.Equal(t, int64(1), c.Size(), "同 key 应只占一个槽位")
	assert.Equal(t, int64(4), c.MergedCount(), "4 次覆盖应计入 merged")

	// Drain 取出的应是最新值（seq=5）
	batch := c.Drain()
	require.Len(t, batch, 1)
	assert.Equal(t, "typing-5", batch[0].Content, "应取出最新版本")
	assert.Equal(t, int64(0), c.Size(), "Drain 后应清空")
}

// TestCoalescerMultiKey 多 key 并存：不同 key 各自独立 latest-wins
func TestCoalescerMultiKey(t *testing.T) {
	c := NewCoalescer(100)

	for i := 0; i < 3; i++ {
		key := fmt.Sprintf("user-%d|typing", i)
		c.Offer(key, newEphemeralMsg(fmt.Sprintf("user-%d", i), 1))
		c.Offer(key, newEphemeralMsg(fmt.Sprintf("user-%d", i), 2))
	}

	assert.Equal(t, int64(3), c.Size())

	batch := c.Drain()
	require.Len(t, batch, 3, "3 个 key 各自最新值")
	contents := map[string]bool{}
	for _, msg := range batch {
		contents[msg.Content] = true
	}
	assert.True(t, contents["typing-2"], "所有 key 都应是第 2 版")
}

// TestCoalescerCapacity 容量保护：新 key 超限被拒，已有 key 覆盖不受限
func TestCoalescerCapacity(t *testing.T) {
	capacity := 10
	c := NewCoalescer(capacity)

	// 填满 10 个不同 key
	for i := 0; i < capacity; i++ {
		key := fmt.Sprintf("u%d|typing", i)
		accepted, _ := c.Offer(key, newEphemeralMsg(fmt.Sprintf("u%d", i), 1))
		assert.True(t, accepted, "第 %d 个新 key 应被接受", i)
	}

	// 第 11 个新 key：容量满，拒绝
	accepted, _ := c.Offer("u-new|typing", newEphemeralMsg("u-new", 1))
	assert.False(t, accepted, "容量满后新 key 应被拒绝")

	// 已有 key 覆盖不受容量限制（更新不增条目）
	accepted, merged := c.Offer("u0|typing", newEphemeralMsg("u0", 99))
	assert.True(t, accepted, "已有 key 的覆盖不应受容量限制")
	assert.True(t, merged, "覆盖应报告 merged")

	assert.Equal(t, int64(capacity), c.Size(), "容量应保持上限")
}

// TestCoalescerInvalidInput 防御：空 key / nil msg 拒绝
func TestCoalescerInvalidInput(t *testing.T) {
	c := NewCoalescer(10)
	accepted, _ := c.Offer("", newEphemeralMsg("u", 1))
	assert.False(t, accepted, "空 key 应拒绝")
	accepted, _ = c.Offer("k", nil)
	assert.False(t, accepted, "nil 消息应拒绝")
	assert.Empty(t, c.Drain())
}

// TestCoalescerDrainSwap Drain 换出语义：Drain 后再 Offer 重新累积
func TestCoalescerDrainSwap(t *testing.T) {
	c := NewCoalescer(100)

	c.Offer("k|typing", newEphemeralMsg("u", 1))
	batch := c.Drain()
	require.Len(t, batch, 1)

	// Drain 后重新累积
	c.Offer("k|typing", newEphemeralMsg("u", 2))
	assert.Equal(t, int64(1), c.Size())
	batch = c.Drain()
	require.Len(t, batch, 1)
	assert.Equal(t, "typing-2", batch[0].Content)
}

// TestCoalescerDefaultCapacity 零值容量兜底（constants.DefaultCoalescerCapacity）
func TestCoalescerDefaultCapacity(t *testing.T) {
	c := NewCoalescer(0)
	assert.Equal(t, constants.DefaultCoalescerCapacity, c.capacity)
}

// TestCoalescerConcurrent 并发 Offer + Drain：无竞态、计数收敛
func TestCoalescerConcurrent(t *testing.T) {
	c := NewCoalescer(1000)

	var wg sync.WaitGroup
	keyCount := 50
	// 并发写：50 个 key，每 key 20 次覆盖
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for round := 0; round < 20; round++ {
				for k := 0; k < keyCount; k++ {
					c.Offer(fmt.Sprintf("u%d|typing", k), newEphemeralMsg(fmt.Sprintf("u%d", k), round))
				}
			}
		}()
	}
	// 并发 Drain
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				c.Drain()
			}
		}()
	}
	wg.Wait()

	// 终态：最后一次 Drain 清空
	final := c.Drain()
	assert.LessOrEqual(t, len(final), keyCount, "终态每 key 最多 1 条")
	assert.Equal(t, int64(0), c.Size(), "最终 Drain 后应为空")
}

// TestEphemeralKey 合并 key 构成：userID × 消息类型
func TestEphemeralKey(t *testing.T) {
	client := &Client{ID: "c1", UserID: "u1"}
	msg := models.NewHubMessage().SetMessageType(models.MessageTypeTyping)
	assert.Equal(t, "u1|typing", ephemeralKey(client, msg))
}

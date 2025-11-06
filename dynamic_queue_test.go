/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-07 00:54:03
 * @FilePath: \go-wsc\dynamic_queue_test.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

// TestSmartGrowth 测试智能增长策略
func TestSmartGrowth(t *testing.T) {
	q := NewDynamicQueue(10, 100000)
	defer q.Close()

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}

	// 逐步增加消息，观察容量变化
	steps := []int{10, 100, 500, 1000, 5000, 10000, 20000}
	
	t.Log("容量增长过程:")
	var prevResizeCount int64 = 0
	
	for _, targetCount := range steps {
		// 填充到目标数量
		for q.Len() < targetCount {
			err := q.Push(msg)
			assert.NoError(t, err, "Push failed at count %d", q.Len())
		}
		
		stats := q.Stats()
		resizeCount := stats["resizeCount"].(int64)
		newResizes := resizeCount - prevResizeCount
		
		t.Logf("  消息数: %5d, 容量: %6d, 本阶段扩容: %d次, 累计扩容: %d次, 使用率: %.1f%%",
			targetCount, q.Cap(), newResizes, resizeCount, stats["utilization"])
		
		prevResizeCount = resizeCount
	}
	
	// 验证最大容量没有爆炸式增长
	// 对于20000消息，2倍增长需要32768容量（10->20->40...->32768）
	// 智能增长应该更节省（预期约22000-25000）
	finalCap := q.Cap()
	doubleGrowthCap := 10
	for doubleGrowthCap < 20000 {
		doubleGrowthCap *= 2
	}
	
	savings := float64(doubleGrowthCap-finalCap) / float64(doubleGrowthCap) * 100
	t.Logf("\n内存节省: 2倍增长需要 %d, 智能增长需要 %d, 节省 %.1f%%",
		doubleGrowthCap, finalCap, savings)
	
	assert.Less(t, finalCap, doubleGrowthCap*2, "智能增长容量不应超过2倍增长策略的2倍")
	
	if finalCap < doubleGrowthCap {
		t.Logf("✓ 智能增长策略有效节省了内存")
	}
}

// TestAdaptiveShrink 测试自适应缩容
func TestAdaptiveShrink(t *testing.T) {
	q := NewDynamicQueue(10, 100000)
	defer q.Close()

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}

	// 先扩容到大容量
	for i := 0; i < 10000; i++ {
		err := q.Push(msg)
		assert.NoError(t, err, "Push failed")
	}

	expandedCap := q.Cap()
	t.Logf("扩容后容量: %d", expandedCap)

	// 逐步消费并观察缩容
	shrinkSteps := []int{9000, 7000, 5000, 3000, 1000, 100}
	
	for _, remaining := range shrinkSteps {
		// 消费到目标剩余数量
		for q.Len() > remaining {
			_, err := q.Pop()
			assert.NoError(t, err, "Pop failed")
		}
		
		stats := q.Stats()
		t.Logf("剩余消息: %5d, 容量: %6d, 缩容次数: %d, 使用率: %.1f%%",
			remaining, q.Cap(), stats["shrinkCount"], stats["utilization"])
	}

	// 验证最终容量缩小了
	finalCap := q.Cap()
	assert.Less(t, finalCap, expandedCap, "容量应该缩小")
	t.Logf("✓ 容量成功缩小：%d -> %d (%.1f%%)", expandedCap, finalCap, 
		float64(finalCap)/float64(expandedCap)*100)
}

// TestGrowthComparison 对比不同增长策略的内存使用
func TestGrowthComparison(t *testing.T) {
	testCases := []struct {
		name          string
		initialCap    int
		targetCount   int
		expectedMaxCap int // 预期的最大容量
	}{
		{"小规模", 10, 100, 256},       // 2倍增长: 10->20->40->80->160
		{"中规模", 100, 5000, 10000},   // 混合增长
		{"大规模", 1000, 50000, 80000}, // 渐进增长
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			q := NewDynamicQueue(tc.initialCap, 100000)
			defer q.Close()

			msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}

			// 填充到目标数量
			for i := 0; i < tc.targetCount; i++ {
				err := q.Push(msg)
				assert.NoError(t, err, "Push failed at %d", i)
			}

			finalCap := q.Cap()
			stats := q.Stats()
			
			t.Logf("目标数量: %d, 最终容量: %d, 扩容次数: %d",
				tc.targetCount, finalCap, stats["resizeCount"])
			
			// 计算如果用2倍策略的容量
			doubleGrowthCap := tc.initialCap
			for doubleGrowthCap < tc.targetCount {
				doubleGrowthCap *= 2
			}
			
			memSaving := float64(doubleGrowthCap-finalCap) / float64(doubleGrowthCap) * 100
			t.Logf("2倍增长需要: %d, 智能增长需要: %d, 节省内存: %.1f%%",
				doubleGrowthCap, finalCap, memSaving)
			
			if finalCap < doubleGrowthCap {
				t.Logf("✓ 智能增长策略更节省内存")
			}
		})
	}
}

// BenchmarkSmartGrowth 性能测试智能增长
func BenchmarkSmartGrowth(b *testing.B) {
	q := NewDynamicQueue(10, 1000000)
	defer q.Close()

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("benchmark")}

	// 启动消费者
	go func() {
		for !q.IsClosed() {
			q.TryPop()
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := q.Push(msg); err == nil {
				break
			}
		}
	}

	b.StopTimer()
	stats := q.Stats()
	b.Logf("最终容量: %d, 扩容次数: %d, 缩容次数: %d",
		stats["capacity"], stats["resizeCount"], stats["shrinkCount"])
}

// TestDynamicQueue_SetAutoResize 测试自动调整开关
func TestDynamicQueue_SetAutoResize(t *testing.T) {
	q := NewDynamicQueue(4, 10)
	defer q.Close()

	// 禁用自动扩容
	q.SetAutoResize(false)

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}

	// 填满队列
	for i := 0; i < 4; i++ {
		err := q.Push(msg)
		assert.NoError(t, err, "Push should succeed")
	}

	// 再次push应该失败（队列已满且不自动扩容）
	err := q.Push(msg)
	assert.Equal(t, ErrBufferFull, err, "Should return ErrBufferFull when auto-resize is disabled")

	// 启用自动扩容
	q.SetAutoResize(true)

	// 现在应该可以push（会自动扩容）
	err = q.Push(msg)
	assert.NoError(t, err, "Push should succeed after enabling auto-resize")
	assert.Greater(t, q.Cap(), 4, "Capacity should have expanded")
}

// TestDynamicQueue_PopFromEmpty 测试从空队列弹出
func TestDynamicQueue_PopFromEmpty(t *testing.T) {
	q := NewDynamicQueue(10, 100)
	defer q.Close()

	// 从空队列弹出应该阻塞，我们用TryPop测试
	msg, ok := q.TryPop()
	assert.Nil(t, msg, "TryPop from empty queue should return nil")
	assert.False(t, ok, "TryPop from empty queue should return false")
}

// TestDynamicQueue_ClosedOperations 测试关闭后的操作
func TestDynamicQueue_ClosedOperations(t *testing.T) {
	q := NewDynamicQueue(10, 100)
	
	// 先添加一些消息
	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}
	err := q.Push(msg)
	assert.NoError(t, err, "Push before close should succeed")

	// 关闭队列
	q.Close()
	assert.True(t, q.IsClosed(), "Queue should be closed")

	// 关闭后push应该失败
	err = q.Push(msg)
	assert.Equal(t, ErrClose, err, "Push after close should return ErrClose")

	// Pop应该返回队列中剩余的消息
	poppedMsg, err := q.Pop()
	assert.NoError(t, err, "Pop should succeed for remaining message")
	assert.NotNil(t, poppedMsg, "Should get the message")

	// 再次Pop应该返回ErrClose（队列已空且已关闭）
	_, err = q.Pop()
	assert.Equal(t, ErrClose, err, "Pop after close with empty queue should return ErrClose")

	// TryPop应该返回false
	_, ok := q.TryPop()
	assert.False(t, ok, "TryPop after close should return false")
}

// TestDynamicQueue_MaxCapacity 测试最大容量限制
func TestDynamicQueue_MaxCapacity(t *testing.T) {
	maxCap := 20
	q := NewDynamicQueue(4, maxCap)
	defer q.Close()

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}

	// 填充到最大容量
	for i := 0; i < maxCap; i++ {
		err := q.Push(msg)
		assert.NoError(t, err, "Push should succeed until max capacity")
	}

	assert.Equal(t, maxCap, q.Cap(), "Capacity should be at max")

	// 超过最大容量应该失败
	err := q.Push(msg)
	assert.Equal(t, ErrBufferFull, err, "Push beyond max capacity should fail")
}

// TestDynamicQueue_EdgeCases 测试边界情况
func TestDynamicQueue_EdgeCases(t *testing.T) {
	t.Run("MinimalCapacity", func(t *testing.T) {
		q := NewDynamicQueue(1, 100)
		defer q.Close()

		msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}
		
		err := q.Push(msg)
		assert.NoError(t, err, "Push to minimal queue should succeed")
		
		popped, err := q.Pop()
		assert.NoError(t, err, "Pop from minimal queue should succeed")
		assert.Equal(t, "test", string(popped.msg), "Popped message should match")
	})

	t.Run("LargeCapacity", func(t *testing.T) {
		q := NewDynamicQueue(10000, 100000)
		defer q.Close()

		assert.Equal(t, 10000, q.Cap(), "Large initial capacity should be set")
		assert.Equal(t, 0, q.Len(), "Queue should be empty initially")
	})

	t.Run("EmptyStats", func(t *testing.T) {
		q := NewDynamicQueue(10, 100)
		defer q.Close()

		stats := q.Stats()
		assert.Equal(t, int64(0), stats["length"].(int64), "Empty queue length should be 0")
		assert.Equal(t, 10, stats["capacity"].(int), "Capacity should match initial")
		assert.Equal(t, float64(0), stats["utilization"].(float64), "Utilization should be 0")
		assert.Equal(t, true, stats["autoResize"].(bool), "AutoResize should be enabled by default")
		assert.Equal(t, false, stats["closed"].(bool), "Queue should not be closed")
	})
}

// TestDynamicQueue_GrowthBoundaries 测试增长策略的边界
func TestDynamicQueue_GrowthBoundaries(t *testing.T) {
	testCases := []struct {
		name       string
		initialCap int
		pushCount  int
		minExpectedCap int
	}{
		{"Below1024", 512, 600, 1024},      // 应触发2倍增长
		{"At1024", 1024, 1100, 1536},       // 应触发1.5倍增长
		{"Above10000", 10000, 11000, 12000}, // 应触发固定增量
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			q := NewDynamicQueue(tc.initialCap, 100000)
			defer q.Close()

			msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}

			for i := 0; i < tc.pushCount; i++ {
				err := q.Push(msg)
				assert.NoError(t, err, "Push %d should succeed", i)
			}

			assert.GreaterOrEqual(t, q.Cap(), tc.minExpectedCap, 
				"Capacity should grow to at least %d", tc.minExpectedCap)
		})
	}
}

// TestDynamicQueue_ShrinkBoundaries 测试缩容策略的边界
func TestDynamicQueue_ShrinkBoundaries(t *testing.T) {
	q := NewDynamicQueue(10, 100000)
	defer q.Close()

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}

	// 扩容到较大容量
	for i := 0; i < 1000; i++ {
		err := q.Push(msg)
		assert.NoError(t, err, "Push should succeed")
	}

	expandedCap := q.Cap()
	t.Logf("Expanded capacity: %d", expandedCap)

	// 消费大部分消息（保留少于25%）
	targetRemaining := expandedCap / 5 // 20%
	for q.Len() > targetRemaining {
		_, err := q.Pop()
		assert.NoError(t, err, "Pop should succeed")
	}

	// 等待可能的缩容（需要再次触发Pop）
	for i := 0; i < 10; i++ {
		q.TryPop()
	}

	shrunkCap := q.Cap()
	t.Logf("After consuming to %d messages, capacity: %d -> %d", 
		targetRemaining, expandedCap, shrunkCap)

	// 验证至少保留了消息的2倍空间
	assert.GreaterOrEqual(t, shrunkCap, q.Len()*2, 
		"Shrunk capacity should be at least 2x message count")
}

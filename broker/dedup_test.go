/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:38:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:38:00
 * @FilePath: \go-wsc\broker\dedup_test.go
 * @Description: 本地两代 TTL 去重窗测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDedupWindow_FirstSeen(t *testing.T) {
	dw := NewDedupWindow(time.Minute, 8)
	assert.True(t, dw.Seen("m1\x00u1"), "首见返回 true")
	assert.False(t, dw.Seen("m1\x00u1"), "重复返回 false")
	assert.True(t, dw.Seen("m1\x00u2"), "同 MessageID 不同 Receiver 是独立键")
	assert.True(t, dw.Seen("m2\x00u1"), "不同 MessageID 互不影响")
}

func TestDedupWindow_RolloverCrossGeneration(t *testing.T) {
	// TTL 200ms，rollover 100ms：两代交替后跨代窗口仍能拦截重复
	dw := NewDedupWindow(200*time.Millisecond, 4)
	assert.True(t, dw.Seen("key"))
	time.Sleep(110 * time.Millisecond) // 跨过一次 rollover，key 进入 prev 代
	assert.False(t, dw.Seen("key"), "代际切换后仍应拦截（prev 代覆盖跨代窗口）")
}

func TestDedupWindow_ExpiredAfterTwoGenerations(t *testing.T) {
	dw := NewDedupWindow(100*time.Millisecond, 4)
	assert.True(t, dw.Seen("key"))
	time.Sleep(230 * time.Millisecond) // 超过 2*rollover，key 完全出窗
	assert.True(t, dw.Seen("key"), "出窗后视为新键（超窗重复由落库幂等兜底）")
}

func TestDedupWindow_ConcurrentSameKey(t *testing.T) {
	dw := NewDedupWindow(time.Minute, 16)
	const workers = 32
	var mu sync.Mutex
	firstCount := 0
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if dw.Seen("hot-key") {
				mu.Lock()
				firstCount++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, firstCount, "并发同键仅一次首见（分片锁保证）")
}

func TestDedupWindow_Defaults(t *testing.T) {
	dw := NewDedupWindow(0, 0)
	assert.Equal(t, DefaultDedupTTL, dw.ttl, "零值 TTL 走默认")
	assert.Len(t, dw.shards, DefaultDedupShards, "零值分片数走默认")
}

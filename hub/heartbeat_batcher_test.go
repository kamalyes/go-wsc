/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-22 00:00:00
 * @FilePath: \go-wsc\hub\heartbeat_batcher_test.go
 * @Description: 心跳统计批量聚合器测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"fmt"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// TestHeartbeatStatsBatcher_AddAndFlush 测试批量聚合器的添加和刷写
func TestHeartbeatStatsBatcher_AddAndFlush(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	batcher := newHeartbeatStatsBatcher(hub)

	for i := 0; i < 10; i++ {
		batcher.Add(
			fmt.Sprintf("client-%d", i),
			time.Now(),
			time.Now(),
			float64(i*10),
			time.Now(),
		)
	}

	batcher.mu.Lock()
	count := len(batcher.buffer)
	batcher.mu.Unlock()
	assert.Equal(t, 10, count, "应有10条心跳统计")

	batcher.flush()

	batcher.mu.Lock()
	count = len(batcher.buffer)
	batcher.mu.Unlock()
	assert.Equal(t, 0, count, "刷写后 buffer 应为空")
}

// TestHeartbeatStatsBatcher_SameClientOverwrite 测试同一客户端只保留最新统计
func TestHeartbeatStatsBatcher_SameClientOverwrite(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	batcher := newHeartbeatStatsBatcher(hub)

	for i := 0; i < 5; i++ {
		batcher.Add("same-client", time.Now(), time.Now(), float64(i*10), time.Now())
	}

	batcher.mu.Lock()
	count := len(batcher.buffer)
	entry := batcher.buffer["same-client"]
	batcher.mu.Unlock()

	assert.Equal(t, 1, count, "同一客户端应只保留1条")
	assert.NotNil(t, entry)
	assert.Equal(t, float64(40), entry.PingMs, "应保留最新的统计值")
}

// TestHeartbeatStatsBatcher_ConcurrentAdd 测试并发添加
func TestHeartbeatStatsBatcher_ConcurrentAdd(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	batcher := newHeartbeatStatsBatcher(hub)

	const numGoroutines = 100
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			batcher.Add(
				fmt.Sprintf("client-%d", idx%50),
				time.Now(),
				time.Now(),
				float64(idx),
				time.Now(),
			)
		}(i)
	}

	wg.Wait()

	batcher.mu.Lock()
	count := len(batcher.buffer)
	batcher.mu.Unlock()
	assert.Equal(t, 50, count, "应有50个不同客户端的统计")
}

// TestHeartbeatStatsBatcher_StartAndStop 测试启动和停止
func TestHeartbeatStatsBatcher_StartAndStop(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	batcher := newHeartbeatStatsBatcher(hub)
	batcher.Start()

	for i := 0; i < 5; i++ {
		batcher.Add(fmt.Sprintf("client-%d", i), time.Now(), time.Now(), float64(i), time.Now())
	}

	batcher.Stop()
	hub.Shutdown()

	batcher.mu.Lock()
	count := len(batcher.buffer)
	batcher.mu.Unlock()
	assert.Equal(t, 0, count, "停止后 buffer 应为空")
}

// TestHeartbeatStatsBatcher_EmptyFlush 测试空 buffer 刷写不报错
func TestHeartbeatStatsBatcher_EmptyFlush(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	batcher := newHeartbeatStatsBatcher(hub)
	batcher.flush() // 不应 panic
}

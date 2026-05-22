/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-22 00:00:00
 * @FilePath: \go-wsc\hub\lifecycle_test.go
 * @Description: lifecycle.go 集成测试（Hub 启动/关闭时 batcher 生命周期）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// TestHubStartsHeartbeatBatcher 测试 Hub 启动时自动启动 batcher
func TestHubStartsHeartbeatBatcher(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	assert.NotNil(t, hub.heartbeatBatcher, "Hub 应创建 heartbeatBatcher")

	go hub.Run()
	hub.WaitForStart()
	time.Sleep(50 * time.Millisecond)

	hub.heartbeatBatcher.Add("test-client", time.Now(), time.Now(), 10.0, time.Now())

	hub.heartbeatBatcher.mu.Lock()
	count := len(hub.heartbeatBatcher.buffer)
	hub.heartbeatBatcher.mu.Unlock()

	assert.Equal(t, 1, count, "batcher 应接受数据")

	hub.Shutdown()
}

// TestHubShutdownFlushesBatcher 测试 Hub 关闭时刷写 batcher 剩余数据
func TestHubShutdownFlushesBatcher(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	go hub.Run()
	hub.WaitForStart()
	time.Sleep(50 * time.Millisecond)

	// 添加数据后立即关闭
	hub.heartbeatBatcher.Add("shutdown-client", time.Now(), time.Now(), 5.0, time.Now())

	hub.Shutdown()

	// 关闭后 batcher buffer 应为空（已刷写）
	hub.heartbeatBatcher.mu.Lock()
	count := len(hub.heartbeatBatcher.buffer)
	hub.heartbeatBatcher.mu.Unlock()
	assert.Equal(t, 0, count, "关闭后 batcher buffer 应为空")
}

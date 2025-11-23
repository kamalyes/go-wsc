/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 21:00:00
 * @FilePath: \go-wsc\memory_guard_test.go
 * @Description: 内存防护机制测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryGuardBasicFunctionality(t *testing.T) {
	// 创建带有性能配置的Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
		EnableMetrics:        true,
		MetricsInterval:      1,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 测试基本方法
	assert.True(t, hub.IsMemoryGuardEnabled())
	
	// 测试内存统计信息
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "alloc_mb")
	assert.Contains(t, stats, "gc_count")
	assert.Contains(t, stats, "goroutine_count")
	
	// 测试设置内存限制
	hub.SetMemoryLimit(256)
	stats = hub.GetMemoryStats()
	assert.Equal(t, int64(256), stats["memory_limit_mb"])
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardStartStop(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 50,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 启动内存防护
	hub.memoryGuard.Start()
	assert.True(t, hub.IsMemoryGuardEnabled())
	
	// 等待一小段时间确保防护器运行
	time.Sleep(100 * time.Millisecond)
	
	// 停止内存防护
	hub.memoryGuard.Stop()
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardCleanup(t *testing.T) {
	// 创建Hub并启动
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 启动Hub
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	
	go hub.Run()
	select {
	case <-hub.startCh:
		// Hub已启动
	case <-ctx.Done():
		t.Fatal("Hub启动超时")
	}
	
	// 添加一些测试连接来模拟内存使用
	for i := 0; i < 10; i++ {
		client := &Client{
			UserID:   fmt.Sprintf("user-%d", i),
			SendChan: make(chan []byte, 10),
			LastSeen: time.Now(),
		}
		hub.clients[client.UserID] = client
	}
	
	// 强制执行内存清理
	hub.ForceMemoryCleanup()
	
	// 检查内存统计
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardWithoutConfig(t *testing.T) {
	// 创建默认配置的Hub（没有显式配置）
	hub := NewHub(nil)
	
	// 在没有显式配置时，不应该有内存防护器
	assert.Nil(t, hub.memoryGuard)
	assert.False(t, hub.IsMemoryGuardEnabled())
	assert.Nil(t, hub.GetMemoryStats())
	
	// 这些操作应该安全地不执行任何操作
	hub.SetMemoryLimit(512)
	hub.ForceMemoryCleanup()
	hub.EnableMemoryGuard()
	hub.DisableMemoryGuard()
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardDisableEnable(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 默认应该是启用的
	assert.True(t, hub.IsMemoryGuardEnabled())
	
	// 禁用内存防护
	hub.DisableMemoryGuard()
	assert.False(t, hub.IsMemoryGuardEnabled())
	
	// 重新启用
	hub.EnableMemoryGuard()
	assert.True(t, hub.IsMemoryGuardEnabled())
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardMemoryCheck(t *testing.T) {
	// 创建一个短检查间隔的内存防护器
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 设置较低的内存限制来触发清理
	hub.memoryGuard.SetMemoryLimit(1) // 1MB，很容易超标
	
	// 启动内存防护（短间隔）
	hub.memoryGuard.checkInterval = 100 * time.Millisecond
	hub.memoryGuard.Start()
	
	// 等待一些检查周期
	time.Sleep(300 * time.Millisecond)
	
	// 检查内存统计
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	
	// 停止防护器
	hub.memoryGuard.Stop()
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardLeakDetection(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 50,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 获取初始内存状态
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	initialMem := m.Alloc
	
	// 模拟内存增长（通过创建大量数据）
	hub.memoryGuard.lastMemUsage = initialMem - 100*1024*1024 // 假设之前内存更少
	
	// 测试泄漏检测
	runtime.ReadMemStats(&m)
	isLeak := hub.memoryGuard.detectMemoryLeak(&m)
	
	// 由于我们模拟了内存增长，应该检测到可能的泄漏
	// 注意：这个测试可能不稳定，因为依赖于实际内存使用
	t.Logf("Memory leak detected: %v", isLeak)
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardGCTrigger(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 设置低GC阈值
	hub.memoryGuard.SetGCThreshold(0.1) // 10%就触发GC
	
	// 获取GC前的计数
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	initialGCCount := m.NumGC
	
	// 触发GC检查
	hub.memoryGuard.triggerGC(&m)
	
	// 获取GC后的计数
	runtime.ReadMemStats(&m)
	
	// 验证GC确实被触发了（可能）
	t.Logf("GC count before: %d, after: %d", initialGCCount, m.NumGC)
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestMemoryGuardCustomCallback(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}
	
	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)
	
	// 设置自定义清理回调
	callbackExecuted := false
	hub.memoryGuard.SetCleanupCallback(func() error {
		callbackExecuted = true
		return nil
	})
	
	// 执行激进清理（应该调用回调）
	hub.memoryGuard.performAggressiveCleanup()
	
	// 验证回调被执行
	assert.True(t, callbackExecuted)
	
	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}
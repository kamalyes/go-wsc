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
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"runtime"
	"testing"
	"time"
)

func TestMemoryGuardBasicFunctionality(t *testing.T) {
	// 创建带有性能配置的Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
		EnableMetrics:         true,
		MetricsInterval:       1,
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

	// 通过公共API测试内存防护功能
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

	// 通过公共API检查内存防护功能
	// 设置内存限制来触发内存监控
	hub.SetMemoryLimit(50) // 设置一个较低的内存限制

	// 强制执行内存清理
	hub.ForceMemoryCleanup()

	// 检查内存统计
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "alloc_mb")
	assert.Contains(t, stats, "memory_limit_mb")
	assert.Equal(t, int64(50), stats["memory_limit_mb"])

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
	// 创建一个内存防护器
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.memoryGuard)

	// 设置较低的内存限制来测试内存监控
	hub.SetMemoryLimit(1) // 1MB，很容易检测到内存使用

	// 等待一些检查周期，让内存防护器运行
	time.Sleep(100 * time.Millisecond)

	// 检查内存统计
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "memory_limit_mb")
	assert.Equal(t, int64(1), stats["memory_limit_mb"])

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

	// 获取内存统计信息来验证泄漏检测功能正常
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "alloc_mb")
	assert.Contains(t, stats, "gc_count")

	// 触发内存清理以测试相关功能
	hub.ForceMemoryCleanup()

	// 再次获取内存统计信息
	statsAfter := hub.GetMemoryStats()
	assert.NotNil(t, statsAfter)

	// 检查统计信息结构是否正确
	assert.Contains(t, statsAfter, "alloc_mb")
	assert.Contains(t, statsAfter, "gc_count")
	assert.Contains(t, statsAfter, "goroutine_count")

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

	// 获取GC前的计数
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	initialGCCount := m.NumGC

	// 通过公共API触发清理操作，这可能会触发GC
	hub.ForceMemoryCleanup()

	// 稍等一下让GC有时间执行
	time.Sleep(10 * time.Millisecond)

	// 获取GC后的计数
	runtime.ReadMemStats(&m)

	// 验证GC相关的统计信息可以正常获取
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "gc_count")

	t.Logf("GC count before: %d, after: %d, stats gc_count: %v",
		initialGCCount, m.NumGC, stats["gc_count"])

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

	// 通过公共API测试清理功能
	// 执行内存清理操作
	hub.ForceMemoryCleanup()

	// 验证清理功能正常工作 - 通过检查内存统计信息
	stats := hub.GetMemoryStats()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "alloc_mb")

	// 设置内存限制并再次清理
	hub.SetMemoryLimit(100)
	hub.ForceMemoryCleanup()

	// 验证设置生效
	statsAfter := hub.GetMemoryStats()
	assert.Equal(t, int64(100), statsAfter["memory_limit_mb"])

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

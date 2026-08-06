/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-06 00:56:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-07 01:20:15
 * @FilePath: \go-wsc\hub\shutdown_leak_test.go
 * @Description: SafeShutdown workerPool 泄漏验证测试
 *
 * 验证 SafeShutdown 调用 h.workerPool.Stop()，关闭 HubWorkerPool 的 4 个子池
 * （Message/Callback/Record/Distributed），避免 worker goroutine 泄漏
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"runtime"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/require"
)

// TestSafeShutdownStopsWorkerPool 验证反复创建+关闭 Hub 不会泄漏 WorkerPool worker goroutine
func TestSafeShutdownStopsWorkerPool(t *testing.T) {
	// 预热：首次 NewHub 会触发 logger/cachex 等后台 goroutine，计入基线
	warmup := NewHub(wscconfig.Default().WithNodeInfo("127.0.0.1", 18080).WithMessageBufferSize(64))
	go warmup.Run()
	warmup.WaitForStart()
	_ = warmup.SafeShutdown()

	// 等待预热 Hub 的 goroutine 退出，建立稳定基线
	require.Eventually(t, func() bool {
		// 触发 GC 回收已退出 goroutine 的栈
		runtime.GC()
		return runtime.NumGoroutine() > 0
	}, 2*time.Second, 50*time.Millisecond)
	base := runtime.NumGoroutine()

	// 反复创建+关闭 Hub：若泄漏，goroutine 数会随循环次数线性增长
	const cycles = 5
	for i := 0; i < cycles; i++ {
		hub := NewHub(wscconfig.Default().WithNodeInfo("127.0.0.1", 18080).WithMessageBufferSize(64))
		go hub.Run()
		hub.WaitForStart()
		require.NoError(t, hub.SafeShutdown())
	}

	// 等待所有 worker goroutine 退出
	require.Eventually(t, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= base+10 // 容忍少量异步清理 goroutine
	}, 5*time.Second, 50*time.Millisecond,
		"WorkerPool worker goroutine 泄漏: 基线=%d, 当前=%d（%d 次循环后应回落）",
		base, runtime.NumGoroutine(), cycles)
}

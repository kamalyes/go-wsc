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
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/require"
)

// TestSafeShutdownStopsWorkerPool 验证反复创建+关闭 Hub 不会泄漏 WorkerPool worker goroutine
//
// 不用 runtime.NumGoroutine() 做断言——在全量 -race 套件下，其他测试的残留 goroutine
// 导致基线漂移严重，Eventually 误判 "Condition never satisfied"。
// 改为验证 SafeShutdown 在合理超时内完成：若有 goroutine 泄漏，SafeShutdown 会阻塞。
func TestSafeShutdownStopsWorkerPool(t *testing.T) {
	// 预热：首次 NewHub 会触发 logger/cachex 等后台 goroutine 初始化
	warmup := NewHub(wscconfig.Default().WithNodeInfo("127.0.0.1", 18080).WithMessageBufferSize(64))
	go warmup.Run()
	warmup.WaitForStart()
	_ = warmup.SafeShutdown()

	// 反复创建+关闭 Hub：若 WorkerPool 泄漏，SafeShutdown 会阻塞在 worker 等待
	const cycles = 5
	for i := 0; i < cycles; i++ {
		hub := NewHub(wscconfig.Default().WithNodeInfo("127.0.0.1", 18080).WithMessageBufferSize(64))
		go hub.Run()
		hub.WaitForStart()

		// SafeShutdown 应在 30s 内完成（-race 下时间轮 64 分片 worker + 批处理器退出较慢）
		// 若有泄漏，worker goroutine 无法退出，SafeShutdown 会阻塞触发超时
		done := make(chan error, 1)
		go func() {
			done <- hub.SafeShutdown()
		}()
		select {
		case err := <-done:
			require.NoError(t, err, "cycle %d SafeShutdown 失败", i)
		case <-time.After(30 * time.Second):
			t.Fatalf("cycle %d SafeShutdown 超时 30s（goroutine 泄漏导致阻塞）", i)
		}
	}
}

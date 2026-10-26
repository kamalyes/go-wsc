/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 02:56:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\models\client_pending_bench_test.go
 * @Description: Hub 共享 pendingCounter 在发送热路径上的争用度量
 *
 * TrySend 每次发送都要递增两个计数：per-client 的 pendingWrites（各连接独立，
 * 无跨核争用）与 Hub 共享的 pendingCounter（全部连接共用一个 atomic.Int64，
 * 即一条 cache line 被所有核反复弹跳）。后者同时是 AdmissionGate.Backlog() 的
 * 数据源，属载荷性计数而非纯遥测。
 *
 * 本组基准用「绑定/不绑定共享计数」两种配置隔离其成本：pendingCounter 为
 * atomic.Pointer，未绑定时 TrySend 完全不触碰它，差值即共享计数的净开销。
 */
package models

import (
	"sync/atomic"
	"testing"
)

// makePendingBenchClients 构造 n 个各自带独立 SendChan 的客户端
// shared 非 nil 时全部绑定同一个共享计数（模拟 Hub 注入）
func makePendingBenchClients(n, bufSize int, shared *atomic.Int64) []*Client {
	clients := make([]*Client, n)
	for i := range clients {
		c := &Client{
			ID:       "bench-pending",
			UserID:   "bench-user",
			UserType: UserTypeCustomer,
			SendChan: make(chan []byte, bufSize),
		}
		if shared != nil {
			c.pendingCounter.Store(shared)
		}
		go func(ch chan []byte) {
			for range ch {
			}
		}(c.SendChan)
		clients[i] = c
	}
	return clients
}

// benchTrySendParallel 多核并行发送：每个 goroutine 轮转自己的客户端子集
// 模拟广播扇出——不同连接由不同核并发投递
func benchTrySendParallel(b *testing.B, shared *atomic.Int64) {
	const nClients = 256
	clients := makePendingBenchClients(nClients, 4096, shared)
	data := []byte(`{"type":"text","content":"benchmark message payload"}`)

	b.ReportAllocs()
	var seq atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		i := int(seq.Add(1)) * 31 // 错开起点，避免各核同时打同一客户端
		for pb.Next() {
			clients[i%nClients].TrySend(data)
			i++
		}
	})
}

// BenchmarkTrySendParallel_SharedCounter 绑定 Hub 共享计数（生产形态）
func BenchmarkTrySendParallel_SharedCounter(b *testing.B) {
	var shared atomic.Int64
	benchTrySendParallel(b, &shared)
}

// BenchmarkTrySendParallel_NoSharedCounter 不绑定共享计数（隔离对照）
// 与上一条的差值 = 共享 cache line 的净争用成本
func BenchmarkTrySendParallel_NoSharedCounter(b *testing.B) {
	benchTrySendParallel(b, nil)
}

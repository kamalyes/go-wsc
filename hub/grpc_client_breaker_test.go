/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 10:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 10:30:00
 * @FilePath: \go-wsc\hub\grpc_client_breaker_test.go
 * @Description: GRPCClientPool 熔断器集成测试
 *
 * 验证 per-node circuit breaker 的核心行为：
 *   1. 连续失败后自动熔断（open），后续请求快速短路
 *   2. ResetTimeout 后进入半开（half-open），放行试探请求
 *   3. 半开成功后恢复（closed），半开失败重新熔断
 *   4. Ping 不经过熔断器（健康检查始终放行）
 *   5. per-node 隔离：A 节点熔断不影响 B 节点
 *   6. 并发安全：多 goroutine 同时调用不产生数据竞争
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/breaker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unreachableAddr 不可达地址，用于模拟 gRPC 调用失败
// 127.0.0.1:1 是特权端口，几乎不会有服务监听，保证连接失败
const unreachableAddr = "127.0.0.1:1"

// newBreakerTestPool 创建带短超时熔断器配置的测试连接池
// MaxFailures=3, ResetTimeout=200ms, HalfOpenSuccesses=1 便于快速验证状态转换
func newBreakerTestPool() *GRPCClientPool {
	pool := NewGRPCClientPool()
	pool.SetBreakerConfig(breaker.Config{
		MaxFailures:       3,
		ResetTimeout:      200 * time.Millisecond,
		HalfOpenSuccesses: 1,
	})
	return pool
}

// ============================================================================
// 1. 熔断器开启测试
// ============================================================================

// TestGRPCBreaker_OpensAfterMaxFailures 连续失败达到 MaxFailures 后熔断器开启
func TestGRPCBreaker_OpensAfterMaxFailures(t *testing.T) {
	pool := newBreakerTestPool()
	defer pool.Close()
	ctx := context.Background()

	// 前 MaxFailures(3) 次调用失败但不熔断（circuit 仍 closed）
	for i := 0; i < 3; i++ {
		_, err := pool.SendToUser(ctx, unreachableAddr, "u1", []byte("msg"))
		assert.Error(t, err, "第 %d 次调用应失败", i+1)
	}

	// 第 3 次失败后熔断器应已开启
	assert.Equal(t, breaker.StateOpen, pool.getOrCreateBreaker(unreachableAddr).GetState(),
		"连续 3 次失败后熔断器应处于 open 状态")

	// 熔断后调用应快速短路（不实际发起 gRPC 连接）
	start := time.Now()
	_, err := pool.SendToUser(ctx, unreachableAddr, "u1", []byte("msg"))
	elapsed := time.Since(start)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, breaker.ErrOpen), "熔断器开启时应返回 ErrOpen")
	assert.Less(t, elapsed, 100*time.Millisecond, "熔断后应快速短路，不等待 gRPC 超时")
}

// ============================================================================
// 2. 半开恢复测试
// ============================================================================

// TestGRPCBreaker_HalfOpenThenClose 熔断→半开→成功→恢复
func TestGRPCBreaker_HalfOpenThenClose(t *testing.T) {
	pool := newBreakerTestPool()
	defer pool.Close()
	ctx := context.Background()

	// 触发熔断
	for i := 0; i < 3; i++ {
		_, _ = pool.SendToUser(ctx, unreachableAddr, "u1", []byte("msg"))
	}
	require.Equal(t, breaker.StateOpen, pool.getOrCreateBreaker(unreachableAddr).GetState(),
		"熔断器应已开启")

	// 等待 ResetTimeout（open→half-open 转换是惰性的，需 AllowRequest 触发）
	time.Sleep(250 * time.Millisecond)
	cb := pool.getOrCreateBreaker(unreachableAddr)
	// AllowRequest 触发 open→half-open 状态转换
	assert.True(t, cb.AllowRequest(), "ResetTimeout 后应放行试探请求")
	assert.Equal(t, breaker.StateHalfOpen, cb.GetState(),
		"AllowRequest 后应进入 half-open")

	// 手动记录成功（模拟节点恢复）
	cb.RecordSuccess()

	// HalfOpenSuccesses=1，一次成功后应恢复 closed
	assert.Equal(t, breaker.StateClosed, cb.GetState(),
		"半开状态成功后应恢复 closed")
}

// TestGRPCBreaker_HalfOpenFailureReopens 半开状态失败→重新熔断
func TestGRPCBreaker_HalfOpenFailureReopens(t *testing.T) {
	pool := newBreakerTestPool()
	defer pool.Close()
	ctx := context.Background()

	// 触发熔断
	for i := 0; i < 3; i++ {
		_, _ = pool.SendToUser(ctx, unreachableAddr, "u1", []byte("msg"))
	}
	require.Equal(t, breaker.StateOpen, pool.getOrCreateBreaker(unreachableAddr).GetState())

	// 等待 ResetTimeout 后触发 open→half-open 转换
	time.Sleep(250 * time.Millisecond)
	cb := pool.getOrCreateBreaker(unreachableAddr)
	assert.True(t, cb.AllowRequest(), "ResetTimeout 后应放行试探请求")
	require.Equal(t, breaker.StateHalfOpen, cb.GetState(), "应进入 half-open")

	// 半开状态下记录失败→重新熔断
	cb.RecordFailure()
	assert.Equal(t, breaker.StateOpen, cb.GetState(),
		"半开状态失败后应重新 open")
}

// ============================================================================
// 3. Ping 不经过熔断器
// ============================================================================

// TestGRPCBreaker_PingBypassesBreaker Ping 直接调用，不受熔断器影响
func TestGRPCBreaker_PingBypassesBreaker(t *testing.T) {
	pool := newBreakerTestPool()
	defer pool.Close()
	ctx := context.Background()

	// 触发熔断
	for i := 0; i < 3; i++ {
		_, _ = pool.SendToUser(ctx, unreachableAddr, "u1", []byte("msg"))
	}
	require.Equal(t, breaker.StateOpen, pool.getOrCreateBreaker(unreachableAddr).GetState(),
		"熔断器应已开启")

	// Ping 不应返回 ErrOpen（虽然连接失败，但不是被熔断器拦截）
	_, err := pool.Ping(ctx, unreachableAddr)
	assert.Error(t, err)
	assert.False(t, errors.Is(err, breaker.ErrOpen),
		"Ping 不应被熔断器拦截")
}

// ============================================================================
// 4. per-node 隔离测试
// ============================================================================

// TestGRPCBreaker_PerNodeIsolation A 节点熔断不影响 B 节点
func TestGRPCBreaker_PerNodeIsolation(t *testing.T) {
	pool := newBreakerTestPool()
	defer pool.Close()
	ctx := context.Background()

	addrA := "127.0.0.1:1" // 不可达
	addrB := "127.0.0.1:2" // 也不可达，但独立熔断

	// 让 A 节点熔断
	for i := 0; i < 3; i++ {
		_, _ = pool.SendToUser(ctx, addrA, "u1", []byte("msg"))
	}
	require.Equal(t, breaker.StateOpen, pool.getOrCreateBreaker(addrA).GetState(),
		"A 节点应已熔断")

	// B 节点的熔断器应仍处于 closed（独立计数）
	assert.Equal(t, breaker.StateClosed, pool.getOrCreateBreaker(addrB).GetState(),
		"B 节点熔断器应不受 A 节点影响，保持 closed")

	// B 节点调用失败但不被熔断（次数不够）
	_, err := pool.SendToUser(ctx, addrB, "u1", []byte("msg"))
	assert.Error(t, err)
	assert.False(t, errors.Is(err, breaker.ErrOpen),
		"B 节点不应返回 ErrOpen")
}

// ============================================================================
// 5. 并发安全测试
// ============================================================================

// TestGRPCBreaker_ConcurrentSafe 多 goroutine 并发调用，-race 下无数据竞争
func TestGRPCBreaker_ConcurrentSafe(t *testing.T) {
	pool := newBreakerTestPool()
	defer pool.Close()
	ctx := context.Background()

	const workers = 20
	const perWorker = 5
	var errCount int64
	wg := sync.WaitGroup{}
	wg.Add(workers)

	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// 交替使用不同地址，触发多个熔断器创建
				addr := unreachableAddr
				if i%2 == 0 {
					addr = "127.0.0.1:2"
				}
				_, err := pool.SendToUser(ctx, addr, "u1", []byte("msg"))
				if err != nil {
					atomic.AddInt64(&errCount, 1)
				}
			}
		}()
	}
	wg.Wait()

	// 所有调用都应失败（不可达地址），但不应 panic 或数据竞争
	assert.Greater(t, errCount, int64(0), "不可达地址应产生失败")

	// 验证熔断器确实被创建（至少 unreachableAddr 的）
	cb := pool.getOrCreateBreaker(unreachableAddr)
	assert.NotNil(t, cb)
	stats := pool.GetBreakerStats(unreachableAddr)
	assert.Equal(t, "grpc-node-127.0.0.1:1", stats.Name)
}

// ============================================================================
// 6. 熔断器统计和状态查询测试
// ============================================================================

// TestGRPCBreaker_StatsAndIsOpen 统计信息和状态查询接口
func TestGRPCBreaker_StatsAndIsOpen(t *testing.T) {
	pool := newBreakerTestPool()
	defer pool.Close()
	ctx := context.Background()

	// 初始状态：closed
	assert.False(t, pool.IsCircuitOpen(unreachableAddr), "初始应为 closed")

	// 触发熔断
	for i := 0; i < 3; i++ {
		_, _ = pool.SendToUser(ctx, unreachableAddr, "u1", []byte("msg"))
	}

	// 熔断后：open
	assert.True(t, pool.IsCircuitOpen(unreachableAddr), "连续失败后应 open")

	// 统计信息
	stats := pool.GetBreakerStats(unreachableAddr)
	assert.Equal(t, "grpc-node-127.0.0.1:1", stats.Name)
	assert.Equal(t, "open", stats.State)
	assert.GreaterOrEqual(t, stats.Failures, int32(3), "失败计数应 >= 3")
}

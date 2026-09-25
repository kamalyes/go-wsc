/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:18:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 19:18:00
 * @FilePath: \go-wsc\hub\node_query_flight_test.go
 * @Description: 用户节点查询 in-flight 合并器测试
 *
 * 覆盖语义：
 *   - 同 key 并发查询共享单次回源（热点用户扇入削峰核心契约）
 *   - 不同 key（跨 appID/namespace 信封）互不共享（信封隔离）
 *   - 无结果缓存：顺序两次同 key 查询各自回源（与逐次直查等价）
 *   - 回源 panic 后 inflight 清理，后续查询正常执行（不残留陈旧错误）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestNodeQueryFlightConcurrentMerge 验证同 key 并发查询仅回源一次且全部共享结果
func TestNodeQueryFlightConcurrentMerge(t *testing.T) {
	var f nodeQueryFlight
	var execCount int32

	const callers = 32
	var wg sync.WaitGroup
	start := make(chan struct{}) // 同时发起，确保落在同一飞行窗口
	results := make([][]string, callers)

	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			val, err := f.Do("app-1|ns-1|user-hot", func() ([]string, error) {
				atomic.AddInt32(&execCount, 1)
				time.Sleep(20 * time.Millisecond) // 模拟 Redis RTT，撑开合并窗口
				return []string{"node-a", "node-b"}, nil
			})
			if err != nil {
				t.Errorf("并发查询返回错误: %v", err)
			}
			results[idx] = val
		}(i)
	}
	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&execCount); got != 1 {
		t.Errorf("同 key %d 个并发查询应仅回源 1 次, 实际 %d 次", callers, got)
	}
	for i, val := range results {
		if len(val) != 2 || val[0] != "node-a" || val[1] != "node-b" {
			t.Errorf("等待者 %d 未共享回源结果: %v", i, val)
		}
	}
}

// TestNodeQueryFlightEnvelopeIsolation 验证不同信封 key 互不共享（appID/namespace 隔离）
func TestNodeQueryFlightEnvelopeIsolation(t *testing.T) {
	var f nodeQueryFlight
	var execCount int32

	// 同名 userID 跨 app/ns 并发查询：各信封独立回源，不共享结果
	keys := []string{"app-1|ns-1|u-shared", "app-2|ns-1|u-shared", "app-1|ns-2|u-shared", "|ns-1|u-shared"}
	var wg sync.WaitGroup
	for _, key := range keys {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_, _ = f.Do(k, func() ([]string, error) {
				atomic.AddInt32(&execCount, 1)
				time.Sleep(10 * time.Millisecond)
				return []string{k}, nil
			})
		}(key)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&execCount); got != int32(len(keys)) {
		t.Errorf("%d 个不同信封 key 应各自回源, 实际回源 %d 次", len(keys), got)
	}
}

// TestNodeQueryFlightNoCaching 验证无结果缓存：顺序同 key 两次查询各自回源
func TestNodeQueryFlightNoCaching(t *testing.T) {
	var f nodeQueryFlight
	var execCount int32

	load := func() ([]string, error) {
		atomic.AddInt32(&execCount, 1)
		return []string{"node-a"}, nil
	}
	if _, err := f.Do("app-1|ns-1|u-seq", load); err != nil {
		t.Fatalf("首次查询失败: %v", err)
	}
	if _, err := f.Do("app-1|ns-1|u-seq", load); err != nil {
		t.Fatalf("第二次查询失败: %v", err)
	}

	if got := atomic.LoadInt32(&execCount); got != 2 {
		t.Errorf("无 TTL 语义下顺序查询应各自回源（共 2 次）, 实际 %d 次", got)
	}
}

// TestNodeQueryFlightPanicCleanup 验证回源 panic 后 inflight 清理，后续查询正常
func TestNodeQueryFlightPanicCleanup(t *testing.T) {
	var f nodeQueryFlight

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("回源 panic 应向当前调用方继续传播")
			}
		}()
		_, _ = f.Do("app-1|ns-1|u-panic", func() ([]string, error) {
			panic("redis 连接池耗尽")
		})
	}()

	// panic 后该 key 不应残留 inflight：后续查询正常回源
	val, err := f.Do("app-1|ns-1|u-panic", func() ([]string, error) {
		return []string{"node-a"}, nil
	})
	if err != nil || len(val) != 1 {
		t.Errorf("panic 清理后后续查询异常: val=%v err=%v", val, err)
	}
}

// TestNodeQueryFlightWaiterSeesError 验证等待者以错误形式感知回源失败（如 Redis 抖动）
func TestNodeQueryFlightWaiterSeesError(t *testing.T) {
	var f nodeQueryFlight
	slowErr := errors.New("redis 超时")

	// ownerStarted 确保 owner 的飞行已注册（Do 注册先于 fn 执行），
	// 等待者必然并入同一次飞行而非自己回源
	ownerStarted := make(chan struct{})
	var wg sync.WaitGroup
	var waiterErr error
	var waiterVal []string

	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = f.Do("app-1|ns-1|u-err", func() ([]string, error) {
			close(ownerStarted)
			time.Sleep(15 * time.Millisecond)
			return nil, slowErr
		})
	}()
	go func() {
		defer wg.Done()
		<-ownerStarted
		waiterVal, waiterErr = f.Do("app-1|ns-1|u-err", func() ([]string, error) {
			return []string{"never"}, nil // 不应被执行（并入首个查询的飞行）
		})
	}()
	wg.Wait()

	if !errors.Is(waiterErr, slowErr) {
		t.Errorf("等待者应共享回源错误, 实际 err=%v", waiterErr)
	}
	if waiterVal != nil {
		t.Errorf("失败查询不应返回结果, 实际 %v", waiterVal)
	}
}

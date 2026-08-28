/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-01 00:00:00
 * @FilePath: \go-wsc\models\client_trysend_bench_test.go
 * @Description: TrySend 热路径微基准（广播扇出的最内层调用）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"sync"
	"testing"
)

// makeBenchTarget 创建基准用客户端（带持续消费的后台 drain，模拟写泵）
func makeBenchTarget(bufSize int) *Client {
	c := &Client{
		ID:       "bench-target",
		UserID:   "bench-user",
		UserType: UserTypeCustomer,
		SendChan: make(chan []byte, bufSize),
	}
	go func() {
		for range c.SendChan {
		}
	}()
	return c
}

// BenchmarkTrySendSerial 串行发送（单 goroutine 顺序扇出）
func BenchmarkTrySendSerial(b *testing.B) {
	c := makeBenchTarget(4096)
	defer close(c.SendChan)
	data := []byte(`{"type":"text","content":"benchmark message payload"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.TrySend(data)
	}
}

// BenchmarkTrySendConcurrent 并行发送（多 goroutine 同客户端争抢——广播并发扇出场景）
// 无锁化前：所有 goroutine 在 CloseMu 上串行化；无锁化后：仅 channel 自身原子操作
func BenchmarkTrySendConcurrent(b *testing.B) {
	for _, workers := range []int{2, 4, 8} {
		b.Run(itoaWorkers(workers), func(b *testing.B) {
			c := makeBenchTarget(4096)
			defer close(c.SendChan)
			data := []byte(`{"type":"text","content":"benchmark message payload"}`)
			b.ReportAllocs()
			b.ResetTimer()
			var wg sync.WaitGroup
			per := b.N / workers
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < per; i++ {
						c.TrySend(data)
					}
				}()
			}
			wg.Wait()
		})
	}
}

// BenchmarkTrySendClosed 已关闭客户端的快速路径（广播扇出时清理竞态窗口的常态分支）
func BenchmarkTrySendClosed(b *testing.B) {
	c := makeBenchTarget(4096)
	c.MarkClosed()
	data := []byte(`{}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.TrySend(data)
	}
}

func itoaWorkers(n int) string {
	switch n {
	case 2:
		return "workers-2"
	case 4:
		return "workers-4"
	default:
		return "workers-8"
	}
}

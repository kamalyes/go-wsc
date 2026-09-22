/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 10:20:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 10:20:00
 * @FilePath: \go-wsc\adapter\redis\online_store_bench_test.go
 * @Description: 在线状态存储的心跳写入路径基准测试
 *
 * 对比两条路径在 N 个客户端下的代价：
 *   1. BenchmarkBatchSetClientsOnline          — 批量重建：单次 Lua，1 次往返
 *   2. BenchmarkUpdateClientHeartbeatPerClient — 逐客户端读-改-写：N 次 GET + N 次写
 *
 * 1 vs 2 即量化「批量重建替掉逐客户端读-改-写」的收益（往返次数 N→1），
 * 这是心跳 worker flush 路径采用批量方案的核心依据。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package redisadapter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/redis/go-redis/v9"
)

// benchHeartbeatScales 基准测试规模档位
var benchHeartbeatScales = []int{100, 1000, 10000}

// newBenchOnlineStore 起一个 miniredis 并返回指向它的在线状态存储
func newBenchOnlineStore(tb testing.TB) (spi.OnlineStore, func()) {
	tb.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		tb.Fatalf("启动 miniredis 失败: %v", err)
	}
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := NewOnlineStore(client, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:hb:bench:online:",
		TTL:       60 * time.Second,
	})
	return store, func() {
		_ = client.Close()
		mr.Close()
	}
}

// makeOnlineStatusClients 批量创建带 NodeID 的测试客户端
// 不启动 drain goroutine：存储方法不触碰 SendChan，多余 goroutine 会干扰测量
func makeOnlineStatusClients(prefix string, n int) []*models.Client {
	clients := make([]*models.Client, n)
	for i := 0; i < n; i++ {
		clients[i] = &models.Client{
			ID:            fmt.Sprintf("%s-c-%d", prefix, i),
			UserID:        fmt.Sprintf("%s-u-%d", prefix, i),
			UserType:      models.UserTypeCustomer,
			Status:        models.UserStatusOnline,
			NodeID:        "bench-node",
			SendChan:      make(chan []byte, 1),
			Context:       context.Background(),
			LastHeartbeat: time.Now(),
		}
	}
	return clients
}

// BenchmarkBatchSetClientsOnline 批量重建：每次迭代处理 N 个客户端，仅 1 次 Redis 往返
func BenchmarkBatchSetClientsOnline(b *testing.B) {
	for _, n := range benchHeartbeatScales {
		b.Run(fmt.Sprintf("clients/%d", n), func(b *testing.B) {
			store, cleanup := newBenchOnlineStore(b)
			defer cleanup()

			clients := makeOnlineStatusClients("bso", n)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := store.BatchSetClientsOnline(ctx, clients); err != nil {
					b.Fatalf("BatchSetClientsOnline 失败: %v", err)
				}
			}
		})
	}
}

// BenchmarkUpdateClientHeartbeatPerClient 旧路径对照：N 次 GET + N 次写，逐客户端刷新
func BenchmarkUpdateClientHeartbeatPerClient(b *testing.B) {
	for _, n := range benchHeartbeatScales {
		b.Run(fmt.Sprintf("clients/%d", n), func(b *testing.B) {
			store, cleanup := newBenchOnlineStore(b)
			defer cleanup()

			clients := makeOnlineStatusClients("uch", n)
			ctx := context.Background()

			// 预写入 client:<id>，使 UpdateClientHeartbeat 走更新路径而非 no-op
			if err := store.BatchSetClientsOnline(ctx, clients); err != nil {
				b.Fatalf("预写入失败: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, c := range clients {
					if err := store.UpdateClientHeartbeat(ctx, c.ID); err != nil {
						b.Fatalf("UpdateClientHeartbeat 失败: %v", err)
					}
				}
			}
		})
	}
}

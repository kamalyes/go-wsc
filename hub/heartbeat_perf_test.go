/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-06 23:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-07 01:20:15
 * @FilePath: \go-wsc\hub\heartbeat_perf_test.go
 * @Description: 心跳 Redis 在线索引重建性能基准测试
 *
 * 验证修复后的性能特征：
 *   1. BenchmarkHeartbeatMessageHotPath   — 心跳热路径开销（channel 投递，Redis 由 worker 异步处理）
 *   2. BenchmarkBatchSetClientsOnline     — worker flush 路径：单次 Lua 批量重建 N 客户端索引
 *   3. BenchmarkUpdateClientHeartbeatPerClient — 旧路径对照：N 次 GET+Lua 逐客户端刷新
 *
 * 对比 2 vs 3 可量化「批量重建 vs 逐客户端读-改-写」的性能收益（Redis 往返次数 N→1）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/redis/go-redis/v9"
)

// benchHeartbeatScales 性能测试规模档位
var benchHeartbeatScales = []int{100, 1000, 10000}

// setupHeartbeatPerfHub 创建带 onlineStatusRepo 并已启动的性能测试 Hub
func setupHeartbeatPerfHub(b testing.TB, ttl time.Duration) (*Hub, *redis.Client, func()) {
	b.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		b.Fatalf("启动 miniredis 失败: %v", err)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(4096)
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0

	hub := NewHub(config)
	hub.SetOnlineStatusRepository(newRedisOnlineStatusRepo(redisClient, "wsc:hb:perf:online:", ttl))

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	cleanup := func() {
		_ = hub.SafeShutdown()
		_ = redisClient.Close()
		mr.Close()
	}
	return hub, redisClient, cleanup
}

// makeOnlineStatusClients 批量创建带 NodeID 的测试客户端（无 drain goroutine，
// 仓储方法不触碰 SendChan，避免无谓 goroutine 开销干扰基准测量）
func makeOnlineStatusClients(prefix string, n int) []*Client {
	clients := make([]*Client, n)
	for i := 0; i < n; i++ {
		clients[i] = &Client{
			ID:            fmt.Sprintf("%s-c-%d", prefix, i),
			UserID:        fmt.Sprintf("%s-u-%d", prefix, i),
			UserType:      UserTypeCustomer,
			Status:        UserStatusOnline,
			NodeID:        "perf-node",
			SendChan:      make(chan []byte, 1),
			Context:       context.Background(),
			LastHeartbeat: time.Now(),
		}
	}
	return clients
}

// ============================================================================
// 1. 心跳热路径（handleHeartbeatMessage：原子更新 + 非阻塞 channel 投递 + pong）
//    Redis 重建由 worker 异步处理，不计入热路径时间 → 验证修复不拖慢心跳主流程
// ============================================================================

func BenchmarkHeartbeatMessageHotPath(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("clients/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupHeartbeatPerfHub(b, 60*time.Second)
			defer cleanup()

			clients := makeBenchClients("hbhp", n, 256) // 带 drain goroutine，防 pong 阻塞
			registerAll(hub, clients)                   // 注册设置 NodeID + 异步 SetClientOnline

			c := clients[0] // 反复对同一客户端心跳（worker 按 clientID 去重）
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				hub.handleHeartbeatMessage(c)
			}
		})
	}
}

// ============================================================================
// 2. worker flush 路径：BatchSetClientsOnline（单次 Lua 批量重建 N 客户端索引）
//    每次 b.N 处理 N 个客户端，仅 1 次 Redis 往返（Lua 脚本）
// ============================================================================

func BenchmarkBatchSetClientsOnline(b *testing.B) {
	for _, n := range benchHeartbeatScales {
		b.Run(fmt.Sprintf("clients/%d", n), func(b *testing.B) {
			mr, err := miniredis.Run()
			if err != nil {
				b.Fatalf("启动 miniredis 失败: %v", err)
			}
			defer mr.Close()
			redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer redisClient.Close()

			repo := newRedisOnlineStatusRepo(redisClient, "wsc:hb:perf:online:", 60*time.Second)
			clients := makeOnlineStatusClients("bso", n)
			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if err := repo.BatchSetClientsOnline(ctx, clients); err != nil {
					b.Fatalf("BatchSetClientsOnline 失败: %v", err)
				}
			}
		})
	}
}

// ============================================================================
// 3. 旧路径对照：UpdateClientHeartbeat 逐客户端刷新（N 次 GET + Lua）
//    每次 b.N 处理 N 个客户端，N 次 Redis 往返（逐客户端读-改-写）
//    与 2 对比：ns/op 比值 ≈ 批量相对逐次的加速比
// ============================================================================

func BenchmarkUpdateClientHeartbeatPerClient(b *testing.B) {
	for _, n := range benchHeartbeatScales {
		b.Run(fmt.Sprintf("clients/%d", n), func(b *testing.B) {
			mr, err := miniredis.Run()
			if err != nil {
				b.Fatalf("启动 miniredis 失败: %v", err)
			}
			defer mr.Close()
			redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			defer redisClient.Close()

			repo := newRedisOnlineStatusRepo(redisClient, "wsc:hb:perf:online:", 60*time.Second)
			clients := makeOnlineStatusClients("uch", n)
			ctx := context.Background()

			// 预写入 client:<id>，使 UpdateClientHeartbeat 走更新路径而非 no-op
			if err := repo.BatchSetClientsOnline(ctx, clients); err != nil {
				b.Fatalf("预写入失败: %v", err)
			}

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				for _, c := range clients {
					if err := repo.UpdateClientHeartbeat(ctx, c.ID); err != nil {
						b.Fatalf("UpdateClientHeartbeat 失败: %v", err)
					}
				}
			}
		})
	}
}

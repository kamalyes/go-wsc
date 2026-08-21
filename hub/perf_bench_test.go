/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-29 00:00:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-29 00:15:15
 * @FilePath: \go-wsc\hub\perf_bench_test.go
 * @Description: Hub 核心能力性能基准测试（注册/注销/发送/广播/分组）
 *
 * 5 个核心 Benchmark，每个通过 sub-bench 覆盖串行+并行+不同规模：
 *   1. BenchmarkRegister       — 注册 + 注销
 *   2. BenchmarkSendToUser     — 点对点发送
 *   3. BenchmarkBroadcast      — 全量广播
 *   4. BenchmarkGroupBroadcast — 群组广播 + 可靠投递
 *   5. BenchmarkMixed          — 混合负载（注册+发送+广播+群组并发）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
)

// benchScale 基准测试的连接规模档位
var benchScale = []int{100, 1000, 10000}

// setupPerfHub 创建并启动用于性能测试的 Hub（带 miniredis 群组仓库）
func setupPerfHub(b testing.TB) (*Hub, repository.GroupRepository, func()) {
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
	groupRepo := repository.NewRedisGroupRepository(redisClient, "wsc:bench:group:")
	hub.SetGroupRepository(groupRepo)

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	cleanup := func() {
		hub.Shutdown()
		_ = redisClient.Close()
		mr.Close()
	}
	return hub, groupRepo, cleanup
}

// makeBenchClient 创建带后台消费 goroutine 的测试客户端
func makeBenchClient(clientID, userID string, bufSize int) *Client {
	c := &Client{
		ID:             clientID,
		UserID:         userID,
		UserType:       UserTypeCustomer,
		Role:           models.UserRoleCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
		SendChan:       make(chan []byte, bufSize),
		Context:        context.WithValue(context.Background(), ContextKeyUserID, userID),
		ConnectedAt:    time.Now(),
		LastSeen:       time.Now(),
	}
	go func() { // 后台 drain 防止 SendChan 满阻塞
		for range c.SendChan {
		}
	}()
	return c
}

// makeBenchClients 批量创建测试客户端
func makeBenchClients(prefix string, n, bufSize int) []*Client {
	clients := make([]*Client, n)
	for i := 0; i < n; i++ {
		clients[i] = makeBenchClient(
			fmt.Sprintf("%s-c-%d", prefix, i),
			fmt.Sprintf("%s-u-%d", prefix, i),
			bufSize,
		)
	}
	return clients
}

// registerAll 批量注册并等待完成
func registerAll(hub *Hub, clients []*Client) {
	for _, c := range clients {
		hub.Register(c)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if hub.GetClientsCount() >= int64(len(clients)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// prepareGroup 创建群组并添加成员
func prepareGroup(b testing.TB, hub *Hub, groupRepo repository.GroupRepository, gid string, memberIDs []string) (string, string) {
	b.Helper()
	const ns = "bench-ns"
	if err := groupRepo.CreateGroup(context.Background(), &Group{
		GroupID: gid, Namespace: ns, Name: "bench", OwnerID: "owner", MaxMembers: 0,
	}); err != nil {
		b.Fatalf("创建群组失败: %v", err)
	}
	if len(memberIDs) > 0 {
		if err := hub.AddGroupMembers(routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace(ns).WithGroupIDs([]string{gid}).Inject(context.Background()), memberIDs); err != nil {
			b.Fatalf("添加成员失败: %v", err)
		}
	}
	return ns, gid
}

// makeBenchMsg 创建测试消息
func makeBenchMsg(sender, receiver string) *HubMessage {
	msg := NewHubMessage()
	msg.Sender = sender
	msg.Receiver = receiver
	msg.MessageType = MessageTypeText
	msg.Content = "benchmark"
	msg.CreateAt = time.Now()
	return msg
}

// memberIDsFrom 从客户端列表提取 userID 列表
func memberIDsFrom(clients []*Client) []string {
	ids := make([]string, len(clients))
	for i, c := range clients {
		ids[i] = c.UserID
	}
	return ids
}

// ============================================================================
// 1. 注册 + 注销
// ============================================================================

func BenchmarkRegisterUnregister(b *testing.B) {
	for _, n := range benchScale {
		// 串行注册
		b.Run(fmt.Sprintf("register/serial/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("reg", n, 64)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				hub.Register(clients[i%n])
			}
		})

		// 并行注册
		b.Run(fmt.Sprintf("register/parallel/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("regp", n, 64)
			b.ResetTimer()
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for i := 0; pb.Next(); i++ {
					hub.Register(clients[i%n])
				}
			})
		})

		// 串行注销（先全量注册再测注销）
		b.Run(fmt.Sprintf("unregister/serial/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("unreg", n, 64)
			registerAll(hub, clients)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				hub.Unregister(clients[i%n])
			}
		})
	}
}

// ============================================================================
// 2. 点对点发送
// ============================================================================

func BenchmarkSendToUser(b *testing.B) {
	for _, n := range benchScale {
		// 串行
		b.Run(fmt.Sprintf("serial/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("snd", n, 512)
			registerAll(hub, clients)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				recv := clients[i%n].UserID
				hub.SendToUserWithRetry(context.Background(), recv, makeBenchMsg("bench", recv))
			}
		})

		// 并行
		b.Run(fmt.Sprintf("parallel/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("sndp", n, 512)
			registerAll(hub, clients)
			b.ResetTimer()
			b.ReportAllocs()
			var idx int64
			b.RunParallel(func(pb *testing.PB) {
				for {
					if !pb.Next() {
						return
					}
					i := atomic.AddInt64(&idx, 1) - 1
					recv := clients[i%int64(n)].UserID
					hub.SendToUserWithRetry(context.Background(), recv, makeBenchMsg("bench", recv))
				}
			})
		})
	}
}

// ============================================================================
// 3. 全量广播
// ============================================================================

func BenchmarkBroadcast(b *testing.B) {
	for _, n := range benchScale {
		// 串行
		b.Run(fmt.Sprintf("serial/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("bc", n, 256)
			registerAll(hub, clients)
			ctx := context.Background()
			msg := makeBenchMsg("bench", "")
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = hub.Deliver(ctx, msg, false)
			}
		})

		// 并行
		b.Run(fmt.Sprintf("parallel/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("bcp", n, 256)
			registerAll(hub, clients)
			ctx := context.Background()
			msg := makeBenchMsg("bench", "")
			b.ResetTimer()
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					_ = hub.Deliver(ctx, msg, false)
				}
			})
		})
	}
}

// ============================================================================
// 4. 群组能力（广播 + 可靠投递）
// ============================================================================

func BenchmarkGroupBroadcast(b *testing.B) {
	for _, n := range benchScale {
		// 群组广播（仅在线投递，高性能）
		b.Run(fmt.Sprintf("broadcast/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("grp", n, 256)
			registerAll(hub, clients)
			ns, gid := prepareGroup(b, hub, groupRepo, "g1", memberIDsFrom(clients))
			ctx := context.Background()
			msg := makeBenchMsg("bench", "")
			groupCtx := routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace(ns).WithGroupIDs([]string{gid}).Inject(ctx)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = hub.Deliver(groupCtx, msg, false)
			}
		})

		// 群组可靠投递（在线+离线存储+重试）
		b.Run(fmt.Sprintf("send/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("sgrp", n, 256)
			registerAll(hub, clients)
			ns, gid := prepareGroup(b, hub, groupRepo, "g2", memberIDsFrom(clients))
			ctx := context.Background()
			msg := makeBenchMsg("bench", "")
			msg.RequireAck = true
			groupCtx := routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace(ns).WithGroupIDs([]string{gid}).Inject(ctx)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = hub.Deliver(groupCtx, msg, false)
			}
		})
	}
}

// ============================================================================
// 5. 混合负载（注册+发送+广播+群组并发）
// ============================================================================

func BenchmarkMixed(b *testing.B) {
	hub, groupRepo, cleanup := setupPerfHub(b)
	defer cleanup()

	// 预注册 500 基础客户端
	base := makeBenchClients("mix", 500, 256)
	registerAll(hub, base)
	ns, gid := prepareGroup(b, hub, groupRepo, "mix-g", memberIDsFrom(base))
	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		done := make(chan struct{}, 4)

		// 注册
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			hub.Register(makeBenchClient(
				fmt.Sprintf("mix-r-%d", idx),
				fmt.Sprintf("mix-ru-%d", idx),
				128,
			))
		}(i)

		// 点对点发送
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			recv := base[idx%500].UserID
			hub.SendToUserWithRetry(ctx, recv, makeBenchMsg("mix", recv))
		}(i)

		// 全量广播
		go func() {
			defer func() { done <- struct{}{} }()
			_ = hub.Deliver(ctx, makeBenchMsg("mix", ""), false)
		}()

		// 群组广播
		go func() {
			defer func() { done <- struct{}{} }()
			groupCtx := routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace(ns).WithGroupIDs([]string{gid}).Inject(ctx)
			_ = hub.Deliver(groupCtx, makeBenchMsg("mix", ""), false)
		}()

		<-done
		<-done
		<-done
		<-done
	}
}

// ============================================================================
// 6. 连接自动加群性能（joinMemberGroupOnConnect 开销 + 默认组消息投递）
//
// 覆盖：
//   - 自动加群方法直接开销（默认组/业务组 × 新成员/重连幂等）
//   - Register 端到端开销（有 GroupID vs 无 GroupID）
//   - SendToGroup → 默认组大规模成员投递
//   - BroadcastToAllGroups 含默认组去重开销
// ============================================================================

// benchJoinScale 自动加群基准测试规模档位（成员数）
var benchJoinScale = []int{100, 1000, 10000}

// BenchmarkJoinMemberGroupOnConnect 直接测量 joinMemberGroupOnConnect 开销
func BenchmarkJoinMemberGroupOnConnect(b *testing.B) {
	for _, n := range benchJoinScale {
		// 默认组 - 新成员（每次不同 userID，走 EnsureSystemGroup + AddMembers）
		b.Run(fmt.Sprintf("default_group/new_member/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			ctx := context.Background()
			// 预填充 n 个成员到默认组
			preIDs := make([]string, n)
			for i := 0; i < n; i++ {
				preIDs[i] = fmt.Sprintf("pre-def-%d", i)
			}
			groupRepo.EnsureSystemGroup(ctx, models.DefaultAppID, "bench-ns", models.DefaultGroupID)
			groupRepo.AddMembers(ctx, models.DefaultAppID, "bench-ns", models.DefaultGroupID, preIDs)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				client := &Client{
					ID:        fmt.Sprintf("c-def-%d", i),
					UserID:    fmt.Sprintf("u-def-%d", i),
					UserType:  UserTypeCustomer,
					Namespace: "bench-ns",
				}
				hub.joinMemberGroupOnConnect(ctx, client)
			}
		})

		// 默认组 - 重连幂等（同一 userID 反复加入，SADD 集合语义去重）
		b.Run(fmt.Sprintf("default_group/reconnect/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			ctx := context.Background()
			preIDs := make([]string, n)
			for i := 0; i < n; i++ {
				preIDs[i] = fmt.Sprintf("pre-rc-%d", i)
			}
			groupRepo.EnsureSystemGroup(ctx, models.DefaultAppID, "bench-ns", models.DefaultGroupID)
			groupRepo.AddMembers(ctx, models.DefaultAppID, "bench-ns", models.DefaultGroupID, preIDs)

			client := &Client{
				ID:        "c-reconnect",
				UserID:    "u-reconnect",
				UserType:  UserTypeCustomer,
				Namespace: "bench-ns",
			}
			// 首次加入
			hub.joinMemberGroupOnConnect(ctx, client)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				hub.joinMemberGroupOnConnect(ctx, client)
			}
		})

		// 业务组 - 新成员（每次不同 userID，走 AddGroupMembers 自动创建判断 + AddMembers）
		b.Run(fmt.Sprintf("business_group/new_member/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			ctx := context.Background()
			preIDs := make([]string, n)
			for i := 0; i < n; i++ {
				preIDs[i] = fmt.Sprintf("pre-bg-%d", i)
			}
			groupRepo.CreateGroup(ctx, &Group{GroupID: "bench-bg", Namespace: "bench-ns", OwnerID: "o"})
			groupRepo.AddMembers(ctx, models.DefaultAppID, "bench-ns", "bench-bg", preIDs)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				client := &Client{
					ID:        fmt.Sprintf("c-bg-%d", i),
					UserID:    fmt.Sprintf("u-bg-%d", i),
					UserType:  UserTypeCustomer,
					Namespace: "bench-ns",
					GroupID:   "bench-bg",
				}
				hub.joinMemberGroupOnConnect(ctx, client)
			}
		})

		// 业务组 - 重连幂等
		b.Run(fmt.Sprintf("business_group/reconnect/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			ctx := context.Background()
			groupRepo.CreateGroup(ctx, &Group{GroupID: "bench-bg-rc", Namespace: "bench-ns", OwnerID: "o"})
			groupRepo.AddMembers(ctx, models.DefaultAppID, "bench-ns", "bench-bg-rc", []string{"u-bg-rc"})

			client := &Client{
				ID:        "c-bg-rc",
				UserID:    "u-bg-rc",
				UserType:  UserTypeCustomer,
				Namespace: "bench-ns",
				GroupID:   "bench-bg-rc",
			}
			hub.joinMemberGroupOnConnect(ctx, client)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				hub.joinMemberGroupOnConnect(ctx, client)
			}
		})
	}
}

// BenchmarkRegisterWithAutoJoin 测量 Register 端到端开销（含异步自动加群）
func BenchmarkRegisterWithAutoJoin(b *testing.B) {
	for _, n := range benchScale {
		// 无 GroupID（自动加入默认组）
		b.Run(fmt.Sprintf("default_group/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("rad", n, 64)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				hub.Register(clients[i%n])
			}
		})

		// 有 GroupID（自动加入业务组）
		b.Run(fmt.Sprintf("business_group/%d", n), func(b *testing.B) {
			hub, _, cleanup := setupPerfHub(b)
			defer cleanup()
			clients := makeBenchClients("rab", n, 64)
			for _, c := range clients {
				c.GroupID = "bench-reg-bg"
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				hub.Register(clients[i%n])
			}
		})
	}
}

// BenchmarkSendToGroupDefaultGroup 测量向默认组投递消息的开销（全员在线）
func BenchmarkSendToGroupDefaultGroup(b *testing.B) {
	for _, n := range benchJoinScale {
		b.Run(fmt.Sprintf("serial/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			ctx := context.Background()
			clients := makeBenchClients("sdg", n, 512)
			registerAll(hub, clients) // 注册时自动加入默认组（__default_ns__）

			// 确保默认组存在并有成员
			ns := models.DefaultNamespace
			groupRepo.EnsureSystemGroup(ctx, models.DefaultAppID, ns, models.DefaultGroupID)
			groupRepo.AddMembers(ctx, models.DefaultAppID, ns, models.DefaultGroupID, memberIDsFrom(clients))

			msg := makeBenchMsg("bench", "")
			msg.RequireAck = true
			groupCtx := routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace(ns).WithGroupIDs([]string{models.DefaultGroupID}).Inject(ctx)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = hub.Deliver(groupCtx, msg, false)
			}
		})
	}
}

// BenchmarkBroadcastToAllGroupsWithDefaultGroup 测量含默认组的全群组广播去重开销
func BenchmarkBroadcastToAllGroupsWithDefaultGroup(b *testing.B) {
	for _, n := range benchJoinScale {
		b.Run(fmt.Sprintf("serial/%d", n), func(b *testing.B) {
			hub, groupRepo, cleanup := setupPerfHub(b)
			defer cleanup()
			ctx := context.Background()
			clients := makeBenchClients("bad", n, 512)
			registerAll(hub, clients)

			ns := "bench-ns"
			// 每个客户端既在业务组也在默认组（模拟自动加群后的真实分布）
			_, gid := prepareGroup(b, hub, groupRepo, "bench-dup-g", memberIDsFrom(clients))
			groupRepo.EnsureSystemGroup(ctx, models.DefaultAppID, ns, models.DefaultGroupID)
			groupRepo.AddMembers(ctx, models.DefaultAppID, ns, models.DefaultGroupID, memberIDsFrom(clients))

			msg := makeBenchMsg("bench", "")
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				gids, err := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace(ns).WithGroupIDs(nil).Inject(ctx))
				if err == nil && len(gids) > 0 {
					groupCtx := routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace(ns).WithGroupIDs(gids).Inject(ctx)
					_ = hub.Deliver(groupCtx, msg, false)
				}
			}
			_ = gid
		})
	}
}

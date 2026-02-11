/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-03 23:05:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-03 23:05:22
 * @FilePath: \go-wsc\hub_observer_bench_test.go
 * @Description: Hub 观察者性能测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	benchObserverResult bool
	benchObserverCount  int
)

// setupBenchHub 创建并启动测试用的Hub
func setupBenchHub() *Hub {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0 // 禁用连接数限制,允许无限多端登录
	config.MessageBufferSize = 1000  // 增大缓冲区以支持基准测试的大量并发注册
	hub := NewHub(config)
	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	return hub
}

// setupRealObservers 创建真实的观察者连接（带消息接收 goroutine）
func setupRealObservers(hub *Hub, count int) ([]*Client, *atomic.Int64) {
	observers := make([]*Client, count)
	receivedCount := &atomic.Int64{}

	for i := 0; i < count; i++ {
		client := createTestClientWithIDGen(UserTypeObserver)
		observers[i] = client
		hub.Register(client)

		// 启动消息接收 goroutine（模拟真实场景）
		go func(c *Client) {
			for range c.SendChan {
				receivedCount.Add(1)
			}
		}(client)
	}

	time.Sleep(50 * time.Millisecond)
	return observers, receivedCount
}

// setupRealUsers 创建真实的用户连接
func setupRealUsers(hub *Hub, count int) []*Client {
	users := make([]*Client, count)
	for i := 0; i < count; i++ {
		client := createTestClientWithIDGen(UserTypeCustomer)
		users[i] = client
		hub.Register(client)
	}
	time.Sleep(50 * time.Millisecond)
	return users
}

// BenchmarkIsObserver 测试 IsObserver 性能（应该是 O(1)）
func BenchmarkIsObserver(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	clients, _ := setupRealObservers(hub, b.N)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		benchObserverResult = hub.IsObserver(clients[i%len(clients)].UserID)
	}
}

// BenchmarkGetObserverCount 测试 GetObserverCount 性能（应该是 O(1)）
func BenchmarkGetObserverCount(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	setupRealObservers(hub, 100)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		benchObserverCount = hub.GetObserverCount()
	}

	b.StopTimer()
	// 验证观察者数量
	assert.Equal(b, 100, benchObserverCount, "观察者数量应该是 100")
}

// BenchmarkAddObserver 测试添加观察者性能
func BenchmarkAddObserver(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	userId := hub.GetIDGenerator().GenerateRequestID()
	clients := createTestClientWithUserID(userId, UserTypeObserver, b.N)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hub.Register(clients[i])
	}

	b.StopTimer()
	// 等待异步注册完成（轮询检查直到所有设备都注册完成）
	maxWait := 2 * time.Second
	checkInterval := 10 * time.Millisecond
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		if hub.GetObserverDeviceCount() == b.N {
			break
		}
		time.Sleep(checkInterval)
	}

	// 验证所有观察者都已注册
	assert.Equal(b, 1, hub.GetObserverCount(), "应该有 1 个观察者用户")
	assert.Equal(b, b.N, hub.GetObserverDeviceCount(), "应该有 N 个观察者设备")
}

// BenchmarkRemoveObserver 测试移除观察者性能
func BenchmarkRemoveObserver(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	userId := hub.GetIDGenerator().GenerateRequestID()
	clients := createTestClientWithUserID(userId, UserTypeObserver, b.N)
	registerClients(hub, clients)
	time.Sleep(100 * time.Millisecond)

	initialCount := hub.GetObserverDeviceCount()
	require.Equal(b, b.N, initialCount, "初始设备数应该等于 N")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hub.Unregister(clients[i])
	}

	b.StopTimer()
	// 验证所有观察者都已移除
	time.Sleep(50 * time.Millisecond)
	assert.Equal(b, 0, hub.GetObserverCount(), "所有观察者应该已移除")
	assert.Equal(b, 0, hub.GetObserverDeviceCount(), "所有观察者设备应该已移除")
}

// BenchmarkNotifyObservers 测试通知所有观察者的性能（真实场景）
func BenchmarkNotifyObservers(b *testing.B) {
	tests := []struct {
		name          string
		observerCount int
	}{
		{"10个观察者", 10},
		{"50个观察者", 50},
		{"100个观察者", 100},
		{"500个观察者", 500},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			hub := setupBenchHub()
			defer hub.Shutdown()

			// 创建真实的观察者连接（带消息接收）
			observers, receivedCount := setupRealObservers(hub, tt.observerCount)
			defer func() {
				// 等待消息处理完成后再注销
				time.Sleep(500 * time.Millisecond)
				for _, obs := range observers {
					hub.Unregister(obs)
				}
			}()

			// 创建真实的用户连接
			users := setupRealUsers(hub, 2)
			defer func() {
				for _, user := range users {
					hub.Unregister(user)
				}
			}()

			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				msg := createTestHubMessage(MessageTypeCommand)
				msg.SetSender(users[0].UserID).
					SetReceiver(users[1].UserID).
					SetContent("Test message")

				hub.SendToUserWithRetry(ctx, users[1].UserID, msg)
			}

			b.StopTimer()
			// 等待消息处理完成
			time.Sleep(200 * time.Millisecond)

			received := receivedCount.Load()
			b.ReportMetric(float64(received), "msgs_received")

			// 验证观察者收到了消息
			assert.Greater(b, received, int64(0), "观察者应该收到消息")
		})
	}
}

// BenchmarkObserverMultiDevice 测试多端登录性能（真实场景）
func BenchmarkObserverMultiDevice(b *testing.B) {
	tests := []struct {
		name        string
		deviceCount int
	}{
		{"单设备", 1},
		{"3设备", 3},
		{"5设备", 5},
		{"10设备", 10},
	}

	for _, tt := range tests {
		b.Run(tt.name, func(b *testing.B) {
			hub := setupBenchHub()
			defer hub.Shutdown()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// 同一用户多个设备
				observers := make([]*Client, tt.deviceCount)
				receivedCount := &atomic.Int64{}

				for j := 0; j < tt.deviceCount; j++ {
					client := createTestClientWithIDGen(UserTypeObserver)
					observers[j] = client

					// 启动消息接收 goroutine
					go func(c *Client) {
						for range c.SendChan {
							receivedCount.Add(1)
						}
					}(client)
				}
				b.StartTimer()

				registerClients(hub, observers)

				b.StopTimer()
				// 清理
				for _, obs := range observers {
					hub.Unregister(obs)
				}
				b.StartTimer()
			}
		})
	}
}

// BenchmarkGetObserverStats 测试获取观察者统计信息的性能
func BenchmarkGetObserverStats(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	setupRealObservers(hub, 100)

	b.ResetTimer()
	b.ReportAllocs()

	var stats []*ObserverStats
	for i := 0; i < b.N; i++ {
		stats = hub.GetObserverStats()
	}

	b.StopTimer()
	// 验证统计信息
	assert.NotEmpty(b, stats, "应该有观察者统计信息")
	assert.Equal(b, 100, len(stats), "应该有 100 个观察者设备的统计")
}

// BenchmarkGetObserverManagerStats 测试获取观察者管理器统计的性能
func BenchmarkGetObserverManagerStats(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	setupRealObservers(hub, 100)

	b.ResetTimer()
	b.ReportAllocs()

	var managerStats *ObserverManagerStats
	for i := 0; i < b.N; i++ {
		managerStats = hub.GetObserverManagerStats()
	}

	b.StopTimer()
	// 验证管理器统计信息
	require.NotNil(b, managerStats, "管理器统计不应为空")
	assert.Equal(b, 100, managerStats.TotalObservers, "应该有 100 个观察者")
	assert.Equal(b, 100, managerStats.TotalDevices, "应该有 100 个设备")
}

// BenchmarkObserverConcurrentAccess 测试观察者并发访问性能
func BenchmarkObserverConcurrentAccess(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	clients, _ := setupRealObservers(hub, 100) // 使用固定数量的观察者
	require.NotEmpty(b, clients, "应该创建观察者")

	b.ResetTimer()
	b.ReportAllocs()

	var counter atomic.Int64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			idx := int(counter.Add(1) % int64(len(clients)))
			benchObserverResult = hub.IsObserver(clients[idx].UserID)
			benchObserverCount = hub.GetObserverCount()
		}
	})

	b.StopTimer()
	// 验证并发访问的结果
	assert.True(b, benchObserverResult, "IsObserver 应该返回 true")
	assert.Equal(b, 100, benchObserverCount, "观察者数量应该是 100")
}

// BenchmarkObserverMessageThroughput 测试观察者消息吞吐量（真实场景）
func BenchmarkObserverMessageThroughput(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	// 创建真实的观察者连接
	observers, receivedCount := setupRealObservers(hub, 10)
	defer func() {
		for _, obs := range observers {
			hub.Unregister(obs)
		}
	}()

	// 创建真实的用户连接（固定2个用户）
	users := setupRealUsers(hub, 2)
	defer func() {
		for _, user := range users {
			hub.Unregister(user)
		}
	}()

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		msg := createTestHubMessage(MessageTypeCommand)
		msg.SetSender(users[0].UserID).
			SetReceiver(users[1].UserID)

		hub.SendToUserWithRetry(ctx, users[1].UserID, msg)
	}

	b.StopTimer()
	time.Sleep(100 * time.Millisecond)
	b.ReportMetric(float64(receivedCount.Load()), "msgs_received")
}

// BenchmarkObserverRealWorldScenario 测试真实场景：观察者上线、发消息、下线
func BenchmarkObserverRealWorldScenario(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()

		// 1. 观察者上线
		observer := createTestClientWithIDGen(UserTypeObserver)
		receivedCount := &atomic.Int64{}

		// 启动消息接收 goroutine
		go func() {
			for range observer.SendChan {
				receivedCount.Add(1)
			}
		}()

		hub.Register(observer)

		// 2. 普通用户上线
		sender := createTestClientWithIDGen(UserTypeCustomer)
		receiver := createTestClientWithIDGen(UserTypeCustomer)

		hub.Register(sender)
		hub.Register(receiver)

		b.StartTimer()

		// 3. 发送消息（观察者会收到通知）
		for j := range 10 {
			msg := createTestHubMessage(MessageTypeCommand)
			msg.SetSender(sender.UserID).
				SetReceiver(receiver.UserID).
				SetContent(fmt.Sprintf("Message %d", j))

			hub.SendToUserWithRetry(ctx, receiver.UserID, msg)
		}

		b.StopTimer()

		// 等待消息处理完成
		time.Sleep(50 * time.Millisecond)

		// 4. 用户下线
		hub.Unregister(sender)
		hub.Unregister(receiver)
		hub.Unregister(observer)

		b.StartTimer()
	}
}

// BenchmarkObserverConcurrentConnections 测试并发连接场景
func BenchmarkObserverConcurrentConnections(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	ctx := context.Background()
	var wg sync.WaitGroup

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()

		// 并发创建 10 个观察者
		observers := make([]*Client, 10)
		for j := range 10 {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				client := createTestClientWithIDGen(UserTypeObserver)
				observers[idx] = client

				// 启动消息接收
				go func(c *Client) {
					for range c.SendChan {
					}
				}(client)

				hub.Register(client)
			}(j)
		}
		wg.Wait()

		// 创建用户并发送消息
		sender := createTestClientWithIDGen(UserTypeCustomer)
		receiver := createTestClientWithIDGen(UserTypeCustomer)

		hub.Register(sender)
		hub.Register(receiver)

		b.StartTimer()

		// 并发发送消息
		for j := range 10 {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				msg := createTestHubMessage(MessageTypeCommand)
				msg.SetSender(sender.UserID).
					SetReceiver(receiver.UserID).
					SetContent(fmt.Sprintf("Message %d", idx))

				hub.SendToUserWithRetry(ctx, receiver.UserID, msg)
			}(j)
		}
		wg.Wait()

		b.StopTimer()

		// 等待消息处理完成
		time.Sleep(50 * time.Millisecond)

		// 清理
		hub.Unregister(sender)
		hub.Unregister(receiver)
		for _, obs := range observers {
			if obs != nil {
				hub.Unregister(obs)
			}
		}

		b.StartTimer()
	}
}

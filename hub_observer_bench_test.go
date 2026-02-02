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
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

var (
	benchObserverResult bool
	benchObserverCount  int
)

// setupBenchHub 创建并启动测试用的Hub
func setupBenchHub() *Hub {
	config := wscconfig.Default()
	config.AllowMultiLogin = true
	hub := NewHub(config)
	go hub.Run()
	time.Sleep(100 * time.Millisecond)
	return hub
}

// BenchmarkIsObserver 测试 IsObserver 性能（应该是 O(1)）
func BenchmarkIsObserver(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	observers := createObservers(100, "observer-user")
	registerClients(hub, observers)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		benchObserverResult = hub.IsObserver("observer-user-50")
	}
}

// BenchmarkGetObserverCount 测试 GetObserverCount 性能（应该是 O(1)）
func BenchmarkGetObserverCount(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	observers := createObservers(100, "observer-user")
	registerClients(hub, observers)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		benchObserverCount = hub.GetObserverCount()
	}
}

// BenchmarkAddObserver 测试添加观察者性能
func BenchmarkAddObserver(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	observers := createObservers(b.N, "observer-user")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hub.Register(observers[i])
	}
}

// BenchmarkRemoveObserver 测试移除观察者性能
func BenchmarkRemoveObserver(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	observers := createObservers(b.N, "observer-user")
	registerClients(hub, observers)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hub.Unregister(observers[i])
	}
}

// BenchmarkNotifyObservers 测试通知所有观察者的性能
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

			observers := createObservers(tt.observerCount, "observer-user")
			registerClients(hub, observers)

			users := []*Client{
				createTestClient("user-1", "user-1", UserTypeCustomer),
				createTestClient("user-2", "user-2", UserTypeCustomer),
			}
			registerClients(hub, users)
			time.Sleep(100 * time.Millisecond)

			msg := NewHubMessage().
				SetID("test-msg").
				SetSender("user-1").
				SetReceiver("user-2").
				SetContent("Test message")

			ctx := context.Background()

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				hub.SendToUserWithRetry(ctx, "user-2", msg)
			}
		})
	}
}

// BenchmarkObserverMultiDevice 测试多端登录性能
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
				for j := 0; j < tt.deviceCount; j++ {
					observers[j] = createObserverClient(
						fmt.Sprintf("observer-%d-dev-%d", i, j),
						fmt.Sprintf("observer-user-%d", i),
					)
				}
				b.StartTimer()

				registerClients(hub, observers)
			}
		})
	}
}

// BenchmarkGetObserverStats 测试获取观察者统计信息的性能
func BenchmarkGetObserverStats(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	// 准备100个观察者，每个2个设备
	for i := 0; i < 100; i++ {
		devices := []*Client{
			createObserverClient(fmt.Sprintf("observer-%d-dev-0", i), fmt.Sprintf("observer-user-%d", i)),
			createObserverClient(fmt.Sprintf("observer-%d-dev-1", i), fmt.Sprintf("observer-user-%d", i)),
		}
		registerClients(hub, devices)
	}
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = hub.GetObserverStats()
	}
}

// BenchmarkGetObserverManagerStats 测试获取观察者管理器统计的性能
func BenchmarkGetObserverManagerStats(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	// 准备100个观察者，每个2个设备
	for i := 0; i < 100; i++ {
		devices := []*Client{
			createObserverClient(fmt.Sprintf("observer-%d-dev-0", i), fmt.Sprintf("observer-user-%d", i)),
			createObserverClient(fmt.Sprintf("observer-%d-dev-1", i), fmt.Sprintf("observer-user-%d", i)),
		}
		registerClients(hub, devices)
	}
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = hub.GetObserverManagerStats()
	}
}

// BenchmarkObserverConcurrentAccess 测试观察者并发访问性能
func BenchmarkObserverConcurrentAccess(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	observers := createObservers(50, "observer-user")
	registerClients(hub, observers)
	time.Sleep(100 * time.Millisecond)

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			benchObserverResult = hub.IsObserver("observer-user-25")
			benchObserverCount = hub.GetObserverCount()
		}
	})
}

// BenchmarkObserverMessageThroughput 测试观察者消息吞吐量
func BenchmarkObserverMessageThroughput(b *testing.B) {
	hub := setupBenchHub()
	defer hub.Shutdown()

	observers := createObservers(10, "observer-user")
	registerClients(hub, observers)

	users := []*Client{
		createTestClient("client-001", "user-1", UserTypeCustomer),
		createTestClient("client-002", "user-2", UserTypeCustomer),
	}
	registerClients(hub, users)
	time.Sleep(100 * time.Millisecond)

	msg := NewHubMessage().
		SetID("test-msg").
		SetSender("user-1").
		SetReceiver("user-2").
		SetContent("Test message")

	ctx := context.Background()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		hub.SendToUserWithRetry(ctx, "user-2", msg)
	}
}

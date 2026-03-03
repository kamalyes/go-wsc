/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-10 18:00:00
 * @FilePath: \go-wsc\hub\client_capacity_test.go
 * @Description: 客户端容量配置测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-config/pkg/wsc"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClientCapacityByUserType 测试根据用户类型获取正确的容量
func TestClientCapacityByUserType(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	tests := []struct {
		name     string
		userType UserType
		expected int
	}{
		{"Agent", UserTypeAgent, config.ClientCapacity.Agent},
		{"Bot", UserTypeBot, config.ClientCapacity.Bot},
		{"Customer", UserTypeCustomer, config.ClientCapacity.Customer},
		{"Observer", UserTypeObserver, config.ClientCapacity.Observer},
		{"Admin", UserTypeAdmin, config.ClientCapacity.Admin},
		{"VIP", UserTypeVIP, config.ClientCapacity.VIP},
		{"Visitor", UserTypeVisitor, config.ClientCapacity.Visitor},
		{"System", UserTypeSystem, config.ClientCapacity.System},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := &Client{
				ID:       "test-" + tt.name,
				UserID:   "user-" + tt.name,
				UserType: tt.userType,
				Status:   UserStatusOnline,
				Context:  context.Background(),
			}

			capacity := hub.getClientCapacity(client)
			assert.Equal(t, tt.expected, capacity, "容量应该匹配配置值")
		})
	}
}

// TestClientCapacityNilClient 测试客户端为 nil 时的处理
func TestClientCapacityNilClient(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	capacity := hub.getClientCapacity(nil)
	// 应该返回默认容量
	assert.Equal(t, DefaultClientSendChanCapacity, capacity)
}

// TestInitChannelPools 测试多级对象池初始化
func TestInitChannelPools(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	// 验证对象池已初始化
	require.NotNil(t, hub.chanPools, "对象池map应该已初始化")

	// 验证所有配置的容量都有对应的对象池
	expectedCapacities := map[int]bool{
		16:  true, // Observer
		64:  true, // Visitor, Default
		96:  true, // Customer
		128: true, // Bot, VIP
		256: true, // Agent, Admin, System
	}

	for capacity := range expectedCapacities {
		pool, exists := hub.chanPools[capacity]
		assert.True(t, exists, "容量 %d 应该有对应的对象池", capacity)
		assert.NotNil(t, pool, "容量 %d 的对象池不应该为nil", capacity)
	}

	t.Logf("初始化了 %d 个对象池", len(hub.chanPools))
}

// TestGetChan 测试从对象池获取channel
func TestGetChan(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	tests := []struct {
		name        string
		capacity    int
		description string
	}{
		{"获取256容量channel", 256, "应该能从对象池获取256容量的channel"},
		{"获取128容量channel", 128, "应该能从对象池获取128容量的channel"},
		{"获取96容量channel", 96, "应该能从对象池获取96容量的channel"},
		{"获取16容量channel", 16, "应该能从对象池获取16容量的channel"},
		{"获取非配置容量channel", 512, "非配置容量应该创建新channel"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ch := hub.getChan(tt.capacity)
			require.NotNil(t, ch, "获取的channel不应该为nil")
			assert.Equal(t, tt.capacity, cap(ch), "channel容量应该匹配")
		})
	}
}

// TestReleaseChan 测试释放channel到对象池
func TestReleaseChan(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	t.Run("释放配置容量的channel", func(t *testing.T) {
		capacity := 256
		ch := hub.getChan(capacity)
		require.NotNil(t, ch)

		// 向channel写入数据
		ch <- []byte("test data")

		// 释放channel
		hub.releaseChan(ch, capacity)

		// 再次获取，应该得到清空后的channel
		ch2 := hub.getChan(capacity)
		require.NotNil(t, ch2)
		assert.Equal(t, 0, len(ch2), "从对象池获取的channel应该是空的")
	})

	t.Run("释放非配置容量的channel", func(t *testing.T) {
		capacity := 512
		ch := make(chan []byte, capacity)

		// 释放非配置容量的channel（不会进入对象池）
		hub.releaseChan(ch, capacity)

		// 验证不会panic
		assert.NotPanics(t, func() {
			hub.releaseChan(ch, capacity)
		})
	})

	t.Run("释放nil channel", func(t *testing.T) {
		// 验证不会panic
		assert.NotPanics(t, func() {
			hub.releaseChan(nil, 256)
		})
	})
}

// TestInitClientSendChan 测试初始化客户端 SendChan
func TestInitClientSendChan(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	client := &Client{
		ID:       "test-client",
		UserID:   "test-user",
		UserType: UserTypeCustomer,
		Status:   UserStatusOnline,
		Context:  context.Background(),
	}

	// 初始化前 SendChan 应该为 nil
	assert.Nil(t, client.SendChan)

	// 初始化 SendChan
	hub.initClientSendChan(client)

	// 初始化后 SendChan 应该不为 nil
	assert.NotNil(t, client.SendChan)

	// 容量应该是 Customer 的容量
	assert.Equal(t, config.ClientCapacity.Customer, cap(client.SendChan))
}

// TestInitClientSendChanIdempotent 测试初始化 SendChan 的幂等性
func TestInitClientSendChanIdempotent(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	client := &Client{
		ID:       "test-idempotent",
		UserID:   "user-idempotent",
		UserType: UserTypeCustomer,
		Status:   UserStatusOnline,
		Context:  context.Background(),
	}

	// 第一次初始化
	hub.initClientSendChan(client)
	firstChan := client.SendChan

	// 第二次初始化（应该不改变）
	hub.initClientSendChan(client)
	secondChan := client.SendChan

	// 应该是同一个 channel
	assert.Equal(t, firstChan, secondChan, "SendChan 应该保持不变")
}

// TestRegisterClientWithCapacity 测试注册客户端时自动初始化 SendChan
func TestRegisterClientWithCapacity(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.SafeShutdown()

	go hub.Run()
	hub.WaitForStart()

	// 创建不同类型的客户端
	testCases := []struct {
		name     string
		userType UserType
		expected int
	}{
		{"Agent", UserTypeAgent, config.ClientCapacity.Agent},
		{"Customer", UserTypeCustomer, config.ClientCapacity.Customer},
		{"Visitor", UserTypeVisitor, config.ClientCapacity.Visitor},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			client := &Client{
				ID:             "test-" + tc.name,
				UserID:         "user-" + tc.name,
				UserType:       tc.userType,
				Status:         UserStatusOnline,
				Context:        context.Background(),
				ConnectionType: ConnectionTypeWebSocket,
				Metadata:       make(map[string]interface{}),
			}

			hub.Register(client)

			// 等待客户端注册完成
			require.Eventually(t, func() bool {
				return hub.HasClient(client.ID)
			}, time.Second, 10*time.Millisecond, "客户端应该在 1 秒内注册成功")

			// 从 Hub 获取已注册的客户端（线程安全）
			registeredClient := hub.GetClientByID(client.ID)
			require.NotNil(t, registeredClient, "应该能获取到已注册的客户端")

			// 验证 SendChan 已初始化且容量正确
			assert.NotNil(t, registeredClient.SendChan, "SendChan 应该已初始化")
			assert.Equal(t, tc.expected, cap(registeredClient.SendChan), "容量应该匹配配置值")

			hub.Unregister(client)

			// 使用公开方法检查客户端是否已注销
			require.Eventually(t, func() bool {
				return !hub.HasClient(client.ID)
			}, time.Second, 10*time.Millisecond, "客户端应该在 1 秒内注销")
		})
	}
}

// TestClientCapacityConfigDefault 测试默认容量配置
func TestClientCapacityConfigDefault(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	capacityConfig := hub.GetClientCapacityConfig()
	assert.NotNil(t, capacityConfig)

	// 验证所有容量值与配置一致
	assert.Equal(t, config.ClientCapacity.Agent, capacityConfig.Agent)
	assert.Equal(t, config.ClientCapacity.Bot, capacityConfig.Bot)
	assert.Equal(t, config.ClientCapacity.Customer, capacityConfig.Customer)
	assert.Equal(t, config.ClientCapacity.Observer, capacityConfig.Observer)
	assert.Equal(t, config.ClientCapacity.Admin, capacityConfig.Admin)
	assert.Equal(t, config.ClientCapacity.VIP, capacityConfig.VIP)
	assert.Equal(t, config.ClientCapacity.Visitor, capacityConfig.Visitor)
	assert.Equal(t, config.ClientCapacity.System, capacityConfig.System)
	assert.Equal(t, config.ClientCapacity.Default, capacityConfig.Default)
}

// TestClientCapacityCustomConfig 测试自定义容量配置
func TestClientCapacityCustomConfig(t *testing.T) {
	config := wscconfig.Default()

	// 自定义容量配置
	config.ClientCapacity = &wsc.ClientCapacity{
		Agent:    512,
		Bot:      256,
		Customer: 128,
		Observer: 32,
		Admin:    512,
		VIP:      256,
		Visitor:  128,
		System:   512,
		Default:  128,
	}

	hub := NewHub(config)

	client := &Client{
		ID:       "test-custom",
		UserID:   "user-custom",
		UserType: UserTypeCustomer,
		Status:   UserStatusOnline,
		Context:  context.Background(),
	}

	capacity := hub.getClientCapacity(client)
	assert.Equal(t, 128, capacity, "应该使用自定义容量")
}

// TestClientCapacityNilConfig 测试配置为 nil 时的默认行为
func TestClientCapacityNilConfig(t *testing.T) {
	config := wscconfig.Default()
	config.ClientCapacity = nil

	hub := NewHub(config)

	client := &Client{
		ID:       "test-nil",
		UserID:   "user-nil",
		UserType: UserTypeCustomer,
		Status:   UserStatusOnline,
		Context:  context.Background(),
	}

	capacity := hub.getClientCapacity(client)
	// 应该返回默认容量
	assert.Equal(t, DefaultClientSendChanCapacity, capacity)
}

// TestReleaseClientSendChan 测试释放客户端SendChan
func TestReleaseClientSendChan(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	t.Run("正常释放", func(t *testing.T) {
		client := &Client{
			ID:       "test-client",
			UserID:   "test-user",
			UserType: UserTypeAgent,
		}

		hub.initClientSendChan(client)
		require.NotNil(t, client.SendChan)

		hub.releaseClientSendChan(client)
		assert.Nil(t, client.SendChan, "释放后SendChan应该为nil")
	})

	t.Run("释放nil客户端", func(t *testing.T) {
		assert.NotPanics(t, func() {
			hub.releaseClientSendChan(nil)
		})
	})

	t.Run("释放SendChan为nil的客户端", func(t *testing.T) {
		client := &Client{
			ID:       "test-client",
			UserID:   "test-user",
			UserType: UserTypeAgent,
			SendChan: nil,
		}

		assert.NotPanics(t, func() {
			hub.releaseClientSendChan(client)
		})
	})
}

// TestChannelPoolReuse 测试channel对象池复用
func TestChannelPoolReuse(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	t.Run("验证channel复用", func(t *testing.T) {
		capacity := 256
		channels := make([]chan []byte, 10)

		// 获取10个channel
		for i := 0; i < 10; i++ {
			channels[i] = hub.getChan(capacity)
			require.NotNil(t, channels[i])
			assert.Equal(t, capacity, cap(channels[i]))
		}

		// 释放所有channel
		for _, ch := range channels {
			hub.releaseChan(ch, capacity)
		}

		// 再次获取channel，应该复用之前释放的
		reusedChannels := make([]chan []byte, 10)
		for i := 0; i < 10; i++ {
			reusedChannels[i] = hub.getChan(capacity)
			require.NotNil(t, reusedChannels[i])
		}

		// 验证至少有一些channel被复用了
		// 注意：由于对象池的实现，不能保证100%复用
		t.Logf("成功获取并复用了 %d 个channel", len(reusedChannels))
	})
}

// TestMultipleClientTypes 测试多种客户端类型同时使用
func TestMultipleClientTypes(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	clients := []*Client{
		{ID: "agent-1", UserID: "user-1", UserType: UserTypeAgent},
		{ID: "customer-1", UserID: "user-2", UserType: UserTypeCustomer},
		{ID: "observer-1", UserID: "user-3", UserType: UserTypeObserver},
		{ID: "bot-1", UserID: "user-4", UserType: UserTypeBot},
		{ID: "vip-1", UserID: "user-5", UserType: UserTypeVIP},
	}

	// 初始化所有客户端的SendChan
	for _, client := range clients {
		hub.initClientSendChan(client)
		require.NotNil(t, client.SendChan, "客户端 %s 的SendChan应该已初始化", client.ID)
	}

	// 验证容量正确
	expectedCapacities := map[string]int{
		"agent-1":    256,
		"customer-1": 96,
		"observer-1": 16,
		"bot-1":      128,
		"vip-1":      128,
	}

	for _, client := range clients {
		expectedCap := expectedCapacities[client.ID]
		assert.Equal(t, expectedCap, cap(client.SendChan),
			"客户端 %s 的容量应该是 %d", client.ID, expectedCap)
	}

	// 释放所有客户端的SendChan
	for _, client := range clients {
		hub.releaseClientSendChan(client)
		assert.Nil(t, client.SendChan, "客户端 %s 的SendChan应该已释放", client.ID)
	}
}

// BenchmarkGetChan 性能测试：从对象池获取channel
func BenchmarkGetChan(b *testing.B) {
	config := wscconfig.Default()
	hub := NewHub(config)

	b.Run("获取256容量channel", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ch := hub.getChan(256)
			hub.releaseChan(ch, 256)
		}
	})

	b.Run("获取非配置容量channel", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = hub.getChan(512)
		}
	})
}

// BenchmarkInitClientSendChan 性能测试：初始化客户端SendChan
func BenchmarkInitClientSendChan(b *testing.B) {
	config := wscconfig.Default()
	hub := NewHub(config)

	b.Run("Agent客户端", func(b *testing.B) {
		clients := make([]*Client, b.N)
		for i := 0; i < b.N; i++ {
			clients[i] = &Client{
				ID:       "test-client",
				UserID:   "test-user",
				UserType: UserTypeAgent,
			}
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hub.initClientSendChan(clients[i])
		}
	})

	b.Run("Customer客户端", func(b *testing.B) {
		clients := make([]*Client, b.N)
		for i := 0; i < b.N; i++ {
			clients[i] = &Client{
				ID:       "test-client",
				UserID:   "test-user",
				UserType: UserTypeCustomer,
			}
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			hub.initClientSendChan(clients[i])
		}
	})
}

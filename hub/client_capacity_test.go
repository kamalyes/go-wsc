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

// TestRegisterClientWithCapacity 测试注册客户端时自动初始化 SendChan
func TestRegisterClientWithCapacity(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.SafeShutdown()

	// 启动 Hub
	go hub.Run()
	time.Sleep(100 * time.Millisecond)

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

			// 注册客户端
			hub.Register(client)
			time.Sleep(50 * time.Millisecond)

			// 验证 SendChan 已初始化且容量正确
			assert.NotNil(t, client.SendChan, "SendChan 应该已初始化")
			assert.Equal(t, tc.expected, cap(client.SendChan), "容量应该匹配配置值")

			// 清理
			hub.Unregister(client)
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

// TestClientCapacityNilClient 测试客户端为 nil 时的处理
func TestClientCapacityNilClient(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	capacity := hub.getClientCapacity(nil)
	// 应该返回默认容量
	assert.Equal(t, DefaultClientSendChanCapacity, capacity)
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

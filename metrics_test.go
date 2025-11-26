/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 20:50:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 20:50:00
 * @FilePath: \go-wsc\metrics_test.go
 * @Description: 高级性能指标收集器测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

// TestMetricsCollector 测试指标收集器
func TestMetricsCollector(t *testing.T) {
	t.Run("基础指标收集", func(t *testing.T) {
		config := wscconfig.Default().
			WithPerformance(wscconfig.DefaultPerformance().
				WithMetrics(true, 1)) // 1秒间隔快速测试

		hub := NewHub(config)
		defer hub.Shutdown()

		// 验证指标收集器已创建
		assert.NotNil(t, hub.metricsCollector)
		assert.True(t, hub.metricsCollector.IsEnabled())

		// 启动Hub
		go hub.Run()
		hub.WaitForStart()

		// 验证指标收集器已启动
		time.Sleep(100 * time.Millisecond)

		// 获取当前指标
		metrics := hub.GetAdvancedMetrics()
		assert.NotNil(t, metrics)
		assert.NotZero(t, metrics.CollectedAt)
	})

	t.Run("连接指标测试", func(t *testing.T) {
		config := wscconfig.Default().
			WithPerformance(wscconfig.DefaultPerformance().
				WithMetrics(true, 1))

		hub := NewHub(config)
		defer hub.Shutdown()

		go hub.Run()
		hub.WaitForStart()

		// 创建测试客户端
		client := &Client{
			ID:       "metrics-test-client-001",
			UserID:   "metrics-test-user-001",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "metrics-test-user-001"),
		}

		// 启动消息消费者避免通道阻塞
		go func() {
			for range client.SendChan {
				// 消费消息
			}
		}()

		// 注册客户端
		hub.Register(client)

		// 等待指标更新
		time.Sleep(100 * time.Millisecond)

		// 检查连接指标
		metrics := hub.GetAdvancedMetrics()
		assert.NotNil(t, metrics)
		assert.Equal(t, int64(1), metrics.ActiveConnections)
		assert.GreaterOrEqual(t, metrics.TotalConnections, int64(1))

		// 注销客户端
		hub.Unregister(client)

		// 等待指标更新
		time.Sleep(100 * time.Millisecond)

		// 检查连接指标已更新
		metrics = hub.GetAdvancedMetrics()
		assert.Equal(t, int64(0), metrics.ActiveConnections)
	})

	t.Run("消息指标测试", func(t *testing.T) {
		config := wscconfig.Default().
			WithPerformance(wscconfig.DefaultPerformance().
				WithMetrics(true, 1))

		hub := NewHub(config)
		defer hub.Shutdown()

		go hub.Run()
		hub.WaitForStart()

		// 创建测试客户端
		client := &Client{
			ID:       "metrics-msg-client-001",
			UserID:   "metrics-msg-user-001",
			UserType: UserTypeCustomer,
			Role:     UserRoleCustomer,
			Status:   UserStatusOnline,
			LastSeen: time.Now(),
			SendChan: make(chan []byte, 256),
			Context:  context.WithValue(context.Background(), ContextKeyUserID, "metrics-msg-user-001"),
		}

		// 启动消息消费者
		go func() {
			for range client.SendChan {
				// 消费消息
			}
		}()

		hub.Register(client)

		// 发送测试消息
		testMsg := &HubMessage{
			ID:          "metrics-test-msg-001",
			MessageType: MessageTypeText,
			Sender:      "sender-001",
			Receiver:    "metrics-msg-user-001",
			Content:     "指标测试消息",
			CreateAt:    time.Now(),
			Status:      MessageStatusSent,
		}

		// 发送消息
		err := hub.SendToUser(context.Background(), "metrics-msg-user-001", testMsg)
		assert.NoError(t, err)

		// 等待指标更新
		time.Sleep(100 * time.Millisecond)

		// 检查消息指标
		metrics := hub.GetAdvancedMetrics()
		assert.NotNil(t, metrics)
		assert.GreaterOrEqual(t, metrics.MessagesSent, int64(1))
		assert.Greater(t, metrics.BytesSent, int64(0))

		hub.Unregister(client)
	})

	t.Run("延迟指标测试", func(t *testing.T) {
		config := wscconfig.Default().
			WithPerformance(wscconfig.DefaultPerformance().
				WithMetrics(true, 1))

		hub := NewHub(config)
		defer hub.Shutdown()

		go hub.Run()
		hub.WaitForStart()

		// 模拟延迟记录
		startTime := time.Now()
		time.Sleep(10 * time.Millisecond)
		hub.recordMessageLatency(startTime)

		// 等待指标更新
		time.Sleep(100 * time.Millisecond)

		// 检查延迟指标
		metrics := hub.GetAdvancedMetrics()
		assert.NotNil(t, metrics)
		assert.GreaterOrEqual(t, metrics.AverageLatency, 10.0) // 应该至少10毫秒
	})

	t.Run("指标历史测试", func(t *testing.T) {
		config := wscconfig.Default().
			WithPerformance(wscconfig.DefaultPerformance().
				WithMetrics(true, 1))

		hub := NewHub(config)
		defer hub.Shutdown()

		go hub.Run()
		hub.WaitForStart()

		// 等待几个指标收集周期
		time.Sleep(2500 * time.Millisecond) // 2.5秒，应该有至少2个指标记录

		// 获取指标历史
		history := hub.GetMetricsHistory()
		assert.NotNil(t, history)
		assert.GreaterOrEqual(t, len(history), 1) // 至少应该有一个历史记录
	})

	t.Run("启用/禁用指标收集", func(t *testing.T) {
		config := wscconfig.Default().
			WithPerformance(wscconfig.DefaultPerformance().
				WithMetrics(true, 1))

		hub := NewHub(config)
		defer hub.Shutdown()

		// 验证默认启用
		assert.True(t, hub.metricsCollector.IsEnabled())

		// 禁用指标收集
		hub.DisableMetrics()
		assert.False(t, hub.metricsCollector.IsEnabled())

		// 重新启用指标收集
		hub.EnableMetrics()
		assert.True(t, hub.metricsCollector.IsEnabled())
	})
}

// TestMetricsCollectorWithoutConfig 测试没有配置时的行为
func TestMetricsCollectorWithoutConfig(t *testing.T) {
	// 使用默认配置（虽然默认启用指标，但不会创建收集器，因为是默认配置）
	hub := NewHub(nil)
	defer hub.Shutdown()

	// 验证指标收集器未创建（因为使用的是默认配置）
	assert.Nil(t, hub.metricsCollector)

	// 验证相关方法返回nil而不是崩溃
	assert.Nil(t, hub.GetAdvancedMetrics())
	assert.Nil(t, hub.GetMetricsHistory())

	// 验证启用/禁用方法不会崩溃
	hub.EnableMetrics()
	hub.DisableMetrics()

	// 验证记录延迟方法不会崩溃
	hub.recordMessageLatency(time.Now())

	// 测试显式禁用指标的配置
	disabledConfig := wscconfig.Default().
		WithPerformance(wscconfig.DefaultPerformance().
			WithMetrics(false, 60))

	hubDisabled := NewHub(disabledConfig)
	defer hubDisabled.Shutdown()

	assert.Nil(t, hubDisabled.metricsCollector)
} // BenchmarkMetricsCollection 指标收集性能基准测试
func BenchmarkMetricsCollection(b *testing.B) {
	config := wscconfig.Default().
		WithPerformance(wscconfig.DefaultPerformance().
			WithMetrics(true, 60)) // 60秒间隔，避免在基准测试中频繁收集

	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	b.ResetTimer()
	b.ReportAllocs()

	b.Run("记录消息发送", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if hub.metricsCollector != nil {
				hub.metricsCollector.IncrementMessageSent(100)
			}
		}
	})

	b.Run("记录延迟", func(b *testing.B) {
		startTime := time.Now()
		for i := 0; i < b.N; i++ {
			hub.recordMessageLatency(startTime)
		}
	})

	b.Run("获取当前指标", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			metrics := hub.GetAdvancedMetrics()
			_ = metrics
		}
	})
}

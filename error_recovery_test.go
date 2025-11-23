/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 21:15:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 21:05:36
 * @FilePath: \go-wsc\error_recovery_test.go
 * @Description: 错误恢复系统测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestErrorRecoverySystemBasicFunctionality(t *testing.T) {
	// 创建带有配置的Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
		EnableMetrics:         true,
		MetricsInterval:       1,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 测试基本方法
	assert.True(t, hub.IsErrorRecoveryEnabled())

	// 测试错误报告
	ctx := map[string]interface{}{
		"test_key": "test_value",
	}
	errorID := hub.ReportError(ErrorTypeConnection, SeverityMedium, "测试连接错误", ctx)
	assert.NotEmpty(t, errorID)

	// 等待错误处理
	time.Sleep(100 * time.Millisecond)

	// 检查错误历史
	history := hub.GetErrorHistory()
	assert.NotNil(t, history)
	assert.Len(t, history, 1)

	// 检查统计信息
	stats := hub.GetErrorRecoveryStatistics()
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "total_errors")
	assert.Equal(t, int64(1), stats["total_errors"])

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestErrorRecoveryWithoutConfig(t *testing.T) {
	// 创建默认配置的Hub（没有显式配置）
	hub := NewHub(nil)

	// 在没有显式配置时，不应该有错误恢复系统
	assert.Nil(t, hub.errorRecovery)
	assert.False(t, hub.IsErrorRecoveryEnabled())
	assert.Nil(t, hub.GetErrorHistory())
	assert.Nil(t, hub.GetErrorRecoveryStatistics())

	// 这些操作应该安全地不执行任何操作
	errorID := hub.ReportError(ErrorTypeConnection, SeverityMedium, "测试错误", nil)
	assert.Empty(t, errorID)

	hub.EnableErrorRecovery()
	hub.DisableErrorRecovery()

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestErrorRecoveryCircuitBreaker(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 启动错误恢复系统
	hub.errorRecovery.Start()

	// 检查初始熔断器状态
	states := hub.GetCircuitBreakerStates()
	assert.NotNil(t, states)
	assert.Equal(t, CircuitClosed, states[ErrorTypeConnection])

	// 报告多个错误以触发熔断器
	for i := 0; i < 6; i++ {
		hub.ReportError(ErrorTypeConnection, SeverityHigh, "连续连接错误", nil)
		time.Sleep(10 * time.Millisecond)
	}

	// 等待错误处理
	time.Sleep(200 * time.Millisecond)

	// 检查统计信息
	stats := hub.GetErrorRecoveryStatistics()
	assert.True(t, stats["total_errors"].(int64) >= 6)

	// 停止错误恢复系统
	hub.errorRecovery.Stop()

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestErrorRecoveryStrategies(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 启动错误恢复系统
	hub.errorRecovery.Start()

	// 测试不同类型的错误恢复
	errorTypes := []ErrorType{
		ErrorTypeConnection,
		ErrorTypeMemory,
		ErrorTypeSystem,
		ErrorTypeNetwork,
	}

	for _, errorType := range errorTypes {
		errorID := hub.ReportError(errorType, SeverityMedium, fmt.Sprintf("测试%d错误", int(errorType)), nil)
		assert.NotEmpty(t, errorID)

		// 等待恢复处理
		time.Sleep(50 * time.Millisecond)
	}

	// 检查错误历史
	history := hub.GetErrorHistory()
	assert.True(t, len(history) >= len(errorTypes))

	// 停止错误恢复系统
	hub.errorRecovery.Stop()

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestErrorRecoveryRetryConfig(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 设置重试配置
	hub.SetErrorRecoveryRetryConfig(5, 500*time.Millisecond, 1.5)

	// 检查配置是否生效
	stats := hub.GetErrorRecoveryStatistics()
	assert.Equal(t, 5, stats["max_retries"])

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestErrorRecoveryCircuitBreakerConfig(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 设置熔断器配置
	hub.SetCircuitBreakerConfig(ErrorTypeConnection, 10, 5, 60*time.Second)

	// 检查熔断器状态
	states := hub.GetCircuitBreakerStates()
	assert.Equal(t, CircuitClosed, states[ErrorTypeConnection])

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestErrorRecoveryEnableDisable(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 默认应该是启用的
	assert.True(t, hub.IsErrorRecoveryEnabled())

	// 禁用错误恢复
	hub.DisableErrorRecovery()
	assert.False(t, hub.IsErrorRecoveryEnabled())

	// 在禁用状态下报告错误应该返回空字符串
	errorID := hub.ReportError(ErrorTypeConnection, SeverityMedium, "测试错误", nil)
	assert.Empty(t, errorID)

	// 重新启用
	hub.EnableErrorRecovery()
	assert.True(t, hub.IsErrorRecoveryEnabled())

	// 启用后应该能正常报告错误
	errorID = hub.ReportError(ErrorTypeConnection, SeverityMedium, "测试错误", nil)
	assert.NotEmpty(t, errorID)

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

func TestCustomRecoveryStrategy(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 创建自定义恢复策略
	customStrategy := &TestRecoveryStrategy{
		name: "TestStrategy",
		canHandleFunc: func(errorType ErrorType, severity ErrorSeverity) bool {
			return errorType == ErrorTypeConfiguration
		},
		recoverFunc: func(ctx context.Context, errorEntry *ErrorEntry) error {
			// 模拟恢复操作
			return nil
		},
	}

	// 添加自定义策略
	hub.AddRecoveryStrategy(customStrategy)

	// 报告配置错误
	errorID := hub.ReportError(ErrorTypeConfiguration, SeverityMedium, "配置错误", nil)
	assert.NotEmpty(t, errorID)

	// 等待恢复处理
	time.Sleep(100 * time.Millisecond)

	// 检查错误是否被处理
	history := hub.GetErrorHistory()
	assert.Len(t, history, 1)

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

// TestRecoveryStrategy 测试用的恢复策略
type TestRecoveryStrategy struct {
	name          string
	canHandleFunc func(ErrorType, ErrorSeverity) bool
	recoverFunc   func(context.Context, *ErrorEntry) error
}

func (trs *TestRecoveryStrategy) CanHandle(errorType ErrorType, severity ErrorSeverity) bool {
	if trs.canHandleFunc != nil {
		return trs.canHandleFunc(errorType, severity)
	}
	return false
}

func (trs *TestRecoveryStrategy) Recover(ctx context.Context, errorEntry *ErrorEntry) error {
	if trs.recoverFunc != nil {
		return trs.recoverFunc(ctx, errorEntry)
	}
	return nil
}

func (trs *TestRecoveryStrategy) GetName() string {
	return trs.name
}

func TestErrorRecoverySystemStartStop(t *testing.T) {
	// 创建Hub
	config := wscconfig.Default()
	config.Performance = &wscconfig.Performance{
		MaxConnectionsPerNode: 100,
	}

	hub := NewHub(config)
	require.NotNil(t, hub.errorRecovery)

	// 启动错误恢复系统
	hub.errorRecovery.Start()

	// 等待一段时间
	time.Sleep(100 * time.Millisecond)

	// 停止错误恢复系统
	hub.errorRecovery.Stop()

	// 关闭Hub
	err := hub.SafeShutdown()
	assert.NoError(t, err)
}

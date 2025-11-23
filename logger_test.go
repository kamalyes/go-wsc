/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 20:15:06
 * @FilePath: \go-wsc\logger_test.go
 * @Description: go-wsc 日志测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"testing"
	"time"
)

func TestNewDefaultWSCLogger(t *testing.T) {
	logger := NewDefaultWSCLogger()
	assert.NotNil(t, logger)

	// 测试基本日志方法
	logger.Info("测试信息日志")
	logger.Debug("测试调试日志")
	logger.Warn("测试警告日志")

	// 测试键值对日志
	logger.InfoKV("测试键值对日志", "key1", "value1", "key2", 123)

	// 测试业务特定方法
	logger.LogConnection("client123", "user456", "connected")
	logger.LogMessage("msg001", "user1", "user2", MessageTypeText, true, nil)
	logger.LogPerformance("send_message", "10ms", map[string]interface{}{
		"message_count": 1,
		"queue_size":    10,
	})
}

func TestNoOpLogger(t *testing.T) {
	logger := NewNoOpLogger()
	assert.NotNil(t, logger)

	// 所有方法都应该正常调用但不产生输出
	logger.Info("这条消息不应该输出")
	logger.LogConnection("client", "user", "test")
	logger.LogMessage("msg", "from", "to", MessageTypeText, true, nil)
}

func TestLoggerWithFields(t *testing.T) {
	logger := NewDefaultWSCLogger()

	// 测试 WithField
	fieldLogger := logger.WithField("component", "hub")
	assert.NotNil(t, fieldLogger)

	// 测试 WithFields
	fieldsLogger := logger.WithFields(map[string]interface{}{
		"module":  "websocket",
		"version": "1.0.0",
	})
	assert.NotNil(t, fieldsLogger)

	fieldsLogger.Info("带字段的日志消息")
}

func TestGlobalLoggerMethods(t *testing.T) {
	// 测试全局日志方法
	Info("全局信息日志")
	Debug("全局调试日志")
	InfoKV("全局键值对日志", "test", true)

	// 切换到空日志器测试
	originalLogger := DefaultLogger
	SetDefaultLogger(NewNoOpLogger())

	Info("这条消息应该被忽略")

	// 恢复原始日志器
	SetDefaultLogger(originalLogger)
}

func TestHubWithLogger(t *testing.T) {
	// 创建配置 - 暂时用默认配置，因为 go-config 的 Logging 字段还在更新中
	config := wscconfig.Default()

	// 创建 Hub，它会自动初始化日志器
	hub := NewHub(config)
	assert.NotNil(t, hub)
	assert.NotNil(t, hub.logger)

	// 测试 Hub 的日志功能
	hub.logger.InfoKV("Hub 初始化完成",
		"node_id", hub.nodeID,
		"node_ip", config.NodeIP,
		"node_port", config.NodePort,
	)

	// 测试连接日志
	hub.logger.LogConnection("test-client-123", "user-456", "connected")

	// 测试消息日志
	hub.logger.LogMessage("msg-001", "user1", "user2", MessageTypeText, true, nil)

	// 测试性能日志
	hub.logger.LogPerformance("message_processing", "5ms", map[string]interface{}{
		"message_count": 100,
		"queue_size":    50,
		"memory_usage":  "10MB",
	})

	// 测试错误日志
	hub.logger.WithField("component", "websocket").
		WithField("client_id", "client-123").
		Error("连接异常断开")

	// 模拟Hub运行时日志
	hub.logger.Info("Hub 启动成功，开始接收连接")
	time.Sleep(10 * time.Millisecond) // 模拟运行时间
	hub.logger.Info("Hub 运行正常")
}

func TestGlobalLoggerUsage(t *testing.T) {
	// 测试全局日志器的使用
	Info("这是全局信息日志")
	InfoKV("服务器状态", "status", "running", "uptime", "1h30m")

	// 在业务逻辑中使用
	processMessage := func(userID, messageContent string) {
		InfoKV("处理消息",
			"user_id", userID,
			"message_length", len(messageContent),
			"timestamp", time.Now().Unix(),
		)

		// 模拟处理时间
		time.Sleep(1 * time.Millisecond)

		Info("消息处理完成")
	}

	processMessage("user123", "Hello World!")
}

func TestLoggerConfiguration(t *testing.T) {
	// 创建自定义日志器配置示例
	customLogger := NewDefaultWSCLogger()

	// 使用自定义日志器
	customLogger.InfoKV("自定义日志器示例",
		"log_level", "info",
		"output", "console",
		"colorful", true,
	)

	// 测试带字段的日志
	contextLogger := customLogger.
		WithField("module", "authentication").
		WithField("session_id", "sess_12345")

	contextLogger.Info("用户登录成功")
	contextLogger.Warn("登录尝试次数较多")

	// 测试业务特定方法 - 需要转换为WSCLogger接口
	if wscLogger, ok := contextLogger.(*WSCLoggerAdapter); ok {
		wscLogger.LogConnection("conn_123", "user_789", "authenticated")
		wscLogger.LogPerformance("login_process", "150ms", map[string]interface{}{
			"auth_method": "oauth2",
			"provider":    "google",
		})
	}
}

// ExampleWSCLogger 使用示例
func ExampleWSCLogger() {
	// 创建默认日志器
	logger := NewDefaultWSCLogger()

	// 基本日志
	logger.Info("服务启动")
	logger.Warn("配置文件缺失，使用默认配置")

	// 键值对日志（推荐）
	logger.InfoKV("用户连接",
		"user_id", "12345",
		"client_ip", "192.168.1.100",
		"timestamp", time.Now(),
	)

	// 带字段的日志
	userLogger := logger.WithField("user_id", "12345")
	userLogger.Info("用户认证成功")
	userLogger.Error("用户权限不足")

	// 业务特定日志
	logger.LogConnection("client_001", "user_12345", "connected")
	logger.LogMessage("msg_001", "user_a", "user_b", MessageTypeText, true, nil)
	logger.LogPerformance("send_message", "10ms", map[string]interface{}{
		"recipients":   5,
		"message_size": 1024,
	})
}

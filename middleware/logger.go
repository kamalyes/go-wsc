/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:22:16
 * @FilePath: \go-wsc\middleware\logger.go
 * @Description: go-wsc 日志接口，直接复用 go-logger
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package middleware

import (
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
)

// WSCLogger 直接使用 go-logger.ILogger
type WSCLogger = logger.ILogger

// NewWSCLogger 创建新的WSC日志器，基于 go-logger
func NewWSCLogger(config *logger.LogConfig) WSCLogger {
	return logger.NewLogger(config)
}

// NewDefaultWSCLogger 创建默认配置的WSC日志器
func NewDefaultWSCLogger() WSCLogger {
	config := logger.DefaultConfig().
		WithLevel(logger.DEBUG).
		WithPrefix("[WSC] ").
		WithShowCaller(false).
		WithColorful(true).
		WithTimeFormat(time.RFC3339Nano)

	return logger.NewLogger(config)
}

// NewNoOpLogger 创建空日志实例
func NewNoOpLogger() WSCLogger {
	return logger.NewEmptyLogger()
}

// 全局日志器
var (
	// DefaultLogger 默认日志器实例
	DefaultLogger WSCLogger = NewDefaultWSCLogger()

	// NoOpLoggerInstance 空日志器实例
	NoOpLoggerInstance WSCLogger = NewNoOpLogger()
)

// SetDefaultLogger 设置默认日志器
func SetDefaultLogger(l WSCLogger) {
	DefaultLogger = l
}

// InitLogger 根据配置初始化日志器
func InitLogger(config *wscconfig.WSC) WSCLogger {
	if config.Logging == nil || !config.Logging.Enabled {
		return NewDefaultWSCLogger()
	}

	loggerConfig := config.Logging.ToLoggerConfig()
	return logger.NewLogger(loggerConfig)
}

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

// NewDefaultWSCLogger 创建默认配置的WSC日志器
func NewDefaultWSCLogger() WSCLogger {
	logger := logger.NewLogger().
		WithLevel(logger.DEBUG).
		WithPrefix("[WSC]").
		WithShowCaller(false).
		WithColorful(true).
		WithTimeFormat(time.RFC3339Nano)

	return logger
}

// InitLogger 根据配置初始化日志器
func InitLogger(config *wscconfig.WSC) WSCLogger {
	if config.Logging == nil || !config.Logging.Enabled {
		return NewDefaultWSCLogger()
	}

	return config.Logging.ToLoggerInstance()
}

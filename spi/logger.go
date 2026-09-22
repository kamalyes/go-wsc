/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 15:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 15:00:00
 * @FilePath: \go-wsc\spi\logger.go
 * @Description: 日志 SPI - Logger 接口契约与 go-logger 默认实现
 *
 * 日志是核心运行时能力，不是可插拔中间件，因此接口归 spi 而非 middleware
 * 默认实现直接使用 go-logger；业务侧可注入任意满足本接口的日志器
 * （zap / logrus / 自研封装等），不注入则自动使用 go-logger 默认实例
 *
 * 注意：本接口的方法集与 go-logger.ILogger 完全一致，因此
 *   - 任何实现 logger.ILogger 的具体类型（如 *logger.Logger）可直接赋给 Logger
 *   - 任何 logger.ILogger 接口值也可直接赋给 Logger
 * 无需显式转换，业务侧零迁移成本
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
)

// Logger 日志接口，等价于 go-logger.ILogger
type Logger logger.ILogger

// NewDefaultLogger 返回默认日志器（go-logger 实例）
func NewDefaultLogger() Logger {
	return logger.NewLogger().
		WithLevel(logger.DEBUG).
		WithPrefix("[WSC]").
		WithShowCaller(false).
		WithColorful(true).
		WithTimeFormat(time.RFC3339Nano)
}

// InitLogger 根据配置初始化日志器
// Logging 未启用时回退 NewDefaultLogger，启用时使用配置构造的 go-logger 实例
func InitLogger(config *wscconfig.WSC) Logger {
	if config == nil || config.Logging == nil || !config.Logging.Enabled {
		return NewDefaultLogger()
	}

	return config.Logging.ToLoggerInstance()
}

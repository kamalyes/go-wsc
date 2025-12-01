/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-01 23:30:00
 * @FilePath: \go-wsc\logger.go
 * @Description: go-wsc 日志接口，直接复用 go-logger
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"os"
	"time"
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
		WithLevel(logger.INFO).
		WithPrefix("[WSC] ").
		WithShowCaller(false).
		WithColorful(true).
		WithTimeFormat("2006-01-02 15:04:05")

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

// initLogger 根据配置初始化日志器
func initLogger(config *wscconfig.WSC) WSCLogger {
	// 如果配置中有日志配置且启用，使用配置中的
	if config.Logging != nil && config.Logging.Enabled {
		// 转换配置到 go-logger 的配置
		loggerConfig := logger.DefaultConfig().
			WithLevel(parseLogLevel(config.Logging.Level)).
			WithPrefix("[WSC] ").
			WithShowCaller(false).
			WithColorful(true).
			WithTimeFormat(time.DateTime)

		// 根据输出类型配置输出
		switch config.Logging.Output {
		case "file":
			if config.Logging.FilePath != "" {
				// 根据是否需要轮转决定使用哪种文件写入器
				if config.Logging.MaxSize > 0 && config.Logging.MaxBackups > 0 {
					// 使用轮转文件写入器
					rotateWriter := logger.NewRotateWriter(
						config.Logging.FilePath,
						int64(config.Logging.MaxSize)*1024*1024, // 转换为字节
						config.Logging.MaxBackups,
					)
					loggerConfig = loggerConfig.WithOutput(rotateWriter)
				} else {
					// 使用简单文件写入器
					fileWriter := logger.NewFileWriter(config.Logging.FilePath)
					loggerConfig = loggerConfig.WithOutput(fileWriter)
				}
			}
		default:
			// 默认使用控制台输出
			loggerConfig = loggerConfig.WithOutput(logger.NewConsoleWriter(os.Stdout))
		}

		return logger.NewLogger(loggerConfig)
	}

	// 使用默认配置
	return NewDefaultWSCLogger()
}

// parseLogLevel 解析日志级别字符串
func parseLogLevel(level string) logger.LogLevel {
	switch level {
	case "debug", "DEBUG":
		return logger.DEBUG
	case "info", "INFO":
		return logger.INFO
	case "warn", "WARN", "warning", "WARNING":
		return logger.WARN
	case "error", "ERROR":
		return logger.ERROR
	case "fatal", "FATAL":
		return logger.FATAL
	default:
		return logger.INFO // 默认级别
	}
}

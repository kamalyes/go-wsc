/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 19:30:00
 * @FilePath: \go-wsc\logger.go
 * @Description: go-wsc 日志接口，复用 go-logger
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"github.com/kamalyes/go-logger"
)

// WSCLogger go-wsc 专用的日志接口（继承 go-logger.ILogger）
type WSCLogger interface {
	logger.ILogger

	// 扩展方法：业务特定的日志记录
	LogConnection(clientID, userID string, action string)
	LogMessage(messageID, fromUser, toUser string, messageType MessageType, success bool, err error)
	LogPerformance(operation string, duration string, details map[string]interface{})
	LogError(component string, err error, details map[string]interface{})
}

// WSCLoggerAdapter go-logger 的适配器实现
type WSCLoggerAdapter struct {
	logger.ILogger
}

// NewWSCLogger 创建新的WSC日志器，基于 go-logger
func NewWSCLogger(config *logger.LogConfig) WSCLogger {
	goLogger := logger.NewLogger(config)

	return &WSCLoggerAdapter{
		ILogger: goLogger,
	}
}

// NewDefaultWSCLogger 创建默认配置的WSC日志器
func NewDefaultWSCLogger() WSCLogger {
	config := logger.DefaultConfig().
		WithLevel(logger.INFO).
		WithPrefix("[WSC] ").
		WithShowCaller(false).
		WithColorful(true).
		WithTimeFormat("2006-01-02 15:04:05")

	return NewWSCLogger(config)
}

// 业务特定的日志方法实现
func (w *WSCLoggerAdapter) LogConnection(clientID, userID string, action string) {
	w.InfoKV("连接事件",
		"client_id", clientID,
		"user_id", userID,
		"action", action,
	)
}

func (w *WSCLoggerAdapter) LogMessage(messageID, fromUser, toUser string, messageType MessageType, success bool, err error) {
	fields := map[string]interface{}{
		"message_id":   messageID,
		"from_user":    fromUser,
		"to_user":      toUser,
		"message_type": messageType,
		"success":      success,
	}

	if err != nil {
		fields["error"] = err.Error()
		w.WithFields(fields).Error("消息发送失败")
	} else {
		w.WithFields(fields).Info("消息发送成功")
	}
}

func (w *WSCLoggerAdapter) LogPerformance(operation string, duration string, details map[string]interface{}) {
	fields := map[string]interface{}{
		"operation": operation,
		"duration":  duration,
	}

	// 合并详细信息
	for k, v := range details {
		fields[k] = v
	}

	w.WithFields(fields).Info("性能统计")
}

func (w *WSCLoggerAdapter) LogError(component string, err error, details map[string]interface{}) {
	fields := map[string]interface{}{
		"component": component,
		"error":     err.Error(),
	}

	// 合并详细信息
	for k, v := range details {
		fields[k] = v
	}

	w.WithFields(fields).Error("组件错误")
}

// NoOpLogger 空日志实现（用于禁用日志）
type NoOpLogger struct {
	logger.ILogger
}

// NewNoOpLogger 创建空日志实例
func NewNoOpLogger() WSCLogger {
	return &NoOpLogger{
		ILogger: logger.NewEmptyLogger(),
	}
}

// 业务方法的空实现
func (n *NoOpLogger) LogConnection(clientID, userID string, action string) {}
func (n *NoOpLogger) LogMessage(messageID, fromUser, toUser string, messageType MessageType, success bool, err error) {
}
func (n *NoOpLogger) LogPerformance(operation string, duration string, details map[string]interface{}) {
}
func (n *NoOpLogger) LogError(component string, err error, details map[string]interface{}) {}

// 全局日志器（可选，用于简化使用）
var (
	// DefaultLogger 默认日志器实例
	DefaultLogger WSCLogger = NewDefaultWSCLogger()

	// NoOpLoggerInstance 空日志器实例
	NoOpLoggerInstance WSCLogger = NewNoOpLogger()
)

// 全局日志方法（可选，用于简化使用）
func Debug(msg string, fields ...interface{}) {
	DefaultLogger.Debug(msg, fields...)
}

func Info(msg string, fields ...interface{}) {
	DefaultLogger.Info(msg, fields...)
}

func Warn(msg string, fields ...interface{}) {
	DefaultLogger.Warn(msg, fields...)
}

func Error(msg string, fields ...interface{}) {
	DefaultLogger.Error(msg, fields...)
}

func InfoKV(msg string, keysAndValues ...interface{}) {
	DefaultLogger.InfoKV(msg, keysAndValues...)
}

func ErrorKV(msg string, keysAndValues ...interface{}) {
	DefaultLogger.ErrorKV(msg, keysAndValues...)
}

// SetDefaultLogger 设置默认日志器
func SetDefaultLogger(logger WSCLogger) {
	DefaultLogger = logger
}

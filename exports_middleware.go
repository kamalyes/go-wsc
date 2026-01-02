/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-30 00:15:51
 * @FilePath: \go-wsc\exports_middleware.go
 * @Description: Middleware 模块类型导出 - 保持向后兼容
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/middleware"
)

// ============================================
// Logger - 日志中间件
// ============================================

// WSCLogger 日志器类型（直接使用 go-logger.ILogger）
type WSCLogger = logger.ILogger

// NewWSCLogger 创建新的WSC日志器
var NewWSCLogger = middleware.NewWSCLogger

// NewDefaultWSCLogger 创建默认配置的WSC日志器
var NewDefaultWSCLogger = middleware.NewDefaultWSCLogger

// NewNoOpLogger 创建空日志实例
var NewNoOpLogger = middleware.NewNoOpLogger

// SetDefaultLogger 设置默认日志器
var SetDefaultLogger = middleware.SetDefaultLogger

// InitLogger 根据配置初始化日志器
var InitLogger = middleware.InitLogger

// ============================================
// Rate Limiter - 限流器
// ============================================

// RateLimiterConfig 限流器配置
type RateLimiterConfig = middleware.RateLimiterConfig

// RedisClient Redis客户端接口
type RedisClient = middleware.RedisClient

// RateLimiter 限流器
type RateLimiter = middleware.RateLimiter

// DefaultRateLimiterConfig 默认限流器配置
var DefaultRateLimiterConfig = middleware.DefaultRateLimiterConfig

// NewRateLimiter 创建限流器
var NewRateLimiter = middleware.NewRateLimiter

// ============================================
// Rate Limit Alert - 限流告警
// ============================================

// EmailSender 邮件发送接口
type EmailSender = middleware.EmailSender

// RateLimitAlertService 限流告警服务
type RateLimitAlertService = middleware.RateLimitAlertService

// AlertTemplateData 告警模板数据
type AlertTemplateData = middleware.AlertTemplateData

// NewRateLimitAlertService 创建限流告警服务
var NewRateLimitAlertService = middleware.NewRateLimitAlertService

// ============================================================================
// RateLimiter 方法导出 - 这些方法通过 RateLimiter 实例调用
// ============================================================================

// 注意：以下是 RateLimiter 类型的方法列表，通过 RateLimiter 实例调用
// 例如：limiter := wsc.NewRateLimiter(config)

// 限流检查方法：
// - CheckLimit(ctx context.Context, userID, userType string) (bool, int64, int64, error): 检查限流
// - ResetUserLimit(ctx context.Context, userID string) error: 重置用户限流
// - GetUserMessageCount(ctx context.Context, userID string) (minuteCount, hourCount int64): 获取用户消息数

// ============================================================================
// RateLimitAlertService 方法导出 - 这些方法通过 RateLimitAlertService 实例调用
// ============================================================================

// 注意：以下是 RateLimitAlertService 类型的方法列表
// 例如：alertService := wsc.NewRateLimitAlertService(...)

// 告警方法：
// - SendAlert(ctx context.Context, userId, userType string, minuteCount, hourCount int64): 发送告警
// - SendBlockAlert(ctx context.Context, userId, userType string, minuteCount, hourCount int64): 发送阻止告警


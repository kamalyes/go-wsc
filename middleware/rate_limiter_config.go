/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-26 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-26 13:02:08
 * @FilePath: \go-wsc\middleware\rate_limiter_config.go
 * @Description: 限流器配置适配器
 *   - 从 go-config 的 MessageRateLimit 构建 RateLimiterConfig
 *   - 使用 safe.MergeWithDefaults 递归合并默认值（零值字段用默认配置填充）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package middleware

import (
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/safe"
)

// NewRateLimiterConfigFromMessageRateLimit 从 go-config 的 MessageRateLimit 构建 RateLimiterConfig
//
// 配置映射：
//   - MaxMessages → MaxMessagesPerMinute（Window 默认 1 分钟）
//   - MaxMessages × 60 → MaxMessagesPerHour
//   - AlertThreshold(%) × MaxMessages / 100 → AlertThreshold(绝对值)
//   - MaxMessages → BlockThreshold（达到上限即封禁）
//   - UseRedis → RedisEnabled
//   - RedisKeyPrefix → RedisKeyPrefix
//
// 默认值策略：
//   - 使用 safe.MergeWithDefaults 递归合并默认值
//   - cfg 为 nil 或字段为零值时，自动使用 wscconfig.DefaultMessageRateLimit() 填充
//   - 默认值集中维护在 go-config，避免业务代码重复定义
//
// 用法：
//
//	cfg := wscconfig.DefaultMessageRateLimit()
//	rateLimiterCfg := middleware.NewRateLimiterConfigFromMessageRateLimit(cfg)
//	rateLimiterCfg.RedisClient = middleware.NewGoRedisRateLimitClient(redisClient) // 注入客户端
//	limiter := middleware.NewRateLimiter(rateLimiterCfg)
func NewRateLimiterConfigFromMessageRateLimit(cfg *wscconfig.MessageRateLimit) *RateLimiterConfig {
	// 使用 safe.MergeWithDefaults 递归合并默认值（cfg 为 nil 或字段为零值时自动填充默认值）
	cfg = safe.MergeWithDefaults(cfg, wscconfig.DefaultMessageRateLimit())

	// 按分钟换算（Window > 1 分钟时按比例降低每分钟限额）
	maxPerMinute := cfg.MaxMessages
	if cfg.Window != time.Minute && cfg.Window > 0 {
		maxPerMinute = int(float64(cfg.MaxMessages) * float64(time.Minute) / float64(cfg.Window))
		if maxPerMinute < 1 {
			maxPerMinute = 1
		}
	}

	// 每小时限额 = 每分钟 × 60
	maxPerHour := maxPerMinute * 60

	// 预警阈值（百分比转绝对值）
	alertAbs := maxPerMinute * cfg.AlertThreshold / 100
	if alertAbs < 1 {
		alertAbs = 1
	}

	// 封禁阈值 = 每分钟最大消息数（达到上限即封禁）
	blockThreshold := maxPerMinute

	// Redis key 前缀兜底（MergeWithDefaults 已处理，此处双重保险）
	redisKeyPrefix := cfg.RedisKeyPrefix
	if redisKeyPrefix == "" {
		redisKeyPrefix = rateLimitDefaultKeyPrefix
	}

	return &RateLimiterConfig{
		MaxMessagesPerMinute: maxPerMinute,
		MaxMessagesPerHour:   maxPerHour,
		AlertThreshold:       alertAbs,
		BlockThreshold:       blockThreshold,
		RedisEnabled:         cfg.UseRedis,
		RedisKeyPrefix:       redisKeyPrefix,
	}
}

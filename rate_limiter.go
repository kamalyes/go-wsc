/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-05 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-05 14:30:00
 * @FilePath: \go-wsc\rate_limiter.go
 * @Description: WebSocket消息频率限制器 - 防止恶意刷屏和异常行为
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// RateLimiterConfig 频率限制配置
type RateLimiterConfig struct {
	// 限流阈值
	MaxMessagesPerMinute int // 每分钟最大消息数
	MaxMessagesPerHour   int // 每小时最大消息数
	AlertThreshold       int // 预警阈值（触发回调）
	BlockThreshold       int // 封禁阈值（拒绝发送）

	// 回调函数
	OnAlert func(ctx context.Context, userID, userType string, minuteCount, hourCount int64) // 预警回调
	OnBlock func(ctx context.Context, userID, userType string, minuteCount, hourCount int64) // 封禁回调

	// Redis相关（可选，不提供则使用内存计数）
	RedisEnabled bool
	RedisClient  RedisClient                               // Redis客户端接口
	RedisKeyFunc func(userID string, window string) string // Redis键生成函数
}

// RedisClient Redis客户端接口
type RedisClient interface {
	Incr(ctx context.Context, key string) (int64, error)
	Expire(ctx context.Context, key string, ttl time.Duration) error
	Get(ctx context.Context, key string) (int64, error)
	Del(ctx context.Context, keys ...string) error
}

// DefaultRateLimiterConfig 默认频率限制配置
func DefaultRateLimiterConfig() *RateLimiterConfig {
	return &RateLimiterConfig{
		MaxMessagesPerMinute: 30,
		MaxMessagesPerHour:   200,
		AlertThreshold:       30,
		BlockThreshold:       50,
		RedisEnabled:         false,
	}
}

// RateLimiter 频率限制器
type RateLimiter struct {
	config *RateLimiterConfig

	// 内存计数器（Redis未启用时使用）
	memoryCounters map[string]*userCounter
	mu             sync.RWMutex
}

// userCounter 用户消息计数器
type userCounter struct {
	minuteCount int64
	hourCount   int64
	minuteTime  time.Time
	hourTime    time.Time
	mu          sync.Mutex
}

// NewRateLimiter 创建频率限制器
func NewRateLimiter(config *RateLimiterConfig) *RateLimiter {
	if config == nil {
		config = DefaultRateLimiterConfig()
	}

	limiter := &RateLimiter{
		config:         config,
		memoryCounters: make(map[string]*userCounter),
	}

	// 如果使用内存计数器，启动清理协程
	if !config.RedisEnabled {
		go limiter.cleanupMemoryCounters()
	}

	return limiter
}

// CheckLimit 检查用户消息发送频率
// 返回：是否允许发送、当前分钟计数、当前小时计数、错误信息
func (r *RateLimiter) CheckLimit(ctx context.Context, userID, userType string) (bool, int64, int64, error) {
	var minuteCount, hourCount int64
	var err error

	// 使用Redis或内存计数器
	if r.config.RedisEnabled && r.config.RedisClient != nil {
		minuteCount, hourCount, err = r.checkRedisLimit(ctx, userID)
	} else {
		minuteCount, hourCount, err = r.checkMemoryLimit(userID)
	}

	if err != nil {
		// 出错时允许通过，避免影响正常业务
		return true, 0, 0, err
	}

	// 检查是否超过封禁阈值
	if minuteCount > int64(r.config.BlockThreshold) {
		// 触发封禁回调
		if r.config.OnBlock != nil {
			go r.config.OnBlock(ctx, userID, userType, minuteCount, hourCount)
		}
		return false, minuteCount, hourCount, fmt.Errorf("消息发送过于频繁，已被临时限制")
	}

	// 检查是否超过预警阈值
	if minuteCount >= int64(r.config.AlertThreshold) || hourCount >= int64(r.config.MaxMessagesPerHour) {
		// 触发预警回调
		if r.config.OnAlert != nil {
			go r.config.OnAlert(ctx, userID, userType, minuteCount, hourCount)
		}
	}

	return true, minuteCount, hourCount, nil
}

// checkRedisLimit 使用Redis进行限流检查
func (r *RateLimiter) checkRedisLimit(ctx context.Context, userID string) (int64, int64, error) {
	minuteKey := r.getRedisKey(userID, "minute")
	hourKey := r.getRedisKey(userID, "hour")

	// 增加分钟计数
	minuteCount, err := r.config.RedisClient.Incr(ctx, minuteKey)
	if err != nil {
		return 0, 0, err
	}
	if minuteCount == 1 {
		_ = r.config.RedisClient.Expire(ctx, minuteKey, 60*time.Second)
	}

	// 增加小时计数
	hourCount, err := r.config.RedisClient.Incr(ctx, hourKey)
	if err != nil {
		return minuteCount, 0, err
	}
	if hourCount == 1 {
		_ = r.config.RedisClient.Expire(ctx, hourKey, 60*time.Minute)
	}

	return minuteCount, hourCount, nil
}

// checkMemoryLimit 使用内存进行限流检查
func (r *RateLimiter) checkMemoryLimit(userID string) (int64, int64, error) {
	r.mu.Lock()
	counter, exists := r.memoryCounters[userID]
	if !exists {
		counter = &userCounter{
			minuteTime: time.Now(),
			hourTime:   time.Now(),
		}
		r.memoryCounters[userID] = counter
	}
	r.mu.Unlock()

	counter.mu.Lock()
	defer counter.mu.Unlock()

	now := time.Now()

	// 检查分钟窗口是否过期
	if now.Sub(counter.minuteTime) > time.Minute {
		counter.minuteCount = 0
		counter.minuteTime = now
	}
	counter.minuteCount++

	// 检查小时窗口是否过期
	if now.Sub(counter.hourTime) > time.Hour {
		counter.hourCount = 0
		counter.hourTime = now
	}
	counter.hourCount++

	return counter.minuteCount, counter.hourCount, nil
}

// ResetUserLimit 重置用户限制
func (r *RateLimiter) ResetUserLimit(ctx context.Context, userID string) error {
	if r.config.RedisEnabled && r.config.RedisClient != nil {
		minuteKey := r.getRedisKey(userID, "minute")
		hourKey := r.getRedisKey(userID, "hour")
		return r.config.RedisClient.Del(ctx, minuteKey, hourKey)
	}

	r.mu.Lock()
	delete(r.memoryCounters, userID)
	r.mu.Unlock()

	return nil
}

// GetUserMessageCount 获取用户消息计数
func (r *RateLimiter) GetUserMessageCount(ctx context.Context, userID string) (minuteCount, hourCount int64) {
	if r.config.RedisEnabled && r.config.RedisClient != nil {
		minuteKey := r.getRedisKey(userID, "minute")
		hourKey := r.getRedisKey(userID, "hour")

		minuteCount, _ = r.config.RedisClient.Get(ctx, minuteKey)
		hourCount, _ = r.config.RedisClient.Get(ctx, hourKey)
		return
	}

	r.mu.RLock()
	counter, exists := r.memoryCounters[userID]
	r.mu.RUnlock()

	if !exists {
		return 0, 0
	}

	counter.mu.Lock()
	minuteCount = counter.minuteCount
	hourCount = counter.hourCount
	counter.mu.Unlock()

	return
}

// getRedisKey 生成Redis键
func (r *RateLimiter) getRedisKey(userID, window string) string {
	if r.config.RedisKeyFunc != nil {
		return r.config.RedisKeyFunc(userID, window)
	}

	var timeKey string
	if window == "minute" {
		timeKey = time.Now().Format("2006-01-02:15:04")
	} else {
		timeKey = time.Now().Format("2006-01-02:15")
	}

	return fmt.Sprintf("wsc:rate_limit:%s:%s:%s", userID, window, timeKey)
}

// cleanupMemoryCounters 定期清理过期的内存计数器
func (r *RateLimiter) cleanupMemoryCounters() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		now := time.Now()
		for userID, counter := range r.memoryCounters {
			counter.mu.Lock()
			// 如果两个窗口都已过期，删除计数器
			if now.Sub(counter.minuteTime) > 5*time.Minute && now.Sub(counter.hourTime) > 2*time.Hour {
				delete(r.memoryCounters, userID)
			}
			counter.mu.Unlock()
		}
		r.mu.Unlock()
	}
}

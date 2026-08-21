/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-26 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-26 00:00:00
 * @FilePath: \go-wsc\middleware\rate_limiter_test.go
 * @Description: 限流器测试
 *   - 内存计数器测试
 *   - 统一 RedisClient 接口测试（IncrExpire + BatchIncrExpire）
 *   - go-config 配置适配测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckMemoryLimit 内存计数器测试
func TestCheckMemoryLimit(t *testing.T) {
	config := &RateLimiterConfig{
		MaxMessagesPerMinute: 30,
		MaxMessagesPerHour:   200,
		AlertThreshold:       30,
		BlockThreshold:       50,
		RedisEnabled:         false,
	}
	limiter := NewRateLimiter(config)

	userID := "test-user"

	// 第一次检查
	minuteCount, hourCount, err := limiter.checkMemoryLimit(userID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), minuteCount)
	assert.Equal(t, int64(1), hourCount)

	// 第二次检查
	minuteCount, hourCount, err = limiter.checkMemoryLimit(userID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), minuteCount)
	assert.Equal(t, int64(2), hourCount)
}

// TestCheckRedisLimit 统一 Redis 客户端限流测试
// 验证 BatchIncrExpire 单次 Pipeline 往返的正确性
func TestCheckRedisLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := NewGoRedisRateLimitClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}))

	config := &RateLimiterConfig{
		MaxMessagesPerMinute: 30,
		MaxMessagesPerHour:   200,
		AlertThreshold:       30,
		BlockThreshold:       50,
		RedisEnabled:         true,
		RedisClient:          client,
	}
	limiter := NewRateLimiter(config)

	ctx := context.Background()
	userID := "redis-user"

	// 第一次检查：minuteCount=1, hourCount=1
	minuteCount, hourCount, err := limiter.checkRedisLimit(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), minuteCount)
	assert.Equal(t, int64(1), hourCount)

	// 第二次检查：minuteCount=2, hourCount=2
	minuteCount, hourCount, err = limiter.checkRedisLimit(ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), minuteCount)
	assert.Equal(t, int64(2), hourCount)

	// 验证 Expire 已设置（key 存在 TTL）
	minuteKey := limiter.getRedisKey(userID, "minute")
	hourKey := limiter.getRedisKey(userID, "hour")

	ttl := mr.TTL(minuteKey)
	assert.True(t, ttl > 0 && ttl <= 60*time.Second, "minute key TTL should be set")
	ttl = mr.TTL(hourKey)
	assert.True(t, ttl > 0 && ttl <= 60*time.Minute, "hour key TTL should be set")
}

// TestGoRedisRateLimitClient_IncrExpire 单个 IncrExpire 测试
func TestGoRedisRateLimitClient_IncrExpire(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := NewGoRedisRateLimitClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx := context.Background()

	// 第一次 Incr
	count, err := client.IncrExpire(ctx, "test:single", 60*time.Second)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)

	// 第二次 Incr
	count, err = client.IncrExpire(ctx, "test:single", 60*time.Second)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)

	// 验证 TTL
	ttl := mr.TTL("test:single")
	assert.True(t, ttl > 0 && ttl <= 60*time.Second)
}

// TestGoRedisRateLimitClient_BatchIncrExpire 批量 BatchIncrExpire 测试
func TestGoRedisRateLimitClient_BatchIncrExpire(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := NewGoRedisRateLimitClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx := context.Background()

	// 批量 Incr+Expire（2 个 key）
	counts, err := client.BatchIncrExpire(ctx,
		IncrExpireEntry{Key: "test:minute", TTL: 60 * time.Second},
		IncrExpireEntry{Key: "test:hour", TTL: 60 * time.Minute},
	)
	require.NoError(t, err)
	assert.Len(t, counts, 2)
	assert.Equal(t, int64(1), counts[0])
	assert.Equal(t, int64(1), counts[1])

	// 第二次调用
	counts, err = client.BatchIncrExpire(ctx,
		IncrExpireEntry{Key: "test:minute", TTL: 60 * time.Second},
		IncrExpireEntry{Key: "test:hour", TTL: 60 * time.Minute},
	)
	require.NoError(t, err)
	assert.Equal(t, int64(2), counts[0])
	assert.Equal(t, int64(2), counts[1])

	// 验证 TTL
	minuteTTL := mr.TTL("test:minute")
	hourTTL := mr.TTL("test:hour")
	assert.True(t, minuteTTL > 0 && minuteTTL <= 60*time.Second)
	assert.True(t, hourTTL > 0 && hourTTL <= 60*time.Minute)
}

// TestGoRedisRateLimitClient_BatchIncrExpire_Empty 空条目测试
func TestGoRedisRateLimitClient_BatchIncrExpire_Empty(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	client := NewGoRedisRateLimitClient(redis.NewClient(&redis.Options{Addr: mr.Addr()}))
	ctx := context.Background()

	counts, err := client.BatchIncrExpire(ctx)
	require.NoError(t, err)
	assert.Nil(t, counts)
}

// TestGetRedisKey_KeyConsistency 验证 minute/hour key 时间基准一致
func TestGetRedisKey_KeyConsistency(t *testing.T) {
	config := &RateLimiterConfig{
		RedisEnabled:   false,
		RedisKeyPrefix: "test:",
	}
	limiter := NewRateLimiter(config)

	userID := "consistency-test"
	minuteKey := limiter.getRedisKey(userID, "minute")
	hourKey := limiter.getRedisKey(userID, "hour")

	// 两个 key 都应包含 userID
	assert.Contains(t, minuteKey, userID)
	assert.Contains(t, hourKey, userID)

	// 两个 key 都应包含 window 标识
	assert.Contains(t, minuteKey, ":minute:")
	assert.Contains(t, hourKey, ":hour:")
}

// TestNewRateLimiterConfigFromMessageRateLimit go-config 配置适配测试
func TestNewRateLimiterConfigFromMessageRateLimit(t *testing.T) {
	t.Run("正常配置", func(t *testing.T) {
		cfg := &wscconfig.MessageRateLimit{
			Enabled:        true,
			Window:         time.Minute,
			MaxMessages:    100,
			AlertThreshold: 80,
			UseRedis:       true,
			RedisKeyPrefix: "myapp:rate:",
		}

		result := NewRateLimiterConfigFromMessageRateLimit(cfg)

		assert.Equal(t, 100, result.MaxMessagesPerMinute)
		assert.Equal(t, 6000, result.MaxMessagesPerHour) // 100 * 60
		assert.Equal(t, 80, result.AlertThreshold)       // 100 * 80 / 100
		assert.Equal(t, 100, result.BlockThreshold)      // = MaxMessages
		assert.True(t, result.RedisEnabled)
		assert.Equal(t, "myapp:rate:", result.RedisKeyPrefix)
	})

	t.Run("nil配置使用go-config默认值", func(t *testing.T) {
		result := NewRateLimiterConfigFromMessageRateLimit(nil)

		// 应使用 wscconfig.DefaultMessageRateLimit() 的值
		// MaxMessages=100, AlertThreshold=80, Window=1m
		assert.Equal(t, 100, result.MaxMessagesPerMinute)
		assert.Equal(t, 6000, result.MaxMessagesPerHour)
		assert.Equal(t, 80, result.AlertThreshold) // 100 * 80 / 100
		assert.Equal(t, 100, result.BlockThreshold)
	})

	t.Run("零值配置使用go-config默认值", func(t *testing.T) {
		cfg := &wscconfig.MessageRateLimit{}

		result := NewRateLimiterConfigFromMessageRateLimit(cfg)

		// 零值字段使用 go-config DefaultMessageRateLimit 的对应值
		assert.Equal(t, 100, result.MaxMessagesPerMinute)
		assert.Equal(t, 6000, result.MaxMessagesPerHour)
		assert.Equal(t, 80, result.AlertThreshold)
		assert.Equal(t, 100, result.BlockThreshold)
	})

	t.Run("5分钟窗口换算", func(t *testing.T) {
		cfg := &wscconfig.MessageRateLimit{
			Window:      5 * time.Minute,
			MaxMessages: 500,
		}

		result := NewRateLimiterConfigFromMessageRateLimit(cfg)

		// 500 / 5 分钟 = 100/分钟
		assert.Equal(t, 100, result.MaxMessagesPerMinute)
		assert.Equal(t, 6000, result.MaxMessagesPerHour) // 100 * 60
	})

	t.Run("空RedisKeyPrefix使用默认值", func(t *testing.T) {
		cfg := &wscconfig.MessageRateLimit{
			RedisKeyPrefix: "",
		}

		result := NewRateLimiterConfigFromMessageRateLimit(cfg)

		assert.Equal(t, rateLimitDefaultKeyPrefix, result.RedisKeyPrefix)
	})
}

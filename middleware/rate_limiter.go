/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-05 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\middleware\rate_limiter.go
 * @Description: WebSocket消息频率限制器 - 防止恶意刷屏和异常行为
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// rateLimitDefaultKeyPrefix 默认 Redis key 前缀
// 走配置时由 RateLimiterConfig.RedisKeyPrefix 覆盖
const rateLimitDefaultKeyPrefix = "wsc:rate_limit:"

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
	RedisEnabled   bool
	RedisClient    RedisClient                               // Redis客户端接口
	RedisKeyFunc   func(userID string, window string) string // 自定义 Redis键生成函数（最高优先级，覆盖 RedisKeyPrefix）
	RedisKeyPrefix string                                    // Redis key 前缀，默认 "wsc:rate_limit:"（来自 go-config MessageRateLimit.RedisKeyPrefix）
}

// RedisClient Redis客户端接口（统一单个和批量操作）
//
// 设计理念：
//   - 单个操作和批量操作合并在同一接口下，避免接口分裂
//   - 实现方应优先使用 Pipeline 执行 BatchIncrExpire，单次往返完成多个操作
//   - IncrExpire 用于单次检查场景，BatchIncrExpire 用于多窗口场景
//
// 性能：
//   - BatchIncrExpire：N 次 IncrExpire → 1 次 Pipeline（N RTT → 1 RTT）
//   - IncrExpire：INCR + 首次 EXPIRE（2 命令 1 RTT，Pipeline 合并）
type RedisClient interface {
	// IncrExpire 递增计数器并设置 TTL
	// 首次 Incr（返回 1）时设置 TTL，后续 Incr 不重置 TTL
	// 返回递增后的计数值
	IncrExpire(ctx context.Context, key string, ttl time.Duration) (int64, error)

	// BatchIncrExpire 批量递增计数器（单次 Redis 往返）
	// 同时为多个 key 执行 Incr 和首次 Expire
	// 返回值按 entries 顺序对应
	BatchIncrExpire(ctx context.Context, entries ...IncrExpireEntry) ([]int64, error)

	// Get 获取计数器值
	Get(ctx context.Context, key string) (int64, error)

	// Del 删除 key
	Del(ctx context.Context, keys ...string) error
}

// IncrExpireEntry 递增条目（用于批量操作）
type IncrExpireEntry struct {
	Key string
	TTL time.Duration
}

// DefaultRateLimiterConfig 默认频率限制配置
func DefaultRateLimiterConfig() *RateLimiterConfig {
	return &RateLimiterConfig{
		MaxMessagesPerMinute: 30,
		MaxMessagesPerHour:   200,
		AlertThreshold:       30,
		BlockThreshold:       50,
		RedisEnabled:         false,
		RedisKeyPrefix:       rateLimitDefaultKeyPrefix,
	}
}

// rateShardCount 限流计数器的分片数量
// 64 个分片将不同用户的计数器分散到不同 shard，消除全局锁竞争
const rateShardCount = 64

// RateLimiter 频率限制器
// 基于 syncx.ShardedMap 实现用户计数器的分片存储，替代单一 mutex + map
// 高并发下不同用户的限流检查完全并行，锁竞争降低 64 倍
type RateLimiter struct {
	config *RateLimiterConfig

	// memoryCounters 内存计数器（分片存储，零全局锁）
	// key: userID (string)，按 FNV-1a hash 分散到 64 个 shard
	// Redis 未启用时使用
	memoryCounters *syncx.ShardedMap[string, *userCounter]
}

// userCounter 用户消息计数器
// 无需内部锁：同一 userID 的操作由 ShardedMap 的 shard 写锁串行化
type userCounter struct {
	minuteCount int64
	hourCount   int64
	minuteTime  time.Time
	hourTime    time.Time
}

// NewRateLimiter 创建频率限制器
func NewRateLimiter(config *RateLimiterConfig) *RateLimiter {
	if config == nil {
		config = DefaultRateLimiterConfig()
	}

	limiter := &RateLimiter{
		config:         config,
		memoryCounters: syncx.NewShardedMap[string, *userCounter](rateShardCount),
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
//
// 性能：BatchIncrExpire 单次 Pipeline 往返（原 4 RTT → 1 RTT）
func (r *RateLimiter) checkRedisLimit(ctx context.Context, userID string) (int64, int64, error) {
	minuteKey := r.getRedisKey(userID, "minute")
	hourKey := r.getRedisKey(userID, "hour")

	// 批量执行 Incr+Expire（单次 Pipeline 往返）
	counts, err := r.config.RedisClient.BatchIncrExpire(ctx,
		IncrExpireEntry{Key: minuteKey, TTL: 60 * time.Second},
		IncrExpireEntry{Key: hourKey, TTL: 60 * time.Minute},
	)
	if err != nil {
		return 0, 0, err
	}

	return counts[0], counts[1], nil
}

// checkMemoryLimit 使用内存进行限流检查
// 在同一 shard 写锁内完成 LoadOrStore + 计数更新，无需二级锁
func (r *RateLimiter) checkMemoryLimit(userID string) (int64, int64, error) {
	now := time.Now()
	var minuteCount, hourCount int64

	// WithShardLock 保证同一 userID 落同一 shard，写锁串行化该用户的所有操作
	r.memoryCounters.WithShardLock(userID, func(data map[string]*userCounter) {
		counter, exists := data[userID]
		if !exists {
			counter = &userCounter{
				minuteTime: now,
				hourTime:   now,
			}
			data[userID] = counter
		}

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

		minuteCount = counter.minuteCount
		hourCount = counter.hourCount
	})

	return minuteCount, hourCount, nil
}

// ResetUserLimit 重置用户限制
func (r *RateLimiter) ResetUserLimit(ctx context.Context, userID string) error {
	if r.config.RedisEnabled && r.config.RedisClient != nil {
		minuteKey := r.getRedisKey(userID, "minute")
		hourKey := r.getRedisKey(userID, "hour")
		return r.config.RedisClient.Del(ctx, minuteKey, hourKey)
	}

	r.memoryCounters.Delete(userID)
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

	r.memoryCounters.WithShardRLock(userID, func(data map[string]*userCounter) {
		counter, exists := data[userID]
		if !exists {
			return
		}
		minuteCount = counter.minuteCount
		hourCount = counter.hourCount
	})

	return
}

// getRedisKey 生成Redis键
// 优先级：RedisKeyFunc > RedisKeyPrefix（来自 go-config）> 默认 "wsc:rate_limit:"
//
// 性能优化：原实现对同一 time.Now() 调用两次 Format（minute/hour 各一次），
// 现合并为单次 time.Now() + 单次 Format，避免重复系统调用和字符串格式化
func (r *RateLimiter) getRedisKey(userID, window string) string {
	if r.config.RedisKeyFunc != nil {
		return r.config.RedisKeyFunc(userID, window)
	}

	// 前缀走配置（默认 "wsc:rate_limit:"，可由 go-config MessageRateLimit.RedisKeyPrefix 覆盖）
	prefix := mathx.IfEmpty(r.config.RedisKeyPrefix, rateLimitDefaultKeyPrefix)

	// 单次 time.Now() 调用，按 window 选择格式（minute 精确到分钟，hour 精确到小时）
	now := time.Now()
	var timeKey string
	if window == "minute" {
		timeKey = now.Format("2006-01-02:15:04")
	} else {
		timeKey = now.Format("2006-01-02:15")
	}

	return prefix + userID + ":" + window + ":" + timeKey
}

// cleanupMemoryCounters 定期清理过期的内存计数器
// 使用 ShardedMap.Range 遍历（分片读锁），收集过期 key 后批量删除
func (r *RateLimiter) cleanupMemoryCounters() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		now := time.Now()
		var expired []string

		r.memoryCounters.Range(func(userID string, counter *userCounter) bool {
			// 如果两个窗口都已过期，标记为待删除
			if now.Sub(counter.minuteTime) > 5*time.Minute && now.Sub(counter.hourTime) > 2*time.Hour {
				expired = append(expired, userID)
			}
			return true
		})

		// 批量删除（Range 期间持有读锁，不能直接 delete）
		for _, userID := range expired {
			r.memoryCounters.Delete(userID)
		}
	}
}

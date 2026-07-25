/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-26 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-26 00:00:00
 * @FilePath: \go-wsc\middleware\redis_client_adapter.go
 * @Description: 限流器 Redis 客户端适配器
 *   - GoRedisRateLimitClient：封装 go-redis UniversalClient
 *   - 实现统一的 RedisClient 接口（IncrExpire + BatchIncrExpire 单往返）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package middleware

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// GoRedisRateLimitClient 基于 go-redis 的限流器客户端
// 实现统一的 RedisClient 接口，单次和批量操作都通过 Pipeline 优化
//
// 性能：
//   - IncrExpire：2 命令 1 RTT（INCR + EXPIRE Pipeline 合并）
//   - BatchIncrExpire：N×2 命令 1 RTT（N 个 INCR + EXPIRE Pipeline 合并）
//
// 用法：
//
//	client := middleware.NewGoRedisRateLimitClient(redisClient)
//	cfg := &middleware.RateLimiterConfig{
//	    RedisEnabled: true,
//	    RedisClient:  client,
//	    // ...
//	}
//	limiter := middleware.NewRateLimiter(cfg)
type GoRedisRateLimitClient struct {
	client redis.UniversalClient
}

// 确保 GoRedisRateLimitClient 实现 RedisClient 接口
var _ RedisClient = (*GoRedisRateLimitClient)(nil)

// NewGoRedisRateLimitClient 创建 go-redis 限流器客户端
func NewGoRedisRateLimitClient(client redis.UniversalClient) *GoRedisRateLimitClient {
	return &GoRedisRateLimitClient{client: client}
}

// IncrExpire 递增计数器并设置 TTL（Pipeline 合并为 1 RTT）
//
// 说明：Expire 始终执行（而非仅在 Incr 结果为 1 时执行）。
// 原因：Pipeline 模式下无法基于 Incr 的返回值条件性执行 Expire，
// 重复设置 Expire 只是刷新 TTL，等价于 SETEX 语义，无副作用且更安全。
// 在限流场景下，每个窗口 key 都应保持固定 TTL，刷新 TTL 反而更合理。
func (c *GoRedisRateLimitClient) IncrExpire(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := c.client.Pipeline()
	incrCmd := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)

	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return 0, err
	}

	return incrCmd.Result()
}

// BatchIncrExpire 批量递增计数器（单次 Pipeline 往返）
//
// 命令顺序：entries[0].Incr → entries[0].Expire → entries[1].Incr → entries[1].Expire → ...
// 返回值按 entries 顺序对应
//
// 性能：N 个 IncrExpire → 1 次 Pipeline（N RTT → 1 RTT）
//
// 说明：Expire 始终执行（同 IncrExpire 说明），刷新 TTL 无副作用。
func (c *GoRedisRateLimitClient) BatchIncrExpire(ctx context.Context, entries ...IncrExpireEntry) ([]int64, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	pipe := c.client.Pipeline()
	cmds := make([]*redis.IntCmd, len(entries))

	for i, entry := range entries {
		cmds[i] = pipe.Incr(ctx, entry.Key)
		pipe.Expire(ctx, entry.Key, entry.TTL)
	}

	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	result := make([]int64, len(entries))
	for i, cmd := range cmds {
		val, err := cmd.Result()
		if err != nil && err != redis.Nil {
			return nil, err
		}
		result[i] = val
	}

	return result, nil
}

// Get 获取计数器值（返回 int64，便于与限流接口对齐）
func (c *GoRedisRateLimitClient) Get(ctx context.Context, key string) (int64, error) {
	return c.client.Get(ctx, key).Int64()
}

// Del 删除键
func (c *GoRedisRateLimitClient) Del(ctx context.Context, keys ...string) error {
	return c.client.Del(ctx, keys...).Err()
}

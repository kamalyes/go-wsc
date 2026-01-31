/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 17:05:16
 * @FilePath: \go-wsc\repository\message_queue_repository.go
 * @Description: Redis消息队列仓库 - 支持看门狗锁的消息队列
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/redis/go-redis/v9"
)

// MessageQueueRepository 消息队列仓库接口
type MessageQueueRepository interface {
	// Enqueue 入队消息
	Enqueue(ctx context.Context, queueName string, msg *models.HubMessage) error

	// Dequeue 出队消息(阻塞式,带看门狗锁)
	Dequeue(ctx context.Context, queueName string, timeout time.Duration) (*models.HubMessage, error)

	// DequeueWithWatchdog 出队消息并启动看门狗锁
	DequeueWithWatchdog(ctx context.Context, queueName string, timeout time.Duration, processFunc func(*models.HubMessage) error) error

	// GetLength 获取队列长度
	GetLength(ctx context.Context, queueName string) (int64, error)

	// Clear 清空队列
	Clear(ctx context.Context, queueName string) error

	// Peek 查看队列头部消息(不移除)
	Peek(ctx context.Context, queueName string) (*models.HubMessage, error)
}

// RedisMessageQueueRepository Redis消息队列实现
type RedisMessageQueueRepository struct {
	client redis.UniversalClient
	prefix string
	ttl    time.Duration
}

// NewRedisMessageQueueRepository 创建Redis消息队列仓库
func NewRedisMessageQueueRepository(client redis.UniversalClient, prefix string, ttl time.Duration) *RedisMessageQueueRepository {
	prefix = mathx.IF(prefix == "", DefaultQueueKeyPrefix, prefix)
	ttl = mathx.IF(ttl < 0, 24*time.Hour, ttl)

	return &RedisMessageQueueRepository{
		client: client,
		prefix: prefix,
		ttl:    ttl,
	}
}

// Enqueue 入队消息
func (r *RedisMessageQueueRepository) Enqueue(ctx context.Context, queueName string, msg *models.HubMessage) error {
	if msg == nil {
		return fmt.Errorf("message is nil")
	}

	key := r.prefix + queueName

	// 使用 Zlib 压缩消息
	compressedData, err := zipx.ZlibCompressObject(msg)
	if err != nil {
		return fmt.Errorf("compress message failed: %w", err)
	}

	// 使用 RPUSH 添加到队列尾部
	pipe := r.client.Pipeline()
	pipe.RPush(ctx, key, compressedData)
	pipe.Expire(ctx, key, r.ttl)

	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("enqueue failed: %w", err)
	}

	return nil
}

// Dequeue 出队消息(阻塞式)
func (r *RedisMessageQueueRepository) Dequeue(ctx context.Context, queueName string, timeout time.Duration) (*models.HubMessage, error) {
	key := r.prefix + queueName

	// 使用 BLPOP 阻塞式从队列头部取出
	result, err := r.client.BLPop(ctx, timeout, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // 超时,无数据
		}
		return nil, fmt.Errorf("dequeue failed: %w", err)
	}

	// result[0]是key, result[1]是value
	if len(result) < 2 {
		return nil, fmt.Errorf("invalid blpop result")
	}

	// 使用 Zlib 解压缩消息
	msg, err := zipx.ZlibDecompressObject[*models.HubMessage]([]byte(result[1]))
	if err != nil {
		return nil, fmt.Errorf(models.ErrMsgDecompressFailed, err)
	}

	return msg, nil
}

// DequeueWithWatchdog 出队消息并启动看门狗锁
// 使用处理队列和看门狗机制防止消息丢失
func (r *RedisMessageQueueRepository) DequeueWithWatchdog(ctx context.Context, queueName string, timeout time.Duration, processFunc func(*models.HubMessage) error) error {
	sourceKey := r.prefix + queueName
	processingKey := r.prefix + queueName + ":processing"

	// 1. 使用 BRPOPLPUSH 原子性地从源队列移到处理队列
	//    这样即使进程崩溃,消息仍在processing队列中
	result, err := r.client.BRPopLPush(ctx, sourceKey, processingKey, timeout).Result()
	if err != nil {
		if err == redis.Nil {
			return nil // 超时,无数据
		}
		return fmt.Errorf("brpoplpush failed: %w", err)
	}

	// 2. 使用 Zlib 解压缩消息
	msg, err := zipx.ZlibDecompressObject[*models.HubMessage]([]byte(result))
	if err != nil {
		return fmt.Errorf(models.ErrMsgDecompressFailed, err)
	}

	// 3. 使用per-message lock key防止并发冲突
	lockKey := r.prefix + queueName + ":lock:" + msg.ID

	// 启动看门狗锁
	lockCtx, cancelLock := context.WithCancel(ctx)
	defer cancelLock()

	lockExpiry := 30 * time.Second
	renewInterval := 10 * time.Second

	// 获取初始锁
	locked, err := r.client.SetNX(lockCtx, lockKey, msg.ID, lockExpiry).Result()
	if err != nil || !locked {
		// 锁被占用,可能是重复处理,跳过
		return fmt.Errorf("failed to acquire lock for message %s", msg.ID)
	}

	// 启动看门狗goroutine自动续期
	watchdogDone := make(chan struct{})
	go func() {
		defer close(watchdogDone)
		ticker := time.NewTicker(renewInterval)
		defer ticker.Stop()

		for {
			select {
			case <-lockCtx.Done():
				return
			case <-ticker.C:
				// 续期锁
				_, _ = r.client.Expire(lockCtx, lockKey, lockExpiry).Result()
			}
		}
	}()

	// 4. 处理消息
	processErr := processFunc(msg)

	// 5. 停止看门狗
	cancelLock()
	<-watchdogDone

	// 6. 删除锁
	r.client.Del(ctx, lockKey)

	// 7. 从处理队列中移除消息(无论成功还是失败)
	r.client.LRem(ctx, processingKey, 1, result)

	// 8. 如果处理失败,可以选择重新入队或记录错误
	if processErr != nil {
		// 这里可以实现重试逻辑,暂时只返回错误
		return fmt.Errorf("process message failed: %w", processErr)
	}

	return nil
}

// GetLength 获取队列长度
func (r *RedisMessageQueueRepository) GetLength(ctx context.Context, queueName string) (int64, error) {
	key := r.prefix + queueName
	length, err := r.client.LLen(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("get queue length failed: %w", err)
	}
	return length, nil
}

// Clear 清空队列
func (r *RedisMessageQueueRepository) Clear(ctx context.Context, queueName string) error {
	key := r.prefix + queueName
	if err := r.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("clear queue failed: %w", err)
	}
	return nil
}

// Peek 查看队列头部消息(不移除)
func (r *RedisMessageQueueRepository) Peek(ctx context.Context, queueName string) (*models.HubMessage, error) {
	key := r.prefix + queueName

	// 使用 LINDEX 0 查看第一个元素
	result, err := r.client.LIndex(ctx, key, 0).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil // 队列为空
		}
		return nil, fmt.Errorf("peek failed: %w", err)
	}

	// 使用 Zlib 解压缩消息
	msg, err := zipx.ZlibDecompressObject[*models.HubMessage]([]byte(result))
	if err != nil {
		return nil, fmt.Errorf(models.ErrMsgDecompressFailed, err)
	}

	return msg, nil
}


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
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/redis/go-redis/v9"
)

// MessageQueueRepository 消息队列仓库接口
type MessageQueueRepository interface {
	// Enqueue 入队消息
	Enqueue(ctx context.Context, queueName string, msg *models.HubMessage) error

	// Dequeue 出队消息(阻塞式,带看门狗锁)
	Dequeue(ctx context.Context, queueName string, timeout time.Duration) (*models.HubMessage, error)

	// DequeueBatch 批量出队消息(非阻塞,单次 Redis 往返)
	DequeueBatch(ctx context.Context, queueName string, count int) ([]*models.HubMessage, error)

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
	prefix = mathx.IF(prefix == "", constants.DefaultQueueKeyPrefix, prefix)
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

// DequeueBatch 批量出队消息(非阻塞)
//
// 使用 LRANGE + LTRIM 管道一次性读取并移除 count 条消息，
// 将 N 次 BLPOP 的 N 次 Redis 往返降为 1 次管道调用
// 适用场景：用户上线拉取离线消息、批量消费等已知队列长度的场景
//
// 注意：LRANGE + LTRIM 非原子操作，但离线消息队列按用户隔离（每用户独立队列），
// 不存在并发消费者，因此管道级别的一致性已足够
func (r *RedisMessageQueueRepository) DequeueBatch(ctx context.Context, queueName string, count int) ([]*models.HubMessage, error) {
	if count <= 0 {
		return nil, nil
	}
	key := r.prefix + queueName

	// 管道：LRANGE 读取前 count 条 + LTRIM 移除已读条目
	pipe := r.client.Pipeline()
	rangeCmd := pipe.LRange(ctx, key, 0, int64(count-1))
	pipe.LTrim(ctx, key, int64(count), -1)

	if _, err := pipe.Exec(ctx); err != nil {
		// LRANGE/LTRIM 不会返回 redis.Nil，管道错误视为真实错误
		return nil, fmt.Errorf("dequeue batch failed: %w", err)
	}

	results, err := rangeCmd.Result()
	if err != nil {
		return nil, fmt.Errorf("lrange result failed: %w", err)
	}

	if len(results) == 0 {
		return nil, nil
	}

	// 预分配切片，逐条解压
	messages := make([]*models.HubMessage, 0, len(results))
	for _, data := range results {
		msg, err := zipx.ZlibDecompressObject[*models.HubMessage]([]byte(data))
		if err != nil {
			return nil, fmt.Errorf(models.ErrMsgDecompressFailed, err)
		}
		messages = append(messages, msg)
	}

	return messages, nil
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

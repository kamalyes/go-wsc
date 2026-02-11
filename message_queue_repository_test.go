/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-12 15:18:15
 * @FilePath: \go-wsc\message_queue_repository_test.go
 * @Description: Redis消息队列仓库集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupMessageQueueTest(t *testing.T) (*RedisMessageQueueRepository, *Hub, string, func()) {
	// 使用共享的 Redis 客户端，避免 FlushDB 清空其他测试的数据
	rdb := GetTestRedisClient(t)

	ctx := context.Background()
	repo := NewRedisMessageQueueRepository(rdb, "wsc:test:queue:", 1*time.Hour)

	// 创建 Hub 以获取 ID 生成器
	hub := NewHub(wscconfig.Default())

	// 使用唯一的队列名称(简化名称,避免特殊字符)
	queueName := fmt.Sprintf("q%s", hub.GetIDGenerator().GenerateTraceID())

	// 彻底清理可能存在的旧数据
	repo.Clear(ctx, queueName)
	repo.Clear(ctx, queueName+":processing")
	// 删除所有相关的锁
	iter := rdb.Scan(ctx, 0, "wsc:test:queue:"+queueName+":lock:*", 100).Iterator()
	for iter.Next(ctx) {
		rdb.Del(ctx, iter.Val())
	}

	cleanup := func() {
		// 清理测试数据
		repo.Clear(ctx, queueName)
		repo.Clear(ctx, queueName+":processing")
		// 使用 SCAN 删除所有相关的锁 key
		iter := rdb.Scan(ctx, 0, "wsc:test:queue:"+queueName+":lock:*", 100).Iterator()
		for iter.Next(ctx) {
			rdb.Del(ctx, iter.Val())
		}
		hub.Shutdown()
	}

	return repo, hub, queueName, cleanup
}

func TestMessageQueueBasicOperations(t *testing.T) {
	repo, hub, queueName, cleanup := setupMessageQueueTest(t)
	defer cleanup()

	ctx := context.Background()

	// 清理之前可能残留的数据
	repo.Clear(ctx, queueName)
	repo.Clear(ctx, queueName+":processing")
	repo.Clear(ctx, "empty-queue")

	t.Run("入队和出队", func(t *testing.T) {
		idGen := hub.GetIDGenerator()
		msg := &HubMessage{
			ID:          idGen.GenerateTraceID(),
			MessageID:   idGen.GenerateRequestID(),
			MessageType: MessageTypeText,
			Sender:      idGen.GenerateSpanID(),
			Receiver:    idGen.GenerateCorrelationID(),
			Content:     "Hello World",
			CreateAt:    time.Now(),
		}

		// 入队
		err := repo.Enqueue(ctx, queueName, msg)
		require.NoError(t, err)

		// 检查队列长度
		length, err := repo.GetLength(ctx, queueName)
		require.NoError(t, err)
		assert.Equal(t, int64(1), length, "入队后队列长度应为1")

		// 出队（增加超时时间）
		dequeued, err := repo.Dequeue(ctx, queueName, 3*time.Second)
		require.NoError(t, err)
		if !assert.NotNil(t, dequeued, "出队的消息不应为nil") {
			t.Logf("队列名: %s, 队列长度: %d", queueName, length)
			return
		}
		assert.Equal(t, msg.ID, dequeued.ID)
		assert.Equal(t, msg.Content, dequeued.Content)

		// 队列应该为空
		length, err = repo.GetLength(ctx, queueName)
		require.NoError(t, err)
		assert.Equal(t, int64(0), length)
	})

	t.Run("Peek操作", func(t *testing.T) {
		idGen := hub.GetIDGenerator()
		msg := &HubMessage{
			ID:       idGen.GenerateTraceID(),
			Content:  "Peek Test",
			CreateAt: time.Now(),
		}

		err := repo.Enqueue(ctx, queueName, msg)
		require.NoError(t, err)

		// Peek不会移除消息
		peeked, err := repo.Peek(ctx, queueName)
		require.NoError(t, err)
		require.NotNil(t, peeked)
		assert.Equal(t, msg.ID, peeked.ID)

		// 队列长度不变
		length, err := repo.GetLength(ctx, queueName)
		require.NoError(t, err)
		assert.Equal(t, int64(1), length)

		// 清理
		repo.Clear(ctx, queueName)
	})

	t.Run("空队列超时", func(t *testing.T) {
		start := time.Now()
		msg, err := repo.Dequeue(ctx, "empty-queue", 500*time.Millisecond)
		elapsed := time.Since(start)

		require.NoError(t, err)
		assert.Nil(t, msg)
		assert.True(t, elapsed >= 400*time.Millisecond, "应该等待超时")
	})
}

func TestMessageQueueClear(t *testing.T) {
	repo, hub, queueName, cleanup := setupMessageQueueTest(t)
	defer cleanup()

	ctx := context.Background()
	idGen := hub.GetIDGenerator()

	// 清理之前可能残留的数据
	repo.Clear(ctx, queueName)
	repo.Clear(ctx, queueName+":processing")

	// 入队多条消息
	for i := 0; i < 10; i++ {
		msg := &HubMessage{
			ID:      idGen.GenerateTraceID(),
			Content: fmt.Sprintf("Content %d", i),
		}
		err := repo.Enqueue(ctx, queueName, msg)
		require.NoError(t, err)
	}

	// 验证长度
	length, err := repo.GetLength(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(10), length)

	// 清空
	err = repo.Clear(ctx, queueName)
	require.NoError(t, err)

	// 验证已清空
	length, err = repo.GetLength(ctx, queueName)
	require.NoError(t, err)
	assert.Equal(t, int64(0), length)
}

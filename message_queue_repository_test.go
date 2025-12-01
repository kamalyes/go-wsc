/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 09:24:13
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

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupMessageQueueTest(t *testing.T) (*RedisMessageQueueRepository, func()) {
	// Redis配置
	rdb := redis.NewClient(&redis.Options{
		Addr:     "120.79.25.168:16389",
		Password: "M5Pi9YW6u",
		DB:       1,
	})

	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("Failed to connect to Redis: %v", err)
	}

	repo := NewRedisMessageQueueRepository(rdb, "wsc:test:queue:", 1*time.Hour)

	cleanup := func() {
		// 清理测试数据
		repo.Clear(ctx, "test")
		repo.Clear(ctx, "test:processing")
		rdb.Del(ctx, "wsc:test:queue:test:lock")
		rdb.Close()
	}

	return repo, cleanup
}

func TestMessageQueueBasicOperations(t *testing.T) {
	repo, cleanup := setupMessageQueueTest(t)
	defer cleanup()

	ctx := context.Background()

	// 清理之前可能残留的数据
	repo.Clear(ctx, "test")
	repo.Clear(ctx, "test:processing")
	repo.Clear(ctx, "empty-queue")

	t.Run("入队和出队", func(t *testing.T) {
		msg := &HubMessage{
			ID:          "msg-001",
			MessageID:   "test-001",
			MessageType: MessageTypeText,
			Sender:      "user-1",
			Receiver:    "user-2",
			Content:     "Hello World",
			CreateAt:    time.Now(),
		}

		// 入队
		err := repo.Enqueue(ctx, "test", msg)
		require.NoError(t, err)

		// 检查队列长度
		length, err := repo.GetLength(ctx, "test")
		require.NoError(t, err)
		assert.Equal(t, int64(1), length)

		// 出队
		dequeued, err := repo.Dequeue(ctx, "test", 1*time.Second)
		require.NoError(t, err)
		require.NotNil(t, dequeued)
		assert.Equal(t, msg.ID, dequeued.ID)
		assert.Equal(t, msg.Content, dequeued.Content)

		// 队列应该为空
		length, err = repo.GetLength(ctx, "test")
		require.NoError(t, err)
		assert.Equal(t, int64(0), length)
	})

	t.Run("Peek操作", func(t *testing.T) {
		msg := &HubMessage{
			ID:       "msg-002",
			Content:  "Peek Test",
			CreateAt: time.Now(),
		}

		err := repo.Enqueue(ctx, "test", msg)
		require.NoError(t, err)

		// Peek不会移除消息
		peeked, err := repo.Peek(ctx, "test")
		require.NoError(t, err)
		require.NotNil(t, peeked)
		assert.Equal(t, msg.ID, peeked.ID)

		// 队列长度不变
		length, err := repo.GetLength(ctx, "test")
		require.NoError(t, err)
		assert.Equal(t, int64(1), length)

		// 清理
		repo.Clear(ctx, "test")
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

func TestMessageQueueWatchdog(t *testing.T) {
	repo, cleanup := setupMessageQueueTest(t)
	defer cleanup()

	ctx := context.Background()

	// 清理之前可能残留的数据
	repo.Clear(ctx, "test")
	repo.Clear(ctx, "test:processing")

	t.Run("看门狗锁正常处理", func(t *testing.T) {
		msg := &HubMessage{
			ID:          "msg-watchdog-001",
			MessageID:   "wd-test-001",
			MessageType: MessageTypeText,
			Content:     "Watchdog Test",
			CreateAt:    time.Now(),
		}

		// 入队
		err := repo.Enqueue(ctx, "test", msg)
		require.NoError(t, err)

		// 使用看门狗处理
		processed := false
		err = repo.DequeueWithWatchdog(ctx, "test", 1*time.Second, func(m *HubMessage) error {
			assert.Equal(t, msg.ID, m.ID)
			assert.Equal(t, msg.Content, m.Content)
			processed = true
			// 模拟处理耗时
			time.Sleep(100 * time.Millisecond)
			return nil
		})

		require.NoError(t, err)
		assert.True(t, processed, "消息应该被处理")

		// 队列应该为空
		length, err := repo.GetLength(ctx, "test")
		require.NoError(t, err)
		assert.Equal(t, int64(0), length)

		// 处理队列也应该为空
		processingLength, err := repo.GetLength(ctx, "test:processing")
		require.NoError(t, err)
		assert.Equal(t, int64(0), processingLength)
	})

	t.Run("看门狗锁处理失败", func(t *testing.T) {
		msg := &HubMessage{
			ID:      "msg-watchdog-002",
			Content: "Watchdog Fail Test",
		}

		err := repo.Enqueue(ctx, "test", msg)
		require.NoError(t, err)

		// 处理失败
		err = repo.DequeueWithWatchdog(ctx, "test", 1*time.Second, func(m *HubMessage) error {
			return fmt.Errorf("processing failed")
		})

		require.Error(t, err)
		assert.Contains(t, err.Error(), "process message failed")

		// 消息应该从处理队列中移除
		processingLength, err := repo.GetLength(ctx, "test:processing")
		require.NoError(t, err)
		assert.Equal(t, int64(0), processingLength)
	})

	t.Run("看门狗锁自动续期", func(t *testing.T) {
		msg := &HubMessage{
			ID:      "msg-watchdog-003",
			Content: "Watchdog Renewal Test",
		}

		err := repo.Enqueue(ctx, "test", msg)
		require.NoError(t, err)

		// 处理时间超过锁的过期时间,测试续期
		err = repo.DequeueWithWatchdog(ctx, "test", 1*time.Second, func(m *HubMessage) error {
			// 处理15秒,超过lockExpiry(30s)的一半,会触发续期
			time.Sleep(15 * time.Second)
			return nil
		})

		require.NoError(t, err)

		// 验证处理成功
		length, err := repo.GetLength(ctx, "test")
		require.NoError(t, err)
		assert.Equal(t, int64(0), length)
	})
}

func TestMessageQueueConcurrency(t *testing.T) {
	repo, cleanup := setupMessageQueueTest(t)
	defer cleanup()

	ctx := context.Background()

	// 清理之前可能残留的数据
	repo.Clear(ctx, "test")
	repo.Clear(ctx, "test:processing")

	t.Run("并发入队出队", func(t *testing.T) {
		const numMessages = 100
		const numWorkers = 10

		// 入队100条消息
		for i := 0; i < numMessages; i++ {
			msg := &HubMessage{
				ID:      fmt.Sprintf("msg-%d", i),
				Content: fmt.Sprintf("Message %d", i),
			}
			err := repo.Enqueue(ctx, "test", msg)
			require.NoError(t, err)
		}

		// 验证队列长度
		length, err := repo.GetLength(ctx, "test")
		require.NoError(t, err)
		assert.Equal(t, int64(numMessages), length)

		// 并发出队
		done := make(chan bool, numWorkers)
		processed := make(map[string]bool)
		var processMutex = make(chan struct{}, 1)
		processMutex <- struct{}{} // 初始化锁

		for i := 0; i < numWorkers; i++ {
			go func(workerID int) {
				defer func() { done <- true }()

				for {
					err := repo.DequeueWithWatchdog(ctx, "test", 500*time.Millisecond, func(m *HubMessage) error {
						<-processMutex
						processed[m.ID] = true
						count := len(processed)
						processMutex <- struct{}{}

						// 如果所有消息都已处理,退出
						if count >= numMessages {
							return nil
						}
						return nil
					})

					// 检查是否所有消息都已处理
					<-processMutex
					count := len(processed)
					processMutex <- struct{}{}

					if count >= numMessages || err != nil {
						return
					}
				}
			}(i)
		}

		// 等待所有worker完成(最多30秒)
		timeout := time.After(30 * time.Second)
		workersCompleted := 0
		for workersCompleted < numWorkers {
			select {
			case <-done:
				workersCompleted++
			case <-timeout:
				t.Fatalf("超时:只有 %d/%d workers完成, 处理了 %d/%d 消息",
					workersCompleted, numWorkers, len(processed), numMessages)
			}
		}

		// 验证所有消息都被处理
		assert.Equal(t, numMessages, len(processed), "应该处理所有消息")

		// 队列应该为空
		length, err = repo.GetLength(ctx, "test")
		require.NoError(t, err)
		assert.Equal(t, int64(0), length)
	})
}

func TestMessageQueueClear(t *testing.T) {
	repo, cleanup := setupMessageQueueTest(t)
	defer cleanup()

	ctx := context.Background()

	// 清理之前可能残留的数据
	repo.Clear(ctx, "test")

	// 入队多条消息
	for i := 0; i < 10; i++ {
		msg := &HubMessage{
			ID:      fmt.Sprintf("msg-%d", i),
			Content: fmt.Sprintf("Content %d", i),
		}
		err := repo.Enqueue(ctx, "test", msg)
		require.NoError(t, err)
	}

	// 验证长度
	length, err := repo.GetLength(ctx, "test")
	require.NoError(t, err)
	assert.Equal(t, int64(10), length)

	// 清空
	err = repo.Clear(ctx, "test")
	require.NoError(t, err)

	// 验证已清空
	length, err = repo.GetLength(ctx, "test")
	require.NoError(t, err)
	assert.Equal(t, int64(0), length)
}

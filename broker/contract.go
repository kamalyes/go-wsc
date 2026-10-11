/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:51:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:51:00
 * @FilePath: \go-wsc\broker\contract.go
 * @Description: EventBus 可移植契约套件（TCK）- 每个适配器必须跑过
 *
 * 覆盖六组语义：queue 单消费者 / fanout 全量分发 / NACK 重投 /
 * Close 排空 / 消费幂等 / 同 user 保序
 *
 * 适配器接入方式（在适配器自己的测试文件里一行接入全套）：
 *
 *	func TestNatsEventBusContract(t *testing.T) {
 *	    broker.RunEventBusContract(t, func() spi.EventBus { return natsadapter.NewEventBus(...) })
 *	}
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// waitReceived 等待接收计数达标（适配器分发异步，轮询收敛）
func waitReceived(count *atomic.Int64, target int64, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count.Load() >= target {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return count.Load() >= target
}

// newContractEnvelope 构造契约测试信封（可靠档默认，保证幂等语义参与）
func newContractEnvelope(id, receiver string) *spi.DeliverEnvelope {
	return &spi.DeliverEnvelope{
		Intent:      spi.IntentDeliver,
		Publisher:   "contract-test",
		PublishedAt: time.Now(),
		Message: &models.HubMessage{
			MessageID: id,
			Receiver:  receiver,
			AppID:     "app1",
			Namespace: "ns1",
			Content:   fmt.Sprintf("content-%s", id),
		},
	}
}

// RunEventBusContract EventBus 契约全套（适配器测试文件接入执行）
func RunEventBusContract(t *testing.T, newBus func() spi.EventBus) {
	t.Run("queue_single_consumer", func(t *testing.T) {
		bus := newBus()
		defer func() { require.NoError(t, bus.Close()) }()

		var received atomic.Int64
		ctx := context.Background()
		handler := func(_ context.Context, _ *spi.DeliverEnvelope) error {
			received.Add(1)
			return nil
		}
		require.NoError(t, bus.Subscribe(ctx, spi.TopicDeliver, handler))
		require.NoError(t, bus.Subscribe(ctx, spi.TopicDeliver, handler))

		for i := 0; i < 20; i++ {
			require.NoError(t, bus.Publish(ctx, newContractEnvelope(fmt.Sprintf("q-%d", i), "u1")))
		}
		assert.True(t, waitReceived(&received, 20, 3*time.Second),
			"queue 语义：20 条消息每条恰好消费一次，got=%d", received.Load())
		assert.Equal(t, int64(20), received.Load(), "queue 语义不允许重复分发")
	})

	t.Run("fanout_all_consumers", func(t *testing.T) {
		bus := newBus()
		defer func() { require.NoError(t, bus.Close()) }()

		var a, b atomic.Int64
		ctx := context.Background()
		require.NoError(t, bus.Subscribe(ctx, spi.TopicBroadcast, func(_ context.Context, _ *spi.DeliverEnvelope) error {
			a.Add(1)
			return nil
		}))
		require.NoError(t, bus.Subscribe(ctx, spi.TopicBroadcast, func(_ context.Context, _ *spi.DeliverEnvelope) error {
			b.Add(1)
			return nil
		}))

		env := newContractEnvelope("f-1", "")
		env.Intent = spi.IntentBroadcast
		require.NoError(t, bus.Publish(ctx, env))

		assert.True(t, waitReceived(&a, 1, 3*time.Second), "fanout 语义：订阅者 A 各消费一份")
		assert.True(t, waitReceived(&b, 1, 3*time.Second), "fanout 语义：订阅者 B 各消费一份")
	})

	t.Run("nack_redelivery", func(t *testing.T) {
		bus := newBus()
		defer func() { require.NoError(t, bus.Close()) }()

		var attempts atomic.Int64
		ctx := context.Background()
		require.NoError(t, bus.Subscribe(ctx, spi.TopicDeliver, func(_ context.Context, _ *spi.DeliverEnvelope) error {
			attempts.Add(1)
			if attempts.Load() < 2 {
				return fmt.Errorf("simulated deliver failure")
			}
			return nil
		}))

		require.NoError(t, bus.Publish(ctx, newContractEnvelope("nack-1", "u1")))
		assert.True(t, waitReceived(&attempts, 2, 3*time.Second),
			"handler 返回 error 应触发重投，attempts=%d", attempts.Load())
	})

	t.Run("close_drains_inflight", func(t *testing.T) {
		bus := newBus()
		ctx := context.Background()

		release := make(chan struct{})
		var handled atomic.Int64
		require.NoError(t, bus.Subscribe(ctx, spi.TopicDeliver, func(_ context.Context, _ *spi.DeliverEnvelope) error {
			<-release // 模拟慢消费者，Close 时在途
			handled.Add(1)
			return nil
		}))

		require.NoError(t, bus.Publish(ctx, newContractEnvelope("drain-1", "u1")))
		time.Sleep(20 * time.Millisecond) // 确保分发已在途

		closed := make(chan error, 1)
		go func() { closed <- bus.Close() }()
		time.Sleep(20 * time.Millisecond)
		close(release) // 释放慢消费者，Close 应等待排空而非丢弃

		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(3 * time.Second):
			t.Fatal("Close 应等待在途消息排空后返回")
		}
		assert.Equal(t, int64(1), handled.Load(), "在途消息应在 Close 前完成分发")
	})

	t.Run("consumer_dedup_reliable", func(t *testing.T) {
		// 全链路：publisher → bus → Consumer（等级分流去重 → Deliver 收敛）
		bus := newBus()
		defer func() { require.NoError(t, bus.Close()) }()

		var delivered sync.Map // messageID → 次数
		deliver := func(_ context.Context, msg *models.HubMessage, _ bool) *models.DeliverResult {
			count, _ := delivered.LoadOrStore(msg.MessageID, new(int32))
			atomic.AddInt32(count.(*int32), 1)
			return &models.DeliverResult{}
		}
		consumer := NewConsumer(deliver, time.Minute, 8, nil)
		ctx := context.Background()
		require.NoError(t, bus.Subscribe(ctx, spi.TopicDeliver, consumer.HandleDeliver()))

		env := newContractEnvelope("dedup-1", "u1")
		require.NoError(t, bus.Publish(ctx, env))
		require.NoError(t, bus.Publish(ctx, env)) // 发布者超时重试：同 MessageID 重复发布

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if c, ok := delivered.Load("dedup-1"); ok && atomic.LoadInt32(c.(*int32)) >= 1 {
				time.Sleep(100 * time.Millisecond) // 留出第二次分发的观察窗口
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		count, _ := delivered.Load("dedup-1")
		require.NotNil(t, count, "首次投递应发生")
		assert.Equal(t, int32(1), atomic.LoadInt32(count.(*int32)),
			"可靠档重复信封应被本地去重窗拦截，Deliver 恰好一次")
	})

	t.Run("same_user_ordering", func(t *testing.T) {
		bus := newBus()
		defer func() { require.NoError(t, bus.Close()) }()

		var mu sync.Mutex
		var order []string
		ctx := context.Background()
		require.NoError(t, bus.Subscribe(ctx, spi.TopicDeliver, func(_ context.Context, env *spi.DeliverEnvelope) error {
			mu.Lock()
			order = append(order, env.Message.MessageID)
			mu.Unlock()
			return nil
		}))

		const total = 50
		for i := 0; i < total; i++ {
			require.NoError(t, bus.Publish(ctx, newContractEnvelope(fmt.Sprintf("ord-%03d", i), "u-order")))
		}

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			n := len(order)
			mu.Unlock()
			if n == total {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, order, total, "全部消息应送达")
		for i := 1; i < len(order); i++ {
			assert.Less(t, order[i-1], order[i],
				"同一 Receiver 的消息应保持发布顺序（got %v)", order)
		}
	})
}

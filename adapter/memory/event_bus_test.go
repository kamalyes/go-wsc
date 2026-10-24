/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:58:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:58:00
 * @FilePath: \go-wsc\adapter\memory\event_bus_test.go
 * @Description: 内存事件总线测试 - 接入可移植契约套件（TCK）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package memory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/broker"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// TestMemoryEventBusContract 全套契约：queue/fanout/NACK/Close/幂等/保序
func TestMemoryEventBusContract(t *testing.T) {
	broker.RunEventBusContract(t, func() spi.EventBus { return NewEventBus() })
}

func TestEventBus_PublishAfterClose(t *testing.T) {
	bus := NewEventBus()
	require.NoError(t, bus.Close())
	assert.ErrorIs(t, bus.Publish(context.Background(), &spi.DeliverEnvelope{}), ErrBusClosed)
}

func TestEventBus_SubscribeAfterClose(t *testing.T) {
	bus := NewEventBus()
	require.NoError(t, bus.Close())
	assert.ErrorIs(t, bus.Subscribe(context.Background(), spi.TopicDeliver, func(context.Context, *spi.DeliverEnvelope) error {
		return nil
	}), ErrBusClosed)
}

func TestEventBus_CloseIdempotent(t *testing.T) {
	bus := NewEventBus()
	require.NoError(t, bus.Close())
	assert.NoError(t, bus.Close(), "重复 Close 幂等")
}

func TestEventBus_NilArguments(t *testing.T) {
	bus := NewEventBus()
	defer func() { _ = bus.Close() }()

	assert.Error(t, bus.Publish(context.Background(), nil), "nil 信封拒绝")
	assert.Error(t, bus.Subscribe(context.Background(), spi.TopicDeliver, nil), "nil handler 拒绝")
}

func TestEventBus_NoSubscriberDrops(t *testing.T) {
	bus := NewEventBus()
	defer func() { _ = bus.Close() }()

	// 无订阅者发布：内存总线无持久化，等价 core NATS 无消费者语义（丢弃不报错）
	env := &spi.DeliverEnvelope{Intent: spi.IntentDeliver, Publisher: "p"}
	require.NoError(t, bus.Publish(context.Background(), env))
	assert.Equal(t, 0, bus.SubscriberCount(spi.TopicDeliver))
}

func TestEventBus_RedeliveryCap(t *testing.T) {
	// 毒消息（永远 NACK）：重投上限后丢弃，不无限循环
	bus := NewEventBus()
	defer func() { _ = bus.Close() }()

	delivered := make(chan struct{}, maxRedeliver+2)
	require.NoError(t, bus.Subscribe(context.Background(), spi.TopicDeliver, func(_ context.Context, _ *spi.DeliverEnvelope) error {
		delivered <- struct{}{}
		return assert.AnError // 永远失败
	}))

	env := &spi.DeliverEnvelope{
		Intent:    spi.IntentDeliver,
		Publisher: "p",
		Message: &models.HubMessage{
			MessageID: "poison",
			Receiver:  "u1",
			AppID:     "app1",
		},
	}
	require.NoError(t, bus.Publish(context.Background(), env))

	// 上限 = maxRedeliver+1 次尝试后静默丢弃
	assert.True(t, waitCount(delivered, maxRedeliver+1, 2*time.Second),
		"毒消息应重投至上限")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, maxRedeliver+1, len(drainCount(delivered)),
		"超限后不再重投（丢弃而非死循环）")
}

func TestEventBus_SubscriberCount(t *testing.T) {
	bus := NewEventBus()
	defer func() { _ = bus.Close() }()

	noop := func(context.Context, *spi.DeliverEnvelope) error { return nil }
	require.NoError(t, bus.Subscribe(context.Background(), spi.TopicDeliver, noop))
	require.NoError(t, bus.Subscribe(context.Background(), spi.TopicDeliver, noop))
	require.NoError(t, bus.Subscribe(context.Background(), spi.TopicBroadcast, noop))

	assert.Equal(t, 2, bus.SubscriberCount(spi.TopicDeliver))
	assert.Equal(t, 1, bus.SubscriberCount(spi.TopicBroadcast))
	assert.Equal(t, 0, bus.SubscriberCount(spi.TopicBroadcast+"-none"))
}

func TestEventBus_HandlerPanicIsolated(t *testing.T) {
	// 毒 handler panic 按 NACK 语义处理：不打崩分发 goroutine，
	// 也不阻断同信封 fanout 的健康订阅者（用例能跑完本身就是"进程未崩"的断言）
	bus := NewEventBus()
	defer func() { _ = bus.Close() }()

	require.NoError(t, bus.Subscribe(context.Background(), spi.TopicBroadcast, func(_ context.Context, _ *spi.DeliverEnvelope) error {
		panic("poison handler")
	}))
	healthy := make(chan struct{}, 2)
	require.NoError(t, bus.Subscribe(context.Background(), spi.TopicBroadcast, func(_ context.Context, _ *spi.DeliverEnvelope) error {
		healthy <- struct{}{}
		return nil
	}))

	for i := 0; i < 2; i++ {
		require.NoError(t, bus.Publish(context.Background(), &spi.DeliverEnvelope{
			Intent:    spi.IntentBroadcast,
			Publisher: "p",
			Message:   &models.HubMessage{MessageID: fmt.Sprintf("m%d", i)},
		}))
	}

	assert.True(t, waitCount(healthy, 2, 2*time.Second),
		"毒 handler panic 后健康订阅者仍应收到全部信封投递")
}

func TestEventBus_HandlerPanicQueueDropsAfterCap(t *testing.T) {
	// queue 语义：毒 handler panic 转错误进入重投链路，maxRedeliver+1 次尝试后静默丢弃
	bus := NewEventBus()
	defer func() { _ = bus.Close() }()

	attempts := make(chan struct{}, maxRedeliver+2)
	require.NoError(t, bus.Subscribe(context.Background(), spi.TopicDeliver, func(_ context.Context, _ *spi.DeliverEnvelope) error {
		attempts <- struct{}{}
		panic("poison handler")
	}))

	require.NoError(t, bus.Publish(context.Background(), &spi.DeliverEnvelope{
		Intent:    spi.IntentDeliver,
		Publisher: "p",
		Message:   &models.HubMessage{MessageID: "poison", Receiver: "u1"},
	}))

	assert.True(t, waitCount(attempts, maxRedeliver+1, 2*time.Second),
		"毒 handler 应重投至上限")
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, maxRedeliver+1, len(drainCount(attempts)),
		"超限后不再重投（panic 与返回 error 的毒 handler 同语义）")
}

// waitCount / drainCount 测试辅助
func waitCount(ch <-chan struct{}, target int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for len(ch) < target && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return len(ch) >= target
}

func drainCount(ch <-chan struct{}) []struct{} {
	var out []struct{}
	for {
		select {
		case <-ch:
			out = append(out, struct{}{})
		default:
			return out
		}
	}
}

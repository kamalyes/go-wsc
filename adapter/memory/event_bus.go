/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:35:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 23:08:00
 * @FilePath: \go-wsc\adapter\memory\event_bus.go
 * @Description: 内存事件总线 - EventBus 契约的进程内实现（契约测试基准）
 *
 * 语义映射（与 NATS 落地形态逐条对齐，契约套件因此可移植）：
 *   - TopicDeliver   → queue 语义：一条消息仅分发一个订阅者（round-robin）
 *   - TopicBroadcast → fanout 语义：每个订阅者各分发一份
 *   - 保序           → 每 topic 单分发 goroutine 串行投递（对齐 JetStream 分片内串行），
 *                      同 topic 发布序即分发序
 *   - NACK 重投      → handler 返回 error 时重试（上限后丢弃计数，等价 JetStream maxDeliver）
 *   - Close          → 拒绝新发布、等待在途分发排空、退出分发循环
 *
 * 用途：契约闭环（publisher → broker → Deliver 全链路单测）与基准测试；
 * 不用于生产（无持久化，进程崩溃即丢——生产选型见 adapter/nats）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/spi"
)

// 分发重投上限（等价 JetStream maxDeliver；超限丢弃并计数，避免毒消息死循环）
const maxRedeliver = 3

// dispatchQueuePerTopic 单 topic 分发队列深度（有界：背压传导给 Publish 调用方）
const dispatchQueueSize = 4096

// ErrBusClosed 总线已关闭（Publish/Subscribe 拒绝）
var ErrBusClosed = errors.New("memory event bus: closed")

// subscriber 单个订阅者（handler + 存活状态）
type subscriber struct {
	handler spi.EventHandler
}

// topicHub 单 topic 的订阅者集合、分发队列与分发 goroutine
type topicHub struct {
	mu       sync.Mutex
	subs     []*subscriber
	rrCursor int // queue 语义 round-robin 游标（仅分发 goroutine 触碰）

	// queue 分发队列：Close 不关闭它（向 closed channel 写会 panic），
	// 改由 stopCh 通知分发循环排空后退出；Publish 经 select 双路在
	// stop 关闭后停止入队
	queue     chan *spi.DeliverEnvelope
	stopCh    chan struct{}
	closeOnce sync.Once
}

// EventBus 内存事件总线（queue / fanout 双语义，topic 内严格保序）
type EventBus struct {
	mu     sync.Mutex
	topics map[spi.EventBusTopic]*topicHub
	wg     sync.WaitGroup
	closed bool
}

// NewEventBus 创建内存事件总线
func NewEventBus() *EventBus {
	return &EventBus{
		topics: make(map[spi.EventBusTopic]*topicHub),
	}
}

// Publish 发布投递指令（入队即返回，分发由 topic 专属 goroutine 串行执行）
//
// 信封按 Intent 路由 topic：IntentBroadcast → broadcast，其余 → deliver
// （与 NATS 适配器的 Publish 路由规则一致，信封语义跨驱动不变）
func (b *EventBus) Publish(ctx context.Context, env *spi.DeliverEnvelope) error {
	if env == nil {
		return errors.New("memory event bus: envelope is nil")
	}

	topic := spi.TopicDeliver
	if env.Intent == spi.IntentBroadcast {
		topic = spi.TopicBroadcast
	}

	hub := b.lockedTopicHub(topic)
	if hub == nil {
		return ErrBusClosed
	}
	// 入队与停止双路 select：stopCh 已关闭时不入队（Close 与在途 Publish 的竞态窗口内可能随机选中 queue 分支，残余消息随分发 循环退出被丢弃——非生产实现的纳秒级窗口，换取零 panic）
	select {
	case hub.queue <- env:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-hub.stopCh:
		return ErrBusClosed
	}
}

// Subscribe 订阅指定语义 topic（首条订阅拉起该 topic 的分发 goroutine）
func (b *EventBus) Subscribe(ctx context.Context, topic spi.EventBusTopic, handler spi.EventHandler) error {
	if handler == nil {
		return errors.New("memory event bus: handler is nil")
	}
	// 锁内取/建 hub 并检查关闭：Close 快照后新建 hub 不在快照内的
	// 泄漏路径已由 lockedTopicHub 的 closed 检查封死（不会出现永不停止的分发 goroutine）
	hub := b.lockedTopicHub(topic)
	if hub == nil {
		return ErrBusClosed
	}

	hub.mu.Lock()
	first := len(hub.subs) == 0
	hub.subs = append(hub.subs, &subscriber{handler: handler})
	hub.mu.Unlock()

	if first {
		b.wg.Add(1)
		go func() {
			defer b.wg.Done()
			b.dispatchLoop(ctx, hub)
		}()
	}
	return nil
}

// Close 优雅关闭：拒绝新消息、等待在途分发排空
//
// 不关闭 queue（向 closed channel 写会 panic），改经 stopCh 通知各
// 分发循环排空后自行退出
func (b *EventBus) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	hubs := make([]*topicHub, 0, len(b.topics))
	for _, hub := range b.topics {
		hubs = append(hubs, hub)
	}
	b.mu.Unlock()

	for _, hub := range hubs {
		hub.closeOnce.Do(func() { close(hub.stopCh) })
	}
	b.wg.Wait()
	return nil
}

// dispatchLoop topic 分发主循环（单 goroutine 串行：保序的分派点）
func (b *EventBus) dispatchLoop(ctx context.Context, hub *topicHub) {
	for {
		select {
		case env := <-hub.queue:
			if ctx.Err() != nil {
				// ctx 取消后仍需排空队列（Close 语义：等停止信号排空）
				// 但不再执行业务 handler，直接跳过剩余消息
				continue
			}
			b.dispatch(ctx, hub, env)
		case <-hub.stopCh:
			// 停止信号：非阻塞排空剩余消息后退出
			for {
				select {
				case env := <-hub.queue:
					if ctx.Err() != nil {
						continue
					}
					b.dispatch(ctx, hub, env)
				default:
					return
				}
			}
		}
	}
}

// dispatch 单条信封分发（queue 单订阅者 / fanout 全量，NACK 重投带上限）
func (b *EventBus) dispatch(ctx context.Context, hub *topicHub, env *spi.DeliverEnvelope) {
	hub.mu.Lock()
	subs := make([]*subscriber, len(hub.subs))
	copy(subs, hub.subs)
	hub.mu.Unlock()

	if len(subs) == 0 {
		return // 无订阅者：丢弃（等价 core NATS 无消费者语义）
	}

	if env.Intent == spi.IntentBroadcast {
		// fanout 语义：每个订阅者各消费一份
		for _, sub := range subs {
			b.deliverWithRetry(ctx, sub, env)
		}
		return
	}

	// queue 语义：round-robin 选一个订阅者独占消费（分发 goroutine 单线程触碰游标）
	hub.mu.Lock()
	idx := hub.rrCursor % len(hub.subs)
	hub.rrCursor++
	target := hub.subs[idx]
	hub.mu.Unlock()
	b.deliverWithRetry(ctx, target, env)
}

// deliverWithRetry NACK 重投（maxRedeliver 上限，超限丢弃）
func (b *EventBus) deliverWithRetry(ctx context.Context, sub *subscriber, env *spi.DeliverEnvelope) {
	for attempt := 0; attempt <= maxRedeliver; attempt++ {
		if err := sub.invoke(ctx, env); err == nil {
			return
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// invoke panic 安全地调用订阅 handler
//
// handler 为业务注入实现，panic 转为 error 返回——与返回 error 的 handler 同语义，
// 进入既有 NACK 重投-上限丢弃链路：毒 handler 不打崩分发 goroutine，
// 也不阻断同信封其他订阅者（fanout 逐订阅者独立投递）
func (s *subscriber) invoke(ctx context.Context, env *spi.DeliverEnvelope) (err error) {
	defer syncx.RecoverWithHandler(func(p interface{}) {
		spi.NewDefaultLogger().ErrorContextKV(ctx, "事件总线分发 handler panic，按 NACK 处理",
			"intent", env.Intent, "panic", p)
		err = fmt.Errorf("handler panicked: %v", p)
	})
	return s.handler(ctx, env)
}

// lockedTopicHub 锁内取或建 topic hub；总线已关闭返回 nil（调用方拒绝）
//
// 关闭检查与取/建同锁：Close 的 hubs 快照发生在同一把锁的另一侧，快照后不可能再新建 hub（杜绝"新建 hub 不在快照内、分发循环永不停止"的泄漏路径）
func (b *EventBus) lockedTopicHub(topic spi.EventBusTopic) *topicHub {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	if hub, ok := b.topics[topic]; ok {
		return hub
	}
	hub := &topicHub{
		queue:  make(chan *spi.DeliverEnvelope, dispatchQueueSize),
		stopCh: make(chan struct{}),
	}
	b.topics[topic] = hub
	return hub
}

// SubscriberCount 订阅者计数（契约测试断言用）
func (b *EventBus) SubscriberCount(topic spi.EventBusTopic) int {
	b.mu.Lock()
	hub, ok := b.topics[topic]
	b.mu.Unlock()
	if !ok {
		return 0
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return len(hub.subs)
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-31 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-06 21:26:29
 * @FilePath: \go-wsc\hub\message_batcher.go
 * @Description: 消息批处理器
 *   按用户聚合短时间内的多条消息，批量发送
 *   减少高频场景下的 per-message 开销（序列化、锁获取、网络调用）
 *
 * 工作机制：
 *   1. Submit(msg) 将消息按 receiver 分组缓冲
 *   2. 满足以下任一条件时触发 flush：
 *      a. 缓冲消息数达到 MaxBatchSize
 *      b. 定时器到达（FlushInterval）
 *   3. flush 时按用户并发发送，每用户的消息合并发送
 *
 * 适用场景：
 *   - 高频消息推送（如批量通知、群消息分发）
 *   - 同一用户短时间内收到多条消息
 *
 * 不适用场景：
 *   - 低频消息（批处理增加延迟无收益）
 *   - 需要严格顺序的消息（批处理可能打乱顺序）
 */

package hub

import (
	"context"
	"sync"
	"time"
)

// MessageBatcherConfig 批处理器配置
type MessageBatcherConfig struct {
	// FlushInterval flush 间隔（默认 5ms）
	// 越短延迟越低但批效果越差，越长批效果越好但延迟越高
	FlushInterval time.Duration
	// MaxBatchSize 单批次最大消息数（默认 256）
	// 达到此数量立即 flush，不等定时器
	MaxBatchSize int
	// MaxBatchSizePerUser 单用户单批次最大消息数（默认 32）
	MaxBatchSizePerUser int
}

// DefaultMessageBatcherConfig 默认批处理器配置
func DefaultMessageBatcherConfig() *MessageBatcherConfig {
	return &MessageBatcherConfig{
		FlushInterval:       5 * time.Millisecond,
		MaxBatchSize:        256,
		MaxBatchSizePerUser: 32,
	}
}

// MessageBatcher 消息批处理器
// 线程安全，可被多个 goroutine 同时 Submit
type MessageBatcher struct {
	cfg *MessageBatcherConfig

	// 缓冲区（按 receiver 分组）
	mu     sync.Mutex
	buffer map[string][]*HubMessage
	count  int

	// flush 回调（由 Hub 注入，通常调用 sendToUserDirect）
	flushHandler func(ctx context.Context, receiver string, messages []*HubMessage)

	// 定时器
	ticker *time.Ticker
	stopCh chan struct{}

	// 状态
	started bool
	stopped bool
}

// NewMessageBatcher 创建消息批处理器
// flushHandler: 由 Hub 注入的发送函数，批处理 flush 时调用
func NewMessageBatcher(cfg *MessageBatcherConfig, flushHandler func(ctx context.Context, receiver string, messages []*HubMessage)) *MessageBatcher {
	if cfg == nil {
		cfg = DefaultMessageBatcherConfig()
	}

	return &MessageBatcher{
		cfg:          cfg,
		buffer:       make(map[string][]*HubMessage),
		flushHandler: flushHandler,
		stopCh:       make(chan struct{}),
	}
}

// Start 启动批处理器（开始定时 flush）
func (b *MessageBatcher) Start() {
	if b.started {
		return
	}
	b.started = true
	b.ticker = time.NewTicker(b.cfg.FlushInterval)

	go b.flushLoop()
}

// Stop 停止批处理器（flush 剩余消息后退出）
func (b *MessageBatcher) Stop() {
	if !b.started || b.stopped {
		return
	}
	b.stopped = true
	close(b.stopCh)
	if b.ticker != nil {
		b.ticker.Stop()
	}
	// flush 剩余消息
	b.flush()
}

// Submit 提交消息到批处理器
// 如果缓冲区满，立即触发 flush
func (b *MessageBatcher) Submit(ctx context.Context, msg *HubMessage) {
	if msg == nil || b.flushHandler == nil {
		return
	}

	receiver := msg.Receiver
	if receiver == "" {
		return
	}

	b.mu.Lock()
	b.buffer[receiver] = append(b.buffer[receiver], msg)
	b.count++
	shouldFlush := b.count >= b.cfg.MaxBatchSize ||
		len(b.buffer[receiver]) >= b.cfg.MaxBatchSizePerUser
	b.mu.Unlock()

	if shouldFlush {
		b.flush()
	}
}

// flush 执行批量发送
// 将缓冲区的消息按用户分组，通过 flushHandler 并发发送
func (b *MessageBatcher) flush() {
	b.mu.Lock()
	if b.count == 0 {
		b.mu.Unlock()
		return
	}

	// 交换缓冲区（快速释放锁）
	buffer := b.buffer
	b.buffer = make(map[string][]*HubMessage)
	b.count = 0
	b.mu.Unlock()

	// 按用户并发发送（使用 sync.WaitGroup 等待完成）
	ctx := context.Background()
	var wg sync.WaitGroup

	for receiver, messages := range buffer {
		wg.Add(1)
		go func(recv string, msgs []*HubMessage) {
			defer wg.Done()
			b.flushHandler(ctx, recv, msgs)
		}(receiver, messages)
	}

	wg.Wait()
}

// flushLoop 定时 flush 循环
func (b *MessageBatcher) flushLoop() {
	for {
		select {
		case <-b.stopCh:
			return
		case <-b.ticker.C:
			b.flush()
		}
	}
}

// PendingCount 获取当前缓冲区消息数
func (b *MessageBatcher) PendingCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.count
}

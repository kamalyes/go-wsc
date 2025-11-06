/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-07 00:55:05
 * @FilePath: \go-wsc\dynamic_queue.go
 * @Description: 动态扩容/缩容的高性能消息队列
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"sync"
	"sync/atomic"
)

// DynamicQueue 动态扩容的消息队列
type DynamicQueue struct {
	items         []*wsMsg      // 消息数组
	mu            sync.RWMutex  // 读写锁
	notEmpty      *sync.Cond    // 非空条件变量
	head          int           // 队列头部索引
	tail          int           // 队列尾部索引
	count         int64         // 当前消息数（原子）
	capacity      int           // 当前容量
	minCapacity   int           // 最小容量
	maxCapacity   int           // 最大容量
	closed        int32         // 关闭标记（原子）
	autoResize    bool          // 是否自动调整容量
	resizeCount   int64         // 扩容次数（用于统计）
	shrinkCount   int64         // 缩容次数（用于统计）
	growthFactor  float64       // 增长因子（默认1.5）
}

// NewDynamicQueue 创建动态队列
// minCap: 最小容量，maxCap: 最大容量
func NewDynamicQueue(minCap, maxCap int) *DynamicQueue {
	if minCap <= 0 {
		minCap = 256
	}
	if maxCap <= 0 {
		maxCap = 100000
	}
	if minCap > maxCap {
		minCap = maxCap
	}

	q := &DynamicQueue{
		items:        make([]*wsMsg, minCap),
		head:         0,
		tail:         0,
		count:        0,
		capacity:     minCap,
		minCapacity:  minCap,
		maxCapacity:  maxCap,
		closed:       0,
		autoResize:   true,
		resizeCount:  0,
		shrinkCount:  0,
		growthFactor: 1.5, // 使用1.5倍增长，比2倍更温和
	}
	q.notEmpty = sync.NewCond(&q.mu)
	return q
}

// Push 将消息推入队列，如果队列满则自动扩容
func (q *DynamicQueue) Push(msg *wsMsg) error {
	if atomic.LoadInt32(&q.closed) == 1 {
		return ErrClose
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	// 检查是否需要扩容
	if q.isFull() {
		if q.autoResize && q.capacity < q.maxCapacity {
			newCap := q.calculateGrowth()
			q.resize(newCap)
		} else {
			return ErrBufferFull
		}
	}

	// 添加消息
	q.items[q.tail] = msg
	q.tail = (q.tail + 1) % q.capacity
	atomic.AddInt64(&q.count, 1)

	// 通知等待的消费者
	q.notEmpty.Signal()
	return nil
}

// Pop 从队列中取出消息，如果队列为空则阻塞等待
func (q *DynamicQueue) Pop() (*wsMsg, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// 等待队列非空或关闭
	for q.isEmpty() && atomic.LoadInt32(&q.closed) == 0 {
		q.notEmpty.Wait()
	}

	// 队列已关闭且为空
	if q.isEmpty() && atomic.LoadInt32(&q.closed) == 1 {
		return nil, ErrClose
	}

	// 取出消息
	msg := q.items[q.head]
	q.items[q.head] = nil // 释放引用，帮助GC
	q.head = (q.head + 1) % q.capacity
	atomic.AddInt64(&q.count, -1)

	// 检查是否需要缩容
	if q.autoResize && q.shouldShrink() {
		newCap := q.calculateShrink()
		q.resize(newCap)
	}

	return msg, nil
}

// TryPop 尝试从队列中取出消息，如果队列为空立即返回
func (q *DynamicQueue) TryPop() (*wsMsg, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.isEmpty() {
		return nil, false
	}

	msg := q.items[q.head]
	q.items[q.head] = nil
	q.head = (q.head + 1) % q.capacity
	atomic.AddInt64(&q.count, -1)

	// 检查是否需要缩容
	if q.autoResize && q.shouldShrink() {
		newCap := q.calculateShrink()
		q.resize(newCap)
	}

	return msg, true
}

// Close 关闭队列
func (q *DynamicQueue) Close() {
	if atomic.CompareAndSwapInt32(&q.closed, 0, 1) {
		q.mu.Lock()
		q.notEmpty.Broadcast() // 唤醒所有等待的消费者
		q.mu.Unlock()
	}
}

// Len 返回当前队列长度
func (q *DynamicQueue) Len() int {
	return int(atomic.LoadInt64(&q.count))
}

// Cap 返回当前队列容量
func (q *DynamicQueue) Cap() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.capacity
}

// IsClosed 检查队列是否已关闭
func (q *DynamicQueue) IsClosed() bool {
	return atomic.LoadInt32(&q.closed) == 1
}

// isEmpty 检查队列是否为空（需要持有锁）
func (q *DynamicQueue) isEmpty() bool {
	return atomic.LoadInt64(&q.count) == 0
}

// isFull 检查队列是否已满（需要持有锁）
func (q *DynamicQueue) isFull() bool {
	return atomic.LoadInt64(&q.count) >= int64(q.capacity)
}

// shouldShrink 判断是否应该缩容
// 当使用率低于25%且容量大于最小容量时缩容
func (q *DynamicQueue) shouldShrink() bool {
	count := atomic.LoadInt64(&q.count)
	return q.capacity > q.minCapacity &&
		count < int64(q.capacity/4)
}

// calculateGrowth 计算新的容量（智能增长策略）
// 采用渐进式增长：小容量时快速增长，大容量时缓慢增长
func (q *DynamicQueue) calculateGrowth() int {
	currentCap := q.capacity
	var newCap int
	
	// 策略1: 容量小于1024时，使用2倍增长
	if currentCap < 1024 {
		newCap = currentCap * 2
	} else if currentCap < 10000 {
		// 策略2: 容量在1024-10000之间，使用1.5倍增长
		newCap = int(float64(currentCap) * q.growthFactor)
	} else {
		// 策略3: 容量大于10000时，使用固定增量（避免过大增长）
		// 每次增加当前容量的25%，但不超过10000
		increment := currentCap / 4
		if increment > 10000 {
			increment = 10000
		}
		newCap = currentCap + increment
	}
	
	// 确保增长倍数不超过2倍（避免突然的大幅增长）
	maxAllowed := currentCap * 2
	if newCap > maxAllowed {
		newCap = maxAllowed
	}
	
	// 确保不超过最大容量
	if newCap > q.maxCapacity {
		newCap = q.maxCapacity
	}
	
	return newCap
}

// calculateShrink 计算缩容后的新容量
func (q *DynamicQueue) calculateShrink() int {
	currentCap := q.capacity
	
	// 缩容为当前容量的2/3，更温和
	newCap := currentCap * 2 / 3
	
	// 确保至少保留当前消息数量的2倍空间
	minRequired := int(atomic.LoadInt64(&q.count)) * 2
	if newCap < minRequired {
		newCap = minRequired
	}
	
	return newCap
}

// resize 调整队列容量（需要持有锁）
func (q *DynamicQueue) resize(newCap int) {
	oldCap := q.capacity
	
	// 限制在最小和最大容量之间
	if newCap < q.minCapacity {
		newCap = q.minCapacity
	}
	if newCap > q.maxCapacity {
		newCap = q.maxCapacity
	}

	if newCap == q.capacity {
		return
	}

	// 创建新数组
	newItems := make([]*wsMsg, newCap)

	// 复制现有消息
	count := int(atomic.LoadInt64(&q.count))
	for i := 0; i < count; i++ {
		newItems[i] = q.items[(q.head+i)%q.capacity]
	}

	// 更新队列状态
	q.items = newItems
	q.head = 0
	q.tail = count
	q.capacity = newCap
	
	// 统计扩容/缩容次数
	if newCap > oldCap {
		atomic.AddInt64(&q.resizeCount, 1)
	} else if newCap < oldCap {
		atomic.AddInt64(&q.shrinkCount, 1)
	}
}

// SetAutoResize 设置是否自动调整容量
func (q *DynamicQueue) SetAutoResize(enabled bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.autoResize = enabled
}

// Stats 返回队列统计信息
func (q *DynamicQueue) Stats() map[string]interface{} {
	q.mu.RLock()
	defer q.mu.RUnlock()

	count := atomic.LoadInt64(&q.count)
	return map[string]interface{}{
		"length":       count,
		"capacity":     q.capacity,
		"minCapacity":  q.minCapacity,
		"maxCapacity":  q.maxCapacity,
		"utilization":  float64(count) / float64(q.capacity) * 100,
		"autoResize":   q.autoResize,
		"closed":       atomic.LoadInt32(&q.closed) == 1,
		"resizeCount":  atomic.LoadInt64(&q.resizeCount),
		"shrinkCount":  atomic.LoadInt64(&q.shrinkCount),
		"growthFactor": q.growthFactor,
	}
}

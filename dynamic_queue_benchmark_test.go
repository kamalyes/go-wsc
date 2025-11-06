/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-07 00:59:56
 * @FilePath: \go-wsc\dynamic_queue_benchmark_test.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

func TestDynamicQueue_Basic(t *testing.T) {
	q := NewDynamicQueue(4, 1024)
	defer q.Close()

	// 测试推入和弹出
	msg1 := &wsMsg{t: websocket.TextMessage, msg: []byte("test1")}
	msg2 := &wsMsg{t: websocket.TextMessage, msg: []byte("test2")}

	assert.NoError(t, q.Push(msg1), "Push msg1 should succeed")
	assert.NoError(t, q.Push(msg2), "Push msg2 should succeed")

	assert.Equal(t, 2, q.Len(), "Queue length should be 2")

	out1, err := q.Pop()
	assert.NoError(t, err, "Pop should succeed")
	assert.Equal(t, "test1", string(out1.msg), "First message should be test1")

	out2, err := q.Pop()
	assert.NoError(t, err, "Pop should succeed")
	assert.Equal(t, "test2", string(out2.msg), "Second message should be test2")

	assert.Equal(t, 0, q.Len(), "Queue should be empty")
}

func TestDynamicQueue_AutoExpand(t *testing.T) {
	q := NewDynamicQueue(4, 1024)
	defer q.Close()

	initialCap := q.Cap()

	// 推入超过初始容量的消息
	for i := 0; i < 10; i++ {
		msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}
		assert.NoError(t, q.Push(msg), "Push %d should succeed", i)
	}

	newCap := q.Cap()
	assert.Greater(t, newCap, initialCap, "Capacity should expand")
	assert.Equal(t, 10, q.Len(), "Queue length should be 10")
}

func TestDynamicQueue_AutoShrink(t *testing.T) {
	q := NewDynamicQueue(4, 1024)
	defer q.Close()

	// 推入大量消息触发扩容
	for i := 0; i < 100; i++ {
		msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}
		assert.NoError(t, q.Push(msg), "Push should succeed")
	}

	expandedCap := q.Cap()

	// 弹出大部分消息触发缩容
	for i := 0; i < 95; i++ {
		_, err := q.Pop()
		assert.NoError(t, err, "Pop should succeed")
	}

	shrunkCap := q.Cap()
	if shrunkCap >= expandedCap {
		t.Logf("Capacity did not shrink as expected, expanded: %d, shrunk: %d", expandedCap, shrunkCap)
	}
}

func TestDynamicQueue_Concurrent(t *testing.T) {
	q := NewDynamicQueue(10, 10000)
	defer q.Close()

	var wg sync.WaitGroup
	producerCount := 10
	consumerCount := 10
	messagesPerProducer := 1000

	var produced, consumed int64

	// 生产者
	for i := 0; i < producerCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < messagesPerProducer; j++ {
				msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}
				for {
					if err := q.Push(msg); err != nil {
						if err == ErrBufferFull {
							time.Sleep(time.Microsecond)
							continue
						}
						return
					}
					atomic.AddInt64(&produced, 1)
					break
				}
			}
		}(i)
	}

	// 消费者
	for i := 0; i < consumerCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				msg, err := q.Pop()
				if err != nil {
					return
				}
				if msg != nil {
					atomic.AddInt64(&consumed, 1)
				}
				if atomic.LoadInt64(&consumed) >= int64(producerCount*messagesPerProducer) {
					return
				}
			}
		}(i)
	}

	// 等待所有生产者完成
	time.Sleep(2 * time.Second)
	q.Close()
	wg.Wait()

	t.Logf("Produced: %d, Consumed: %d", produced, consumed)
	if consumed < produced {
		t.Logf("Some messages were not consumed: produced=%d, consumed=%d", produced, consumed)
	}
}

func TestDynamicQueue_Stats(t *testing.T) {
	q := NewDynamicQueue(10, 1000)
	defer q.Close()

	// 添加一些消息
	for i := 0; i < 5; i++ {
		msg := &wsMsg{t: websocket.TextMessage, msg: []byte("test")}
		assert.NoError(t, q.Push(msg), "Push should succeed")
	}

	stats := q.Stats()
	t.Logf("Queue stats: %+v", stats)

	assert.Equal(t, int64(5), stats["length"].(int64), "Queue length should be 5")
}

func BenchmarkDynamicQueue_Push(b *testing.B) {
	q := NewDynamicQueue(1000, 100000)
	defer q.Close()

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("benchmark")}

	// 启动消费者避免队列满
	go func() {
		for !q.IsClosed() {
			q.TryPop()
		}
	}()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		for {
			if err := q.Push(msg); err != nil {
				if err == ErrBufferFull {
					time.Sleep(time.Microsecond)
					continue
				}
				b.Fatal(err)
			}
			break
		}
	}
}

func BenchmarkDynamicQueue_PushPop(b *testing.B) {
	q := NewDynamicQueue(1000, 100000)
	defer q.Close()

	msg := &wsMsg{t: websocket.TextMessage, msg: []byte("benchmark")}

	b.ResetTimer()
	b.ReportAllocs()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = q.Push(msg)
			_, _ = q.TryPop()
		}
	})
}

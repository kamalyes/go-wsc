/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:02:15
 * @FilePath: \go-wsc\hub\worker_pool_test.go
 * @Description: Hub WorkerPool 封装白盒单元测试（覆盖 hub/worker_pool.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// newTestWorkerPool 构造指定 workers/queueSize 的测试工作池
func newTestWorkerPool(t *testing.T, msgWorkers, msgQueue, cbWorkers, cbQueue, recWorkers, recQueue, distWorkers, distQueue int) *HubWorkerPool {
	t.Helper()
	cfg := &wscconfig.WorkerPoolConfig{
		MessageWorkers:       msgWorkers,
		MessageQueueSize:     msgQueue,
		CallbackWorkers:      cbWorkers,
		CallbackQueueSize:    cbQueue,
		RecordWorkers:        recWorkers,
		RecordQueueSize:      recQueue,
		DistributedWorkers:   distWorkers,
		DistributedQueueSize: distQueue,
	}
	return NewHubWorkerPool(cfg, NewHub(wscconfig.Default()).GetLogger())
}

// TestNewHubWorkerPool 验证工作池构造
func TestNewHubWorkerPool(t *testing.T) {
	wp := newTestWorkerPool(t, 2, 4, 2, 4, 2, 4, 2, 4)
	defer wp.Stop()

	assert.NotNil(t, wp.MessagePool)
	assert.NotNil(t, wp.CallbackPool)
	assert.NotNil(t, wp.RecordPool)
	assert.NotNil(t, wp.DistributedPool)
}

// TestHubWorkerPool_SubmitMessage 验证消息池提交（阻塞式）
func TestHubWorkerPool_SubmitMessage(t *testing.T) {
	wp := newTestWorkerPool(t, 2, 4, 1, 1, 1, 1, 1, 1)
	defer wp.Stop()

	var executed int32
	wp.SubmitMessage(context.Background(), func() {
		atomic.AddInt32(&executed, 1)
	})

	require.Eventually(t, func() bool { return atomic.LoadInt32(&executed) == 1 }, time.Second, 10*time.Millisecond)
}

// TestHubWorkerPool_TrySubmitMessage 验证消息池非阻塞提交
func TestHubWorkerPool_TrySubmitMessage(t *testing.T) {
	wp := newTestWorkerPool(t, 2, 4, 1, 1, 1, 1, 1, 1)
	defer wp.Stop()

	var executed int32
	assert.True(t, wp.TrySubmitMessage(func() { atomic.AddInt32(&executed, 1) }))
	require.Eventually(t, func() bool { return atomic.LoadInt32(&executed) == 1 }, time.Second, 10*time.Millisecond)
}

// TestHubWorkerPool_TrySubmitMessage_Rejected 验证消息队列满时 TrySubmit 返回 false
func TestHubWorkerPool_TrySubmitMessage_Rejected(t *testing.T) {
	// workers=1, queueSize=1：一个阻塞任务占用 worker，再填满 queue，第三次被拒
	wp := newTestWorkerPool(t, 1, 1, 1, 1, 1, 1, 1, 1)
	defer wp.Stop()

	release := make(chan struct{})
	started := make(chan struct{})
	// 占用唯一 worker（用 started channel 确定性等待 worker 取走任务，不依赖 sleep）
	wp.SubmitMessage(context.Background(), func() {
		close(started) // 信号：worker 已开始执行
		<-release      // 阻塞占用 worker
	})
	<-started // 确定性等待 worker 确认已取走任务

	// 填满 queue（queueSize=1）
	assert.True(t, wp.TrySubmitMessage(func() {}))
	// 队列满，拒绝
	assert.False(t, wp.TrySubmitMessage(func() {}))

	close(release)
}

// TestHubWorkerPool_SubmitCallback 验证回调池提交
func TestHubWorkerPool_SubmitCallback(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 1, 2, 4, 1, 1, 1, 1)
	defer wp.Stop()

	var executed int32
	wp.SubmitCallback(context.Background(), func() { atomic.AddInt32(&executed, 1) })
	require.Eventually(t, func() bool { return atomic.LoadInt32(&executed) == 1 }, time.Second, 10*time.Millisecond)
}

// TestHubWorkerPool_TrySubmitCallback_Rejected 验证回调队列满时被拒
func TestHubWorkerPool_TrySubmitCallback_Rejected(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 1, 1, 1, 1, 1, 1, 1)
	defer wp.Stop()

	release := make(chan struct{})
	started := make(chan struct{})
	wp.SubmitCallback(context.Background(), func() {
		close(started) // 信号：worker 已开始执行
		<-release      // 阻塞占用 worker
	})
	<-started // 确定性等待 worker 确认已取走任务

	assert.True(t, wp.TrySubmitCallback(func() {}))
	assert.False(t, wp.TrySubmitCallback(func() {}))

	close(release)
}

// TestHubWorkerPool_SubmitRecord 验证记录池提交
func TestHubWorkerPool_SubmitRecord(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 1, 1, 1, 2, 4, 1, 1)
	defer wp.Stop()

	var executed int32
	wp.SubmitRecord(context.Background(), func() { atomic.AddInt32(&executed, 1) })
	require.Eventually(t, func() bool { return atomic.LoadInt32(&executed) == 1 }, time.Second, 10*time.Millisecond)
}

// TestHubWorkerPool_TrySubmitRecord_Rejected 验证记录队列满时被丢弃（返回 false）
func TestHubWorkerPool_TrySubmitRecord_Rejected(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 1, 1, 1, 1, 1, 1, 1)
	defer wp.Stop()

	release := make(chan struct{})
	wp.SubmitRecord(context.Background(), func() { <-release })
	time.Sleep(50 * time.Millisecond)

	assert.True(t, wp.TrySubmitRecord(func() {}))
	assert.False(t, wp.TrySubmitRecord(func() {}))

	close(release)
}

// TestHubWorkerPool_SubmitDistributed 验证跨节点池提交
func TestHubWorkerPool_SubmitDistributed(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 1, 1, 1, 1, 1, 2, 4)
	defer wp.Stop()

	var executed int32
	wp.SubmitDistributed(context.Background(), func() { atomic.AddInt32(&executed, 1) })
	require.Eventually(t, func() bool { return atomic.LoadInt32(&executed) == 1 }, time.Second, 10*time.Millisecond)
}

// TestHubWorkerPool_TrySubmitDistributed_Rejected 验证跨节点队列满时被拒
func TestHubWorkerPool_TrySubmitDistributed_Rejected(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 1, 1, 1, 1, 1, 1, 1)
	defer wp.Stop()

	release := make(chan struct{})
	wp.SubmitDistributed(context.Background(), func() { <-release })
	time.Sleep(50 * time.Millisecond)

	assert.True(t, wp.TrySubmitDistributed(func() {}))
	assert.False(t, wp.TrySubmitDistributed(func() {}))

	close(release)
}

// TestHubWorkerPool_Stats 验证各池队列长度统计
func TestHubWorkerPool_Stats(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 2, 1, 2, 1, 2, 1, 2)
	defer wp.Stop()

	stats := wp.Stats()
	assert.GreaterOrEqual(t, stats.MessageQueueLen, 0)
	assert.GreaterOrEqual(t, stats.CallbackQueueLen, 0)
	assert.GreaterOrEqual(t, stats.RecordQueueLen, 0)
	assert.GreaterOrEqual(t, stats.DistributedQueueLen, 0)
}

// TestHubWorkerPool_Stats_WithPending 验证统计能反映排队中的任务
func TestHubWorkerPool_Stats_WithPending(t *testing.T) {
	wp := newTestWorkerPool(t, 1, 4, 1, 4, 1, 4, 1, 4)
	defer wp.Stop()

	var wg sync.WaitGroup
	release := make(chan struct{})

	// 占用 message worker
	wg.Add(1)
	wp.SubmitMessage(context.Background(), func() {
		defer wg.Done()
		<-release
	})
	time.Sleep(50 * time.Millisecond)

	// 排入 2 个待处理任务
	wp.TrySubmitMessage(func() {})
	wp.TrySubmitMessage(func() {})

	stats := wp.Stats()
	assert.GreaterOrEqual(t, stats.MessageQueueLen, 1, "应有任务排队")

	close(release)
	wg.Wait()
}

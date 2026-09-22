/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-08 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 21:05:29
 * @FilePath: \go-wsc\batcher\message_status_updater_test.go
 * @Description: 消息状态批量更新器测试 - 覆盖 message_status_updater.go
 *
 * 覆盖场景：
 *   1. 创建更新器 + Submit 非阻塞提交
 *   2. 批量 flush 按 status+reason+errMsg 分组合并
 *   3. Stop 刷盘剩余数据
 *   4. 队列满时 Submit 返回 false
 *   5. BatchUpdateStatus 失败时不 panic
 *   6. 空批次 flush 不调用 DB
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// newStatusUpdaterWriter 创建带假消息仓储的端口替身（本包不依赖真实 Hub）
func newStatusUpdaterWriter(t *testing.T) (*fakeBatchWriter, *fakeMessageSink) {
	t.Helper()
	w := newFakeBatchWriter()
	return w, w.sink
}

// TestNewMessageStatusUpdater 验证创建更新器
func TestNewMessageStatusUpdater(t *testing.T) {
	w, _ := newStatusUpdaterWriter(t)

	u := NewMessageStatusUpdater(w, 100, 10, 50*time.Millisecond)
	require.NotNil(t, u)
	require.NotNil(t, u.processor)
	defer u.Stop()
}

// TestMessageStatusUpdater_SubmitAndFlush 验证提交后批量 flush 到 DB
func TestMessageStatusUpdater_SubmitAndFlush(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)

	u := NewMessageStatusUpdater(w, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 提交 3 条 Success 消息
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "m1", Status: models.MessageSendStatusSuccess}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "m2", Status: models.MessageSendStatusSuccess}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "m3", Status: models.MessageSendStatusSuccess}))

	// 等待 flush
	require.Eventually(t, func() bool {
		return len(repo.snapshot()) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// 验证：3 条 Success 合并为 1 次 BatchUpdateStatus 调用
	require.Len(t, repo.snapshot(), 1)
	assert.Equal(t, models.MessageSendStatusSuccess, repo.snapshot()[0].Status)
	assert.ElementsMatch(t, statusKeys("m1", "m2", "m3"), repo.snapshot()[0].Keys)
}

// TestMessageStatusUpdater_GroupByStatus 验证不同 status 分组为多次调用
func TestMessageStatusUpdater_GroupByStatus(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)

	u := NewMessageStatusUpdater(w, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 提交不同 status 的消息
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "s1", Status: models.MessageSendStatusSuccess}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "f1", Status: models.MessageSendStatusFailed, Reason: models.FailureReasonQueueFull, ErrMsg: "queue full"}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "s2", Status: models.MessageSendStatusSuccess}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "o1", Status: models.MessageSendStatusUserOffline, Reason: models.FailureReasonUserOffline}))

	// 等待 flush
	require.Eventually(t, func() bool {
		return len(repo.snapshot()) >= 3
	}, 2*time.Second, 10*time.Millisecond)

	// 验证：3 种 status → 3 次调用
	assert.GreaterOrEqual(t, len(repo.snapshot()), 3)

	// 按 status 收集结果
	statusMap := make(map[models.MessageSendStatus][]models.MessageRecordKey)
	for _, call := range repo.snapshot() {
		statusMap[call.Status] = append(statusMap[call.Status], call.Keys...)
	}
	assert.ElementsMatch(t, statusKeys("s1", "s2"), statusMap[models.MessageSendStatusSuccess])
	assert.ElementsMatch(t, statusKeys("f1"), statusMap[models.MessageSendStatusFailed])
	assert.ElementsMatch(t, statusKeys("o1"), statusMap[models.MessageSendStatusUserOffline])
}

// TestMessageStatusUpdater_BatchSizeTrigger 验证 batchSize 满时立即 flush
func TestMessageStatusUpdater_BatchSizeTrigger(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)

	// batchSize=2，提交 2 条立即触发 flush
	u := NewMessageStatusUpdater(w, 100, 2, 10*time.Second)
	defer u.Stop()

	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "b1", Status: models.MessageSendStatusSuccess}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "b2", Status: models.MessageSendStatusSuccess}))

	// batchSize 满应立即 flush（不等 10s 定时器）
	require.Eventually(t, func() bool {
		return len(repo.snapshot()) > 0
	}, 2*time.Second, 5*time.Millisecond)
}

// TestMessageStatusUpdater_StopFlushes 验证 Stop 时刷盘剩余数据
func TestMessageStatusUpdater_StopFlushes(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)

	// 长 flushInterval，确保只有 Stop 触发 flush
	u := NewMessageStatusUpdater(w, 100, 10, 10*time.Second)

	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "stop1", Status: models.MessageSendStatusSuccess}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "stop2", Status: models.MessageSendStatusSuccess}))

	u.Stop() // 应 flush 剩余

	// Stop 是同步的，flush 完成后返回
	require.Len(t, repo.snapshot(), 1)
	assert.ElementsMatch(t, statusKeys("stop1", "stop2"), repo.snapshot()[0].Keys)
}

// TestMessageStatusUpdater_QueueFull 验证队列满时 Submit 返回 false
//
// 确定性设计：用 batchUpdateBlock 卡住 worker 的 flush 回调，使其无法 drain queue。
// batchSize=1 → 第 1 条立即触发 flush（阻塞），worker 卡住后 queue 恢复空闲容量，
// 此时再填满 queue 即可确定性地触发 Submit 返回 false。
func TestMessageStatusUpdater_QueueFull(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)

	// 阻塞式 flush：worker 进入 BatchUpdateStatus 后卡住，无法 drain queue
	flushStarted := make(chan struct{})
	flushRelease := make(chan struct{})
	repo.block = func() {
		close(flushStarted)
		<-flushRelease
	}

	// queueSize=1, batchSize=1 → 第 1 条立即触发 flush（阻塞）
	u := NewMessageStatusUpdater(w, 1, 1, 10*time.Second)
	defer u.Stop()

	// 提交第 1 条 → worker 读出后触发 flush（阻塞）
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "q1", Status: models.MessageSendStatusSuccess}))
	// 等待 worker 确认进入 flush（此时 queue 已被 drain，容量恢复）
	require.Eventually(t, func() bool {
		select {
		case <-flushStarted:
			return true
		default:
			return false
		}
	}, 2*time.Second, time.Millisecond)

	// worker 卡在 flush 中，queue 空闲容量=1
	// 提交第 2 条 → 进入队列
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "q2", Status: models.MessageSendStatusSuccess}))
	// 提交第 3 条 → 队列满 → 返回 false
	assert.False(t, u.Submit(&StatusUpdateItem{MessageID: "q3", Status: models.MessageSendStatusSuccess}))

	// 释放 flush，让 worker 正常退出
	close(flushRelease)
}

// TestMessageStatusUpdater_BatchUpdateError 验证 DB 更新失败时不 panic
func TestMessageStatusUpdater_BatchUpdateError(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)
	repo.err = assertError("batch update error")

	u := NewMessageStatusUpdater(w, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 提交后 flush 会遇到 batchUpdateErr，不应 panic
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "err1", Status: models.MessageSendStatusFailed, Reason: models.FailureReasonQueueFull}))

	// 等待 flush 执行（即使失败）
	require.Eventually(t, func() bool {
		return len(repo.snapshot()) > 0
	}, 2*time.Second, 10*time.Millisecond)
}

// TestMessageStatusUpdater_ConcurrentSubmit 验证并发提交安全
func TestMessageStatusUpdater_ConcurrentSubmit(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)

	u := NewMessageStatusUpdater(w, 1000, 50, 50*time.Millisecond)
	defer u.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			u.Submit(&StatusUpdateItem{
				MessageID: "concurrent-" + string(rune('A'+idx%26)),
				Status:    models.MessageSendStatusSuccess,
			})
		}(i)
	}
	wg.Wait()

	// 等待所有 flush 完成
	require.Eventually(t, func() bool {
		totalIDs := 0
		for _, call := range repo.snapshot() {
			totalIDs += len(call.Keys)
		}
		return totalIDs > 0
	}, 3*time.Second, 10*time.Millisecond)
}

// TestMessageStatusUpdater_SameReasonGroups 验证相同 status 不同 reason 分组
func TestMessageStatusUpdater_SameReasonGroups(t *testing.T) {
	w, repo := newStatusUpdaterWriter(t)

	u := NewMessageStatusUpdater(w, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 同为 Failed 但 reason/errMsg 不同 → 分为不同组
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "f1", Status: models.MessageSendStatusFailed, Reason: models.FailureReasonQueueFull, ErrMsg: "queue full"}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "f2", Status: models.MessageSendStatusFailed, Reason: models.FailureReasonQueueFull, ErrMsg: "queue full"}))
	require.True(t, u.Submit(&StatusUpdateItem{MessageID: "f3", Status: models.MessageSendStatusFailed, Reason: models.FailureReasonConnError, ErrMsg: "conn error"}))

	require.Eventually(t, func() bool {
		return len(repo.snapshot()) >= 2
	}, 2*time.Second, 10*time.Millisecond)

	assert.GreaterOrEqual(t, len(repo.snapshot()), 2)

	// 收集 reason → keys
	reasonMap := make(map[models.FailureReason][]models.MessageRecordKey)
	for _, call := range repo.snapshot() {
		reasonMap[call.Reason] = append(reasonMap[call.Reason], call.Keys...)
	}
	assert.ElementsMatch(t, statusKeys("f1", "f2"), reasonMap[models.FailureReasonQueueFull])
	assert.ElementsMatch(t, statusKeys("f3"), reasonMap[models.FailureReasonConnError])
}

// assertError 返回一个简单的 error 用于测试
func assertError(msg string) error { return errors.New(msg) }

// statusKeys 构造复合键断言载荷（Receiver 为空串，对应广播类记录）
func statusKeys(ids ...string) []models.MessageRecordKey {
	keys := make([]models.MessageRecordKey, len(ids))
	for i, id := range ids {
		keys[i] = models.MessageRecordKey{MessageID: id}
	}
	return keys
}

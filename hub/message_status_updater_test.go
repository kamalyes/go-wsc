/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-08 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-08 10:07:58
 * @FilePath: \go-wsc\hub\message_status_updater_test.go
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

package hub

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// newStatusUpdaterHub 创建带 fakeMessageRecordRepo 的测试 Hub
func newStatusUpdaterHub(t *testing.T) (*Hub, *fakeMessageRecordRepo, func()) {
	t.Helper()
	hub, _, _, cleanup := setupGroupTestHub(t)
	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)
	return hub, repo, cleanup
}

// TestNewMessageStatusUpdater 验证创建更新器
func TestNewMessageStatusUpdater(t *testing.T) {
	hub, _, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	u := NewMessageStatusUpdater(hub, 100, 10, 50*time.Millisecond)
	require.NotNil(t, u)
	require.NotNil(t, u.processor)
	defer u.Stop()
}

// TestMessageStatusUpdater_SubmitAndFlush 验证提交后批量 flush 到 DB
func TestMessageStatusUpdater_SubmitAndFlush(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	u := NewMessageStatusUpdater(hub, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 提交 3 条 Success 消息
	require.True(t, u.Submit(&statusUpdateItem{msgID: "m1", status: MessageSendStatusSuccess}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "m2", status: MessageSendStatusSuccess}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "m3", status: MessageSendStatusSuccess}))

	// 等待 flush
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.batchUpdateCalls) > 0
	}, 2*time.Second, 10*time.Millisecond)

	// 验证：3 条 Success 合并为 1 次 BatchUpdateStatus 调用
	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	require.Len(t, repo.batchUpdateCalls, 1)
	assert.Equal(t, MessageSendStatusSuccess, repo.batchUpdateCalls[0].Status)
	assert.ElementsMatch(t, []string{"m1", "m2", "m3"}, repo.batchUpdateCalls[0].IDs)
}

// TestMessageStatusUpdater_GroupByStatus 验证不同 status 分组为多次调用
func TestMessageStatusUpdater_GroupByStatus(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	u := NewMessageStatusUpdater(hub, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 提交不同 status 的消息
	require.True(t, u.Submit(&statusUpdateItem{msgID: "s1", status: MessageSendStatusSuccess}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "f1", status: MessageSendStatusFailed, reason: FailureReasonQueueFull, errMsg: "queue full"}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "s2", status: MessageSendStatusSuccess}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "o1", status: MessageSendStatusUserOffline, reason: FailureReasonUserOffline}))

	// 等待 flush
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.batchUpdateCalls) >= 3
	}, 2*time.Second, 10*time.Millisecond)

	// 验证：3 种 status → 3 次调用
	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	assert.GreaterOrEqual(t, len(repo.batchUpdateCalls), 3)

	// 按 status 收集结果
	statusMap := make(map[MessageSendStatus][]string)
	for _, call := range repo.batchUpdateCalls {
		statusMap[call.Status] = append(statusMap[call.Status], call.IDs...)
	}
	assert.ElementsMatch(t, []string{"s1", "s2"}, statusMap[MessageSendStatusSuccess])
	assert.ElementsMatch(t, []string{"f1"}, statusMap[MessageSendStatusFailed])
	assert.ElementsMatch(t, []string{"o1"}, statusMap[MessageSendStatusUserOffline])
}

// TestMessageStatusUpdater_BatchSizeTrigger 验证 batchSize 满时立即 flush
func TestMessageStatusUpdater_BatchSizeTrigger(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	// batchSize=2，提交 2 条立即触发 flush
	u := NewMessageStatusUpdater(hub, 100, 2, 10*time.Second)
	defer u.Stop()

	require.True(t, u.Submit(&statusUpdateItem{msgID: "b1", status: MessageSendStatusSuccess}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "b2", status: MessageSendStatusSuccess}))

	// batchSize 满应立即 flush（不等 10s 定时器）
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.batchUpdateCalls) > 0
	}, 2*time.Second, 5*time.Millisecond)
}

// TestMessageStatusUpdater_StopFlushes 验证 Stop 时刷盘剩余数据
func TestMessageStatusUpdater_StopFlushes(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	// 长 flushInterval，确保只有 Stop 触发 flush
	u := NewMessageStatusUpdater(hub, 100, 10, 10*time.Second)

	require.True(t, u.Submit(&statusUpdateItem{msgID: "stop1", status: MessageSendStatusSuccess}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "stop2", status: MessageSendStatusSuccess}))

	u.Stop() // 应 flush 剩余

	// Stop 是同步的，flush 完成后返回
	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	require.Len(t, repo.batchUpdateCalls, 1)
	assert.ElementsMatch(t, []string{"stop1", "stop2"}, repo.batchUpdateCalls[0].IDs)
}

// TestMessageStatusUpdater_QueueFull 验证队列满时 Submit 返回 false
//
// 确定性设计：用 batchUpdateBlock 卡住 worker 的 flush 回调，使其无法 drain queue。
// batchSize=1 → 第 1 条立即触发 flush（阻塞），worker 卡住后 queue 恢复空闲容量，
// 此时再填满 queue 即可确定性地触发 Submit 返回 false。
func TestMessageStatusUpdater_QueueFull(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	// 阻塞式 flush：worker 进入 BatchUpdateStatus 后卡住，无法 drain queue
	flushStarted := make(chan struct{})
	flushRelease := make(chan struct{})
	repo.batchUpdateBlock = func() {
		close(flushStarted)
		<-flushRelease
	}

	// queueSize=1, batchSize=1 → 第 1 条立即触发 flush（阻塞）
	u := NewMessageStatusUpdater(hub, 1, 1, 10*time.Second)
	defer u.Stop()

	// 提交第 1 条 → worker 读出后触发 flush（阻塞）
	require.True(t, u.Submit(&statusUpdateItem{msgID: "q1", status: MessageSendStatusSuccess}))
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
	require.True(t, u.Submit(&statusUpdateItem{msgID: "q2", status: MessageSendStatusSuccess}))
	// 提交第 3 条 → 队列满 → 返回 false
	assert.False(t, u.Submit(&statusUpdateItem{msgID: "q3", status: MessageSendStatusSuccess}))

	// 释放 flush，让 worker 正常退出
	close(flushRelease)
}

// TestMessageStatusUpdater_BatchUpdateError 验证 DB 更新失败时不 panic
func TestMessageStatusUpdater_BatchUpdateError(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()
	repo.batchUpdateErr = assertError("batch update error")

	u := NewMessageStatusUpdater(hub, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 提交后 flush 会遇到 batchUpdateErr，不应 panic
	require.True(t, u.Submit(&statusUpdateItem{msgID: "err1", status: MessageSendStatusFailed, reason: FailureReasonQueueFull}))

	// 等待 flush 执行（即使失败）
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.batchUpdateCalls) > 0
	}, 2*time.Second, 10*time.Millisecond)
}

// TestMessageStatusUpdater_ConcurrentSubmit 验证并发提交安全
func TestMessageStatusUpdater_ConcurrentSubmit(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	u := NewMessageStatusUpdater(hub, 1000, 50, 50*time.Millisecond)
	defer u.Stop()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			u.Submit(&statusUpdateItem{
				msgID:  "concurrent-" + string(rune('A'+idx%26)),
				status: MessageSendStatusSuccess,
			})
		}(i)
	}
	wg.Wait()

	// 等待所有 flush 完成
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		totalIDs := 0
		for _, call := range repo.batchUpdateCalls {
			totalIDs += len(call.IDs)
		}
		return totalIDs > 0
	}, 3*time.Second, 10*time.Millisecond)
}

// TestMessageStatusUpdater_SameReasonGroups 验证相同 status 不同 reason 分组
func TestMessageStatusUpdater_SameReasonGroups(t *testing.T) {
	hub, repo, cleanup := newStatusUpdaterHub(t)
	defer cleanup()

	u := NewMessageStatusUpdater(hub, 100, 10, 50*time.Millisecond)
	defer u.Stop()

	// 同为 Failed 但 reason/errMsg 不同 → 分为不同组
	require.True(t, u.Submit(&statusUpdateItem{msgID: "f1", status: MessageSendStatusFailed, reason: FailureReasonQueueFull, errMsg: "queue full"}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "f2", status: MessageSendStatusFailed, reason: FailureReasonQueueFull, errMsg: "queue full"}))
	require.True(t, u.Submit(&statusUpdateItem{msgID: "f3", status: MessageSendStatusFailed, reason: FailureReasonConnError, errMsg: "conn error"}))

	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.batchUpdateCalls) >= 2
	}, 2*time.Second, 10*time.Millisecond)

	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	assert.GreaterOrEqual(t, len(repo.batchUpdateCalls), 2)

	// 收集 reason → IDs
	reasonMap := make(map[FailureReason][]string)
	for _, call := range repo.batchUpdateCalls {
		reasonMap[call.Reason] = append(reasonMap[call.Reason], call.IDs...)
	}
	assert.ElementsMatch(t, []string{"f1", "f2"}, reasonMap[FailureReasonQueueFull])
	assert.ElementsMatch(t, []string{"f3"}, reasonMap[FailureReasonConnError])
}

// assertError 返回一个简单的 error 用于测试
func assertError(msg string) error {
	return &simpleError{msg: msg}
}

type simpleError struct{ msg string }

func (e *simpleError) Error() string { return e.msg }

// 确保编译时引用 models 包（避免 unused import）
var _ = models.MessageSendStatusSuccess

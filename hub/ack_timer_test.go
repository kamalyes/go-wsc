/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 10:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 10:30:00
 * @FilePath: \go-wsc\hub\ack_timer_test.go
 * @Description: 跨节点投递 ACK 超时时间轮测试（覆盖 hub/ack_timer.go）
 *
 * 测试分层：
 *   1. 回调逻辑（makeAckTimeoutCallback）：认领/转存/幂等/广播/空跑
 *   2. 时间轮触发链路：调度→触发→ClaimStaleSending→转存离线 / 取消→不触发
 *   3. wiring 与 nil 安全：scheduleAckTimeout/cancelAckTimeout 装配与空跑
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
)

// newAckTimerTestHub 构造带 ackTimeoutTimer 的最小 Hub（不经过 Run，直接装配时间轮）
// newMinHub 不调用 Run()，ackTimeoutTimer 默认为 nil，此处手动注入以便单测
func newAckTimerTestHub(t *testing.T) *Hub {
	t.Helper()
	h := newMinHub()
	h.ackTimeoutTimer = syncx.NewHashedWheelTimer()
	return h
}

// ============================================================================
// 回调逻辑测试（makeAckTimeoutCallback）
// ============================================================================

// TestAckTimeoutCallback_MarksAckTimeoutAndStoresOffline
// 超时回调：状态仍 sending → ClaimStaleSending 认领 → 标记 AckTimeout → 转存离线
func TestAckTimeoutCallback_MarksAckTimeoutAndStoresOffline(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	repo := &fakeMessageRecordRepo{}
	h.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	h.SetOfflineMessageHandler(offline)

	// ClaimStaleSending 从 queryResult 查找 sending 记录；FindByMessageID 返回带消息体的记录供转存
	record := makeStaleSendingRecord(t, "m-ack-cb", "u-cb", time.Now())
	repo.queryResult = []*models.MessageSendRecord{record}
	repo.findByMessageIDResult = record

	// 直接执行超时回调（跳过 30s 调度延迟，聚焦回调逻辑）
	h.makeAckTimeoutCallback("m-ack-cb")()

	assert.True(t, hasAckTimeoutUpdate(repo, "m-ack-cb"), "超时回调应标记 ack_timeout")
	require.Eventually(t, func() bool {
		return offline.getStoreCalled() > 0
	}, 2*time.Second, 10*time.Millisecond, "超时消息应转存离线队列")
}

// TestAckTimeoutCallback_StatusAlreadyUpdated_Noop
// 状态已被目标节点回报更新（success/failed）→ ClaimStaleSending 状态守卫扑空 → 不标记不转存
func TestAckTimeoutCallback_StatusAlreadyUpdated_Noop(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	repo := &fakeMessageRecordRepo{}
	h.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	h.SetOfflineMessageHandler(offline)

	// 记录状态已是 Success（目标节点已回报），ClaimStaleSending 状态守卫扑空
	record := makeStaleSendingRecord(t, "m-ack-done", "u-done", time.Now())
	record.Status = models.MessageSendStatusSuccess
	repo.queryResult = []*models.MessageSendRecord{record}
	repo.findByMessageIDResult = record

	h.makeAckTimeoutCallback("m-ack-done")()

	// ClaimStaleSending 会被调用但认领结果为空（状态守卫），不标记 AckTimeout、不转存离线
	assert.False(t, hasAckTimeoutUpdate(repo, "m-ack-done"), "状态已变更的记录不应被标记 ack_timeout")
	time.Sleep(200 * time.Millisecond) // 转存是异步的，等待窗口确认未触发
	assert.Zero(t, offline.getStoreCalled(), "已成功投递的消息不应转存离线")
}

// TestAckTimeoutCallback_BroadcastNotStoredOffline
// 广播类记录（Receiver 为空）：标记 AckTimeout 供审计，但不转存离线
func TestAckTimeoutCallback_BroadcastNotStoredOffline(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	repo := &fakeMessageRecordRepo{}
	h.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	h.SetOfflineMessageHandler(offline)

	record := makeStaleSendingRecord(t, "m-ack-bcast", "", time.Now())
	repo.queryResult = []*models.MessageSendRecord{record}
	repo.findByMessageIDResult = record

	h.makeAckTimeoutCallback("m-ack-bcast")()

	assert.True(t, hasAckTimeoutUpdate(repo, "m-ack-bcast"), "超时广播记录仍应标记 ack_timeout 供审计")
	time.Sleep(200 * time.Millisecond) // 转存是异步的，等待窗口
	assert.Zero(t, offline.getStoreCalled(), "广播消息（无 Receiver）不应转存离线")
}

// TestAckTimeoutCallback_NoRepo 未配置记录仓库时安全空跑
func TestAckTimeoutCallback_NoRepo(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	// 不设置 messageRecordRepo（默认 nil）
	assert.NotPanics(t, func() {
		h.makeAckTimeoutCallback("m-no-repo")()
	})
}

// TestAckTimeoutCallback_ClaimErrorSafe ClaimStaleSending 返回错误时不 panic
func TestAckTimeoutCallback_ClaimErrorSafe(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	repo := &fakeMessageRecordRepo{batchUpdateErr: assertAnError("claim failed")}
	h.SetMessageRecordRepository(repo)
	h.SetOfflineMessageHandler(newAckFakeOfflineHandler())

	record := makeStaleSendingRecord(t, "m-claim-err", "u-err", time.Now())
	repo.queryResult = []*models.MessageSendRecord{record}

	assert.NotPanics(t, func() {
		h.makeAckTimeoutCallback("m-claim-err")()
	})
}

// ============================================================================
// 时间轮触发链路测试（短延迟，绕过 nodeAckTimeout 30s 常量）
// ============================================================================

// TestAckTimeoutTimer_FiresCallbackAndStoresOffline
// 真实时间轮短延迟调度 → 触发 → ClaimStaleSending → 转存离线 全链路
func TestAckTimeoutTimer_FiresCallbackAndStoresOffline(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	repo := &fakeMessageRecordRepo{}
	h.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	h.SetOfflineMessageHandler(offline)

	record := makeStaleSendingRecord(t, "m-fire", "u-fire", time.Now())
	repo.queryResult = []*models.MessageSendRecord{record}
	repo.findByMessageIDResult = record

	// 直接在时间轮上用短延迟调度（绕过 scheduleAckTimeout 的 30s 常量，聚焦触发链路）
	h.ackTimeoutTimer.ScheduleWithKey("m-fire", 200*time.Millisecond, h.makeAckTimeoutCallback("m-fire"))

	// -race 全量套件下时间轮 worker 可能被并行测试拖慢，200ms 回调可能延迟到 2s 后才触发
	require.Eventually(t, func() bool {
		return offline.getStoreCalled() > 0
	}, 8*time.Second, 20*time.Millisecond, "时间轮触发后应转存离线")
	assert.True(t, hasAckTimeoutUpdate(repo, "m-fire"), "时间轮触发后应标记 ack_timeout")
}

// TestAckTimeoutTimer_CancelPreventsFire
// 调度后 O(1) 取消（CancelByKey 惰性取消）→ 回调不再触发
func TestAckTimeoutTimer_CancelPreventsFire(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	repo := &fakeMessageRecordRepo{}
	h.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	h.SetOfflineMessageHandler(offline)

	record := makeStaleSendingRecord(t, "m-cancel", "u-cancel", time.Now())
	repo.queryResult = []*models.MessageSendRecord{record}
	repo.findByMessageIDResult = record

	h.ackTimeoutTimer.ScheduleWithKey("m-cancel", 200*time.Millisecond, h.makeAckTimeoutCallback("m-cancel"))
	h.ackTimeoutTimer.CancelByKey("m-cancel") // O(1) 惰性取消

	time.Sleep(500 * time.Millisecond) // 等待超过触发窗口，确认回调未触发
	assert.Zero(t, offline.getStoreCalled(), "取消后不应转存离线")
	assert.False(t, hasAckTimeoutUpdate(repo, "m-cancel"), "取消后不应标记 ack_timeout")
}

// TestAckTimeoutTimer_RefreshReplacesOldTask
// Refresh（同 key 重新调度）会惰性取消旧任务，仅新任务触发一次
func TestAckTimeoutTimer_RefreshReplacesOldTask(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	repo := &fakeMessageRecordRepo{}
	h.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	h.SetOfflineMessageHandler(offline)

	record := makeStaleSendingRecord(t, "m-refresh", "u-refresh", time.Now())
	repo.queryResult = []*models.MessageSendRecord{record}
	repo.findByMessageIDResult = record

	// 旧任务短延迟，新任务长延迟：Refresh 后旧任务被惰性取消，仅新任务触发
	h.ackTimeoutTimer.ScheduleWithKey("m-refresh", 100*time.Millisecond, h.makeAckTimeoutCallback("m-refresh"))
	h.ackTimeoutTimer.Refresh("m-refresh", 400*time.Millisecond, h.makeAckTimeoutCallback("m-refresh"))

	// 100ms 后旧任务本应触发，但已被 Refresh 惰性取消
	time.Sleep(250 * time.Millisecond)
	assert.Zero(t, offline.getStoreCalled(), "旧任务被 Refresh 取消，不应提前触发")

	// 400ms 新任务触发（-race 下时间轮 worker 可能被高负载拖慢，窗口放宽到 8s）
	require.Eventually(t, func() bool {
		return offline.getStoreCalled() > 0
	}, 8*time.Second, 20*time.Millisecond, "新任务应在 Refresh 延迟后触发")
}

// ============================================================================
// wiring 与 nil 安全测试
// ============================================================================

// TestScheduleAckTimeout_Wiring scheduleAckTimeout 在时间轮注册活跃任务
func TestScheduleAckTimeout_Wiring(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()
	h.SetMessageRecordRepository(&fakeMessageRecordRepo{})

	// scheduleAckTimeout 使用 nodeAckTimeout(30s) 调度，测试期间不会触发
	before := h.ackTimeoutTimer.Stats().ActiveTasks
	h.scheduleAckTimeout("m-wire")
	after := h.ackTimeoutTimer.Stats().ActiveTasks
	assert.Equal(t, before+1, after, "scheduleAckTimeout 应在时间轮注册 1 个活跃任务")
}

// TestScheduleCancelAckTimeout_NilTimerSafe ackTimeoutTimer 为 nil 时不 panic
func TestScheduleCancelAckTimeout_NilTimerSafe(t *testing.T) {
	t.Parallel()
	h := newMinHub() // 不注入 ackTimeoutTimer（默认 nil）
	defer h.Shutdown()
	h.SetMessageRecordRepository(&fakeMessageRecordRepo{})

	assert.NotPanics(t, func() {
		h.scheduleAckTimeout("m-nil")
		h.cancelAckTimeout("m-nil")
	})
}

// TestScheduleCancelAckTimeout_EmptyIDSafe 空 messageID 时不调度/不取消
func TestScheduleCancelAckTimeout_EmptyIDSafe(t *testing.T) {
	t.Parallel()
	h := newAckTimerTestHub(t)
	defer h.Shutdown()

	before := h.ackTimeoutTimer.Stats().ActiveTasks
	assert.NotPanics(t, func() {
		h.scheduleAckTimeout("")
		h.cancelAckTimeout("")
	})
	assert.Equal(t, before, h.ackTimeoutTimer.Stats().ActiveTasks, "空 messageID 不应注册任务")
}

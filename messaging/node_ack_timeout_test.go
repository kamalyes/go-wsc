/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-19 00:19:16
 * @FilePath: \go-wsc\messaging\node_ack_timeout_test.go
 * @Description: 跨节点投递 ACK 超时兜底扫描测试（覆盖 hub/node_ack_timeout.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// makeStaleSendingRecord 构造一条指定创建时间的 sending 记录
func makeStaleSendingRecord(t *testing.T, msgID, receiver string, createTime time.Time) *models.MessageSendRecord {
	t.Helper()
	msg := makeGroupMessage("sender")
	msg.MessageID = msgID
	msg.Receiver = receiver
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	return &models.MessageSendRecord{
		MessageID:   msgID,
		Receiver:    receiver,
		Status:      models.MessageSendStatusSending,
		CreateTime:  createTime,
		MessageData: string(data),
	}
}

// hasAckTimeoutUpdate 判断 fake repo 是否收到指定 messageID 的 ack_timeout 批量更新
func hasAckTimeoutUpdate(repo *fakeMessageRecordRepo, msgID string) bool {
	return hasBatchUpdate(repo, msgID, models.MessageSendStatusAckTimeout)
}

// hasBatchUpdate 判断 fake repo 是否收到指定 messageID + 状态的批量更新
// （write-ahead 状态回报经 MessageStatusUpdater flush 后可观测）
func hasBatchUpdate(repo *fakeMessageRecordRepo, msgID string, status models.MessageSendStatus) bool {
	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	for _, call := range repo.batchUpdateCalls {
		if call.Status != status {
			continue
		}
		for _, key := range call.Keys {
			if key.MessageID == msgID {
				return true
			}
		}
	}
	return false
}

// TestTimeoutStaleSendingRecords_MarksAckTimeoutAndStoresOffline
// 超时未确认的 sending 记录应标记 AckTimeout 并转存离线（PubSub 消息丢失时的最终一致性兜底）
func TestTimeoutStaleSendingRecords_MarksAckTimeoutAndStoresOffline(t *testing.T) {
	t.Parallel()
	m, host, offline := newAckTimeoutTestManager()
	defer m.Stop()

	repo := &fakeMessageRecordRepo{}
	host.messageSink = repo

	// 创建时间早于 ACK 超时窗口（默认 30s）
	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-ack-stale", "u-ack", time.Now().Add(-time.Minute)),
	}

	m.timeoutStaleSendingRecords()

	assert.True(t, hasAckTimeoutUpdate(repo, "m-ack-stale"),
		"超时 sending 记录应被标记为 ack_timeout")

	// P2P 消息（Receiver 非空）应异步转存离线
	require.Eventually(t, func() bool {
		return offline.getStoreCalled() > 0
	}, 2*time.Second, 10*time.Millisecond, "超时消息应转存离线队列")
}

// TestTimeoutStaleSendingRecords_SkipsFreshSending 新近 sending 记录不应被误判超时
func TestTimeoutStaleSendingRecords_SkipsFreshSending(t *testing.T) {
	t.Parallel()
	m, host, offline := newAckTimeoutTestManager()
	defer m.Stop()

	repo := &fakeMessageRecordRepo{}
	host.messageSink = repo

	// 创建时间在 ACK 超时窗口内（目标节点可能仍在处理中）
	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-ack-fresh", "u-ack", time.Now().Add(-time.Second)),
	}

	m.timeoutStaleSendingRecords()

	repo.batchUpdateMu.Lock()
	calls := len(repo.batchUpdateCalls)
	repo.batchUpdateMu.Unlock()
	assert.Zero(t, calls, "未超时的 sending 记录不应触发批量更新")
	assert.Zero(t, offline.getStoreCalled(), "未超时的消息不应转存离线")
}

// TestTimeoutStaleSendingRecords_BroadcastNotStoredOffline
// 广播类记录（Receiver 为空）只标记 AckTimeout 供审计，不转存离线
func TestTimeoutStaleSendingRecords_BroadcastNotStoredOffline(t *testing.T) {
	t.Parallel()
	m, host, offline := newAckTimeoutTestManager()
	defer m.Stop()

	repo := &fakeMessageRecordRepo{}
	host.messageSink = repo

	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-ack-broadcast", "", time.Now().Add(-time.Minute)),
	}

	m.timeoutStaleSendingRecords()

	assert.True(t, hasAckTimeoutUpdate(repo, "m-ack-broadcast"),
		"超时广播记录仍应标记 ack_timeout 供审计")
	time.Sleep(100 * time.Millisecond) // 转存是异步的，等待窗口
	assert.Zero(t, offline.getStoreCalled(), "广播消息（无 Receiver）不应转存离线")
}

// TestTimeoutStaleSendingRecords_NoRepo 未配置记录仓库时应安全空跑
func TestTimeoutStaleSendingRecords_NoRepo(t *testing.T) {
	t.Parallel()
	m, _, _ := newAckTimeoutTestManager()
	defer m.Stop()

	assert.NotPanics(t, func() {
		m.timeoutStaleSendingRecords()
	})
}

// TestTimeoutStaleSendingRecords_MultiNodeClaimDedup
// 多节点并发扫描同一批超时记录：ClaimStaleSending 状态守卫保证仅一个节点认领成功，
// 未认领成功的节点不得重复转存离线（修复前 N 个 Pod 各转存一次 → 用户上线收到 N 份重复推送）
func TestTimeoutStaleSendingRecords_MultiNodeClaimDedup(t *testing.T) {
	t.Parallel()
	m1, host1, _ := newAckTimeoutTestManager()
	defer m1.Stop()
	m2, host2, _ := newAckTimeoutTestManager()
	defer m2.Stop()

	// 两个节点共享同一 fake repo（模拟共享记录表）与同一离线处理器：
	// 认领去重语义要求转存动作全局仅发生一次，无论哪个节点认领成功
	repo := &fakeMessageRecordRepo{}
	host1.messageSink = repo
	host2.messageSink = repo
	sharedOffline, log := newOfflineRecordingHandler()
	m1.offlineHandler = sharedOffline
	m2.offlineHandler = sharedOffline

	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-claim-race", "u-claim", time.Now().Add(-time.Minute)),
	}

	// 同一轮扫描窗口内两个节点先后扫描（fake 的状态守卫模拟真实 DB 的并发 UPDATE 语义）
	m1.timeoutStaleSendingRecords()
	m2.timeoutStaleSendingRecords()

	require.Eventually(t, func() bool {
		return log.getStoreCalled() == 1
	}, 2*time.Second, 10*time.Millisecond, "仅认领成功的节点应转存离线一次")

	// 等待异步转存窗口结束，确认第二个节点未重复转存
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, log.getStoreCalled(), "未认领成功的节点不得重复转存离线")
}

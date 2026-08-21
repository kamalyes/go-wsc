/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-19 00:19:16
 * @FilePath: \go-wsc\hub\node_ack_timeout_test.go
 * @Description: 跨节点投递 ACK 超时兜底扫描测试（覆盖 hub/node_ack_timeout.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

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
	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	for _, call := range repo.batchUpdateCalls {
		if call.Status != models.MessageSendStatusAckTimeout {
			continue
		}
		for _, id := range call.IDs {
			if id == msgID {
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
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	hub.SetOfflineMessageHandler(offline)

	// 创建时间早于 ACK 超时窗口（默认 30s）
	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-ack-stale", "u-ack", time.Now().Add(-time.Minute)),
	}

	hub.timeoutStaleSendingRecords()

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
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	hub.SetOfflineMessageHandler(offline)

	// 创建时间在 ACK 超时窗口内（目标节点可能仍在处理中）
	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-ack-fresh", "u-ack", time.Now().Add(-time.Second)),
	}

	hub.timeoutStaleSendingRecords()

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
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	hub.SetOfflineMessageHandler(offline)

	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-ack-broadcast", "", time.Now().Add(-time.Minute)),
	}

	hub.timeoutStaleSendingRecords()

	assert.True(t, hasAckTimeoutUpdate(repo, "m-ack-broadcast"),
		"超时广播记录仍应标记 ack_timeout 供审计")
	time.Sleep(100 * time.Millisecond) // 转存是异步的，等待窗口
	assert.Zero(t, offline.getStoreCalled(), "广播消息（无 Receiver）不应转存离线")
}

// TestTimeoutStaleSendingRecords_NoRepo 未配置记录仓库时应安全空跑
func TestTimeoutStaleSendingRecords_NoRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	assert.NotPanics(t, func() {
		hub.timeoutStaleSendingRecords()
	})
}

// TestTimeoutStaleSendingRecords_MultiNodeClaimDedup
// 多节点并发扫描同一批超时记录：ClaimStaleSending 状态守卫保证仅一个节点认领成功，
// 未认领成功的节点不得重复转存离线（修复前 N 个 Pod 各转存一次 → 用户上线收到 N 份重复推送）
func TestTimeoutStaleSendingRecords_MultiNodeClaimDedup(t *testing.T) {
	t.Parallel()
	hub1 := newMinHub()
	defer hub1.Shutdown()
	hub2 := newMinHub()
	defer hub2.Shutdown()

	// 两个节点共享同一 fake repo（模拟共享记录表）与同一离线处理器
	repo := &fakeMessageRecordRepo{}
	hub1.SetMessageRecordRepository(repo)
	hub2.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	hub1.SetOfflineMessageHandler(offline)
	hub2.SetOfflineMessageHandler(offline)

	repo.queryResult = []*models.MessageSendRecord{
		makeStaleSendingRecord(t, "m-claim-race", "u-claim", time.Now().Add(-time.Minute)),
	}

	// 同一轮扫描窗口内两个节点先后扫描（fake 的状态守卫模拟真实 DB 的并发 UPDATE 语义）
	hub1.timeoutStaleSendingRecords()
	hub2.timeoutStaleSendingRecords()

	require.Eventually(t, func() bool {
		return offline.getStoreCalled() == 1
	}, 2*time.Second, 10*time.Millisecond, "仅认领成功的节点应转存离线一次")

	// 等待异步转存窗口结束，确认第二个节点未重复转存
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, 1, offline.getStoreCalled(), "未认领成功的节点不得重复转存离线")
}

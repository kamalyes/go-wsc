/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-06 10:15:23
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-06 10:15:23
 * @FilePath: \go-wsc\messaging\delivery_tiering_test.go
 * @Description: 消息分级投递测试 —— express 分道（P2P 轻装路径）与跨节点队列满分道
 *
 * express 语义契约（fire-and-forget）：
 *   - 不落 message_record（防高频消息灌爆记录表）
 *   - 不进离线队列（用户离线即放弃本条，计入高频语义丢弃）
 *   - 不走重试链（单次尝试，latest-wins 语义下同 key 下一条自然覆盖）
 *   - 跨节点分发队列满语义丢弃（可靠档同步直发兜底，notice 与高频消息不同命）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/models"
)

// makeExpressMessage 构造 express 档消息（typing 类型默认表 → GuaranteeEphemeral）
func makeExpressMessage(sender string) *models.HubMessage {
	return models.NewHubMessage().
		SetSender(sender).
		SetMessageType(models.MessageTypeTyping).
		SetContent("typing")
}

// TestExpressP2P_OfflineGoesLightweight express 档 P2P 离线轻装路径：
// 用户离线即放弃本条（Success 按语义返回 true），不进离线队列、不落 message_record
func TestExpressP2P_OfflineGoesLightweight(t *testing.T) {
	t.Parallel()
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	result := m.SendToUserWithRetry(context.Background(), "u-express-offline", makeExpressMessage("danmaku-bot"))

	// 语义丢弃：离线放弃按语义完成（非失败），不进离线队列
	assert.True(t, result.Success, "express 离线放弃应按语义完成")
	assert.False(t, result.StoredOffline, "express 不进离线队列")
	assert.Nil(t, result.FinalError)

	// 不落 message_record（outbox flush 窗口内记录数恒为 0）
	require.Never(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.createdRecords) > 0
	}, 150*time.Millisecond, 20*time.Millisecond, "express 消息不应产生 write-ahead 记录")

	// 离线放弃计入高频语义丢弃（漏斗可观测）
	assert.EqualValues(t, 1, host.metrics.MetricsSnapshot()["ephemeral_dropped"],
		"express 离线放弃应计入高频语义丢弃")
}

// TestExpressP2P_OnlineSingleAttempt express 档 P2P 在线投递：单次尝试、不经重试链、不落库
func TestExpressP2P_OnlineSingleAttempt(t *testing.T) {
	t.Parallel()
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	client := makeTestClient("c-express-on", "u-express-on")
	host.GetShardedRegistry().AddClient(client)

	result := m.SendToUserWithRetry(context.Background(), "u-express-on", makeExpressMessage("danmaku-bot"))

	assert.True(t, result.Success, "在线 express 投递应成功")
	require.Len(t, result.Attempts, 1, "express 单次尝试（不经重试链）")

	// 不落 message_record（outbox flush 窗口内记录数恒为 0）
	require.Never(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.createdRecords) > 0
	}, 150*time.Millisecond, 20*time.Millisecond, "express 消息不应产生 write-ahead 记录")

	assert.EqualValues(t, 0, host.metrics.MetricsSnapshot()["ephemeral_dropped"],
		"在线投递成功不应产生丢弃计数")
}

// TestReliableP2P_OnlineKeepsWriteAhead 对照组：可靠档在线保持全机制——
// write-ahead 记录照常落库（与 express 在线路径的分道差异）
func TestReliableP2P_OnlineKeepsWriteAhead(t *testing.T) {
	t.Parallel()
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	client := makeTestClient("c-reliable-on", "u-reliable-on")
	host.GetShardedRegistry().AddClient(client)

	msg := makeGroupMessage("sender")
	result := m.SendToUserWithRetry(context.Background(), "u-reliable-on", msg)

	assert.True(t, result.Success, "在线可靠档投递应成功")

	// write-ahead 记录照常落库（与 express 分道的核心差异）
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.createdRecords) >= 1
	}, 2*time.Second, 10*time.Millisecond, "可靠档应产生 write-ahead 记录")

	// 可靠档丢弃计数恒为 0（分道不误伤）
	assert.EqualValues(t, 0, host.metrics.MetricsSnapshot()["ephemeral_dropped"])
}

// TestSubmitClusterDispatch_TieredOnQueueFull 跨节点分发队列满分道：
// express 语义丢弃计数（不同步直发）、可靠档同步直发兜底（notice 不因队列满丢失）
func TestSubmitClusterDispatch_TieredOnQueueFull(t *testing.T) {
	t.Parallel()
	m, host := newTestManager()
	host.dispatchQueueFull = true

	// express 队列满：语义丢弃，不直发
	expressMsg := makeExpressMessage("danmaku-bot")
	ok := m.submitClusterDispatch(context.Background(), expressMsg, cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
	})
	assert.False(t, ok, "express 队列满应语义丢弃")
	assert.EqualValues(t, 0, host.clusterDirectCalls.Load(), "express 不应触发同步直发")
	assert.EqualValues(t, 1, host.metrics.MetricsSnapshot()["ephemeral_dropped"])

	// 可靠档队列满：同步直发兜底成功（降级次数可观测）
	reliableMsg := models.NewHubMessage().
		SetSender("system").
		SetMessageType(models.MessageTypeText).
		SetContent("notice").
		WithGuarantee(models.GuaranteeGuaranteed)
	ok = m.submitClusterDispatch(context.Background(), reliableMsg, cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
	})
	assert.True(t, ok, "可靠档队列满应同步直发兜底成功")
	assert.EqualValues(t, 1, host.clusterDirectCalls.Load(), "可靠档应恰好直发一次")
	assert.EqualValues(t, 1, host.metrics.MetricsSnapshot()["cluster_dispatch_syncs"],
		"同步直发降级应计入 stats")
}

// TestSubmitClusterDispatch_NormalPath 队列未满：两种分级均正常入池（无降级副作用）
func TestSubmitClusterDispatch_NormalPath(t *testing.T) {
	t.Parallel()
	m, host := newTestManager()

	for name, msg := range map[string]*models.HubMessage{
		"express": makeExpressMessage("danmaku-bot"),
		"reliable": models.NewHubMessage().
			SetSender("system").
			SetMessageType(models.MessageTypeText).
			SetContent("notice").
			WithGuarantee(models.GuaranteeStandard),
	} {
		ok := m.submitClusterDispatch(context.Background(), msg, cluster.ClusterDispatchOptions{
			Operation: models.OperationTypeBroadcast,
		})
		assert.True(t, ok, "%s 队列未满应正常入池", name)
	}

	assert.EqualValues(t, 0, host.clusterDirectCalls.Load(), "未降级不应直发")
	assert.EqualValues(t, 0, host.metrics.MetricsSnapshot()["cluster_dispatch_syncs"])
	assert.EqualValues(t, 0, host.metrics.MetricsSnapshot()["ephemeral_dropped"])
}

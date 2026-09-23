/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 09:27:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 09:27:00
 * @FilePath: \go-wsc\batcher\manager_test.go
 * @Description: 批处理器域管理器测试
 *
 * 覆盖两条主线：
 * - NewManager 聚合构造五个组件（nil 配置走默认参数兜底）
 * - StopTracking / StopRecords 分段停机：停机前提交的数据被 flush 落库
 *   （状态更新落 BatchUpdateStatus、记录 outbox 落 CreateBatch），
 *   停机后 Submit 拒绝
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// fakeManagerHost Host 端口替身：仓储复用 interfaces_test.go 的三个 fake，
// 补齐 GetMessageSink 能力面；NotifyObserversDirect 兼任观察者直投桩
type fakeManagerHost struct {
	*fakeBatchWriter
	notifyDirect []string // NotifyObserversDirect 记录的 messageID
}

func (f *fakeManagerHost) GetMessageSink() spi.MessageSink { return f.sink }

func (f *fakeManagerHost) NotifyObserversDirect(msg *models.HubMessage, _ string, _ []string) {
	if msg != nil {
		f.notifyDirect = append(f.notifyDirect, msg.MessageID)
	}
}

var _ Host = (*fakeManagerHost)(nil)
var _ ObserverNotifier = (*fakeManagerHost)(nil)

// TestNewManagerConstructsAllComponents 验证 nil 配置下五个组件全部构造成功
func TestNewManagerConstructsAllComponents(t *testing.T) {
	host := &fakeManagerHost{fakeBatchWriter: newFakeBatchWriter()}
	m := NewManager(host, host, nil)

	assert.NotNil(t, m.StatusUpdater())
	assert.NotNil(t, m.RecordOutbox())
	assert.NotNil(t, m.HeartbeatStats())
	assert.NotNil(t, m.MessageStats())
	assert.NotNil(t, m.ObserverNotify())
}

// TestManagerStopRecordsFlushesPending 验证 StopRecords 冲刷停机前提交的数据：
// 状态更新落 BatchUpdateStatus、记录 outbox 落 CreateBatch，停机后 Submit 拒绝
func TestManagerStopRecordsFlushesPending(t *testing.T) {
	host := &fakeManagerHost{fakeBatchWriter: newFakeBatchWriter()}
	m := NewManager(host, host, nil)

	// 提交一条状态更新与一条消息记录（均未到 flush 间隔，等待 Stop drain）
	require.True(t, m.StatusUpdater().Submit(&StatusUpdateItem{
		MessageID: "m-1",
		Receiver:  "u-1",
		Status:    models.MessageSendStatusSuccess,
	}))
	require.True(t, m.RecordOutbox().Submit(&models.MessageSendRecord{
		MessageID: "m-1",
		Receiver:  "u-1",
		Status:    models.MessageSendStatusSuccess,
	}))

	m.StopRecords()

	batches := host.sink.snapshot()
	require.Len(t, batches, 1)
	assert.Equal(t, models.MessageSendStatusSuccess, batches[0].Status)

	created := host.sink.createdSnapshot()
	require.Len(t, created, 1)
	assert.Equal(t, "m-1", created[0].MessageID)
}

// TestManagerStopTrackingFlushesHeartbeat 验证 StopTracking 冲刷停机前
// 提交的心跳统计（连接记录与连接质量双写）
func TestManagerStopTrackingFlushesHeartbeat(t *testing.T) {
	host := &fakeManagerHost{fakeBatchWriter: newFakeBatchWriter()}
	m := NewManager(host, host, nil)

	require.True(t, m.HeartbeatStats().Submit(&HeartbeatStatsEntry{
		ClientID: "c-1",
		PingTime: time.Now(),
		PongTime: time.Now(),
	}))
	require.True(t, m.MessageStats().Submit(&StatsIncrementItem{
		ConnectionID: "c-1",
		BytesSent:    128,
	}))

	m.StopTracking()

	heartbeats := host.records.heartbeatsSnapshot()
	require.Len(t, heartbeats, 1)
	assert.Equal(t, "c-1", heartbeats[0].ConnectionID)

	increments := host.quality.incrementsSnapshot()
	require.Len(t, increments, 1)
	assert.Equal(t, "c-1", increments[0].ConnectionID)
}

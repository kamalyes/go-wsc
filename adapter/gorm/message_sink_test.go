/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 17:10:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 17:10:02
 * @FilePath: \go-wsc\adapter\gorm\message_sink_test.go
 * @Description: 消息发送记录仓库的用例（sqlite 真实执行，覆盖攒批合并 SQL）
 *
 * 覆盖：
 *   - CreateBatch/FindByID/FindByMessageID：批量写入与复合键寻址
 *   - QueryRecords：多条件过滤 + 排序 + limit
 *   - FindRetryable：仅 failed/ack_timeout、未耗尽重试、未过期、create_time 升序
 *   - BatchUpdateStatus：(message_id, receiver) 行值 IN 批量定位、
 *     first_send_time 只写一次、空 reason 不清空 failure_reason
 *   - ClaimStaleSending：仅认领 sending 态（状态守卫）、缺失键静默
 *   - IncrementRetry：单条 UPDATE 完成状态机迁移 + 方言感知 JSON 数组追加
 *     （sqlite json_insert '$[#]' 路径真实执行）
 *   - GetStatistics：单条 GROUP BY 替代 9 次 COUNT
 *   - DeleteExpired/Delete/DeleteByMessageID/CleanupOld
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newMsgSink 建库 + 建表 + 建仓库（无自动清理）
func newMsgSink(t *testing.T) (*MessageSink, *gorm.DB) {
	t.Helper()
	db := newTestDB(t, &models.MessageSendRecord{})
	return NewMessageSink(db, nil, spi.NewDefaultLogger()), db
}

// newMsgRecord 构造一条消息发送记录（CreateTime 显式指定，保证排序用例确定性）
func newMsgRecord(messageID, receiver string, status models.MessageSendStatus, createTime time.Time) *models.MessageSendRecord {
	return &models.MessageSendRecord{
		SessionID:  "sess-1",
		MessageID:  messageID,
		HubID:      "hub-" + messageID,
		Sender:     "sender-1",
		Receiver:   receiver,
		Status:     status,
		CreateTime: createTime,
		MaxRetry:   3,
	}
}

// TestMessageSink_CreateBatch 批量写入与寻址
func TestMessageSink_CreateBatch(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	// 空批次不报错不触库
	require.NoError(t, sink.CreateBatch(ctx, nil))

	// 同一 message_id 多 receiver：P2P 每成员一条记录，复合键互不干扰
	records := []*models.MessageSendRecord{
		newMsgRecord("m-1", "r-1", models.MessageSendStatusPending, base),
		newMsgRecord("m-1", "r-2", models.MessageSendStatusPending, base),
		newMsgRecord("m-2", "r-1", models.MessageSendStatusSending, base.Add(time.Minute)),
	}
	require.NoError(t, sink.CreateBatch(ctx, records))

	byID, err := sink.FindByID(ctx, records[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "m-1", byID.MessageID)
	assert.Equal(t, "r-1", byID.Receiver)

	// 复合键精确定位：m-1 有两个 receiver，FindByMessageID(m-1, r-2) 只取 r-2
	byKey, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "m-1", Receiver: "r-2"})
	require.NoError(t, err)
	assert.Equal(t, "r-2", byKey.Receiver)

	_, err = sink.FindByID(ctx, 99999)
	require.Error(t, err)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestMessageSink_QueryRecords 多条件过滤 + 排序 + limit
func TestMessageSink_QueryRecords(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.MessageSendRecord{
		{SessionID: "s", MessageID: "q-1", HubID: "h1", Sender: "alice", Receiver: "bob",
			NodeIP: "10.0.0.1", ClientIP: "1.1.1.1", Status: models.MessageSendStatusSuccess, CreateTime: base},
		{SessionID: "s", MessageID: "q-2", HubID: "h2", Sender: "alice", Receiver: "carol",
			NodeIP: "10.0.0.1", ClientIP: "2.2.2.2", Status: models.MessageSendStatusFailed, CreateTime: base.Add(time.Minute)},
		{SessionID: "s", MessageID: "q-3", HubID: "h3", Sender: "dave", Receiver: "bob",
			NodeIP: "10.0.0.2", ClientIP: "1.1.1.1", Status: models.MessageSendStatusSuccess, CreateTime: base.Add(2 * time.Minute)},
	}
	require.NoError(t, sink.CreateBatch(ctx, seed))

	// 状态过滤
	got, err := sink.QueryRecords(ctx, &models.MessageRecordFilter{
		Status: statusPtr(models.MessageSendStatusSuccess),
	})
	require.NoError(t, err)
	assert.Len(t, got, 2)

	// 发送者 + 客户端 IP 组合
	got, err = sink.QueryRecords(ctx, &models.MessageRecordFilter{
		Sender: "alice", ClientIP: "2.2.2.2",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "q-2", got[0].MessageID)

	// 升序 + limit
	got, err = sink.QueryRecords(ctx, &models.MessageRecordFilter{OrderDesc: false, Limit: 2})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "q-1", got[0].MessageID)
	assert.Equal(t, "q-2", got[1].MessageID)

	// 降序（OrderDesc 零值为升序，降序须显式开启）
	got, err = sink.QueryRecords(ctx, &models.MessageRecordFilter{OrderDesc: true, Limit: 1})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "q-3", got[0].MessageID)

	// 接收者过滤
	got, err = sink.QueryRecords(ctx, &models.MessageRecordFilter{Receiver: "bob"})
	require.NoError(t, err)
	assert.Len(t, got, 2)
}

// TestMessageSink_FindRetryable 可重试记录筛选
func TestMessageSink_FindRetryable(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	expired := base.Add(-2 * time.Hour)
	future := base.Add(2 * time.Hour)

	seed := []*models.MessageSendRecord{
		// 命中：failed 且未耗尽未过期（最早，应排第一）
		newMsgRecord("rt-1", "r", models.MessageSendStatusFailed, base),
		// 命中：ack_timeout 且未耗尽未过期
		newMsgRecord("rt-2", "r", models.MessageSendStatusAckTimeout, base.Add(time.Minute)),
		// 排除：重试耗尽（retry_count >= max_retry）
		newMsgRecord("rt-3", "r", models.MessageSendStatusFailed, base.Add(2*time.Minute)),
		// 排除：已过期
		newMsgRecord("rt-4", "r", models.MessageSendStatusFailed, base.Add(3*time.Minute)),
		// 排除：状态不在可重试集合
		newMsgRecord("rt-5", "r", models.MessageSendStatusSuccess, base.Add(4*time.Minute)),
		newMsgRecord("rt-6", "r", models.MessageSendStatusSending, base.Add(5*time.Minute)),
	}
	seed[2].RetryCount = 3
	seed[3].ExpiresAt = &expired
	seed[1].ExpiresAt = &future
	require.NoError(t, sink.CreateBatch(ctx, seed))

	got, err := sink.FindRetryable(ctx, 10)
	require.NoError(t, err)
	require.Len(t, got, 2, "仅未耗尽、未过期的 failed/ack_timeout 入选")
	assert.Equal(t, "rt-1", got[0].MessageID, "按 create_time 升序")
	assert.Equal(t, "rt-2", got[1].MessageID)

	// limit 截断
	got, err = sink.FindRetryable(ctx, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
}

// TestMessageSink_BatchUpdateStatus 复合键行值 IN 批量状态更新
func TestMessageSink_BatchUpdateStatus(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.MessageSendRecord{
		newMsgRecord("bu-1", "r-1", models.MessageSendStatusSending, base),
		newMsgRecord("bu-1", "r-2", models.MessageSendStatusSending, base), // 同消息不同接收者
		newMsgRecord("bu-2", "r-1", models.MessageSendStatusSending, base),
	}
	require.NoError(t, sink.CreateBatch(ctx, seed))

	keys := []models.MessageRecordKey{
		{MessageID: "bu-1", Receiver: "r-1"},
		{MessageID: "bu-2", Receiver: "r-1"},
	}
	require.NoError(t, sink.BatchUpdateStatus(ctx, keys, models.MessageSendStatusSuccess, "", ""))

	// 命中的两条 → success；同消息其他接收者不受影响
	r1, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "bu-1", Receiver: "r-1"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusSuccess, r1.Status)
	require.NotNil(t, r1.SuccessTime, "success 应落 success_time")
	require.NotNil(t, r1.FirstSendTime, "首次发送应落 first_send_time")
	require.NotNil(t, r1.LastSendTime)

	r2, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "bu-1", Receiver: "r-2"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusSending, r2.Status, "未列入 keys 的接收者不受影响")

	// 失败带原因：failure_reason/error_message 落库，first_send_time 不被二次改写
	require.NoError(t, sink.BatchUpdateStatus(ctx,
		[]models.MessageRecordKey{{MessageID: "bu-2", Receiver: "r-1"}},
		models.MessageSendStatusFailed, models.FailureReasonConnError, "dial timeout"))

	r21, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "bu-2", Receiver: "r-1"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusFailed, r21.Status)
	assert.Equal(t, models.FailureReasonConnError, r21.FailureReason)
	assert.Equal(t, "dial timeout", r21.ErrorMessage)
	firstSend := *r21.FirstSendTime
	assert.WithinDuration(t, firstSend, *r21.FirstSendTime, 0, "first_send_time 只写一次")

	// 失败但 reason 为空：不应清空已有 failure_reason
	require.NoError(t, sink.BatchUpdateStatus(ctx,
		[]models.MessageRecordKey{{MessageID: "bu-2", Receiver: "r-1"}},
		models.MessageSendStatusRetrying, "", ""))
	r22, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "bu-2", Receiver: "r-1"})
	require.NoError(t, err)
	assert.Equal(t, models.FailureReasonConnError, r22.FailureReason, "空 reason 不应覆盖已有 failure_reason")

	// 空批次
	require.NoError(t, sink.BatchUpdateStatus(ctx, nil, models.MessageSendStatusSuccess, "", ""))
}

// TestMessageSink_ClaimStaleSending 认领超时 sending：状态守卫 + 认领归属返回
func TestMessageSink_ClaimStaleSending(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.MessageSendRecord{
		newMsgRecord("cl-1", "r", models.MessageSendStatusSending, base),
		newMsgRecord("cl-2", "r", models.MessageSendStatusSending, base.Add(time.Minute)),
		newMsgRecord("cl-3", "r", models.MessageSendStatusSuccess, base.Add(2*time.Minute)), // 非 sending
		newMsgRecord("cl-4", "r", models.MessageSendStatusFailed, base.Add(3*time.Minute)),  // 非 sending
	}
	require.NoError(t, sink.CreateBatch(ctx, seed))

	keys := []models.MessageRecordKey{
		{MessageID: "cl-1", Receiver: "r"},
		{MessageID: "cl-2", Receiver: "r"},
		{MessageID: "cl-3", Receiver: "r"},   // 状态守卫应使其认领失败
		{MessageID: "cl-404", Receiver: "r"}, // 不存在
	}
	claimed, err := sink.ClaimStaleSending(ctx, keys, models.MessageSendStatusAckTimeout,
		models.FailureReasonAckTimeout, "ack wait timeout")
	require.NoError(t, err)
	assert.ElementsMatch(t, []models.MessageRecordKey{
		{MessageID: "cl-1", Receiver: "r"},
		{MessageID: "cl-2", Receiver: "r"},
	}, claimed, "仅 sending 态被认领，非 sending 与缺失键静默跳过")

	c1, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "cl-1", Receiver: "r"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusAckTimeout, c1.Status)
	assert.Equal(t, models.FailureReasonAckTimeout, c1.FailureReason)
	assert.Equal(t, "ack wait timeout", c1.ErrorMessage)
	require.NotNil(t, c1.FirstSendTime)

	c3, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "cl-3", Receiver: "r"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusSuccess, c3.Status, "已被回报 success 的记录不应被认领改写")

	// 空批次
	claimed, err = sink.ClaimStaleSending(ctx, nil, models.MessageSendStatusAckTimeout, "", "")
	require.NoError(t, err)
	assert.Empty(t, claimed)
}

// TestMessageSink_IncrementRetry 单条 UPDATE 的重试状态机（sqlite 真实执行 JSON 数组追加）
func TestMessageSink_IncrementRetry(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	// A: 本次重试成功 → success + success_time
	seedA := newMsgRecord("ir-a", "r", models.MessageSendStatusRetrying, base)
	seedA.RetryCount = 1
	require.NoError(t, sink.CreateBatch(ctx, []*models.MessageSendRecord{seedA}))
	require.NoError(t, sink.IncrementRetry(ctx,
		models.MessageRecordKey{MessageID: "ir-a", Receiver: "r"},
		models.RetryAttempt{AttemptNumber: 2, Timestamp: time.Now(), Duration: 30 * time.Millisecond, Success: true}))

	gotA, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "ir-a", Receiver: "r"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusSuccess, gotA.Status, "成功重试应迁移 success")
	assert.Equal(t, 2, gotA.RetryCount)
	require.NotNil(t, gotA.SuccessTime)
	require.Len(t, gotA.RetryHistory, 1, "重试历史应追加一条（json_insert '$[#]' 路径）")
	assert.True(t, gotA.RetryHistory[0].Success)

	// B: 失败未耗尽（2 < max_retry=3）→ retrying，错误信息落库
	seedB := newMsgRecord("ir-b", "r", models.MessageSendStatusRetrying, base)
	seedB.RetryCount = 1
	seedB.FailureReason = models.FailureReasonNetworkError // 预置历史原因，验证未被覆盖
	require.NoError(t, sink.CreateBatch(ctx, []*models.MessageSendRecord{seedB}))
	require.NoError(t, sink.IncrementRetry(ctx,
		models.MessageRecordKey{MessageID: "ir-b", Receiver: "r"},
		models.RetryAttempt{AttemptNumber: 2, Error: "dial tcp: refused"}))

	gotB, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "ir-b", Receiver: "r"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusRetrying, gotB.Status, "未达 max_retry 应停留在 retrying")
	assert.Equal(t, 2, gotB.RetryCount)
	assert.Equal(t, "dial tcp: refused", gotB.ErrorMessage)
	assert.Equal(t, models.FailureReasonNetworkError, gotB.FailureReason, "未耗尽时不应改写 failure_reason")
	require.Len(t, gotB.RetryHistory, 1)
	assert.Equal(t, "dial tcp: refused", gotB.RetryHistory[0].Error)

	// C: 失败耗尽（3 >= max_retry=3）→ failed + failure_reason=max_retry
	seedC := newMsgRecord("ir-c", "r", models.MessageSendStatusRetrying, base)
	seedC.RetryCount = 2
	require.NoError(t, sink.CreateBatch(ctx, []*models.MessageSendRecord{seedC}))
	require.NoError(t, sink.IncrementRetry(ctx,
		models.MessageRecordKey{MessageID: "ir-c", Receiver: "r"},
		models.RetryAttempt{AttemptNumber: 3, Error: "boom"}))

	gotC, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "ir-c", Receiver: "r"})
	require.NoError(t, err)
	assert.Equal(t, models.MessageSendStatusFailed, gotC.Status, "达到 max_retry 应迁移 failed")
	assert.Equal(t, 3, gotC.RetryCount)
	assert.Equal(t, models.FailureReasonMaxRetry, gotC.FailureReason)
	assert.Equal(t, "boom", gotC.ErrorMessage)
	require.Len(t, gotC.RetryHistory, 1)

	// 追加第二次重试：历史按数组追加而非覆盖
	require.NoError(t, sink.IncrementRetry(ctx,
		models.MessageRecordKey{MessageID: "ir-c", Receiver: "r"},
		models.RetryAttempt{AttemptNumber: 4, Error: "again"}))
	gotC2, err := sink.FindByMessageID(ctx, models.MessageRecordKey{MessageID: "ir-c", Receiver: "r"})
	require.NoError(t, err)
	require.Len(t, gotC2.RetryHistory, 2, "JSON 数组应尾部追加")
	assert.Equal(t, "again", gotC2.RetryHistory[1].Error)
}

// TestMessageSink_GetStatistics 单条 GROUP BY 统计 + 全状态零填充
func TestMessageSink_GetStatistics(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.MessageSendRecord{
		newMsgRecord("st-1", "r", models.MessageSendStatusPending, base),
		newMsgRecord("st-2", "r", models.MessageSendStatusPending, base),
		newMsgRecord("st-3", "r", models.MessageSendStatusSuccess, base),
		newMsgRecord("st-4", "r", models.MessageSendStatusFailed, base),
	}
	require.NoError(t, sink.CreateBatch(ctx, seed))

	stats, err := sink.GetStatistics(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{
		"pending":      2,
		"sending":      0,
		"success":      1,
		"failed":       1,
		"retrying":     0,
		"ack_timeout":  0,
		"user_offline": 0,
		"expired":      0,
		"total":        4,
	}, stats)
}

// TestMessageSink_DeleteExpired 只删过记录（expires_at 非空且已过期）
func TestMessageSink_DeleteExpired(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	past := base.Add(-2 * time.Hour)
	future := base.Add(2 * time.Hour)
	seed := []*models.MessageSendRecord{
		newMsgRecord("de-1", "r", models.MessageSendStatusSuccess, base),
		newMsgRecord("de-2", "r", models.MessageSendStatusSuccess, base),
		newMsgRecord("de-3", "r", models.MessageSendStatusPending, base), // 无过期时间
	}
	seed[0].ExpiresAt = &past
	seed[1].ExpiresAt = &future
	require.NoError(t, sink.CreateBatch(ctx, seed))

	deleted, err := sink.DeleteExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	remaining, err := sink.QueryRecords(ctx, &models.MessageRecordFilter{})
	require.NoError(t, err)
	assert.Len(t, remaining, 2)
}

// TestMessageSink_Delete/DeleteByMessageID 删除路径
func TestMessageSink_Delete(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.MessageSendRecord{
		newMsgRecord("dl-1", "r-1", models.MessageSendStatusSuccess, base),
		newMsgRecord("dl-1", "r-2", models.MessageSendStatusSuccess, base),
		newMsgRecord("dl-2", "r-1", models.MessageSendStatusSuccess, base),
	}
	require.NoError(t, sink.CreateBatch(ctx, seed))

	require.NoError(t, sink.Delete(ctx, seed[2].ID))
	_, err := sink.FindByID(ctx, seed[2].ID)
	require.Error(t, err)

	// 按消息 ID 删除：同消息的所有接收者记录一并清理
	require.NoError(t, sink.DeleteByMessageID(ctx, "dl-1"))
	remaining, err := sink.QueryRecords(ctx, &models.MessageRecordFilter{})
	require.NoError(t, err)
	assert.Empty(t, remaining)
}

// TestMessageSink_CleanupOld 只清理窗口前的终态记录（success/failed/expired）
func TestMessageSink_CleanupOld(t *testing.T) {
	sink, _ := newMsgSink(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-3 * 24 * time.Hour)
	recent := now.Add(-1 * time.Hour)
	past := now.Add(-2 * time.Hour)

	seed := []*models.MessageSendRecord{
		newMsgRecord("co-1", "r", models.MessageSendStatusSuccess, old),    // 命中：老 + success
		newMsgRecord("co-2", "r", models.MessageSendStatusFailed, old),     // 命中：老 + failed
		newMsgRecord("co-3", "r", models.MessageSendStatusExpired, old),    // 命中：老 + expired
		newMsgRecord("co-4", "r", models.MessageSendStatusRetrying, old),   // 保留：非终态
		newMsgRecord("co-5", "r", models.MessageSendStatusSuccess, recent), // 保留：窗口内
	}
	seed[2].ExpiresAt = &past
	require.NoError(t, sink.CreateBatch(ctx, seed))

	deleted, err := sink.CleanupOld(ctx, now.Add(-2*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(3), deleted)

	remaining, err := sink.QueryRecords(ctx, &models.MessageRecordFilter{})
	require.NoError(t, err)
	assert.Len(t, remaining, 2)
}

// TestMessageSink_GetDB 返回底层 GORM 句柄
func TestMessageSink_GetDB(t *testing.T) {
	sink, db := newMsgSink(t)
	assert.Same(t, db, sink.GetDB())
}

// TestMessageSink_Close 关闭仓库应正常返回
func TestMessageSink_Close(t *testing.T) {
	sink, _ := newMsgSink(t)
	require.NoError(t, sink.Close())
}

// ============================================================================
// 测试辅助
// ============================================================================

func statusPtr(s models.MessageSendStatus) *models.MessageSendStatus { return &s }

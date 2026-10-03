/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 17:10:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 17:10:02
 * @FilePath: \go-wsc\adapter\gorm\offline_queue_test.go
 * @Description: 离线消息数据库仓库的用例（sqlite 真实执行）
 *
 * 覆盖：
 *   - Save/BatchSave：单条与批量写入
 *   - QueryMessages：接收者/发送者视角、app_id 隔离、namespace 通配（ns=''）、
 *     状态集合、过期过滤、ExcludeExhausted 毒消息死信、cursor 分页
 *     （含悬空游标回退首屏）、limit、created_at 升序
 *   - DeleteByMessageIDs：按 app+ns+receiver 精确删除
 *   - GetCountByReceiver/GetCountBySender：仅待推送状态、含通配 ns
 *   - ClearByReceiver：整租户清理
 *   - UpdatePushStatus：first_push_at 只写一次（COALESCE）、
 *     failed 递增 retry_count、success 清空错误
 *   - DeleteExpired/CleanupOld：分批清理语义
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

// newOfflineStore 建库 + 建表 + 建仓库（无自动清理）
func newOfflineStore(t *testing.T) (*OfflineStore, *gorm.DB) {
	t.Helper()
	db := newTestDB(t, &models.OfflineMessageRecord{})
	store, _ := NewOfflineStore(db, nil, spi.NewDefaultLogger()).(*OfflineStore)
	return store, db
}

// newOfflineRecord 构造一条离线消息（CreatedAt 显式指定保证游标/排序用例确定性）
func newOfflineRecord(messageID, sender, receiver, appID, namespace string, createdAt time.Time) *models.OfflineMessageRecord {
	return &models.OfflineMessageRecord{
		MessageID:      messageID,
		Sender:         sender,
		Receiver:       receiver,
		AppID:          appID,
		Namespace:      namespace,
		SessionID:      "sess-1",
		CompressedData: []byte("compressed-payload"),
		Status:         models.MessageSendStatusUserOffline,
		ScheduledAt:    createdAt,
		// 显式写入 created_at：gorm autoCreateTime 仅在零值时回填"当前时间"，
		// 不显式赋值则老化类用例（CleanupOld 等）永远无法命中 created_at < before
		CreatedAt: createdAt,
		ExpireAt:  createdAt.Add(24 * time.Hour),
		MaxRetry:  3,
	}
}

// TestOfflineStore_Save 单条保存与读回
func TestOfflineStore_Save(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	createdAt := time.Now().Add(-time.Minute)

	rec := newOfflineRecord("om-1", "alice", "bob", "app-1", "ns-1", createdAt)
	require.NoError(t, store.Save(ctx, rec))

	got, err := store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "om-1", got[0].MessageID)
	assert.Equal(t, []byte("compressed-payload"), got[0].CompressedData)
}

// TestOfflineStore_BatchSave 批量保存（空批次不报错）
func TestOfflineStore_BatchSave(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.BatchSave(ctx, nil))

	records := []*models.OfflineMessageRecord{
		newOfflineRecord("om-b-1", "s", "r", "app-1", "ns-1", now),
		newOfflineRecord("om-b-2", "s", "r", "app-1", "ns-1", now.Add(time.Second)),
		newOfflineRecord("om-b-3", "s", "r", "app-1", "ns-1", now.Add(2*time.Second)),
	}
	require.NoError(t, store.BatchSave(ctx, records))

	count, err := store.GetCountByReceiver(ctx, "app-1", "ns-1", "r")
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

// TestOfflineStore_QueryMessages_FilterMatrix 查询过滤矩阵：
// 角色视角 / app 隔离 / namespace 通配 / 状态 / 过期 / 排序 / limit
func TestOfflineStore_QueryMessages_FilterMatrix(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.OfflineMessageRecord{
		newOfflineRecord("q-1", "alice", "bob", "app-1", "ns-1", base),
		// 通配群组消息：namespace=''，任何租户上线都应命中
		newOfflineRecord("q-2", "alice", "bob", "app-1", "", base.Add(time.Second)),
		newOfflineRecord("q-3", "alice", "bob", "app-1", "ns-2", base.Add(2*time.Second)),
		newOfflineRecord("q-4", "alice", "bob", "app-2", "ns-1", base.Add(3*time.Second)),
		// 发送者视角的记录
		newOfflineRecord("q-5", "bob", "carol", "app-1", "ns-1", base.Add(4*time.Second)),
	}
	// q-3 置为已成功推送（不在待推送状态集合）
	seed[2].Status = models.MessageSendStatusSuccess
	require.NoError(t, store.BatchSave(ctx, seed))

	// 接收者视角 + ns 过滤：ns-1 命中 ns-1 与通配 ns=''
	got, err := store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"q-1", "q-2"}, []string{got[0].MessageID, got[1].MessageID},
		"created_at 升序 + 通配 namespace='' 命中")

	// namespace 为空：查询所有命名空间（历史行为）
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver, AppID: "app-1",
	})
	require.NoError(t, err)
	assert.Len(t, got, 2, "ns-2 已推送成功，剩余 q-1/q-2")

	// app 隔离：app-2 的记录不串扰
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver, AppID: "app-2", Namespace: "ns-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "q-4", got[0].MessageID)

	// 不指定 app：跨 app 全量
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver,
	})
	require.NoError(t, err)
	assert.Len(t, got, 3)

	// 发送者视角
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "alice", Role: spi.MessageRoleSender, AppID: "app-1",
	})
	require.NoError(t, err)
	assert.Len(t, got, 2)

	// limit 截断
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver, Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "q-1", got[0].MessageID)

	// 已过期消息不返回（expire_at > now 过滤）
	expired := newOfflineRecord("q-exp", "alice", "bob", "app-1", "ns-1", base.Add(5*time.Second))
	expired.ExpireAt = base // 已过期
	require.NoError(t, store.Save(ctx, expired))
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1",
	})
	require.NoError(t, err)
	assert.Len(t, got, 2, "过期消息不应进入推送流程")

	// 指定状态集合：显式查已成功的 q-3
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "bob", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-2",
		Statuses: []models.MessageSendStatus{models.MessageSendStatusSuccess},
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "q-3", got[0].MessageID)
}

// TestOfflineStore_QueryMessages_ExcludeExhausted 毒消息死信过滤
func TestOfflineStore_QueryMessages_ExcludeExhausted(t *testing.T) {
	store, db := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.OfflineMessageRecord{
		newOfflineRecord("ex-1", "s", "r", "app-1", "ns-1", base),
		// 耗尽：retry_count >= max_retry
		newOfflineRecord("ex-2", "s", "r", "app-1", "ns-1", base.Add(time.Second)),
		// max_retry <= 0 视为不限制
		newOfflineRecord("ex-3", "s", "r", "app-1", "ns-1", base.Add(2*time.Second)),
	}
	seed[1].RetryCount = 3
	seed[2].RetryCount = 9
	require.NoError(t, store.BatchSave(ctx, seed))
	// max_retry=0（不限制）经 gorm 结构化插入会被列默认值 3 劫持，
	// raw UPDATE 置 0 以覆盖 reader 的 max_retry<=0 分支（重试上限语义见 offline_queue.go ExcludeExhausted 注释）
	require.NoError(t, db.Model(&models.OfflineMessageRecord{}).
		Where("message_id = ?", "ex-3").Update("max_retry", 0).Error)

	got, err := store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "r", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1",
		ExcludeExhausted: true,
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "ex-1", got[0].MessageID)
	assert.Equal(t, "ex-3", got[1].MessageID, "max_retry<=0 不视为耗尽")

	// 不过滤时全量可见
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "r", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1",
	})
	require.NoError(t, err)
	assert.Len(t, got, 3)
}

// TestOfflineStore_QueryMessages_Cursor 游标分页：从 cursor 之后读取；悬空游标回退首屏
func TestOfflineStore_QueryMessages_Cursor(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	require.NoError(t, store.BatchSave(ctx, []*models.OfflineMessageRecord{
		newOfflineRecord("pg-1", "s", "r", "app-1", "ns-1", base),
		newOfflineRecord("pg-2", "s", "r", "app-1", "ns-1", base.Add(time.Second)),
		newOfflineRecord("pg-3", "s", "r", "app-1", "ns-1", base.Add(2*time.Second)),
	}))

	// 从 pg-1 之后读取
	got, err := store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "r", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1", Cursor: "pg-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "pg-2", got[0].MessageID)
	assert.Equal(t, "pg-3", got[1].MessageID)

	// 末页：从 pg-3 之后 → 空
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "r", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1", Cursor: "pg-3",
	})
	require.NoError(t, err)
	assert.Empty(t, got)

	// 悬空游标（消息已 ACK 删除）：子查询 NULL → 回退首屏而非静默空集
	require.NoError(t, store.DeleteByMessageIDs(ctx, "app-1", "ns-1", "r", []string{"pg-1"}))
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "r", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1", Cursor: "pg-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 2, "悬空游标应回退首屏，从剩余消息头部开始")
	assert.Equal(t, "pg-2", got[0].MessageID)
}

// TestOfflineStore_DeleteByMessageIDs 精确删除：app+ns+receiver 三元组不串删
func TestOfflineStore_DeleteByMessageIDs(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	require.NoError(t, store.BatchSave(ctx, []*models.OfflineMessageRecord{
		newOfflineRecord("dd-1", "s", "r-1", "app-1", "ns-1", base),
		// 同 message_id 不同接收者
		newOfflineRecord("dd-1", "s", "r-2", "app-1", "ns-1", base.Add(time.Second)),
		// 同接收者不同命名空间（ns-2）
		newOfflineRecord("dd-2", "s", "r-1", "app-1", "ns-2", base.Add(2*time.Second)),
		// 同接收者不同应用
		newOfflineRecord("dd-3", "s", "r-1", "app-2", "ns-1", base.Add(3*time.Second)),
	}))

	// 空列表不报错
	require.NoError(t, store.DeleteByMessageIDs(ctx, "app-1", "ns-1", "r-1", nil))

	// 只删 (app-1, ns-1, r-1) 的 dd-1
	require.NoError(t, store.DeleteByMessageIDs(ctx, "app-1", "ns-1", "r-1", []string{"dd-1"}))

	got, err := store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "r-2", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "dd-1", got[0].MessageID, "其他接收者的记录不应被删")

	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "r-1", Role: spi.MessageRoleReceiver, AppID: "app-1", Namespace: "ns-2",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "dd-2", got[0].MessageID, "其他命名空间的记录不应被删")
}

// TestOfflineStore_Counts 计数：仅待推送状态、未过期、含通配 ns
func TestOfflineStore_Counts(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.OfflineMessageRecord{
		newOfflineRecord("ct-1", "alice", "bob", "app-1", "ns-1", base),
		newOfflineRecord("ct-2", "alice", "bob", "app-1", "", base.Add(time.Second)), // 通配 ns
		newOfflineRecord("ct-3", "carol", "bob", "app-1", "ns-1", base.Add(2*time.Second)),
		// 已成功推送：不计入
		newOfflineRecord("ct-4", "alice", "bob", "app-1", "ns-1", base.Add(3*time.Second)),
		// 已过期：不计入
		newOfflineRecord("ct-5", "alice", "bob", "app-1", "ns-1", base.Add(4*time.Second)),
		// 另一命名空间：不计入 ns-1 计数
		newOfflineRecord("ct-6", "alice", "bob", "app-1", "ns-9", base.Add(5*time.Second)),
	}
	seed[3].Status = models.MessageSendStatusSuccess
	seed[4].ExpireAt = base
	require.NoError(t, store.BatchSave(ctx, seed))

	// 接收者计数：ns-1 两条（ct-1 来自 alice、ct-3 来自 carol）+ 通配 ns='' 一条
	rc, err := store.GetCountByReceiver(ctx, "app-1", "ns-1", "bob")
	require.NoError(t, err)
	assert.Equal(t, int64(3), rc)

	// 发送者计数：alice 的 ns-1/通配消息
	sc, err := store.GetCountBySender(ctx, "app-1", "ns-1", "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(2), sc)

	// 空用户
	rc, err = store.GetCountByReceiver(ctx, "app-1", "ns-1", "ghost")
	require.NoError(t, err)
	assert.Zero(t, rc)
}

// TestOfflineStore_ClearByReceiver 整租户清理：含通配 ns 消息
func TestOfflineStore_ClearByReceiver(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	require.NoError(t, store.BatchSave(ctx, []*models.OfflineMessageRecord{
		newOfflineRecord("cr-1", "s", "victim", "app-1", "ns-1", base),
		newOfflineRecord("cr-2", "s", "victim", "app-1", "", base.Add(time.Second)), // 通配 ns
		newOfflineRecord("cr-3", "s", "victim", "app-2", "ns-1", base.Add(2*time.Second)),
		newOfflineRecord("cr-4", "s", "other", "app-1", "ns-1", base.Add(3*time.Second)),
	}))

	require.NoError(t, store.ClearByReceiver(ctx, "app-1", "ns-1", "victim"))

	// app-1 维度（含通配 ns）清空
	got, err := store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "victim", Role: spi.MessageRoleReceiver, AppID: "app-1",
	})
	require.NoError(t, err)
	assert.Empty(t, got)

	// 其他应用/接收者不受影响
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "victim", Role: spi.MessageRoleReceiver, AppID: "app-2",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	got, err = store.QueryMessages(ctx, &spi.OfflineMessageFilter{
		UserID: "other", Role: spi.MessageRoleReceiver, AppID: "app-1",
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
}

// TestOfflineStore_UpdatePushStatus 推送状态更新：COALESCE 首推时间、失败重试计数、成功清错
func TestOfflineStore_UpdatePushStatus(t *testing.T) {
	store, db := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	seed := []*models.OfflineMessageRecord{
		newOfflineRecord("ps-1", "s", "r", "app-1", "ns-1", base),
		newOfflineRecord("ps-2", "s", "r", "app-1", "ns-1", base.Add(time.Second)),
	}
	require.NoError(t, store.BatchSave(ctx, seed))

	// 空列表不报错
	require.NoError(t, store.UpdatePushStatus(ctx, nil, models.MessageSendStatusSuccess, ""))

	// 失败：retry_count 递增、错误信息落库
	require.NoError(t, store.UpdatePushStatus(ctx, []string{"ps-1"},
		models.MessageSendStatusFailed, "push refused"))
	var got models.OfflineMessageRecord
	require.NoError(t, db.WithContext(ctx).
		Where("message_id = ?", "ps-1").First(&got).Error)
	assert.Equal(t, models.MessageSendStatusFailed, got.Status)
	assert.Equal(t, 1, got.RetryCount)
	assert.Equal(t, "push refused", got.ErrorMessage)
	require.NotNil(t, got.FirstPushAt, "首次推送应落 first_push_at")
	require.NotNil(t, got.LastPushAt)

	// 再次失败：retry_count 继续递增、first_push_at 不变
	// gorm First 复用结构体会残留上次主键（WHERE 追加 AND id=N），查询前清零
	firstPush := *got.FirstPushAt
	got = models.OfflineMessageRecord{}
	require.NoError(t, store.UpdatePushStatus(ctx, []string{"ps-1"},
		models.MessageSendStatusFailed, "push refused again"))
	require.NoError(t, db.WithContext(ctx).
		Where("message_id = ?", "ps-1").First(&got).Error)
	assert.Equal(t, 2, got.RetryCount)
	assert.WithinDuration(t, firstPush, *got.FirstPushAt, 0, "first_push_at 只写一次")

	// 成功：清空错误信息
	got = models.OfflineMessageRecord{}
	require.NoError(t, store.UpdatePushStatus(ctx, []string{"ps-2"},
		models.MessageSendStatusSuccess, ""))
	require.NoError(t, db.WithContext(ctx).
		Where("message_id = ?", "ps-2").First(&got).Error)
	assert.Equal(t, models.MessageSendStatusSuccess, got.Status)
	assert.Empty(t, got.ErrorMessage)
	require.NotNil(t, got.FirstPushAt)
	require.NotNil(t, got.LastPushAt)
}

// TestOfflineStore_DeleteExpired 只删已过期消息
func TestOfflineStore_DeleteExpired(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	base := time.Now().Add(-2 * time.Hour)

	seed := []*models.OfflineMessageRecord{
		newOfflineRecord("oe-1", "s", "r", "app-1", "ns-1", base),
		newOfflineRecord("oe-2", "s", "r", "app-1", "ns-1", base.Add(time.Second)),
	}
	seed[0].ExpireAt = base.Add(-time.Hour) // 已过期
	require.NoError(t, store.BatchSave(ctx, seed))

	deleted, err := store.DeleteExpired(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	count, err := store.GetCountByReceiver(ctx, "app-1", "ns-1", "r")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// TestOfflineStore_CleanupOld 老记录清理：已成功或已过期才回收
func TestOfflineStore_CleanupOld(t *testing.T) {
	store, _ := newOfflineStore(t)
	ctx := context.Background()
	now := time.Now()
	old := now.Add(-3 * 24 * time.Hour)
	recent := now.Add(-1 * time.Hour)

	seed := []*models.OfflineMessageRecord{
		newOfflineRecord("oc-1", "s", "r", "app-1", "ns-1", old),    // 老 + 待推送（未过期）→ 保留
		newOfflineRecord("oc-2", "s", "r", "app-1", "ns-1", old),    // 老 + 已成功 → 清理
		newOfflineRecord("oc-3", "s", "r", "app-1", "ns-1", old),    // 老 + 已过期 → 清理
		newOfflineRecord("oc-4", "s", "r", "app-1", "ns-1", recent), // 近期 → 保留
	}
	// helper 默认 ExpireAt=created+24h：3 天前创建的记录默认已过期 2 天，
	// 显式延后才能构造"老但仍在投递窗内"的保留用例
	seed[0].ExpireAt = now.Add(24 * time.Hour)
	seed[1].Status = models.MessageSendStatusSuccess
	seed[2].ExpireAt = now.Add(-2 * time.Hour)
	require.NoError(t, store.BatchSave(ctx, seed))

	deleted, err := store.CleanupOld(ctx, now.Add(-2*24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	count, err := store.GetCountByReceiver(ctx, "app-1", "ns-1", "r")
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "老且待推送的消息保留（仍需投递）")
}

// TestOfflineStore_Close 关闭仓库应正常返回
func TestOfflineStore_Close(t *testing.T) {
	store, _ := newOfflineStore(t)
	require.NoError(t, store.Close())
}

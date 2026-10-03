/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 17:10:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 17:10:02
 * @FilePath: \go-wsc\adapter\gorm\connection_quality_test.go
 * @Description: 连接质量记录仓库的用例（sqlite 真实执行，覆盖攒批合并 SQL）
 *
 * 覆盖：
 *   - Upsert：首连建行（评分默认 100/活跃时间兜底/租户归一化）、
 *     冲突时 reconnect_count 表名限定自增、user_id/last_active_at 刷新
 *   - BatchUpdateHeartbeats：last_active_at 刷新 + avg/max/min_ping_ms
 *     移动平均（0.7/0.3 权重）与极值维护、字段级合并、全无效批次无操作
 *   - BatchIncrementStats：四列自增式 CASE 合并、批内同连接累加、
 *     零增量列跳过、全零批次无操作
 *   - BatchAddErrors：error_count 累加 + last_error/last_error_at 取最新
 *   - BatchFinalizeOnDisconnect：终评按 duration 梯度落库、缺质量行的连接跳过
 *   - 查询：GetByConnectionID/GetByUserID/高错误率/频繁重连
 *   - CleanupInactiveRecords：按 last_active_at 分批清理
 *   - WithTableName：自定义表名隔离
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// customQualityTable 与 models.ConnectionQuality 同构但独立表名（WithTableName 隔离用）
// 索引名是 schema 级对象（模型注释同此认知）：同库双表直接复用模型 tag 会因固定命名
// 复合索引 idx_quality_app_namespace 重名建表失败，此处以等列集合显式重建，
// 唯一索引走 gorm 表内自动命名（Upsert 的 ON CONFLICT(connection_id) 依赖项）
type customQualityTable struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	ConnectionID string `gorm:"column:connection_id;size:64;uniqueIndex;not null"`
	UserID       string `gorm:"column:user_id;size:64"`
	AppID        string `gorm:"column:app_id;size:64"`
	Namespace    string `gorm:"column:namespace;size:64"`

	ReconnectCount   int     `gorm:"column:reconnect_count;default:0"`
	MessagesSent     int64   `gorm:"column:messages_sent;default:0"`
	MessagesReceived int64   `gorm:"column:messages_received;default:0"`
	BytesSent        int64   `gorm:"column:bytes_sent;default:0"`
	BytesReceived    int64   `gorm:"column:bytes_received;default:0"`
	AveragePingMs    float64 `gorm:"column:average_ping_ms"`
	MaxPingMs        float64 `gorm:"column:max_ping_ms"`
	MinPingMs        float64 `gorm:"column:min_ping_ms"`
	PacketLossRate   float64 `gorm:"column:packet_loss_rate"`

	ErrorCount   int        `gorm:"column:error_count;default:0"`
	LastError    string     `gorm:"column:last_error;type:text"`
	LastErrorAt  *time.Time `gorm:"column:last_error_at"`
	QualityScore float64    `gorm:"column:quality_score"`
	LastActiveAt *time.Time `gorm:"column:last_active_at"`

	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (customQualityTable) TableName() string { return "wsc_connection_qualities_custom" }

// newQualityStore 建库 + 建表 + 建仓库（无自动清理）
func newQualityStore(t *testing.T) (*ConnectionQualityStore, *gorm.DB) {
	t.Helper()
	db := newTestDB(t, &models.ConnectionQuality{})
	return NewConnectionQualityStore(db, nil, spi.NewDefaultLogger()), db
}

// TestConnectionQualityStore_Upsert_Insert 首次连接：默认评分 100、活跃时间兜底、租户维度归一化
func TestConnectionQualityStore_Upsert_Insert(t *testing.T) {
	store, _ := newQualityStore(t)
	ctx := context.Background()
	before := time.Now()

	q := &models.ConnectionQuality{ConnectionID: "q-ins-1", UserID: "user-1"}
	require.NoError(t, store.Upsert(ctx, q))

	got, err := store.GetByConnectionID(ctx, "q-ins-1")
	require.NoError(t, err)
	assert.Equal(t, constants.DefaultAppID, got.AppID, "空 app_id 应归一化")
	assert.Equal(t, constants.DefaultNamespace, got.Namespace, "空 namespace 应归一化")
	assert.Equal(t, float64(100), got.QualityScore, "零评分应默认 100")
	require.NotNil(t, got.LastActiveAt)
	assert.False(t, got.LastActiveAt.Before(before), "缺省 last_active_at 应兜底为当前时间")
	assert.Zero(t, got.ReconnectCount)
}

// TestConnectionQualityStore_Upsert_Guard nil 记录与空 connection_id 直接报错
func TestConnectionQualityStore_Upsert_Guard(t *testing.T) {
	store, _ := newQualityStore(t)
	ctx := context.Background()
	require.Error(t, store.Upsert(ctx, nil))
	require.Error(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: ""}))
}

// TestConnectionQualityStore_Upsert_ReconnectIncrement 冲突时重连次数自增、
// user_id/last_active_at 刷新、评分保留原值（excluded 无 score 列）
//
// 注：reconnect_count 用「表名.列名 + 1」自引用写法（PG ON CONFLICT 下防 42702
// 歧义），sqlite 同样接受表名限定引用，此用例顺带回归该写法的可移植性
func TestConnectionQualityStore_Upsert_ReconnectIncrement(t *testing.T) {
	store, _ := newQualityStore(t)
	ctx := context.Background()

	first := &models.ConnectionQuality{ConnectionID: "q-re-1", UserID: "user-1", QualityScore: 88}
	require.NoError(t, store.Upsert(ctx, first))

	// 传入的 QualityScore 为 0 → 被兜底为 100，但冲突分支不更新 score，行内保留 88
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{
		ConnectionID: "q-re-1", UserID: "user-1-new",
	}))

	got, err := store.GetByConnectionID(ctx, "q-re-1")
	require.NoError(t, err)
	assert.Equal(t, 1, got.ReconnectCount, "重连一次应递增为 1")
	assert.Equal(t, "user-1-new", got.UserID, "冲突分支应刷新 user_id")
	assert.Equal(t, float64(88), got.QualityScore, "评分不在冲突更新列，应保留首次值")

	// 再次重连 → 2
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-re-1", UserID: "user-1"}))
	got, err = store.GetByConnectionID(ctx, "q-re-1")
	require.NoError(t, err)
	assert.Equal(t, 2, got.ReconnectCount)
}

// TestConnectionQualityStore_BatchUpdateHeartbeats 心跳批量更新：
// 移动平均 avg=旧*0.7+新*0.3、max/min 极值维护、last_active_at 刷新
func TestConnectionQualityStore_BatchUpdateHeartbeats(t *testing.T) {
	store, db := newQualityStore(t)
	ctx := context.Background()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{
		ConnectionID: "q-hb-1", UserID: "u", // 首连建行
	}))
	// 预置统计基线：avg=100 max=200 min=50
	require.NoError(t, db.WithContext(ctx).Model(&models.ConnectionQuality{}).
		Where("connection_id = ?", "q-hb-1").
		Updates(map[string]any{
			"average_ping_ms": 100, "max_ping_ms": 200, "min_ping_ms": 50,
		}).Error)
	// 第二条连接零基线
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-hb-2", UserID: "u"}))

	pingAt := time.Now().Add(-5 * time.Second)
	require.NoError(t, store.BatchUpdateHeartbeats(ctx, []*models.HeartbeatUpdateEntry{
		{ConnectionID: "q-hb-1", PingTime: &pingAt, PingMs: 50},
		{ConnectionID: "q-hb-2", PingTime: &pingAt, PingMs: 60},
	}))

	q1, err := store.GetByConnectionID(ctx, "q-hb-1")
	require.NoError(t, err)
	assert.InDelta(t, 85.0, q1.AveragePingMs, 0.001, "100*0.7+50*0.3=85（移动平均）")
	assert.Equal(t, float64(200), q1.MaxPingMs, "新样本 50 < 旧 max 200，保持")
	assert.Equal(t, float64(50), q1.MinPingMs, "新样本 50 不小于旧 min 50，保持")
	require.NotNil(t, q1.LastActiveAt)
	assert.WithinDuration(t, pingAt, *q1.LastActiveAt, time.Second)

	q2, err := store.GetByConnectionID(ctx, "q-hb-2")
	require.NoError(t, err)
	assert.InDelta(t, 60.0, q2.AveragePingMs, 0.001, "零基线首次采样直接赋值")
	assert.Equal(t, float64(60), q2.MaxPingMs)
	assert.Equal(t, float64(60), q2.MinPingMs)
}

// TestConnectionQualityStore_BatchUpdateHeartbeats_FieldMerge 字段级合并与无效批次
func TestConnectionQualityStore_BatchUpdateHeartbeats_FieldMerge(t *testing.T) {
	store, db := newQualityStore(t)
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)

	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-hb-3", UserID: "u"}))
	require.NoError(t, db.WithContext(ctx).Model(&models.ConnectionQuality{}).
		Where("connection_id = ?", "q-hb-3").
		Updates(map[string]any{"last_active_at": base, "average_ping_ms": 10}).Error)

	// 只有 PingMs（PingTime nil）：仅刷新统计列，last_active_at 不动
	require.NoError(t, store.BatchUpdateHeartbeats(ctx, []*models.HeartbeatUpdateEntry{
		{ConnectionID: "q-hb-3", PingMs: 30},
	}))
	got, err := store.GetByConnectionID(ctx, "q-hb-3")
	require.NoError(t, err)
	assert.InDelta(t, 10*0.7+30*0.3, got.AveragePingMs, 0.001)
	require.NotNil(t, got.LastActiveAt)
	assert.WithinDuration(t, base, *got.LastActiveAt, time.Second, "无 PingTime 不应改写 last_active_at")

	// 全无效批次（PingTime nil + PingMs 0）：无操作
	before, err := store.GetByConnectionID(ctx, "q-hb-3")
	require.NoError(t, err)
	require.NoError(t, store.BatchUpdateHeartbeats(ctx, []*models.HeartbeatUpdateEntry{
		{ConnectionID: "q-hb-3"},
	}))
	after, err := store.GetByConnectionID(ctx, "q-hb-3")
	require.NoError(t, err)
	assert.InDelta(t, before.AveragePingMs, after.AveragePingMs, 0.001)

	// 空批次
	require.NoError(t, store.BatchUpdateHeartbeats(ctx, nil))
}

// TestConnectionQualityStore_BatchIncrementStats 四列统计自增：
// 批内同连接累加、无增量行保持、零增量批次无操作
func TestConnectionQualityStore_BatchIncrementStats(t *testing.T) {
	store, _ := newQualityStore(t)
	ctx := context.Background()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-stat-1", UserID: "u"}))
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-stat-2", UserID: "u"}))
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-stat-3", UserID: "u"}))

	// conn1 两条 entry 批内合并（2+3=5），conn2 只有接收增量，conn3 全零增量
	require.NoError(t, store.BatchIncrementStats(ctx, []*models.StatsIncrementEntry{
		{ConnectionID: "q-stat-1", MessagesSent: 2, MessagesReceived: 1, BytesSent: 100, BytesReceived: 50},
		{ConnectionID: "q-stat-1", MessagesSent: 3, BytesReceived: 10},
		{ConnectionID: "q-stat-2", MessagesReceived: 4, BytesReceived: 60},
		{ConnectionID: "q-stat-3"},
	}))

	q1, err := store.GetByConnectionID(ctx, "q-stat-1")
	require.NoError(t, err)
	assert.Equal(t, int64(5), q1.MessagesSent, "批内同连接 2+3 累加")
	assert.Equal(t, int64(1), q1.MessagesReceived)
	assert.Equal(t, int64(100), q1.BytesSent)
	assert.Equal(t, int64(60), q1.BytesReceived, "50+10 累加")

	q2, err := store.GetByConnectionID(ctx, "q-stat-2")
	require.NoError(t, err)
	assert.Zero(t, q2.MessagesSent, "无发送增量的行 ELSE 0 保持不变")
	assert.Equal(t, int64(4), q2.MessagesReceived)
	assert.Equal(t, int64(60), q2.BytesReceived)

	q3, err := store.GetByConnectionID(ctx, "q-stat-3")
	require.NoError(t, err)
	assert.Zero(t, q3.MessagesSent)
	assert.Zero(t, q3.BytesReceived)

	// 追加一轮验证跨批累计
	require.NoError(t, store.BatchIncrementStats(ctx, []*models.StatsIncrementEntry{
		{ConnectionID: "q-stat-1", MessagesSent: 1},
	}))
	q1, err = store.GetByConnectionID(ctx, "q-stat-1")
	require.NoError(t, err)
	assert.Equal(t, int64(6), q1.MessagesSent, "跨批次应基于列当前值继续自增")

	// 全零批次无操作
	require.NoError(t, store.BatchIncrementStats(ctx, []*models.StatsIncrementEntry{{ConnectionID: "q-stat-1"}}))
	// 空批次
	require.NoError(t, store.BatchIncrementStats(ctx, nil))
}

// TestConnectionQualityStore_BatchAddErrors 错误合并：次数累加、最新错误覆盖
func TestConnectionQualityStore_BatchAddErrors(t *testing.T) {
	store, db := newQualityStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-err-1", UserID: "u"}))
	require.NoError(t, db.WithContext(ctx).Model(&models.ConnectionQuality{}).
		Where("connection_id = ?", "q-err-1").
		Update("error_count", 1).Error) // 预置历史错误 1 次
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-err-2", UserID: "u"}))

	t1 := now.Add(-2 * time.Minute)
	t2 := now.Add(-1 * time.Minute)
	t3 := now
	require.NoError(t, store.BatchAddErrors(ctx, []*models.ErrorUpdateEntry{
		{ConnectionID: "q-err-1", ErrorCount: 1, LastError: "e1", LastErrorAt: t1},
		// 同连接第二条：批内合并后 count=3、最新错误 e2
		{ConnectionID: "q-err-1", ErrorCount: 2, LastError: "e2", LastErrorAt: t2},
		{ConnectionID: "q-err-2", ErrorCount: 5, LastError: "x", LastErrorAt: t3},
	}))

	q1, err := store.GetByConnectionID(ctx, "q-err-1")
	require.NoError(t, err)
	assert.EqualValues(t, 4, q1.ErrorCount, "历史 1 + 批内合并 3 = 4")
	assert.Equal(t, "e2", q1.LastError, "最新一次错误覆盖")
	require.NotNil(t, q1.LastErrorAt)
	assert.WithinDuration(t, t2, *q1.LastErrorAt, time.Second)

	q2, err := store.GetByConnectionID(ctx, "q-err-2")
	require.NoError(t, err)
	assert.EqualValues(t, 5, q2.ErrorCount)
	assert.Equal(t, "x", q2.LastError)

	// 空批次
	require.NoError(t, store.BatchAddErrors(ctx, nil))
}

// TestConnectionQualityStore_BatchFinalizeOnDisconnect 断开终评：
// duration 梯度（≥30min 不扣/<30s 扣 20）、ConnectedAt 零值按 duration=0、
// 无质量行的连接静默跳过
func TestConnectionQualityStore_BatchFinalizeOnDisconnect(t *testing.T) {
	store, db := newQualityStore(t)
	ctx := context.Background()
	now := time.Now()

	// 三条连接：avg=60 → LiveScore 96；零值 → LiveScore 100
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-fin-1", UserID: "u"}))
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-fin-2", UserID: "u"}))
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-fin-3", UserID: "u"}))
	require.NoError(t, db.WithContext(ctx).Model(&models.ConnectionQuality{}).
		Where("connection_id IN ?", []string{"q-fin-1", "q-fin-3"}).
		Update("average_ping_ms", 60).Error) // LiveScore: 100-4=96

	require.NoError(t, store.BatchFinalizeOnDisconnect(ctx, []*models.DisconnectionEntry{
		// q-fin-1：1 小时时长 ≥ 30min 不扣 → 96
		{ConnectionID: "q-fin-1", ConnectedAt: now.Add(-time.Hour), DisconnectedAt: now,
			Reason: models.DisconnectReasonClientRequest},
		// q-fin-2：10s < 30s 扣 20 → 100-20=80
		{ConnectionID: "q-fin-2", ConnectedAt: now.Add(-10 * time.Second), DisconnectedAt: now,
			Reason: models.DisconnectReasonReadError},
		// q-fin-3：ConnectedAt 零值 → duration 0 → 96-20=76
		{ConnectionID: "q-fin-3", DisconnectedAt: now, Reason: models.DisconnectReasonReadError},
		// 无质量行：静默跳过
		{ConnectionID: "q-fin-404", ConnectedAt: now.Add(-time.Hour), DisconnectedAt: now,
			Reason: models.DisconnectReasonReadError},
	}))

	q1, err := store.GetByConnectionID(ctx, "q-fin-1")
	require.NoError(t, err)
	assert.InDelta(t, 96.0, q1.QualityScore, 0.001, "时长充足无追加扣分")

	q2, err := store.GetByConnectionID(ctx, "q-fin-2")
	require.NoError(t, err)
	assert.InDelta(t, 80.0, q2.QualityScore, 0.001, "短连接 <30s 扣 20")

	q3, err := store.GetByConnectionID(ctx, "q-fin-3")
	require.NoError(t, err)
	assert.InDelta(t, 76.0, q3.QualityScore, 0.001, "零值时长按 duration=0 走 <30s 梯度")

	// 空批次
	require.NoError(t, store.BatchFinalizeOnDisconnect(ctx, nil))
}

// TestConnectionQualityStore_GetByUserID 用户维度查询
func TestConnectionQualityStore_GetByUserID(t *testing.T) {
	store, _ := newQualityStore(t)
	ctx := context.Background()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-u-1", UserID: "user-a"}))
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-u-2", UserID: "user-a"}))
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-u-3", UserID: "user-b"}))

	got, err := store.GetByUserID(ctx, "user-a")
	require.NoError(t, err)
	assert.Len(t, got, 2)

	_, err = store.GetByConnectionID(ctx, "no-such")
	require.Error(t, err)
}

// TestConnectionQualityStore_GetHighErrorRateConnections 高错误率连接：阈值过滤 + 降序 + limit
func TestConnectionQualityStore_GetHighErrorRateConnections(t *testing.T) {
	store, _ := newQualityStore(t)
	ctx := context.Background()

	for i, count := range []int{5, 1, 9} {
		q := &models.ConnectionQuality{
			ConnectionID: "q-he-" + string(rune('0'+i)),
			UserID:       "u",
			ErrorCount:   count,
		}
		require.NoError(t, store.Upsert(ctx, q))
	}

	got, err := store.GetHighErrorRateConnections(ctx, 3, 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, 9, got[0].ErrorCount, "按 error_count DESC")
	assert.Equal(t, 5, got[1].ErrorCount)

	// 阈值以上全部返回
	all, err := store.GetHighErrorRateConnections(ctx, 3, 0)
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

// TestConnectionQualityStore_GetFrequentReconnectConnections 频繁重连连接：阈值 + 降序 + limit
func TestConnectionQualityStore_GetFrequentReconnectConnections(t *testing.T) {
	store, db := newQualityStore(t)
	ctx := context.Background()

	for i, count := range []int{2, 0, 7} {
		q := &models.ConnectionQuality{
			ConnectionID: "q-fr-" + string(rune('0'+i)),
			UserID:       "u",
		}
		require.NoError(t, store.Upsert(ctx, q))
		require.NoError(t, db.WithContext(ctx).Model(&models.ConnectionQuality{}).
			Where("connection_id = ?", "q-fr-"+string(rune('0'+i))).
			Update("reconnect_count", count).Error)
	}

	got, err := store.GetFrequentReconnectConnections(ctx, 2, 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, 7, got[0].ReconnectCount)
}

// TestConnectionQualityStore_CleanupInactiveRecords 只清理窗口前不活跃的质量记录
func TestConnectionQualityStore_CleanupInactiveRecords(t *testing.T) {
	store, db := newQualityStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, id := range []string{"q-cl-1", "q-cl-2", "q-cl-3", "q-cl-4"} {
		require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: id, UserID: "u"}))
	}
	old := now.Add(-48 * time.Hour)
	recent := now
	require.NoError(t, db.Model(&models.ConnectionQuality{}).Where("connection_id IN ?", []string{"q-cl-1", "q-cl-2"}).
		Update("last_active_at", old).Error)
	require.NoError(t, db.Model(&models.ConnectionQuality{}).Where("connection_id IN ?", []string{"q-cl-3", "q-cl-4"}).
		Update("last_active_at", recent).Error)

	deleted, err := store.CleanupInactiveRecords(ctx, now.Add(-24*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted)

	var remain int64
	require.NoError(t, db.Model(&models.ConnectionQuality{}).Count(&remain).Error)
	assert.Equal(t, int64(2), remain, "近窗口活跃的记录保留")
}

// TestConnectionQualityStore_WithTableName 自定义表名隔离：含表名限定自增写法
func TestConnectionQualityStore_WithTableName(t *testing.T) {
	db := newTestDB(t, &models.ConnectionQuality{}, &customQualityTable{})
	store := NewConnectionQualityStore(db, nil, spi.NewDefaultLogger()).
		WithTableName(customQualityTable{}.TableName())
	ctx := context.Background()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-custom-1", UserID: "u"}))
	require.NoError(t, store.Upsert(ctx, &models.ConnectionQuality{ConnectionID: "q-custom-1", UserID: "u"}))

	got, err := store.GetByConnectionID(ctx, "q-custom-1")
	require.NoError(t, err)
	assert.Equal(t, 1, got.ReconnectCount, "自定义表名下表名限定自增写法（table.reconnect_count + 1）仍应生效")

	// 默认表保持空
	var defaultCount int64
	require.NoError(t, db.Model(&models.ConnectionQuality{}).Count(&defaultCount).Error)
	assert.Zero(t, defaultCount)
}

// TestConnectionQualityStore_Close 关闭仓库应正常返回
func TestConnectionQualityStore_Close(t *testing.T) {
	store, _ := newQualityStore(t)
	require.NoError(t, store.Close())
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 17:10:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 17:10:02
 * @FilePath: \go-wsc\adapter\gorm\connection_store_test.go
 * @Description: WebSocket 连接记录仓库的用例（sqlite 真实执行，覆盖攒批合并 SQL）
 *
 * 覆盖：
 *   - Upsert/BatchUpsert：ON CONFLICT 冲突更新重连字段、user_id/metadata 首写保留、
 *     多租户维度归一化、nil/空 connection_id 防御
 *   - BatchMarkDisconnected：CASE WHEN 终态合并、批内去重保留最后一条、
 *     duration 计算、is_abnormal 判定、IN 未命中静默
 *   - BatchUpdateHeartbeats：last_ping_at 批量合并、nil PingTime 条目跳过、
 *     全 nil 批次无操作
 *   - List/Count：条件过滤/排序/分页
 *   - 统计：GetConnectionStats / GetConnectionStatsByID / GetUserConnectionStats /
 *     GetNodeConnectionStats（含空数据防御）
 *   - CleanupInactiveRecords：按 disconnected_at 分批清理
 *   - WithTableName：自定义表名测试隔离
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kamalyes/go-sqlbuilder"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// customConnTable 与 models.ConnectionRecord 同构但独立表名（WithTableName 隔离用）
// 索引名是 schema 级对象：同库双表直接复用模型 tag 会因固定命名复合索引
// idx_app_namespace 重名建表失败（连接质量模型注释同此认知），此处以等列集合
// 显式重建，唯一索引走 gorm 表内自动命名（Upsert 的 ON CONFLICT(connection_id) 依赖项）
type customConnTable struct {
	ID           uint64 `gorm:"primaryKey;autoIncrement"`
	ConnectionID string `gorm:"column:connection_id;size:64;uniqueIndex;not null"`
	UserID       string `gorm:"column:user_id;size:64;not null"`
	AppID        string `gorm:"column:app_id;size:64"`
	Namespace    string `gorm:"column:namespace;size:64"`

	NodeID   string `gorm:"column:node_id;size:100"`
	NodeIP   string `gorm:"column:node_ip;size:45"`
	NodePort int    `gorm:"column:node_port"`

	ClientIP   string                `gorm:"column:client_ip;size:45"`
	ClientType models.ClientType     `gorm:"column:client_type;size:20"`
	Protocol   models.ConnectionType `gorm:"column:protocol;size:20;default:websocket"`

	ConnectedAt    time.Time  `gorm:"column:connected_at;not null"`
	DisconnectedAt *time.Time `gorm:"column:disconnected_at"`
	Duration       int64      `gorm:"column:duration"`
	LastPingAt     *time.Time `gorm:"column:last_ping_at"`
	LastPongAt     *time.Time `gorm:"column:last_pong_at"`

	DisconnectReason string `gorm:"column:disconnect_reason;size:50"`
	DisconnectCode   int    `gorm:"column:disconnect_code"`

	IsActive        bool              `gorm:"column:is_active"`
	IsForcedOffline bool              `gorm:"column:is_forced_offline"`
	IsAbnormal      bool              `gorm:"column:is_abnormal"`
	Metadata        sqlbuilder.MapAny `gorm:"column:metadata;type:json"`

	CreatedAt time.Time `gorm:"autoCreateTime"`
	UpdatedAt time.Time `gorm:"autoUpdateTime"`
}

func (customConnTable) TableName() string { return "wsc_connection_records_custom" }

// newConnStore 建库 + 建表 + 建仓库（无自动清理，避免后台协程干扰测试）
func newConnStore(t *testing.T) (*ConnectionStore, *gorm.DB) {
	t.Helper()
	db := newTestDB(t, &models.ConnectionRecord{})
	return NewConnectionStore(db, nil, spi.NewDefaultLogger()), db
}

// TestConnectionStore_Upsert_Insert 新连接首次写入：多租户维度归一化落库
func TestConnectionStore_Upsert_Insert(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionRecord{
		ConnectionID: "conn-insert-1",
		UserID:       "user-1",
		NodeID:       "node-1",
		ConnectedAt:  now,
		IsActive:     true,
	}))

	got, err := store.GetByConnectionID(ctx, "conn-insert-1")
	require.NoError(t, err)
	assert.Equal(t, "user-1", got.UserID)
	assert.Equal(t, constants.DefaultAppID, got.AppID, "空 app_id 应归一化为默认应用")
	assert.Equal(t, constants.DefaultNamespace, got.Namespace, "空 namespace 应归一化为默认命名空间")
	assert.True(t, got.IsActive)
	assert.Equal(t, now.Unix(), got.ConnectedAt.Unix())
}

// TestConnectionStore_Upsert_Guard nil 记录与空 connection_id 直接报错，不触库
func TestConnectionStore_Upsert_Guard(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()

	require.Error(t, store.Upsert(ctx, nil))
	require.Error(t, store.Upsert(ctx, &models.ConnectionRecord{ConnectionID: ""}))
}

// TestConnectionStore_Upsert_ReconnectConflict 重连冲突更新：节点字段刷新、生命周期重置、
// user_id 与 metadata 保留首次写入值（同一 connection_id 的重复注册来自同一次握手）
func TestConnectionStore_Upsert_ReconnectConflict(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	meta := map[string]any{"k": "v"}

	// 首次连接：断开状态入库（模拟历史遗留，冲突分支会重置这些终态）
	first := &models.ConnectionRecord{
		ConnectionID: "conn-reconnect-1",
		UserID:       "user-1",
		NodeID:       "node-1",
		ConnectedAt:  time.Now().Add(-2 * time.Hour),
		Duration:     7200,
		IsActive:     false,
		IsAbnormal:   true,
	}
	first.Metadata = meta
	require.NoError(t, store.Upsert(ctx, first))

	// 重连：换节点，故意改 UserID 验证冲突分支不更新 user_id
	second := &models.ConnectionRecord{
		ConnectionID: "conn-reconnect-1",
		UserID:       "user-CHANGED",
		NodeID:       "node-2",
		NodeIP:       "10.0.0.2",
		NodePort:     8081,
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	require.NoError(t, store.Upsert(ctx, second))

	got, err := store.GetByConnectionID(ctx, "conn-reconnect-1")
	require.NoError(t, err)
	assert.Equal(t, "node-2", got.NodeID, "冲突分支应刷新 node_id")
	assert.Equal(t, "10.0.0.2", got.NodeIP)
	assert.Equal(t, 8081, got.NodePort)
	assert.Equal(t, "user-1", got.UserID, "user_id 不在冲突更新列，应保留首次写入值")
	assert.Equal(t, meta["k"], got.Metadata["k"], "metadata 只随首次 INSERT 写入")
	assert.True(t, got.IsActive, "重连应重置为活跃")
	assert.False(t, got.IsAbnormal, "重连应清除异常标记")
	assert.Zero(t, got.Duration, "重连应清零时长")
	assert.Nil(t, got.LastPingAt, "重连应清零心跳时间戳")

	// 同一连接仅一行（upsert 不产生重复记录）
	records, err := store.GetByUserID(ctx, "user-1")
	require.NoError(t, err)
	assert.Len(t, records, 1)
}

// TestConnectionStore_BatchUpsert_BatchWrite 批量写入多条新连接（一次 upsert 攒批）
func TestConnectionStore_BatchUpsert_BatchWrite(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()

	records := make([]*models.ConnectionRecord, 0, 3)
	for i := 1; i <= 3; i++ {
		records = append(records, &models.ConnectionRecord{
			ConnectionID: fmt.Sprintf("conn-batch-%d", i),
			UserID:       "user-batch",
			NodeID:       "node-1",
			ConnectedAt:  time.Now(),
			IsActive:     true,
		})
	}
	require.NoError(t, store.BatchUpsert(ctx, records))

	count, err := store.Count(ctx, &models.ConnectionQueryOptions{UserID: "user-batch"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

// TestConnectionStore_BatchUpsert_Empty 空批次直接返回，不触库
func TestConnectionStore_BatchUpsert_Empty(t *testing.T) {
	store, _ := newConnStore(t)
	require.NoError(t, store.BatchUpsert(context.Background(), nil))
}

// TestConnectionStore_BatchMarkDisconnected CASE WHEN 终态合并：
// 去重保留最后一条、duration 计算（含 ConnectedAt 零值→0）、is_abnormal 判定、
// 表中不存在的连接静默跳过、未列入批次的连接不受影响
func TestConnectionStore_BatchMarkDisconnected(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()

	seed := []*models.ConnectionRecord{
		{ConnectionID: "conn-disc-1", UserID: "u", ConnectedAt: now, IsActive: true},
		{ConnectionID: "conn-disc-2", UserID: "u", ConnectedAt: now, IsActive: true},
		{ConnectionID: "conn-disc-3", UserID: "u", ConnectedAt: now, IsActive: true},
	}
	require.NoError(t, store.BatchUpsert(ctx, seed))

	// conn-disc-1 出现两条：去重应保留最后一条（reason=client_request，duration=110）
	entries := []*models.DisconnectionEntry{
		{ConnectionID: "conn-disc-1", ConnectedAt: now.Add(-60 * time.Second),
			DisconnectedAt: now, Reason: models.DisconnectReasonReadError, Code: 1001},
		{ConnectionID: "conn-disc-1", ConnectedAt: now.Add(-120 * time.Second),
			DisconnectedAt: now.Add(-10 * time.Second), Reason: models.DisconnectReasonClientRequest, Code: 1000},
		// ConnectedAt 零值 → duration 为 0；server_shutdown 不算异常
		{ConnectionID: "conn-disc-2", DisconnectedAt: now,
			Reason: models.DisconnectReasonServerShutdown, Code: 1001},
		// 表中不存在的连接 → IN 影响 0 行，静默返回
		{ConnectionID: "conn-disc-404", ConnectedAt: now, DisconnectedAt: now,
			Reason: models.DisconnectReasonReadError, Code: 1001},
	}
	require.NoError(t, store.BatchMarkDisconnected(ctx, entries))

	r1, err := store.GetByConnectionID(ctx, "conn-disc-1")
	require.NoError(t, err)
	assert.False(t, r1.IsActive)
	assert.False(t, r1.IsAbnormal, "client_request 主动断开不算异常")
	assert.Equal(t, int64(110), r1.Duration, "去重保留最后一条：duration=(断开-连接) 秒数")
	assert.Equal(t, models.DisconnectReasonClientRequest, models.DisconnectReason(r1.DisconnectReason))
	assert.Equal(t, 1000, r1.DisconnectCode)

	r2, err := store.GetByConnectionID(ctx, "conn-disc-2")
	require.NoError(t, err)
	assert.False(t, r2.IsActive)
	assert.Zero(t, r2.Duration, "ConnectedAt 零值时 duration 应为 0")
	assert.False(t, r2.IsAbnormal, "server_shutdown 不算异常")

	// 未列入批次的连接保持原样
	r3, err := store.GetByConnectionID(ctx, "conn-disc-3")
	require.NoError(t, err)
	assert.True(t, r3.IsActive, "未列入批次的连接不应被改动")
	assert.Zero(t, r3.Duration)
}

// TestConnectionStore_BatchMarkDisconnected_Abnormal 非正常原因（read_error）→ is_abnormal=true
func TestConnectionStore_BatchMarkDisconnected_Abnormal(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.BatchUpsert(ctx, []*models.ConnectionRecord{
		{ConnectionID: "conn-abn-1", UserID: "u", ConnectedAt: now.Add(-30 * time.Second), IsActive: true},
	}))
	require.NoError(t, store.BatchMarkDisconnected(ctx, []*models.DisconnectionEntry{
		{ConnectionID: "conn-abn-1", ConnectedAt: now.Add(-30 * time.Second), DisconnectedAt: now,
			Reason: models.DisconnectReasonReadError, Code: 1006},
	}))

	got, err := store.GetByConnectionID(ctx, "conn-abn-1")
	require.NoError(t, err)
	assert.True(t, got.IsAbnormal, "read_error 异常断开应标记 is_abnormal")
	assert.Equal(t, int64(30), got.Duration)
}

// TestConnectionStore_BatchMarkDisconnected_Empty 空批次直接返回
func TestConnectionStore_BatchMarkDisconnected_Empty(t *testing.T) {
	store, _ := newConnStore(t)
	require.NoError(t, store.BatchMarkDisconnected(context.Background(), nil))
}

// TestConnectionStore_BatchUpdateHeartbeats last_ping_at CASE WHEN 批量更新：
// 去重取最后非 nil、nil PingTime 条目不覆盖、全 nil 批次无操作
func TestConnectionStore_BatchUpdateHeartbeats(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.BatchUpsert(ctx, []*models.ConnectionRecord{
		{ConnectionID: "conn-hb-1", UserID: "u", ConnectedAt: now, IsActive: true},
		{ConnectionID: "conn-hb-2", UserID: "u", ConnectedAt: now, IsActive: true},
	}))

	t1 := now.Add(-10 * time.Second)
	t2 := now.Add(-5 * time.Second)
	entries := []*models.HeartbeatUpdateEntry{
		{ConnectionID: "conn-hb-1", PingTime: &t1},
		// 同连接第二条 nil ping：字段级合并，t1 不应被 nil 覆盖
		{ConnectionID: "conn-hb-1", PingTime: nil},
		// conn-hb-2 先有 ping 再 nil：保留最后非 nil
		{ConnectionID: "conn-hb-2", PingTime: &t2},
		{ConnectionID: "conn-hb-2", PingTime: nil},
	}
	require.NoError(t, store.BatchUpdateHeartbeats(ctx, entries))

	r1, err := store.GetByConnectionID(ctx, "conn-hb-1")
	require.NoError(t, err)
	require.NotNil(t, r1.LastPingAt)
	assert.WithinDuration(t, t1, *r1.LastPingAt, time.Second)

	r2, err := store.GetByConnectionID(ctx, "conn-hb-2")
	require.NoError(t, err)
	require.NotNil(t, r2.LastPingAt)
	assert.WithinDuration(t, t2, *r2.LastPingAt, time.Second)

	// 全 nil 批次：无有效字段可写，返回 nil 且不改动数据
	before, err := store.GetByConnectionID(ctx, "conn-hb-1")
	require.NoError(t, err)
	require.NoError(t, store.BatchUpdateHeartbeats(ctx, []*models.HeartbeatUpdateEntry{
		{ConnectionID: "conn-hb-1", PingTime: nil},
	}))
	after, err := store.GetByConnectionID(ctx, "conn-hb-1")
	require.NoError(t, err)
	require.NotNil(t, after.LastPingAt)
	assert.WithinDuration(t, *before.LastPingAt, *after.LastPingAt, 0)

	// 空批次直接返回
	require.NoError(t, store.BatchUpdateHeartbeats(ctx, nil))
}

// TestConnectionStore_ListFiltersAndPaging 条件过滤 + 排序 + 分页
func TestConnectionStore_ListFiltersAndPaging(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	base := time.Now().Add(-24 * time.Hour)

	seed := []*models.ConnectionRecord{
		{ConnectionID: "c-u1-1", UserID: "user-1", ClientIP: "1.1.1.1", NodeID: "n1", ConnectedAt: base, IsActive: true},
		{ConnectionID: "c-u1-2", UserID: "user-1", ClientIP: "1.1.1.2", NodeID: "n1", ConnectedAt: base.Add(time.Hour), IsActive: false, IsAbnormal: true},
		{ConnectionID: "c-u1-3", UserID: "user-1", ClientIP: "1.1.1.1", NodeID: "n2", ConnectedAt: base.Add(2 * time.Hour), IsActive: false},
		{ConnectionID: "c-u2-1", UserID: "user-2", ClientIP: "2.2.2.2", NodeID: "n1", ConnectedAt: base.Add(3 * time.Hour), IsActive: true},
	}
	require.NoError(t, store.BatchUpsert(ctx, seed))

	// 默认按 connected_at DESC：最新在前
	got, err := store.GetByUserID(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, "c-u1-3", got[0].ConnectionID, "默认 connected_at DESC")

	// IsActive 过滤
	active, err := store.GetActiveByUserID(ctx, "user-1")
	require.NoError(t, err)
	require.Len(t, active, 1)
	assert.Equal(t, "c-u1-1", active[0].ConnectionID)

	// IsAbnormal + ClientIP 组合过滤
	abnormal, err := store.List(ctx, &models.ConnectionQueryOptions{UserID: "user-1", IsAbnormal: boolPtr(true), ClientIP: "1.1.1.2"})
	require.NoError(t, err)
	require.Len(t, abnormal, 1)
	assert.Equal(t, "c-u1-2", abnormal[0].ConnectionID)

	// 自定义排序 + Limit/Offset
	page, err := store.List(ctx, &models.ConnectionQueryOptions{
		OrderBy: "connected_at ASC", Limit: 2, Offset: 1,
	})
	require.NoError(t, err)
	require.Len(t, page, 2)
	assert.Equal(t, "c-u1-2", page[0].ConnectionID)
	assert.Equal(t, "c-u1-3", page[1].ConnectionID)

	// Count 与 List 同条件一致
	count, err := store.Count(ctx, &models.ConnectionQueryOptions{NodeID: "n1"})
	require.NoError(t, err)
	assert.Equal(t, int64(3), count)

	// 未命中条件返回空集不报错
	none, err := store.List(ctx, &models.ConnectionQueryOptions{UserID: "nobody"})
	require.NoError(t, err)
	assert.Empty(t, none)
}

// TestConnectionStore_GetByConnectionID_NotFound 查无此连接 → gorm.ErrRecordNotFound
func TestConnectionStore_GetByConnectionID_NotFound(t *testing.T) {
	store, _ := newConnStore(t)
	_, err := store.GetByConnectionID(context.Background(), "no-such-conn")
	require.Error(t, err)
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestConnectionStore_GetConnectionStats 时间窗口内聚合：总数/活跃/平均时长/异常率
func TestConnectionStore_GetConnectionStats(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()

	seed := []*models.ConnectionRecord{
		{ConnectionID: "cs-1", UserID: "u", ConnectedAt: now.Add(-30 * time.Minute), IsActive: true, Duration: 60},
		{ConnectionID: "cs-2", UserID: "u", ConnectedAt: now.Add(-20 * time.Minute), IsActive: true, Duration: 30, IsAbnormal: true},
		{ConnectionID: "cs-3", UserID: "u", ConnectedAt: now.Add(-10 * time.Minute), IsActive: false, Duration: 120},
		// 窗口外：不计入
		{ConnectionID: "cs-out", UserID: "u", ConnectedAt: now.Add(-2 * time.Hour), IsActive: true, Duration: 999},
	}
	require.NoError(t, store.BatchUpsert(ctx, seed))

	stats, err := store.GetConnectionStats(ctx, now.Add(-1*time.Hour), now.Add(1*time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(3), stats.TotalConnections)
	assert.Equal(t, int64(2), stats.ActiveConnections)
	assert.InDelta(t, 70.0, stats.AverageDuration, 0.001, "(60+30+120)/3")
	assert.InDelta(t, 100.0/3.0, stats.AbnormalRate, 0.001, "1/3 异常率")
	assert.Zero(t, stats.TotalMessagesSent, "拆表后质量维度零填充")
}

// TestConnectionStore_GetConnectionStats_EmptyWindow 空窗口：零值统计不报错
func TestConnectionStore_GetConnectionStats_EmptyWindow(t *testing.T) {
	store, _ := newConnStore(t)
	stats, err := store.GetConnectionStats(context.Background(),
		time.Now().Add(-time.Minute), time.Now())
	require.NoError(t, err)
	assert.Zero(t, stats.TotalConnections)
	assert.Zero(t, stats.AverageDuration)
	assert.Zero(t, stats.AbnormalRate)
}

// TestConnectionStore_GetConnectionStatsByID 单连接统计；不存在时报错
func TestConnectionStore_GetConnectionStatsByID(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionRecord{
		ConnectionID: "csid-1", UserID: "user-x", ConnectedAt: now, IsActive: true, Duration: 42,
	}))

	stats, err := store.GetConnectionStatsByID(ctx, "csid-1")
	require.NoError(t, err)
	assert.Equal(t, "user-x", stats.UserID)
	assert.True(t, stats.IsActive)
	assert.Equal(t, int64(42), stats.Duration)

	_, err = store.GetConnectionStatsByID(ctx, "no-such")
	require.Error(t, err)
}

// TestConnectionStore_GetUserConnectionStats 用户维度汇总：活跃标记、最早连接、最晚断开、累计时长
func TestConnectionStore_GetUserConnectionStats(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()
	disconnectAt := now.Add(-30 * time.Minute)

	require.NoError(t, store.BatchUpsert(ctx, []*models.ConnectionRecord{
		{ConnectionID: "us-1", UserID: "user-9", ConnectedAt: now.Add(-2 * time.Hour), Duration: 50, IsActive: true},
		{ConnectionID: "us-2", UserID: "user-9", ConnectedAt: now.Add(-1 * time.Hour),
			DisconnectedAt: &disconnectAt, Duration: 120, IsActive: false},
	}))

	stats, err := store.GetUserConnectionStats(ctx, "user-9")
	require.NoError(t, err)
	assert.Equal(t, "user-9", stats.UserID)
	assert.True(t, stats.IsActive, "任一连接活跃即视为活跃")
	assert.WithinDuration(t, now.Add(-2*time.Hour), stats.ConnectedAt, time.Second, "最早连接时间")
	require.NotNil(t, stats.DisconnectedAt)
	assert.WithinDuration(t, disconnectAt, *stats.DisconnectedAt, time.Second, "最晚断开时间")
	assert.Equal(t, int64(170), stats.Duration, "50+120 累计")

	_, err = store.GetUserConnectionStats(ctx, "ghost")
	require.Error(t, err, "无记录用户应报错")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

// TestConnectionStore_GetNodeConnectionStats 节点维度统计（含不存在的节点零值防御）
func TestConnectionStore_GetNodeConnectionStats(t *testing.T) {
	store, _ := newConnStore(t)
	ctx := context.Background()
	now := time.Now()

	require.NoError(t, store.BatchUpsert(ctx, []*models.ConnectionRecord{
		{ConnectionID: "ns-1", UserID: "u1", NodeID: "node-a", NodeIP: "10.0.0.1", NodePort: 8080, ConnectedAt: now, IsActive: true, Duration: 100},
		{ConnectionID: "ns-2", UserID: "u2", NodeID: "node-a", NodeIP: "10.0.0.1", NodePort: 8080, ConnectedAt: now, IsActive: true, Duration: 60},
		{ConnectionID: "ns-3", UserID: "u3", NodeID: "node-a", NodeIP: "10.0.0.1", NodePort: 8080, ConnectedAt: now,
			IsActive: false, IsAbnormal: true, Duration: 40},
	}))

	stats, err := store.GetNodeConnectionStats(ctx, "node-a")
	require.NoError(t, err)
	assert.Equal(t, "node-a", stats.NodeID)
	assert.Equal(t, "10.0.0.1", stats.NodeIP)
	assert.Equal(t, 8080, stats.NodePort)
	assert.Equal(t, int64(3), stats.TotalConnections)
	assert.Equal(t, int64(2), stats.ActiveConnections)
	assert.Equal(t, int64(1), stats.DisconnectedCount)
	assert.Equal(t, int64(1), stats.AbnormalCount)
	assert.InDelta(t, 100.0/3.0, stats.AbnormalRate, 0.001)
	assert.InDelta(t, 200.0/3.0, stats.AverageDuration, 0.001, "(100+60+40)/3")

	// 不存在的节点：零值统计而非报错
	empty, err := store.GetNodeConnectionStats(ctx, "node-ghost")
	require.NoError(t, err)
	assert.Equal(t, "node-ghost", empty.NodeID)
	assert.Zero(t, empty.TotalConnections)
	assert.Zero(t, empty.AbnormalRate)
}

// TestConnectionStore_CleanupInactiveRecords 只清理窗口前的非活跃记录，活跃与窗口内保留
func TestConnectionStore_CleanupInactiveRecords(t *testing.T) {
	store, db := newConnStore(t)
	ctx := context.Background()
	now := time.Now()
	before := now.Add(-24 * time.Hour)

	require.NoError(t, store.BatchUpsert(ctx, []*models.ConnectionRecord{
		{ConnectionID: "cl-1", UserID: "u", ConnectedAt: now.Add(-48 * time.Hour), DisconnectedAt: timePtr(now.Add(-47 * time.Hour)), IsActive: false},
		{ConnectionID: "cl-2", UserID: "u", ConnectedAt: now.Add(-30 * time.Hour), DisconnectedAt: timePtr(now.Add(-29 * time.Hour)), IsActive: false},
		{ConnectionID: "cl-3", UserID: "u", ConnectedAt: now.Add(-2 * time.Hour), DisconnectedAt: timePtr(now.Add(-1 * time.Hour)), IsActive: false},
		{ConnectionID: "cl-4", UserID: "u", ConnectedAt: now, IsActive: true},
	}))

	deleted, err := store.CleanupInactiveRecords(ctx, before)
	require.NoError(t, err)
	assert.Equal(t, int64(2), deleted, "仅 24h 前断开的 2 条应被清理")

	var remain int64
	require.NoError(t, db.Model(&models.ConnectionRecord{}).Count(&remain).Error)
	assert.Equal(t, int64(2), remain, "近窗口非活跃 + 活跃记录保留")
}

// TestConnectionStore_WithTableName 自定义表名隔离：读写均路由到自定义表，默认表不受影响
func TestConnectionStore_WithTableName(t *testing.T) {
	db := newTestDB(t, &models.ConnectionRecord{}, &customConnTable{})
	store := NewConnectionStore(db, nil, spi.NewDefaultLogger()).WithTableName(customConnTable{}.TableName())
	ctx := context.Background()

	require.NoError(t, store.Upsert(ctx, &models.ConnectionRecord{
		ConnectionID: "conn-custom-1", UserID: "user-c", ConnectedAt: time.Now(), IsActive: true,
	}))

	// 自定义表可见
	got, err := store.GetByConnectionID(ctx, "conn-custom-1")
	require.NoError(t, err)
	assert.Equal(t, "user-c", got.UserID)

	// 终态 CASE WHEN 合并 SQL 也应路由到自定义表
	require.NoError(t, store.BatchMarkDisconnected(ctx, []*models.DisconnectionEntry{
		{ConnectionID: "conn-custom-1", ConnectedAt: got.ConnectedAt, DisconnectedAt: time.Now(),
			Reason: models.DisconnectReasonReadError, Code: 1006},
	}))
	marked, err := store.GetByConnectionID(ctx, "conn-custom-1")
	require.NoError(t, err)
	assert.False(t, marked.IsActive)

	// 默认表保持空（隔离生效）
	var defaultCount int64
	require.NoError(t, db.Model(&models.ConnectionRecord{}).Count(&defaultCount).Error)
	assert.Zero(t, defaultCount)
}

// TestConnectionStore_Close 关闭仓库应正常返回（停止后台清理任务的路径）
func TestConnectionStore_Close(t *testing.T) {
	store, _ := newConnStore(t)
	require.NoError(t, store.Close())
}

// ============================================================================
// 测试辅助
// ============================================================================

func boolPtr(b bool) *bool           { return &b }
func timePtr(t time.Time) *time.Time { return &t }

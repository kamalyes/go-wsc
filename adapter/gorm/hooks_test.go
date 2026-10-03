/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 17:10:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 17:10:02
 * @FilePath: \go-wsc\adapter\gorm\hooks_test.go
 * @Description: GORM 适配器装配钩子的用例（含本包各 *_test.go 共用的测试基建）
 *
 * 覆盖：
 *   - Configure：DB 缺失跳过（纯 Redis 部署）、hub 为 nil 报错、
 *     依赖齐备时三个仓库正确注入 StoreTarget
 *   - NewOfflineStoreFor：返回可用的 spi.OfflineStore
 *
 * 基建（本文件定义，包内其余 *_test.go 共用）：
 *   - openTestDB/newTestDB：临时文件 sqlite + AutoMigrate（纯 Go 驱动，无 cgo，
 *     适配 Windows 测试环境；t.Cleanup 关闭底层连接，先于 TempDir 回收释放文件句柄）
 *   - stubStoreTarget：spi.StoreTarget 记账桩（只记录注入了什么）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"path/filepath"
	"testing"

	glebarezsqlite "github.com/glebarez/sqlite"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ============================================================================
// 共享测试基建
// ============================================================================

// openTestDB 打开临时文件 sqlite 并迁移给定模型，允许自定义 gorm.Config（如注入 SQL 记录 logger）
func openTestDB(t *testing.T, cfg *gorm.Config, models ...any) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	if cfg == nil {
		cfg = &gorm.Config{}
	}
	db, err := gorm.Open(glebarezsqlite.Open(dsn), cfg)
	require.NoError(t, err, "打开测试 sqlite 失败")
	if len(models) > 0 {
		require.NoError(t, db.AutoMigrate(models...), "AutoMigrate 失败")
	}
	// 先关闭底层 sql.DB 再由 TempDir 回收目录（Windows 上打开的文件句柄会阻止目录删除）
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// newTestDB 打开临时文件 sqlite 并迁移给定模型（本包各 *_test.go 共用的测试入口）
func newTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	return openTestDB(t, nil, models...)
}

// stubStoreTarget spi.StoreTarget 的记账桩：只记录「被注入了什么」，不模拟存储行为
type stubStoreTarget struct {
	online      spi.OnlineStore
	stats       spi.HubStats
	group       spi.GroupStore
	workload    spi.WorkloadStore
	messageSink spi.MessageSink
	connStore   spi.ConnectionStore
	connQuality spi.ConnectionQualityStore
	offline     spi.OfflineQueue
}

func (t *stubStoreTarget) SetOnlineStatusRepository(s spi.OnlineStore)  { t.online = s }
func (t *stubStoreTarget) SetHubStatsRepository(s spi.HubStats)         { t.stats = s }
func (t *stubStoreTarget) SetGroupRepository(s spi.GroupStore)          { t.group = s }
func (t *stubStoreTarget) SetWorkloadRepository(s spi.WorkloadStore)    { t.workload = s }
func (t *stubStoreTarget) SetMessageRecordRepository(s spi.MessageSink) { t.messageSink = s }
func (t *stubStoreTarget) SetConnectionRecordRepository(s spi.ConnectionStore) {
	t.connStore = s
}
func (t *stubStoreTarget) SetConnectionQualityRepository(s spi.ConnectionQualityStore) {
	t.connQuality = s
}
func (t *stubStoreTarget) SetOfflineMessageHandler(q spi.OfflineQueue) { t.offline = q }

var _ spi.StoreTarget = (*stubStoreTarget)(nil)

// ============================================================================
// Configure 用例
// ============================================================================

// TestHooks_Configure_NilDB DB 缺失（纯 Redis 部署）：直接跳过，不触碰 hub
func TestHooks_Configure_NilDB(t *testing.T) {
	h := NewHooks(nil)
	target := &stubStoreTarget{}
	err := h.Configure(context.Background(), target, spi.StoreDeps{})
	require.NoError(t, err, "deps.DB 为 nil 应静默跳过而非报错")
	assert.Nil(t, target.messageSink)
	assert.Nil(t, target.connStore)
	assert.Nil(t, target.connQuality)
}

// TestHooks_Configure_NilTarget 依赖齐备但装配目标为 nil：报错而非 panic
func TestHooks_Configure_NilTarget(t *testing.T) {
	db := newTestDB(t)
	h := NewHooks(nil)
	err := h.Configure(context.Background(), nil, spi.StoreDeps{DB: db})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "store target is nil")
}

// TestHooks_Configure_InjectsRepositories 依赖齐备：三个关系型仓库正确注入
func TestHooks_Configure_InjectsRepositories(t *testing.T) {
	db := newTestDB(t) // Configure 仅构造仓库不执行 SQL，无需迁移表

	// 显式传非 nil 配置（零值 → 不启用自动清理），覆盖 cfg 兜底分支
	h := NewHooks(&wscconfig.Database{})
	target := &stubStoreTarget{}
	err := h.Configure(context.Background(), target, spi.StoreDeps{DB: db})
	require.NoError(t, err)

	// 三个仓库均注入，且为本适配器实现
	require.NotNil(t, target.messageSink)
	require.NotNil(t, target.connStore)
	require.NotNil(t, target.connQuality)
	_, isSink := target.messageSink.(*MessageSink)
	_, isConn := target.connStore.(*ConnectionStore)
	_, isQuality := target.connQuality.(*ConnectionQualityStore)
	assert.True(t, isSink, "MessageRecord 槽位应注入 *MessageSink")
	assert.True(t, isConn, "ConnectionRecord 槽位应注入 *ConnectionStore")
	assert.True(t, isQuality, "ConnectionQuality 槽位应注入 *ConnectionQualityStore")

	// 不归本钩子管的槽位不应被触碰
	assert.Nil(t, target.online)
	assert.Nil(t, target.stats)
	assert.Nil(t, target.group)
	assert.Nil(t, target.workload)
	assert.Nil(t, target.offline)
}

// TestHooks_Configure_NilCfgFallback NewHooks(nil) 路径：cfg 为 nil 时使用内置默认值照常装配
func TestHooks_Configure_NilCfgFallback(t *testing.T) {
	db := newTestDB(t)
	h := NewHooks(nil)
	target := &stubStoreTarget{}
	require.NoError(t, h.Configure(context.Background(), target, spi.StoreDeps{DB: db}))
	assert.NotNil(t, target.messageSink)
	assert.NotNil(t, target.connStore)
	assert.NotNil(t, target.connQuality)
}

// TestNewOfflineStoreFor 混合离线处理器的组装入口：返回可用的 spi.OfflineStore
func TestNewOfflineStoreFor(t *testing.T) {
	db := newTestDB(t)
	store := NewOfflineStoreFor(db, &wscconfig.OfflineMessage{})
	require.NotNil(t, store)
	_, isImpl := store.(*OfflineStore)
	assert.True(t, isImpl, "NewOfflineStoreFor 应返回本适配器的 *OfflineStore")
	require.NoError(t, store.Close())
}

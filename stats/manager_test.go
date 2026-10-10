/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:08:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:08:00
 * @FilePath: \go-wsc\stats\manager_test.go
 * @Description: 统计域单元测试（局部替身，不构造真实 Hub）
 *
 * 重点覆盖「未注入后端即 no-op」这一契约：纯 WebSocket 连接管理场景下
 * 不配置任何存储后端，所有追踪方法必须在首个 nil 判断处返回，
 * 既不能 panic，也不能产生副作用。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package stats

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// ShouldTrackUserStats
// ============================================================================

func TestShouldTrackUserStats(t *testing.T) {
	m := NewManager(newFakeHost())

	tracked := []models.UserType{
		models.UserTypeCustomer, models.UserTypeAgent, models.UserTypeAdmin,
		models.UserTypeVIP, models.UserTypeVisitor,
	}
	for _, ut := range tracked {
		assert.Truef(t, m.ShouldTrackUserStats(ut), "%s 应被追踪", ut)
	}

	excluded := []models.UserType{
		models.UserTypeSystem, models.UserTypeBot, models.UserTypeObserver,
	}
	for _, ut := range excluded {
		assert.Falsef(t, m.ShouldTrackUserStats(ut), "%s 应被排除", ut)
	}
}

// ============================================================================
// 未注入后端 → no-op（零依赖启动契约）
// ============================================================================

// 全部追踪方法在无任何存储后端时必须安全返回，不 panic 不产生副作用
func TestTrackMethodsNoOpWithoutBackends(t *testing.T) {
	m := NewManager(newFakeHost())
	client := models.NewClient("c1", "u1", models.UserTypeCustomer)

	assert.NotPanics(t, func() {
		m.TrackSenderMessageStats("conn-1", models.UserTypeCustomer)
		m.TrackReceiverMessageStats("conn-1", models.UserTypeCustomer, 128)
		m.TrackConnectionError("conn-1", models.UserTypeCustomer, errors.New("boom"))
		m.TrackHeartbeatStats(client)
		m.TrackHeartbeatStats(nil)
		m.SyncClientStats()
		m.SyncOnlineStatus(client)
		m.LogClientConnection(client)
	})
}

// TrackConnectionError 对 nil error 应直接返回（无错误可记）
func TestTrackConnectionErrorIgnoresNilError(t *testing.T) {
	host := newFakeHost()
	quality := &recordingQualityStore{}
	host.qualityRepo = quality
	// 真实攒批器：拦截应发生在 Submit 之前，Stop flush 后落库条目为空
	host.errBatcher = batcher.NewErrorStatsBatcher(host, 16, 8, time.Hour)
	defer host.errBatcher.Stop()
	m := NewManager(host)

	m.TrackConnectionError("conn-1", models.UserTypeCustomer, nil)
	host.errBatcher.Stop()
	host.errBatcher = nil // 防 defer 二次 Stop（幂等，双保险省略）
	assert.Empty(t, quality.errors, "nil error 不应记录")
}

// 空 connectionID 即使在有质量仓储时也不应提交统计
func TestTrackStatsSkipsEmptyConnectionID(t *testing.T) {
	host := newFakeHost()
	host.qualityRepo = &recordingQualityStore{}
	m := NewManager(host)

	assert.NotPanics(t, func() {
		m.TrackSenderMessageStats("", models.UserTypeCustomer)
		m.TrackReceiverMessageStats("", models.UserTypeCustomer, 1)
	})
}

// 系统/机器人/观察者即使有仓储也不应记录错误
func TestTrackErrorSkipsExcludedUserTypes(t *testing.T) {
	host := newFakeHost()
	quality := &recordingQualityStore{}
	host.qualityRepo = quality
	m := NewManager(host)

	for _, ut := range []models.UserType{
		models.UserTypeSystem, models.UserTypeBot, models.UserTypeObserver,
	} {
		m.TrackConnectionError("conn-x", ut, errors.New("boom"))
	}
	assert.Empty(t, quality.errors, "系统/机器人/观察者不应记录错误")
}

// 心跳统计落库节流：节流窗口内同一连接的重复心跳仅落库一次
// （写热点的等比下降依赖此拦截发生在 Submit 之前——队列与 flush 均不应看到窗口内条目）
func TestTrackHeartbeatStatsThrottled(t *testing.T) {
	host := newFakeHost()
	records := &recordingHeartbeatStore{}
	host.connRecRepo = records
	// 真实攒批器 + 长 flush 间隔（仅 Stop 触发 flush）
	host.hbBatcher = batcher.NewHeartbeatStatsUpdater(host, 16, 8, time.Hour)
	m := NewManager(host)

	// 节流间隔调大避免测试对时钟边界敏感（同包 var 直改，测试结束还原）
	origThrottle := heartbeatStatsFlushThrottle
	heartbeatStatsFlushThrottle = time.Hour
	defer func() { heartbeatStatsFlushThrottle = origThrottle }()

	client := models.NewClient("c-throttle", "u-throttle", models.UserTypeCustomer)
	m.TrackHeartbeatStats(client) // 首调：零值状态放行
	m.TrackHeartbeatStats(client) // 节流窗口内：拦截

	host.hbBatcher.Stop()
	require.Len(t, records.heartbeats, 1, "节流窗口内重复心跳不应重复落库")
	assert.Equal(t, "c-throttle", records.heartbeats[0].ConnectionID)
	assert.NotNil(t, records.heartbeats[0].PingTime, "放行条目应携带心跳时间戳")
}

// ============================================================================
// 在线状态：无后端时读侧退化为本地注册表视图
// ============================================================================

func TestOnlineQueriesFallBackToLocalRegistry(t *testing.T) {
	host := newFakeHost()
	host.nodeID = "node-a"
	m := NewManager(host)

	ids, err := m.GetAllOnlineUserIDs()
	assert.NoError(t, err)
	assert.Empty(t, ids)

	cnt, err := m.GetOnlineUserCount()
	assert.NoError(t, err)
	assert.EqualValues(t, 0, cnt)

	// 本节点：退化为本地视图，不报错
	ids, err = m.GetOnlineUsersByNode("node-a")
	assert.NoError(t, err)
	assert.Empty(t, ids)

	// 其他节点：无后端无法回答，必须报 ErrOnlineStatusRepositoryNotSet
	_, err = m.GetOnlineUsersByNode("node-b")
	assert.ErrorIs(t, err, models.ErrOnlineStatusRepositoryNotSet)
}

// 无后端时同步写入必须显式报错，而不是静默成功（否则掩盖配置缺失）
func TestSyncOnlineStatusToRedisWithoutBackendErrors(t *testing.T) {
	m := NewManager(newFakeHost())
	assert.ErrorIs(t, m.SyncOnlineStatusToRedis(), models.ErrOnlineStatusRepositoryNotSet)
}

// ============================================================================
// 替身：嵌入接口 + 只覆盖用到的方法
// ============================================================================

// recordingQualityStore 记录 BatchAddErrors 调用。
// 嵌入 spi.ConnectionQualityStore（nil）—— 未被覆盖的方法被调用即 panic，
// 从而暴露本包对质量仓储的、测试尚未覆盖的依赖。
type recordingQualityStore struct {
	spi.ConnectionQualityStore

	errors []*models.ErrorUpdateEntry
}

func (s *recordingQualityStore) BatchAddErrors(_ context.Context, entries []*models.ErrorUpdateEntry) error {
	s.errors = append(s.errors, entries...)
	return nil
}

// recordingHeartbeatStore 记录 BatchUpdateHeartbeats 调用（心跳节流测试观测点）
type recordingHeartbeatStore struct {
	spi.ConnectionStore

	heartbeats []*models.HeartbeatUpdateEntry
}

func (s *recordingHeartbeatStore) BatchUpdateHeartbeats(_ context.Context, entries []*models.HeartbeatUpdateEntry) error {
	s.heartbeats = append(s.heartbeats, entries...)
	return nil
}

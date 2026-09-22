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

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
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
		m.TrackConnectionError(context.Background(), "conn-1", models.UserTypeCustomer, errors.New("boom"))
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
	m := NewManager(host)

	m.TrackConnectionError(context.Background(), "conn-1", models.UserTypeCustomer, nil)
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
		m.TrackConnectionError(context.Background(), "conn-x", ut, errors.New("boom"))
	}
	assert.Empty(t, quality.errors, "系统/机器人/观察者不应记录错误")
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

// recordingQualityStore 记录 AddError 调用。
// 嵌入 spi.ConnectionQualityStore（nil）—— 未被覆盖的方法被调用即 panic，
// 从而暴露本包对质量仓储的、测试尚未覆盖的依赖。
type recordingQualityStore struct {
	spi.ConnectionQualityStore

	errors []error
}

func (s *recordingQualityStore) AddError(ctx context.Context, connectionID string, err error) error {
	s.errors = append(s.errors, err)
	return nil
}

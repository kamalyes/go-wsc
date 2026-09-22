/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 23:39:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 23:39:00
 * @FilePath: \go-wsc\messaging\offline_test.go
 * @Description: 离线队列装配用例（自 wiring/storage_test.go 迁入）
 *
 * InitializeOfflineQueue 跟随混合处理器落在 messaging，装配校验用例随之迁入。
 * 桩实现仅满足接口：这里验证的是「两半依赖齐不齐、注入有没有落到 Hub」，
 * 存储方法的行为属于适配器测试的范畴（由各适配器包自己的测试覆盖）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 桩实现
// ============================================================================

// stubTarget spi.StoreTarget 的记账桩：只记录离线处理器是否被注入
type stubTarget struct {
	offline spi.OfflineQueue
}

func (t *stubTarget) SetOnlineStatusRepository(s spi.OnlineStore)         { _ = s }
func (t *stubTarget) SetHubStatsRepository(s spi.HubStats)                { _ = s }
func (t *stubTarget) SetGroupRepository(s spi.GroupStore)                 { _ = s }
func (t *stubTarget) SetWorkloadRepository(s spi.WorkloadStore)           { _ = s }
func (t *stubTarget) SetMessageRecordRepository(s spi.MessageSink)        { _ = s }
func (t *stubTarget) SetConnectionRecordRepository(s spi.ConnectionStore) { _ = s }
func (t *stubTarget) SetConnectionQualityRepository(s spi.ConnectionQualityStore) {
	_ = s
}
func (t *stubTarget) SetOfflineMessageHandler(q spi.OfflineQueue) { t.offline = q }

var _ spi.StoreTarget = (*stubTarget)(nil)

// ============================================================================
// InitializeOfflineQueue 参数校验与注入
// ============================================================================

// TestInitializeOfflineQueue_Validation 两半依赖任一缺失即报错
//
// 离线消息是「用户离线期间消息不丢」这条承诺的唯一兜底。依赖缺失时若静默
// 跳过，消息会在无人察觉的情况下丢失 —— 这类失败没有报错、没有告警，只有
// 用户投诉「消息没了」，代价极高。故宁可启动期硬失败。
func TestInitializeOfflineQueue_Validation(t *testing.T) {
	cfg := wscconfig.DefaultOfflineMessage()

	cases := []struct {
		name string
		deps OfflineDeps
		want string
	}{
		{
			name: "队列缺失",
			deps: OfflineDeps{Queue: nil, Store: &stubOfflineStore{}, Config: cfg},
			want: "offline queue is nil",
		},
		{
			name: "持久化缺失",
			deps: OfflineDeps{Queue: &stubQueue{}, Store: nil, Config: cfg},
			want: "offline store is nil",
		},
		{
			name: "配置缺失",
			deps: OfflineDeps{Queue: &stubQueue{}, Store: &stubOfflineStore{}, Config: nil},
			want: "offline config is nil",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := InitializeOfflineQueue(&stubTarget{}, tc.deps)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestInitializeOfflineQueue_NilHub 注入目标为 nil 时优先报错
//
// 校验顺序：先看 hub，再看依赖。hub 为 nil 是调用方写错了参数，
// 与「哪个适配器没配」是两类问题，错误信息不应混为一谈。
func TestInitializeOfflineQueue_NilHub(t *testing.T) {
	err := InitializeOfflineQueue(nil, OfflineDeps{
		Queue:  &stubQueue{},
		Store:  &stubOfflineStore{},
		Config: wscconfig.DefaultOfflineMessage(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub target is nil")
}

// TestInitializeOfflineQueue_Injects 依赖齐备时注入成功
func TestInitializeOfflineQueue_Injects(t *testing.T) {
	target := &stubTarget{}

	err := InitializeOfflineQueue(target, OfflineDeps{
		Queue:  &stubQueue{},
		Store:  &stubOfflineStore{},
		Config: wscconfig.DefaultOfflineMessage(),
	})
	require.NoError(t, err)
	assert.NotNil(t, target.offline, "混合离线消息处理器应已注入 Hub")
}

// ============================================================================
// 离线消息两半契约的桩（仅满足接口，供组装用例使用）
// ============================================================================

type stubQueue struct{}

func (q *stubQueue) Enqueue(_ context.Context, _ string, _ *models.HubMessage) error { return nil }
func (q *stubQueue) Dequeue(_ context.Context, _ string, _ time.Duration) (*models.HubMessage, error) {
	return nil, nil
}
func (q *stubQueue) DequeueBatch(_ context.Context, _ string, _ int) ([]*models.HubMessage, error) {
	return nil, nil
}
func (q *stubQueue) GetLength(_ context.Context, _ string) (int64, error) { return 0, nil }
func (q *stubQueue) Clear(_ context.Context, _ string) error              { return nil }
func (q *stubQueue) Peek(_ context.Context, _ string) (*models.HubMessage, error) {
	return nil, nil
}

var _ spi.MessageQueue = (*stubQueue)(nil)

type stubOfflineStore struct{}

func (s *stubOfflineStore) Save(_ context.Context, _ *models.OfflineMessageRecord) error {
	return nil
}
func (s *stubOfflineStore) BatchSave(_ context.Context, _ []*models.OfflineMessageRecord) error {
	return nil
}
func (s *stubOfflineStore) QueryMessages(_ context.Context, _ *spi.OfflineMessageFilter) ([]*models.OfflineMessageRecord, error) {
	return nil, nil
}
func (s *stubOfflineStore) DeleteByMessageIDs(_ context.Context, _, _, _ string, _ []string) error {
	return nil
}
func (s *stubOfflineStore) GetCountByReceiver(_ context.Context, _, _, _ string) (int64, error) {
	return 0, nil
}
func (s *stubOfflineStore) GetCountBySender(_ context.Context, _, _, _ string) (int64, error) {
	return 0, nil
}
func (s *stubOfflineStore) ClearByReceiver(_ context.Context, _, _, _ string) error { return nil }
func (s *stubOfflineStore) DeleteExpired(_ context.Context) (int64, error)          { return 0, nil }
func (s *stubOfflineStore) UpdatePushStatus(_ context.Context, _ []string, _ models.MessageSendStatus, _ string) error {
	return nil
}
func (s *stubOfflineStore) CleanupOld(_ context.Context, _ time.Time) (int64, error) { return 0, nil }
func (s *stubOfflineStore) Close() error                                             { return nil }

var _ spi.OfflineStore = (*stubOfflineStore)(nil)

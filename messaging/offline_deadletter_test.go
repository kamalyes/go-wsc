/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 23:06:11
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 23:06:11
 * @FilePath: \go-wsc\messaging\offline_deadletter_test.go
 * @Description: 离线推送死信闭环测试（毒消息防御：重试耗尽过滤与失败状态攒批）
 *
 * 毒消息场景：客户端持续不 ACK 的消息经 ACK 超时转存离线后，每次上线 drain
 * 都会被捞出重推，推送失败仅递增 retry_count——若读取侧不过滤重试耗尽的记录，
 * 重连风暴下毒消息将无限反复重推（DB 写放大）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
)

// deadLetterSpyStore 记录查询过滤条件与推送状态更新的存储桩（其余方法 no-op）
type deadLetterSpyStore struct {
	mu           sync.Mutex
	lastFilter   *spi.OfflineMessageFilter
	pushCalls    [][]string                 // 每次 UpdatePushStatus 的 messageIDs 快照
	pushStatuses []models.MessageSendStatus // 与 pushCalls 一一对应
}

func (s *deadLetterSpyStore) QueryMessages(_ context.Context, filter *spi.OfflineMessageFilter) ([]*models.OfflineMessageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFilter = filter
	return nil, nil
}

func (s *deadLetterSpyStore) UpdatePushStatus(_ context.Context, messageIDs []string, status models.MessageSendStatus, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushCalls = append(s.pushCalls, append([]string(nil), messageIDs...))
	s.pushStatuses = append(s.pushStatuses, status)
	return nil
}

func (s *deadLetterSpyStore) Save(context.Context, *models.OfflineMessageRecord) error {
	// 双写语义下任一侧成功即算转存成功，两侧恒失败才能驱动推送失败路径
	return errors.New("offline store write failed")
}
func (s *deadLetterSpyStore) BatchSave(context.Context, []*models.OfflineMessageRecord) error {
	return nil
}
func (s *deadLetterSpyStore) DeleteByMessageIDs(context.Context, string, string, string, []string) error {
	return nil
}
func (s *deadLetterSpyStore) GetCountByReceiver(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (s *deadLetterSpyStore) GetCountBySender(context.Context, string, string, string) (int64, error) {
	return 0, nil
}
func (s *deadLetterSpyStore) ClearByReceiver(context.Context, string, string, string) error {
	return nil
}
func (s *deadLetterSpyStore) DeleteExpired(context.Context) (int64, error)         { return 0, nil }
func (s *deadLetterSpyStore) CleanupOld(context.Context, time.Time) (int64, error) { return 0, nil }
func (s *deadLetterSpyStore) Close() error                                         { return nil }

var _ spi.OfflineStore = (*deadLetterSpyStore)(nil)

// deadLetterFullQueue 恒队列满的队列桩（模拟离线存储失败，驱动推送失败路径）
type deadLetterFullQueue struct{}

func (q *deadLetterFullQueue) Enqueue(context.Context, string, *models.HubMessage) error {
	return errors.New("offline queue full")
}
func (q *deadLetterFullQueue) Dequeue(context.Context, string, time.Duration) (*models.HubMessage, error) {
	return nil, nil
}
func (q *deadLetterFullQueue) DequeueBatch(context.Context, string, int) ([]*models.HubMessage, error) {
	return nil, nil
}
func (q *deadLetterFullQueue) GetLength(context.Context, string) (int64, error) { return 0, nil }
func (q *deadLetterFullQueue) Clear(context.Context, string) error              { return nil }
func (q *deadLetterFullQueue) Peek(context.Context, string) (*models.HubMessage, error) {
	return nil, nil
}

var _ spi.MessageQueue = (*deadLetterFullQueue)(nil)

// deadLetterDrainCtx 上线回放语义的路由 ctx（P2P 队列维度）
func deadLetterDrainCtx() context.Context {
	return routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		Inject(context.Background())
}

// TestGetOfflineMessagesExcludesExhausted MySQL 兜底查询必须携带死信过滤
// （重试耗尽的毒消息不再进入推送流程，留在表内供审计，由过期/清理任务回收）
func TestGetOfflineMessagesExcludesExhausted(t *testing.T) {
	spy := &deadLetterSpyStore{}
	handler := NewHybridOfflineMessageHandler(
		&deadLetterFullQueue{},
		spy,
		wscconfig.DefaultOfflineMessage(),
		spi.NewDefaultLogger(),
	)

	msgs, next, err := handler.GetOfflineMessages(deadLetterDrainCtx(), "u-1", 100, "")
	require.NoError(t, err)
	assert.Empty(t, msgs)
	assert.Empty(t, next)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	require.NotNil(t, spy.lastFilter, "应发起 MySQL 兜底查询")
	assert.True(t, spy.lastFilter.ExcludeExhausted, "兜底查询必须排除重试耗尽的毒消息")
}

// TestPushAndDeleteOfflineBatchesFailureStatus 推送失败状态攒批单次 UPDATE
// （N 条失败 = 1 次同步 DB 调用，消除逐条 N+1；retry_count 由存储侧原子递增）
func TestPushAndDeleteOfflineBatchesFailureStatus(t *testing.T) {
	m, _ := newTestManager()
	spy := &deadLetterSpyStore{}
	m.offlineHandler = NewHybridOfflineMessageHandler(
		&deadLetterFullQueue{},
		spy,
		wscconfig.DefaultOfflineMessage(),
		spi.NewDefaultLogger(),
	)

	const total = 3
	messages := make([]*models.HubMessage, 0, total)
	for i := 0; i < total; i++ {
		messages = append(messages, makeGroupMessage("sender-1").WithGuarantee(models.GuaranteeGuaranteed))
	}

	pushed, failed := m.pushAndDeleteOffline(deadLetterDrainCtx(), "u-offline", messages)
	assert.Empty(t, pushed, "离线存储恒失败时不应有成功推送")
	require.Len(t, failed, total, "全部消息应进入失败清单")

	spy.mu.Lock()
	defer spy.mu.Unlock()
	require.Len(t, spy.pushCalls, 1, "失败状态应攒批为单次 UPDATE（消除逐条 N+1）")
	assert.ElementsMatch(t, failed, spy.pushCalls[0], "攒批 UPDATE 应覆盖全部失败消息")
	require.Len(t, spy.pushStatuses, 1)
	assert.Equal(t, models.MessageSendStatusFailed, spy.pushStatuses[0], "失败推送应标记 Failed 并由存储侧递增 retry_count")
}

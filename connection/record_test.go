/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:08:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 19:08:00
 * @FilePath: \go-wsc\connection\record_test.go
 * @Description: 连接域连接记录管理器测试 - 快照构造 / 异步落库 / 停机批量终态

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// fakeRecordHost 连接记录端口测试桩：仓储可运行期切换（未注入 → 注入）
type fakeRecordHost struct {
	mu    sync.Mutex
	store spi.ConnectionStore
}

// GetLogger 返回 nil（构造器内部兜底默认日志器）
func (f *fakeRecordHost) GetLogger() spi.Logger { return nil }

func (f *fakeRecordHost) GetConnectionRecordRepo() spi.ConnectionStore {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.store
}

// markDisconnectedCall 记录一次 MarkDisconnected 调用参数
type markDisconnectedCall struct {
	connectionID string
	reason      models.DisconnectReason
	code        int
}

// fakeRecordStore 连接记录仓储桩：只覆盖记录语义相关方法（未覆盖方法 panic）
type fakeRecordStore struct {
	spi.ConnectionStore // 未覆盖的方法调用即 panic

	mu           sync.Mutex
	upserts      []*models.ConnectionRecord
	disconnected []markDisconnectedCall
}

func (f *fakeRecordStore) Upsert(_ context.Context, record *models.ConnectionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, record)
	return nil
}

func (f *fakeRecordStore) MarkDisconnected(_ context.Context, connectionID string, reason models.DisconnectReason, code int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.disconnected = append(f.disconnected, markDisconnectedCall{connectionID, reason, code})
	return nil
}

func (f *fakeRecordStore) upsertCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.upserts)
}

func (f *fakeRecordStore) disconnectCalls() []markDisconnectedCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]markDisconnectedCall(nil), f.disconnected...)
}

// newRecordManagerFixture 构造记录管理器与端口桩
func newRecordManagerFixture(t *testing.T) (*RecordManager, *fakeRecordHost, *fakeRecordStore) {
	t.Helper()
	store := &fakeRecordStore{}
	host := &fakeRecordHost{store: store}
	return NewRecordManager(host), host, store
}

// TestRecordCreateSnapshot 记录构造：Client 关键字段快照到 ConnectionRecord
func TestRecordCreateSnapshot(t *testing.T) {
	manager, _, _ := newRecordManagerFixture(t)

	client := models.NewClient("rec-1", "u-13000", models.UserTypeCustomer)
	client.NodeID = "node-a"
	client.NodeIP = "10.0.0.1"
	client.NodePort = 8080
	client.ConnectionType = models.ConnectionTypeWebSocket
	client.SetMetadataValue("device", "ios")

	record := manager.Create(client)

	assert.Equal(t, client.ID, record.ConnectionID)
	assert.Equal(t, client.UserID, record.UserID)
	assert.Equal(t, "node-a", record.NodeID)
	assert.Equal(t, "10.0.0.1", record.NodeIP)
	assert.Equal(t, 8080, record.NodePort)
	assert.Equal(t, models.ConnectionTypeWebSocket, record.Protocol)
	assert.True(t, record.IsActive, "新建记录应为活跃状态")
	assert.Equal(t, client.ConnectedAt, record.ConnectedAt)
	assert.NotNil(t, record.Metadata, "metadata 快照应写入记录")
}

// TestRecordSaveUpsertsAsync 异步保存：经 syncx.Go 异步 Upsert（Eventually 等待）
func TestRecordSaveUpsertsAsync(t *testing.T) {
	manager, _, store := newRecordManagerFixture(t)

	client := models.NewClient("rec-2", "u-13001", models.UserTypeCustomer)
	record := manager.Create(client)

	manager.Save(context.Background(), record)

	require.Eventually(t, func() bool {
		return store.upsertCount() == 1
	}, 2*time.Second, 10*time.Millisecond, "记录应被异步 Upsert")
}

// TestRecordMarkDisconnectedAsync 异步标记断开：单连接路径 reason=ClientRequest、code=0
func TestRecordMarkDisconnectedAsync(t *testing.T) {
	manager, _, store := newRecordManagerFixture(t)

	client := models.NewClient("rec-3", "u-13002", models.UserTypeCustomer)
	manager.MarkDisconnected(context.Background(), client)

	require.Eventually(t, func() bool {
		return len(store.disconnectCalls()) == 1
	}, 2*time.Second, 10*time.Millisecond, "断开标记应被异步写入")
	call := store.disconnectCalls()[0]
	assert.Equal(t, client.ID, call.connectionID)
	assert.Equal(t, models.DisconnectReasonClientRequest, call.reason)
	assert.Zero(t, call.code)
}

// TestRecordMarkDisconnectedBatch 停机批量终态：并行标记 reason=ServerShutdown、code=1001
func TestRecordMarkDisconnectedBatch(t *testing.T) {
	manager, _, store := newRecordManagerFixture(t)

	clients := []*models.Client{
		models.NewClient("rec-b1", "u-13003", models.UserTypeCustomer),
		models.NewClient("rec-b2", "u-13003", models.UserTypeCustomer),
		models.NewClient("rec-b3", "u-13003", models.UserTypeCustomer),
	}

	manager.MarkDisconnectedBatch(clients)

	// 并行同步执行：调用返回时已全部完成
	calls := store.disconnectCalls()
	require.Len(t, calls, 3, "每个连接应被标记一次断开")
	ids := make(map[string]bool, len(calls))
	for _, call := range calls {
		assert.Equal(t, models.DisconnectReasonServerShutdown, call.reason)
		assert.Equal(t, 1001, call.code, "停机路径应携带 1001 GoingAway 关闭码")
		ids[call.connectionID] = true
	}
	for _, client := range clients {
		assert.True(t, ids[client.ID], "连接 %s 应被标记", client.ID)
	}
}

// TestRecordWithoutStoreNoOp 仓储未注入：全路径 no-op 降级不 panic
func TestRecordWithoutStoreNoOp(t *testing.T) {
	host := &fakeRecordHost{store: nil} // 未注入仓储
	manager := NewRecordManager(host)
	client := models.NewClient("rec-noop", "u-13004", models.UserTypeCustomer)
	record := manager.Create(client)

	require.NotPanics(t, func() {
		manager.Save(context.Background(), record)
		manager.MarkDisconnected(context.Background(), client)
		manager.MarkDisconnectedBatch([]*models.Client{client})
	})
}

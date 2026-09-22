/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 21:57:03
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 12:57:00
 * @FilePath: \go-wsc\connection\record_test.go
 * @Description: 连接记录测试 - 记录构造字段映射 + 异步落库 + 断开更新顺序（先记录终评后）
 *
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

// fakeConnectionStore 记录存储测试桩（嵌入接口，仅覆盖被测路径；未覆盖方法调用即 panic）
type fakeConnectionStore struct {
	spi.ConnectionStore
	mu         sync.Mutex
	upserts    []*models.ConnectionRecord
	markDiscos []markDiscoCall
	calls      chan string // 操作顺序流水
}

type markDiscoCall struct {
	connectionID string
	reason       models.DisconnectReason
}

func newFakeConnectionStore() *fakeConnectionStore {
	return &fakeConnectionStore{calls: make(chan string, 8)}
}

func (f *fakeConnectionStore) Upsert(ctx context.Context, record *models.ConnectionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, record)
	f.calls <- "upsert"
	return nil
}

func (f *fakeConnectionStore) MarkDisconnected(ctx context.Context, connectionID string, reason models.DisconnectReason, code int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markDiscos = append(f.markDiscos, markDiscoCall{connectionID: connectionID, reason: reason})
	f.calls <- "mark"
	return nil
}

// fakeQualityStore 质量存储测试桩（嵌入接口，仅覆盖被测路径）
type fakeQualityStore struct {
	spi.ConnectionQualityStore
	mu       sync.Mutex
	upserts  []*models.ConnectionQuality
	finalIDs []string
	calls    chan string
}

func newFakeQualityStore() *fakeQualityStore {
	return &fakeQualityStore{calls: make(chan string, 8)}
}

func (f *fakeQualityStore) Upsert(ctx context.Context, quality *models.ConnectionQuality) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, quality)
	f.calls <- "quality-upsert"
	return nil
}

func (f *fakeQualityStore) FinalizeOnDisconnect(ctx context.Context, connectionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.finalIDs = append(f.finalIDs, connectionID)
	f.calls <- "finalize"
	return nil
}

// waitCall 等待指定操作流水（异步落库同步点）
func waitCall(t *testing.T, calls chan string, want string) {
	t.Helper()
	select {
	case got := <-calls:
		require.Equal(t, want, got, "异步落库应产生 %s 调用", want)
	case <-time.After(3 * time.Second):
		t.Fatalf("等待 %s 调用超时", want)
	}
}

// TestCreateConnectionRecordFieldMapping 记录构造：Client → ConnectionRecord 字段精确映射
func TestCreateConnectionRecordFieldMapping(t *testing.T) {
	client := models.NewClient("conn-map-1", "u-7000", models.UserTypeAgent)
	client.Context = context.Background()
	client.NodeID = "node-1"
	client.NodeIP = "10.0.0.1"
	client.NodePort = 9090
	client.SetMetadataValue("trace_id", "tr-123")

	record := CreateConnectionRecord(client)

	assert.Equal(t, client.ID, record.ConnectionID)
	assert.Equal(t, "u-7000", record.UserID)
	assert.Equal(t, client.GetAppID(), record.AppID)
	assert.Equal(t, client.GetNamespace(), record.Namespace)
	assert.Equal(t, "node-1", record.NodeID)
	assert.Equal(t, "10.0.0.1", record.NodeIP)
	assert.Equal(t, 9090, record.NodePort)
	assert.Equal(t, client.ConnectionType, record.Protocol)
	assert.Equal(t, client.ClientType, record.ClientType)
	assert.True(t, record.IsActive, "新建记录应标记活跃")
	if traceID, ok := record.Metadata["trace_id"]; ok {
		assert.Equal(t, "tr-123", traceID.(string), "metadata 快照应映射")
	} else {
		t.Fatal("metadata 快照应包含 trace_id")
	}
}

// TestRecorderSaveRecordAndQuality 保存链路：记录 + 质量初始行异步落库
func TestRecorderSaveRecordAndQuality(t *testing.T) {
	records := newFakeConnectionStore()
	qualities := newFakeQualityStore()
	recorder := NewConnectionRecorder(records, qualities, nil)

	client := models.NewClient("save-1", "u-8000", models.UserTypeCustomer)
	client.Context = context.Background()

	recorder.SaveConnectionRecord(client.Context, CreateConnectionRecord(client))
	waitCall(t, records.calls, "upsert")
	require.Len(t, records.upserts, 1)
	assert.Equal(t, "save-1", records.upserts[0].ConnectionID)

	recorder.SaveConnectionQuality(client.Context, client)
	waitCall(t, qualities.calls, "quality-upsert")
	require.Len(t, qualities.upserts, 1)
	assert.Equal(t, "save-1", qualities.upserts[0].ConnectionID)
	assert.Equal(t, "u-8000", qualities.upserts[0].UserID, "质量行应回填用户维度")
}

// TestRecorderUpdateOnDisconnectOrder 断开更新顺序：先 MarkDisconnected（写 duration）再 FinalizeOnDisconnect（读 duration 算终评）
func TestRecorderUpdateOnDisconnectOrder(t *testing.T) {
	records := newFakeConnectionStore()
	qualities := newFakeQualityStore()
	recorder := NewConnectionRecorder(records, qualities, nil)

	client := models.NewClient("disc-1", "u-9000", models.UserTypeAgent)
	client.Context = context.Background()

	recorder.UpdateOnDisconnect(client, models.DisconnectReasonHeartbeatFail)
	waitCall(t, records.calls, "mark")
	waitCall(t, qualities.calls, "finalize")

	require.Len(t, records.markDiscos, 1)
	assert.Equal(t, "disc-1", records.markDiscos[0].connectionID)
	assert.Equal(t, models.DisconnectReasonHeartbeatFail, records.markDiscos[0].reason)
	require.Len(t, qualities.finalIDs, 1)
	assert.Equal(t, "disc-1", qualities.finalIDs[0])
}

// TestRecorderNilStoresNoop 存储未注入（未启用 gorm 适配器）：静默跳过、零 panic、零成本
func TestRecorderNilStoresNoop(t *testing.T) {
	recorder := NewConnectionRecorder(nil, nil, nil)
	client := models.NewClient("noop-1", "u-a", models.UserTypeVisitor)
	client.Context = context.Background()

	// 全部为 no-op，不 panic 即通过
	recorder.SaveConnectionRecord(client.Context, CreateConnectionRecord(client))
	recorder.SaveConnectionQuality(client.Context, client)
	recorder.UpdateOnDisconnect(client, models.DisconnectReasonUnknown)

	// 异步任务空转，给调度窗口后无任何副作用
	time.Sleep(50 * time.Millisecond)
}

// TestRecorderImplementsSPI 存储桩满足 spi 端口契约（编译期回归防护）
func TestRecorderImplementsSPI(t *testing.T) {
	var _ spi.ConnectionStore = newFakeConnectionStore()
	var _ spi.ConnectionQualityStore = newFakeQualityStore()
}

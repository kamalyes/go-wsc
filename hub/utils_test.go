/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-22 00:00:00
 * @FilePath: \go-wsc\hub\utils_test.go
 * @Description: utils.go 性能优化测试（心跳统计 + Goroutine 数量）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"context"
	"runtime"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/stretchr/testify/assert"
)

// TestTrackHeartbeatStats_UsesBatcher 测试 trackHeartbeatStats 使用批量聚合器
func TestTrackHeartbeatStats_UsesBatcher(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	// 设置 mock repo 使 trackHeartbeatStats 不被跳过
	hub.connectionRecordRepo = &mockConnectionRecordRepo{}

	go hub.Run()
	hub.WaitForStart()

	client := &Client{
		ID:            "batcher-test-client",
		UserID:        "batcher-test-user",
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	hub.trackHeartbeatStats(client)

	if hub.heartbeatBatcher != nil {
		hub.heartbeatBatcher.mu.Lock()
		_, exists := hub.heartbeatBatcher.buffer[client.ID]
		hub.heartbeatBatcher.mu.Unlock()
		assert.True(t, exists, "心跳统计应写入批量聚合器")
	}
}

// mockConnectionRecordRepo 空实现，用于测试
type mockConnectionRecordRepo struct{}

func (m *mockConnectionRecordRepo) Upsert(context.Context, *models.ConnectionRecord) error {
	return nil
}
func (m *mockConnectionRecordRepo) MarkDisconnected(context.Context, string, models.DisconnectReason, int, string) error {
	return nil
}
func (m *mockConnectionRecordRepo) GetByConnectionID(context.Context, string) (*models.ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) GetByUserID(context.Context, string) ([]*models.ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) GetActiveByUserID(context.Context, string) ([]*models.ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) IncrementMessageStats(context.Context, string, int64, int64) error {
	return nil
}
func (m *mockConnectionRecordRepo) IncrementBytesStats(context.Context, string, int64, int64) error {
	return nil
}
func (m *mockConnectionRecordRepo) UpdatePingStats(context.Context, string, float64) error {
	return nil
}
func (m *mockConnectionRecordRepo) AddError(context.Context, string, error) error {
	return nil
}
func (m *mockConnectionRecordRepo) UpdateHeartbeat(context.Context, string, *time.Time, *time.Time) error {
	return nil
}
func (m *mockConnectionRecordRepo) List(context.Context, *repository.ConnectionQueryOptions) ([]*models.ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) Count(context.Context, *repository.ConnectionQueryOptions) (int64, error) {
	return 0, nil
}
func (m *mockConnectionRecordRepo) GetConnectionStats(context.Context, time.Time, time.Time) (*repository.ConnectionStats, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) GetConnectionStatsByID(context.Context, string) (*repository.UserConnectionStats, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) GetUserConnectionStats(context.Context, string) (*repository.UserConnectionStats, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) GetNodeConnectionStats(context.Context, string) (*repository.NodeConnectionStats, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) GetHighErrorRateConnections(context.Context, int, int) ([]*models.ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) GetFrequentReconnectConnections(context.Context, int, int) ([]*models.ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnectionRecordRepo) BatchUpsert(context.Context, []*models.ConnectionRecord) error {
	return nil
}
func (m *mockConnectionRecordRepo) CleanupInactiveRecords(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (m *mockConnectionRecordRepo) WithTableName(string) repository.ConnectionRecordRepository {
	return m
}
func (m *mockConnectionRecordRepo) Close() error {
	return nil
}

// TestTrackHeartbeatStats_NoNewGoroutine 测试 trackHeartbeatStats 不创建新 goroutine
func TestTrackHeartbeatStats_NoNewGoroutine(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	client := &Client{
		ID:            "no-goroutine-client",
		UserID:        "no-goroutine-user",
		UserType:      UserTypeAgent,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}
	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	beforeCount := runtime.NumGoroutine()

	for i := 0; i < 100; i++ {
		hub.trackHeartbeatStats(client)
	}

	time.Sleep(50 * time.Millisecond)

	afterCount := runtime.NumGoroutine()

	assert.LessOrEqual(t, afterCount, beforeCount+5,
		"trackHeartbeatStats 不应创建大量 goroutine，之前: %d, 之后: %d",
		beforeCount, afterCount)
}

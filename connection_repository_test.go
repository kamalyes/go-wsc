/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 00:00:00
 * @FilePath: \go-wsc\connection_repository_test.go
 * @Description: WebSocket连接记录仓储测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// testConnectionRepoContext 测试上下文
type testConnectionRepoContext struct {
	t           *testing.T
	db          *gorm.DB
	repo        repository.ConnectionRecordRepository
	ctx         context.Context
	idGenerator *idgen.ShortFlakeGenerator
	tableName   string
}

// newTestConnectionRepoContext 创建测试上下文
func newTestConnectionRepoContext(t *testing.T) *testConnectionRepoContext {
	workerID := osx.GetWorkerIdForSnowflake()

	// 使用测试函数名生成唯一的表名（移除特殊字符）
	testName := strings.ReplaceAll(t.Name(), "/", "_")
	testName = strings.ReplaceAll(testName, " ", "_")
	idGenerator := getTestIDGenerator()
	table := idGenerator.GenerateSpanID()
	tableName := fmt.Sprintf("wsc_connection_records_%s", table)

	// 获取基础数据库连接并创建表
	db := getConnectTestDB(t, tableName)

	// 创建 repository 并使用 WithTableName 设置自定义表名
	baseRepo := repository.NewConnectionRecordRepository(db, nil, NewDefaultWSCLogger())
	scopedRepo := baseRepo.WithTableName(tableName)

	tc := &testConnectionRepoContext{
		t:           t,
		db:          db,
		repo:        scopedRepo,
		ctx:         context.Background(),
		idGenerator: idgen.NewShortFlakeGenerator(workerID),
		tableName:   tableName,
	}

	// 测试结束后清理表
	t.Cleanup(func() {
		db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS `%s`", tableName))
	})

	return tc
}

// generateUserID 生成用户ID
func (c *testConnectionRepoContext) generateUserID() string {
	return fmt.Sprintf("user-%020d", c.idGenerator.Generate())
}

// generateConnectionID 生成连接ID
func (c *testConnectionRepoContext) generateConnectionID() string {
	return fmt.Sprintf("conn-%020d", c.idGenerator.Generate())
}

var (
	testConnectDBInstance *gorm.DB
	testConnectDBOnce     sync.Once
)

// getConnectTestDB 获取测试数据库连接并创建独立表
func getConnectTestDB(t *testing.T, tableName string) *gorm.DB {
	testConnectDBOnce.Do(func() {
		db := GetTestDB(t)
		testConnectDBInstance = db
	})

	db := testConnectDBInstance

	// 先删除表（如果存在）
	if db.Migrator().HasTable(tableName) {
		err := db.Migrator().DropTable(tableName)
		require.NoError(t, err, "删除表失败: %s", tableName)
	}

	// 使用 Migrator 创建自定义表名的表
	err := db.Table(tableName).Migrator().CreateTable(&models.ConnectionRecord{})
	require.NoError(t, err, "创建表失败: %s", tableName)

	// 验证表已创建（等待一小段时间确保创建完成）
	time.Sleep(50 * time.Millisecond)
	require.True(t, db.Migrator().HasTable(tableName), "表创建后验证失败: %s", tableName)

	return db
}

// TestUpsert 测试创建或更新连接记录
func TestUpsert(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	userID := tc.generateUserID()
	connID1 := tc.generateConnectionID()

	// 首次连接 - 创建记录
	record := &models.ConnectionRecord{
		ConnectionID: connID1,
		UserID:       userID,
		NodeID:       "node-1",
		ClientIP:     "192.168.1.1",
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}

	err := tc.repo.Upsert(tc.ctx, record)
	assert.NoError(t, err)

	// 验证记录已创建
	saved, err := tc.repo.GetByConnectionID(tc.ctx, connID1)
	assert.NoError(t, err)
	assert.Equal(t, connID1, saved.ConnectionID)
	assert.Equal(t, 0, saved.ReconnectCount)

	// 同一连接重连 - 更新记录
	time.Sleep(100 * time.Millisecond)
	err = tc.repo.Upsert(tc.ctx, record)
	assert.NoError(t, err)

	// 验证重连次数增加
	updated, err := tc.repo.GetByConnectionID(tc.ctx, connID1)
	assert.NoError(t, err)
	assert.Equal(t, 1, updated.ReconnectCount)
	assert.True(t, updated.IsActive)
}

// TestMultiDeviceLogin 测试多设备登录
func TestMultiDeviceLogin(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	userID := tc.generateUserID()

	// 设备1连接
	conn1 := &models.ConnectionRecord{
		ConnectionID: tc.generateConnectionID(),
		UserID:       userID,
		NodeID:       "node-1",
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err := tc.repo.Upsert(tc.ctx, conn1)
	assert.NoError(t, err)

	// 设备2连接
	conn2 := &models.ConnectionRecord{
		ConnectionID: tc.generateConnectionID(),
		UserID:       userID,
		NodeID:       "node-1",
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err = tc.repo.Upsert(tc.ctx, conn2)
	assert.NoError(t, err)

	// 验证用户有2个活跃连接
	activeConns, err := tc.repo.GetActiveByUserID(tc.ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(activeConns))
}

// TestMarkDisconnected 测试标记断开连接
func TestMarkDisconnected(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	connID := tc.generateConnectionID()

	// 创建连接记录
	record := &models.ConnectionRecord{
		ConnectionID: connID,
		UserID:       tc.generateUserID(),
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err := tc.repo.Upsert(tc.ctx, record)
	assert.NoError(t, err)

	// 标记断开
	time.Sleep(100 * time.Millisecond)
	err = tc.repo.MarkDisconnected(tc.ctx, connID, models.DisconnectReasonTimeout, 1001, "连接超时")
	assert.NoError(t, err)

	// 验证断开信息
	disconnected, err := tc.repo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	assert.False(t, disconnected.IsActive)
	assert.NotNil(t, disconnected.DisconnectedAt)
	assert.Equal(t, string(models.DisconnectReasonTimeout), disconnected.DisconnectReason)
	assert.Equal(t, 1001, disconnected.DisconnectCode)
	assert.True(t, disconnected.IsAbnormal)
}

// TestIncrementMessageStats 测试增加消息统计
func TestIncrementMessageStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	connID := tc.generateConnectionID()

	// 创建连接记录
	record := &models.ConnectionRecord{
		ConnectionID: connID,
		UserID:       tc.generateUserID(),
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err := tc.repo.Upsert(tc.ctx, record)
	assert.NoError(t, err)

	// 增加消息统计
	err = tc.repo.IncrementMessageStats(tc.ctx, connID, 10, 5)
	assert.NoError(t, err)

	// 验证统计
	updated, err := tc.repo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	assert.Equal(t, int64(10), updated.MessagesSent)
	assert.Equal(t, int64(5), updated.MessagesReceived)
}

// TestUpdatePingStats 测试更新Ping统计
func TestUpdatePingStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	connID := tc.generateConnectionID()

	// 创建连接记录
	record := &models.ConnectionRecord{
		ConnectionID: connID,
		UserID:       tc.generateUserID(),
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err := tc.repo.Upsert(tc.ctx, record)
	assert.NoError(t, err)

	// 更新Ping统计
	err = tc.repo.UpdatePingStats(tc.ctx, connID, 50.5)
	assert.NoError(t, err)

	updated, err := tc.repo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	assert.Equal(t, 50.5, updated.AveragePingMs)
	assert.Equal(t, 50.5, updated.MaxPingMs)
	assert.Equal(t, 50.5, updated.MinPingMs)

	// 再次更新
	err = tc.repo.UpdatePingStats(tc.ctx, connID, 30.0)
	assert.NoError(t, err)

	updated, err = tc.repo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	assert.Equal(t, 40.25, updated.AveragePingMs)
	assert.Equal(t, 50.5, updated.MaxPingMs)
	assert.Equal(t, 30.0, updated.MinPingMs)
}

// TestAddError 测试记录错误
func TestAddError(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	connID := tc.generateConnectionID()

	// 创建连接记录
	record := &models.ConnectionRecord{
		ConnectionID: connID,
		UserID:       tc.generateUserID(),
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err := tc.repo.Upsert(tc.ctx, record)
	assert.NoError(t, err)

	// 记录错误
	testErr := errors.New("test error")
	err = tc.repo.AddError(tc.ctx, connID, testErr)
	assert.NoError(t, err)

	// 验证错误记录
	updated, err := tc.repo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	assert.Equal(t, 1, updated.ErrorCount)
	assert.Equal(t, "test error", updated.LastError)
	assert.NotNil(t, updated.LastErrorAt)
}

// TestListWithOptions 测试条件查询
func TestListWithOptions(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	// 调试：检查表是否为空
	initialCount, err := tc.repo.Count(tc.ctx, nil)
	require.NoError(t, err)
	t.Logf("表 %s 初始记录数: %d", tc.tableName, initialCount)

	total := 5

	// 创建多个连接记录
	for i := 1; i <= total; i++ {
		isActive := i <= 3
		record := &models.ConnectionRecord{
			ConnectionID: tc.generateConnectionID(),
			UserID:       tc.generateUserID(),
			NodeID:       fmt.Sprintf("node-%d", (i-1)%2+1),
			ConnectedAt:  time.Now(),
			IsActive:     isActive,
		}
		t.Logf("创建记录 %d: ConnectionID=%s, IsActive=%v", i, record.ConnectionID, record.IsActive)
		err := tc.repo.Upsert(tc.ctx, record)
		assert.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	// 验证插入后的数据
	allRecords, err := tc.repo.List(tc.ctx, nil)
	assert.NoError(t, err)
	t.Logf("总记录数: %d", len(allRecords))
	for i, r := range allRecords {
		t.Logf("记录 %d: ConnectionID=%s, IsActive=%v", i+1, r.ConnectionID, r.IsActive)
	}

	// 测试：获取所有活跃连接
	isActive := true
	activeConns, err := tc.repo.List(tc.ctx, &repository.ConnectionQueryOptions{
		IsActive: &isActive,
	})
	assert.NoError(t, err)
	t.Logf("活跃连接数: %d, 期望: 3", len(activeConns))
	assert.Equal(t, 3, len(activeConns))

	// 测试：获取node-1的活跃连接
	node1ActiveConns, err := tc.repo.List(tc.ctx, &repository.ConnectionQueryOptions{
		NodeID:   "node-1",
		IsActive: &isActive,
	})
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(node1ActiveConns), 1)

	// 测试：分页查询
	pagedConns, err := tc.repo.List(tc.ctx, &repository.ConnectionQueryOptions{
		Limit:  2,
		Offset: 0,
	})
	assert.NoError(t, err)
	assert.Equal(t, 2, len(pagedConns))
}

// TestCount 测试统计
func TestCount(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	// 创建连接记录
	for i := 1; i <= 5; i++ {
		record := &models.ConnectionRecord{
			ConnectionID: tc.generateConnectionID(),
			UserID:       tc.generateUserID(),
			ConnectedAt:  time.Now(),
			IsActive:     i <= 3,
		}
		err := tc.repo.Upsert(tc.ctx, record)
		assert.NoError(t, err)
		time.Sleep(10 * time.Millisecond) // 增加等待时间确保数据写入
	}

	// 等待确保所有记录都已提交到数据库
	time.Sleep(50 * time.Millisecond)

	// 统计活跃连接
	isActive := true
	count, err := tc.repo.Count(tc.ctx, &repository.ConnectionQueryOptions{
		IsActive: &isActive,
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)

	// 统计所有连接
	totalCount, err := tc.repo.Count(tc.ctx, nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), totalCount)
}

// TestGetConnectionStats 测试获取连接统计
func TestGetConnectionStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	now := time.Now()
	startTime := now.Add(-1 * time.Hour)
	endTime := now.Add(1 * time.Hour)

	// 创建测试数据
	for i := 1; i <= 3; i++ {
		record := &models.ConnectionRecord{
			ConnectionID:     tc.generateConnectionID(),
			UserID:           tc.generateUserID(),
			ConnectedAt:      now,
			MessagesSent:     int64(i * 10),
			MessagesReceived: int64(i * 5),
			IsActive:         i <= 2,
		}
		err := tc.repo.Upsert(tc.ctx, record)
		assert.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	// 获取统计
	stats, err := tc.repo.GetConnectionStats(tc.ctx, startTime, endTime)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), stats.TotalConnections)
	assert.Equal(t, int64(2), stats.ActiveConnections)
	assert.Equal(t, int64(60), stats.TotalMessagesSent)
	assert.Equal(t, int64(30), stats.TotalMessagesReceived)
}

// TestGetUserConnectionStats 测试获取用户连接统计
func TestGetUserConnectionStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	userID := tc.generateUserID()

	// 创建用户的多个连接
	for i := 1; i <= 3; i++ {
		record := &models.ConnectionRecord{
			ConnectionID:     tc.generateConnectionID(),
			UserID:           userID,
			ConnectedAt:      time.Now(),
			MessagesSent:     int64(i * 10),
			MessagesReceived: int64(i * 5),
			IsActive:         i <= 2,
		}
		err := tc.repo.Upsert(tc.ctx, record)
		assert.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	// 获取用户统计
	stats, err := tc.repo.GetUserConnectionStats(tc.ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, userID, stats.UserID)
	assert.True(t, stats.IsActive) // 有活跃连接
	assert.Equal(t, int64(60), stats.MessagesSent)
	assert.Equal(t, int64(30), stats.MessagesReceived)
}

// TestGetNodeConnectionStats 测试获取节点连接统计
func TestGetNodeConnectionStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	nodeID := "node-test"

	// 创建节点的多个连接
	for i := 1; i <= 3; i++ {
		record := &models.ConnectionRecord{
			ConnectionID:     tc.generateConnectionID(),
			UserID:           tc.generateUserID(),
			NodeID:           nodeID,
			NodeIP:           "192.168.1.100",
			NodePort:         8080,
			ConnectedAt:      time.Now(),
			MessagesSent:     int64(i * 10),
			MessagesReceived: int64(i * 5),
			IsActive:         i <= 2,
		}
		err := tc.repo.Upsert(tc.ctx, record)
		assert.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	// 获取节点统计
	stats, err := tc.repo.GetNodeConnectionStats(tc.ctx, nodeID)
	assert.NoError(t, err)
	assert.Equal(t, nodeID, stats.NodeID)
	assert.Equal(t, "192.168.1.100", stats.NodeIP)
	assert.Equal(t, 8080, stats.NodePort)
	assert.Equal(t, int64(3), stats.TotalConnections)
	assert.Equal(t, int64(2), stats.ActiveConnections)
	assert.Equal(t, int64(60), stats.TotalMessagesSent)
	assert.Equal(t, int64(30), stats.TotalMessagesReceived)
}

// TestCleanupInactiveRecords 测试清理非活跃记录
func TestCleanupInactiveRecords(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	// 创建旧的断开连接
	oldTime := time.Now().Add(-48 * time.Hour)
	oldConnID := tc.generateConnectionID()
	oldRecord := &models.ConnectionRecord{
		UserID:       tc.generateUserID(),
		ConnectionID: oldConnID,
		ConnectedAt:  oldTime,
		IsActive:     true,
	}
	err := tc.repo.Upsert(tc.ctx, oldRecord)
	assert.NoError(t, err)

	// 标记为断开
	err = tc.repo.MarkDisconnected(tc.ctx, oldConnID, models.DisconnectReasonTimeout, 1000, "timeout")
	assert.NoError(t, err)

	// 手动更新断开时间为旧时间
	err = tc.db.Table(tc.tableName).
		Where("connection_id = ?", oldConnID).
		Update("disconnected_at", oldTime).Error
	assert.NoError(t, err)

	// 创建新的活跃连接
	newConnID := tc.generateConnectionID()
	newRecord := &models.ConnectionRecord{
		ConnectionID: newConnID,
		UserID:       tc.generateUserID(),
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err = tc.repo.Upsert(tc.ctx, newRecord)
	assert.NoError(t, err)

	// 清理24小时前的非活跃记录
	before := time.Now().Add(-24 * time.Hour)
	deleted, err := tc.repo.CleanupInactiveRecords(tc.ctx, before)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// 验证旧记录已删除
	_, err = tc.repo.GetByConnectionID(tc.ctx, oldConnID)
	assert.Error(t, err)

	// 验证新记录仍存在
	_, err = tc.repo.GetByConnectionID(tc.ctx, newConnID)
	assert.NoError(t, err)
}

// TestBatchUpsert 测试批量创建或更新
func TestBatchUpsert(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	// 批量创建
	records := []*models.ConnectionRecord{
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ConnectedAt: time.Now(), IsActive: true},
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ConnectedAt: time.Now(), IsActive: true},
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ConnectedAt: time.Now(), IsActive: true},
	}

	err := tc.repo.BatchUpsert(tc.ctx, records)
	assert.NoError(t, err)

	// 等待确保批量插入完成
	time.Sleep(100 * time.Millisecond)

	// 验证记录已创建
	for _, record := range records {
		saved, err := tc.repo.GetByConnectionID(tc.ctx, record.ConnectionID)
		assert.NoError(t, err)
		if assert.NotNil(t, saved, "Record should not be nil for ConnectionID: %s", record.ConnectionID) {
			assert.Equal(t, record.ConnectionID, saved.ConnectionID)
		}
	}
}

// TestGetFrequentReconnectConnections 测试获取频繁重连的连接
func TestGetFrequentReconnectConnections(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	// 创建测试数据
	records := []*models.ConnectionRecord{
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ConnectedAt: time.Now(), ReconnectCount: 10, IsActive: true},
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ConnectedAt: time.Now(), ReconnectCount: 5, IsActive: true},
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ConnectedAt: time.Now(), ReconnectCount: 2, IsActive: true},
	}

	for _, record := range records {
		err := tc.repo.Upsert(tc.ctx, record)
		assert.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	// 获取重连次数>=5的连接
	frequentConns, err := tc.repo.GetFrequentReconnectConnections(tc.ctx, 5, 10)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(frequentConns))
}

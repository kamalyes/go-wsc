/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 00:00:00
 * @FilePath: \go-wsc\connection_repository_test.go
 * @Description: WebSocket连接记录仓储测试（拆表版）
 *
 * 拆表后 connect 表承载身份+会话生命周期+心跳时间戳(last_ping_at/last_pong_at)，
 * 质量指标(Ping统计/消息/错误/评分)由 ConnectionQualityRepository 落到 wsc_connection_qualities 表。
 * 本测试同时持有 connect repo 和 quality repo，两表用同 span 后缀隔离，覆盖拆表后的正确语义
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
// 拆表后同时持有 connect repo 和 quality repo，两表用同后缀隔离测试
type testConnectionRepoContext struct {
	t                *testing.T
	db               *gorm.DB
	repo             repository.ConnectionRecordRepository
	qualityRepo      repository.ConnectionQualityRepository
	ctx              context.Context
	idGenerator      *idgen.ShortFlakeGenerator
	tableName        string
	qualityTableName string
}

// newTestConnectionRepoContext 创建测试上下文
// 同时创建 connect 表和 quality 表（同 span 后缀），两 repo 各自 WithTableName 隔离
func newTestConnectionRepoContext(t *testing.T) *testConnectionRepoContext {
	workerID := osx.GetWorkerIdForSnowflake()

	// 使用测试函数名生成唯一的表名（移除特殊字符）
	testName := strings.ReplaceAll(t.Name(), "/", "_")
	testName = strings.ReplaceAll(testName, " ", "_")
	idGenerator := getTestIDGenerator()
	table := idGenerator.GenerateSpanID()
	tableName := fmt.Sprintf("wsc_connection_records_%s", table)
	qualityTableName := fmt.Sprintf("wsc_connection_qualities_%s", table)

	// 获取基础数据库连接并创建 connect 表
	db := getConnectTestDB(t, tableName)

	// 创建 quality 表（与 connect 表同后缀，1:1 关联）
	err := db.Table(qualityTableName).Migrator().CreateTable(&models.ConnectionQuality{})
	require.NoError(t, err, "创建质量表失败: %s", qualityTableName)
	time.Sleep(50 * time.Millisecond)
	require.True(t, db.Migrator().HasTable(qualityTableName), "质量表创建后验证失败: %s", qualityTableName)

	// 创建 repository 并使用 WithTableName 设置自定义表名
	baseRepo := repository.NewConnectionRecordRepository(db, nil, NewDefaultWSCLogger())
	scopedRepo := baseRepo.WithTableName(tableName)

	baseQualityRepo := repository.NewConnectionQualityRepository(db, nil, NewDefaultWSCLogger())
	scopedQualityRepo := baseQualityRepo.WithTableName(qualityTableName)

	tc := &testConnectionRepoContext{
		t:                t,
		db:               db,
		repo:             scopedRepo,
		qualityRepo:      scopedQualityRepo,
		ctx:              context.Background(),
		idGenerator:      idgen.NewShortFlakeGenerator(workerID),
		tableName:        tableName,
		qualityTableName: qualityTableName,
	}

	// 测试结束后清理两张表（先 quality 后 connect，避免外键依赖）
	t.Cleanup(func() {
		db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", qualityTableName))
		db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS \"%s\"", tableName))
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
// 拆表后 connect repo 只写 connect 表，重连计数由 quality repo 维护
func TestUpsert(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	userID := tc.generateUserID()
	connID1 := tc.generateConnectionID()

	// 首次连接 - 创建 connect 记录
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

	// 验证 connect 记录已创建
	saved, err := tc.repo.GetByConnectionID(tc.ctx, connID1)
	assert.NoError(t, err)
	assert.Equal(t, connID1, saved.ConnectionID)

	// 首次连接 - 创建 quality 记录（初始 ReconnectCount=0）
	quality := &models.ConnectionQuality{
		ConnectionID: connID1,
		UserID:       userID,
	}
	err = tc.qualityRepo.Upsert(tc.ctx, quality)
	assert.NoError(t, err)

	// 验证质量记录初始重连次数为 0
	savedQuality, err := tc.qualityRepo.GetByConnectionID(tc.ctx, connID1)
	assert.NoError(t, err)
	assert.Equal(t, 0, savedQuality.ReconnectCount)

	// 同一连接重连 - 再次 Upsert quality，reconnect_count+1
	time.Sleep(100 * time.Millisecond)
	err = tc.qualityRepo.Upsert(tc.ctx, &models.ConnectionQuality{
		ConnectionID: connID1,
		UserID:       userID,
	})
	assert.NoError(t, err)

	// 验证重连次数递增为 1
	updatedQuality, err := tc.qualityRepo.GetByConnectionID(tc.ctx, connID1)
	assert.NoError(t, err)
	assert.Equal(t, 1, updatedQuality.ReconnectCount)

	// connect 记录仍为活跃
	updated, err := tc.repo.GetByConnectionID(tc.ctx, connID1)
	assert.NoError(t, err)
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
	err = tc.repo.MarkDisconnected(tc.ctx, connID, models.DisconnectReasonTimeout, 1001)
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

// TestBatchUpdateHeartbeats 测试批量更新心跳（拆表分工版）
// 心跳时间戳(last_ping_at/last_pong_at)由 connect repo 写 wsc_connection_records，
// Ping 统计(average/max/min_ping_ms)与活跃时间由 quality repo 写 wsc_connection_qualities
func TestBatchUpdateHeartbeats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	userID := tc.generateUserID()
	connID := tc.generateConnectionID()

	// 创建 connect 记录 + quality 记录（心跳更新要求两表行已存在）
	err := tc.repo.Upsert(tc.ctx, &models.ConnectionRecord{
		ConnectionID: connID,
		UserID:       userID,
		ConnectedAt:  time.Now(),
		IsActive:     true,
	})
	assert.NoError(t, err)

	err = tc.qualityRepo.Upsert(tc.ctx, &models.ConnectionQuality{
		ConnectionID: connID,
		UserID:       userID,
	})
	assert.NoError(t, err)

	// 批量更新心跳：混入不存在的 connectionID，验证单条失败不影响整批
	pingTime := time.Now().Add(-200 * time.Millisecond)
	pongTime := time.Now()
	entries := []*repository.HeartbeatUpdateEntry{
		{ConnectionID: connID, PingTime: &pingTime, PongTime: &pongTime, PingMs: 100},
		{ConnectionID: "not-exist-conn", PingTime: &pingTime, PongTime: &pongTime, PingMs: 50},
	}

	// 1. connect repo 写心跳时间戳（wsc_connection_records）
	err = tc.repo.BatchUpdateHeartbeats(tc.ctx, entries)
	assert.NoError(t, err, "含不存在连接的批次不应整体失败")

	saved, err := tc.repo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	require.NotNil(t, saved.LastPingAt, "last_ping_at 应写入 connect 表")
	require.NotNil(t, saved.LastPongAt, "last_pong_at 应写入 connect 表")
	assert.WithinDuration(t, pingTime, *saved.LastPingAt, time.Second, "last_ping_at 应等于提交的心跳时间")
	assert.WithinDuration(t, pongTime, *saved.LastPongAt, time.Second, "last_pong_at 应等于提交的Pong时间")

	// 2. quality repo 写 Ping 统计与活跃时间（wsc_connection_qualities）
	err = tc.qualityRepo.BatchUpdateHeartbeats(tc.ctx, entries)
	assert.NoError(t, err)

	savedQuality, err := tc.qualityRepo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	assert.Equal(t, 100.0, savedQuality.AveragePingMs, "首次 Ping 统计 average 应为 100")
	assert.Equal(t, 100.0, savedQuality.MaxPingMs)
	assert.Equal(t, 100.0, savedQuality.MinPingMs)
	require.NotNil(t, savedQuality.LastActiveAt, "last_active_at 应随心跳刷新")
	assert.WithinDuration(t, pingTime, *savedQuality.LastActiveAt, time.Second)

	// 3. 重连（Upsert 已存在记录）应重置 connect 表心跳时间戳
	time.Sleep(10 * time.Millisecond)
	err = tc.repo.Upsert(tc.ctx, &models.ConnectionRecord{
		ConnectionID: connID,
		UserID:       userID,
		ConnectedAt:  time.Now(),
		IsActive:     true,
	})
	assert.NoError(t, err)

	reconnected, err := tc.repo.GetByConnectionID(tc.ctx, connID)
	assert.NoError(t, err)
	assert.Nil(t, reconnected.LastPingAt, "重连后 last_ping_at 应重置为 nil")
	assert.Nil(t, reconnected.LastPongAt, "重连后 last_pong_at 应重置为 nil")

	// 4. 空 batch 直接返回，不产生 DB 调用
	err = tc.repo.BatchUpdateHeartbeats(tc.ctx, nil)
	assert.NoError(t, err)
	err = tc.qualityRepo.BatchUpdateHeartbeats(tc.ctx, nil)
	assert.NoError(t, err)
}

// TestQualityAddError 测试记录错误（拆表后由 ConnectionQualityRepository 承载）
func TestQualityAddError(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	connID := tc.generateConnectionID()
	userID := tc.generateUserID()

	// 创建 connect 记录
	record := &models.ConnectionRecord{
		ConnectionID: connID,
		UserID:       userID,
		ConnectedAt:  time.Now(),
		IsActive:     true,
	}
	err := tc.repo.Upsert(tc.ctx, record)
	assert.NoError(t, err)

	// 创建 quality 记录（AddError 要求质量行已存在）
	err = tc.qualityRepo.Upsert(tc.ctx, &models.ConnectionQuality{
		ConnectionID: connID,
		UserID:       userID,
	})
	assert.NoError(t, err)

	// 记录错误（qualityRepo.AddError）
	testErr := errors.New("test error")
	err = tc.qualityRepo.AddError(tc.ctx, connID, testErr)
	assert.NoError(t, err)

	// 验证错误记录（从 quality 表读）
	updated, err := tc.qualityRepo.GetByConnectionID(tc.ctx, connID)
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
// 拆表后 connect 表无 MessagesSent/MessagesReceived 字段，stats 中质量维度零填充
// 消息统计需跨表从 qualityRepo 取，本测试只验证 connect 身份维度统计
func TestGetConnectionStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	now := time.Now()
	startTime := now.Add(-1 * time.Hour)
	endTime := now.Add(1 * time.Hour)

	// 创建测试数据（connect 表不再承载消息计数字段）
	for i := 1; i <= 3; i++ {
		record := &models.ConnectionRecord{
			ConnectionID: tc.generateConnectionID(),
			UserID:       tc.generateUserID(),
			ConnectedAt:  now,
			IsActive:     i <= 2,
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
	// 拆表后质量维度由本方法零填充，跨表补充由调用方按需从 qualityRepo 取
	assert.Equal(t, int64(0), stats.TotalMessagesSent)
	assert.Equal(t, int64(0), stats.TotalMessagesReceived)
}

// TestGetUserConnectionStats 测试获取用户连接统计
// 拆表后 connect 表无 MessagesSent/MessagesReceived 字段，stats 中质量维度零填充
func TestGetUserConnectionStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	userID := tc.generateUserID()

	// 创建用户的多个连接（connect 表不再承载消息计数字段）
	for i := 1; i <= 3; i++ {
		record := &models.ConnectionRecord{
			ConnectionID: tc.generateConnectionID(),
			UserID:       userID,
			ConnectedAt:  time.Now(),
			IsActive:     i <= 2,
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
	// 拆表后质量维度零填充，跨表补充由调用方按需从 qualityRepo 取
	assert.Equal(t, int64(0), stats.MessagesSent)
	assert.Equal(t, int64(0), stats.MessagesReceived)
}

// TestGetNodeConnectionStats 测试获取节点连接统计
// 拆表后 connect 表无 MessagesSent/MessagesReceived 字段，stats 中质量维度零填充
func TestGetNodeConnectionStats(t *testing.T) {
	tc := newTestConnectionRepoContext(t)
	nodeID := "node-test"

	// 创建节点的多个连接（connect 表不再承载消息计数字段）
	for i := 1; i <= 3; i++ {
		record := &models.ConnectionRecord{
			ConnectionID: tc.generateConnectionID(),
			UserID:       tc.generateUserID(),
			NodeID:       nodeID,
			NodeIP:       "192.168.1.100",
			NodePort:     8080,
			ConnectedAt:  time.Now(),
			IsActive:     i <= 2,
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
	// 拆表后质量维度零填充，跨表补充由调用方按需从 qualityRepo 取
	assert.Equal(t, int64(0), stats.TotalMessagesSent)
	assert.Equal(t, int64(0), stats.TotalMessagesReceived)
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
	err = tc.repo.MarkDisconnected(tc.ctx, oldConnID, models.DisconnectReasonTimeout, 1000)
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
// 拆表后重连次数由 ConnectionQualityRepository 承载，本测试从 qualityRepo 写入并查询
func TestGetFrequentReconnectConnections(t *testing.T) {
	tc := newTestConnectionRepoContext(t)

	// 创建 3 个质量记录，ReconnectCount 分别为 10/5/2
	// 首次 Upsert 时 ReconnectCount 用传入值（OnConflict 才走 +1）
	qualityRecords := []*models.ConnectionQuality{
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ReconnectCount: 10},
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ReconnectCount: 5},
		{ConnectionID: tc.generateConnectionID(), UserID: tc.generateUserID(), ReconnectCount: 2},
	}

	for _, q := range qualityRecords {
		// 先建 connect 记录保持语义完整（1:1 关联）
		_ = tc.repo.Upsert(tc.ctx, &models.ConnectionRecord{
			ConnectionID: q.ConnectionID,
			UserID:       q.UserID,
			ConnectedAt:  time.Now(),
			IsActive:     true,
		})
		// 直接写质量行（绕过 OnConflict +1，用 Create 写入指定 ReconnectCount）
		err := tc.qualityRepo.Upsert(tc.ctx, q)
		assert.NoError(t, err)
		time.Sleep(2 * time.Millisecond)
	}

	// 获取重连次数>=5的连接（从 qualityRepo 查）
	frequentConns, err := tc.qualityRepo.GetFrequentReconnectConnections(tc.ctx, 5, 10)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(frequentConns))
}

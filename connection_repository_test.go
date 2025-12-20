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
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	testConnectDBInstance *gorm.DB
	testConnectDBOnce     sync.Once
)

// cleanTestData 清理测试数据
func cleanTestData(t *testing.T, db *gorm.DB) {
	// 使用 TRUNCATE 更快更彻底地清理数据
	err := db.Exec("TRUNCATE TABLE wsc_connection_records").Error
	if err != nil {
		// 如果 TRUNCATE 失败（比如没有权限），回退到 DELETE
		err = db.Exec("DELETE FROM wsc_connection_records").Error
		if err != nil {
			t.Logf("清理测试数据时出现警告: %v", err)
		}
	}
}

// 测试用 MySQL 配置（使用 local 配置文件中的数据库）- 单例模式
func getConnectTestDB(t *testing.T) *gorm.DB {
	testConnectDBOnce.Do(func() {
		dsn := "root:idev88888@tcp(120.77.38.35:13306)/im_agent?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Silent), // 测试时使用静默模式
			SkipDefaultTransaction: true,                                  // 跳过默认事务，提升性能
			PrepareStmt:            true,                                  // 预编译语句，提升性能
		})
		require.NoError(t, err, "数据库连接失败")

		// 只执行一次自动迁移
		err = db.AutoMigrate(&ConnectionRecord{})
		require.NoError(t, err, "数据库迁移失败")

		// 配置连接池
		sqlDB, err := db.DB()
		require.NoError(t, err, "获取底层DB失败")
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetConnMaxLifetime(time.Hour)

		testConnectDBInstance = db
	})

	// 每次获取测试DB时清理数据（确保在 Once.Do 之后执行）
	if testConnectDBInstance != nil {
		cleanTestData(t, testConnectDBInstance)
	}
	return testConnectDBInstance
}

// createTestRecord 创建测试记录
func createTestRecord(userID, nodeID string, isActive bool) *ConnectionRecord {
	now := time.Now()
	return &ConnectionRecord{
		ConnectionID:     osx.HashUnixMicroCipherText(),
		UserID:           userID,
		NodeID:           nodeID,
		NodeIP:           "127.0.0.1",
		NodePort:         8080,
		ClientIP:         "192.168.1.100",
		ClientType:       "web",
		Protocol:         "websocket",
		ConnectedAt:      now,
		IsActive:         isActive,
		MessagesSent:     10,
		MessagesReceived: 8,
		BytesSent:        1024,
		BytesReceived:    768,
		AveragePingMs:    25.5,
		MaxPingMs:        50.0,
		MinPingMs:        10.0,
	}
}

// TestCreate 测试创建连接记录
func TestCreate(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)

	err := repo.Create(ctx, record)
	assert.NoError(t, err)
	assert.NotZero(t, record.ID)

	// 验证记录已创建
	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, record.UserID, found.UserID)
	assert.Equal(t, record.NodeID, found.NodeID)
}

// TestUpdate 测试更新连接记录
func TestUpdate(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	// 更新记录
	record.MessagesSent = 20
	record.BytesSent = 2048
	err = repo.Update(ctx, record)
	assert.NoError(t, err)

	// 验证更新
	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, int64(20), found.MessagesSent)
	assert.Equal(t, int64(2048), found.BytesSent)
}

// TestUpdateByConnectionID 测试通过连接ID更新
func TestUpdateByConnectionID(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	updates := map[string]interface{}{
		"messages_sent": 100,
		"is_active":     false,
	}

	err = repo.UpdateByConnectionID(ctx, record.ConnectionID, updates)
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, int64(100), found.MessagesSent)
	assert.False(t, found.IsActive)
}

// TestGetByID 测试通过主键ID查询
func TestGetByID(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	found, err := repo.GetByID(ctx, record.ID)
	assert.NoError(t, err)
	assert.Equal(t, record.ConnectionID, found.ConnectionID)
}

// TestDelete 测试删除记录
func TestDelete(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	err = repo.Delete(ctx, record.ID)
	assert.NoError(t, err)

	// 验证已删除
	_, err = repo.GetByID(ctx, record.ID)
	assert.Error(t, err)
}

// TestMarkDisconnected 测试标记断开连接
func TestMarkDisconnected(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	time.Sleep(1100 * time.Millisecond) // 确保有持续时间（至少1秒）
	err = repo.MarkDisconnected(ctx, record.ConnectionID, DisconnectReasonTimeout, 1001, "timeout")
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.False(t, found.IsActive)
	assert.NotNil(t, found.DisconnectedAt)
	assert.Equal(t, string(DisconnectReasonTimeout), found.DisconnectReason)
	assert.Equal(t, 1001, found.DisconnectCode)
	assert.True(t, found.IsAbnormal) // timeout是异常断开
	assert.Greater(t, found.Duration, int64(0))
}

// TestMarkForcedOffline 测试标记强制下线
func TestMarkForcedOffline(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	err = repo.MarkForcedOffline(ctx, record.ConnectionID, "admin kicked")
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.False(t, found.IsActive)
	assert.Equal(t, string(DisconnectReasonForceOffline), found.DisconnectReason)
	assert.Equal(t, "admin kicked", found.DisconnectMessage)
}

// TestIncrementMessageStats 测试增加消息统计
func TestIncrementMessageStats(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	record.MessagesSent = 10
	record.MessagesReceived = 8
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	err = repo.IncrementMessageStats(ctx, record.ConnectionID, 5, 3)
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, int64(15), found.MessagesSent)
	assert.Equal(t, int64(11), found.MessagesReceived)
}

// TestIncrementBytesStats 测试增加字节统计
func TestIncrementBytesStats(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	record.BytesSent = 1024
	record.BytesReceived = 512
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	err = repo.IncrementBytesStats(ctx, record.ConnectionID, 2048, 1024)
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3072), found.BytesSent)
	assert.Equal(t, int64(1536), found.BytesReceived)
}

// TestUpdatePingStats 测试更新Ping统计
func TestUpdatePingStats(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	record.AveragePingMs = 20.0
	record.MaxPingMs = 30.0
	record.MinPingMs = 10.0
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	// 更新ping统计
	err = repo.UpdatePingStats(ctx, record.ConnectionID, 50.0)
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, 35.0, found.AveragePingMs) // (20 + 50) / 2
	assert.Equal(t, 50.0, found.MaxPingMs)     // 更新为50
	assert.Equal(t, 10.0, found.MinPingMs)     // 保持10
}

// TestIncrementReconnect 测试增加重连次数
func TestIncrementReconnect(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	record.ReconnectCount = 2
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	err = repo.IncrementReconnect(ctx, record.ConnectionID)
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, 3, found.ReconnectCount)
}

// TestAddError 测试添加错误记录
func TestAddError(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	testErr := fmt.Errorf("connection timeout")
	err = repo.AddError(ctx, record.ConnectionID, testErr)
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.Equal(t, 1, found.ErrorCount)
	assert.Equal(t, "connection timeout", found.LastError)
	assert.NotNil(t, found.LastErrorAt)
}

// TestUpdateHeartbeat 测试更新心跳时间
func TestUpdateHeartbeat(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	record := createTestRecord("user1", "node1", true)
	err := repo.Create(ctx, record)
	require.NoError(t, err)

	pingTime := time.Now()
	pongTime := time.Now().Add(10 * time.Millisecond)

	err = repo.UpdateHeartbeat(ctx, record.ConnectionID, &pingTime, &pongTime)
	assert.NoError(t, err)

	found, err := repo.GetByConnectionID(ctx, record.ConnectionID)
	assert.NoError(t, err)
	assert.NotNil(t, found.LastPingAt)
	assert.NotNil(t, found.LastPongAt)
}

// TestGetActiveByUserID 测试获取用户的活跃连接
func TestGetActiveByUserID(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建3个连接，2个活跃，1个非活跃
	repo.Create(ctx, createTestRecord("user1", "node1", true))
	repo.Create(ctx, createTestRecord("user1", "node2", true))
	inactiveRecord := createTestRecord("user1", "node3", false)
	repo.Create(ctx, inactiveRecord)
	// 手动更新 IsActive 为 false
	db.WithContext(ctx).Model(inactiveRecord).Update("is_active", false)
	repo.Create(ctx, createTestRecord("user2", "node1", true))

	records, err := repo.GetActiveByUserID(ctx, "user1")
	assert.NoError(t, err)
	assert.Len(t, records, 2)
	for _, r := range records {
		assert.True(t, r.IsActive)
		assert.Equal(t, "user1", r.UserID)
	}
}

// TestGetByUserID 测试获取用户的所有连接记录
func TestGetByUserID(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建5个连接
	for i := 0; i < 5; i++ {
		repo.Create(ctx, createTestRecord("user1", fmt.Sprintf("node%d", i), i%2 == 0))
	}

	records, err := repo.GetByUserID(ctx, "user1", 3, 0)
	assert.NoError(t, err)
	assert.Len(t, records, 3) // limit为3

	records, err = repo.GetByUserID(ctx, "user1", 3, 3)
	assert.NoError(t, err)
	assert.Len(t, records, 2) // offset为3，剩余2条
}

// TestGetActiveByNodeID 测试获取节点的活跃连接
func TestGetActiveByNodeID(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建3个连接，2个活跃，1个非活跃
	repo.Create(ctx, createTestRecord("user1", "node1", true))
	repo.Create(ctx, createTestRecord("user2", "node1", true))
	inactiveRecord := createTestRecord("user3", "node1", false)
	repo.Create(ctx, inactiveRecord)
	// 手动更新 IsActive 为 false
	db.WithContext(ctx).Model(inactiveRecord).Update("is_active", false)
	repo.Create(ctx, createTestRecord("user4", "node2", true))

	records, err := repo.GetActiveByNodeID(ctx, "node1")
	assert.NoError(t, err)
	assert.Len(t, records, 2)
	for _, r := range records {
		assert.True(t, r.IsActive)
		assert.Equal(t, "node1", r.NodeID)
	}
}

// TestGetByNodeID 测试获取节点的所有连接记录
func TestGetByNodeID(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建5个连接
	for i := 0; i < 5; i++ {
		repo.Create(ctx, createTestRecord(fmt.Sprintf("user%d", i), "node1", i%2 == 0))
	}

	records, err := repo.GetByNodeID(ctx, "node1", 3, 0)
	assert.NoError(t, err)
	assert.Len(t, records, 3) // limit为3

	records, err = repo.GetByNodeID(ctx, "node1", 3, 3)
	assert.NoError(t, err)
	assert.Len(t, records, 2) // offset为3，剩余2条
}

// TestCountActiveConnections 测试统计活跃连接数
func TestCountActiveConnections(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	repo.Create(ctx, createTestRecord("user1", "node1", true))
	repo.Create(ctx, createTestRecord("user2", "node1", true))
	inactiveRecord := createTestRecord("user3", "node1", false)
	repo.Create(ctx, inactiveRecord)
	// 手动更新 IsActive 为 false
	db.WithContext(ctx).Model(inactiveRecord).Update("is_active", false)

	count, err := repo.CountActiveConnections(ctx)
	assert.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

// TestGetConnectionStats 测试获取连接统计信息
func TestGetConnectionStats(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	now := time.Now()
	startTime := now.Add(-1 * time.Hour)
	endTime := now.Add(1 * time.Hour)

	// 创建测试数据
	for i := 0; i < 5; i++ {
		record := createTestRecord(fmt.Sprintf("user%d", i), "node1", i < 3)
		record.ConnectedAt = now
		if i >= 3 {
			// 已断开的连接
			disconnectTime := now.Add(5 * time.Minute)
			record.DisconnectedAt = &disconnectTime
			record.Duration = 300
			record.IsAbnormal = i == 4
		}
		repo.Create(ctx, record)
		// 对于i>=3的记录，手动更新 IsActive 为 false
		if i >= 3 {
			db.WithContext(ctx).Model(record).Update("is_active", false)
		}
	}

	stats, err := repo.GetConnectionStats(ctx, startTime, endTime)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), stats.TotalConnections)
	assert.Equal(t, int64(3), stats.ActiveConnections)
	assert.Greater(t, stats.AveragePingMs, 0.0)
}

// TestBatchCreate 测试批量创建
func TestBatchCreate(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建100条记录（测试分批处理）
	records := make([]*ConnectionRecord, 100)
	for i := 0; i < 100; i++ {
		records[i] = createTestRecord(fmt.Sprintf("user%d", i), "node1", true)
	}

	err := repo.BatchCreate(ctx, records)
	assert.NoError(t, err)

	// 验证数量
	var count int64
	db.Model(&ConnectionRecord{}).Count(&count)
	assert.Equal(t, int64(100), count)
}

// TestBatchUpdateActive 测试批量更新活跃状态
func TestBatchUpdateActive(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建3个连接
	connIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		record := createTestRecord(fmt.Sprintf("user%d", i), "node1", true)
		repo.Create(ctx, record)
		connIDs[i] = record.ConnectionID
	}

	// 批量更新为非活跃
	err := repo.BatchUpdateActive(ctx, connIDs, false)
	assert.NoError(t, err)

	// 验证
	for _, connID := range connIDs {
		record, _ := repo.GetByConnectionID(ctx, connID)
		assert.False(t, record.IsActive)
	}
}

// TestBatchDelete 测试批量删除
func TestBatchDelete(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建3个连接
	ids := make([]uint64, 3)
	for i := 0; i < 3; i++ {
		record := createTestRecord(fmt.Sprintf("user%d", i), "node1", true)
		repo.Create(ctx, record)
		ids[i] = record.ID
	}

	err := repo.BatchDelete(ctx, ids)
	assert.NoError(t, err)

	// 验证已删除
	for _, id := range ids {
		_, err := repo.GetByID(ctx, id)
		assert.Error(t, err)
	}
}

// TestCleanupOldRecords 测试清理旧记录
func TestCleanupOldRecords(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	now := time.Now()
	oldTime := now.Add(-30 * 24 * time.Hour) // 30天前

	// 创建旧记录（非活跃）
	oldRecord := createTestRecord("user1", "node1", false)
	oldRecord.ConnectedAt = oldTime
	oldRecord.IsActive = false // 显式设置
	disconnectedTime := oldTime.Add(1 * time.Hour)
	oldRecord.DisconnectedAt = &disconnectedTime
	// GORM会跳过bool的false值，使用Session配置强制创建
	err := db.WithContext(ctx).Session(&gorm.Session{FullSaveAssociations: false}).Create(oldRecord).Error
	require.NoError(t, err)

	// 手动更新 IsActive 为 false（因为GORM Create会使用数据库默认值true）
	db.WithContext(ctx).Model(oldRecord).Update("is_active", false)
	require.NoError(t, err)

	// 验证旧记录已正确保存
	saved, err := repo.GetByConnectionID(ctx, oldRecord.ConnectionID)
	require.NoError(t, err)
	assert.True(t, saved.ConnectedAt.Before(now.Add(-29*24*time.Hour)), "ConnectedAt should be old")
	assert.False(t, saved.IsActive, "IsActive should be false")

	// 创建新记录
	newRecord := createTestRecord("user2", "node2", false)
	newRecord.ConnectedAt = now
	repo.Create(ctx, newRecord)

	// 清理7天前的记录
	deletedCount, err := repo.CleanupOldRecords(ctx, now.Add(-7*24*time.Hour))
	assert.NoError(t, err)
	assert.Equal(t, int64(1), deletedCount)

	// 验证旧记录已删除
	_, err = repo.GetByConnectionID(ctx, oldRecord.ConnectionID)
	assert.Error(t, err)

	// 验证新记录仍存在
	_, err = repo.GetByConnectionID(ctx, newRecord.ConnectionID)
	assert.NoError(t, err)
}

// TestArchiveOldRecords 测试归档旧记录
func TestArchiveOldRecords(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	now := time.Now()
	oldTime := now.Add(-30 * 24 * time.Hour)

	// 创建50条旧记录（测试分批处理）
	for i := 0; i < 50; i++ {
		record := createTestRecord(fmt.Sprintf("user%d", i), "node1", false)
		record.ConnectedAt = oldTime
		record.IsActive = false
		disconnectedTime := oldTime.Add(1 * time.Hour)
		record.DisconnectedAt = &disconnectedTime
		db.WithContext(ctx).Create(record)
		// 手动更新 IsActive 为 false
		db.WithContext(ctx).Model(record).Update("is_active", false)
	}

	// 创建新记录
	newRecord := createTestRecord("user-new", "node1", true)
	newRecord.ConnectedAt = now
	repo.Create(ctx, newRecord)

	// 归档30天前的记录
	var archivedRecords []*ConnectionRecord
	processedCount, err := repo.ArchiveOldRecords(ctx, now.Add(-7*24*time.Hour), func(records []*ConnectionRecord) error {
		archivedRecords = append(archivedRecords, records...)
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, int64(50), processedCount)
	assert.Len(t, archivedRecords, 50)

	// 验证所有归档的记录都是旧记录
	for _, record := range archivedRecords {
		assert.True(t, record.ConnectedAt.Before(now.Add(-7*24*time.Hour)))
	}
}

// TestArchiveOldRecordsNilProcessor 测试归档时传入nil处理函数
func TestArchiveOldRecordsNilProcessor(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	_, err := repo.ArchiveOldRecords(ctx, time.Now(), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "processor function cannot be nil")
}

// TestArchiveOldRecordsProcessorError 测试归档时处理函数返回错误
func TestArchiveOldRecordsProcessorError(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建测试数据
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	for i := 0; i < 10; i++ {
		record := createTestRecord(fmt.Sprintf("user%d", i), "node1", false)
		record.ConnectedAt = oldTime
		record.IsActive = false
		disconnectedTime := oldTime.Add(1 * time.Hour)
		record.DisconnectedAt = &disconnectedTime
		db.WithContext(ctx).Create(record)
		// 手动更新 IsActive 为 false
		db.WithContext(ctx).Model(record).Update("is_active", false)
	}

	// 处理函数返回错误
	_, err := repo.ArchiveOldRecords(ctx, time.Now(), func(records []*ConnectionRecord) error {
		return fmt.Errorf("archive failed")
	})

	assert.Error(t, err)
	if err != nil {
		assert.Contains(t, err.Error(), "processor failed")
	}
}

// TestArchiveOldRecordsWithFileExport 测试归档到文件
func TestArchiveOldRecordsWithFileExport(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建测试数据
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		record := createTestRecord(fmt.Sprintf("user%d", i), "node1", false)
		record.ConnectedAt = oldTime
		record.IsActive = false
		disconnectedTime := oldTime.Add(1 * time.Hour)
		record.DisconnectedAt = &disconnectedTime
		db.WithContext(ctx).Create(record)
		// 手动更新 IsActive 为 false
		db.WithContext(ctx).Model(record).Update("is_active", false)
	}

	// 归档到JSON文件
	filename := "archive_test.json"
	defer os.Remove(filename)

	var allRecords []*ConnectionRecord
	processedCount, err := repo.ArchiveOldRecords(ctx, time.Now(), func(records []*ConnectionRecord) error {
		allRecords = append(allRecords, records...)
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, int64(5), processedCount)

	// 写入文件
	data, err := json.MarshalIndent(allRecords, "", "  ")
	assert.NoError(t, err)
	err = os.WriteFile(filename, data, 0644)
	assert.NoError(t, err)

	// 验证文件存在
	_, err = os.Stat(filename)
	assert.NoError(t, err)
}

// TestGetFrequentReconnectUsers 测试获取频繁重连的用户
func TestGetFrequentReconnectUsers(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	now := time.Now()

	// 用户1：重连10次
	for i := 0; i < 2; i++ {
		record := createTestRecord("user1", "node1", false)
		record.ReconnectCount = 5
		record.ConnectedAt = now.Add(-1 * time.Hour)
		repo.Create(ctx, record)
		db.WithContext(ctx).Model(record).Update("is_active", false)
	}

	// 用户2：重连2次
	record2 := createTestRecord("user2", "node1", false)
	record2.ReconnectCount = 2
	record2.ConnectedAt = now.Add(-1 * time.Hour)
	repo.Create(ctx, record2)
	db.WithContext(ctx).Model(record2).Update("is_active", false)

	stats, err := repo.GetFrequentReconnectUsers(ctx, 5, 24*time.Hour)
	assert.NoError(t, err)
	assert.Len(t, stats, 1)
	assert.Equal(t, "user1", stats[0].UserID)
	assert.Equal(t, int64(10), stats[0].ReconnectCount)
}

// TestGetAbnormalConnections 测试获取异常连接
func TestGetAbnormalConnections(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建正常和异常连接
	normal := createTestRecord("user1", "node1", false)
	normal.IsAbnormal = false
	repo.Create(ctx, normal)
	db.WithContext(ctx).Model(normal).Update("is_active", false)

	abnormal1 := createTestRecord("user2", "node1", false)
	abnormal1.IsAbnormal = true
	repo.Create(ctx, abnormal1)
	db.WithContext(ctx).Model(abnormal1).Update("is_active", false)

	abnormal2 := createTestRecord("user3", "node1", false)
	abnormal2.IsAbnormal = true
	repo.Create(ctx, abnormal2)
	db.WithContext(ctx).Model(abnormal2).Update("is_active", false)

	records, err := repo.GetAbnormalConnections(ctx, 10, 0)
	assert.NoError(t, err)
	assert.Len(t, records, 2)
	for _, r := range records {
		assert.True(t, r.IsAbnormal)
	}
}

// TestGetHighErrorRateConnections 测试获取高错误率连接
func TestGetHighErrorRateConnections(t *testing.T) {
	db := getConnectTestDB(t)
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 创建低错误率连接
	low := createTestRecord("user1", "node1", false)
	low.ErrorCount = 2
	repo.Create(ctx, low)
	db.WithContext(ctx).Model(low).Update("is_active", false)

	// 创建高错误率连接
	high1 := createTestRecord("user2", "node1", false)
	high1.ErrorCount = 10
	repo.Create(ctx, high1)
	db.WithContext(ctx).Model(high1).Update("is_active", false)

	high2 := createTestRecord("user3", "node1", false)
	high2.ErrorCount = 15
	repo.Create(ctx, high2)
	db.WithContext(ctx).Model(high2).Update("is_active", false)

	records, err := repo.GetHighErrorRateConnections(ctx, 5, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 2)
	for _, r := range records {
		assert.GreaterOrEqual(t, r.ErrorCount, 5)
	}
}

// BenchmarkArchiveOldRecords 性能测试：归档旧记录
func BenchmarkArchiveOldRecords(b *testing.B) {
	db := getConnectTestDB(&testing.T{})
	repo := NewConnectionRecordRepository(db)
	ctx := context.Background()

	// 准备10000条旧记录
	oldTime := time.Now().Add(-30 * 24 * time.Hour)
	for i := 0; i < 10000; i++ {
		record := createTestRecord(fmt.Sprintf("user%d", i), "node1", false)
		record.ConnectedAt = oldTime
		repo.Create(ctx, record)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		repo.ArchiveOldRecords(ctx, time.Now(), func(records []*ConnectionRecord) error {
			// 模拟处理逻辑
			return nil
		})
	}
}

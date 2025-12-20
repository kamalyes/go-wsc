/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-19 16:54:26
 * @FilePath: \go-wsc\offline_message_repository_test.go
 * @Description: 离线消息仓库集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	testOfflineDBInstance *gorm.DB
	testOfflineDBOnce     sync.Once
)

// 测试用 MySQL 配置（使用 local 配置文件中的数据库）- 单例模式
func getTestOfflineDB(t *testing.T) *gorm.DB {
	testOfflineDBOnce.Do(func() {
		dsn := "root:idev88888@tcp(120.77.38.35:13306)/im_agent?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Silent), // 测试时使用静默模式
			SkipDefaultTransaction: true,                                  // 跳过默认事务，提升性能
			PrepareStmt:            true,                                  // 预编译语句，提升性能
		})
		require.NoError(t, err, "数据库连接失败")

		// 只执行一次自动迁移
		err = db.AutoMigrate(&OfflineMessageRecord{})
		require.NoError(t, err, "数据库迁移失败")

		// 配置连接池
		sqlDB, err := db.DB()
		require.NoError(t, err, "获取底层DB失败")
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetConnMaxLifetime(time.Hour)

		testOfflineDBInstance = db
	})
	return testOfflineDBInstance
}

// 创建测试用的离线消息记录
func createTestOfflineMessageRecord(messageID, userID, sessionID string) *OfflineMessageRecord {
	now := time.Now()

	// 创建 HubMessage 对象
	hubMsg := &HubMessage{
		ID:           messageID,
		MessageType:  MessageTypeText,
		Sender:       "sender-001",
		SenderType:   UserTypeCustomer,
		Receiver:     userID,
		ReceiverType: UserTypeAgent,
		SessionID:    sessionID,
		Content:      "这是一条测试离线消息",
		Data:         map[string]interface{}{"key": "value"},
		CreateAt:     now,
	}

	// 压缩 HubMessage
	compressedData, _, err := zipx.ZlibCompressObjectWithSize(hubMsg)
	if err != nil {
		panic("compress test message failed: " + err.Error())
	}

	return &OfflineMessageRecord{
		MessageID:      messageID,
		Receiver:       userID,
		SessionID:      sessionID,
		CompressedData: compressedData,
		ScheduledAt:    now,
		ExpireAt:       now.Add(7 * 24 * time.Hour),
		CreatedAt:      now,
	}
}

func TestOfflineMessageRepositorySave(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	messageID := osx.HashUnixMicroCipherText()
	userID := "user-001"
	sessionID := "session-001"

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, []string{messageID})
	}()

	record := createTestOfflineMessageRecord(messageID, userID, sessionID)

	// 保存记录
	err := repo.Save(ctx, record)
	assert.NoError(t, err)
	assert.NotZero(t, record.ID)
}

func TestOfflineMessageRepositoryGetByReceiver(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-002"
	sessionID := "session-002"

	// 创建多条测试数据
	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond) // 确保创建时间不同
	}

	// 查询用户的离线消息
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 3)

	// 验证按创建时间升序排列
	for i := 0; i < len(records)-1; i++ {
		assert.True(t, records[i].CreatedAt.Before(records[i+1].CreatedAt) || records[i].CreatedAt.Equal(records[i+1].CreatedAt))
	}
}

func TestOfflineMessageRepositoryGetByReceiverWithLimit(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-003"
	sessionID := "session-003"

	// 创建5条消息
	messageIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	// 限制只获取3条
	records, err := repo.GetByReceiver(ctx, userID, 3)
	assert.NoError(t, err)
	assert.Len(t, records, 3, "应该只返回3条记录")
}

func TestOfflineMessageRepositoryDeleteByMessageIDs(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-004"
	sessionID := "session-004"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 验证保存成功
	beforeDelete, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, beforeDelete, 2)

	// 删除第一条消息
	err = repo.DeleteByMessageIDs(ctx, userID, []string{messageIDs[0]})
	assert.NoError(t, err)

	// 验证删除
	afterDelete, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, afterDelete, 1)
	assert.Equal(t, messageIDs[1], afterDelete[0].MessageID)

	// 清理剩余数据
	_ = repo.DeleteByMessageIDs(ctx, userID, []string{messageIDs[1]})
}

func TestOfflineMessageRepositoryGetCountByReceiver(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-005"
	sessionID := "session-005"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 获取消息数量
	count, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestOfflineMessageRepositoryClearByReceiver(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-006"
	sessionID := "session-006"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 验证数据存在
	beforeClear, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(2), beforeClear)

	// 清空用户的所有离线消息
	err = repo.ClearByReceiver(ctx, userID)
	assert.NoError(t, err)

	// 验证清空
	afterClear, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), afterClear)
}

func TestOfflineMessageRepositoryDeleteExpired(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-007"
	sessionID := "session-007"

	// 创建已过期的消息
	expiredMsgID := osx.HashUnixMicroCipherText()
	expiredRecord := createTestOfflineMessageRecord(expiredMsgID, userID, sessionID)
	expiredRecord.ExpireAt = time.Now().Add(-1 * time.Hour) // 1小时前过期
	err := repo.Save(ctx, expiredRecord)
	require.NoError(t, err)

	// 创建未过期的消息
	validMsgID := osx.HashUnixMicroCipherText()
	validRecord := createTestOfflineMessageRecord(validMsgID, userID, sessionID)
	validRecord.ExpireAt = time.Now().Add(24 * time.Hour) // 24小时后过期
	err = repo.Save(ctx, validRecord)
	require.NoError(t, err)

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, []string{validMsgID})
	}()

	// 删除过期消息
	deletedCount, err := repo.DeleteExpired(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, deletedCount, int64(1), "应该至少删除1条过期消息")

	// 验证未过期的消息仍然存在
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1)
	assert.Equal(t, validMsgID, records[0].MessageID)
}

func TestOfflineMessageRepositoryConcurrentSave(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-009"
	sessionID := "session-009"
	concurrency := 10

	messageIDs := make([]string, concurrency)
	for i := 0; i < concurrency; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 清理测试数据
	defer func() {
		_ = repo.ClearByReceiver(ctx, userID)
	}()

	// 并发保存消息
	var wg sync.WaitGroup
	errChan := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			record := createTestOfflineMessageRecord(messageIDs[idx], userID, sessionID)
			if err := repo.Save(ctx, record); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// 检查是否有错误
	for err := range errChan {
		assert.NoError(t, err)
	}

	// 验证所有消息都保存成功
	count, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(concurrency), count)
}

func TestOfflineMessageRepositoryEmptyDeleteByMessageIDs(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	// 删除空数组应该不报错
	err := repo.DeleteByMessageIDs(ctx, "any-user", []string{})
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryGetBySender(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	senderID := "sender-010"
	userID := "user-010"
	sessionID := "session-010"

	// 创建多条测试数据
	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		record.Sender = senderID
		err := repo.Save(ctx, record)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	// 查询发送者的离线消息
	records, err := repo.GetBySender(ctx, senderID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 2)

	// 验证所有记录的发送者都是指定的senderID
	for _, record := range records {
		assert.Equal(t, senderID, record.Sender)
	}
}

func TestOfflineMessageRepositoryGetCountBySender(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	senderID := "sender-011"
	userID := "user-011"
	sessionID := "session-011"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		record.Sender = senderID
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 获取发送者的消息数量
	count, err := repo.GetCountBySender(ctx, senderID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestOfflineMessageRepositoryMarkAsPushed(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-012"
	sessionID := "session-012"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 验证初始状态：PushedAt 应该为 nil
	beforePush, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, beforePush, 2)
	for _, record := range beforePush {
		assert.Nil(t, record.PushedAt)
	}

	// 标记第一条消息为已推送
	err = repo.MarkAsPushed(ctx, []string{messageIDs[0]})
	assert.NoError(t, err)

	// 验证：只有未推送的消息才会被查询到
	afterPush, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, afterPush, 1, "应该只有1条未推送的消息")
	assert.Equal(t, messageIDs[1], afterPush[0].MessageID)

	// 标记所有消息为已推送
	err = repo.MarkAsPushed(ctx, messageIDs)
	assert.NoError(t, err)

	// 验证：所有消息都已推送，查询结果为空
	allPushed, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, allPushed, 0, "所有消息都已推送，应该返回空列表")
}

func TestOfflineMessageRepositoryMarkAsPushedEmptyList(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	// 空数组应该不报错
	err := repo.MarkAsPushed(ctx, []string{})
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryExpiredMessageNotRetrieved(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-013"
	sessionID := "session-013"

	// 创建已过期的消息
	expiredMsgID := osx.HashUnixMicroCipherText()
	expiredRecord := createTestOfflineMessageRecord(expiredMsgID, userID, sessionID)
	expiredRecord.ExpireAt = time.Now().Add(-1 * time.Hour)
	err := repo.Save(ctx, expiredRecord)
	require.NoError(t, err)

	// 创建未过期的消息
	validMsgID := osx.HashUnixMicroCipherText()
	validRecord := createTestOfflineMessageRecord(validMsgID, userID, sessionID)
	err = repo.Save(ctx, validRecord)
	require.NoError(t, err)

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, []string{expiredMsgID, validMsgID})
	}()

	// 查询：只应该返回未过期的消息
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1, "只应该返回未过期的消息")
	assert.Equal(t, validMsgID, records[0].MessageID)

	// 统计：也只应该计算未过期的消息
	count, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count, "统计时只应该包含未过期的消息")
}

func TestOfflineMessageRepositoryPushedMessageNotRetrieved(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-014"
	sessionID := "session-014"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 标记第一条消息为已推送
	err := repo.MarkAsPushed(ctx, []string{messageIDs[0]})
	assert.NoError(t, err)

	// GetByReceiver 不应该返回已推送的消息
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1)
	assert.Equal(t, messageIDs[1], records[0].MessageID)

	// GetCountByReceiver 也不应该计算已推送的消息
	count, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestOfflineMessageRepositoryConcurrentMarkAsPushed(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-015"
	sessionID := "session-015"
	concurrency := 10

	messageIDs := make([]string, concurrency)
	for i := 0; i < concurrency; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 并发标记为已推送
	var wg sync.WaitGroup
	errChan := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := repo.MarkAsPushed(ctx, []string{messageIDs[idx]}); err != nil {
				errChan <- err
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	// 检查是否有错误
	for err := range errChan {
		assert.NoError(t, err)
	}

	// 验证所有消息都已标记为推送
	count, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count, "所有消息都应该已推送")
}

func TestOfflineMessageRepositoryDeleteNonExistentMessage(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	// 删除不存在的消息应该不报错
	err := repo.DeleteByMessageIDs(ctx, "non-existent-user", []string{"non-existent-id"})
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryGetByReceiverNoResults(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	// 查询不存在的用户
	records, err := repo.GetByReceiver(ctx, "non-existent-user", 10)
	assert.NoError(t, err)
	assert.Len(t, records, 0)
}

func TestOfflineMessageRepositoryGetCountByReceiverZero(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	// 查询不存在的用户
	count, err := repo.GetCountByReceiver(ctx, "non-existent-user")
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestOfflineMessageRepositoryClearNonExistentUser(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	// 清空不存在的用户应该不报错
	err := repo.ClearByReceiver(ctx, "non-existent-user")
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryBatchMarkAsPushed(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db)
	ctx := context.Background()

	userID := "user-016"
	sessionID := "session-016"

	// 创建5条消息
	messageIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := createTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 批量标记前3条为已推送
	err := repo.MarkAsPushed(ctx, messageIDs[:3])
	assert.NoError(t, err)

	// 验证：只有2条未推送的消息
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 2, "应该有2条未推送的消息")

	// 验证未推送的消息ID
	unpushedIDs := []string{records[0].MessageID, records[1].MessageID}
	assert.Contains(t, unpushedIDs, messageIDs[3])
	assert.Contains(t, unpushedIDs, messageIDs[4])
}

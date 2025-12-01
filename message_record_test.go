/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-02 09:24:27
 * @FilePath: \go-wsc\message_record_test.go
 * @Description: 消息记录仓库集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
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
	testDBInstance *gorm.DB
	testDBOnce     sync.Once
)

// 测试用 MySQL 配置（使用 local 配置文件中的数据库）- 单例模式
func getTestDB(t *testing.T) *gorm.DB {
	testDBOnce.Do(func() {
		dsn := "root:idev88888@tcp(120.77.38.35:13306)/im_agent?charset=utf8mb4&parseTime=True&loc=Local&timeout=10s"
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
			Logger:                 logger.Default.LogMode(logger.Silent), // 测试时使用静默模式
			SkipDefaultTransaction: true,                                  // 跳过默认事务，提升性能
			PrepareStmt:            true,                                  // 预编译语句，提升性能
		})
		require.NoError(t, err, "数据库连接失败")

		// 只执行一次自动迁移
		err = db.AutoMigrate(&MessageSendRecord{})
		require.NoError(t, err, "数据库迁移失败")

		// 配置连接池
		sqlDB, err := db.DB()
		require.NoError(t, err, "获取底层DB失败")
		sqlDB.SetMaxIdleConns(10)
		sqlDB.SetMaxOpenConns(20)
		sqlDB.SetConnMaxLifetime(time.Hour)

		testDBInstance = db
	})
	return testDBInstance
}

// 创建测试用的 HubMessage
func createTestHubMessage(id, sender, receiver string, msgType MessageType) *HubMessage {
	return &HubMessage{
		ID:          id,
		MessageID:   id,
		Sender:      sender,
		Receiver:    receiver,
		MessageType: msgType,
		Content:     "test message",
		Data:        map[string]interface{}{"content": "test data"},
		CreateAt:    time.Now(),
		Priority:    PriorityNormal,
		Status:      MessageStatusDelivered,
	}
}

func TestMessageSendRecord_SetAndGetMessage(t *testing.T) {
	record := &MessageSendRecord{}
	msg := createTestHubMessage("msg-001", "user-001", "user-002", MessageTypeText)

	// 设置消息
	err := record.SetMessage(msg)
	assert.NoError(t, err)
	assert.Equal(t, "msg-001", record.MessageID)
	assert.Equal(t, "user-001", record.Sender)
	assert.Equal(t, "user-002", record.Receiver)
	assert.Equal(t, MessageTypeText, record.MessageType)
	assert.NotEmpty(t, record.MessageData)

	// 获取消息
	retrievedMsg, err := record.GetMessage()
	assert.NoError(t, err)
	assert.NotNil(t, retrievedMsg)
	assert.Equal(t, msg.ID, retrievedMsg.ID)
	assert.Equal(t, msg.Sender, retrievedMsg.Sender)
	assert.Equal(t, msg.Receiver, retrievedMsg.Receiver)
	assert.Equal(t, msg.MessageType, retrievedMsg.MessageType)
}

func TestMessageRecordRepository_Create(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	// 使用唯一ID生成器
	msgID := osx.HashUnixMicroCipherText()

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-001", "receiver-001", MessageTypeText)
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.1.100",
		ClientIP:   "10.0.0.1",
	}

	err := record.SetMessage(msg)
	require.NoError(t, err)

	err = repo.Create(record)
	assert.NoError(t, err)
	assert.NotZero(t, record.ID)
}

func TestMessageRecordRepository_CreateFromMessage(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-002", "receiver-002", MessageTypeImage)
	expiresAt := time.Now().Add(1 * time.Hour)

	record, err := repo.CreateFromMessage(msg, 5, &expiresAt)
	assert.NoError(t, err)
	assert.NotNil(t, record)
	assert.NotZero(t, record.ID)
	assert.Equal(t, MessageSendStatusPending, record.Status)
	assert.Equal(t, 5, record.MaxRetry)
	assert.Equal(t, msgID, record.MessageID)
}

func TestMessageRecordRepository_FindByMessageID(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-003", "receiver-003", MessageTypeText)
	created, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	// 查找记录
	found, err := repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.NotNil(t, found)
	assert.Equal(t, created.ID, found.ID)
	assert.Equal(t, msgID, found.MessageID)
}

func TestMessageRecordRepository_UpdateStatus(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-004", "receiver-004", MessageTypeText)
	created, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	// 更新为发送中
	err = repo.UpdateStatus(msgID, MessageSendStatusSending, "", "")
	assert.NoError(t, err)

	// 验证更新
	updated, err := repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusSending, updated.Status)

	// 更新为成功
	err = repo.UpdateStatus(msgID, MessageSendStatusSuccess, "", "")
	assert.NoError(t, err)

	// 验证成功状态
	success, err := repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusSuccess, success.Status)
	assert.NotNil(t, success.SuccessTime)

	// 更新为失败
	err = repo.UpdateStatus(msgID, MessageSendStatusFailed, FailureReasonNetworkError, "Network timeout")
	assert.NoError(t, err)

	// 验证失败状态
	failed, err := repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, failed.Status)
	assert.Equal(t, FailureReasonNetworkError, failed.FailureReason)
	assert.Equal(t, "Network timeout", failed.ErrorMessage)

	_ = created
}

func TestMessageRecordRepository_IncrementRetry(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-005", "receiver-005", MessageTypeText)
	_, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	// 第一次重试
	attempt1 := RetryAttempt{
		AttemptNumber: 1,
		Timestamp:     time.Now(),
		Duration:      100 * time.Millisecond,
		Error:         "connection timeout",
		Success:       false,
	}
	err = repo.IncrementRetry(msgID, attempt1)
	assert.NoError(t, err)

	// 验证重试次数
	record, err := repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.Equal(t, 1, record.RetryCount)
	assert.Len(t, record.RetryHistory, 1)
	assert.Equal(t, MessageSendStatusRetrying, record.Status)

	// 第二次重试
	attempt2 := RetryAttempt{
		AttemptNumber: 2,
		Timestamp:     time.Now(),
		Duration:      150 * time.Millisecond,
		Error:         "queue full",
		Success:       false,
	}
	err = repo.IncrementRetry(msgID, attempt2)
	assert.NoError(t, err)

	record, err = repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.Equal(t, 2, record.RetryCount)
	assert.Len(t, record.RetryHistory, 2)

	// 第三次重试成功
	attempt3 := RetryAttempt{
		AttemptNumber: 3,
		Timestamp:     time.Now(),
		Duration:      80 * time.Millisecond,
		Error:         "",
		Success:       true,
	}
	err = repo.IncrementRetry(msgID, attempt3)
	assert.NoError(t, err)

	record, err = repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.Equal(t, 3, record.RetryCount)
	assert.Len(t, record.RetryHistory, 3)
	assert.Equal(t, MessageSendStatusSuccess, record.Status)
	assert.NotNil(t, record.SuccessTime)
}

func TestMessageRecordRepository_FindByStatus(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	// 生成唯一ID
	msgIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		for _, msgID := range msgIDs {
			_ = repo.DeleteByMessageID(msgID)
		}
	}()

	// 创建不同状态的记录
	statuses := []MessageSendStatus{
		MessageSendStatusPending,
		MessageSendStatusSuccess,
		MessageSendStatusFailed,
	}

	for i, status := range statuses {
		msg := createTestHubMessage(msgIDs[i], "sender", "receiver", MessageTypeText)
		record, err := repo.CreateFromMessage(msg, 3, nil)
		require.NoError(t, err)

		if status != MessageSendStatusPending {
			err = repo.UpdateStatus(record.MessageID, status, "", "")
			require.NoError(t, err)
		}
	}

	// 查找待发送的记录
	pending, err := repo.FindByStatus(MessageSendStatusPending, 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(pending), 1)

	// 查找成功的记录
	success, err := repo.FindByStatus(MessageSendStatusSuccess, 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(success), 1)

	// 查找失败的记录
	failed, err := repo.FindByStatus(MessageSendStatusFailed, 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(failed), 1)
}

func TestMessageRecordRepository_FindBySenderAndReceiver(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "alice", "bob", MessageTypeText)
	_, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	// 按发送者查找
	senderRecords, err := repo.FindBySender("alice", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(senderRecords), 1)
	found := false
	for _, r := range senderRecords {
		if r.MessageID == msgID {
			found = true
			break
		}
	}
	assert.True(t, found)

	// 按接收者查找
	receiverRecords, err := repo.FindByReceiver("bob", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(receiverRecords), 1)
	found = false
	for _, r := range receiverRecords {
		if r.MessageID == msgID {
			found = true
			break
		}
	}
	assert.True(t, found)
}

func TestMessageRecordRepository_FindByNodeIPAndClientIP(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender-ip", "receiver-ip", MessageTypeText)
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		NodeIP:     "192.168.100.50",
		ClientIP:   "10.20.30.40",
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)
	err = repo.Create(record)
	require.NoError(t, err)

	// 按节点IP查找
	nodeRecords, err := repo.FindByNodeIP("192.168.100.50", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(nodeRecords), 1)
	found := false
	for _, r := range nodeRecords {
		if r.MessageID == msgID {
			found = true
			assert.Equal(t, "192.168.100.50", r.NodeIP)
			break
		}
	}
	assert.True(t, found)

	// 按客户端IP查找
	clientRecords, err := repo.FindByClientIP("10.20.30.40", 10)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, len(clientRecords), 1)
	found = false
	for _, r := range clientRecords {
		if r.MessageID == msgID {
			found = true
			assert.Equal(t, "10.20.30.40", r.ClientIP)
			break
		}
	}
	assert.True(t, found)
}

func TestMessageRecordRepository_FindRetryable(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID1 := osx.HashUnixMicroCipherText()
	msgID2 := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID1)
		_ = repo.DeleteByMessageID(msgID2)
	}()

	// 创建可重试的失败记录
	msg1 := createTestHubMessage(msgID1, "sender", "receiver", MessageTypeText)
	record1, err := repo.CreateFromMessage(msg1, 3, nil)
	require.NoError(t, err)
	err = repo.UpdateStatus(record1.MessageID, MessageSendStatusFailed, FailureReasonNetworkError, "timeout")
	require.NoError(t, err)

	// 创建超过最大重试次数的记录
	msg2 := createTestHubMessage(msgID2, "sender", "receiver", MessageTypeText)
	record2, err := repo.CreateFromMessage(msg2, 1, nil)
	require.NoError(t, err)
	err = repo.IncrementRetry(record2.MessageID, RetryAttempt{
		AttemptNumber: 1,
		Timestamp:     time.Now(),
		Success:       false,
	})
	require.NoError(t, err)

	// 查找可重试的记录
	retryable, err := repo.FindRetryable(10)
	assert.NoError(t, err)

	// 验证第一条记录在可重试列表中
	found := false
	for _, r := range retryable {
		if r.MessageID == msgID1 {
			found = true
			assert.Less(t, r.RetryCount, r.MaxRetry)
			break
		}
	}
	assert.True(t, found)
}

func TestMessageRecordRepository_FindExpired(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	// 创建过期的记录
	msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
	pastTime := time.Now().Add(-1 * time.Hour) // 1小时前过期
	record, err := repo.CreateFromMessage(msg, 3, &pastTime)
	require.NoError(t, err)

	// 查找过期的记录
	expired, err := repo.FindExpired(10)
	assert.NoError(t, err)

	// 验证记录在过期列表中
	found := false
	for _, r := range expired {
		if r.MessageID == msgID {
			found = true
			assert.NotNil(t, r.ExpiresAt)
			assert.True(t, r.ExpiresAt.Before(time.Now()))
			break
		}
	}
	assert.True(t, found)

	_ = record
}

func TestMessageRecordRepository_GetStatistics(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	stats, err := repo.GetStatistics()
	assert.NoError(t, err)
	assert.NotNil(t, stats)
	assert.Contains(t, stats, "total")
	assert.Contains(t, stats, string(MessageSendStatusPending))
	assert.Contains(t, stats, string(MessageSendStatusSuccess))
	assert.Contains(t, stats, string(MessageSendStatusFailed))
}

func TestMessageRecordRepository_CleanupOld(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 创建旧记录
	msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
	record, err := repo.CreateFromMessage(msg, 3, nil)
	require.NoError(t, err)

	// 更新为成功状态
	err = repo.UpdateStatus(record.MessageID, MessageSendStatusSuccess, "", "")
	require.NoError(t, err)

	// 手动设置创建时间为很久以前
	oldTime := time.Now().Add(-365 * 24 * time.Hour) // 1年前
	err = repo.GetDB().Model(&MessageSendRecord{}).
		Where("message_id = ?", record.MessageID).
		Update("create_time", oldTime).Error
	require.NoError(t, err)

	// 清理30天前的记录
	cutoff := time.Now().Add(-30 * 24 * time.Hour)
	deleted, err := repo.CleanupOld(cutoff)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, deleted, int64(1))

	// 验证记录已删除
	_, err = repo.FindByMessageID(msgID)
	assert.Error(t, err) // 应该找不到
}

func TestMessageSendRecord_JSONFields(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	msgID := osx.HashUnixMicroCipherText()
	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageID(msgID)
	}()

	msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   3,
		Metadata: JSONMap{
			"priority": "high",
			"source":   "mobile_app",
			"version":  1.5,
		},
		CustomFields: JSONMap{
			"user_level": "vip",
			"region":     "asia",
		},
		Tags: StringArray{"urgent", "support", "vip"},
	}
	err := record.SetMessage(msg)
	require.NoError(t, err)

	err = repo.Create(record)
	require.NoError(t, err)

	// 重新查询验证JSON字段
	found, err := repo.FindByMessageID(msgID)
	assert.NoError(t, err)
	assert.NotNil(t, found.Metadata)
	assert.Equal(t, "high", found.Metadata["priority"])
	assert.Equal(t, "mobile_app", found.Metadata["source"])
	assert.NotNil(t, found.CustomFields)
	assert.Equal(t, "vip", found.CustomFields["user_level"])
	assert.Len(t, found.Tags, 3)
	assert.Contains(t, found.Tags, "urgent")
}

func TestMessageRecordRepository_Concurrency(t *testing.T) {
	db := getTestDB(t)
	repo := NewMessageRecordRepository(db)

	// 并发创建记录
	const goroutines = 10
	done := make(chan bool, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(index int) {
			defer func() { done <- true }()

			msgID := osx.HashUnixMicroCipherText()
			msg := createTestHubMessage(msgID, "sender", "receiver", MessageTypeText)
			_, err := repo.CreateFromMessage(msg, 3, nil)
			assert.NoError(t, err)

			// 清理
			defer func() {
				_ = repo.DeleteByMessageID(msgID)
			}()
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < goroutines; i++ {
		<-done
	}
}

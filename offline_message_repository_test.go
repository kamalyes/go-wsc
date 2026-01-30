/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 17:15:29
 * @FilePath: \go-wsc\offline_message_repository_test.go
 * @Description: 离线消息仓库集成测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ============================================================================
// 测试辅助函数
// ============================================================================

// testOfflineRepoContext 封装测试仓库的上下文
type testOfflineRepoContext struct {
	t           *testing.T
	db          *gorm.DB
	repo        OfflineMessageDBRepository
	ctx         context.Context
	userID      string
	sessionID   string
	cleanupIDs  []string
	idGenerator *idgen.ShortFlakeGenerator
}

// newTestOfflineRepoContext 创建测试仓库上下文
func newTestOfflineRepoContext(t *testing.T, userSuffix string) *testOfflineRepoContext {
	workerID := osx.GetWorkerIdForSnowflake()
	tc := &testOfflineRepoContext{
		t:           t,
		db:          getTestOfflineDB(t),
		repo:        NewGormOfflineMessageRepository(getTestOfflineDB(t),nil,NewDefaultWSCLogger()),
		ctx:         context.Background(),
		userID:      "user-" + userSuffix,
		sessionID:   "session-" + userSuffix,
		cleanupIDs:  make([]string, 0),
		idGenerator: idgen.NewShortFlakeGenerator(workerID),
	}
	// 预清理避免数据污染
	tc.cleanupAll()
	return tc
}

// cleanup 清理测试数据
func (c *testOfflineRepoContext) cleanup() {
	if len(c.cleanupIDs) > 0 {
		_ = c.repo.DeleteByMessageIDs(c.ctx, c.userID, c.cleanupIDs)
	}
}

// cleanupAll 清空用户所有数据
func (c *testOfflineRepoContext) cleanupAll() {
	_ = c.repo.ClearByReceiver(c.ctx, c.userID)
}

// createMessage 创建单条测试消息并添加到清理列表（使用雪花算法生成ID确保时间序列）
func (c *testOfflineRepoContext) createMessage() (string, *OfflineMessageRecord) {
	// 使用雪花算法生成数字ID,转换为至少20字符的零填充字符串
	msgID := fmt.Sprintf("%020d", c.idGenerator.Generate())
	c.cleanupIDs = append(c.cleanupIDs, msgID)
	time.Sleep(2 * time.Millisecond) // 确保雪花算法生成的ID有时间差异
	return msgID, CreateTestOfflineMessageRecord(msgID, c.userID, c.sessionID)
}

// createMessages 创建多条测试消息并添加到清理列表
func (c *testOfflineRepoContext) createMessages(count int) ([]string, []*OfflineMessageRecord) {
	msgIDs := make([]string, count)
	records := make([]*OfflineMessageRecord, count)
	for i := 0; i < count; i++ {
		msgIDs[i], records[i] = c.createMessage()
	}
	return msgIDs, records
}

// saveMessages 保存多条消息(带时间间隔确保顺序)
func (c *testOfflineRepoContext) saveMessages(records []*OfflineMessageRecord, withDelay bool) {
	for _, record := range records {
		err := c.repo.Save(c.ctx, record)
		require.NoError(c.t, err)
		if withDelay {
			time.Sleep(20 * time.Millisecond) // 增加到20ms确保MySQL datetime精度
		}
	}
}

func TestOfflineMessageRepositoryGetByReceiver(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "002")
	defer tc.cleanup()

	_, records := tc.createMessages(3)
	tc.saveMessages(records, true)

	result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, result, 3)

	// 验证按创建时间升序排列
	for i := 0; i < len(result)-1; i++ {
		assert.True(t, result[i].CreatedAt.Before(result[i+1].CreatedAt) || result[i].CreatedAt.Equal(result[i+1].CreatedAt))
	}
}

func TestOfflineMessageRepositoryGetByReceiverWithLimit(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "003")
	defer tc.cleanup()

	_, records := tc.createMessages(5)
	tc.saveMessages(records, true)

	result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 3)
	assert.NoError(t, err)
	assert.Len(t, result, 3, "应该只返回3条记录")
}

func TestOfflineMessageRepositoryDeleteByMessageIDs(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "004")
	defer tc.cleanup()

	msgIDs, records := tc.createMessages(2)
	tc.saveMessages(records, false)

	// 验证保存成功
	beforeDelete, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, beforeDelete, 2)

	// 删除第一条消息
	err = tc.repo.DeleteByMessageIDs(tc.ctx, tc.userID, []string{msgIDs[0]})
	assert.NoError(t, err)

	// 验证删除
	afterDelete, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, afterDelete, 1)
	assert.Equal(t, msgIDs[1], afterDelete[0].MessageID)
}

func TestOfflineMessageRepositoryGetCountByReceiver(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "005")
	defer tc.cleanup()

	_, records := tc.createMessages(3)
	tc.saveMessages(records, false)

	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestOfflineMessageRepositoryClearByReceiver(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "006")

	_, records := tc.createMessages(2)
	tc.saveMessages(records, false)

	// 验证数据存在
	beforeClear, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(2), beforeClear)

	// 清空用户的所有离线消息
	err = tc.repo.ClearByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)

	// 验证清空
	afterClear, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), afterClear)
}

func TestOfflineMessageRepositoryDeleteExpired(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "007")
	defer tc.cleanup()

	// 创建已过期的消息
	expiredMsgID := osx.HashUnixMicroCipherText()
	expiredRecord := CreateTestOfflineMessageRecord(expiredMsgID, tc.userID, tc.sessionID)
	expiredRecord.ExpireAt = time.Now().Add(-1 * time.Hour)
	err := tc.repo.Save(tc.ctx, expiredRecord)
	require.NoError(t, err)

	// 创建未过期的消息
	validMsgID, validRecord := tc.createMessage()
	validRecord.ExpireAt = time.Now().Add(24 * time.Hour)
	err = tc.repo.Save(tc.ctx, validRecord)
	require.NoError(t, err)

	// 删除过期消息
	deletedCount, err := tc.repo.DeleteExpired(tc.ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, deletedCount, int64(1), "应该至少删除1条过期消息")

	// 验证未过期的消息仍然存在
	records, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1)
	assert.Equal(t, validMsgID, records[0].MessageID)
}

func TestOfflineMessageRepositoryConcurrentSave(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "009")
	defer tc.cleanup()

	concurrency := 10
	msgIDs, records := tc.createMessages(concurrency)

	// 并发保存消息
	var wg sync.WaitGroup
	errChan := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := tc.repo.Save(tc.ctx, records[idx]); err != nil {
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
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(concurrency), count)

	// 验证 messageIDs 被使用
	_ = msgIDs
}

func TestOfflineMessageRepositoryEmptyDeleteByMessageIDs(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "empty")

	// 删除空数组应该不报错
	err := tc.repo.DeleteByMessageIDs(tc.ctx, "any-user", []string{})
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryGetBySender(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "010")
	defer tc.cleanup()

	senderID := "sender-010"
	_, records := tc.createMessages(2)

	// 设置发送者并保存
	for _, record := range records {
		record.Sender = senderID
	}
	tc.saveMessages(records, true)

	// 查询发送者的离线消息
	result, err := tc.repo.GetBySender(tc.ctx, senderID, 10)
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	// 验证所有记录的发送者都是指定的senderID
	for _, record := range result {
		assert.Equal(t, senderID, record.Sender)
	}
}

func TestOfflineMessageRepositoryGetCountBySender(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "011")
	defer tc.cleanup()

	senderID := "sender-011"
	_, records := tc.createMessages(3)

	// 设置发送者并保存
	for _, record := range records {
		record.Sender = senderID
	}
	tc.saveMessages(records, false)

	// 获取发送者的消息数量
	count, err := tc.repo.GetCountBySender(tc.ctx, senderID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestOfflineMessageRepositoryUpdatePushStatus(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "012")
	defer tc.cleanup()

	// ========== 阶段1: 用户离线，消息存储 ==========
	msgIDs, records := tc.createMessages(3)

	// 手动设置正确的初始状态
	for _, record := range records {
		record.Status = MessageSendStatusUserOffline
	}
	tc.saveMessages(records, false)
	t.Logf("✅ 阶段1完成：存储了3条离线消息(状态=user_offline)")

	// 验证初始状态：Status 应该为 UserOffline
	beforePush, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, beforePush, 3)
	for _, record := range beforePush {
		assert.Equal(t, MessageSendStatusUserOffline, record.Status, "初始状态应该是 user_offline")
		assert.Nil(t, record.FirstPushAt, "未推送时 FirstPushAt 应为 nil")
		assert.Nil(t, record.LastPushAt, "未推送时 LastPushAt 应为 nil")
	}
	t.Logf("✅ 验证通过：3条消息都是 user_offline 状态，未推送")

	// ========== 阶段2: 用户上线，推送消息 ==========
	time.Sleep(100 * time.Millisecond) // 模拟时间流逝

	// 第一条：推送成功
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgIDs[0]}, MessageSendStatusSuccess, "")
	assert.NoError(t, err)
	t.Logf("✅ 阶段2.1完成：第一条消息推送成功")

	// 第二条：推送失败
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgIDs[1]}, MessageSendStatusFailed, "push timeout")
	assert.NoError(t, err)
	t.Logf("✅ 阶段2.2完成：第二条消息推送失败")

	// 第三条：推送失败后重试
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgIDs[2]}, MessageSendStatusFailed, "network error")
	assert.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	// 重试后成功
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgIDs[2]}, MessageSendStatusSuccess, "")
	assert.NoError(t, err)
	t.Logf("✅ 阶段2.3完成：第三条消息重试后推送成功")

	// ========== 阶段3: 验证最终状态 ==========
	var successRecord1, failedRecord, successRecord2 OfflineMessageRecord

	// 验证第一条：推送成功
	err = tc.db.Where("message_id = ?", msgIDs[0]).First(&successRecord1).Error
	assert.NoError(t, err)
	t.Logf("📊 第一条消息原始数据: ID=%s, Status=%s, RetryCount=%d, FirstPushAt=%v, LastPushAt=%v, Error=%q",
		successRecord1.MessageID, successRecord1.Status, successRecord1.RetryCount,
		successRecord1.FirstPushAt, successRecord1.LastPushAt, successRecord1.ErrorMessage)

	assert.Equal(t, MessageSendStatusSuccess, successRecord1.Status)
	assert.NotNil(t, successRecord1.LastPushAt, "成功推送后应有 LastPushAt")
	assert.NotNil(t, successRecord1.FirstPushAt, "成功推送后应有 FirstPushAt")
	assert.Empty(t, successRecord1.ErrorMessage, "成功时应清空错误信息")
	assert.Equal(t, 0, successRecord1.RetryCount, "成功时重试次数应为0")
	t.Logf("✅ 第一条消息：推送成功 (FirstPushAt=%v, LastPushAt=%v)",
		successRecord1.FirstPushAt.Format("15:04:05.000"),
		successRecord1.LastPushAt.Format("15:04:05.000"))

	// 验证第二条：推送失败
	err = tc.db.Where("message_id = ?", msgIDs[1]).First(&failedRecord).Error
	assert.NoError(t, err)
	t.Logf("📊 第二条消息原始数据: ID=%s, Status=%s, RetryCount=%d, FirstPushAt=%v, LastPushAt=%v, Error=%q",
		failedRecord.MessageID, failedRecord.Status, failedRecord.RetryCount,
		failedRecord.FirstPushAt, failedRecord.LastPushAt, failedRecord.ErrorMessage)

	assert.Equal(t, MessageSendStatusFailed, failedRecord.Status)
	assert.Equal(t, "push timeout", failedRecord.ErrorMessage)
	assert.NotNil(t, failedRecord.LastPushAt, "失败推送后应有 LastPushAt")
	assert.NotNil(t, failedRecord.FirstPushAt, "失败推送后应有 FirstPushAt")
	assert.Equal(t, 1, failedRecord.RetryCount, "首次失败重试次数为1")
	t.Logf("✅ 第二条消息：推送失败 (重试次数=%d, 错误=%s)",
		failedRecord.RetryCount, failedRecord.ErrorMessage)

	// 验证第三条：重试后成功
	err = tc.db.Where("message_id = ?", msgIDs[2]).First(&successRecord2).Error
	assert.NoError(t, err)
	t.Logf("📊 第三条消息原始数据: ID=%s, Status=%s, RetryCount=%d, FirstPushAt=%v, LastPushAt=%v, Error=%q",
		successRecord2.MessageID, successRecord2.Status, successRecord2.RetryCount,
		successRecord2.FirstPushAt, successRecord2.LastPushAt, successRecord2.ErrorMessage)

	assert.Equal(t, MessageSendStatusSuccess, successRecord2.Status)
	assert.NotNil(t, successRecord2.FirstPushAt, "重试成功后应保留 FirstPushAt")
	assert.NotNil(t, successRecord2.LastPushAt, "重试成功后应更新 LastPushAt")
	assert.True(t, successRecord2.LastPushAt.After(*successRecord2.FirstPushAt),
		"LastPushAt 应该晚于 FirstPushAt")
	assert.Empty(t, successRecord2.ErrorMessage, "成功后应清空错误信息")
	assert.Equal(t, 1, successRecord2.RetryCount, "重试后成功应保留重试计数")
	t.Logf("✅ 第三条消息：重试后成功 (FirstPushAt=%v, LastPushAt=%v, 重试=%d)",
		successRecord2.FirstPushAt.Format("15:04:05.000"),
		successRecord2.LastPushAt.Format("15:04:05.000"),
		successRecord2.RetryCount)

	// ========== 阶段4: 验证查询过滤 ==========
	// GetByReceiver 不应返回已成功推送的消息
	pendingMessages, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, pendingMessages, 1, "只应返回1条失败的消息")
	assert.Equal(t, msgIDs[1], pendingMessages[0].MessageID, "应该是推送失败的那条")
	t.Logf("✅ 阶段4完成：GetByReceiver 正确过滤，只返回失败的消息")

	t.Log("========== 测试完成：完整验证了离线消息生命周期 ==========")
}

func TestOfflineMessageRepositoryUpdatePushStatusEmptyList(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "mark-empty")

	// 空数组应该不报错
	err := tc.repo.UpdatePushStatus(tc.ctx, []string{}, MessageSendStatusSuccess, "")
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryExpiredMessageNotRetrieved(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "013")
	defer tc.cleanup()

	// 创建已过期的消息
	expiredMsgID := osx.HashUnixMicroCipherText()
	expiredRecord := CreateTestOfflineMessageRecord(expiredMsgID, tc.userID, tc.sessionID)
	expiredRecord.ExpireAt = time.Now().Add(-1 * time.Hour)
	err := tc.repo.Save(tc.ctx, expiredRecord)
	require.NoError(t, err)
	tc.cleanupIDs = append(tc.cleanupIDs, expiredMsgID)

	// 创建未过期的消息
	validMsgID, validRecord := tc.createMessage()
	err = tc.repo.Save(tc.ctx, validRecord)
	require.NoError(t, err)

	// 查询：只应该返回未过期的消息
	records, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1, "只应该返回未过期的消息")
	assert.Equal(t, validMsgID, records[0].MessageID)

	// 统计：也只应该计算未过期的消息
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count, "统计时只应该包含未过期的消息")
}

func TestOfflineMessageRepositoryPushedMessageNotRetrieved(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "014")
	defer tc.cleanup()

	msgIDs, records := tc.createMessages(2)
	tc.saveMessages(records, false)

	// 标记第一条消息为已推送
	err := tc.repo.UpdatePushStatus(tc.ctx, []string{msgIDs[0]}, MessageSendStatusSuccess, "")
	assert.NoError(t, err)

	// GetByReceiver 不应该返回已推送的消息
	result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, msgIDs[1], result[0].MessageID)

	// GetCountByReceiver 也不应该计算已推送的消息
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestOfflineMessageRepositoryConcurrentUpdatePushStatus(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "015")
	defer tc.cleanup()

	concurrency := 10
	msgIDs, records := tc.createMessages(concurrency)
	tc.saveMessages(records, false)

	// 并发标记为已推送
	var wg sync.WaitGroup
	errChan := make(chan error, concurrency)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// 随机成功/失败
			var status MessageSendStatus
			var errMsg string
			if idx%2 == 0 {
				status = MessageSendStatusSuccess
			} else {
				status = MessageSendStatusFailed
				errMsg = fmt.Sprintf("error %d", idx)
			}
			if err := tc.repo.UpdatePushStatus(tc.ctx, []string{msgIDs[idx]}, status, errMsg); err != nil {
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
}

func TestOfflineMessageRepositoryDeleteNonExistentMessage(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "del-non-existent")

	// 删除不存在的消息应该不报错
	err := tc.repo.DeleteByMessageIDs(tc.ctx, tc.userID, []string{"non-existent-id"})
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryGetByReceiverNoResults(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "non-existent")

	// 查询不存在的用户
	records, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 0)
}

func TestOfflineMessageRepositoryGetCountByReceiverZero(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "zero-count")

	// 查询不存在的用户
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestOfflineMessageRepositoryClearNonExistentUser(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "clear-non-existent")

	// 清空不存在的用户应该不报错
	err := tc.repo.ClearByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryBatchUpdatePushStatus(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "016")
	defer tc.cleanup()

	msgIDs, records := tc.createMessages(5)
	tc.saveMessages(records, false)

	// 批量标记前3条为已推送
	err := tc.repo.UpdatePushStatus(tc.ctx, msgIDs[:3], MessageSendStatusSuccess, "")
	assert.NoError(t, err)

	// 验证：只有2条未推送的消息
	result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, result, 2, "应该有2条未推送的消息")

	// 验证未推送的消息ID
	unpushedIDs := []string{result[0].MessageID, result[1].MessageID}
	assert.Contains(t, unpushedIDs, msgIDs[3])
	assert.Contains(t, unpushedIDs, msgIDs[4])
}

func TestOfflineMessageRepositoryBatchSave(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "017")
	defer tc.cleanup()

	batchSize := 100
	_, records := tc.createMessages(batchSize)

	// 批量保存
	err := tc.repo.BatchSave(tc.ctx, records)
	assert.NoError(t, err)

	// 验证保存成功
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(batchSize), count)
}

func TestOfflineMessageRepositoryBatchSaveEmpty(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "batch-empty")

	// 批量保存空数组应该不报错
	err := tc.repo.BatchSave(tc.ctx, []*OfflineMessageRecord{})
	assert.NoError(t, err)
}

func TestOfflineMessageRepositoryGetByReceiverWithCursor(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "018")
	defer tc.cleanup()

	_, records := tc.createMessages(10)
	tc.saveMessages(records, true)

	// 验证总数
	totalCount, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	require.NoError(t, err)
	t.Logf("总消息数: %d", totalCount)
	require.Equal(t, int64(10), totalCount, "应该有10条消息")

	// 第一次查询：获取前5条
	firstBatch, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 5)
	assert.NoError(t, err)
	t.Logf("第一批消息数: %d", len(firstBatch))
	for i, msg := range firstBatch {
		t.Logf("  [%d] MessageID=%s, CreatedAt=%v", i+1, msg.MessageID, msg.CreatedAt)
	}
	assert.Len(t, firstBatch, 5)

	// 使用 cursor 获取后续数据
	cursor := firstBatch[len(firstBatch)-1].MessageID
	t.Logf("使用 cursor: %s, CreatedAt=%v", cursor, firstBatch[len(firstBatch)-1].CreatedAt)
	secondBatch, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 5, cursor)
	assert.NoError(t, err)
	t.Logf("第二批消息数: %d (期望5)", len(secondBatch))
	for i, msg := range secondBatch {
		t.Logf("  [%d] MessageID=%s, CreatedAt=%v", i+1, msg.MessageID, msg.CreatedAt)
	}
	assert.Len(t, secondBatch, 5)

	// 验证两批数据不重复
	firstIDs := make(map[string]bool)
	for _, record := range firstBatch {
		firstIDs[record.MessageID] = true
	}
	for _, record := range secondBatch {
		assert.False(t, firstIDs[record.MessageID], "不应该有重复的消息")
	}
}

func TestOfflineMessageRepositoryGetByReceiverWithInvalidCursor(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "019")
	defer tc.cleanup()

	_, records := tc.createMessages(2)
	tc.saveMessages(records, false)

	// 使用不存在的 cursor
	// 当 cursor 不存在时，子查询返回 NULL，created_at > NULL 为 false，不会返回任何记录
	result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10, "non-existent-cursor")
	assert.NoError(t, err)
	assert.Len(t, result, 0, "不存在的 cursor 应该返回空列表")
}

func TestOfflineMessageRepositoryGetByReceiverWithZeroLimit(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "020")
	defer tc.cleanup()

	_, records := tc.createMessages(2)
	tc.saveMessages(records, false)

	// limit=0 应该返回默认最大值（10000）
	result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 0)
	assert.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestOfflineMessageRepositoryGetByReceiverLargeLimit(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "021")
	defer tc.cleanup()

	_, records := tc.createMessages(2)
	tc.saveMessages(records, false)

	// limit 超过 10000 应该被限制为 10000
	result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 20000)
	assert.NoError(t, err)
	assert.Len(t, result, 2)
}

func TestOfflineMessageRepositoryMultipleSessionMessages(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	userID := "user-022"
	session1 := "session-022-1"
	session2 := "session-022-2"

	// 创建两个会话的消息
	session1IDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}
	session2IDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	allIDs := append(session1IDs, session2IDs...)

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, allIDs)
	}()

	// 保存会话1的消息
	for _, msgID := range session1IDs {
		record := CreateTestOfflineMessageRecord(msgID, userID, session1)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 保存会话2的消息
	for _, msgID := range session2IDs {
		record := CreateTestOfflineMessageRecord(msgID, userID, session2)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 查询用户的所有离线消息
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 4, "应该返回两个会话的所有消息")

	// 验证包含不同会话的消息
	sessions := make(map[string]int)
	for _, record := range records {
		sessions[record.SessionID]++
	}
	assert.Equal(t, 2, sessions[session1])
	assert.Equal(t, 2, sessions[session2])
}

func TestOfflineMessageRepositoryMultipleSendersToSameReceiver(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	userID := "user-023"
	sessionID := "session-023"
	sender1 := "sender-023-1"
	sender2 := "sender-023-2"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// sender1 发送2条消息
	for i := 0; i < 2; i++ {
		record := CreateTestOfflineMessageRecord(messageIDs[i], userID, sessionID)
		record.Sender = sender1
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// sender2 发送1条消息
	record := CreateTestOfflineMessageRecord(messageIDs[2], userID, sessionID)
	record.Sender = sender2
	err := repo.Save(ctx, record)
	require.NoError(t, err)

	// 查询接收者的消息
	receiverRecords, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, receiverRecords, 3)

	// 查询发送者1的消息
	sender1Records, err := repo.GetBySender(ctx, sender1, 10)
	assert.NoError(t, err)
	assert.Len(t, sender1Records, 2)

	// 查询发送者2的消息
	sender2Records, err := repo.GetBySender(ctx, sender2, 10)
	assert.NoError(t, err)
	assert.Len(t, sender2Records, 1)
}

func TestOfflineMessageRepositoryExpireAtBoundary(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	userID := "user-024"
	sessionID := "session-024"

	// 创建刚好在过期边界的消息
	almostExpiredID := osx.HashUnixMicroCipherText()
	almostExpired := CreateTestOfflineMessageRecord(almostExpiredID, userID, sessionID)
	almostExpired.ExpireAt = time.Now().Add(1 * time.Second) // 1秒后过期
	err := repo.Save(ctx, almostExpired)
	require.NoError(t, err)

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, []string{almostExpiredID})
	}()

	// 立即查询，应该能查到
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1)

	// 等待消息过期
	time.Sleep(2 * time.Second)

	// 再次查询，应该查不到
	expiredRecords, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	assert.Len(t, expiredRecords, 0)
}

func TestOfflineMessageRepositoryConcurrentDeleteAndQuery(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	userID := "user-025"
	sessionID := "session-025"

	messageIDs := make([]string, 10)
	for i := 0; i < 10; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 清理测试数据
	defer func() {
		_ = repo.ClearByReceiver(ctx, userID)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := CreateTestOfflineMessageRecord(msgID, userID, sessionID)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 并发删除和查询
	var wg sync.WaitGroup
	errChan := make(chan error, 10)

	// 5个协程并发删除
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := repo.DeleteByMessageIDs(ctx, userID, []string{messageIDs[idx]}); err != nil {
				errChan <- err
			}
		}(i)
	}

	// 5个协程并发查询
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := repo.GetByReceiver(ctx, userID, 10); err != nil {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)

	// 检查是否有错误
	for err := range errChan {
		assert.NoError(t, err)
	}

	// 验证最终状态：应该有5条消息
	finalCount, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), finalCount)
}

func TestOfflineMessageRepositoryGetBySenderWithLimit(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	senderID := "sender-026"
	userID := "user-026"
	sessionID := "session-026"

	// 创建10条消息
	messageIDs := make([]string, 10)
	for i := 0; i < 10; i++ {
		messageIDs[i] = osx.HashUnixMicroCipherText()
	}

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, messageIDs)
	}()

	// 保存测试记录
	for _, msgID := range messageIDs {
		record := CreateTestOfflineMessageRecord(msgID, userID, sessionID)
		record.Sender = senderID
		err := repo.Save(ctx, record)
		require.NoError(t, err)
		time.Sleep(10 * time.Millisecond)
	}

	// 限制只获取5条
	records, err := repo.GetBySender(ctx, senderID, 5)
	assert.NoError(t, err)
	assert.Len(t, records, 5, "应该只返回5条记录")

	// 验证按时间升序排列
	for i := 0; i < len(records)-1; i++ {
		assert.True(t, records[i].CreatedAt.Before(records[i+1].CreatedAt) || records[i].CreatedAt.Equal(records[i+1].CreatedAt))
	}
}

func TestOfflineMessageRepositoryDeleteExpiredBatchProcessing(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	userID := "user-027"
	sessionID := "session-027"

	// 创建多条已过期的消息
	expiredCount := 5
	expiredIDs := make([]string, expiredCount)
	for i := 0; i < expiredCount; i++ {
		expiredIDs[i] = osx.HashUnixMicroCipherText()
		record := CreateTestOfflineMessageRecord(expiredIDs[i], userID, sessionID)
		record.ExpireAt = time.Now().Add(-1 * time.Hour)
		err := repo.Save(ctx, record)
		require.NoError(t, err)
	}

	// 创建未过期的消息
	validID := osx.HashUnixMicroCipherText()
	validRecord := CreateTestOfflineMessageRecord(validID, userID, sessionID)
	err := repo.Save(ctx, validRecord)
	require.NoError(t, err)

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, []string{validID})
	}()

	// 删除过期消息
	deletedCount, err := repo.DeleteExpired(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, deletedCount, int64(expiredCount))

	// 验证未过期的消息仍然存在
	count, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestOfflineMessageRepositorySaveWithCustomExpireTime(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	userID := "user-028"
	sessionID := "session-028"
	messageID := osx.HashUnixMicroCipherText()

	// 清理测试数据
	defer func() {
		_ = repo.DeleteByMessageIDs(ctx, userID, []string{messageID})
	}()

	// 创建自定义过期时间的消息（1小时后过期）
	record := CreateTestOfflineMessageRecord(messageID, userID, sessionID)
	customExpireAt := time.Now().Add(1 * time.Hour)
	record.ExpireAt = customExpireAt

	err := repo.Save(ctx, record)
	assert.NoError(t, err)

	// 查询并验证过期时间
	records, err := repo.GetByReceiver(ctx, userID, 10)
	assert.NoError(t, err)
	require.Len(t, records, 1)
	assert.WithinDuration(t, customExpireAt, records[0].ExpireAt, time.Second)
}

func TestOfflineMessageRepositoryClearByReceiverWithMultipleSenders(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())
	ctx := context.Background()

	userID := "user-029"
	sessionID := "session-029"
	sender1 := "sender-029-1"
	sender2 := "sender-029-2"

	messageIDs := []string{
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
		osx.HashUnixMicroCipherText(),
	}

	// 保存来自不同发送者的消息
	record1 := CreateTestOfflineMessageRecord(messageIDs[0], userID, sessionID)
	record1.Sender = sender1
	err := repo.Save(ctx, record1)
	require.NoError(t, err)

	record2 := CreateTestOfflineMessageRecord(messageIDs[1], userID, sessionID)
	record2.Sender = sender2
	err = repo.Save(ctx, record2)
	require.NoError(t, err)

	record3 := CreateTestOfflineMessageRecord(messageIDs[2], userID, sessionID)
	record3.Sender = sender1
	err = repo.Save(ctx, record3)
	require.NoError(t, err)

	// 验证消息存在
	beforeClear, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), beforeClear)

	// 清空该接收者的所有消息
	err = repo.ClearByReceiver(ctx, userID)
	assert.NoError(t, err)

	// 验证清空
	afterClear, err := repo.GetCountByReceiver(ctx, userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), afterClear)

	// 验证发送者视角也看不到这些消息
	sender1Count, err := repo.GetCountBySender(ctx, sender1)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), sender1Count)

	sender2Count, err := repo.GetCountBySender(ctx, sender2)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), sender2Count)
}

// ============================================================================
// 增强测试 - 边界条件、性能、可靠性
// ============================================================================

// TestOfflineMessageRepositoryLargeMessageContent 测试大消息内容处理
func TestOfflineMessageRepositoryLargeMessageContent(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "large-msg")
	defer tc.cleanup()

	// 创建一个包含大量数据的消息（1MB）
	largeData := make(map[string]interface{})
	largeData["content"] = string(make([]byte, 1024*1024)) // 1MB 数据

	msgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, msgID)

	now := time.Now()
	hubMsg := &HubMessage{
		ID:           msgID,
		MessageType:  MessageTypeText,
		Sender:       "sender-large",
		SenderType:   UserTypeCustomer,
		Receiver:     tc.userID,
		ReceiverType: UserTypeAgent,
		SessionID:    tc.sessionID,
		Content:      "Large message test",
		Data:         largeData,
		CreateAt:     now,
	}

	compressedData, _, err := zipx.ZlibCompressObjectWithSize(hubMsg)
	require.NoError(t, err)

	record := &OfflineMessageRecord{
		MessageID:      msgID,
		Receiver:       tc.userID,
		SessionID:      tc.sessionID,
		CompressedData: compressedData,
		ScheduledAt:    now,
		ExpireAt:       now.Add(7 * 24 * time.Hour),
		CreatedAt:      now,
	}

	// 保存应该成功（压缩后会小很多）
	err = tc.repo.Save(tc.ctx, record)
	assert.NoError(t, err)

	// 验证可以正常检索
	records, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1)
	assert.Equal(t, msgID, records[0].MessageID)
}

// TestOfflineMessageRepositoryEmptyMessageContent 测试空消息内容
func TestOfflineMessageRepositoryEmptyMessageContent(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "empty-msg")
	defer tc.cleanup()

	msgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, msgID)

	now := time.Now()
	hubMsg := &HubMessage{
		ID:          msgID,
		MessageType: MessageTypeText,
		Sender:      "sender-empty",
		Receiver:    tc.userID,
		SessionID:   tc.sessionID,
		Content:     "",
		Data:        map[string]interface{}{},
		CreateAt:    now,
	}

	compressedData, _, err := zipx.ZlibCompressObjectWithSize(hubMsg)
	require.NoError(t, err)

	record := &OfflineMessageRecord{
		MessageID:      msgID,
		Receiver:       tc.userID,
		SessionID:      tc.sessionID,
		CompressedData: compressedData,
		ScheduledAt:    now,
		ExpireAt:       now.Add(7 * 24 * time.Hour),
		CreatedAt:      now,
	}

	err = tc.repo.Save(tc.ctx, record)
	assert.NoError(t, err)

	records, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
	assert.NoError(t, err)
	assert.Len(t, records, 1)
}

// TestOfflineMessageRepositorySpecialCharactersInUserID 测试特殊字符用户ID
func TestOfflineMessageRepositorySpecialCharactersInUserID(t *testing.T) {
	specialUserIDs := []string{
		"user@example.com",
		"user-with-dash",
		"user_with_underscore",
		"user.with.dot",
		"user+tag@example.com",
		"用户中文名",
		"ユーザー日本語",
	}

	for _, userID := range specialUserIDs {
		t.Run(userID, func(t *testing.T) {
			repo := NewGormOfflineMessageRepository(getTestOfflineDB(t),nil,NewDefaultWSCLogger())
			ctx := context.Background()

			msgID := osx.HashUnixMicroCipherText()
			record := CreateTestOfflineMessageRecord(msgID, userID, "session-special")

			// 清理测试数据
			defer func() {
				_ = repo.DeleteByMessageIDs(ctx, userID, []string{msgID})
			}()

			err := repo.Save(ctx, record)
			assert.NoError(t, err)

			// 验证可以正确检索
			records, err := repo.GetByReceiver(ctx, userID, 10)
			assert.NoError(t, err)
			assert.Len(t, records, 1)
			assert.Equal(t, userID, records[0].Receiver)
		})
	}
}

// TestOfflineMessageRepositoryMaxLimitBoundary 测试limit边界值
func TestOfflineMessageRepositoryMaxLimitBoundary(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "limit-boundary")
	defer tc.cleanup()

	// 创建10条消息
	_, records := tc.createMessages(10)
	tc.saveMessages(records, false)

	testCases := []struct {
		name          string
		limit         int
		expectedCount int
	}{
		{"零limit应使用默认值", 0, 10},
		{"负数limit应使用默认值", -1, 10},
		{"正常limit", 5, 5},
		{"超大limit应被限制", 20000, 10},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			records, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, testCase.limit)
			assert.NoError(t, err)
			assert.LessOrEqual(t, len(records), testCase.expectedCount)
		})
	}
}

// TestOfflineMessageRepositoryConcurrentReadWrite 测试并发读写
func TestOfflineMessageRepositoryConcurrentReadWrite(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "concurrent-rw")
	defer tc.cleanup()

	const (
		writers      = 5
		readers      = 10
		msgPerWriter = 10
	)

	var wg sync.WaitGroup
	errChan := make(chan error, writers+readers)

	// 启动写入协程
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < msgPerWriter; i++ {
				msgID := osx.HashUnixMicroCipherText()
				record := CreateTestOfflineMessageRecord(msgID, tc.userID, tc.sessionID)
				if err := tc.repo.Save(tc.ctx, record); err != nil {
					errChan <- fmt.Errorf("writer %d: %w", writerID, err)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(w)
	}

	// 启动读取协程
	for r := 0; r < readers; r++ {
		wg.Add(1)
		go func(readerID int) {
			defer wg.Done()
			for i := 0; i < 5; i++ {
				if _, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10); err != nil {
					errChan <- fmt.Errorf("reader %d: %w", readerID, err)
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}(r)
	}

	wg.Wait()
	close(errChan)

	// 验证没有错误
	for err := range errChan {
		t.Errorf("并发操作错误: %v", err)
	}

	// 验证最终数据一致性
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(writers*msgPerWriter), count)
}

// TestOfflineMessageRepositoryConcurrentDelete 测试并发删除
func TestOfflineMessageRepositoryConcurrentDelete(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "concurrent-del")
	defer tc.cleanup()

	// 创建50条消息
	msgIDs, records := tc.createMessages(50)
	tc.saveMessages(records, false)

	// 并发删除，每个协程删除5条
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		start := i * 5
		end := start + 5
		go func(ids []string) {
			defer wg.Done()
			_ = tc.repo.DeleteByMessageIDs(tc.ctx, tc.userID, ids)
		}(msgIDs[start:end])
	}

	wg.Wait()

	// 验证所有消息都被删除
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestOfflineMessageRepositoryIdempotentOperations 测试幂等性操作
func TestOfflineMessageRepositoryIdempotentOperations(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "idempotent")
	defer tc.cleanup()

	msgID, record := tc.createMessage()
	err := tc.repo.Save(tc.ctx, record)
	require.NoError(t, err)

	// 多次标记为已推送应该是幂等的
	for i := 0; i < 5; i++ {
		err := tc.repo.UpdatePushStatus(tc.ctx, []string{msgID}, MessageSendStatusSuccess, "")
		assert.NoError(t, err)
	}

	// 验证结果一致
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)

	// 多次删除不存在的消息应该不报错
	for i := 0; i < 3; i++ {
		err := tc.repo.DeleteByMessageIDs(tc.ctx, tc.userID, []string{"non-existent-id"})
		assert.NoError(t, err)
	}
}

// TestOfflineMessageRepositoryCleanupOld 测试清理旧记录
func TestOfflineMessageRepositoryCleanupOld(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "cleanup-old")
	defer tc.cleanup()

	now := time.Now()

	// 创建3条旧的已成功推送的消息（10天前）
	oldSuccessMsgIDs := make([]string, 3)
	for i := 0; i < 3; i++ {
		msgID := osx.HashUnixMicroCipherText()
		oldSuccessMsgIDs[i] = msgID
		tc.cleanupIDs = append(tc.cleanupIDs, msgID)

		record := CreateTestOfflineMessageRecord(msgID, tc.userID, tc.sessionID)
		record.Status = MessageSendStatusSuccess
		record.CreatedAt = now.AddDate(0, 0, -10)
		err := tc.db.Create(record).Error
		require.NoError(t, err)
	}

	// 创建2条旧的已过期消息（10天前）
	oldExpiredMsgIDs := make([]string, 2)
	for i := 0; i < 2; i++ {
		msgID := osx.HashUnixMicroCipherText()
		oldExpiredMsgIDs[i] = msgID
		tc.cleanupIDs = append(tc.cleanupIDs, msgID)

		record := CreateTestOfflineMessageRecord(msgID, tc.userID, tc.sessionID)
		record.Status = MessageSendStatusUserOffline
		record.ExpireAt = now.Add(-1 * time.Hour)
		record.CreatedAt = now.AddDate(0, 0, -10)
		err := tc.db.Create(record).Error
		require.NoError(t, err)
	}

	// 创建1条旧的待推送消息（10天前，未过期）
	oldPendingMsgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, oldPendingMsgID)
	oldPendingRecord := CreateTestOfflineMessageRecord(oldPendingMsgID, tc.userID, tc.sessionID)
	oldPendingRecord.Status = MessageSendStatusUserOffline
	oldPendingRecord.CreatedAt = now.AddDate(0, 0, -10)
	oldPendingRecord.ExpireAt = now.Add(24 * time.Hour)
	err := tc.db.Create(oldPendingRecord).Error
	require.NoError(t, err)

	// 创建1条新的已成功推送的消息（1天前）
	newSuccessMsgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, newSuccessMsgID)
	newSuccessRecord := CreateTestOfflineMessageRecord(newSuccessMsgID, tc.userID, tc.sessionID)
	newSuccessRecord.Status = MessageSendStatusSuccess
	newSuccessRecord.CreatedAt = now.AddDate(0, 0, -1)
	err = tc.db.Create(newSuccessRecord).Error
	require.NoError(t, err)

	// 执行清理：清理7天前的数据
	before := now.AddDate(0, 0, -7)
	deletedCount, err := tc.repo.CleanupOld(tc.ctx, before)
	assert.NoError(t, err)
	assert.Equal(t, int64(5), deletedCount, "应该删除3条已成功+2条已过期的消息")

	// 验证剩余消息
	var remainingRecords []OfflineMessageRecord
	err = tc.db.Where("receiver = ?", tc.userID).Find(&remainingRecords).Error
	assert.NoError(t, err)
	assert.Len(t, remainingRecords, 2, "应该剩余2条消息")

	// 验证剩余的是待推送和新成功的消息
	remainingIDs := make(map[string]bool)
	for _, record := range remainingRecords {
		remainingIDs[record.MessageID] = true
	}
	assert.True(t, remainingIDs[oldPendingMsgID], "旧的待推送消息应该保留")
	assert.True(t, remainingIDs[newSuccessMsgID], "新的成功消息应该保留")
}

// TestOfflineMessageRepositoryCleanupOldEmptyResult 测试清理旧记录无数据情况
func TestOfflineMessageRepositoryCleanupOldEmptyResult(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "cleanup-empty")
	defer tc.cleanup()

	// 创建1条新消息
	_, record := tc.createMessage()
	err := tc.repo.Save(tc.ctx, record)
	require.NoError(t, err)

	// 清理10天前的数据，应该没有删除
	before := time.Now().AddDate(0, 0, -10)
	deletedCount, err := tc.repo.CleanupOld(tc.ctx, before)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), deletedCount)

	// 验证消息仍然存在
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

// TestOfflineMessageRepositoryCleanupOldOnlySuccess 测试只清理已成功的旧消息
func TestOfflineMessageRepositoryCleanupOldOnlySuccess(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "cleanup-success")
	defer tc.cleanup()

	now := time.Now()

	// 创建旧的已成功消息
	successMsgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, successMsgID)
	successRecord := CreateTestOfflineMessageRecord(successMsgID, tc.userID, tc.sessionID)
	successRecord.Status = MessageSendStatusSuccess
	successRecord.CreatedAt = now.AddDate(0, 0, -10)
	err := tc.db.Create(successRecord).Error
	require.NoError(t, err)

	// 创建旧的失败消息（未过期）
	failedMsgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, failedMsgID)
	failedRecord := CreateTestOfflineMessageRecord(failedMsgID, tc.userID, tc.sessionID)
	failedRecord.Status = MessageSendStatusFailed
	failedRecord.CreatedAt = now.AddDate(0, 0, -10)
	failedRecord.ExpireAt = now.Add(24 * time.Hour)
	err = tc.db.Create(failedRecord).Error
	require.NoError(t, err)

	// 清理7天前的数据
	before := now.AddDate(0, 0, -7)
	deletedCount, err := tc.repo.CleanupOld(tc.ctx, before)
	assert.NoError(t, err)
	assert.Equal(t, int64(1), deletedCount, "只应该删除已成功的消息")

	// 验证失败的消息仍然存在
	var remainingRecord OfflineMessageRecord
	err = tc.db.Where("message_id = ?", failedMsgID).First(&remainingRecord).Error
	assert.NoError(t, err)
	assert.Equal(t, MessageSendStatusFailed, remainingRecord.Status)
}

// TestOfflineMessageRepositoryCleanupOldWithExpired 测试同时清理旧记录和过期消息
func TestOfflineMessageRepositoryCleanupOldWithExpired(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "cleanup-mixed")
	defer tc.cleanup()

	now := time.Now()

	// 创建旧的已成功消息
	oldSuccessMsgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, oldSuccessMsgID)
	oldSuccessRecord := CreateTestOfflineMessageRecord(oldSuccessMsgID, tc.userID, tc.sessionID)
	oldSuccessRecord.Status = MessageSendStatusSuccess
	oldSuccessRecord.CreatedAt = now.AddDate(0, 0, -10)
	err := tc.db.Create(oldSuccessRecord).Error
	require.NoError(t, err)

	// 创建新的已过期消息
	newExpiredMsgID := osx.HashUnixMicroCipherText()
	tc.cleanupIDs = append(tc.cleanupIDs, newExpiredMsgID)
	newExpiredRecord := CreateTestOfflineMessageRecord(newExpiredMsgID, tc.userID, tc.sessionID)
	newExpiredRecord.Status = MessageSendStatusUserOffline
	newExpiredRecord.ExpireAt = now.Add(-1 * time.Hour)
	newExpiredRecord.CreatedAt = now.AddDate(0, 0, -1)
	err = tc.db.Create(newExpiredRecord).Error
	require.NoError(t, err)

	// 清理7天前的数据
	before := now.AddDate(0, 0, -7)
	deletedCount, err := tc.repo.CleanupOld(tc.ctx, before)
	assert.NoError(t, err)
	// 旧成功消息(10天前)会被删除，新过期消息(1天前)也会被删除
	assert.GreaterOrEqual(t, deletedCount, int64(1), "应该至少删除1条消息")

	// 验证都被删除
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

// TestOfflineMessageRepositoryClose 测试关闭仓库
func TestOfflineMessageRepositoryClose(t *testing.T) {
	db := getTestOfflineDB(t)
	repo := NewGormOfflineMessageRepository(db, nil, NewDefaultWSCLogger())

	// 关闭应该不报错
	err := repo.Close()
	assert.NoError(t, err)

	// 多次关闭应该不报错
	err = repo.Close()
	assert.NoError(t, err)
}

// TestOfflineMessageRepositoryAutoCleanupDisabled 测试禁用自动清理
func TestOfflineMessageRepositoryAutoCleanupDisabled(t *testing.T) {
	db := getTestOfflineDB(t)

	// 创建配置：禁用自动清理
	config := &wscconfig.OfflineMessage{
		EnableAutoCleanup: false,
		CleanupDaysAgo:    7,
	}

	repo := NewGormOfflineMessageRepository(db, config, NewDefaultWSCLogger())
	defer repo.Close()

	// 仓库应该正常工作
	ctx := context.Background()
	userID := "user-no-cleanup"
	msgID := osx.HashUnixMicroCipherText()
	record := CreateTestOfflineMessageRecord(msgID, userID, "session-no-cleanup")

	err := repo.Save(ctx, record)
	assert.NoError(t, err)

	// 清理
	_ = repo.DeleteByMessageIDs(ctx, userID, []string{msgID})
}

// TestOfflineMessageRepositoryAutoCleanupZeroDays 测试自动清理天数为0
func TestOfflineMessageRepositoryAutoCleanupZeroDays(t *testing.T) {
	db := getTestOfflineDB(t)

	// 创建配置：清理天数为0
	config := &wscconfig.OfflineMessage{
		EnableAutoCleanup: true,
		CleanupDaysAgo:    0,
	}

	repo := NewGormOfflineMessageRepository(db, config, NewDefaultWSCLogger())
	defer repo.Close()

	// 仓库应该正常工作，但不会执行清理
	ctx := context.Background()
	userID := "user-zero-days"
	msgID := osx.HashUnixMicroCipherText()
	record := CreateTestOfflineMessageRecord(msgID, userID, "session-zero-days")

	err := repo.Save(ctx, record)
	assert.NoError(t, err)

	// 清理
	_ = repo.DeleteByMessageIDs(ctx, userID, []string{msgID})
}

// TestOfflineMessageRepositoryRetryCountIncrement 测试重试次数递增
func TestOfflineMessageRepositoryRetryCountIncrement(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "retry-count")
	defer tc.cleanup()

	msgID, record := tc.createMessage()
	err := tc.repo.Save(tc.ctx, record)
	require.NoError(t, err)

	// 第一次失败
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgID}, MessageSendStatusFailed, "error 1")
	assert.NoError(t, err)

	var record1 OfflineMessageRecord
	err = tc.db.Where("message_id = ?", msgID).First(&record1).Error
	assert.NoError(t, err)
	assert.Equal(t, 1, record1.RetryCount)

	// 第二次失败
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgID}, MessageSendStatusFailed, "error 2")
	assert.NoError(t, err)

	var record2 OfflineMessageRecord
	err = tc.db.Where("message_id = ?", msgID).First(&record2).Error
	assert.NoError(t, err)
	assert.Equal(t, 2, record2.RetryCount)

	// 第三次失败
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgID}, MessageSendStatusFailed, "error 3")
	assert.NoError(t, err)

	var record3 OfflineMessageRecord
	err = tc.db.Where("message_id = ?", msgID).First(&record3).Error
	assert.NoError(t, err)
	assert.Equal(t, 3, record3.RetryCount)
	assert.Equal(t, "error 3", record3.ErrorMessage)
}

// TestOfflineMessageRepositoryFirstPushAtOnlySetOnce 测试FirstPushAt只设置一次
func TestOfflineMessageRepositoryFirstPushAtOnlySetOnce(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "first-push-once")
	defer tc.cleanup()

	msgID, record := tc.createMessage()
	err := tc.repo.Save(tc.ctx, record)
	require.NoError(t, err)

	// 第一次推送失败
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgID}, MessageSendStatusFailed, "first error")
	assert.NoError(t, err)

	var record1 OfflineMessageRecord
	err = tc.db.Where("message_id = ?", msgID).First(&record1).Error
	assert.NoError(t, err)
	assert.NotNil(t, record1.FirstPushAt)
	firstPushTime := *record1.FirstPushAt

	time.Sleep(100 * time.Millisecond)

	// 第二次推送失败
	err = tc.repo.UpdatePushStatus(tc.ctx, []string{msgID}, MessageSendStatusFailed, "second error")
	assert.NoError(t, err)

	var record2 OfflineMessageRecord
	err = tc.db.Where("message_id = ?", msgID).First(&record2).Error
	assert.NoError(t, err)
	assert.NotNil(t, record2.FirstPushAt)
	assert.Equal(t, firstPushTime.Unix(), record2.FirstPushAt.Unix(), "FirstPushAt不应该改变")
	assert.True(t, record2.LastPushAt.After(firstPushTime), "LastPushAt应该更新")
}

// TestOfflineMessageRepositoryBatchSaveLargeDataset 测试批量保存大数据集
func TestOfflineMessageRepositoryBatchSaveLargeDataset(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "batch-large")
	defer tc.cleanup()

	// 创建2500条消息（超过单批1000的限制）
	batchSize := 2500
	_, records := tc.createMessages(batchSize)

	// 批量保存
	err := tc.repo.BatchSave(tc.ctx, records)
	assert.NoError(t, err)

	// 验证保存成功
	count, err := tc.repo.GetCountByReceiver(tc.ctx, tc.userID)
	assert.NoError(t, err)
	assert.Equal(t, int64(batchSize), count)
}

// TestOfflineMessageRepositoryGetByReceiverOrderConsistency 测试查询结果顺序一致性
func TestOfflineMessageRepositoryGetByReceiverOrderConsistency(t *testing.T) {
	tc := newTestOfflineRepoContext(t, "order-consistency")
	defer tc.cleanup()

	// 创建10条消息
	_, records := tc.createMessages(10)
	tc.saveMessages(records, true)

	// 多次查询，验证顺序一致
	var previousOrder []string
	for i := 0; i < 5; i++ {
		result, err := tc.repo.GetByReceiver(tc.ctx, tc.userID, 10)
		assert.NoError(t, err)
		assert.Len(t, result, 10)

		currentOrder := make([]string, len(result))
		for j, record := range result {
			currentOrder[j] = record.MessageID
		}

		if i > 0 {
			assert.Equal(t, previousOrder, currentOrder, "查询顺序应该保持一致")
		}
		previousOrder = currentOrder
	}
}

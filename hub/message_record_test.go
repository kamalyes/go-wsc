/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 01:07:50
 * @FilePath: \go-wsc\hub\message_record_test.go
 * @Description: Hub 消息记录查询和管理白盒单元测试（覆盖 hub/message_record.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
)

// fakeMessageRecordRepo MessageRecordRepository 的内存 fake 实现
type fakeMessageRecordRepo struct {
	findByMessageIDResult *models.MessageSendRecord
	findByMessageIDErr    error
	queryResult           []*models.MessageSendRecord
	queryErr              error
	retryableResult       []*models.MessageSendRecord
	retryableErr          error
	updateStatusErr       error
	updateErr             error
	deleteErr             error
	deleteByMessageIDErr  error
	batchUpdateErr        error
	batchUpdateBlock      func() // optional: if set, BatchUpdateStatus blocks by calling this (用于测试队列满场景)
	deleteExpiredCount    int64
	deleteExpiredErr      error

	lastFilter         *repository.MessageRecordFilter
	lastUpdateStatusID string
	lastUpdateStatus   models.MessageSendStatus
	lastUpdateRecord   *models.MessageSendRecord
	lastDeletedID      uint
	lastDeletedMessage string
	lastRetryableLimit int
	batchUpdateCalls   []batchUpdateCall
	createdRecords     []*models.MessageSendRecord
	batchUpdateMu      sync.Mutex
}

// batchUpdateCall 记录一次 BatchUpdateStatus 调用参数
type batchUpdateCall struct {
	IDs    []string
	Status models.MessageSendStatus
	Reason models.FailureReason
	ErrMsg string
}

func (f *fakeMessageRecordRepo) Create(_ context.Context, record *models.MessageSendRecord) error {
	f.batchUpdateMu.Lock()
	defer f.batchUpdateMu.Unlock()
	f.createdRecords = append(f.createdRecords, record)
	return nil
}
func (f *fakeMessageRecordRepo) Update(_ context.Context, record *models.MessageSendRecord) error {
	f.lastUpdateRecord = record
	return f.updateErr
}
func (f *fakeMessageRecordRepo) FindByID(_ context.Context, _ uint) (*models.MessageSendRecord, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepo) FindByMessageID(_ context.Context, _ string) (*models.MessageSendRecord, error) {
	return f.findByMessageIDResult, f.findByMessageIDErr
}
func (f *fakeMessageRecordRepo) QueryRecords(_ context.Context, filter *repository.MessageRecordFilter) ([]*models.MessageSendRecord, error) {
	f.lastFilter = filter
	return f.queryResult, f.queryErr
}
func (f *fakeMessageRecordRepo) FindRetryable(_ context.Context, limit int) ([]*models.MessageSendRecord, error) {
	f.lastRetryableLimit = limit
	return f.retryableResult, f.retryableErr
}
func (f *fakeMessageRecordRepo) DeleteExpired(_ context.Context) (int64, error) {
	return f.deleteExpiredCount, f.deleteExpiredErr
}
func (f *fakeMessageRecordRepo) Delete(_ context.Context, id uint) error {
	f.lastDeletedID = id
	return f.deleteErr
}
func (f *fakeMessageRecordRepo) DeleteByMessageID(_ context.Context, messageID string) error {
	f.lastDeletedMessage = messageID
	return f.deleteByMessageIDErr
}
func (f *fakeMessageRecordRepo) UpdateStatus(_ context.Context, messageID string, status models.MessageSendStatus, _ models.FailureReason, _ string) error {
	f.batchUpdateMu.Lock()
	f.lastUpdateStatusID = messageID
	f.lastUpdateStatus = status
	f.batchUpdateMu.Unlock()
	return f.updateStatusErr
}
func (f *fakeMessageRecordRepo) BatchUpdateStatus(_ context.Context, ids []string, status models.MessageSendStatus, reason models.FailureReason, errMsg string) error {
	f.batchUpdateMu.Lock()
	f.batchUpdateCalls = append(f.batchUpdateCalls, batchUpdateCall{
		IDs:    append([]string(nil), ids...),
		Status: status,
		Reason: reason,
		ErrMsg: errMsg,
	})
	f.batchUpdateMu.Unlock()
	if f.batchUpdateBlock != nil {
		f.batchUpdateBlock()
	}
	return f.batchUpdateErr
}
func (f *fakeMessageRecordRepo) IncrementRetry(_ context.Context, _ string, _ models.RetryAttempt) error {
	return nil
}
func (f *fakeMessageRecordRepo) GetStatistics(_ context.Context) (map[string]int64, error) {
	return nil, nil
}
func (f *fakeMessageRecordRepo) CleanupOld(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeMessageRecordRepo) GetDB() *gorm.DB { return nil }
func (f *fakeMessageRecordRepo) Close() error    { return nil }

// ============================================================================
// nil repository 分支测试
// ============================================================================

// TestMessageRecord_NoRepo 验证所有方法在 repository 未设置时返回 ErrRecordRepositoryNotSet
func TestMessageRecord_NoRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("QueryMessageRecord", func(t *testing.T) {
		_, err := hub.QueryMessageRecord(ctx, "m1")
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("QueryMessageRecordsBySender", func(t *testing.T) {
		_, err := hub.QueryMessageRecordsBySender(ctx, "s1", 10)
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("QueryMessageRecordsByReceiver", func(t *testing.T) {
		_, err := hub.QueryMessageRecordsByReceiver(ctx, "r1", 10)
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("QueryMessageRecordsByNodeIP", func(t *testing.T) {
		_, err := hub.QueryMessageRecordsByNodeIP(ctx, "1.1.1.1", 10)
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("QueryMessageRecordsByClientIP", func(t *testing.T) {
		_, err := hub.QueryMessageRecordsByClientIP(ctx, "2.2.2.2", 10)
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("QueryMessageRecordsByStatus", func(t *testing.T) {
		_, err := hub.QueryMessageRecordsByStatus(ctx, models.MessageSendStatusSuccess, 10)
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("QueryRetryableMessageRecords", func(t *testing.T) {
		_, err := hub.QueryRetryableMessageRecords(ctx, 10)
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("UpdateMessageRecordStatus", func(t *testing.T) {
		err := hub.UpdateMessageRecordStatus(ctx, "m1", models.MessageSendStatusSuccess, models.FailureReasonUnknown, "")
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("UpdateMessageRecord", func(t *testing.T) {
		err := hub.UpdateMessageRecord(ctx, &models.MessageSendRecord{})
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("DeleteMessageRecord", func(t *testing.T) {
		err := hub.DeleteMessageRecord(ctx, 1)
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
	t.Run("DeleteMessageRecordByMessageID", func(t *testing.T) {
		err := hub.DeleteMessageRecordByMessageID(ctx, "m1")
		assert.Equal(t, ErrRecordRepositoryNotSet, err)
	})
}

// ============================================================================
// 非 nil repository 分支测试
// ============================================================================

// TestQueryMessageRecord_WithRepo 验证查询消息记录
func TestQueryMessageRecord_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	expected := &models.MessageSendRecord{MessageID: "m1"}
	repo := &fakeMessageRecordRepo{findByMessageIDResult: expected}
	hub.SetMessageRecordRepository(repo)

	got, err := hub.QueryMessageRecord(ctx, "m1")
	require.NoError(t, err)
	assert.Same(t, expected, got)
}

// TestQueryMessageRecordsBySender_WithRepo 验证按发送者查询
func TestQueryMessageRecordsBySender_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{queryResult: []*models.MessageSendRecord{{MessageID: "m1"}}}
	hub.SetMessageRecordRepository(repo)

	got, err := hub.QueryMessageRecordsBySender(ctx, "sender1", 5)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, "sender1", repo.lastFilter.Sender)
	assert.Equal(t, 5, repo.lastFilter.Limit)
	assert.True(t, repo.lastFilter.OrderDesc)
}

// TestQueryMessageRecordsByReceiver_WithRepo 验证按接收者查询
func TestQueryMessageRecordsByReceiver_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	_, err := hub.QueryMessageRecordsByReceiver(ctx, "receiver1", 3)
	require.NoError(t, err)
	assert.Equal(t, "receiver1", repo.lastFilter.Receiver)
	assert.Equal(t, 3, repo.lastFilter.Limit)
}

// TestQueryMessageRecordsByNodeIP_WithRepo 验证按节点IP查询
func TestQueryMessageRecordsByNodeIP_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	_, err := hub.QueryMessageRecordsByNodeIP(ctx, "10.0.0.1", 8)
	require.NoError(t, err)
	assert.Equal(t, "10.0.0.1", repo.lastFilter.NodeIP)
	assert.Equal(t, 8, repo.lastFilter.Limit)
}

// TestQueryMessageRecordsByClientIP_WithRepo 验证按客户端IP查询
func TestQueryMessageRecordsByClientIP_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	_, err := hub.QueryMessageRecordsByClientIP(ctx, "192.168.1.1", 0)
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.1", repo.lastFilter.ClientIP)
	assert.Equal(t, 0, repo.lastFilter.Limit)
}

// TestQueryMessageRecordsByStatus_WithRepo 验证按状态查询
func TestQueryMessageRecordsByStatus_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	_, err := hub.QueryMessageRecordsByStatus(ctx, models.MessageSendStatusFailed, 2)
	require.NoError(t, err)
	require.NotNil(t, repo.lastFilter.Status)
	assert.Equal(t, models.MessageSendStatusFailed, *repo.lastFilter.Status)
	assert.Equal(t, 2, repo.lastFilter.Limit)
}

// TestQueryRetryableMessageRecords_WithRepo 验证查询可重试记录
func TestQueryRetryableMessageRecords_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{retryableResult: []*models.MessageSendRecord{{MessageID: "m1"}}}
	hub.SetMessageRecordRepository(repo)

	got, err := hub.QueryRetryableMessageRecords(ctx, 7)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Equal(t, 7, repo.lastRetryableLimit)
}

// TestUpdateMessageRecordStatus_WithRepo 验证更新消息状态
func TestUpdateMessageRecordStatus_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	err := hub.UpdateMessageRecordStatus(ctx, "m1", models.MessageSendStatusSuccess, models.FailureReasonUnknown, "")
	require.NoError(t, err)
	assert.Equal(t, "m1", repo.lastUpdateStatusID)
	assert.Equal(t, models.MessageSendStatusSuccess, repo.lastUpdateStatus)
}

// TestUpdateMessageRecord_WithRepo 验证更新消息记录
func TestUpdateMessageRecord_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	record := &models.MessageSendRecord{MessageID: "m1"}
	err := hub.UpdateMessageRecord(ctx, record)
	require.NoError(t, err)
	assert.Same(t, record, repo.lastUpdateRecord)
}

// TestDeleteMessageRecord_WithRepo 验证删除消息记录
func TestDeleteMessageRecord_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	err := hub.DeleteMessageRecord(ctx, 42)
	require.NoError(t, err)
	assert.Equal(t, uint(42), repo.lastDeletedID)
}

// TestDeleteMessageRecordByMessageID_WithRepo 验证按消息ID删除
func TestDeleteMessageRecordByMessageID_WithRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	err := hub.DeleteMessageRecordByMessageID(ctx, "m-del")
	require.NoError(t, err)
	assert.Equal(t, "m-del", repo.lastDeletedMessage)
}

// TestMessageRecord_RepoError 验证 repository 返回错误时透传
func TestMessageRecord_RepoError(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	customErr := assertAnError("repo error")
	repo := &fakeMessageRecordRepo{
		findByMessageIDErr:   customErr,
		queryErr:             customErr,
		retryableErr:         customErr,
		updateStatusErr:      customErr,
		updateErr:            customErr,
		deleteErr:            customErr,
		deleteByMessageIDErr: customErr,
	}
	hub.SetMessageRecordRepository(repo)

	_, err := hub.QueryMessageRecord(ctx, "m1")
	assert.Equal(t, customErr, err)
	_, err = hub.QueryMessageRecordsBySender(ctx, "s1", 1)
	assert.Equal(t, customErr, err)
	_, err = hub.QueryRetryableMessageRecords(ctx, 1)
	assert.Equal(t, customErr, err)
	err = hub.UpdateMessageRecordStatus(ctx, "m1", models.MessageSendStatusSuccess, models.FailureReasonUnknown, "")
	assert.Equal(t, customErr, err)
	err = hub.UpdateMessageRecord(ctx, &models.MessageSendRecord{})
	assert.Equal(t, customErr, err)
	err = hub.DeleteMessageRecord(ctx, 1)
	assert.Equal(t, customErr, err)
	err = hub.DeleteMessageRecordByMessageID(ctx, "m1")
	assert.Equal(t, customErr, err)
}

// assertAnError 返回一个简单的 error
func assertAnError(msg string) error { return &simpleErr{msg: msg} }

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

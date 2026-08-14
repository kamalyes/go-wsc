/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 00:00:00
 * @FilePath: \go-wsc\hub\ack_test.go
 * @Description: Hub ACK 确认机制白盒单元测试（覆盖 hub/ack.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/protocol"
)

// ============================================================================
// 测试专用 fake：带调用记录的消息记录仓储
// ============================================================================

type ackFakeMessageRecordRepo struct {
	fakeMessageRecordRepo

	incrementRetryCalls int32
	lastIncrementMsgID  string
	lastIncrementNum    int
	lastIncrementErr    string
	incrementRetryMu    sync.Mutex
}

func newAckFakeMessageRecordRepo() *ackFakeMessageRecordRepo {
	return &ackFakeMessageRecordRepo{}
}

func (f *ackFakeMessageRecordRepo) IncrementRetry(_ context.Context, messageID string, attempt models.RetryAttempt) error {
	atomic.AddInt32(&f.incrementRetryCalls, 1)
	f.incrementRetryMu.Lock()
	f.lastIncrementMsgID = messageID
	f.lastIncrementNum = attempt.AttemptNumber
	if attempt.Error != "" {
		f.lastIncrementErr = attempt.Error
	}
	f.incrementRetryMu.Unlock()
	return f.updateStatusErr
}

func (f *ackFakeMessageRecordRepo) getIncrementRetryCalls() int {
	return int(atomic.LoadInt32(&f.incrementRetryCalls))
}

// ============================================================================
// 测试专用 fake：离线消息处理器（带调用记录）
// ============================================================================

type ackFakeOfflineHandler struct {
	mu            sync.Mutex
	storedUserIDs []string
	storedMsgIDs  []string
	storeErr      error
	storeCalled   int32
}

func newAckFakeOfflineHandler() *ackFakeOfflineHandler {
	return &ackFakeOfflineHandler{}
}

func (f *ackFakeOfflineHandler) StoreOfflineMessage(_ context.Context, userID string, msg *HubMessage) error {
	atomic.AddInt32(&f.storeCalled, 1)
	f.mu.Lock()
	f.storedUserIDs = append(f.storedUserIDs, userID)
	f.storedMsgIDs = append(f.storedMsgIDs, msg.MessageID)
	f.mu.Unlock()
	return f.storeErr
}

func (f *ackFakeOfflineHandler) DrainOfflineQueue(_ context.Context, _ string, _ int) ([]*HubMessage, error) {
	return nil, nil
}
func (f *ackFakeOfflineHandler) GetOfflineMessages(_ context.Context, _ string, _ int, _ string) ([]*HubMessage, string, error) {
	return nil, "", nil
}
func (f *ackFakeOfflineHandler) DeleteOfflineMessages(_ context.Context, _ string, _ []string) error {
	return nil
}
func (f *ackFakeOfflineHandler) GetOfflineMessageCount(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (f *ackFakeOfflineHandler) ClearOfflineMessages(_ context.Context, _ string, _ []string) error {
	return nil
}
func (f *ackFakeOfflineHandler) UpdatePushStatus(_ context.Context, _ []string, _ error) error {
	return nil
}

func (f *ackFakeOfflineHandler) getStoreCalled() int { return int(atomic.LoadInt32(&f.storeCalled)) }

// ============================================================================
// 测试 Setup Helpers
// ============================================================================

// setupAckTestHub 创建启用 ACK 的测试 Hub（不启动 Run）
func setupAckTestHub(t *testing.T, ackTimeout time.Duration) *Hub {
	t.Helper()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256).
		WithAck(ackTimeout).
		WithAckMaxRetries(3)
	hub := NewHub(config)
	t.Cleanup(func() { hub.Shutdown() })
	return hub
}

// setupAckTestHubNoAck 创建未启用 ACK 的测试 Hub（不启动 Run）
func setupAckTestHubNoAck(t *testing.T) *Hub {
	t.Helper()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	hub := NewHub(config)
	t.Cleanup(func() { hub.Shutdown() })
	return hub
}

// ============================================================================
// SendToUserWithAck 测试
// ============================================================================

// TestSendToUserWithAck_AckDisabled 验证 EnableAck=false 时走 SendToUserWithRetry 路径
func TestSendToUserWithAck_AckDisabled(t *testing.T) {
	hub := setupAckTestHubNoAck(t)
	assert.False(t, hub.config.EnableAck, "测试前置条件：EnableAck 应为 false")

	ctx := context.Background()
	msg := makeGroupMessage("sender-ack-disabled")

	// 离线无 handler → SendToUserWithRetry 返回 FinalError
	ackMsg, err := hub.SendToUserWithAck(ctx, "u-offline-noack", msg, time.Second, 1)
	assert.Nil(t, ackMsg, "EnableAck=false 时 ackMsg 应为 nil")
	require.Error(t, err)
	assert.True(t, IsUserOfflineError(err), "应返回用户离线错误")
}

// TestSendToUserWithAck_UserOfflineNoHandler 验证用户离线且无 offlineMessageHandler 时失败
func TestSendToUserWithAck_UserOfflineNoHandler(t *testing.T) {
	hub := setupAckTestHub(t, 500*time.Millisecond)
	assert.True(t, hub.config.EnableAck)

	ctx := context.Background()
	msg := makeGroupMessage("sender-off-nohandler")

	ackMsg, err := hub.SendToUserWithAck(ctx, "u-off-nohandler", msg, time.Second, 1)

	require.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusFailed, ackMsg.Status)
	assert.Equal(t, msg.MessageID, ackMsg.MessageID)
	require.Error(t, err)
	assert.Equal(t, ErrTypeUserOffline, errorx.ClassifyError(err))
}

// TestSendToUserWithAck_UserOfflineWithHandler 验证用户离线有 offlineMessageHandler 时存储并 Confirmed
func TestSendToUserWithAck_UserOfflineWithHandler(t *testing.T) {
	hub := setupAckTestHub(t, 500*time.Millisecond)
	handler := newAckFakeOfflineHandler()
	hub.SetOfflineMessageHandler(handler)

	ctx := context.Background()
	msg := makeGroupMessage("sender-off-handler")

	ackMsg, err := hub.SendToUserWithAck(ctx, "u-off-handler", msg, time.Second, 1)

	require.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusConfirmed, ackMsg.Status)
	assert.Equal(t, msg.MessageID, ackMsg.MessageID)
	assert.NoError(t, err)
	assert.Equal(t, 1, handler.getStoreCalled(), "应调用一次 StoreOfflineMessage")
}

// TestSendToUserWithAck_UserOnlineAckSuccess 验证用户在线并成功 ACK
func TestSendToUserWithAck_UserOnlineAckSuccess(t *testing.T) {
	hub := setupAckTestHub(t, 3*time.Second)
	repo := newAckFakeMessageRecordRepo()
	hub.SetMessageRecordRepository(repo)

	client := makeTestClient("c-ack-success", "u-ack-success")
	hub.shardedRegistry.AddClient(client)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	ctx := context.Background()
	msg := makeGroupMessage("sender-ack-success")
	msg.Receiver = "u-ack-success"

	go func() {
		select {
		case <-client.SendChan:
			ack := &AckMessage{
				MessageID: msg.MessageID,
				Status:    AckStatusConfirmed,
				Timestamp: time.Now(),
			}
			hub.HandleAck(ack)
		case <-time.After(5 * time.Second):
		}
	}()

	ackMsg, err := hub.SendToUserWithAck(ctx, "u-ack-success", msg, 2*time.Second, 1)

	require.NoError(t, err, "ACK 成功不应返回错误")
	require.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusConfirmed, ackMsg.Status)
	assert.Equal(t, msg.MessageID, ackMsg.MessageID)
}

// TestSendToUserWithAck_UserOnlineAckTimeout 验证用户在线但不 ACK 导致超时确认失败
func TestSendToUserWithAck_UserOnlineAckTimeout(t *testing.T) {
	hub := setupAckTestHub(t, 200*time.Millisecond)

	client := makeTestClient("c-ack-timeout", "u-ack-timeout")
	hub.shardedRegistry.AddClient(client)

	ctx := context.Background()
	msg := makeGroupMessage("sender-ack-timeout")

	// 用户不回复 ACK，等待超时
	ackMsg, err := hub.SendToUserWithAck(ctx, "u-ack-timeout", msg, 200*time.Millisecond, 0)

	require.Error(t, err, "ACK 超时应返回错误")
	require.NotNil(t, ackMsg)
	assert.Equal(t, protocol.AckStatusTimeout, ackMsg.Status)
	assert.Equal(t, msg.MessageID, ackMsg.MessageID)
}

// ============================================================================
// HandleAck 测试
// ============================================================================

// TestHandleAck_ConfirmedUpdatesDB 验证 Status=Confirmed 时 UpdateStatus 被调用
func TestHandleAck_ConfirmedUpdatesDB(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	repo := newAckFakeMessageRecordRepo()
	hub.SetMessageRecordRepository(repo)

	// 先添加一条 pending message（否则 ConfirmMessage 无效果但 HandleAck 仍会调用 UpdateStatus）
	msg := makeGroupMessage("sender-handleack-confirmed")
	pm := hub.ackManager.AddPendingMessage(msg)
	require.NotNil(t, pm)

	ackMsg := &AckMessage{
		MessageID: msg.MessageID,
		Status:    AckStatusConfirmed,
		Timestamp: time.Now(),
	}
	hub.HandleAck(ackMsg)

	// 异步 UpdateStatus，等待执行（加锁读取避免 data race）
	assert.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return repo.lastUpdateStatusID == msg.MessageID &&
			repo.lastUpdateStatus == MessageSendStatusSuccess
	}, 2*time.Second, 20*time.Millisecond,
		"Status=Confirmed 时应调用 UpdateStatus 置为 Success")
}

// TestHandleAck_NonConfirmedSkipsDB 验证 Status!=Confirmed 时不调用 UpdateStatus
func TestHandleAck_NonConfirmedSkipsDB(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	repo := newAckFakeMessageRecordRepo()
	hub.SetMessageRecordRepository(repo)

	// Timeout 状态
	ackTimeout := &AckMessage{
		MessageID: "msg-timeout-no-db",
		Status:    protocol.AckStatusTimeout,
		Timestamp: time.Now(),
	}
	hub.HandleAck(ackTimeout)
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, repo.lastUpdateStatusID, "Timeout 状态不应触发 UpdateStatus")

	// Failed 状态
	ackFailed := &AckMessage{
		MessageID: "msg-failed-no-db",
		Status:    AckStatusFailed,
		Timestamp: time.Now(),
	}
	hub.HandleAck(ackFailed)
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, repo.lastUpdateStatusID, "Failed 状态不应触发 UpdateStatus")

	// Pending 状态
	ackPending := &AckMessage{
		MessageID: "msg-pending-no-db",
		Status:    protocol.AckStatusPending,
		Timestamp: time.Now(),
	}
	hub.HandleAck(ackPending)
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, repo.lastUpdateStatusID, "Pending 状态不应触发 UpdateStatus")
}

// TestHandleAck_NilRepoNoPanic 验证 repo=nil 时 HandleAck 不 panic
func TestHandleAck_NilRepoNoPanic(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)

	assert.NotPanics(t, func() {
		hub.HandleAck(&AckMessage{
			MessageID: "msg-nil-repo",
			Status:    AckStatusConfirmed,
			Timestamp: time.Now(),
		})
	})
}

// ============================================================================
// checkUserOnlineForAck 测试
// ============================================================================

// TestCheckUserOnlineForAck_Online 验证用户在线时返回 true,nil,nil,true
func TestCheckUserOnlineForAck_Online(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)

	client := makeTestClient("c-check-online", "u-check-online")
	hub.shardedRegistry.AddClient(client)

	ctx := context.Background()
	msg := makeGroupMessage("sender-check-online")

	ackMsg, err, isOnline := hub.checkUserOnlineForAck(ctx, "u-check-online", msg)
	assert.True(t, isOnline)
	assert.Nil(t, ackMsg)
	assert.NoError(t, err)
}

// TestCheckUserOnlineForAck_Offline 验证用户离线时走 handleOfflineAckMessage
func TestCheckUserOnlineForAck_Offline(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)

	ctx := context.Background()
	msg := makeGroupMessage("sender-check-offline")

	ackMsg, err, isOnline := hub.checkUserOnlineForAck(ctx, "u-check-offline-notexist", msg)
	assert.False(t, isOnline)
	require.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusFailed, ackMsg.Status)
	require.Error(t, err)
}

// ============================================================================
// handleOfflineAckMessage 测试
// ============================================================================

// TestHandleOfflineAckMessage_NoHandler 验证无 handler 返回 AckStatusFailed + error
func TestHandleOfflineAckMessage_NoHandler(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)

	ctx := context.Background()
	msg := makeGroupMessage("sender-off-nohandler")

	ackMsg, err, isOnline := hub.handleOfflineAckMessage(ctx, "u-off-nohandler", msg)
	assert.False(t, isOnline)
	require.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusFailed, ackMsg.Status)
	assert.Equal(t, msg.MessageID, ackMsg.MessageID)
	require.Error(t, err)
}

// TestHandleOfflineAckMessage_WithHandlerSuccess 验证有 handler 成功存储时返回 Confirmed
func TestHandleOfflineAckMessage_WithHandlerSuccess(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	handler := newAckFakeOfflineHandler()
	hub.SetOfflineMessageHandler(handler)

	ctx := context.Background()
	msg := makeGroupMessage("sender-off-handler-ok")

	ackMsg, err, isOnline := hub.handleOfflineAckMessage(ctx, "u-off-handler-ok", msg)
	assert.False(t, isOnline)
	require.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusConfirmed, ackMsg.Status)
	assert.Equal(t, msg.MessageID, ackMsg.MessageID)
	assert.NoError(t, err)
	assert.Equal(t, 1, handler.getStoreCalled())
}

// TestHandleOfflineAckMessage_WithHandlerStoreError 验证 handler 存储失败时仍返回 Confirmed（只记录日志）
func TestHandleOfflineAckMessage_WithHandlerStoreError(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	handler := newAckFakeOfflineHandler()
	handler.storeErr = errorx.NewError(models.ErrTypeRecordRepositoryNotSet)
	hub.SetOfflineMessageHandler(handler)

	ctx := context.Background()
	msg := makeGroupMessage("sender-off-handler-err")

	ackMsg, err, isOnline := hub.handleOfflineAckMessage(ctx, "u-off-handler-err", msg)
	assert.False(t, isOnline)
	require.NotNil(t, ackMsg)
	assert.Equal(t, AckStatusConfirmed, ackMsg.Status)
	assert.Equal(t, msg.MessageID, ackMsg.MessageID)
	assert.NoError(t, err)
	assert.Equal(t, 1, handler.getStoreCalled())
}

// ============================================================================
// createAckRetryFunc 测试
// ============================================================================

// TestCreateAckRetryFunc_AttemptIncrement 验证重试函数递增 attemptNum 并调用 sendToUser
func TestCreateAckRetryFunc_AttemptIncrement(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)

	client := makeTestClient("c-retry-inc", "u-retry-inc")
	hub.shardedRegistry.AddClient(client)

	ctx := context.Background()
	msg := makeGroupMessage("sender-retry-inc")
	attemptNum := 0

	retryFunc := hub.createAckRetryFunc(ctx, "u-retry-inc", msg, &attemptNum)

	// 第 1 次：attemptNum 0→1，不记录重试（<=1）
	err := retryFunc()
	require.NoError(t, err)
	assert.Equal(t, 1, attemptNum)

	// 第 2 次：attemptNum 1→2，>1 触发 recordAckRetryAttempt
	repo := newAckFakeMessageRecordRepo()
	hub.SetMessageRecordRepository(repo)

	err = retryFunc()
	require.NoError(t, err)
	assert.Equal(t, 2, attemptNum)

	// recordAckRetryAttempt 是异步，等待一下
	assert.Eventually(t, func() bool {
		return repo.getIncrementRetryCalls() >= 1
	}, time.Second, 20*time.Millisecond, "第2次重试应调用 IncrementRetry")
}

// TestCreateAckRetryFunc_FirstAttemptNoRecord 验证第 1 次发送不记录重试
func TestCreateAckRetryFunc_FirstAttemptNoRecord(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	repo := newAckFakeMessageRecordRepo()
	hub.SetMessageRecordRepository(repo)

	client := makeTestClient("c-retry-no1st", "u-retry-no1st")
	hub.shardedRegistry.AddClient(client)

	ctx := context.Background()
	msg := makeGroupMessage("sender-retry-no1st")
	attemptNum := 0

	retryFunc := hub.createAckRetryFunc(ctx, "u-retry-no1st", msg, &attemptNum)
	err := retryFunc()
	require.NoError(t, err)
	assert.Equal(t, 1, attemptNum)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, 0, repo.getIncrementRetryCalls(), "第1次尝试不应记录重试")
}

// ============================================================================
// recordAckRetryAttempt 测试
// ============================================================================

// TestRecordAckRetryAttempt_SuccessCase 验证发送成功时记录 Success=true
func TestRecordAckRetryAttempt_SuccessCase(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	repo := newAckFakeMessageRecordRepo()
	hub.SetMessageRecordRepository(repo)

	msg := makeGroupMessage("sender-retry-ok")
	hub.recordAckRetryAttempt(msg.MessageID, 2, nil)

	assert.Eventually(t, func() bool {
		repo.incrementRetryMu.Lock()
		defer repo.incrementRetryMu.Unlock()
		return repo.getIncrementRetryCalls() == 1 &&
			repo.lastIncrementMsgID == msg.MessageID &&
			repo.lastIncrementNum == 2 &&
			repo.lastIncrementErr == ""
	}, time.Second, 20*time.Millisecond, "成功重试应记录 AttemptNumber=2，Error 为空")
}

// TestRecordAckRetryAttempt_ErrorCase 验证发送失败时记录 Error 字段
func TestRecordAckRetryAttempt_ErrorCase(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	repo := newAckFakeMessageRecordRepo()
	hub.SetMessageRecordRepository(repo)

	msg := makeGroupMessage("sender-retry-err")
	sendErr := errorx.NewError(ErrTypeUserOffline, "u1")
	hub.recordAckRetryAttempt(msg.MessageID, 3, sendErr)

	assert.Eventually(t, func() bool {
		repo.incrementRetryMu.Lock()
		defer repo.incrementRetryMu.Unlock()
		return repo.getIncrementRetryCalls() == 1 &&
			repo.lastIncrementMsgID == msg.MessageID &&
			repo.lastIncrementNum == 3 &&
			repo.lastIncrementErr != ""
	}, time.Second, 20*time.Millisecond, "失败重试应记录 AttemptNumber=3，Error 非空")
}

// TestRecordAckRetryAttempt_NilRepoNoPanic 验证 repo=nil 时不 panic（在 createAckRetryFunc 的 guard 外直接调用）
func TestRecordAckRetryAttempt_NilRepoNoPanic(t *testing.T) {
	hub := setupAckTestHub(t, time.Second)
	// 显式不设置 repo
	assert.NotPanics(t, func() {
		hub.recordAckRetryAttempt("msg-nilrepo-retry", 2, nil)
	})
}

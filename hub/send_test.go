/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-06-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-06-25 10:56:20
 * @FilePath: \go-wsc\hub\send_test.go
 * @Description: Hub 发送核心路径白盒单元测试（覆盖 hub/send.go）
 *
 * 复用 group_test.go 中的 setupGroupTestHub / makeTestClient / makeGroupMessage 等 helper。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 测试专用 mock：连接记录仓库（仅观测 BatchIncrementStats 计数调用）
// ============================================================================

// mockConnRecordRepo 内存连接记录仓库，记录 BatchIncrementStats 调用用于断言统计计数
type mockConnRecordRepo struct {
	mu          sync.Mutex
	statEntries []*repository.StatsIncrementEntry
}

func (m *mockConnRecordRepo) Upsert(_ context.Context, _ *ConnectionRecord) error { return nil }
func (m *mockConnRecordRepo) MarkDisconnected(_ context.Context, _ string, _ DisconnectReason, _ int, _ string) error {
	return nil
}
func (m *mockConnRecordRepo) GetByConnectionID(_ context.Context, _ string) (*ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) GetByUserID(_ context.Context, _ string) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) GetActiveByUserID(_ context.Context, _ string) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) AddError(_ context.Context, _ string, _ error) error { return nil }
func (m *mockConnRecordRepo) BatchUpdateHeartbeats(_ context.Context, _ []*repository.HeartbeatUpdateEntry) error {
	return nil
}
func (m *mockConnRecordRepo) BatchIncrementStats(_ context.Context, entries []*repository.StatsIncrementEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statEntries = append(m.statEntries, entries...)
	return nil
}
func (m *mockConnRecordRepo) List(_ context.Context, _ *repository.ConnectionQueryOptions) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) Count(_ context.Context, _ *repository.ConnectionQueryOptions) (int64, error) {
	return 0, nil
}
func (m *mockConnRecordRepo) GetConnectionStats(_ context.Context, _, _ time.Time) (*repository.ConnectionStats, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) GetConnectionStatsByID(_ context.Context, _ string) (*repository.UserConnectionStats, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) GetUserConnectionStats(_ context.Context, _ string) (*repository.UserConnectionStats, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) GetNodeConnectionStats(_ context.Context, _ string) (*repository.NodeConnectionStats, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) GetHighErrorRateConnections(_ context.Context, _, _ int) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) GetFrequentReconnectConnections(_ context.Context, _, _ int) ([]*ConnectionRecord, error) {
	return nil, nil
}
func (m *mockConnRecordRepo) BatchUpsert(_ context.Context, _ []*ConnectionRecord) error { return nil }
func (m *mockConnRecordRepo) CleanupInactiveRecords(_ context.Context, _ time.Time) (int64, error) {
	return 0, nil
}
func (m *mockConnRecordRepo) WithTableName(_ string) ConnectionRecordRepository { return m }
func (m *mockConnRecordRepo) Close() error                                      { return nil }

// statsEntriesCount 返回已记录的统计条目数（线程安全）
func (m *mockConnRecordRepo) statsEntriesCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.statEntries)
}

// findStatEntry 查找指定 connectionID 的统计条目是否存在
func (m *mockConnRecordRepo) findStatEntry(connectionID string) (*repository.StatsIncrementEntry, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.statEntries {
		if e.ConnectionID == connectionID {
			return e, true
		}
	}
	return nil, false
}

// ============================================================================
// sendToClientSerialized 测试
// ============================================================================

// TestSendToClientSerializedClosedClient 验证客户端已标记关闭时直接返回，不 panic 且不投递
func TestSendToClientSerializedClosedClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-closed", "u-closed")
	client.MarkClosed()
	msg := makeGroupMessage("sender")

	assert.NotPanics(t, func() {
		hub.sendToClientSerialized(hub.ctx, client, msg, nil)
	})

	// 通道不应收到任何消息
	select {
	case <-client.SendChan:
		t.Fatal("已关闭客户端不应收到消息")
	default:
	}
}

// TestSendToClientSerializedClosedChannel 验证 SendChan 被 close 时 TrySend 内部 recover，不 panic
func TestSendToClientSerializedClosedChannel(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-chan-closed", "u-chan-closed")
	close(client.SendChan) // 实际关闭 channel，IsClosed 仍为 false，走 TrySend 的 recover 路径
	msg := makeGroupMessage("sender")

	assert.NotPanics(t, func() {
		hub.sendToClientSerialized(hub.ctx, client, msg, nil)
	})
	assert.True(t, client.IsClosed(), "TrySend recover 后应标记为已关闭")
}

// TestSendToClientSerializedNormalWS 验证 WS 客户端正常投递：SendChan 收到帧且 trackReceiverMessageStats 触发计数
func TestSendToClientSerializedNormalWS(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 注入 mock 连接记录仓库以观测统计计数
	mockRepo := &mockConnRecordRepo{}
	hub.SetConnectionRecordRepository(mockRepo)

	client := makeTestClient("c-normal", "u-normal")
	msg := makeGroupMessage("sender")
	msg.MessageID = "msg-stats-1"

	hub.sendToClientSerialized(hub.ctx, client, msg, nil)

	// SendChan 应收到序列化帧
	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data)
		var got HubMessage
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, "msg-stats-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("WS 客户端未收到消息帧")
	}

	// trackReceiverMessageStats 通过批量处理器异步刷写，等待计数落库
	require.Eventually(t, func() bool {
		e, ok := mockRepo.findStatEntry("c-normal")
		if !ok {
			return false
		}
		return e.MessagesReceived == 1 && e.BytesReceived > 0
	}, 3*time.Second, 50*time.Millisecond, "trackReceiverMessageStats 应触发统计计数")
}

// TestSendToClientSerializedTrySendFail 验证 SendChan 已满时 TrySend 失败，不 panic 且不新增投递
func TestSendToClientSerializedTrySendFail(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-full", "u-full")
	// 填满 SendChan（缓冲 16），使后续 TrySend 走 default 返回 false
	for i := 0; i < cap(client.SendChan); i++ {
		client.SendChan <- []byte("filler")
	}
	msg := makeGroupMessage("sender")

	assert.NotPanics(t, func() {
		hub.sendToClientSerialized(hub.ctx, client, msg, nil)
	})

	// 通道仍为满（16 条 filler），新消息未入队
	assert.Equal(t, cap(client.SendChan), len(client.SendChan))
	// 排空并确认全是 filler，不含新消息
	drained := 0
	for {
		select {
		case d := <-client.SendChan:
			assert.Equal(t, "filler", string(d))
			drained++
		default:
			assert.Equal(t, cap(client.SendChan), drained)
			return
		}
	}
}

// TestSendToClientSerializedPreSerialized 验证传入预序列化数据时被直接复用
func TestSendToClientSerializedPreSerialized(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-pre", "u-pre")
	msg := makeGroupMessage("sender")
	msg.MessageID = "msg-pre-1"
	pre, err := json.Marshal(msg)
	require.NoError(t, err)

	hub.sendToClientSerialized(hub.ctx, client, msg, pre)

	select {
	case data := <-client.SendChan:
		assert.Equal(t, string(pre), string(data), "应直接复用预序列化数据")
	case <-time.After(time.Second):
		t.Fatal("未收到预序列化消息")
	}
}

// ============================================================================
// SendToAllClientsInMap 测试
// ============================================================================

// TestSendToAllClientsInMapEmpty 验证空 map 直接返回，不 panic
func TestSendToAllClientsInMapEmpty(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.NotPanics(t, func() {
		hub.SendToAllClientsInMap(map[string]*Client{}, makeGroupMessage("sender"))
	})
}

// TestSendToAllClientsInMapMultiple 验证多客户端均收到消息
func TestSendToAllClientsInMapMultiple(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c1", "u1")
	c2 := makeTestClient("c2", "u2")
	c3 := makeTestClient("c3", "u3")
	clientMap := map[string]*Client{c1.ID: c1, c2.ID: c2, c3.ID: c3}

	hub.SendToAllClientsInMap(clientMap, makeGroupMessage("sender"))

	for _, c := range []*Client{c1, c2, c3} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("客户端 %s 未收到消息", c.ID)
		}
	}
}

// ============================================================================
// SendToMultipleUsers 测试
// ============================================================================

// TestSendToMultipleUsersEmpty 验证空用户列表返回空 map
func TestSendToMultipleUsersEmpty(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	errs := hub.SendToMultipleUsers(context.Background(), nil, makeGroupMessage("sender"))
	assert.Empty(t, errs)
}

// TestSendToMultipleUsersOnlineAndOffline 验证混合在线/离线用户：在线收到、离线报错
func TestSendToMultipleUsersOnlineAndOffline(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	// 在线用户
	online := makeTestClient("c-online", "u-online")
	hub.shardedRegistry.AddClient(online)

	msg := makeGroupMessage("sender")
	errs := hub.SendToMultipleUsers(context.Background(), []string{"u-online", "u-offline"}, msg)

	// 离线用户（无 handler）应在错误 map 中
	require.Contains(t, errs, "u-offline")
	// 使用 ClassifyError 判定错误类型（IsUserOfflineError 因源码 Type()/GetType() 不匹配而失效）
	assert.Equal(t, ErrTypeUserOffline, errorx.ClassifyError(errs["u-offline"]))
	// 在线用户不在错误 map 中
	_, hasOnlineErr := errs["u-online"]
	assert.False(t, hasOnlineErr)

	// 在线用户应收到消息
	require.Eventually(t, func() bool {
		select {
		case <-online.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "在线用户应收到消息")
}

// ============================================================================
// SendToClientsWithRetry 测试
// ============================================================================

// TestSendToClientsWithRetryEmpty 验证空客户端列表返回空 map
func TestSendToClientsWithRetryEmpty(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	results := hub.SendToClientsWithRetry(context.Background(), nil, makeGroupMessage("sender"), 1)
	assert.Empty(t, results)
}

// ============================================================================
// SendToGroupMembers 测试
// ============================================================================

// TestSendToGroupMembersExcludeSender 验证排除发送者：发送者不收到，其他在线成员收到，离线成员计失败
func TestSendToGroupMembersExcludeSender(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	sender := makeTestClient("c-sender", "u-sender")
	other := makeTestClient("c-other", "u-other")
	hub.shardedRegistry.AddClient(sender)
	hub.shardedRegistry.AddClient(other)

	msg := makeGroupMessage("u-sender")
	// 成员含 sender、other、offline-user
	result := hub.SendToGroupMembers(context.Background(),
		[]string{"u-sender", "u-other", "u-offline"}, msg, true)

	// 排除 sender 后 filteredIDs = [u-other, u-offline]
	assert.Equal(t, 2, result.Total)
	assert.Equal(t, 1, result.Success, "u-other 在线应成功")
	assert.Equal(t, 1, result.Failed, "u-offline 离线应失败")
	assert.Contains(t, result.FailedIDs, "u-offline")

	// other 应收到
	require.Eventually(t, func() bool {
		select {
		case <-other.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "其他成员应收到消息")

	// sender 不应收到（排除）
	select {
	case <-sender.SendChan:
		t.Fatal("发送者被排除不应收到消息")
	case <-time.After(200 * time.Millisecond):
	}
}

// ============================================================================
// SendConditional 测试
// ============================================================================

// TestSendConditionalFiltering 验证条件过滤：false 不收到，true 收到
func TestSendConditionalFiltering(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	customer := makeTestClient("c-customer", "u-customer")
	agent := makeTestClient("c-agent", "u-agent")
	agent.UserType = UserTypeAgent
	hub.shardedRegistry.AddClient(customer)
	hub.shardedRegistry.AddClient(agent)

	msg := makeGroupMessage("sender")
	// 仅投递给 Agent 类型客户端
	delivered := hub.SendConditional(context.Background(), func(c *Client) bool {
		return c.UserType == UserTypeAgent
	}, msg)

	assert.Equal(t, 1, delivered)

	// agent 收到
	select {
	case <-agent.SendChan:
	default:
		t.Fatal("agent 应收到消息")
	}
	// customer 不收到
	select {
	case <-customer.SendChan:
		t.Fatal("customer 不应收到消息")
	default:
	}
}

// ============================================================================
// isRetryableError 测试
// ============================================================================

// TestIsRetryableError 验证错误可重试判定：nil/不可重试→false，可重试→true
func TestIsRetryableError(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("nil错误返回false", func(t *testing.T) {
		assert.False(t, hub.isRetryableError(nil))
	})
	t.Run("可重试错误返回true", func(t *testing.T) {
		assert.True(t, hub.isRetryableError(ErrQueueAndPendingFull))
	})
	t.Run("不可重试错误返回false", func(t *testing.T) {
		// 修复 sentinel 初始化顺序后，sentinel 与运行时错误均应正确判定为不可重试
		assert.False(t, hub.isRetryableError(ErrClientNotFound), "ErrClientNotFound sentinel 不应可重试")
		nonRetryable := errorx.NewError(ErrTypeClientNotFound)
		assert.False(t, hub.isRetryableError(nonRetryable))
	})
	t.Run("普通error返回false", func(t *testing.T) {
		assert.False(t, hub.isRetryableError(errors.New("plain error")))
	})
}

// ============================================================================
// SendToUserWithRetry 测试
// ============================================================================

// TestSendToUserWithRetryOfflineNoHandler 验证离线用户无 offlineMessageHandler 时返回 FinalError
func TestSendToUserWithRetryOfflineNoHandler(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	result := hub.SendToUserWithRetry(context.Background(), "u-offline-noop", msg)

	assert.False(t, result.Success)
	require.NotNil(t, result.FinalError)
	// 修复 Is*Error 后，IsUserOfflineError 对运行时离线错误正确返回 true
	assert.True(t, IsUserOfflineError(result.FinalError), "应识别为用户离线错误")
	assert.Equal(t, ErrTypeUserOffline, errorx.ClassifyError(result.FinalError))
	assert.False(t, result.StoredOffline)
}

// TestSendToUserWithRetryOnline 验证在线用户发送成功并送达
func TestSendToUserWithRetryOnline(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	client := makeTestClient("c-online", "u-online")
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("sender")
	result := hub.SendToUserWithRetry(context.Background(), "u-online", msg)

	assert.True(t, result.Success)
	assert.NoError(t, result.FinalError)

	require.Eventually(t, func() bool {
		select {
		case <-client.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "在线用户应收到消息")
}

// ============================================================================
// SendPriority 测试
// ============================================================================

// TestSendPriorityHighAndNormal 验证高优先级与普通优先级路径均投递成功
func TestSendPriorityHighAndNormal(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	high := makeTestClient("c-high", "u-high")
	normal := makeTestClient("c-normal", "u-normal")
	hub.shardedRegistry.AddClient(high)
	hub.shardedRegistry.AddClient(normal)

	// 高优先级走异步 goroutine
	hub.SendPriority(context.Background(), "u-high", makeGroupMessage("sender"), PriorityHigh)
	// 普通优先级走标准同步流程
	hub.SendPriority(context.Background(), "u-normal", makeGroupMessage("sender"), PriorityNormal)

	require.Eventually(t, func() bool {
		select {
		case <-high.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "高优先级用户应收到消息")

	require.Eventually(t, func() bool {
		select {
		case <-normal.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "普通优先级用户应收到消息")
}

// ============================================================================
// syncToSenderDevices 测试
// ============================================================================

// TestSyncToSenderDevices 验证多端同步：无 sender 返回、单设备不回环、多设备其他设备收到
func TestSyncToSenderDevices(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("无sender直接返回", func(t *testing.T) {
		c := makeTestClient("c1", "u1")
		hub.shardedRegistry.AddClient(c)
		msg := makeGroupMessage("")
		assert.NotPanics(t, func() {
			hub.syncToSenderDevices(hub.ctx, msg)
		})
		select {
		case <-c.SendChan:
			t.Fatal("无 sender 不应同步")
		default:
		}
	})

	t.Run("单设备不回环", func(t *testing.T) {
		c := makeTestClient("c-single", "u-single")
		hub.shardedRegistry.AddClient(c)
		msg := makeGroupMessage("u-single")
		msg.SenderClient = "c-single"
		hub.syncToSenderDevices(hub.ctx, msg)
		select {
		case <-c.SendChan:
			t.Fatal("发送者自身设备不应收到回环消息")
		case <-time.After(200 * time.Millisecond):
		}
	})

	t.Run("多设备其他设备收到", func(t *testing.T) {
		dev1 := makeTestClient("c-dev1", "u-multi")
		dev2 := makeTestClient("c-dev2", "u-multi")
		hub.shardedRegistry.AddClient(dev1)
		hub.shardedRegistry.AddClient(dev2)

		msg := makeGroupMessage("u-multi")
		msg.SenderClient = "c-dev1" // dev1 为发送设备，dev2 应收到同步
		hub.syncToSenderDevices(hub.ctx, msg)

		// dev2 收到
		select {
		case data := <-dev2.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatal("发送者其他设备应收到同步消息")
		}
		// dev1 不收到
		select {
		case <-dev1.SendChan:
			t.Fatal("发送设备自身不应收到回环")
		default:
		}
	})
}

// ============================================================================
// notifyQueueFull 测试
// ============================================================================

// TestNotifyQueueFullNilCallback 验证 callback 为 nil 时不 panic
func TestNotifyQueueFullNilCallback(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	assert.NotPanics(t, func() {
		hub.notifyQueueFull(msg, "u-recipient", QueueTypeAllQueues, ErrQueueAndPendingFull)
	})
}

// TestNotifyQueueFullCallbackInvoked 验证 callback 非 nil 时被异步调用
func TestNotifyQueueFullCallbackInvoked(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	var mu sync.Mutex
	got := make(chan struct{}, 1)
	hub.OnQueueFull(func(msg *HubMessage, recipient string, queueType QueueType, _ errorx.BaseError) {
		mu.Lock()
		defer mu.Unlock()
		assert.Equal(t, "u-recipient", recipient)
		assert.Equal(t, QueueTypeAllQueues, queueType)
		select {
		case got <- struct{}{}:
		default:
		}
	})

	msg := makeGroupMessage("sender")
	hub.notifyQueueFull(msg, "u-recipient", QueueTypeAllQueues, ErrQueueAndPendingFull)

	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("queueFullCallback 未被调用")
	}
}

// ============================================================================
// invokeMessageSendCallback 测试
// ============================================================================

// TestInvokeMessageSendCallbackNil 验证 callback 为 nil 时跳过不 panic
func TestInvokeMessageSendCallbackNil(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	result := &SendResult{Success: true}
	assert.NotPanics(t, func() {
		hub.invokeMessageSendCallback(msg, result)
	})
}

// TestInvokeMessageSendCallbackNonHumanReceiverType 验证 ReceiverType 非人类时跳过回调
func TestInvokeMessageSendCallbackNonHumanReceiverType(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	called := make(chan struct{}, 1)
	hub.OnMessageSend(func(_ *HubMessage, _ *SendResult) {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	// ReceiverType 为机器人（非人类），回调应被跳过
	msg := makeGroupMessage("sender")
	msg.ReceiverType = UserTypeBot
	hub.invokeMessageSendCallback(msg, &SendResult{Success: true})

	select {
	case <-called:
		t.Fatal("非人类 ReceiverType 不应触发回调")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestInvokeMessageSendCallbackHumanInvoked 验证人类/空 ReceiverType 时回调被调用
func TestInvokeMessageSendCallbackHumanInvoked(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	called := make(chan struct{}, 1)
	hub.OnMessageSend(func(_ *HubMessage, _ *SendResult) {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	// ReceiverType 为空（向后兼容，视为人类）
	msg := makeGroupMessage("sender")
	hub.invokeMessageSendCallback(msg, &SendResult{Success: true})

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("人类 ReceiverType 应触发回调")
	}
}

// fakeOfflineHandler 是 OfflineMessageHandler 的内存 fake，用于离线存储场景测试。
type fakeOfflineHandler struct {
	mu       sync.Mutex
	stored   []string // 记录被存储离线消息的 userID
	failOnce int32
}

func (f *fakeOfflineHandler) StoreOfflineMessage(_ context.Context, userID string, _ *HubMessage) error {
	if atomic.LoadInt32(&f.failOnce) == 1 {
		return errorx.NewError(models.ErrTypeRecordRepositoryNotSet)
	}
	f.mu.Lock()
	f.stored = append(f.stored, userID)
	f.mu.Unlock()
	return nil
}

func (f *fakeOfflineHandler) GetOfflineMessages(_ context.Context, _ string, _ int, _ string) ([]*HubMessage, string, error) {
	return nil, "", nil
}
func (f *fakeOfflineHandler) DrainOfflineQueue(_ context.Context, _ string, _ int) ([]*HubMessage, error) {
	return nil, nil
}
func (f *fakeOfflineHandler) DeleteOfflineMessages(_ context.Context, _ string, _ []string) error {
	return nil
}
func (f *fakeOfflineHandler) GetOfflineMessageCount(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (f *fakeOfflineHandler) ClearOfflineMessages(_ context.Context, _ string, _ []string) error { return nil }
func (f *fakeOfflineHandler) UpdatePushStatus(_ context.Context, _ []string, _ error) error {
	return nil
}

func (f *fakeOfflineHandler) storedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stored)
}

// smallRetryHubConfig 构造一个小缓冲 + 快速重试的 Hub 配置，用于触发队列满重试。
func smallRetryHubConfig(maxRetries int) *wscconfig.WSC {
	return wscconfig.Default().
		WithMessageBufferSize(1).
		WithMaxPendingQueueSize(1).
		WithRetryPolicy(wscconfig.DefaultRetryPolicy().
			WithMaxRetries(maxRetries).
			WithDelay(time.Millisecond, 5*time.Millisecond))
}

// TestSendScenario_QueueFullRetriesExhausted 验证核心发送路径：用户在线但 broadcast 与
// pending 队列均满时，sendToUser 返回 ErrQueueAndPendingFull（可重试），重试循环按
// MaxRetries+1 次耗尽后给出 FinalError，且错误被正确分类为队列满/可重试。
//
// 该场景锁定 errors.go 修复后 IsRetryableError/IsQueueFullError 对 sentinel 的判定：
// 修复前 sentinel 全为 Type=0 互相相等，IsQueueFullError(ErrQueueAndPendingFull) 经由
// 断言失败路径退回 == 比较虽可命中，但 IsRetryableError 对其它 sentinel 误判 true；
// 修复后基于 ClassifyError 严格按 ErrorType 判定，行为正确且不回归。
func TestSendScenario_QueueFullRetriesExhausted(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	// 故意不启动 Run()：EventLoop 不消费 broadcast/pending，队列可被填满且不排水。
	defer hub.SafeShutdown()

	// 直接登记一个在线用户（checkUserOnline 先查 shardedRegistry.HasUser）
	client := makeTestClient("c-qfull", "u-qfull")
	hub.shardedRegistry.AddClient(client)

	// 填满 broadcast（cap = MessageBufferSize*4 = 4）与 pending（cap = MaxPendingQueueSize = 1）
	fill := &HubMessage{ID: "fill"}
	for i := 0; i < 4; i++ {
		hub.broadcast <- fill
	}
	hub.pendingMessages <- fill

	msg := makeGroupMessage("sender")
	msg.ReceiverType = UserTypeCustomer
	result := hub.SendToUserWithRetry(context.Background(), "u-qfull", msg)

	require.NotNil(t, result)
	assert.False(t, result.Success, "队列满时应失败")
	require.NotNil(t, result.FinalError, "应有最终错误")
	assert.Equal(t, 3, len(result.Attempts), "应重试 MaxRetries+1=3 次")

	// 错误分类：队列满 + 可重试（修复后对 sentinel 与运行时错误均生效）
	assert.Equal(t, models.ErrTypeQueueAndPendingFull, errorx.ClassifyError(result.FinalError))
	assert.True(t, IsQueueFullError(result.FinalError), "应识别为队列满错误")
	assert.True(t, IsRetryableError(result.FinalError), "ErrQueueAndPendingFull 应可重试")
	assert.False(t, IsUserOfflineError(result.FinalError), "不应误判为离线错误")
}

// TestSendScenario_OfflineNoHandlerClassifiedCorrectly 验证离线用户无 handler 时：
// 运行时创建的离线错误被 IsUserOfflineError 正确识别（修复前 Type() 断言失败导致误判 false），
// 且不进入重试（离线路径在重试循环之前返回）。
func TestSendScenario_OfflineNoHandlerClassifiedCorrectly(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(3))
	defer hub.SafeShutdown()

	result := hub.SendToUserWithRetry(context.Background(), "u-not-online", makeGroupMessage("sender"))

	require.NotNil(t, result)
	assert.False(t, result.Success)
	require.NotNil(t, result.FinalError)
	// 离线错误分类（修复后生效）
	assert.True(t, IsUserOfflineError(result.FinalError), "运行时离线错误应被识别")
	assert.Equal(t, ErrTypeUserOffline, errorx.ClassifyError(result.FinalError))
	// 离线不进入重试循环
	assert.Equal(t, 0, len(result.Attempts), "离线路径不应产生发送尝试")
	assert.False(t, IsRetryableError(result.FinalError), "离线错误不应可重试")
	assert.False(t, IsQueueFullError(result.FinalError))
}

// TestSendScenario_OfflineWithHandlerStored 验证离线用户配置 handler 时走离线存储路径。
func TestSendScenario_OfflineWithHandlerStored(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	handler := &fakeOfflineHandler{}
	hub.SetOfflineMessageHandler(handler)
	defer hub.SafeShutdown()

	result := hub.SendToUserWithRetry(context.Background(), "u-offline-store", makeGroupMessage("sender"))

	require.NotNil(t, result)
	assert.True(t, result.Success, "离线存储成功应标记 Success")
	assert.True(t, result.StoredOffline, "应标记 StoredOffline")
	assert.Nil(t, result.FinalError)
	assert.Equal(t, 1, handler.storedCount(), "handler 应被调用一次")
}

// TestSendScenario_NonRetryableSentinelNotRetried 验证 hub/observer.go 直接 return 的
// ErrClientNotFound（sentinel）被正确分类为不可重试、非队列满，因此重试循环遇到它应立即停止。
// 修复前：所有 sentinel 互相相等（Type=0），IsRetryableError(ErrClientNotFound) 误判 true，
// 会导致对“客户端未找到”这种不可恢复错误进行无意义重试。
func TestSendScenario_NonRetryableSentinelNotRetried(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(3))
	defer hub.SafeShutdown()

	// 模拟 observer.go 直接返回 sentinel 的场景
	err := ErrClientNotFound
	assert.False(t, hub.isRetryableError(err), "ErrClientNotFound 不应可重试")
	assert.False(t, IsQueueFullError(err), "不应误判为队列满")
	assert.False(t, IsUserOfflineError(err), "不应误判为离线")
	assert.Equal(t, ErrTypeClientNotFound, errorx.ClassifyError(err))

	// 同样验证 client/wsc.go 直接 return 的 ErrMessageBufferFull 应可重试+队列满
	retryable := models.ErrMessageBufferFull
	assert.True(t, hub.isRetryableError(retryable), "ErrMessageBufferFull 应可重试")
	assert.True(t, IsQueueFullError(retryable), "ErrMessageBufferFull 应识别为队列满")
}

// ============================================================================
// 测试专用 mock：捕获 ctx 路由元数据的离线 handler
// ============================================================================

// nsCapturingOfflineHandler 嵌入 fakeOfflineHandler 复用其余方法，
// 仅覆盖 StoreOfflineMessage 捕获入口注入的 namespace/groupIDs 用于断言
type nsCapturingOfflineHandler struct {
	fakeOfflineHandler
	mu         sync.Mutex
	lastNS     string
	lastGroups []string
	storeCount int
}

func (n *nsCapturingOfflineHandler) StoreOfflineMessage(ctx context.Context, userID string, msg *HubMessage) error {
	n.mu.Lock()
	n.lastNS = routing.NamespaceFromContext(ctx)
	n.lastGroups = routing.GroupIDsFromContext(ctx)
	n.storeCount++
	n.mu.Unlock()
	return n.fakeOfflineHandler.StoreOfflineMessage(ctx, userID, msg)
}

// snapshot 返回捕获到的 namespace、groupIDs 与存储调用次数
func (n *nsCapturingOfflineHandler) snapshot() (string, []string, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastNS, n.lastGroups, n.storeCount
}

// ============================================================================
// SendToUserWithRetry 入口注入测试
// ============================================================================

// TestSendToUserWithRetry_InjectDefaultNamespaceForLegacy 老系统不传 namespace 时
// 入口应注入 DefaultNamespace，且 group 保持 nil（P2P 不与群组逻辑捆绑）
func TestSendToUserWithRetry_InjectDefaultNamespaceForLegacy(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	h := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(h)
	defer hub.SafeShutdown()

	// context.Background() 模拟老系统不传 namespace/group，用户离线走存储路径
	hub.SendToUserWithRetry(context.Background(), "u-offline-legacy", makeGroupMessage("sender"))

	ns, groups, count := h.snapshot()
	require.Equal(t, 1, count, "离线用户应触发一次 StoreOfflineMessage")
	assert.Equal(t, models.DefaultNamespace, ns, "老系统不传 namespace 应注入 DefaultNamespace")
	assert.Empty(t, groups, "P2P 发送方法 group 不参与，应保持空")
}

// TestSendToUserWithRetry_PreservesExistingNamespace ctx 已有 namespace 时不应被覆盖
func TestSendToUserWithRetry_PreservesExistingNamespace(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	h := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(h)
	defer hub.SafeShutdown()

	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-custom", nil)
	hub.SendToUserWithRetry(ctx, "u-offline-custom", makeGroupMessage("sender"))

	ns, groups, count := h.snapshot()
	require.Equal(t, 1, count)
	assert.Equal(t, "ns-custom", ns, "ctx 已有 namespace 不应被覆盖")
	assert.Empty(t, groups, "P2P 方法 group 不参与")
}

// ============================================================================
// SendToUserWithAck 入口注入测试
// ============================================================================

// TestSendToUserWithAck_InjectDefaultNamespaceForLegacy ACK 路径同样注入 DefaultNamespace
// 无论 EnableAck 取值，离线用户最终都会走 StoreOfflineMessage，可据此断言注入结果
func TestSendToUserWithAck_InjectDefaultNamespaceForLegacy(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	h := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(h)
	defer hub.SafeShutdown()

	hub.SendToUserWithAck(context.Background(), "u-offline-ack", makeGroupMessage("sender"), time.Second, 1)

	ns, groups, count := h.snapshot()
	require.Equal(t, 1, count, "ACK 路径离线用户应触发一次 StoreOfflineMessage")
	assert.Equal(t, models.DefaultNamespace, ns, "ACK 入口同样应注入 DefaultNamespace")
	assert.Empty(t, groups, "P2P 发送方法 group 不参与")
}

// TestSendToUserWithAck_PreservesExistingNamespace ACK 路径不覆盖已有 namespace
func TestSendToUserWithAck_PreservesExistingNamespace(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	h := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(h)
	defer hub.SafeShutdown()

	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-ack", nil)
	hub.SendToUserWithAck(ctx, "u-offline-ack2", makeGroupMessage("sender"), time.Second, 1)

	ns, _, count := h.snapshot()
	require.Equal(t, 1, count)
	assert.Equal(t, "ns-ack", ns, "ACK 入口不应覆盖已有 namespace")
}

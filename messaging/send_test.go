/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-06-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-06-25 10:56:20
 * @FilePath: \go-wsc\messaging\send_test.go
 * @Description: Hub 发送核心路径白盒单元测试（覆盖 hub/send.go）
 *
 * 复用 group_test.go 中的 setupGroupTestHub / makeTestClient / makeGroupMessage 等 helper。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================\r\n// SendToClientSerialized 测试
// ============================================================================

// TestSendToClientSerializedClosedClient 验证客户端已标记关闭时直接返回，不 panic 且不投递
func TestSendToClientSerializedClosedClient(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-closed", "u-closed")
	client.MarkClosed()
	msg := makeGroupMessage("sender")

	assert.NotPanics(t, func() {
		m.SendToClientSerialized(host.Context(), client, msg, nil)
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
	m, host := newTestManager()

	client := makeTestClient("c-chan-closed", "u-chan-closed")
	close(client.SendChan) // 实际关闭 channel，IsClosed 仍为 false，走 TrySend 的 recover 路径
	msg := makeGroupMessage("sender")

	assert.NotPanics(t, func() {
		m.SendToClientSerialized(host.Context(), client, msg, nil)
	})
	assert.True(t, client.IsClosed(), "TrySend recover 后应标记为已关闭")
}

// TestSendToClientSerializedNormalWS 验证 WS 客户端正常投递：SendChan 收到帧且 trackReceiverMessageStats 触发计数
func TestSendToClientSerializedNormalWS(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-normal", "u-normal")
	msg := makeGroupMessage("sender")
	msg.MessageID = "msg-stats-1"

	m.SendToClientSerialized(host.Context(), client, msg, nil)

	// SendChan 应收到序列化帧
	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data)
		var got models.HubMessage
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, "msg-stats-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("WS 客户端未收到消息帧")
	}

	// 接收方统计经 Host 端口上报（编排层转 stats 域落库），替身断言投递方完成上报
	require.Eventually(t, func() bool {
		e, ok := host.findTrackedStat("c-normal")
		return ok && e.messages == 1 && e.bytes > 0
	}, 3*time.Second, 50*time.Millisecond, "trackReceiverMessageStats 应经 Host 端口上报")
}

// TestSendToClientSerializedTrySendFail 验证 SendChan 已满时 TrySend 失败，不 panic 且不新增投递
func TestSendToClientSerializedTrySendFail(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-full", "u-full")
	// 填满 SendChan（缓冲 16），使后续 TrySend 走 default 返回 false
	for i := 0; i < cap(client.SendChan); i++ {
		client.SendChan <- []byte("filler")
	}
	msg := makeGroupMessage("sender")

	assert.NotPanics(t, func() {
		m.SendToClientSerialized(host.Context(), client, msg, nil)
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
	m, host := newTestManager()

	client := makeTestClient("c-pre", "u-pre")
	msg := makeGroupMessage("sender")
	msg.MessageID = "msg-pre-1"
	pre, err := json.Marshal(msg)
	require.NoError(t, err)

	m.SendToClientSerialized(host.Context(), client, msg, pre)

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
	m, _ := newTestManager()

	assert.NotPanics(t, func() {
		m.SendToAllClientsInMap(map[string]*models.Client{}, makeGroupMessage("sender"))
	})
}

// TestSendToAllClientsInMapMultiple 验证多客户端均收到消息
func TestSendToAllClientsInMapMultiple(t *testing.T) {
	m, _ := newTestManager()

	c1 := makeTestClient("c1", "u1")
	c2 := makeTestClient("c2", "u2")
	c3 := makeTestClient("c3", "u3")
	clientMap := map[string]*models.Client{c1.ID: c1, c2.ID: c2, c3.ID: c3}

	m.SendToAllClientsInMap(clientMap, makeGroupMessage("sender"))

	for _, c := range []*models.Client{c1, c2, c3} {
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
	m, _ := newTestManager()

	errs := m.SendToMultipleUsers(context.Background(), nil, makeGroupMessage("sender"))
	assert.Empty(t, errs)
}

// TestSendToMultipleUsersOnlineAndOffline 验证混合在线/离线用户：在线收到、离线报错
func TestSendToMultipleUsersOnlineAndOffline(t *testing.T) {
	m, host := newTestManager()

	// 在线用户
	online := makeTestClient("c-online", "u-online")
	host.GetShardedRegistry().AddClient(online)

	msg := makeGroupMessage("sender")
	errs := m.SendToMultipleUsers(context.Background(), []string{"u-online", "u-offline"}, msg)

	// 离线用户（无 handler）应在错误 map 中
	require.Contains(t, errs, "u-offline")
	// 使用 ClassifyError 判定错误类型（models.IsUserOfflineError 因源码 Type()/GetType() 不匹配而失效）
	assert.Equal(t, models.ErrTypeUserOffline, errorx.ClassifyError(errs["u-offline"]))
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
	m, _ := newTestManager()

	results := m.SendToClientsWithRetry(context.Background(), nil, makeGroupMessage("sender"), 1)
	assert.Empty(t, results)
}

// ============================================================================
// SendToGroupMembers 测试
// ============================================================================

// TestSendToGroupMembersExcludeSender 验证排除发送者：发送者不收到，其他在线成员收到，离线成员计失败
func TestSendToGroupMembersExcludeSender(t *testing.T) {
	m, host := newTestManager()

	sender := makeTestClient("c-sender", "u-sender")
	other := makeTestClient("c-other", "u-other")
	host.GetShardedRegistry().AddClient(sender)
	host.GetShardedRegistry().AddClient(other)

	msg := makeGroupMessage("u-sender")
	// 成员含 sender、other、offline-user
	result := m.SendToGroupMembers(context.Background(),
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
	m, host := newTestManager()

	customer := makeTestClient("c-customer", "u-customer")
	agent := makeTestClient("c-agent", "u-agent")
	agent.UserType = models.UserTypeAgent
	host.GetShardedRegistry().AddClient(customer)
	host.GetShardedRegistry().AddClient(agent)

	msg := makeGroupMessage("sender")
	// 仅投递给 Agent 类型客户端
	delivered := m.SendConditional(context.Background(), func(c *models.Client) bool {
		return c.UserType == models.UserTypeAgent
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
	m, _ := newTestManager()

	t.Run("nil错误返回false", func(t *testing.T) {
		assert.False(t, m.isRetryableError(nil))
	})
	t.Run("可重试错误返回true", func(t *testing.T) {
		assert.True(t, m.isRetryableError(models.ErrQueueAndPendingFull))
	})
	t.Run("不可重试错误返回false", func(t *testing.T) {
		// 修复 sentinel 初始化顺序后，sentinel 与运行时错误均应正确判定为不可重试
		assert.False(t, m.isRetryableError(models.ErrClientNotFound), "models.ErrClientNotFound sentinel 不应可重试")
		nonRetryable := errorx.NewError(models.ErrTypeClientNotFound)
		assert.False(t, m.isRetryableError(nonRetryable))
	})
	t.Run("普通error返回false", func(t *testing.T) {
		assert.False(t, m.isRetryableError(errors.New("plain error")))
	})
}

// ============================================================================
// SendToUserWithRetry 测试
// ============================================================================

// TestSendToUserWithRetryOfflineNoHandler 验证离线用户无 offlineMessageHandler 时返回 FinalError
func TestSendToUserWithRetryOfflineNoHandler(t *testing.T) {
	m, _ := newTestManager()

	msg := makeGroupMessage("sender")
	result := m.SendToUserWithRetry(context.Background(), "u-offline-noop", msg)

	assert.False(t, result.Success)
	require.NotNil(t, result.FinalError)
	// 修复 Is*Error 后，models.IsUserOfflineError 对运行时离线错误正确返回 true
	assert.True(t, models.IsUserOfflineError(result.FinalError), "应识别为用户离线错误")
	assert.Equal(t, models.ErrTypeUserOffline, errorx.ClassifyError(result.FinalError))
	assert.False(t, result.StoredOffline)
}

// TestSendToUserWithRetryOnline 验证在线用户发送成功并送达
func TestSendToUserWithRetryOnline(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-online", "u-online")
	host.GetShardedRegistry().AddClient(client)

	msg := makeGroupMessage("sender")
	result := m.SendToUserWithRetry(context.Background(), "u-online", msg)

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
	m, host := newTestManager()

	high := makeTestClient("c-high", "u-high")
	normal := makeTestClient("c-normal", "u-normal")
	host.GetShardedRegistry().AddClient(high)
	host.GetShardedRegistry().AddClient(normal)

	// 高优先级走异步 goroutine
	m.SendPriority(context.Background(), "u-high", makeGroupMessage("sender"), models.PriorityHigh)
	// 普通优先级走标准同步流程
	m.SendPriority(context.Background(), "u-normal", makeGroupMessage("sender"), models.PriorityNormal)

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
	m, host := newTestManager()

	t.Run("无sender直接返回", func(t *testing.T) {
		c := makeTestClient("c1", "u1")
		host.GetShardedRegistry().AddClient(c)
		msg := makeGroupMessage("")
		assert.NotPanics(t, func() {
			m.syncToSenderDevices(host.Context(), msg)
		})
		select {
		case <-c.SendChan:
			t.Fatal("无 sender 不应同步")
		default:
		}
	})

	t.Run("单设备不回环", func(t *testing.T) {
		c := makeTestClient("c-single", "u-single")
		host.GetShardedRegistry().AddClient(c)
		msg := makeGroupMessage("u-single")
		msg.SenderClient = "c-single"
		m.syncToSenderDevices(host.Context(), msg)
		select {
		case <-c.SendChan:
			t.Fatal("发送者自身设备不应收到回环消息")
		case <-time.After(200 * time.Millisecond):
		}
	})

	t.Run("多设备其他设备收到", func(t *testing.T) {
		dev1 := makeTestClient("c-dev1", "u-multi")
		dev2 := makeTestClient("c-dev2", "u-multi")
		host.GetShardedRegistry().AddClient(dev1)
		host.GetShardedRegistry().AddClient(dev2)

		msg := makeGroupMessage("u-multi")
		msg.SenderClient = "c-dev1" // dev1 为发送设备，dev2 应收到同步
		// 与生产入口契约一致：上游 InjectRoute 注入路由信封（appID 归一化为 DefaultAppID），
		// syncToSenderDevices 内部 ForEachUserClientFiltered 按 msg.AppID 严格匹配
		msg.InjectRoute(host.Context())
		m.syncToSenderDevices(host.Context(), msg)

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
// invokeMessageSendCallback 测试
// ============================================================================

// TestInvokeMessageSendCallbackNil 验证 callback 为 nil 时跳过不 panic
func TestInvokeMessageSendCallbackNil(t *testing.T) {
	m, _ := newTestManager()

	msg := makeGroupMessage("sender")
	result := &models.SendResult{Success: true}
	assert.NotPanics(t, func() {
		m.invokeMessageSendCallback(msg, result)
	})
}

// TestInvokeMessageSendCallbackNonHumanReceiverType 验证 ReceiverType 非人类时跳过回调
func TestInvokeMessageSendCallbackNonHumanReceiverType(t *testing.T) {
	m, _ := newTestManager()

	called := make(chan struct{}, 1)
	m.WithMessageSendCallback(func(_ *models.HubMessage, _ *models.SendResult) {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	// ReceiverType 为机器人（非人类），回调应被跳过
	msg := makeGroupMessage("sender")
	msg.ReceiverType = models.UserTypeBot
	m.invokeMessageSendCallback(msg, &models.SendResult{Success: true})

	select {
	case <-called:
		t.Fatal("非人类 ReceiverType 不应触发回调")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestInvokeMessageSendCallbackHumanInvoked 验证人类/空 ReceiverType 时回调被调用
func TestInvokeMessageSendCallbackHumanInvoked(t *testing.T) {
	m, _ := newTestManager()

	called := make(chan struct{}, 1)
	m.WithMessageSendCallback(func(_ *models.HubMessage, _ *models.SendResult) {
		select {
		case called <- struct{}{}:
		default:
		}
	})

	// ReceiverType 为空（向后兼容，视为人类）
	msg := makeGroupMessage("sender")
	m.invokeMessageSendCallback(msg, &models.SendResult{Success: true})

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("人类 ReceiverType 应触发回调")
	}
}

// TestSendScenario_OfflineNoHandlerClassifiedCorrectly 验证离线用户无 handler 时：
// 运行时创建的离线错误被 models.IsUserOfflineError 正确识别（修复前 Type() 断言失败导致误判 false），
// 且不进入重试（离线路径在重试循环之前返回）。
func TestSendScenario_OfflineNoHandlerClassifiedCorrectly(t *testing.T) {
	m, _ := newTestManager()

	result := m.SendToUserWithRetry(context.Background(), "u-not-online", makeGroupMessage("sender"))

	require.NotNil(t, result)
	assert.False(t, result.Success)
	require.NotNil(t, result.FinalError)
	// 离线错误分类（修复后生效）
	assert.True(t, models.IsUserOfflineError(result.FinalError), "运行时离线错误应被识别")
	assert.Equal(t, models.ErrTypeUserOffline, errorx.ClassifyError(result.FinalError))
	// 离线不进入重试循环
	assert.Equal(t, 0, len(result.Attempts), "离线路径不应产生发送尝试")
	assert.False(t, models.IsRetryableError(result.FinalError), "离线错误不应可重试")
	assert.False(t, models.IsQueueFullError(result.FinalError))
}

// TestSendScenario_OfflineWithHandlerStored 验证离线用户配置 handler 时走离线存储路径。
func TestSendScenario_OfflineWithHandlerStored(t *testing.T) {
	m, _ := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline

	result := m.SendToUserWithRetry(context.Background(), "u-offline-store", makeGroupMessage("sender"))

	require.NotNil(t, result)
	assert.True(t, result.Success, "离线存储成功应标记 Success")
	assert.True(t, result.StoredOffline, "应标记 StoredOffline")
	assert.Nil(t, result.FinalError)
	assert.Equal(t, 1, log.getStoreCalled(), "离线存储应恰好发生一次")
}

// TestSendScenario_NonRetryableSentinelNotRetried 验证 hub/observer.go 直接 return 的
// models.ErrClientNotFound（sentinel）被正确分类为不可重试、非队列满，因此重试循环遇到它应立即停止。
// 修复前：所有 sentinel 互相相等（Type=0），models.IsRetryableError(models.ErrClientNotFound) 误判 true，
// 会导致对“客户端未找到”这种不可恢复错误进行无意义重试。
func TestSendScenario_NonRetryableSentinelNotRetried(t *testing.T) {
	m, _ := newTestManager()

	// 模拟 observer.go 直接返回 sentinel 的场景
	err := models.ErrClientNotFound
	assert.False(t, m.isRetryableError(err), "models.ErrClientNotFound 不应可重试")
	assert.False(t, models.IsQueueFullError(err), "不应误判为队列满")
	assert.False(t, models.IsUserOfflineError(err), "不应误判为离线")
	assert.Equal(t, models.ErrTypeClientNotFound, errorx.ClassifyError(err))

	// 同样验证 client/wsc.go 直接 return 的 ErrMessageBufferFull 应可重试+队列满
	retryable := models.ErrMessageBufferFull
	assert.True(t, m.isRetryableError(retryable), "ErrMessageBufferFull 应可重试")
	assert.True(t, models.IsQueueFullError(retryable), "ErrMessageBufferFull 应识别为队列满")
}

// ============================================================================
// SendToUserWithRetry 入口注入测试
// ============================================================================

// TestSendToUserWithRetry_InjectDefaultNamespaceForLegacy 老系统不传 namespace 时
// 入口应注入 DefaultNamespace（回归断言从离线队列 key 的 ns 维度读取，P2P 补默认组）
func TestSendToUserWithRetry_InjectDefaultNamespaceForLegacy(t *testing.T) {
	m, _ := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline

	// context.Background() 模拟老系统不传 namespace/group，用户离线走存储路径
	m.SendToUserWithRetry(context.Background(), "u-offline-legacy", makeGroupMessage("sender"))

	require.Equal(t, 1, log.getStoreCalled(), "离线用户应触发一次 StoreOfflineMessage")
	ns, groupID := log.lastKeyDimension()
	assert.Equal(t, constants.DefaultNamespace, ns, "老系统不传 namespace 应注入 DefaultNamespace")
	assert.Equal(t, constants.DefaultGroupID, groupID, "P2P 离线存储应补默认组维度")
}

// TestSendToUserWithRetry_PreservesExistingNamespace ctx 已有 namespace 时不应被覆盖
func TestSendToUserWithRetry_PreservesExistingNamespace(t *testing.T) {
	m, _ := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline

	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-custom").WithGroupIDs(nil).Inject(context.Background())
	m.SendToUserWithRetry(ctx, "u-offline-custom", makeGroupMessage("sender"))

	require.Equal(t, 1, log.getStoreCalled())
	ns, _ := log.lastKeyDimension()
	assert.Equal(t, "ns-custom", ns, "ctx 已有 namespace 不应被覆盖")
}

// ============================================================================
// SendToClientSerialized 返回值测试（跨节点 PubSub 路径依赖返回值统计投递成败）
// ============================================================================

// TestSendToClientSerialized_ReturnsTrueOnSuccess WebSocket 正常投递应返回 true 且数据入通道
func TestSendToClientSerialized_ReturnsTrueOnSuccess(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager()

	client := makeTestClient("c-ser-ok", "u-ser-ok")
	msg := makeGroupMessage("sender")

	ok := m.SendToClientSerialized(context.Background(), client, msg, nil)

	assert.True(t, ok, "投递成功应返回 true")
	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "客户端通道应收到序列化数据")
	default:
		t.Fatal("客户端通道应收到数据")
	}
}

// TestSendToClientSerialized_ReturnsFalseWhenClosed 客户端已关闭应返回 false 且不投递
func TestSendToClientSerialized_ReturnsFalseWhenClosed(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager()

	client := makeTestClient("c-ser-closed", "u-ser-closed")
	client.MarkClosed()

	ok := m.SendToClientSerialized(context.Background(), client, makeGroupMessage("sender"), nil)

	assert.False(t, ok, "客户端已关闭应返回 false")
	assert.Empty(t, len(client.SendChan), "已关闭客户端不应收到数据")
}

// TestSendToClientSerialized_ReturnsFalseWhenChannelFull WebSocket 通道满应返回 false
func TestSendToClientSerialized_ReturnsFalseWhenChannelFull(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager()

	client := makeTestClient("c-ser-full", "u-ser-full")
	client.SendChan = make(chan []byte, 1)
	client.SendChan <- []byte("filler") // 填满缓冲区

	ok := m.SendToClientSerialized(context.Background(), client, makeGroupMessage("sender"), nil)

	assert.False(t, ok, "通道满应返回 false")
}

// TestSendToClientSerialized_SSEReturnsTrueAndDelivers SSE 客户端应走 SSE 通道投递 *models.HubMessage 并返回 true
func TestSendToClientSerialized_SSEReturnsTrueAndDelivers(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager()

	client := makeTestClient("c-ser-sse", "u-ser-sse")
	client.ConnectionType = models.ConnectionTypeSSE
	client.WithSSEChannels(make(chan *models.HubMessage, 1), make(chan struct{}))

	msg := makeGroupMessage("sender")
	msg.MessageID = "m-sse-ser"

	ok := m.SendToClientSerialized(context.Background(), client, msg, nil)

	assert.True(t, ok, "SSE 投递成功应返回 true")
	select {
	case received := <-client.SSEMessageCh:
		require.NotNil(t, received)
		assert.Equal(t, "m-sse-ser", received.MessageID, "SSE 通道应收到原始消息对象")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：SSE 通道未收到消息")
	}
}

// TestSendToClientSerialized_SSEChannelFullReturnsFalse SSE 通道满应返回 false
func TestSendToClientSerialized_SSEChannelFullReturnsFalse(t *testing.T) {
	t.Parallel()
	m, _ := newTestManager()

	client := makeTestClient("c-ser-sse-full", "u-ser-sse-full")
	client.ConnectionType = models.ConnectionTypeSSE
	sseCh := make(chan *models.HubMessage, 1)
	sseCh <- makeGroupMessage("filler") // 填满 SSE 缓冲区
	client.WithSSEChannels(sseCh, make(chan struct{}))

	ok := m.SendToClientSerialized(context.Background(), client, makeGroupMessage("sender"), nil)

	assert.False(t, ok, "SSE 通道满应返回 false")
}

// ============================================================================
// sendToUser write-ahead（outbox）行为测试
// 修复前：Publish/本地投递之后才异步创建 sending 记录，投递侧的状态回报
// UPDATE 可能扑空（记录未落库）→ 永久停留 sending → 被 ACK 兜底误标 + 重复转离线
// 修复后：先提交记录创建任务，再执行投递，状态回报可正常覆盖
// ============================================================================

// TestSendToUser_WriteAheadRecordCreatedBeforeDelivery
// sendToUser 本地投递路径：sending 记录被异步创建，投递成功后状态被回报覆盖为 success
func TestSendToUser_WriteAheadRecordCreatedBeforeDelivery(t *testing.T) {
	t.Parallel()
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	client := makeTestClient("c-wa-local", "u-wa-local")
	host.GetShardedRegistry().AddClient(client)

	msg := makeGroupMessage("sender")
	msg.MessageID = "m-wa-local"
	msg.Receiver = "u-wa-local"

	err := m.sendToUser(context.Background(), "u-wa-local", msg, nil)
	require.NoError(t, err)

	// 记录被 outbox 攒批创建（恰好一条）
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.createdRecords) == 1
	}, 2*time.Second, 10*time.Millisecond, "write-ahead 记录应被攒批创建")

	// 投递成功后终态可达 success，两条路径均为合法：
	//   a) outbox 合并：投递回报先于 flush，INSERT 直接带 success 终态（攒批竞态消除）
	//   b) statusUpdater 回报：flush 先于投递完成，UPDATE 命中已落库的 sending 记录
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		mergedSuccess := len(repo.createdRecords) == 1 &&
			repo.createdRecords[0].Status == models.MessageSendStatusSuccess
		repo.batchUpdateMu.Unlock()
		if mergedSuccess {
			return true // 路径 a：合并终态
		}
		return hasBatchUpdate(repo, "m-wa-local", models.MessageSendStatusSuccess) // 路径 b：UPDATE 回报
	}, 2*time.Second, 10*time.Millisecond, "本地投递成功应使记录终态为 success")
}

// TestSendToUser_WriteAheadRecordCreatedForRoutedMessage
// write-ahead 记录的创建不依赖路由结果：无论消息最终路由到其他节点还是本地投递，
// 记录创建都在投递决策之前提交（恰好一条，不重复）
func TestSendToUser_WriteAheadRecordCreatedForRoutedMessage(t *testing.T) {
	t.Parallel()
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	client := makeTestClient("c-wa-route", "u-wa-route")
	client.NodeID = "other-node"
	host.GetShardedRegistry().AddClient(client)

	msg := makeGroupMessage("sender")
	msg.MessageID = "m-wa-route"
	msg.Receiver = "u-wa-route"

	// 单机无 pubsub：checkAndRouteToNode 不跨节点路由，走本地投递
	err := m.sendToUser(context.Background(), "u-wa-route", msg, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		return len(repo.createdRecords) == 1
	}, 2*time.Second, 10*time.Millisecond, "write-ahead 记录应在投递决策前提交创建")

	repo.batchUpdateMu.Lock()
	created := repo.createdRecords[0]
	repo.batchUpdateMu.Unlock()
	assert.Equal(t, "m-wa-route", created.MessageID, "任意投递路径都应恰好创建一条记录")
}

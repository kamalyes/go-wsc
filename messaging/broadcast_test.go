/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 11:50:00
 * @FilePath: \go-wsc\messaging\broadcast_test.go
 * @Description: 消息域广播功能白盒单元测试（覆盖 messaging/broadcast.go）
 *
 * 复用 fake_host_test.go 中的 newTestManager / newStatusRecordingManager /
 * makeTestClient / makeSSEClient / makeAgentClient / makeVIPClient / makeGroupMessage 等 helper
 *
 * 覆盖场景：
 *   1. BroadcastToFiltered（WS+SSE 投递/条件过滤/路由信封隔离/序列化失败/关闭客户端跳过/状态回报）
 *   2. BroadcastToUserIDs（空列表/WS+SSE 投递/关闭客户端跳过/路由隔离）
 *   3. BroadcastToNamespace（nsIdx 索引路径/自适应回退/空 ns 守卫/空命中/appID 隔离）
 *   4. 广播消息 ID 生成（广播纯雪花 ID/P2P 前缀 ID/HandleBroadcastMessage 幂等）
 *   5. BroadcastByUserType / BroadcastToRole / BroadcastToClientType / BroadcastToDepartment
 *   6. BroadcastExclude
 *   7. GetClientsByUserType / GetClientsByRole / GetClientsByClientType / GetClientsByDepartment / GetClientsByVIPLevel
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// BroadcastToFiltered 测试
// ============================================================================

// TestBroadcastToFiltered_WSAndSSE 验证 WS 和 SSE 客户端均收到消息
func TestBroadcastToFiltered_WSAndSSE(t *testing.T) {
	m, host := newTestManager()

	wsClient := makeTestClient("c-ws", "u-ws")
	sseClient := makeSSEClient("c-sse", "u-sse")
	host.GetShardedRegistry().AddClient(wsClient)
	host.GetShardedRegistry().AddClient(sseClient)

	msg := makeGroupMessage("sender")
	msg.MessageID = "filtered-1"

	// 注入路由使 namespace 匹配（makeTestClient 默认 ns=default）
	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true // 全匹配
	}, msg)

	assert.Equal(t, 2, delivered)

	// WS 客户端收到序列化数据
	select {
	case data := <-wsClient.SendChan:
		var got models.HubMessage
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, "filtered-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("WS 客户端未收到消息")
	}

	// SSE 客户端收到 msg 对象
	select {
	case got := <-sseClient.SSEMessageCh:
		assert.Equal(t, "filtered-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("SSE 客户端未收到消息")
	}
}

// TestBroadcastToFiltered_ConditionFilter 验证条件过滤：false 的客户端不收到
func TestBroadcastToFiltered_ConditionFilter(t *testing.T) {
	m, host := newTestManager()

	matched := makeTestClient("c-match", "u-match")
	unmatched := makeTestClient("c-unmatch", "u-unmatch")
	host.GetShardedRegistry().AddClient(matched)
	host.GetShardedRegistry().AddClient(unmatched)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return c.UserID == "u-match"
	}, msg)

	assert.Equal(t, 1, delivered)

	select {
	case <-matched.SendChan:
	default:
		t.Fatal("匹配客户端应收到消息")
	}
	select {
	case <-unmatched.SendChan:
		t.Fatal("不匹配客户端不应收到消息")
	default:
	}
}

// TestBroadcastToFiltered_NamespaceIsolation 验证 namespace 路由信封隔离
func TestBroadcastToFiltered_NamespaceIsolation(t *testing.T) {
	m, host := newTestManager()

	ns1Client := makeTestClient("c-ns1", "u-ns1", "ns-alpha")
	ns2Client := makeTestClient("c-ns2", "u-ns2", "ns-beta")
	host.GetShardedRegistry().AddClient(ns1Client)
	host.GetShardedRegistry().AddClient(ns2Client)

	// 向 ns-alpha 广播
	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-alpha").WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)

	assert.Equal(t, 1, delivered, "仅 ns-alpha 客户端应收到")

	select {
	case <-ns1Client.SendChan:
	default:
		t.Fatal("ns-alpha 客户端应收到消息")
	}
	select {
	case <-ns2Client.SendChan:
		t.Fatal("ns-beta 客户端不应收到 ns-alpha 的消息")
	default:
	}
}

// TestBroadcastToFiltered_GroupNotIsolated 验证 BroadcastToFiltered 不做业务群组隔离
//
// 设计说明（与 ClientMatchesEnvelope / ForEachUserClientFiltered 对称）：
//   - msg.GroupIDs 是"业务群组ID"（如 "group-a"），client.GroupID 是"连接级系统组"（如 __default_gp__）
//   - 两者完全两个维度，强行匹配会导致群成员设备全部被过滤（delivered=0）
//   - 群组隔离应通过 BroadcastToUserIDs（groupRepo.GetMembers 成员列表查找）实现
//   - BroadcastToFiltered 仅做 namespace 隔离，用于角色/类型/部门等属性过滤广播
func TestBroadcastToFiltered_GroupNotIsolated(t *testing.T) {
	m, host := newTestManager()

	g1Client := makeTestClient("c-g1", "u-g1", constants.DefaultNamespace, "group-a")
	g2Client := makeTestClient("c-g2", "u-g2", constants.DefaultNamespace, "group-b")
	host.GetShardedRegistry().AddClient(g1Client)
	host.GetShardedRegistry().AddClient(g2Client)

	// 向 group-a 广播（BroadcastToFiltered 仅做 namespace 隔离，不做业务群组隔离）
	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"group-a"}).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)

	// 同 namespace 不同 group 的客户端都应收到（BroadcastToFiltered 不隔离业务群组）
	assert.Equal(t, 2, delivered, "同 namespace 不同 group 的客户端都应收到（BroadcastToFiltered 仅做 namespace 隔离）")

	select {
	case <-g1Client.SendChan:
	default:
		t.Fatal("group-a 客户端应收到消息")
	}
	select {
	case <-g2Client.SendChan:
	default:
		t.Fatal("group-b 客户端也应收到消息（BroadcastToFiltered 不隔离业务群组）")
	}
}

// TestBroadcastToFiltered_EmptyNamespaceMatchesAll 验证 msg.Namespace 为空时跳过 ns 过滤（全局广播）
//
// 设计：ClientMatchesEnvelope 对 namespace 空值兼容——msgNamespace=="" 表示全局广播，
// 跳过 ns 过滤匹配所有客户端（跨命名空间的系统通知场景）
func TestBroadcastToFiltered_EmptyNamespaceMatchesAll(t *testing.T) {
	m, host := newTestManager()

	ns1Client := makeTestClient("c-ns1", "u-ns1", "ns-alpha")
	ns2Client := makeTestClient("c-ns2", "u-ns2", "ns-beta")
	host.GetShardedRegistry().AddClient(ns1Client)
	host.GetShardedRegistry().AddClient(ns2Client)

	// ctx 无路由 → InjectRoute 保留 namespace="" → ClientMatchesEnvelope 跳过 ns 过滤匹配所有
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(context.Background(), func(c *models.Client) bool {
		return true
	}, msg)

	assert.Equal(t, 2, delivered, "空 namespace 应匹配所有客户端（全局广播）")
}

// TestBroadcastToFiltered_ClosedClientSkipped 验证已关闭客户端被跳过
func TestBroadcastToFiltered_ClosedClientSkipped(t *testing.T) {
	m, host := newTestManager()

	closed := makeTestClient("c-closed", "u-closed")
	closed.MarkClosed()
	open := makeTestClient("c-open", "u-open")
	host.GetShardedRegistry().AddClient(closed)
	host.GetShardedRegistry().AddClient(open)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)

	assert.Equal(t, 1, delivered, "仅未关闭客户端应收到")

	select {
	case <-open.SendChan:
	default:
		t.Fatal("未关闭客户端应收到消息")
	}
}

// TestBroadcastToFiltered_TrySendFail 验证 SendChan 满时 TrySend 失败不计数
// （离线兜底因未注入 offlineHandler 仅计数告警，不影响 delivered 统计）
func TestBroadcastToFiltered_TrySendFail(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-full", "u-full")
	// 填满 SendChan（缓冲 16）
	for i := 0; i < cap(client.SendChan); i++ {
		client.SendChan <- []byte("filler")
	}
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)

	assert.Equal(t, 0, delivered, "SendChan 满时 TrySend 失败应返回 0")
}

// TestBroadcastToFiltered_NoMatchReturnsZero 验证无匹配客户端时返回 0 且不更新状态
func TestBroadcastToFiltered_NoMatchReturnsZero(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-1", "u-1")
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "no-match-1"

	// condition 永远返回 false
	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return false
	}, msg)

	assert.Equal(t, 0, delivered)
	// 无消息投递，SendChan 应为空
	select {
	case <-client.SendChan:
		t.Fatal("不匹配时不应投递")
	default:
	}
}

// TestBroadcastToFiltered_MarshalFailReturnsZero 验证序列化失败时返回 0
func TestBroadcastToFiltered_MarshalFailReturnsZero(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-1", "u-1")
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	// Data 中放入不可序列化的值（func），触发 json.Marshal 失败
	msg.Data = map[string]interface{}{
		"bad": func() {},
	}

	assert.NotPanics(t, func() {
		delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
			return true
		}, msg)
		assert.Equal(t, 0, delivered)
	})
}

// TestBroadcastToFiltered_UpdatesMessageStatus 验证成功投递后状态回报经 Host 端口批量落库
func TestBroadcastToFiltered_UpdatesMessageStatus(t *testing.T) {
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	client := makeTestClient("c-1", "u-1")
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "status-1"

	m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)

	// updateMessageStatusAsync 经 MessageStatusUpdater 批量更新，等待 flush 周期到达
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		for _, call := range repo.batchUpdateCalls {
			if call.Status == models.MessageSendStatusSuccess {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "成功投递应触发状态更新为 Success")
}

// ============================================================================
// BroadcastToUserIDs 测试
// ============================================================================

// TestBroadcastToUserIDs_EmptyListReturnsZero 验证空 userIDs 列表返回 0
func TestBroadcastToUserIDs_EmptyListReturnsZero(t *testing.T) {
	m, _ := newTestManager()

	msg := makeGroupMessage("sender")
	delivered := m.BroadcastToUserIDs(context.Background(), nil, msg)
	assert.Equal(t, 0, delivered)

	delivered = m.BroadcastToUserIDs(context.Background(), []string{}, msg)
	assert.Equal(t, 0, delivered)
}

// TestBroadcastToUserIDs_WSAndSSE 验证 WS 和 SSE 客户端均收到消息
func TestBroadcastToUserIDs_WSAndSSE(t *testing.T) {
	m, host := newTestManager()

	wsClient := makeTestClient("c-ws", "u-ws")
	sseClient := makeSSEClient("c-sse", "u-sse")
	host.GetShardedRegistry().AddClient(wsClient)
	host.GetShardedRegistry().AddClient(sseClient)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "uids-1"

	delivered := m.BroadcastToUserIDs(ctx, []string{"u-ws", "u-sse"}, msg)
	assert.Equal(t, 2, delivered)

	select {
	case data := <-wsClient.SendChan:
		var got models.HubMessage
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, "uids-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("WS 客户端未收到消息")
	}

	select {
	case got := <-sseClient.SSEMessageCh:
		assert.Equal(t, "uids-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("SSE 客户端未收到消息")
	}
}

// TestBroadcastFrameToUserIDs_WSAndSSE 验证预编码帧直发：WS 原样收到 frame 字节（零二次编码），
// SSE 经 msg 对象通道收到 wrapper（SSE 无预编码通道）
func TestBroadcastFrameToUserIDs_WSAndSSE(t *testing.T) {
	m, host := newTestManager()

	wsClient := makeTestClient("c-frame-ws", "u-frame-ws")
	sseClient := makeSSEClient("c-frame-sse", "u-frame-sse")
	host.GetShardedRegistry().AddClient(wsClient)
	host.GetShardedRegistry().AddClient(sseClient)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "frame-1"
	frame := []byte(`["p1","p2","p3"]`)

	delivered := m.BroadcastFrameToUserIDs(ctx, []string{"u-frame-ws", "u-frame-sse"}, msg, frame)
	assert.Equal(t, 2, delivered)

	select {
	case data := <-wsClient.SendChan:
		assert.JSONEq(t, `["p1","p2","p3"]`, string(data), "WS 应原样收到预编码数组帧，不经历二次 Marshal")
		assert.Equal(t, frame, data, "帧字节应逐字节一致（无转义膨胀）")
	case <-time.After(time.Second):
		t.Fatal("WS 客户端未收到预编码帧")
	}

	select {
	case got := <-sseClient.SSEMessageCh:
		assert.Equal(t, "frame-1", got.MessageID, "SSE 仍经 wrapper msg 对象通道")
	case <-time.After(time.Second):
		t.Fatal("SSE 客户端未收到消息")
	}
}

// TestBroadcastFrameToUserIDs_EmptyFrameFallsBack 验证空 frame 回退常规 Marshal 路径（防御未编码调用方）
func TestBroadcastFrameToUserIDs_EmptyFrameFallsBack(t *testing.T) {
	m, host := newTestManager()

	wsClient := makeTestClient("c-fb-ws", "u-fb-ws")
	host.GetShardedRegistry().AddClient(wsClient)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "fallback-1"

	delivered := m.BroadcastFrameToUserIDs(ctx, []string{"u-fb-ws"}, msg, nil)
	assert.Equal(t, 1, delivered)

	select {
	case data := <-wsClient.SendChan:
		var got models.HubMessage
		require.NoError(t, json.Unmarshal(data, &got))
		assert.Equal(t, "fallback-1", got.MessageID, "空 frame 应回退 Marshal(msg)")
	case <-time.After(time.Second):
		t.Fatal("WS 客户端未收到消息")
	}
}

// TestBroadcastToUserIDs_NamespaceIsolation 验证 namespace 路由隔离
func TestBroadcastToUserIDs_NamespaceIsolation(t *testing.T) {
	m, host := newTestManager()

	ns1Client := makeTestClient("c-ns1", "u-shared", "ns-alpha")
	ns2Client := makeTestClient("c-ns2", "u-shared", "ns-beta")
	host.GetShardedRegistry().AddClient(ns1Client)
	host.GetShardedRegistry().AddClient(ns2Client)

	// 向 ns-alpha 广播给 u-shared
	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-alpha").WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToUserIDs(ctx, []string{"u-shared"}, msg)
	assert.Equal(t, 1, delivered, "仅 ns-alpha 的 u-shared 客户端应收到")

	select {
	case <-ns1Client.SendChan:
	default:
		t.Fatal("ns-alpha 客户端应收到消息")
	}
	select {
	case <-ns2Client.SendChan:
		t.Fatal("ns-beta 客户端不应收到 ns-alpha 的消息")
	default:
	}
}

// TestBroadcastToUserIDs_ClosedClientSkipped 验证已关闭客户端被跳过
func TestBroadcastToUserIDs_ClosedClientSkipped(t *testing.T) {
	m, host := newTestManager()

	closed := makeTestClient("c-closed", "u-closed")
	closed.MarkClosed()
	open := makeTestClient("c-open", "u-open")
	host.GetShardedRegistry().AddClient(closed)
	host.GetShardedRegistry().AddClient(open)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToUserIDs(ctx, []string{"u-closed", "u-open"}, msg)
	assert.Equal(t, 1, delivered, "仅未关闭客户端应收到")

	select {
	case <-open.SendChan:
	default:
		t.Fatal("未关闭客户端应收到消息")
	}
}

// TestBroadcastToUserIDs_NonExistentUser 验证不存在的 userID 不影响投递
func TestBroadcastToUserIDs_NonExistentUser(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-1", "u-exist")
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	// 包含不存在 userID 和存在 userID
	delivered := m.BroadcastToUserIDs(ctx, []string{"u-nonexist", "u-exist"}, msg)
	assert.Equal(t, 1, delivered, "仅存在的用户应收到")

	select {
	case <-client.SendChan:
	default:
		t.Fatal("存在的用户应收到消息")
	}
}

// TestBroadcastToUserIDs_TrySendFail 验证 SendChan 满时 TrySend 失败不计数
// （离线兜底因未注入 offlineHandler 仅计数告警，不影响 delivered 统计）
func TestBroadcastToUserIDs_TrySendFail(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-full", "u-full")
	for i := 0; i < cap(client.SendChan); i++ {
		client.SendChan <- []byte("filler")
	}
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToUserIDs(ctx, []string{"u-full"}, msg)
	assert.Equal(t, 0, delivered, "SendChan 满时 TrySend 失败应返回 0")
}

// TestBroadcastToUserIDs_MarshalFailReturnsZero 验证序列化失败时返回 0
func TestBroadcastToUserIDs_MarshalFailReturnsZero(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-1", "u-1")
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.Data = map[string]interface{}{
		"bad": func() {},
	}

	assert.NotPanics(t, func() {
		delivered := m.BroadcastToUserIDs(ctx, []string{"u-1"}, msg)
		assert.Equal(t, 0, delivered)
	})
}

// ============================================================================
// BroadcastToNamespace 测试（nsIdx 反向索引定点扇出 + 大命中率自适应回退）
// ============================================================================

// 路径选择的白盒锚点：两条路径存在可观测差异——
// 索引路径（BroadcastToUserIDs → broadcastToUserIDsNow）不更新消息状态，
// 回退路径（BroadcastToFiltered → broadcastToFilteredNow）成功投递后批量回报 Success

// TestBroadcastToNamespace_IndexedDelivery 低命中率（3 用户/8 连接）走 nsIdx 索引路径：
// 仅目标 ns 的 WS+SSE 客户端收到，其他 ns 不受影响，且不触发消息状态回报
func TestBroadcastToNamespace_IndexedDelivery(t *testing.T) {
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	targetWS1 := makeTestClient("c-t1", "u-t1", "ns-target")
	targetWS2 := makeTestClient("c-t2", "u-t2", "ns-target")
	targetSSE := makeSSEClient("c-t3", "u-t3")
	targetSSE.Namespace = "ns-target"
	host.GetShardedRegistry().AddClient(targetWS1)
	host.GetShardedRegistry().AddClient(targetWS2)
	host.GetShardedRegistry().AddClient(targetSSE)

	// 其他 ns 用户 5 个：总连接 8，命中用户 3，3*100 < 8*50 → 索引路径
	otherClients := make([]*models.Client, 0, 5)
	for i := 1; i <= 5; i++ {
		c := makeTestClient(fmt.Sprintf("c-o%d", i), fmt.Sprintf("u-o%d", i), "ns-other")
		host.GetShardedRegistry().AddClient(c)
		otherClients = append(otherClients, c)
	}

	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-target").WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "ns-idx-1"

	delivered := m.BroadcastToNamespace(ctx, "ns-target", msg)
	assert.Equal(t, 3, delivered)

	// 目标 ns 的 WS 客户端收到序列化数据
	for _, ws := range []*models.Client{targetWS1, targetWS2} {
		select {
		case data := <-ws.SendChan:
			var got models.HubMessage
			require.NoError(t, json.Unmarshal(data, &got))
			assert.Equal(t, "ns-idx-1", got.MessageID)
		case <-time.After(time.Second):
			t.Fatal("目标 ns 的 WS 客户端应收到消息")
		}
	}

	// 目标 ns 的 SSE 客户端收到 msg 对象
	select {
	case got := <-targetSSE.SSEMessageCh:
		assert.Equal(t, "ns-idx-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("目标 ns 的 SSE 客户端应收到消息")
	}

	// 其他 ns 客户端不收到
	for _, c := range otherClients {
		select {
		case <-c.SendChan:
			t.Fatalf("其他 ns 客户端 %s 不应收到消息", c.ID)
		default:
		}
	}

	// 索引路径不触发状态回报（broadcastToUserIDsNow 无状态回报，区别于全量扫描路径）
	time.Sleep(150 * time.Millisecond) // 超过状态批量更新 flush 周期（20ms）
	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	for _, call := range repo.batchUpdateCalls {
		assert.NotEqual(t, models.MessageSendStatusSuccess, call.Status,
			"索引路径（BroadcastToUserIDs）不应触发状态回报")
	}
}

// TestBroadcastToNamespace_AdaptiveFallback 命中率 50%（4 用户/8 连接）触发自适应回退：
// 退回 BroadcastToFiltered 全量扫描路径，投递结果与索引路径一致，且成功投递触发状态回报
func TestBroadcastToNamespace_AdaptiveFallback(t *testing.T) {
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	// 目标 ns 4 用户 + 其他 ns 4 用户：4*100 >= 8*50 → 回退全量扫描
	for i := 1; i <= 4; i++ {
		host.GetShardedRegistry().AddClient(
			makeTestClient(fmt.Sprintf("c-t%d", i), fmt.Sprintf("u-t%d", i), "ns-half"))
	}
	for i := 1; i <= 4; i++ {
		host.GetShardedRegistry().AddClient(
			makeTestClient(fmt.Sprintf("c-o%d", i), fmt.Sprintf("u-o%d", i), "ns-other"))
	}

	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-half").WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "ns-fallback-1"

	delivered := m.BroadcastToNamespace(ctx, "ns-half", msg)
	assert.Equal(t, 4, delivered)

	// 回退路径成功投递后经状态批量更新器回报 Success（索引路径无状态回报，此处为白盒锚点）
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		for _, call := range repo.batchUpdateCalls {
			if call.Status == models.MessageSendStatusSuccess {
				return true
			}
		}
		return false
	}, 3*time.Second, 50*time.Millisecond, "回退路径成功投递应触发状态更新为 Success")
}

// TestBroadcastToNamespace_EmptyNamespaceGuard 空 namespace = 全局广播语义：
// 不属于本入口（由 deliverGlobally 全量扫描处理），直接返回 0 且不投递
func TestBroadcastToNamespace_EmptyNamespaceGuard(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-1", "u-1") // 默认 ns
	host.GetShardedRegistry().AddClient(client)

	msg := makeGroupMessage("sender")
	delivered := m.BroadcastToNamespace(context.Background(), "", msg)
	assert.Equal(t, 0, delivered)

	select {
	case <-client.SendChan:
		t.Fatal("空 ns 不应触发任何投递")
	default:
	}
}

// TestBroadcastToNamespace_NoLocalUsers 本地无该 ns 用户：索引空命中直接返回 0，不进入扇出
func TestBroadcastToNamespace_NoLocalUsers(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-1", "u-1", "ns-a")
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-empty").WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToNamespace(ctx, "ns-empty", msg)
	assert.Equal(t, 0, delivered)

	select {
	case <-client.SendChan:
		t.Fatal("其他 ns 客户端不应收到消息")
	default:
	}
}

// TestBroadcastToNamespace_AppIDIsolation 索引键 appID:namespace 双维度隔离：
// 跨 app 同 ns 的客户端不进入命中集（本例 2 用户/2 连接=100% 命中走回退路径，
// 回退路径的 ClientMatchesEnvelope 同样做 appID 严格隔离，两条路径行为一致）
func TestBroadcastToNamespace_AppIDIsolation(t *testing.T) {
	m, host := newTestManager()

	sameApp := makeTestClient("c-same", "u-same", "ns-mixed")
	otherApp := makeTestClient("c-other", "u-other", "ns-mixed")
	otherApp.AppID = "app-other"
	host.GetShardedRegistry().AddClient(sameApp)
	host.GetShardedRegistry().AddClient(otherApp)

	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-mixed").WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "ns-app-1"

	// ctx appID 空 → 归一化为 DefaultAppID：仅命中同 app 客户端
	delivered := m.BroadcastToNamespace(ctx, "ns-mixed", msg)
	assert.Equal(t, 1, delivered)

	select {
	case <-sameApp.SendChan:
	default:
		t.Fatal("同 app 客户端应收到消息")
	}
	select {
	case <-otherApp.SendChan:
		t.Fatal("跨 app 客户端不应收到消息")
	default:
	}
}

// ============================================================================
// 广播消息 Hub 内部 ID 生成测试
// ============================================================================

// 契约：每条消息都有唯一标识，不因非 ACK 路径缺席——
//   - 广播类（群组/命名空间/全局）：Deliver 入口生成纯雪花 ID（无单一接收者）
//   - P2P：send 路径生成 userID 前缀 ID（ACK 对账语义），Deliver 入口不抢先
//   - HandleBroadcastMessage（跨节点/队列消费入口）：幂等兜底，已有不覆盖

// receiveJSON 从客户端 SendChan 取一条序列化消息并解析为 map
// （fanout 路径预序列化，客户端视角验证投递副本的最终形态）
func receiveJSON(t *testing.T, c *models.Client) map[string]any {
	t.Helper()
	select {
	case data := <-c.SendChan:
		var payload map[string]any
		require.NoError(t, json.Unmarshal(data, &payload))
		return payload
	default:
		t.Fatalf("客户端 %s 应收到消息", c.UserID)
		return nil
	}
}

// TestDeliverBroadcastGeneratesHubID fire-and-forget 群组广播（跑马灯同型）生成 Hub 内部 ID
// Deliver 入口 Clone：ID 生成在投递副本上，调用方原对象零侵入——
// 经在线成员收到的序列化数据验证（修复前跑马灯消息 id 恒为空串）
// fire-and-forget 的成员定位走本地反向索引（客户端 GroupID 标签），不走 GroupStore
func TestDeliverBroadcastGeneratesHubID(t *testing.T) {
	m, host := newTestManager()

	cRecv := makeTestClient("c-recv", "u-recv", "tenantA", "g-idgen")
	host.GetShardedRegistry().AddClient(cRecv)

	msg := makeGroupMessage("owner1")
	require.Empty(t, msg.ID, "前置：构造时无 ID")

	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantA").WithGroupIDs([]string{"g-idgen"}).Inject(context.Background())
	result := m.Deliver(groupCtx, msg, false)
	require.NotNil(t, result)

	payload := receiveJSON(t, cRecv)
	assert.NotEmpty(t, payload["id"], "广播消息应生成 Hub 内部 ID（非 ACK 路径同样有唯一标识）")
	assert.Empty(t, msg.ID, "调用方原对象不受影响（Deliver 入口 Clone，ID 在投递副本上生成）")
}

// TestHandleBroadcastMessageGeneratesHubID 本地直投/跨节点消费入口：空 ID 就地生成，已有幂等不覆盖
func TestHandleBroadcastMessageGeneratesHubID(t *testing.T) {
	m, _ := newTestManager()

	msg := makeGroupMessage("owner2")
	require.Empty(t, msg.ID, "前置：构造时无 ID")

	m.HandleBroadcastMessage(context.Background(), msg)
	assert.NotEmpty(t, msg.ID, "广播入口应为空 ID 消息生成 Hub 内部 ID")

	idFirst := msg.ID
	m.HandleBroadcastMessage(context.Background(), msg)
	assert.Equal(t, idFirst, msg.ID, "已生成的 ID 不应被覆盖（幂等）")
}

// TestDeliverP2PKeepsSendPathPrefix P2P 分支不在 Deliver 入口生成：
// 客户端收到的 id 保持 send 路径的 userID 前缀格式（ACK 对账语义），非入口纯雪花
// 在线判定按 appID+namespace 匹配本地注册表，路由 ctx 须与客户端标签对齐
func TestDeliverP2PKeepsSendPathPrefix(t *testing.T) {
	m, host := newTestManager()

	cRecv := makeTestClient("c-p2p", "u-p2p", "tenantA")
	host.GetShardedRegistry().AddClient(cRecv)

	msg := makeGroupMessage("owner3")
	msg.Receiver = "u-p2p"

	p2pCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantA").Inject(context.Background())
	result := m.Deliver(p2pCtx, msg, false)
	require.NotNil(t, result)

	payload := receiveJSON(t, cRecv)
	id, _ := payload["id"].(string)
	assert.True(t, strings.HasPrefix(id, "u-p2p-"),
		"P2P 的 ID 应为 send 路径的 userID 前缀格式（实际: %q）", id)
}

// ============================================================================
// BroadcastByUserType / BroadcastToRole / BroadcastToClientType / BroadcastToDepartment 测试
// ============================================================================

// TestBroadcastByUserType 验证按用户类型广播
func TestBroadcastByUserType(t *testing.T) {
	m, host := newTestManager()

	customer1 := makeTestClient("c-c1", "u-c1")
	customer2 := makeTestClient("c-c2", "u-c2")
	agent := makeAgentClient("c-a1", "u-a1")
	host.GetShardedRegistry().AddClient(customer1)
	host.GetShardedRegistry().AddClient(customer2)
	host.GetShardedRegistry().AddClient(agent)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastByUserType(ctx, models.UserTypeCustomer, msg)
	assert.Equal(t, 2, delivered, "仅 Customer 类型客户端应收到")

	// 两个 customer 收到
	select {
	case <-customer1.SendChan:
	default:
		t.Fatal("customer1 应收到消息")
	}
	select {
	case <-customer2.SendChan:
	default:
		t.Fatal("customer2 应收到消息")
	}
	// agent 不收到
	select {
	case <-agent.SendChan:
		t.Fatal("agent 不应收到 Customer 类型广播")
	default:
	}
}

// TestBroadcastByUserType_NoMatch 验证无匹配类型返回 0
func TestBroadcastByUserType_NoMatch(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-1", "u-1")
	host.GetShardedRegistry().AddClient(client)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastByUserType(ctx, models.UserTypeBot, msg)
	assert.Equal(t, 0, delivered, "无 Bot 类型客户端应返回 0")
}

// TestBroadcastToRole 验证按角色广播
func TestBroadcastToRole(t *testing.T) {
	m, host := newTestManager()

	customer := makeTestClient("c-c", "u-c")
	customer.Role = models.UserRoleCustomer

	admin := makeTestClient("c-a", "u-a")
	admin.Role = models.UserRoleAdmin

	host.GetShardedRegistry().AddClient(customer)
	host.GetShardedRegistry().AddClient(admin)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToRole(ctx, models.UserRoleAdmin, msg)
	assert.Equal(t, 1, delivered, "仅 Admin 角色客户端应收到")

	select {
	case <-admin.SendChan:
	default:
		t.Fatal("admin 应收到消息")
	}
	select {
	case <-customer.SendChan:
		t.Fatal("customer 不应收到 Admin 角色广播")
	default:
	}
}

// TestBroadcastToClientType 验证按客户端类型广播
func TestBroadcastToClientType(t *testing.T) {
	m, host := newTestManager()

	webClient := makeTestClient("c-web", "u-web")
	webClient.ClientType = models.ClientTypeWeb

	mobileClient := makeTestClient("c-mobile", "u-mobile")
	mobileClient.ClientType = models.ClientTypeMobile

	host.GetShardedRegistry().AddClient(webClient)
	host.GetShardedRegistry().AddClient(mobileClient)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToClientType(ctx, models.ClientTypeWeb, msg)
	assert.Equal(t, 1, delivered, "仅 Web 客户端应收到")

	select {
	case <-webClient.SendChan:
	default:
		t.Fatal("web 客户端应收到消息")
	}
	select {
	case <-mobileClient.SendChan:
		t.Fatal("mobile 客户端不应收到 Web 类型广播")
	default:
	}
}

// TestBroadcastToDepartment 验证按部门广播
func TestBroadcastToDepartment(t *testing.T) {
	m, host := newTestManager()

	salesClient := makeTestClient("c-sales", "u-sales")
	salesClient.Department = models.DepartmentSales

	techClient := makeTestClient("c-tech", "u-tech")
	techClient.Department = models.DepartmentTechnical

	host.GetShardedRegistry().AddClient(salesClient)
	host.GetShardedRegistry().AddClient(techClient)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToDepartment(ctx, models.DepartmentTechnical, msg)
	assert.Equal(t, 1, delivered, "仅 Technical 部门客户端应收到")

	select {
	case <-techClient.SendChan:
	default:
		t.Fatal("tech 客户端应收到消息")
	}
	select {
	case <-salesClient.SendChan:
		t.Fatal("sales 客户端不应收到 Technical 部门广播")
	default:
	}
}

// ============================================================================
// BroadcastExclude 测试
// ============================================================================

// TestBroadcastExclude 验证排除指定用户后广播给其余客户端
func TestBroadcastExclude(t *testing.T) {
	m, host := newTestManager()

	c1 := makeTestClient("c-1", "u-1")
	c2 := makeTestClient("c-2", "u-2")
	c3 := makeTestClient("c-3", "u-3")
	c4 := makeTestClient("c-4", "u-4")
	host.GetShardedRegistry().AddClient(c1)
	host.GetShardedRegistry().AddClient(c2)
	host.GetShardedRegistry().AddClient(c3)
	host.GetShardedRegistry().AddClient(c4)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	// 排除 u-2 和 u-4
	delivered := m.BroadcastExclude(ctx, msg, []string{"u-2", "u-4"})
	assert.Equal(t, 2, delivered, "排除 2 个用户后应投递 2 个")

	// u-1 和 u-3 收到
	select {
	case <-c1.SendChan:
	default:
		t.Fatal("u-1 应收到消息")
	}
	select {
	case <-c3.SendChan:
	default:
		t.Fatal("u-3 应收到消息")
	}
	// u-2 和 u-4 不收到
	select {
	case <-c2.SendChan:
		t.Fatal("u-2 应被排除")
	default:
	}
	select {
	case <-c4.SendChan:
		t.Fatal("u-4 应被排除")
	default:
	}
}

// TestBroadcastExclude_EmptyExclude 验证空排除列表全员收到
func TestBroadcastExclude_EmptyExclude(t *testing.T) {
	m, host := newTestManager()

	c1 := makeTestClient("c-1", "u-1")
	c2 := makeTestClient("c-2", "u-2")
	host.GetShardedRegistry().AddClient(c1)
	host.GetShardedRegistry().AddClient(c2)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastExclude(ctx, msg, nil)
	assert.Equal(t, 2, delivered, "空排除列表应全员收到")
}

// TestBroadcastExclude_AllExcluded 验证全部排除返回 0
func TestBroadcastExclude_AllExcluded(t *testing.T) {
	m, host := newTestManager()

	c1 := makeTestClient("c-1", "u-1")
	host.GetShardedRegistry().AddClient(c1)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastExclude(ctx, msg, []string{"u-1"})
	assert.Equal(t, 0, delivered, "全部排除应返回 0")
}

// ============================================================================
// GetClientsBy* 系列测试
// ============================================================================

// setupDiverseClients 构造属性各异的客户端集合用于 GetClientsBy* 测试
func setupDiverseClients(t *testing.T) *Manager {
	t.Helper()
	m, host := newTestManager()

	clients := []*models.Client{
		{ID: "c-customer-1", UserID: "u-c1", UserType: models.UserTypeCustomer, Role: models.UserRoleCustomer, ClientType: models.ClientTypeWeb, Department: models.DepartmentGeneral, Namespace: constants.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-customer-2", UserID: "u-c2", UserType: models.UserTypeCustomer, Role: models.UserRoleCustomer, ClientType: models.ClientTypeDesktop, Department: models.DepartmentGeneral, Namespace: constants.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-agent-1", UserID: "u-a1", UserType: models.UserTypeAgent, Role: models.UserRoleAgent, ClientType: models.ClientTypeWeb, Department: models.DepartmentSales, Namespace: constants.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-agent-2", UserID: "u-a2", UserType: models.UserTypeAgent, Role: models.UserRoleAgent, ClientType: models.ClientTypeMobile, Department: models.DepartmentTechnical, Namespace: constants.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-vip-1", UserID: "u-v1", UserType: models.UserTypeVIP, Role: models.UserRoleCustomer, ClientType: models.ClientTypeAPI, Department: models.DepartmentGeneral, Namespace: constants.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-admin-1", UserID: "u-ad1", UserType: models.UserTypeAdmin, Role: models.UserRoleAdmin, ClientType: models.ClientTypeDesktop, Department: models.DepartmentGeneral, Namespace: constants.DefaultNamespace, SendChan: make(chan []byte, 1)},
	}

	// 设置 VIP 等级
	clients[4].SetVIPLevel(models.VIPLevelV5) // vip-1 = V5
	clients[1].SetVIPLevel(models.VIPLevelV3) // customer-2 = V3

	for _, c := range clients {
		host.GetShardedRegistry().AddClient(c)
	}
	return m
}

// TestGetClientsByUserType 验证按 UserType 获取客户端列表
func TestGetClientsByUserType(t *testing.T) {
	m := setupDiverseClients(t)

	tests := []struct {
		name     string
		userType models.UserType
		expected int
	}{
		{"Customer", models.UserTypeCustomer, 2},
		{"Agent", models.UserTypeAgent, 2},
		{"VIP", models.UserTypeVIP, 1},
		{"Admin", models.UserTypeAdmin, 1},
		{"Bot(不存在)", models.UserTypeBot, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := m.GetClientsByUserType(tt.userType)
			assert.Len(t, clients, tt.expected)
			for _, c := range clients {
				assert.Equal(t, tt.userType, c.UserType)
			}
		})
	}
}

// TestGetClientsByRole 验证按 Role 获取客户端列表
func TestGetClientsByRole(t *testing.T) {
	m := setupDiverseClients(t)

	tests := []struct {
		name     string
		role     models.UserRole
		expected int
	}{
		{"Customer角色", models.UserRoleCustomer, 3}, // c1, c2, v1
		{"Agent角色", models.UserRoleAgent, 2},       // a1, a2
		{"Admin角色", models.UserRoleAdmin, 1},       // ad1
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := m.GetClientsByRole(tt.role)
			assert.Len(t, clients, tt.expected)
			for _, c := range clients {
				assert.Equal(t, tt.role, c.Role)
			}
		})
	}
}

// TestGetClientsByClientType 验证按 ClientType 获取客户端列表
func TestGetClientsByClientType(t *testing.T) {
	m := setupDiverseClients(t)

	tests := []struct {
		name       string
		clientType models.ClientType
		expected   int
		clientIDs  []string
	}{
		{"Web", models.ClientTypeWeb, 2, []string{"c-customer-1", "c-agent-1"}},
		{"Desktop", models.ClientTypeDesktop, 2, []string{"c-customer-2", "c-admin-1"}},
		{"Mobile", models.ClientTypeMobile, 1, []string{"c-agent-2"}},
		{"API", models.ClientTypeAPI, 1, []string{"c-vip-1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := m.GetClientsByClientType(tt.clientType)
			assert.Len(t, clients, tt.expected)
			ids := make(map[string]bool, len(clients))
			for _, c := range clients {
				assert.Equal(t, tt.clientType, c.ClientType)
				ids[c.ID] = true
			}
			for _, expectedID := range tt.clientIDs {
				assert.True(t, ids[expectedID], "应包含 %s", expectedID)
			}
		})
	}
}

// TestGetClientsByDepartment 验证按 Department 获取客户端列表
func TestGetClientsByDepartment(t *testing.T) {
	m := setupDiverseClients(t)

	tests := []struct {
		name       string
		department models.Department
		expected   int
	}{
		{"Technical", models.DepartmentTechnical, 1}, // c-agent-2
		{"Sales", models.DepartmentSales, 1},         // c-agent-1
		{"General", models.DepartmentGeneral, 4},     // c1, c2, v1, ad1
		{"Support(不存在)", models.DepartmentSupport, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := m.GetClientsByDepartment(tt.department)
			assert.Len(t, clients, tt.expected)
			for _, c := range clients {
				assert.Equal(t, tt.department, c.Department)
			}
		})
	}
}

// TestGetClientsByVIPLevel 验证按 VIP 等级及以上获取客户端列表
func TestGetClientsByVIPLevel(t *testing.T) {
	m := setupDiverseClients(t)

	tests := []struct {
		name     string
		minLevel models.VIPLevel
		expected int
	}{
		{"V0+(全员)", models.VIPLevelV0, 6}, // 所有客户端都 >= V0
		{"V3+", models.VIPLevelV3, 2},     // customer-2(V3), vip-1(V5)
		{"V5+", models.VIPLevelV5, 1},     // vip-1(V5)
		{"V6+(无)", models.VIPLevelV6, 0},  // 无 >= V6
		{"V8+(无)", models.VIPLevelV8, 0},  // 无 >= V8
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := m.GetClientsByVIPLevel(tt.minLevel)
			assert.Len(t, clients, tt.expected)
			minL := tt.minLevel.GetLevel()
			for _, c := range clients {
				assert.GreaterOrEqual(t, c.GetVIPLevel().GetLevel(), minL)
			}
		})
	}
}

// TestGetClientsByVIPLevel_BoundaryCheck 验证 VIP 等级边界匹配
func TestGetClientsByVIPLevel_BoundaryCheck(t *testing.T) {
	m, host := newTestManager()

	// 创建精确 V3 的客户端
	v3 := makeVIPClient("c-v3", "u-v3", models.VIPLevelV3)
	v4 := makeVIPClient("c-v4", "u-v4", models.VIPLevelV4)
	host.GetShardedRegistry().AddClient(v3)
	host.GetShardedRegistry().AddClient(v4)

	// V3+ 应包含 V3 和 V4
	clients := m.GetClientsByVIPLevel(models.VIPLevelV3)
	assert.Len(t, clients, 2)

	// V4+ 应只包含 V4
	clients = m.GetClientsByVIPLevel(models.VIPLevelV4)
	assert.Len(t, clients, 1)
	assert.Equal(t, "c-v4", clients[0].ID)
}

// ============================================================================
// 集成场景测试
// ============================================================================

// TestBroadcastByUserType_WithNamespaceIsolation 验证按类型广播 + namespace 隔离组合
func TestBroadcastByUserType_WithNamespaceIsolation(t *testing.T) {
	m, host := newTestManager()

	// 同一 UserType 在不同 namespace
	ns1Customer := makeTestClient("c-ns1", "u-ns1", "ns-alpha")
	ns2Customer := makeTestClient("c-ns2", "u-ns2", "ns-beta")
	host.GetShardedRegistry().AddClient(ns1Customer)
	host.GetShardedRegistry().AddClient(ns2Customer)

	// 向 ns-alpha 的 Customer 广播
	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-alpha").WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastByUserType(ctx, models.UserTypeCustomer, msg)
	assert.Equal(t, 1, delivered, "仅 ns-alpha 的 Customer 应收到")

	select {
	case <-ns1Customer.SendChan:
	default:
		t.Fatal("ns-alpha 的 Customer 应收到消息")
	}
	select {
	case <-ns2Customer.SendChan:
		t.Fatal("ns-beta 的 Customer 不应收到 ns-alpha 的消息")
	default:
	}
}

// TestBroadcastToRole_NoGroupIsolation 验证按角色广播不做业务群组隔离
//
// 设计说明：BroadcastToRole 基于 client 属性（角色）过滤，仅做 namespace 隔离。
// 群组维度的角色广播应通过 groupRepo.GetMembers + 角色过滤 + BroadcastToUserIDs 实现，
// 而非在 BroadcastToFiltered 中做 msg.GroupIDs vs client.GroupID 匹配（维度不同，会导致 delivered=0）。
func TestBroadcastToRole_NoGroupIsolation(t *testing.T) {
	m, host := newTestManager()

	g1Agent := makeTestClient("c-g1", "u-g1", constants.DefaultNamespace, "group-a")
	g1Agent.UserType = models.UserTypeAgent
	g1Agent.Role = models.UserRoleAgent

	g2Agent := makeTestClient("c-g2", "u-g2", constants.DefaultNamespace, "group-b")
	g2Agent.UserType = models.UserTypeAgent
	g2Agent.Role = models.UserRoleAgent

	host.GetShardedRegistry().AddClient(g1Agent)
	host.GetShardedRegistry().AddClient(g2Agent)

	// 向 Agent 角色广播（BroadcastToFiltered 仅做 namespace 隔离，不做 group 隔离）
	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"group-a"}).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToRole(ctx, models.UserRoleAgent, msg)
	// 同 namespace 的两个 Agent 都应收到（角色过滤 + namespace 隔离，无群组隔离）
	assert.Equal(t, 2, delivered, "同 namespace 不同 group 的 Agent 都应收到")

	select {
	case <-g1Agent.SendChan:
	default:
		t.Fatal("group-a 的 Agent 应收到消息")
	}
	select {
	case <-g2Agent.SendChan:
	default:
		t.Fatal("group-b 的 Agent 也应收到消息（BroadcastToFiltered 不隔离业务群组）")
	}
}

// TestBroadcastToFiltered_MultiSuccessUpdatesStatusOnce 验证多客户端成功投递后只更新一次状态
func TestBroadcastToFiltered_MultiSuccessUpdatesStatusOnce(t *testing.T) {
	m, host, repo, cleanup := newStatusRecordingManager()
	defer cleanup()

	// 3 个客户端
	for i := 0; i < 3; i++ {
		c := makeTestClient("c-"+string(rune('A'+i)), "u-"+string(rune('A'+i)))
		host.GetShardedRegistry().AddClient(c)
	}

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")
	msg.MessageID = "multi-status-1"

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)
	assert.Equal(t, 3, delivered)

	// 等待状态更新（同一 msgID 只更新一次）
	require.Eventually(t, func() bool {
		repo.batchUpdateMu.Lock()
		defer repo.batchUpdateMu.Unlock()
		successCount := 0
		for _, call := range repo.batchUpdateCalls {
			if call.Status == models.MessageSendStatusSuccess {
				successCount++
			}
		}
		return successCount >= 1
	}, 3*time.Second, 50*time.Millisecond, "应至少触发一次 Success 状态更新")
}

// TestBroadcastToUserIDs_MultiUsersMixedTypes 验证多用户混合 WS+SSE 类型投递
func TestBroadcastToUserIDs_MultiUsersMixedTypes(t *testing.T) {
	m, host := newTestManager()

	// u1: WS, u2: SSE, u3: WS+SSE（同用户不同设备）
	ws1 := makeTestClient("c-ws1", "u1")
	sse2 := makeSSEClient("c-sse2", "u2")
	ws3 := makeTestClient("c-ws3", "u3")
	sse3 := makeSSEClient("c-sse3", "u3")
	host.GetShardedRegistry().AddClient(ws1)
	host.GetShardedRegistry().AddClient(sse2)
	host.GetShardedRegistry().AddClient(ws3)
	host.GetShardedRegistry().AddClient(sse3)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToUserIDs(ctx, []string{"u1", "u2", "u3"}, msg)
	assert.Equal(t, 4, delivered, "u1(WS) + u2(SSE) + u3(WS+SSE) = 4")

	// 验证每个客户端都收到
	select {
	case <-ws1.SendChan:
	default:
		t.Fatal("ws1 应收到消息")
	}
	select {
	case <-sse2.SSEMessageCh:
	default:
		t.Fatal("sse2 应收到消息")
	}
	select {
	case <-ws3.SendChan:
	default:
		t.Fatal("ws3 应收到消息")
	}
	select {
	case <-sse3.SSEMessageCh:
	default:
		t.Fatal("sse3 应收到消息")
	}
}

// TestBroadcastToFiltered_OnlySSEClients 验证仅有 SSE 客户端时的投递
func TestBroadcastToFiltered_OnlySSEClients(t *testing.T) {
	m, host := newTestManager()

	sse1 := makeSSEClient("c-sse1", "u-sse1")
	sse2 := makeSSEClient("c-sse2", "u-sse2")
	host.GetShardedRegistry().AddClient(sse1)
	host.GetShardedRegistry().AddClient(sse2)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)
	assert.Equal(t, 2, delivered, "两个 SSE 客户端都应收到")

	select {
	case <-sse1.SSEMessageCh:
	default:
		t.Fatal("sse1 应收到消息")
	}
	select {
	case <-sse2.SSEMessageCh:
	default:
		t.Fatal("sse2 应收到消息")
	}
}

// TestBroadcastToFiltered_OnlyWSClients 验证仅有 WS 客户端时的投递
func TestBroadcastToFiltered_OnlyWSClients(t *testing.T) {
	m, host := newTestManager()

	ws1 := makeTestClient("c-ws1", "u-ws1")
	ws2 := makeTestClient("c-ws2", "u-ws2")
	host.GetShardedRegistry().AddClient(ws1)
	host.GetShardedRegistry().AddClient(ws2)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := makeGroupMessage("sender")

	delivered := m.BroadcastToFiltered(ctx, func(c *models.Client) bool {
		return true
	}, msg)
	assert.Equal(t, 2, delivered, "两个 WS 客户端都应收到")

	select {
	case <-ws1.SendChan:
	default:
		t.Fatal("ws1 应收到消息")
	}
	select {
	case <-ws2.SendChan:
	default:
		t.Fatal("ws2 应收到消息")
	}
}

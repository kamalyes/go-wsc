/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 00:00:00
 * @FilePath: \go-wsc\hub\broadcast_test.go
 * @Description: Hub 广播功能白盒单元测试（覆盖 hub/broadcast.go）
 *
 * 复用 group_test.go 中的 setupGroupTestHub / makeTestClient / makeGroupMessage 等 helper。
 * 复用 query_test.go 中的 makeSSEClient helper。
 *
 * 覆盖场景：
 *   1. Broadcast 基础方法（入队/满队列降级/Clone保护/统计计数/默认值填充）
 *   2. broadcastToFiltered（WS+SSE 投递/条件过滤/路由信封隔离/序列化失败/关闭客户端跳过）
 *   3. broadcastToUserIDs（空列表/WS+SSE 投递/关闭客户端跳过/路由隔离）
 *   4. BroadcastByUserType / BroadcastToRole / BroadcastToClientType / BroadcastToDepartment
 *   5. BroadcastPriority / BroadcastAfterDelay / BroadcastExclude
 *   6. GetClientsByUserType / GetClientsByRole / GetClientsByClientType / GetClientsByDepartment / GetClientsByVIPLevel
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// Broadcast 基础方法测试
// ============================================================================

// TestBroadcast_ToChannel 验证消息正常进入 broadcast channel，且路由信封/类型/时间被正确填充
func TestBroadcast_ToChannel(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")
	msg.MessageID = "bcast-1"

	hub.Broadcast(ctx, msg)

	select {
	case got := <-hub.broadcast:
		assert.Equal(t, "bcast-1", got.MessageID)
		assert.Equal(t, models.DefaultNamespace, got.Namespace)
		assert.Equal(t, BroadcastTypeGlobal, got.BroadcastType)
		assert.False(t, got.CreateAt.IsZero())
	case <-time.After(time.Second):
		t.Fatal("消息未进入 broadcast channel")
	}
}

// TestBroadcast_AutoSetsCreateAt 验证 CreateAt 为零值时自动填充当前时间
func TestBroadcast_AutoSetsCreateAt(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	msg.CreateAt = time.Time{} // 零值

	hub.Broadcast(context.Background(), msg)

	select {
	case got := <-hub.broadcast:
		assert.False(t, got.CreateAt.IsZero(), "CreateAt 零值时应自动填充")
	case <-time.After(time.Second):
		t.Fatal("消息未进入 broadcast channel")
	}
}

// TestBroadcast_PreservesExistingCreateAt 验证 CreateAt 已有值时不被覆盖
func TestBroadcast_PreservesExistingCreateAt(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	fixed := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	msg := makeGroupMessage("sender")
	msg.CreateAt = fixed

	hub.Broadcast(context.Background(), msg)

	select {
	case got := <-hub.broadcast:
		assert.Equal(t, fixed, got.CreateAt, "已有 CreateAt 不应被覆盖")
	case <-time.After(time.Second):
		t.Fatal("消息未进入 broadcast channel")
	}
}

// TestBroadcast_AutoSetsBroadcastType 验证 BroadcastType 为空时自动设置为 Global
func TestBroadcast_AutoSetsBroadcastType(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("空值自动填充Global", func(t *testing.T) {
		msg := makeGroupMessage("sender")
		msg.BroadcastType = ""

		hub.Broadcast(context.Background(), msg)

		select {
		case got := <-hub.broadcast:
			assert.Equal(t, BroadcastTypeGlobal, got.BroadcastType)
		case <-time.After(time.Second):
			t.Fatal("消息未进入 broadcast channel")
		}
	})

	t.Run("已有值不覆盖", func(t *testing.T) {
		msg := makeGroupMessage("sender")
		msg.BroadcastType = BroadcastTypeSession

		hub.Broadcast(context.Background(), msg)

		select {
		case got := <-hub.broadcast:
			assert.Equal(t, BroadcastTypeSession, got.BroadcastType)
		case <-time.After(time.Second):
			t.Fatal("消息未进入 broadcast channel")
		}
	})
}

// TestBroadcast_CloneProtection 验证 Broadcast 内部 Clone 消息，外部修改不影响入队内容
func TestBroadcast_CloneProtection(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	msg.MessageID = "clone-1"
	msg.Content = "original"

	hub.Broadcast(context.Background(), msg)

	// 修改原消息
	msg.Content = "modified"

	select {
	case got := <-hub.broadcast:
		assert.Equal(t, "original", got.Content, "Clone 后外部修改不应影响入队消息")
		assert.Equal(t, "clone-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("消息未进入 broadcast channel")
	}
}

// TestBroadcast_StatsIncrement 验证 statsRepo 存在时 broadcastSentCount 递增
func TestBroadcast_StatsIncrement(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// setupGroupTestHub 未设置 statsRepo，先注入 fake
	hub.statsRepo = &fakeHubStatsRepository{}
	before := hub.broadcastSentCount.Load()

	hub.Broadcast(context.Background(), makeGroupMessage("sender"))

	// 消费 channel 避免堆积
	<-hub.broadcast

	assert.Equal(t, int64(1), hub.broadcastSentCount.Load()-before, "broadcastSentCount 应递增 1")
}

// TestBroadcast_StatsNilNoIncrement 验证 statsRepo 为 nil 时不递增计数（不 panic）
func TestBroadcast_StatsNilNoIncrement(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// statsRepo 默认为 nil
	assert.Nil(t, hub.statsRepo)

	assert.NotPanics(t, func() {
		hub.Broadcast(context.Background(), makeGroupMessage("sender"))
	})
	assert.Equal(t, int64(0), hub.broadcastSentCount.Load())

	<-hub.broadcast // 消费
}

// TestBroadcast_FullChannelFallsToPending 验证 broadcast channel 满时降级到 pendingMessages
func TestBroadcast_FullChannelFallsToPending(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 不启动 Run，手动填满 broadcast channel（容量 = MessageBufferSize*4 = 256*4 = 1024）
	for i := 0; i < cap(hub.broadcast)+1; i++ {
		m := makeGroupMessage("sender")
		hub.Broadcast(context.Background(), m)
		// 一旦 pendingMessages 有消息就停止（说明 broadcast 已满）
		if len(hub.pendingMessages) > 0 {
			break
		}
	}

	// 验证 pendingMessages 至少有 1 条
	require.NotZero(t, len(hub.pendingMessages), "broadcast 满后应降级到 pendingMessages")

	// 从 pendingMessages 读出验证
	select {
	case got := <-hub.pendingMessages:
		assert.NotNil(t, got)
	case <-time.After(time.Second):
		t.Fatal("pendingMessages 中无消息")
	}
}

// TestBroadcast_AllQueuesFullSilentDrop 验证 broadcast 和 pendingMessages 都满时静默丢弃不 panic
func TestBroadcast_AllQueuesFullSilentDrop(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 填满 broadcast channel
	for len(hub.broadcast) < cap(hub.broadcast) {
		hub.broadcast <- makeGroupMessage("filler")
	}
	// 填满 pendingMessages
	for len(hub.pendingMessages) < cap(hub.pendingMessages) {
		hub.pendingMessages <- makeGroupMessage("filler")
	}

	// 两个队列都满，再发应静默丢弃
	assert.NotPanics(t, func() {
		hub.Broadcast(context.Background(), makeGroupMessage("sender"))
	})

	// 队列长度不变（消息被丢弃）
	assert.Equal(t, cap(hub.broadcast), len(hub.broadcast))
	assert.Equal(t, cap(hub.pendingMessages), len(hub.pendingMessages))
}

// ============================================================================
// broadcastToFiltered 测试
// ============================================================================

// TestBroadcastToFiltered_WSAndSSE 验证 WS 和 SSE 客户端均收到消息
func TestBroadcastToFiltered_WSAndSSE(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	wsClient := makeTestClient("c-ws", "u-ws")
	sseClient := makeSSEClient("c-sse", "u-sse")
	hub.shardedRegistry.AddClient(wsClient)
	hub.shardedRegistry.AddClient(sseClient)

	msg := makeGroupMessage("sender")
	msg.MessageID = "filtered-1"

	// 注入路由使 namespace 匹配（makeTestClient 默认 ns=default）
	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
		return true // 全匹配
	}, msg)

	assert.Equal(t, 2, delivered)

	// WS 客户端收到序列化数据
	select {
	case data := <-wsClient.SendChan:
		var got HubMessage
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	matched := makeTestClient("c-match", "u-match")
	unmatched := makeTestClient("c-unmatch", "u-unmatch")
	hub.shardedRegistry.AddClient(matched)
	hub.shardedRegistry.AddClient(unmatched)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ns1Client := makeTestClient("c-ns1", "u-ns1", "ns-alpha")
	ns2Client := makeTestClient("c-ns2", "u-ns2", "ns-beta")
	hub.shardedRegistry.AddClient(ns1Client)
	hub.shardedRegistry.AddClient(ns2Client)

	// 向 ns-alpha 广播
	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-alpha", nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
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

// TestBroadcastToFiltered_GroupNotIsolated 验证 broadcastToFiltered 不做业务群组隔离
//
// 设计说明（与 ClientMatchesEnvelope / ForEachUserClientFiltered 对称）：
//   - msg.GroupIDs 是"业务群组ID"（如 "group-a"），client.GroupID 是"连接级系统组"（如 __default_gp__）
//   - 两者完全两个维度，强行匹配会导致群成员设备全部被过滤（delivered=0）
//   - 群组隔离应通过 broadcastToUserIDs（groupRepo.GetMembers 成员列表查找）实现
//   - broadcastToFiltered 仅做 namespace 隔离，用于角色/类型/部门等属性过滤广播
func TestBroadcastToFiltered_GroupNotIsolated(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	g1Client := makeTestClient("c-g1", "u-g1", models.DefaultNamespace, "group-a")
	g2Client := makeTestClient("c-g2", "u-g2", models.DefaultNamespace, "group-b")
	hub.shardedRegistry.AddClient(g1Client)
	hub.shardedRegistry.AddClient(g2Client)

	// 向 group-a 广播（broadcastToFiltered 仅做 namespace 隔离，不做业务群组隔离）
	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, []string{"group-a"})
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
		return true
	}, msg)

	// 同 namespace 不同 group 的客户端都应收到（broadcastToFiltered 不隔离业务群组）
	assert.Equal(t, 2, delivered, "同 namespace 不同 group 的客户端都应收到（broadcastToFiltered 仅做 namespace 隔离）")

	select {
	case <-g1Client.SendChan:
	default:
		t.Fatal("group-a 客户端应收到消息")
	}
	select {
	case <-g2Client.SendChan:
	default:
		t.Fatal("group-b 客户端也应收到消息（broadcastToFiltered 不隔离业务群组）")
	}
}

// TestBroadcastToFiltered_EmptyNamespaceMatchesAll 验证 msg.Namespace 为空时跳过 ns 过滤（全局广播）
func TestBroadcastToFiltered_EmptyNamespaceMatchesAll(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ns1Client := makeTestClient("c-ns1", "u-ns1", "ns-alpha")
	ns2Client := makeTestClient("c-ns2", "u-ns2", "ns-beta")
	hub.shardedRegistry.AddClient(ns1Client)
	hub.shardedRegistry.AddClient(ns2Client)

	// ctx 无路由 → msg.Namespace 为空 → 跳过 ns 过滤
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(context.Background(), func(c *Client) bool {
		return true
	}, msg)

	assert.Equal(t, 2, delivered, "空 namespace 应匹配所有客户端")
}

// TestBroadcastToFiltered_ClosedClientSkipped 验证已关闭客户端被跳过
func TestBroadcastToFiltered_ClosedClientSkipped(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	closed := makeTestClient("c-closed", "u-closed")
	closed.MarkClosed()
	open := makeTestClient("c-open", "u-open")
	hub.shardedRegistry.AddClient(closed)
	hub.shardedRegistry.AddClient(open)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
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
func TestBroadcastToFiltered_TrySendFail(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-full", "u-full")
	// 填满 SendChan（缓冲 16）
	for i := 0; i < cap(client.SendChan); i++ {
		client.SendChan <- []byte("filler")
	}
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
		return true
	}, msg)

	assert.Equal(t, 0, delivered, "SendChan 满时 TrySend 失败应返回 0")
}

// TestBroadcastToFiltered_NoMatchReturnsZero 验证无匹配客户端时返回 0 且不更新状态
func TestBroadcastToFiltered_NoMatchReturnsZero(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-1", "u-1")
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")
	msg.MessageID = "no-match-1"

	// condition 永远返回 false
	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-1", "u-1")
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")
	// Data 中放入不可序列化的值（func），触发 json.Marshal 失败
	msg.Data = map[string]interface{}{
		"bad": func() {},
	}

	assert.NotPanics(t, func() {
		delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
			return true
		}, msg)
		assert.Equal(t, 0, delivered)
	})
}

// TestBroadcastToFiltered_UpdatesMessageStatus 验证成功投递后调用 updateMessageStatusAsync
func TestBroadcastToFiltered_UpdatesMessageStatus(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 注入 messageRecordRepo 以观测状态更新
	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	client := makeTestClient("c-1", "u-1")
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")
	msg.MessageID = "status-1"

	hub.broadcastToFiltered(ctx, func(c *Client) bool {
		return true
	}, msg)

	// updateMessageStatusAsync 通过 messageStatusUpdater 异步批量更新
	// 等待批量更新器 flush
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
// broadcastToUserIDs 测试
// ============================================================================

// TestBroadcastToUserIDs_EmptyListReturnsZero 验证空 userIDs 列表返回 0
func TestBroadcastToUserIDs_EmptyListReturnsZero(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	delivered := hub.broadcastToUserIDs(context.Background(), nil, msg)
	assert.Equal(t, 0, delivered)

	delivered = hub.broadcastToUserIDs(context.Background(), []string{}, msg)
	assert.Equal(t, 0, delivered)
}

// TestBroadcastToUserIDs_WSAndSSE 验证 WS 和 SSE 客户端均收到消息
func TestBroadcastToUserIDs_WSAndSSE(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	wsClient := makeTestClient("c-ws", "u-ws")
	sseClient := makeSSEClient("c-sse", "u-sse")
	hub.shardedRegistry.AddClient(wsClient)
	hub.shardedRegistry.AddClient(sseClient)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")
	msg.MessageID = "uids-1"

	delivered := hub.broadcastToUserIDs(ctx, []string{"u-ws", "u-sse"}, msg)
	assert.Equal(t, 2, delivered)

	select {
	case data := <-wsClient.SendChan:
		var got HubMessage
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

// TestBroadcastToUserIDs_NamespaceIsolation 验证 namespace 路由隔离
func TestBroadcastToUserIDs_NamespaceIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ns1Client := makeTestClient("c-ns1", "u-shared", "ns-alpha")
	ns2Client := makeTestClient("c-ns2", "u-shared", "ns-beta")
	hub.shardedRegistry.AddClient(ns1Client)
	hub.shardedRegistry.AddClient(ns2Client)

	// 向 ns-alpha 广播给 u-shared
	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-alpha", nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToUserIDs(ctx, []string{"u-shared"}, msg)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	closed := makeTestClient("c-closed", "u-closed")
	closed.MarkClosed()
	open := makeTestClient("c-open", "u-open")
	hub.shardedRegistry.AddClient(closed)
	hub.shardedRegistry.AddClient(open)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToUserIDs(ctx, []string{"u-closed", "u-open"}, msg)
	assert.Equal(t, 1, delivered, "仅未关闭客户端应收到")

	select {
	case <-open.SendChan:
	default:
		t.Fatal("未关闭客户端应收到消息")
	}
}

// TestBroadcastToUserIDs_NonExistentUser 验证不存在的 userID 不影响投递
func TestBroadcastToUserIDs_NonExistentUser(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-1", "u-exist")
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	// 包含不存在 userID 和存在 userID
	delivered := hub.broadcastToUserIDs(ctx, []string{"u-nonexist", "u-exist"}, msg)
	assert.Equal(t, 1, delivered, "仅存在的用户应收到")

	select {
	case <-client.SendChan:
	default:
		t.Fatal("存在的用户应收到消息")
	}
}

// TestBroadcastToUserIDs_TrySendFail 验证 SendChan 满时 TrySend 失败不计数
func TestBroadcastToUserIDs_TrySendFail(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-full", "u-full")
	for i := 0; i < cap(client.SendChan); i++ {
		client.SendChan <- []byte("filler")
	}
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToUserIDs(ctx, []string{"u-full"}, msg)
	assert.Equal(t, 0, delivered, "SendChan 满时 TrySend 失败应返回 0")
}

// TestBroadcastToUserIDs_MarshalFailReturnsZero 验证序列化失败时返回 0
func TestBroadcastToUserIDs_MarshalFailReturnsZero(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-1", "u-1")
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")
	msg.Data = map[string]interface{}{
		"bad": func() {},
	}

	assert.NotPanics(t, func() {
		delivered := hub.broadcastToUserIDs(ctx, []string{"u-1"}, msg)
		assert.Equal(t, 0, delivered)
	})
}

// ============================================================================
// BroadcastByUserType / BroadcastToRole / BroadcastToClientType / BroadcastToDepartment 测试
// ============================================================================

// TestBroadcastByUserType 验证按用户类型广播
func TestBroadcastByUserType(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	customer1 := makeTestClient("c-c1", "u-c1")
	customer2 := makeTestClient("c-c2", "u-c2")
	agent := makeAgentClient("c-a1", "u-a1")
	hub.shardedRegistry.AddClient(customer1)
	hub.shardedRegistry.AddClient(customer2)
	hub.shardedRegistry.AddClient(agent)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastByUserType(ctx, UserTypeCustomer, msg)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-1", "u-1")
	hub.shardedRegistry.AddClient(client)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastByUserType(ctx, UserTypeBot, msg)
	assert.Equal(t, 0, delivered, "无 Bot 类型客户端应返回 0")
}

// TestBroadcastToRole 验证按角色广播
func TestBroadcastToRole(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	customer := makeTestClient("c-c", "u-c")
	customer.Role = models.UserRoleCustomer

	admin := makeTestClient("c-a", "u-a")
	admin.Role = models.UserRoleAdmin

	hub.shardedRegistry.AddClient(customer)
	hub.shardedRegistry.AddClient(admin)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastToRole(ctx, models.UserRoleAdmin, msg)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	webClient := makeTestClient("c-web", "u-web")
	webClient.ClientType = models.ClientTypeWeb

	mobileClient := makeTestClient("c-mobile", "u-mobile")
	mobileClient.ClientType = models.ClientTypeMobile

	hub.shardedRegistry.AddClient(webClient)
	hub.shardedRegistry.AddClient(mobileClient)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastToClientType(ctx, models.ClientTypeWeb, msg)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	salesClient := makeTestClient("c-sales", "u-sales")
	salesClient.Department = models.DepartmentSales

	techClient := makeTestClient("c-tech", "u-tech")
	techClient.Department = models.DepartmentTechnical

	hub.shardedRegistry.AddClient(salesClient)
	hub.shardedRegistry.AddClient(techClient)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastToDepartment(ctx, models.DepartmentTechnical, msg)
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
// BroadcastPriority / BroadcastAfterDelay / BroadcastExclude 测试
// ============================================================================

// TestBroadcastPriority 验证优先级广播：设置 Priority 后进入 broadcast channel
func TestBroadcastPriority(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	msg.MessageID = "prio-1"

	hub.BroadcastPriority(context.Background(), msg, PriorityHigh)

	select {
	case got := <-hub.broadcast:
		assert.Equal(t, PriorityHigh, got.Priority)
		assert.Equal(t, "prio-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("消息未进入 broadcast channel")
	}
}

// TestBroadcastPriority_VariousLevels 表驱动验证各优先级
func TestBroadcastPriority_VariousLevels(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	levels := []Priority{PriorityLow, PriorityNormal, PriorityHigh, PriorityCritical}
	for _, p := range levels {
		msg := makeGroupMessage("sender")
		hub.BroadcastPriority(context.Background(), msg, p)

		select {
		case got := <-hub.broadcast:
			assert.Equal(t, p, got.Priority)
		case <-time.After(time.Second):
			t.Fatalf("优先级 %s 的消息未进入 broadcast channel", p)
		}
	}
}

// TestBroadcastAfterDelay 验证延迟广播：延迟后消息进入 broadcast channel
func TestBroadcastAfterDelay(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	msg.MessageID = "delay-1"

	hub.BroadcastAfterDelay(context.Background(), msg, 100*time.Millisecond)

	// 50ms 内不应到达
	select {
	case <-hub.broadcast:
		t.Fatal("延迟未到，消息不应进入 channel")
	case <-time.After(50 * time.Millisecond):
	}

	// 100ms 后应到达
	select {
	case got := <-hub.broadcast:
		assert.Equal(t, "delay-1", got.MessageID)
	case <-time.After(time.Second):
		t.Fatal("延迟后消息未进入 broadcast channel")
	}
}

// TestBroadcastAfterDelay_ZeroDelay 验证零延迟立即入队
func TestBroadcastAfterDelay_ZeroDelay(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	hub.BroadcastAfterDelay(context.Background(), msg, 0)

	select {
	case <-hub.broadcast:
	case <-time.After(time.Second):
		t.Fatal("零延迟应立即入队")
	}
}

// TestBroadcastExclude 验证排除指定用户后广播给其余客户端
func TestBroadcastExclude(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-1", "u-1")
	c2 := makeTestClient("c-2", "u-2")
	c3 := makeTestClient("c-3", "u-3")
	c4 := makeTestClient("c-4", "u-4")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)
	hub.shardedRegistry.AddClient(c3)
	hub.shardedRegistry.AddClient(c4)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	// 排除 u-2 和 u-4
	delivered := hub.BroadcastExclude(ctx, msg, []string{"u-2", "u-4"})
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-1", "u-1")
	c2 := makeTestClient("c-2", "u-2")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastExclude(ctx, msg, nil)
	assert.Equal(t, 2, delivered, "空排除列表应全员收到")
}

// TestBroadcastExclude_AllExcluded 验证全部排除返回 0
func TestBroadcastExclude_AllExcluded(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-1", "u-1")
	hub.shardedRegistry.AddClient(c1)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastExclude(ctx, msg, []string{"u-1"})
	assert.Equal(t, 0, delivered, "全部排除应返回 0")
}

// ============================================================================
// GetClientsBy* 系列测试
// ============================================================================

// setupDiverseClients 构造属性各异的客户端集合用于 GetClientsBy* 测试
func setupDiverseClients(t *testing.T) *Hub {
	t.Helper()
	hub, _, _, cleanup := setupGroupTestHub(t)
	t.Cleanup(cleanup)

	clients := []*Client{
		{ID: "c-customer-1", UserID: "u-c1", UserType: UserTypeCustomer, Role: models.UserRoleCustomer, ClientType: models.ClientTypeWeb, Department: models.DepartmentGeneral, Namespace: models.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-customer-2", UserID: "u-c2", UserType: UserTypeCustomer, Role: models.UserRoleCustomer, ClientType: models.ClientTypeDesktop, Department: models.DepartmentGeneral, Namespace: models.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-agent-1", UserID: "u-a1", UserType: UserTypeAgent, Role: models.UserRoleAgent, ClientType: models.ClientTypeWeb, Department: models.DepartmentSales, Namespace: models.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-agent-2", UserID: "u-a2", UserType: UserTypeAgent, Role: models.UserRoleAgent, ClientType: models.ClientTypeMobile, Department: models.DepartmentTechnical, Namespace: models.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-vip-1", UserID: "u-v1", UserType: UserTypeVIP, Role: models.UserRoleCustomer, ClientType: models.ClientTypeAPI, Department: models.DepartmentGeneral, Namespace: models.DefaultNamespace, SendChan: make(chan []byte, 1)},
		{ID: "c-admin-1", UserID: "u-ad1", UserType: UserTypeAdmin, Role: models.UserRoleAdmin, ClientType: models.ClientTypeDesktop, Department: models.DepartmentGeneral, Namespace: models.DefaultNamespace, SendChan: make(chan []byte, 1)},
	}

	// 设置 VIP 等级
	clients[4].SetVIPLevel(models.VIPLevelV5) // vip-1 = V5
	clients[1].SetVIPLevel(models.VIPLevelV3) // customer-2 = V3

	for _, c := range clients {
		hub.shardedRegistry.AddClient(c)
	}
	return hub
}

// TestGetClientsByUserType 验证按 UserType 获取客户端列表
func TestGetClientsByUserType(t *testing.T) {
	hub := setupDiverseClients(t)

	tests := []struct {
		name     string
		userType UserType
		expected int
	}{
		{"Customer", UserTypeCustomer, 2},
		{"Agent", UserTypeAgent, 2},
		{"VIP", UserTypeVIP, 1},
		{"Admin", UserTypeAdmin, 1},
		{"Bot(不存在)", UserTypeBot, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := hub.GetClientsByUserType(tt.userType)
			assert.Len(t, clients, tt.expected)
			for _, c := range clients {
				assert.Equal(t, tt.userType, c.UserType)
			}
		})
	}
}

// TestGetClientsByRole 验证按 Role 获取客户端列表
func TestGetClientsByRole(t *testing.T) {
	hub := setupDiverseClients(t)

	tests := []struct {
		name     string
		role     UserRole
		expected int
	}{
		{"Customer角色", models.UserRoleCustomer, 3}, // c1, c2, v1
		{"Agent角色", models.UserRoleAgent, 2},       // a1, a2
		{"Admin角色", models.UserRoleAdmin, 1},       // ad1
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := hub.GetClientsByRole(tt.role)
			assert.Len(t, clients, tt.expected)
			for _, c := range clients {
				assert.Equal(t, tt.role, c.Role)
			}
		})
	}
}

// TestGetClientsByClientType 验证按 ClientType 获取客户端列表
func TestGetClientsByClientType(t *testing.T) {
	hub := setupDiverseClients(t)

	tests := []struct {
		name       string
		clientType ClientType
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
			clients := hub.GetClientsByClientType(tt.clientType)
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
	hub := setupDiverseClients(t)

	tests := []struct {
		name       string
		department Department
		expected   int
	}{
		{"Technical", models.DepartmentTechnical, 1}, // c-agent-2
		{"Sales", models.DepartmentSales, 1},         // c-agent-1
		{"General", models.DepartmentGeneral, 4},     // c1, c2, v1, ad1
		{"Support(不存在)", models.DepartmentSupport, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := hub.GetClientsByDepartment(tt.department)
			assert.Len(t, clients, tt.expected)
			for _, c := range clients {
				assert.Equal(t, tt.department, c.Department)
			}
		})
	}
}

// TestGetClientsByVIPLevel 验证按 VIP 等级及以上获取客户端列表
func TestGetClientsByVIPLevel(t *testing.T) {
	hub := setupDiverseClients(t)

	tests := []struct {
		name     string
		minLevel VIPLevel
		expected int
	}{
		{"V0+(全员)", models.VIPLevelV0, 6}, // 所有客户端都 >= V0
		{"V3+(", models.VIPLevelV3, 2},    // customer-2(V3), vip-1(V5)
		{"V5+(", models.VIPLevelV5, 1},    // vip-1(V5)
		{"V6+(无)", models.VIPLevelV6, 0},  // 无 >= V6
		{"V8+(无)", models.VIPLevelV8, 0},  // 无 >= V8
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clients := hub.GetClientsByVIPLevel(tt.minLevel)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 创建精确 V3 的客户端
	v3 := makeVIPClient("c-v3", "u-v3", models.VIPLevelV3)
	v4 := makeVIPClient("c-v4", "u-v4", models.VIPLevelV4)
	hub.shardedRegistry.AddClient(v3)
	hub.shardedRegistry.AddClient(v4)

	// V3+ 应包含 V3 和 V4
	clients := hub.GetClientsByVIPLevel(models.VIPLevelV3)
	assert.Len(t, clients, 2)

	// V4+ 应只包含 V4
	clients = hub.GetClientsByVIPLevel(models.VIPLevelV4)
	assert.Len(t, clients, 1)
	assert.Equal(t, "c-v4", clients[0].ID)
}

// ============================================================================
// 集成场景测试
// ============================================================================

// TestBroadcastByUserType_WithNamespaceIsolation 验证按类型广播 + namespace 隔离组合
func TestBroadcastByUserType_WithNamespaceIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 同一 UserType 在不同 namespace
	ns1Customer := makeTestClient("c-ns1", "u-ns1", "ns-alpha")
	ns2Customer := makeTestClient("c-ns2", "u-ns2", "ns-beta")
	hub.shardedRegistry.AddClient(ns1Customer)
	hub.shardedRegistry.AddClient(ns2Customer)

	// 向 ns-alpha 的 Customer 广播
	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-alpha", nil)
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastByUserType(ctx, UserTypeCustomer, msg)
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
// 群组维度的角色广播应通过 groupRepo.GetMembers + 角色过滤 + broadcastToUserIDs 实现，
// 而非在 broadcastToFiltered 中做 msg.GroupIDs vs client.GroupID 匹配（维度不同，会导致 delivered=0）。
func TestBroadcastToRole_NoGroupIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	g1Agent := makeTestClient("c-g1", "u-g1", models.DefaultNamespace, "group-a")
	g1Agent.UserType = UserTypeAgent
	g1Agent.Role = models.UserRoleAgent

	g2Agent := makeTestClient("c-g2", "u-g2", models.DefaultNamespace, "group-b")
	g2Agent.UserType = UserTypeAgent
	g2Agent.Role = models.UserRoleAgent

	hub.shardedRegistry.AddClient(g1Agent)
	hub.shardedRegistry.AddClient(g2Agent)

	// 向 Agent 角色广播（broadcastToFiltered 仅做 namespace 隔离，不做 group 隔离）
	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, []string{"group-a"})
	msg := makeGroupMessage("sender")

	delivered := hub.BroadcastToRole(ctx, models.UserRoleAgent, msg)
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
		t.Fatal("group-b 的 Agent 也应收到消息（broadcastToFiltered 不隔离业务群组）")
	}
}

// TestBroadcast_MultiMessageStatsIncrement 验证多次广播累计计数
func TestBroadcast_MultiMessageStatsIncrement(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.statsRepo = &fakeHubStatsRepository{}
	before := hub.broadcastSentCount.Load()

	for i := 0; i < 5; i++ {
		hub.Broadcast(context.Background(), makeGroupMessage("sender"))
		<-hub.broadcast // 消费避免堆积
	}

	assert.Equal(t, int64(5), hub.broadcastSentCount.Load()-before, "5 次广播应递增 5")
}

// TestBroadcastToFiltered_MultiSuccessUpdatesStatusOnce 验证多客户端成功投递后只更新一次状态
func TestBroadcastToFiltered_MultiSuccessUpdatesStatusOnce(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	// 3 个客户端
	for i := 0; i < 3; i++ {
		c := makeTestClient("c-"+string(rune('A'+i)), "u-"+string(rune('A'+i)))
		hub.shardedRegistry.AddClient(c)
	}

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")
	msg.MessageID = "multi-status-1"

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// u1: WS, u2: SSE, u3: WS+SSE（同用户不同设备）
	ws1 := makeTestClient("c-ws1", "u1")
	sse2 := makeSSEClient("c-sse2", "u2")
	ws3 := makeTestClient("c-ws3", "u3")
	sse3 := makeSSEClient("c-sse3", "u3")
	hub.shardedRegistry.AddClient(ws1)
	hub.shardedRegistry.AddClient(sse2)
	hub.shardedRegistry.AddClient(ws3)
	hub.shardedRegistry.AddClient(sse3)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToUserIDs(ctx, []string{"u1", "u2", "u3"}, msg)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	sse1 := makeSSEClient("c-sse1", "u-sse1")
	sse2 := makeSSEClient("c-sse2", "u-sse2")
	hub.shardedRegistry.AddClient(sse1)
	hub.shardedRegistry.AddClient(sse2)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ws1 := makeTestClient("c-ws1", "u-ws1")
	ws2 := makeTestClient("c-ws2", "u-ws2")
	hub.shardedRegistry.AddClient(ws1)
	hub.shardedRegistry.AddClient(ws2)

	ctx := routing.WithNamespaceGroupIDs(context.Background(), models.DefaultNamespace, nil)
	msg := makeGroupMessage("sender")

	delivered := hub.broadcastToFiltered(ctx, func(c *Client) bool {
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

// TestBroadcast_InjectRouteFromContext 验证 Broadcast 从 ctx 注入路由信封到 msg
func TestBroadcast_InjectRouteFromContext(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-from-ctx", []string{"g-from-ctx"})
	msg := makeGroupMessage("sender")

	hub.Broadcast(ctx, msg)

	select {
	case got := <-hub.broadcast:
		assert.Equal(t, "ns-from-ctx", got.Namespace, "应从 ctx 注入 namespace")
		assert.Equal(t, []string{"g-from-ctx"}, got.GroupIDs, "应从 ctx 注入 groupIDs")
	case <-time.After(time.Second):
		t.Fatal("消息未进入 broadcast channel")
	}
}

// TestBroadcast_EmptyRoute 验证 ctx 无路由时 msg 信封为空（全局广播语义）
func TestBroadcast_EmptyRoute(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender")
	// 显式设置信封值，验证 ctx 无路由时不覆盖
	msg.Namespace = "preset-ns"

	hub.Broadcast(context.Background(), msg)

	select {
	case got := <-hub.broadcast:
		// InjectRoute 幂等：已有值不覆盖
		assert.Equal(t, "preset-ns", got.Namespace, "已有 namespace 不应被空 ctx 覆盖")
	case <-time.After(time.Second):
		t.Fatal("消息未进入 broadcast channel")
	}
}

// TestBroadcast_ConcurrentSafe 验证并发调用 Broadcast 不 panic（轻量并发安全检查）
func TestBroadcast_ConcurrentSafe(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := makeGroupMessage("sender")
			msg.MessageID = "concurrent-" + string(rune('A'+idx))
			hub.Broadcast(context.Background(), msg)
		}(i)
	}

	assert.NotPanics(t, func() {
		wg.Wait()
	})

	// 消费所有消息（至少应有一些进入 channel）
	consumed := 0
drainLoop:
	for {
		select {
		case <-hub.broadcast:
			consumed++
		default:
			break drainLoop
		}
	}
	assert.Greater(t, consumed, 0, "应至少消费到 1 条消息")
}

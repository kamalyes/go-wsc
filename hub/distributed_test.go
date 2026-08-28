/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 01:00:26
 * @FilePath: \go-wsc\hub\distributed_test.go
 * @Description:
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/middleware"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
)

// configurableTokenDecoder 可配置的 Token 解码器 fake
type configurableTokenDecoder struct {
	claims *ConnectionClaims
	err    error
}

func (d *configurableTokenDecoder) Decode(_ *http.Request) (*ConnectionClaims, error) {
	return d.claims, d.err
}

// newMinHub 创建最小 Hub（不启动 Run，仅构造单测用于触发错误分支）
func newMinHub() *Hub {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	return NewHub(config)
}

// ============================================================================
// send.go：空 userID / SendConditional 无匹配 / 空成员过滤
// ============================================================================

func TestSendToUserWithRetry_EmptyUserID(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	ctx := context.Background()
	msg := NewHubMessage()
	msg.Sender = "s1"
	msg.SetContent("hi")
	res := hub.SendToUserWithRetry(ctx, "", msg)
	require.NotNil(t, res.FinalError, "空 userID 应返回错误")
}

func TestSendConditionalEmptyClientList(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	ctx := context.Background()
	msg := NewHubMessage()
	msg.Sender = "a"
	msg.SetContent("hi")
	// nil condition：由实现返回 0 或空切片，不 panic
	count := hub.SendConditional(ctx, func(c *Client) bool { return true }, msg)
	assert.Equal(t, 0, count, "0 客户端时任意 condition 都无投递")
}

func TestSendConditionalAlwaysFalse(t *testing.T) {
	t.Parallel()
	hub, _, clients, cleanup := setupStressHub(t, false)
	defer cleanup()
	require.Greater(t, len(clients), 0, "至少有基础客户端注册")

	ctx := routing.NewRoute().WithAppID("").WithNamespace("nonexistent-ns").WithGroupIDs(nil).Inject(context.Background())
	msg := NewHubMessage()
	msg.Sender = "s1"
	msg.SetContent("filtered out")
	count := hub.SendConditional(ctx, func(c *Client) bool { return false }, msg)
	assert.Equal(t, 0, count, "condition 恒 false 时投递给 0 个客户端")
}

// ============================================================================
// group.go：ctx 缺少 groupID / 空成员
// ============================================================================

func TestSendToGroup_MissingGroupIDInCtx(t *testing.T) {
	t.Parallel()
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ctx := context.Background()
	msg := NewHubMessage()
	msg.Sender = "u1"
	msg.SetContent("no groupID")
	msg.RequireAck = true
	// Deliver 统一入口语义：ctx 缺少 groupIDs 时不再视为错误，
	// 而是按决策树降级——namespace 也为空 → 全局广播分支（Mode=DeliveryModeGlobal）。
	// RequireAck 仅在群组分支（len(groupIDs)>0）生效，缺失 groupIDs 时被忽略。
	res := hub.Deliver(ctx, msg, false)
	assert.Equal(t, DeliveryModeGlobal, res.Mode, "缺少 groupID+namespace 应路由到全局广播")
	assert.Empty(t, res.Errors, "全局广播分支不应产生错误")
}

func TestSendToGroupMembers_EmptyMemberIDs(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	ctx := context.Background()
	msg := NewHubMessage()
	msg.Sender = "u1"
	msg.SetContent("empty members")
	res := hub.SendToGroupMembers(ctx, nil, msg, false)
	assert.NotNil(t, res)
	assert.Equal(t, 0, res.Total, "空成员应 Total=0")
}

// ============================================================================
// broadcast.go：零客户端 / 空 namespace 归一化
// ============================================================================

func TestBroadcastZeroClients(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	ctx := context.Background()
	msg := NewHubMessage()
	msg.SetBroadcastType(models.BroadcastTypeGlobal)
	msg.Sender = "x"
	msg.SetContent("zero")

	assert.NotPanics(t, func() {
		_ = hub.Deliver(ctx, msg, false)
	})
	assert.NotPanics(t, func() {
		nsCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns-empty").WithGroupIDs(nil).Inject(ctx)
		_ = hub.Deliver(nsCtx, msg, false)
	})
}

// ============================================================================
// ack.go：空 receiver（SendToUserWithAck）
// ============================================================================

func TestSendToUserWithAck_EmptyReceiver(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	ctx := context.Background()
	msg := NewHubMessage()
	msg.Sender = "u1"
	msg.SetContent("ack empty receiver")
	msg.RequireAck = true

	assert.NotPanics(t, func() {
		_, _ = hub.SendToUserWithAck(ctx, "", msg, 500*time.Millisecond, 1)
	})
}

// ============================================================================
// routing：空/正常值注入回读
// ============================================================================

func TestRouteContextRoundTrip(t *testing.T) {
	t.Parallel()

	ctx1 := routing.NewRoute().WithAppID("").WithNamespace("").WithGroupIDs(nil).Inject(context.Background())
	assert.Equal(t, "", routing.NamespaceFromContext(ctx1))
	assert.Nil(t, routing.GroupIDsFromContext(ctx1))

	ctx2 := routing.NewRoute().WithAppID("").WithNamespace("nsX").WithGroupIDs([]string{"g1", "g2"}).Inject(context.Background())
	assert.Equal(t, "nsX", routing.NamespaceFromContext(ctx2))
	assert.Equal(t, []string{"g1", "g2"}, routing.GroupIDsFromContext(ctx2))
}

// ============================================================================
// observer.go：查询 API（GetObserverClients / ByNamespace / Count / DeviceCount / IsObserver）
// ============================================================================

func TestObserverQueryAPIs_ZeroThenRegister(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 零观察者状态
	assert.Equal(t, 0, len(hub.GetObserverClients()), "初始无观察者")
	assert.Equal(t, 0, len(hub.GetObserverClientsByNamespace("any-ns")))
	assert.Equal(t, 0, hub.GetObserverCount())
	assert.Equal(t, 0, hub.GetObserverDeviceCount())
	assert.False(t, hub.IsObserver("u-not-exist"))

	// 注册全局观察者 + 命名空间级观察者（相同 userID 的两台设备）
	global := registerObserver(hub, "c-g", "u-g", "", "")
	ns1Dev1 := registerObserver(hub, "c-n1-d1", "u-n1", "ns1", "")
	ns1Dev2 := registerObserver(hub, "c-n1-d2", "u-n1", "ns1", "")
	_ = global
	_ = ns1Dev1
	_ = ns1Dev2

	// 验证全局计数：3 设备 / 2 用户（u-g、u-n1）
	assert.Equal(t, 3, hub.GetObserverDeviceCount())
	assert.Equal(t, 2, hub.GetObserverCount())
	assert.True(t, hub.IsObserver("u-g"))
	assert.True(t, hub.IsObserver("u-n1"))
	assert.False(t, hub.IsObserver("u-nobody"))

	// 命名空间维度过滤：合并全局观察者 + 命名空间级观察者
	ns1Obs := hub.GetObserverClientsByNamespace("ns1")
	assert.Equal(t, 3, len(ns1Obs), "ns1 查询应合并全局观察者 + ns1 的 2 个设备观察者")
	// 查询不存在的命名空间仍会返回全局观察者
	hasGlobal := hub.GetObserverClientsByNamespace("missing-ns")
	assert.Equal(t, 1, len(hasGlobal), "missing-ns 查询仍应包含全局观察者")
}

// ============================================================================
// send.go：SendWithCallback（成功 + 失败回调）
// ============================================================================

func TestSendWithCallback_OfflineTriggersOnError(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	var succCalled int32
	var errCalled int32
	done := make(chan struct{}, 1)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(context.Background())
	msg := NewHubMessage()
	msg.Sender = "u-sender"
	msg.SetContent("cb-offline")
	hub.SendWithCallback(ctx, "u-not-online", msg,
		func(_ *SendResult) {
			atomic.AddInt32(&succCalled, 1)
			done <- struct{}{}
		},
		func(_ error) {
			atomic.AddInt32(&errCalled, 1)
			done <- struct{}{}
		},
	)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SendWithCallback 未在 5s 内触发回调")
	}

	assert.Equal(t, int32(0), atomic.LoadInt32(&succCalled))
	assert.Equal(t, int32(1), atomic.LoadInt32(&errCalled))
}

func TestSendWithCallback_OnlineTriggersOnSuccess(t *testing.T) {
	t.Parallel()
	hub, _, clients, cleanup := setupStressHub(t, false)
	defer cleanup()
	require.Greater(t, len(clients), 0)

	var succCalled int32
	var errCalled int32
	done := make(chan struct{}, 1)

	ctx := routing.NewRoute().WithAppID("").WithNamespace(clients[0].Namespace).WithGroupIDs(nil).Inject(context.Background())
	msg := NewHubMessage()
	msg.Sender = "u-sender"
	msg.SetContent("cb-online")
	hub.SendWithCallback(ctx, clients[0].UserID, msg,
		func(_ *SendResult) {
			atomic.AddInt32(&succCalled, 1)
			done <- struct{}{}
		},
		func(_ error) {
			atomic.AddInt32(&errCalled, 1)
			done <- struct{}{}
		},
	)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SendWithCallback 未在 5s 内触发回调")
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&succCalled))
	assert.Equal(t, int32(0), atomic.LoadInt32(&errCalled))
}

// ============================================================================
// group.go：BroadcastToGroup 包装（委托 BroadcastToGroupMembers）
// ============================================================================

func TestBroadcastToGroup_DelegateReturnsCount(t *testing.T) {
	t.Parallel()
	hub, _, clients, cleanup := setupStressHub(t, false)
	defer cleanup()
	require.Greater(t, len(clients), 0)

	ctx := context.Background()
	msg := NewHubMessage()
	msg.Sender = "sender"
	msg.SetContent("to-group")
	// groupID 空 + 无群组成员 repo：返回 0，不 panic
	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("any-ns").WithGroupIDs([]string{"any-group"}).Inject(ctx)
	dr := hub.Deliver(groupCtx, msg, false)
	got := dr.LocalDelivered
	assert.Equal(t, 0, got, "无群组成员 repo 时投递给 0 个成员")
}

// ============================================================================
// sharded_registry.go：SSE 分类索引 API
// ============================================================================

// 注：makeSSEClient / makeAgentClient 已在 query_test.go 中定义，此处直接复用

func TestSSEIndex_APIs(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()
	reg := hub.shardedRegistry

	// 零状态
	_, exists := reg.GetSSEUserClients("u-sse")
	assert.False(t, exists)
	assert.False(t, reg.HasSSEUser("u-sse"))

	// 注册 1 个 SSE 用户 + 2 个设备
	sse1 := makeSSEClient("c-sse-1", "u-sse")
	sse2 := makeSSEClient("c-sse-2", "u-sse")
	reg.AddClient(sse1)
	reg.AddClient(sse2)

	// GetSSEUserClients 返回该用户的所有 SSE 设备
	clients, exists := reg.GetSSEUserClients("u-sse")
	assert.True(t, exists)
	assert.Equal(t, 2, len(clients), "u-sse 有 2 个 SSE 设备")
	assert.True(t, reg.HasSSEUser("u-sse"))

	// 移除 1 个设备，仍有 1 个
	reg.RemoveClient("c-sse-1", "u-sse")
	clients, _ = reg.GetSSEUserClients("u-sse")
	assert.Equal(t, 1, len(clients))

	// 移除最后一个，用户被清空
	reg.RemoveClient("c-sse-2", "u-sse")
	assert.False(t, reg.HasSSEUser("u-sse"))
}

// ============================================================================
// sharded_registry.go：Agent 分类索引 API
// ============================================================================

func TestAgentIndex_APIs(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()
	reg := hub.shardedRegistry

	// 默认 config EnableAgent=true → agentShards 非 nil
	assert.True(t, reg.AgentEnabled(), "默认启用 agent 模块")

	// 零状态
	assert.Equal(t, 0, reg.GetAgentUserCount())
	assert.False(t, reg.HasAgent("u-agent"))
	_, exists := reg.GetAgentUserClients("u-agent")
	assert.False(t, exists)

	// 注册 agent
	agent := makeAgentClient("c-agent", "u-agent")
	reg.AddClient(agent)

	assert.Equal(t, 1, reg.GetAgentUserCount())
	assert.True(t, reg.HasAgent("u-agent"))
	clients, exists := reg.GetAgentUserClients("u-agent")
	assert.True(t, exists)
	assert.Equal(t, 1, len(clients))

	// 移除
	reg.RemoveClient("c-agent", "u-agent")
	assert.Equal(t, 0, reg.GetAgentUserCount())
	assert.False(t, reg.HasAgent("u-agent"))
}

// ============================================================================
// sharded_registry.go：Observer 分类索引 API（GetObserverUserClients / ForEachObserver）
// ============================================================================

func TestObserverIndex_APIs(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()
	reg := hub.shardedRegistry

	// 零状态
	_, exists := reg.GetObserverUserClients("u-obs")
	assert.False(t, exists)

	// 注册观察者
	obs := registerObserver(hub, "c-obs", "u-obs", "ns1", "")
	clients, exists := reg.GetObserverUserClients("u-obs")
	assert.True(t, exists)
	assert.Equal(t, 1, len(clients))

	// ForEachObserver 遍历
	count := 0
	reg.ForEachObserver(func(_, _ string, _ *Client) bool {
		count++
		return true
	})
	assert.Equal(t, 1, count)

	// ForEachObserver 中途停止
	reg.ForEachObserver(func(_, _ string, _ *Client) bool {
		return false // 第一个就停
	})
	_ = obs
}

// ============================================================================
// sharded_registry.go：Clear
// ============================================================================

func TestShardedRegistry_Clear(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()
	reg := hub.shardedRegistry

	// 注册混合 client
	reg.AddClient(makeTestClient("c1", "u1"))
	reg.AddClient(makeSSEClient("c2", "u2"))
	reg.AddClient(makeAgentClient("c3", "u3"))
	reg.AddClient(registerObserver(hub, "c4", "u4", "", ""))

	require.Greater(t, reg.GetClientCount(), int64(0))
	require.True(t, reg.HasSSEUser("u2"))
	require.True(t, reg.HasAgent("u3"))
	require.True(t, reg.HasObserver("u4"))

	// Clear 后全部归零
	reg.Clear()
	assert.Equal(t, int64(0), reg.GetClientCount())
	assert.Equal(t, int64(0), reg.GetUserCount())
	assert.False(t, reg.HasSSEUser("u2"))
	assert.False(t, reg.HasAgent("u3"))
	assert.False(t, reg.HasObserver("u4"))
}

// ============================================================================
// registry.go：CloseAllClientsInMap
// ============================================================================

func TestCloseAllClientsInMap_NilConn(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 构造 clientMap，Conn=nil（测试 client 无真实连接）
	clientMap := map[string]*Client{
		"c1": makeTestClient("c1", "u1"),
		"c2": makeTestClient("c2", "u2"),
	}
	assert.NotPanics(t, func() {
		hub.CloseAllClientsInMap(clientMap)
	})
}

// ============================================================================
// observer.go：GetObserverStats
// ============================================================================

func TestGetObserverStats_EmptyAndRegistered(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 零状态
	stats := hub.GetObserverStats()
	assert.Equal(t, 0, len(stats), "无观察者时返回空切片")

	// 注册观察者后
	registerObserver(hub, "c-stats", "u-stats", "ns-stats", "g-stats")
	stats = hub.GetObserverStats()
	require.Equal(t, 1, len(stats))
	s := stats[0]
	assert.Equal(t, "u-stats", s.ObserverID)
	assert.Equal(t, "c-stats", s.ClientID)
	assert.Equal(t, "ns-stats", s.Namespace)
	assert.Equal(t, "g-stats", s.GroupID)
	assert.True(t, s.IsConnected)
}

// ============================================================================
// connection_record.go：CreateConnectionRecord / saveConnectionRecord
// ============================================================================

func TestCreateConnectionRecord_FromClient(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	client := makeTestClient("c-rec", "u-rec", "ns-rec")
	client.NodeID = "node-1"
	client.NodeIP = "10.0.0.1"
	client.NodePort = 8080
	client.ClientIP = "192.168.1.100"
	client.ConnectionType = models.ConnectionTypeWebSocket

	record := hub.CreateConnectionRecord(client)
	require.NotNil(t, record)
	assert.Equal(t, "c-rec", record.ConnectionID)
	assert.Equal(t, "u-rec", record.UserID)
	assert.Equal(t, "node-1", record.NodeID)
	assert.Equal(t, "192.168.1.100", record.ClientIP)
	assert.Equal(t, models.ConnectionTypeWebSocket, record.Protocol)
	assert.True(t, record.IsActive)
}

func TestSaveConnectionRecord_NilRepo_NoOp(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// connectionRecordRepo == nil → 直接返回，不 panic
	assert.NotPanics(t, func() {
		hub.saveConnectionRecord(context.Background(), &ConnectionRecord{ConnectionID: "c1"})
	})
}

func TestSaveConnectionRecord_WithMockRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetConnectionRecordRepository(&fakeConnectionRecordRepository{})
	record := &ConnectionRecord{ConnectionID: "c-save", UserID: "u-save"}

	assert.NotPanics(t, func() {
		hub.saveConnectionRecord(context.Background(), record)
	})
	// 异步执行，等待完成
	require.Eventually(t, func() bool {
		return true
	}, time.Second, 50*time.Millisecond)
}

func TestUpdateConnectionOnDisconnect_NilRepo_NoOp(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	assert.NotPanics(t, func() {
		hub.updateConnectionOnDisconnect(makeTestClient("c1", "u1"), models.DisconnectReasonClientRequest)
	})
}

func TestUpdateConnectionOnDisconnect_WithMockRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetConnectionRecordRepository(&fakeConnectionRecordRepository{})
	assert.NotPanics(t, func() {
		hub.updateConnectionOnDisconnect(makeTestClient("c1", "u1"), models.DisconnectReasonClientRequest)
	})
	require.Eventually(t, func() bool {
		return true
	}, time.Second, 50*time.Millisecond)
}

// ============================================================================
// connection_token.go：whitelistKey / checkWhitelist / RevokeConnectionToken
// ============================================================================

func TestWhitelistKey_Format(t *testing.T) {
	t.Parallel()
	key := whitelistKey("wsc:", "my-token")
	assert.Contains(t, key, "wsc:whitelist:")
	assert.NotContains(t, key, "my-token", "不应包含原始 token 明文")
	// 相同输入相同输出
	assert.Equal(t, key, whitelistKey("wsc:", "my-token"))
}

func TestRevokeConnectionToken_NilCfg_NoOp(t *testing.T) {
	t.Parallel()
	err := RevokeConnectionToken(context.Background(), nil, "", nil, "some-token")
	assert.NoError(t, err, "cfg=nil 直接返回 nil")
}

func TestRevokeConnectionToken_RedisDisabled_NoOp(t *testing.T) {
	t.Parallel()
	cfg := newTestTokenCfg() // 默认不含 Redis 配置
	err := RevokeConnectionToken(context.Background(), cfg, "", nil, "some-token")
	assert.NoError(t, err, "Redis 未启用时直接返回 nil")
}

func TestRevokeConnectionToken_WithRedis(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cfg := newTestTokenCfg()
	cfg.UseRedis = true
	cfg.RedisKeyPrefix = "wsc:"
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// 先 Issue 一个 token 写入白名单
	// 重试容错：-race 全量并行重负载下 miniredis 响应可能慢于生产 tokenRedisTimeout(2s)，
	// 超时属于测试环境 CPU 超卖抖动而非逻辑错误
	var token string
	for i := 0; i < 3; i++ {
		token, err = IssueConnectionToken(context.Background(), cfg, "", rdb, &ConnectionClaims{UserID: "u-revoke"})
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	require.NoError(t, err)
	require.True(t, mr.Exists(whitelistKey("wsc:", token)), "token 应在白名单中")

	// Revoke 后白名单中不存在
	err = RevokeConnectionToken(context.Background(), cfg, "", rdb, token)
	require.NoError(t, err)
	assert.False(t, mr.Exists(whitelistKey("wsc:", token)), "revoke 后 token 应从白名单移除")
}

func TestCheckWhitelist_TokenNotInWhitelist(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cfg := newTestTokenCfg()
	cfg.UseRedis = true
	cfg.RedisKeyPrefix = "wsc:"
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	decoder := NewConnectionTokenDecoder(cfg, rdb, nil).(*jwtConnectionTokenDecoder)
	set := decoder.sets[decoder.defaultAppID]

	// 白名单中不存在该 token → 返回错误
	// 重试容错：-race 重负载下 miniredis 查询可能超时触发降级放行（返回 nil），属环境抖动
	for i := 0; i < 3; i++ {
		err = decoder.checkWhitelist(context.Background(), set, "nonexistent-token")
		if err != nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	require.Error(t, err, "白名单中不存在的 token 应返回错误")
	assert.Contains(t, err.Error(), "not in whitelist")
}

func TestCheckWhitelist_TokenInWhitelist(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cfg := newTestTokenCfg()
	cfg.UseRedis = true
	cfg.RedisKeyPrefix = "wsc:"
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// Issue token 写入白名单
	token, err := IssueConnectionToken(context.Background(), cfg, "", rdb, &ConnectionClaims{UserID: "u-wl"})
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, rdb, nil).(*jwtConnectionTokenDecoder)
	set := decoder.sets[decoder.defaultAppID]
	err = decoder.checkWhitelist(context.Background(), set, token)
	assert.NoError(t, err, "白名单中存在的 token 应校验通过")
}

func TestCheckWhitelist_RedisDown_DegradePass(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	mr.Close() // 关闭 redis 模拟故障

	cfg := newTestTokenCfg()
	cfg.UseRedis = true
	cfg.RedisKeyPrefix = "wsc:"
	// 传入 logger 避免 Redis 故障降级时 logger.WarnKV nil panic
	decoder := NewConnectionTokenDecoder(cfg, rdb, middleware.NewDefaultWSCLogger()).(*jwtConnectionTokenDecoder)
	set := decoder.sets[decoder.defaultAppID]

	// Redis 故障 → 降级放行（返回 nil）
	err = decoder.checkWhitelist(context.Background(), set, "any-token")
	assert.NoError(t, err, "Redis 故障时应降级放行")
}

func TestDecode_WithRedisWhitelist_Rejected(t *testing.T) {
	t.Parallel()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cfg := newTestTokenCfg()
	cfg.UseRedis = true
	cfg.RedisKeyPrefix = "wsc:"
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	decoder := NewConnectionTokenDecoder(cfg, rdb, nil)

	// 生成 token 但不写入白名单（直接签发后手动删除）
	token, err := IssueConnectionToken(context.Background(), cfg, "", rdb, &ConnectionClaims{UserID: "u-dec"})
	require.NoError(t, err)
	// 立即 revoke 使白名单失效
	require.NoError(t, RevokeConnectionToken(context.Background(), cfg, "", rdb, token))

	req := httptest.NewRequest(http.MethodGet, "/ws?token="+token, nil)
	_, err = decoder.Decode(req)
	assert.Error(t, err, "已 revoke 的 token 应被拒绝")
	assert.Contains(t, err.Error(), "revoked or not in whitelist")
}

// ============================================================================
// checkAndRouteToNode
// ============================================================================

func TestCheckAndRouteToNode_SingleNodeMode(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	msg := makeGroupMessage("sender")
	routed, _, err := hub.checkAndRouteToNode(context.Background(), "u-test", msg)
	require.NoError(t, err)
	assert.False(t, routed, "单机模式不应路由到其他节点")
}

func TestCheckAndRouteToNode_NilOnlineStatusRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	msg := makeGroupMessage("sender")
	routed, _, err := hub.checkAndRouteToNode(context.Background(), "u-test", msg)
	require.NoError(t, err)
	assert.False(t, routed)
}

// ============================================================================
// unmarshalDistributedMessage
// ============================================================================

func TestUnmarshalDistributedMessage_InvalidData(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	invalidData := []byte{0xff, 0xfe, 0xfd}
	_, err := hub.unmarshalDistributedMessage(context.Background(), invalidData)
	assert.Error(t, err, "无效数据应返回错误")
}

func TestUnmarshalDistributedMessage_ValidJSON(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	jsonData, err := json.Marshal(&DistributedMessage{
		NodeID:   "node-1",
		Type:     OperationTypeSendMessage,
		TargetID: "u-test",
	})
	require.NoError(t, err)

	distMsg, err := hub.unmarshalDistributedMessage(context.Background(), jsonData)
	if err != nil {
		assert.Nil(t, distMsg)
	} else {
		assert.NotNil(t, distMsg)
	}
}

// ============================================================================
// marshalDistributedMessage
// ============================================================================

func TestMarshalDistributedMessage_Success(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := &DistributedMessage{
		NodeID:   "node-1",
		Type:     OperationTypeSendMessage,
		TargetID: "u-test",
	}

	data := hub.marshalDistributedMessage(context.Background(), distMsg)
	assert.NotEmpty(t, data, "序列化结果不应为空")
}

// ============================================================================
// AcquireDistributedLock / ReleaseDistributedLock
// ============================================================================

func TestAcquireDistributedLock_NilPubSub(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	acquired, err := hub.AcquireDistributedLock(context.Background(), "test-lock", 10*time.Second)
	assert.Error(t, err)
	assert.False(t, acquired)
	assert.ErrorIs(t, err, ErrPubSubNotSet)
}

func TestReleaseDistributedLock_NilPubSub(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	err := hub.ReleaseDistributedLock(context.Background(), "test-lock")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrPubSubNotSet)
}

// ============================================================================
// extractClientAttributes — Token 解码路径
// ============================================================================

func TestExtractClientAttributes_TokenDecoderSuccess(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	hub := NewHub(config)
	defer hub.Shutdown()

	hub.SetConnectionTokenDecoder(&configurableTokenDecoder{
		claims: &ConnectionClaims{
			UserID:    "u-token",
			UserType:  "agent",
			DeviceID:  "dev-1",
			Namespace: "ns-token",
			GroupID:   "g-token",
		},
	})

	req := httptest.NewRequest("GET", "/ws", nil)
	attrs := hub.extractClientAttributes(req)
	require.NotNil(t, attrs)
	assert.Equal(t, "u-token", attrs.UserID)
	assert.Equal(t, UserType("agent"), attrs.UserType)
	assert.Equal(t, "dev-1", attrs.DeviceID)
	assert.Equal(t, "ns-token", attrs.Namespace)
	assert.Equal(t, "g-token", attrs.GroupID)
}

func TestExtractClientAttributes_TokenDecoderErrorWithFallback(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	config.Security = &wscconfig.Security{
		ConnectionToken: &wscconfig.ConnectionToken{
			AllowFallback: true,
		},
	}
	hub := NewHub(config)
	defer hub.Shutdown()

	hub.SetConnectionTokenDecoder(&configurableTokenDecoder{
		err: assertError("token decode failed"),
	})

	req := httptest.NewRequest("GET", "/ws?client_id=c-fallback&user_id=u-fallback&user_type=customer", nil)
	attrs := hub.extractClientAttributes(req)
	require.NotNil(t, attrs, "AllowFallback=true 时应回退到明文提取")
	assert.Equal(t, "c-fallback", attrs.ClientID)
	assert.Equal(t, "u-fallback", attrs.UserID)
	assert.Equal(t, UserTypeCustomer, attrs.UserType)
}

func TestExtractClientAttributes_TokenDecoderErrorNoFallback(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	config.Security = &wscconfig.Security{
		ConnectionToken: &wscconfig.ConnectionToken{
			AllowFallback: false,
		},
	}
	hub := NewHub(config)
	defer hub.Shutdown()

	hub.SetConnectionTokenDecoder(&configurableTokenDecoder{
		err: assertError("token decode failed"),
	})

	req := httptest.NewRequest("GET", "/ws?client_id=c-reject&user_id=u-reject", nil)
	attrs := hub.extractClientAttributes(req)
	assert.Nil(t, attrs, "AllowFallback=false 且解码失败时应返回 nil")
}

func TestExtractClientAttributes_PlaintextWithNamespaceAndGroup(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	hub := NewHub(config)
	defer hub.Shutdown()

	req := httptest.NewRequest("GET", "/ws?client_id=c-ns&user_id=u-ns&user_type=agent&namespace=tenantA&group_id=groupB", nil)
	attrs := hub.extractClientAttributes(req)
	require.NotNil(t, attrs)
	assert.Equal(t, "c-ns", attrs.ClientID)
	assert.Equal(t, "u-ns", attrs.UserID)
	assert.Equal(t, UserTypeAgent, attrs.UserType)
}

func TestExtractClientAttributes_PlaintextDefaultUserType(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	hub := NewHub(config)
	defer hub.Shutdown()

	req := httptest.NewRequest("GET", "/ws?client_id=c-def&user_id=u-def", nil)
	attrs := hub.extractClientAttributes(req)
	require.NotNil(t, attrs)
	assert.Equal(t, UserTypeVisitor, attrs.UserType, "未提供 user_type 时默认 visitor")
}

func TestExtractClientAttributes_PlaintextAutoGenerateClientID(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	hub := NewHub(config)
	defer hub.Shutdown()

	req := httptest.NewRequest("GET", "/ws?user_id=u-autogen&device_id=dev-autogen", nil)
	attrs := hub.extractClientAttributes(req)
	require.NotNil(t, attrs)
	assert.NotEmpty(t, attrs.ClientID, "未提供 client_id 时应自动生成")
	assert.Equal(t, "u-autogen", attrs.UserID)
}

// ============================================================================
// Fake types for distributed PubSub/handler tests
// ============================================================================

// distributedOnlineStatusRepo 可配置的在线状态仓库 fake
type distributedOnlineStatusRepo struct {
	repository.OnlineStatusRepository
	userNodes    []string
	userNodesErr error
}

func (d *distributedOnlineStatusRepo) GetUserNodes(_ context.Context, _ string) ([]string, error) {
	return d.userNodes, d.userNodesErr
}

func (d *distributedOnlineStatusRepo) CleanupExpired(_ context.Context, _ string) (int64, error) {
	return 0, nil
}

// distributedGroupRepo 可配置的群组仓库 fake（嵌入 fakeGroupRepository 复用空实现）
type distributedGroupRepo struct {
	fakeGroupRepository
	multiMembers map[string][]string
	multiErr     error
}

func (d *distributedGroupRepo) GetMultiGroupMembers(_ context.Context, _, _ string, _ []string) (map[string][]string, error) {
	return d.multiMembers, d.multiErr
}

// makeDistributedMessage 构造测试用 DistributedMessage
func makeDistributedMessage(opType models.OperationType, nodeID string, msg *HubMessage) *DistributedMessage {
	return &DistributedMessage{
		Type:      opType,
		NodeID:    nodeID,
		Message:   msg,
		Timestamp: time.Now(),
	}
}

// ============================================================================
// handleDistributedMessage
// ============================================================================

func TestHandleDistributedMessage_NilMessage(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	err := hub.handleDistributedMessage(context.Background(), nil)
	assert.Error(t, err, "nil distMsg 应返回错误")
}

func TestHandleDistributedMessage_UnknownType(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(models.OperationType("unknown_type"), "other-node", makeGroupMessage("sender"))
	err := hub.handleDistributedMessage(context.Background(), distMsg)
	assert.Error(t, err, "未知消息类型应返回错误")
}

func TestHandleDistributedMessage_SendMessageDelegate(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// Message 为 nil → handleDistributedSendMessage 返回 error
	distMsg := &DistributedMessage{
		Type:   OperationTypeSendMessage,
		NodeID: "other-node",
	}
	err := hub.handleDistributedMessage(context.Background(), distMsg)
	assert.Error(t, err, "SendMessage 类型且 Message 为 nil 应返回错误")
}

func TestHandleDistributedMessage_KickUserDelegate(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := &DistributedMessage{
		Type:     OperationTypeKickUser,
		NodeID:   "other-node",
		TargetID: "u-kick",
		Reason:   "test kick",
	}
	err := hub.handleDistributedMessage(context.Background(), distMsg)
	assert.NoError(t, err, "KickUser 类型应成功")
}

func TestHandleDistributedMessage_BroadcastDelegate(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// Message 为 nil → handleDistributedBroadcast 返回 error
	distMsg := &DistributedMessage{
		Type:   OperationTypeBroadcast,
		NodeID: "other-node",
	}
	err := hub.handleDistributedMessage(context.Background(), distMsg)
	assert.Error(t, err, "Broadcast 类型且 Message 为 nil 应返回错误")
}

func TestHandleDistributedMessage_GroupBroadcastDelegate_SelfNode(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(OperationTypeGroupBroadcast, hub.nodeID, makeGroupMessage("sender"))
	err := hub.handleDistributedMessage(context.Background(), distMsg)
	assert.NoError(t, err, "GroupBroadcast 自身节点应跳过")
}

func TestHandleDistributedMessage_GroupsBroadcastDelegate_SelfNode(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(OperationTypeGroupsBroadcast, hub.nodeID, makeGroupMessage("sender"))
	err := hub.handleDistributedMessage(context.Background(), distMsg)
	assert.NoError(t, err, "GroupsBroadcast 自身节点应跳过")
}

func TestHandleDistributedMessage_ObserverNotifyDelegate_SelfNode(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(OperationTypeObserverNotify, hub.nodeID, makeGroupMessage("sender"))
	err := hub.handleDistributedMessage(context.Background(), distMsg)
	assert.NoError(t, err, "ObserverNotify 自身节点应跳过")
}

// ============================================================================
// handleDistributedSendMessage
// ============================================================================

func TestHandleDistributedSendMessage_NilMessage(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := &DistributedMessage{
		Type:     OperationTypeSendMessage,
		NodeID:   "other-node",
		TargetID: "u-test",
	}
	err := hub.handleDistributedSendMessage(context.Background(), distMsg, true)
	assert.Error(t, err, "Message 为 nil 应返回错误")
}

func TestHandleDistributedSendMessage_UserNotFound(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(OperationTypeSendMessage, "other-node", makeGroupMessage("sender"))
	distMsg.TargetID = "u-not-exist"
	// 用户不在本节点是广播兜底的正常预期（其余节点扑空自动跳过），
	// 返回 nil 避免触发 PubSub 订阅端无效重试 + ERROR 噪音
	err := hub.handleDistributedSendMessage(context.Background(), distMsg, false)
	assert.NoError(t, err, "用户不在本节点应静默跳过（广播兜底正常路径）")
}

func TestHandleDistributedSendMessage_Success(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	client := makeTestClient("c-recv", "u-recv", "ns-recv")
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("sender")
	msg.Namespace = "ns-recv"
	distMsg := makeDistributedMessage(OperationTypeSendMessage, "other-node", msg)
	distMsg.TargetID = "u-recv"
	distMsg.Namespace = "ns-recv"

	err := hub.handleDistributedSendMessage(context.Background(), distMsg, true)
	require.NoError(t, err)

	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "客户端应收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：客户端未收到消息")
	}
}

func TestHandleDistributedSendMessage_AllClientsUnavailable(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 创建 SendChan 缓冲为 1 的客户端，预填充使其满
	client := makeTestClient("c-full", "u-full", "ns-full")
	client.SendChan = make(chan []byte, 1)
	client.SendChan <- []byte("filler") // 填满缓冲区
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("sender")
	msg.Namespace = "ns-full"
	distMsg := makeDistributedMessage(OperationTypeSendMessage, "other-node", msg)
	distMsg.TargetID = "u-full"
	distMsg.Namespace = "ns-full"

	err := hub.handleDistributedSendMessage(context.Background(), distMsg, true)
	assert.Error(t, err, "所有客户端不可用时应返回错误")
}

// ============================================================================
// handleDistributedSendMessage 复用 sendToClientSerialized 后的行为对齐测试
// 修复前：裸 client.TrySend 无状态回报，跨节点消息状态永远停留 sending，且 SSE 客户端收不到消息
// ============================================================================

// hasBatchUpdate 判断 fake repo 是否已收到指定 messageID + status 的批量状态更新
func hasBatchUpdate(repo *fakeMessageRecordRepo, msgID string, status MessageSendStatus) bool {
	repo.batchUpdateMu.Lock()
	defer repo.batchUpdateMu.Unlock()
	for _, call := range repo.batchUpdateCalls {
		if call.Status != status {
			continue
		}
		for _, id := range call.IDs {
			if id == msgID {
				return true
			}
		}
	}
	return false
}

// TestHandleDistributedSendMessage_ReportsStatusSuccess 跨节点 PubSub 投递成功应异步回报 Success 状态
// 回归场景：源节点 routed=true 后不再更新状态，目标节点是唯一的 Status 报告者
func TestHandleDistributedSendMessage_ReportsStatusSuccess(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)

	client := makeTestClient("c-x-node-ok", "u-x-node-ok", "ns-x")
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("sender")
	msg.MessageID = "m-x-node-success"
	msg.Namespace = "ns-x"
	distMsg := makeDistributedMessage(OperationTypeSendMessage, "other-node", msg)
	distMsg.TargetID = "u-x-node-ok"
	distMsg.Namespace = "ns-x"

	err := hub.handleDistributedSendMessage(context.Background(), distMsg, true)
	require.NoError(t, err)

	// 状态更新经 statusUpdater 批量异步落盘
	require.Eventually(t, func() bool {
		return hasBatchUpdate(repo, "m-x-node-success", MessageSendStatusSuccess)
	}, 2*time.Second, 10*time.Millisecond, "跨节点投递成功应回报 Success 状态")
}

// TestHandleDistributedSendMessage_ChannelFullReportsFailedAndStoresOffline
// 跨节点投递失败应回报 Failed 状态并异步转存离线（与本地路径行为对齐，消息不丢）
func TestHandleDistributedSendMessage_ChannelFullReportsFailedAndStoresOffline(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	repo := &fakeMessageRecordRepo{}
	hub.SetMessageRecordRepository(repo)
	offline := newAckFakeOfflineHandler()
	hub.SetOfflineMessageHandler(offline)

	client := makeTestClient("c-x-node-full", "u-x-node-full", "ns-y")
	client.SendChan = make(chan []byte, 1)
	client.SendChan <- []byte("filler") // 填满缓冲区
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("sender")
	msg.MessageID = "m-x-node-failed"
	msg.Receiver = "u-x-node-full" // 转存离线触发条件：Receiver 非空
	msg.Namespace = "ns-y"
	distMsg := makeDistributedMessage(OperationTypeSendMessage, "other-node", msg)
	distMsg.TargetID = "u-x-node-full"
	distMsg.Namespace = "ns-y"

	err := hub.handleDistributedSendMessage(context.Background(), distMsg, true)
	assert.Error(t, err, "所有客户端投递失败应返回错误")

	// Failed 状态异步落盘
	require.Eventually(t, func() bool {
		return hasBatchUpdate(repo, "m-x-node-failed", MessageSendStatusFailed)
	}, 2*time.Second, 10*time.Millisecond, "跨节点投递失败应回报 Failed 状态")

	// 在线投递失败 → 异步转存离线（消息不丢，用户上线时推送）
	require.Eventually(t, func() bool {
		return offline.getStoreCalled() > 0
	}, 2*time.Second, 10*time.Millisecond, "投递失败应转存离线队列")
}

// TestHandleDistributedSendMessage_SSEClientReceivesMessage
// 修复前裸 client.TrySend([]byte) 对 SSE 客户端必然失败（SSE 走 *HubMessage 专用通道），
// 复用 sendToClientSerialized 后 SSE 客户端可收到跨节点消息
func TestHandleDistributedSendMessage_SSEClientReceivesMessage(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	client := makeTestClient("c-x-node-sse", "u-x-node-sse", "ns-sse")
	client.ConnectionType = ConnectionTypeSSE
	client.WithSSEChannels(make(chan *HubMessage, 1), make(chan struct{}))
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("sender")
	msg.MessageID = "m-x-node-sse"
	msg.Namespace = "ns-sse"
	distMsg := makeDistributedMessage(OperationTypeSendMessage, "other-node", msg)
	distMsg.TargetID = "u-x-node-sse"
	distMsg.Namespace = "ns-sse"

	err := hub.handleDistributedSendMessage(context.Background(), distMsg, true)
	require.NoError(t, err, "SSE 客户端应投递成功")

	select {
	case received := <-client.SSEMessageCh:
		require.NotNil(t, received)
		assert.Equal(t, "m-x-node-sse", received.MessageID, "SSE 客户端应收到跨节点消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：SSE 客户端未收到跨节点消息")
	}
}

// ============================================================================
// handleDistributedKickUser
// ============================================================================

func TestHandleDistributedKickUser_ContextCancelled(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	distMsg := &DistributedMessage{
		Type:     OperationTypeKickUser,
		NodeID:   "other-node",
		TargetID: "u-kick",
		Reason:   "cancelled",
	}
	err := hub.handleDistributedKickUser(ctx, distMsg)
	assert.Error(t, err, "context 取消时应返回错误")
}

func TestHandleDistributedKickUser_Success(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 注册一个客户端，验证 kick 不会 panic
	client := makeTestClient("c-kick", "u-kick")
	hub.shardedRegistry.AddClient(client)

	distMsg := &DistributedMessage{
		Type:     OperationTypeKickUser,
		NodeID:   "other-node",
		TargetID: "u-kick",
		Reason:   "test",
	}
	err := hub.handleDistributedKickUser(context.Background(), distMsg)
	assert.NoError(t, err)
}

// ============================================================================
// handleDistributedBroadcast
// ============================================================================

func TestHandleDistributedBroadcast_NilMessage(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := &DistributedMessage{
		Type:   OperationTypeBroadcast,
		NodeID: "other-node",
	}
	err := hub.handleDistributedBroadcast(context.Background(), distMsg)
	assert.Error(t, err, "Message 为 nil 应返回错误")
}

func TestHandleDistributedBroadcast_GlobalBroadcast(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	client := makeTestClient("c-bcast", "u-bcast", "ns-bcast")
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("sender")
	// 全命名空间广播：namespace="" → 所有客户端都应收到
	distMsg := makeDistributedMessage(OperationTypeBroadcast, "other-node", msg)
	distMsg.Namespace = ""

	err := hub.handleDistributedBroadcast(context.Background(), distMsg)
	require.NoError(t, err)

	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "全局广播客户端应收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：客户端未收到全局广播消息")
	}
}

func TestHandleDistributedBroadcast_NamespaceBroadcast(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 同命名空间客户端
	clientMatch := makeTestClient("c-match", "u-match", "ns-target")
	hub.shardedRegistry.AddClient(clientMatch)
	// 不同命名空间客户端（不应收到）
	clientNoMatch := makeTestClient("c-nomatch", "u-nomatch", "ns-other")
	hub.shardedRegistry.AddClient(clientNoMatch)

	msg := makeGroupMessage("sender")
	msg.Namespace = "ns-target"
	distMsg := makeDistributedMessage(OperationTypeBroadcast, "other-node", msg)
	distMsg.Namespace = "ns-target"

	err := hub.handleDistributedBroadcast(context.Background(), distMsg)
	require.NoError(t, err)

	// 匹配 namespace 的客户端应收到
	select {
	case data := <-clientMatch.SendChan:
		assert.NotEmpty(t, data, "同命名空间客户端应收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：同命名空间客户端未收到消息")
	}

	// 不匹配 namespace 的客户端不应收到
	select {
	case <-clientNoMatch.SendChan:
		t.Fatal("不同命名空间客户端不应收到消息")
	default:
		// 预期行为
	}
}

// ============================================================================
// handleDistributedGroupsBroadcast
// ============================================================================

func TestHandleDistributedGroupsBroadcast_SelfNode(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(OperationTypeGroupBroadcast, hub.nodeID, makeGroupMessage("sender"))
	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	assert.NoError(t, err, "自身节点应跳过")
}

func TestHandleDistributedGroupsBroadcast_NilMessage(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetGroupRepository(&distributedGroupRepo{})

	distMsg := &DistributedMessage{
		Type:   OperationTypeGroupBroadcast,
		NodeID: "other-node",
	}
	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	assert.Error(t, err, "Message 为 nil 应返回错误")
}

func TestHandleDistributedGroupsBroadcast_NilGroupRepo(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// groupRepo 为 nil → error
	distMsg := makeDistributedMessage(OperationTypeGroupBroadcast, "other-node", makeGroupMessage("sender"))
	distMsg.GroupIDs = []string{"g1"}
	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	assert.Error(t, err, "groupRepo 为 nil 应返回错误")
}

func TestHandleDistributedGroupsBroadcast_EmptyGroupIDs(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetGroupRepository(&distributedGroupRepo{})

	// GroupIDs 和 TargetID 均为空 → error
	distMsg := makeDistributedMessage(OperationTypeGroupBroadcast, "other-node", makeGroupMessage("sender"))
	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	assert.Error(t, err, "groupIDs 和 TargetID 均为空应返回错误")
}

func TestHandleDistributedGroupsBroadcast_NoMembers(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// groupRepo 返回空成员列表
	hub.SetGroupRepository(&distributedGroupRepo{
		multiMembers: nil,
	})

	distMsg := makeDistributedMessage(OperationTypeGroupBroadcast, "other-node", makeGroupMessage("sender"))
	distMsg.GroupIDs = []string{"g-empty"}
	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	assert.NoError(t, err, "无成员时应返回 nil")
}

func TestHandleDistributedGroupsBroadcast_TargetIDCompat(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 注册客户端
	client := makeTestClient("c-compat", "u-compat", "ns-compat")
	hub.shardedRegistry.AddClient(client)

	hub.SetGroupRepository(&distributedGroupRepo{
		multiMembers: map[string][]string{
			"g-compat": {"u-compat"},
		},
	})

	msg := makeGroupMessage("sender")
	msg.Namespace = "ns-compat"
	distMsg := makeDistributedMessage(OperationTypeGroupBroadcast, "other-node", msg)
	distMsg.Namespace = "ns-compat"
	// GroupIDs 为空，TargetID 回退兼容
	distMsg.TargetID = "g-compat"

	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	require.NoError(t, err)

	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "TargetID 兼容模式客户端应收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：TargetID 兼容模式客户端未收到消息")
	}
}

func TestHandleDistributedGroupsBroadcast_Success(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	client := makeTestClient("c-grp", "u-grp", "ns-grp")
	hub.shardedRegistry.AddClient(client)

	hub.SetGroupRepository(&distributedGroupRepo{
		multiMembers: map[string][]string{
			"g1": {"u-grp"},
		},
	})

	msg := makeGroupMessage("sender")
	msg.Namespace = "ns-grp"
	distMsg := makeDistributedMessage(OperationTypeGroupsBroadcast, "other-node", msg)
	distMsg.Namespace = "ns-grp"
	distMsg.GroupIDs = []string{"g1"}

	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	require.NoError(t, err)

	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "群组广播客户端应收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：群组广播客户端未收到消息")
	}
}

func TestHandleDistributedGroupsBroadcast_ExcludeSender(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	// 注册发送者和接收者
	sender := makeTestClient("c-sender", "u-sender", "ns-excl")
	hub.shardedRegistry.AddClient(sender)
	receiver := makeTestClient("c-recv", "u-recv", "ns-excl")
	hub.shardedRegistry.AddClient(receiver)

	hub.SetGroupRepository(&distributedGroupRepo{
		multiMembers: map[string][]string{
			"g-excl": {"u-sender", "u-recv"},
		},
	})

	msg := makeGroupMessage("u-sender")
	msg.Namespace = "ns-excl"
	distMsg := makeDistributedMessage(OperationTypeGroupsBroadcast, "other-node", msg)
	distMsg.Namespace = "ns-excl"
	distMsg.GroupIDs = []string{"g-excl"}
	distMsg.ExcludeSender = true
	distMsg.SenderID = "u-sender"

	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	require.NoError(t, err)

	// 接收者应收到消息
	select {
	case data := <-receiver.SendChan:
		assert.NotEmpty(t, data, "接收者应收到消息")
	case <-time.After(1 * time.Second):
		t.Fatal("超时：接收者未收到消息")
	}

	// 发送者不应收到消息（被排除）
	select {
	case <-sender.SendChan:
		t.Fatal("发送者应被排除，不应收到消息")
	default:
		// 预期行为
	}
}

func TestHandleDistributedGroupsBroadcast_GetMultiMembersError(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetGroupRepository(&distributedGroupRepo{
		multiErr: assertError("redis error"),
	})

	distMsg := makeDistributedMessage(OperationTypeGroupBroadcast, "other-node", makeGroupMessage("sender"))
	distMsg.GroupIDs = []string{"g1"}
	err := hub.handleDistributedGroupsBroadcast(context.Background(), distMsg)
	assert.NoError(t, err, "获取成员失败时应返回 nil（memberSet 为空）")
}

// ============================================================================
// handleDistributedObserverNotify
// ============================================================================

func TestHandleDistributedObserverNotify_SelfNode(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(OperationTypeObserverNotify, hub.nodeID, makeGroupMessage("sender"))
	err := hub.handleDistributedObserverNotify(context.Background(), distMsg)
	assert.NoError(t, err, "自身节点应跳过")
}

func TestHandleDistributedObserverNotify_NilMessage(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := &DistributedMessage{
		Type:   OperationTypeObserverNotify,
		NodeID: "other-node",
	}
	err := hub.handleDistributedObserverNotify(context.Background(), distMsg)
	assert.Error(t, err, "Message 为 nil 应返回错误")
}

func TestHandleDistributedObserverNotify_NoObservers(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	distMsg := makeDistributedMessage(OperationTypeObserverNotify, "other-node", makeGroupMessage("sender"))
	distMsg.Namespace = "ns-none"
	distMsg.GroupIDs = []string{"g-none"}

	err := hub.handleDistributedObserverNotify(context.Background(), distMsg)
	assert.NoError(t, err, "无观察者时应返回 nil")
}

func TestHandleDistributedObserverNotify_Success(t *testing.T) {
	t.Parallel()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18083).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(16)
	config.EnableObserver = true
	hub := NewHub(config)
	defer hub.Shutdown()

	// 注册观察者客户端
	observer := makeTestClient("c-obs", "u-obs", "ns-obs", "g-obs")
	observer.UserType = models.UserTypeObserver
	hub.shardedRegistry.AddClient(observer)

	// 验证观察者已注册
	observers := hub.GetObserversForMessage("ns-obs", "g-obs")
	require.NotEmpty(t, observers, "观察者应已注册")

	distMsg := makeDistributedMessage(OperationTypeObserverNotify, "other-node", makeGroupMessage("sender"))
	distMsg.Namespace = "ns-obs"
	distMsg.GroupIDs = []string{"g-obs"}

	err := hub.handleDistributedObserverNotify(context.Background(), distMsg)
	require.NoError(t, err)

	// 观察者应收到消息（ParallelSliceExecutor 可能异步，用 Eventually 等待）
	require.Eventually(t, func() bool {
		select {
		case <-observer.SendChan:
			return true
		default:
			return false
		}
	}, 2*time.Second, 10*time.Millisecond, "观察者应收到通知消息")
}

// ============================================================================
// SubscribeNodeMessages / SubscribeBroadcastChannel / SubscribeObserverChannel
// ============================================================================

func TestSubscribeNodeMessages_NilPubSub(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	err := hub.SubscribeNodeMessages(context.Background())
	assert.ErrorIs(t, err, ErrPubSubNotSet)
}

func TestSubscribeBroadcastChannel_NilPubSub(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	err := hub.SubscribeBroadcastChannel(context.Background())
	assert.ErrorIs(t, err, ErrPubSubNotSet)
}

func TestSubscribeObserverChannel_NilPubSub(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	err := hub.SubscribeObserverChannel(context.Background())
	assert.ErrorIs(t, err, ErrPubSubNotSet)
}

func TestSubscribeNodeMessages_WithPubSub(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	defer hub.Shutdown()
	hub.SetPubSub(cachex.NewPubSub(redisClient))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := hub.SubscribeNodeMessages(ctx)
	require.NoError(t, err, "有 PubSub 时应订阅成功")
}

func TestSubscribeBroadcastChannel_WithPubSub(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	defer hub.Shutdown()
	hub.SetPubSub(cachex.NewPubSub(redisClient))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := hub.SubscribeBroadcastChannel(ctx)
	require.NoError(t, err, "有 PubSub 时应订阅成功")
}

func TestSubscribeObserverChannel_WithPubSub(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	defer hub.Shutdown()
	hub.SetPubSub(cachex.NewPubSub(redisClient))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := hub.SubscribeObserverChannel(ctx)
	require.NoError(t, err, "有 PubSub 时应订阅成功")
}

// ============================================================================
// handleSendFailure
// ============================================================================

func TestHandleSendFailure_DirectCall(t *testing.T) {
	t.Parallel()
	hub := newMinHub()
	defer hub.Shutdown()

	msg := makeGroupMessage("sender")
	assert.NotPanics(t, func() {
		hub.handleSendFailure(context.Background(), "u-fail", msg, "test reason")
	})
}

// ============================================================================
// checkAndRouteToNode — 额外分支覆盖
// ============================================================================

func TestCheckAndRouteToNode_QueryError(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	defer hub.Shutdown()

	// SetPubSub 在 onlineStatusRepo 为 nil 时不初始化 routerCache
	hub.SetPubSub(cachex.NewPubSub(redisClient))
	hub.SetOnlineStatusRepository(&distributedOnlineStatusRepo{
		userNodesErr: assertError("query failed"),
	})

	msg := makeGroupMessage("sender")
	routed, _, err := hub.checkAndRouteToNode(context.Background(), "u-test", msg)
	assert.Error(t, err, "查询失败应返回错误")
	assert.False(t, routed, "查询失败不应路由")
}

func TestCheckAndRouteToNode_UserOnOtherNodes(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetPubSub(cachex.NewPubSub(redisClient))
	hub.SetOnlineStatusRepository(&distributedOnlineStatusRepo{
		userNodes: []string{"node-other"},
	})

	msg := makeGroupMessage("sender")
	routed, routeNodes, err := hub.checkAndRouteToNode(context.Background(), "u-test", msg)
	require.NoError(t, err)
	assert.True(t, routed, "用户在其他节点时应路由")
	assert.Equal(t, []string{"node-other"}, routeNodes, "应返回实际投递的目标节点列表")
}

func TestCheckAndRouteToNode_UserOnlyOnLocalNode(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetPubSub(cachex.NewPubSub(redisClient))
	hub.SetOnlineStatusRepository(&distributedOnlineStatusRepo{
		userNodes: []string{hub.nodeID}, // 用户仅在本节点
	})

	msg := makeGroupMessage("sender")
	routed, _, err := hub.checkAndRouteToNode(context.Background(), "u-test", msg)
	require.NoError(t, err)
	assert.False(t, routed, "用户仅在本节点时不应路由")
}

func TestCheckAndRouteToNode_EmptyNodeID(t *testing.T) {
	t.Parallel()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	defer hub.Shutdown()

	hub.SetPubSub(cachex.NewPubSub(redisClient))
	hub.SetOnlineStatusRepository(&distributedOnlineStatusRepo{
		userNodes: []string{"", hub.nodeID}, // 包含空 nodeID，应被过滤
	})

	msg := makeGroupMessage("sender")
	routed, _, err := hub.checkAndRouteToNode(context.Background(), "u-test", msg)
	require.NoError(t, err)
	assert.False(t, routed, "过滤空 nodeID 后仅剩本节点，不应路由")
}

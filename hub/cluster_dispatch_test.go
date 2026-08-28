/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-09 00:16:29
 * @FilePath: \go-wsc\hub\cluster_dispatch_test.go
 * @Description: 统一跨节点路由测试 - 真实双节点 gRPC 直连 + PubSub 兜底
 *
 * 覆盖 cluster_dispatch.go 的 routeToCluster 决策链：
 *   - gRPC 直连（SendMessage/KickUser/GroupBroadcast/GroupsBroadcast/ObserverNotify/Broadcast）
 *   - PubSub 兜底（gRPC 未启用 / 目标地址未知 / 序列化失败）
 *   - 辅助方法（resolveDispatchTargetID/resolveGRPCTargetNodes/getAllClusterNodeIDs 等）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
)

// clusterHubPortSeq 为每个 newClusterHub 分配唯一的 WS 端口，确保 generateNodeID 产出不同 nodeID
var clusterHubPortSeq int32 = 18080

// ============================================================================
// 双节点拓扑辅助
// ============================================================================

// newClusterHub 创建启用 gRPC 的节点 Hub（InitNodeGRPC + startNodeGRPC）
// 共享 redisClient 以支持节点发现；enableObserver 控制观察者模块
// 每次调用分配唯一 WS 端口 → generateNodeID 产出唯一 nodeID，避免双节点 ID 冲突
func newClusterHub(t *testing.T, redisClient *redis.Client, groupPrefix string, enableObserver bool) *Hub {
	t.Helper()
	wsPort := int(atomic.AddInt32(&clusterHubPortSeq, 1))
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", wsPort).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	config.NodeGRPC.Enabled = true
	config.NodeGRPC.Host = "127.0.0.1"
	config.NodeGRPC.Port = 0 // 随机端口
	config.EnableObserver = enableObserver

	hub := NewHub(config)
	hub.SetPubSub(cachex.NewPubSub(redisClient))
	hub.SetGroupRepository(repository.NewRedisGroupRepository(redisClient, groupPrefix))
	hub.InitNodeGRPC()
	require.True(t, hub.IsGRPCEnabled())
	hub.startNodeGRPC()
	require.NotNil(t, hub.grpcServer.listener)
	return hub
}

// clusterHubAddr 返回节点 gRPC 实际监听地址
func clusterHubAddr(h *Hub) string {
	return h.grpcServer.listener.Addr().String()
}

// linkClusterNodes 互相注册对方 gRPC 地址到本地节点注册表（绕过 Redis 发现刷新延迟）
func linkClusterNodes(a, b *Hub) {
	a.nodeRegistry.nodes.Store(b.GetNodeID(), clusterHubAddr(b))
	b.nodeRegistry.nodes.Store(a.GetNodeID(), clusterHubAddr(a))
}

// linkClusterNodesAndWait 等待两节点异步 Register 完成后再互相注册地址
// Register 内部 refreshNodes 会删除未在 Redis 中的节点，若 linkClusterNodes 在 Register 之前执行，
// refreshNodes 会清除手动注册的地址导致间歇性 gRPC 拨号失败
func linkClusterNodesAndWait(t *testing.T, redisClient *redis.Client, a, b *Hub) {
	t.Helper()
	require.Eventually(t, func() bool {
		ctx := context.Background()
		_, errA := redisClient.HGet(ctx, a.nodeRegistry.grpcKey, a.GetNodeID()).Result()
		_, errB := redisClient.HGet(ctx, b.nodeRegistry.grpcKey, b.GetNodeID()).Result()
		return errA == nil && errB == nil
	}, 3*time.Second, 20*time.Millisecond)
	linkClusterNodes(a, b)
}

// newClusterPair 创建共享 miniredis 的双节点拓扑，互相发现，返回 (hubA, hubB, cleanup)
func newClusterPair(t *testing.T) (*Hub, *Hub, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	hubA := newClusterHub(t, redisClient, "wsc:test:cluster:group:", false)
	hubB := newClusterHub(t, redisClient, "wsc:test:cluster:group:", false)
	linkClusterNodesAndWait(t, redisClient, hubA, hubB)
	cleanup := func() {
		_ = hubA.SafeShutdown()
		_ = hubB.SafeShutdown()
		_ = redisClient.Close()
	}
	return hubA, hubB, cleanup
}

// ============================================================================
// routeToCluster 单机模式
// ============================================================================

// TestRouteToCluster_StandaloneMode 验证无 PubSub 且未启用 gRPC 时不跨节点
func TestRouteToCluster_StandaloneMode(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// setupGroupTestHub 未设置 pubsub 也未启用 gRPC
	require.False(t, hub.IsGRPCEnabled())
	assert.Nil(t, hub.routeToCluster(context.Background(), makeGroupMessage("s"), ClusterDispatchOptions{
		Operation: models.OperationTypeSendMessage,
	}))
}

// ============================================================================
// gRPC 直连 - 各操作类型
// ============================================================================

// TestRouteToCluster_GRPCDirect_SendToUser 验证 gRPC 直连点对点投递：hubA 发消息经 gRPC 到 hubB 的在线客户端
func TestRouteToCluster_GRPCDirect_SendToUser(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	// hubB 上注册目标用户客户端
	recv := makeTestClient("c-recv", "u-recv")
	hubB.shardedRegistry.AddClient(recv)

	msg := makeGroupMessage("sender-a")
	msg.Receiver = "u-recv"

	err := hubA.routeToCluster(context.Background(), msg, ClusterDispatchOptions{
		Operation:    models.OperationTypeSendMessage,
		TargetNodeID: hubB.GetNodeID(),
		TargetUserID: "u-recv",
	})
	require.NoError(t, err)

	got := recvFromSendChan(t, recv, time.Second)
	assert.Equal(t, "sender-a", got.Sender)
}

// TestRouteToCluster_GRPCDirect_KickUser 验证 KickUser 操作经 gRPC SendToUser 投递
// 注：executeGRPCDispatch 中 KickUser 与 SendMessage 共用 SendToUser RPC
func TestRouteToCluster_GRPCDirect_KickUser(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	recv := makeTestClient("c-kick-recv", "u-kick")
	hubB.shardedRegistry.AddClient(recv)

	msg := makeGroupMessage("sender-kick")
	require.NoError(t, hubA.routeToCluster(context.Background(), msg, ClusterDispatchOptions{
		Operation:    models.OperationTypeKickUser,
		TargetNodeID: hubB.GetNodeID(),
		TargetUserID: "u-kick",
		Reason:       "cross-node-kick",
	}))

	assert.NotNil(t, recvFromSendChan(t, recv, time.Second))
}

// TestRouteToCluster_GRPCDirect_GroupBroadcast 验证单群组广播 gRPC 直连
func TestRouteToCluster_GRPCDirect_GroupBroadcast(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, hubB.groupRepo.CreateGroup(ctx, &Group{GroupID: "g-bc", Namespace: "ns-bc", OwnerID: "owner"}))
	require.NoError(t, hubB.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns-bc").WithGroupIDs([]string{"g-bc"}).Inject(ctx), []string{"u-m1", "u-m2"}))

	m1 := makeTestClient("c-m1", "u-m1", "ns-bc")
	m2 := makeTestClient("c-m2", "u-m2", "ns-bc")
	hubB.shardedRegistry.AddClient(m1)
	hubB.shardedRegistry.AddClient(m2)

	require.NoError(t, hubA.routeToCluster(ctx, makeGroupMessage("sender-g"), ClusterDispatchOptions{
		Operation:    models.OperationTypeGroupBroadcast,
		Namespace:    "ns-bc",
		GroupIDs:     []string{"g-bc"},
		TargetNodeID: hubB.GetNodeID(),
	}))

	assert.NotNil(t, recvFromSendChan(t, m1, time.Second))
	assert.NotNil(t, recvFromSendChan(t, m2, time.Second))
}

// TestRouteToCluster_GRPCDirect_GroupsBroadcast 验证批量群组广播 gRPC 直连（并行复用 BroadcastGroup RPC）
func TestRouteToCluster_GRPCDirect_GroupsBroadcast(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	ctx := context.Background()
	require.NoError(t, hubB.groupRepo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: "ns-gb", OwnerID: "owner"}))
	require.NoError(t, hubB.groupRepo.CreateGroup(ctx, &Group{GroupID: "g2", Namespace: "ns-gb", OwnerID: "owner"}))
	require.NoError(t, hubB.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns-gb").WithGroupIDs([]string{"g1"}).Inject(ctx), []string{"u-a"}))
	require.NoError(t, hubB.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns-gb").WithGroupIDs([]string{"g2"}).Inject(ctx), []string{"u-b"}))

	ca := makeTestClient("c-a", "u-a", "ns-gb")
	cb := makeTestClient("c-b", "u-b", "ns-gb")
	hubB.shardedRegistry.AddClient(ca)
	hubB.shardedRegistry.AddClient(cb)

	require.NoError(t, hubA.routeToCluster(ctx, makeGroupMessage("sender-gb"), ClusterDispatchOptions{
		Operation:    models.OperationTypeGroupsBroadcast,
		Namespace:    "ns-gb",
		GroupIDs:     []string{"g1", "g2"},
		TargetNodeID: hubB.GetNodeID(),
	}))

	assert.NotNil(t, recvFromSendChan(t, ca, time.Second))
	assert.NotNil(t, recvFromSendChan(t, cb, time.Second))
}

// TestRouteToCluster_GRPCDirect_ObserverNotify 验证观察者通知 gRPC 直连
func TestRouteToCluster_GRPCDirect_ObserverNotify(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	hubA := newClusterHub(t, redisClient, "wsc:test:obs:group:", false)
	hubB := newClusterHub(t, redisClient, "wsc:test:obs:group:", true) // hubB 启用观察者
	linkClusterNodesAndWait(t, redisClient, hubA, hubB)
	defer func() {
		_ = hubA.SafeShutdown()
		_ = hubB.SafeShutdown()
		_ = redisClient.Close()
	}()

	observer := makeObserverClient("c-obs", "u-obs")
	hubB.shardedRegistry.AddClient(observer)

	require.NoError(t, hubA.routeToCluster(context.Background(), makeGroupMessage("sender-obs"), ClusterDispatchOptions{
		Operation:    models.OperationTypeObserverNotify,
		Namespace:    "ns-obs",
		GroupIDs:     []string{"g-obs"},
		TargetNodeID: hubB.GetNodeID(),
	}))

	assert.NotNil(t, recvFromSendChan(t, observer, time.Second))
}

// TestRouteToCluster_GRPCDirect_Broadcast 验证全局广播 gRPC 直连（复用 BroadcastGroup RPC，groupID 留空）
func TestRouteToCluster_GRPCDirect_Broadcast(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	// Broadcast 操作 groupID 留空，服务端 GetMembers(namespace,"") 返回空，RPC 无 error 即覆盖分支
	require.NoError(t, hubA.routeToCluster(context.Background(), makeGroupMessage("sender-bc"), ClusterDispatchOptions{
		Operation:    models.OperationTypeBroadcast,
		Namespace:    "", // 全命名空间
		TargetNodeID: hubB.GetNodeID(),
	}))
}

// TestRouteToCluster_GRPCDirect_AllNodesBroadcast 验证 TargetNodeID 为空时广播到所有已知节点
func TestRouteToCluster_GRPCDirect_AllNodesBroadcast(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	recv := makeTestClient("c-all", "u-all")
	hubB.shardedRegistry.AddClient(recv)

	// TargetNodeID 为空 → resolveGRPCTargetNodes 返回所有其他节点
	require.NoError(t, hubA.routeToCluster(context.Background(), makeGroupMessage("sender-all"), ClusterDispatchOptions{
		Operation:    models.OperationTypeSendMessage,
		TargetUserID: "u-all",
		// TargetNodeID 留空
	}))

	assert.NotNil(t, recvFromSendChan(t, recv, time.Second))
}

// ============================================================================
// PubSub 兜底
// ============================================================================

// TestRouteToCluster_PubSubFallback_NoGRPC 验证 gRPC 未启用但有 PubSub 时走 PubSub 兜底
func TestRouteToCluster_PubSubFallback_NoGRPC(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	config := wscconfig.Default().WithNodeInfo("127.0.0.1", 18080).WithMessageBufferSize(256)
	hub := NewHub(config)
	hub.SetPubSub(cachex.NewPubSub(redisClient))
	defer hub.Shutdown()
	require.False(t, hub.IsGRPCEnabled())

	// gRPC 未启用 → dispatchViaGRPC 返回 pubsubFallback=allNodes → publishToCluster 发布无 error
	assert.NoError(t, hub.routeToCluster(context.Background(), makeGroupMessage("s"), ClusterDispatchOptions{
		Operation: models.OperationTypeSendMessage,
	}))
}

// TestRouteToCluster_PubSubFallback_OnlyPubSubNoNodes 验证无 gRPC 且无其他节点时仍正常返回
func TestRouteToCluster_PubSubFallback_OnlyPubSubNoNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	config := wscconfig.Default().WithNodeInfo("127.0.0.1", 18080).WithMessageBufferSize(256)
	hub := NewHub(config)
	hub.SetPubSub(cachex.NewPubSub(redisClient))
	defer hub.Shutdown()

	// 无 gRPC、无其他节点：dispatchViaGRPC 返回空 fallback，pubsubFallback 为空但 grpcDelivered=0
	// 走 publishToCluster（pubsub 非 nil）发布，返回 nil
	assert.NoError(t, hub.routeToCluster(context.Background(), makeGroupMessage("s"), ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
	}))
}

// TestRouteToCluster_GRPCAddrUnknown_PubSubFallback 验证 gRPC 启用但目标节点地址未知时降级 PubSub
func TestRouteToCluster_GRPCAddrUnknown_PubSubFallback(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	hubA := newClusterHub(t, redisClient, "wsc:test:unk:group:", false)
	defer func() {
		_ = hubA.SafeShutdown()
		_ = redisClient.Close()
	}()

	// 不 link 任何节点，TargetNodeID 指向不存在的节点
	// GetNodeAddr 失败 → pubsubFallback 追加 → publishToCluster 兜底
	assert.NoError(t, hubA.routeToCluster(context.Background(), makeGroupMessage("s"), ClusterDispatchOptions{
		Operation:    models.OperationTypeSendMessage,
		TargetNodeID: "ghost-node",
		TargetUserID: "u-ghost",
	}))
}

// TestPublishToCluster_NoPubSub 验证 pubsub 为 nil 时 publishToCluster 返回 nil
func TestPublishToCluster_NoPubSub(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// setupGroupTestHub 未设置 pubsub
	assert.NoError(t, hub.publishToCluster(context.Background(), &DistributedMessage{
		Type:    models.OperationTypeBroadcast,
		NodeID:  hub.GetNodeID(),
		Message: makeGroupMessage("s"),
	}))
}

// ============================================================================
// 辅助方法单元测试
// ============================================================================

// TestResolveDispatchTargetID 验证按操作类型解析 TargetID
func TestResolveDispatchTargetID(t *testing.T) {
	assert.Equal(t, "u1", resolveDispatchTargetID(ClusterDispatchOptions{
		Operation: models.OperationTypeSendMessage, TargetUserID: "u1",
	}))
	assert.Equal(t, "u2", resolveDispatchTargetID(ClusterDispatchOptions{
		Operation: models.OperationTypeKickUser, TargetUserID: "u2",
	}))
	// 广播类操作无特定目标
	assert.Empty(t, resolveDispatchTargetID(ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
	}))
	assert.Empty(t, resolveDispatchTargetID(ClusterDispatchOptions{
		Operation: models.OperationTypeGroupBroadcast,
	}))
}

// TestGetAllClusterNodeIDs 验证获取其他节点列表
func TestGetAllClusterNodeIDs(t *testing.T) {
	// nodeRegistry 为 nil
	hubNoReg := NewHub(wscconfig.Default().WithNodeInfo("127.0.0.1", 18080))
	assert.Empty(t, hubNoReg.getAllClusterNodeIDs())

	// 有节点
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()
	ids := hubA.getAllClusterNodeIDs()
	assert.ElementsMatch(t, []string{hubB.GetNodeID()}, ids)
}

// TestResolveGRPCTargetNodes 验证目标节点解析
func TestResolveGRPCTargetNodes(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	// TargetNodeID 非空 → 精确单节点
	assert.Equal(t, []string{hubB.GetNodeID()}, hubA.resolveGRPCTargetNodes(ClusterDispatchOptions{
		TargetNodeID: hubB.GetNodeID(),
	}))

	// TargetNodeID 空 → 所有其他节点
	assert.ElementsMatch(t, []string{hubB.GetNodeID()}, hubA.resolveGRPCTargetNodes(ClusterDispatchOptions{}))
}

// TestExecuteGRPCDispatch_UnknownOperation 验证未知操作类型返回 fallback
func TestExecuteGRPCDispatch_UnknownOperation(t *testing.T) {
	hubA, hubB, cleanup := newClusterPair(t)
	defer cleanup()

	msgData := mustMarshalHubMessagePB(t, makeGroupMessage("s"))
	// 使用未定义的操作类型
	assert.Equal(t, grpcOutcomeFallback, hubA.executeGRPCDispatch(context.Background(), clusterHubAddr(hubB), msgData, ClusterDispatchOptions{
		Operation:    models.OperationType("unknown_op_999"),
		TargetNodeID: hubB.GetNodeID(),
	}))
}

// TestExecuteGRPCDispatch_NilPool 验证 grpcClientPool 为 nil 时返回 fallback
func TestExecuteGRPCDispatch_NilPool(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// setupGroupTestHub 的 grpcClientPool 为 nil
	msgData := mustMarshalHubMessagePB(t, makeGroupMessage("s"))
	assert.Equal(t, grpcOutcomeFallback, hub.executeGRPCDispatch(context.Background(), "127.0.0.1:1", msgData, ClusterDispatchOptions{
		Operation: models.OperationTypeSendMessage,
	}))
}

// TestGrpcBroadcastGroup_EmptyGroupIDs 验证 GroupIDs 为空或 pool 为 nil 时返回 false
func TestGrpcBroadcastGroup_EmptyGroupIDs(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	// GroupIDs 空
	assert.False(t, hub.grpcBroadcastGroup(context.Background(), "127.0.0.1:1", ClusterDispatchOptions{}, nil))
}

// TestGrpcBroadcastGroups_EmptyAndFail 验证批量群组广播边界：GroupIDs 空 / pool nil / 全失败
func TestGrpcBroadcastGroups_EmptyAndFail(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// GroupIDs 空
	assert.False(t, hub.grpcBroadcastGroups(context.Background(), "127.0.0.1:1", ClusterDispatchOptions{}, nil))

	// pool 为 nil 但 GroupIDs 非空（setupGroupTestHub 的 grpcClientPool 为 nil）
	assert.False(t, hub.grpcBroadcastGroups(context.Background(), "127.0.0.1:1", ClusterDispatchOptions{
		GroupIDs: []string{"g1", "g2"},
	}, nil))

	// 不可达地址 → 全失败返回 false
	hubA, _, cleanupPair := newClusterPair(t)
	defer cleanupPair()
	msgData := mustMarshalHubMessagePB(t, makeGroupMessage("s"))
	assert.False(t, hubA.grpcBroadcastGroups(context.Background(), "127.0.0.1:9", ClusterDispatchOptions{
		GroupIDs: []string{"g1"},
	}, msgData))
}

// TestGrpcBroadcastGroup_UnreachableAddr 验证单群组广播地址不可达返回 false
func TestGrpcBroadcastGroup_UnreachableAddr(t *testing.T) {
	hubA, _, cleanup := newClusterPair(t)
	defer cleanup()
	msgData := mustMarshalHubMessagePB(t, makeGroupMessage("s"))
	// 不可达地址 → BroadcastGroup RPC 失败
	assert.False(t, hubA.grpcBroadcastGroup(context.Background(), "127.0.0.1:9", ClusterDispatchOptions{
		GroupIDs: []string{"g1"},
	}, msgData))
}

// TestDispatchViaGRPC_NoTargetNodes 验证无目标节点时返回空 result
func TestDispatchViaGRPC_NoTargetNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	hub := newClusterHub(t, redisClient, "wsc:test:ntn:group:", false)
	defer func() {
		_ = hub.SafeShutdown()
		_ = redisClient.Close()
	}()

	// 不 link 节点，TargetNodeID 也为空 → getAllClusterNodeIDs 返回空 → 无目标节点
	result := hub.dispatchViaGRPC(context.Background(), makeGroupMessage("s"), ClusterDispatchOptions{
		Operation: models.OperationTypeSendMessage,
	})
	assert.Equal(t, 0, result.grpcDelivered)
	assert.Empty(t, result.pubsubFallback)
}

// TestDispatchViaGRPC_GRPCDisabled 验证 gRPC 未启用时所有节点进 PubSub 兜底
func TestDispatchViaGRPC_GRPCDisabled(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	config := wscconfig.Default().WithNodeInfo("127.0.0.1", 18080).WithMessageBufferSize(256)
	hub := NewHub(config)
	hub.SetPubSub(cachex.NewPubSub(redisClient))
	defer hub.Shutdown()

	// 手动注册一个其他节点到 registry（通过构造 nodeRegistry）
	// IsGRPCEnabled 为 false → 所有节点进 fallback
	require.False(t, hub.IsGRPCEnabled())
	result := hub.dispatchViaGRPC(context.Background(), makeGroupMessage("s"), ClusterDispatchOptions{
		Operation: models.OperationTypeSendMessage,
	})
	assert.Equal(t, 0, result.grpcDelivered)
	// nodeRegistry 为 nil → getAllClusterNodeIDs 返回空
	assert.Empty(t, result.pubsubFallback)
}

// ============================================================================
// 辅助
// ============================================================================

// mustMarshalHubMessagePB 序列化消息，失败终止测试（protobuf 序列化）
func mustMarshalHubMessagePB(t *testing.T, msg *HubMessage) []byte {
	t.Helper()
	data, err := marshalHubMessagePBForTest(msg)
	require.NoError(t, err)
	return data
}

// marshalHubMessagePBForTest 包装 protobuf 序列化供测试使用
func marshalHubMessagePBForTest(msg *HubMessage) ([]byte, error) {
	return wscpb.MarshalHubMessage(msg)
}

// ============================================================================
// 死节点秒级兜底（publishToTargetedNodes PUBLISH 返回值检测 + handleDeadNodesForP2P）
// ============================================================================

// deadNodeTestHub 构造 PubSub-only（gRPC 未启用）节点 + 可配置在线索引 + 离线捕获
func deadNodeTestHub(t *testing.T, userNodes []string, userNodesErr error) (*Hub, *redis.Client, *fakeOfflineHandler) {
	t.Helper()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	hub := newMinHub()
	hub.SetPubSub(cachex.NewPubSub(redisClient))
	hub.SetOnlineStatusRepository(&distributedOnlineStatusRepo{
		userNodes:    userNodes,
		userNodesErr: userNodesErr,
	})
	offline := &fakeOfflineHandler{}
	hub.SetOfflineMessageHandler(offline)
	return hub, redisClient, offline
}

// offlineStoredCount 读取 fakeOfflineHandler 已转存数量（并发安全）
func offlineStoredCount(f *fakeOfflineHandler) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.stored)
}

// makeP2PMessage 构造 P2P 投递消息（Receiver 非空才会触发转离线）
func makeP2PMessage(receiver string) *HubMessage {
	msg := makeGroupMessage("sender")
	msg.Receiver = receiver
	return msg
}

// TestDeadNodesAllDead_StoresOfflineImmediately
// 用户所有连接所在节点频道均无人订阅（Pod 全挂）→ 消息秒级转离线，不等 30s ACK 超时
func TestDeadNodesAllDead_StoresOfflineImmediately(t *testing.T) {
	hub, redisClient, offline := deadNodeTestHub(t, []string{"node-dead"}, nil)
	defer func() {
		hub.Shutdown()
		_ = redisClient.Close()
	}()

	// 无任何节点订阅 node-dead 频道 → PUBLISH 返回 0 → deadNodes 触发秒级转离线
	err := hub.routeToCluster(context.Background(), makeP2PMessage("u-dead"), ClusterDispatchOptions{
		Operation:     models.OperationTypeSendMessage,
		TargetUserID:  "u-dead",
		TargetNodeIDs: []string{"node-dead"},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return offlineStoredCount(offline) == 1
	}, 2*time.Second, 10*time.Millisecond, "所有目标节点失活时应立即转存离线")
}

// TestDeadNodesPartialAlive_DoesNotStoreOffline
// 用户多端分布：部分节点失活、部分健康（频道有订阅者）→ 已送达健康节点，不转离线
func TestDeadNodesPartialAlive_DoesNotStoreOffline(t *testing.T) {
	hub, redisClient, offline := deadNodeTestHub(t, []string{"node-dead", "node-alive"}, nil)
	defer func() {
		hub.Shutdown()
		_ = redisClient.Close()
	}()

	// 为 node-alive 频道挂一个真实订阅者（模拟健康节点），node-dead 无订阅者
	ctx := context.Background()
	prefix := hub.config.RedisRepository.PubSub.GetNodeChannelPrefix()
	aliveChannel := prefix + "node-alive"
	sub := redisClient.Subscribe(ctx, aliveChannel)
	defer func() { _ = sub.Close() }()
	require.Eventually(t, func() bool {
		subs, _ := redisClient.PubSubNumSub(ctx, aliveChannel).Result()
		return subs[aliveChannel] == 1
	}, 2*time.Second, 20*time.Millisecond, "node-alive 频道应完成订阅")

	err := hub.routeToCluster(ctx, makeP2PMessage("u-multi"), ClusterDispatchOptions{
		Operation:     models.OperationTypeSendMessage,
		TargetUserID:  "u-multi",
		TargetNodeIDs: []string{"node-dead", "node-alive"},
	})
	require.NoError(t, err)

	// 健康节点已收到消息（PUBLISH=1），不应触发转离线；短暂等待确认无异步转存
	time.Sleep(200 * time.Millisecond)
	assert.Zero(t, offlineStoredCount(offline), "存在健康节点时不应转存离线")
}

// TestDeadNodesIndexQueryError_KeepsAckTimeoutFallback
// 死节点检测后重查在线索引失败 → 未决场景不转离线，保留 30s ACK 超时兜底
func TestDeadNodesIndexQueryError_KeepsAckTimeoutFallback(t *testing.T) {
	hub, redisClient, offline := deadNodeTestHub(t, nil, assert.AnError)
	defer func() {
		hub.Shutdown()
		_ = redisClient.Close()
	}()

	err := hub.routeToCluster(context.Background(), makeP2PMessage("u-err"), ClusterDispatchOptions{
		Operation:     models.OperationTypeSendMessage,
		TargetUserID:  "u-err",
		TargetNodeIDs: []string{"node-dead"},
	})
	require.NoError(t, err)

	time.Sleep(200 * time.Millisecond)
	assert.Zero(t, offlineStoredCount(offline), "索引查询失败时应保留 ACK 超时兜底，不立即转离线")
}

// TestDeadNodesLocalNodeInIndex_SkipsLocal
// 索引含本节点 + 死节点：本节点由本地投递路径负责（跳过），其余远端全死 → 仍应转离线
func TestDeadNodesLocalNodeInIndex_SkipsLocal(t *testing.T) {
	hub, redisClient, offline := deadNodeTestHub(t, nil, nil)
	defer func() {
		hub.Shutdown()
		_ = redisClient.Close()
	}()

	// 索引 = 真实本节点（跳过，由本地 sendToUser 投递路径负责）+ 死节点
	repo := hub.onlineStatusRepo.(*distributedOnlineStatusRepo)
	repo.userNodes = []string{hub.GetNodeID(), "node-dead"}

	err := hub.routeToCluster(context.Background(), makeP2PMessage("u-local"), ClusterDispatchOptions{
		Operation:     models.OperationTypeSendMessage,
		TargetUserID:  "u-local",
		TargetNodeIDs: []string{"node-dead"},
	})
	require.NoError(t, err)

	// 本节点跳过后剩余远端节点全死（本节点不在 TargetNodeIDs 中，本地路径未投递此消息）→ 转离线
	require.Eventually(t, func() bool {
		return offlineStoredCount(offline) == 1
	}, 2*time.Second, 10*time.Millisecond, "索引中无健康远端节点时应转存离线")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-25 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-25 00:00:00
 * @FilePath: \go-wsc\hub\cluster_dispatch_test.go
 * @Description: 跨节点 gRPC 直连路由修复回归测试
 *
 * 覆盖两条修复主线：
 *   1. 全局/命名空间广播（Broadcast）短路：无定向目标节点时不做 gRPC 定向直连，
 *      统一走 Redis PubSub 广播频道，避免复用空 groupID 的 BroadcastGroup RPC 导致
 *      服务端返回 Delivered=0 被误判投递成功、跳过 PubSub 兜底而静默丢消息
 *   2. 路由信封归一化：调用方（messaging 域）常遗漏 opts.AppID，仅 msg 信封携带，
 *      进入 gRPC 前必须用 msg 优先回填 opts，保证 gRPC metadata 携带正确 appID，
 *      否则广播类 RPC 在服务端反序列化 msg 前用 AppIDFromContext 查成员会退
 *      DefaultAppID → 跨租户隔离失效
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// dispatchMockServer 记录 gRPC 调用并捕获 incoming metadata 中的路由信封（appID）
type dispatchMockServer struct {
	wscpb.UnimplementedNodeServiceServer
	mu       sync.Mutex
	captured []string // 收到的 appID（来自 incoming metadata）
}

// BroadcastGroup 记录本次调用携带的 appID 并回写成功结果
func (m *dispatchMockServer) BroadcastGroup(ctx context.Context, _ *wscpb.BroadcastGroupRequest) (*wscpb.BroadcastGroupResponse, error) {
	m.mu.Lock()
	m.captured = append(m.captured, appIDFromIncoming(ctx))
	m.mu.Unlock()
	return &wscpb.BroadcastGroupResponse{Delivered: 1}, nil
}

// appIDFromIncoming 从 gRPC incoming metadata 提取 appID（未注入则空串）
func appIDFromIncoming(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	if vals := md.Get(constants.MetadataKeyAppID); len(vals) > 0 {
		return vals[0]
	}
	return ""
}

// startDispatchMockServer 在随机回环端口启动 mock gRPC server，返回 addr 与清理函数
func startDispatchMockServer(t *testing.T, mock *dispatchMockServer) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	wscpb.RegisterNodeServiceServer(srv, mock)
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() {
		srv.Stop()
		_ = lis.Close()
	}
}

// newDispatchTestHub 构造启用 gRPC 直连的最小 Hub：nodeRegistry 登记一个远端节点
// （node-b → addr），grpcClientPool 直连该 mock server（绕过 Redis 与真实节点发现）
func newDispatchTestHub(addr string) *Hub {
	hub := NewHub(&wscconfig.WSC{
		ClientTimeout: time.Minute,
		NodeGRPC:      &wscconfig.NodeGRPC{Enabled: true},
	})
	hub.nodeID = "node-a"
	hub.grpcClientPool = cluster.NewGRPCClientPool()
	hub.nodeRegistry = cluster.NewNodeRegistry(nil, "node-a", "", "wsc:nodes:grpc", "wsc:nodes:heartbeat", nil)
	hub.nodeRegistry.SetNodeAddr("node-b", addr)
	return hub
}

// TestDispatchViaGRPCBroadcastShortCircuit 全局/命名空间广播不做 gRPC 定向直连，
// 返回空路由结果，交由 routeToCluster 走 PubSub 广播频道兜底
func TestDispatchViaGRPCBroadcastShortCircuit(t *testing.T) {
	mock := &dispatchMockServer{}
	addr, cleanup := startDispatchMockServer(t, mock)
	defer cleanup()

	hub := newDispatchTestHub(addr)
	defer hub.grpcClientPool.Close()

	msg := models.NewHubMessage()
	msg.AppID = "app-broadcast"

	res := hub.dispatchViaGRPC(context.Background(), msg, cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeBroadcast,
		AppID:     "app-broadcast",
	})

	assert.Equal(t, 0, res.grpcDelivered)
	assert.Empty(t, res.pubsubFallback, "Broadcast 不应产生 pubsubFallback 节点列表")
	assert.Empty(t, res.userMissNodes)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Empty(t, mock.captured, "Broadcast 不应复用 BroadcastGroup RPC（否则空 groupID 静默丢消息）")
}

// TestDispatchViaGRPCEnvelopeNormalization 调用方遗漏 opts.AppID 时，msg 信封优先回填，
// gRPC metadata 必须携带归一化后的 appID，避免服务端退 DefaultAppID 导致跨租户隔离失效
func TestDispatchViaGRPCEnvelopeNormalization(t *testing.T) {
	mock := &dispatchMockServer{}
	addr, cleanup := startDispatchMockServer(t, mock)
	defer cleanup()

	hub := newDispatchTestHub(addr)
	defer hub.grpcClientPool.Close()

	// 模拟 messaging.broadcast.go 的 crossNodeGroupBroadcast：仅 msg 信封携带 appID，opts 留空
	msg := models.NewHubMessage()
	msg.AppID = "tenant-a"

	res := hub.dispatchViaGRPC(context.Background(), msg, cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeGroupBroadcast,
		GroupIDs:  []string{"g1"},
		// AppID 留空：历史上的缺陷输入，归一化必须用 msg.AppID 兜底
	})

	assert.Equal(t, 1, res.grpcDelivered, "单节点群组广播应 gRPC 直连投递成功")

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Len(t, mock.captured, 1)
	assert.Equal(t, "tenant-a", mock.captured[0],
		"gRPC metadata 应携带归一化后的 msg.AppID，而非空/DefaultAppID")
}

// TestDispatchViaGRPCEnvelopeAppIDDelegate msg 信封无 appID 时回退 opts.AppID，
// 确保归一化兜底链（msg → opts → DefaultAppID）与 routeToCluster 口径一致
func TestDispatchViaGRPCEnvelopeAppIDDelegate(t *testing.T) {
	mock := &dispatchMockServer{}
	addr, cleanup := startDispatchMockServer(t, mock)
	defer cleanup()

	hub := newDispatchTestHub(addr)
	defer hub.grpcClientPool.Close()

	msg := models.NewHubMessage() // appID 留空

	res := hub.dispatchViaGRPC(context.Background(), msg, cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeGroupBroadcast,
		GroupIDs:  []string{"g1"},
		AppID:     "tenant-b",
	})

	assert.Equal(t, 1, res.grpcDelivered)

	mock.mu.Lock()
	defer mock.mu.Unlock()
	require.Len(t, mock.captured, 1)
	assert.Equal(t, "tenant-b", mock.captured[0], "msg.AppID 为空时 gRPC metadata 应回退 opts.AppID")
}
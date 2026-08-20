/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 10:05:53
 * @FilePath: \go-wsc\hub\grpc_client_test.go
 * @Description: GRPCClientPool 测试 - 覆盖连接缓存复用、Close 清理、
 * 路由元数据（namespace）经 gRPC metadata 正确注入并影响服务端行为
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
)

// countPoolConnections 统计连接池中缓存的连接数
func countPoolConnections(p *GRPCClientPool) int {
	var n int
	p.connections.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}

// TestGRPCClientPool_GetClient_ReusesConnection 验证同一地址复用同一连接
func TestGRPCClientPool_GetClient_ReusesConnection(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	c1, err := pool.GetClient(addr)
	require.NoError(t, err)
	require.NotNil(t, c1)
	assert.Equal(t, 1, countPoolConnections(pool))

	// 同地址再次获取应复用缓存连接，不新增条目
	c2, err := pool.GetClient(addr)
	require.NoError(t, err)
	require.NotNil(t, c2)
	assert.Equal(t, 1, countPoolConnections(pool))

	// 两个客户端都能正常完成一次 RPC（连接有效）
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	resp, err := c1.Ping(ctx, &wscpb.PingRequest{})
	require.NoError(t, err)
	assert.Equal(t, hub.GetNodeID(), resp.GetNodeId())
}

// TestGRPCClientPool_Close_ClearsConnections 验证 Close 清空连接池
func TestGRPCClientPool_Close_ClearsConnections(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	pool := NewGRPCClientPool()
	_, err := pool.GetClient(addr)
	require.NoError(t, err)
	require.Equal(t, 1, countPoolConnections(pool))

	pool.Close()
	assert.Equal(t, 0, countPoolConnections(pool))

	// Close 后仍可重新获取连接（连接池可复用）
	pool2 := NewGRPCClientPool()
	_, err = pool2.GetClient(addr)
	require.NoError(t, err)
	pool2.Close()
}

// TestGRPCClientPool_BroadcastGroup_PropagatesNamespaceMetadata 验证客户端将
// ctx 中的 namespace 经 gRPC metadata 注入，服务端据此过滤群组成员
//
// 场景：群组 g-meta 在 ns-A 下有成员，用 ns-B 路由广播应投递 0（namespace 隔离生效）
func TestGRPCClientPool_BroadcastGroup_PropagatesNamespaceMetadata(t *testing.T) {
	hub, groupRepo, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	ctx := context.Background()
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-meta", Namespace: "ns-A", OwnerID: "owner"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace("ns-A").WithGroupIDs([]string{"g-meta"}).Inject(ctx), []string{"u-meta"}))

	hub.shardedRegistry.AddClient(makeTestClient("c-meta", "u-meta", "ns-A"))

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	msgData, err := wscpb.MarshalHubMessage(makeGroupMessage("sender"))
	require.NoError(t, err)

	// 用错误的 namespace 路由 → 服务端 GetMembers("ns-B", "g-meta") 返回空 → delivered 0
	wrongNsCtx := routing.NewRoute().WithAppID("").WithNamespace("ns-B").WithGroupIDs([]string{"g-meta"}).Inject(ctx)
	delivered, err := pool.BroadcastGroup(wrongNsCtx, addr, msgData, false, "")
	require.NoError(t, err)
	assert.Equal(t, int32(0), delivered)

	// 用正确的 namespace 路由 → 命中成员 → delivered 1
	rightNsCtx := routing.NewRoute().WithAppID("").WithNamespace("ns-A").WithGroupIDs([]string{"g-meta"}).Inject(ctx)
	delivered, err = pool.BroadcastGroup(rightNsCtx, addr, msgData, false, "")
	require.NoError(t, err)
	assert.Equal(t, int32(1), delivered)
}

// TestGRPCClientPool_ConcurrentGetClient 验证并发获取同一地址不产生重复连接
func TestGRPCClientPool_ConcurrentGetClient(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	var done atomic.Int32
	for i := 0; i < 8; i++ {
		go func() {
			defer done.Add(1)
			_, err := pool.GetClient(addr)
			assert.NoError(t, err)
		}()
	}
	require.Eventually(t, func() bool { return done.Load() == 8 }, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, countPoolConnections(pool))
}

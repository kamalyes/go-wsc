/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-08 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 10:08:18
 * @FilePath: \go-wsc\hub\node_registry_test.go
 * @Description: 节点注册与发现白盒单元测试（覆盖 hub/node_registry.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNodeRegistry_NilRedis 验证 redisClient 为 nil 时 Register/Unregister 返回 nil
func TestNodeRegistry_NilRedis(t *testing.T) {
	r := NewNodeRegistry(nil, "node1", "127.0.0.1:50051", "wsc:nodes:grpc", "wsc:nodes:heartbeat", nil)

	require.Nil(t, r.Register(context.Background()))
	require.Nil(t, r.Unregister(context.Background()))
	// Stop 不应 panic
	assert.NotPanics(t, func() { r.Stop() })
}

// TestNodeRegistry_EmptyAddr 验证 grpcAddr 为空时 Register 返回 nil
func TestNodeRegistry_EmptyAddr(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	r := NewNodeRegistry(client, "node1", "", "wsc:nodes:grpc", "wsc:nodes:heartbeat", nil)
	require.Nil(t, r.Register(context.Background()))
	r.Stop()
}

// newTestNodeRegistry 用 miniredis 构造节点注册中心
func newTestNodeRegistry(t *testing.T, nodeID, grpcAddr string) (*NodeRegistry, redis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	r := NewNodeRegistry(client, nodeID, grpcAddr, "wsc:nodes:grpc", "wsc:nodes:heartbeat", nil)
	return r, client
}

// TestNodeRegistry_RegisterAndGetAddr 验证注册后获取本节点地址
func TestNodeRegistry_RegisterAndGetAddr(t *testing.T) {
	r, _ := newTestNodeRegistry(t, "node-local", "127.0.0.1:50051")
	defer r.Stop()

	require.NoError(t, r.Register(context.Background()))

	// 本节点地址
	addr, ok := r.GetNodeAddr("node-local")
	assert.True(t, ok)
	assert.Equal(t, "127.0.0.1:50051", addr)
}

// TestNodeRegistry_GetNodeAddr_NotFound 验证获取不存在的节点返回 false
func TestNodeRegistry_GetNodeAddr_NotFound(t *testing.T) {
	r, _ := newTestNodeRegistry(t, "node-local", "127.0.0.1:50051")
	defer r.Stop()

	addr, ok := r.GetNodeAddr("node-missing")
	assert.False(t, ok)
	assert.Empty(t, addr)
}

// TestNodeRegistry_GetAllNodes 验证获取所有节点（排除本节点）
func TestNodeRegistry_GetAllNodes(t *testing.T) {
	r, _ := newTestNodeRegistry(t, "node-local", "127.0.0.1:50051")
	defer r.Stop()

	// 手动存储其他节点
	r.nodes.Store("nodeA", "10.0.0.1:50051")
	r.nodes.Store("nodeB", "10.0.0.2:50051")

	all := r.GetAllNodes()
	assert.Len(t, all, 2)
	assert.Equal(t, "10.0.0.1:50051", all["nodeA"])
	assert.Equal(t, "10.0.0.2:50051", all["nodeB"])
	// 不含本节点
	_, exists := all["node-local"]
	assert.False(t, exists)
}

// TestNodeRegistry_Unregister 验证注销节点
func TestNodeRegistry_Unregister(t *testing.T) {
	r, client := newTestNodeRegistry(t, "node-unreg", "127.0.0.1:50052")
	defer r.Stop()
	ctx := context.Background()

	require.NoError(t, r.Register(ctx))

	// 确认已注册
	hb, err := client.HGet(ctx, "wsc:nodes:heartbeat", "node-unreg").Result()
	require.NoError(t, err)
	assert.NotEmpty(t, hb)

	require.NoError(t, r.Unregister(ctx))

	// 注销后应不存在
	_, err = client.HGet(ctx, "wsc:nodes:grpc", "node-unreg").Result()
	assert.Error(t, err)
}

// TestNodeRegistry_StopTwice 验证重复 Stop 不 panic
func TestNodeRegistry_StopTwice(t *testing.T) {
	r, _ := newTestNodeRegistry(t, "node-stop", "127.0.0.1:50053")

	assert.NotPanics(t, func() {
		r.Stop()
		r.Stop()
	})
}

// TestNodeRegistry_Register_AfterStop 验证 Stop 后 Register 返回 nil（stopped 分支）
func TestNodeRegistry_Register_AfterStop(t *testing.T) {
	r, _ := newTestNodeRegistry(t, "node-stop2", "127.0.0.1:50054")

	r.Stop()
	// Stop 后 Register 应直接返回 nil（stopped 分支）
	require.Nil(t, r.Register(context.Background()))
}

// TestNodeRegistry_RefreshNodes_LoadAndExpire 验证 refreshNodes 加载节点并清理过期节点
func TestNodeRegistry_RefreshNodes_LoadAndExpire(t *testing.T) {
	r, client := newTestNodeRegistry(t, "node-local", "127.0.0.1:50051")
	defer r.Stop()
	ctx := context.Background()

	// 手动注册一个活跃节点（新鲜心跳）
	now := time.Now().Unix()
	require.NoError(t, client.HSet(ctx, "wsc:nodes:grpc", "nodeA", "10.0.0.1:50051").Err())
	require.NoError(t, client.HSet(ctx, "wsc:nodes:heartbeat", "nodeA", now).Err())

	// 注册一个过期节点（心跳超时）
	expired := now - int64(nodeRegistryTTL/time.Second) - 10
	require.NoError(t, client.HSet(ctx, "wsc:nodes:grpc", "nodeExpired", "10.0.0.9:50051").Err())
	require.NoError(t, client.HSet(ctx, "wsc:nodes:heartbeat", "nodeExpired", expired).Err())

	// refreshNodes 加载活跃节点、清理过期节点
	require.NoError(t, r.refreshNodes(ctx))

	// nodeA 应被加载
	addr, ok := r.GetNodeAddr("nodeA")
	require.True(t, ok)
	assert.Equal(t, "10.0.0.1:50051", addr)

	// nodeExpired 应被清理
	addr, ok = r.GetNodeAddr("nodeExpired")
	assert.False(t, ok)
	assert.Empty(t, addr)

	// 过期节点应从 Redis 删除
	_, err := client.HGet(ctx, "wsc:nodes:grpc", "nodeExpired").Result()
	assert.Error(t, err, "过期节点应从 Redis 删除")
}

// TestNodeRegistry_ReRegisterAfterKeyDeleted 验证 Redis key 被删除后 registerNode 能重新写入注册信息
// 回归场景：refreshLoop 周期调用 registerNode，key 被手动删除或 TTL 过期后节点可自动恢复上报
func TestNodeRegistry_ReRegisterAfterKeyDeleted(t *testing.T) {
	r, client := newTestNodeRegistry(t, "node-re", "127.0.0.1:50055")
	defer r.Stop()
	ctx := context.Background()

	require.NoError(t, r.Register(ctx))

	// 模拟 key 被删除
	require.NoError(t, client.Del(ctx, "wsc:nodes:grpc").Err())
	require.NoError(t, client.Del(ctx, "wsc:nodes:heartbeat").Err())

	// 周期刷新重新注册
	require.NoError(t, r.registerNode(ctx))

	// grpc 地址与心跳均应恢复
	addr, err := client.HGet(ctx, "wsc:nodes:grpc", "node-re").Result()
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:50055", addr)

	hb, err := client.HGet(ctx, "wsc:nodes:heartbeat", "node-re").Result()
	require.NoError(t, err)
	assert.NotEmpty(t, hb)
}

// TestNodeRegistry_RefreshTTL 验证 registerNode 刷新 key 的 TTL（防止 90s 后整体过期）
func TestNodeRegistry_RefreshTTL(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	r := NewNodeRegistry(client, "node-ttl", "127.0.0.1:50056", "wsc:nodes:grpc", "wsc:nodes:heartbeat", nil)
	defer r.Stop()
	ctx := context.Background()

	require.NoError(t, r.Register(ctx))

	// 模拟时间流逝 60s，TTL 剩余约 30s
	mr.FastForward(60 * time.Second)
	ttlBefore, err := client.TTL(ctx, "wsc:nodes:grpc").Result()
	require.NoError(t, err)
	require.True(t, ttlBefore > 0 && ttlBefore <= 30*time.Second)

	// 重新注册应续期 TTL 至 90s
	require.NoError(t, r.registerNode(ctx))
	ttlAfter, err := client.TTL(ctx, "wsc:nodes:grpc").Result()
	require.NoError(t, err)
	assert.True(t, ttlAfter > ttlBefore, "重新注册后续期 TTL")
}

// TestNodeRegistry_RefreshNodes_RemovesStaleLocal 验证 refreshNodes 清理本地缓存中已不存在的节点
func TestNodeRegistry_RefreshNodes_RemovesStaleLocal(t *testing.T) {
	r, _ := newTestNodeRegistry(t, "node-local", "127.0.0.1:50051")
	defer r.Stop()
	ctx := context.Background()

	// 本地缓存一个节点，但 Redis 中不存在
	r.nodes.Store("ghostNode", "10.0.0.99:50051")

	// refreshNodes 应清理本地不存在的节点
	require.NoError(t, r.refreshNodes(ctx))

	_, ok := r.GetNodeAddr("ghostNode")
	assert.False(t, ok, "Redis 中不存在的节点应从本地缓存清理")
}

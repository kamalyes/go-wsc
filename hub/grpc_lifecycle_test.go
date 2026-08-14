/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 10:06:20
 * @FilePath: \go-wsc\hub\grpc_lifecycle_test.go
 * @Description: 节点 gRPC 生命周期测试 - 覆盖 InitNodeGRPC/startNodeGRPC/stopNodeGRPC
 * 的启用/未启用/PubSub 缺失等分支，以及 start→register→stop→unregister 真实流程
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// newGRPCEnabledHub 构造带 miniredis PubSub 的 Hub，按 enableGRPC 开关节点 gRPC 配置
func newGRPCEnabledHub(t *testing.T, enableGRPC bool) (*Hub, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	config.NodeGRPC.Enabled = enableGRPC
	config.NodeGRPC.Host = "127.0.0.1"
	config.NodeGRPC.Port = 0 // 随机端口

	hub := NewHub(config)
	hub.SetPubSub(cachex.NewPubSub(redisClient))

	t.Cleanup(func() {
		_ = hub.SafeShutdown()
		_ = redisClient.Close()
	})
	return hub, mr
}

// TestInitNodeGRPC_Disabled 验证未启用时跳过初始化，组件均为 nil
func TestInitNodeGRPC_Disabled(t *testing.T) {
	hub, _ := newGRPCEnabledHub(t, false)
	hub.InitNodeGRPC()

	assert.Nil(t, hub.nodeRegistry)
	assert.Nil(t, hub.grpcServer)
	assert.Nil(t, hub.grpcClientPool)
	assert.False(t, hub.IsGRPCEnabled())
}

// TestInitNodeGRPC_NoPubSub 验证启用但 PubSub 缺失时跳过（节点发现依赖 Redis）
func TestInitNodeGRPC_NoPubSub(t *testing.T) {
	mr := miniredis.RunT(t)
	defer mr.Close()

	config := wscconfig.Default().WithNodeInfo("127.0.0.1", 18080)
	config.NodeGRPC.Enabled = true
	config.NodeGRPC.Host = "127.0.0.1"
	config.NodeGRPC.Port = 0

	hub := NewHub(config)
	defer hub.Shutdown()
	// 不调用 SetPubSub
	hub.InitNodeGRPC()

	assert.Nil(t, hub.nodeRegistry)
	assert.False(t, hub.IsGRPCEnabled())
}

// TestInitNodeGRPC_EnabledWithPubSub 验证启用 + PubSub 已设置时创建三件套
func TestInitNodeGRPC_EnabledWithPubSub(t *testing.T) {
	hub, _ := newGRPCEnabledHub(t, true)
	hub.InitNodeGRPC()

	require.NotNil(t, hub.nodeRegistry)
	require.NotNil(t, hub.grpcServer)
	require.NotNil(t, hub.grpcClientPool)
	assert.True(t, hub.IsGRPCEnabled())
}

// TestStartAndStopNodeGRPC 验证完整生命周期：start→注册到 Redis→stop→注销
//
// 注：GetNodeAddr 对本节点恒返回 true（localNodeID 特殊分支），故通过直接查 Redis
// 验证注册/注销生效
func TestStartAndStopNodeGRPC(t *testing.T) {
	hub, mr := newGRPCEnabledHub(t, true)
	hub.InitNodeGRPC()
	require.True(t, hub.IsGRPCEnabled())

	// start：启动 gRPC 服务端 + 注册节点到 Redis
	hub.startNodeGRPC()
	require.NotNil(t, hub.grpcServer.listener)

	grpcKey := hub.config.NodeGRPC.GetNodeGRPCKey()
	// 节点注册异步执行，等待 Redis 可见
	require.Eventually(t, func() bool {
		return mr.HGet(grpcKey, hub.GetNodeID()) != ""
	}, 2*time.Second, 20*time.Millisecond, "节点应注册到 Redis 节点发现表")

	// stop：注销节点 + 停止服务端 + 关闭连接池
	hub.stopNodeGRPC()

	// 注销在 stopNodeGRPC 中同步执行（HDel），Redis 应已移除
	assert.Empty(t, mr.HGet(grpcKey, hub.GetNodeID()), "stop 后节点应已从 Redis 注销")
}

// TestStartNodeGRPC_NotEnabled 验证未启用时 startNodeGRPC/stopNodeGRPC 为 no-op
func TestStartNodeGRPC_NotEnabled(t *testing.T) {
	hub, _ := newGRPCEnabledHub(t, false)
	hub.InitNodeGRPC()

	assert.NotPanics(t, func() {
		hub.startNodeGRPC()
		hub.stopNodeGRPC()
	})
	assert.Nil(t, hub.grpcServer)
}

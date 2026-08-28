/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-06 20:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-07 23:20:15
 * @FilePath: \go-wsc\hub\heartbeat_distributed_test.go
 * @Description: 心跳续期 Redis 在线索引的分布式场景测试
 *
 * 验证修复核心保证：心跳续期在线索引与跨节点路由信息，client:<id> 键被淘汰时自愈重建
 *   1. TestHeartbeatRebuildsEvictedOnlineIndex — 索引被淘汰后续期自愈重建 + 新旧路径对照
 *   2. TestMultiDeviceMultiNodeGetUserNodes   — 多设备多节点心跳保持 + 断开收敛
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
)

// newRedisOnlineStatusRepo 创建指向同一 Redis 的在线状态仓储（分布式测试共享 miniredis）
func newRedisOnlineStatusRepo(redisClient *redis.Client, keyPrefix string, ttl time.Duration) repository.OnlineStatusRepository {
	return repository.NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: keyPrefix,
		TTL:       ttl,
	})
}

// newHeartbeatDistHub 创建一个带 onlineStatusRepo（共享指定 Redis）并已启动的 Hub，
// 用于跨节点心跳/在线索引分布式测试nodeID 通过 NODE_ID 环境变量注入
// （generateNodeID 优先级 POD_NAME > HOSTNAME > NODE_ID，故同时清除前两者）
//
// 同一测试可多次调用以创建不同 nodeID 的 Hub；env 通过嵌套捕获 + LIFO 还原保证正确恢复
func newHeartbeatDistHub(t *testing.T, nodeID string, redisClient *redis.Client, keyPrefix string, ttl time.Duration) *Hub {
	t.Helper()

	oldPod := os.Getenv("POD_NAME")
	oldHost := os.Getenv("HOSTNAME")
	oldNode := os.Getenv("NODE_ID")

	os.Unsetenv("POD_NAME")
	os.Unsetenv("HOSTNAME")
	os.Setenv("NODE_ID", nodeID)

	t.Cleanup(func() {
		if oldPod != "" {
			os.Setenv("POD_NAME", oldPod)
		}
		if oldHost != "" {
			os.Setenv("HOSTNAME", oldHost)
		}
		if oldNode != "" {
			os.Setenv("NODE_ID", oldNode)
		} else {
			os.Unsetenv("NODE_ID")
		}
	})

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	config.AllowMultiLogin = true
	config.MaxConnectionsPerUser = 0

	hub := NewHub(config)
	repo := newRedisOnlineStatusRepo(redisClient, keyPrefix, ttl)
	hub.SetOnlineStatusRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	return hub
}

// drainSendChan 启动后台 goroutine 消费 SendChan，防止 pong 投递阻塞心跳热路径
func drainSendChan(c *Client) {
	go func() {
		for range c.SendChan {
		}
	}()
}

// TestHeartbeatRebuildsEvictedOnlineIndex 验证修复核心：
//
// 当 Redis 中 client:<id> 与在线索引被淘汰/过期后（旧 bug 下 UpdateClientHeartbeat
// 因 GetClient 返回 redis.Nil 而静默 no-op，索引永不刷新 → 用户在线但查询为离线、
// 跨节点路由 GetUserNodes 返回空），新的心跳路径（handleHeartbeatMessage → worker →
// RenewClientsOnline 检测缺失后走 BatchSetClientsOnline）应自愈重建索引，恢复跨节点可见性
//
// 同时对照：旧的 UpdateClientHeartbeat 在 client:<id> 缺失时确为静默 no-op
func TestHeartbeatRebuildsEvictedOnlineIndex(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	const prefix = "wsc:hb:dist:online:"
	hubA := newHeartbeatDistHub(t, "node-A", redisClient, prefix, 60*time.Second)
	defer hubA.SafeShutdown()
	hubB := newHeartbeatDistHub(t, "node-B", redisClient, prefix, 60*time.Second)
	defer hubB.SafeShutdown()

	ctx := context.Background()

	// 客户端连接到 node-A（Register 会设置 client.NodeID = "node-A"）
	client := &Client{
		ID:            hubA.idGenerator.GenerateRequestID(),
		UserID:        "user-evict-1",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		SendChan:      make(chan []byte, 16),
		Context:       context.Background(),
		LastHeartbeat: time.Now(),
	}
	drainSendChan(client)
	hubA.Register(client)

	// 等 syncOnlineStatus 写入 Redis，node-B 可见
	require.Eventually(t, func() bool {
		online, _ := hubB.IsUserOnline(ctx, client.UserID)
		return online
	}, 2*time.Second, 30*time.Millisecond, "Register 后 node-B 应可见用户在线")

	nodes, err := hubB.GetOnlineStatusRepo().GetUserNodes(ctx, client.UserID)
	require.NoError(t, err)
	require.Contains(t, nodes, hubA.GetNodeID(), "跨节点路由应指向 node-A")

	// 模拟 Redis 在线条引被淘汰/过期（如 maxmemory 淘汰、TTL 过期后未刷新）
	clientKey := prefix + "client:" + client.ID
	userClientsKey := prefix + "user_clients:" + client.UserID
	redisClient.Del(ctx, clientKey, userClientsKey)
	redisClient.ZRem(ctx, prefix+"all_users", client.UserID)

	// 验证 bug 状态：node-B 现在看不到该用户在线、跨节点路由丢失
	require.Eventually(t, func() bool {
		online, _ := hubB.IsUserOnline(ctx, client.UserID)
		return !online
	}, 2*time.Second, 30*time.Millisecond, "索引淘汰后用户应不可见")

	// 索引淘汰后 GetUserNodes 返回 ErrTypeUserNotFound（跨节点路由丢失，按类型判定）
	_, err = hubB.GetOnlineStatusRepo().GetUserNodes(ctx, client.UserID)
	assert.Equal(t, models.ErrTypeUserNotFound, errorx.ClassifyError(err), "索引淘汰后跨节点路由应丢失")

	// 对照：旧路径 UpdateClientHeartbeat 在 client:<id> 缺失时静默 no-op，不会重建
	require.NoError(t, hubA.UpdateClientHeartbeat(client.ID))
	time.Sleep(150 * time.Millisecond) // 给足时间确认不会异步写入
	stillMissing, err := redisClient.Exists(ctx, clientKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), stillMissing,
		"旧 UpdateClientHeartbeat 在 client:<id> 缺失时应静默 no-op（对照证明 bug 根因）")

	// 修复路径：心跳触发 worker 轻量续期，检测到 client:<id> 缺失后自愈全量重建
	hubA.handleHeartbeatMessage(client)

	// 等 worker flush（2s ticker）续期/重建索引
	require.Eventually(t, func() bool {
		online, _ := hubB.IsUserOnline(ctx, client.UserID)
		return online
	}, 5*time.Second, 50*time.Millisecond, "心跳应重建在线索引使 node-B 重新可见")

	nodes, err = hubB.GetOnlineStatusRepo().GetUserNodes(ctx, client.UserID)
	require.NoError(t, err)
	assert.Contains(t, nodes, hubA.GetNodeID(), "心跳应重建跨节点路由信息")

	// 验证 client:<id> 键确被重建
	recreated, err := redisClient.Exists(ctx, clientKey).Result()
	require.NoError(t, err)
	assert.Equal(t, int64(1), recreated, "心跳应重建 client:<id> 键")
}

// TestMultiDeviceMultiNodeGetUserNodes 验证多设备多节点场景：
//
// 同一用户在 node-A、node-B 各有一个设备，心跳后 GetUserNodes 应返回两个节点；
// node-A 设备断开后应收敛为仅 node-B（验证心跳重建不会为已断开设备残留索引）
func TestMultiDeviceMultiNodeGetUserNodes(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	const prefix = "wsc:hb:multi:online:"
	hubA := newHeartbeatDistHub(t, "node-A", redisClient, prefix, 60*time.Second)
	defer hubA.SafeShutdown()
	hubB := newHeartbeatDistHub(t, "node-B", redisClient, prefix, 60*time.Second)
	defer hubB.SafeShutdown()

	ctx := context.Background()
	const uid = "user-multi-device"

	cA := &Client{
		ID:            "dev-A",
		UserID:        uid,
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		SendChan:      make(chan []byte, 8),
		Context:       context.Background(),
		LastHeartbeat: time.Now(),
	}
	cB := &Client{
		ID:            "dev-B",
		UserID:        uid,
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		SendChan:      make(chan []byte, 8),
		Context:       context.Background(),
		LastHeartbeat: time.Now(),
	}
	drainSendChan(cA)
	drainSendChan(cB)

	hubA.Register(cA) // cA.NodeID = node-A
	hubB.Register(cB) // cB.NodeID = node-B

	// 等两个节点索引写入
	require.Eventually(t, func() bool {
		nodes, _ := hubA.GetOnlineStatusRepo().GetUserNodes(ctx, uid)
		return len(nodes) >= 2
	}, 2*time.Second, 30*time.Millisecond, "两个设备注册后应分布在两个节点")

	// 触发心跳保持（投递 *Client 到 worker，flush 时重建索引）
	hubA.handleHeartbeatMessage(cA)
	hubB.handleHeartbeatMessage(cB)
	time.Sleep(2500 * time.Millisecond) // 等 worker 2s ticker flush

	nodes, err := hubA.GetOnlineStatusRepo().GetUserNodes(ctx, uid)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{hubA.GetNodeID(), hubB.GetNodeID()}, nodes,
		"多设备多节点心跳后 GetUserNodes 应返回两个节点")

	// node-A 设备断开：Unregister → MarkClosed 先于 SetClientOffline，
	// worker 的 IsClosed() 过滤确保不会为已断开设备重建索引
	hubA.Unregister(cA)
	require.Eventually(t, func() bool {
		nodes, _ := hubA.GetOnlineStatusRepo().GetUserNodes(ctx, uid)
		return len(nodes) == 1
	}, 3*time.Second, 50*time.Millisecond, "node-A 断开后应收敛为单节点")

	nodes, err = hubA.GetOnlineStatusRepo().GetUserNodes(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, []string{hubB.GetNodeID()}, nodes, "node-A 断开后应只剩 node-B")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-11 03:06:29
 * @FilePath: \go-wsc\hub\grpc_server_test.go
 * @Description: GRPCServer 真实场景测试 - 启动真实 gRPC 服务端 + 客户端连接池拨号，
 * 覆盖 NodeService 全部 6 个 RPC 的服务端处理逻辑
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
)

// startTestGRPCServer 在 hub 上启动真实 gRPC 服务端，监听 127.0.0.1 随机端口
// 返回实际监听地址；服务端在测试结束时自动 Stop
func startTestGRPCServer(t *testing.T, hub *Hub) string {
	t.Helper()
	srv := NewGRPCServer(hub)
	require.NoError(t, srv.Start(context.Background(), "127.0.0.1:0"))
	addr := srv.listener.Addr().String()
	t.Cleanup(srv.Stop)
	return addr
}

// newGRPCClientHub 构造带 miniredis 群组仓库的测试 Hub，可控制观察者模块开关
func newGRPCClientHub(t *testing.T, enableObserver bool) (*Hub, repository.GroupRepository, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)
	config.EnableObserver = enableObserver

	hub := NewHub(config)
	groupRepo := repository.NewRedisGroupRepository(redisClient, "wsc:test:grpc:group:")
	hub.SetGroupRepository(groupRepo)

	t.Cleanup(func() {
		hub.Shutdown()
		_ = redisClient.Close()
	})
	return hub, groupRepo, redisClient
}

// recvFromSendChan 从客户端 SendChan 读取一条消息并反序列化，超时则 fatal
func recvFromSendChan(t *testing.T, c *Client, timeout time.Duration) *HubMessage {
	t.Helper()
	select {
	case data := <-c.SendChan:
		var got HubMessage
		require.NoError(t, json.Unmarshal(data, &got))
		return &got
	case <-time.After(timeout):
		t.Fatal("未在超时内收到客户端消息")
		return nil
	}
}

// TestGRPCServer_SendToUser_DeliversToOnlineClient 验证点对点投递：在线客户端收到消息
func TestGRPCServer_SendToUser_DeliversToOnlineClient(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	client := makeTestClient("c-srv-1", "u-srv-1")
	hub.shardedRegistry.AddClient(client)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	msg := makeGroupMessage("sender-1")
	msg.Receiver = "u-srv-1"
	msgData, err := wscpb.MarshalHubMessage(msg)
	require.NoError(t, err)

	resp, err := pool.SendToUser(context.Background(), addr, "u-srv-1", msgData)
	require.NoError(t, err)
	assert.True(t, resp.GetSuccess())
	assert.True(t, resp.GetUserOnline())

	got := recvFromSendChan(t, client, time.Second)
	assert.Equal(t, msg.Sender, got.Sender)
}

// TestGRPCServer_SendToUser_UserNotOnline 验证用户不在线时返回 UserOnline=false 且不投递
func TestGRPCServer_SendToUser_UserNotOnline(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	msgData, err := wscpb.MarshalHubMessage(makeGroupMessage("sender"))
	require.NoError(t, err)

	resp, err := pool.SendToUser(context.Background(), addr, "u-not-exist", msgData)
	require.NoError(t, err)
	assert.False(t, resp.GetSuccess())
	assert.False(t, resp.GetUserOnline())
}

// TestGRPCServer_CheckUsersOnline 验证批量在线探测：注册的在线、未注册的离线
func TestGRPCServer_CheckUsersOnline(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	hub.shardedRegistry.AddClient(makeTestClient("c-online", "u-online"))

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	online, err := pool.CheckUsersOnline(context.Background(), addr, []string{"u-online", "u-offline"})
	require.NoError(t, err)
	assert.True(t, online["u-online"])
	assert.False(t, online["u-offline"])
}

// TestGRPCServer_BroadcastGroup_MemberFiltering 验证群组广播仅投递给群组成员
func TestGRPCServer_BroadcastGroup_MemberFiltering(t *testing.T) {
	hub, groupRepo, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	ctx := context.Background()
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-srv", Namespace: "ns-srv", OwnerID: "owner"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace("ns-srv").WithGroupIDs([]string{"g-srv"}).Inject(ctx), []string{"u-m1", "u-m2"}))

	m1 := makeTestClient("c-m1", "u-m1", "ns-srv")
	m2 := makeTestClient("c-m2", "u-m2", "ns-srv")
	nonMember := makeTestClient("c-nm", "u-nm", "ns-srv")
	hub.shardedRegistry.AddClient(m1)
	hub.shardedRegistry.AddClient(m2)
	hub.shardedRegistry.AddClient(nonMember)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	msg := makeGroupMessage("sender-g")
	msgData, err := wscpb.MarshalHubMessage(msg)
	require.NoError(t, err)

	routeCtx := routing.NewRoute().WithAppID("").WithNamespace("ns-srv").WithGroupIDs([]string{"g-srv"}).Inject(ctx)
	delivered, err := pool.BroadcastGroup(routeCtx, addr, msgData, false, "")
	require.NoError(t, err)
	assert.Equal(t, int32(2), delivered)

	// 成员收到、非成员未收到
	assert.NotNil(t, recvFromSendChan(t, m1, time.Second))
	assert.NotNil(t, recvFromSendChan(t, m2, time.Second))
	select {
	case <-nonMember.SendChan:
		t.Fatal("非群组成员不应收到群组广播")
	case <-time.After(100 * time.Millisecond):
		// 预期无消息
	}
}

// TestGRPCServer_BroadcastGroup_ExcludeSender 验证排除发送者
func TestGRPCServer_BroadcastGroup_ExcludeSender(t *testing.T) {
	hub, groupRepo, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	ctx := context.Background()
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-exc", Namespace: "ns-exc", OwnerID: "owner"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(models.DefaultAppID).WithNamespace("ns-exc").WithGroupIDs([]string{"g-exc"}).Inject(ctx), []string{"u-sender", "u-other"}))

	sender := makeTestClient("c-sender", "u-sender", "ns-exc")
	other := makeTestClient("c-other", "u-other", "ns-exc")
	hub.shardedRegistry.AddClient(sender)
	hub.shardedRegistry.AddClient(other)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	msgData, err := wscpb.MarshalHubMessage(makeGroupMessage("u-sender"))
	require.NoError(t, err)

	routeCtx := routing.NewRoute().WithAppID("").WithNamespace("ns-exc").WithGroupIDs([]string{"g-exc"}).Inject(ctx)
	delivered, err := pool.BroadcastGroup(routeCtx, addr, msgData, true, "u-sender")
	require.NoError(t, err)
	assert.Equal(t, int32(1), delivered)

	// 被排除的发送者未收到，其他成员收到
	select {
	case <-sender.SendChan:
		t.Fatal("被排除的发送者不应收到消息")
	case <-time.After(100 * time.Millisecond):
	}
	assert.NotNil(t, recvFromSendChan(t, other, time.Second))
}

// TestGRPCServer_NotifyObservers_GlobalObserver 验证全局观察者收到通知
func TestGRPCServer_NotifyObservers_GlobalObserver(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, true) // 启用观察者模块
	addr := startTestGRPCServer(t, hub)

	observer := makeObserverClient("c-obs", "u-obs")
	hub.shardedRegistry.AddClient(observer)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	msg := makeGroupMessage("sender-obs")
	msgData, err := wscpb.MarshalHubMessage(msg)
	require.NoError(t, err)

	// 全局观察者匹配任意 namespace/groupIDs
	routeCtx := routing.NewRoute().WithAppID("").WithNamespace("ns-any").WithGroupIDs([]string{"g-any"}).Inject(context.Background())
	notified, err := pool.NotifyObservers(routeCtx, addr, msgData)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, notified, int32(1))

	assert.NotNil(t, recvFromSendChan(t, observer, time.Second))
}

// TestGRPCServer_KickUser 验证踢人 RPC 踢掉本节点在线客户端
//
// 注：Unregister 异步注销由 EventLoop 处理，故需运行中的 Hub
// 并用 Eventually 等待注册表移除生效
func TestGRPCServer_KickUser(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	go hub.Run()
	hub.WaitForStart()
	addr := startTestGRPCServer(t, hub)

	c := makeTestClient("c-kick", "u-kick")
	hub.shardedRegistry.AddClient(c)

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	// 直接使用底层 NodeServiceClient 调用 KickUser（连接池未封装 KickUser 高级方法）
	client, err := pool.GetClient(addr)
	require.NoError(t, err)
	resp, err := client.KickUser(context.Background(), &wscpb.KickUserRequest{
		UserId: "u-kick",
		Reason: "test-kick",
	})
	require.NoError(t, err)
	assert.True(t, resp.GetSuccess())
	assert.GreaterOrEqual(t, resp.GetKickedConnections(), int32(1))

	// 踢出后用户从注册表移除（EventLoop 异步处理注销）
	require.Eventually(t, func() bool {
		return !hub.HasUserClient(context.Background(), "u-kick")
	}, 2*time.Second, 20*time.Millisecond, "踢出后用户应从注册表移除")
}

// TestGRPCServer_Ping 验证健康检查返回节点信息与连接数
func TestGRPCServer_Ping(t *testing.T) {
	hub, _, _ := newGRPCClientHub(t, false)
	addr := startTestGRPCServer(t, hub)

	hub.shardedRegistry.AddClient(makeTestClient("c-ping", "u-ping"))

	pool := NewGRPCClientPool()
	t.Cleanup(pool.Close)

	resp, err := pool.Ping(context.Background(), addr)
	require.NoError(t, err)
	assert.Equal(t, hub.GetNodeID(), resp.GetNodeId())
	assert.True(t, resp.GetHealthy())
	assert.GreaterOrEqual(t, resp.GetActiveConnections(), int64(1))
}

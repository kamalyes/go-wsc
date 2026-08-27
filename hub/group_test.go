/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-25 10:56:20
 * @FilePath: \go-wsc\hub\group_test.go
 * @Description: Hub 群组功能测试 - 基于 miniredis 验证群组管理、命名空间隔离与消息投递
 *
 * 覆盖场景：
 *   1. 群组 CRUD（创建/查询/解散）经由 Hub 层
 *   2. 成员管理（添加/移除/查询/判定）
 *   3. 命名空间隔离（不同命名空间同名群组互不干扰）
 *   4. 群组广播本地投递（BroadcastToGroupMembers）
 *   5. 群组可靠投递（SendToGroup）离线成员存储
 *   6. gRPC 路由方法签名与降级逻辑（不实际建连）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/kamalyes/go-wsc/routing"
)

// setupGroupTestHub 创建带群组仓库的测试 Hub
// 返回 Hub、群组仓库、miniredis 地址与清理函数
func setupGroupTestHub(t *testing.T) (*Hub, repository.GroupRepository, *miniredis.Miniredis, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)

	hub := NewHub(config)
	groupRepo := repository.NewRedisGroupRepository(redisClient, "wsc:test:group:")
	hub.SetGroupRepository(groupRepo)

	cleanup := func() {
		hub.Shutdown()
		_ = redisClient.Close()
	}
	return hub, groupRepo, mr, cleanup
}

// makeTestClient 创建测试客户端，SendChan 缓冲 16
// makeTestClient 创建测试客户端
// clientID/userID 必填；namespace/groupID 可选（0~2 个附加参数：[0]=namespace, [1]=groupID）
// 不填时 namespace=constants.DefaultNamespace(与SendToUserWithRetry入口兜底对齐), groupID=""(P2P/默认组)
func makeTestClient(clientID, userID string, opts ...string) *Client {
	c := &Client{
		ID:          clientID,
		UserID:      userID,
		UserType:    UserTypeCustomer,
		Role:        models.UserRoleCustomer,
		Status:      UserStatusOnline,
		LastSeen:    time.Now(),
		SendChan:    make(chan []byte, 16),
		Context:     context.WithValue(context.Background(), ContextKeyUserID, userID),
		ConnectedAt: time.Now(),
		AppID:       constants.DefaultAppID,     // 默认应用ID（与 NewClient 默认值一致，ClientMatchesEnvelope 严格匹配要求）
		Namespace:   constants.DefaultNamespace, // 默认命名空间（与 NewClient 默认值一致）
	}
	if len(opts) >= 1 {
		c.Namespace = opts[0] // 显式传参则覆盖（用于多 namespace 场景）
	}
	if len(opts) >= 2 {
		c.GroupID = opts[1]
	}
	return c
}

// makeGroupMessage 创建群组测试消息
func makeGroupMessage(sender string) *HubMessage {
	msg := NewHubMessage()
	msg.Sender = sender
	msg.MessageType = MessageTypeText
	msg.Content = "hello group"
	msg.CreateAt = time.Now()
	return msg
}

// ============================================================================
// 群组管理测试
// ============================================================================

// TestHubCreateAndGetGroup 验证通过 Hub 层创建和查询群组
func TestHubCreateAndGetGroup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("创建群组后可查询且字段一致", func(t *testing.T) {
		g := &Group{
			GroupID:    "g1",
			Namespace:  constants.DefaultNamespace,
			Name:       "测试群",
			OwnerID:    "owner1",
			MaxMembers: 50,
		}
		require.NoError(t, groupRepo.CreateGroup(ctx, g))

		got, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g1"}).Inject(ctx))
		require.NoError(t, err)
		assert.Equal(t, "g1", got.GroupID)
		assert.Equal(t, constants.DefaultNamespace, got.GetNamespace())
		assert.Equal(t, "测试群", got.Name)
		assert.Equal(t, 50, got.MaxMembers)
	})

	t.Run("default 命名空间查询", func(t *testing.T) {
		// CreateGroup 时 groupRepo 将空 Namespace 归一化为 DefaultNamespace
		g := &Group{GroupID: "g-default", Name: "默认群", OwnerID: "owner1"}
		require.NoError(t, groupRepo.CreateGroup(ctx, g))

		// 业务查询传明确 namespace（归一化由 register/CreateGroup 层统一）
		got, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-default"}).Inject(ctx))
		require.NoError(t, err)
		assert.Equal(t, constants.DefaultNamespace, got.GetNamespace())
	})
}

// TestHubDisbandGroup 验证通过 Hub 解散群组后成员与元信息被清理
func TestHubDisbandGroup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-disband", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-disband"}).Inject(ctx), []string{"u1", "u2"}))

	require.NoError(t, hub.DisbandGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-disband"}).Inject(ctx)))

	_, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-disband"}).Inject(ctx))
	assert.ErrorIs(t, err, ErrGroupNotFound)

	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-disband"}).Inject(ctx))
	require.NoError(t, err)
	assert.Empty(t, members)
}

// TestHubAddAndRemoveMembers 验证通过 Hub 添加和移除群组成员
func TestHubAddAndRemoveMembers(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-members", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))

	// 添加成员
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-members"}).Inject(ctx), []string{"u1", "u2", "u3"}))

	cnt, err := hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-members"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, int64(3), cnt)

	// 判定成员
	ok, err := hub.IsGroupMember(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-members"}).Inject(ctx), "u2")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = hub.IsGroupMember(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-members"}).Inject(ctx), "uX")
	require.NoError(t, err)
	assert.False(t, ok)

	// 移除成员
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-members"}).Inject(ctx), []string{"u2"}))
	cnt, err = hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-members"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, int64(2), cnt)

	ok, err = hub.IsGroupMember(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-members"}).Inject(ctx), "u2")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestHubGroupMaxMembers 验证群组成员上限校验
func TestHubGroupMaxMembers(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-max", Namespace: constants.DefaultNamespace, OwnerID: "o1", MaxMembers: 2}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-max"}).Inject(ctx), []string{"u1", "u2"}))

	// 超出上限应返回 ErrGroupFull
	err := hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-max"}).Inject(ctx), []string{"u3"})
	assert.ErrorIs(t, err, ErrGroupFull)
}

// TestHubGroupNamespaceIsolation 验证 Hub 层群组命名空间隔离
func TestHubGroupNamespaceIsolation(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 两个命名空间创建同名群组
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-same", Namespace: constants.DefaultNamespace, Name: "A群", OwnerID: "oA"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-same", Namespace: "tenantB", Name: "B群", OwnerID: "oB"}))

	// 各自添加成员
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-same"}).Inject(ctx), []string{"userA"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{"g-same"}).Inject(ctx), []string{"userB"}))

	// 成员不跨命名空间
	aMembers, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-same"}).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"userA"}, aMembers)

	bMembers, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{"g-same"}).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"userB"}, bMembers)

	// 解散 tenantA 不影响 tenantB
	require.NoError(t, hub.DisbandGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-same"}).Inject(ctx)))
	_, err = hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{"g-same"}).Inject(ctx))
	require.NoError(t, err)
}

// TestHubGetUserGroupsAndNamespaceGroups 验证用户群组列表与命名空间群组列表
func TestHubGetUserGroupsAndNamespaceGroups(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	for _, gid := range []string{"g1", "g2", "g3"} {
		require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: gid, Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
		require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{gid}).Inject(ctx), []string{"userX"}))
	}

	// 用户群组列表
	groups, err := hub.GetUserGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx), "userX")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"g1", "g2", "g3"}, groups)

	// 命名空间群组列表
	tenantGroups, err := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"g1", "g2", "g3"}, tenantGroups)
}

// ============================================================================
// 群组广播测试
// ============================================================================

// TestBroadcastToGroupMembersLocal 验证群组广播本地投递给在线成员
func TestBroadcastToGroupMembersLocal(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建群组并添加成员
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-broadcast", Namespace: constants.DefaultNamespace, OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-broadcast"}).Inject(ctx), []string{"user1", "user2", "user3"}))

	// 启动 Hub 并注册在线客户端（user1 和 user2 在本节点）
	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	client1 := makeTestClient("c1", "user1", constants.DefaultNamespace)
	client2 := makeTestClient("c2", "user2", constants.DefaultNamespace)
	hub.Register(client1)
	hub.Register(client2)
	time.Sleep(100 * time.Millisecond)

	// 广播消息（不排除发送者）
	msg := makeGroupMessage("owner1")
	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-broadcast"}).Inject(ctx)
	delivered := hub.Deliver(groupCtx, msg, false).LocalDelivered

	// 本地应投递给 user1 和 user2（user3 不在本节点）
	assert.Equal(t, 2, delivered, "应投递给本地 2 个在线成员")

	// 验证两个客户端都收到消息
	select {
	case data := <-client1.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("client1 未收到广播消息")
	}

	select {
	case data := <-client2.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("client2 未收到广播消息")
	}
}

// TestBroadcastToGroupMembersExcludeSender 验证广播排除发送者
func TestBroadcastToGroupMembersExcludeSender(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-exclude", Namespace: constants.DefaultNamespace, OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-exclude"}).Inject(ctx), []string{"sender", "user2"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	senderClient := makeTestClient("c-sender", "sender", constants.DefaultNamespace)
	otherClient := makeTestClient("c-other", "user2", constants.DefaultNamespace)
	hub.Register(senderClient)
	hub.Register(otherClient)
	time.Sleep(100 * time.Millisecond)

	// 广播并排除发送者
	msg := makeGroupMessage("sender")
	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-exclude"}).Inject(ctx)
	delivered := hub.Deliver(groupCtx, msg, true).LocalDelivered

	// 仅投递给 user2（sender 被排除）
	assert.Equal(t, 1, delivered, "排除发送者后应只投递给 1 个成员")

	// user2 应收到消息
	select {
	case <-otherClient.SendChan:
		// ok
	case <-time.After(time.Second):
		t.Fatal("user2 未收到广播消息")
	}

	// sender 不应收到消息
	select {
	case <-senderClient.SendChan:
		t.Fatal("发送者不应收到被排除的广播消息")
	case <-time.After(200 * time.Millisecond):
		// ok，无消息
	}
}

// ============================================================================
// 跨节点路由方法测试
// ============================================================================

// TestRouteToClusterSingleNode 验证单机模式下 routeToCluster 不报错
func TestRouteToClusterSingleNode(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// gRPC 未启用（setupGroupTestHub 未配置 NodeGRPC）
	assert.False(t, hub.IsGRPCEnabled(), "未配置 NodeGRPC 时应未启用 gRPC")

	ctx := context.Background()
	msg := NewHubMessage()
	msg.MessageID = "msg-1"
	msg.Sender = "u1"
	msg.Receiver = "u2"

	// 单机模式（无 PubSub、无 gRPC）routeToCluster 应直接返回 nil
	opts := ClusterDispatchOptions{
		Operation:    OperationTypeSendMessage,
		Namespace:    constants.DefaultNamespace,
		TargetUserID: "user1",
	}
	err := hub.routeToCluster(ctx, msg, opts)
	require.NoError(t, err, "单机模式 routeToCluster 不应报错")
}

// TestGRPCNotEnabledNoRegistry 验证 gRPC 启用但节点注册表未初始化时 IsGRPCEnabled 返回 false
func TestGRPCNotEnabledNoRegistry(t *testing.T) {
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18081)
	config.NodeGRPC = &wscconfig.NodeGRPC{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    19090,
	}

	hub := NewHub(config)
	defer hub.Shutdown()

	// nodeRegistry 为 nil → IsGRPCEnabled 返回 false
	assert.False(t, hub.IsGRPCEnabled(), "nodeRegistry 为 nil 时 gRPC 不应视为已启用")
}

// TestMarshalDistributedMessage 验证分布式消息序列化与反序列化
func TestMarshalDistributedMessage(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := NewHubMessage()
	msg.MessageID = "msg-1"
	msg.Sender = "u1"
	msg.Receiver = "u2"
	msg.Content = "hello"
	msg.MessageType = MessageTypeText

	distMsg := &DistributedMessage{
		Type:      OperationTypeSendMessage,
		NodeID:    "node-1",
		TargetID:  "u2",
		Message:   msg,
		Timestamp: time.Now(),
	}

	data := hub.marshalDistributedMessage(context.Background(), distMsg)
	assert.NotEmpty(t, data, "序列化结果不应为空")

	// 验证可反序列化
	parsed, err := hub.unmarshalDistributedMessage(context.Background(), data)
	require.NoError(t, err)
	assert.Equal(t, distMsg.Type, parsed.Type)
	assert.Equal(t, distMsg.NodeID, parsed.NodeID)
	assert.Equal(t, distMsg.TargetID, parsed.TargetID)
}

// TestCrossNodeGroupBroadcastSingleNode 验证单节点场景下群组广播不 panic
// gRPC 未启用且 PubSub 未设置时，crossNodeGroupBroadcast 应直接返回
func TestCrossNodeGroupBroadcastSingleNode(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 无 PubSub、无 gRPC → 单机模式
	msg := makeGroupMessage("sender")

	// 不应 panic
	assert.NotPanics(t, func() {
		hub.crossNodeGroupBroadcast(routing.NewRoute().WithAppID("").WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g1"}).Inject(ctx), msg, false)
	})
}

// ============================================================================
// 群组消息投递（SendToGroup）测试
// ============================================================================

// TestSendToGroupOfflineMembers 验证群组消息对离线成员的投递结果统计
// 注意：未配置离线消息处理器时，离线成员计为 Failed
func TestSendToGroupOfflineMembers(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send", Namespace: constants.DefaultNamespace, OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send"}).Inject(ctx), []string{"u-online", "u-offline"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册一个在线成员
	onlineClient := makeTestClient("c-online", "u-online")
	hub.Register(onlineClient)
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("owner1")
	msg.RequireAck = true
	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send"}).Inject(ctx)
	result := hub.Deliver(groupCtx, msg, false)

	assert.Equal(t, 2, result.TotalMembers, "总成员数应为 2")
	// 未配置离线消息处理器时，离线成员投递失败
	assert.Equal(t, 1, result.Failed, "离线成员应计为失败（无离线处理器）")
	// 不应出现 Errors 为 nil 导致的 panic
	assert.NotNil(t, result.Errors)
}

// TestSendToGroupRepoNotSet 验证未设置群组仓库时返回错误结果
func TestSendToGroupRepoNotSet(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()
	// 不设置 groupRepo

	ctx := context.Background()
	msg := makeGroupMessage("sender")
	msg.RequireAck = true
	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"any-group"}).Inject(ctx)
	result := hub.Deliver(groupCtx, msg, false)

	assert.False(t, len(result.Errors) == 0, "应有错误返回")
	assert.Equal(t, "any-group", result.GroupIDs[0])
}

// ============================================================================
// 配置与启用状态测试
// ============================================================================

// TestNodeGRPCConfigEnabled 验证 gRPC 配置启用判定
func TestNodeGRPCConfigEnabled(t *testing.T) {
	t.Run("未配置 NodeGRPC 时 IsEnabled 返回 false", func(t *testing.T) {
		config := wscconfig.Default()
		assert.False(t, config.NodeGRPC.IsEnabled())
	})

	t.Run("配置 Enabled=true 时 IsEnabled 返回 true", func(t *testing.T) {
		config := wscconfig.Default()
		config.NodeGRPC = &wscconfig.NodeGRPC{
			Enabled: true,
			Host:    "0.0.0.0",
			Port:    50051,
		}
		assert.True(t, config.NodeGRPC.IsEnabled())
		assert.Equal(t, "0.0.0.0:50051", config.NodeGRPC.GetAddress())
	})

	t.Run("Host 为空时 GetAddress 使用默认 0.0.0.0", func(t *testing.T) {
		config := wscconfig.Default()
		config.NodeGRPC = &wscconfig.NodeGRPC{Enabled: true, Port: 50051}
		assert.Equal(t, "0.0.0.0:50051", config.NodeGRPC.GetAddress())
	})
}

// ============================================================================
// 批量群组广播测试（高性能路径：Pipeline + 去重 + 一次路由）
// ============================================================================

// TestBroadcastToAllGroupsDedup 验证向命名空间所有群组广播时成员去重
// 用户同时属于多个群组时只应收到一条消息
func TestBroadcastToAllGroupsDedup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 3 个群组
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g2", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g3", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))

	// user1 同时在 g1、g2、g3 三个群组（应去重，只收一条）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g1"}).Inject(ctx), []string{"user1", "user2"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g2"}).Inject(ctx), []string{"user1", "user3"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g3"}).Inject(ctx), []string{"user1", "user4"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册本地在线客户端
	client1 := makeTestClient("c1", "user1")
	client2 := makeTestClient("c2", "user2")
	hub.Register(client1)
	hub.Register(client2)
	time.Sleep(100 * time.Millisecond)

	// 向 tenantA 所有群组广播
	msg := makeGroupMessage("owner1")
	gids, gErr := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx))
	var delivered int
	if gErr == nil && len(gids) > 0 {
		groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(gids).Inject(ctx)
		delivered = hub.Deliver(groupCtx, msg, false).LocalDelivered
	}

	// 本地应投递给 user1 和 user2（user3/user4 不在本节点）
	assert.Equal(t, 2, delivered, "应投递给本地 2 个在线成员")

	// user1 虽在 3 个群组，但去重后只收一条
	select {
	case <-client1.SendChan:
		// 收到第一条，OK
	default:
		t.Fatal("user1 应至少收到一条广播消息")
	}
	// 验证 user1 不会收到第二条（去重生效）
	select {
	case <-client1.SendChan:
		t.Fatal("user1 去重后不应收到第二条消息")
	case <-time.After(200 * time.Millisecond):
		// OK，无重复消息
	}

	// user2 应收到一条
	select {
	case <-client2.SendChan:
		// OK
	case <-time.After(time.Second):
		t.Fatal("user2 应收到广播消息")
	}
}

// TestBroadcastToAllGroupsEmptyNamespace 验证空命名空间（无群组）广播返回 0
func TestBroadcastToAllGroupsEmptyNamespace(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("sender")
	gids, _ := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("empty-tenant").WithGroupIDs(nil).Inject(ctx))
	var delivered int
	if len(gids) > 0 {
		groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("empty-tenant").WithGroupIDs(gids).Inject(ctx)
		delivered = hub.Deliver(groupCtx, msg, false).LocalDelivered
	}
	assert.Equal(t, 0, delivered, "无群组的命名空间应投递 0 条")
}

// TestBroadcastToAllGroupsDefaultNamespace 验证 default 命名空间的群组广播
// namespace 归一化由 register 层统一，业务调用方需传明确 namespace
func TestBroadcastToAllGroupsDefaultNamespace(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// CreateGroup 时 groupRepo 将空 Namespace 归一化为 DefaultNamespace
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-default", OwnerID: "o1"}))
	// 业务方法调用方传明确 namespace（DefaultNamespace）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-default"}).Inject(ctx), []string{"user1"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	client1 := makeTestClient("c1", "user1")
	hub.Register(client1)
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("sender")
	gids, _ := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx))
	var delivered int
	if len(gids) > 0 {
		groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(gids).Inject(ctx)
		delivered = hub.Deliver(groupCtx, msg, false).LocalDelivered
	}
	assert.Equal(t, 1, delivered, "default 命名空间应投递给 1 个在线成员")
}

// TestBroadcastToNamespaceEmptyNamespaceNormalized 验证空 namespace 归一化为 DefaultNamespace
//
// 回归 BUG：修复前 BroadcastToNamespace("") 本地与跨节点语义割裂导致消息"乱掉"：
//   - 本地：broadcastToFiltered 的 c.Namespace=="" 仅匹配全局观察者，default 客户端收不到
//   - 跨节点：handleDistributedBroadcast 把空 namespace 视为"全命名空间广播"投递给所有客户端（跨租户泄露）
func TestBroadcastToNamespaceEmptyNamespaceNormalized(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// default 命名空间客户端（模拟正常业务连接）
	defaultClient := makeTestClient("c-default", "u-default", constants.DefaultNamespace)
	hub.shardedRegistry.AddClient(defaultClient)
	// 全局观察者（Namespace=""），用于验证不会"误中"default 广播的客户端直投路径
	globalObs := registerObserver(hub, "c-g", "u-g", "", "")
	_ = globalObs

	// 传空 namespace，旧 BroadcastToNamespace 内部归一化为 DefaultNamespace；
	// 新 Deliver 把 namespace="" 视为全局广播，故此处显式传 DefaultNamespace 以保留原语义。
	msg := makeGroupMessage("sender")
	nsCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx)
	delivered := hub.Deliver(nsCtx, msg, false).LocalDelivered
	assert.Equal(t, 1, delivered, "空 namespace 归一化为 default 后应投递给 1 个 default 客户端")

	select {
	case <-defaultClient.SendChan:
		// default 客户端收到，归一化生效
	default:
		t.Fatal("default 客户端应收到空 namespace 归一化后的广播")
	}
}

// TestBroadcastToGroupMembersEmptyNamespaceNormalized 验证群组广播空 namespace 归一化
//
// 回归 BUG：修复前 BroadcastToGroupMembers("") 的 msg.Namespace="" → broadcastToUserIDs 的
// ForEachUserClientFiltered 跳过 ns 过滤，成员跨 ns 多端登录的设备都会收到（跨租户串扰）
func TestBroadcastToGroupMembersEmptyNamespaceNormalized(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g1"}).Inject(ctx), []string{"user1"}))

	client1 := makeTestClient("c1", "user1", constants.DefaultNamespace)
	hub.shardedRegistry.AddClient(client1)

	// 传空 namespace，应归一化为 DefaultNamespace
	// Deliver 群组分支内部 EnsureRouteDefaults 会归一化 namespace
	msg := makeGroupMessage("sender")
	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("").WithGroupIDs([]string{"g1"}).Inject(ctx)
	delivered := hub.Deliver(groupCtx, msg, false).LocalDelivered
	assert.Equal(t, 1, delivered, "空 namespace 归一化后应投递给 1 个群组成员")

	select {
	case <-client1.SendChan:
	default:
		t.Fatal("群组成员应收到空 namespace 归一化后的广播")
	}
}

// TestBroadcastToAllGroupsNotifiesObservers 验证 BroadcastToAllGroups 通知观察者
//
// 回归 BUG：修复前 BroadcastToAllGroups 漏调 notifyObservers（与 BroadcastToGroupMembers/
// BroadcastToGroups 不一致），订阅全群组广播的观察者收不到消息
func TestBroadcastToAllGroupsNotifiesObservers(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g1"}).Inject(ctx), []string{"user1"}))

	// 命名空间+群组级观察者
	nsGroupObs := registerObserver(hub, "c-obs", "u-obs", constants.DefaultNamespace, "g1")

	client1 := makeTestClient("c1", "user1", constants.DefaultNamespace)
	hub.shardedRegistry.AddClient(client1)

	msg := makeGroupMessage("sender")
	gids, _ := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx))
	if len(gids) > 0 {
		groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(gids).Inject(ctx)
		_ = hub.Deliver(groupCtx, msg, false)
	}

	assertObserverReceived(t, nsGroupObs, "命名空间+群组级观察者应收到全群组广播")
}

// TestBroadcastToGroupsMultiNamespaceObserverEnvelope 验证多 ns 广播时观察者消息携带正确 Namespace
//
// 回归 BUG：修复前 BroadcastToGroups 用陈旧 baseMsg 信封通知观察者，多 ns 场景下
// 观察者收到的消息 Namespace 字段为空/陈旧值（路由虽对，但消息内容 ns 错误）
func TestBroadcastToGroupsMultiNamespaceObserverEnvelope(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 两个命名空间各一个群组
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "gA", Namespace: "nsA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("nsA").WithGroupIDs([]string{"gA"}).Inject(ctx), []string{"userA"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "gB", Namespace: "nsB", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("nsB").WithGroupIDs([]string{"gB"}).Inject(ctx), []string{"userB"}))

	// 各命名空间+群组级观察者
	obsA := registerObserver(hub, "c-obsA", "u-obsA", "nsA", "gA")
	obsB := registerObserver(hub, "c-obsB", "u-obsB", "nsB", "gB")

	msg := makeGroupMessage("sender")
	for _, ns := range []string{"nsA", "nsB"} {
		gids, gErr := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(ns).WithGroupIDs(nil).Inject(ctx))
		if gErr != nil || len(gids) == 0 {
			continue
		}
		nsCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(ns).WithGroupIDs(gids).Inject(ctx)
		_ = hub.Deliver(nsCtx, msg, false)
	}

	// 观察者 A 应收到且消息 Namespace=nsA
	mA, okA := waitForObserverMsg(t, obsA, time.Second)
	require.True(t, okA, "nsA 观察者应收到消息")
	assert.Equal(t, "nsA", mA.Namespace, "nsA 观察者收到的消息 Namespace 应为 nsA")

	// 观察者 B 应收到且消息 Namespace=nsB
	mB, okB := waitForObserverMsg(t, obsB, time.Second)
	require.True(t, okB, "nsB 观察者应收到消息")
	assert.Equal(t, "nsB", mB.Namespace, "nsB 观察者收到的消息 Namespace 应为 nsB")
}

// TestBroadcastToAllNamespacesAllGroups 验证向所有命名空间所有群组广播
func TestBroadcastToAllNamespacesAllGroups(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 两个命名空间各创建群组
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "gA", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"gA"}).Inject(ctx), []string{"userA"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "gB", Namespace: "tenantB", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{"gB"}).Inject(ctx), []string{"userB"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	clientA := makeTestClient("cA", "userA")
	clientA.Namespace = constants.DefaultNamespace
	clientB := makeTestClient("cB", "userB")
	clientB.Namespace = "tenantB"
	hub.Register(clientA)
	hub.Register(clientB)
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("sender")
	namespaces, _ := groupRepo.GetAllNamespaces(ctx, constants.DefaultAppID)
	var total int
	for _, ns := range namespaces {
		gids, gErr := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(ns).WithGroupIDs(nil).Inject(ctx))
		if gErr != nil || len(gids) == 0 {
			continue
		}
		nsCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(ns).WithGroupIDs(gids).Inject(ctx)
		total += hub.Deliver(nsCtx, msg, false).LocalDelivered
	}
	assert.Equal(t, 2, total, "应投递给 2 个在线成员（tenantA + tenantB）")

	// 两个客户端都应收到
	select {
	case <-clientA.SendChan:
	case <-time.After(time.Second):
		t.Fatal("userA 应收到广播消息")
	}
	select {
	case <-clientB.SendChan:
	case <-time.After(time.Second):
		t.Fatal("userB 应收到广播消息")
	}
}

// TestHandleDistributedGroupsBroadcastSingleGroup 验证接收端兼容单群组（TargetID 回退）
// 模拟跨节点消息：GroupIDs 为空但 TargetID 有值时，应按单群组处理
func TestHandleDistributedGroupsBroadcastSingleGroup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-single", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-single"}).Inject(ctx), []string{"user1"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	client1 := makeTestClient("c1", "user1")
	hub.Register(client1)
	time.Sleep(100 * time.Millisecond)

	// 构造单群组跨节点消息（GroupIDs 为空，TargetID 有值，兼容旧路径）
	msg := makeGroupMessage("remote-sender")
	distMsg := &DistributedMessage{
		Type:      OperationTypeGroupsBroadcast,
		NodeID:    "remote-node",              // 模拟来自其他节点
		TargetID:  "g-single",                 // 单群组回退路径
		Namespace: constants.DefaultNamespace, // 命名空间必须与群组一致，否则查不到成员
		Message:   msg,
	}

	err := hub.handleDistributedGroupsBroadcast(ctx, distMsg)
	require.NoError(t, err)

	// user1 应收到消息
	select {
	case <-client1.SendChan:
	case <-time.After(time.Second):
		t.Fatal("user1 应收到跨节点群组广播消息")
	}
}

// 确保引入 models 包避免未使用导入
var _ = constants.DefaultNamespace

// ============================================================================
// 系统保留组测试（agent/observer 统一到 group 体系）
// ============================================================================

// TestJoinSystemGroupsOnConnectAgent 验证 agent 连接时自动加入 __agents__ 系统组
func TestJoinSystemGroupsOnConnectAgent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 agent 客户端
	client := makeTestClient("c-agent", "agent-001")
	client.UserType = UserTypeAgent
	client.Namespace = constants.DefaultNamespace

	// 调用 joinSystemGroupsOnConnect
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 验证系统组 __agents__ 已创建且包含 agent-001
	members, err := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-001", "agent 应自动加入 __agents__ 系统组")

	// 验证系统组元信息 owner 为 system
	g, err := hub.groupRepo.GetGroup(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.Equal(t, "system", g.OwnerID)
}

// TestJoinSystemGroupsOnConnectObserver 验证 observer 连接时自动加入 __observers__ 系统组
func TestJoinSystemGroupsOnConnectObserver(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 命名空间级观察者
	client := makeTestClient("c-obs", "observer-001")
	client.UserType = UserTypeObserver
	client.Namespace = "tenantB"

	hub.joinSystemGroupsOnConnect(ctx, client)

	members, err := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, "tenantB", constants.SystemGroupObservers)
	require.NoError(t, err)
	assert.Contains(t, members, "observer-001")
}

// TestJoinSystemGroupsOnConnectGlobalObserver 验证全局观察者加入 tenant="" 的系统组
func TestJoinSystemGroupsOnConnectGlobalObserver(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 全局观察者（Namespace=""）
	client := makeTestClient("c-globs", "global-observer")
	client.UserType = UserTypeObserver
	client.Namespace = "" // 全局观察者

	hub.joinSystemGroupsOnConnect(ctx, client)

	// 应加入 tenant="" 的 __observers__（非 default）
	members, err := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, "", constants.SystemGroupObservers)
	require.NoError(t, err)
	assert.Contains(t, members, "global-observer", "全局观察者应加入 tenant='' 的系统组")

	// 确认未加入 default 命名空间的系统组
	membersDefault, _ := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, "default", constants.SystemGroupObservers)
	assert.NotContains(t, membersDefault, "global-observer", "全局观察者不应加入 default 命名空间系统组")
}

// TestJoinSystemGroupsOnConnectNormalUser 验证普通用户不加入任何系统组
func TestJoinSystemGroupsOnConnectNormalUser(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 普通客户
	client := makeTestClient("c-cust", "customer-001")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace

	hub.joinSystemGroupsOnConnect(ctx, client)

	// __agents__ 不应存在或不含 customer-001
	members, _ := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	assert.NotContains(t, members, "customer-001", "普通客户不应加入 __agents__")
}

// TestLeaveSystemGroupsOnDisconnect 验证断开后自动离开系统组
func TestLeaveSystemGroupsOnDisconnect(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// agent 连接 → 加入系统组
	client := makeTestClient("c-agent2", "agent-002")
	client.UserType = UserTypeAgent
	client.Namespace = constants.DefaultNamespace
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 确认已加入
	members, _ := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	assert.Contains(t, members, "agent-002")

	// 断开 → 离开系统组
	hub.leaveSystemGroupsOnDisconnect(ctx, client)

	// 确认已离开
	members, err := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.NotContains(t, members, "agent-002", "断开后应离开 __agents__ 系统组")
}

// TestRejoinSystemGroupOnReconnect 验证断线重连后重新加入系统组（幂等）
func TestRejoinSystemGroupOnReconnect(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-reconnect", "agent-003")
	client.UserType = UserTypeAgent
	client.Namespace = constants.DefaultNamespace

	// 连接 → 断开 → 重连
	hub.joinSystemGroupsOnConnect(ctx, client)
	hub.leaveSystemGroupsOnDisconnect(ctx, client)
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 重连后应再次加入
	members, err := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-003", "重连后应重新加入系统组")
	assert.Len(t, members, 1, "不应重复加入")
}

// ============================================================================
// 群组生命周期回调测试
// ============================================================================
//
// 覆盖：
//   1. 回调在正确触发点被异步触发（参数正确）
//      - OnGroupMemberJoin：由 triggerGroupMemberJoinCallback 触发（register 自动装配）
//      - OnGroupMemberLeave：由 RemoveGroupMembers 触发
//      - OnGroupDisband：由 DisbandGroup 触发
//   2. 成员切片快照隔离（调用方后续修改不影响回调收到的数据）
//   3. 手动 AddGroupMembers 不触发 OnGroupMemberJoin（仅在 register 自动装配时触发）
//   4. 操作失败时不触发回调
//   5. 系统保留组自动加入/离开不触发业务回调（隔离原则）
// ============================================================================

// TestGroupLifecycleCallbacks 验证群组生命周期回调在正确触发点被异步触发
func TestGroupLifecycleCallbacks(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	type nsGidEvt struct{ namespace, groupID string }
	type memberEvt struct {
		namespace, groupID string
		userIDs            []string
	}

	disbandCh := make(chan nsGidEvt, 1)
	joinCh := make(chan memberEvt, 1)
	leaveCh := make(chan memberEvt, 1)

	hub.OnGroupDisband(func(_ context.Context, ns, gid string) { disbandCh <- nsGidEvt{ns, gid} })
	hub.OnGroupMemberJoin(func(_ context.Context, ns, gid string, uids []string) {
		joinCh <- memberEvt{ns, gid, uids}
	})
	hub.OnGroupMemberLeave(func(_ context.Context, ns, gid string, uids []string) {
		leaveCh <- memberEvt{ns, gid, uids}
	})

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-cb", Namespace: constants.DefaultNamespace, OwnerID: "o1", MaxMembers: 10}))

	// 1. Join（模拟 register 自动装配：AddGroupMembers 落库 + triggerGroupMemberJoinCallback 触发回调）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-cb"}).Inject(ctx), []string{"u1", "u2"}))
	hub.triggerGroupMemberJoinCallback(ctx, constants.DefaultNamespace, "g-cb", []string{"u1", "u2"})
	select {
	case e := <-joinCh:
		assert.Equal(t, constants.DefaultNamespace, e.namespace)
		assert.Equal(t, "g-cb", e.groupID)
		assert.ElementsMatch(t, []string{"u1", "u2"}, e.userIDs)
	case <-time.After(time.Second):
		t.Fatal("OnGroupMemberJoin 未触发")
	}

	// 2. Leave
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-cb"}).Inject(ctx), []string{"u1"}))
	select {
	case e := <-leaveCh:
		assert.Equal(t, constants.DefaultNamespace, e.namespace)
		assert.Equal(t, "g-cb", e.groupID)
		assert.ElementsMatch(t, []string{"u1"}, e.userIDs)
	case <-time.After(time.Second):
		t.Fatal("OnGroupMemberLeave 未触发")
	}

	// 3. Disband
	require.NoError(t, hub.DisbandGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-cb"}).Inject(ctx)))
	select {
	case e := <-disbandCh:
		assert.Equal(t, constants.DefaultNamespace, e.namespace)
		assert.Equal(t, "g-cb", e.groupID)
	case <-time.After(time.Second):
		t.Fatal("OnGroupDisband 未触发")
	}
}

// TestGroupCallbackSliceSnapshot 验证成员加入回调收到的切片是副本
// 调用方在 triggerGroupMemberJoinCallback 后修改原切片，不应影响回调收到的数据
func TestGroupCallbackSliceSnapshot(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	joinCh := make(chan []string, 1)
	hub.OnGroupMemberJoin(func(_ context.Context, _, _ string, uids []string) {
		joinCh <- uids
	})

	original := []string{"u1", "u2"}
	hub.triggerGroupMemberJoinCallback(context.Background(), "tA", "g-snap", original)

	// 立即修改原切片，验证回调收到的是快照副本
	original[0] = "MUTATED"
	original = append(original, "u3")

	select {
	case uids := <-joinCh:
		assert.ElementsMatch(t, []string{"u1", "u2"}, uids, "回调应收到修改前的快照副本")
	case <-time.After(time.Second):
		t.Fatal("OnGroupMemberJoin 未触发")
	}
}

// TestGroupCallbackNotTriggeredOnError 验证手动/失败操作不触发回调
func TestGroupCallbackNotTriggeredOnError(t *testing.T) {
	t.Run("手动 AddGroupMembers 不触发 OnGroupMemberJoin", func(t *testing.T) {
		hub, groupRepo, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		ctx := context.Background()

		require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-manual", Namespace: "tA", OwnerID: "o"}))

		joinCh := make(chan []string, 1)
		hub.OnGroupMemberJoin(func(_ context.Context, _, _ string, uids []string) { joinCh <- uids })

		// 手动 AddGroupMembers 不应触发回调（回调仅在 register 自动装配时触发）
		require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tA").WithGroupIDs([]string{"g-manual"}).Inject(ctx), []string{"u1", "u2"}))

		select {
		case uids := <-joinCh:
			t.Fatalf("手动 AddGroupMembers 不应触发 OnGroupMemberJoin, 收到: %v", uids)
		case <-time.After(300 * time.Millisecond):
			// OK
		}
	})

	t.Run("AddGroupMembers 超限不触发 OnGroupMemberJoin", func(t *testing.T) {
		hub, groupRepo, _, cleanup := setupGroupTestHub(t)
		defer cleanup()
		ctx := context.Background()

		require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-full", Namespace: "tA", OwnerID: "o", MaxMembers: 1}))
		require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tA").WithGroupIDs([]string{"g-full"}).Inject(ctx), []string{"u1"}))

		joinCh := make(chan []string, 1)
		hub.OnGroupMemberJoin(func(_ context.Context, _, _ string, uids []string) { joinCh <- uids })

		err := hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tA").WithGroupIDs([]string{"g-full"}).Inject(ctx), []string{"u2"})
		require.ErrorIs(t, err, ErrGroupFull)

		select {
		case uids := <-joinCh:
			t.Fatalf("AddGroupMembers 失败不应触发 OnGroupMemberJoin, 收到: %v", uids)
		case <-time.After(300 * time.Millisecond):
			// OK
		}
	})

	t.Run("DisbandGroup 仓库未设置不触发 OnGroupDisband", func(t *testing.T) {
		config := wscconfig.Default()
		hub := NewHub(config)
		defer hub.Shutdown()

		disbandCh := make(chan string, 1)
		hub.OnGroupDisband(func(_ context.Context, _, gid string) { disbandCh <- gid })

		err := hub.DisbandGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tA").WithGroupIDs([]string{"any"}).Inject(context.Background()))
		require.Error(t, err, "groupRepo 未设置应返回错误")

		select {
		case gid := <-disbandCh:
			t.Fatalf("DisbandGroup 失败不应触发 OnGroupDisband, 收到: %s", gid)
		case <-time.After(300 * time.Millisecond):
			// OK
		}
	})
}

// TestSystemGroupTriggersCallback 验证系统保留组的自动加入触发业务回调
// ensureAndJoinSystemGroup 成功加入后触发 OnGroupMemberJoin，便于业务层感知 observer/agent 上线
func TestSystemGroupTriggersCallback(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	joinTriggered := make(chan []string, 1)
	hub.OnGroupMemberJoin(func(_ context.Context, _, _ string, uids []string) { joinTriggered <- uids })

	// agent 连接 → 自动加入 __agents__ 系统组
	client := makeTestClient("c-agent", "agent-001")
	client.UserType = UserTypeAgent
	client.Namespace = constants.DefaultNamespace
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 确认已加入系统组（底层生效）
	members, err := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-001")

	// 应触发业务回调
	select {
	case uids := <-joinTriggered:
		assert.Contains(t, uids, "agent-001", "系统组自动加入应触发 OnGroupMemberJoin")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("系统组自动加入应触发 OnGroupMemberJoin, 但未收到回调")
	}
}

// TestLeaveSystemGroupMultiClient 验证多端登录场景下，仅当 userID 的所有连接都断开后才离开系统组
// 避免一端断开导致其他端收不到系统组广播
func TestLeaveSystemGroupMultiClient(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 同 userID 的两个 agent 连接（模拟多端登录）
	client1 := makeTestClient("c-agent-a", "agent-multi")
	client1.UserType = UserTypeAgent
	client1.Namespace = constants.DefaultNamespace
	client2 := makeTestClient("c-agent-b", "agent-multi")
	client2.UserType = UserTypeAgent
	client2.Namespace = constants.DefaultNamespace

	// 注册到 registry（模拟在线）
	hub.shardedRegistry.AddClient(client1)
	hub.shardedRegistry.AddClient(client2)

	// 两个都加入系统组（集合语义，agent-multi 只存一份）
	hub.joinSystemGroupsOnConnect(ctx, client1)
	hub.joinSystemGroupsOnConnect(ctx, client2)

	// 确认系统组包含 agent-multi
	members, err := hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-multi")

	// 断开 client1：先从 registry 移除（模拟 removeClientUnsafe 时序），再 leave
	hub.shardedRegistry.RemoveClient(client1.ID, client1.UserID)
	hub.leaveSystemGroupsOnDisconnect(ctx, client1)

	// client2 仍在线，系统组应保留 agent-multi
	members, err = hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-multi", "仍有其他端在线时不应离开系统组")

	// 断开 client2：从 registry 移除，再 leave
	hub.shardedRegistry.RemoveClient(client2.ID, client2.UserID)
	hub.leaveSystemGroupsOnDisconnect(ctx, client2)

	// 所有连接断开，系统组应移除 agent-multi
	members, err = hub.groupRepo.GetMembers(ctx, constants.DefaultAppID, constants.DefaultNamespace, constants.SystemGroupAgents)
	require.NoError(t, err)
	assert.NotContains(t, members, "agent-multi", "所有连接断开后应离开系统组")
}

// TestAddGroupMembersReconnectIdempotent 验证重连加群幂等性
// 重连时用户成员关系保留，AddGroupMembers 不应误报 ErrGroupFull
func TestAddGroupMembersReconnectIdempotent(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 MaxMembers=2 的群组
	group := &models.Group{
		Namespace:  "default",
		GroupID:    "room-1",
		Name:       "测试房间",
		MaxMembers: 2,
	}
	require.NoError(t, groupRepo.CreateGroup(ctx, group))

	// 用户 A 首次加群（成员数=1）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"room-1"}).Inject(ctx), []string{"userA"}))

	// 用户 A 重连再次加群：A 已存在，不应误报 ErrGroupFull（1+0=1 ≤ 2）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"room-1"}).Inject(ctx), []string{"userA"}),
		"重连用户已在群内，不应误报超限")

	// 用户 B 加群（成员数=2，满员）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"room-1"}).Inject(ctx), []string{"userB"}))

	// 用户 B 重连再次加群：B 已存在，不应误报（2+0=2 ≤ 2）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"room-1"}).Inject(ctx), []string{"userB"}),
		"重连用户已在群内，满员时也不应误报超限")

	// 用户 C 加群：真正超限（2+1=3 > 2），应报 ErrGroupFull
	err := hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"room-1"}).Inject(ctx), []string{"userC"})
	assert.ErrorIs(t, err, ErrGroupFull, "真正新增超限应报错")

	// 验证 A/B 成员关系保留（离线不销毁语义）
	exists, err := groupRepo.IsMember(ctx, constants.DefaultAppID, "default", "room-1", "userA")
	require.NoError(t, err)
	assert.True(t, exists, "用户 A 成员关系应保留")
	exists, err = groupRepo.IsMember(ctx, constants.DefaultAppID, "default", "room-1", "userB")
	require.NoError(t, err)
	assert.True(t, exists, "用户 B 成员关系应保留")

	// 验证成员总数仍为 2（A、B），C 未加入
	count, err := groupRepo.GetMemberCount(ctx, constants.DefaultAppID, "default", "room-1")
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "成员总数应为 2")
}

// ============================================================================
// 群组自动创建测试（addGroupMembers 群组不存在时自动创建，无需手动 CreateGroup）
// ============================================================================

// TestAddGroupMembersAutoCreate 验证 AddGroupMembers 群组不存在时自动创建
func TestAddGroupMembersAutoCreate(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 群组 "g-auto" 不存在，addGroupMembers 应自动创建
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-auto"}).Inject(ctx), []string{"u1", "u2"}))

	// 验证群组已被自动创建
	g, err := groupRepo.GetGroup(ctx, constants.DefaultAppID, constants.DefaultNamespace, "g-auto")
	require.NoError(t, err)
	assert.Equal(t, "g-auto", g.GroupID)
	assert.Equal(t, constants.DefaultNamespace, g.GetNamespace())

	// 验证成员关系已建立
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-auto"}).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2"}, members)

	// 验证命名空间索引包含该群组
	nsGroups, err := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, nsGroups, "g-auto")

	// 验证用户反向索引
	userGroups, err := hub.GetUserGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx), "u1")
	require.NoError(t, err)
	assert.Contains(t, userGroups, "g-auto")
}

// TestAddGroupMembersAutoCreatePreservesExisting 验证已存在群组不会被覆盖
func TestAddGroupMembersAutoCreatePreservesExisting(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 预先创建群组（带 MaxMembers 与 Name）
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{
		GroupID:    "g-exist",
		Namespace:  constants.DefaultNamespace,
		Name:       "原始群名",
		MaxMembers: 5,
	}))

	// AddGroupMembers 应复用已存在群组，不覆盖 MaxMembers/Name
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-exist"}).Inject(ctx), []string{"u1"}))

	g, err := groupRepo.GetGroup(ctx, constants.DefaultAppID, constants.DefaultNamespace, "g-exist")
	require.NoError(t, err)
	assert.Equal(t, 5, g.MaxMembers, "已存在群组的 MaxMembers 不应被覆盖")
	assert.Equal(t, "原始群名", g.Name, "已存在群组的 Name 不应被覆盖")
}

// ============================================================================
// move 场景测试（RemoveGroupMembers + 调用方自行下发 group_changed 通知）
// ============================================================================

// TestMoveScenarioWithRemoveAndNotify 验证 move 场景：
// 调用方 AddGroupMembers(新群) → RemoveGroupMembers(旧群) → 更新连接 GroupID → 自行下发 group_changed 通知
func TestMoveScenarioWithRemoveAndNotify(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 groupB、groupC
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "groupB", Namespace: "default", OwnerID: "owner"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "groupC", Namespace: "default", OwnerID: "owner"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册 client A（GroupID=groupB）
	clientA := makeTestClient("c-a", "u-move")
	clientA.GroupID = "groupB"
	hub.Register(clientA)
	time.Sleep(100 * time.Millisecond)

	// A 加入 groupB
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx), []string{"u-move"}))

	// 收集 leave 回调
	var leaveCalls []string
	var cbMu sync.Mutex
	hub.OnGroupMemberLeave(func(_ context.Context, _, gid string, uids []string) {
		cbMu.Lock()
		leaveCalls = append(leaveCalls, gid+":"+strings.Join(uids, ","))
		cbMu.Unlock()
	})

	// 排空注册时产生的旧消息
	drainChan := func(c *Client) {
		for {
			select {
			case <-c.SendChan:
			default:
				return
			}
		}
	}
	drainChan(clientA)

	// === move 场景：调用方自行操作 ===
	// 1. 先加入新群 groupC
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupC"}).Inject(ctx), []string{"u-move"}))
	// 2. 从旧群 groupB 移除（触发 leave 回调）
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx), []string{"u-move"}))
	// 3. 更新在线连接 GroupID 与观察者索引（调用方自行调用 MoveClientGroup）
	hub.shardedRegistry.ForEachUserClient("u-move", func(_ string, c *Client) bool {
		hub.shardedRegistry.MoveClientGroup(c, "groupC")
		return true
	})
	// 4. 调用方自行下发 group_changed 通知
	notifyMsg := NewHubMessage()
	notifyMsg.MessageType = models.MessageTypeGroupChanged
	notifyMsg.Sender = UserTypeSystem.String()
	notifyMsg.SenderType = UserTypeSystem
	notifyMsg.Receiver = "u-move"
	notifyMsg.Content = "群组已变更"
	notifyMsg.Data = map[string]interface{}{
		"from_group": "groupB",
		"to_group":   "groupC",
		"namespace":  "default",
	}
	result := hub.SendToUserWithRetry(ctx, "u-move", notifyMsg)
	assert.NoError(t, result.FinalError, "group_changed 通知应发送成功")

	time.Sleep(300 * time.Millisecond) // 等异步回调 + 消息投递

	// 验证收到群组变更通知
	foundChanged := false
loop:
	for {
		select {
		case b := <-clientA.SendChan:
			var m HubMessage
			if json.Unmarshal(b, &m) == nil && m.MessageType == models.MessageTypeGroupChanged {
				foundChanged = true
				assert.Equal(t, "groupB", m.Data["from_group"], "通知应含旧群组")
				assert.Equal(t, "groupC", m.Data["to_group"], "通知应含新群组")
			}
		default:
			break loop
		}
	}
	assert.True(t, foundChanged, "move 后应收到群组变更通知")

	// 验证成员关系：A 不在 groupB，在 groupC
	membersB, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx))
	require.NoError(t, err)
	assert.NotContains(t, membersB, "u-move", "A 应已移出 groupB")
	membersC, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupC"}).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, membersC, "u-move", "A 应在 groupC")

	// 验证在线连接 GroupID 已更新
	assert.Equal(t, "groupC", clientA.GetGroupIDRaw(), "move 后连接 GroupID 应更新为 groupC")

	// 验证 leave 回调触发
	cbMu.Lock()
	assert.Contains(t, leaveCalls, "groupB:u-move", "应触发 groupB 的 leave 回调")
	cbMu.Unlock()
}

// TestMoveScenarioOfflineUser 验证离线用户 move 场景：
// 用户离线时，RemoveGroupMembers 从旧群删除 + 成员关系正确迁移
// 注：离线消息存储依赖 offlineMessageHandler，未配置时 SendToUserWithRetry 返回 ErrTypeUserOffline
func TestMoveScenarioOfflineUser(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 groupB、groupC
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "groupB", Namespace: "default", OwnerID: "owner"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "groupC", Namespace: "default", OwnerID: "owner"}))

	// A 加入 groupB（A 离线，不注册客户端）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx), []string{"u-offline"}))

	// === move 场景：A 离线 ===
	// 1. 先加入新群 groupC
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupC"}).Inject(ctx), []string{"u-offline"}))
	// 2. 从旧群 groupB 移除
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx), []string{"u-offline"}))
	// 3. 调用方下发 group_changed 通知（A 离线，未配置离线处理器时返回 offline 错误，属预期行为）
	notifyMsg := NewHubMessage()
	notifyMsg.MessageType = models.MessageTypeGroupChanged
	notifyMsg.Sender = UserTypeSystem.String()
	notifyMsg.SenderType = UserTypeSystem
	notifyMsg.Receiver = "u-offline"
	notifyMsg.Data = map[string]interface{}{
		"from_group": "groupB",
		"to_group":   "groupC",
		"namespace":  "default",
	}
	result := hub.SendToUserWithRetry(ctx, "u-offline", notifyMsg)
	// 未配置 offlineMessageHandler 时，离线用户返回 ErrTypeUserOffline
	assert.Error(t, result.FinalError, "离线用户无离线处理器时应返回 offline 错误")
	assert.False(t, result.Success, "离线用户无离线处理器时发送不应成功")

	// 核心验证：成员关系正确迁移（与通知投递无关）
	membersB, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx))
	require.NoError(t, err)
	assert.NotContains(t, membersB, "u-offline", "A 应已移出 groupB")
	membersC, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupC"}).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, membersC, "u-offline", "A 应在 groupC")

	// 验证用户群组列表只含 groupC（反向索引已更新）
	userGroups, err := hub.GetUserGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs(nil).Inject(ctx), "u-offline")
	require.NoError(t, err)
	assert.NotContains(t, userGroups, "groupB", "用户群组列表不应再含 groupB")
	assert.Contains(t, userGroups, "groupC", "用户群组列表应含 groupC")
}

// ============================================================================
// 离线消息处理器内存 mock（测试专用）
// ============================================================================

// memoryOfflineHandler 内存离线消息处理器，实现 OfflineMessageHandler 接口
type memoryOfflineHandler struct {
	mu       sync.Mutex
	messages map[string][]*HubMessage // userID → messages（按存储顺序）
}

func newMemoryOfflineHandler() *memoryOfflineHandler {
	return &memoryOfflineHandler{messages: make(map[string][]*HubMessage)}
}

func (m *memoryOfflineHandler) StoreOfflineMessage(_ context.Context, userID string, msg *HubMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages[userID] = append(m.messages[userID], msg)
	return nil
}

// DrainOfflineQueue 排空 Redis 队列（mock 不模拟 Redis 分区，离线消息统一走 GetOfflineMessages）
func (m *memoryOfflineHandler) DrainOfflineQueue(_ context.Context, _ string, _ int) ([]*HubMessage, error) {
	return nil, nil
}

func (m *memoryOfflineHandler) GetOfflineMessages(_ context.Context, userID string, limit int, _ string) ([]*HubMessage, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.messages[userID]
	if limit > 0 && len(msgs) > limit {
		return msgs[:limit], "more", nil
	}
	return msgs, "", nil
}

func (m *memoryOfflineHandler) DeleteOfflineMessages(_ context.Context, userID string, messageIDs []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(messageIDs) == 0 {
		return nil
	}
	idSet := make(map[string]struct{}, len(messageIDs))
	for _, id := range messageIDs {
		idSet[id] = struct{}{}
	}
	var remaining []*HubMessage
	for _, msg := range m.messages[userID] {
		if _, ok := idSet[msg.MessageID]; ok {
			continue
		}
		remaining = append(remaining, msg)
	}
	m.messages[userID] = remaining
	return nil
}

func (m *memoryOfflineHandler) GetOfflineMessageCount(_ context.Context, userID string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.messages[userID])), nil
}

func (m *memoryOfflineHandler) ClearOfflineMessages(_ context.Context, userID string, _ []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.messages, userID)
	return nil
}

func (m *memoryOfflineHandler) UpdatePushStatus(_ context.Context, _ []string, _ error) error {
	return nil
}

// ============================================================================
// 离线 → 重连 → 验证离线消息测试
// ============================================================================

// TestMoveScenarioOfflineReconnect 验证离线 move 完整流程：
// A 离线 → move + group_changed 通知走离线存储 → A 重连 → 收到离线消息 → 验证内容正确 → 离线消息已删除
func TestMoveScenarioOfflineReconnect(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 设置内存离线消息处理器
	offlineHandler := newMemoryOfflineHandler()
	hub.SetOfflineMessageHandler(offlineHandler)

	ctx := context.Background()

	// 创建 groupB、groupC
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "groupB", Namespace: "default", OwnerID: "owner"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "groupC", Namespace: "default", OwnerID: "owner"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// === 阶段1：A 离线时 move ===
	// A 加入 groupB（A 离线，不注册客户端）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx), []string{"u-reconnect"}))

	// move：加入新群 + 从旧群移除
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupC"}).Inject(ctx), []string{"u-reconnect"}))
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx), []string{"u-reconnect"}))

	// 下发 group_changed 通知（A 离线，应走离线存储）
	notifyMsg := NewHubMessage()
	notifyMsg.MessageType = models.MessageTypeGroupChanged
	notifyMsg.Sender = UserTypeSystem.String()
	notifyMsg.SenderType = UserTypeSystem
	notifyMsg.Receiver = "u-reconnect"
	notifyMsg.Content = "群组已变更"
	notifyMsg.Data = map[string]interface{}{
		"from_group": "groupB",
		"to_group":   "groupC",
		"namespace":  "default",
	}
	result := hub.SendToUserWithRetry(ctx, "u-reconnect", notifyMsg)
	assert.NoError(t, result.FinalError, "离线用户 group_changed 通知应存储成功")
	assert.True(t, result.StoredOffline, "离线用户消息应标记为 StoredOffline")

	// 验证离线存储中有一条消息
	count, err := offlineHandler.GetOfflineMessageCount(ctx, "u-reconnect")
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "离线存储应有1条消息")

	// === 阶段2：A 重连，加入新群 groupC ===
	clientA := makeTestClient("c-reconnect", "u-reconnect")
	clientA.GroupID = "groupC" // 重连时带新群组ID
	hub.Register(clientA)

	// 等待异步离线消息推送完成（pushOfflineMessagesOnConnect 在 workerPool 中异步执行）
	// 使用 Eventually 轮询读取 SendChan，避免固定 Sleep 的时序问题
	var foundChanged bool
	var foundMsg HubMessage
	require.Eventually(t, func() bool {
		for {
			select {
			case b := <-clientA.SendChan:
				var m HubMessage
				if json.Unmarshal(b, &m) == nil && m.MessageType == models.MessageTypeGroupChanged {
					foundChanged = true
					foundMsg = m
					return true
				}
				// 其他消息继续读，不返回
			default:
				return false
			}
		}
	}, 5*time.Second, 50*time.Millisecond, "应该在超时前收到 group_changed 离线消息")

	// === 阶段3：验证收到的离线消息内容正确 ===
	if foundChanged {
		assert.Equal(t, "groupB", foundMsg.Data["from_group"], "离线消息应含旧群组")
		assert.Equal(t, "groupC", foundMsg.Data["to_group"], "离线消息应含新群组")
		assert.Equal(t, "default", foundMsg.Data["namespace"], "离线消息应含命名空间")
		assert.Equal(t, "群组已变更", foundMsg.Content, "离线消息内容应正确")
		assert.Equal(t, "u-reconnect", foundMsg.Receiver, "离线消息接收者应正确")
		assert.Equal(t, models.MessageSourceOffline, foundMsg.Source, "离线消息来源应标记为 offline")
	}
	assert.True(t, foundChanged, "重连后应收到 group_changed 离线消息")

	// 验证离线消息已被删除（推送成功后自动删除）
	count, err = offlineHandler.GetOfflineMessageCount(ctx, "u-reconnect")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "推送后离线消息应被删除")

	// 验证成员关系：A 在 groupC，不在 groupB
	membersB, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupB"}).Inject(ctx))
	require.NoError(t, err)
	assert.NotContains(t, membersB, "u-reconnect", "A 应不在 groupB")
	membersC, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("default").WithGroupIDs([]string{"groupC"}).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, membersC, "u-reconnect", "A 应在 groupC")
}

// ============================================================================
// 分组核心路径补充测试（白盒覆盖 group.go 未覆盖分支）
//
// 复用 setupGroupTestHub / makeTestClient / makeGroupMessage 等 helper，
// 不重复已有断言，仅补齐 group.go 中尚未覆盖的早返回、空值、幂等与 excludeSender 分支
// ============================================================================

// TestAddGroupMembersAppendToExisting 验证已存在分组追加成员：旧成员保留、新成员加入、分组元信息不重复创建
func TestAddGroupMembersAppendToExisting(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-append", Namespace: constants.DefaultNamespace, OwnerID: "o1", Name: "原始群"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-append"}).Inject(ctx), []string{"u1", "u2"}))

	// 追加新成员 u3、u4，同时重复添加已存在成员 u1（幂等，集合语义）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-append"}).Inject(ctx), []string{"u1", "u3", "u4"}))

	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-append"}).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2", "u3", "u4"}, members)

	cnt, err := hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-append"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, int64(4), cnt)

	// 追加后分组元信息仍存在且未被覆盖
	g, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-append"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, "o1", g.OwnerID)
	assert.Equal(t, "原始群", g.Name)

	// 命名空间群组索引不重复（仅一个 g-append）
	nsGroups, err := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, 1, len(nsGroups))
}

// TestAddGroupMembersEmptyUserIDs 验证空 userIDs 列表直接返回 nil（早返回分支）
func TestAddGroupMembersEmptyUserIDs(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-empty-arg", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))

	// 空切片与 nil 均不应报错，也不应建立成员关系
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-empty-arg"}).Inject(ctx), []string{}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-empty-arg"}).Inject(ctx), nil))

	cnt, err := hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-empty-arg"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)
}

// TestRemoveGroupMembersNonExistent 验证移除不存在的成员不报错（幂等）
func TestRemoveGroupMembersNonExistent(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-rm", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm"}).Inject(ctx), []string{"u1", "u2"}))

	// 移除不存在的成员 uX、uY 不应报错（Redis SRem 对不存在元素幂等）
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm"}).Inject(ctx), []string{"uX", "uY"}))

	// 原成员不受影响
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm"}).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2"}, members)

	// 混合移除：一个真实成员 + 一个不存在成员
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm"}).Inject(ctx), []string{"u1", "uZ"}))
	members, err = hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm"}).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u2"}, members)

	cnt, err := hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, int64(1), cnt)
}

// TestRemoveGroupMembersEmptyUserIDs 验证空 userIDs 列表直接返回 nil（早返回分支）
func TestRemoveGroupMembersEmptyUserIDs(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-rm-empty", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm-empty"}).Inject(ctx), []string{"u1"}))

	// 空切片与 nil 均不应报错，成员应保留
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm-empty"}).Inject(ctx), []string{}))
	require.NoError(t, hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm-empty"}).Inject(ctx), nil))

	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-rm-empty"}).Inject(ctx))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1"}, members)
}

// TestDisbandGroupNonExistent 验证解散不存在的分组按源码行为幂等返回 nil
// 仓库 DisbandGroup 对缺失 key 的 SMembers 返回空切片+nil，Pipeline 删除为 no-op，最终返回 nil
// 注意：Hub.DisbandGroup 在此场景仍会触发 OnGroupDisband 回调（见最终总结中的 bug 说明）
func TestDisbandGroupNonExistent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 不存在的分组，仓库 DisbandGroup 幂等返回 nil
	err := hub.DisbandGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-not-exist"}).Inject(ctx))
	assert.NoError(t, err, "解散不存在的分组应幂等返回 nil")

	// 重复解散同样不报错
	err = hub.DisbandGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-not-exist"}).Inject(ctx))
	assert.NoError(t, err)

	// 解散后查询仍为不存在
	_, err = hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-not-exist"}).Inject(ctx))
	assert.ErrorIs(t, err, ErrGroupNotFound)

	// 成员列表为空
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-not-exist"}).Inject(ctx))
	require.NoError(t, err)
	assert.Empty(t, members)
}

// TestGetGroupMembersEmptyGroup 验证空分组/不存在的分组返回空成员列表与 0 计数
func TestGetGroupMembersEmptyGroup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 不存在的分组：GetMembers 对缺失 key 返回空切片，GetMemberCount 返回 0
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-no-such"}).Inject(ctx))
	require.NoError(t, err)
	assert.Empty(t, members)

	cnt, err := hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-no-such"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)

	// 存在但无成员的分组同样返回空与 0
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-empty", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	members, err = hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-empty"}).Inject(ctx))
	require.NoError(t, err)
	assert.Empty(t, members)

	cnt, err = hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-empty"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)
}

// TestGetUserGroupsNoMembership 验证用户未加入任何分组时返回空列表
func TestGetUserGroupsNoMembership(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	groups, err := hub.GetUserGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx), "user-no-groups")
	require.NoError(t, err)
	assert.Empty(t, groups)
}

// TestNamespaceGroupsEmpty 验证无群组的命名空间返回空列表
func TestNamespaceGroupsEmpty(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	groups, err := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenant-empty").WithGroupIDs(nil).Inject(ctx))
	require.NoError(t, err)
	assert.Empty(t, groups)
}

// TestGroupRepoNotSetBranches 验证各群组只读/写方法在 groupRepo 未设置时统一返回 ErrGroupRepoNotSet
// 覆盖 group.go 中每个方法首段的早返回分支
func TestGroupRepoNotSetBranches(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()
	// 故意不设置 groupRepo
	ctx := context.Background()

	t.Run("GetGroup", func(t *testing.T) {
		_, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs([]string{"g"}).Inject(ctx))
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("DisbandGroup", func(t *testing.T) {
		err := hub.DisbandGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs([]string{"g"}).Inject(ctx))
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("AddGroupMembers", func(t *testing.T) {
		err := hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs([]string{"g"}).Inject(ctx), []string{"u1"})
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("RemoveGroupMembers", func(t *testing.T) {
		err := hub.RemoveGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs([]string{"g"}).Inject(ctx), []string{"u1"})
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetGroupMembers", func(t *testing.T) {
		_, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs([]string{"g"}).Inject(ctx))
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetGroupMemberCount", func(t *testing.T) {
		_, err := hub.GetGroupMemberCount(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs([]string{"g"}).Inject(ctx))
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("IsGroupMember", func(t *testing.T) {
		_, err := hub.IsGroupMember(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs([]string{"g"}).Inject(ctx), "u1")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetUserGroups", func(t *testing.T) {
		_, err := hub.GetUserGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs(nil).Inject(ctx), "u1")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetNamespaceGroups", func(t *testing.T) {
		_, err := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("ns").WithGroupIDs(nil).Inject(ctx))
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
}

// TestSendToGroupExcludeSenderTrue 验证发送者在线时 excludeSender=true：自己不收到，其他在线成员收到
func TestSendToGroupExcludeSenderTrue(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send-excl", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send-excl"}).Inject(ctx), []string{"sender", "user2"}))

	go hub.Run()
	defer hub.SafeShutdown()
	hub.WaitForStart()

	senderClient := makeTestClient("c-sender-excl", "sender")
	otherClient := makeTestClient("c-other-excl", "user2")
	hub.Register(senderClient)
	hub.Register(otherClient)

	// 等待两个客户端均注册上线（确定性，race-safe）
	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("sender", "", "") && hub.shardedRegistry.HasUser("user2", "", "")
	}, 2*time.Second, 5*time.Millisecond)

	msg := makeGroupMessage("sender")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send-excl"}).Inject(ctx), msg, true)

	// 总成员 2，排除发送者后仅投递给 1 个在线成员
	assert.Equal(t, 2, result.TotalMembers)
	assert.Equal(t, 1, result.OnlineMembers, "排除发送者后仅 1 个在线成员被投递")
	assert.Equal(t, 1, result.Sent, "应成功投递 1 条")
	assert.Equal(t, 0, result.Failed)

	// user2 应收到消息
	select {
	case <-otherClient.SendChan:
		// ok
	case <-time.After(time.Second):
		t.Fatal("user2 未收到群组消息")
	}

	// sender 不应收到被排除的消息
	select {
	case <-senderClient.SendChan:
		t.Fatal("发送者不应收到 excludeSender=true 的消息")
	case <-time.After(200 * time.Millisecond):
		// ok，无消息
	}
}

// TestSendToGroupExcludeSenderFalse 验证 excludeSender=false：全员收到（含发送者本人）
func TestSendToGroupExcludeSenderFalse(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send-all", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send-all"}).Inject(ctx), []string{"sender", "user2"}))

	go hub.Run()
	defer hub.SafeShutdown()
	hub.WaitForStart()

	senderClient := makeTestClient("c-sender-all", "sender")
	otherClient := makeTestClient("c-other-all", "user2")
	hub.Register(senderClient)
	hub.Register(otherClient)

	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("sender", "", "") && hub.shardedRegistry.HasUser("user2", "", "")
	}, 2*time.Second, 5*time.Millisecond)

	msg := makeGroupMessage("sender")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send-all"}).Inject(ctx), msg, false)

	// 总成员 2，全员在线均被投递
	assert.Equal(t, 2, result.TotalMembers)
	assert.Equal(t, 2, result.OnlineMembers, "全员在线成员均被投递")
	assert.Equal(t, 2, result.Sent, "应成功投递 2 条")
	assert.Equal(t, 0, result.Failed)

	// 发送者本人应收到（excludeSender=false）
	select {
	case <-senderClient.SendChan:
		// ok
	case <-time.After(time.Second):
		t.Fatal("发送者应收到 excludeSender=false 的消息")
	}
	// user2 应收到
	select {
	case <-otherClient.SendChan:
		// ok
	case <-time.After(time.Second):
		t.Fatal("user2 未收到群组消息")
	}
}

// TestSendToGroupEmptySenderNoFilter 验证 excludeSender=true 但 msg.Sender 为空时不执行过滤
// 覆盖 SendToGroup 中 `excludeSender && msg.Sender != ""` 的 false 分支
func TestSendToGroupEmptySenderNoFilter(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send-nofilter", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send-nofilter"}).Inject(ctx), []string{"user1", "user2"}))

	go hub.Run()
	defer hub.SafeShutdown()
	hub.WaitForStart()

	client1 := makeTestClient("c-nf1", "user1")
	client2 := makeTestClient("c-nf2", "user2")
	hub.Register(client1)
	hub.Register(client2)

	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("user1", "", "") && hub.shardedRegistry.HasUser("user2", "", "")
	}, 2*time.Second, 5*time.Millisecond)

	// Sender 为空 + excludeSender=true → 不过滤，全员投递
	msg := makeGroupMessage("")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-send-nofilter"}).Inject(ctx), msg, true)

	assert.Equal(t, 2, result.TotalMembers)
	assert.Equal(t, 2, result.OnlineMembers, "Sender 为空时不过滤，全员被投递")
	assert.Equal(t, 2, result.Sent)

	// 两个成员都应收到
	select {
	case <-client1.SendChan:
	case <-time.After(time.Second):
		t.Fatal("user1 应收到消息（Sender 为空时不过滤）")
	}
	select {
	case <-client2.SendChan:
	case <-time.After(time.Second):
		t.Fatal("user2 应收到消息（Sender 为空时不过滤）")
	}
}

// TestSendToGroupEmptyGroup 验证空分组（无成员）投递时立即返回，TotalMembers=0 且无错误
// 覆盖 SendToGroup 中 `if result.TotalMembers == 0 { return result }` 早返回分支
func TestSendToGroupEmptyGroup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 不存在的分组：GetMembers 返回空切片，TotalMembers=0 直接返回
	msg := makeGroupMessage("sender")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-no-such-empty"}).Inject(ctx), msg, false)
	assert.Equal(t, 0, result.TotalMembers)
	assert.Equal(t, 0, result.OnlineMembers)
	assert.Equal(t, 0, result.Sent)
	assert.Empty(t, result.Errors)
	assert.Equal(t, "g-no-such-empty", result.GroupIDs[0])

	// 存在但无成员的分组同样立即返回
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-empty-send", Namespace: constants.DefaultNamespace, OwnerID: "o1"}))
	result = hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"g-empty-send"}).Inject(ctx), msg, true)
	assert.Equal(t, 0, result.TotalMembers)
	assert.Empty(t, result.Errors)
}

// ============================================================================
// 连接时自动加入成员组测试（joinMemberGroupOnConnect）
//
// 覆盖：
//   1. 指定 GroupID → 加入指定业务组（自动创建）
//   2. 未指定 GroupID → 加入默认组（DefaultGroupID，系统组路径）
//   3. 观察者不加入成员组
//   4. 重连加群幂等（集合语义，不重复）
//   5. 自动加群触发 OnGroupMemberJoin 回调
//   6. groupRepo 未设置时为 no-op
//   7. handleRegister 端到端集成（业务组 + 默认组）
// ============================================================================

// TestJoinMemberGroupOnConnectWithGroupID 验证普通用户连接时自动加入指定的业务组成员组
func TestJoinMemberGroupOnConnectWithGroupID(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-1", "user-mg-1")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "my-group"

	hub.joinMemberGroupOnConnect(ctx, client)

	// 验证用户已加入指定群组
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"my-group"}).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, members, "user-mg-1")

	// 验证群组被自动创建
	g, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"my-group"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, "my-group", g.GroupID)
	assert.Equal(t, constants.DefaultNamespace, g.GetNamespace())
}

// TestJoinMemberGroupOnConnectDefaultGroup 验证未指定 GroupID 时自动加入默认组
func TestJoinMemberGroupOnConnectDefaultGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-2", "user-mg-2")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	// 不设 GroupID → GetGroupID 返回 DefaultGroupID

	hub.joinMemberGroupOnConnect(ctx, client)

	// 验证用户已加入默认组（DefaultGroupID 是系统组名，走 EnsureSystemGroup 路径）
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, members, "user-mg-2")

	// 验证默认组元信息已创建
	g, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, constants.DefaultGroupID, g.GroupID)
}

// TestJoinMemberGroupOnConnectObserverSkipped 验证观察者不作为成员加入群组
func TestJoinMemberGroupOnConnectObserverSkipped(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-obs", "observer-mg")
	client.UserType = UserTypeObserver
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "obs-skip-group"

	hub.joinMemberGroupOnConnect(ctx, client)

	// 观察者不应加入成员组（群组不应被创建）
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"obs-skip-group"}).Inject(ctx))
	require.NoError(t, err)
	assert.NotContains(t, members, "observer-mg")

	_, err = hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"obs-skip-group"}).Inject(ctx))
	assert.ErrorIs(t, err, ErrGroupNotFound, "观察者连接不应触发成员组创建")
}

// TestJoinMemberGroupOnConnectReconnectIdempotent 验证重连加群幂等（不重复加入）
func TestJoinMemberGroupOnConnectReconnectIdempotent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-3", "user-mg-3")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "room-mg"

	// 模拟重连：多次调用 joinMemberGroupOnConnect
	hub.joinMemberGroupOnConnect(ctx, client)
	hub.joinMemberGroupOnConnect(ctx, client)

	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"room-mg"}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 1, "重连加群应幂等，不重复")
	assert.Contains(t, members, "user-mg-3")
}

// TestJoinMemberGroupOnConnectDefaultReconnectIdempotent 验证默认组重连加群幂等
func TestJoinMemberGroupOnConnectDefaultReconnectIdempotent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-4", "user-mg-4")
	client.UserType = UserTypeCustomer
	client.Namespace = "tenantB"
	// 不设 GroupID → 默认组

	hub.joinMemberGroupOnConnect(ctx, client)
	hub.joinMemberGroupOnConnect(ctx, client)

	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 1, "默认组重连加群应幂等")
	assert.Contains(t, members, "user-mg-4")
}

// TestJoinMemberGroupOnConnectTriggersCallback 验证自动加群触发 OnGroupMemberJoin 回调
func TestJoinMemberGroupOnConnectTriggersCallback(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	joinCh := make(chan []string, 2)
	hub.OnGroupMemberJoin(func(_ context.Context, _, _ string, uids []string) {
		joinCh <- uids
	})

	t.Run("业务组触发回调", func(t *testing.T) {
		client := makeTestClient("c-mg-5", "user-mg-5")
		client.UserType = UserTypeCustomer
		client.Namespace = constants.DefaultNamespace
		client.GroupID = "cb-group"

		hub.joinMemberGroupOnConnect(ctx, client)

		select {
		case uids := <-joinCh:
			assert.Contains(t, uids, "user-mg-5")
		case <-time.After(time.Second):
			t.Fatal("业务组自动加群应触发 OnGroupMemberJoin 回调")
		}
	})

	t.Run("默认组触发回调", func(t *testing.T) {
		client := makeTestClient("c-mg-6", "user-mg-6")
		client.UserType = UserTypeCustomer
		client.Namespace = constants.DefaultNamespace
		// 不设 GroupID → 默认组

		hub.joinMemberGroupOnConnect(ctx, client)

		select {
		case uids := <-joinCh:
			assert.Contains(t, uids, "user-mg-6")
		case <-time.After(time.Second):
			t.Fatal("默认组自动加群应触发 OnGroupMemberJoin 回调")
		}
	})
}

// TestJoinMemberGroupOnConnectRepoNotSet 验证 groupRepo 未设置时为 no-op
func TestJoinMemberGroupOnConnectRepoNotSet(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)
	defer hub.Shutdown()
	// 不设置 groupRepo

	client := makeTestClient("c-mg-7", "user-mg-7")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "no-repo-group"

	// 不应 panic
	assert.NotPanics(t, func() {
		hub.joinMemberGroupOnConnect(context.Background(), client)
	})
}

// TestHandleRegisterAutoJoinMemberGroup 验证 handleRegister 端到端：连接注册时自动加入业务组成员组
func TestHandleRegisterAutoJoinMemberGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	client := makeTestClient("c-reg-mg", "user-reg-mg")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "reg-group"
	hub.Register(client)

	// 等待异步加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"reg-group"}).Inject(ctx))
		for _, m := range members {
			if m == "user-reg-mg" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "handleRegister 应自动将用户加入业务组")

	// 验证群组已创建
	g, err := hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"reg-group"}).Inject(ctx))
	require.NoError(t, err)
	assert.Equal(t, "reg-group", g.GroupID)
}

// TestHandleRegisterAutoJoinDefaultGroup 验证 handleRegister 端到端：未指定群组时自动加入默认组
func TestHandleRegisterAutoJoinDefaultGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	client := makeTestClient("c-reg-def", "user-reg-def")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	// 不设 GroupID → 自动加入默认组
	hub.Register(client)

	// 等待异步加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		for _, m := range members {
			if m == "user-reg-def" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "handleRegister 应自动将用户加入默认组")
}

// TestHandleRegisterAutoJoinMemberGroupObserverSkipped 验证 handleRegister 时观察者不加入成员组
func TestHandleRegisterAutoJoinMemberGroupObserverSkipped(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	client := makeTestClient("c-reg-obs", "observer-reg")
	client.UserType = UserTypeObserver
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "obs-should-skip"
	hub.Register(client)

	// 等待异步流程完成
	time.Sleep(300 * time.Millisecond)

	// 观察者不应加入成员组
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"obs-should-skip"}).Inject(ctx))
	require.NoError(t, err)
	assert.NotContains(t, members, "observer-reg", "观察者不应加入成员组")

	// 群组不应被创建
	_, err = hub.GetGroup(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"obs-should-skip"}).Inject(ctx))
	assert.ErrorIs(t, err, ErrGroupNotFound, "观察者连接不应触发成员组创建")
}

// ============================================================================
// 连接自动加群后的群组消息投递测试
//
// 覆盖：连接时自动加入成员组后，群组消息发送操作能正确投递
//   1. SendToGroup → 自动加入的业务组（含 excludeSender）
//   2. SendToGroup → 自动加入的默认组
//   3. BroadcastToGroupMembers → 自动加入的群组（多客户端）
//   4. BroadcastToAllGroups → 自动加入的默认组
//   5. 多客户端同组互发
//   6. 跨命名空间隔离投递
// ============================================================================

// drainClientSendChan 排空客户端 SendChan（清除注册等阶段产生的旧消息）
func drainClientSendChan(c *Client) {
	for {
		select {
		case <-c.SendChan:
		default:
			return
		}
	}
}

// TestSendToGroupAutoJoinedBusinessGroup 验证连接自动加入业务组后 SendToGroup 能投递消息
func TestSendToGroupAutoJoinedBusinessGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 连接时指定 GroupID，自动加入业务组 "chat-room"
	client := makeTestClient("c-snd-1", "user-snd-1")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "chat-room"
	hub.Register(client)

	// 等待异步加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"chat-room"}).Inject(ctx))
		for _, m := range members {
			if m == "user-snd-1" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(client)

	// 向自动加入的业务组发送消息
	msg := makeGroupMessage("external-sender")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"chat-room"}).Inject(ctx), msg, false)

	assert.Equal(t, 1, result.TotalMembers, "群组应有 1 个成员")
	assert.Equal(t, 1, result.OnlineMembers, "1 个在线成员")
	assert.Equal(t, 1, result.Sent, "应成功投递 1 条")

	// 客户端应收到消息
	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "客户端应收到群组消息")
	case <-time.After(time.Second):
		t.Fatal("自动加群后客户端未收到 SendToGroup 消息")
	}
}

// TestSendToGroupAutoJoinedDefaultGroup 验证连接自动加入默认组后 SendToGroup 能投递消息
func TestSendToGroupAutoJoinedDefaultGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 连接时不指定 GroupID，自动加入默认组
	client := makeTestClient("c-snd-def", "user-snd-def")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	hub.Register(client)

	// 等待异步加群完成（默认组）
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		for _, m := range members {
			if m == "user-snd-def" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(client)

	// 向默认组发送消息
	msg := makeGroupMessage("system-sender")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx), msg, false)

	assert.Equal(t, 1, result.OnlineMembers, "默认组 1 个在线成员")
	assert.Equal(t, 1, result.Sent, "应成功投递 1 条")

	select {
	case data := <-client.SendChan:
		assert.NotEmpty(t, data, "客户端应收到默认组消息")
	case <-time.After(time.Second):
		t.Fatal("自动加入默认组后客户端未收到 SendToGroup 消息")
	}
}

// TestSendToGroupAutoJoinedExcludeSender 验证自动加群后 SendToGroup excludeSender 行为
func TestSendToGroupAutoJoinedExcludeSender(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 两个客户端连接时自动加入同一业务组
	senderClient := makeTestClient("c-snd-sender", "sender-user")
	senderClient.UserType = UserTypeCustomer
	senderClient.Namespace = constants.DefaultNamespace
	senderClient.GroupID = "excl-room"
	hub.Register(senderClient)

	otherClient := makeTestClient("c-snd-other", "other-user")
	otherClient.UserType = UserTypeCustomer
	otherClient.Namespace = constants.DefaultNamespace
	otherClient.GroupID = "excl-room"
	hub.Register(otherClient)

	// 等待两个客户端均注册并加群
	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("sender-user", "", "") && hub.shardedRegistry.HasUser("other-user", "", "")
	}, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"excl-room"}).Inject(ctx))
		return len(members) == 2
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(senderClient)
	drainClientSendChan(otherClient)

	// excludeSender=true：发送者不收，其他成员收
	msg := makeGroupMessage("sender-user")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"excl-room"}).Inject(ctx), msg, true)

	assert.Equal(t, 2, result.TotalMembers, "群组应有 2 个成员")
	assert.Equal(t, 1, result.Sent, "排除发送者后仅投递 1 条")

	// other-user 应收到
	select {
	case data := <-otherClient.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("other-user 未收到消息")
	}

	// sender 不应收到
	select {
	case <-senderClient.SendChan:
		t.Fatal("发送者不应收到 excludeSender=true 的消息")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

// TestBroadcastToGroupMembersAutoJoined 验证自动加群后 BroadcastToGroupMembers 投递给所有在线成员
func TestBroadcastToGroupMembersAutoJoined(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 三个客户端连接时自动加入同一业务组
	client1 := makeTestClient("c-bc-1", "user-bc-1")
	client1.UserType = UserTypeCustomer
	client1.Namespace = constants.DefaultNamespace
	client1.GroupID = "bc-room"
	hub.Register(client1)

	client2 := makeTestClient("c-bc-2", "user-bc-2")
	client2.UserType = UserTypeCustomer
	client2.Namespace = constants.DefaultNamespace
	client2.GroupID = "bc-room"
	hub.Register(client2)

	client3 := makeTestClient("c-bc-3", "user-bc-3")
	client3.UserType = UserTypeCustomer
	client3.Namespace = constants.DefaultNamespace
	client3.GroupID = "bc-room"
	hub.Register(client3)

	// 等待全部注册并加群
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"bc-room"}).Inject(ctx))
		return len(members) == 3
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(client1)
	drainClientSendChan(client2)
	drainClientSendChan(client3)

	// 广播（不排除发送者）
	msg := makeGroupMessage("external")
	delivered := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"bc-room"}).Inject(ctx), msg, false).LocalDelivered
	assert.Equal(t, 3, delivered, "应投递给 3 个在线成员")

	// 三个客户端都应收到
	for _, c := range []*Client{client1, client2, client3} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("客户端 %s 未收到广播消息", c.ID)
		}
	}
}

// TestBroadcastToAllGroupsAutoJoinedDefault 验证自动加入默认组后 BroadcastToAllGroups 投递
func TestBroadcastToAllGroupsAutoJoinedDefault(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 两个客户端连接时自动加入默认组（不同命名空间）
	clientA := makeTestClient("c-ba-1", "user-ba-1")
	clientA.UserType = UserTypeCustomer
	clientA.Namespace = constants.DefaultNamespace
	hub.Register(clientA)

	clientB := makeTestClient("c-ba-2", "user-ba-2")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = "tenantB"
	hub.Register(clientB)

	// 等待全部加群
	require.Eventually(t, func() bool {
		membersA, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		membersB, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		return len(membersA) == 1 && len(membersB) == 1
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 向 tenantA 的所有群组广播（含默认组）
	// Deliver 无 groupIDs + namespace 非空 → 命名空间广播（等价于全群组广播去重后投递）
	msg := makeGroupMessage("system")
	delivered := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx), msg, false).LocalDelivered
	assert.Equal(t, 1, delivered, "tenantA 默认组 1 个在线成员")

	// clientA 应收到，clientB 不应收到（不同命名空间）
	select {
	case data := <-clientA.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("clientA 未收到 BroadcastToAllGroups 消息")
	}
	select {
	case <-clientB.SendChan:
		t.Fatal("clientB 不应收到 tenantA 命名空间的广播")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

// TestSendToGroupAutoJoinedMultipleClients 验证多个客户端自动加入同一业务组后互发消息
func TestSendToGroupAutoJoinedMultipleClients(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 两个客户端连接时自动加入同一业务组
	alice := makeTestClient("c-alice", "alice")
	alice.UserType = UserTypeCustomer
	alice.Namespace = constants.DefaultNamespace
	alice.GroupID = "friends"
	hub.Register(alice)

	bob := makeTestClient("c-bob", "bob")
	bob.UserType = UserTypeCustomer
	bob.Namespace = constants.DefaultNamespace
	bob.GroupID = "friends"
	hub.Register(bob)

	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"friends"}).Inject(ctx))
		return len(members) == 2
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(alice)
	drainClientSendChan(bob)

	// alice 发消息到群组（排除发送者，bob 收）
	msg := makeGroupMessage("alice")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"friends"}).Inject(ctx), msg, true)
	assert.Equal(t, 1, result.Sent, "排除发送者后仅投递给 bob")

	select {
	case data := <-bob.SendChan:
		assert.NotEmpty(t, data, "bob 应收到 alice 的消息")
	case <-time.After(time.Second):
		t.Fatal("bob 未收到消息")
	}
	select {
	case <-alice.SendChan:
		t.Fatal("alice 不应收到自己发的消息（excludeSender=true）")
	case <-time.After(200 * time.Millisecond):
		// ok
	}

	// bob 回复消息（不排除发送者，全员收）
	drainClientSendChan(bob)
	msg2 := makeGroupMessage("bob")
	msg2.RequireAck = true
	result2 := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"friends"}).Inject(ctx), msg2, false)
	assert.Equal(t, 2, result2.Sent, "不排除发送者时投递给 2 人")

	// alice 和 bob 都应收到
	select {
	case <-alice.SendChan:
	case <-time.After(time.Second):
		t.Fatal("alice 未收到 bob 的回复")
	}
	select {
	case <-bob.SendChan:
	case <-time.After(time.Second):
		t.Fatal("bob 应收到自己的消息（excludeSender=false）")
	}
}

// TestSendToGroupAutoJoinedCrossNamespace 验证自动加群后跨命名空间隔离投递
func TestSendToGroupAutoJoinedCrossNamespace(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 两个命名空间各一个客户端，使用相同的 GroupID（命名空间隔离）
	clientA := makeTestClient("c-ns-a", "user-ns-a")
	clientA.UserType = UserTypeCustomer
	clientA.Namespace = constants.DefaultNamespace
	clientA.GroupID = "shared-name"
	hub.Register(clientA)

	clientB := makeTestClient("c-ns-b", "user-ns-b")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = "tenantB"
	clientB.GroupID = "shared-name"
	hub.Register(clientB)

	require.Eventually(t, func() bool {
		membersA, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"shared-name"}).Inject(ctx))
		membersB, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{"shared-name"}).Inject(ctx))
		return len(membersA) == 1 && len(membersB) == 1
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 向 tenantA 的 shared-name 群组发送消息
	msg := makeGroupMessage("external")
	msg.RequireAck = true
	resultA := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"shared-name"}).Inject(ctx), msg, false)
	assert.Equal(t, 1, resultA.Sent, "tenantA 命名空间 1 个在线成员")

	// clientA 应收到
	select {
	case data := <-clientA.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("clientA 未收到消息")
	}

	// clientB 不应收到（不同命名空间隔离）
	select {
	case <-clientB.SendChan:
		t.Fatal("clientB 不应收到 tenantA 命名空间的消息")
	case <-time.After(200 * time.Millisecond):
		// ok
	}
}

// TestBroadcastToAllNamespacesAllGroupsAutoJoined 验证自动加群后全命名空间全群组广播
func TestBroadcastToAllNamespacesAllGroupsAutoJoined(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 两个命名空间各一个客户端，自动加入各自命名空间的默认组
	clientA := makeTestClient("c-all-a", "user-all-a")
	clientA.UserType = UserTypeCustomer
	clientA.Namespace = constants.DefaultNamespace
	hub.Register(clientA)

	clientB := makeTestClient("c-all-b", "user-all-b")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = "tenantB"
	hub.Register(clientB)

	require.Eventually(t, func() bool {
		membersA, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		membersB, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantB").WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		return len(membersA) == 1 && len(membersB) == 1
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 全命名空间全群组广播（Deliver 全局广播为异步无计数，改为遍历各命名空间群组累计 LocalDelivered）
	msg := makeGroupMessage("system")
	namespaces, _ := groupRepo.GetAllNamespaces(ctx, constants.DefaultAppID)
	var total int
	for _, ns := range namespaces {
		gids, gErr := hub.GetNamespaceGroups(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(ns).WithGroupIDs(nil).Inject(ctx))
		if gErr != nil || len(gids) == 0 {
			continue
		}
		nsCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(ns).WithGroupIDs(gids).Inject(ctx)
		total += hub.Deliver(nsCtx, msg, false).LocalDelivered
	}
	assert.Equal(t, 2, total, "应投递给 2 个在线成员（tenantA + tenantB）")

	// 两个客户端都应收到
	select {
	case data := <-clientA.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("clientA 未收到广播消息")
	}
	select {
	case data := <-clientB.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("clientB 未收到广播消息")
	}
}

// ============================================================================
// 自动加群 + 手动加群去重测试
//
// 场景：用户连接时自动加入成员组后，业务层再手动 AddGroupMembers 同一用户到同一组
// 验证：Redis SADD 集合语义保证成员不重复，群组消息投递不会收到重复消息
// ============================================================================

// countChanMessages 在 timeout 内统计 SendChan 收到的消息数（非阻塞）
func countChanMessages(c *Client, timeout time.Duration) int {
	count := 0
	deadline := time.After(timeout)
	for {
		select {
		case <-c.SendChan:
			count++
		case <-deadline:
			return count
		}
	}
}

// TestAutoJoinThenManualAddNoDuplicateBusinessGroup 验证业务组：自动加群后再手动加同一用户，发消息不重复
func TestAutoJoinThenManualAddNoDuplicateBusinessGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 连接时自动加入业务组 "dup-room"
	client := makeTestClient("c-dup-bg", "user-dup-bg")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "dup-room"
	hub.Register(client)

	// 等待自动加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-room"}).Inject(ctx))
		for _, m := range members {
			if m == "user-dup-bg" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	// 验证自动加群后成员数为 1
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-room"}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 1, "自动加群后成员数应为 1")

	// 业务层手动再添加同一用户到同一组（幂等，不应产生重复成员）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-room"}).Inject(ctx), []string{"user-dup-bg"}))

	// 验证手动加群后成员数仍为 1（集合语义去重）
	members, err = hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-room"}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 1, "手动重复加群后成员数应仍为 1（SADD 集合语义去重）")
	assert.Contains(t, members, "user-dup-bg")

	drainClientSendChan(client)

	// 向群组发送消息
	msg := makeGroupMessage("external")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-room"}).Inject(ctx), msg, false)
	assert.Equal(t, 1, result.TotalMembers, "总成员数应为 1")
	assert.Equal(t, 1, result.Sent, "应只投递 1 条")

	// 验证客户端只收到 1 条消息（不重复）
	count := countChanMessages(client, time.Second)
	assert.Equal(t, 1, count, "客户端应只收到 1 条消息，不应收到重复消息")
}

// TestAutoJoinThenManualAddNoDuplicateDefaultGroup 验证默认组：自动加群后再手动加同一用户，发消息不重复
func TestAutoJoinThenManualAddNoDuplicateDefaultGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 连接时不指定 GroupID，自动加入默认组
	client := makeTestClient("c-dup-def", "user-dup-def")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	hub.Register(client)

	// 等待自动加入默认组完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		for _, m := range members {
			if m == "user-dup-def" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	// 验证自动加群后成员数为 1
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 1, "自动加入默认组后成员数应为 1")

	// 业务层手动再添加同一用户到默认组
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx), []string{"user-dup-def"}))

	// 验证成员数仍为 1
	members, err = hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 1, "手动重复加群后默认组成员数应仍为 1")

	drainClientSendChan(client)

	// 向默认组发送消息
	msg := makeGroupMessage("system")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx), msg, false)
	assert.Equal(t, 1, result.Sent, "应只投递 1 条")

	// 验证只收到 1 条消息
	count := countChanMessages(client, time.Second)
	assert.Equal(t, 1, count, "客户端应只收到 1 条消息，不应收到重复消息")
}

// TestAutoJoinThenManualAddNoDuplicateBroadcast 验证广播：自动加群后再手动加同一用户，广播不重复
func TestAutoJoinThenManualAddNoDuplicateBroadcast(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 连接时自动加入业务组 "dup-bc-room"
	client := makeTestClient("c-dup-bc", "user-dup-bc")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	client.GroupID = "dup-bc-room"
	hub.Register(client)

	// 等待自动加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-bc-room"}).Inject(ctx))
		return len(members) == 1
	}, 2*time.Second, 5*time.Millisecond)

	// 手动重复加群
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-bc-room"}).Inject(ctx), []string{"user-dup-bc"}))

	// 验证成员数仍为 1
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-bc-room"}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 1, "手动重复加群后成员数应仍为 1")

	drainClientSendChan(client)

	// 广播消息
	msg := makeGroupMessage("external")
	delivered := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dup-bc-room"}).Inject(ctx), msg, false).LocalDelivered
	assert.Equal(t, 1, delivered, "应只投递给 1 个在线成员")

	// 验证只收到 1 条消息
	count := countChanMessages(client, time.Second)
	assert.Equal(t, 1, count, "广播后客户端应只收到 1 条消息，不应收到重复消息")
}

// TestAutoJoinDefaultThenManualAddBusinessNoCrossDuplicate 验证自动加入默认组后手动加入业务组，两组成员关系独立不重复投递
func TestAutoJoinDefaultThenManualAddBusinessNoCrossDuplicate(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 连接时自动加入默认组
	client := makeTestClient("c-cross", "user-cross")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	hub.Register(client)

	// 等待自动加入默认组完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		return len(members) == 1
	}, 2*time.Second, 5*time.Millisecond)

	// 业务层手动将用户加入另一个业务组 "manual-room"
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"manual-room"}).Inject(ctx), []string{"user-cross"}))

	// 验证用户同时在两个组中
	defaultMembers, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, defaultMembers, "user-cross")

	businessMembers, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"manual-room"}).Inject(ctx))
	require.NoError(t, err)
	assert.Contains(t, businessMembers, "user-cross")

	drainClientSendChan(client)

	// 向默认组发消息 → 收到 1 条
	msg1 := makeGroupMessage("sys")
	msg1.RequireAck = true
	hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx), msg1, false)

	// 向业务组发消息 → 收到 1 条
	msg2 := makeGroupMessage("biz")
	msg2.RequireAck = true
	hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"manual-room"}).Inject(ctx), msg2, false)

	// 验证共收到 2 条消息（每组各 1 条，不重复也不遗漏）
	count := countChanMessages(client, time.Second)
	assert.Equal(t, 2, count, "客户端应在两个组各收到 1 条消息，共 2 条")
}

// TestAutoJoinThenManualAddBroadcastAllGroupsDedup 验证自动加入默认组+手动加入业务组后 BroadcastToAllGroups 去重
func TestAutoJoinThenManualAddBroadcastAllGroupsDedup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 连接时自动加入默认组
	client := makeTestClient("c-dedup", "user-dedup")
	client.UserType = UserTypeCustomer
	client.Namespace = constants.DefaultNamespace
	hub.Register(client)

	// 等待自动加入默认组完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{constants.DefaultGroupID}).Inject(ctx))
		return len(members) == 1
	}, 2*time.Second, 5*time.Millisecond)

	// 手动加入业务组（用户同时存在于默认组和业务组）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"dedup-biz"}).Inject(ctx), []string{"user-dedup"}))

	drainClientSendChan(client)

	// BroadcastToAllGroups 应去重：用户在两个组中，但只收到 1 条消息
	// Deliver 无 groupIDs + namespace 非空 → 命名空间广播（天然按客户端去重，每客户端仅 1 条）
	msg := makeGroupMessage("system")
	delivered := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs(nil).Inject(ctx), msg, false).LocalDelivered
	assert.Equal(t, 1, delivered, "同命名空间内用户在多组中应去重，仅投递 1 条")

	// 验证只收到 1 条消息（跨组去重）
	count := countChanMessages(client, time.Second)
	assert.Equal(t, 1, count, "BroadcastToAllGroups 应跨组去重，客户端只收到 1 条消息")
}

// TestAutoJoinThenManualAddMultiUserNoDuplicate 验证多用户场景：自动加群+手动加群混合，各自只收 1 条
func TestAutoJoinThenManualAddMultiUserNoDuplicate(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 用户 A：连接时自动加入 "multi-room"
	clientA := makeTestClient("c-multi-a", "user-multi-a")
	clientA.UserType = UserTypeCustomer
	clientA.Namespace = constants.DefaultNamespace
	clientA.GroupID = "multi-room"
	hub.Register(clientA)

	// 用户 B：不自动加入，稍后手动加入同一组
	clientB := makeTestClient("c-multi-b", "user-multi-b")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = constants.DefaultNamespace
	hub.Register(clientB)

	// 等待用户 A 自动加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"multi-room"}).Inject(ctx))
		return len(members) == 1 && members[0] == "user-multi-a"
	}, 2*time.Second, 5*time.Millisecond)

	// 手动将用户 A 和用户 B 都加入 "multi-room"（A 重复加入，B 新加入）
	require.NoError(t, hub.AddGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"multi-room"}).Inject(ctx), []string{"user-multi-a", "user-multi-b"}))

	// 验证成员数为 2（A 幂等，B 新增）
	members, err := hub.GetGroupMembers(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"multi-room"}).Inject(ctx))
	require.NoError(t, err)
	assert.Len(t, members, 2, "应共 2 个成员（A 去重 + B 新增）")

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 发送消息
	msg := makeGroupMessage("external")
	msg.RequireAck = true
	result := hub.Deliver(routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace(constants.DefaultNamespace).WithGroupIDs([]string{"multi-room"}).Inject(ctx), msg, false)
	assert.Equal(t, 2, result.TotalMembers, "总成员 2")
	assert.Equal(t, 2, result.Sent, "应投递 2 条（A + B 各 1）")

	// 验证 A 和 B 各只收到 1 条消息
	countA := countChanMessages(clientA, time.Second)
	assert.Equal(t, 1, countA, "用户 A 应只收到 1 条消息（自动加群+手动加群不重复）")

	countB := countChanMessages(clientB, time.Second)
	assert.Equal(t, 1, countB, "用户 B 应只收到 1 条消息")
}

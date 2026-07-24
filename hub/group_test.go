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
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
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
func makeTestClient(clientID, userID string) *Client {
	return &Client{
		ID:          clientID,
		UserID:      userID,
		UserType:    UserTypeCustomer,
		Role:        models.UserRoleCustomer,
		Status:      UserStatusOnline,
		LastSeen:    time.Now(),
		SendChan:    make(chan []byte, 16),
		Context:     context.WithValue(context.Background(), ContextKeyUserID, userID),
		ConnectedAt: time.Now(),
	}
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("创建群组后可查询且字段一致", func(t *testing.T) {
		g := &Group{
			GroupID:    "g1",
			Namespace:  "tenantA",
			Name:       "测试群",
			OwnerID:    "owner1",
			MaxMembers: 50,
		}
		require.NoError(t, hub.CreateGroup(ctx, g))

		got, err := hub.GetGroup(ctx, "tenantA", "g1")
		require.NoError(t, err)
		assert.Equal(t, "g1", got.GroupID)
		assert.Equal(t, "tenantA", got.GetNamespace())
		assert.Equal(t, "测试群", got.Name)
		assert.Equal(t, 50, got.MaxMembers)
	})

	t.Run("default 命名空间查询", func(t *testing.T) {
		// CreateGroup 时 groupRepo 将空 Namespace 归一化为 default
		g := &Group{GroupID: "g-default", Name: "默认群", OwnerID: "owner1"}
		require.NoError(t, hub.CreateGroup(ctx, g))

		// 业务查询传明确 namespace（归一化由 register/CreateGroup 层统一）
		got, err := hub.GetGroup(ctx, "default", "g-default")
		require.NoError(t, err)
		assert.Equal(t, "default", got.GetNamespace())
	})
}

// TestHubDisbandGroup 验证通过 Hub 解散群组后成员与元信息被清理
func TestHubDisbandGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-disband", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-disband", []string{"u1", "u2"}))

	require.NoError(t, hub.DisbandGroup(ctx, "tenantA", "g-disband"))

	_, err := hub.GetGroup(ctx, "tenantA", "g-disband")
	assert.ErrorIs(t, err, ErrGroupNotFound)

	members, err := hub.GetGroupMembers(ctx, "tenantA", "g-disband")
	require.NoError(t, err)
	assert.Empty(t, members)
}

// TestHubAddAndRemoveMembers 验证通过 Hub 添加和移除群组成员
func TestHubAddAndRemoveMembers(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-members", Namespace: "tenantA", OwnerID: "o1"}))

	// 添加成员
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-members", []string{"u1", "u2", "u3"}))

	cnt, err := hub.GetGroupMemberCount(ctx, "tenantA", "g-members")
	require.NoError(t, err)
	assert.Equal(t, int64(3), cnt)

	// 判定成员
	ok, err := hub.IsGroupMember(ctx, "tenantA", "g-members", "u2")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = hub.IsGroupMember(ctx, "tenantA", "g-members", "uX")
	require.NoError(t, err)
	assert.False(t, ok)

	// 移除成员
	require.NoError(t, hub.RemoveGroupMembers(ctx, "tenantA", "g-members", []string{"u2"}))
	cnt, err = hub.GetGroupMemberCount(ctx, "tenantA", "g-members")
	require.NoError(t, err)
	assert.Equal(t, int64(2), cnt)

	ok, err = hub.IsGroupMember(ctx, "tenantA", "g-members", "u2")
	require.NoError(t, err)
	assert.False(t, ok)
}

// TestHubGroupMaxMembers 验证群组成员上限校验
func TestHubGroupMaxMembers(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-max", Namespace: "tenantA", OwnerID: "o1", MaxMembers: 2}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-max", []string{"u1", "u2"}))

	// 超出上限应返回 ErrGroupFull
	err := hub.AddGroupMembers(ctx, "tenantA", "g-max", []string{"u3"})
	assert.ErrorIs(t, err, ErrGroupFull)
}

// TestHubGroupNamespaceIsolation 验证 Hub 层群组命名空间隔离
func TestHubGroupNamespaceIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 两个命名空间创建同名群组
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-same", Namespace: "tenantA", Name: "A群", OwnerID: "oA"}))
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-same", Namespace: "tenantB", Name: "B群", OwnerID: "oB"}))

	// 各自添加成员
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-same", []string{"userA"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantB", "g-same", []string{"userB"}))

	// 成员不跨命名空间
	aMembers, err := hub.GetGroupMembers(ctx, "tenantA", "g-same")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"userA"}, aMembers)

	bMembers, err := hub.GetGroupMembers(ctx, "tenantB", "g-same")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"userB"}, bMembers)

	// 解散 tenantA 不影响 tenantB
	require.NoError(t, hub.DisbandGroup(ctx, "tenantA", "g-same"))
	_, err = hub.GetGroup(ctx, "tenantB", "g-same")
	require.NoError(t, err)
}

// TestHubGetUserGroupsAndNamespaceGroups 验证用户群组列表与命名空间群组列表
func TestHubGetUserGroupsAndNamespaceGroups(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	for _, gid := range []string{"g1", "g2", "g3"} {
		require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: gid, Namespace: "tenantA", OwnerID: "o1"}))
		require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", gid, []string{"userX"}))
	}

	// 用户群组列表
	groups, err := hub.GetUserGroups(ctx, "tenantA", "userX")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"g1", "g2", "g3"}, groups)

	// 命名空间群组列表
	tenantGroups, err := hub.GetNamespaceGroups(ctx, "tenantA")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"g1", "g2", "g3"}, tenantGroups)
}

// ============================================================================
// 群组广播测试
// ============================================================================

// TestBroadcastToGroupMembersLocal 验证群组广播本地投递给在线成员
func TestBroadcastToGroupMembersLocal(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建群组并添加成员
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-broadcast", Namespace: "tenantA", OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-broadcast", []string{"user1", "user2", "user3"}))

	// 启动 Hub 并注册在线客户端（user1 和 user2 在本节点）
	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	client1 := makeTestClient("c1", "user1")
	client2 := makeTestClient("c2", "user2")
	hub.Register(client1)
	hub.Register(client2)
	time.Sleep(100 * time.Millisecond)

	// 广播消息（不排除发送者）
	msg := makeGroupMessage("owner1")
	delivered := hub.BroadcastToGroupMembers(ctx, "tenantA", "g-broadcast", msg, false)

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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-exclude", Namespace: "tenantA", OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-exclude", []string{"sender", "user2"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	senderClient := makeTestClient("c-sender", "sender")
	otherClient := makeTestClient("c-other", "user2")
	hub.Register(senderClient)
	hub.Register(otherClient)
	time.Sleep(100 * time.Millisecond)

	// 广播并排除发送者
	msg := makeGroupMessage("sender")
	delivered := hub.BroadcastToGroupMembers(ctx, "tenantA", "g-exclude", msg, true)

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
		Namespace:    "tenantA",
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

	data := hub.marshalDistributedMessage(distMsg, msg.MessageID)
	assert.NotEmpty(t, data, "序列化结果不应为空")

	// 验证可反序列化
	parsed, err := hub.unmarshalDistributedMessage(data)
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
		hub.crossNodeGroupBroadcast(ctx, "tenantA", "g1", msg, false)
	})
}

// ============================================================================
// 群组消息投递（SendToGroup）测试
// ============================================================================

// TestSendToGroupOfflineMembers 验证群组消息对离线成员的投递结果统计
// 注意：未配置离线消息处理器时，离线成员计为 Failed
func TestSendToGroupOfflineMembers(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-send", Namespace: "tenantA", OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-send", []string{"u-online", "u-offline"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	// 注册一个在线成员
	onlineClient := makeTestClient("c-online", "u-online")
	hub.Register(onlineClient)
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("owner1")
	result := hub.SendToGroup(ctx, "tenantA", "g-send", msg, false)

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
	result := hub.SendToGroup(ctx, "tenantA", "any-group", msg, false)

	assert.False(t, len(result.Errors) == 0, "应有错误返回")
	assert.Equal(t, "any-group", result.GroupID)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 3 个群组
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g2", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g3", Namespace: "tenantA", OwnerID: "o1"}))

	// user1 同时在 g1、g2、g3 三个群组（应去重，只收一条）
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g1", []string{"user1", "user2"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g2", []string{"user1", "user3"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g3", []string{"user1", "user4"}))

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
	delivered := hub.BroadcastToAllGroups(ctx, "tenantA", msg)

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
	delivered := hub.BroadcastToAllGroups(ctx, "empty-tenant", msg)
	assert.Equal(t, 0, delivered, "无群组的命名空间应投递 0 条")
}

// TestBroadcastToAllGroupsDefaultNamespace 验证 default 命名空间的群组广播
// namespace 归一化由 register 层统一，业务调用方需传明确 namespace
func TestBroadcastToAllGroupsDefaultNamespace(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// CreateGroup 时 groupRepo 将空 Namespace 归一化为 default
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-default", OwnerID: "o1"}))
	// 业务方法调用方传明确 namespace（default）
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "g-default", []string{"user1"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	client1 := makeTestClient("c1", "user1")
	hub.Register(client1)
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("sender")
	delivered := hub.BroadcastToAllGroups(ctx, "default", msg)
	assert.Equal(t, 1, delivered, "default 命名空间应投递给 1 个在线成员")
}

// TestBroadcastToAllNamespacesAllGroups 验证向所有命名空间所有群组广播
func TestBroadcastToAllNamespacesAllGroups(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 两个命名空间各创建群组
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "gA", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "gA", []string{"userA"}))
	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "gB", Namespace: "tenantB", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantB", "gB", []string{"userB"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	clientA := makeTestClient("cA", "userA")
	clientB := makeTestClient("cB", "userB")
	hub.Register(clientA)
	hub.Register(clientB)
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("sender")
	total := hub.BroadcastToAllNamespacesAllGroups(ctx, msg)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, hub.CreateGroup(ctx, &Group{GroupID: "g-single", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-single", []string{"user1"}))

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
		NodeID:    "remote-node", // 模拟来自其他节点
		TargetID:  "g-single",    // 单群组回退路径
		Namespace: "tenantA",     // 命名空间必须与群组一致，否则查不到成员
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
var _ = models.DefaultNamespace

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
	client.Namespace = "tenantA"

	// 调用 joinSystemGroupsOnConnect
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 验证系统组 __agents__ 已创建且包含 agent-001
	members, err := hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-001", "agent 应自动加入 __agents__ 系统组")

	// 验证系统组元信息 owner 为 system
	g, err := hub.groupRepo.GetGroup(ctx, "tenantA", models.SystemGroupAgents)
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

	members, err := hub.groupRepo.GetMembers(ctx, "tenantB", models.SystemGroupObservers)
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
	members, err := hub.groupRepo.GetMembers(ctx, "", models.SystemGroupObservers)
	require.NoError(t, err)
	assert.Contains(t, members, "global-observer", "全局观察者应加入 tenant='' 的系统组")

	// 确认未加入 default 命名空间的系统组
	membersDefault, _ := hub.groupRepo.GetMembers(ctx, "default", models.SystemGroupObservers)
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
	client.Namespace = "tenantA"

	hub.joinSystemGroupsOnConnect(ctx, client)

	// __agents__ 不应存在或不含 customer-001
	members, _ := hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
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
	client.Namespace = "tenantA"
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 确认已加入
	members, _ := hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
	assert.Contains(t, members, "agent-002")

	// 断开 → 离开系统组
	hub.leaveSystemGroupsOnDisconnect(ctx, client)

	// 确认已离开
	members, err := hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
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
	client.Namespace = "tenantA"

	// 连接 → 断开 → 重连
	hub.joinSystemGroupsOnConnect(ctx, client)
	hub.leaveSystemGroupsOnDisconnect(ctx, client)
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 重连后应再次加入
	members, err := hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-003", "重连后应重新加入系统组")
	assert.Len(t, members, 1, "不应重复加入")
}

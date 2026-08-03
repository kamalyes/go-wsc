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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
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
		require.NoError(t, groupRepo.CreateGroup(ctx, g))

		got, err := hub.GetGroup(ctx, "tenantA", "g1")
		require.NoError(t, err)
		assert.Equal(t, "g1", got.GroupID)
		assert.Equal(t, "tenantA", got.GetNamespace())
		assert.Equal(t, "测试群", got.Name)
		assert.Equal(t, 50, got.MaxMembers)
	})

	t.Run("default 命名空间查询", func(t *testing.T) {
		// CreateGroup 时 groupRepo 将空 Namespace 归一化为 DefaultNamespace
		g := &Group{GroupID: "g-default", Name: "默认群", OwnerID: "owner1"}
		require.NoError(t, groupRepo.CreateGroup(ctx, g))

		// 业务查询传明确 namespace（归一化由 register/CreateGroup 层统一）
		got, err := hub.GetGroup(ctx, models.DefaultNamespace, "g-default")
		require.NoError(t, err)
		assert.Equal(t, models.DefaultNamespace, got.GetNamespace())
	})
}

// TestHubDisbandGroup 验证通过 Hub 解散群组后成员与元信息被清理
func TestHubDisbandGroup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-disband", Namespace: "tenantA", OwnerID: "o1"}))
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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-members", Namespace: "tenantA", OwnerID: "o1"}))

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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-max", Namespace: "tenantA", OwnerID: "o1", MaxMembers: 2}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-max", []string{"u1", "u2"}))

	// 超出上限应返回 ErrGroupFull
	err := hub.AddGroupMembers(ctx, "tenantA", "g-max", []string{"u3"})
	assert.ErrorIs(t, err, ErrGroupFull)
}

// TestHubGroupNamespaceIsolation 验证 Hub 层群组命名空间隔离
func TestHubGroupNamespaceIsolation(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 两个命名空间创建同名群组
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-same", Namespace: "tenantA", Name: "A群", OwnerID: "oA"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-same", Namespace: "tenantB", Name: "B群", OwnerID: "oB"}))

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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	for _, gid := range []string{"g1", "g2", "g3"} {
		require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: gid, Namespace: "tenantA", OwnerID: "o1"}))
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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建群组并添加成员
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-broadcast", Namespace: "tenantA", OwnerID: "owner1"}))
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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-exclude", Namespace: "tenantA", OwnerID: "owner1"}))
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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send", Namespace: "tenantA", OwnerID: "owner1"}))
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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 3 个群组
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g2", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g3", Namespace: "tenantA", OwnerID: "o1"}))

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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// CreateGroup 时 groupRepo 将空 Namespace 归一化为 default
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-default", OwnerID: "o1"}))
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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 两个命名空间各创建群组
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "gA", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "gA", []string{"userA"}))
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "gB", Namespace: "tenantB", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantB", "gB", []string{"userB"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	clientA := makeTestClient("cA", "userA")
	clientA.Namespace = "tenantA"
	clientB := makeTestClient("cB", "userB")
	clientB.Namespace = "tenantB"
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
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-single", Namespace: "tenantA", OwnerID: "o1"}))
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

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-cb", Namespace: "tenantA", OwnerID: "o1", MaxMembers: 10}))

	// 1. Join（模拟 register 自动装配：AddGroupMembers 落库 + triggerGroupMemberJoinCallback 触发回调）
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-cb", []string{"u1", "u2"}))
	hub.triggerGroupMemberJoinCallback("tenantA", "g-cb", []string{"u1", "u2"})
	select {
	case e := <-joinCh:
		assert.Equal(t, "tenantA", e.namespace)
		assert.Equal(t, "g-cb", e.groupID)
		assert.ElementsMatch(t, []string{"u1", "u2"}, e.userIDs)
	case <-time.After(time.Second):
		t.Fatal("OnGroupMemberJoin 未触发")
	}

	// 2. Leave
	require.NoError(t, hub.RemoveGroupMembers(ctx, "tenantA", "g-cb", []string{"u1"}))
	select {
	case e := <-leaveCh:
		assert.Equal(t, "tenantA", e.namespace)
		assert.Equal(t, "g-cb", e.groupID)
		assert.ElementsMatch(t, []string{"u1"}, e.userIDs)
	case <-time.After(time.Second):
		t.Fatal("OnGroupMemberLeave 未触发")
	}

	// 3. Disband
	require.NoError(t, hub.DisbandGroup(ctx, "tenantA", "g-cb"))
	select {
	case e := <-disbandCh:
		assert.Equal(t, "tenantA", e.namespace)
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
	hub.triggerGroupMemberJoinCallback("tA", "g-snap", original)

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
		require.NoError(t, hub.AddGroupMembers(ctx, "tA", "g-manual", []string{"u1", "u2"}))

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
		require.NoError(t, hub.AddGroupMembers(ctx, "tA", "g-full", []string{"u1"}))

		joinCh := make(chan []string, 1)
		hub.OnGroupMemberJoin(func(_ context.Context, _, _ string, uids []string) { joinCh <- uids })

		err := hub.AddGroupMembers(ctx, "tA", "g-full", []string{"u2"})
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

		err := hub.DisbandGroup(context.Background(), "tA", "any")
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
	client.Namespace = "tenantA"
	hub.joinSystemGroupsOnConnect(ctx, client)

	// 确认已加入系统组（底层生效）
	members, err := hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
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
	client1.Namespace = "tenantA"
	client2 := makeTestClient("c-agent-b", "agent-multi")
	client2.UserType = UserTypeAgent
	client2.Namespace = "tenantA"

	// 注册到 registry（模拟在线）
	hub.shardedRegistry.AddClient(client1)
	hub.shardedRegistry.AddClient(client2)

	// 两个都加入系统组（集合语义，agent-multi 只存一份）
	hub.joinSystemGroupsOnConnect(ctx, client1)
	hub.joinSystemGroupsOnConnect(ctx, client2)

	// 确认系统组包含 agent-multi
	members, err := hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-multi")

	// 断开 client1：先从 registry 移除（模拟 removeClientUnsafe 时序），再 leave
	hub.shardedRegistry.RemoveClient(client1.ID, client1.UserID)
	hub.leaveSystemGroupsOnDisconnect(ctx, client1)

	// client2 仍在线，系统组应保留 agent-multi
	members, err = hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
	require.NoError(t, err)
	assert.Contains(t, members, "agent-multi", "仍有其他端在线时不应离开系统组")

	// 断开 client2：从 registry 移除，再 leave
	hub.shardedRegistry.RemoveClient(client2.ID, client2.UserID)
	hub.leaveSystemGroupsOnDisconnect(ctx, client2)

	// 所有连接断开，系统组应移除 agent-multi
	members, err = hub.groupRepo.GetMembers(ctx, "tenantA", models.SystemGroupAgents)
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
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "room-1", []string{"userA"}))

	// 用户 A 重连再次加群：A 已存在，不应误报 ErrGroupFull（1+0=1 ≤ 2）
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "room-1", []string{"userA"}),
		"重连用户已在群内，不应误报超限")

	// 用户 B 加群（成员数=2，满员）
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "room-1", []string{"userB"}))

	// 用户 B 重连再次加群：B 已存在，不应误报（2+0=2 ≤ 2）
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "room-1", []string{"userB"}),
		"重连用户已在群内，满员时也不应误报超限")

	// 用户 C 加群：真正超限（2+1=3 > 2），应报 ErrGroupFull
	err := hub.AddGroupMembers(ctx, "default", "room-1", []string{"userC"})
	assert.ErrorIs(t, err, ErrGroupFull, "真正新增超限应报错")

	// 验证 A/B 成员关系保留（离线不销毁语义）
	exists, err := groupRepo.IsMember(ctx, "default", "room-1", "userA")
	require.NoError(t, err)
	assert.True(t, exists, "用户 A 成员关系应保留")
	exists, err = groupRepo.IsMember(ctx, "default", "room-1", "userB")
	require.NoError(t, err)
	assert.True(t, exists, "用户 B 成员关系应保留")

	// 验证成员总数仍为 2（A、B），C 未加入
	count, err := groupRepo.GetMemberCount(ctx, "default", "room-1")
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
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-auto", []string{"u1", "u2"}))

	// 验证群组已被自动创建
	g, err := groupRepo.GetGroup(ctx, "tenantA", "g-auto")
	require.NoError(t, err)
	assert.Equal(t, "g-auto", g.GroupID)
	assert.Equal(t, "tenantA", g.GetNamespace())

	// 验证成员关系已建立
	members, err := hub.GetGroupMembers(ctx, "tenantA", "g-auto")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2"}, members)

	// 验证命名空间索引包含该群组
	nsGroups, err := hub.GetNamespaceGroups(ctx, "tenantA")
	require.NoError(t, err)
	assert.Contains(t, nsGroups, "g-auto")

	// 验证用户反向索引
	userGroups, err := hub.GetUserGroups(ctx, "tenantA", "u1")
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
		Namespace:  "tenantA",
		Name:       "原始群名",
		MaxMembers: 5,
	}))

	// AddGroupMembers 应复用已存在群组，不覆盖 MaxMembers/Name
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-exist", []string{"u1"}))

	g, err := groupRepo.GetGroup(ctx, "tenantA", "g-exist")
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
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "groupB", []string{"u-move"}))

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
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "groupC", []string{"u-move"}))
	// 2. 从旧群 groupB 移除（触发 leave 回调）
	require.NoError(t, hub.RemoveGroupMembers(ctx, "default", "groupB", []string{"u-move"}))
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
	membersB, err := hub.GetGroupMembers(ctx, "default", "groupB")
	require.NoError(t, err)
	assert.NotContains(t, membersB, "u-move", "A 应已移出 groupB")
	membersC, err := hub.GetGroupMembers(ctx, "default", "groupC")
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
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "groupB", []string{"u-offline"}))

	// === move 场景：A 离线 ===
	// 1. 先加入新群 groupC
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "groupC", []string{"u-offline"}))
	// 2. 从旧群 groupB 移除
	require.NoError(t, hub.RemoveGroupMembers(ctx, "default", "groupB", []string{"u-offline"}))
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
	membersB, err := hub.GetGroupMembers(ctx, "default", "groupB")
	require.NoError(t, err)
	assert.NotContains(t, membersB, "u-offline", "A 应已移出 groupB")
	membersC, err := hub.GetGroupMembers(ctx, "default", "groupC")
	require.NoError(t, err)
	assert.Contains(t, membersC, "u-offline", "A 应在 groupC")

	// 验证用户群组列表只含 groupC（反向索引已更新）
	userGroups, err := hub.GetUserGroups(ctx, "default", "u-offline")
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

func (m *memoryOfflineHandler) ClearOfflineMessages(_ context.Context, userID string) error {
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
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "groupB", []string{"u-reconnect"}))

	// move：加入新群 + 从旧群移除
	require.NoError(t, hub.AddGroupMembers(ctx, "default", "groupC", []string{"u-reconnect"}))
	require.NoError(t, hub.RemoveGroupMembers(ctx, "default", "groupB", []string{"u-reconnect"}))

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

	// 等待异步离线消息推送（pushOfflineMessagesOnConnect 在 workerPool 中异步执行）
	time.Sleep(500 * time.Millisecond)

	// === 阶段3：验证收到的离线消息内容正确 ===
	foundChanged := false
loop:
	for {
		select {
		case b := <-clientA.SendChan:
			var m HubMessage
			if json.Unmarshal(b, &m) == nil && m.MessageType == models.MessageTypeGroupChanged {
				foundChanged = true
				assert.Equal(t, "groupB", m.Data["from_group"], "离线消息应含旧群组")
				assert.Equal(t, "groupC", m.Data["to_group"], "离线消息应含新群组")
				assert.Equal(t, "default", m.Data["namespace"], "离线消息应含命名空间")
				assert.Equal(t, "群组已变更", m.Content, "离线消息内容应正确")
				assert.Equal(t, "u-reconnect", m.Receiver, "离线消息接收者应正确")
				assert.Equal(t, models.MessageSourceOffline, m.Source, "离线消息来源应标记为 offline")
			}
		default:
			break loop
		}
	}
	assert.True(t, foundChanged, "重连后应收到 group_changed 离线消息")

	// 验证离线消息已被删除（推送成功后自动删除）
	count, err = offlineHandler.GetOfflineMessageCount(ctx, "u-reconnect")
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "推送后离线消息应被删除")

	// 验证成员关系：A 在 groupC，不在 groupB
	membersB, err := hub.GetGroupMembers(ctx, "default", "groupB")
	require.NoError(t, err)
	assert.NotContains(t, membersB, "u-reconnect", "A 应不在 groupB")
	membersC, err := hub.GetGroupMembers(ctx, "default", "groupC")
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

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-append", Namespace: "tenantA", OwnerID: "o1", Name: "原始群"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-append", []string{"u1", "u2"}))

	// 追加新成员 u3、u4，同时重复添加已存在成员 u1（幂等，集合语义）
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-append", []string{"u1", "u3", "u4"}))

	members, err := hub.GetGroupMembers(ctx, "tenantA", "g-append")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2", "u3", "u4"}, members)

	cnt, err := hub.GetGroupMemberCount(ctx, "tenantA", "g-append")
	require.NoError(t, err)
	assert.Equal(t, int64(4), cnt)

	// 追加后分组元信息仍存在且未被覆盖
	g, err := hub.GetGroup(ctx, "tenantA", "g-append")
	require.NoError(t, err)
	assert.Equal(t, "o1", g.OwnerID)
	assert.Equal(t, "原始群", g.Name)

	// 命名空间群组索引不重复（仅一个 g-append）
	nsGroups, err := hub.GetNamespaceGroups(ctx, "tenantA")
	require.NoError(t, err)
	assert.Equal(t, 1, len(nsGroups))
}

// TestAddGroupMembersEmptyUserIDs 验证空 userIDs 列表直接返回 nil（早返回分支）
func TestAddGroupMembersEmptyUserIDs(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-empty-arg", Namespace: "tenantA", OwnerID: "o1"}))

	// 空切片与 nil 均不应报错，也不应建立成员关系
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-empty-arg", []string{}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-empty-arg", nil))

	cnt, err := hub.GetGroupMemberCount(ctx, "tenantA", "g-empty-arg")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)
}

// TestRemoveGroupMembersNonExistent 验证移除不存在的成员不报错（幂等）
func TestRemoveGroupMembersNonExistent(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-rm", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-rm", []string{"u1", "u2"}))

	// 移除不存在的成员 uX、uY 不应报错（Redis SRem 对不存在元素幂等）
	require.NoError(t, hub.RemoveGroupMembers(ctx, "tenantA", "g-rm", []string{"uX", "uY"}))

	// 原成员不受影响
	members, err := hub.GetGroupMembers(ctx, "tenantA", "g-rm")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2"}, members)

	// 混合移除：一个真实成员 + 一个不存在成员
	require.NoError(t, hub.RemoveGroupMembers(ctx, "tenantA", "g-rm", []string{"u1", "uZ"}))
	members, err = hub.GetGroupMembers(ctx, "tenantA", "g-rm")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u2"}, members)

	cnt, err := hub.GetGroupMemberCount(ctx, "tenantA", "g-rm")
	require.NoError(t, err)
	assert.Equal(t, int64(1), cnt)
}

// TestRemoveGroupMembersEmptyUserIDs 验证空 userIDs 列表直接返回 nil（早返回分支）
func TestRemoveGroupMembersEmptyUserIDs(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-rm-empty", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-rm-empty", []string{"u1"}))

	// 空切片与 nil 均不应报错，成员应保留
	require.NoError(t, hub.RemoveGroupMembers(ctx, "tenantA", "g-rm-empty", []string{}))
	require.NoError(t, hub.RemoveGroupMembers(ctx, "tenantA", "g-rm-empty", nil))

	members, err := hub.GetGroupMembers(ctx, "tenantA", "g-rm-empty")
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
	err := hub.DisbandGroup(ctx, "tenantA", "g-not-exist")
	assert.NoError(t, err, "解散不存在的分组应幂等返回 nil")

	// 重复解散同样不报错
	err = hub.DisbandGroup(ctx, "tenantA", "g-not-exist")
	assert.NoError(t, err)

	// 解散后查询仍为不存在
	_, err = hub.GetGroup(ctx, "tenantA", "g-not-exist")
	assert.ErrorIs(t, err, ErrGroupNotFound)

	// 成员列表为空
	members, err := hub.GetGroupMembers(ctx, "tenantA", "g-not-exist")
	require.NoError(t, err)
	assert.Empty(t, members)
}

// TestGetGroupMembersEmptyGroup 验证空分组/不存在的分组返回空成员列表与 0 计数
func TestGetGroupMembersEmptyGroup(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	// 不存在的分组：GetMembers 对缺失 key 返回空切片，GetMemberCount 返回 0
	members, err := hub.GetGroupMembers(ctx, "tenantA", "g-no-such")
	require.NoError(t, err)
	assert.Empty(t, members)

	cnt, err := hub.GetGroupMemberCount(ctx, "tenantA", "g-no-such")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)

	// 存在但无成员的分组同样返回空与 0
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-empty", Namespace: "tenantA", OwnerID: "o1"}))
	members, err = hub.GetGroupMembers(ctx, "tenantA", "g-empty")
	require.NoError(t, err)
	assert.Empty(t, members)

	cnt, err = hub.GetGroupMemberCount(ctx, "tenantA", "g-empty")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)
}

// TestGetUserGroupsNoMembership 验证用户未加入任何分组时返回空列表
func TestGetUserGroupsNoMembership(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	groups, err := hub.GetUserGroups(ctx, "tenantA", "user-no-groups")
	require.NoError(t, err)
	assert.Empty(t, groups)
}

// TestNamespaceGroupsEmpty 验证无群组的命名空间返回空列表
func TestNamespaceGroupsEmpty(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	groups, err := hub.GetNamespaceGroups(ctx, "tenant-empty")
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
		_, err := hub.GetGroup(ctx, "ns", "g")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("DisbandGroup", func(t *testing.T) {
		err := hub.DisbandGroup(ctx, "ns", "g")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("AddGroupMembers", func(t *testing.T) {
		err := hub.AddGroupMembers(ctx, "ns", "g", []string{"u1"})
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("RemoveGroupMembers", func(t *testing.T) {
		err := hub.RemoveGroupMembers(ctx, "ns", "g", []string{"u1"})
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetGroupMembers", func(t *testing.T) {
		_, err := hub.GetGroupMembers(ctx, "ns", "g")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetGroupMemberCount", func(t *testing.T) {
		_, err := hub.GetGroupMemberCount(ctx, "ns", "g")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("IsGroupMember", func(t *testing.T) {
		_, err := hub.IsGroupMember(ctx, "ns", "g", "u1")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetUserGroups", func(t *testing.T) {
		_, err := hub.GetUserGroups(ctx, "ns", "u1")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
	t.Run("GetNamespaceGroups", func(t *testing.T) {
		_, err := hub.GetNamespaceGroups(ctx, "ns")
		assert.ErrorIs(t, err, ErrGroupRepoNotSet)
	})
}

// TestSendToGroupExcludeSenderTrue 验证发送者在线时 excludeSender=true：自己不收到，其他在线成员收到
func TestSendToGroupExcludeSenderTrue(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send-excl", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-send-excl", []string{"sender", "user2"}))

	go hub.Run()
	defer hub.SafeShutdown()
	hub.WaitForStart()

	senderClient := makeTestClient("c-sender-excl", "sender")
	otherClient := makeTestClient("c-other-excl", "user2")
	hub.Register(senderClient)
	hub.Register(otherClient)

	// 等待两个客户端均注册上线（确定性，race-safe）
	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("sender") && hub.shardedRegistry.HasUser("user2")
	}, 2*time.Second, 5*time.Millisecond)

	msg := makeGroupMessage("sender")
	result := hub.SendToGroup(ctx, "tenantA", "g-send-excl", msg, true)

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

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send-all", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-send-all", []string{"sender", "user2"}))

	go hub.Run()
	defer hub.SafeShutdown()
	hub.WaitForStart()

	senderClient := makeTestClient("c-sender-all", "sender")
	otherClient := makeTestClient("c-other-all", "user2")
	hub.Register(senderClient)
	hub.Register(otherClient)

	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("sender") && hub.shardedRegistry.HasUser("user2")
	}, 2*time.Second, 5*time.Millisecond)

	msg := makeGroupMessage("sender")
	result := hub.SendToGroup(ctx, "tenantA", "g-send-all", msg, false)

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

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-send-nofilter", Namespace: "tenantA", OwnerID: "o1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-send-nofilter", []string{"user1", "user2"}))

	go hub.Run()
	defer hub.SafeShutdown()
	hub.WaitForStart()

	client1 := makeTestClient("c-nf1", "user1")
	client2 := makeTestClient("c-nf2", "user2")
	hub.Register(client1)
	hub.Register(client2)

	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("user1") && hub.shardedRegistry.HasUser("user2")
	}, 2*time.Second, 5*time.Millisecond)

	// Sender 为空 + excludeSender=true → 不过滤，全员投递
	msg := makeGroupMessage("")
	result := hub.SendToGroup(ctx, "tenantA", "g-send-nofilter", msg, true)

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
	result := hub.SendToGroup(ctx, "tenantA", "g-no-such-empty", msg, false)
	assert.Equal(t, 0, result.TotalMembers)
	assert.Equal(t, 0, result.OnlineMembers)
	assert.Equal(t, 0, result.Sent)
	assert.Empty(t, result.Errors)
	assert.Equal(t, "g-no-such-empty", result.GroupID)

	// 存在但无成员的分组同样立即返回
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-empty-send", Namespace: "tenantA", OwnerID: "o1"}))
	result = hub.SendToGroup(ctx, "tenantA", "g-empty-send", msg, true)
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
	client.Namespace = "tenantA"
	client.GroupID = "my-group"

	hub.joinMemberGroupOnConnect(ctx, client)

	// 验证用户已加入指定群组
	members, err := hub.GetGroupMembers(ctx, "tenantA", "my-group")
	require.NoError(t, err)
	assert.Contains(t, members, "user-mg-1")

	// 验证群组被自动创建
	g, err := hub.GetGroup(ctx, "tenantA", "my-group")
	require.NoError(t, err)
	assert.Equal(t, "my-group", g.GroupID)
	assert.Equal(t, "tenantA", g.GetNamespace())
}

// TestJoinMemberGroupOnConnectDefaultGroup 验证未指定 GroupID 时自动加入默认组
func TestJoinMemberGroupOnConnectDefaultGroup(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-2", "user-mg-2")
	client.UserType = UserTypeCustomer
	client.Namespace = "tenantA"
	// 不设 GroupID → GetGroupID 返回 DefaultGroupID

	hub.joinMemberGroupOnConnect(ctx, client)

	// 验证用户已加入默认组（DefaultGroupID 是系统组名，走 EnsureSystemGroup 路径）
	members, err := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
	require.NoError(t, err)
	assert.Contains(t, members, "user-mg-2")

	// 验证默认组元信息已创建
	g, err := hub.GetGroup(ctx, "tenantA", models.DefaultGroupID)
	require.NoError(t, err)
	assert.Equal(t, models.DefaultGroupID, g.GroupID)
}

// TestJoinMemberGroupOnConnectObserverSkipped 验证观察者不作为成员加入群组
func TestJoinMemberGroupOnConnectObserverSkipped(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-obs", "observer-mg")
	client.UserType = UserTypeObserver
	client.Namespace = "tenantA"
	client.GroupID = "obs-skip-group"

	hub.joinMemberGroupOnConnect(ctx, client)

	// 观察者不应加入成员组（群组不应被创建）
	members, err := hub.GetGroupMembers(ctx, "tenantA", "obs-skip-group")
	require.NoError(t, err)
	assert.NotContains(t, members, "observer-mg")

	_, err = hub.GetGroup(ctx, "tenantA", "obs-skip-group")
	assert.ErrorIs(t, err, ErrGroupNotFound, "观察者连接不应触发成员组创建")
}

// TestJoinMemberGroupOnConnectReconnectIdempotent 验证重连加群幂等（不重复加入）
func TestJoinMemberGroupOnConnectReconnectIdempotent(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	client := makeTestClient("c-mg-3", "user-mg-3")
	client.UserType = UserTypeCustomer
	client.Namespace = "tenantA"
	client.GroupID = "room-mg"

	// 模拟重连：多次调用 joinMemberGroupOnConnect
	hub.joinMemberGroupOnConnect(ctx, client)
	hub.joinMemberGroupOnConnect(ctx, client)

	members, err := hub.GetGroupMembers(ctx, "tenantA", "room-mg")
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

	members, err := hub.GetGroupMembers(ctx, "tenantB", models.DefaultGroupID)
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
		client.Namespace = "tenantA"
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
		client.Namespace = "tenantA"
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
	client.Namespace = "tenantA"
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
	client.Namespace = "tenantA"
	client.GroupID = "reg-group"
	hub.Register(client)

	// 等待异步加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "reg-group")
		for _, m := range members {
			if m == "user-reg-mg" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "handleRegister 应自动将用户加入业务组")

	// 验证群组已创建
	g, err := hub.GetGroup(ctx, "tenantA", "reg-group")
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
	client.Namespace = "tenantA"
	// 不设 GroupID → 自动加入默认组
	hub.Register(client)

	// 等待异步加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
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
	client.Namespace = "tenantA"
	client.GroupID = "obs-should-skip"
	hub.Register(client)

	// 等待异步流程完成
	time.Sleep(300 * time.Millisecond)

	// 观察者不应加入成员组
	members, err := hub.GetGroupMembers(ctx, "tenantA", "obs-should-skip")
	require.NoError(t, err)
	assert.NotContains(t, members, "observer-reg", "观察者不应加入成员组")

	// 群组不应被创建
	_, err = hub.GetGroup(ctx, "tenantA", "obs-should-skip")
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
	client.Namespace = "tenantA"
	client.GroupID = "chat-room"
	hub.Register(client)

	// 等待异步加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "chat-room")
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
	result := hub.SendToGroup(ctx, "tenantA", "chat-room", msg, false)

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
	client.Namespace = "tenantA"
	hub.Register(client)

	// 等待异步加群完成（默认组）
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
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
	result := hub.SendToGroup(ctx, "tenantA", models.DefaultGroupID, msg, false)

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
	senderClient.Namespace = "tenantA"
	senderClient.GroupID = "excl-room"
	hub.Register(senderClient)

	otherClient := makeTestClient("c-snd-other", "other-user")
	otherClient.UserType = UserTypeCustomer
	otherClient.Namespace = "tenantA"
	otherClient.GroupID = "excl-room"
	hub.Register(otherClient)

	// 等待两个客户端均注册并加群
	require.Eventually(t, func() bool {
		return hub.shardedRegistry.HasUser("sender-user") && hub.shardedRegistry.HasUser("other-user")
	}, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "excl-room")
		return len(members) == 2
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(senderClient)
	drainClientSendChan(otherClient)

	// excludeSender=true：发送者不收，其他成员收
	msg := makeGroupMessage("sender-user")
	result := hub.SendToGroup(ctx, "tenantA", "excl-room", msg, true)

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
	client1.Namespace = "tenantA"
	client1.GroupID = "bc-room"
	hub.Register(client1)

	client2 := makeTestClient("c-bc-2", "user-bc-2")
	client2.UserType = UserTypeCustomer
	client2.Namespace = "tenantA"
	client2.GroupID = "bc-room"
	hub.Register(client2)

	client3 := makeTestClient("c-bc-3", "user-bc-3")
	client3.UserType = UserTypeCustomer
	client3.Namespace = "tenantA"
	client3.GroupID = "bc-room"
	hub.Register(client3)

	// 等待全部注册并加群
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "bc-room")
		return len(members) == 3
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(client1)
	drainClientSendChan(client2)
	drainClientSendChan(client3)

	// 广播（不排除发送者）
	msg := makeGroupMessage("external")
	delivered := hub.BroadcastToGroupMembers(ctx, "tenantA", "bc-room", msg, false)
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
	clientA.Namespace = "tenantA"
	hub.Register(clientA)

	clientB := makeTestClient("c-ba-2", "user-ba-2")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = "tenantB"
	hub.Register(clientB)

	// 等待全部加群
	require.Eventually(t, func() bool {
		membersA, _ := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
		membersB, _ := hub.GetGroupMembers(ctx, "tenantB", models.DefaultGroupID)
		return len(membersA) == 1 && len(membersB) == 1
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 向 tenantA 的所有群组广播（含默认组）
	msg := makeGroupMessage("system")
	delivered := hub.BroadcastToAllGroups(ctx, "tenantA", msg)
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
	alice.Namespace = "tenantA"
	alice.GroupID = "friends"
	hub.Register(alice)

	bob := makeTestClient("c-bob", "bob")
	bob.UserType = UserTypeCustomer
	bob.Namespace = "tenantA"
	bob.GroupID = "friends"
	hub.Register(bob)

	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "friends")
		return len(members) == 2
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(alice)
	drainClientSendChan(bob)

	// alice 发消息到群组（排除发送者，bob 收）
	msg := makeGroupMessage("alice")
	result := hub.SendToGroup(ctx, "tenantA", "friends", msg, true)
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
	result2 := hub.SendToGroup(ctx, "tenantA", "friends", msg2, false)
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
	clientA.Namespace = "tenantA"
	clientA.GroupID = "shared-name"
	hub.Register(clientA)

	clientB := makeTestClient("c-ns-b", "user-ns-b")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = "tenantB"
	clientB.GroupID = "shared-name"
	hub.Register(clientB)

	require.Eventually(t, func() bool {
		membersA, _ := hub.GetGroupMembers(ctx, "tenantA", "shared-name")
		membersB, _ := hub.GetGroupMembers(ctx, "tenantB", "shared-name")
		return len(membersA) == 1 && len(membersB) == 1
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 向 tenantA 的 shared-name 群组发送消息
	msg := makeGroupMessage("external")
	resultA := hub.SendToGroup(ctx, "tenantA", "shared-name", msg, false)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	go hub.Run()
	defer hub.Shutdown()
	hub.WaitForStart()

	// 两个命名空间各一个客户端，自动加入各自命名空间的默认组
	clientA := makeTestClient("c-all-a", "user-all-a")
	clientA.UserType = UserTypeCustomer
	clientA.Namespace = "tenantA"
	hub.Register(clientA)

	clientB := makeTestClient("c-all-b", "user-all-b")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = "tenantB"
	hub.Register(clientB)

	require.Eventually(t, func() bool {
		membersA, _ := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
		membersB, _ := hub.GetGroupMembers(ctx, "tenantB", models.DefaultGroupID)
		return len(membersA) == 1 && len(membersB) == 1
	}, 2*time.Second, 5*time.Millisecond)

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 全命名空间全群组广播
	msg := makeGroupMessage("system")
	total := hub.BroadcastToAllNamespacesAllGroups(ctx, msg)
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
	client.Namespace = "tenantA"
	client.GroupID = "dup-room"
	hub.Register(client)

	// 等待自动加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "dup-room")
		for _, m := range members {
			if m == "user-dup-bg" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	// 验证自动加群后成员数为 1
	members, err := hub.GetGroupMembers(ctx, "tenantA", "dup-room")
	require.NoError(t, err)
	assert.Len(t, members, 1, "自动加群后成员数应为 1")

	// 业务层手动再添加同一用户到同一组（幂等，不应产生重复成员）
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "dup-room", []string{"user-dup-bg"}))

	// 验证手动加群后成员数仍为 1（集合语义去重）
	members, err = hub.GetGroupMembers(ctx, "tenantA", "dup-room")
	require.NoError(t, err)
	assert.Len(t, members, 1, "手动重复加群后成员数应仍为 1（SADD 集合语义去重）")
	assert.Contains(t, members, "user-dup-bg")

	drainClientSendChan(client)

	// 向群组发送消息
	msg := makeGroupMessage("external")
	result := hub.SendToGroup(ctx, "tenantA", "dup-room", msg, false)
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
	client.Namespace = "tenantA"
	hub.Register(client)

	// 等待自动加入默认组完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
		for _, m := range members {
			if m == "user-dup-def" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)

	// 验证自动加群后成员数为 1
	members, err := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
	require.NoError(t, err)
	assert.Len(t, members, 1, "自动加入默认组后成员数应为 1")

	// 业务层手动再添加同一用户到默认组
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", models.DefaultGroupID, []string{"user-dup-def"}))

	// 验证成员数仍为 1
	members, err = hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
	require.NoError(t, err)
	assert.Len(t, members, 1, "手动重复加群后默认组成员数应仍为 1")

	drainClientSendChan(client)

	// 向默认组发送消息
	msg := makeGroupMessage("system")
	result := hub.SendToGroup(ctx, "tenantA", models.DefaultGroupID, msg, false)
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
	client.Namespace = "tenantA"
	client.GroupID = "dup-bc-room"
	hub.Register(client)

	// 等待自动加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "dup-bc-room")
		return len(members) == 1
	}, 2*time.Second, 5*time.Millisecond)

	// 手动重复加群
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "dup-bc-room", []string{"user-dup-bc"}))

	// 验证成员数仍为 1
	members, err := hub.GetGroupMembers(ctx, "tenantA", "dup-bc-room")
	require.NoError(t, err)
	assert.Len(t, members, 1, "手动重复加群后成员数应仍为 1")

	drainClientSendChan(client)

	// 广播消息
	msg := makeGroupMessage("external")
	delivered := hub.BroadcastToGroupMembers(ctx, "tenantA", "dup-bc-room", msg, false)
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
	client.Namespace = "tenantA"
	hub.Register(client)

	// 等待自动加入默认组完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
		return len(members) == 1
	}, 2*time.Second, 5*time.Millisecond)

	// 业务层手动将用户加入另一个业务组 "manual-room"
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "manual-room", []string{"user-cross"}))

	// 验证用户同时在两个组中
	defaultMembers, err := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
	require.NoError(t, err)
	assert.Contains(t, defaultMembers, "user-cross")

	businessMembers, err := hub.GetGroupMembers(ctx, "tenantA", "manual-room")
	require.NoError(t, err)
	assert.Contains(t, businessMembers, "user-cross")

	drainClientSendChan(client)

	// 向默认组发消息 → 收到 1 条
	msg1 := makeGroupMessage("sys")
	hub.SendToGroup(ctx, "tenantA", models.DefaultGroupID, msg1, false)

	// 向业务组发消息 → 收到 1 条
	msg2 := makeGroupMessage("biz")
	hub.SendToGroup(ctx, "tenantA", "manual-room", msg2, false)

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
	client.Namespace = "tenantA"
	hub.Register(client)

	// 等待自动加入默认组完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", models.DefaultGroupID)
		return len(members) == 1
	}, 2*time.Second, 5*time.Millisecond)

	// 手动加入业务组（用户同时存在于默认组和业务组）
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "dedup-biz", []string{"user-dedup"}))

	drainClientSendChan(client)

	// BroadcastToAllGroups 应去重：用户在两个组中，但只收到 1 条消息
	msg := makeGroupMessage("system")
	delivered := hub.BroadcastToAllGroups(ctx, "tenantA", msg)
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
	clientA.Namespace = "tenantA"
	clientA.GroupID = "multi-room"
	hub.Register(clientA)

	// 用户 B：不自动加入，稍后手动加入同一组
	clientB := makeTestClient("c-multi-b", "user-multi-b")
	clientB.UserType = UserTypeCustomer
	clientB.Namespace = "tenantA"
	hub.Register(clientB)

	// 等待用户 A 自动加群完成
	require.Eventually(t, func() bool {
		members, _ := hub.GetGroupMembers(ctx, "tenantA", "multi-room")
		return len(members) == 1 && members[0] == "user-multi-a"
	}, 2*time.Second, 5*time.Millisecond)

	// 手动将用户 A 和用户 B 都加入 "multi-room"（A 重复加入，B 新加入）
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "multi-room", []string{"user-multi-a", "user-multi-b"}))

	// 验证成员数为 2（A 幂等，B 新增）
	members, err := hub.GetGroupMembers(ctx, "tenantA", "multi-room")
	require.NoError(t, err)
	assert.Len(t, members, 2, "应共 2 个成员（A 去重 + B 新增）")

	drainClientSendChan(clientA)
	drainClientSendChan(clientB)

	// 发送消息
	msg := makeGroupMessage("external")
	result := hub.SendToGroup(ctx, "tenantA", "multi-room", msg, false)
	assert.Equal(t, 2, result.TotalMembers, "总成员 2")
	assert.Equal(t, 2, result.Sent, "应投递 2 条（A + B 各 1）")

	// 验证 A 和 B 各只收到 1 条消息
	countA := countChanMessages(clientA, time.Second)
	assert.Equal(t, 1, countA, "用户 A 应只收到 1 条消息（自动加群+手动加群不重复）")

	countB := countChanMessages(clientB, time.Second)
	assert.Equal(t, 1, countB, "用户 B 应只收到 1 条消息")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-09 00:16:29
 * @FilePath: \go-wsc\hub\callbacks_test.go
 * @Description: Hub 回调管理白盒单元测试（覆盖 hub/callbacks.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCallbacks_RegisterAll 验证所有回调注册方法能正确存储回调
// 覆盖 callbacks.go 中全部 OnXxx setter
func TestCallbacks_RegisterAll(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.OnOfflineMessagePush(func(userID string, pushed, failed []string) {})
	hub.OnMessageSend(func(msg *HubMessage, result *SendResult) {})
	hub.OnQueueFull(func(msg *HubMessage, recipient string, qt QueueType, err errorx.BaseError) {})
	hub.OnHeartbeatTimeout(func(clientID, userID string, last time.Time) {})
	hub.OnHeartbeatReport(func(client *Client) {})
	hub.OnBeforeHeartbeat(func(client *Client) bool { return true })
	hub.OnAfterHeartbeat(func(client *Client) {})
	hub.OnClientConnect(func(ctx context.Context, client *Client) error { return nil })
	hub.OnClientDisconnect(func(ctx context.Context, client *Client, reason DisconnectReason) error { return nil })
	hub.OnMessageReceived(func(ctx context.Context, client *Client, msg *HubMessage) error { return nil })
	hub.OnError(func(ctx context.Context, err error, severity ErrorSeverity) error { return nil })
	hub.OnBatchSendFailure(func(userID string, msg *HubMessage, err error) {})
	hub.OnGroupDisband(func(ctx context.Context, namespace, groupID string) {})
	hub.OnGroupMemberJoin(func(ctx context.Context, namespace, groupID string, userIDs []string) {})
	hub.OnGroupMemberLeave(func(ctx context.Context, namespace, groupID string, userIDs []string) {})

	// 验证所有回调字段均已设置（非 nil）
	assert.NotNil(t, hub.offlineMessagePushCallback)
	assert.NotNil(t, hub.messageSendCallback)
	assert.NotNil(t, hub.queueFullCallback)
	assert.NotNil(t, hub.heartbeatTimeoutCallback)
	assert.NotNil(t, hub.heartbeatReportCallback)
	assert.NotNil(t, hub.beforeHeartbeatCallback)
	assert.NotNil(t, hub.afterHeartbeatCallback)
	assert.NotNil(t, hub.clientConnectCallback)
	assert.NotNil(t, hub.clientDisconnectCallback)
	assert.NotNil(t, hub.messageReceivedCallback)
	assert.NotNil(t, hub.errorCallback)
	assert.NotNil(t, hub.batchSendFailureCallback)
	assert.NotNil(t, hub.groupDisbandCallback)
	assert.NotNil(t, hub.groupMemberJoinCallback)
	assert.NotNil(t, hub.groupMemberLeaveCallback)
}

// TestOnMessageSend_Triggered 验证消息发送完成回调被触发
// invokeMessageSendCallback 异步执行（syncx.Go），用 channel 等待
func TestOnMessageSend_Triggered(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.SetOfflineMessageHandler(&fakeOfflineHandler{})

	done := make(chan struct{}, 1)
	hub.OnMessageSend(func(msg *HubMessage, result *SendResult) {
		assert.NotNil(t, msg)
		assert.NotNil(t, result)
		select {
		case done <- struct{}{}:
		default:
		}
	})

	result := hub.SendToUserWithRetry(context.Background(), "u-offline", makeGroupMessage("s1"))
	require.NotNil(t, result)
	assert.True(t, result.Success, "离线存储应视为成功")

	select {
	case <-done:
		// 回调被调用
	case <-time.After(2 * time.Second):
		t.Fatal("消息发送完成回调超时未触发")
	}
}

// TestOnBatchSendFailure_Triggered 验证批量发送失败回调被触发
func TestOnBatchSendFailure_Triggered(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	var called int32
	hub.OnBatchSendFailure(func(userID string, msg *HubMessage, err error) {
		atomic.StoreInt32(&called, 1)
		assert.NotNil(t, err)
	})

	bs := hub.NewBatchSender(context.Background())
	bs.AddMessage("u-fail-cb", makeGroupMessage("s1"))
	bs.Execute()

	assert.Equal(t, int32(1), atomic.LoadInt32(&called))
}

// TestOnGroupDisband_Triggered 验证群组解散回调被触发
func TestOnGroupDisband_Triggered(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-disband", Namespace: "tenantA", OwnerID: "owner1"}))

	var ns, gid string
	var called int32
	hub.OnGroupDisband(func(ctx context.Context, namespace, groupID string) {
		ns = namespace
		gid = groupID
		atomic.StoreInt32(&called, 1) // 最后写 called，确保 ns/gid 对 Load 方可见
	})

	require.NoError(t, hub.DisbandGroup(ctx, "tenantA", "g-disband"))

	require.Eventually(t, func() bool { return atomic.LoadInt32(&called) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, "tenantA", ns)
	assert.Equal(t, "g-disband", gid)
}

// TestOnGroupMemberLeave_Triggered 验证群组成员离开回调被触发
func TestOnGroupMemberLeave_Triggered(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-leave", Namespace: "tenantA", OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-leave", []string{"u-leave-1", "u-leave-2"}))

	var leaveUIDs []string
	var called int32
	hub.OnGroupMemberLeave(func(ctx context.Context, namespace, groupID string, userIDs []string) {
		leaveUIDs = userIDs
		atomic.StoreInt32(&called, 1) // 最后写 called，确保 leaveUIDs 对 Load 方可见
	})

	require.NoError(t, hub.RemoveGroupMembers(ctx, "tenantA", "g-leave", []string{"u-leave-1"}))

	require.Eventually(t, func() bool { return atomic.LoadInt32(&called) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"u-leave-1"}, leaveUIDs)
}

// TestOnGroupMemberJoin_Triggered 验证群组成员加入回调被触发（通过 triggerGroupMemberJoinCallback）
func TestOnGroupMemberJoin_Triggered(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	var joinUIDs []string
	var called int32
	hub.OnGroupMemberJoin(func(ctx context.Context, namespace, groupID string, userIDs []string) {
		joinUIDs = userIDs
		atomic.StoreInt32(&called, 1) // 最后写 called，确保 joinUIDs 对 Load 方可见
	})

	hub.triggerGroupMemberJoinCallback("tenantA", "g-join", []string{"u-join-1", "u-join-2"})

	require.Eventually(t, func() bool { return atomic.LoadInt32(&called) == 1 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"u-join-1", "u-join-2"}, joinUIDs)
}

// TestOnQueueFull_Triggered 验证队列满回调被触发
// 用 smallRetryHubConfig（队列极小）+ 不启动 Run + 填满队列 → 触发 queueFullCallback
func TestOnQueueFull_Triggered(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(0))
	defer hub.SafeShutdown()

	var called int32
	hub.OnQueueFull(func(msg *HubMessage, recipient string, qt QueueType, err errorx.BaseError) {
		atomic.StoreInt32(&called, 1)
	})

	// 注册在线用户但填满队列，sendToUser 走队列满分支
	client := makeTestClient("c-qfull-cb", "u-qfull-cb")
	hub.shardedRegistry.AddClient(client)

	// 填满 broadcast（cap = MessageBufferSize*4 = 4）与 pending（cap = 1）
	fill := &HubMessage{ID: "fill"}
	for i := 0; i < 4; i++ {
		hub.broadcast <- fill
	}
	hub.pendingMessages <- fill

	msg := makeGroupMessage("sender")
	msg.ReceiverType = UserTypeCustomer
	hub.SendToUserWithRetry(context.Background(), "u-qfull-cb", msg)

	// queueFullCallback 在 sendToUser 内异步触发（worker pool），等待
	require.Eventually(t, func() bool { return atomic.LoadInt32(&called) == 1 }, 2*time.Second, 20*time.Millisecond)
}

// TestOnMessageReceived_Triggered 验证消息接收回调被触发
func TestOnMessageReceived_Triggered(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()
	ctx := context.Background()

	var called int32
	hub.OnMessageReceived(func(ctx context.Context, client *Client, msg *HubMessage) error {
		atomic.StoreInt32(&called, 1)
		return nil
	})

	client := makeTestClient("c-recv", "u-recv")
	msg := makeGroupMessage("sender")

	err := hub.InvokeMessageReceivedCallback(ctx, client, msg)
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&called))
}

// TestOnMessageReceived_NoCallback 验证无回调时返回 nil
func TestOnMessageReceived_NoCallback(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-recv2", "u-recv2")
	msg := makeGroupMessage("sender")
	// 未注册回调，应返回 nil
	err := hub.InvokeMessageReceivedCallback(context.Background(), client, msg)
	assert.Nil(t, err)
}

// TestOnClientConnect_Triggered 验证客户端连接回调被触发（通过 register 流程）
func TestOnClientConnect_Triggered(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	var called int32
	hub.OnClientConnect(func(ctx context.Context, client *Client) error {
		atomic.StoreInt32(&called, 1)
		return nil
	})

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	client := makeTestClient("c-conn", "u-conn")
	hub.register <- client

	require.Eventually(t, func() bool { return atomic.LoadInt32(&called) == 1 }, time.Second, 10*time.Millisecond)
}

// TestOnClientConnect_Error 验证连接回调返回错误时被记录
func TestOnClientConnect_Error(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.OnClientConnect(func(ctx context.Context, client *Client) error {
		return errors.New("connect rejected")
	})

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	client := makeTestClient("c-conn-err", "u-conn-err")
	// 回调返回错误不应 panic
	assert.NotPanics(t, func() {
		hub.register <- client
		time.Sleep(100 * time.Millisecond)
	})
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-08 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-08 03:09:16
 * @FilePath: \go-wsc\hub\send_route_dimension_test.go
 * @Description: 发消息路由维度修复的回归测试
 *
 * 覆盖两处修复：
 *   1. SendToGroup 注入 groupID 到 ctx，群组离线消息存 ns:groupID:userID 维度
 *   2. handleForwardableMessage P2P 转发覆盖发送方 group 为 nil，存 P2P 维度
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendToGroup_InjectGroupForOfflineDimension 群组消息应注入 groupID 到 ctx
// 保证离线成员消息存到 ns:groupID:userID 维度（而非 P2P 的 ns:默认组:userID）
// 修复前：SendToGroup 未注入 group，离线成员消息存 P2P 队列，group 归属丢失
func TestSendToGroup_InjectGroupForOfflineDimension(t *testing.T) {
	hub, groupRepo, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	handler := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(handler)

	ctx := context.Background()
	require.NoError(t, groupRepo.CreateGroup(ctx, &Group{GroupID: "g-dim", Namespace: "tenantA", OwnerID: "owner1"}))
	require.NoError(t, hub.AddGroupMembers(ctx, "tenantA", "g-dim", []string{"u-offline-dim"}))

	go hub.Run()
	defer hub.Shutdown()
	time.Sleep(100 * time.Millisecond)

	msg := makeGroupMessage("owner1")
	result := hub.SendToGroup(routing.WithNamespaceGroupIDs(ctx, "tenantA", []string{"g-dim"}), msg, false)
	require.NotNil(t, result)

	ns, groups, count := handler.snapshot()
	require.Equal(t, 1, count, "离线成员应触发 StoreOfflineMessage")
	assert.Equal(t, "tenantA", ns, "namespace 应为 tenantA")
	assert.Equal(t, []string{"g-dim"}, groups, "群组消息应注入 groupID，离线存群组维度 ns:groupID:userID")
}

// TestHandleForwardableMessage_P2PGroupNil P2P 转发应覆盖发送方 group 为 nil
// handleTextMessage 注入发送方 group 仅用于观察者通知；P2P 离线存储必须按 P2P 维度
// 修复前：透传发送方 group，离线消息存 ns:senderGroup:receiver，接收方上线取不到 → 丢消息
func TestHandleForwardableMessage_P2PGroupNil(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	handler := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(handler)
	defer hub.SafeShutdown()

	// 模拟 handleTextMessage 源头注入发送方 namespace+group
	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-sender", []string{"g-sender"})

	msg := makeGroupMessage("sender")
	msg.Receiver = "u-offline-p2p"

	hub.handleForwardableMessage(ctx, msg)

	ns, groups, count := handler.snapshot()
	require.Equal(t, 1, count, "P2P 转发离线用户应触发 StoreOfflineMessage")
	assert.Equal(t, "ns-sender", ns, "namespace 保留发送方")
	assert.Empty(t, groups, "P2P 转发应覆盖发送方 group 为 nil，存 P2P 维度（ns:默认组:userID）")
}

// TestSendToUserWithRetry_P2PNotUseSenderGroup 直接 P2P 发送不携带 group
// 验证 P2P 调用方传 group=nil 时，离线存储按 P2P 维度（与群组维度区分）
func TestSendToUserWithRetry_P2PNotUseSenderGroup(t *testing.T) {
	hub := NewHub(smallRetryHubConfig(2))
	handler := &nsCapturingOfflineHandler{}
	hub.SetOfflineMessageHandler(handler)
	defer hub.SafeShutdown()

	// P2P 调用方正确传入 group=nil（P2P 不捆绑 group）
	ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns-p2p", nil)
	hub.SendToUserWithRetry(ctx, "u-offline-direct", makeGroupMessage("sender"))

	ns, groups, count := handler.snapshot()
	require.Equal(t, 1, count)
	assert.Equal(t, "ns-p2p", ns)
	assert.Empty(t, groups, "P2P 发送 group 应为空")
}

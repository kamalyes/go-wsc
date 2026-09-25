/*
* @Author: kamalyes 501893067@qq.com
* @Date: 2026-08-08 00:00:00
* @LastEditors: kamalyes 501893067@qq.com
* @LastEditTime: 2026-09-22 11:58:00
* @FilePath: \go-wsc\messaging\send_route_dimension_test.go
* @Description: 发消息路由维度修复的回归测试

* 覆盖三处维度约束（断言从离线队列 key 的 ns/group 段读取）：
*   1. Deliver 群组路径注入 groupID，群组离线消息存 ns:groupID:userID 维度
*   2. handleForwardableMessage P2P 转发存 P2P 维度（group 补默认组，不携带发送方群组）
*   3. SendToUserWithRetry 直接 P2P 发送不携带 group

* Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package messaging

import (
	"context"
	"testing"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSendToGroup_InjectGroupForOfflineDimension 群组消息应注入 groupID
// 保证离线成员消息存到 ns:groupID:userID 维度（而非 P2P 的 ns:默认组:userID）
// 修复前：群组投递未注入 group，离线成员消息存 P2P 队列，group 归属丢失
func TestSendToGroup_InjectGroupForOfflineDimension(t *testing.T) {
	m, host := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline
	host.groupRepo = newFakeGroupStore()

	ctx := context.Background()
	require.NoError(t, host.groupRepo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-dim", []string{"u-offline-dim"}))

	msg := makeGroupMessage("owner1")
	msg.RequireAck = true
	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantA").WithGroupIDs([]string{"g-dim"}).Inject(ctx)
	result := m.Deliver(groupCtx, msg, false)
	require.NotNil(t, result)

	require.Equal(t, 1, log.getStoreCalled(), "离线成员应触发 StoreOfflineMessage")
	ns, groupID := log.lastKeyDimension()
	assert.Equal(t, "tenantA", ns, "namespace 应为 tenantA")
	assert.Equal(t, "g-dim", groupID, "群组消息应注入 groupID，离线存群组维度 ns:groupID:userID")
}

// TestHandleForwardableMessage_P2PGroupNil P2P 转发应存 P2P 维度
// handleTextMessage 注入发送方 group 仅用于观察者通知；P2P 离线存储必须按 P2P 维度
// 修复前：透传发送方 group，离线消息存 ns:senderGroup:receiver，接收方上线取不到 → 丢消息
func TestHandleForwardableMessage_P2PGroupNil(t *testing.T) {
	m, _ := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline

	// 模拟 handleTextMessage 源头注入发送方 namespace+group
	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-sender").WithGroupIDs([]string{"g-sender"}).Inject(context.Background())

	msg := makeGroupMessage("sender")
	msg.Receiver = "u-offline-p2p"

	m.handleForwardableMessage(ctx, msg)

	require.Equal(t, 1, log.getStoreCalled(), "P2P 转发离线用户应触发 StoreOfflineMessage")
	ns, groupID := log.lastKeyDimension()
	assert.Equal(t, "ns-sender", ns, "namespace 保留发送方")
	assert.Equal(t, constants.DefaultGroupID, groupID, "P2P 转发应存 P2P 维度（group 补默认组，不携带发送方群组）")
}

// TestSendToUserWithRetry_P2PNotUseSenderGroup 直接 P2P 发送不携带 group
// 验证 P2P 调用方传 group=nil 时，离线存储按 P2P 维度（与群组维度区分）
func TestSendToUserWithRetry_P2PNotUseSenderGroup(t *testing.T) {
	m, _ := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline

	// P2P 调用方正确传入 group=nil（P2P 不捆绑 group）
	ctx := routing.NewRoute().WithAppID("").WithNamespace("ns-p2p").WithGroupIDs(nil).Inject(context.Background())
	m.SendToUserWithRetry(ctx, "u-offline-direct", makeGroupMessage("sender"))

	require.Equal(t, 1, log.getStoreCalled())
	ns, groupID := log.lastKeyDimension()
	assert.Equal(t, "ns-p2p", ns)
	assert.Equal(t, constants.DefaultGroupID, groupID, "P2P 发送离线存储应补默认组维度")
}

// TestDeliverGroupNamespaceRequired 群组投递 ns 是必要参数：缺失直接报错，不做默认值兜底
// 修复前：群组分支静默 EnsureRouteDefaults 补默认 ns，调用方漏传路由时消息悄悄投到 default 维度（跨租户隐患）
func TestDeliverGroupNamespaceRequired(t *testing.T) {
	m, host := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline
	host.groupRepo = newFakeGroupStore()

	// 漏传必要路由参数：只给 groupIDs 不给 namespace
	groupCtx := routing.NewRoute().WithAppID("").WithNamespace("").WithGroupIDs([]string{"g-any"}).Inject(context.Background())

	// 可靠投递分支（RequireAck=true）与 fire-and-forget 分支（RequireAck=false）执行同一契约
	msgReliable := makeGroupMessage("owner1")
	msgReliable.RequireAck = true
	resultReliable := m.Deliver(groupCtx, msgReliable, false)
	require.NotNil(t, resultReliable)
	require.Len(t, resultReliable.Errors, 1, "ns 缺失应报 ErrRouteNamespaceMissing")
	assert.Equal(t, models.ErrRouteNamespaceMissing, resultReliable.Errors[0])
	assert.Equal(t, 0, resultReliable.TotalMembers, "ns 缺失应短路在群组成员查询之前")

	msgFireForget := makeGroupMessage("owner2")
	resultFireForget := m.Deliver(groupCtx, msgFireForget, false)
	require.NotNil(t, resultFireForget)
	require.Len(t, resultFireForget.Errors, 1, "fire-and-forget 分支同样报 ErrRouteNamespaceMissing")
	assert.Equal(t, models.ErrRouteNamespaceMissing, resultFireForget.Errors[0])

	assert.Equal(t, 0, log.getStoreCalled(), "ns 缺失短路在成员获取之前，不应产生任何投递/离线转存")
}

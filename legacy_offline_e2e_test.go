/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-21 21:07:56
 * @FilePath: \go-wsc\legacy_offline_e2e_test.go
 * @Description: 老系统不传 appid/namespace/group 的离线消息端到端验证
 *
 * 验证要点：
 *   1. 老系统 context.Background() 下离线消息 store 与 drain 的 Redis key 维度一致（不丢消息）
 *   2. EnsureRouteDefaults 兜底 DefaultAppID/DefaultNamespace 后，P2P 补默认组维度一致
 *   3. pushOfflineMessagesOnConnect 阶段1 的 WithGroup("") 不产生伪群组条目
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"testing"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLegacySystem_OfflineStoreDrainKeyConsistency 老系统 context.Background() 下
// 离线消息 store 与 drain 的 Redis key 维度一致（不丢消息）
// 核心验证：EnsureRouteDefaults 兜底 DefaultAppID/DefaultNamespace + P2P group 补 DefaultGroupID，
// store 落点 = drain 取点
func TestLegacySystem_OfflineStoreDrainKeyConsistency(t *testing.T) {
	// 复用离线消息测试的 handler 上下文（已注入 DefaultNamespace + group=nil）
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	// 模拟老系统：用 context.Background()（全空），经 EnsureRouteDefaults 兜底
	// 验证 SendToUserWithRetry 入口的 EnsureRouteDefaults 行为
	legacyCtx := routing.EnsureRouteDefaults(context.Background())
	require.Equal(t, models.DefaultAppID, routing.AppIDFromContext(legacyCtx), "EnsureRouteDefaults 应兜底 DefaultAppID")
	require.Equal(t, models.DefaultNamespace, routing.NamespaceFromContext(legacyCtx), "EnsureRouteDefaults 应兜底 DefaultNamespace")
	require.Nil(t, routing.GroupIDsFromContext(legacyCtx), "P2P 场景 groupIDs 应为 nil")

	// store 2 条离线消息（用兜底后的 ctx，与 SendToUserWithRetry 入口一致）
	for i := 0; i < 2; i++ {
		msg := tc.createTestMessage(userID)
		msg.MessageID = tc.idGen.GenerateCorrelationID()
		require.NoError(t, tc.handler.StoreOfflineMessage(legacyCtx, userID, msg))
	}

	// drain 取回（用同维度 ctx，模拟 pushOfflineMessagesOnConnect 阶段1 的 P2P 队列 drain）
	// pushOfflineMessagesOnConnect 对 P2P 队列用 WithGroup("") 注入 groupIDs=[""]
	// FirstGroupIDFromContext 返回 ""，normalizeGroupID("") 补 DefaultGroupID，与 store 时一致
	p2pDrainCtx := routing.NewRoute().
		WithAppID(models.DefaultAppID).
		WithNamespace(models.DefaultNamespace).
		WithGroup(""). // 模拟 pushOfflineMessagesOnConnect 阶段1 的 P2P 队列（gid=""）
		Inject(context.Background())

	drained, err := tc.handler.DrainOfflineQueue(p2pDrainCtx, userID, 0)
	require.NoError(t, err)
	assert.Len(t, drained, 2, "老系统 ctx 下 store/drain key 应一致，取回全部消息")

	// 再 drain 应为空（已排空）
	drained2, _ := tc.handler.DrainOfflineQueue(p2pDrainCtx, userID, 0)
	assert.Empty(t, drained2, "排空后再 drain 应无消息")
}

// TestLegacySystem_EmptyGroupID_P2PQueueNotBogus 老系统不传 GroupID，
// pushOfflineMessagesOnConnect 阶段1 的 groupIDs 首项 "" 经 WithGroup("") 后，
// DrainOfflineQueue 的 FirstGroupIDFromContext 应返回 ""（与 store 路径 groupIDs=nil → FirstGroupID="" 一致）
// normalizeGroupID("") 补 DefaultGroupID，保证 store/drain 维度一致
func TestLegacySystem_EmptyGroupID_P2PQueueNotBogus(t *testing.T) {
	// 验证 routing 层：WithGroup("") 产生的 groupIDs 的 FirstGroupIDFromContext 返回 ""
	ctx := routing.NewRoute().
		WithAppID(models.DefaultAppID).
		WithNamespace(models.DefaultNamespace).
		WithGroup(""). // 模拟 pushOfflineMessagesOnConnect 阶段1 的 P2P 队列（gid=""）
		Inject(context.Background())

	firstGID := routing.FirstGroupIDFromContext(ctx)
	assert.Equal(t, "", firstGID, "WithGroup(\"\") 的 FirstGroupID 应为空串，经 normalizeGroupID 补 DefaultGroupID")
	// 关键：与 store 路径（groupIDs=nil → FirstGroupID=""）一致，两者 normalizeGroupID 后都补 DefaultGroupID
	// 保证 P2P 消息 store 落点与 drain 取点 key 一致
}

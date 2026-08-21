/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-09 00:16:29
 * @FilePath: \go-wsc\handler\offline_message_test.go
 * @Description: 离线消息处理器白盒单元测试（覆盖 handler/offline_message.go）
 *
 * 保证 Redis key 与 MySQL group_id 维度一致
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package handler

import (
	"context"
	"testing"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
)

// queueKeyFromCtx 从 ctx 提取 (appID, ns, firstGroupID) 并构造 Redis 队列 key
// appID 归一化（空→DefaultAppID，最上层隔离维度必填），namespace 保持原值不归一化（与 resolveOfflineRoute 一致）
func queueKeyFromCtx(ctx context.Context, userID string) string {
	// routing.AppIDFromContext 内部已 constants.NormalizeAppID 归一化，必返回 DefaultAppID，无需二次兜底
	appID := routing.AppIDFromContext(ctx)
	ns := routing.NamespaceFromContext(ctx)
	gid := routing.FirstGroupIDFromContext(ctx)
	return queueKey(appID, ns, constants.NormalizeGroupID(gid), userID)
}

// ============================================================================
// queueKey 单元测试
// ============================================================================

// TestQueueKey 验证 Redis 队列 key 构造：P2P 补默认组，三段非空格式统一
// queueKey 是包级纯函数，不依赖 HybridOfflineMessageHandler 实例
func TestQueueKey(t *testing.T) {
	t.Run("P2P消息补默认组", func(t *testing.T) {
		ctx := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs(nil).Inject(context.Background())
		assert.Equal(t, constants.DefaultAppID+":ns1:"+constants.DefaultGroupID+":u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("群组消息保留group", func(t *testing.T) {
		ctx := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs([]string{"g-100"}).Inject(context.Background())
		assert.Equal(t, constants.DefaultAppID+":ns1:g-100:u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("namespace空P2P补默认组", func(t *testing.T) {
		// namespace 为空时 key 中间段为空，appID:__default_gp__:u1（namespace 不归一化，保持空串）
		ctx := routing.NewRoute().WithAppID("").WithNamespace("").WithGroupIDs(nil).Inject(context.Background())
		assert.Equal(t, constants.DefaultAppID+"::"+constants.DefaultGroupID+":u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("显式传DefaultGroupID不二次归一化", func(t *testing.T) {
		ctx := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs([]string{constants.DefaultGroupID}).Inject(context.Background())
		assert.Equal(t, constants.DefaultAppID+":ns1:"+constants.DefaultGroupID+":u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("多group取首个", func(t *testing.T) {
		ctx := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs([]string{"g-1", "g-2"}).Inject(context.Background())
		assert.Equal(t, constants.DefaultAppID+":ns1:g-1:u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("store与drain同ctx产出同key", func(t *testing.T) {
		// 保证 store 落点与 drain 取点一致，不会因 key 不一致丢消息
		ctx := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs(nil).Inject(context.Background())
		storeKey := queueKeyFromCtx(ctx, "u1")
		drainKey := queueKeyFromCtx(ctx, "u1")
		assert.Equal(t, storeKey, drainKey)
	})

	t.Run("不同namespace隔离", func(t *testing.T) {
		ctxA := routing.NewRoute().WithAppID("").WithNamespace("nsA").WithGroupIDs(nil).Inject(context.Background())
		ctxB := routing.NewRoute().WithAppID("").WithNamespace("nsB").WithGroupIDs(nil).Inject(context.Background())
		assert.NotEqual(t, queueKeyFromCtx(ctxA, "u1"), queueKeyFromCtx(ctxB, "u1"))
	})

	t.Run("不同group隔离", func(t *testing.T) {
		ctxP2P := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs(nil).Inject(context.Background())
		ctxGroup := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs([]string{"g-100"}).Inject(context.Background())
		assert.NotEqual(t, queueKeyFromCtx(ctxP2P, "u1"), queueKeyFromCtx(ctxGroup, "u1"))
	})
}

// TestQueueKey_P2PAndGroupDimensionConsistency 验证 P2P 与群组消息的 key 维度互不冲突
// P2P 补 DefaultGroupID 后，与真实群组（名为 DefaultGroupID 的除外）的 key 不会碰撞
func TestQueueKey_P2PAndGroupDimensionConsistency(t *testing.T) {
	ctxP2P := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs(nil).Inject(context.Background())
	ctxGroup := routing.NewRoute().WithAppID("").WithNamespace("ns1").WithGroupIDs([]string{"g-real"}).Inject(context.Background())

	p2pKey := queueKeyFromCtx(ctxP2P, "u1")
	groupKey := queueKeyFromCtx(ctxGroup, "u1")

	assert.NotEqual(t, p2pKey, groupKey, "P2P(默认组)与真实群组的 key 必须隔离")
	assert.Contains(t, p2pKey, constants.DefaultGroupID)
	assert.Contains(t, groupKey, "g-real")
}

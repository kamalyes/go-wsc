/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-09 00:16:29
 * @FilePath: \go-wsc\handler\offline_message_test.go
 * @Description: 离线消息处理器白盒单元测试（覆盖 handler/offline_message.go）
 *
 * 重点覆盖 P2P 补默认组（normalizeGroupID）与 Redis 队列 key 构造（queueKey），
 * 保证 Redis key 与 MySQL group_id 维度一致。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package handler

import (
	"context"
	"testing"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
)

// queueKeyFromCtx 从 ctx 提取 (ns, firstGroupID) 并构造 Redis 队列 key
// 与生产代码 storeToRedis/drainFromRedis 的调用路径一致：queueKey(ns, normalizeGroupID(gid), userID)
func queueKeyFromCtx(ctx context.Context, userID string) string {
	ns := routing.NamespaceFromContext(ctx)
	gid := routing.FirstGroupIDFromContext(ctx)
	return queueKey(ns, normalizeGroupID(gid), userID)
}

// ============================================================================
// normalizeGroupID 单元测试
// ============================================================================

// TestNormalizeGroupID 验证 groupID 归一化：空串补 DefaultGroupID，非空保持原值
func TestNormalizeGroupID(t *testing.T) {
	t.Run("空串补默认组", func(t *testing.T) {
		assert.Equal(t, models.DefaultGroupID, normalizeGroupID(""))
	})
	t.Run("非空保持原值", func(t *testing.T) {
		assert.Equal(t, "g-100", normalizeGroupID("g-100"))
	})
	t.Run("DefaultGroupID本身不二次归一化", func(t *testing.T) {
		assert.Equal(t, models.DefaultGroupID, normalizeGroupID(models.DefaultGroupID))
	})
}

// ============================================================================
// queueKey 单元测试
// ============================================================================

// TestQueueKey 验证 Redis 队列 key 构造：P2P 补默认组，三段非空格式统一
// queueKey 是包级纯函数，不依赖 HybridOfflineMessageHandler 实例
func TestQueueKey(t *testing.T) {
	t.Run("P2P消息补默认组", func(t *testing.T) {
		ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns1", nil)
		assert.Equal(t, "ns1:"+models.DefaultGroupID+":u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("群组消息保留group", func(t *testing.T) {
		ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns1", []string{"g-100"})
		assert.Equal(t, "ns1:g-100:u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("namespace空P2P补默认组", func(t *testing.T) {
		// namespace 为空时 key 以冒号开头，三段结构仍保持（:__default_gp__:u1）
		ctx := routing.WithNamespaceGroupIDs(context.Background(), "", nil)
		assert.Equal(t, ":"+models.DefaultGroupID+":u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("显式传DefaultGroupID不二次归一化", func(t *testing.T) {
		ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns1", []string{models.DefaultGroupID})
		assert.Equal(t, "ns1:"+models.DefaultGroupID+":u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("多group取首个", func(t *testing.T) {
		ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns1", []string{"g-1", "g-2"})
		assert.Equal(t, "ns1:g-1:u1", queueKeyFromCtx(ctx, "u1"))
	})

	t.Run("store与drain同ctx产出同key", func(t *testing.T) {
		// 保证 store 落点与 drain 取点一致，不会因 key 不一致丢消息
		ctx := routing.WithNamespaceGroupIDs(context.Background(), "ns1", nil)
		storeKey := queueKeyFromCtx(ctx, "u1")
		drainKey := queueKeyFromCtx(ctx, "u1")
		assert.Equal(t, storeKey, drainKey)
	})

	t.Run("不同namespace隔离", func(t *testing.T) {
		ctxA := routing.WithNamespaceGroupIDs(context.Background(), "nsA", nil)
		ctxB := routing.WithNamespaceGroupIDs(context.Background(), "nsB", nil)
		assert.NotEqual(t, queueKeyFromCtx(ctxA, "u1"), queueKeyFromCtx(ctxB, "u1"))
	})

	t.Run("不同group隔离", func(t *testing.T) {
		ctxP2P := routing.WithNamespaceGroupIDs(context.Background(), "ns1", nil)
		ctxGroup := routing.WithNamespaceGroupIDs(context.Background(), "ns1", []string{"g-100"})
		assert.NotEqual(t, queueKeyFromCtx(ctxP2P, "u1"), queueKeyFromCtx(ctxGroup, "u1"))
	})
}

// TestQueueKey_P2PAndGroupDimensionConsistency 验证 P2P 与群组消息的 key 维度互不冲突
// P2P 补 DefaultGroupID 后，与真实群组（名为 DefaultGroupID 的除外）的 key 不会碰撞
func TestQueueKey_P2PAndGroupDimensionConsistency(t *testing.T) {
	ctxP2P := routing.WithNamespaceGroupIDs(context.Background(), "ns1", nil)
	ctxGroup := routing.WithNamespaceGroupIDs(context.Background(), "ns1", []string{"g-real"})

	p2pKey := queueKeyFromCtx(ctxP2P, "u1")
	groupKey := queueKeyFromCtx(ctxGroup, "u1")

	assert.NotEqual(t, p2pKey, groupKey, "P2P(默认组)与真实群组的 key 必须隔离")
	assert.Contains(t, p2pKey, models.DefaultGroupID)
	assert.Contains(t, groupKey, "g-real")
}

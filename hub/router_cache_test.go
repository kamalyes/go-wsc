/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-08 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-08 00:09:16
 * @FilePath: \go-wsc\hub\router_cache_test.go
 * @Description: 分布式路由缓存白盒单元测试（覆盖 hub/router_cache.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// TestRouterCache_NilReceiver 验证 nil receiver 时所有方法安全返回
func TestRouterCache_NilReceiver(t *testing.T) {
	var r *RouterCache

	// nil receiver 不应 panic
	assert.NotPanics(t, func() {
		nodes, err := r.GetUserNodes(context.Background(), "u1")
		assert.Nil(t, nodes)
		assert.Nil(t, err)
	})

	assert.NotPanics(t, func() {
		err := r.InvalidateUser(context.Background(), "u1")
		assert.Nil(t, err)
	})

	assert.NotPanics(t, func() {
		err := r.SetUserNodes(context.Background(), "u1", []string{"n1"})
		assert.Nil(t, err)
	})

	assert.NotPanics(t, func() {
		r.Stop()
	})
}

// newTestRouterCache 用 miniredis 构造路由缓存
func newTestRouterCache(t *testing.T) (*RouterCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	rc := NewRouterCache(client, nil, wscconfig.DefaultRouterCacheConfig())
	return rc, mr
}

// TestRouterCache_SetGetInvalidate 验证设置/获取/失效路由缓存
func TestRouterCache_SetGetInvalidate(t *testing.T) {
	rc, _ := newTestRouterCache(t)
	defer rc.Stop()
	ctx := context.Background()

	// 设置用户节点
	require.NoError(t, rc.SetUserNodes(ctx, "u-rc1", []string{"nodeA", "nodeB"}))

	// 获取应返回设置的节点
	nodes, err := rc.GetUserNodes(ctx, "u-rc1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"nodeA", "nodeB"}, nodes)

	// 失效后获取应返回 nil
	require.NoError(t, rc.InvalidateUser(ctx, "u-rc1"))
	// 失效后 BatchLoader 回源（onlineRepo=nil 返回 nil）→ 缓存空切片
	nodes, err = rc.GetUserNodes(ctx, "u-rc1")
	require.NoError(t, err)
	assert.Empty(t, nodes)
}

// TestRouterCache_SetEmptyNodes 验证设置空节点列表等价于删除
func TestRouterCache_SetEmptyNodes(t *testing.T) {
	rc, _ := newTestRouterCache(t)
	defer rc.Stop()
	ctx := context.Background()

	require.NoError(t, rc.SetUserNodes(ctx, "u-rc2", []string{"nodeA"}))
	nodes, err := rc.GetUserNodes(ctx, "u-rc2")
	require.NoError(t, err)
	assert.NotEmpty(t, nodes)

	// 设置空 → 删除
	require.NoError(t, rc.SetUserNodes(ctx, "u-rc2", []string{}))
	nodes, err = rc.GetUserNodes(ctx, "u-rc2")
	require.NoError(t, err)
	assert.Empty(t, nodes)
}

// TestRouterCache_GetUser_NotFound 验证未设置的用户返回空
func TestRouterCache_GetUser_NotFound(t *testing.T) {
	rc, _ := newTestRouterCache(t)
	defer rc.Stop()
	ctx := context.Background()

	nodes, err := rc.GetUserNodes(ctx, "u-notexist")
	require.NoError(t, err)
	// BatchLoader 回源 nil → 缓存空切片
	assert.Empty(t, nodes)
}

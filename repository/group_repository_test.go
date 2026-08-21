/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-22 14:52:49
 * @FilePath: \go-wsc\repository\group_repository_test.go
 * @Description: 群组仓库测试 - 基于 miniredis 内存 Redis 验证群组元信息、成员管理、
 * 反向索引及命名空间隔离等行为
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestRepo 创建基于 miniredis（纯内存 Redis mock）的群组仓库
// 返回群组仓库实例与清理函数；miniredis 通过 RunT 自动注册清理
func setupTestRepo(t *testing.T) (GroupRepository, func()) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	repo := NewRedisGroupRepository(client, "wsc:test:group:")
	cleanup := func() { _ = client.Close() }
	return repo, cleanup
}

// TestCreateGroup 验证群组创建与默认命名空间填充
func TestCreateGroup(t *testing.T) {
	ctx := context.Background()

	t.Run("创建群组后可读取且字段一致", func(t *testing.T) {
		repo, cleanup := setupTestRepo(t)
		defer cleanup()

		now := time.Now().Truncate(time.Second)
		g := &Group{
			GroupID:    "g-create",
			Namespace:  "tenantA",
			Name:       "测试群",
			OwnerID:    "u-owner",
			MaxMembers: 100,
			CreatedAt:  now,
			Metadata:   map[string]interface{}{"k": "v"},
		}
		require.NoError(t, repo.CreateGroup(ctx, g))

		got, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "g-create")
		require.NoError(t, err)
		assert.Equal(t, "g-create", got.GroupID)
		assert.Equal(t, "tenantA", got.Namespace)
		assert.Equal(t, "测试群", got.Name)
		assert.Equal(t, "u-owner", got.OwnerID)
		assert.Equal(t, 100, got.MaxMembers)
		assert.Equal(t, now.Unix(), got.CreatedAt.Unix())
		assert.Equal(t, "v", got.Metadata["k"])

		// 创建后应出现在该命名空间的群组集合中
		namespaceGroups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "tenantA")
		require.NoError(t, err)
		assert.Contains(t, namespaceGroups, "g-create")
	})

	t.Run("Namespace 为空时归入 default 命名空间", func(t *testing.T) {
		repo, cleanup := setupTestRepo(t)
		defer cleanup()

		g := &Group{
			GroupID: "g-default-tenant",
			Name:    "默认命名空间群",
			OwnerID: "u-owner",
		}
		require.NoError(t, repo.CreateGroup(ctx, g))

		// 应能在 DefaultNamespace 下查到（CreateGroup 内部按 GetNamespace 解析 key）
		got, err := repo.GetGroup(ctx, constants.DefaultAppID, constants.DefaultNamespace, "g-default-tenant")
		require.NoError(t, err)
		// 元信息按原值序列化，Namespace 字段保持空串，但语义上归入 DefaultNamespace
		assert.Equal(t, constants.DefaultNamespace, got.GetNamespace(), "GetNamespace 应解析为 DefaultNamespace")

		// 不应在其他命名空间下查到
		_, err = repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "g-default-tenant")
		assert.ErrorIs(t, err, models.ErrGroupNotFound)

		// DefaultNamespace 命名空间群组集合应包含该群组
		namespaceGroups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, constants.DefaultNamespace)
		require.NoError(t, err)
		assert.Contains(t, namespaceGroups, "g-default-tenant")
	})

	t.Run("CreatedAt 为零值时自动填充当前时间", func(t *testing.T) {
		repo, cleanup := setupTestRepo(t)
		defer cleanup()

		before := time.Now()
		g := &Group{GroupID: "g-ts", Namespace: "tenantA", OwnerID: "u-owner"}
		require.NoError(t, repo.CreateGroup(ctx, g))

		got, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "g-ts")
		require.NoError(t, err)
		assert.False(t, got.CreatedAt.IsZero(), "CreatedAt 应被自动填充")
		assert.True(t, got.CreatedAt.After(before.Add(-time.Second)), "CreatedAt 应不早于创建前")
	})
}

// TestGetGroup_NotFound 验证获取不存在的群组返回 models.ErrGroupNotFound
func TestGetGroup_NotFound(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	_, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "not-exist")
	assert.ErrorIs(t, err, models.ErrGroupNotFound, "不存在的群组应返回 models.ErrGroupNotFound")
}

// TestDisbandGroup 验证解散群组后元信息、成员集合、反向索引与命名空间索引均被清理
func TestDisbandGroup(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	// 创建群组并加入成员
	g := &Group{GroupID: "g-disband", Namespace: "tenantA", Name: "解散群", OwnerID: "u-owner"}
	require.NoError(t, repo.CreateGroup(ctx, g))
	require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-disband", []string{"u1", "u2", "u3"}))

	// 解散前确认成员与反向索引存在
	members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "g-disband")
	require.NoError(t, err)
	assert.Len(t, members, 3)
	for _, u := range []string{"u1", "u2", "u3"} {
		ok, err := repo.IsMember(ctx, constants.DefaultAppID, "tenantA", "g-disband", u)
		require.NoError(t, err)
		assert.True(t, ok)
	}

	// 执行解散
	require.NoError(t, repo.DisbandGroup(ctx, constants.DefaultAppID, "tenantA", "g-disband"))

	// 元信息应被删除
	_, err = repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "g-disband")
	assert.ErrorIs(t, err, models.ErrGroupNotFound, "解散后元信息应被删除")

	// 成员集合应被清空
	members, err = repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "g-disband")
	require.NoError(t, err)
	assert.Empty(t, members, "解散后成员集合应被清空")

	// 成员数量应为 0
	cnt, err := repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "g-disband")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)

	// 各成员反向索引应被清理
	for _, u := range []string{"u1", "u2", "u3"} {
		groups, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantA", u)
		require.NoError(t, err)
		assert.NotContains(t, groups, "g-disband", "解散后成员反向索引应被清理")
	}

	// 命名空间群组集合应不再包含该群组
	namespaceGroups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "tenantA")
	require.NoError(t, err)
	assert.NotContains(t, namespaceGroups, "g-disband", "解散后应从命名空间群组集合移除")
}

// TestAddMembers 验证添加成员后成员集合与反向索引同步更新
func TestAddMembers(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g-add", Namespace: "tenantA", OwnerID: "u-owner"}))

	t.Run("添加成员后成员集合与反向索引同步更新", func(t *testing.T) {
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-add", []string{"u1", "u2"}))

		// 成员集合应包含新增成员
		members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "g-add")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1", "u2"}, members)

		// 反向索引：每个用户应能查到该群组
		for _, u := range []string{"u1", "u2"} {
			groups, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantA", u)
			require.NoError(t, err)
			assert.Contains(t, groups, "g-add")
		}
	})

	t.Run("重复添加成员应为幂等操作", func(t *testing.T) {
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-add", []string{"u1"}))
		cnt, err := repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "g-add")
		require.NoError(t, err)
		assert.Equal(t, int64(2), cnt, "重复添加不应增加成员数")
	})

	t.Run("空成员列表为空操作且不报错", func(t *testing.T) {
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-add", []string{}))
	})
}

// TestRemoveMembers 验证移除成员后成员集合与反向索引同步清理
func TestRemoveMembers(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g-rm", Namespace: "tenantA", OwnerID: "u-owner"}))
	require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-rm", []string{"u1", "u2", "u3"}))

	// 移除部分成员
	require.NoError(t, repo.RemoveMembers(ctx, constants.DefaultAppID, "tenantA", "g-rm", []string{"u1", "u2"}))

	// 剩余成员应为 u3
	members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "g-rm")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u3"}, members)

	cnt, err := repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "g-rm")
	require.NoError(t, err)
	assert.Equal(t, int64(1), cnt)

	// 被移除成员不再属于群组
	for _, u := range []string{"u1", "u2"} {
		ok, err := repo.IsMember(ctx, constants.DefaultAppID, "tenantA", "g-rm", u)
		require.NoError(t, err)
		assert.False(t, ok, "%s 应已不再是成员", u)

		// 反向索引应同步清理
		groups, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantA", u)
		require.NoError(t, err)
		assert.NotContains(t, groups, "g-rm", "%s 的反向索引应被清理", u)
	}

	// 保留成员仍属于群组且反向索引仍在
	ok, err := repo.IsMember(ctx, constants.DefaultAppID, "tenantA", "g-rm", "u3")
	require.NoError(t, err)
	assert.True(t, ok)
	groups, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantA", "u3")
	require.NoError(t, err)
	assert.Contains(t, groups, "g-rm")

	// 移除空列表不应报错
	require.NoError(t, repo.RemoveMembers(ctx, constants.DefaultAppID, "tenantA", "g-rm", []string{}))
}

// TestGetMembers 验证获取群组全部成员
func TestGetMembers(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g-members", Namespace: "tenantA", OwnerID: "u-owner"}))

	t.Run("无成员时返回空切片", func(t *testing.T) {
		members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "g-members")
		require.NoError(t, err)
		assert.Empty(t, members)
	})

	t.Run("添加多个成员后全部返回", func(t *testing.T) {
		added := []string{"u1", "u2", "u3", "u4", "u5"}
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-members", added))

		members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "g-members")
		require.NoError(t, err)
		assert.ElementsMatch(t, added, members, "应返回全部成员（顺序无关）")
	})

	t.Run("不存在的群组返回空切片而非报错", func(t *testing.T) {
		members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "not-exist")
		require.NoError(t, err)
		assert.Empty(t, members)
	})
}

// TestGetUserGroups 验证用户加入多个群组后可全部查询
func TestGetUserGroups(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	// 用户加入多个群组
	for _, gid := range []string{"g1", "g2", "g3"} {
		require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: gid, Namespace: "tenantA", OwnerID: "u-owner"}))
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", gid, []string{"userX"}))
	}

	groups, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantA", "userX")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"g1", "g2", "g3"}, groups, "应返回用户加入的全部群组")

	// 未加入任何群组的用户应返回空
	empty, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantA", "nobody")
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestIsMember 验证成员与非成员的判定
func TestIsMember(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g-is", Namespace: "tenantA", OwnerID: "u-owner"}))
	require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-is", []string{"u-in"}))

	t.Run("成员返回 true", func(t *testing.T) {
		ok, err := repo.IsMember(ctx, constants.DefaultAppID, "tenantA", "g-is", "u-in")
		require.NoError(t, err)
		assert.True(t, ok)
	})

	t.Run("非成员返回 false", func(t *testing.T) {
		ok, err := repo.IsMember(ctx, constants.DefaultAppID, "tenantA", "g-is", "u-out")
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("不存在的群组对任意用户返回 false", func(t *testing.T) {
		ok, err := repo.IsMember(ctx, constants.DefaultAppID, "tenantA", "not-exist", "u-in")
		require.NoError(t, err)
		assert.False(t, ok)
	})
}

// TestGetMemberCount 验证群组成员数量统计
func TestGetMemberCount(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g-count", Namespace: "tenantA", OwnerID: "u-owner"}))

	// 初始成员数为 0
	cnt, err := repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "g-count")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)

	// 添加 3 个成员
	require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-count", []string{"u1", "u2", "u3"}))
	cnt, err = repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "g-count")
	require.NoError(t, err)
	assert.Equal(t, int64(3), cnt)

	// 移除 1 个后应为 2
	require.NoError(t, repo.RemoveMembers(ctx, constants.DefaultAppID, "tenantA", "g-count", []string{"u1"}))
	cnt, err = repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "g-count")
	require.NoError(t, err)
	assert.Equal(t, int64(2), cnt)

	// 不存在的群组成员数为 0
	cnt, err = repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "not-exist")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt)
}

// TestGetNamespaceGroups 验证获取命名空间下全部群组
func TestGetNamespaceGroups(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("命名空间下创建多个群组后全部返回", func(t *testing.T) {
		for _, gid := range []string{"t-g1", "t-g2", "t-g3"} {
			require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: gid, Namespace: "tenantA", OwnerID: "u-owner"}))
		}

		groups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "tenantA")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"t-g1", "t-g2", "t-g3"}, groups)
	})

	t.Run("不同命名空间的群组互不干扰", func(t *testing.T) {
		require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "t-gB", Namespace: "tenantB", OwnerID: "u-owner"}))

		aGroups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "tenantA")
		require.NoError(t, err)
		assert.NotContains(t, aGroups, "t-gB", "tenantB 的群组不应出现在 tenantA")

		bGroups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "tenantB")
		require.NoError(t, err)
		assert.Contains(t, bGroups, "t-gB")
	})

	t.Run("空命名空间返回空切片", func(t *testing.T) {
		empty, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "empty-tenant")
		require.NoError(t, err)
		assert.Empty(t, empty)
	})
}

// TestNamespaceIsolation 验证相同 groupID 在不同命名空间间完全隔离
func TestNamespaceIsolation(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	// 在 tenantA 与 tenantB 下分别创建同名为 g1 的群组，但属主不同
	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: "tenantA", Name: "A群", OwnerID: "ownerA"}))
	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: "tenantB", Name: "B群", OwnerID: "ownerB"}))

	t.Run("元信息按命名空间隔离", func(t *testing.T) {
		gA, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "g1")
		require.NoError(t, err)
		assert.Equal(t, "A群", gA.Name)
		assert.Equal(t, "ownerA", gA.OwnerID)

		gB, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantB", "g1")
		require.NoError(t, err)
		assert.Equal(t, "B群", gB.Name)
		assert.Equal(t, "ownerB", gB.OwnerID)
	})

	t.Run("成员不跨命名空间泄漏", func(t *testing.T) {
		// tenantA 的 g1 加入 userA
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g1", []string{"userA"}))
		// tenantB 的 g1 加入 userB
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantB", "g1", []string{"userB"}))

		// tenantA.g1 成员只应包含 userA
		aMembers, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", "g1")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"userA"}, aMembers)

		// tenantB.g1 成员只应包含 userB
		bMembers, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantB", "g1")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"userB"}, bMembers)

		// userA 不应是 tenantB.g1 的成员
		ok, err := repo.IsMember(ctx, constants.DefaultAppID, "tenantB", "g1", "userA")
		require.NoError(t, err)
		assert.False(t, ok, "userA 不应跨命名空间出现在 tenantB.g1")

		// userB 不应是 tenantA.g1 的成员
		ok, err = repo.IsMember(ctx, constants.DefaultAppID, "tenantA", "g1", "userB")
		require.NoError(t, err)
		assert.False(t, ok, "userB 不应跨命名空间出现在 tenantA.g1")
	})

	t.Run("反向索引按命名空间隔离", func(t *testing.T) {
		// 同名 userA 分别在两个命名空间加入各自的 g1
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantB", "g1", []string{"userA"}))

		aGroups, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantA", "userA")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"g1"}, aGroups, "tenantA 下 userA 只应有一个 g1")

		bGroups, err := repo.GetUserGroups(ctx, constants.DefaultAppID, "tenantB", "userA")
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"g1"}, bGroups, "tenantB 下 userA 也只应有一个 g1")

		// 两个命名空间的成员数量各自独立
		aCnt, err := repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantA", "g1")
		require.NoError(t, err)
		assert.Equal(t, int64(1), aCnt, "tenantA.g1 成员数应为 1")

		bCnt, err := repo.GetMemberCount(ctx, constants.DefaultAppID, "tenantB", "g1")
		require.NoError(t, err)
		assert.Equal(t, int64(2), bCnt, "tenantB.g1 成员数应为 2（userA + userB）")
	})

	t.Run("解散某命名空间群组不影响另一命名空间同名群组", func(t *testing.T) {
		require.NoError(t, repo.DisbandGroup(ctx, constants.DefaultAppID, "tenantA", "g1"))

		// tenantA.g1 应已不存在
		_, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "g1")
		assert.ErrorIs(t, err, models.ErrGroupNotFound)

		// tenantB.g1 应仍存在且成员不变
		gB, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantB", "g1")
		require.NoError(t, err)
		assert.Equal(t, "B群", gB.Name)

		bMembers, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantB", "g1")
		require.NoError(t, err)
		assert.Len(t, bMembers, 2, "tenantB.g1 成员不应受 tenantA 解散影响")
	})

	t.Run("命名空间群组集合按命名空间隔离", func(t *testing.T) {
		// 解散 tenantA.g1 后，tenantA 集合应不再包含 g1
		aGroups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "tenantA")
		require.NoError(t, err)
		assert.NotContains(t, aGroups, "g1")

		// tenantB 集合仍应包含 g1
		bGroups, err := repo.GetNamespaceGroups(ctx, constants.DefaultAppID, "tenantB")
		require.NoError(t, err)
		assert.Contains(t, bGroups, "g1")
	})
}

// TestCreateGroupDuplicate 验证同命名空间下 groupID 唯一性校验
// 重复创建应返回 models.ErrGroupExisted；不同命名空间同名 groupID 不冲突
func TestCreateGroupDuplicate(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g-dup", Namespace: "tenantA", OwnerID: "o1"}))

	t.Run("同命名空间重复创建返回 models.ErrGroupExisted", func(t *testing.T) {
		err := repo.CreateGroup(ctx, &Group{GroupID: "g-dup", Namespace: "tenantA", Name: "覆盖尝试"})
		assert.ErrorIs(t, err, models.ErrGroupExisted, "同命名空间重复创建应返回 models.ErrGroupExisted")

		// 原群组元信息不应被覆盖
		got, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "g-dup")
		require.NoError(t, err)
		assert.Equal(t, "", got.Name, "重复创建被拒绝后元信息不应被覆盖")
	})

	t.Run("不同命名空间同名 groupID 不冲突", func(t *testing.T) {
		err := repo.CreateGroup(ctx, &Group{GroupID: "g-dup", Namespace: "tenantB", Name: "B群"})
		assert.NoError(t, err, "不同命名空间同名 groupID 应创建成功")

		got, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantB", "g-dup")
		require.NoError(t, err)
		assert.Equal(t, "B群", got.Name)
	})
}

// TestGetAllNamespaces 验证获取所有有群组的命名空间ID
func TestGetAllNamespaces(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("无群组时返回空", func(t *testing.T) {
		namespaces, err := repo.GetAllNamespaces(ctx, constants.DefaultAppID)
		require.NoError(t, err)
		assert.Empty(t, namespaces)
	})

	t.Run("返回所有有群组的命名空间", func(t *testing.T) {
		require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: "tenantA", OwnerID: "o"}))
		require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g2", Namespace: "tenantB", OwnerID: "o"}))
		require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g3", Namespace: "default", OwnerID: "o"}))

		namespaces, err := repo.GetAllNamespaces(ctx, constants.DefaultAppID)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"tenantA", "tenantB", "default"}, namespaces)
	})

	t.Run("解散群组后命名空间仍存在其他群组时保留", func(t *testing.T) {
		// tenantA 再加一个群组
		require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "gA2", Namespace: "tenantA", OwnerID: "o"}))
		// 解散 g1
		require.NoError(t, repo.DisbandGroup(ctx, constants.DefaultAppID, "tenantA", "g1"))

		namespaces, err := repo.GetAllNamespaces(ctx, constants.DefaultAppID)
		require.NoError(t, err)
		assert.Contains(t, namespaces, "tenantA", "tenantA 仍有 gA2，应保留")
	})
}

// TestGetMultiGroupMembers 验证批量获取多个群组成员
func TestGetMultiGroupMembers(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	// 创建 3 个群组并添加成员
	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g1", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g2", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, repo.CreateGroup(ctx, &Group{GroupID: "g3", Namespace: "tenantA", OwnerID: "o"}))

	require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g1", []string{"u1", "u2"}))
	require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g2", []string{"u2", "u3"}))
	// g3 无成员

	t.Run("批量返回所有群组成员", func(t *testing.T) {
		result, err := repo.GetMultiGroupMembers(ctx, constants.DefaultAppID, "tenantA", []string{"g1", "g2", "g3"})
		require.NoError(t, err)
		assert.Len(t, result, 3, "应返回 3 个群组的成员")

		assert.ElementsMatch(t, []string{"u1", "u2"}, result["g1"])
		assert.ElementsMatch(t, []string{"u2", "u3"}, result["g2"])
		assert.Empty(t, result["g3"], "g3 无成员应返回空切片")
	})

	t.Run("空 groupIDs 返回 nil", func(t *testing.T) {
		result, err := repo.GetMultiGroupMembers(ctx, constants.DefaultAppID, "tenantA", []string{})
		require.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("包含不存在的群组时该 key 返回空切片", func(t *testing.T) {
		result, err := repo.GetMultiGroupMembers(ctx, constants.DefaultAppID, "tenantA", []string{"g1", "not-exist"})
		require.NoError(t, err)
		assert.Contains(t, result, "g1")
		// 不存在的群组：miniredis 对 SMEMBERS 不存在的 key 返回空切片
		if members, ok := result["not-exist"]; ok {
			assert.Empty(t, members)
		}
	})

	t.Run("跨命名空间隔离：查 tenantB 的群组应返回空", func(t *testing.T) {
		result, err := repo.GetMultiGroupMembers(ctx, constants.DefaultAppID, "tenantB", []string{"g1", "g2"})
		require.NoError(t, err)
		// tenantB 下无这些群组，成员应为空
		for _, members := range result {
			assert.Empty(t, members, "tenantB 下不应有 tenantA 的群组成员")
		}
	})
}

// TestCreateGroupReserved 验证业务组禁止使用系统保留名（__ 前缀）
func TestCreateGroupReserved(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("业务组用 __ 前缀返回 models.ErrGroupReserved", func(t *testing.T) {
		err := repo.CreateGroup(ctx, &Group{GroupID: "__mygroup__", Namespace: "tenantA", OwnerID: "o"})
		assert.ErrorIs(t, err, models.ErrGroupReserved, "__ 前缀为系统保留名，业务组应拒绝")

		// 确认未创建
		_, err = repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", "__mygroup__")
		assert.Error(t, err, "不应创建成功")
	})

	t.Run("普通业务组名正常创建", func(t *testing.T) {
		err := repo.CreateGroup(ctx, &Group{GroupID: "normal-group", Namespace: "tenantA", OwnerID: "o"})
		assert.NoError(t, err)
	})
}

// TestEnsureSystemGroup 验证系统保留组的幂等初始化
func TestEnsureSystemGroup(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	t.Run("非系统组名返回 models.ErrGroupReserved", func(t *testing.T) {
		err := repo.EnsureSystemGroup(ctx, constants.DefaultAppID, "tenantA", "normal-group")
		assert.ErrorIs(t, err, models.ErrGroupReserved, "仅允许 __ 前缀系统组名")
	})

	t.Run("首次创建系统组成功", func(t *testing.T) {
		err := repo.EnsureSystemGroup(ctx, constants.DefaultAppID, "tenantA", constants.SystemGroupAgents)
		require.NoError(t, err)

		// 确认已创建且可查询
		g, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", constants.SystemGroupAgents)
		require.NoError(t, err)
		assert.Equal(t, "system", g.OwnerID, "系统组 owner 应为 system")
	})

	t.Run("重复 ensure 幂等返回 nil", func(t *testing.T) {
		// 再次 ensure 不应报错
		err := repo.EnsureSystemGroup(ctx, constants.DefaultAppID, "tenantA", constants.SystemGroupAgents)
		assert.NoError(t, err, "已存在的系统组 ensure 应幂等返回 nil")

		// 确认仍是原系统组（未被覆盖）
		g, err := repo.GetGroup(ctx, constants.DefaultAppID, "tenantA", constants.SystemGroupAgents)
		require.NoError(t, err)
		assert.Equal(t, "system", g.OwnerID)
	})

	t.Run("系统组可正常添加和查询成员", func(t *testing.T) {
		require.NoError(t, repo.EnsureSystemGroup(ctx, constants.DefaultAppID, "tenantA", constants.SystemGroupObservers))
		require.NoError(t, repo.AddMembers(ctx, constants.DefaultAppID, "tenantA", constants.SystemGroupObservers, []string{"u1", "u2"}))

		members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantA", constants.SystemGroupObservers)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1", "u2"}, members)
	})

	t.Run("全局观察者系统组 tenant 为空", func(t *testing.T) {
		// 全局观察者系统组 tenant=""
		err := repo.EnsureSystemGroup(ctx, constants.DefaultAppID, "", constants.SystemGroupObservers)
		require.NoError(t, err)

		g, err := repo.GetGroup(ctx, constants.DefaultAppID, "", constants.SystemGroupObservers)
		require.NoError(t, err)
		assert.Equal(t, "system", g.OwnerID)
	})

	t.Run("并发 ensure 同一系统组幂等", func(t *testing.T) {
		// 并发 ensure 不应报错（Lua 脚本原子保证）
		var wg sync.WaitGroup
		errs := make([]error, 10)
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				errs[idx] = repo.EnsureSystemGroup(ctx, constants.DefaultAppID, "tenantC", constants.SystemGroupAgents)
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			assert.NoError(t, err, "并发 ensure 应全部幂等成功")
		}

		// 确认只创建了一个
		members, err := repo.GetMembers(ctx, constants.DefaultAppID, "tenantC", constants.SystemGroupAgents)
		require.NoError(t, err)
		assert.Empty(t, members, "无成员添加，应为空")
	})
}

// ============================================================================
// 反向映射测试（groupID → namespace）
// ============================================================================

func TestGetGroupNamespace(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	// 创建群组
	require.NoError(t, repo.CreateGroup(ctx, &Group{
		GroupID:    "group-1",
		Namespace:  "tenantA",
		OwnerID:    "owner-1",
		MaxMembers: 100,
	}))

	t.Run("反查存在的群组", func(t *testing.T) {
		namespace, err := repo.GetGroupNamespace(ctx, constants.DefaultAppID, "group-1")
		require.NoError(t, err)
		assert.Equal(t, "tenantA", namespace)
	})

	t.Run("反查不存在的群组返回 models.ErrGroupNotFound", func(t *testing.T) {
		_, err := repo.GetGroupNamespace(ctx, constants.DefaultAppID, "nonexistent")
		assert.ErrorIs(t, err, models.ErrGroupNotFound)
	})

	t.Run("跨命名空间同名群组各自反查正确", func(t *testing.T) {
		// tenantB 也创建 group-1（同命名空间唯一，跨命名空间可重复）
		require.NoError(t, repo.CreateGroup(ctx, &Group{
			GroupID:    "group-1",
			Namespace:  "tenantB",
			OwnerID:    "owner-2",
			MaxMembers: 100,
		}))
		// 反查会返回其中一个（取决于谁后写入），这里只验证能查到
		namespace, err := repo.GetGroupNamespace(ctx, constants.DefaultAppID, "group-1")
		require.NoError(t, err)
		assert.Contains(t, []string{"tenantA", "tenantB"}, namespace)
	})
}

func TestGetMultiGroupNamespaces(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	// 创建多个群组
	for _, g := range []struct {
		namespace, groupID string
	}{
		{"tenantA", "g1"},
		{"tenantA", "g2"},
		{"tenantB", "g3"},
	} {
		require.NoError(t, repo.CreateGroup(ctx, &Group{
			GroupID: g.groupID, Namespace: g.namespace, OwnerID: "owner", MaxMembers: 100,
		}))
	}

	t.Run("批量反查全部存在", func(t *testing.T) {
		result, err := repo.GetMultiGroupNamespaces(ctx, constants.DefaultAppID, []string{"g1", "g2", "g3"})
		require.NoError(t, err)
		assert.Equal(t, "tenantA", result["g1"])
		assert.Equal(t, "tenantA", result["g2"])
		assert.Equal(t, "tenantB", result["g3"])
	})

	t.Run("批量反查部分不存在", func(t *testing.T) {
		result, err := repo.GetMultiGroupNamespaces(ctx, constants.DefaultAppID, []string{"g1", "nonexistent"})
		require.NoError(t, err)
		assert.Equal(t, "tenantA", result["g1"])
		_, ok := result["nonexistent"]
		assert.False(t, ok, "不存在的 groupID 不应在结果中")
	})

	t.Run("空输入返回 nil", func(t *testing.T) {
		result, err := repo.GetMultiGroupNamespaces(ctx, constants.DefaultAppID, nil)
		require.NoError(t, err)
		assert.Nil(t, result)
	})
}

func TestDisbandGroupDeletesReverseMapping(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, repo.CreateGroup(ctx, &Group{
		GroupID: "to-delete", Namespace: "tenantA", OwnerID: "owner", MaxMembers: 100,
	}))

	// 确认反查可用
	namespace, err := repo.GetGroupNamespace(ctx, constants.DefaultAppID, "to-delete")
	require.NoError(t, err)
	assert.Equal(t, "tenantA", namespace)

	// 删除群组
	require.NoError(t, repo.DisbandGroup(ctx, constants.DefaultAppID, "tenantA", "to-delete"))

	// 反查应返回 models.ErrGroupNotFound（反向映射已清理）
	_, err = repo.GetGroupNamespace(ctx, constants.DefaultAppID, "to-delete")
	assert.ErrorIs(t, err, models.ErrGroupNotFound, "删除群组后反向映射应同步清理")
}

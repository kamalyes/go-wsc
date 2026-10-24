/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 09:58:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 11:28:16
 * @FilePath: \go-wsc\adapter\redis\group_member_cache_test.go
 * @Description: 群组成员拓扑缓存测试 - 基于 miniredis 真实仓储验证命中挡回源、
 * 写路径逐出、TTL 过期、LRU 容量逐出、大群预算与 app 隔离
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package redisadapter

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingGroupStore 回源计数器：仅覆盖聚合读（未覆盖方法经嵌入透传真实仓储）
type countingGroupStore struct {
	spi.GroupStore
	backfills int32
}

func (s *countingGroupStore) GetMultiGroupMembers(ctx context.Context, appID string, groupIDs []string) (map[string][]string, error) {
	atomic.AddInt32(&s.backfills, 1)
	return s.GroupStore.GetMultiGroupMembers(ctx, appID, groupIDs)
}

func (s *countingGroupStore) backfillCount() int { return int(atomic.LoadInt32(&s.backfills)) }

// setupCache 构造「真实仓储 + 回源计数 + 拓扑缓存」三层装配
func setupCache(t *testing.T, ttl, negativeTTL time.Duration, maxEntries, maxMembers int) (*GroupMemberCache, *countingGroupStore) {
	t.Helper()
	repo, cleanup := setupTestRepo(t)
	t.Cleanup(cleanup)
	counting := &countingGroupStore{GroupStore: repo}
	return NewGroupMemberCache(counting, ttl, negativeTTL, maxEntries, maxMembers), counting
}

// TestGroupMemberCache_HitAvoidsBackfill 命中挡回源：首次读回源并缓存跨 ns 聚合结果，后续读零回源
func TestGroupMemberCache_HitAvoidsBackfill(t *testing.T) {
	cache, counting := setupCache(t, 0, 0, 0, 0) // 全零值走默认参数
	ctx := context.Background()

	// g1 跨 ns 双实例（tenantA/tenantB），g2 单实例
	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g1", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g1", Namespace: "tenantB", OwnerID: "o"}))
	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g2", Namespace: "tenantB", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g1", []string{"u1", "u2"}))
	require.NoError(t, cache.AddMembers(ctx, constants.DefaultAppID, "tenantB", "g1", []string{"u9", "u2"}))
	require.NoError(t, cache.AddMembers(ctx, constants.DefaultAppID, "tenantB", "g2", []string{"u3"}))

	t.Run("首次读回源并缓存跨 ns 聚合结果", func(t *testing.T) {
		result, err := cache.GetMultiGroupMembers(ctx, constants.DefaultAppID, []string{"g1", "g2"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1", "u2", "u9"}, result["g1"], "聚合语义与无缓存一致")
		assert.ElementsMatch(t, []string{"u3"}, result["g2"])
		assert.Equal(t, 1, counting.backfillCount(), "首次读应恰好回源 1 次")
	})

	t.Run("第二次读全部命中不再回源", func(t *testing.T) {
		result, err := cache.GetMultiGroupMembers(ctx, constants.DefaultAppID, []string{"g1", "g2"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1", "u2", "u9"}, result["g1"])
		assert.ElementsMatch(t, []string{"u3"}, result["g2"])
		assert.Equal(t, 1, counting.backfillCount(), "命中后不应再回源")
	})

	t.Run("无实例 gid 产生负缓存且结果 key 缺失", func(t *testing.T) {
		result, err := cache.GetMultiGroupMembers(ctx, constants.DefaultAppID, []string{"g1", "not-exist"})
		require.NoError(t, err)
		assert.Equal(t, 2, counting.backfillCount(), "not-exist 未命中应触发一次回源")
		_, ok := result["not-exist"]
		assert.False(t, ok, "无实例 gid 的结果 key 应缺失（与回源语义一致）")

		// 负缓存命中：not-exist 不再回源
		_, err = cache.GetMultiGroupMembers(ctx, constants.DefaultAppID, []string{"not-exist"})
		require.NoError(t, err)
		assert.Equal(t, 2, counting.backfillCount(), "负缓存命中不应回源")
	})
}

// TestGroupMemberCache_WriteEvicts 写路径即时逐出：增删成员/解散后下一次读拿到最新拓扑
func TestGroupMemberCache_WriteEvicts(t *testing.T) {
	cache, counting := setupCache(t, time.Minute, 0, 0, 0)
	ctx := context.Background()
	app := constants.DefaultAppID

	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-evict", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-evict", []string{"u1"}))

	_, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-evict"})
	require.NoError(t, err)
	require.Equal(t, 1, counting.backfillCount())

	t.Run("AddMembers 后读可见新成员", func(t *testing.T) {
		require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-evict", []string{"u2"}))
		result, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-evict"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1", "u2"}, result["g-evict"], "写后逐出，读到最新成员")
		assert.Equal(t, 2, counting.backfillCount(), "逐出后应回源")
	})

	t.Run("RemoveMembers 后读不再包含被移除成员", func(t *testing.T) {
		require.NoError(t, cache.RemoveMembers(ctx, app, "tenantA", "g-evict", []string{"u2"}))
		result, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-evict"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1"}, result["g-evict"])
		assert.Equal(t, 3, counting.backfillCount())
	})

	t.Run("DisbandGroup 后读 key 缺失且负缓存生效", func(t *testing.T) {
		require.NoError(t, cache.DisbandGroup(ctx, app, "tenantA", "g-evict"))
		result, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-evict"})
		require.NoError(t, err)
		_, ok := result["g-evict"]
		assert.False(t, ok, "解散后聚合结果 key 应缺失")
		assert.Equal(t, 4, counting.backfillCount())

		_, err = cache.GetMultiGroupMembers(ctx, app, []string{"g-evict"})
		require.NoError(t, err)
		assert.Equal(t, 4, counting.backfillCount(), "解散后的负缓存命中不应回源")
	})
}

// TestGroupMemberCache_CreateEvictsNegativeEntry 建组逐出负缓存：
// 先查未建 gid 写入负缓存后建组，新实例必须立即可见（否则 TTL 窗口内被误报为无实例、群组投递漏投）
func TestGroupMemberCache_CreateEvictsNegativeEntry(t *testing.T) {
	cache, counting := setupCache(t, time.Minute, 0, 0, 0)
	ctx := context.Background()
	app := constants.DefaultAppID

	// 两个 gid 均未建组，各查一次写入负缓存并验证命中不回源
	_, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-late", "__observer__"})
	require.NoError(t, err)
	require.Equal(t, 1, counting.backfillCount())
	_, err = cache.GetMultiGroupMembers(ctx, app, []string{"g-late", "__observer__"})
	require.NoError(t, err)
	require.Equal(t, 1, counting.backfillCount(), "负缓存命中不应回源")

	t.Run("CreateGroup 后新实例立即可见", func(t *testing.T) {
		require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-late", Namespace: "tenantA", OwnerID: "o"}))
		require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-late", []string{"u1"}))
		result, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-late"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1"}, result["g-late"], "负缓存不得挡住建组后的查询")
		assert.Equal(t, 2, counting.backfillCount(), "建组逐出负缓存后应回源")
	})

	t.Run("EnsureSystemGroup 后系统组立即可见", func(t *testing.T) {
		require.NoError(t, cache.EnsureSystemGroup(ctx, app, "", "__observer__"))
		require.NoError(t, cache.AddMembers(ctx, app, "", "__observer__", []string{"u-sys"}))
		result, err := cache.GetMultiGroupMembers(ctx, app, []string{"__observer__"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u-sys"}, result["__observer__"], "负缓存不得挡住系统组建立后的查询")
		assert.Equal(t, 3, counting.backfillCount(), "建系统组逐出负缓存后应回源")
	})
}

// TestGroupMemberCache_TTLExpiry 条目过期后视作 miss 重新回源（跨节点写入的一致性兜底）
func TestGroupMemberCache_TTLExpiry(t *testing.T) {
	cache, counting := setupCache(t, 25*time.Millisecond, 0, 0, 0)
	ctx := context.Background()
	app := constants.DefaultAppID

	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-ttl", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-ttl", []string{"u1"}))

	_, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-ttl"})
	require.NoError(t, err)
	require.Equal(t, 1, counting.backfillCount())

	time.Sleep(60 * time.Millisecond) // 越过 TTL

	result, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-ttl"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1"}, result["g-ttl"])
	assert.Equal(t, 2, counting.backfillCount(), "过期后应重新回源")
}

// TestGroupMemberCache_OversizedEntrySkipped 大群超成员预算不入缓存，每次读都回源透传
func TestGroupMemberCache_OversizedEntrySkipped(t *testing.T) {
	cache, counting := setupCache(t, time.Minute, 0, 0, 2) // 单条目预算 2 人
	ctx := context.Background()
	app := constants.DefaultAppID

	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-big", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-big", []string{"u1", "u2", "u3"}))

	for i := 0; i < 3; i++ {
		result, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-big"})
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{"u1", "u2", "u3"}, result["g-big"], "超预算大群结果应原样透传")
	}
	assert.Equal(t, 3, counting.backfillCount(), "超预算条目每次读都应回源，不入缓存")
}

// TestGroupMemberCache_LRUEviction 容量超限从队尾逐出，最近访问的条目保留
func TestGroupMemberCache_LRUEviction(t *testing.T) {
	// 容量 128 → 分片配额 2；白盒选三个同分片 gid，使配额在该分片内精确生效（分片配额语义）
	cache, counting := setupCache(t, time.Minute, 0, 128, 0)
	ctx := context.Background()
	app := constants.DefaultAppID

	gids := make([]string, 0, 3)
	for i := 0; len(gids) < 3; i++ {
		cand := "g" + strconv.Itoa(i)
		if len(gids) == 0 || groupShardOf(app, cand) == groupShardOf(app, gids[0]) {
			gids = append(gids, cand)
		}
	}
	g1, g2, g3 := gids[0], gids[1], gids[2]

	for _, gid := range gids {
		require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: gid, Namespace: "tenantA", OwnerID: "o"}))
		require.NoError(t, cache.AddMembers(ctx, app, "tenantA", gid, []string{"u-" + gid}))
	}

	// 回源填充 g1/g2，fill 顺序 g1 先 g2 后 → LRU 队尾为 g1
	_, err := cache.GetMultiGroupMembers(ctx, app, []string{g1, g2})
	require.NoError(t, err)
	require.Equal(t, 1, counting.backfillCount())

	// 命中 g1 并移到队首 → 队尾变为 g2
	_, err = cache.GetMultiGroupMembers(ctx, app, []string{g1})
	require.NoError(t, err)
	require.Equal(t, 1, counting.backfillCount(), "g1 应命中")

	// g3 入缓存 → 逐出队尾 g2
	_, err = cache.GetMultiGroupMembers(ctx, app, []string{g3})
	require.NoError(t, err)
	assert.Equal(t, 2, counting.backfillCount(), "g3 未命中应回源")

	// g2 被逐出 → miss；g1 保留 → hit
	_, err = cache.GetMultiGroupMembers(ctx, app, []string{g2, g1})
	require.NoError(t, err)
	assert.Equal(t, 3, counting.backfillCount(), "仅 g2 miss 回源 1 次，g1 应命中不回源")
}

// TestGroupMemberCache_AppIsolation 缓存 key 按 appID 隔离：同 gid 跨 app 互不串扰
func TestGroupMemberCache_AppIsolation(t *testing.T) {
	cache, counting := setupCache(t, time.Minute, 0, 0, 0)
	ctx := context.Background()

	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-iso", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, constants.DefaultAppID, "tenantA", "g-iso", []string{"u1"}))
	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-iso", AppID: "other-app", Namespace: "tenantB", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, "other-app", "tenantB", "g-iso", []string{"u77"}))

	defaultResult, err := cache.GetMultiGroupMembers(ctx, constants.DefaultAppID, []string{"g-iso"})
	require.NoError(t, err)
	otherResult, err := cache.GetMultiGroupMembers(ctx, "other-app", []string{"g-iso"})
	require.NoError(t, err)
	require.Equal(t, 2, counting.backfillCount(), "两个 app 各回源 1 次")

	assert.ElementsMatch(t, []string{"u1"}, defaultResult["g-iso"])
	assert.ElementsMatch(t, []string{"u77"}, otherResult["g-iso"])

	// 命中各自缓存，互不影响
	_, err = cache.GetMultiGroupMembers(ctx, constants.DefaultAppID, []string{"g-iso"})
	require.NoError(t, err)
	_, err = cache.GetMultiGroupMembers(ctx, "other-app", []string{"g-iso"})
	require.NoError(t, err)
	assert.Equal(t, 2, counting.backfillCount(), "命中后不应回源")

	// other-app 写路径只逐出 other-app 条目，默认 app 条目仍命中
	require.NoError(t, cache.AddMembers(ctx, "other-app", "tenantB", "g-iso", []string{"u78"}))
	_, err = cache.GetMultiGroupMembers(ctx, constants.DefaultAppID, []string{"g-iso"})
	require.NoError(t, err)
	assert.Equal(t, 2, counting.backfillCount(), "跨 app 写入不应逐出默认 app 条目")
}

// staleBackfillGroupStore 回源快照门控仓储：先读快照再挂起（仅第二轮），
// 用于确定性构造「回源快照已读、fill 未执行」窗口内发生拓扑写的竞态时序
type staleBackfillGroupStore struct {
	spi.GroupStore
	backfills  int32
	inBackfill chan struct{} // 第二轮快照读完后通知（一次）
	release    chan struct{} // 第二轮挂起至此放行（一次）
}

func (s *staleBackfillGroupStore) GetMultiGroupMembers(ctx context.Context, appID string, groupIDs []string) (map[string][]string, error) {
	n := atomic.AddInt32(&s.backfills, 1)
	result, err := s.GroupStore.GetMultiGroupMembers(ctx, appID, groupIDs)
	if err != nil {
		return nil, err
	}
	if n == 2 { // 仅竞态构造轮（第二轮）挂起：此后的 Redis 写不影响本快照
		s.inBackfill <- struct{}{}
		<-s.release
	}
	return result, nil
}

// TestGroupMemberCache_BackfillGenerationGuard 回源-回填窗口的世代双检
// 回归场景：miss 后锁外回源读到旧快照，期间本节点拓扑写（AddMembers）触发逐出——
// 逐出时缓存条目尚不存在（miss 已摘除/未回填）落空，仅世代递增。无世代守卫时
// fill 把旧快照重新入缓存，新入群成员最长 TTL 窗口漏收必达消息且无离线转存。
// 修复后 fill 比对世代放弃回填，下一次读重新回源拿到新拓扑
func TestGroupMemberCache_BackfillGenerationGuard(t *testing.T) {
	repo, cleanup := setupTestRepo(t)
	t.Cleanup(cleanup)
	stale := &staleBackfillGroupStore{
		GroupStore: repo,
		inBackfill: make(chan struct{}),
		release:    make(chan struct{}),
	}
	cache := NewGroupMemberCache(stale, time.Minute, 0, 0, 0)
	ctx := context.Background()
	app := constants.DefaultAppID

	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-gen", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-gen", []string{"u1"}))

	// 第一轮：正常回源回填，缓存就绪
	first, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-gen"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1"}, first["g-gen"])

	// 手动逐出（等价一次拓扑失效）：第二轮读必然 miss
	cache.InvalidateTopology(app, "g-gen")

	// 第二轮：miss → 回源读旧快照（仅 u1）→ 挂起
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		result, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-gen"})
		assert.NoError(t, err)
		// 本轮拿到的是回源时刻的旧快照（不可避免）；断言放到主协程统一做
		got := result["g-gen"]
		assert.ElementsMatch(t, []string{"u1"}, got)
	}()
	<-stale.inBackfill // 快照已读（u1），fill 未执行

	// 回源挂起期间拓扑写：新成员入群（evict 落空——条目不存在，仅世代递增）
	require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-gen", []string{"u2"}))

	close(stale.release) // 放行：旧快照返回，fill 应被世代双检拦截
	<-secondDone

	// 第三轮：旧快照未回填（世代守卫），重新回源拿到含 u2 的新拓扑
	third, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-gen"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u1", "u2"}, third["g-gen"],
		"旧快照不得回填，重新回源应拿到新成员")

	// 第四轮：命中，不再回源
	_, err = cache.GetMultiGroupMembers(ctx, app, []string{"g-gen"})
	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&stale.backfills),
		"共回源 3 次（初始/竞态轮/重试轮），第四轮应命中")
}

// TestGroupMemberCache_ConcurrentAccess 并发读写冒烟：混度读写无死锁/无竞态（-race 下验证）
func TestGroupMemberCache_ConcurrentAccess(t *testing.T) {
	cache, _ := setupCache(t, time.Minute, 0, 0, 0)
	ctx := context.Background()
	app := constants.DefaultAppID

	require.NoError(t, cache.CreateGroup(ctx, &models.Group{GroupID: "g-hot", Namespace: "tenantA", OwnerID: "o"}))
	require.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-hot", []string{"u1", "u2"}))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, err := cache.GetMultiGroupMembers(ctx, app, []string{"g-hot"})
				assert.NoError(t, err)
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			uid := "u-churn-" + string(rune('a'+idx))
			for j := 0; j < 20; j++ {
				// goroutine 内禁用 require（t.FailNow 仅允许测试主协程调用），失败经 assert 记账
				assert.NoError(t, cache.AddMembers(ctx, app, "tenantA", "g-hot", []string{uid}))
				assert.NoError(t, cache.RemoveMembers(ctx, app, "tenantA", "g-hot", []string{uid}))
			}
		}(i)
	}
	wg.Wait()
}

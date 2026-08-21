/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-22 00:00:00
 * @FilePath: \go-wsc\repository\uid_offset_cache.go
 * @Description: userID→数字 offset 的进程内 L1 缓存
 *
 * 用于 Bitmap 快速判否层:每次 IsUserOnlineFast 需要 userID 对应的数字 offset
 * 作为 GETBIT 的位偏移。若每次都走 Redis HGET uid_map,会给热路径增加 1 次网络往返
 * 本缓存在进程内复用 offset,首次 miss 时由 Lua 脚本分配并回填,后续命中零网络
 *
 * 设计取舍:
 *   - 容量超限时整表 Clear(而非 LRU 半清):ShardedMap 的 Clear 是 O(shard 数) 重置,
 *     比维护 LRU 链表的写入开销低;2M 容量对千万级用户系统足够,清空后由后续查询按需重建
 *   - offset 永久复用(不 Delete):用户下线后再上线 offset 不变,避免 INCR 空洞累积
 *   - 并发安全由 ShardedMap 分片锁保证,无额外锁
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"sync/atomic"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

const (
	// defaultUIDCacheShardCount offset 缓存的分片数,与 shardedRegistry 对齐
	defaultUIDCacheShardCount = 64
	// defaultMaxCachedUIDs 缓存容量上限,超限触发整表清空
	defaultMaxCachedUIDs = 2_000_000
)

// uidOffsetCache userID→数字 offset 的进程内 L1 缓存
//
// 缓存命中流程(读路径 IsUserOnlineFast):
//  1. Load(userID) 命中 → 直接拿 offset 走 Lua GETBIT
//  2. Load(userID) 未命中 → 走 Lua getOrAssignOffset(HGET+INCR+HSETNX)分配,回填缓存
//
// 缓存命中流程(写路径 SetClientOnline):
//  1. Load(userID) 命中 → 直接拿 offset 在批量 Lua 内 SETBIT
//  2. Load(userID) 未命中 → 批量 Lua 内 getOrAssignOffset 分配并回填缓存
//
// 注:并发首次 miss 时多 goroutine 会各自走 Lua 分配,Lua 内 HSETNX 保证最终 offset 唯一,
// 回填缓存是幂等 Store(覆盖写相同值),无正确性问题。
type uidOffsetCache struct {
	cache *syncx.ShardedMap[string, int64]

	// maxCapacity 容量上限,Len() 超过此值时整表 Clear
	// 防止恶意大量 userID 导致进程内存膨胀
	maxCapacity int

	// overflowCount offset 超限累计次数(MaxBitmapOffset 触发),监控用
	overflowCount atomic.Int64
}

// newUIDOffsetCache 创建 offset 缓存
//
// maxCapacity <= 0 时用 defaultMaxCachedUIDs
func newUIDOffsetCache(maxCapacity int) *uidOffsetCache {
	if maxCapacity <= 0 {
		maxCapacity = defaultMaxCachedUIDs
	}
	return &uidOffsetCache{
		cache:       syncx.NewShardedMap[string, int64](defaultUIDCacheShardCount),
		maxCapacity: maxCapacity,
	}
}

// Load 从缓存读取 offset
// 返回 (offset, true) 命中;(0, false) 未命中,调用方应走 Lua 分配
func (c *uidOffsetCache) Load(userID string) (int64, bool) {
	return c.cache.Load(userID)
}

// Store 写入 offset
//
// 容量超限时整表 Clear:简单策略,清空后由后续查询按需重建。
// 牺牲短时缓存命中率(重建窗口内多走 Lua),换取写入零链表开销。
// 并发 Store 多次 Clear 是幂等的,无正确性问题。
func (c *uidOffsetCache) Store(userID string, offset int64) {
	c.cache.Store(userID, offset)
	if c.cache.Len() > c.maxCapacity {
		c.cache.Clear()
	}
}

// LoadOrStore 加载或存储:命中返回 (existing, true),未命中存储 value 并返回 (value, false)
// 用于并发首次分配场景:多 goroutine 同时 miss 时,只有一个会真正 Store,其余拿到相同值
func (c *uidOffsetCache) LoadOrStore(userID string, offset int64) (int64, bool) {
	actual, loaded := c.cache.LoadOrStore(userID, offset)
	if !loaded && c.cache.Len() > c.maxCapacity {
		c.cache.Clear()
	}
	return actual, loaded
}

// Delete 删除缓存项(通常不调用,offset 永久复用)
func (c *uidOffsetCache) Delete(userID string) {
	c.cache.Delete(userID)
}

// Len 当前缓存条数
func (c *uidOffsetCache) Len() int {
	return c.cache.Len()
}

// Clear 清空全部缓存
func (c *uidOffsetCache) Clear() {
	c.cache.Clear()
}

// IncOverflow offset 超限计数 +1,返回累计值
// MaxBitmapOffset 触发时调用,供监控指标采集
func (c *uidOffsetCache) IncOverflow() int64 {
	return c.overflowCount.Add(1)
}

// OverflowCount 返回累计 offset 超限次数
func (c *uidOffsetCache) OverflowCount() int64 {
	return c.overflowCount.Load()
}

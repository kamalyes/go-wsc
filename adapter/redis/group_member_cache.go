/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 01:58:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 11:28:16
 * @FilePath: \go-wsc\adapter\redis\group_member_cache.go
 * @Description: 群组成员拓扑缓存 - GetMultiGroupMembers 的本地缓存装饰器（64 分片）
 *
 * 群消息投递热路径的 0 回源优化：稳态下投递只读本地聚合拓扑，Redis 两段 Pipeline
 * 仅在未命中/过期时回源缓存 key 为 (appID, groupID)，与跨 ns 聚合语义一致、
 * 不感知 namespace本地写路径（建组/增删成员/解散）即时逐出对应条目；跨节点写入由
 * TTL 兜底（失效广播暂缓）
 *
 * 分片结构（与 ShardedRegistry/AckManager 的 FNV-1a 惯例同款）：64 个独立 shard
 * 各持私有 LRU+items，命中/回填/逐出均在 shard 内加锁，高并发群投递下锁竞争面
 * 缩至 1/64；容量语义为分片配额（maxEntries 均摊），非精确全局上限
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package redisadapter

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// groupCacheShards 分片数（2 的幂，配合按位与取模）
const groupCacheShards = 64

// groupCacheKey 聚合拓扑的复合缓存 key（结构体 map key，零冲突零分配）
type groupCacheKey struct {
	appID string
	gid   string
}

// groupCacheEntry 单个群组的聚合拓扑条目
type groupCacheEntry struct {
	key      groupCacheKey // 仅供 LRU 逐出时反查索引
	members  []string      // nil 表示确认无实例的负缓存，命中时结果 key 缺失与回源语义一致
	expireAt time.Time     // 过期后视作 miss 并即时清除
}

// groupMemberShard 单个分片：独立锁 + LRU + 索引（与 Coalescer 的 shardOf 同风格）
type groupMemberShard struct {
	mu    sync.Mutex
	lru   *list.List // front=最近访问，配额超限从队尾逐出
	items map[groupCacheKey]*list.Element
}

// GroupMemberCache 群组成员拓扑缓存装饰器（LRU + TTL + 大群条目预算 + 64 分片）
//
// 装饰 spi.GroupStore：覆盖成员聚合读（GetMultiGroupMembers）与拓扑变更写
// （CreateGroup/EnsureSystemGroup/AddMembers/RemoveMembers/DisbandGroup，写后逐出，
// 建组逐出负缓存防新实例被误报为无实例），其余方法经嵌入原样透传
type GroupMemberCache struct {
	spi.GroupStore

	ttl        time.Duration // 条目存活期（兜住跨节点写入的最终一致窗口）
	maxEntries int           // 全局条目配额（按分片均摊，非精确全局上限）
	maxMembers int           // 单条目成员数预算，超出不入缓存（防大群挤爆内存）

	shards [groupCacheShards]groupMemberShard
}

// NewGroupMemberCache 创建群组成员拓扑缓存装饰器
//
// inner 为被装饰的群组仓储（必需，nil 直接 panic 暴露装配错误）
// ttl/maxEntries/maxMembers 传零值时使用 constants 默认值（见 delivery.go）
func NewGroupMemberCache(inner spi.GroupStore, ttl time.Duration, maxEntries, maxMembers int) *GroupMemberCache {
	if inner == nil {
		panic("GroupMemberCache: inner group store is required")
	}
	if maxEntries <= 0 {
		maxEntries = constants.DefaultGroupMemberCacheEntries
	}
	quota := mathx.Max(maxEntries/groupCacheShards, 1)
	c := &GroupMemberCache{
		GroupStore: inner,
		ttl:        mathx.IF(ttl > 0, ttl, constants.DefaultGroupMemberCacheTTL),
		maxEntries: quota * groupCacheShards, // 回算全局口径，仅用于日志与观测
		maxMembers: mathx.IF(maxMembers > 0, maxMembers, constants.DefaultGroupMemberCacheMaxMembers),
	}
	for i := range c.shards {
		c.shards[i].lru = list.New()
		c.shards[i].items = make(map[groupCacheKey]*list.Element, quota)
	}
	return c
}

// shardOf 复合 key → 分片索引（FNV-1a 逐字段散列，零分配热路径）
func groupShardOf(appID, gid string) int {
	const (
		offset32 = 2166136261
		prime32  = 16777619
	)
	var h uint32 = offset32
	for i := 0; i < len(appID); i++ {
		h ^= uint32(appID[i])
		h *= prime32
	}
	for i := 0; i < len(gid); i++ {
		h ^= uint32(gid[i])
		h *= prime32
	}
	return int(h & (groupCacheShards - 1))
}

// GetMultiGroupMembers 批量获取群组成员：先查本地拓扑缓存，未命中部分回源并回填
//
// 返回语义与被装饰仓储完全一致：结果中 key 缺失表示该 gid 无实例
// 命中条目直接返回缓存切片（零分配热路径），调用方只读、不得修改返回切片
func (c *GroupMemberCache) GetMultiGroupMembers(ctx context.Context, appID string, groupIDs []string) (map[string][]string, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	appID = mathx.IfEmpty(appID, constants.DefaultAppID)

	// 命中收集：过期条目即时清除视作 miss，每 gid 仅持所属分片锁（全程无 IO）
	result := make(map[string][]string, len(groupIDs))
	misses := make([]string, 0, len(groupIDs))
	for _, gid := range groupIDs {
		s := &c.shards[groupShardOf(appID, gid)]
		s.mu.Lock()
		el, ok := s.items[groupCacheKey{appID, gid}]
		if ok {
			if time.Now().After(el.Value.(*groupCacheEntry).expireAt) {
				s.removeLocked(el)
				misses = append(misses, gid)
			} else {
				s.lru.MoveToFront(el)
				if e := el.Value.(*groupCacheEntry); e.members != nil {
					result[gid] = e.members
				}
			}
		} else {
			misses = append(misses, gid)
		}
		s.mu.Unlock()
	}

	if len(misses) == 0 {
		return result, nil
	}

	// 未命中回源：inner 两段 Pipeline 跨 ns 聚合，锁外执行不阻塞其他读
	fresh, err := c.GroupStore.GetMultiGroupMembers(ctx, appID, misses)
	if err != nil {
		return nil, err
	}
	for _, gid := range misses {
		members, ok := fresh[gid]
		if !ok {
			c.fill(appID, gid, nil) // 负缓存：确认无实例，挡住解散后群组的重复回源
			continue
		}
		if len(members) > c.maxMembers {
			result[gid] = members // 大群超预算不入缓存，原样透传
			continue
		}
		c.fill(appID, gid, members)
		result[gid] = members
	}
	return result, nil
}

// AddMembers 添加成员后逐出该群组拓扑（本地写路径即时失效）
func (c *GroupMemberCache) AddMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error {
	err := c.GroupStore.AddMembers(ctx, appID, namespace, groupID, userIDs)
	c.evict(appID, groupID)
	return err
}

// RemoveMembers 移除成员后逐出该群组拓扑
func (c *GroupMemberCache) RemoveMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error {
	err := c.GroupStore.RemoveMembers(ctx, appID, namespace, groupID, userIDs)
	c.evict(appID, groupID)
	return err
}

// CreateGroup 建组后逐出该群组拓扑
// 负缓存条目（确认无实例）必须在建组后失效，否则 TTL 窗口内新实例被误报为无实例、群组投递漏投
func (c *GroupMemberCache) CreateGroup(ctx context.Context, group *models.Group) error {
	err := c.GroupStore.CreateGroup(ctx, group)
	if group != nil {
		c.evict(group.AppID, group.GroupID)
	}
	return err
}

// EnsureSystemGroup 确保系统组后逐出该群组拓扑（与 CreateGroup 同因：负缓存不得挡住新实例）
func (c *GroupMemberCache) EnsureSystemGroup(ctx context.Context, appID, namespace, groupID string) error {
	err := c.GroupStore.EnsureSystemGroup(ctx, appID, namespace, groupID)
	c.evict(appID, groupID)
	return err
}

// DisbandGroup 解散群组后逐出该群组拓扑
func (c *GroupMemberCache) DisbandGroup(ctx context.Context, appID, namespace, groupID string) error {
	err := c.GroupStore.DisbandGroup(ctx, appID, namespace, groupID)
	c.evict(appID, groupID)
	return err
}

// fill 回填一条聚合拓扑到所属分片（已存在的条目原地刷新并移到队首，配额超限从队尾逐出）
func (c *GroupMemberCache) fill(appID, gid string, members []string) {
	s := &c.shards[groupShardOf(appID, gid)]
	quota := c.maxEntries / groupCacheShards
	s.mu.Lock()
	defer s.mu.Unlock()
	key := groupCacheKey{appID, gid}
	if el, ok := s.items[key]; ok {
		e := el.Value.(*groupCacheEntry)
		e.members = members
		e.expireAt = time.Now().Add(c.ttl)
		s.lru.MoveToFront(el)
		return
	}
	for s.lru.Len() >= quota {
		s.removeLocked(s.lru.Back())
	}
	s.items[key] = s.lru.PushFront(&groupCacheEntry{key: key, members: members, expireAt: time.Now().Add(c.ttl)})
}

// evict 逐出一个群组的聚合拓扑（写路径失效，appID 归一化与读路径同口径）
func (c *GroupMemberCache) evict(appID, gid string) {
	appID = mathx.IfEmpty(appID, constants.DefaultAppID)
	s := &c.shards[groupShardOf(appID, gid)]
	s.mu.Lock()
	defer s.mu.Unlock()
	if el, ok := s.items[groupCacheKey{appID, gid}]; ok {
		s.removeLocked(el)
	}
}

// removeLocked 摘除 LRU 节点并同步清理索引（须持所属分片锁调用）
func (s *groupMemberShard) removeLocked(el *list.Element) {
	delete(s.items, el.Value.(*groupCacheEntry).key)
	s.lru.Remove(el)
}

// 编译期断言：装饰器必须始终满足 spi.GroupStore 契约
var _ spi.GroupStore = (*GroupMemberCache)(nil)

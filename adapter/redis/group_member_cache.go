/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 01:58:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 11:28:16
 * @FilePath: \go-wsc\adapter\redis\group_member_cache.go
 * @Description: 群组成员拓扑缓存 - GetMultiGroupMembers 的本地缓存装饰器
 *
 * 群消息投递热路径的 0 回源优化：稳态下投递只读本地聚合拓扑，Redis 两段 Pipeline
 * 仅在未命中/过期时回源缓存 key 为 (appID, groupID)，与跨 ns 聚合语义一致、
 * 不感知 namespace本地写路径（增删成员/解散）即时逐出对应条目；跨节点写入由
 * TTL 兜底（失效广播暂缓）
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
	"github.com/kamalyes/go-wsc/spi"
)

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

// GroupMemberCache 群组成员拓扑缓存装饰器（LRU + TTL + 大群条目预算）
//
// 装饰 spi.GroupStore：仅覆盖成员聚合读（GetMultiGroupMembers）与成员变更写
// （AddMembers/RemoveMembers/DisbandGroup，写后逐出），其余方法经嵌入原样透传
type GroupMemberCache struct {
	spi.GroupStore

	ttl        time.Duration // 条目存活期（兜住跨节点写入的最终一致窗口）
	maxEntries int           // 缓存条目上限（LRU 容量）
	maxMembers int           // 单条目成员数预算，超出不入缓存（防大群挤爆内存）

	mu    sync.Mutex
	lru   *list.List // front=最近访问，容量超限从队尾逐出
	items map[groupCacheKey]*list.Element
}

// NewGroupMemberCache 创建群组成员拓扑缓存装饰器
//
// inner 为被装饰的群组仓储（必需，nil 直接 panic 暴露装配错误）
// ttl/maxEntries/maxMembers 传零值时使用 constants 默认值（见 delivery.go）
func NewGroupMemberCache(inner spi.GroupStore, ttl time.Duration, maxEntries, maxMembers int) *GroupMemberCache {
	if inner == nil {
		panic("GroupMemberCache: inner group store is required")
	}
	return &GroupMemberCache{
		GroupStore: inner,
		ttl:        mathx.IF(ttl > 0, ttl, constants.DefaultGroupMemberCacheTTL),
		maxEntries: mathx.IF(maxEntries > 0, maxEntries, constants.DefaultGroupMemberCacheEntries),
		maxMembers: mathx.IF(maxMembers > 0, maxMembers, constants.DefaultGroupMemberCacheMaxMembers),
		lru:        list.New(),
		items:      make(map[groupCacheKey]*list.Element, constants.DefaultGroupMemberCacheEntries),
	}
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

	// 命中收集：过期条目即时清除视作 miss，全程持锁无 IO
	result := make(map[string][]string, len(groupIDs))
	misses := make([]string, 0, len(groupIDs))
	c.mu.Lock()
	for _, gid := range groupIDs {
		el, ok := c.items[groupCacheKey{appID, gid}]
		if !ok {
			misses = append(misses, gid)
			continue
		}
		e := el.Value.(*groupCacheEntry)
		if time.Now().After(e.expireAt) {
			c.removeLocked(el)
			misses = append(misses, gid)
			continue
		}
		c.lru.MoveToFront(el)
		if e.members != nil {
			result[gid] = e.members
		}
	}
	c.mu.Unlock()

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
			c.fill(groupCacheKey{appID, gid}, nil) // 负缓存：确认无实例，挡住解散后群组的重复回源
			continue
		}
		if len(members) > c.maxMembers {
			result[gid] = members // 大群超预算不入缓存，原样透传
			continue
		}
		c.fill(groupCacheKey{appID, gid}, members)
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

// DisbandGroup 解散群组后逐出该群组拓扑
func (c *GroupMemberCache) DisbandGroup(ctx context.Context, appID, namespace, groupID string) error {
	err := c.GroupStore.DisbandGroup(ctx, appID, namespace, groupID)
	c.evict(appID, groupID)
	return err
}

// fill 回填一条聚合拓扑（已存在的条目原地刷新并移到队首，容量超限从队尾逐出）
func (c *GroupMemberCache) fill(key groupCacheKey, members []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[key]; ok {
		e := el.Value.(*groupCacheEntry)
		e.members = members
		e.expireAt = time.Now().Add(c.ttl)
		c.lru.MoveToFront(el)
		return
	}
	for c.lru.Len() >= c.maxEntries {
		c.removeLocked(c.lru.Back())
	}
	c.items[key] = c.lru.PushFront(&groupCacheEntry{key: key, members: members, expireAt: time.Now().Add(c.ttl)})
}

// evict 逐出一个群组的聚合拓扑（写路径失效，appID 归一化与读路径同口径）
func (c *GroupMemberCache) evict(appID, gid string) {
	appID = mathx.IfEmpty(appID, constants.DefaultAppID)
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[groupCacheKey{appID, gid}]; ok {
		c.removeLocked(el)
	}
}

// removeLocked 摘除 LRU 节点并同步清理索引（须持锁调用）
func (c *GroupMemberCache) removeLocked(el *list.Element) {
	delete(c.items, el.Value.(*groupCacheEntry).key)
	c.lru.Remove(el)
}

// 编译期断言：装饰器必须始终满足 spi.GroupStore 契约
var _ spi.GroupStore = (*GroupMemberCache)(nil)

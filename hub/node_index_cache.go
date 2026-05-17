/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-17 15:12:26
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-17 15:12:26
 * @FilePath: \go-wsc\hub\node_index_cache.go
 * @Description: 消息投递热路径节点索引短 TTL 缓存 — 消减同收件人重复回源
 *
 * 每条 P2P 投递都要查一次用户节点索引判定多端跨节点（本地命中连接也不豁免），
 * in-flight 合并只消减并发扇入，串行连发（聊天/弹幕场景）仍是逐条 Redis 往返。
 * 本缓存以毫秒级窗口复用最近一次索引快照，把串行重复查询降为 O(1) 本地读
 *
 * 陈旧窗口（≤ TTL）的风险全部由既有兜底链路吸收，不新增丢消息路径：
 *   - 远端设备新上线（多端场景）→ 窗口内仅本地投递，远端延迟至多 TTL
 *   - 用户已迁离远端节点 → 多投一次扑空，既有 user_not_found 回告自愈
 *   - 窗口上限（300ms）远小于心跳批写固有的索引延迟（2s），不放大既有感知
 *
 * 消费边界：仅 checkAndRouteToNode（消息投递路由判定）挂载；
 * 踢人分发 / 死索引自愈 / ACK 重路由等实时敏感路径直查不经过
 *
 * 内存有界：条目过期靠访问惰性逐出，冷 key 残留在写路径触达容量上限时
 * 由全量清扫回收；清扫后仍超限则本次写入降级放弃（回退直查，正确性不受影响）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-wsc/routing"
)

// nodeIndexCache 常量（实现细节，不跨包共享）
const (
	// nodeIndexCacheDefaultTTL 条目存活窗口：显著小于心跳批写固有的索引延迟（2s），
	// 保证缓存引入的陈旧感知不劣于索引自身的既有滞后
	nodeIndexCacheDefaultTTL = 300 * time.Millisecond
	// nodeIndexCacheMaxEntries 条目容量上限：仅活跃被发送的收件人入缓存，
	// TTL 窗口极短，常态存活条目远低于上限；超限走全量清扫降级路径
	nodeIndexCacheMaxEntries = 1 << 18
)

// nodeIndexEntry 不可变缓存条目（写入后只读，跨 goroutine 无锁共享）
type nodeIndexEntry struct {
	nodes    []string
	expireAt int64 // unix nano，过期判定基准
}

// nodeIndexCache 节点索引短 TTL 缓存
// 零值可用（ttl/maxEntries 零值回退默认，适配测试中 &Hub{} 直接构造）
type nodeIndexCache struct {
	entries sync.Map // nodeQueryKey → *nodeIndexEntry
	size    atomic.Int64
	ttl     time.Duration // 条目存活窗口，零值回退 nodeIndexCacheDefaultTTL（测试注入短窗口）
	max     int           // 条目容量上限，零值回退 nodeIndexCacheMaxEntries（测试注入小容量）
}

// load 读取缓存条目：命中返回节点快照；过期条目惰性逐出后按未命中处理
func (c *nodeIndexCache) load(key nodeQueryKey) ([]string, bool) {
	v, ok := c.entries.Load(key)
	if !ok {
		return nil, false
	}
	e := v.(*nodeIndexEntry)
	if time.Now().UnixNano() > e.expireAt {
		if c.entries.CompareAndDelete(key, v) {
			c.size.Add(-1)
		}
		return nil, false
	}
	return e.nodes, true
}

// store 写入缓存条目（同 key 覆盖顶替，Swap 区分插入/覆盖保证计数守恒）：
// 新插入触达容量上限时先全量清扫过期条目，
// 清扫后仍超限则回退本次插入（降级直查，不阻塞不扩容）
func (c *nodeIndexCache) store(key nodeQueryKey, nodes []string) {
	ttl := c.ttl
	if ttl <= 0 {
		ttl = nodeIndexCacheDefaultTTL
	}
	e := &nodeIndexEntry{
		nodes:    nodes,
		expireAt: time.Now().UnixNano() + int64(ttl),
	}
	if _, existed := c.entries.Swap(key, e); existed {
		// 覆盖顶替：旧条目（含已过期未逐出的）计数由新条目继承，计数不变
		return
	}
	if c.size.Add(1) > c.capacity() && c.evictExpired() > c.capacity() {
		// 容量降级：回退本次插入（CAS 保证仅移除自己刚写入的条目；
		// 被并发覆盖时计数由顶替方继承，无需回退）
		if c.entries.CompareAndDelete(key, e) {
			c.size.Add(-1)
		}
	}
}

// evictExpired 全量清扫过期条目（低频：仅写入触达容量上限时触发；
// 并发清扫经 CompareAndDelete 保证逐出与计数严格配对）
func (c *nodeIndexCache) evictExpired() int64 {
	now := time.Now().UnixNano()
	c.entries.Range(func(k, v any) bool {
		if v.(*nodeIndexEntry).expireAt < now && c.entries.CompareAndDelete(k, v) {
			c.size.Add(-1)
		}
		return true
	})
	return c.size.Load()
}

// capacity 生效容量上限（零值回退默认）
func (c *nodeIndexCache) capacity() int64 {
	max := c.max
	if max <= 0 {
		return nodeIndexCacheMaxEntries
	}
	return int64(max)
}

// nodeIndexCacheKey 构造缓存 key（与 in-flight 合并器共用三元组定义，信封隔离）
func nodeIndexCacheKey(ctx context.Context, userID string) nodeQueryKey {
	return nodeQueryKey{
		appID:     routing.AppIDFromContext(ctx),
		namespace: routing.NamespaceFromContext(ctx),
		userID:    userID,
	}
}

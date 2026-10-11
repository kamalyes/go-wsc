/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:03:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:03:00
 * @FilePath: \go-wsc\broker\dedup.go
 * @Description: 可靠档本地两代 TTL 去重窗 - 消费幂等的零 Redis 实现
 *
 *   - express 档跳过（latest-wins 重复无害，调用方判定）
 *   - reliable / 必达档过本地窗：分片 map 两代交替（current/prev 按 TTL/2 切换），
 *     纯内存零 RTT——每消息一跳 Redis 的方案已被否决（valkey 单线程脆弱面）
 *   - 必达档另有落库幂等兜底（MessageRecord 唯一键），本地窗是第一道便宜防线
 *
 * 内存账（默认参数）：TTL 60s 下键生命周期 ≤ 2*TTL，1 万 msg/s 约 40MB
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"sync"
	"time"
)

// dedupShard 去重窗分片（单锁粒度，降低并发争用）
type dedupShard struct {
	mu     sync.Mutex
	active map[string]struct{} // 当前代
	prev   map[string]struct{} // 上一代（切换时承接，覆盖跨代窗口）
}

// DedupWindow 本地两代 TTL 去重窗
//
// 两代交替语义：每 TTL/2 一次 rollover（prev=active, active=新建），
// 键的最长可见期接近 2*TTL——覆盖 MQ redelivery 的常规间隔，
// 超窗重复由必达档落库幂等 / reliable 档投递语义兜底
type DedupWindow struct {
	shards   []*dedupShard
	ttl      time.Duration
	rollover time.Duration // 切换周期 = ttl/2
	lastSwap time.Time
	mu       sync.Mutex // 保护 rollover 的切换互斥（分片只被 Seen 内部触碰）
}

// NewDedupWindow 创建去重窗（ttl/shards 零值走默认参数）
func NewDedupWindow(ttl time.Duration, shards int) *DedupWindow {
	if ttl <= 0 {
		ttl = DefaultDedupTTL
	}
	if shards <= 0 {
		shards = DefaultDedupShards
	}
	dw := &DedupWindow{
		shards:   make([]*dedupShard, shards),
		ttl:      ttl,
		rollover: ttl / 2,
		lastSwap: time.Now(),
	}
	for i := range dw.shards {
		dw.shards[i] = &dedupShard{active: make(map[string]struct{})}
	}
	return dw
}

// Seen 记录并判定键是否重复（true = 首见，false = 重复应跳过）
//
// 分片路由：FNV-1a 哈希取模，单键路径单锁；
// rollover 惰性触发（免独立 ticker goroutine，消费节拍天然驱动），
// 空闲后首次调用连续追代（while 循环翻转至当前时刻，跨多代窗口正确出窗）
func (dw *DedupWindow) Seen(key string) bool {
	now := time.Now()

	dw.mu.Lock()
	for now.Sub(dw.lastSwap) >= dw.rollover {
		dw.swapLocked()
		dw.lastSwap = dw.lastSwap.Add(dw.rollover)
	}
	dw.mu.Unlock()

	shard := dw.shards[fnvShard(key, len(dw.shards))]
	shard.mu.Lock()
	defer shard.mu.Unlock()

	if _, ok := shard.active[key]; ok {
		return false
	}
	if _, ok := shard.prev[key]; ok {
		return false
	}
	shard.active[key] = struct{}{}
	return true
}

// swapLocked 代际切换（持 dw.mu 调用）：prev 释放，active 晋升
func (dw *DedupWindow) swapLocked() {
	for _, shard := range dw.shards {
		shard.mu.Lock()
		shard.prev = shard.active
		shard.active = make(map[string]struct{}, len(shard.prev)/2+8)
		shard.mu.Unlock()
	}
}

// fnvShard FNV-1a 分片路由（key 短且量级固定，32 位足够）
func fnvShard(key string, shards int) int {
	var h uint32 = 2166136261
	for i := 0; i < len(key); i++ {
		h ^= uint32(key[i])
		h *= 16777619
	}
	return int(h % uint32(shards))
}

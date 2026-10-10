/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-09 00:50:00
 * @FilePath: \go-wsc\overload\coalescer.go
 * @Description: 高频消息合并器 —— latest-wins 语义（同 key 只保留最新）
 *
 * 高频流式消息（正在输入/已读/状态信号）语义上只需最新值到达：
 *   - Offer(key, msg)：同 key 直接覆盖旧消息（旧值天然过期，覆盖≠丢弃）
 *   - Drain()：批量取出当前所有最新值（投递入口周期性/水位驱动调用）
 *
 * 并发模型：单写者覆盖（Offer 持分片锁覆盖 map 槽位），Drain 换出整个 map
 * （swap-and-rebuild，O(容量) 一次拷贝，投递侧遍历新表无锁）
 * 分片锁降低 Offer 热点竞争（同 ShardedMap 模式，shard = FNV(按字段流式) % N）
 *
 * 容量保护：条目数超限（高频 key 滥用/攻击）时 Offer 返回 false，
 * 上层按高频级语义丢弃计数（latest-wins 的尽头是容量上限，防内存膨胀）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"sync"
	"sync/atomic"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

// coalescerShardCount 合并器分片数（Offer 热点分散）
const coalescerShardCount = 16

// FNV-1a 哈希常量（分片定位：按字段流式吸收，杜绝 key 拼接分配）
const (
	fnvOffset32 uint32 = 2166136261
	fnvPrime32  uint32 = 16777619
)

// fnvString 对字符串追加 FNV-1a 吸收（跨字段顺序调用即得多字段组合哈希）
func fnvString(h uint32, s string) uint32 {
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= fnvPrime32
	}
	return h
}

// EphemeralKey 高频合并 key（用户 × 消息类型：同用户同类型只保最新值）
//
// 结构化 key：map 查找与分片哈希均按字段进行，Offer 热路径零字符串分配
// （拼接式 key 在百万级高频扇出下是持续的 GC 分配热点）
type EphemeralKey struct {
	UserID      string
	MessageType models.MessageType
}

// shardOf 分片定位（FNV-1a 按字段流式吸收）
func (k EphemeralKey) shardOf() int {
	return int(fnvString(fnvString(fnvOffset32, k.UserID), string(k.MessageType)) % coalescerShardCount)
}

// Coalescer 高频消息合并器（latest-wins）
type Coalescer struct {
	shards [coalescerShardCount]struct {
		mu    sync.Mutex
		slots map[EphemeralKey]*models.HubMessage // key → 最新消息
	}

	capacity int          // 总容量上限（所有分片之和）
	size     atomic.Int64 // 当前条目数（容量保护）
	merged   atomic.Int64 // 累计合并（覆盖）次数（观测：削峰效果）
}

// NewCoalescer 创建合并器（capacity <= 0 时用默认容量）
func NewCoalescer(capacity int) *Coalescer {
	if capacity <= 0 {
		capacity = constants.DefaultCoalescerCapacity
	}
	c := &Coalescer{capacity: capacity}
	for i := range c.shards {
		c.shards[i].slots = make(map[EphemeralKey]*models.HubMessage)
	}
	return c
}

// Offer 提交一条高频消息（latest-wins：同 key 覆盖旧消息）
//
// 返回 (accepted, merged)：
//   - accepted=false：容量超限（防滥用）或参数非法，上层按高频语义丢弃计数
//   - merged=true：本次覆盖了同 key 旧消息（latest-wins 削峰，供漏斗 merged 埋点）
func (c *Coalescer) Offer(key EphemeralKey, msg *models.HubMessage) (accepted, merged bool) {
	if key.UserID == "" || key.MessageType == "" || msg == nil {
		return false, false
	}

	shard := &c.shards[key.shardOf()]
	shard.mu.Lock()
	_, existed := shard.slots[key]
	if !existed && c.size.Load() >= int64(c.capacity) {
		// 新 key 且容量已满：拒绝（保护内存）；已存在 key 的覆盖不受限（更新不增条目）
		shard.mu.Unlock()
		return false, false
	}
	shard.slots[key] = msg
	shard.mu.Unlock()

	if existed {
		c.merged.Add(1) // 覆盖旧消息：latest-wins 削峰计数
	} else {
		c.size.Add(1)
	}
	return true, existed
}

// Drain 取出当前所有最新消息（swap-and-rebuild）
//
// 返回合并后的消息列表（可空）；调用方投递后由下一轮 Offer 重新累积
// 高频消息的投递语义：本轮 Drain 的即当前最新快照
func (c *Coalescer) Drain() []*models.HubMessage {
	var drained []*models.HubMessage
	total := 0
	for i := range c.shards {
		shard := &c.shards[i]
		shard.mu.Lock()
		if len(shard.slots) > 0 {
			for _, msg := range shard.slots {
				drained = append(drained, msg)
			}
			// swap：换新 map，投递侧遍历快照期间 Offer 写新 map，互不阻塞
			shard.slots = make(map[EphemeralKey]*models.HubMessage)
			total += len(drained) - total
		}
		shard.mu.Unlock()
	}
	if total > 0 {
		c.size.Add(-int64(total))
	}
	return drained
}

// Size 当前累积条目数（观测用）
func (c *Coalescer) Size() int64 {
	return c.size.Load()
}

// MergedCount 累计合并（覆盖）次数——latest-wins 削峰效果的直接量化
func (c *Coalescer) MergedCount() int64 {
	return c.merged.Load()
}

// Capacity 返回合并器总容量上限
func (c *Coalescer) Capacity() int { return c.capacity }

// startCoalescerDrain 启动合并器 drain ticker（Run 时调用，ctx 结束自动退出）
//
// 周期：每 50ms Drain 一次 latest-wins 快照，逐条按 Receiver 走 P2P 投递
// （TrySendWithFallback：满则高频语义丢弃——最新值本就只需送达一次）

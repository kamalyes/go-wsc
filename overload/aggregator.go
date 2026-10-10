/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-08 01:56:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 19:56:00
 * @FilePath: \go-wsc\overload\aggregator.go
 * @Description: 消息聚合器 —— per-group 窗口攒批 + 数组零重编码单帧推送（N 条合 1 条）
 *
 * 与 Coalescer（latest-wins，丢旧保新，适配行情/输入状态）互补：
 * 聚合语义是全保留合并——窗口内消息按序拼装为 JSON 数组单帧推送，
 * 每连接帧率 = 窗口频率（聚合后推送数 = 在线连接数，内核 pps 友好）
 *
 * 聚合维度与投递路由统一：聚合 key 就是群组广播的 groupID（不引入房间
 * 等平行概念），聚合产出直接回流群组扇出链路；弹幕/跑马灯/滚动通知等
 * 一切"容忍百毫秒延迟、全保留合并"的群组流式消息均可接入
 *
 * 编码前置：Offer 接收的是调用方已序列化的消息字节，数组拼装为纯字节
 * 拼接（[p1,p2,...]），零重编码——1000w msg/s 输入下编码次数 = 消息数
 *
 * 降级语义（性能降级不丢消息）：单群组窗口满 / 群组数超限时 Offer 返回
 * false，调用方直发单条；空群组在 Flush 换出空批时惰性删除
 *
 * 并发模型：16 分片 × per-group 独立锁；Offer 持 shard 锁嵌套 group 锁完成入窗（消除取引用与空窗口删除之间的竞态窗口，防消息写入已删除的孤儿群组）；
 * Flush 同序（shard→group 单向，无死锁），锁内仅双缓冲 swap，数组装配锁外执行（大窗口 memcpy 不阻塞入窗）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"sync"
	"sync/atomic"

	"github.com/kamalyes/go-wsc/constants"
)

// aggregatorShardCount 聚合分片数（Offer 热点分散，同 Coalescer 风格）
const aggregatorShardCount = 16

// MessageAggregator 消息聚合器（per-group 窗口攒批，N 条合 1 条数组帧）
// 零值可用（构造经 NewMessageAggregator 兜底默认容量）；
// drain 节拍与产出投递归编排层（hub 的周期 ticker 调 Flush）
type MessageAggregator struct {
	shards [aggregatorShardCount]struct {
		mu     sync.Mutex
		groups map[aggregatorGroupKey]*aggregatorGroup
	}

	maxPerGroup int // 单群组窗口攒批上限（超限直通降级）
	maxGroups   int // 并发活跃群组数上限（超限新群组直通降级）

	groupCount atomic.Int64 // 活跃群组数（惰性删除后递减）
	offered    atomic.Int64 // 累计聚合入窗消息数（观测）
	aggregated atomic.Int64 // 累计拼装进数组的消息数（观测）
	batches    atomic.Int64 // 累计产出的聚合批数（观测）
	overflowed atomic.Int64 // 累计直通降级次数（观测：窗口满 + 群组数满）
}

// aggregatorGroup 单群组窗口缓冲（独立锁，Offer/Flush 竞争域最小化）
type aggregatorGroup struct {
	appID    string
	groupID  string
	mu       sync.Mutex
	payloads [][]byte // 窗口内已序列化消息字节（Flush 与 spare 双缓冲 swap 复用）
	spare    [][]byte // 已排空待复用的空缓冲（Flush swap 的另一侧，保持 cap 免重分配）
}

// AggregatedBatch 一批聚合产出（单群组一个窗口的全部消息）
type AggregatedBatch struct {
	AppID   string
	GroupID string
	Frame   []byte // JSON 数组体 "[p1,p2,...]"（元素为已序列化消息字节，零重编码）
}

// NewMessageAggregator 创建消息聚合器
// maxPerGroup <= 0 时用 constants.DefaultAggregatorBatchSize；
// maxGroups <= 0 时用 constants.DefaultAggregatorMaxGroups
func NewMessageAggregator(maxPerGroup, maxGroups int) *MessageAggregator {
	if maxPerGroup <= 0 {
		maxPerGroup = constants.DefaultAggregatorBatchSize
	}
	if maxGroups <= 0 {
		maxGroups = constants.DefaultAggregatorMaxGroups
	}
	a := &MessageAggregator{maxPerGroup: maxPerGroup, maxGroups: maxGroups}
	for i := range a.shards {
		a.shards[i].groups = make(map[aggregatorGroupKey]*aggregatorGroup)
	}
	return a
}

// aggregatorGroupKey 群组定位键（appID 隔离：同 groupID 跨应用互不混批）
//
// 结构化 key：map 查找与分片哈希均按字段进行，Offer 热路径零字符串分配
// （拼接式 key 在高频弹幕入窗下是持续的 GC 分配热点）
type aggregatorGroupKey struct {
	appID   string
	groupID string
}

// shardOf 分片定位（FNV-1a 按字段流式吸收，与 Coalescer 同风格）
func (k aggregatorGroupKey) shardOf() int {
	return int(fnvString(fnvString(fnvOffset32, k.appID), k.groupID) % aggregatorShardCount)
}

// Offer 提交一条已序列化的消息进入窗口攒批
//
// 返回 false = 降级直通（调用方单发该条，性能降级不丢消息）：
//   - 单群组窗口满（maxPerGroup）：洪峰下窗口来不及 Flush
//   - 群组数超限（maxGroups）：群组风暴保护
//   - 参数非法（空 appID/groupID/载荷）
//
// 锁结构：shard 锁内嵌套 group 锁完成入窗——若先释放 shard 锁再拿
// group 锁，Flush 可能在此间隙删除空窗口群组，append 会写入已从
// map 摘除的孤儿对象导致消息永久丢失（并发守恒测试可复现）
func (a *MessageAggregator) Offer(appID, groupID string, payload []byte) bool {
	if appID == "" || groupID == "" || len(payload) == 0 {
		return false
	}

	key := aggregatorGroupKey{appID: appID, groupID: groupID}
	shard := &a.shards[key.shardOf()]
	shard.mu.Lock()
	group, ok := shard.groups[key]
	if !ok {
		if a.groupCount.Load() >= int64(a.maxGroups) {
			shard.mu.Unlock()
			a.overflowed.Add(1)
			return false
		}
		group = &aggregatorGroup{appID: appID, groupID: groupID}
		shard.groups[key] = group
		a.groupCount.Add(1)
	}
	group.mu.Lock()
	if len(group.payloads) >= a.maxPerGroup {
		group.mu.Unlock()
		shard.mu.Unlock()
		a.overflowed.Add(1)
		return false
	}
	group.payloads = append(group.payloads, payload)
	group.mu.Unlock()
	shard.mu.Unlock()

	a.offered.Add(1)
	return true
}

// Flush 换出全部窗口攒批并拼装为 JSON 数组帧（drain ticker 周期调用）
//
// 返回本窗口的全部聚合批（可空）；空窗口的群组惰性删除（无人发言的
// 群组在换出后回收，群组数随活跃群组自然收敛）；
// 拼装为纯字节拼接，元素零重编码
//
// 锁内仅做双缓冲 swap（O(1) 指针交换），数组装配全程锁外执行：
// 换出走 spare 交换（ taken 缓冲底层数组在归还前仅 Flush 持有，并发 Offer
// 写入的是 swap 后的新缓冲），大窗口 MB 级 memcpy 不再阻塞入窗路径
// 单写者契约：Flush 仅由 drain ticker 单 goroutine 周期调用，swap 无竞争
func (a *MessageAggregator) Flush() []AggregatedBatch {
	var batches []AggregatedBatch
	for i := range a.shards {
		shard := &a.shards[i]
		shard.mu.Lock()
		for key, group := range shard.groups {
			// 双缓冲 swap（持锁 O(1)）：payloads 交给拼装，spare 接管窗口；
			// 之后的并发 Offer append 到 spare 侧，与锁外拼装完全隔离
			group.mu.Lock()
			payloads := group.payloads
			group.payloads = group.spare[:0]
			group.spare = nil
			group.mu.Unlock()

			var frame []byte
			if len(payloads) > 0 {
				frame = joinJSONPayloads(payloads)
				// 拼装完成后归还缓冲（保持 cap，下轮 swap 零分配）；
				// 仅在 spare 侧更小时回收，防多批交错下缓冲膨胀
				group.mu.Lock()
				if cap(group.spare) < cap(payloads) {
					group.spare = payloads[:0]
				}
				group.mu.Unlock()
			}

			if frame == nil {
				// 空窗口：惰性删除群组（Offer 全程持 shard 锁，不存在
				// 持群组引用的并发 Offer，删除即安全；锁序 shard→group 单向）
				delete(shard.groups, key)
				a.groupCount.Add(-1)
				continue
			}
			batches = append(batches, AggregatedBatch{
				AppID:   group.appID,
				GroupID: group.groupID,
				Frame:   frame,
			})
			a.aggregated.Add(int64(len(payloads)))
			a.batches.Add(1)
		}
		shard.mu.Unlock()
	}
	return batches
}

// joinJSONPayloads 拼装 JSON 数组体（零重编码：元素为已序列化字节，纯拼接）
func joinJSONPayloads(payloads [][]byte) []byte {
	total := 2
	for _, p := range payloads {
		total += len(p) + 1
	}
	frame := make([]byte, 0, total)
	frame = append(frame, '[')
	for i, p := range payloads {
		if i > 0 {
			frame = append(frame, ',')
		}
		frame = append(frame, p...)
	}
	return append(frame, ']')
}

// Stats 快照（观测用）
func (a *MessageAggregator) Stats() map[string]int64 {
	return map[string]int64{
		"max_per_group": int64(a.maxPerGroup),
		"max_groups":    int64(a.maxGroups),
		"groups":        a.groupCount.Load(),
		"offered":       a.offered.Load(),
		"aggregated":    a.aggregated.Load(),
		"batches":       a.batches.Load(),
		"overflowed":    a.overflowed.Load(),
	}
}

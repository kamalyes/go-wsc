/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:20:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-25 23:50:00
 * @FilePath: \go-wsc\group\invalidation.go
 * @Description: 群拓扑失效广播聚合器 - 拓扑写路径失效的跨节点传播（群组域组件）

 * 连接注册自动入组（hub 侧 JoinMemberGroupOnConnect / JoinSystemGroupsOnConnect
 * 均走 AddMembers）使拓扑写成为高频路径：100w 用户上线风暴下逐次 PUBLISH 会打成
 * 广播风暴。本组件以 100ms 窗口合并去重，广播量 O(写次数) → O(群组数/窗口)，
 * 跨节点写一致性窗口从 30s TTL 兜底收敛至约 100ms

 * 数据流：GroupMemberCache 写路径本地逐出后回调 MarkDirty → Run 循环每窗口
 * Flush 一次 → Host.PublishGroupInvalidations 批量发布（事件信封封装与传输
 * 细节由 hub 编排层收口，本组件不感知 DistributedMessage）

 * 并发结构（64 分片，与 ShardedRegistry/GroupMemberCache 的 FNV-1a 分片惯例同款）：
 * MarkDirty 处于拓扑写热路径，全局单锁会让上线风暴的全部注册入组串行化；
 * 分片后锁竞争面缩至 1/64，每分片单层 map（结构体 key 零拼接），Flush 仅在
 * 持分片锁期间做 map swap，分组与发布全部锁外执行

 * 可靠性：Flush 发布失败仅记日志（30s TTL 兜底广播丢失场景）；单机形态由
 * Host 实现侧短路，标记排干即弃零发布

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/spi"
)

// invalidationShards 分片数（2 的幂，配合按位与取模）
const invalidationShards = 64

// invKey 复合标记 key（结构体 map key，零冲突零分配，与 GroupMemberCache 的 groupCacheKey 同惯例）
type invKey struct {
	appID string
	gid   string
}

// invalidationShard 单个分片：独立锁 + 待广播标记集合
type invalidationShard struct {
	mu    sync.Mutex
	dirty map[invKey]struct{}
}

// InvalidationBroadcaster 群拓扑失效广播聚合器（一组件一文件惯例，与 observer/workload 同形态）
type InvalidationBroadcaster struct {
	host     Host // 端口：批量发布与日志（消费者定义接口，hub.Hub 实现）
	interval time.Duration
	logger   spi.Logger // host.GetLogger() 构造期获取

	shards [invalidationShards]invalidationShard
}

// NewInvalidationBroadcaster 创建群拓扑失效广播聚合器
// interval 传零值时使用 constants.DefaultGroupInvalidationFlushInterval
func NewInvalidationBroadcaster(host Host, interval time.Duration) *InvalidationBroadcaster {
	b := &InvalidationBroadcaster{
		host:     host,
		interval: mathx.IF(interval > 0, interval, constants.DefaultGroupInvalidationFlushInterval),
		logger:   host.GetLogger(),
	}
	for i := range b.shards {
		b.shards[i].dirty = make(map[invKey]struct{})
	}
	return b
}

// invShardOf 复合 key → 分片索引（FNV-1a 逐字段散列，零分配热路径）
func invShardOf(appID, gid string) int {
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
	return int(h & (invalidationShards - 1))
}

// MarkDirty 标记该群组拓扑待失效广播（GroupStore 写路径回调，聚合器唯一入口）
// 仅收集不发布：窗口内同 (appID, gid) 多次写合并为一条，上线风暴下广播量恒为群组数量级
func (b *InvalidationBroadcaster) MarkDirty(appID, gid string) {
	s := &b.shards[invShardOf(appID, gid)]
	s.mu.Lock()
	s.dirty[invKey{appID, gid}] = struct{}{}
	s.mu.Unlock()
}

// Run 聚合循环：每窗口 Flush 一次，ctx 结束排干残余后退出（panic 保护由 hub 启动侧兜底）
func (b *InvalidationBroadcaster) Run(ctx context.Context) {
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			b.Flush(ctx)
		case <-ctx.Done():
			// 排干残余标记：停机窗口的失效尽力而为（脱离取消保留链路 value），TTL 兜底
			b.Flush(context.WithoutCancel(ctx))
			return
		}
	}
}

// Flush 排干全部分片：分片锁内仅做 map swap，锁外按 app 分组批量发布
func (b *InvalidationBroadcaster) Flush(ctx context.Context) {
	byApp := make(map[string][]string)
	for i := range b.shards {
		s := &b.shards[i]
		s.mu.Lock()
		if len(s.dirty) == 0 {
			s.mu.Unlock()
			continue
		}
		dirty := s.dirty
		s.dirty = make(map[invKey]struct{}, len(dirty))
		s.mu.Unlock()

		for k := range dirty {
			byApp[k.appID] = append(byApp[k.appID], k.gid)
		}
	}
	if len(byApp) == 0 {
		return
	}

	groupTotal := 0
	for appID, groupIDs := range byApp {
		sort.Strings(groupIDs) // 确定性输出，便于对账与测试断言
		groupTotal += len(groupIDs)

		if err := b.host.PublishGroupInvalidations(ctx, appID, groupIDs); err != nil {
			// 失败仅记日志：本轮失效丢失由 30s TTL 兜底收敛，下一窗口的写会重新标记
			b.logger.WarnContextKV(ctx, "群拓扑失效广播发布失败", "app_id", appID, "group_count", len(groupIDs), "error", err)
		}
	}
	b.logger.DebugContextKV(ctx, "群拓扑失效广播已发布",
		"app_count", len(byApp),
		"group_total", groupTotal,
	)
}

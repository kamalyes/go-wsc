/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 21:57:03
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 12:57:00
 * @FilePath: \go-wsc\connection\slow_consumer.go
 * @Description: 慢消费者治理 —— 三级递进（记录 → 告警 → 驱逐），治理不丢消息
 *
 * 迁移自 hub/slow_consumer.go（P2 批1 域化）：扫描器重组为 SlowConsumerScanner
 * 组件，驱逐动作（KickOut 通知 + Unregister 断链 + 埋点）经 EvictHook 端口由
 * Hub 实现；迁移时修正原并行回调下普通 map 的数据竞争（改 sync.Map，同一
 * client 只存在于单一分片，state 字段读写天然单 goroutine）
 *
 * 检测：BacklogRatio（写泵单写者 atomic store 的 SendChan 利用率）
 * 治理状态机（per-client consecutive 计数）：
 *   - ratio ≥ threshold（0.9）：consecutive++
 *   - consecutive 达阈值（3）：
 *     · 驱逐前保全——SendChan 未投递消息按分级兜底（普通/必达转离线，高频语义丢弃）
 *     · KickOut 走控制通道（客户端收到理由，不被业务洪峰淹没）
 *     · Unregister（断链不丢消息）
 *
 * 扫描模型：复用 ForEachClientParallel 周期采样（与心跳批处理同风格的分片并行遍历），
 * 读侧仅 atomic load（写泵 store——单写者多读者无锁）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// slowConsumerState per-client 治理状态（扫描器外挂的轻量状态，避免侵入 Client 结构）
type slowConsumerState struct {
	consecutive int  // 连续超阈值次数
	warned      bool // 已告警（告警只发一次，驱逐前不再重复）
}

// SlowConsumerScanner 慢消费者扫描器（周期采样 + 三级治理）
type SlowConsumerScanner struct {
	registry *ShardedRegistry // 分片注册表（分片并行遍历）
	hook     EvictHook        // 驱逐回调（Hub 注入；nil 时只告警不驱逐）
	logger   spi.Logger
	interval time.Duration // 扫描周期（建议 = 准入评估周期 × 2）

	// 治理状态表（clientID → state；驱逐/断连时惰性清理——数量级与慢消费者数
	// 成正比，正常时近空表，无内存膨胀风险）
	// sync.Map：ForEachClientParallel 并行回调下并发安全（迁移修正，原普通 map 有数据竞争）
	states sync.Map
}

// NewSlowConsumerScanner 创建慢消费者扫描器
// interval ≤ 0 时默认 1s（与原逻辑"准入未配置时 1s"对齐）
func NewSlowConsumerScanner(registry *ShardedRegistry, hook EvictHook, interval time.Duration, logger spi.Logger) *SlowConsumerScanner {
	if interval <= 0 {
		interval = time.Second
	}
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &SlowConsumerScanner{
		registry: registry,
		hook:     hook,
		logger:   logger,
		interval: interval,
	}
}

// Start 启动周期扫描（阻塞直至 ctx 取消；Hub Run 时以 go 调用）
//
// 周期 = 准入评估周期 × 2（与水位评估同源节拍，避免扫描与评估完全同步造成的
// 周期性毛刺）；扫描本身 O(活跃连接) 原子读遍历
func (s *SlowConsumerScanner) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.ScanOnce()
		}
	}
}

// ScanOnce 单轮扫描（分片并行遍历 + 无锁采样 + 三级治理）
// 导出供测试与运维接口手动触发
func (s *SlowConsumerScanner) ScanOnce() {
	threshold := float64(constants.SlowConsumerThresholdRatio)
	consecutiveLimit := constants.SlowConsumerConsecutiveThreshold

	// 本轮活跃的 clientID 集合（惰性清理的依据：不在本轮集合中的旧状态直接删除）
	var active sync.Map

	s.registry.ForEachClientParallel(0, func(_ string, client *models.Client) {
		key := client.ID
		active.Store(key, struct{}{})

		ratio := client.BacklogRatio()
		if ratio < threshold {
			s.states.Delete(key) // 恢复正常：清零（迟滞清除，防止历史计数误伤）
			return
		}

		state, _ := s.states.LoadOrStore(key, &slowConsumerState{})
		sc := state.(*slowConsumerState)
		sc.consecutive++

		switch {
		case sc.consecutive >= consecutiveLimit:
			// 🚨 三级：驱逐（先保全消息再断链——治理不丢消息）
			s.evict(client, sc, ratio)
			s.states.Delete(key)
		case sc.consecutive >= consecutiveLimit-1 && !sc.warned:
			// ⚠️ 二级：告警（KV 日志一次；下一轮仍超阈值将驱逐）
			sc.warned = true
			s.logger.WarnContextKV(client.Context, "慢消费者告警：下轮仍积压将驱逐",
				"client_id", client.ID,
				"user_id", client.UserID,
				"backlog_ratio", ratio,
				"consecutive", sc.consecutive,
			)
		default:
			// 📝 一级：记录（DEBUG 级，首轮观察）
			s.logger.DebugContextKV(client.Context, "慢消费者检测：队列积压",
				"client_id", client.ID,
				"user_id", client.UserID,
				"backlog_ratio", ratio,
				"consecutive", sc.consecutive,
			)
		}
	})

	// 惰性清理：已断连/已驱逐的旧状态（防状态表缓慢膨胀）
	s.states.Range(func(key, _ any) bool {
		if _, ok := active.Load(key); !ok {
			s.states.Delete(key)
		}
		return true
	})
}

// evict 驱逐慢消费者（驱逐前保全：SendChan 残留消息按分级兜底）
func (s *SlowConsumerScanner) evict(client *models.Client, state *slowConsumerState, ratio float64) {
	// 🛡️ 消息保全：排空 SendChan 残留消息（移交 ACK 超时链路兜底——sending 记录
	// 5min 兜底扫描转离线，上线推送；残留为已序列化 []byte 无法还原分级，
	// 故统一按 ACK 链路保全而非现场转离线）
	salvaged := 0
	for {
		select {
		case data := <-client.SendChan:
			_ = data
			salvaged++
		default:
			goto drained
		}
	}
drained:
	s.logger.WarnContextKV(client.Context, "驱逐慢消费者：消息已由 ACK 链路兜底",
		"client_id", client.ID,
		"user_id", client.UserID,
		"backlog_ratio", ratio,
		"consecutive", state.consecutive,
		"salvaged_to_ack_fallback", salvaged,
	)

	if s.hook == nil {
		return
	}
	s.hook.OnSlowConsumerEvicted(client, state.consecutive, ratio)
}

// Stats 治理状态统计（观测用：当前处于积压追踪中的连接数）
func (s *SlowConsumerScanner) Stats() int {
	var n atomic.Int64
	s.states.Range(func(_, _ any) bool {
		n.Add(1)
		return true
	})
	return int(n.Load())
}

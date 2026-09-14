/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 19:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-11 20:30:00
 * @FilePath: \go-wsc\hub\admission.go
 * @Description: 准入闸门 —— 分级驱动的削峰填谷核心
 *
 * 机制（AIMD + 分级决策矩阵）：
 *   - backlog = written - delivered（在途量，Little's law：backlog/吞吐 ≈ 排队延迟）
 *   - 每 evalInterval 评估一次：backlog > 高水位 → level++（快速升载，过载保护优先）
 *     连续 cooldownLevels 个周期 backlog < 低水位 → level--（缓慢降载，防抖动）
 *   - Admit 按分级×水位矩阵给出三态裁决：放行（实时）/ 延迟（入延迟队列）/ 离线（转离线补发）
 *
 * 设计原则：拒绝≠丢弃——过载时按消息分级路由兜底（延迟/离线），全链路无静默丢弃
 *
 * 性能红线：
 *   - Admit 零分配（读 atomic level + 查表返回枚举，无 map/无 heap）
 *   - 埋点仅 atomic add（onWriteBatch 每批 1 次、OnDelivered 每投递 1 次）
 *   - 未启用（nil gate）时接线层直接跳过，零开销
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

// OverloadLevel 过载水位等级（atomic.Int32 存储，热路径无锁读）
//
// 升级快（每周期最多 +1 但立即生效）、降级慢（连续 N 周期低于低水位才 -1），
// 与 TCP 拥塞控制的 AIMD 语义一致：快速响应过载、缓慢恢复防抖动
type OverloadLevel int32

const (
	// LevelNormal L0：正常，全部实时投递
	LevelNormal OverloadLevel = iota
	// LevelDelayStandard L1：延迟普通级（普通消息入延迟队列，必达/高频照常）
	LevelDelayStandard
	// LevelDelayBroadcast L2：延迟广播（广播走慢速整形，普通级延迟/转离线）
	LevelDelayBroadcast
	// LevelCriticalOnly L3：仅关键（普通级全部转离线，必达走控制通道，高频只发最新）
	LevelCriticalOnly
	// LevelReadOnly L4：只读（仅控制通道可投递，普通级转离线，高频丢弃计数）
	LevelReadOnly
)

// String 水位等级的日志友好表示
func (l OverloadLevel) String() string {
	switch l {
	case LevelReadOnly:
		return "L4_readonly"
	case LevelCriticalOnly:
		return "L3_critical_only"
	case LevelDelayBroadcast:
		return "L2_delay_broadcast"
	case LevelDelayStandard:
		return "L1_delay_standard"
	default:
		return "L0_normal"
	}
}

// AdmitVerdict 准入裁决（零分配枚举，三态）
type AdmitVerdict int

const (
	// VerdictAdmit 放行（实时投递）
	VerdictAdmit AdmitVerdict = iota
	// VerdictDelay 延迟（入有界延迟队列，水位回落后加速 drain——填谷）
	VerdictDelay
	// VerdictOffline 转离线（离线补发链路，上线时推送——仍是送达）
	VerdictOffline
)

// AdmissionGate 准入闸门
//
// 零值/默认构造即可工作（NewAdmissionGate 填充默认水位），未 Start 时
// level 恒为 L0（Admit 全放行），Start 后开始水位评估
type AdmissionGate struct {
	level atomic.Int32 // 当前 OverloadLevel（热路径无锁读）

	written   atomic.Int64 // 累计写入数（写泵埋点）
	delivered atomic.Int64 // 累计投递数（投递入口埋点）

	highWatermark     int64         // 高水位线（在途量超过即升级）
	lowWatermark      int64         // 低水位线（连续 N 周期低于即降级）
	cooldownLevels    int           // 降级所需连续低水位周期数（防抖动）
	evalInterval      time.Duration // 评估周期
	lowStreak         atomic.Int32  // 连续低水位周期计数（降级判定用）
	lastLevelChangeAt atomic.Int64  // 上次水位变更时间戳（UnixNano，日志/观测用）

	stopCh chan struct{}
}

// NewAdmissionGate 创建准入闸门（默认水位开箱即用，参数零值时用 constants 默认值）
func NewAdmissionGate(highWatermark, lowWatermark int64, evalInterval time.Duration) *AdmissionGate {
	if highWatermark <= 0 {
		highWatermark = constants.DefaultAdmissionHighWatermark
	}
	if lowWatermark <= 0 {
		lowWatermark = constants.DefaultAdmissionLowWatermark
	}
	if lowWatermark >= highWatermark {
		lowWatermark = highWatermark / 4 // 保证迟滞区间非空
	}
	if evalInterval <= 0 {
		evalInterval, _ = time.ParseDuration(constants.DefaultAdmissionEvalInterval)
	}
	return &AdmissionGate{
		highWatermark:  highWatermark,
		lowWatermark:   lowWatermark,
		cooldownLevels: constants.DefaultAdmissionCooldownLevels,
		evalInterval:   evalInterval,
		stopCh:         make(chan struct{}),
	}
}

// Start 启动水位评估循环（幂等：重复调用无效果）
func (g *AdmissionGate) Start() {
	select {
	case <-g.stopCh:
		return // 已停止，不重启（生命周期与 Hub 一致）
	default:
	}
	go g.evaluateLoop()
}

// Stop 停止评估循环（Hub 关闭时调用）
func (g *AdmissionGate) Stop() {
	select {
	case <-g.stopCh:
	default:
		close(g.stopCh)
	}
}

// evaluateLoop 水位评估循环（AIMD：升载立即、降载需连续 N 周期确认）
func (g *AdmissionGate) evaluateLoop() {
	ticker := time.NewTicker(g.evalInterval)
	defer ticker.Stop()

	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			backlog := g.written.Load() - g.delivered.Load()
			current := OverloadLevel(g.level.Load())

			if backlog > g.highWatermark {
				// 快速升载：立即 +1（上限 L4）
				if current < LevelReadOnly {
					g.level.Store(int32(current + 1))
					g.lastLevelChangeAt.Store(time.Now().UnixNano())
				}
				g.lowStreak.Store(0)
			} else if backlog < g.lowWatermark {
				// 缓慢降载：连续 N 周期低于低水位才 -1（防抖动）
				streak := g.lowStreak.Add(1)
				if streak >= int32(g.cooldownLevels) && current > LevelNormal {
					g.level.Store(int32(current - 1))
					g.lastLevelChangeAt.Store(time.Now().UnixNano())
					g.lowStreak.Store(0)
				}
			} else {
				// 迟滞区间：不升不降，重置降级计数
				g.lowStreak.Store(0)
			}
		}
	}
}

// Admit 消息级准入裁决（零分配热路径：atomic load + 查表）
//
// 决策矩阵（分级 × 水位 → 放行/延迟/离线）：
//
//	水位             必达级          普通级           高频级
//	L0 正常          放行            放行             放行（合并后）
//	L1 延迟普通      放行            延迟             放行（合并本身削峰）
//	L2 延迟广播      放行            延迟/离线        放行（合并后）
//	L3 仅关键        放行            离线             放行（合并后）
//	L4 只读          放行*           离线             离线**
//
//	* L4 必达级仍放行：控制通道独立（CtrlCh cap 16），KickOut/断链语义不受数据面过载影响
//	** L4 高频级：转离线无意义（状态类语义只需最新），由 Coalescer latest-wins 兜底
func (g *AdmissionGate) Admit(msg *models.HubMessage, isBroadcast bool) AdmitVerdict {
	guarantee := msg.ResolveGuarantee()
	level := OverloadLevel(g.level.Load())

	// 必达级：任何水位放行（控制通道独立 + ACK/离线双保险）
	if guarantee == models.GuaranteeGuaranteed {
		return VerdictAdmit
	}

	switch level {
	case LevelNormal:
		return VerdictAdmit
	case LevelDelayStandard:
		// L1：普通级延迟；高频级放行（合并本身削峰）；广播延迟
		if guarantee == models.GuaranteeEphemeral {
			return VerdictAdmit
		}
		if isBroadcast {
			return VerdictDelay
		}
		return VerdictDelay
	case LevelDelayBroadcast:
		// L2：广播走离线/延迟；普通级 P2P 延迟；高频放行
		if guarantee == models.GuaranteeEphemeral {
			return VerdictAdmit
		}
		return VerdictOffline
	case LevelCriticalOnly:
		// L3：普通级全部转离线（送达保证——延迟到上线补发）；高频放行（只发最新）
		if guarantee == models.GuaranteeEphemeral {
			return VerdictAdmit
		}
		return VerdictOffline
	case LevelReadOnly:
		// L4：普通级转离线；高频级放行（latest-wins 合并后仍投，洪峰削峰）
		if guarantee == models.GuaranteeEphemeral {
			return VerdictAdmit
		}
		return VerdictOffline
	default:
		return VerdictAdmit
	}
}

// OnDelivered 投递埋点（每条消息成功入队后调用，1 次 atomic add）
func (g *AdmissionGate) OnDelivered() {
	g.delivered.Add(1)
}

// onWriteBatch 写泵批埋点（每批 1 次 atomic add，batch 条消息共用）
// Hub 的 nil-safe 转发方法见 hub.go 的 onWriteBatch
func (g *AdmissionGate) onWriteBatch(n int) {
	g.written.Add(int64(n))
}

// Level 当前水位（无锁读，观测/日志用）
func (g *AdmissionGate) Level() OverloadLevel {
	return OverloadLevel(g.level.Load())
}

// Backlog 当前在途量（written - delivered，Little's law 排队延迟估计的分子）
func (g *AdmissionGate) Backlog() int64 {
	return g.written.Load() - g.delivered.Load()
}

// ForceLevel 强制设置水位（测试/运维手动降载用）
func (g *AdmissionGate) ForceLevel(level OverloadLevel) {
	g.level.Store(int32(level))
	g.lastLevelChangeAt.Store(time.Now().UnixNano())
}

// Stats 闸门状态快照（观测用）
type AdmissionStats struct {
	Level     OverloadLevel
	Backlog   int64
	Written   int64
	Delivered int64
}

// Stats 获取状态快照
func (g *AdmissionGate) Stats() AdmissionStats {
	return AdmissionStats{
		Level:     g.Level(),
		Backlog:   g.Backlog(),
		Written:   g.written.Load(),
		Delivered: g.delivered.Load(),
	}
}

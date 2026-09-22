/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 22:16:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-11 23:59:00
 * @FilePath: \go-wsc\overload\tokenbucket.go
 * @Description: GCRA 令牌桶 —— 广播出向整形（削峰填谷的"谷"填充器）
 *
 * GCRA（Generic Cell Rate Algorithm）虚调度实现：
 *   - 仅 1 个 atomic.Int64（theoretical arrival time 纳秒值），无桶无队列
 *   - Allow() 单次 CAS 循环：允许则推进 tat，拒绝则不动
 *   - burst 容量通过容忍的提前量（burst offset）实现
 *
 * 与经典令牌桶的区别：无锁（单 atomic CAS）、无定时补充协程、零分配；
 * SetRate 支持运行时热调整（AIMD 联动：过载降速/恢复提速）
 *
 * 用途：广播扇出前每条消息 1 令牌（非每客户端），洪峰时平滑扇出速率；
 * 被拒消息进有界延迟队列（填谷时 AIMD 提速加速 drain），不丢弃
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-wsc/constants"
)

// Shaper GCRA 令牌桶（出向整形器）
//
// 零值不可直接使用（rate 未设置），NewShaper 构造
type Shaper struct {
	// tat 理论到达时间（theoretical arrival time，UnixNano）
	// GCRA 核心：上次"符合速率的虚拟到达时刻"，CAS 单点更新
	tat atomic.Int64

	// ratePerSec 每秒允许的令牌数（atomic 支持 SetRate 热调整）
	ratePerSec atomic.Int64

	// burstTime 突发容忍量（纳秒）——允许提前量，等效桶容量 burst = ratePerSec * burstTime
	burstTime int64
}

// NewShaper 创建整形器（ratePerSec <= 0 时用默认广播整形速率）
func NewShaper(ratePerSec int64) *Shaper {
	if ratePerSec <= 0 {
		ratePerSec = constants.DefaultBroadcastShaperRate
	}
	s := &Shaper{
		// 突发容忍 100ms：瞬时洪峰可突发 rate*0.1 条，随后回到稳态速率
		burstTime: int64(100 * time.Millisecond),
	}
	s.ratePerSec.Store(ratePerSec)
	s.tat.Store(time.Now().UnixNano())
	return s
}

// Allow 是否放行一个令牌（GCRA 单次 CAS，无锁零分配）
//
// GCRA 判定：now + burstTime >= tat 则放行并推进 tat = max(tat, now) + interval
// 突发场景 tat 领先 now（排队虚拟时间）；稳态 tat 与 now 同步推进
func (s *Shaper) Allow() bool {
	now := time.Now().UnixNano()
	interval := s.Interval()
	if interval <= 0 {
		return true // rate 未设置或为 0：不整形（fail-open，与限流器出错放行语义一致）
	}

	for {
		tat := s.tat.Load()
		// 判定：当前时间 + 突发容忍 ≥ 理论到达时间
		if now+s.burstTime < tat {
			return false
		}
		// 新 tat：从 max(旧 tat, now) 起推进一个 interval（追赶式推进，空闲期不累积令牌）
		newTAT := tat
		if newTAT < now {
			newTAT = now
		}
		newTAT += interval
		if s.tat.CompareAndSwap(tat, newTAT) {
			return true
		}
		// CAS 失败（并发 Allow）：重读 tat 重试
	}
}

// interval 每令牌间隔（纳秒）；rate<=0 返回 0 表示不整形
func (s *Shaper) Interval() int64 {
	rate := s.ratePerSec.Load()
	if rate <= 0 {
		return 0
	}
	// interval = 1e9 / rate（纳秒）；用整数除法，rate ≥ 1 时精度足够
	return int64(time.Second) / rate
}

// SetRate 热调整速率（AIMD 联动：过载时降速、水位恢复时提速）
// min 1（0 表示关闭整形，与关闭语义冲突的由 SetShaping 控制）
func (s *Shaper) SetRate(ratePerSec int64) {
	if ratePerSec < 1 {
		ratePerSec = 1
	}
	s.ratePerSec.Store(ratePerSec)
}

// Rate 当前速率（观测用）
func (s *Shaper) Rate() int64 {
	return s.ratePerSec.Load()
}

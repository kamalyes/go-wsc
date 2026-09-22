/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-09 21:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-10 21:00:00
 * @FilePath: \go-wsc\overload\overload_metrics.go
 * @Description: 过载观测指标 —— 送达漏斗（全 atomic 零锁计数）
 *
 * 送达漏斗（per Guarantee 分级）：
 *   admitted（准入放行） → realtime（实时送达）/ offline（转离线补发）
 *   ephemeral-merged（latest-wins 合并）/ ephemeral-dropped（高频语义丢弃）
 *   unrecoverable（无兜底可用时的最终丢失——仅离线 handler 未配置的部署问题）
 *
 * 守恒不变量（送达保证的量化验收，测试断言）：
 *   必达/普通级：realtime + offline + unrecoverable == admitted
 *   高频级：merged + dropped == 高频 admitted
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"sync/atomic"

	"github.com/kamalyes/go-wsc/models"
)

// guaranteeMetricBuckets 分级计数桶数（标准/高频/必达 + 汇总，对齐 DeliveryGuarantee 枚举宽度）
type guaranteeMetricBuckets [4]atomic.Int64

// OverloadMetrics 过载与送达漏斗指标（零值可用，全 atomic 无锁）
type OverloadMetrics struct {
	// admitted per 分级准入（放行进投递链路）计数
	admitted guaranteeMetricBuckets

	// realtime per 分级实时送达（TrySend/写泵成功）计数
	realtime guaranteeMetricBuckets

	// offlineFallback per 分级转离线补发计数
	offlineFallback guaranteeMetricBuckets

	// ephemeralMerged 高频级 latest-wins 合并（覆盖旧消息）次数
	ephemeralMerged atomic.Int64

	// ephemeralDropped 高频级语义丢弃（latest-wins 过期/容量保护）次数
	ephemeralDropped atomic.Int64

	// unrecoverable 无兜底可用的最终丢失（离线 handler 未配置：部署缺陷信号，正常应为 0）
	unrecoverable guaranteeMetricBuckets

	// admissionRejected 准入延迟/离线裁决计数（VerdictDelay + VerdictOffline）
	admissionDelayed atomic.Int64
	admissionOffline atomic.Int64

	// shaperDenied 出向整形拒绝次数（进延迟队列，非丢弃）
	shaperDenied atomic.Int64

	// writeBatchTotal / writeBatchCount 写泵批量统计（平均合帧条数 = total/count）
	writeBatchTotal atomic.Int64
	writeBatchCount atomic.Int64

	// evictedSlow 慢消费者驱逐计数（驱逐前消息已保全）
	evictedSlow atomic.Int64
}

// bucketIndex DeliveryGuarantee → 计数桶下标
// GuaranteeUnset(0) 映射到桶 3（汇总桶：实际投递时 Unset 已被 Resolve 归一到具体分级，
// 进此桶的仅是未走 Resolve 的旁路调用，观测上单独可见）
func bucketIndex(g models.DeliveryGuarantee) int {
	switch g {
	case models.GuaranteeStandard:
		return 0
	case models.GuaranteeEphemeral:
		return 1
	case models.GuaranteeGuaranteed:
		return 2
	default:
		return 3
	}
}

// RecordAdmitted 准入埋点（每消息 1 次 atomic add）
func (m *OverloadMetrics) RecordAdmitted(g models.DeliveryGuarantee) {
	m.admitted[bucketIndex(g)].Add(1)
}

// RecordRealtime 实时送达埋点
func (m *OverloadMetrics) RecordRealtime(g models.DeliveryGuarantee) {
	m.realtime[bucketIndex(g)].Add(1)
}

// RecordOfflineFallback 转离线补发埋点
func (m *OverloadMetrics) RecordOfflineFallback(g models.DeliveryGuarantee) {
	m.offlineFallback[bucketIndex(g)].Add(1)
}

// RecordEphemeralDrop 高频语义丢弃埋点
func (m *OverloadMetrics) RecordEphemeralDrop() {
	m.ephemeralDropped.Add(1)
}

// RecordEphemeralMerged 高频 latest-wins 合并埋点（Offer 覆盖同 key 旧消息时调用）
func (m *OverloadMetrics) RecordEphemeralMerged() {
	m.ephemeralMerged.Add(1)
}

// RecordUnrecoverable 无兜底丢失埋点（正常为 0；非 0 = 离线 handler 未配置的部署缺陷）
func (m *OverloadMetrics) RecordUnrecoverable(g models.DeliveryGuarantee) {
	m.unrecoverable[bucketIndex(g)].Add(1)
}

// RecordAdmissionVerdict 准入裁决埋点（延迟/离线三态分流）
func (m *OverloadMetrics) RecordAdmissionVerdict(v AdmitVerdict) {
	switch v {
	case VerdictDelay:
		m.admissionDelayed.Add(1)
	case VerdictOffline:
		m.admissionOffline.Add(1)
	}
}

// RecordShaperDenied 整形拒绝埋点（进延迟队列）
func (m *OverloadMetrics) RecordShaperDenied() {
	m.shaperDenied.Add(1)
}

// RecordWriteBatch 写泵批量埋点（n = 本批消息数）
func (m *OverloadMetrics) RecordWriteBatch(n int) {
	m.writeBatchTotal.Add(int64(n))
	m.writeBatchCount.Add(1)
}

// RecordSlowEvict 慢消费者驱逐埋点
func (m *OverloadMetrics) RecordSlowEvict() {
	m.evictedSlow.Add(1)
}

// OverloadStats 送达漏斗与过载指标快照（map 形态，并入现有 stats 查询体系）
func (m *OverloadMetrics) OverloadStats() map[string]any {
	read := func(b *guaranteeMetricBuckets) map[string]int64 {
		return map[string]int64{
			"standard":   b[0].Load(),
			"ephemeral":  b[1].Load(),
			"guaranteed": b[2].Load(),
			"unset":      b[3].Load(),
		}
	}
	return map[string]any{
		"admitted":          read(&m.admitted),
		"realtime":          read(&m.realtime),
		"offline_fallback":  read(&m.offlineFallback),
		"unrecoverable":     read(&m.unrecoverable),
		"ephemeral_merged":  m.ephemeralMerged.Load(),
		"ephemeral_dropped": m.ephemeralDropped.Load(),
		"admission_delayed": m.admissionDelayed.Load(),
		"admission_offline": m.admissionOffline.Load(),
		"shaper_denied":     m.shaperDenied.Load(),
		"write_batch_total": m.writeBatchTotal.Load(),
		"write_batch_count": m.writeBatchCount.Load(),
		"slow_evicted":      m.evictedSlow.Load(),
	}
}

// MetricsSnapshot 标量计数快照（测试/自检断言用；分桶计数见 OverloadStats）
func (m *OverloadMetrics) MetricsSnapshot() map[string]int64 {
	return map[string]int64{
		"ephemeral_merged":  m.ephemeralMerged.Load(),
		"ephemeral_dropped": m.ephemeralDropped.Load(),
		"admission_delayed": m.admissionDelayed.Load(),
		"admission_offline": m.admissionOffline.Load(),
		"shaper_denied":     m.shaperDenied.Load(),
		"slow_evicted":      m.evictedSlow.Load(),
		"write_batch_total": m.writeBatchTotal.Load(),
		"write_batch_count": m.writeBatchCount.Load(),
	}
}

// DeliveryFunnelConsistent 送达漏斗守恒校验（测试/自检断言）
//
// 必达/普通级守恒：realtime + offline_fallback + unrecoverable == admitted
// （高频级走 latest-wins 覆盖语义，覆盖发生在 admitted 计数之后，精确守恒无意义，不校验）
// 返回 false 时返回的 map 描述不守恒的分级与差值（运维告警信号）
func (m *OverloadMetrics) DeliveryFunnelConsistent() (bool, map[string]int64) {
	offences := make(map[string]int64)
	checks := []struct {
		name  string
		index int
	}{
		{"standard", 0},   // GuaranteeStandard
		{"guaranteed", 2}, // GuaranteeGuaranteed
	}
	for _, c := range checks {
		in := m.admitted[c.index].Load()
		out := m.realtime[c.index].Load() + m.offlineFallback[c.index].Load() + m.unrecoverable[c.index].Load()
		if in != out {
			offences[c.name] = in - out
		}
	}
	return len(offences) == 0, offences
}

// DebugHandler 诊断 HTTP handler（pprof + 漏斗快照 + 组件运行态，默认不挂载——调用方自行 mux）
//
// 快照内容（全 atomic 读，无锁一致性按"各自快照"语义）：
//   - overload：送达漏斗计数（admitted → realtime/offline/merged/dropped）
//   - admission：闸门水位/在途量（AIMD 状态一眼可见）
//   - coalescer：合并器槽位数/累计合并次数（合并效率）
//   - delay_queue：填谷队列积压/入出队/满次数（削峰深度）
//
// 挂载示例：http.Handle("/debug/overload", hub.DebugHandler())

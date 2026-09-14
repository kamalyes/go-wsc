/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 10:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 10:00:00
 * @FilePath: \go-wsc\hub\admission_test.go
 * @Description: 准入闸门测试 —— AIMD 升降级 + 分级决策矩阵
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAdmissionGateDecisionMatrix 决策矩阵：分级 × 水位 → 三态裁决
//
// 必达级任何水位放行；高频级 L0-L4 放行（合并本身削峰）；
// 普通级 L0 放行 / L1 延迟 / L2-L4 离线
func TestAdmissionGateDecisionMatrix(t *testing.T) {
	gate := NewAdmissionGate(100, 10, 50*time.Millisecond)
	defer gate.Stop()

	guaranteed := models.NewHubMessage().
		SetMessageType(models.MessageTypePayment).
		WithGuarantee(models.GuaranteeGuaranteed)
	standard := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard)
	ephemeral := models.NewHubMessage().
		SetMessageType(models.MessageTypeTyping).
		WithGuarantee(models.GuaranteeEphemeral)

	levels := []OverloadLevel{LevelNormal, LevelDelayStandard, LevelDelayBroadcast, LevelCriticalOnly, LevelReadOnly}

	for _, level := range levels {
		gate.ForceLevel(level)
		t.Run(level.String(), func(t *testing.T) {
			// 必达级恒放行（控制通道独立，不受数据面过载影响）
			assert.Equal(t, VerdictAdmit, gate.Admit(guaranteed, false), "必达级 P2P 应放行")
			assert.Equal(t, VerdictAdmit, gate.Admit(guaranteed, true), "必达级广播应放行")

			// 高频级恒放行（latest-wins 合并削峰 + 只需最新值语义）
			assert.Equal(t, VerdictAdmit, gate.Admit(ephemeral, false), "高频级 P2P 应放行")
			assert.Equal(t, VerdictAdmit, gate.Admit(ephemeral, true), "高频级广播应放行")
		})
	}

	// 普通级随水位收紧（核心削峰矩阵）
	gate.ForceLevel(LevelNormal)
	assert.Equal(t, VerdictAdmit, gate.Admit(standard, false), "L0 普通级放行")
	gate.ForceLevel(LevelDelayStandard)
	assert.Equal(t, VerdictDelay, gate.Admit(standard, false), "L1 普通级延迟")
	assert.Equal(t, VerdictDelay, gate.Admit(standard, true), "L1 普通级广播延迟")
	gate.ForceLevel(LevelDelayBroadcast)
	assert.Equal(t, VerdictOffline, gate.Admit(standard, true), "L2 普通级广播转离线")
	assert.Equal(t, VerdictOffline, gate.Admit(standard, false), "L2 普通级 P2P 转离线")
	gate.ForceLevel(LevelCriticalOnly)
	assert.Equal(t, VerdictOffline, gate.Admit(standard, false), "L3 普通级转离线")
	gate.ForceLevel(LevelReadOnly)
	assert.Equal(t, VerdictOffline, gate.Admit(standard, false), "L4 普通级转离线")
}

// TestAdmissionGateAIMDLevelUp 过载升载：backlog 超高水位 → level+1（快速，立即生效）
func TestAdmissionGateAIMDLevelUp(t *testing.T) {
	gate := NewAdmissionGate(100, 10, 10*time.Millisecond)
	defer gate.Stop()
	gate.Start()

	// 模拟写入洪峰：在途量 = written - delivered 超过高水位
	gate.onWriteBatch(150) // backlog=150 > high=100

	require.Eventually(t, func() bool {
		return gate.Level() >= LevelDelayStandard
	}, 2*time.Second, 5*time.Millisecond, "过载后应在 1 个评估周期内升级")
}

// TestAdmissionGateAIMDLevelUpCap 升级上限 L4：持续过载不会超过 LevelReadOnly
func TestAdmissionGateAIMDLevelUpCap(t *testing.T) {
	gate := NewAdmissionGate(10, 1, 5*time.Millisecond)
	defer gate.Stop()
	gate.Start()

	gate.onWriteBatch(1000) // backlog 恒超高水位

	require.Eventually(t, func() bool {
		return gate.Level() == LevelReadOnly
	}, 2*time.Second, 2*time.Millisecond, "持续过载应升到 L4 上限")

	// 多等几个周期验证不越界
	time.Sleep(30 * time.Millisecond)
	assert.Equal(t, LevelReadOnly, gate.Level(), "升级不应超过 L4")
}

// TestAdmissionGateAIMDLevelDown 缓慢降级：连续 N 周期低于低水位才 -1（防抖动）
func TestAdmissionGateAIMDLevelDown(t *testing.T) {
	gate := NewAdmissionGate(100, 50, 10*time.Millisecond)
	defer gate.Stop()
	gate.Start()

	// 升到 L2
	gate.onWriteBatch(200)
	require.Eventually(t, func() bool { return gate.Level() == LevelDelayBroadcast }, 2*time.Second, 2*time.Millisecond)

	// 水位回落（无新增写入，backlog 仍为 200 > low=50——需要 delivered 追上）
	// 投递追平：delivered 增加 200，backlog 归 0 < low
	for i := 0; i < 200; i++ {
		gate.OnDelivered()
	}

	// 连续 cooldown 个周期低于低水位 → 降 1 级（L2 → L1）
	require.Eventually(t, func() bool {
		return gate.Level() == LevelDelayStandard
	}, 2*time.Second, 5*time.Millisecond, "连续低水位后应降一级")

	// 再等连续 N 周期 → 再降一级（L1 → L0）
	require.Eventually(t, func() bool {
		return gate.Level() == LevelNormal
	}, 2*time.Second, 5*time.Millisecond, "再次连续低水位应降到 L0")
}

// TestAdmissionGateDownNotInstant 单周期低水位不降级（防抖动：需连续 N 周期）
func TestAdmissionGateDownNotInstant(t *testing.T) {
	// cooldown=3：构造评估周期远长于观察窗口，观察 1 个周期内的行为
	gate := NewAdmissionGate(100, 50, 500*time.Millisecond)
	defer gate.Stop()
	gate.ForceLevel(LevelDelayStandard)

	gate.onWriteBatch(200)
	gate.Start()

	// 低于低水位（delivered 追平）
	for i := 0; i < 200; i++ {
		gate.OnDelivered()
	}

	// 只过 1 个评估周期：不应降级
	time.Sleep(600 * time.Millisecond)
	assert.Equal(t, LevelDelayStandard, gate.Level(), "单周期低水位不应立即降级（防抖动）")
}

// TestAdmissionGateHysteresis 迟滞区间：低水位 < backlog < 高水位 → 水位不变
func TestAdmissionGateHysteresis(t *testing.T) {
	gate := NewAdmissionGate(100, 50, 20*time.Millisecond)
	defer gate.Stop()
	gate.ForceLevel(LevelDelayStandard)
	gate.Start()

	// backlog=60：位于 (50, 100) 迟滞区间
	gate.onWriteBatch(60)

	time.Sleep(80 * time.Millisecond)
	assert.Equal(t, LevelDelayStandard, gate.Level(), "迟滞区间内水位应保持不变")
	assert.Equal(t, int64(60), gate.Backlog(), "在途量应等于 written-delivered")
}

// TestAdmissionGateStats 快照一致性
func TestAdmissionGateStats(t *testing.T) {
	gate := NewAdmissionGate(100, 50, 50*time.Millisecond)
	defer gate.Stop()

	gate.onWriteBatch(30)
	for i := 0; i < 10; i++ {
		gate.OnDelivered()
	}

	stats := gate.Stats()
	assert.Equal(t, int64(30), stats.Written)
	assert.Equal(t, int64(10), stats.Delivered)
	assert.Equal(t, int64(20), stats.Backlog)
	assert.Equal(t, LevelNormal, stats.Level)
}

// TestAdmissionGateDefaultParams 默认参数兜底（零值 → constants 默认）
func TestAdmissionGateDefaultParams(t *testing.T) {
	gate := NewAdmissionGate(0, 0, 0)
	defer gate.Stop()

	stats := gate.Stats()
	assert.Equal(t, int64(constants.DefaultAdmissionHighWatermark), gate.highWatermark)
	assert.Equal(t, int64(constants.DefaultAdmissionLowWatermark), gate.lowWatermark)
	assert.Equal(t, constants.DefaultAdmissionCooldownLevels, gate.cooldownLevels)
	assert.Equal(t, LevelNormal, stats.Level, "未 Start 时恒为 L0")

	interval, err := time.ParseDuration(constants.DefaultAdmissionEvalInterval)
	require.NoError(t, err)
	assert.Equal(t, interval, gate.evalInterval)
}

// TestAdmissionGateLowGeHigh 防御：low >= high 时收缩为 high/4（迟滞区间非空）
func TestAdmissionGateLowGeHigh(t *testing.T) {
	gate := NewAdmissionGate(100, 200, time.Second)
	defer gate.Stop()
	assert.Equal(t, int64(25), gate.lowWatermark, "low>=high 时应收缩为 high/4")
}

// TestAdmissionGateStopIdempotent Stop 幂等（重复 Stop 不 panic）
func TestAdmissionGateStopIdempotent(t *testing.T) {
	gate := NewAdmissionGate(100, 50, time.Second)
	gate.Start()
	time.Sleep(10 * time.Millisecond)
	gate.Stop()
	gate.Stop() // 重复 Stop：不 panic

	// Stop 后再 Start：不重启（生命周期与 Hub 一致）
	gate.Start()
	time.Sleep(30 * time.Millisecond)
}

// TestAdmitMessageNilGate Hub 接线：nil 闸门全放行 + 漏斗计数仍工作
func TestAdmitMessageNilGate(t *testing.T) {
	hub := NewHub(newTestHubConfig())
	hub.admission.Store(nil) // 模拟关闭准入

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard)

	verdict := hub.admitMessage(msg, false)
	assert.Equal(t, VerdictAdmit, verdict, "nil 闸门应全放行")

	stats := hub.overloadMetrics.OverloadStats()
	admitted := stats["admitted"].(map[string]int64)
	assert.Equal(t, int64(1), admitted["standard"], "nil 闸门时漏斗 admitted 计数仍生效")
}

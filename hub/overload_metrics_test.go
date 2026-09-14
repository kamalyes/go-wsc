/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 11:57:26
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 12:00:00
 * @FilePath: \go-wsc\hub\overload_metrics_test.go
 * @Description: 送达漏斗指标测试 —— 守恒不变量 + 快照形态
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOverloadMetricsFunnelConsistent 守恒不变量：realtime + offline + unrecoverable == admitted
func TestOverloadMetricsFunnelConsistent(t *testing.T) {
	var m OverloadMetrics

	// 普通级漏斗：100 admitted = 70 realtime + 25 offline + 5 unrecoverable
	for i := 0; i < 100; i++ {
		m.recordAdmitted(models.GuaranteeStandard)
	}
	for i := 0; i < 70; i++ {
		m.recordRealtime(models.GuaranteeStandard)
	}
	for i := 0; i < 25; i++ {
		m.recordOfflineFallback(models.GuaranteeStandard)
	}
	for i := 0; i < 5; i++ {
		m.recordUnrecoverable(models.GuaranteeStandard)
	}

	ok, offences := m.DeliveryFunnelConsistent()
	assert.True(t, ok, "漏斗应守恒")
	assert.Empty(t, offences)

	// 再记一笔 admitted 破坏守恒
	m.recordAdmitted(models.GuaranteeStandard)
	ok, offences = m.DeliveryFunnelConsistent()
	assert.False(t, ok, "不平衡时应检出")
	assert.Equal(t, int64(1), offences["standard"], "差值应为 1")
}

// TestOverloadMetricsFunnelGuaranteed 必达级守恒独立校验
func TestOverloadMetricsFunnelGuaranteed(t *testing.T) {
	var m OverloadMetrics

	for i := 0; i < 50; i++ {
		m.recordAdmitted(models.GuaranteeGuaranteed)
	}
	for i := 0; i < 50; i++ {
		m.recordRealtime(models.GuaranteeGuaranteed)
	}

	ok, _ := m.DeliveryFunnelConsistent()
	assert.True(t, ok, "必达级全部实时送达也应守恒")
}

// TestOverloadMetricsEphemeral 高频级计数（merged + dropped 独立轨道）
func TestOverloadMetricsEphemeral(t *testing.T) {
	var m OverloadMetrics

	m.recordAdmitted(models.GuaranteeEphemeral)
	m.recordEphemeralMerged()
	m.recordEphemeralMerged()
	m.recordEphemeralMerged()
	m.recordEphemeralDrop()

	stats := m.OverloadStats()
	assert.Equal(t, int64(3), stats["ephemeral_merged"])
	assert.Equal(t, int64(1), stats["ephemeral_dropped"])
}

// TestOverloadMetricsStatsSnapshot 快照结构完整性（map 形态可 JSON 序列化）
func TestOverloadMetricsStatsSnapshot(t *testing.T) {
	var m OverloadMetrics
	m.recordAdmitted(models.GuaranteeStandard)
	m.recordRealtime(models.GuaranteeStandard)
	m.recordAdmissionVerdict(VerdictDelay)
	m.recordShaperDenied()
	m.recordWriteBatch(16)
	m.recordSlowEvict()

	stats := m.OverloadStats()

	requiredKeys := []string{
		"admitted", "realtime", "offline_fallback", "unrecoverable",
		"ephemeral_merged", "ephemeral_dropped",
		"admission_delayed", "admission_offline", "shaper_denied",
		"write_batch_total", "write_batch_count", "slow_evicted",
	}
	for _, key := range requiredKeys {
		assert.Contains(t, stats, key, "快照应包含 %s", key)
	}

	// 可序列化（HTTP 端点用）
	_, err := json.Marshal(stats)
	assert.NoError(t, err, "快照应可 JSON 序列化")

	// 写泵平均批量 = total / count
	assert.Equal(t, int64(16), stats["write_batch_total"])
	assert.Equal(t, int64(1), stats["write_batch_count"])
}

// TestOverloadMetricsAdmissionVerdict 准入裁决三态分流埋点
func TestOverloadMetricsAdmissionVerdict(t *testing.T) {
	var m OverloadMetrics

	m.recordAdmissionVerdict(VerdictAdmit) // 放行不计入 delayed/offline
	m.recordAdmissionVerdict(VerdictDelay)
	m.recordAdmissionVerdict(VerdictDelay)
	m.recordAdmissionVerdict(VerdictOffline)

	stats := m.OverloadStats()
	assert.Equal(t, int64(2), stats["admission_delayed"])
	assert.Equal(t, int64(1), stats["admission_offline"])
}

// TestDebugHandler 诊断端点：漏斗快照 + pprof 路由挂载
func TestDebugHandler(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	server := httptest.NewServer(hub.DebugHandler())
	defer server.Close()

	// /debug/overload 返回 JSON 快照
	resp, err := server.Client().Get(server.URL + "/debug/overload")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	assert.Contains(t, payload, "overload")
	assert.Contains(t, payload, "runtime")

	// 组件运行态快照（atomic 热替换安全的观测面）
	assert.Contains(t, payload, "admission", "应包含准入闸门快照")
	assert.Contains(t, payload, "coalescer", "应包含合并器快照")
	assert.Contains(t, payload, "delay_queue", "应包含延迟队列快照")

	// pprof 索引页可达
	resp2, err := server.Client().Get(server.URL + "/debug/pprof/")
	require.NoError(t, err)
	defer resp2.Body.Close()
	assert.Equal(t, http.StatusOK, resp2.StatusCode)
}

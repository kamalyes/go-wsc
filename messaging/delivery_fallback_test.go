/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 14:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 14:30:00
 * @FilePath: \go-wsc\messaging\delivery_fallback_test.go
 * @Description: 投递兜底路由白盒单元测试（覆盖 delivery_fallback.go）
 *
 * 重点回归 guarantee 透传语义：广播扇出在循环外预解析一次 guarantee 后逐客户端传入
 * TrySendWithFallback / routeDeliveryFallback，二者必须使用透传值而非对每条 msg 重新
 * ResolveGuarantee。测试通过「传入与 msg 类型默认分级不同的 guarantee」锁定该契约：
 * 若实现回退到重新解析，埋点会落入 msg 默认分级桶而非透传分级桶。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
)

// guaranteeBucket 读取送达漏斗某分级桶（realtime/offline_fallback/unrecoverable）下指定分级计数
func guaranteeBucket(t *testing.T, metrics *overload.OverloadMetrics, bucket, level string) int64 {
	t.Helper()
	b, ok := metrics.OverloadStats()[bucket].(map[string]int64)
	require.True(t, ok, "OverloadStats 应返回 %s 分桶 map", bucket)
	return b[level]
}

// overloadScalar 读取送达漏斗标量指标（ephemeral_dropped 等）
func overloadScalar(t *testing.T, metrics *overload.OverloadMetrics, key string) int64 {
	t.Helper()
	v, ok := metrics.OverloadStats()[key].(int64)
	require.True(t, ok, "OverloadStats 应返回 %s 标量", key)
	return v
}

// ============================================================================
// TrySendWithFallback 成功路径 —— guarantee 透传
// ============================================================================

// TestTrySendWithFallback_RealtimeUsesProvidedGuarantee 验证实时送达埋点使用透传的
// guarantee 参数：text 消息默认分级为 Standard，但显式传入 Guaranteed 时，realtime
// 应计在 guaranteed 分桶而非 standard 分桶，证明实现未回退到重新 ResolveGuarantee。
func TestTrySendWithFallback_RealtimeUsesProvidedGuarantee(t *testing.T) {
	m, host := newTestManager()

	client := makeTestClient("c-fb-realtime", "u-fb-realtime") // 开放，SendChan 缓冲 16
	msg := makeGroupMessage("sender")                          // text：ResolveGuarantee 默认 = Standard

	ok := m.TrySendWithFallback(client, []byte("payload"), msg, models.GuaranteeGuaranteed)
	assert.True(t, ok, "开放客户端投递应成功")

	// 关键断言：埋点落透传分级（Guaranteed），而非 msg 重新解析的 Standard
	assert.Equal(t, int64(1), guaranteeBucket(t, host.metrics, "realtime", "guaranteed"),
		"realtime 应计在透传的 guaranteed 分桶")
	assert.Equal(t, int64(0), guaranteeBucket(t, host.metrics, "realtime", "standard"),
		"realtime 的 standard 分桶应保持 0（证明未重新 ResolveGuarantee）")

	// 通道应收到消息
	select {
	case <-client.SendChan:
	default:
		t.Fatal("投递成功后 SendChan 应收到消息")
	}
}

// ============================================================================
// TrySendWithFallback 失败路径 —— 按透传 guarantee 分级兜底路由
// ============================================================================

// TestTrySendWithFallback_FailureRoutesByProvidedGuarantee 验证失败兜底路由按透传的
// guarantee 分级分流：高频级语义丢弃、普通级/必达级转离线。text 消息默认分级为
// Standard，因此 Guaranteed 落在 guaranteed 分桶、Ephemeral 走丢弃（而非 offline）
// 均直接锁定「透传而非重新解析」契约。
func TestTrySendWithFallback_FailureRoutesByProvidedGuarantee(t *testing.T) {
	cases := []struct {
		name        string
		guarantee   models.DeliveryGuarantee
		needOffline bool // Standard/Guaranteed 需注入 offlineHandler 才走 offline_fallback
		assertFn    func(t *testing.T, metrics *overload.OverloadMetrics)
	}{
		{
			name:        "高频级-语义丢弃",
			guarantee:   models.GuaranteeEphemeral,
			needOffline: false,
			assertFn: func(t *testing.T, metrics *overload.OverloadMetrics) {
				assert.Equal(t, int64(1), overloadScalar(t, metrics, "ephemeral_dropped"),
					"高频级应走语义丢弃")
				assert.Equal(t, int64(0), guaranteeBucket(t, metrics, "offline_fallback", "standard"),
					"高频级不应转离线（证明未回退到 msg 默认 Standard 分级）")
			},
		},
		{
			name:        "普通级-转离线",
			guarantee:   models.GuaranteeStandard,
			needOffline: true,
			assertFn: func(t *testing.T, metrics *overload.OverloadMetrics) {
				assert.Equal(t, int64(1), guaranteeBucket(t, metrics, "offline_fallback", "standard"),
					"普通级失败应转离线补发")
			},
		},
		{
			name:        "必达级-转离线",
			guarantee:   models.GuaranteeGuaranteed,
			needOffline: true,
			assertFn: func(t *testing.T, metrics *overload.OverloadMetrics) {
				assert.Equal(t, int64(1), guaranteeBucket(t, metrics, "offline_fallback", "guaranteed"),
					"必达级失败应落到 guaranteed 分桶（透传而非 msg 默认 Standard）")
				assert.Equal(t, int64(0), guaranteeBucket(t, metrics, "offline_fallback", "standard"),
					"必达级不应落到 standard 分桶")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, host := newTestManager()
			if tc.needOffline {
				offline, _ := newOfflineRecordingHandler()
				m.offlineHandler = offline
			}

			client := makeTestClient("c-fb-"+tc.name, "u-fb-"+tc.name)
			client.MarkClosed() // 触发 TrySend 失败
			msg := makeGroupMessage("sender")

			ok := m.TrySendWithFallback(client, []byte("payload"), msg, tc.guarantee)
			assert.False(t, ok, "已关闭客户端投递应失败")

			tc.assertFn(t, host.metrics)
		})
	}
}

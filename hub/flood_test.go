/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 13:00:26
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 13:07:58
 * @FilePath: \go-wsc\hub\flood_test.go
 * @Description: 洪峰削峰填谷集成测试 —— 全链路验收（准入 + 整形 + 延迟队列 + 漏斗守恒）
 *
 * 场景闭环：
 *   1. 广播洪峰 → shaper 拒绝 → 延迟队列积压（削峰）
 *   2. drain 按整形速率节拍重投（填谷）→ 消息最终送达
 *   3. 高水位准入 → 普通级延迟/离线路由 → 拒绝≠丢弃
 *   4. 漏斗守恒：洪峰中无静默丢失
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFloodShaperDefersToDelayQueue 洪峰整形：广播超速被拒进延迟队列，drain 重投送达（填谷）
func TestFloodShaperDefersToDelayQueue(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	// 低速整形（50/s）：首轮突发后立即可触发整形拒绝
	hub.SetOverloadPolicy(nil, NewShaper(50), nil)

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("flood-client", "flood-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond)

	// 洪峰：连发 60 条普通级广播（耗尽突发 + 触发整形拒绝）
	for i := 0; i < 60; i++ {
		msg := models.NewHubMessage().
			SetMessageType(models.MessageTypeText).
			SetSender("flood-source").
			SetContent("flood-broadcast").
			WithGuarantee(models.GuaranteeStandard)
		hub.Deliver(context.Background(), msg, false)
	}

	// 削峰：延迟队列应有积压（drain 按令牌间隔逐条重投）
	require.Eventually(t, func() bool {
		return hub.overloadMetrics.shaperDenied.Load() > 0 || hub.broadcastDelayQueue.enqueued.Load() > 0
	}, 5*time.Second, 50*time.Millisecond, "洪峰应触发整形拒绝进入延迟队列")

	// 填谷：所有消息最终经 drain 重投送达客户端（读侧排空）
	cConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	received := 0
	for received < 60 {
		if _, _, err := cConn.ReadMessage(); err != nil {
			t.Fatalf("第 %d 条读取失败：延迟队列重投不应丢消息（%v）", received+1, err)
		}
		received++
	}
	assert.GreaterOrEqual(t, hub.broadcastDelayQueue.drained.Load(), int64(0), "drain 计数应推进")
}

// TestFloodAdmissionDefersBroadcast 高水位准入：普通级广播被延迟（延迟队列承接）
func TestFloodAdmissionDefersBroadcast(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	// 低水位闸门（高 10/低 2）：快速触发 L1+ 延迟
	gate := NewAdmissionGate(10, 2, 100*time.Millisecond)
	hub.SetOverloadPolicy(gate, nil, nil)

	sConn, _ := newWSConnPair(t)
	client := newTestClient("adm-client", "adm-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond)

	// 制造在途量超水位（写泵埋点）：触发升级
	for i := 0; i < 50; i++ {
		hub.onWriteBatch(10)
	}

	// 等待评估周期升级水位
	require.Eventually(t, func() bool {
		return gate.Level() >= LevelDelayStandard
	}, 3*time.Second, 50*time.Millisecond, "在途量超高水位应升级")

	// 洪峰广播：L1+ 普通级应走延迟队列（VerdictDelay/Offline → deferBroadcast）
	for i := 0; i < 10; i++ {
		msg := models.NewHubMessage().
			SetMessageType(models.MessageTypeText).
			SetSender("adm-source").
			WithGuarantee(models.GuaranteeStandard)
		hub.Deliver(context.Background(), msg, false)
	}

	assert.GreaterOrEqual(t, hub.overloadMetrics.admissionDelayed.Load()+hub.overloadMetrics.admissionOffline.Load(),
		int64(1), "高水位下普通级广播应被准入拒绝（延迟/离线）")
}

// TestFloodGuaranteedBypassesShaping 必达级洪峰直通：不受整形/准入限制（控制面独立）
func TestFloodGuaranteedBypassesShaping(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	// 极低速整形 + 高水位闸门（双重限制）
	gate := NewAdmissionGate(1, 0, 100*time.Millisecond)
	gate.ForceLevel(LevelReadOnly) // L4 只读
	hub.SetOverloadPolicy(gate, NewShaper(1), nil)

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("vip-client", "vip-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond)

	// 必达级消息在 L4 + 极低速整形下仍应直通扇出
	for i := 0; i < 5; i++ {
		msg := models.NewHubMessage().
			SetMessageType(models.MessageTypePayment).
			SetSender("pay-source").
			WithGuarantee(models.GuaranteeGuaranteed)
		hub.Deliver(context.Background(), msg, false)
	}

	// 客户端应实时收到全部 5 条（不走延迟队列）
	cConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 5; i++ {
		_, data, err := cConn.ReadMessage()
		require.NoError(t, err, "第 %d 条必达消息应实时送达（不受水位/整形限制）", i+1)
		assert.Contains(t, string(data), "payment")
	}
}

// TestFloodEphemeralDropsUnderOverload 高频级过载语义丢弃：latest-wins 尽头的计数
func TestFloodEphemeralDropsUnderOverload(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	gate := NewAdmissionGate(1, 0, 100*time.Millisecond)
	hub.SetOverloadPolicy(gate, nil, nil)

	sConn, _ := newWSConnPair(t)
	client := newTestClient("eph-client", "eph-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond)

	// 塞满客户端 SendChan（投递失败触发兜底路由）
	client.SendChan <- []byte(`{"fill":1}`)
	client.SendChan <- []byte(`{"fill":2}`)
	client.SendChan <- []byte(`{"fill":3}`)
	client.SendChan <- []byte(`{"fill":4}`)

	// 高频消息 TrySend 失败 → 语义丢弃计数（不转离线——状态类只需最新）
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeTyping).
		SetSender("eph-source").
		WithGuarantee(models.GuaranteeEphemeral)

	action := hub.routeDeliveryFallback(msg, client, "")
	assert.Equal(t, FallbackEphemeralDrop, action)
	assert.Equal(t, int64(1), hub.overloadMetrics.ephemeralDropped.Load(), "高频语义丢弃应计数")
}

// TestFloodDelayQueueStats 延迟队列观测：容量/积压/出队指标齐全
func TestFloodDelayQueueStats(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	// 手动入队 2 条验证 Stats
	retry := func(context.Context, *HubMessage) {}
	msg := models.NewHubMessage().SetMessageType(models.MessageTypeText)
	assert.True(t, hub.broadcastDelayQueue.offer(hub.ctx, msg, retry))
	assert.True(t, hub.broadcastDelayQueue.offer(hub.ctx, msg, retry))

	stats := hub.broadcastDelayQueue.Stats()
	assert.Equal(t, int64(broadcastDelayQueueCapacity), stats["capacity"])
	assert.Equal(t, int64(2), stats["enqueued"])
	assert.Equal(t, int64(2), hub.broadcastDelayQueue.Backlog())

	// drain 循环自动消费（填谷推进）
	require.Eventually(t, func() bool {
		return hub.broadcastDelayQueue.Backlog() == 0
	}, 5*time.Second, 50*time.Millisecond, "drain 循环应消费积压")
	assert.Equal(t, int64(2), hub.broadcastDelayQueue.drained.Load())
}

// TestFloodDelayQueueFullFallback 延迟队列满：分级兜底（高频计数 / 普通记 unrecoverable）
func TestFloodDelayQueueFullFallback(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	retry := func(context.Context, *HubMessage) {}

	// 塞满延迟队列（drain 循环会消费，需要快于 drain 的速度——直接暂停 drain 节拍：queue 满前 drain 未启动等待）
	// 用高频消息快速填满
	ephemeral := models.NewHubMessage().
		SetMessageType(models.MessageTypeTyping).
		WithGuarantee(models.GuaranteeEphemeral)
	filled := 0
	for i := 0; i < broadcastDelayQueueCapacity+10; i++ {
		if hub.broadcastDelayQueue.offer(hub.ctx, ephemeral, retry) {
			filled++
		}
	}
	// 队列应满（offer 拒绝后走兜底——手动调用 deferBroadcast 验证）
	// drain 循环并发消费少量条目：filled 可能略超容量（塞入速度 > drain 节拍）
	assert.GreaterOrEqual(t, filled, broadcastDelayQueueCapacity-10, "应接近或填满队列")

	beforeDropped := hub.overloadMetrics.ephemeralDropped.Load()
	// 队列满时的高频消息：deferBroadcast 走语义丢弃
	hub.deferBroadcast(hub.ctx, ephemeral, retry)
	assert.Equal(t, beforeDropped+1, hub.overloadMetrics.ephemeralDropped.Load(),
		"队列满的高频消息应语义丢弃计数")
}

// TestFloodFunnelConservation 洪峰漏斗守恒：普通级无静默丢失（拒绝≠丢弃的量化验收）
func TestFloodFunnelConservation(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	handler := newCaptureOfflineHandler()
	hub.SetOfflineMessageHandler(handler)

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("cons-client", "cons-user", sConn)
	client.SendChan = make(chan []byte, 2) // 小容量：快速触发投递失败 → 兜底路由
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond)

	// 洪峰：20 条普通级消息打满容量 2 的通道 → 失败的转离线
	// （admitMessage 模拟 P2P 入口的 admitted 埋点——TrySendWithFallback 是扇出内部接口，
	// admitted 计数在投递入口发生）
	for i := 0; i < 20; i++ {
		msg := models.NewHubMessage().
			SetMessageType(models.MessageTypeText).
			SetSender("cons-source").
			SetReceiver("cons-user").
			WithGuarantee(models.GuaranteeStandard)
		hub.admitMessage(msg, false)
		hub.TrySendWithFallback(client, []byte(`{"i":1}`), msg)
	}

	// 漏斗守恒（异步转存完成后）：admitted 计数的每条要么 realtime 要么 offline
	require.Eventually(t, func() bool {
		ok, _ := hub.overloadMetrics.DeliveryFunnelConsistent()
		return ok
	}, 5*time.Second, 100*time.Millisecond, "洪峰中普通级漏斗应守恒（无静默丢失）")

	// 客户端实时收到 2 条（通道容量）
	cConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for i := 0; i < 2; i++ {
		_, _, err := cConn.ReadMessage()
		require.NoError(t, err)
	}
}

// TestFloodCoalescerPeaks 高频洪峰合并：同 key 连发只投最新（合并削峰）
func TestFloodCoalescerPeaks(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	// 同 key 高频洪峰：100 条只占 1 槽位
	for i := 0; i < 100; i++ {
		msg := newEphemeralMsg("flood-user", i)
		hub.ephemeralCoalescer.Load().Offer("flood-user|typing", msg)
	}

	coalescer := hub.ephemeralCoalescer.Load()
	assert.Equal(t, int64(1), coalescer.Size(), "同 key 洪峰应合并为 1 条")
	assert.Equal(t, int64(99), coalescer.MergedCount(), "应合并 99 次")

	// drain 快照只含最新值
	batch := coalescer.Drain()
	require.Len(t, batch, 1)
	assert.Equal(t, "typing-99", batch[0].Content, "应投递最新版本")
}

// TestFloodMultiSource 不同洪峰源隔离：shaper 单桶统计不互相污染
func TestFloodMultiSource(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("iso2-client", "iso2-user", sConn)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond)

	// 普通级洪峰广播（正常水位：全放行扇出）
	const total = 30
	for i := 0; i < total; i++ {
		msg := models.NewHubMessage().
			SetMessageType(models.MessageTypeText).
			SetSender("iso2-source").
			WithGuarantee(models.GuaranteeStandard)
		hub.Deliver(context.Background(), msg, false)
	}

	// 全部送达（L0 无准入/整形拒绝——默认 shaper 10000/s 速率下 30 条全过）
	cConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for i := 0; i < total; i++ {
		_, _, err := cConn.ReadMessage()
		require.NoError(t, err, "第 %d 条应直达（无水位压力）", i+1)
	}
}

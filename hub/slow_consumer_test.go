/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 13:22:07
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 13:30:00
 * @FilePath: \go-wsc\hub\slow_consumer_test.go
 * @Description: 慢消费者治理测试 —— 三级递进状态机（记录 → 告警 → 驱逐）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSlowConsumerClient 构造慢消费者测试客户端（小容量 SendChan 便于操纵积压）
func newSlowConsumerClient(id, userID string) *Client {
	client := newTestClient(id, userID, nil)
	client.SendChan = make(chan []byte, 4)
	client.CtrlCh = make(chan []byte, 16)
	return client
}

// TestScanSlowConsumersLevel1 一级（记录）：首轮超阈值仅计数，不驱逐不告警
func TestScanSlowConsumersLevel1(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newSlowConsumerClient("slow-l1", "u-l1")
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 模拟写泵上报高积压（≥0.9）
	client.SetBacklogRatio(4, 4)

	states := make(map[string]*slowConsumerState)
	hub.scanSlowConsumersOnce(states)

	assert.Len(t, states, 1, "首轮应记录状态")
	assert.Equal(t, 1, states[client.ID].consecutive, "连续计数应为 1")
	assert.True(t, hub.HasClient(client.ID), "一级不应驱逐")
	assert.Equal(t, int64(0), hub.overloadMetrics.evictedSlow.Load(), "驱逐计数应为 0")
}

// TestScanSlowConsumersWarn 二级（告警）：达到 consecutiveLimit-1 时 warned 置位
func TestScanSlowConsumersWarn(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newSlowConsumerClient("slow-l2", "u-l2")
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	client.SetBacklogRatio(4, 4)

	states := make(map[string]*slowConsumerState)
	limit := hubSlowConsumerConsecutiveLimit()
	for i := 0; i < limit-1; i++ {
		hub.scanSlowConsumersOnce(states)
	}

	state := states[client.ID]
	require.NotNil(t, state)
	assert.Equal(t, limit-1, state.consecutive, "连续计数应为告警阈值")
	assert.True(t, state.warned, "告警轮应置位 warned")
	assert.True(t, hub.HasClient(client.ID), "二级不应驱逐")
}

// TestScanSlowConsumersEvict 三级（驱逐）：达 consecutiveLimit 驱逐 + KickOut 走控制通道 + 注销
func TestScanSlowConsumersEvict(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newSlowConsumerClient("slow-l3", "u-l3")
	client.Conn = sConn
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 塞入未投递消息（驱逐前保全路径排空）
	client.SendChan <- []byte(`{"salvage":1}`)

	states := make(map[string]*slowConsumerState)
	limit := hubSlowConsumerConsecutiveLimit()
	for i := 0; i < limit; i++ {
		// 每轮扫描前维持高积压：写泵 drain 首条后会重置真实利用率，
		// 此处持续注入伪造采样驱动状态机走满 limit 轮（生产语义由真实积压驱动）
		client.SetBacklogRatio(4, 4)
		hub.scanSlowConsumersOnce(states)
	}

	// 驱逐后：状态清除 + 注册表移除 + 指标计数
	assert.NotContains(t, states, client.ID, "驱逐后状态应清除")
	assert.Equal(t, int64(1), hub.overloadMetrics.evictedSlow.Load(), "驱逐计数应 +1")

	// KickOut 通过控制通道送达：写泵先写出 SendChan 数据帧，KickOut 紧随其后
	// （持续读直到收到驱逐理由或连接关闭）
	cConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	kickReceived := false
	for !kickReceived {
		_, data, err := cConn.ReadMessage()
		if err != nil {
			break // 连接关闭（KickOut 后 Unregister 断链）
		}
		if len(data) > 0 && string(data) != `{"salvage":1}` {
			// 非数据帧即控制消息（KickOut 携带驱逐理由）
			kickReceived = true
		}
	}
	// KickOut 帧或断链二选一（SendControl 满时降级直接断链——语义达成即送达）
	// 连接最终被关闭（Unregister 断链）
	require.Eventually(t, func() bool { return !hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond,
		"驱逐后客户端应被注销")
}

// TestScanSlowConsumersRecovery 恢复清零：积压回落后连续计数重置（迟滞清除）
func TestScanSlowConsumersRecovery(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newSlowConsumerClient("slow-recover", "u-recover")
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	states := make(map[string]*slowConsumerState)

	// 两轮超阈值积累计数
	client.SetBacklogRatio(4, 4)
	hub.scanSlowConsumersOnce(states)
	hub.scanSlowConsumersOnce(states)
	assert.Equal(t, 2, states[client.ID].consecutive)

	// 积压回落
	client.SetBacklogRatio(0, 4)
	hub.scanSlowConsumersOnce(states)

	assert.NotContains(t, states, client.ID, "恢复后状态应清除（防历史计数误伤）")
	assert.True(t, hub.HasClient(client.ID), "恢复的客户端不应被驱逐")
}

// TestScanSlowConsumersLazyCleanup 惰性清理：断连客户端的旧状态被清除
func TestScanSlowConsumersLazyCleanup(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	client := newSlowConsumerClient("slow-gone", "u-gone")
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	states := make(map[string]*slowConsumerState)
	client.SetBacklogRatio(4, 4)
	hub.scanSlowConsumersOnce(states)
	require.Contains(t, states, client.ID)

	// 客户端断连（直接注销）
	hub.Unregister(client)
	require.Eventually(t, func() bool { return !hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 下一轮扫描：惰性清理旧状态（防状态表膨胀）
	hub.scanSlowConsumersOnce(states)
	assert.NotContains(t, states, client.ID, "断连客户端的旧状态应被惰性清理")
}

// TestEvictSlowConsumerSalvage 驱逐前保全：SendChan 残留消息被排空（ACK 链路兜底）
func TestEvictSlowConsumerSalvage(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, _ := newWSConnPair(t)
	client := newSlowConsumerClient("slow-salvage", "u-salvage")
	client.Conn = sConn
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 塞 3 条未投递消息（模拟洪峰积压）
	for i := 0; i < 3; i++ {
		client.SendChan <- []byte(`{"pending":true}`)
	}

	state := &slowConsumerState{consecutive: 3}
	hub.evictSlowConsumer(client, state, 1.0)

	// 残留消息被排空（保全后不再滞留）
	assert.Equal(t, 0, len(client.SendChan), "驱逐后 SendChan 应被排空")
	assert.Equal(t, int64(1), hub.overloadMetrics.evictedSlow.Load())
	require.Eventually(t, func() bool { return !hub.HasClient(client.ID) }, 5*time.Second, 20*time.Millisecond,
		"驱逐后应注销")
}

// hubSlowConsumerConsecutiveLimit 读取驱逐连续阈值（constants 单一事实源）
func hubSlowConsumerConsecutiveLimit() int {
	return constants.SlowConsumerConsecutiveThreshold
}

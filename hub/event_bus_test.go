/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 23:12:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 23:12:00
 * @FilePath: \go-wsc\hub\event_bus_test.go
 * @Description: 消息总线 Hub 接线测试（装配与消费冒烟）
 *
 * 覆盖：WithEventBus 装配语义（nil 安全）、经 memory 总线的真决策树消费冒烟
 * （P2P 离线分支 + 全局广播 + 非法信封丢弃，均无仓储注入 nil-safe 降级）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	memorybus "github.com/kamalyes/go-wsc/adapter/memory"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/publisher"
	"github.com/kamalyes/go-wsc/spi"
)

func TestWithEventBus_Assembly(t *testing.T) {
	h := NewHub(wscconfig.Default())
	assert.Nil(t, h.brokerMgr, "未注入时 broker 域零初始化（直调模式）")

	h.WithEventBus(nil)
	assert.Nil(t, h.brokerMgr, "nil 总线不装配")

	bus := memorybus.NewEventBus()
	h.WithEventBus(bus)
	require.NotNil(t, h.brokerMgr, "注入即装配消费域")
	require.NoError(t, h.brokerMgr.Close())
}

// TestWithEventBus_ConsumeSmoke 经 memory 总线的真决策树消费冒烟：
// 消费链 consumer → 路由恢复 → hub.Deliver（无仓储注入 nil-safe 降级），
// 三类信封（P2P 离线 / 全局广播 / 非法信封）全程无 panic，Close 排空即消费完成
func TestWithEventBus_ConsumeSmoke(t *testing.T) {
	bus := memorybus.NewEventBus()
	h := NewHub(wscconfig.Default()).WithEventBus(bus)
	require.NotNil(t, h.brokerMgr)

	ctx := context.Background()
	require.NoError(t, h.brokerMgr.Start(ctx))

	pub, err := publisher.New(bus).WithPublisher("hub-smoke-test").Build()
	require.NoError(t, err)

	// P2P 离线用户（决策树走离线分支，无 offline handler 降级）
	p2p := &models.HubMessage{
		Receiver:  "u-offline",
		AppID:     "app1",
		Namespace: "ns1",
		Content:   "p2p offline smoke",
	}
	require.NoError(t, pub.Publish(ctx, publisher.NewEnvelope(p2p).Deliver()))

	// 全局广播（无本地连接，LocalDelivered=0 语义）
	broadcast := &models.HubMessage{AppID: "app1", Content: "broadcast smoke"}
	require.NoError(t, pub.Publish(ctx, publisher.NewEnvelope(broadcast).Broadcast()))

	// 非法信封（缺 Publisher，consumer 校验丢弃路径）
	invalid := &spi.DeliverEnvelope{
		Intent:  spi.IntentDeliver,
		Message: &models.HubMessage{MessageID: "bad-1", Receiver: "u1", AppID: "app1"},
	}
	require.NoError(t, bus.Publish(ctx, invalid))

	// Close 排空在途消息后返回（分发完成即全部消费完成）
	require.NoError(t, h.brokerMgr.Close())
	require.NoError(t, h.SafeShutdown())
}

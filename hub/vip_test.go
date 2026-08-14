/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:02:15
 * @FilePath: \go-wsc\hub\vip_test.go
 * @Description: Hub VIP 功能白盒单元测试（覆盖 hub/vip.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// makeVIPClient 构造指定 VIP 等级的客户端
func makeVIPClient(clientID, userID string, level VIPLevel) *Client {
	c := makeTestClient(clientID, userID)
	c.SetVIPLevel(level)
	return c
}

func TestSendToVIPUsers(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	vip := makeVIPClient("c-vip", "u-vip", models.VIPLevelV5)
	normal := makeVIPClient("c-normal", "u-normal", models.VIPLevelV0)
	hub.shardedRegistry.AddClient(vip)
	hub.shardedRegistry.AddClient(normal)

	delivered := hub.SendToVIPUsers(context.Background(), models.VIPLevelV3, makeGroupMessage("sender"))
	assert.Equal(t, 1, delivered, "仅 VIP>=V3 的客户端应收到")

	// vip 收到
	select {
	case <-vip.SendChan:
	default:
		t.Fatal("VIP 客户端应收到消息")
	}
	// normal 不收到
	select {
	case <-normal.SendChan:
		t.Fatal("普通客户端不应收到")
	default:
	}
}

func TestSendToExactVIPLevel(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	v5 := makeVIPClient("c-v5", "u-v5", models.VIPLevelV5)
	v6 := makeVIPClient("c-v6", "u-v6", models.VIPLevelV6)
	hub.shardedRegistry.AddClient(v5)
	hub.shardedRegistry.AddClient(v6)

	delivered := hub.SendToExactVIPLevel(context.Background(), models.VIPLevelV5, makeGroupMessage("sender"))
	assert.Equal(t, 1, delivered, "仅 V5 客户端应收到")

	select {
	case <-v5.SendChan:
	default:
		t.Fatal("V5 客户端应收到")
	}
	select {
	case <-v6.SendChan:
		t.Fatal("V6 客户端不应收到")
	default:
	}
}

func TestSendWithVIPPriority(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	t.Run("V6设置高优先级", func(t *testing.T) {
		client := makeVIPClient("c-v6p", "u-v6p", models.VIPLevelV6)
		hub.shardedRegistry.AddClient(client)
		msg := makeGroupMessage("sender")
		hub.SendWithVIPPriority(context.Background(), "u-v6p", msg)
		assert.Equal(t, PriorityHigh, msg.Priority)
	})
	t.Run("V3设置普通优先级", func(t *testing.T) {
		client := makeVIPClient("c-v3p", "u-v3p", models.VIPLevelV3)
		hub.shardedRegistry.AddClient(client)
		msg := makeGroupMessage("sender")
		hub.SendWithVIPPriority(context.Background(), "u-v3p", msg)
		assert.Equal(t, PriorityNormal, msg.Priority)
	})
	t.Run("V1设置低优先级", func(t *testing.T) {
		client := makeVIPClient("c-v1p", "u-v1p", models.VIPLevelV1)
		hub.shardedRegistry.AddClient(client)
		msg := makeGroupMessage("sender")
		hub.SendWithVIPPriority(context.Background(), "u-v1p", msg)
		assert.Equal(t, PriorityLow, msg.Priority)
	})
	t.Run("离线用户不设置优先级", func(t *testing.T) {
		msg := makeGroupMessage("sender")
		originalPriority := msg.Priority
		hub.SendWithVIPPriority(context.Background(), "u-offline-vip", msg)
		assert.Equal(t, originalPriority, msg.Priority, "离线用户未找到客户端，优先级不应被修改")
	})
}

func TestSendToVIPWithPriority(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	v6 := makeVIPClient("c-v6w", "u-v6w", models.VIPLevelV6)
	v3 := makeVIPClient("c-v3w", "u-v3w", models.VIPLevelV3)
	hub.shardedRegistry.AddClient(v6)
	hub.shardedRegistry.AddClient(v3)

	msg := makeGroupMessage("sender")
	// V5 及以上 → 高优先级，投递给 V6（>=V5）
	delivered := hub.SendToVIPWithPriority(context.Background(), models.VIPLevelV5, msg)
	assert.Equal(t, 1, delivered)
	assert.Equal(t, PriorityHigh, msg.Priority)

	select {
	case <-v6.SendChan:
	default:
		t.Fatal("V6 客户端应收到")
	}
}

func TestSendToUserWithClassification(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	client := makeVIPClient("c-cls", "u-cls", models.VIPLevelV8)
	hub.shardedRegistry.AddClient(client)

	t.Run("nil分类直接发送", func(t *testing.T) {
		msg := makeGroupMessage("sender")
		assert.NotPanics(t, func() {
			hub.SendToUserWithClassification(context.Background(), "u-cls", msg, nil)
		})
	})
	t.Run("非nil分类设置优先级与数据", func(t *testing.T) {
		msg := makeGroupMessage("sender")
		cls := &MessageClassification{
			Type:             MessageTypeText,
			VIPLevel:         models.VIPLevelV8,
			UrgencyLevel:     models.UrgencyLevelHigh,
			BusinessCategory: models.BusinessCategorySecurity,
		}
		hub.SendToUserWithClassification(context.Background(), "u-cls", msg, cls)
		assert.NotEqual(t, Priority(""), msg.Priority, "应根据分类设置优先级")
		require.NotNil(t, msg.Data)
		assert.NotNil(t, msg.Data["classification"])
		assert.NotNil(t, msg.Data["priority_score"])
	})
}

func TestGetVIPStatistics(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeVIPClient("c-vs1", "u-vs1", models.VIPLevelV5))
	hub.shardedRegistry.AddClient(makeVIPClient("c-vs2", "u-vs2", models.VIPLevelV5))
	hub.shardedRegistry.AddClient(makeVIPClient("c-vs3", "u-vs3", models.VIPLevelV8))
	hub.shardedRegistry.AddClient(makeVIPClient("c-vs4", "u-vs4", models.VIPLevelV0))

	stats := hub.GetVIPStatistics()
	require.NotNil(t, stats)
	assert.Equal(t, 2, stats[string(models.VIPLevelV5)])
	assert.Equal(t, 1, stats[string(models.VIPLevelV8)])
	// total_vip 不含 v0
	assert.Equal(t, 3, stats["total_vip"])
}

func TestFilterVIPClients(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeVIPClient("c-fv1", "u-fv1", models.VIPLevelV4))
	hub.shardedRegistry.AddClient(makeVIPClient("c-fv2", "u-fv2", models.VIPLevelV6))
	hub.shardedRegistry.AddClient(makeVIPClient("c-fv3", "u-fv3", models.VIPLevelV2))

	clients := hub.FilterVIPClients(models.VIPLevelV4)
	require.Len(t, clients, 2)
}

func TestUpgradeVIPLevel(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("有效升级", func(t *testing.T) {
		c := makeVIPClient("c-up1", "u-up1", models.VIPLevelV2)
		hub.shardedRegistry.AddClient(c)
		assert.True(t, hub.UpgradeVIPLevel("u-up1", models.VIPLevelV5))
		assert.Equal(t, models.VIPLevelV5, c.GetVIPLevel())
	})
	t.Run("无效等级返回false", func(t *testing.T) {
		c := makeVIPClient("c-up2", "u-up2", models.VIPLevelV2)
		hub.shardedRegistry.AddClient(c)
		assert.False(t, hub.UpgradeVIPLevel("u-up2", models.VIPLevel("v99")))
	})
	t.Run("不存在用户返回false", func(t *testing.T) {
		assert.False(t, hub.UpgradeVIPLevel("u-none", models.VIPLevelV5))
	})
	t.Run("降级不生效返回false", func(t *testing.T) {
		c := makeVIPClient("c-up3", "u-up3", models.VIPLevelV5)
		hub.shardedRegistry.AddClient(c)
		assert.False(t, hub.UpgradeVIPLevel("u-up3", models.VIPLevelV2))
		assert.Equal(t, models.VIPLevelV5, c.GetVIPLevel(), "降级不应改变等级")
	})
	t.Run("同级不生效返回false", func(t *testing.T) {
		c := makeVIPClient("c-up4", "u-up4", models.VIPLevelV5)
		hub.shardedRegistry.AddClient(c)
		assert.False(t, hub.UpgradeVIPLevel("u-up4", models.VIPLevelV5))
	})
}

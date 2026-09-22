/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 00:00:00
 * @FilePath: \go-wsc\connection\capacity_test.go
 * @Description: 客户端容量管理测试 - UserType 分派 / 初始化与回收 / 池复用 / 零容量兜底
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

// newCapacityConfig 生产规模样例配置（各类型独立容量，覆盖全部 UserType 分支）
func newCapacityConfig() *wscconfig.ClientCapacity {
	return &wscconfig.ClientCapacity{
		Agent:    512,
		Bot:      128,
		Customer: 256,
		Observer: 64,
		Admin:    1024,
		VIP:      2048,
		Visitor:  96,
		System:   32,
		Default:  256,
	}
}

// TestCapacityForUserTypeDispatch 按 UserType 精确分派配置容量
func TestCapacityForUserTypeDispatch(t *testing.T) {
	pool := NewChanPool(newCapacityConfig(), nil)

	cases := []struct {
		ut   models.UserType
		want int
	}{
		{models.UserTypeAgent, 512},
		{models.UserTypeBot, 128},
		{models.UserTypeCustomer, 256},
		{models.UserTypeObserver, 64},
		{models.UserTypeAdmin, 1024},
		{models.UserTypeVIP, 2048},
		{models.UserTypeVisitor, 96},
		{models.UserTypeSystem, 32},
		{models.UserType("unknown"), 256}, // 未知类型走 Default
	}
	for _, tc := range cases {
		client := models.NewClient("cap-"+string(tc.ut), "u-1", tc.ut)
		assert.Equal(t, tc.want, pool.CapacityFor(client), "UserType=%s 应分派容量 %d", tc.ut, tc.want)
	}
}

// TestCapacityForNilFallback nil client / nil 配置均回落默认值
func TestCapacityForNilFallback(t *testing.T) {
	pool := NewChanPool(newCapacityConfig(), nil)
	assert.Equal(t, DefaultClientSendChanCapacity, pool.CapacityFor(nil))

	poolDefault := NewChanPool(nil, nil) // 配置缺失 → wscconfig.DefaultClientCapacity()
	client := models.NewClient("c1", "u-1", models.UserTypeCustomer)
	assert.Equal(t, wscconfig.DefaultClientCapacity().Customer, poolDefault.CapacityFor(client))
}

// TestInitClientSendChanWebSocket WS 客户端：SendChan/CtrlCh/PongCh/DoneCh 四件套初始化
func TestInitClientSendChanWebSocket(t *testing.T) {
	pool := NewChanPool(newCapacityConfig(), nil)
	client := models.NewClient("ws-1", "u-100", models.UserTypeCustomer)
	client.Context = context.Background()

	pool.InitClientSendChan(client)

	require.NotNil(t, client.SendChan)
	assert.Equal(t, 256, cap(client.SendChan), "customer 容量应取配置 256")
	require.NotNil(t, client.CtrlCh)
	assert.Equal(t, constants.CtrlChanCapacity, cap(client.CtrlCh))
	require.NotNil(t, client.PongCh)
	assert.Equal(t, 1, cap(client.PongCh), "pong 队列容量固定 1")
	require.NotNil(t, client.DoneCh)

	// 幂等：重复初始化不重建（channel 可用 == 判同一）
	before := client.SendChan
	pool.InitClientSendChan(client)
	assert.Equal(t, before, client.SendChan, "已初始化的 SendChan 不应被重建")
}

// TestInitClientSendChanSSE SSE 客户端：无 PongCh/CtrlCh（SSE 无控制帧），仅 DoneCh+SendChan
func TestInitClientSendChanSSE(t *testing.T) {
	pool := NewChanPool(newCapacityConfig(), nil)
	client := models.NewClient("sse-1", "u-200", models.UserTypeVisitor)
	client.ConnectionType = models.ConnectionTypeSSE
	client.Context = context.Background()

	pool.InitClientSendChan(client)

	require.NotNil(t, client.SendChan)
	assert.Equal(t, 96, cap(client.SendChan), "visitor 容量应取配置 96")
	assert.Nil(t, client.PongCh, "SSE 无控制帧，PongCh 应跳过")
	assert.Nil(t, client.CtrlCh, "SSE 无控制帧，CtrlCh 应跳过")
	require.NotNil(t, client.DoneCh)
}

// TestReleaseClientSendChan 释放回收：通道置 nil、残留数据清空、chan 可再复用
func TestReleaseClientSendChan(t *testing.T) {
	pool := NewChanPool(newCapacityConfig(), nil)
	client := models.NewClient("rel-1", "u-300", models.UserTypeAgent)
	client.Context = context.Background()
	pool.InitClientSendChan(client)

	// 模拟残留积压（正常断链前由上层排空，此处验证释放清理能力）
	client.SendChan <- []byte(`{"stale":true}`)

	sendBefore := client.SendChan
	pool.ReleaseClientSendChan(client)

	assert.Nil(t, client.SendChan, "释放后 SendChan 应置 nil")
	assert.Nil(t, client.CtrlCh, "释放后 CtrlCh 应置 nil")

	// 归还的 chan 已被清空，可被同容量池再次取出复用
	reused := pool.getChan(512)
	assert.Equal(t, 512, cap(reused))
	assert.Equal(t, 0, len(reused), "复用的 chan 应无残留数据")
	_ = sendBefore
}

// TestZeroCapacityFallback 配置容量 0 时兜底 DefaultClientSendChanCapacity
func TestZeroCapacityFallback(t *testing.T) {
	pool := NewChanPool(&wscconfig.ClientCapacity{}, nil) // 全零配置
	client := models.NewClient("zero-1", "u-400", models.UserTypeCustomer)
	client.Context = context.Background()

	pool.InitClientSendChan(client)

	require.NotNil(t, client.SendChan)
	assert.Equal(t, DefaultClientSendChanCapacity, cap(client.SendChan), "零容量配置应兜底 256")
}

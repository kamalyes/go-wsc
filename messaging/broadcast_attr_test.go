/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 02:23:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\messaging\broadcast_attr_test.go
 * @Description: 属性维度广播（attrIdx 定点路径）的正确性回归
 *
 * attrIdx 把 BroadcastByUserType / BroadcastToRole / BroadcastToClientType /
 * BroadcastToDepartment 从「全连接扫描」改为「索引定点 + 自适应回退全量」。
 * 索引是 userID 粒度（设备去重），而这四个维度是**连接**属性——同一用户多端
 * 连接的取值可以不同（ClientType 必然不同）。因此定点路径必须仍逐连接施加谓词，
 * 不可退化为按 userID 整户投递，否则会误投到该用户不匹配的其余设备。
 *
 * 本文件的用例在「索引命中」与「回退全量」两条路径下都断言同一结果，
 * 任一路径退化都会失败。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package messaging

import (
	"context"
	"testing"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
)

func attrTestCtx() context.Context {
	return routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())
}

// received 非阻塞判定客户端是否收到消息
func received(c *models.Client) bool {
	select {
	case <-c.SendChan:
		return true
	default:
		return false
	}
}

// TestBroadcastToClientType_MultiDeviceSameUser 同一用户多端、仅匹配端收到
//
// 这是 attrIdx 最容易退化的用例：索引按 userID 去重后只会返回一个 userID，
// 若定点路径按整户扇出，该用户的 mobile 连接会错误收到 web 定向的消息。
func TestBroadcastToClientType_MultiDeviceSameUser(t *testing.T) {
	m, host := newTestManager()

	web := makeTestClient("c-web", "u-multi")
	web.ClientType = models.ClientTypeWeb
	mobile := makeTestClient("c-mobile", "u-multi")
	mobile.ClientType = models.ClientTypeMobile

	host.GetShardedRegistry().AddClient(web)
	host.GetShardedRegistry().AddClient(mobile)

	// 垫连接：拉低命中占比，确保走索引定点路径而非自适应回退
	for i := 0; i < 20; i++ {
		pad := makeTestClient("c-pad-"+string(rune('a'+i)), "u-pad-"+string(rune('a'+i)))
		pad.ClientType = models.ClientTypeDesktop
		host.GetShardedRegistry().AddClient(pad)
	}

	delivered := m.BroadcastToClientType(attrTestCtx(), models.ClientTypeWeb, makeGroupMessage("sender"))

	assert.Equal(t, 1, delivered, "仅 web 连接应计入投递")
	assert.True(t, received(web), "web 连接应收到消息")
	assert.False(t, received(mobile), "同用户的 mobile 连接不得收到 web 定向消息（整户扇出退化）")
}

// TestBroadcastByUserType_IndexedPath 索引定点路径命中正确
func TestBroadcastByUserType_IndexedPath(t *testing.T) {
	m, host := newTestManager()

	agent := makeTestClient("c-agent", "u-agent")
	agent.UserType = models.UserTypeAgent
	host.GetShardedRegistry().AddClient(agent)

	// 大量 customer 垫连接，使 agent 命中占比远低于回退阈值
	customers := make([]*models.Client, 0, 30)
	for i := 0; i < 30; i++ {
		c := makeTestClient("c-cust-"+string(rune('a'+i)), "u-cust-"+string(rune('a'+i)))
		c.UserType = models.UserTypeCustomer
		host.GetShardedRegistry().AddClient(c)
		customers = append(customers, c)
	}

	delivered := m.BroadcastByUserType(attrTestCtx(), models.UserTypeAgent, makeGroupMessage("sender"))

	assert.Equal(t, 1, delivered)
	assert.True(t, received(agent), "agent 应收到消息")
	for _, c := range customers {
		assert.False(t, received(c), "customer 不应收到 agent 定向消息")
	}
}

// TestBroadcastByUserType_FullScanFallback 高命中占比时回退全量，结果一致
//
// 仅 2 条连接且全部命中 → 占比 100% ≥ 阈值 → 走全量扫描分支。
// 断言与索引路径同结果，证明回退不改变语义。
func TestBroadcastByUserType_FullScanFallback(t *testing.T) {
	m, host := newTestManager()

	a := makeTestClient("c-a", "u-a")
	a.UserType = models.UserTypeCustomer
	b := makeTestClient("c-b", "u-b")
	b.UserType = models.UserTypeCustomer
	host.GetShardedRegistry().AddClient(a)
	host.GetShardedRegistry().AddClient(b)

	delivered := m.BroadcastByUserType(attrTestCtx(), models.UserTypeCustomer, makeGroupMessage("sender"))

	assert.Equal(t, 2, delivered, "全量回退路径应投递全部命中连接")
	assert.True(t, received(a))
	assert.True(t, received(b))
}

// TestBroadcastByAttr_NoHitShortCircuits 索引零命中直接短路
func TestBroadcastByAttr_NoHitShortCircuits(t *testing.T) {
	m, host := newTestManager()

	c := makeTestClient("c-only", "u-only")
	c.UserType = models.UserTypeCustomer
	host.GetShardedRegistry().AddClient(c)

	delivered := m.BroadcastByUserType(attrTestCtx(), models.UserTypeAgent, makeGroupMessage("sender"))

	assert.Equal(t, 0, delivered, "无该类型用户时应 0 投递")
	assert.False(t, received(c), "不匹配连接不得收到消息")
}

// TestBroadcastToDepartment_EmptyValueFallsBackToScan 空取值不入索引，回退全量
//
// 空值维度不写索引（空值桶会聚集全部未设置该维度的连接，索引退化为全量扫描的副本）。
// 查询空值时必须回退全量扫描而非误判为「零命中」直接返回 0。
func TestBroadcastToDepartment_EmptyValueFallsBackToScan(t *testing.T) {
	m, host := newTestManager()

	noDept := makeTestClient("c-nodept", "u-nodept") // Department 零值
	withDept := makeTestClient("c-dept", "u-dept")
	withDept.Department = models.Department("eng")
	host.GetShardedRegistry().AddClient(noDept)
	host.GetShardedRegistry().AddClient(withDept)

	delivered := m.BroadcastToDepartment(attrTestCtx(), models.Department(""), makeGroupMessage("sender"))

	assert.Equal(t, 1, delivered, "空部门查询须回退全量扫描并命中无部门连接，而非短路返回 0")
	assert.True(t, received(noDept), "无部门连接应收到空部门定向消息")
	assert.False(t, received(withDept), "有部门连接不应收到")
}

// TestAttrIdx_RemoveClientSymmetry 注销后索引不再命中（增删对称）
//
// addToAttrIdx 跳过空值维度，removeFromAttrIdx 必须同样跳过，否则引用计数被压成负值。
func TestAttrIdx_RemoveClientSymmetry(t *testing.T) {
	m, host := newTestManager()
	registry := host.GetShardedRegistry()

	agent := makeTestClient("c-ag", "u-ag")
	agent.UserType = models.UserTypeAgent
	registry.AddClient(agent)

	users, indexed := registry.GetLocalUsersByUserType(constants.DefaultAppID, models.UserTypeAgent)
	assert.True(t, indexed)
	assert.Equal(t, []string{"u-ag"}, users, "注册后索引应命中")

	registry.RemoveClient("c-ag", "u-ag")

	users, indexed = registry.GetLocalUsersByUserType(constants.DefaultAppID, models.UserTypeAgent)
	assert.True(t, indexed)
	assert.Empty(t, users, "注销后索引不应再命中")

	delivered := m.BroadcastByUserType(attrTestCtx(), models.UserTypeAgent, makeGroupMessage("sender"))
	assert.Equal(t, 0, delivered)
}

// TestAttrIdx_MultiDeviceRefCount 多端登录引用计数：移除一端后仍命中
func TestAttrIdx_MultiDeviceRefCount(t *testing.T) {
	_, host := newTestManager()
	registry := host.GetShardedRegistry()

	d1 := makeTestClient("c-d1", "u-dual")
	d1.Role = models.UserRoleAdmin
	d2 := makeTestClient("c-d2", "u-dual")
	d2.Role = models.UserRoleAdmin
	registry.AddClient(d1)
	registry.AddClient(d2)

	registry.RemoveClient("c-d1", "u-dual")
	users, _ := registry.GetLocalUsersByRole(constants.DefaultAppID, models.UserRoleAdmin)
	assert.Equal(t, []string{"u-dual"}, users, "仍有一端在线时索引应保留该 userID")

	registry.RemoveClient("c-d2", "u-dual")
	users, _ = registry.GetLocalUsersByRole(constants.DefaultAppID, models.UserRoleAdmin)
	assert.Empty(t, users, "两端均下线后索引应清空")
}

// TestAttrIdx_AppIDIsolation 不同 appID 的同名取值互不串键
func TestAttrIdx_AppIDIsolation(t *testing.T) {
	_, host := newTestManager()
	registry := host.GetShardedRegistry()

	a1 := makeTestClient("c-a1", "u-a1")
	a1.AppID = "app-1"
	a1.Role = models.UserRoleAdmin
	a2 := makeTestClient("c-a2", "u-a2")
	a2.AppID = "app-2"
	a2.Role = models.UserRoleAdmin
	registry.AddClient(a1)
	registry.AddClient(a2)

	u1, _ := registry.GetLocalUsersByRole("app-1", models.UserRoleAdmin)
	u2, _ := registry.GetLocalUsersByRole("app-2", models.UserRoleAdmin)
	assert.Equal(t, []string{"u-a1"}, u1)
	assert.Equal(t, []string{"u-a2"}, u2)
}

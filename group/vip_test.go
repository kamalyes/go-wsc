/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:58:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 14:02:00
 * @FilePath: \go-wsc\group\vip_test.go
 * @Description: VIP 分级发送与统计单元测试
 *
 * 复用 workload_test.go 的 fakeHost，额外实现 VIP 路径用到的
 * 注册表与发送端口。条件函数在此直接求值，用于断言「选择逻辑」，
 * 投递本身由 Host 负责，不在本域测试范围内。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"
	"testing"

	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 发送端口假实现
// ============================================================================

// fakeSender 记录条件发送与点对点发送的调用
type fakeSender struct {
	conditionalCalls int
	conditionalHits  int
	conditionalMsg   *models.HubMessage

	p2pUser string
	p2pMsg  *models.HubMessage
}

// sendConditional 用注册表里的客户端对条件求值，模拟真实扇出语义
func (s *fakeSender) sendConditional(reg *connection.ShardedRegistry, condition func(*models.Client) bool, msg *models.HubMessage) int {
	s.conditionalMsg = msg
	s.conditionalCalls++
	delivered := 0
	reg.ForEachClient(func(_ string, c *models.Client) bool {
		if condition(c) {
			delivered++
		}
		return true
	})
	s.conditionalHits = delivered
	return delivered
}

// vipFixture 组装带注册表与发送端口的假 Host
type vipFixture struct {
	*fakeHost
	reg    *connection.ShardedRegistry
	sender *fakeSender
}

func newVIPFixture() *vipFixture {
	return &vipFixture{
		fakeHost: newFakeHost(),
		reg:      connection.NewShardedRegistry(false, false, connection.RegistryCapacity{}),
		sender:   &fakeSender{},
	}
}

func (f *vipFixture) GetShardedRegistry() *connection.ShardedRegistry { return f.reg }

func (f *vipFixture) SendConditional(_ context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int {
	return f.sender.sendConditional(f.reg, condition, msg)
}

func (f *vipFixture) SendToUserWithRetry(_ context.Context, userID string, msg *models.HubMessage) *models.SendResult {
	f.sender.p2pUser = userID
	f.sender.p2pMsg = msg
	return &models.SendResult{Success: true}
}

// addClient 注册一个客户端并设定 VIP 等级
func (f *vipFixture) addClient(t *testing.T, clientID, userID string, level models.VIPLevel) {
	t.Helper()
	c := models.NewClient(clientID, userID, models.UserTypeCustomer)
	c.AppID = constants.DefaultAppID
	c.Namespace = constants.DefaultNamespace
	c.SetVIPLevel(level)
	f.reg.AddClient(c)
}

// vipCtx 构造与 addClient 同信封的路由上下文
func vipCtx() context.Context {
	return routing.NewRoute().
		WithAppID(constants.DefaultAppID).
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())
}

// ============================================================================
// 分级发送：条件必须按等级正确求值
// ============================================================================

func TestSendToVIPUsersFiltersByMinLevel(t *testing.T) {
	f := newVIPFixture()
	f.addClient(t, "c1", "u-v0", models.VIPLevelV0)
	f.addClient(t, "c2", "u-v3", models.VIPLevelV3)
	f.addClient(t, "c3", "u-v6", models.VIPLevelV6)
	mgr := NewVIPManager(f)

	got := mgr.SendToVIPUsers(vipCtx(), models.VIPLevelV3, &models.HubMessage{})
	if got != 2 {
		t.Fatalf("命中数 = %d, want 2（V3/V6 达标，V0 不达标）", got)
	}
}

func TestSendToExactVIPLevelOnlyMatching(t *testing.T) {
	f := newVIPFixture()
	f.addClient(t, "c1", "u-a", models.VIPLevelV3)
	f.addClient(t, "c2", "u-b", models.VIPLevelV3)
	f.addClient(t, "c3", "u-c", models.VIPLevelV6)
	mgr := NewVIPManager(f)

	if got := mgr.SendToExactVIPLevel(vipCtx(), models.VIPLevelV3, &models.HubMessage{}); got != 2 {
		t.Fatalf("命中数 = %d, want 2（仅 V3，不含 V6）", got)
	}
}

func TestSendToVIPWithPrioritySetsPriority(t *testing.T) {
	cases := []struct {
		name   string
		level  models.VIPLevel
		expect models.Priority
	}{
		{"V5 及以上走高优先级", models.VIPLevelV5, models.PriorityHigh},
		{"V3-V4 走普通优先级", models.VIPLevelV3, models.PriorityNormal},
		{"V0-V2 保持原优先级", models.VIPLevelV0, models.PriorityLow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVIPFixture()
			f.addClient(t, "c1", "u1", tc.level)
			mgr := NewVIPManager(f)

			msg := &models.HubMessage{Priority: models.PriorityLow}
			mgr.SendToVIPWithPriority(vipCtx(), tc.level, msg)
			if msg.Priority != tc.expect {
				t.Fatalf("Priority = %v, want %v", msg.Priority, tc.expect)
			}
		})
	}
}

func TestSendWithVIPPriorityReadsClientLevel(t *testing.T) {
	// V6 客户端 → 该用户的消息应被提升为高优先级
	f := newVIPFixture()
	f.addClient(t, "c1", "vip6", models.VIPLevelV6)
	mgr := NewVIPManager(f)

	msg := &models.HubMessage{Priority: models.PriorityLow}
	mgr.SendWithVIPPriority(vipCtx(), "vip6", msg)

	if msg.Priority != models.PriorityHigh {
		t.Fatalf("Priority = %v, want %v", msg.Priority, models.PriorityHigh)
	}
	if f.sender.p2pUser != "vip6" {
		t.Fatalf("点对点发送目标 = %q, want vip6", f.sender.p2pUser)
	}
}

func TestSendWithVIPPriorityUnknownUserKeepsPriority(t *testing.T) {
	// 用户不在注册表：不臆断等级，优先级保持原值，但消息仍要尝试投递
	f := newVIPFixture()
	mgr := NewVIPManager(f)

	msg := &models.HubMessage{Priority: models.PriorityLow}
	mgr.SendWithVIPPriority(vipCtx(), "ghost", msg)

	if msg.Priority != models.PriorityLow {
		t.Fatalf("Priority = %v, want 保持 %v", msg.Priority, models.PriorityLow)
	}
	if f.sender.p2pUser != "ghost" {
		t.Fatalf("点对点发送目标 = %q, want ghost", f.sender.p2pUser)
	}
}

// ============================================================================
// 分类发送：综合分值区间决定优先级
// ============================================================================

// TestSendToUserWithClassificationScoreBranches 覆盖分值三段分档
//
// 分值构成：Priority.GetWeight()*5 + VIPLevel.GetLevel()*5 +
// UrgencyLevel.GetLevel()*10 + 业务分类加分（安全 15）
func TestSendToUserWithClassificationScoreBranches(t *testing.T) {
	cases := []struct {
		name   string
		cls    *models.MessageClassification
		expect models.Priority
	}{
		{
			name: "分值 >= 80 走高优先级",
			cls: &models.MessageClassification{
				Type:             models.MessageTypeText,
				Priority:         models.MessagePriorityHigh, // 3*5 = 15
				VIPLevel:         models.VIPLevelV6,          // 6*5 = 30
				UrgencyLevel:     models.UrgencyLevelHigh,    // 2*10 = 20
				BusinessCategory: models.BusinessCategorySecurity,
			}, // 合计 80
			expect: models.PriorityHigh,
		},
		{
			name: "分值 50-79 走普通优先级",
			cls: &models.MessageClassification{
				Type:         models.MessageTypeText,
				Priority:     models.MessagePriorityNormal, // 2*5 = 10
				VIPLevel:     models.VIPLevelV6,            // 30
				UrgencyLevel: models.UrgencyLevelHigh,      // 20
			}, // 合计 60
			expect: models.PriorityNormal,
		},
		{
			name: "分值 < 50 走低优先级",
			cls: &models.MessageClassification{
				Type:     models.MessageTypeText,
				Priority: models.MessagePriorityLow, // 5
			}, // 合计 5
			expect: models.PriorityLow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVIPFixture()
			mgr := NewVIPManager(f)
			msg := &models.HubMessage{}

			if score := tc.cls.GetFinalPriority(); (score >= 80) != (tc.expect == models.PriorityHigh) {
				t.Fatalf("测试用例自身失效：分值 = %d, 期望 %v", score, tc.expect)
			}

			mgr.SendToUserWithClassification(vipCtx(), "u1", msg, tc.cls)

			if msg.MessageType != models.MessageTypeText {
				t.Fatalf("MessageType = %v, want %v", msg.MessageType, models.MessageTypeText)
			}
			if msg.Priority != tc.expect {
				t.Fatalf("Priority = %v, want %v", msg.Priority, tc.expect)
			}
			if _, ok := msg.Data["priority_score"]; !ok {
				t.Fatal("分类分值未写入 msg.Data")
			}
			if _, ok := msg.Data["is_critical"]; !ok {
				t.Fatal("关键消息标记未写入 msg.Data")
			}
			if f.sender.p2pUser != "u1" {
				t.Fatalf("点对点发送目标 = %q, want u1", f.sender.p2pUser)
			}
		})
	}
}

func TestSendToUserWithClassificationNilSafe(t *testing.T) {
	// classification 为 nil：只投递，不改写消息
	f := newVIPFixture()
	mgr := NewVIPManager(f)

	msg := &models.HubMessage{Priority: models.PriorityNormal}
	mgr.SendToUserWithClassification(vipCtx(), "u1", msg, nil)

	if msg.Priority != models.PriorityNormal {
		t.Fatalf("Priority = %v, want 保持 %v", msg.Priority, models.PriorityNormal)
	}
	if msg.MessageType != "" {
		t.Fatalf("MessageType = %q, want 空（未分类不改写）", msg.MessageType)
	}
	if f.sender.p2pUser != "u1" {
		t.Fatalf("点对点发送目标 = %q, want u1", f.sender.p2pUser)
	}
}

// ============================================================================
// 统计与筛选
// ============================================================================

func TestGetVIPStatisticsCountsNonV0(t *testing.T) {
	f := newVIPFixture()
	f.addClient(t, "c1", "u-a", models.VIPLevelV0)
	f.addClient(t, "c2", "u-b", models.VIPLevelV3)
	f.addClient(t, "c3", "u-c", models.VIPLevelV6)
	mgr := NewVIPManager(f)

	stats := mgr.GetVIPStatistics()
	if stats["total_vip"] != 2 {
		t.Fatalf("total_vip = %d, want 2（V0 不计入）", stats["total_vip"])
	}
	if stats[string(models.VIPLevelV3)] != 1 || stats[string(models.VIPLevelV6)] != 1 {
		t.Fatalf("分级计数 = %v, want V3=1 V6=1", stats)
	}
}

func TestFilterVIPClients(t *testing.T) {
	f := newVIPFixture()
	f.addClient(t, "c1", "u-a", models.VIPLevelV0)
	f.addClient(t, "c2", "u-b", models.VIPLevelV6)
	mgr := NewVIPManager(f)

	got := mgr.FilterVIPClients(models.VIPLevelV3)
	if len(got) != 1 || got[0].ID != "c2" {
		t.Fatalf("筛选结果 = %v, want 仅 c2", got)
	}
}

// ============================================================================
// 等级管理：只升不降
// ============================================================================

func TestUpgradeVIPLevelOnlyUpgrades(t *testing.T) {
	f := newVIPFixture()
	f.addClient(t, "c1", "u1", models.VIPLevelV3)
	mgr := NewVIPManager(f)

	if !mgr.UpgradeVIPLevel(vipCtx(), "u1", models.VIPLevelV6) {
		t.Fatal("V3 → V6 应升级成功")
	}
	// 同一用户换客户端 ID 再挂一个，确认升级作用于该用户全部连接
	f.addClient(t, "c2", "u1", models.VIPLevelV3)
	if !mgr.UpgradeVIPLevel(vipCtx(), "u1", models.VIPLevelV6) {
		t.Fatal("多连接用户应能再次升级新连接")
	}
	// 降级尝试
	if mgr.UpgradeVIPLevel(vipCtx(), "u1", models.VIPLevelV1) {
		t.Fatal("V6 → V1 不应生效（只升不降）")
	}

	for _, c := range mgr.FilterVIPClients(models.VIPLevelV6) {
		if c.UserID != "u1" {
			t.Fatalf("意外客户端被升级：%s", c.ID)
		}
	}
	// 降级请求被拒绝：两个客户端的等级都应仍是 V6
	for _, id := range []string{"c1", "c2"} {
		c, ok := f.reg.GetClient(id)
		if !ok {
			t.Fatalf("%s 不在注册表", id)
		}
		if c.GetVIPLevel() != models.VIPLevelV6 {
			t.Fatalf("%s 等级 = %v, want V6（降级未被接受）", id, c.GetVIPLevel())
		}
	}
}

func TestUpgradeVIPLevelRejectsInvalidAndUnknownUser(t *testing.T) {
	f := newVIPFixture()
	f.addClient(t, "c1", "u1", models.VIPLevelV3)
	mgr := NewVIPManager(f)

	if mgr.UpgradeVIPLevel(vipCtx(), "u1", models.VIPLevel("bogus")) {
		t.Fatal("非法等级应返回 false")
	}
	if mgr.UpgradeVIPLevel(vipCtx(), "ghost", models.VIPLevelV6) {
		t.Fatal("不存在的用户应返回 false")
	}
}

func TestManagerExposesVIP(t *testing.T) {
	mgr := NewManager(newVIPFixture())
	first := mgr.VIP()
	if first == nil {
		t.Fatal("VIP() 返回 nil")
	}
	if mgr.VIP() != first {
		t.Fatal("VIP() 每次调用应返回同一实例")
	}
}

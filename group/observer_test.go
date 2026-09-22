/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 15:08:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 15:08:00
 * @FilePath: \go-wsc\group\observer_test.go
 * @Description: 观察者查询与统计单元测试
 *
 * 覆盖三级观察模型（全局 / 命名空间 / 命名空间+群组）的匹配边界，
 * 以及按 namespace 分组统计时「命名空间级」与「群组级」的分流口径。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"testing"

	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
)

// observerFixture 注册表开启观察者能力（构造器第二参为 observerEnabled）
type observerFixture struct {
	*fakeHost
	reg *connection.ShardedRegistry
}

func newObserverFixture() *observerFixture {
	f := &observerFixture{
		fakeHost: newFakeHost(),
		reg:      connection.NewShardedRegistry(false, true, connection.RegistryCapacity{}),
	}
	return f
}

func (f *observerFixture) GetShardedRegistry() *connection.ShardedRegistry { return f.reg }

// addObserver 注册一个观察者；groupID 为空表示命名空间级，namespace 为空表示全局
func (f *observerFixture) addObserver(t *testing.T, clientID, userID, namespace, groupID string) *models.Client {
	t.Helper()
	c := models.NewClient(clientID, userID, models.UserTypeObserver)
	c.Namespace = namespace
	c.GroupID = groupID
	f.reg.AddClient(c)
	return c
}

// ============================================================================
// 三级索引匹配
// ============================================================================

func TestGetObserversForMessageThreeTiers(t *testing.T) {
	f := newObserverFixture()
	f.addObserver(t, "c-global", "u-global", "", "")     // 全局：收所有命名空间
	f.addObserver(t, "c-ns", "u-ns", "ns1", "")          // 命名空间级：只收 ns1
	f.addObserver(t, "c-group", "u-group", "ns1", "g1")  // 群组级：只收 ns1+g1
	f.addObserver(t, "c-other-ns", "u-other", "ns2", "") // 不应命中
	mgr := NewObserverManager(f)

	got := mgr.GetObserversForMessage("ns1", "g1")
	ids := make(map[string]bool, len(got))
	for _, c := range got {
		ids[c.ID] = true
	}

	for _, want := range []string{"c-global", "c-ns", "c-group"} {
		if !ids[want] {
			t.Errorf("ns1+g1 应命中 %s，实际 %v", want, ids)
		}
	}
	if ids["c-other-ns"] {
		t.Errorf("ns2 的观察者不应命中 ns1 的消息，实际 %v", ids)
	}
}

func TestGetObserversForMessageGroupTierNeedsMatchingGroup(t *testing.T) {
	f := newObserverFixture()
	f.addObserver(t, "c-g1", "u1", "ns1", "g1")
	f.addObserver(t, "c-g2", "u2", "ns1", "g2")
	mgr := NewObserverManager(f)

	got := mgr.GetObserversForMessage("ns1", "g1")
	if len(got) != 1 || got[0].ID != "c-g1" {
		t.Fatalf("ns1+g1 命中 = %v, want 仅 c-g1（群组级观察者不跨组）", got)
	}
}

func TestGetObserverClientsByNamespaceExcludesGroupTier(t *testing.T) {
	// 兼容接口的语义：只取全局 + 命名空间级，不含群组级
	f := newObserverFixture()
	f.addObserver(t, "c-global", "u-global", "", "")
	f.addObserver(t, "c-ns", "u-ns", "ns1", "")
	f.addObserver(t, "c-group", "u-group", "ns1", "g1")
	mgr := NewObserverManager(f)

	got := mgr.GetObserverClientsByNamespace("ns1")
	if len(got) != 2 {
		t.Fatalf("命中 %d 个, want 2（全局 + 命名空间级）", len(got))
	}
	for _, c := range got {
		if c.ID == "c-group" {
			t.Fatal("群组级观察者不应出现在按命名空间的兼容接口结果里")
		}
	}
}

func TestGetObserverClientsSkipsClosed(t *testing.T) {
	f := newObserverFixture()
	f.addObserver(t, "c-open", "u1", "ns1", "")
	closed := f.addObserver(t, "c-closed", "u2", "ns1", "")
	closed.MarkClosed() // 已关闭的连接不应出现在统计里

	mgr := NewObserverManager(f)
	got := mgr.GetObserverClients()
	if len(got) != 1 || got[0].ID != "c-open" {
		t.Fatalf("观察者客户端 = %v, want 仅 c-open", got)
	}
}

// ============================================================================
// 计数与判定
// ============================================================================

func TestObserverCountsUserVsDevice(t *testing.T) {
	// 同一用户两个设备：用户数 1，设备数 2
	f := newObserverFixture()
	f.addObserver(t, "c1", "u1", "ns1", "")
	f.addObserver(t, "c2", "u1", "ns1", "")
	f.addObserver(t, "c3", "u2", "ns1", "")
	mgr := NewObserverManager(f)

	if got := mgr.GetObserverCount(); got != 2 {
		t.Fatalf("观察者用户数 = %d, want 2", got)
	}
	if got := mgr.GetObserverDeviceCount(); got != 3 {
		t.Fatalf("观察者设备数 = %d, want 3", got)
	}
}

func TestIsObserver(t *testing.T) {
	f := newObserverFixture()
	f.addObserver(t, "c1", "observer-user", "ns1", "")
	mgr := NewObserverManager(f)

	if !mgr.IsObserver("observer-user") {
		t.Fatal("已注册观察者应判定为 true")
	}
	if mgr.IsObserver("normal-user") {
		t.Fatal("未注册用户应判定为 false")
	}
}

func TestObserverDisabledRegistry(t *testing.T) {
	// 观察者能力关闭时注册表不建索引，查询应为空而非 panic
	f := &observerFixture{
		fakeHost: newFakeHost(),
		reg:      connection.NewShardedRegistry(false, false, connection.RegistryCapacity{}),
	}
	c := models.NewClient("c1", "u1", models.UserTypeObserver)
	c.Namespace = "ns1"
	f.reg.AddClient(c)
	mgr := NewObserverManager(f)

	if got := mgr.GetObserverCount(); got != 0 {
		t.Fatalf("观察者能力关闭时用户数 = %d, want 0", got)
	}
	if got := mgr.GetObserverClients(); len(got) != 0 {
		t.Fatalf("观察者能力关闭时客户端 = %v, want 空", got)
	}
}

// ============================================================================
// 统计
// ============================================================================

func TestGetObserverStatsFields(t *testing.T) {
	f := newObserverFixture()
	f.addObserver(t, "c1", "u1", "ns1", "g1")
	mgr := NewObserverManager(f)

	stats := mgr.GetObserverStats()
	if len(stats) != 1 {
		t.Fatalf("统计条数 = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.ObserverID != "u1" || s.ClientID != "c1" {
		t.Fatalf("标识字段 = (%q, %q), want (u1, c1)", s.ObserverID, s.ClientID)
	}
	if s.Namespace != "ns1" || s.GroupID != "g1" {
		t.Fatalf("路由字段 = (%q, %q), want (ns1, g1)", s.Namespace, s.GroupID)
	}
	if !s.IsConnected {
		t.Fatal("IsConnected 应为 true")
	}
	if s.BufferUsage > s.BufferSize {
		t.Fatalf("缓冲区用量 %d 超过容量 %d", s.BufferUsage, s.BufferSize)
	}
}

func TestGetObserverManagerStatsGroupsByNamespaceAndGroup(t *testing.T) {
	f := newObserverFixture()
	f.addObserver(t, "c1", "u1", "ns1", "")   // 命名空间级
	f.addObserver(t, "c2", "u2", "ns1", "g1") // 群组级
	f.addObserver(t, "c3", "u3", "ns1", "g2") // 群组级（新组）
	f.addObserver(t, "c4", "u4", "ns2", "")   // 另一命名空间
	mgr := NewObserverManager(f)

	out := mgr.GetObserverManagerStats()

	if out.TotalObservers != 4 || out.TotalDevices != 4 {
		t.Fatalf("总数 = (%d, %d), want (4, 4)", out.TotalObservers, out.TotalDevices)
	}

	ns1 := out.NamespaceStats["ns1"]
	if ns1 == nil {
		t.Fatal("ns1 分组缺失")
	}
	// 命名空间级观察者计入 TotalUsers；群组级走 GroupStats，不计入命名空间级
	if ns1.TotalUsers != 1 {
		t.Fatalf("ns1 命名空间级用户数 = %d, want 1", ns1.TotalUsers)
	}
	if ns1.TotalDevices != 3 {
		t.Fatalf("ns1 设备数 = %d, want 3", ns1.TotalDevices)
	}
	if len(ns1.GroupStats) != 2 {
		t.Fatalf("ns1 群组数 = %d, want 2（g1/g2 各一组）", len(ns1.GroupStats))
	}
	for _, g := range ns1.GroupStats {
		if g.TotalUsers != 1 || g.TotalDevices != 1 {
			t.Fatalf("群组 %s 计数 = (%d, %d), want (1, 1)", g.GroupID, g.TotalUsers, g.TotalDevices)
		}
	}

	ns2 := out.NamespaceStats["ns2"]
	if ns2 == nil || ns2.TotalUsers != 1 {
		t.Fatalf("ns2 分组 = %v, want 1 个命名空间级观察者", ns2)
	}
}

func TestGetObserverManagerStatsSameGroupAggregates(t *testing.T) {
	// 同一群组两个设备：GroupStats 应聚合为一条，而非两条
	f := newObserverFixture()
	f.addObserver(t, "c1", "u1", "ns1", "g1")
	f.addObserver(t, "c2", "u2", "ns1", "g1")
	mgr := NewObserverManager(f)

	ns1 := mgr.GetObserverManagerStats().NamespaceStats["ns1"]
	if len(ns1.GroupStats) != 1 {
		t.Fatalf("群组分组数 = %d, want 1（同组聚合）", len(ns1.GroupStats))
	}
	if ns1.GroupStats[0].TotalDevices != 2 {
		t.Fatalf("g1 设备数 = %d, want 2", ns1.GroupStats[0].TotalDevices)
	}
}

func TestManagerExposesObserver(t *testing.T) {
	mgr := NewManager(newObserverFixture())
	first := mgr.Observer()
	if first == nil {
		t.Fatal("Observer() 返回 nil")
	}
	if mgr.Observer() != first {
		t.Fatal("Observer() 每次调用应返回同一实例")
	}
}

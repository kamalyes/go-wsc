/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 03:56:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\routing\route_inject_test.go
 * @Description: Route.Inject 幂等短路的等价性与零分配回归
 *
 * Inject 原先无条件 context.WithValue，逐成员扇出路径上每个收件人都会把同一份
 * 路由重建一次（alloc profile 实测 Inject + WithValue 合占扇出全部分配 18.7%）
 * 短路后「ctx 已载相同路由」直接返回原 ctx
 *
 * 本文件锁定两件事：短路必须只在路由真正相同时触发（否则会把不同路由的注入吞掉，
 * 导致下游按错误的 appID/namespace/group 过滤——静默投错），以及相同路由下必须零分配
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package routing

import (
	"context"
	"testing"
)

// TestInject_ShortCircuitsOnIdenticalRoute 路由完全相同时必须返回原 ctx（零分配）
func TestInject_ShortCircuitsOnIdenticalRoute(t *testing.T) {
	base := NewRoute().WithAppID("app1").WithNamespace("ns1").WithGroupIDs([]string{"g1", "g2"})
	ctx := base.Inject(context.Background())

	same := NewRoute().WithAppID("app1").WithNamespace("ns1").WithGroupIDs([]string{"g1", "g2"})
	got := same.Inject(ctx)

	if got != ctx {
		t.Error("路由相同却新建了 ctx：短路未生效，逐成员扇出仍会重复分配")
	}
}

// TestInject_DoesNotShortCircuitOnDifference 任一维度不同都必须真正注入
//
// 这是反向保护：短路判定过宽会把不同路由的注入吞掉，下游按上一份路由过滤连接，
// 表现为跨 app/ns/群组静默投错或漏投，且无任何报错。
func TestInject_DoesNotShortCircuitOnDifference(t *testing.T) {
	base := NewRoute().WithAppID("app1").WithNamespace("ns1").WithGroupIDs([]string{"g1"})
	ctx := base.Inject(context.Background())

	cases := []struct {
		name  string
		route *Route
		want  func(*RoutingContext) bool
	}{
		{
			name:  "appID 不同",
			route: NewRoute().WithAppID("app2").WithNamespace("ns1").WithGroupIDs([]string{"g1"}),
			want:  func(rc *RoutingContext) bool { return rc.AppID == "app2" },
		},
		{
			name:  "namespace 不同",
			route: NewRoute().WithAppID("app1").WithNamespace("ns2").WithGroupIDs([]string{"g1"}),
			want:  func(rc *RoutingContext) bool { return rc.Namespace == "ns2" },
		},
		{
			name:  "groupIDs 内容不同",
			route: NewRoute().WithAppID("app1").WithNamespace("ns1").WithGroupIDs([]string{"g9"}),
			want:  func(rc *RoutingContext) bool { return len(rc.GroupIDs) == 1 && rc.GroupIDs[0] == "g9" },
		},
		{
			name:  "groupIDs 长度不同",
			route: NewRoute().WithAppID("app1").WithNamespace("ns1").WithGroupIDs([]string{"g1", "g2"}),
			want:  func(rc *RoutingContext) bool { return len(rc.GroupIDs) == 2 },
		},
		{
			name:  "groupIDs 由非空变空",
			route: NewRoute().WithAppID("app1").WithNamespace("ns1"),
			want:  func(rc *RoutingContext) bool { return len(rc.GroupIDs) == 0 },
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.route.Inject(ctx)
			if got == ctx {
				t.Fatal("路由不同却短路返回原 ctx：下游会按上一份路由过滤，静默投错")
			}
			rc := RoutingFromContext(got)
			if rc == nil {
				t.Fatal("注入后 ctx 无路由")
			}
			if !c.want(rc) {
				t.Errorf("注入值不符预期: appID=%q ns=%q gids=%v", rc.AppID, rc.Namespace, rc.GroupIDs)
			}
		})
	}
}

// TestInject_EmptyCtxStillInjects 原 ctx 无路由时必须注入（不可因 cur==nil 而误短路）
func TestInject_EmptyCtxStillInjects(t *testing.T) {
	got := NewRoute().WithAppID("app1").WithNamespace("ns1").Inject(context.Background())
	rc := RoutingFromContext(got)
	if rc == nil {
		t.Fatal("空 ctx 注入后仍无路由")
	}
	if rc.AppID != "app1" || rc.Namespace != "ns1" {
		t.Errorf("注入值错误: appID=%q ns=%q", rc.AppID, rc.Namespace)
	}
}

// TestInject_AppIDNormalizationRespectedByShortCircuit appID 归一化后再比较
//
// Inject 对空 appID 归一化为 DefaultAppID。短路比较必须用归一化后的值，
// 否则「ctx 里是 DefaultAppID、传入空串」会被判为不同而白注入一次。
func TestInject_AppIDNormalizationRespectedByShortCircuit(t *testing.T) {
	ctx := NewRoute().WithNamespace("ns1").Inject(context.Background()) // appID 空 → 归一化
	rc := RoutingFromContext(ctx)
	if rc == nil || rc.AppID == "" {
		t.Fatal("空 appID 未被归一化")
	}
	// 同样传空 appID，归一化后与 ctx 相同，应短路
	if got := NewRoute().WithNamespace("ns1").Inject(ctx); got != ctx {
		t.Error("归一化后相同却未短路：比较用了归一化前的值")
	}
}

// BenchmarkInject_ShortCircuit 相同路由下应零分配
func BenchmarkInject_ShortCircuit(b *testing.B) {
	r := NewRoute().WithAppID("app1").WithNamespace("ns1").WithGroupIDs([]string{"g1"})
	ctx := r.Inject(context.Background())
	b.ReportAllocs()
	for b.Loop() {
		_ = r.Inject(ctx)
	}
}

// BenchmarkInject_Fresh 不同路由下为对照（必然分配）
func BenchmarkInject_Fresh(b *testing.B) {
	r := NewRoute().WithAppID("app1").WithNamespace("ns1").WithGroupIDs([]string{"g1"})
	b.ReportAllocs()
	for b.Loop() {
		_ = r.Inject(context.Background())
	}
}

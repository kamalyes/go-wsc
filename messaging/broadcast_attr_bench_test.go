/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 02:23:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\messaging\broadcast_attr_bench_test.go
 * @Description: 属性维度广播基准（attrIdx 定点 vs 全量扫描）
 *
 * 对照 BroadcastToFiltered_ScanSmallSubset：后者用通用谓词必然全量扫描，
 * 本组用四个典型入口，索引命中占比低时走定点路径。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package messaging

import (
	"fmt"
	"testing"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/models"
)

// benchAttrRegistry 注册 n 个 customer 连接 + hits 个 agent 连接
// agent 占比极低，模拟「向少量管理员广播」的真实形态
func benchAttrRegistry(b *testing.B, n, hits int) *Manager {
	b.Helper()
	m, host := newTestManager()
	// 与 benchScanRegistry 同口径压低日志等级：DEBUG 下每次广播一条诊断日志，
	// 其序列化开销会淹没定点路径本身的耗时
	host.logger = logger.NewLogger().WithLevel(logger.ERROR).WithShowCaller(false)
	registry := host.GetShardedRegistry()
	for i := 0; i < n; i++ {
		c := makeTestClient(fmt.Sprintf("c-%06d", i), fmt.Sprintf("u-%06d", i))
		c.UserType = models.UserTypeCustomer
		c.Role = models.UserRoleCustomer
		c.ClientType = models.ClientTypeWeb
		registry.AddClient(c)
	}
	for i := 0; i < hits; i++ {
		c := makeTestClient(fmt.Sprintf("a-%06d", i), fmt.Sprintf("au-%06d", i))
		c.UserType = models.UserTypeAgent
		c.Role = models.UserRoleAdmin
		c.ClientType = models.ClientTypeDesktop
		registry.AddClient(c)
	}
	return m
}

// BenchmarkBroadcastByUserType_SmallSubset 1 万连接中命中 50 个 agent
func BenchmarkBroadcastByUserType_SmallSubset(b *testing.B) {
	m := benchAttrRegistry(b, 10000, 50)
	msg := makeGroupMessage("sender").WithGuarantee(models.GuaranteeEphemeral)
	ctx := benchScanCtx()
	b.ReportAllocs()
	for b.Loop() {
		m.BroadcastByUserType(ctx, models.UserTypeAgent, msg)
	}
}

// BenchmarkBroadcastToRole_SmallSubset 同上，Role 维度
func BenchmarkBroadcastToRole_SmallSubset(b *testing.B) {
	m := benchAttrRegistry(b, 10000, 50)
	msg := makeGroupMessage("sender").WithGuarantee(models.GuaranteeEphemeral)
	ctx := benchScanCtx()
	b.ReportAllocs()
	for b.Loop() {
		m.BroadcastToRole(ctx, models.UserRoleAdmin, msg)
	}
}

// BenchmarkBroadcastToClientType_SmallSubset 同上，ClientType 维度
func BenchmarkBroadcastToClientType_SmallSubset(b *testing.B) {
	m := benchAttrRegistry(b, 10000, 50)
	msg := makeGroupMessage("sender").WithGuarantee(models.GuaranteeEphemeral)
	ctx := benchScanCtx()
	b.ReportAllocs()
	for b.Loop() {
		m.BroadcastToClientType(ctx, models.ClientTypeDesktop, msg)
	}
}

// BenchmarkBroadcastByUserType_SmallSubset50k 5 万连接中命中 50 个
// 索引路径成本与总连接数无关，此项与 10k 版本应基本同价
func BenchmarkBroadcastByUserType_SmallSubset50k(b *testing.B) {
	m := benchAttrRegistry(b, 50000, 50)
	msg := makeGroupMessage("sender").WithGuarantee(models.GuaranteeEphemeral)
	ctx := benchScanCtx()
	b.ReportAllocs()
	for b.Loop() {
		m.BroadcastByUserType(ctx, models.UserTypeAgent, msg)
	}
}

// BenchmarkBroadcastByUserType_NoHit 零命中短路（索引与注册表事务性维护）
func BenchmarkBroadcastByUserType_NoHit(b *testing.B) {
	m := benchAttrRegistry(b, 10000, 0)
	msg := makeGroupMessage("sender").WithGuarantee(models.GuaranteeEphemeral)
	ctx := benchScanCtx()
	b.ReportAllocs()
	for b.Loop() {
		m.BroadcastByUserType(ctx, models.UserTypeAgent, msg)
	}
}

// BenchmarkRegistry_AddRemoveWithAttrIdx 注册/注销的索引维护开销
// 索引不是免费的：每次 Add/Remove 多付 4 个维度的分片锁写入
func BenchmarkRegistry_AddRemoveWithAttrIdx(b *testing.B) {
	_, host := newTestManager()
	registry := host.GetShardedRegistry()
	c := makeTestClient("c-churn", "u-churn")
	c.UserType = models.UserTypeAgent
	c.Role = models.UserRoleAdmin
	c.ClientType = models.ClientTypeDesktop
	c.Department = models.Department("eng")
	b.ReportAllocs()
	for b.Loop() {
		registry.AddClient(c)
		registry.RemoveClient("c-churn", "u-churn")
	}
}

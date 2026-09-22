/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 10:22:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 10:57:00
 * @FilePath: \go-wsc\spi\wiring_test.go
 * @Description: 装配编排的用例

 * 本包不 import 任何适配器包（依赖方向 adapter -> core），故测试用桩
 * StoreHooks 而非真实适配器：这里要验证的是「编排」——前置校验怎么判、
 * hook 按什么顺序调、失败如何中止、依赖齐不齐——而不是某个适配器实现
 * 得对不对（那由各适配器包自己的测试覆盖）

 * 顺带替换了 hub/hub_getter_test.go 里删掉的
 * TestHub_InitializeRepositories_ParamValidation：该函数已从 Hub 迁到本包，
 * 参数校验的用例随之归位

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 桩实现
// ============================================================================

// stubTarget StoreTarget 的记账桩
//
// 只记录「被注入了什么」，不模拟存储行为：本包关心的是注入是否发生、
// 发生了几次，存储方法的行为属于适配器测试的范畴
type stubTarget struct {
	online      OnlineStore
	stats       HubStats
	group       GroupStore
	workload    WorkloadStore
	messageSink MessageSink
	connStore   ConnectionStore
	connQuality ConnectionQualityStore
	offline     OfflineQueue
}

func (t *stubTarget) SetOnlineStatusRepository(s OnlineStore)         { t.online = s }
func (t *stubTarget) SetHubStatsRepository(s HubStats)                { t.stats = s }
func (t *stubTarget) SetGroupRepository(s GroupStore)                 { t.group = s }
func (t *stubTarget) SetWorkloadRepository(s WorkloadStore)           { t.workload = s }
func (t *stubTarget) SetMessageRecordRepository(s MessageSink)        { t.messageSink = s }
func (t *stubTarget) SetConnectionRecordRepository(s ConnectionStore) { t.connStore = s }
func (t *stubTarget) SetConnectionQualityRepository(s ConnectionQualityStore) {
	t.connQuality = s
}
func (t *stubTarget) SetOfflineMessageHandler(q OfflineQueue) { t.offline = q }

var _ StoreTarget = (*stubTarget)(nil)

// stubHook StoreHooks 的记账桩
type stubHook struct {
	name string
	// err 非 nil 时 Configure 返回它，用于验证失败中止
	err error
	// calls 记录被调用次数；捕获到的 deps 供断言整包传递
	calls int
	deps  StoreDeps
	// order 共享调用序，验证 hook 按传入顺序串行执行
	order *[]string
	// target 非 nil 时注入一个存根 online store，验证注入确实落到 target
	target *stubTarget
}

func (h *stubHook) Configure(_ context.Context, tgt StoreTarget, deps StoreDeps) error {
	h.calls++
	h.deps = deps
	if h.order != nil {
		*h.order = append(*h.order, h.name)
	}
	if h.err != nil {
		return h.err
	}
	if h.target != nil {
		tgt.SetOnlineStatusRepository(nil)
	}
	return nil
}

var _ StoreHooks = (*stubHook)(nil)

// ============================================================================
// Initialize 参数校验
// ============================================================================

// TestInitialize_NilHub 注入目标为 nil 时立即报错
//
// 不校验的话要在第一个 hook 里解引用 nil 才 panic，栈里看不出是调用方
// 传错了参数
func TestInitialize_NilHub(t *testing.T) {
	err := Initialize(context.Background(), nil, nil, nil, &stubHook{name: "a"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hub target is nil")
}

// TestInitialize_NoHooks 未提供任何 hook 时报错
//
// 「没有任何持久化后端」几乎总是配置疏漏，静默通过会把问题推到线上才显形
func TestInitialize_NoHooks(t *testing.T) {
	err := Initialize(context.Background(), &stubTarget{}, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no storage hooks")
}

// TestInitialize_AllHooksNil 显式传入 nil hook 等同未提供
//
// 调用方常写 Initialize(ctx, hub, redis, db, redisHooks, gormHooks)，单后端
// 部署时其中一个为 nil —— 两个都 nil 才应报错
func TestInitialize_AllHooksNil(t *testing.T) {
	err := Initialize(context.Background(), &stubTarget{}, nil, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no storage hooks")
}

// TestInitialize_OneNilOneActive 单后端部署：一个是 nil，另一个照常装配
func TestInitialize_OneNilOneActive(t *testing.T) {
	hook := &stubHook{name: "redis"}
	err := Initialize(context.Background(), &stubTarget{}, nil, nil, nil, hook)
	require.NoError(t, err)
	assert.Equal(t, 1, hook.calls, "非 nil 的 hook 应被调用一次")
}

// ============================================================================
// Initialize 编排行为
// ============================================================================

// TestInitialize_PassesDepsWhole 依赖整包传递
//
// StoreDeps 刻意不拆成 Redis / RDBMS 两份：离线消息处理器这类组件同时
// 需要队列与持久化，拆开后任一 hook 都拿不到完整依赖
func TestInitialize_PassesDepsWhole(t *testing.T) {
	hook := &stubHook{name: "h"}
	target := &stubTarget{}

	// 传 nil 句柄（避免依赖真实 Redis/GORM），只验证「整包透传」这一点
	err := Initialize(context.Background(), target, nil, nil, hook)
	require.NoError(t, err)
	assert.Equal(t, 1, hook.calls)
	assert.Nil(t, hook.deps.Redis)
	assert.Nil(t, hook.deps.DB)
}

// TestInitialize_CallsHooksInOrder hook 按传入顺序串行执行
//
// gorm 适配器的离线消息持久化可能依赖 redis 适配器先建好队列，顺序不能乱，
// 更不能并行
func TestInitialize_CallsHooksInOrder(t *testing.T) {
	var order []string
	a := &stubHook{name: "redis", order: &order}
	b := &stubHook{name: "gorm", order: &order}
	c := &stubHook{name: "clickhouse", order: &order}

	err := Initialize(context.Background(), &stubTarget{}, nil, nil, a, b, c)
	require.NoError(t, err)
	assert.Equal(t, []string{"redis", "gorm", "clickhouse"}, order)
}

// TestInitialize_AbortsOnHookError hook 失败立即中止，后续 hook 不再执行
//
// 继续执行会让「装配一半成功」的状态更难诊断：后面的 hook 可能依赖前面
// 已建好的东西，一个失败后继续跑只会产生二次错误掩盖根因
func TestInitialize_AbortsOnHookError(t *testing.T) {
	var order []string
	sentinel := errors.New("boom")
	a := &stubHook{name: "redis", order: &order}
	b := &stubHook{name: "gorm", order: &order, err: sentinel}
	c := &stubHook{name: "clickhouse", order: &order}

	err := Initialize(context.Background(), &stubTarget{}, nil, nil, a, b, c)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "原始错误应可 errors.Is 追溯")
	assert.Equal(t, []string{"redis", "gorm"}, order, "gorm 失败后 clickhouse 不应执行")
	assert.Equal(t, 0, c.calls)
}

// TestInitialize_AllHooksRun 全部 hook 成功时每个恰好调用一次
func TestInitialize_AllHooksRun(t *testing.T) {
	a := &stubHook{name: "redis"}
	b := &stubHook{name: "gorm"}

	err := Initialize(context.Background(), &stubTarget{}, nil, nil, a, b)
	require.NoError(t, err)
	assert.Equal(t, 1, a.calls)
	assert.Equal(t, 1, b.calls)
}

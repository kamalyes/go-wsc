/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-02 10:00:09
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-27 17:26:03
 * @FilePath: \go-wsc\messaging\fanout_test.go
 * @Description: 批量扇出投递测试与基准（覆盖 messaging/fanout.go）
 *
 *   - SendToGroupMembers 核心语义：全部成员触达 / 离线成员转存分层 /
 *     GroupIDs 业务群组不做设备级系统组过滤 / VIP 成员等权扇出（以实际代码为准）
 *   - SendToClientsWithRetry 非空列表结果覆盖
 *   - BenchmarkFanout：N 成员扇出分配画像（路线图 M1：每消息 allocs/op）
 *
 * 复用 fake_host_test.go 的 newTestManager / makeTestClient / makeVIPClient /
 * makeGroupMessage / newOfflineRecordingHandler 等 helper，与 send_test.go 既有
 * 用例互补（既有覆盖排除发送者与无 handler 的失败分层，本文件补齐全员触达、
 * 离线转存分层、GroupIDs/VIP 语义与扇出基准）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// SendToGroupMembers 核心语义测试
// ============================================================================

// TestSendToGroupMembers_AllMembersReached 验证全部成员触达：
// N 个在线成员每人一条连接，扇出后全员收到可解析的消息帧，
// BroadcastResult 计数全绿（Total=Success=N，无离线无失败）
func TestSendToGroupMembers_AllMembersReached(t *testing.T) {
	m, host := newTestManager()

	const members = 3
	clients := make([]*models.Client, members)
	memberIDs := make([]string, members)
	for i := 0; i < members; i++ {
		clients[i] = makeTestClient(fmt.Sprintf("c-all-%d", i), fmt.Sprintf("u-all-%d", i))
		host.GetShardedRegistry().AddClient(clients[i])
		memberIDs[i] = clients[i].UserID
	}

	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())

	result := m.SendToGroupMembers(ctx, memberIDs, makeGroupMessage("sender"), false)

	require.NotNil(t, result)
	assert.Equal(t, members, result.Total, "投递目标数应等于成员数")
	assert.Equal(t, members, result.Success, "全员在线应全部成功")
	assert.Equal(t, 0, result.Offline)
	assert.Equal(t, 0, result.Failed)
	assert.Empty(t, result.Errors)

	// 逐成员验证收到的是可解析消息帧（executor 同步返回，无需等待）
	for _, c := range clients {
		select {
		case data := <-c.SendChan:
			var got models.HubMessage
			require.NoErrorf(t, json.Unmarshal(data, &got), "成员 %s 收到的应为合法 JSON 帧", c.UserID)
			assert.Equal(t, "hello group", got.Content, "成员 %s 应收到原文内容", c.UserID)
		case <-time.After(time.Second):
			t.Fatalf("成员 %s 应收到消息", c.UserID)
		}
	}
}

// TestSendToGroupMembers_OfflineStoredWithHandler 验证成员分层语义（在线送达 / 离线转存）：
// 注入离线处理器后，在线成员实时送达计 Success，离线成员转存离线队列计 Offline，
// OfflineIDs 精确记录离线成员（补齐 send_test.go 仅覆盖无 handler 失败分层的另一半）
func TestSendToGroupMembers_OfflineStoredWithHandler(t *testing.T) {
	m, host := newTestManager()
	offline, log := newOfflineRecordingHandler()
	m.offlineHandler = offline

	online := makeTestClient("c-tier-on", "u-tier-on")
	host.GetShardedRegistry().AddClient(online)

	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())

	result := m.SendToGroupMembers(ctx,
		[]string{"u-tier-on", "u-tier-off"}, makeGroupMessage("sender"), false)

	assert.Equal(t, 2, result.Total)
	assert.Equal(t, 1, result.Success, "在线成员应实时送达")
	assert.Equal(t, 1, result.Offline, "离线成员应转存离线队列")
	assert.Equal(t, 0, result.Failed)
	assert.Contains(t, result.OfflineIDs, "u-tier-off", "OfflineIDs 应记录离线成员")

	// 转存记账：离线成员恰好转存一次（StoreOfflineMessage 同步双写完成）
	assert.Equal(t, 1, log.getStoreCalled(), "离线成员应恰好转存一次")

	// 在线成员收到实时帧
	select {
	case data := <-online.SendChan:
		assert.NotEmpty(t, data)
	case <-time.After(time.Second):
		t.Fatal("在线成员应实时收到消息")
	}
}

// TestSendToGroupMembers_GroupIDsNotDeviceFilter 验证 GroupIDs 过滤语义（以实际代码为准）：
// msg.GroupIDs 为「业务群组ID」（成员定位已由 memberIDs 显式给出），投递时
// ForEachUserClientFiltered 不将其与 client.GroupID「连接级系统组」做匹配过滤——
// 成员在不同系统组（含无组）的设备均应触达（强行匹配会导致群成员设备全部被过滤）
func TestSendToGroupMembers_GroupIDsNotDeviceFilter(t *testing.T) {
	m, host := newTestManager()

	inGroup := makeTestClient("c-ingroup", "u-grp", constants.DefaultNamespace, "group-sys-a")
	noGroup := makeTestClient("c-nogroup", "u-grp-2")
	host.GetShardedRegistry().AddClient(inGroup)
	host.GetShardedRegistry().AddClient(noGroup)

	msg := makeGroupMessage("sender")
	msg.SetGroupIDs([]string{"g-biz-1"}) // 业务群组ID，与连接级系统组不同维度

	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs([]string{"g-biz-1"}).
		Inject(context.Background())

	result := m.SendToGroupMembers(ctx, []string{"u-grp", "u-grp-2"}, msg, false)

	assert.Equal(t, 2, result.Success, "不同系统组的成员设备都应触达（GroupIDs 不做设备级过滤）")

	for _, c := range []*models.Client{inGroup, noGroup} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("成员设备 %s 应收到消息（GroupIDs 不参与系统组过滤）", c.ID)
		}
	}
}

// TestSendToGroupMembers_VIPMembersEqualTiering 验证 VIP/成员分层语义（以实际代码为准）：
// fanout.go 逐成员扇出无 VIP 分层——VIP 等级不影响触达与结果分类，V5 VIP 成员与
// V0 普通成员在同一批等权投递（VIP 维度的定向广播在 broadcast.go GetClientsByVIPLevel，
// 不经 fanout.go 扇出管道）
func TestSendToGroupMembers_VIPMembersEqualTiering(t *testing.T) {
	m, host := newTestManager()

	vip := makeVIPClient("c-vip5", "u-vip5", models.VIPLevelV5)
	normal := makeVIPClient("c-vip0", "u-vip0", models.VIPLevelV0)
	host.GetShardedRegistry().AddClient(vip)
	host.GetShardedRegistry().AddClient(normal)

	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())

	result := m.SendToGroupMembers(ctx, []string{"u-vip5", "u-vip0"}, makeGroupMessage("sender"), false)

	assert.Equal(t, 2, result.Success, "VIP 与普通成员应等权触达（扇出无 VIP 分层）")
	assert.Equal(t, 0, result.Failed)

	for _, c := range []*models.Client{vip, normal} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("成员 %s 不应因 VIP 等级被区别对待", c.UserID)
		}
	}
}

// ============================================================================
// SendToClientsWithRetry 测试
// ============================================================================

// TestSendToClientsWithRetry_AllClientsReached 验证非空客户端列表：
// 按客户端列表逐用户投递，结果 map 以 UserID 为键覆盖全部目标用户
// （补齐 send_test.go 仅覆盖空列表的另一半）
func TestSendToClientsWithRetry_AllClientsReached(t *testing.T) {
	m, host := newTestManager()

	c1 := makeTestClient("c-retry-1", "u-retry-1")
	c2 := makeTestClient("c-retry-2", "u-retry-2")
	host.GetShardedRegistry().AddClient(c1)
	host.GetShardedRegistry().AddClient(c2)

	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())

	results := m.SendToClientsWithRetry(ctx, []*models.Client{c1, c2}, makeGroupMessage("sender"), 1)

	require.Len(t, results, 2, "结果 map 应覆盖全部目标用户")
	for _, uid := range []string{"u-retry-1", "u-retry-2"} {
		r, ok := results[uid]
		require.Truef(t, ok, "用户 %s 应有投递结果", uid)
		assert.Truef(t, r.Success, "在线用户 %s 应投递成功", uid)
		assert.False(t, r.StoredOffline)
	}

	for _, c := range []*models.Client{c1, c2} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("客户端 %s 应收到消息", c.ID)
		}
	}
}

// ============================================================================
// runFanout 有界并发执行器语义
// ============================================================================

// TestRunFanout_AllItemsExecutedOnce 验证每个条目恰好执行一次、同步返回时已全部完成
// 并发度小于条目数时 worker 需轮转取号，不得漏号或重号
func TestRunFanout_AllItemsExecutedOnce(t *testing.T) {
	m, _ := newTestManager()
	for _, concurrency := range []int{1, 3, 64, 1000} {
		t.Run(fmt.Sprintf("concurrency=%d", concurrency), func(t *testing.T) {
			const n = 500
			items := make([]int, n)
			hits := make([]int32, n)
			for i := range items {
				items[i] = i
			}
			runFanout(context.Background(), m, items, concurrency, func(idx int, item int) {
				require.Equal(t, idx, item, "idx 必须与条目对齐")
				atomic.AddInt32(&hits[idx], 1)
			})
			for i := range hits {
				assert.Equal(t, int32(1), hits[i], "条目 %d 应恰好执行一次", i)
			}
		})
	}
}

// TestRunFanout_PanicIsolatedPerItem 验证逐条目 panic 隔离：
// 单条目 panic 不打挂 worker、不波及其他条目，其余条目照常全部完成
// （旧执行器的 recover 粒度亦为逐条目，此语义不得退化）
func TestRunFanout_PanicIsolatedPerItem(t *testing.T) {
	m, _ := newTestManager()
	const n = 200
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	var done int32
	runFanout(context.Background(), m, items, 8, func(_ int, item int) {
		if item%7 == 0 {
			panic(fmt.Sprintf("boom-%d", item))
		}
		atomic.AddInt32(&done, 1)
	})
	expected := int32(0)
	for i := 0; i < n; i++ {
		if i%7 != 0 {
			expected++
		}
	}
	assert.Equal(t, expected, atomic.LoadInt32(&done), "非 panic 条目应全部完成")
}

// TestRunFanout_ConcurrencyBounded 验证在途条目数不超过并发上限
// （数万成员群广播无界扇出会打爆 Redis 连接池并滞留消息副本，见 fanoutConcurrency 注释）
func TestRunFanout_ConcurrencyBounded(t *testing.T) {
	m, _ := newTestManager()
	const (
		n     = 300
		limit = 5
	)
	items := make([]int, n)
	var inFlight, peak int32
	runFanout(context.Background(), m, items, limit, func(_ int, _ int) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(time.Microsecond)
		atomic.AddInt32(&inFlight, -1)
	})
	assert.LessOrEqual(t, atomic.LoadInt32(&peak), int32(limit), "在途条目数不得超过并发上限")
}

// TestRunFanout_EmptyItems 空列表直接返回，不得 spawn worker 也不得 panic
func TestRunFanout_EmptyItems(t *testing.T) {
	m, _ := newTestManager()
	called := false
	runFanout(context.Background(), m, []string(nil), 64, func(int, string) { called = true })
	assert.False(t, called)
}

// ============================================================================
// 扇出基准
// ============================================================================

// benchFanoutSink 扇出基准产物 sink：防止死码消除
var benchFanoutSink int

// BenchmarkFanout N 成员扇出基准（SendToGroupMembers 有界并发逐成员可靠扇出）
//
// 形态说明：全员本地在线（每成员 1 条 WS 连接 + 消费协程常驻排空，生产 WS 写协程
// 恒消费，TrySend 恒成功），稳态测量逐成员主链路：CloneForFanout 扇出克隆 +
// InjectRoute 信封注入 + 在线判定 + 送达分级解析 + json.Marshal + TrySend 入队。
//
// 路线图 M1 关注点：allocs/op 分配画像——总分配数 / N 即每消息分配数，
// 为后续扇出路径优化（克隆共享 / 序列化复用）提供对照基线。
// 日志器注入 Info 级：默认 NewDefaultLogger 为 Debug 控制台输出，控制台 IO 会污染测量
func BenchmarkFanout(b *testing.B) {
	for _, n := range []int{100, 1000} {
		b.Run(fmt.Sprintf("members=%d", n), func(b *testing.B) {
			m, host := newTestManager()
			host.logger = logger.NewLogger().WithLevel(logger.INFO).WithShowCaller(false)

			ids := make([]string, n)
			registry := host.GetShardedRegistry()
			for i := 0; i < n; i++ {
				uid := fmt.Sprintf("u-%06d", i)
				ids[i] = uid
				client := makeTestClient(fmt.Sprintf("c-%06d", i), uid)
				registry.AddClient(client)
				go func() {
					for range client.SendChan {
					}
				}()
			}

			ctx := routing.NewRoute().
				WithAppID("").
				WithNamespace(constants.DefaultNamespace).
				WithGroupIDs(nil).
				Inject(context.Background())
			msg := makeGroupMessage("sender-bench")

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				result := m.SendToGroupMembers(ctx, ids, msg, false)
				benchFanoutSink += result.Success
			}
		})
	}
}

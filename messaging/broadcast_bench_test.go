/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-10 21:36:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-10 21:36:00
 * @FilePath: \go-wsc\messaging\broadcast_bench_test.go
 * @Description: 消息域扇出热路径基准（BroadcastFrameToUserIDs 分片并行扇出）
 *
 * 形态说明：每用户 1 条 WS 连接，SendChan 缓冲 16
 * 首轮后通道写满，TrySend 失败走成员级分级兜底，即 express 语义丢弃纯计数零 IO
 * 稳态测量完整扇出循环：分片锁遍历 + 回放门闩快路径 + 兜底分支
 * 与生产洪峰下扇出能力压满时的真实形态一致
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// benchFanoutUsers 注册 n 个用户（每用户 1 条 WS 连接）并返回 userID 列表
// 日志器注入 Info 级：默认 NewDefaultLogger 为 Debug 控制台输出，控制台 IO 会污染测量
func benchFanoutUsers(b *testing.B, n int) (*Manager, []string) {
	b.Helper()
	m, host := newTestManager()
	host.logger = logger.NewLogger().WithLevel(logger.INFO).WithShowCaller(false)
	ids := make([]string, n)
	registry := host.GetShardedRegistry()
	for i := 0; i < n; i++ {
		uid := fmt.Sprintf("u-%06d", i)
		ids[i] = uid
		registry.AddClient(makeTestClient(fmt.Sprintf("c-%06d", i), uid))
	}
	return m, ids
}

// BenchmarkManager_BroadcastFrameToUserIDs 预编码帧全量扇出（聚合 drain 的产出投递路径）
func BenchmarkManager_BroadcastFrameToUserIDs(b *testing.B) {
	for _, n := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("users=%d", n), func(b *testing.B) {
			m, ids := benchFanoutUsers(b, n)

			// ephemeral 档：通道满兜底为语义丢弃（无离线落库副作用，稳态可测）
			msg := makeGroupMessage("sender").WithGuarantee(models.GuaranteeEphemeral)
			frame, err := json.Marshal(msg)
			require.NoError(b, err)

			ctx := routing.NewRoute().
				WithAppID("").
				WithNamespace(constants.DefaultNamespace).
				WithGroupIDs(nil).
				Inject(context.Background())

			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				m.BroadcastFrameToUserIDs(ctx, ids, msg, frame)
			}
		})
	}
}

// BenchmarkManager_DeliverToGroupReliable 群组可靠投递 P2P 逐成员扇出（ACK 对账语义）
//
// 形态说明：全员本地在线，消费协程常驻排空 SendChan
// 生产 WS 写协程恒消费，TrySend 恒成功，稳态测量逐成员主链路
// 即消息 Clone、信封注入、重试链构造、落库前置、门闩预检
// 日志器注入 Info 级：默认 NewDefaultLogger 为 Debug 控制台输出，控制台 IO 会淹没测量
func BenchmarkManager_DeliverToGroupReliable(b *testing.B) {
	const members = 1000
	m, host := newTestManager()
	// 生产日志形态：Info 级
	host.logger = logger.NewLogger().WithLevel(logger.INFO).WithShowCaller(false)
	// 群组仓储：内存假体（默认 nil，可靠投递前置校验直接报错）
	groupRepo := newFakeGroupStore()
	host.groupRepo = groupRepo

	ids := make([]string, 0, members)
	registry := host.GetShardedRegistry()
	for i := 0; i < members; i++ {
		uid := fmt.Sprintf("u-%06d", i)
		ids = append(ids, uid)
		client := makeTestClient(fmt.Sprintf("c-%06d", i), uid)
		registry.AddClient(client)
		// 消费协程常驻排空（生产 WS 写协程恒消费，避免 bench 落入重试退避睡眠）
		go func() {
			for range client.SendChan {
			}
		}()
	}

	msg := makeGroupMessage("sender-bench")
	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs([]string{"g-bench"}).
		Inject(context.Background())
	// 成员注册用 ctx 实际携带的 appID（Inject 幂等归一化可能补默认值，
	// 与 deliverToGroupReliable 的 batchGetGroupMembers 同源同键）
	require.NoError(b, groupRepo.AddMembers(context.Background(), routing.AppIDFromContext(ctx), constants.DefaultNamespace, "g-bench", ids))

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		m.deliverToGroupReliable(ctx, msg, false)
	}
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:09:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-25 17:02:51
 * @FilePath: \go-wsc\broker\fanout_guard_test.go
 * @Description: fanout 来源标记回归测试（跨节点重复派发防护）
 *
 * 覆盖缺陷：HandleBroadcast 与 HandleDeliver 曾返回同一个 c.handle，消费侧无从区分
 * 扇出责任归属。TopicBroadcast 是 fanout 语义（每节点各消费一份），而下游
 * deliverGlobally / deliverToNamespace 会「跨节点派发 + 本地投递」双路执行——
 * 于是 N 个节点各以「源节点」身份派发给其余 N−1 个节点：
 *   - 跨节点调用 N×(N−1) 次（10 节点 = 90 次，20 节点 = 380 次）
 *   - 每个连接收到 N 份重复（本地去重窗按节点维护，拦不住跨节点重复）
 *
 * 内存适配器掩盖了该缺陷（单进程只有一个「节点」），因此必须由本测试锁定语义：
 * broadcast 消费路径必须携带 fanout 来源标记，deliver 路径必须不带。
 */

package broker

import (
	"context"
	"testing"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleBroadcast_MarksFanoutSourced broadcast 消费必须标记 fanout 来源
//
// 该标记由 messaging.Manager.submitClusterDispatch 消费，用于跳过跨节点派发。
func TestHandleBroadcast_MarksFanoutSourced(t *testing.T) {
	var gotCtx context.Context
	deliver := func(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
		gotCtx = ctx
		return &models.DeliverResult{}
	}
	c := NewConsumer(deliver, 0, 0, nil)

	env := newContractEnvelope("fanout-1", "u-1")
	require.NoError(t, c.HandleBroadcast()(context.Background(), env))

	require.NotNil(t, gotCtx, "deliver 未被调用")
	assert.True(t, routing.IsFanoutSourced(gotCtx),
		"broadcast 消费路径必须携带 fanout 来源标记，否则各节点重复跨节点派发（N×(N−1) 放大）")
}

// TestHandleDeliver_NotFanoutSourced deliver 消费必须不带 fanout 标记
//
// TopicDeliver 是 queue 语义（仅单节点消费），该节点必须作为源节点正常跨节点派发，
// 误打标记会导致消息只投本地、其余节点的目标连接收不到——静默丢消息。
func TestHandleDeliver_NotFanoutSourced(t *testing.T) {
	var gotCtx context.Context
	deliver := func(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult {
		gotCtx = ctx
		return &models.DeliverResult{}
	}
	c := NewConsumer(deliver, 0, 0, nil)

	env := newContractEnvelope("deliver-1", "u-2")
	require.NoError(t, c.HandleDeliver()(context.Background(), env))

	require.NotNil(t, gotCtx, "deliver 未被调用")
	assert.False(t, routing.IsFanoutSourced(gotCtx),
		"deliver 路径不得带 fanout 标记，否则跨节点派发被跳过、异节点连接静默丢消息")
}

// TestFanoutSourced_SurvivesRouteRestore 标记必须穿透路由恢复
//
// handle 内部会调 RestoreRouteCtx 重建路由信封，若其以 context.Background() 另起
// 新链，fanout 标记会在抵达 submitClusterDispatch 前丢失，守卫形同虚设。
func TestFanoutSourced_SurvivesRouteRestore(t *testing.T) {
	base := routing.WithFanoutSourced(context.Background())
	restored := RestoreRouteCtx(base, &models.HubMessage{
		AppID:     "app1",
		Namespace: "ns1",
	})

	assert.True(t, routing.IsFanoutSourced(restored),
		"RestoreRouteCtx 必须保留父 ctx 的 fanout 标记")
}

// TestIsFanoutSourced_Defaults 未标记与 nil ctx 均判定为非 fanout 来源
func TestIsFanoutSourced_Defaults(t *testing.T) {
	assert.False(t, routing.IsFanoutSourced(context.Background()), "未标记应为 false")
	assert.False(t, routing.IsFanoutSourced(nil), "nil ctx 不得 panic，应为 false")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 09:41:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 09:41:00
 * @FilePath: \go-wsc\hub\callbacks_test.go
 * @Description: 应用层回调收敛与心跳回调链测试
 *
 * 覆盖两条主线：
 * - With 与 Set 系列注入写入 h.callbacks 集合，群组生命周期回调经
 *   group.Host 端口 getter 可见
 * - 心跳回调链时序：Before（拦截跳过）→ Report → After，
 *   其中 Report 为拆域迁移后重新接线的回调
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"

	"github.com/kamalyes/go-wsc/models"
)

// newCallbacksTestHub 构造最小配置 Hub（ClientTimeout 给足量，避免时间轮任务
// 在测试期间触发超时注销干扰回调断言）
func newCallbacksTestHub() *Hub {
	return NewHub(&wscconfig.WSC{ClientTimeout: time.Minute})
}

// TestHeartbeatCallbackChainOrder 验证心跳回调链按 Before → Report → After 顺序触发
func TestHeartbeatCallbackChainOrder(t *testing.T) {
	hub := newCallbacksTestHub()

	var order []string
	hub.WithBeforeHeartbeatCallback(func(*models.Client) bool {
		order = append(order, "before")
		return true
	})
	hub.WithHeartbeatReportCallback(func(*models.Client) {
		order = append(order, "report")
	})
	hub.WithAfterHeartbeatCallback(func(*models.Client) {
		order = append(order, "after")
	})

	hub.HandleHeartbeat(&models.Client{ID: "c-1", UserID: "u-1"})

	assert.Equal(t, []string{"before", "report", "after"}, order)
}

// TestBeforeHeartbeatSkipsSubsequent 验证前置回调返回 false 时跳过
// 上报与后置回调（拦截语义）
func TestBeforeHeartbeatSkipsSubsequent(t *testing.T) {
	hub := newCallbacksTestHub()

	var reportCalled, afterCalled bool
	hub.SetBeforeHeartbeatCallback(func(*models.Client) bool { return false })
	hub.SetHeartbeatReportCallback(func(*models.Client) { reportCalled = true })
	hub.SetAfterHeartbeatCallback(func(*models.Client) { afterCalled = true })

	hub.HandleHeartbeat(&models.Client{ID: "c-2", UserID: "u-2"})

	assert.False(t, reportCalled)
	assert.False(t, afterCalled)
}

// TestGroupCallbacksInjectionAndPorts 验证群组生命周期回调注入后经
// group.Host 端口 getter 可见（nil 安全：未注入返回 nil）
func TestGroupCallbacksInjectionAndPorts(t *testing.T) {
	hub := newCallbacksTestHub()

	// 未注入：端口返回 nil
	assert.Nil(t, hub.GetGroupDisbandCallback())
	assert.Nil(t, hub.GetGroupMemberJoinCallback())
	assert.Nil(t, hub.GetGroupMemberLeaveCallback())

	var joinCtx context.Context
	hub.WithGroupDisbandCallback(func(ctx context.Context, ns, gid string) {})
	hub.WithGroupMemberJoinCallback(func(ctx context.Context, ns, gid string, uids []string) {
		joinCtx = ctx
	})
	hub.WithGroupMemberLeaveCallback(func(ctx context.Context, ns, gid string, uids []string) {})

	assert.NotNil(t, hub.GetGroupDisbandCallback())
	requireJoin := hub.GetGroupMemberJoinCallback()
	assert.NotNil(t, requireJoin)
	requireJoin(context.Background(), "ns-1", "g-1", []string{"u-9"})
	assert.NotNil(t, joinCtx)
	assert.NotNil(t, hub.GetGroupMemberLeaveCallback())
}

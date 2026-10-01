/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-01 09:05:08
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-01 09:05:08
 * @FilePath: \go-wsc\overload\admission_regression_test.go
 * @Description: 准入 AIMD 回归测试与基准（水位升降/积压计数/写批折扣/准入门热路径基准）
 */

package overload

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/require"
)

func TestAdmissionPendingBacklogAndRecovery(t *testing.T) {
	var pending atomic.Int64
	g := NewAdmissionGate(8, 2, time.Millisecond)
	g.SetPendingCounter(&pending)
	g.Start()
	defer g.Stop()
	pending.Store(9)
	require.Eventually(t, func() bool { return g.Level() == LevelReadOnly }, time.Second, time.Millisecond)
	pending.Store(0)
	require.Eventually(t, func() bool { return g.Level() == LevelNormal }, time.Second, time.Millisecond)
	// 已写出的历史消息和旧路径的局部计数不能抬高实际积压
	g.OnWriteBatch(100000)
	require.Zero(t, g.Backlog())
	replacement := NewAdmissionGate(8, 2, time.Millisecond)
	replacement.SetPendingCounter(&pending)
	pending.Store(7)
	require.EqualValues(t, 7, replacement.Backlog())
}

func TestAdmissionUnboundCounterAlwaysDrains(t *testing.T) {
	// 未绑定积压计数：安全退化——水位评估恒低于低水位不升载，Admit 恒放行，
	// delivered/written 仅作为 Stats 观测口径不参与决策（delivered-written 累计差在断连放弃帧场景会单调漂移，已从 Backlog 决策中移除）
	g := NewAdmissionGate(0, 0, 0)
	g.OnDelivered()
	g.OnWriteBatch(1)
	require.Zero(t, g.Backlog())
	require.Equal(t, LevelNormal, g.Level())
	require.Equal(t, VerdictAdmit, g.Admit(models.NewHubMessage(), false))
}

// benchAdmitSink 基准结果汇（包级 atomic：防死码消除，并行写入也不引入伪 data race）
var benchAdmitSink atomic.Int32

// BenchmarkAdmissionGate 单 goroutine 允许路径（L0 正常态热路径）
//
// 形态说明：未 Start（水位恒 L0）+ 普通级消息 → ResolveGuarantee 决策树走查 +
// L0 放行返回，即生产无过载时的每消息准入成本（零分配裁决）
func BenchmarkAdmissionGate(b *testing.B) {
	g := NewAdmissionGate(0, 0, 0)
	msg := models.NewHubMessage()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchAdmitSink.Store(int32(g.Admit(msg, false)))
	}
}

// BenchmarkAdmissionGateParallel 并行争抢（多写泵并发准入裁决）
//
// 形态说明：L0 恒定无水位写争抢（水位变化是低频事件），争抢点在 msg 读锁的
// readerCount 原子累加与 level 原子读的缓存行共享，与生产常态一致
func BenchmarkAdmissionGateParallel(b *testing.B) {
	g := NewAdmissionGate(0, 0, 0)
	msg := models.NewHubMessage()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			benchAdmitSink.Store(int32(g.Admit(msg, false)))
		}
	})
}

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

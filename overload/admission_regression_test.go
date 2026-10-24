package overload

import (
	"sync/atomic"
	"testing"
	"time"

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

func TestAdmissionStandaloneEnqueuedMinusSettled(t *testing.T) {
	g := NewAdmissionGate(0, 0, 0)
	g.OnDelivered()
	require.EqualValues(t, 1, g.Backlog())
	g.OnWriteBatch(1)
	require.Zero(t, g.Backlog())
}

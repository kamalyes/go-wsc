package messaging

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/require"
)

func TestP2PRefusalReportsFailureOrConfirmedOfflineStorage(t *testing.T) {
	for _, store := range []bool{false, true} {
		t.Run(map[bool]string{false: "no storage", true: "stored"}[store], func(t *testing.T) {
			m, h := newTestManager()
			h.verdict = overload.VerdictOffline
			h.config.RetryPolicy.MaxRetries = 0
			c := makeTestClient("c", "u")
			h.registry.AddClient(c)
			if store {
				q, _ := newOfflineRecordingHandler()
				m.offlineHandler = q
			}
			msg := makeGroupMessage("system")
			msg.MessageType = models.MessageTypeNotice
			result := m.SendToUserWithRetry(context.Background(), "u", msg)
			require.Equal(t, store, result.Success)
			require.Equal(t, store, result.StoredOffline)
			require.Empty(t, c.SendChan)
			if !store {
				require.Error(t, result.FinalError)
			}
		})
	}
}

type rejectingOfflineQueue struct {
	spi.OfflineQueue
	err error
}

func (q rejectingOfflineQueue) StoreOfflineMessage(context.Context, string, *models.HubMessage) error {
	return q.err
}

func TestP2PFailedOrSkippedStorageNeverReportsQueued(t *testing.T) {
	for _, err := range []error{errors.New("database down"), spi.ErrOfflineStorageSkipped} {
		m, h := newTestManager()
		h.verdict = overload.VerdictOffline
		h.config.RetryPolicy.MaxRetries = 0
		h.registry.AddClient(makeTestClient("c", "u"))
		m.offlineHandler = rejectingOfflineQueue{err: err}
		result := m.SendToUserWithRetry(context.Background(), "u", makeGroupMessage("system"))
		require.False(t, result.Success)
		require.False(t, result.StoredOffline)
		require.ErrorIs(t, result.FinalError, err)
	}
}

func TestNoticeOfflineReplayAndExplicitSkip(t *testing.T) {
	q, log := newOfflineRecordingHandler()
	ctx := context.Background()
	notice := models.NewHubMessage().SetMessageType(models.MessageTypeNotice).SetMessageID("notice")
	require.NoError(t, q.StoreOfflineMessage(ctx, "u", notice))
	require.Equal(t, 1, log.getStoreCalled())
	ping := models.NewHubMessage().SetMessageType(models.MessageTypePing)
	require.ErrorIs(t, q.StoreOfflineMessage(ctx, "u", ping), spi.ErrOfflineStorageSkipped)
	require.Equal(t, 1, log.getStoreCalled())
}

func TestBroadcastWritesDoNotLeavePhantomBacklog(t *testing.T) {
	m, h := newTestManager()
	var pending atomic.Int64
	c := makeTestClient("c", "u")
	c.SetPendingCounter(&pending)
	h.registry.AddClient(c)
	msg := makeGroupMessage("s")
	for i := 0; i < 9; i++ {
		require.Equal(t, 1, m.BroadcastToFiltered(context.Background(), func(*models.Client) bool { return true }, msg))
		<-c.SendChan
		c.CompletePendingWrites(1)
	}
	require.Zero(t, pending.Load())
}

func TestDataWriteSettlesPendingFrames(t *testing.T) {
	m, _ := newTestManager()
	server, peer := newTestWSPair(t)
	c := makeTestClient("c", "u")
	c.Conn = server
	var pending atomic.Int64
	c.SetPendingCounter(&pending)
	for i := 0; i < 3; i++ {
		require.True(t, c.TrySend([]byte("frame")))
	}
	require.EqualValues(t, 3, pending.Load())
	require.NoError(t, m.writeClientMessagesBatch(c, <-c.SendChan))
	require.Zero(t, pending.Load())
	peer.SetReadDeadline(time.Now().Add(time.Second))
	for i := 0; i < 3; i++ {
		_, _, err := peer.ReadMessage()
		require.NoError(t, err)
	}
	require.True(t, c.TrySend([]byte("fails")))
	require.NoError(t, server.Close())
	require.Error(t, m.writeClientMessagesBatch(c, <-c.SendChan))
	require.Zero(t, pending.Load())
}

func TestLocalPartialDeviceDeliveryCountsOnlyAccepted(t *testing.T) {
	m, h := newTestManager()
	a, b := makeTestClient("full", "u"), makeTestClient("ready", "u")
	for i := 0; i < cap(a.SendChan); i++ {
		a.SendChan <- []byte("full")
	}
	h.registry.AddClient(a)
	h.registry.AddClient(b)
	msg := makeGroupMessage("s")
	msg.Receiver = "u"
	msg.AppID = a.AppID
	msg.Namespace = a.Namespace
	require.Equal(t, 1, m.handleDirectMessage(context.Background(), msg, nil))
	require.Len(t, b.SendChan, 1)
}

// TestHeldDeliveryRefusalFallsBackOffline 验证回放期间暂存的消息补投失败仍可恢复
func TestHeldDeliveryRefusalFallsBackOffline(t *testing.T) {
	m, h := newTestManager()
	m.WithReplayGate(NewReplayGate())
	h.registry.AddClient(makeTestClient("c", "u"))
	q, log := newOfflineRecordingHandler()
	m.offlineHandler = q
	m.BeginUserReplay("u")
	msg := makeGroupMessage("system")
	msg.MessageType = models.MessageTypeNotice
	result := m.SendToUserWithRetry(context.Background(), "u", msg)
	require.True(t, result.Success)
	require.Zero(t, log.getStoreCalled())
	h.verdict = overload.VerdictOffline
	m.EndUserReplay("u")
	require.Eventually(t, func() bool { return log.getStoreCalled() == 1 }, time.Second, time.Millisecond)
}

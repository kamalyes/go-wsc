/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 02:56:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\models\\client_pending_test.go
 * @Description: Client 待写计数的增减配平测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPendingFramesSettleOnWriteFailureAndDisconnect(t *testing.T) {
	var pending atomic.Int64
	c := NewClient("c", "u", UserTypeCustomer).WithSendChan(make(chan []byte, 2))
	c.SetPendingCounter(&pending)
	require.True(t, c.TrySend([]byte("a")))
	require.True(t, c.TrySend([]byte("b")))
	require.False(t, c.TrySend([]byte("full")))
	require.EqualValues(t, 2, pending.Load())
	<-c.SendChan
	c.CompletePendingWrites(1)
	require.EqualValues(t, 1, pending.Load())
	c.MarkClosed()
	require.Zero(t, pending.Load())
	c.CompletePendingWrites(10)
	require.Zero(t, pending.Load())
	require.False(t, c.TrySend([]byte("closed")))
	require.Zero(t, pending.Load())
}

func TestPendingFramesConcurrentTeardown(t *testing.T) {
	for n := 0; n < 50; n++ {
		var pending atomic.Int64
		c := NewClient("c", "u", UserTypeCustomer).WithSendChan(make(chan []byte, 32))
		c.SetPendingCounter(&pending)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 100; j++ {
					c.TrySend([]byte("x"))
					c.CompletePendingWrites(1)
				}
			}()
		}
		c.MarkClosed()
		wg.Wait()
		require.Zero(t, pending.Load())
	}
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 00:00:00
 * @FilePath: \go-wsc\hub\observer_batcher_test.go
 * @Description: 观察者通知批量处理器独立单元测试
 *   - NewObserverNotificationBatcher 创建
 *   - Submit 提交（nil msg 返回 false、nil batcher 返回 false）
 *   - flush 调用 notifyObserversDirect（通过注册观察者捕获调用参数）
 *   - Stop 安全停止
 *   - DroppedCount 计数
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// newObserverTestHub 创建简易测试 Hub（不含 miniredis，仅用于 batcher 单元测试）
func newObserverTestHub(t *testing.T) (*Hub, func()) {
	t.Helper()
	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18080).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)

	hub := NewHub(config)
	cleanup := func() {
		hub.Shutdown()
	}
	return hub, cleanup
}

// ============================================================================
// NewObserverNotificationBatcher 创建测试
// ============================================================================

// TestNewObserverNotificationBatcher 验证创建后的字段正确
func TestNewObserverNotificationBatcher(t *testing.T) {
	hub, cleanup := newObserverTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 64, 8, 50*time.Millisecond)

	require.NotNil(t, batcher, "创建的 batcher 不应为 nil")
	assert.Same(t, hub, batcher.hub, "batcher.hub 应指向传入的 hub")
	assert.NotNil(t, batcher.processor, "batcher.processor 不应为 nil")
}

// ============================================================================
// Submit 边界测试
// ============================================================================

// TestObserverBatcherUnit_SubmitNilMsg 验证 Submit(nil msg) 返回 false
func TestObserverBatcherUnit_SubmitNilMsg(t *testing.T) {
	hub, cleanup := newObserverTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 64, 8, 50*time.Millisecond)

	ok := batcher.Submit(nil, "ns-1", []string{"g-1"})
	assert.False(t, ok, "Submit(nil msg) 应返回 false")
}

// TestObserverBatcherUnit_SubmitNilBatcher 验证 nil batcher.Submit(...) 返回 false
func TestObserverBatcherUnit_SubmitNilBatcher(t *testing.T) {
	var b *ObserverNotificationBatcher

	msg := makeGroupMessage("sender-001")
	ok := b.Submit(msg, "ns-1", []string{"g-1"})
	assert.False(t, ok, "nil batcher.Submit 应返回 false")
}

// TestObserverBatcherUnit_SubmitValidMsg 验证 Submit 正常 msg 返回 true
func TestObserverBatcherUnit_SubmitValidMsg(t *testing.T) {
	hub, cleanup := newObserverTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 64, 8, 10*time.Second)

	msg := makeGroupMessage("sender-valid")
	msg.Receiver = "user-1"
	ok := batcher.Submit(msg, "ns-valid", []string{"g-valid"})
	assert.True(t, ok, "Submit 正常消息应返回 true")
}

// ============================================================================
// flush 调用 notifyObserversDirect 测试
// ============================================================================

// TestObserverBatcherUnit_FlushEmpty 验证 flush 空切片不 panic 且不触发通知
func TestObserverBatcherUnit_FlushEmpty(t *testing.T) {
	hub, cleanup := newObserverTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 64, 8, 10*time.Second)

	assert.NotPanics(t, func() {
		batcher.flush(nil)
		batcher.flush([]*observerNotifyItem{})
	}, "flush 空切片不应 panic")
}

// TestObserverBatcherUnit_FlushCallsNotifyDirect 验证 flush 会调用 notifyObserversDirect，
// 通过注册观察者并从其 SendChan 接收消息，来捕获 msg.Sender / msg.Content / namespace / groupIDs 参数。
// notifyObserversDirect 会为消息注入 observer_mode=true 元数据，可用于验证调用链路。
func TestObserverBatcherUnit_FlushCallsNotifyDirect(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	observer := makeTestClient("c-obs-capture", "u-obs-capture")
	observer.UserType = UserTypeObserver
	observer.Namespace = "ns-capture"
	hub.shardedRegistry.AddClient(observer)

	batcher := NewObserverNotificationBatcher(hub, 100, 100, 10*time.Second)

	msg := makeGroupMessage("sender-direct")
	msg.Sender = "expected-sender-001"
	msg.Content = "expected-content-002"

	namespace := "ns-capture"
	groupIDs := []string{"g-cap-1", "g-cap-2"}

	items := []*observerNotifyItem{
		{msg: msg, namespace: namespace, groupIDs: groupIDs},
	}
	batcher.flush(items)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case data := <-observer.SendChan:
			var received HubMessage
			require.NoError(t, json.Unmarshal(data, &received))
			assert.Equal(t, "expected-sender-001", received.Sender,
				"notifyObserversDirect 应正确传递 msg.Sender 参数")
			assert.Equal(t, "expected-content-002", received.Content,
				"notifyObserversDirect 应正确传递 msg.Content 参数")
			val, ok := received.GetMetadata("observer_mode")
			assert.True(t, ok, "notifyObserversDirect 应注入 observer_mode 元数据")
			assert.Equal(t, "true", val, "observer_mode 元数据应为 true")
			origSender, _ := received.GetMetadata("original_sender")
			assert.Equal(t, "expected-sender-001", origSender,
				"notifyObserversDirect 应注入 original_sender 元数据")
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatal("flush 应调用 notifyObserversDirect 并使观察者收到消息")
}

// TestObserverBatcherUnit_FlushMultipleItems 验证 flush 多个 item 会逐项调用 notifyObserversDirect
func TestObserverBatcherUnit_FlushMultipleItems(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	observer := makeTestClient("c-obs-multi", "u-obs-multi")
	observer.UserType = UserTypeObserver
	observer.Namespace = "ns-multi"
	hub.shardedRegistry.AddClient(observer)

	batcher := NewObserverNotificationBatcher(hub, 100, 100, 10*time.Second)

	items := []*observerNotifyItem{
		{
			msg: func() *HubMessage {
				m := makeGroupMessage("s1")
				m.Content = "msg-1"
				return m
			}(),
			namespace: "ns-multi",
			groupIDs:  nil,
		},
		{
			msg: func() *HubMessage {
				m := makeGroupMessage("s2")
				m.Content = "msg-2"
				return m
			}(),
			namespace: "ns-multi",
			groupIDs:  nil,
		},
	}
	batcher.flush(items)

	receivedContents := make(map[string]bool)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(receivedContents) < 2 {
		select {
		case data := <-observer.SendChan:
			var m HubMessage
			if err := json.Unmarshal(data, &m); err == nil {
				receivedContents[m.Content] = true
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	assert.True(t, receivedContents["msg-1"], "第一个 item 应触发 notifyObserversDirect")
	assert.True(t, receivedContents["msg-2"], "第二个 item 应触发 notifyObserversDirect")
}

// ============================================================================
// Stop 安全停止测试
// ============================================================================

// TestObserverBatcherUnit_StopSafe 验证 Stop 能在合理时间完成且不 panic
func TestObserverBatcherUnit_StopSafe(t *testing.T) {
	hub, cleanup := newObserverTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 100, 100, 10*time.Second)

	msg := makeGroupMessage("sender-stop")
	for i := 0; i < 8; i++ {
		batcher.Submit(msg, "ns-stop", []string{"g-stop"})
	}

	done := make(chan struct{})
	go func() {
		batcher.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 应在 3s 内完成（flush 剩余数据后退出）")
	}
}

// TestObserverBatcherUnit_NilBatcherStopSafe 验证 nil batcher.Stop 不 panic
func TestObserverBatcherUnit_NilBatcherStopSafe(t *testing.T) {
	var b *ObserverNotificationBatcher
	assert.NotPanics(t, func() {
		b.Stop()
	}, "nil batcher.Stop 不应 panic")
}

// TestObserverBatcherUnit_MultipleStopSafe 验证多次调用 Stop 不 panic
func TestObserverBatcherUnit_MultipleStopSafe(t *testing.T) {
	hub, cleanup := newObserverTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 10, 10, 10*time.Second)
	assert.NotPanics(t, func() {
		batcher.Stop()
		batcher.Stop()
		batcher.Stop()
	}, "多次调用 Stop 不应 panic")
}

// ============================================================================
// DroppedCount 测试
// ============================================================================

// TestObserverBatcherUnit_DroppedCountInit 验证新 batcher DroppedCount 为 0
func TestObserverBatcherUnit_DroppedCountInit(t *testing.T) {
	hub, cleanup := newObserverTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 100, 100, 10*time.Second)
	assert.Equal(t, int64(0), batcher.DroppedCount(),
		"新建 batcher DroppedCount 应为 0")
}

// TestObserverBatcherUnit_NilBatcherDroppedCountZero 验证 nil batcher DroppedCount 返回 0
func TestObserverBatcherUnit_NilBatcherDroppedCountZero(t *testing.T) {
	var b *ObserverNotificationBatcher
	assert.Equal(t, int64(0), b.DroppedCount(),
		"nil batcher DroppedCount 应返回 0")
}

// ============================================================================
// Submit + 自动 flush 集成测试（短间隔验证端到端）
// ============================================================================

// TestObserverBatcherUnit_SubmitAutoFlush 验证 Submit 后按 flushInterval 自动触发 notify
func TestObserverBatcherUnit_SubmitAutoFlush(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	observer := makeTestClient("c-obs-auto", "u-obs-auto")
	observer.UserType = UserTypeObserver
	observer.Namespace = "ns-auto"
	hub.shardedRegistry.AddClient(observer)

	batcher := NewObserverNotificationBatcher(hub, 100, 10, 50*time.Millisecond)

	msg := makeGroupMessage("sender-auto")
	msg.Sender = "auto-sender"
	msg.Content = "auto-content"
	ok := batcher.Submit(msg, "ns-auto", nil)
	require.True(t, ok)

	require.Eventually(t, func() bool {
		return len(observer.SendChan) > 0
	}, 2*time.Second, 20*time.Millisecond, "应在 flushInterval 后自动触发通知")

	select {
	case data := <-observer.SendChan:
		var received HubMessage
		require.NoError(t, json.Unmarshal(data, &received))
		assert.Equal(t, "auto-sender", received.Sender)
		assert.Equal(t, "auto-content", received.Content)
	default:
		t.Fatal("观察者应已收到消息")
	}
}

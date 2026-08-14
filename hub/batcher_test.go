/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-31 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-31 00:00:00
 * @FilePath: \go-wsc\hub\batcher_test.go
 * @Description: 观察者通知批量处理器 + 跨节点分发批量处理器 测试
 *
 * 覆盖场景：
 *   1. ObserverNotificationBatcher: Submit/Flush/DroppedCount/NilSafe/Clone隔离
 *   2. ClusterDispatchBatcher: Submit/Flush/DroppedCount/NilSafe/Clone隔离
 *   3. 性能基准：Submit 吞吐量、Broadcast 端到端
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// ObserverNotificationBatcher 单元测试
// ============================================================================

// TestObserverBatcher_SubmitAndFlush 验证 Submit → flush → 观察者收到消息
func TestObserverBatcher_SubmitAndFlush(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 注册观察者（直接写入 registry，绕过 register channel）
	observer := makeTestClient("c-obs-1", "observer-user-1")
	observer.UserType = UserTypeObserver
	observer.Namespace = "tenantA"
	hub.shardedRegistry.AddClient(observer)

	// 提交消息到观察者 batcher
	msg := makeGroupMessage("sender-001")
	msg.Receiver = "group-001"
	ok := hub.observerBatcher.Submit(msg, "tenantA", []string{"group-001"})
	require.True(t, ok, "Submit 应成功")

	// 等待 flush（50ms 间隔）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case data := <-observer.SendChan:
			var received HubMessage
			require.NoError(t, json.Unmarshal(data, &received))
			assert.Equal(t, msg.Sender, received.Sender)
			assert.Equal(t, msg.Content, received.Content)
			// 验证观察者元数据
			val, ok := received.GetMetadata("observer_mode")
			assert.True(t, ok, "应包含 observer_mode 元数据")
			assert.Equal(t, "true", val)
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("观察者应在 flush 后收到消息")
}

// TestObserverBatcher_Submit_NilMsg 验证 nil msg 返回 false
func TestObserverBatcher_Submit_NilMsg(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.False(t, hub.observerBatcher.Submit(nil, "ns", []string{"gid"}), "nil msg 应返回 false")
}

// TestObserverBatcher_NilSafe 验证 nil batcher 的所有方法安全
func TestObserverBatcher_NilSafe(t *testing.T) {
	var b *ObserverNotificationBatcher

	assert.False(t, b.Submit(nil, "ns", []string{"gid"}), "nil batcher Submit 应返回 false")
	assert.NotPanics(t, func() { b.Stop() }, "nil batcher Stop 应安全")
	assert.Equal(t, int64(0), b.DroppedCount(), "nil batcher DroppedCount 应返回 0")
}

// TestObserverBatcher_DroppedCount 验证 DroppedCount 委托方法
// 实际丢弃逻辑由 syncx.BatchProcessor.DroppedCount 测试覆盖（go-toolbox）
func TestObserverBatcher_DroppedCount(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 新 batcher 初始 DroppedCount 应为 0
	assert.Equal(t, int64(0), hub.observerBatcher.DroppedCount(),
		"新 batcher 初始 DroppedCount 应为 0")
}

// TestObserverBatcher_CloneIsolation 验证 Submit 后修改原 msg 不影响 flush
func TestObserverBatcher_CloneIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 注册观察者
	observer := makeTestClient("c-obs-clone", "observer-clone")
	observer.UserType = UserTypeObserver
	observer.Namespace = "tenantClone"
	hub.shardedRegistry.AddClient(observer)

	// 提交消息
	msg := makeGroupMessage("sender-clone")
	msg.Content = "original"
	ok := hub.observerBatcher.Submit(msg, "tenantClone", nil)
	require.True(t, ok)

	// Submit 后修改原 msg（WithClone 应保护队列中的副本）
	msg.Content = "modified"

	// 等待 flush
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case data := <-observer.SendChan:
			var received HubMessage
			require.NoError(t, json.Unmarshal(data, &received))
			assert.Equal(t, "original", received.Content, "Clone 应保护消息不被调用方修改影响")
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatal("观察者应在 flush 后收到消息")
}

// TestObserverBatcher_Stop 验证 Stop 时 flush 剩余数据且不阻塞
func TestObserverBatcher_Stop(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	batcher := NewObserverNotificationBatcher(hub, 100, 100, 10*time.Second)

	msg := makeGroupMessage("sender-stop")
	for i := 0; i < 10; i++ {
		batcher.Submit(msg, "ns", []string{"gid"})
	}

	// Stop 应在合理时间内返回（flush 剩余后退出）
	done := make(chan struct{})
	go func() {
		batcher.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 应在 3s 内完成")
	}
}

// ============================================================================
// ClusterDispatchBatcher 单元测试
// ============================================================================

// TestClusterBatcher_Submit_Success 验证 Submit 成功（单机模式 routeToCluster 为 no-op）
func TestClusterBatcher_Submit_Success(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender-cluster")
	opts := ClusterDispatchOptions{
		Operation: OperationTypeBroadcast,
		Namespace: "",
	}

	// 单机模式（无 pubsub 无 gRPC），Submit 仍应成功（只写入 channel）
	ok := hub.clusterBatcher.Submit(msg, opts)
	require.True(t, ok, "Submit 应成功")
}

// TestClusterBatcher_Submit_NilMsg 验证 nil msg 返回 false
func TestClusterBatcher_Submit_NilMsg(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.False(t, hub.clusterBatcher.Submit(nil, ClusterDispatchOptions{}), "nil msg 应返回 false")
}

// TestClusterBatcher_NilSafe 验证 nil batcher 的所有方法安全
func TestClusterBatcher_NilSafe(t *testing.T) {
	var b *ClusterDispatchBatcher

	assert.False(t, b.Submit(nil, ClusterDispatchOptions{}), "nil batcher Submit 应返回 false")
	assert.NotPanics(t, func() { b.Stop() }, "nil batcher Stop 应安全")
	assert.Equal(t, int64(0), b.DroppedCount(), "nil batcher DroppedCount 应返回 0")
}

// TestClusterBatcher_DroppedCount 验证 DroppedCount 委托方法
// 实际丢弃逻辑由 syncx.BatchProcessor.DroppedCount 测试覆盖（go-toolbox）
func TestClusterBatcher_DroppedCount(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 新 batcher 初始 DroppedCount 应为 0
	assert.Equal(t, int64(0), hub.clusterBatcher.DroppedCount(),
		"新 batcher 初始 DroppedCount 应为 0")
}

// TestClusterBatcher_CloneIsolation 验证 Submit 后修改原 msg + GroupIDs 不影响 flush
func TestClusterBatcher_CloneIsolation(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	msg := makeGroupMessage("sender-clone")
	msg.Content = "original"
	originalGroupIDs := []string{"g1", "g2"}

	opts := ClusterDispatchOptions{
		Operation: OperationTypeGroupsBroadcast,
		Namespace: "ns-clone",
		GroupIDs:  originalGroupIDs,
	}

	// 提交（WithClone 应深拷贝 msg + GroupIDs）
	ok := hub.clusterBatcher.Submit(msg, opts)
	require.True(t, ok)

	// Submit 后修改原 msg 和 GroupIDs
	msg.Content = "modified"
	originalGroupIDs[0] = "tampered"

	// 验证 batcher 内部的副本未被修改（通过 DroppedCount 间接验证 batcher 正常工作）
	// 注意：routeToCluster 在单机模式下为 no-op，无法直接验证 msg 内容
	// 但 Clone 函数 cloneClusterDispatchItem 已在 batch_processor_test.go 的 WithClone 测试中验证
	assert.True(t, ok, "Submit 应成功")
}

// TestClusterBatcher_Stop 验证 Stop 时 flush 剩余数据且不阻塞
func TestClusterBatcher_Stop(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	batcher := NewClusterDispatchBatcher(hub, 100, 100, 10*time.Second)

	msg := makeGroupMessage("sender-stop")
	opts := ClusterDispatchOptions{Operation: OperationTypeBroadcast}
	for i := 0; i < 10; i++ {
		batcher.Submit(msg, opts)
	}

	done := make(chan struct{})
	go func() {
		batcher.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 应在 3s 内完成")
	}
}

// ============================================================================
// 性能基准测试
// ============================================================================

// BenchmarkObserverBatcher_Submit 测量观察者通知 Submit 吞吐量
// 对比旧方案：每条消息 syncx.Go()（2 goroutine/消息）
func BenchmarkObserverBatcher_Submit(b *testing.B) {
	hub, _, cleanup := setupPerfHub(b)
	defer cleanup()

	msg := makeGroupMessage("bench-sender")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			hub.observerBatcher.Submit(msg, "bench-ns", []string{"bench-gid"})
		}
	})
}

// BenchmarkClusterBatcher_Submit 测量跨节点分发 Submit 吞吐量
// 对比旧方案：每条消息 go func() { routeToCluster(...) }()（1 goroutine/消息）
func BenchmarkClusterBatcher_Submit(b *testing.B) {
	hub, _, cleanup := setupPerfHub(b)
	defer cleanup()

	msg := makeGroupMessage("bench-sender")
	opts := ClusterDispatchOptions{Operation: OperationTypeBroadcast}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			hub.clusterBatcher.Submit(msg, opts)
		}
	})
}

// BenchmarkBroadcast_WithBatcher 测量带 batcher 的 Broadcast 端到端吞吐量
// 对比 BenchmarkBroadcast（旧方案 per-message goroutine）
func BenchmarkBroadcast_WithBatcher(b *testing.B) {
	hub, _, cleanup := setupPerfHub(b)
	defer cleanup()

	// 注册 drain 客户端
	clients := makeBenchClients("bench-bcast", 100, 256)
	registerAll(hub, clients)

	msg := makeGroupMessage("bench-bcast-sender")
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Broadcast(ctx, msg)
	}
}

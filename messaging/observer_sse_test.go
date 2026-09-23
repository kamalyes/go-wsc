/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:28:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 19:28:00
 * @FilePath: \go-wsc\messaging\observer_sse_test.go
 * @Description: 消息域观察者投递与 SSE 投递测试 - 攒批降级 / 本地直投 / 信封隔离

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

// newObserverEnabledHost 构造启用观察者索引的 Host 替身（registry 可换成含观察者的实例）
func newObserverEnabledHost(t *testing.T) *fakeHost {
	t.Helper()
	host := newFakeHost()
	host.registry = connection.NewShardedRegistry(false, true, connection.RegistryCapacity{})
	return host
}

// newObserverClient 构造全局观察者客户端（Namespace 空 = 观察所有命名空间）
func newObserverClient(id, groupID string) *models.Client {
	observer := models.NewClient(id, "u-"+id, models.UserTypeObserver)
	observer.Namespace = "" // 全局观察（空命名空间入 global 索引）
	observer.SendChan = make(chan []byte, 1)
	if groupID != "" {
		observer.SetGroupID(groupID)
	}
	return observer
}

// newSSEClient 构造 SSE 订阅客户端
func newSSEClient(id, userID, namespace string) *models.Client {
	client := models.NewClient(id, userID, models.UserTypeCustomer)
	client.ConnectionType = models.ConnectionTypeSSE
	client.Namespace = namespace
	client.SSEMessageCh = make(chan *models.HubMessage, 1)
	return client
}

// TestObserverNotifyWithoutNotifier 攒批入口降级：未注入观察者批量处理器时 no-op
func TestObserverNotifyWithoutNotifier(t *testing.T) {
	host := newFakeHost() // GetObserverNotifier 返回 nil
	m := NewManager(host)

	msg := models.NewHubMessage().SetMessageType(models.MessageTypeText)
	require.NotPanics(t, func() {
		m.NotifyObservers(context.Background(), msg)
	})
}

// TestObserverDirectDeliversToGlobalObserver 本地直投：全局观察者收到
// 预序列化消息（含 observer_mode / original_sender 元数据标记）
func TestObserverDirectDeliversToGlobalObserver(t *testing.T) {
	host := newObserverEnabledHost(t)
	m := NewManager(host)

	observer := newObserverClient("obs-1", "")
	host.registry.AddClient(observer)

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetSender("u-sender").
		SetReceiver("u-receiver")
	msg.MessageID = "obs-msg-1"

	m.NotifyObserversDirect(msg, "tenantA", nil)

	select {
	case data := <-observer.SendChan:
		assert.NotEmpty(t, data, "观察者应收到序列化消息")
		var payload map[string]any
		require.NoError(t, json.Unmarshal(data, &payload))
		assert.Equal(t, "obs-msg-1", payload["message_id"])
		// metadata 经 WithMetadata 写入 Data[metadata]（观察者模式标记 + 原始收发双方）
		dataField, ok := payload["data"].(map[string]any)
		require.True(t, ok, "观察者消息应携带 data 字段")
		metadata, ok := dataField["metadata"].(map[string]any)
		require.True(t, ok, "观察者消息应携带 metadata")
		assert.Equal(t, "true", metadata["observer_mode"])
		assert.Equal(t, "u-sender", metadata["original_sender"])
	case <-time.After(time.Second):
		t.Fatal("全局观察者应收到直投消息")
	}
}

// TestObserverDirectWithoutObserver 单机无观察者：直投快速路径不 panic（仅跨节点广播，单机 no-op）
func TestObserverDirectWithoutObserver(t *testing.T) {
	host := newObserverEnabledHost(t)
	m := NewManager(host)

	msg := models.NewHubMessage().SetMessageType(models.MessageTypeText)
	require.NotPanics(t, func() {
		m.NotifyObserversDirect(msg, "tenantA", nil)
	})
}

// TestSendToUserViaSSEDelivers SSE 点对点：用户订阅通道收到消息并返回 true
func TestSendToUserViaSSEDelivers(t *testing.T) {
	host := newObserverEnabledHost(t)
	m := NewManager(host)

	client := newSSEClient("sse-1", "u-sse", constants.DefaultNamespace)
	host.registry.AddClient(client)

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetReceiver("u-sse")
	msg.Namespace = constants.DefaultNamespace

	assert.True(t, m.SendToUserViaSSE("u-sse", msg))

	select {
	case received := <-client.SSEMessageCh:
		assert.Same(t, msg, received)
	case <-time.After(time.Second):
		t.Fatal("SSE 订阅通道应收到消息")
	}
}

// TestSendToUserViaSSENamespaceIsolated SSE 点对点命名空间隔离：
// msg.Namespace 与设备 ns 不匹配时不投递返回 false
func TestSendToUserViaSSENamespaceIsolated(t *testing.T) {
	host := newObserverEnabledHost(t)
	m := NewManager(host)

	client := newSSEClient("sse-2", "u-sse", "tenantA")
	host.registry.AddClient(client)

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetReceiver("u-sse")
	msg.Namespace = "tenantB"

	assert.False(t, m.SendToUserViaSSE("u-sse", msg))
	select {
	case <-client.SSEMessageCh:
		t.Fatal("跨命名空间消息不应投递给 SSE 设备")
	default:
	}
}

// TestBroadcastToSSEClientsEnvelopeFiltered SSE 广播信封隔离：仅 appID+ns 匹配的设备收到消息
func TestBroadcastToSSEClientsEnvelopeFiltered(t *testing.T) {
	host := newObserverEnabledHost(t)
	m := NewManager(host)

	matched := newSSEClient("sse-match", "u-1", constants.DefaultNamespace)
	mismatched := newSSEClient("sse-miss", "u-2", "tenantB")
	host.registry.AddClient(matched)
	host.registry.AddClient(mismatched)

	msg := models.NewHubMessage().SetMessageType(models.MessageTypeSystem)
	msg.AppID = constants.DefaultAppID
	msg.Namespace = constants.DefaultNamespace

	m.BroadcastToSSEClients(msg)

	select {
	case <-matched.SSEMessageCh:
	default:
		t.Fatal("信封匹配的 SSE 设备应收到广播")
	}
	select {
	case <-mismatched.SSEMessageCh:
		t.Fatal("信封不匹配的 SSE 设备不应收到广播")
	default:
	}
}

// TestSendToUserViaSSENoSubscription 用户无 SSE 订阅：快速路径返回 false
func TestSendToUserViaSSENoSubscription(t *testing.T) {
	host := newObserverEnabledHost(t)
	m := NewManager(host)

	msg := models.NewHubMessage().SetMessageType(models.MessageTypeText)
	assert.False(t, m.SendToUserViaSSE("u-not-subscribed", msg))
}

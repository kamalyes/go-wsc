/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-12 11:22:31
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 22:03:31
 * @FilePath: \go-wsc\hub\delivery_fallback_test.go
 * @Description: 投递兜底路由测试 —— TrySend 失败按分级路由（拒绝≠丢弃）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
)

// captureOfflineHandler 捕获型离线处理器 fake（验证转存行为）
type captureOfflineHandler struct {
	stored chan *models.HubMessage
}

func (c *captureOfflineHandler) StoreOfflineMessage(_ context.Context, _ string, msg *HubMessage) error {
	c.stored <- msg
	return nil
}
func (c *captureOfflineHandler) DrainOfflineQueue(_ context.Context, _ string, _ int) ([]*HubMessage, error) {
	return nil, nil
}
func (c *captureOfflineHandler) GetOfflineMessages(_ context.Context, _ string, _ int, _ string) ([]*HubMessage, string, error) {
	return nil, "", nil
}
func (c *captureOfflineHandler) DeleteOfflineMessages(_ context.Context, _ string, _ []string) error {
	return nil
}
func (c *captureOfflineHandler) GetOfflineMessageCount(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (c *captureOfflineHandler) ClearOfflineMessages(_ context.Context, _ string, _ []string) error {
	return nil
}
func (c *captureOfflineHandler) UpdatePushStatus(_ context.Context, _ []string, _ error) error {
	return nil
}

// newCaptureOfflineHandler 构造捕获型离线处理器
func newCaptureOfflineHandler() *captureOfflineHandler {
	return &captureOfflineHandler{stored: make(chan *models.HubMessage, 8)}
}

// newFallbackTestClient 构造投递兜底测试客户端（满容量 SendChan 模拟洪峰）
func newFallbackTestClient(id, userID string) *Client {
	client := &Client{
		ID:             id,
		UserID:         userID,
		UserType:       UserTypeCustomer,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket,
	}
	client.SendChan = make(chan []byte, 1)
	return client
}

// waitStored 等待异步转存完成（tryStoreOfflineOnDeliveryFailure 内部 syncx.Go）
func waitStored(t *testing.T, stored chan *models.HubMessage) *models.HubMessage {
	t.Helper()
	select {
	case msg := <-stored:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("等待异步转存离线超时")
		return nil
	}
}

// TestRouteDeliveryFallbackEphemeral 高频级：丢弃计数（latest-wins 语义正确行为）
func TestRouteDeliveryFallbackEphemeral(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	client := newFallbackTestClient("fb-eph", "u-eph")
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeTyping).
		WithGuarantee(models.GuaranteeEphemeral)

	action := hub.routeDeliveryFallback(msg, client, "")
	assert.Equal(t, FallbackEphemeralDrop, action, "高频级应语义丢弃")
	assert.Equal(t, int64(1), hub.overloadMetrics.ephemeralDropped.Load())
}

// TestRouteDeliveryFallbackStandard 普通级：转离线补发（修复静默丢失）
func TestRouteDeliveryFallbackStandard(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	// 配置离线处理器（捕获转存的消息）
	handler := newCaptureOfflineHandler()
	hub.SetOfflineMessageHandler(handler)

	client := newFallbackTestClient("fb-std", "u-std")
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard).
		SetReceiver("u-std")

	action := hub.routeDeliveryFallback(msg, client, "")
	assert.Equal(t, FallbackOffline, action, "普通级应转离线")

	offline := waitStored(t, handler.stored)
	assert.Equal(t, "u-std", offline.Receiver, "离线消息应补写接收者")
}

// TestRouteDeliveryFallbackGuaranteed 必达级：立即转离线（不等 ACK 超时兜底）
func TestRouteDeliveryFallbackGuaranteed(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	handler := newCaptureOfflineHandler()
	hub.SetOfflineMessageHandler(handler)

	client := newFallbackTestClient("fb-gua", "u-gua")
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypePayment).
		WithGuarantee(models.GuaranteeGuaranteed).
		SetReceiver("u-gua")

	action := hub.routeDeliveryFallback(msg, client, "")
	assert.Equal(t, FallbackOffline, action, "必达级应立即转离线")
	waitStored(t, handler.stored)
}

// TestRouteDeliveryFallbackNoReceiver 无接收者信息：unrecoverable 计数（部署防御）
func TestRouteDeliveryFallbackNoReceiver(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard)

	// 无 client 且无 targetUserID 且消息无 Receiver
	action := hub.routeDeliveryFallback(msg, nil, "")
	assert.Equal(t, FallbackNone, action, "无接收者信息应返回 None")
	assert.Equal(t, int64(1), hub.overloadMetrics.unrecoverable[0].Load(),
		"unrecoverable 普通级计数应 +1")
}

// TestRouteDeliveryFallbackNoHandler 未配置离线处理器：unrecoverable 计数
func TestRouteDeliveryFallbackNoHandler(t *testing.T) {
	hub := NewHub(newTestHubConfig()) // offlineMessageHandler 为 nil

	client := newFallbackTestClient("fb-nohandler", "u-nohandler")
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard).
		SetReceiver("u-nohandler")

	action := hub.routeDeliveryFallback(msg, client, "")
	assert.Equal(t, FallbackNone, action, "未配置离线处理器应返回 None")
	assert.Equal(t, int64(1), hub.overloadMetrics.unrecoverable[0].Load())
}

// TestRouteDeliveryFallbackBroadcast 广播扇出兜底：targetUserID 补写 Receiver
func TestRouteDeliveryFallbackBroadcast(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	handler := newCaptureOfflineHandler()
	hub.SetOfflineMessageHandler(handler)

	client := newFallbackTestClient("fb-bc", "u-bc-target")
	// 广播消息原 Receiver 为空（扇出时才有目标）
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard)

	action := hub.routeDeliveryFallback(msg, client, "u-bc-target")
	assert.Equal(t, FallbackOffline, action)

	offline := waitStored(t, handler.stored)
	assert.Equal(t, "u-bc-target", offline.Receiver, "广播兜底应补写目标接收者")
}

// TestTrySendWithFallbackSuccess 成功路径：TrySend 一次（零新增开销）
func TestTrySendWithFallbackSuccess(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	client := newFallbackTestClient("ts-ok", "u-ok")
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard).
		SetReceiver("u-ok")

	assert.True(t, hub.TrySendWithFallback(client, []byte(`{}`), msg), "通道空闲应实时送达")
	assert.Equal(t, int64(1), hub.overloadMetrics.realtime[0].Load(), "realtime 计数应 +1")
}

// TestTrySendWithFallbackFull 失败路径：按分级兜底（不静默丢失）
func TestTrySendWithFallbackFull(t *testing.T) {
	hub := NewHub(newTestHubConfig())

	handler := newCaptureOfflineHandler()
	hub.SetOfflineMessageHandler(handler)

	client := newFallbackTestClient("ts-full", "u-full")
	client.SendChan <- []byte(`{"flood":1}`) // 塞满
	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		WithGuarantee(models.GuaranteeStandard).
		SetReceiver("u-full")

	assert.False(t, hub.TrySendWithFallback(client, []byte(`{}`), msg), "通道满应返回 false")
	assert.Equal(t, int64(1), hub.overloadMetrics.offlineFallback[0].Load(), "应转离线补发")
	waitStored(t, handler.stored)
}

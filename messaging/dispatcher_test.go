/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-25 10:56:20
 * @FilePath: \go-wsc\messaging\message_handler_test.go
 * @Description: Hub 消息处理白盒单元测试（覆盖 hub/message_handler.go）
 *
 * 覆盖：
 *   - normalizeMessageFields 字段补全/保留
 *   - InvokeMessageReceivedCallback / InvokeErrorCallback nil 安全与触发
 *   - handleBinaryMessage 不 panic
 *   - handleDirectMessage / HandleBroadcastMessage 分支送达
 *   - handleBroadcast 经 EventLoop 投递（direct + global）
 *
 * 复用 group_test.go 中的 setupGroupTestHub / makeTestClient / makeGroupMessage 等 helper。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// normalizeMessageFields 测试
// ============================================================================

// TestNormalizeMessageFieldsFillsEmpty 验证空字段被客户端信息补全
func TestNormalizeMessageFieldsFillsEmpty(t *testing.T) {
	m, _ := newTestManager()

	client := makeTestClient("c-norm", "u-norm")
	msg := &models.HubMessage{} // 全空消息，避免 NewHubMessage 预填 system 字段

	m.normalizeMessageFields(client, msg)

	assert.Equal(t, "u-norm", msg.Sender, "Sender 应被客户端 UserID 补全")
	assert.Equal(t, models.UserTypeCustomer, msg.SenderType, "SenderType 应被客户端 UserType 补全")
	assert.Equal(t, "c-norm", msg.SenderClient, "SenderClient 应被客户端 ID 补全")
	assert.Equal(t, models.MessageTypeText, msg.MessageType, "MessageType 默认应为文本")
	assert.False(t, msg.CreateAt.IsZero(), "CreateAt 应被填充为当前时间")
	assert.NotEmpty(t, msg.ID, "ID 应被生成")
	assert.Contains(t, msg.ID, "u-norm-", "ID 应以 userID 为前缀")
}

// TestNormalizeMessageFieldsPreservesExisting 验证已填字段不被覆盖
func TestNormalizeMessageFieldsPreservesExisting(t *testing.T) {
	m, _ := newTestManager()

	client := makeTestClient("c-keep", "u-keep")
	fixedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	msg := models.NewHubMessage().
		SetSender("custom-sender").
		SetSenderType(models.UserTypeAgent).
		SetMessageType(models.MessageTypePong)
	msg.SenderClient = "custom-client" // 无 Setter，直接赋值
	msg.CreateAt = fixedTime
	msg.ID = "predefined-id"

	m.normalizeMessageFields(client, msg)

	assert.Equal(t, "custom-sender", msg.Sender, "已存在的 Sender 不应被覆盖")
	assert.Equal(t, models.UserTypeAgent, msg.SenderType, "已存在的 SenderType 不应被覆盖")
	assert.Equal(t, "custom-client", msg.SenderClient, "已存在的 SenderClient 不应被覆盖")
	assert.Equal(t, models.MessageTypePong, msg.MessageType, "已存在的 MessageType 不应被覆盖")
	assert.Equal(t, fixedTime, msg.CreateAt, "已存在的 CreateAt 不应被覆盖")
	assert.Equal(t, "predefined-id", msg.ID, "已存在的 ID 不应被覆盖")
}

// ============================================================================
// InvokeMessageReceivedCallback 测试
// ============================================================================

// TestInvokeMessageReceivedCallbackNil 验证 callback 为 nil 时返回 nil 且不 panic
func TestInvokeMessageReceivedCallbackNil(t *testing.T) {
	m, _ := newTestManager()

	client := makeTestClient("c-rcv", "u-rcv")
	msg := makeGroupMessage("sender")
	assert.NotPanics(t, func() {
		err := m.InvokeMessageReceivedCallback(context.Background(), client, msg)
		assert.NoError(t, err)
	})
}

// TestInvokeMessageReceivedCallbackInvoked 验证非 nil callback 被调用且消息字段被规范化
func TestInvokeMessageReceivedCallbackInvoked(t *testing.T) {
	m, _ := newTestManager()

	client := makeTestClient("c-rcv2", "u-rcv2")
	msg := &models.HubMessage{} // Sender 为空，依赖 normalize 补全

	done := make(chan *models.HubMessage, 1)
	m.WithMessageReceivedCallback(func(_ context.Context, _ *models.Client, m *models.HubMessage) error {
		select {
		case done <- m:
		default:
		}
		return nil
	})

	err := m.InvokeMessageReceivedCallback(context.Background(), client, msg)
	require.NoError(t, err)

	select {
	case got := <-done:
		assert.Equal(t, "u-rcv2", got.Sender, "回调收到的消息应已被 normalize 补全 Sender")
		assert.Equal(t, "c-rcv2", got.SenderClient)
	case <-time.After(time.Second):
		t.Fatal("消息接收回调未被调用")
	}
}

// ============================================================================
// InvokeErrorCallback 测试
// ============================================================================

// TestInvokeErrorCallbackNil 验证 callback 为 nil 时返回 nil 且不 panic
func TestInvokeErrorCallbackNil(t *testing.T) {
	m, _ := newTestManager()

	assert.NotPanics(t, func() {
		err := m.InvokeErrorCallback(context.Background(), errors.New("some err"), models.ErrorSeverityWarning)
		assert.NoError(t, err)
	})
}

// TestInvokeErrorCallbackInvoked 验证非 nil callback 被调用并透传参数
func TestInvokeErrorCallbackInvoked(t *testing.T) {
	m, _ := newTestManager()

	type evt struct {
		err      error
		severity models.ErrorSeverity
	}
	done := make(chan evt, 1)
	m.WithErrorCallback(func(_ context.Context, err error, severity models.ErrorSeverity) error {
		select {
		case done <- evt{err, severity}:
		default:
		}
		return err
	})

	inErr := errors.New("test error")
	returned := m.InvokeErrorCallback(context.Background(), inErr, models.ErrorSeverityError)

	select {
	case e := <-done:
		assert.Equal(t, inErr, e.err)
		assert.Equal(t, models.ErrorSeverityError, e.severity)
	case <-time.After(time.Second):
		t.Fatal("错误回调未被调用")
	}
	assert.Equal(t, inErr, returned, "应透传回调返回值")
}

// ============================================================================
// handleBinaryMessage 测试
// ============================================================================

// TestMessageHandlerBinary 验证二进制消息处理不 panic
func TestMessageHandlerBinary(t *testing.T) {
	m, _ := newTestManager()

	client := makeTestClient("c-bin", "u-bin")
	payload := []byte{0x01, 0x02, 0x03, 0xFF}

	assert.NotPanics(t, func() {
		m.handleBinaryMessage(client, payload)
	})
}

// ============================================================================
// handleDirectMessage 测试（直接调用，确定性断言）
// ============================================================================

// TestMessageHandlerDirectMessage 验证点对点消息分支送达
func TestMessageHandlerDirectMessage(t *testing.T) {
	m, host := newTestManager()

	t.Run("指定ReceiverClient精准投递", func(t *testing.T) {
		c1 := makeTestClient("c-dm1", "u-dm")
		c2 := makeTestClient("c-dm2", "u-dm")
		host.GetShardedRegistry().AddClient(c1)
		host.GetShardedRegistry().AddClient(c2)

		msg := makeGroupMessage("sender")
		msg.MessageID = "dm-client"
		msg.Receiver = "u-dm"
		msg.ReceiverClient = "c-dm2" // 仅投递给 c2

		m.handleDirectMessage(host.Context(), msg)

		// c2 收到
		select {
		case data := <-c2.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatal("指定 ReceiverClient 的客户端应收到消息")
		}
		// c1 不收到
		select {
		case <-c1.SendChan:
			t.Fatal("非指定客户端不应收到消息")
		default:
		}
	})

	t.Run("未指定ReceiverClient遍历用户所有设备", func(t *testing.T) {
		c1 := makeTestClient("c-dm3", "u-multi")
		c2 := makeTestClient("c-dm4", "u-multi")
		host.GetShardedRegistry().AddClient(c1)
		host.GetShardedRegistry().AddClient(c2)

		msg := makeGroupMessage("sender")
		msg.MessageID = "dm-all-devices"
		msg.Receiver = "u-multi"
		// SenderClient 为空，避免触发 syncToSenderDevices 回环
		// 与生产入口契约一致：上游 InjectRoute 注入路由信封（appID 归一化为 DefaultAppID），
		// handleDirectMessage 直接读 msg.AppID 做 ClientMatchesEnvelope 严格匹配
		msg.InjectRoute(host.Context())

		m.handleDirectMessage(host.Context(), msg)

		for _, c := range []*models.Client{c1, c2} {
			select {
			case data := <-c.SendChan:
				assert.NotEmpty(t, data)
			case <-time.After(time.Second):
				t.Fatalf("用户设备 %s 应收到消息", c.ID)
			}
		}
	})
}

// ============================================================================
// HandleBroadcastMessage 测试（直接调用，确定性断言）
// ============================================================================

// TestMessageHandlerBroadcastMessage 验证全站广播送达所有注册 WS 客户端
func TestMessageHandlerBroadcastMessage(t *testing.T) {
	m, host := newTestManager()

	c1 := makeTestClient("c-bc1", "u-bc1")
	c2 := makeTestClient("c-bc2", "u-bc2")
	c3 := makeTestClient("c-bc3", "u-bc3")
	host.GetShardedRegistry().AddClient(c1)
	host.GetShardedRegistry().AddClient(c2)
	host.GetShardedRegistry().AddClient(c3)

	msg := makeGroupMessage("sender")
	msg.MessageID = "bc-all"

	m.HandleBroadcastMessage(host.Context(), msg)

	for _, c := range []*models.Client{c1, c2, c3} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("客户端 %s 应收到广播消息", c.ID)
		}
	}
}

// ============================================================================
// handleTextMessage 测试（覆盖心跳/ACK 短路与回调路径）
// ============================================================================

// TestMessageHandlerTextHeartbeat 验证心跳消息被处理且不触发业务回调
func TestMessageHandlerTextHeartbeat(t *testing.T) {
	m, _ := newTestManager()

	client := makeTestClient("c-hb", "u-hb")
	called := make(chan struct{}, 1)
	m.WithMessageReceivedCallback(func(_ context.Context, _ *models.Client, _ *models.HubMessage) error {
		select {
		case called <- struct{}{}:
		default:
		}
		return nil
	})

	// 构造心跳消息 JSON
	hbMsg := models.NewHubMessage().SetMessageType(models.MessageTypeHeartbeat)
	data := mustMarshalHubMessage(t, hbMsg)

	assert.NotPanics(t, func() {
		m.handleTextMessage(context.Background(), client, data)
	})

	// 心跳消息不应触发业务接收回调
	select {
	case <-called:
		t.Fatal("心跳消息不应触发消息接收回调")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestMessageHandlerTextNonForwardable 验证普通文本消息触发接收回调
func TestMessageHandlerTextNonForwardable(t *testing.T) {
	m, _ := newTestManager()

	client := makeTestClient("c-txt", "u-txt")
	called := make(chan *models.HubMessage, 1)
	m.WithMessageReceivedCallback(func(_ context.Context, _ *models.Client, m *models.HubMessage) error {
		select {
		case called <- m:
		default:
		}
		return nil
	})

	txtMsg := &models.HubMessage{MessageType: models.MessageTypeText, Content: "hi"}
	data := mustMarshalHubMessage(t, txtMsg)

	m.handleTextMessage(context.Background(), client, data)

	select {
	case m := <-called:
		assert.Equal(t, "u-txt", m.Sender, "回调消息应被 normalize 补全 Sender")
	case <-time.After(time.Second):
		t.Fatal("普通文本消息应触发接收回调")
	}
}

// mustMarshalHubMessage 测试辅助：序列化 models.HubMessage，失败 fatal
func mustMarshalHubMessage(t *testing.T, msg *models.HubMessage) []byte {
	t.Helper()
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	return data
}

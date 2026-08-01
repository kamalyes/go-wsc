/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-25 10:56:20
 * @FilePath: \go-wsc\hub\message_handler_test.go
 * @Description: Hub 消息处理白盒单元测试（覆盖 hub/message_handler.go）
 *
 * 覆盖：
 *   - normalizeMessageFields 字段补全/保留
 *   - InvokeMessageReceivedCallback / InvokeErrorCallback nil 安全与触发
 *   - handleBinaryMessage 不 panic
 *   - handleDirectMessage / handleBroadcastMessage 分支送达
 *   - handleBroadcast 经 EventLoop 投递（direct + global）
 *
 * 复用 group_test.go 中的 setupGroupTestHub / makeTestClient / makeGroupMessage 等 helper。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// normalizeMessageFields 测试
// ============================================================================

// TestNormalizeMessageFieldsFillsEmpty 验证空字段被客户端信息补全
func TestNormalizeMessageFieldsFillsEmpty(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-norm", "u-norm")
	msg := &HubMessage{} // 全空消息，避免 NewHubMessage 预填 system 字段

	hub.normalizeMessageFields(client, msg)

	assert.Equal(t, "u-norm", msg.Sender, "Sender 应被客户端 UserID 补全")
	assert.Equal(t, UserTypeCustomer, msg.SenderType, "SenderType 应被客户端 UserType 补全")
	assert.Equal(t, "c-norm", msg.SenderClient, "SenderClient 应被客户端 ID 补全")
	assert.Equal(t, MessageTypeText, msg.MessageType, "MessageType 默认应为文本")
	assert.False(t, msg.CreateAt.IsZero(), "CreateAt 应被填充为当前时间")
	assert.NotEmpty(t, msg.ID, "ID 应被生成")
	assert.Contains(t, msg.ID, "u-norm-", "ID 应以 userID 为前缀")
}

// TestNormalizeMessageFieldsPreservesExisting 验证已填字段不被覆盖
func TestNormalizeMessageFieldsPreservesExisting(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-keep", "u-keep")
	fixedTime := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	msg := NewHubMessage().
		SetSender("custom-sender").
		SetSenderType(UserTypeAgent).
		SetMessageType(MessageTypePong)
	msg.SenderClient = "custom-client" // 无 Setter，直接赋值
	msg.CreateAt = fixedTime
	msg.ID = "predefined-id"

	hub.normalizeMessageFields(client, msg)

	assert.Equal(t, "custom-sender", msg.Sender, "已存在的 Sender 不应被覆盖")
	assert.Equal(t, UserTypeAgent, msg.SenderType, "已存在的 SenderType 不应被覆盖")
	assert.Equal(t, "custom-client", msg.SenderClient, "已存在的 SenderClient 不应被覆盖")
	assert.Equal(t, MessageTypePong, msg.MessageType, "已存在的 MessageType 不应被覆盖")
	assert.Equal(t, fixedTime, msg.CreateAt, "已存在的 CreateAt 不应被覆盖")
	assert.Equal(t, "predefined-id", msg.ID, "已存在的 ID 不应被覆盖")
}

// ============================================================================
// InvokeMessageReceivedCallback 测试
// ============================================================================

// TestInvokeMessageReceivedCallbackNil 验证 callback 为 nil 时返回 nil 且不 panic
func TestInvokeMessageReceivedCallbackNil(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-rcv", "u-rcv")
	msg := makeGroupMessage("sender")
	assert.NotPanics(t, func() {
		err := hub.InvokeMessageReceivedCallback(context.Background(), client, msg)
		assert.NoError(t, err)
	})
}

// TestInvokeMessageReceivedCallbackInvoked 验证非 nil callback 被调用且消息字段被规范化
func TestInvokeMessageReceivedCallbackInvoked(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-rcv2", "u-rcv2")
	msg := &HubMessage{} // Sender 为空，依赖 normalize 补全

	done := make(chan *HubMessage, 1)
	hub.OnMessageReceived(func(_ context.Context, _ *Client, m *HubMessage) error {
		select {
		case done <- m:
		default:
		}
		return nil
	})

	err := hub.InvokeMessageReceivedCallback(context.Background(), client, msg)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	assert.NotPanics(t, func() {
		err := hub.InvokeErrorCallback(context.Background(), errors.New("some err"), ErrorSeverityWarning)
		assert.NoError(t, err)
	})
}

// TestInvokeErrorCallbackInvoked 验证非 nil callback 被调用并透传参数
func TestInvokeErrorCallbackInvoked(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	type evt struct {
		err      error
		severity ErrorSeverity
	}
	done := make(chan evt, 1)
	hub.OnError(func(_ context.Context, err error, severity ErrorSeverity) error {
		select {
		case done <- evt{err, severity}:
		default:
		}
		return err
	})

	inErr := errors.New("test error")
	returned := hub.InvokeErrorCallback(context.Background(), inErr, ErrorSeverityError)

	select {
	case e := <-done:
		assert.Equal(t, inErr, e.err)
		assert.Equal(t, ErrorSeverityError, e.severity)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-bin", "u-bin")
	payload := []byte{0x01, 0x02, 0x03, 0xFF}

	assert.NotPanics(t, func() {
		hub.handleBinaryMessage(client, payload)
	})
}

// ============================================================================
// handleDirectMessage 测试（直接调用，确定性断言）
// ============================================================================

// TestMessageHandlerDirectMessage 验证点对点消息分支送达
func TestMessageHandlerDirectMessage(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("指定ReceiverClient精准投递", func(t *testing.T) {
		c1 := makeTestClient("c-dm1", "u-dm")
		c2 := makeTestClient("c-dm2", "u-dm")
		hub.shardedRegistry.AddClient(c1)
		hub.shardedRegistry.AddClient(c2)

		msg := makeGroupMessage("sender")
		msg.MessageID = "dm-client"
		msg.Receiver = "u-dm"
		msg.ReceiverClient = "c-dm2" // 仅投递给 c2

		hub.handleDirectMessage(hub.ctx, msg)

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
		hub.shardedRegistry.AddClient(c1)
		hub.shardedRegistry.AddClient(c2)

		msg := makeGroupMessage("sender")
		msg.MessageID = "dm-all-devices"
		msg.Receiver = "u-multi"
		// SenderClient 为空，避免触发 syncToSenderDevices 回环

		hub.handleDirectMessage(hub.ctx, msg)

		for _, c := range []*Client{c1, c2} {
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
// handleBroadcastMessage 测试（直接调用，确定性断言）
// ============================================================================

// TestMessageHandlerBroadcastMessage 验证全站广播送达所有注册 WS 客户端
func TestMessageHandlerBroadcastMessage(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	c1 := makeTestClient("c-bc1", "u-bc1")
	c2 := makeTestClient("c-bc2", "u-bc2")
	c3 := makeTestClient("c-bc3", "u-bc3")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)
	hub.shardedRegistry.AddClient(c3)

	msg := makeGroupMessage("sender")
	msg.MessageID = "bc-all"

	hub.handleBroadcastMessage(hub.ctx, msg)

	for _, c := range []*Client{c1, c2, c3} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		case <-time.After(time.Second):
			t.Fatalf("客户端 %s 应收到广播消息", c.ID)
		}
	}
}

// ============================================================================
// handleBroadcast 测试（经 EventLoop 投递，require.Eventually 断言）
// ============================================================================

// TestBroadcastViaEventLoopDirect 验证 direct 消息经 EventLoop 送达已注册用户
func TestBroadcastViaEventLoopDirect(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	client := makeTestClient("c-el-dm", "u-el-dm")
	hub.shardedRegistry.AddClient(client)

	msg := makeGroupMessage("remote-sender")
	msg.MessageID = "el-direct"
	msg.Receiver = "u-el-dm"
	// BroadcastType 为空 → handleBroadcast 走 handleDirectMessage

	// 投递到 broadcast channel，由 EventLoop 消费
	hub.broadcast <- msg

	require.Eventually(t, func() bool {
		select {
		case <-client.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "direct 消息应经 EventLoop 送达注册用户")
}

// TestBroadcastViaEventLoopGlobal 验证 global 广播经 EventLoop 送达所有注册客户端
func TestBroadcastViaEventLoopGlobal(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	c1 := makeTestClient("c-el-g1", "u-el-g1")
	c2 := makeTestClient("c-el-g2", "u-el-g2")
	hub.shardedRegistry.AddClient(c1)
	hub.shardedRegistry.AddClient(c2)

	msg := makeGroupMessage("remote-sender")
	msg.MessageID = "el-global"
	msg.BroadcastType = BroadcastTypeGlobal // → handleBroadcast 走 handleBroadcastMessage

	hub.broadcast <- msg

	require.Eventually(t, func() bool {
		select {
		case <-c1.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "global 广播应送达客户端1")
	require.Eventually(t, func() bool {
		select {
		case <-c2.SendChan:
			return true
		default:
			return false
		}
	}, time.Second, 20*time.Millisecond, "global 广播应送达客户端2")
}

// ============================================================================
// handleTextMessage 测试（覆盖心跳/ACK 短路与回调路径）
// ============================================================================

// TestMessageHandlerTextHeartbeat 验证心跳消息被处理且不触发业务回调
func TestMessageHandlerTextHeartbeat(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-hb", "u-hb")
	called := make(chan struct{}, 1)
	hub.OnMessageReceived(func(_ context.Context, _ *Client, _ *HubMessage) error {
		select {
		case called <- struct{}{}:
		default:
		}
		return nil
	})

	// 构造心跳消息 JSON
	hbMsg := NewHubMessage().SetMessageType(MessageTypeHeartbeat)
	data := mustMarshalHubMessage(t, hbMsg)

	assert.NotPanics(t, func() {
		hub.handleTextMessage(context.Background(), client, data)
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
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	client := makeTestClient("c-txt", "u-txt")
	called := make(chan *HubMessage, 1)
	hub.OnMessageReceived(func(_ context.Context, _ *Client, m *HubMessage) error {
		select {
		case called <- m:
		default:
		}
		return nil
	})

	txtMsg := &HubMessage{MessageType: MessageTypeText, Content: "hi"}
	data := mustMarshalHubMessage(t, txtMsg)

	hub.handleTextMessage(context.Background(), client, data)

	select {
	case m := <-called:
		assert.Equal(t, "u-txt", m.Sender, "回调消息应被 normalize 补全 Sender")
	case <-time.After(time.Second):
		t.Fatal("普通文本消息应触发接收回调")
	}
}

// mustMarshalHubMessage 测试辅助：序列化 HubMessage，失败 fatal
func mustMarshalHubMessage(t *testing.T, msg *HubMessage) []byte {
	t.Helper()
	data, err := json.Marshal(msg)
	require.NoError(t, err)
	return data
}

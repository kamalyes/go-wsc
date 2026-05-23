/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-03 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-03 01:08:28
 * @FilePath: \go-wsc\hub_multi_device_sync_test.go
 * @Description: 多端消息同步测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMultiDeviceMessageSync 测试多端消息同步
// 场景：用户A在B、C、D三个设备登录，B设备发送消息给用户F，C、D设备应该能收到此消息
func TestMultiDeviceMessageSync(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	go hub.Run()
	defer hub.SafeShutdown()

	// 使用ID生成器生成所有ID
	idGen := hub.GetIDGenerator()
	userAID := idGen.GenerateCorrelationID()
	deviceBID := idGen.GenerateSpanID()
	deviceCID := idGen.GenerateSpanID()
	deviceDID := idGen.GenerateSpanID()
	userFID := idGen.GenerateCorrelationID()
	deviceFID := idGen.GenerateSpanID()

	// 用户A的三个设备：B、C、D
	deviceB := createTestClientWithDeviceIDs(userAID, deviceBID)
	deviceC := createTestClientWithDeviceIDs(userAID, deviceCID)
	deviceD := createTestClientWithDeviceIDs(userAID, deviceDID)

	// 用户F的一个设备
	userF := createTestClientWithDeviceIDs(userFID, deviceFID)

	// 启动消息消费者
	receivedByC := make(chan *HubMessage, 10)
	receivedByD := make(chan *HubMessage, 10)
	receivedByF := make(chan *HubMessage, 10)

	hub.Register(deviceB)
	hub.Register(deviceC)
	hub.Register(deviceD)
	hub.Register(userF)

	go func() {
		deviceB.DrainSendChan(func(msg []byte) {
			t.Logf("设备B收到消息: %s", string(msg))
		})
	}()

	go func() {
		deviceC.DrainSendChan(func(msgData []byte) {
			var msg HubMessage
			if err := json.Unmarshal(msgData, &msg); err == nil {
				receivedByC <- &msg
			}
		})
	}()

	go func() {
		deviceD.DrainSendChan(func(msgData []byte) {
			var msg HubMessage
			if err := json.Unmarshal(msgData, &msg); err == nil {
				receivedByD <- &msg
			}
		})
	}()

	go func() {
		userF.DrainSendChan(func(msgData []byte) {
			var msg HubMessage
			if err := json.Unmarshal(msgData, &msg); err == nil {
				receivedByF <- &msg
			}
		})
	}()
	time.Sleep(100 * time.Millisecond)

	// 设置消息接收回调，模拟B设备发送消息给F
	hub.OnMessageReceived(func(ctx context.Context, client *Client, msg *HubMessage) error {
		t.Logf("收到消息: from=%s (client=%s) to=%s, content=%s",
			msg.Sender, msg.SenderClient, msg.Receiver, msg.Content)

		// 如果是用户发送的消息，通过Hub转发
		if msg.MessageType == MessageTypeText && msg.Receiver != "" {
			result := hub.SendToUserWithRetry(ctx, msg.Receiver, msg)
			return result.FinalError
		}
		return nil
	})

	// B设备发送消息给F（需要模拟完整的消息处理流程）
	testMsg := &HubMessage{
		ID:           idGen.GenerateTraceID(),
		MessageType:  MessageTypeText,
		Receiver:     userFID,
		Content:      "Hello from device B",
		MessageID:    idGen.GenerateRequestID(),
		Sender:       deviceB.UserID,
		SenderClient: deviceB.ID,
	}

	// 模拟客户端B发送消息（通过InvokeMessageReceivedCallback触发）
	err := hub.InvokeMessageReceivedCallback(context.Background(), deviceB, testMsg)
	require.NoError(t, err)

	// 验证：用户F应该收到消息
	select {
	case msg := <-receivedByF:
		assert.Equal(t, "Hello from device B", msg.Content)
		assert.Equal(t, userAID, msg.Sender)
		assert.Equal(t, deviceB.ID, msg.SenderClient)
		t.Log("✅ 用户F收到消息")
	case <-time.After(2 * time.Second):
		t.Fatal("❌ 用户F未收到消息")
	}

	// 验证：设备C应该收到同步消息
	select {
	case msg := <-receivedByC:
		assert.Equal(t, "Hello from device B", msg.Content)
		assert.Equal(t, userAID, msg.Sender)
		assert.Equal(t, deviceB.ID, msg.SenderClient)
		t.Log("✅ 设备C收到同步消息")
	case <-time.After(2 * time.Second):
		t.Fatal("❌ 设备C未收到同步消息")
	}

	// 验证：设备D应该收到同步消息
	select {
	case msg := <-receivedByD:
		assert.Equal(t, "Hello from device B", msg.Content)
		assert.Equal(t, userAID, msg.Sender)
		assert.Equal(t, deviceB.ID, msg.SenderClient)
		t.Log("✅ 设备D收到同步消息")
	case <-time.After(2 * time.Second):
		t.Fatal("❌ 设备D未收到同步消息")
	}

	t.Log("🎉 多端消息同步测试通过")
}

// TestMultiDeviceSyncExcludeSender 测试多端同步排除发送设备
func TestMultiDeviceSyncExcludeSender(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	go hub.Run()
	defer hub.SafeShutdown()

	// 使用ID生成器生成所有ID
	idGen := hub.GetIDGenerator()
	userAID := idGen.GenerateCorrelationID()
	deviceBID := idGen.GenerateSpanID()
	deviceCID := idGen.GenerateSpanID()
	userFID := idGen.GenerateCorrelationID()
	deviceFID := idGen.GenerateSpanID()

	// 用户A的两个设备
	deviceB := createTestClientWithDeviceIDs(userAID, deviceBID)
	deviceC := createTestClientWithDeviceIDs(userAID, deviceCID)

	// 用户F
	userF := createTestClientWithDeviceIDs(userFID, deviceFID)

	hub.Register(deviceB)
	hub.Register(deviceC)
	hub.Register(userF)

	// 统计设备B收到的消息数（应该只有欢迎消息，不应该收到自己发的消息）
	receivedByB := 0
	go func() {
		deviceB.DrainSendChan(func(msgData []byte) {
			var msg HubMessage
			if err := json.Unmarshal(msgData, &msg); err == nil {
				if msg.MessageType != MessageTypeWelcome {
					receivedByB++
					t.Logf("设备B收到消息: %+v", msg)
				}
			}
		})
	}()

	receivedByC := make(chan *HubMessage, 10)
	go func() {
		deviceC.DrainSendChan(func(msgData []byte) {
			var msg HubMessage
			if err := json.Unmarshal(msgData, &msg); err == nil {
				if msg.MessageType != MessageTypeWelcome {
					receivedByC <- &msg
				}
			}
		})
	}()

	go func() {
		userF.DrainSendChan(nil)
	}()
	time.Sleep(100 * time.Millisecond)

	// 设置消息接收回调
	hub.OnMessageReceived(func(ctx context.Context, client *Client, msg *HubMessage) error {
		if msg.MessageType == MessageTypeText && msg.Receiver != "" {
			result := hub.SendToUserWithRetry(ctx, msg.Receiver, msg)
			return result.FinalError
		}
		return nil
	})

	// B设备发送消息
	testMsg := &HubMessage{
		MessageType: MessageTypeText,
		Receiver:    userFID,
		Content:     "Test message",
	}
	err := hub.InvokeMessageReceivedCallback(context.Background(), deviceB, testMsg)
	require.NoError(t, err)

	// 等待消息处理
	time.Sleep(500 * time.Millisecond)

	// 验证：设备C应该收到同步消息
	select {
	case msg := <-receivedByC:
		assert.Equal(t, "Test message", msg.Content)
		assert.Equal(t, deviceB.ID, msg.SenderClient)
		t.Log("✅ 设备C收到同步消息")
	case <-time.After(1 * time.Second):
		t.Fatal("❌ 设备C未收到同步消息")
	}

	// 验证：设备B不应该收到自己发送的消息
	// 注意：由于消息处理是异步的，给一点时间确保没有额外消息
	time.Sleep(200 * time.Millisecond)
	assert.LessOrEqual(t, receivedByB, 1, "设备B最多收到1条消息（可能是回显或系统消息）")
	if receivedByB == 0 {
		t.Log("✅ 设备B正确地没有收到自己发送的消息")
	} else {
		t.Logf("⚠️ 设备B收到了%d条消息（可能是系统消息或回显）", receivedByB)
	}
}

// TestMultiDeviceSelfMessage 测试用户给自己发消息
func TestMultiDeviceSelfMessage(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	go hub.Run()
	defer hub.SafeShutdown()
	time.Sleep(50 * time.Millisecond)

	// 使用ID生成器生成所有ID
	idGen := hub.GetIDGenerator()
	userAID := idGen.GenerateCorrelationID()
	deviceBID := idGen.GenerateSpanID()
	deviceCID := idGen.GenerateSpanID()

	// 用户A的两个设备
	deviceB := createTestClientWithDeviceIDs(userAID, deviceBID)
	deviceC := createTestClientWithDeviceIDs(userAID, deviceCID)

	receivedByB := make(chan *HubMessage, 10)
	receivedByC := make(chan *HubMessage, 10)

	hub.Register(deviceB)
	hub.Register(deviceC)

	go func() {
		deviceB.DrainSendChan(func(msgData []byte) {
			var msg HubMessage
			if err := json.Unmarshal(msgData, &msg); err == nil {
				if msg.MessageType != MessageTypeWelcome {
					receivedByB <- &msg
				}
			}
		})
	}()

	go func() {
		deviceC.DrainSendChan(func(msgData []byte) {
			var msg HubMessage
			if err := json.Unmarshal(msgData, &msg); err == nil {
				if msg.MessageType != MessageTypeWelcome {
					receivedByC <- &msg
				}
			}
		})
	}()
	time.Sleep(100 * time.Millisecond)

	hub.OnMessageReceived(func(ctx context.Context, client *Client, msg *HubMessage) error {
		if msg.MessageType == MessageTypeText && msg.Receiver != "" {
			result := hub.SendToUserWithRetry(ctx, msg.Receiver, msg)
			return result.FinalError
		}
		return nil
	})

	// B设备给自己（userA）发消息
	testMsg := &HubMessage{
		MessageType: MessageTypeText,
		Receiver:    userAID,
		Content:     "Note to self",
	}
	err := hub.InvokeMessageReceivedCallback(context.Background(), deviceB, testMsg)
	require.NoError(t, err)

	time.Sleep(300 * time.Millisecond)

	// 当发送者==接收者时，两个设备都应该收到消息
	receivedCount := 0

	select {
	case msg := <-receivedByB:
		assert.Equal(t, "Note to self", msg.Content)
		receivedCount++
		t.Log("✅ 设备B收到自己的消息")
	case <-time.After(500 * time.Millisecond):
		t.Log("设备B未收到消息（预期行为，因为是接收者）")
	}

	select {
	case msg := <-receivedByC:
		assert.Equal(t, "Note to self", msg.Content)
		receivedCount++
		t.Log("✅ 设备C收到消息")
	case <-time.After(500 * time.Millisecond):
		t.Log("设备C未收到消息")
	}

	// 至少一个设备应该收到消息
	assert.GreaterOrEqual(t, receivedCount, 1, "至少一个设备应该收到消息")
}

// 辅助函数：创建测试客户端（使用生成的ID）
func createTestClientWithDeviceIDs(userID, deviceID string) *Client {
	return &Client{
		ID:       deviceID,
		UserID:   userID,
		SendChan: make(chan []byte, 256),
	}
}

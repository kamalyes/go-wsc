/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 00:00:00
 * @FilePath: \go-wsc\connection\control_lane_test.go
 * @Description: 控制通道测试 - 纯函数判定 + 四级降级链（优先 lane / 数据 lane 回退 / 断链降级 / 拒绝）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

// TestIsControlMessage 控制类消息集合判定（7 类）
func TestIsControlMessage(t *testing.T) {
	for _, mt := range []models.MessageType{
		models.MessageTypeKickOut,
		models.MessageTypeForceOffline,
		models.MessageTypeTerminate,
		models.MessageTypeAck,
		models.MessageTypeConnectionRejected,
		models.MessageTypeConnectionError,
		models.MessageTypeConnectionTimeout,
	} {
		assert.True(t, IsControlMessage(mt), "%s 应为控制类消息", mt)
	}
	for _, mt := range []models.MessageType{
		models.MessageTypeText, models.MessageTypeWelcome, models.MessageTypeImage,
	} {
		assert.False(t, IsControlMessage(mt), "%s 不应为控制类消息", mt)
	}
}

// TestIsDisconnectMessage 断链类判定（KickOut/ForceOffline/Terminate）
func TestIsDisconnectMessage(t *testing.T) {
	assert.True(t, IsDisconnectMessage(models.MessageTypeKickOut))
	assert.True(t, IsDisconnectMessage(models.MessageTypeForceOffline))
	assert.True(t, IsDisconnectMessage(models.MessageTypeTerminate))
	assert.False(t, IsDisconnectMessage(models.MessageTypeAck), "Ack 属控制类但非断链类")
}

// TestSendControlViaControlLane 降级链第 1 级：CtrlCh 独立 lane 投递
func TestSendControlViaControlLane(t *testing.T) {
	lane := NewControlLane(nil)
	client := models.NewClient("ctl-1", "u-500", models.UserTypeCustomer)
	client.Context = context.Background()
	client.CtrlCh = make(chan []byte, constants.CtrlChanCapacity)
	client.SendChan = make(chan []byte, 4) // 数据 lane 提供但不应被使用
	payload := []byte(`{"type":"kick_out"}`)
	assert.True(t, lane.SendControl(client, payload, nil))
	select {
	case got := <-client.CtrlCh:
		assert.Equal(t, payload, got)
	default:
		t.Fatal("消息应进入控制 lane")
	}
	assert.Equal(t, 0, len(client.SendChan), "控制消息不应占用数据 lane")
}

// TestSendControlFallbackToDataLane 降级链第 2 级：CtrlCh 未初始化 → 回退数据 lane TrySend
func TestSendControlFallbackToDataLane(t *testing.T) {
	lane := NewControlLane(nil)
	client := models.NewClient("ctl-2", "u-600", models.UserTypeCustomer)
	client.Context = context.Background()
	client.SendChan = make(chan []byte, 4) // CtrlCh 保持 nil（手工构造/SSE 早期客户端）

	payload := []byte(`{"type":"ack"}`)
	assert.True(t, lane.SendControl(client, payload, nil))
	select {
	case got := <-client.SendChan:
		assert.Equal(t, payload, got)
	default:
		t.Fatal("CtrlCh 未初始化时应回退数据 lane")
	}
}

// TestSendControlRejectWhenFull 降级链第 4 级：双通道满 + 非断链类 → false（调用方走各自兜底）
func TestSendControlRejectWhenFull(t *testing.T) {
	lane := NewControlLane(nil)
	client := models.NewClient("ctl-3", "u-700", models.UserTypeCustomer)
	client.Context = context.Background()
	client.CtrlCh = make(chan []byte, 1)
	client.SendChan = make(chan []byte, 1)
	client.CtrlCh <- []byte("full") // 控制通道已满
	client.SendChan <- []byte("full")

	msg := &models.HubMessage{MessageType: models.MessageTypeAck}
	assert.False(t, lane.SendControl(client, []byte(`{"type":"ack"}`), msg),
		"双通道满且非断链类应返回 false")
}

// TestSendControlDisconnectFallback 降级链第 3 级：通道满 + 断链类 + 有 Conn → 直接断链即视为送达
func TestSendControlDisconnectFallback(t *testing.T) {
	sConn, cConn := newWSConnPair(t)
	defer sConn.Close()

	lane := NewControlLane(nil)
	client := models.NewClient("ctl-4", "u-800", models.UserTypeCustomer)
	client.Context = context.Background()
	client.Conn = sConn
	client.CtrlCh = make(chan []byte, 1)
	client.SendChan = make(chan []byte, 1)
	client.CtrlCh <- []byte("full")
	client.SendChan <- []byte("full")

	kickMsg := &models.HubMessage{MessageType: models.MessageTypeKickOut}
	assert.True(t, lane.SendControl(client, []byte(`{"type":"kick_out"}`), kickMsg),
		"断链类通道满时应降级直接断链并视为送达")

	// 客户端侧应感知连接关闭
	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := cConn.ReadMessage()
	require.Error(t, err, "断链后客户端读应失败")
}

// TestSendControlNilClient nil 防御
func TestSendControlNilClient(t *testing.T) {
	lane := NewControlLane(nil)
	assert.False(t, lane.SendControl(nil, []byte("x"), nil))
	assert.False(t, lane.SendControlMessage(nil, &models.HubMessage{MessageType: models.MessageTypeAck}))
}

// TestSendControlMessageSerialization 序列化一体化：客户端读回完整 JSON 控制消息
func TestSendControlMessageSerialization(t *testing.T) {
	lane := NewControlLane(nil)
	client := models.NewClient("ctl-5", "u-900", models.UserTypeAgent)
	client.Context = context.Background()
	client.CtrlCh = make(chan []byte, constants.CtrlChanCapacity)

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeForceOffline).
		SetSender("system").
		SetReceiver(client.UserID)

	assert.True(t, lane.SendControlMessage(client, msg))

	select {
	case data := <-client.CtrlCh:
		assert.Contains(t, string(data), `"force_offline"`, "控制消息应含消息类型")
		assert.Contains(t, string(data), `"system"`, "控制消息应含发送者")
	default:
		t.Fatal("控制消息应进入控制 lane")
	}
}

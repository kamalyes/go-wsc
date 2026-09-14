/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-10 21:36:18
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 11:30:00
 * @FilePath: \go-wsc\hub\control_lane_test.go
 * @Description: 控制通道测试 —— 必达级独立 lane + CtrlCh 满时断链降级
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsControlMessage 控制类消息判定收敛
func TestIsControlMessage(t *testing.T) {
	controlTypes := []models.MessageType{
		models.MessageTypeKickOut,
		models.MessageTypeForceOffline,
		models.MessageTypeTerminate,
		models.MessageTypeAck,
		models.MessageTypeConnectionRejected,
		models.MessageTypeConnectionError,
		models.MessageTypeConnectionTimeout,
	}
	for _, mt := range controlTypes {
		assert.True(t, IsControlMessage(mt), "%s 应为控制类", mt)
	}
	// 业务消息不是控制类
	assert.False(t, IsControlMessage(models.MessageTypeText))
	assert.False(t, IsControlMessage(models.MessageTypePayment))
}

// TestIsDisconnectMessage 断链类判定（CtrlCh 满时降级直接断链的依据）
func TestIsDisconnectMessage(t *testing.T) {
	assert.True(t, IsDisconnectMessage(models.MessageTypeKickOut))
	assert.True(t, IsDisconnectMessage(models.MessageTypeForceOffline))
	assert.True(t, IsDisconnectMessage(models.MessageTypeTerminate))
	// ACK 等非断链类控制消息不降级断链
	assert.False(t, IsDisconnectMessage(models.MessageTypeAck))
}

// TestSendControlMessageDelivered 控制消息走 CtrlCh 独立投递（数据 lane 满时仍可达）
func TestSendControlMessageDelivered(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("ctrl-client", "ctrl-user", sConn)
	client.SendChan = make(chan []byte, 1)
	client.CtrlCh = make(chan []byte, 16)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 填满数据 lane（模拟业务洪峰）
	client.SendChan <- []byte(`{"flood":1}`)

	// 控制消息仍应通过 CtrlCh 送达
	kick := models.NewHubMessage().
		SetMessageType(models.MessageTypeKickOut).
		SetSender("system").
		SetReceiver(client.UserID)
	assert.True(t, hub.SendControlMessage(client, kick), "控制消息应走 CtrlCh 送达")

	// 端到端验收：写泵唤醒（数据 lane 静默期也由主 select 的 CtrlCh case 兜底），
	// KickOut 帧最终到达客户端
	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		_, data, err := cConn.ReadMessage()
		require.NoError(t, err, "读取 KickOut 帧失败")
		if strings.Contains(string(data), `"kick_out"`) {
			break
		}
	}
}

// TestSendControlDegradeToClose CtrlCh 满且断链类：降级直接断链（语义达成即送达）
func TestSendControlDegradeToClose(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("degrade-client", "degrade-user", sConn)
	client.CtrlCh = make(chan []byte, 1) // 容量 1 便于填满
	hub.Register(client)

	// 填满控制通道
	client.CtrlCh <- []byte(`{"stale":1}`)

	kick := models.NewHubMessage().SetMessageType(models.MessageTypeKickOut)
	// 断链类 + CtrlCh 满：降级直接 Close（返回 true = 语义达成）
	assert.True(t, hub.SendControl(client, []byte(`{}`), kick), "断链类降级断链应视为送达目的")

	// 连接应被关闭
	cConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err := cConn.ReadMessage()
	require.Error(t, err, "降级断链后对端读取应失败")
}

// TestSendControlFullNonDisconnect CtrlCh 满且非断链类：返回 false 走调用方兜底
func TestSendControlFullNonDisconnect(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, _ := newWSConnPair(t)
	client := newTestClient("ack-client", "ack-user", sConn)
	client.CtrlCh = make(chan []byte, 1)
	hub.Register(client)

	client.CtrlCh <- []byte(`{"stale":1}`)

	// ACK 非断链类：通道满返回 false（不降级断链——ACK 有重试兜底）
	ackMsg := models.NewHubMessage().SetMessageType(models.MessageTypeAck)
	assert.False(t, hub.SendControl(client, []byte(`{}`), ackMsg),
		"非断链类通道满应返回 false 交由调用方兜底")
}

// TestSendControlNilDefs nil 防御：nil client / nil msg
func TestSendControlNilDefs(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	assert.False(t, hub.SendControl(nil, []byte{}, nil), "nil client 应返回 false")
	assert.False(t, hub.SendControlMessage(nil, nil), "nil client/msg 应返回 false")
}

// TestControlLaneNotFlooded 数据 lane 洪峰不影响控制 lane（双 lane 隔离语义）
func TestControlLaneNotFlooded(t *testing.T) {
	hub, shutdown := startTestHub(t, newTestHubConfig())
	defer shutdown()

	sConn, cConn := newWSConnPair(t)
	client := newTestClient("iso-client", "iso-user", sConn)
	client.SendChan = make(chan []byte, 4)
	client.CtrlCh = make(chan []byte, 16)
	hub.Register(client)
	require.Eventually(t, func() bool { return hub.HasClient(client.ID) }, 2*time.Second, 10*time.Millisecond)

	// 数据 lane 洪峰塞满
	for i := 0; i < 4; i++ {
		client.SendChan <- []byte(`{"flood":true}`)
	}

	// 控制消息全部入 lane（不受数据洪峰影响）
	for i := 0; i < 8; i++ {
		terminate := models.NewHubMessage().SetMessageType(models.MessageTypeTerminate)
		assert.True(t, hub.SendControlMessage(client, terminate), "第 %d 条控制消息应送达", i)
	}

	// 端到端验收：写泵并发消费双 lane（控制帧优先），8 条控制帧最终全部到达客户端。
	// 注：len(CtrlCh) 是与写泵消费的竞态快照，不能作为送达断言依据
	cConn.SetReadDeadline(time.Now().Add(3 * time.Second))
	terminated := 0
	for terminated < 8 {
		_, data, err := cConn.ReadMessage()
		if err != nil {
			t.Fatalf("第 %d 条控制帧读取失败（%v）", terminated+1, err)
		}
		if strings.Contains(string(data), `"terminate"`) {
			terminated++
		}
	}
}

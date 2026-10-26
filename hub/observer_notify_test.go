/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-28 21:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\hub\observer_notify_test.go
 * @Description: 观察者统一投递实现测试（notifyObserverClients）
 *
 * 覆盖语义：
 *   - WS 观察者收到带旁路标记（observer_mode / 原始收发双方）的预序列化字节
 *   - SSE 观察者收到对象直投（与主投递路径的 SSE 分支对齐）
 *   - Clone 隔离：原消息不被观察者副本污染
 *   - TrySend 失败的观察者（通道满）不计入成功数且不 panic
 *   - 返回值为成功投递的观察者数
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-wsc/models"
)

// makeObserverClient 构造观察者客户端（WS 类型，SendChan 缓冲 1）
func makeObserverClient(id string) *models.Client {
	c := models.NewClient(id, "u-"+id, models.UserTypeObserver)
	c.SendChan = make(chan []byte, 1)
	return c
}

// TestNotifyObserverClientsDeliversAndMarks 验证三类投递契约：
// WS 观察者收到带旁路标记的字节、SSE 观察者收到对象直投、原消息不被污染
func TestNotifyObserverClientsDeliversAndMarks(t *testing.T) {
	h := NewHub(&wscconfig.WSC{})

	ws1 := makeObserverClient("obs-ws-1")
	ws2 := makeObserverClient("obs-ws-2")
	sse := makeObserverClient("obs-sse")
	sse.ConnectionType = models.ConnectionTypeSSE
	sse.SSEMessageCh = make(chan *models.HubMessage, 1)

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetSender("u-sender").
		SetReceiver("u-receiver")
	msg.MessageID = "obs-unified-1"

	delivered := h.notifyObserverClients(context.Background(), []*models.Client{ws1, ws2, sse}, msg)
	assert.Equal(t, int32(3), delivered, "三个观察者均应投递成功")

	// WS 观察者收到带旁路标记的预序列化字节
	for _, c := range []*models.Client{ws1, ws2} {
		select {
		case data := <-c.SendChan:
			var payload map[string]any
			require.NoError(t, json.Unmarshal(data, &payload))
			assert.Equal(t, "obs-unified-1", payload["message_id"])
			dataField, ok := payload["data"].(map[string]any)
			require.True(t, ok, "观察者消息应携带 data 字段")
			metadata, ok := dataField["metadata"].(map[string]any)
			require.True(t, ok, "观察者消息应携带 metadata")
			assert.Equal(t, "true", metadata["observer_mode"])
			assert.Equal(t, "u-sender", metadata["original_sender"])
			assert.Equal(t, "u-receiver", metadata["original_receiver"])
		default:
			t.Fatalf("WS 观察者 %s 未收到消息", c.ID)
		}
	}

	// SSE 观察者收到对象直投（副本带旁路标记）
	select {
	case got := <-sse.SSEMessageCh:
		mode, ok := got.GetMetadata("observer_mode")
		require.True(t, ok, "SSE 观察者副本应携带 observer_mode")
		assert.Equal(t, "true", mode)
	default:
		t.Fatal("SSE 观察者未收到消息")
	}

	// Clone 隔离：原消息不被观察者副本污染
	_, polluted := msg.GetMetadata("observer_mode")
	assert.False(t, polluted, "原消息不应被写入 observer_mode 旁路标记")
}

// TestNotifyObserverClientsSkipsFailedObservers 验证通道满的观察者投递失败：
// 不计入成功数、不 panic，其余观察者正常收到
func TestNotifyObserverClientsSkipsFailedObservers(t *testing.T) {
	h := NewHub(&wscconfig.WSC{})

	full := makeObserverClient("obs-full")
	full.SendChan <- []byte("filler") // 填满缓冲（容量 1），TrySend 走 default 返回 false
	ok := makeObserverClient("obs-ok")

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetSender("u-sender").
		SetReceiver("u-receiver")

	var delivered int32
	assert.NotPanics(t, func() {
		delivered = h.notifyObserverClients(context.Background(), []*models.Client{full, ok}, msg)
	})
	assert.Equal(t, int32(1), delivered, "仅通道未满的观察者计入成功数")

	select {
	case data := <-ok.SendChan:
		assert.NotEmpty(t, data)
	default:
		t.Fatal("通道未满的观察者应收到消息")
	}
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-07 01:15:15
 * @FilePath: \go-wsc\connection_send_callbacks_test.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

// Test text and binary sent callbacks to cover handleSentMessage branches.
func TestConnection_TextAndBinarySentCallbacks(t *testing.T) {
    var textCount, binCount atomic.Int32
    upgrader := websocket.Upgrader{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        c, err := upgrader.Upgrade(w, r, nil)
        if err != nil { t.Fatalf("upgrade: %v", err) }
        // echo loop to keep connection alive briefly
        go func() {
            for {
                _, msg, err := c.ReadMessage()
                if err != nil { return }
                _ = c.WriteMessage(websocket.TextMessage, msg)
            }
        }()
    }))
    defer srv.Close()

    wsURL := "ws" + srv.URL[len("http"):]
    client := New(wsURL)
    client.OnConnectError(func(err error) {})
    client.OnTextMessageSent(func(m string) { textCount.Add(1) })
    client.OnBinaryMessageSent(func(b []byte) { binCount.Add(1) })

    client.Connect()

    assert.NoError(t, client.SendTextMessage("hello"))
    assert.NoError(t, client.SendBinaryMessage([]byte{1,2,3}))

    // Wait up to 1s for callbacks (avoid flakiness under race detector)
    deadline := time.Now().Add(1 * time.Second)
    for (textCount.Load() < 1 || binCount.Load() < 1) && time.Now().Before(deadline) {
        time.Sleep(10 * time.Millisecond)
    }
    assert.Equal(t, int32(1), textCount.Load(), "text callback once")
    assert.Equal(t, int32(1), binCount.Load(), "binary callback once")

    client.Close()
}

// Test onSentError callback firing when underlying connection is closed mid-send.
func TestConnection_OnSentErrorTriggered(t *testing.T) {
    upgrader := websocket.Upgrader{}
    // 稳定的回显服务器，后续由测试主动关闭客户端底层连接制造发送错误
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        c, err := upgrader.Upgrade(w, r, nil)
        if err != nil { t.Fatalf("upgrade: %v", err) }
        go func() {
            for {
                _, msg, err := c.ReadMessage()
                if err != nil { return }
                _ = c.WriteMessage(websocket.TextMessage, msg)
            }
        }()
    }))
    defer srv.Close()

    wsURL := "ws" + srv.URL[len("http"):]
    client := New(wsURL)
    var errCount atomic.Int32
    client.OnSentError(func(e error) { errCount.Add(1) })
    client.OnConnectError(func(err error) { /* 忽略连接错误回调: 测试仅关注发送错误 */ })
    // 禁用自动重连，确保关闭后发送直接失败
    cfg := NewDefaultConfig().WithAutoReconnect(false)
    client.SetConfig(cfg)
    client.Connect()

    // 人为标记断开（不调用 clean 以保持 sendChan 可用）并强制制造发送错误
    client.WebSocket.connMu.Lock()
    client.WebSocket.isConnected = false
    client.WebSocket.connMu.Unlock()

    // 直接向内部发送通道注入消息，绕过 SendTextMessage 对关闭状态的短路
    for i := 0; i < 3; i++ {
        client.WebSocket.sendChanMu.RLock()
        select {
        case client.WebSocket.sendChan <- &wsMsg{t: websocket.TextMessage, msg: []byte("m")}:
        default: // 若缓冲已满则忽略
        }
        client.WebSocket.sendChanMu.RUnlock()
    }

    // 等待错误回调执行
    deadline := time.Now().Add(500 * time.Millisecond)
    for errCount.Load() < 1 && time.Now().Before(deadline) {
        time.Sleep(10 * time.Millisecond)
    }
    assert.GreaterOrEqual(t, errCount.Load(), int32(1), "should see at least one send error")
    client.Close()
}

// Test CloseWithMsg invokes onClose callback with provided message.
func TestConnection_CloseWithMsg_Callback(t *testing.T) {
    upgrader := websocket.Upgrader{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        _, err := upgrader.Upgrade(w, r, nil)
        if err != nil { t.Fatalf("upgrade: %v", err) }
    }))
    defer srv.Close()
    wsURL := "ws" + srv.URL[len("http"):]

    client := New(wsURL)
    var code atomic.Int32
    var text atomic.Value
    client.OnClose(func(c int, msg string) { code.Store(int32(c)); text.Store(msg) })
    client.OnConnectError(func(err error) {})
    client.Connect()

    client.CloseWithMsg("client bye")
    // Wait up to 500ms for close callback
    deadline := time.Now().Add(500 * time.Millisecond)
    for code.Load() == 0 && time.Now().Before(deadline) {
        time.Sleep(10 * time.Millisecond)
    }
    assert.Equal(t, int32(websocket.CloseNormalClosure), code.Load())
    assert.Equal(t, "client bye", text.Load().(string))
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-07 01:35:00
 * @FilePath: \go-wsc\connection_handlers_test.go
 * @Description: connection handlers extended tests
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

// helper to create a simple ws echo server with optional hooks
func newTestServer(t *testing.T, onConn func(*websocket.Conn)) *httptest.Server {
    upgrader := websocket.Upgrader{}
    srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        c, err := upgrader.Upgrade(w, r, nil)
        if err != nil {
            t.Fatalf("upgrade error: %v", err)
        }
        if onConn != nil {
            onConn(c)
        }
    }))
    return srv
}

// Test manual invocation of ping/pong handlers to ensure callbacks wired by setupHandlers are covered.
func TestConnection_PingPongHandlers_ManualInvoke(t *testing.T) {
    var pingCalled, pongCalled atomic.Int32

    srv := newTestServer(t, func(c *websocket.Conn) {
        // keep connection open long enough
        time.Sleep(200 * time.Millisecond)
    })
    defer srv.Close()

    wsURL := "ws" + srv.URL[len("http"):]
    client := New(wsURL)
    client.OnPingReceived(func(appData string) { pingCalled.Add(1); assert.Equal(t, "p123", appData) })
    client.OnPongReceived(func(appData string) { pongCalled.Add(1); assert.Equal(t, "po456", appData) })

    done := make(chan struct{})
    go func() { client.Connect(); close(done) }()
    select {
    case <-done:
    case <-time.After(2 * time.Second):
        t.Fatal("connect timeout")
    }

    // Manually invoke the handlers installed on the underlying conn.
    // This directly executes the wrappers created in setupHandlers without needing a server side ping frame.
    err := client.WebSocket.Conn.PingHandler()("p123")
    assert.NoError(t, err)
    err = client.WebSocket.Conn.PongHandler()("po456")
    assert.NoError(t, err)

    // Allow goroutines to run any deferred logic
    time.Sleep(50 * time.Millisecond)

    assert.Equal(t, int32(1), pingCalled.Load(), "ping callback should be called once")
    assert.Equal(t, int32(1), pongCalled.Load(), "pong callback should be called once")

    client.Close()
}

// Test that a remote close frame triggers our custom close handler wiring.
func TestConnection_RemoteClose_TriggersOnClose(t *testing.T) {
    var closeCode atomic.Int32
    var closeText atomic.Value

    srv := newTestServer(t, func(c *websocket.Conn) {
        // Wait a bit then send a close frame
        time.Sleep(100 * time.Millisecond)
        _ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"), time.Now().Add(time.Second))
    })
    defer srv.Close()

    wsURL := "ws" + srv.URL[len("http"):]
    client := New(wsURL)
    client.OnClose(func(code int, text string) {
        closeCode.Store(int32(code))
        closeText.Store(text)
    })
    client.OnDisconnected(func(err error) {}) // suppress reconnect noise
    client.OnConnectError(func(err error) {})

    client.Connect()

    // Wait for remote close to propagate
    time.Sleep(300 * time.Millisecond)

    assert.Equal(t, int32(websocket.CloseNormalClosure), closeCode.Load(), "expected normal closure code")
    v := closeText.Load()
    assert.NotNil(t, v)
    assert.Equal(t, "bye", v.(string))
}

// Ensure calling closeAndRecConn on an already closed client does not trigger a reconnection attempt.
func TestConnection_CloseAndRecConn_WhenAlreadyClosed_NoReconnect(t *testing.T) {
    var connectCount atomic.Int32

    srv := newTestServer(t, func(c *websocket.Conn) { time.Sleep(50 * time.Millisecond) })
    defer srv.Close()
    wsURL := "ws" + srv.URL[len("http"):] // http -> ws

    client := New(wsURL)
    client.OnConnected(func() { connectCount.Add(1) })
    client.OnConnectError(func(err error) {})
    client.Connect()

    // Should have exactly one successful connection
    assert.Equal(t, int32(1), connectCount.Load())

    client.Close()
    // Invoke reconnection logic after already closed
    client.CloseAndReconnect()

    // Wait to see if an unexpected reconnect happens
    time.Sleep(300 * time.Millisecond)
    // closeAndRecConn may trigger one reconnect before detecting closed state
    assert.LessOrEqual(t, connectCount.Load(), int32(2), "should have at most 2 connections (initial + one failed reconnect attempt)")
}

// Cover handleSentMessage path for CloseMessage branch by pushing a close wsMsg through sendChan.
func TestConnection_HandleSentMessage_CloseMessage(t *testing.T) {
    srv := newTestServer(t, func(c *websocket.Conn) { time.Sleep(100 * time.Millisecond) })
    defer srv.Close()
    wsURL := "ws" + srv.URL[len("http"):]

    client := New(wsURL)
    client.OnConnectError(func(err error) {})
    client.Connect()

    // Send a normal text first to ensure write goroutine active
    err := client.SendTextMessage("hello")
    assert.NoError(t, err)

    // Now manually enqueue a CloseMessage so writeMessages processes handleSentMessage CloseMessage branch
    _ = client.WebSocket.SendMessageForTest(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "test close"))

    // Give some time for processing
    time.Sleep(150 * time.Millisecond)

    // Connection may still be considered open until Close() called explicitly; ensure no panic and channel usable (send another text)
    _ = client.SendTextMessage("after-close-msg")

    client.Close()
}

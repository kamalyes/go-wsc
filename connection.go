/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-06 11:36:49
 * @FilePath: \go-wsc\connection.go
 * @Description: 连接管理逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jpillora/backoff"
)

// Closed 返回连接状态
func (wsc *Wsc) Closed() bool {
	wsc.WebSocket.connMu.RLock()
	defer wsc.WebSocket.connMu.RUnlock()
	return !wsc.WebSocket.isConnected
}

// Connect 发起连接
func (wsc *Wsc) Connect() {
	// 初始化/重置发送通道以及其关闭控制结构（支持断线重连后的再次关闭）
	wsc.WebSocket.sendChanMu.Lock()
	wsc.WebSocket.sendChan = make(chan *wsMsg, wsc.Config.MessageBufferSize) // 缓冲（替换引用）
	wsc.WebSocket.sendChanOnce = sync.Once{}
	atomic.StoreInt32(&wsc.WebSocket.sendChanClosed, 0)
	wsc.WebSocket.sendChanMu.Unlock()
	b := &backoff.Backoff{
		Min:    wsc.Config.MinRecTime,
		Max:    wsc.Config.MaxRecTime,
		Factor: wsc.Config.RecFactor,
		Jitter: true,
	}
	for {
		var err error
		nextRec := b.Duration()
		wsc.WebSocket.Conn, wsc.WebSocket.HttpResponse, err =
			wsc.WebSocket.Dialer.Dial(wsc.WebSocket.Url, wsc.WebSocket.RequestHeader)
		if err != nil {
				if f := wsc.onConnectError.Load(); f != nil {
					f.(func(error))(err)
				}
			// 重试
			time.Sleep(nextRec)
			continue
		}
		// 变更连接状态
		wsc.WebSocket.connMu.Lock()
		wsc.WebSocket.isConnected = true
		wsc.WebSocket.connMu.Unlock()
		// 连接成功回调
			if f := wsc.onConnected.Load(); f != nil {
				f.(func())()
			}
		// 设置支持接受的消息最大长度
		wsc.WebSocket.Conn.SetReadLimit(wsc.Config.MaxMessageSize)
		// 设置关闭、ping 和 pong 处理
		wsc.setupHandlers()
		// 启动读写协程
		go wsc.readMessages()
		go wsc.writeMessages()
		return
	}
}

// setupHandlers 设置关闭、ping 和 pong 的处理函数
func (wsc *Wsc) setupHandlers() {
	// 收到连接关闭信号回调
	defaultCloseHandler := wsc.WebSocket.Conn.CloseHandler()
	wsc.WebSocket.Conn.SetCloseHandler(func(code int, text string) error {
		result := defaultCloseHandler(code, text)
		wsc.clean()
		if f := wsc.onClose.Load(); f != nil {
			f.(func(int, string))(code, text)
		}
		return result
	})

	// 收到 ping 回调
	defaultPingHandler := wsc.WebSocket.Conn.PingHandler()
	wsc.WebSocket.Conn.SetPingHandler(func(appData string) error {
		if f := wsc.onPingReceived.Load(); f != nil {
			f.(func(string))(appData)
		}
		return defaultPingHandler(appData)
	})

	// 收到 pong 回调
	defaultPongHandler := wsc.WebSocket.Conn.PongHandler()
	wsc.WebSocket.Conn.SetPongHandler(func(appData string) error {
		if f := wsc.onPongReceived.Load(); f != nil {
			f.(func(string))(appData)
		}
		return defaultPongHandler(appData)
	})
}

// readMessages 启动读消息的协程
func (wsc *Wsc) readMessages() {
	for {
		messageType, message, err := wsc.WebSocket.Conn.ReadMessage()
		if err != nil {
			// 异常断线重连
		       if f := wsc.onDisconnected.Load(); f != nil {
			       f.(func(error))(err)
		       }
			wsc.closeAndRecConn()
			return
		}
		// 处理消息时加锁
		wsc.mu.Lock()
		switch messageType {
		case websocket.TextMessage:
			if f := wsc.onTextMessageReceived.Load(); f != nil {
				f.(func(string))(string(message))
			}
		case websocket.BinaryMessage:
			if f := wsc.onBinaryMessageReceived.Load(); f != nil {
				f.(func([]byte))(message)
			}
		}
		wsc.mu.Unlock()
	}
}

// writeMessages 启动写消息的协程
// 该方法不断从发送消息的通道中读取消息，并将其发送到 WebSocket 连接中
func (wsc *Wsc) writeMessages() {
	// 捕获当前的 sendChan 引用（读锁保护期间读取）
	wsc.WebSocket.sendChanMu.RLock()
	sendChan := wsc.WebSocket.sendChan
	wsc.WebSocket.sendChanMu.RUnlock()
	for wsMsg := range sendChan {
		// 尝试发送消息
		if err := wsc.send(wsMsg.t, wsMsg.msg); err != nil {
			// 如果发送出错，调用错误回调（如果已设置）
			if f := wsc.onSentError.Load(); f != nil {
				f.(func(error))(err)
			}
			continue // 继续处理下一个消息
		}

		// 处理已发送消息时加锁
		wsc.mu.Lock()
		// 根据消息类型处理后续逻辑
		wsc.handleSentMessage(wsMsg)
		wsc.mu.Unlock()
	}
}

// handleSentMessage 处理已发送消息的后续逻辑
// 参数 wsMsg: 发送的消息结构
func (wsc *Wsc) handleSentMessage(wsMsg *wsMsg) {
	switch wsMsg.t {
	case websocket.CloseMessage:
		// 如果发送的是关闭消息，则退出写协程
		return
	case websocket.TextMessage:
		// 如果发送的是文本消息，调用文本消息发送成功的回调（如果已设置）
		if f := wsc.onTextMessageSent.Load(); f != nil {
			f.(func(string))(string(wsMsg.msg))
		}
	case websocket.BinaryMessage:
		// 如果发送的是二进制消息，调用二进制消息发送成功的回调（如果已设置）
		if f := wsc.onBinaryMessageSent.Load(); f != nil {
			f.(func([]byte))(wsMsg.msg)
		}
	}
}

// closeAndRecConn 处理断线重连
func (wsc *Wsc) closeAndRecConn() {
	if wsc.Closed() {
		return
	}
	wsc.clean()
	go wsc.Connect()
}

// Close 主动关闭连接
func (wsc *Wsc) Close() {
	wsc.CloseWithMsg("")
}

// CloseWithMsg 主动关闭连接并附带消息
func (wsc *Wsc) CloseWithMsg(msg string) {
	if wsc.Closed() {
		return
	}
	_ = wsc.send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, msg))
	wsc.clean()
	if f := wsc.onClose.Load(); f != nil {
		f.(func(int, string))(websocket.CloseNormalClosure, msg)
	}
}

// clean 清理资源
func (wsc *Wsc) clean() {
	wsc.mu.Lock()
	defer wsc.mu.Unlock() // 确保在退出时解锁
	if wsc.Closed() {
		return
	}
	wsc.WebSocket.connMu.Lock()
	wsc.WebSocket.isConnected = false
	_ = wsc.WebSocket.Conn.Close()
	// 原子关闭 sendChan（写锁保护）
	wsc.WebSocket.sendChanMu.Lock()
	wsc.WebSocket.sendChanOnce.Do(func() {
		atomic.StoreInt32(&wsc.WebSocket.sendChanClosed, 1)
		close(wsc.WebSocket.sendChan)
	})
	wsc.WebSocket.sendChanMu.Unlock()
	wsc.WebSocket.connMu.Unlock()
}

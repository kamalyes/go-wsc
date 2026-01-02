/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 20:01:00
 * @FilePath: \go-wsc\client\connection.go
 * @Description: 连接管理逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package client

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jpillora/backoff"
)

// Closed 返回连接状态
func (wsc *Wsc) Closed() bool {
	return wsc.stateMachine.CurrentState() == ConnectionStatusDisconnected
}

// Connect 发起连接
func (wsc *Wsc) Connect() {
	// 转换到连接中状态
	if err := wsc.stateMachine.TransitionTo(ConnectionStatusConnecting); err != nil {
		wsc.handleConnectError(err)
		return
	}

	wsc.initSendChannel()
	b := wsc.createBackoff()
	for {
		nextRec := b.Duration()
		if err := wsc.attemptConnection(); err != nil {
			// 转换到错误状态
			_ = wsc.stateMachine.TransitionTo(ConnectionStatusError)
			wsc.handleConnectError(err)
			time.Sleep(nextRec)
			// 转换到重连中状态
			_ = wsc.stateMachine.TransitionTo(ConnectionStatusReconnecting)
			continue
		}
		wsc.onConnectionSuccess()
		return
	}
}

// initSendChannel 初始化/重置发送通道以及其关闭控制结构（支持断线重连后的再次关闭）
func (wsc *Wsc) initSendChannel() {
	wsc.WebSocket.sendChanMu.Lock()
	// 创建新的缓冲通道(替换旧引用)
	wsc.WebSocket.sendChan = make(chan *ClientMessage, wsc.Config.MessageBufferSize)
	// 重置 sync.Once，允许重新关闭通道
	wsc.WebSocket.sendChanOnce = sync.Once{}
	// 重置关闭标志
	atomic.StoreInt32(&wsc.WebSocket.sendChanClosed, 0)
	wsc.WebSocket.sendChanMu.Unlock()
}

// createBackoff 创建退避策略
func (wsc *Wsc) createBackoff() *backoff.Backoff {
	// 根据配置创建指数退避策略，用于连接重试
	return &backoff.Backoff{
		Min:    wsc.Config.MinRecTime,
		Max:    wsc.Config.MaxRecTime,
		Factor: wsc.Config.RecFactor,
		Jitter: true,
	}
}

// attemptConnection 尝试建立连接
func (wsc *Wsc) attemptConnection() error {
	// 使用 Dialer 建立 WebSocket 连接
	var err error
	wsc.WebSocket.Conn, wsc.WebSocket.HttpResponse, err =
		wsc.WebSocket.Dialer.Dial(wsc.WebSocket.Url, wsc.WebSocket.RequestHeader)
	return err
}

// handleConnectError 处理连接错误
func (wsc *Wsc) handleConnectError(err error) {
	// 调用连接错误回调（如果已设置）
	if f := wsc.onConnectError.Load(); f != nil {
		f.(func(error))(err)
	}
}

// onConnectionSuccess 连接成功后的处理
func (wsc *Wsc) onConnectionSuccess() {
	// 变更连接状态
	wsc.setConnectedState()
	// 连接成功回调
	wsc.notifyConnected()
	// 设置支持接受的消息最大长度
	wsc.WebSocket.Conn.SetReadLimit(wsc.Config.MaxMessageSize)
	// 设置关闭、ping 和 pong 处理
	wsc.setupHandlers()
	// 启动读写协程
	go wsc.readMessages()
	go wsc.writeMessages()
}

// setConnectedState 设置连接状态为已连接
func (wsc *Wsc) setConnectedState() {
	wsc.WebSocket.connMu.Lock()
	wsc.WebSocket.isConnected = true
	wsc.WebSocket.connMu.Unlock()
	// 使用状态机管理状态
	_ = wsc.stateMachine.TransitionTo(ConnectionStatusConnected)
}

// notifyConnected 通知连接成功
func (wsc *Wsc) notifyConnected() {
	if f := wsc.onConnected.Load(); f != nil {
		f.(func())()
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
			wsc.handleReadError(err)
			return
		}
		wsc.processReceivedMessage(messageType, message)
	}
}

// handleReadError 处理读取消息时的错误
func (wsc *Wsc) handleReadError(err error) {
	// 异常断线，通知断线回调
	wsc.notifyDisconnected(err)
	// 根据配置决定是否重连
	wsc.handleReconnectOrClean()
}

// notifyDisconnected 通知断线
func (wsc *Wsc) notifyDisconnected(err error) {
	if f := wsc.onDisconnected.Load(); f != nil {
		f.(func(error))(err)
	}
}

// handleReconnectOrClean 根据配置决定是否重连
func (wsc *Wsc) handleReconnectOrClean() {
	if wsc.Config == nil || wsc.Config.AutoReconnect {
		wsc.closeAndRecConn()
	} else {
		wsc.clean()
	}
}

// processReceivedMessage 处理接收到的消息
func (wsc *Wsc) processReceivedMessage(messageType int, message []byte) {
	// 处理消息时加锁
	wsc.mu.Lock()
	defer wsc.mu.Unlock()

	// 根据消息类型分发处理
	switch messageType {
	case websocket.TextMessage:
		wsc.handleTextMessage(message)
	case websocket.BinaryMessage:
		wsc.handleBinaryMessage(message)
	}
}

// handleTextMessage 处理文本消息
func (wsc *Wsc) handleTextMessage(message []byte) {
	// 调用文本消息接收回调（如果已设置）
	if f := wsc.onTextMessageReceived.Load(); f != nil {
		f.(func(string))(string(message))
	}
}

// handleBinaryMessage 处理二进制消息
func (wsc *Wsc) handleBinaryMessage(message []byte) {
	// 调用二进制消息接收回调（如果已设置）
	if f := wsc.onBinaryMessageReceived.Load(); f != nil {
		f.(func([]byte))(message)
	}
}

// writeMessages 启动写消息的协程
// 该方法不断从发送消息的通道中读取消息，并将其发送到 WebSocket 连接中
func (wsc *Wsc) writeMessages() {
	// 捕获当前的 sendChan 引用（读锁保护期间读取）
	wsc.WebSocket.sendChanMu.RLock()
	sendChan := wsc.WebSocket.sendChan
	wsc.WebSocket.sendChanMu.RUnlock()
	for msg := range sendChan {
		// 尝试发送消息
		if err := wsc.send(msg.T, msg.Msg); err != nil {
			// 如果发送出错，调用错误回调（如果已设置）
			if f := wsc.onSentError.Load(); f != nil {
				f.(func(error))(err)
			}
			continue // 继续处理下一个消息
		}

		// 处理已发送消息时加锁
		wsc.mu.Lock()
		// 根据消息类型处理后续逻辑
		wsc.handleSentMessage(msg)
		wsc.mu.Unlock()
	}
}

// handleSentMessage 处理已发送消息的后续逻辑
// 参数 msg: 发送的消息结构
func (wsc *Wsc) handleSentMessage(msg *ClientMessage) {
	switch msg.T {
	case websocket.CloseMessage:
		// 如果发送的是关闭消息，则退出写协程
		return
	case websocket.TextMessage:
		// 如果发送的是文本消息，调用文本消息发送成功的回调（如果已设置）
		if f := wsc.onTextMessageSent.Load(); f != nil {
			f.(func(string))(string(msg.Msg))
		}
	case websocket.BinaryMessage:
		// 如果发送的是二进制消息，调用二进制消息发送成功的回调（如果已设置）
		if f := wsc.onBinaryMessageSent.Load(); f != nil {
			f.(func([]byte))(msg.Msg)
		}
	}
}

// CloseAndReconnect 处理断线重连（公开用于测试）
func (wsc *Wsc) CloseAndReconnect() {
	if wsc.Closed() {
		return
	}
	wsc.clean()
	go wsc.Connect()
}

// closeAndRecConn 内部方法，调用公有方法
func (wsc *Wsc) closeAndRecConn() {
	wsc.CloseAndReconnect()
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

	// 先转换状态为Disconnected,确保Closed()立即返回true
	_ = wsc.stateMachine.TransitionTo(ConnectionStatusDisconnected)

	if wsc.WebSocket == nil {
		return
	}

	wsc.WebSocket.connMu.Lock()
	wsc.WebSocket.isConnected = false
	if wsc.WebSocket.Conn != nil {
		_ = wsc.WebSocket.Conn.Close()
	}
	// 原子关闭 sendChan（写锁保护）
	wsc.WebSocket.sendChanMu.Lock()
	wsc.WebSocket.sendChanOnce.Do(func() {
		atomic.StoreInt32(&wsc.WebSocket.sendChanClosed, 1)
		close(wsc.WebSocket.sendChan)
	})
	wsc.WebSocket.sendChanMu.Unlock()
	wsc.WebSocket.connMu.Unlock()
}

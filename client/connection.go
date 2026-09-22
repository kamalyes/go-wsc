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

	"github.com/kamalyes/go-wsc/models"

	"github.com/gorilla/websocket"
	"github.com/jpillora/backoff"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// Closed 返回连接状态
func (c *Client) Closed() bool {
	return c.stateMachine.CurrentState() == models.ConnectionStatusDisconnected
}

// Connect 发起连接
func (c *Client) Connect() {
	// 转换到连接中状态
	if err := c.stateMachine.TransitionTo(models.ConnectionStatusConnecting); err != nil {
		c.handleConnectError(err)
		return
	}

	c.initSendChannel()
	b := c.createBackoff()
	for {
		nextRec := b.Duration()
		if err := c.attemptConnection(); err != nil {
			// 转换到错误状态
			_ = c.stateMachine.TransitionTo(models.ConnectionStatusError)
			c.handleConnectError(err)
			time.Sleep(nextRec)
			// 转换到重连中状态
			_ = c.stateMachine.TransitionTo(models.ConnectionStatusReconnecting)
			continue
		}
		c.onConnectionSuccess()
		return
	}
}

// initSendChannel 初始化/重置发送通道以及其关闭控制结构（支持断线重连后的再次关闭）
func (c *Client) initSendChannel() {
	c.WebSocket.sendChanMu.Lock()
	// 创建新的缓冲通道(替换旧引用)
	c.WebSocket.sendChan = make(chan *ClientMessage, c.Config.MessageBufferSize)
	// 重置 sync.Once，允许重新关闭通道
	c.WebSocket.sendChanOnce = sync.Once{}
	// 重置关闭标志
	atomic.StoreInt32(&c.WebSocket.sendChanClosed, 0)
	c.WebSocket.sendChanMu.Unlock()
}

// createBackoff 创建退避策略
func (c *Client) createBackoff() *backoff.Backoff {
	// 根据配置创建指数退避策略，用于连接重试
	return &backoff.Backoff{
		Min:    c.Config.MinRecTime,
		Max:    c.Config.MaxRecTime,
		Factor: c.Config.RecFactor,
		Jitter: true,
	}
}

// attemptConnection 尝试建立连接
func (c *Client) attemptConnection() error {
	// 使用 Dialer 建立 WebSocket 连接
	conn, httpResp, err := c.WebSocket.Dialer.Dial(c.WebSocket.Url, c.WebSocket.RequestHeader)
	if err != nil {
		return err
	}

	// 使用写锁保护 Conn 和 HttpResponse 的写入
	c.WebSocket.connMu.Lock()
	c.WebSocket.Conn = conn
	c.WebSocket.HttpResponse = httpResp
	c.WebSocket.connMu.Unlock()

	return nil
}

// handleConnectError 处理连接错误
func (c *Client) handleConnectError(err error) {
	// 调用连接错误回调（如果已设置）
	if f := c.onConnectError.Load(); f != nil {
		f.(func(error))(err)
	}
}

// onConnectionSuccess 连接成功后的处理
func (c *Client) onConnectionSuccess() {
	// 变更连接状态
	c.setConnectedState()
	// 连接成功回调
	c.notifyConnected()
	// 设置支持接受的消息最大长度
	c.WebSocket.Conn.SetReadLimit(c.Config.MaxMessageSize)
	// 设置关闭、ping 和 pong 处理
	c.setupHandlers()
	// 启动读写协程
	go c.readMessages()
	go c.writeMessages()
}

// setConnectedState 设置连接状态为已连接
func (c *Client) setConnectedState() {
	c.WebSocket.connMu.Lock()
	c.WebSocket.isConnected = true
	c.WebSocket.connMu.Unlock()
	// 使用状态机管理状态
	_ = c.stateMachine.TransitionTo(models.ConnectionStatusConnected)
}

// notifyConnected 通知连接成功
func (c *Client) notifyConnected() {
	if f := c.onConnected.Load(); f != nil {
		f.(func())()
	}
}

// setupHandlers 设置关闭、ping 和 pong 的处理函数
func (c *Client) setupHandlers() {
	// 收到连接关闭信号回调
	defaultCloseHandler := c.WebSocket.Conn.CloseHandler()
	c.WebSocket.Conn.SetCloseHandler(func(code int, text string) error {
		result := defaultCloseHandler(code, text)
		c.clean()
		if f := c.onClose.Load(); f != nil {
			f.(func(int, string))(code, text)
		}
		return result
	})

	// 收到 ping 回调：pong 响应走 c.send（内部 sendMu 串行化所有写操作）
	// 不用 gorilla 默认 handler——它在读协程内直接 WriteControl 写 pong，
	// 与写协程 WriteMessage 的数据帧并发写同一连接（WriteControl 的内部锁仅
	// 串行化控制帧之间），高负载下帧交错损坏连接
	c.WebSocket.Conn.SetPingHandler(func(appData string) error {
		if f := c.onPingReceived.Load(); f != nil {
			f.(func(string))(appData)
		}
		err := c.send(websocket.PongMessage, []byte(appData))
		if err == websocket.ErrCloseSent {
			return nil
		}
		return err
	})

	// 收到 pong 回调
	defaultPongHandler := c.WebSocket.Conn.PongHandler()
	c.WebSocket.Conn.SetPongHandler(func(appData string) error {
		if f := c.onPongReceived.Load(); f != nil {
			f.(func(string))(appData)
		}
		return defaultPongHandler(appData)
	})
}

// readMessages 启动读消息的协程
func (c *Client) readMessages() {
	for {
		messageType, message, err := c.WebSocket.Conn.ReadMessage()
		if err != nil {
			c.handleReadError(err)
			return
		}
		c.processReceivedMessage(messageType, message)
	}
}

// handleReadError 处理读取消息时的错误
func (c *Client) handleReadError(err error) {
	// 异常断线，通知断线回调
	c.notifyDisconnected(err)
	// 根据配置决定是否重连
	c.handleReconnectOrClean()
}

// notifyDisconnected 通知断线
func (c *Client) notifyDisconnected(err error) {
	if f := c.onDisconnected.Load(); f != nil {
		f.(func(error))(err)
	}
}

// handleReconnectOrClean 根据配置决定是否重连
func (c *Client) handleReconnectOrClean() {
	if c.Config == nil || c.Config.AutoReconnect {
		c.closeAndRecConn()
	} else {
		c.clean()
	}
}

// processReceivedMessage 处理接收到的消息
func (c *Client) processReceivedMessage(messageType int, message []byte) {
	// 处理消息时加锁
	syncx.WithLock(&c.mu, func() {
		// 根据消息类型分发处理
		switch messageType {
		case websocket.TextMessage:
			c.handleTextMessage(message)
		case websocket.BinaryMessage:
			c.handleBinaryMessage(message)
		}
	})
}

// handleTextMessage 处理文本消息
func (c *Client) handleTextMessage(message []byte) {
	// 调用文本消息接收回调（如果已设置）
	if f := c.onTextMessageReceived.Load(); f != nil {
		f.(func(string))(string(message))
	}
}

// handleBinaryMessage 处理二进制消息
func (c *Client) handleBinaryMessage(message []byte) {
	// 调用二进制消息接收回调（如果已设置）
	if f := c.onBinaryMessageReceived.Load(); f != nil {
		f.(func([]byte))(message)
	}
}

// writeMessages 启动写消息的协程
// 该方法不断从发送消息的通道中读取消息，并将其发送到 WebSocket 连接中
func (c *Client) writeMessages() {
	// 捕获当前的 sendChan 引用（读锁保护期间读取）
	c.WebSocket.sendChanMu.RLock()
	sendChan := c.WebSocket.sendChan
	c.WebSocket.sendChanMu.RUnlock()
	for msg := range sendChan {
		// 尝试发送消息
		if err := c.send(msg.T, msg.Msg); err != nil {
			// 如果发送出错，调用错误回调（如果已设置）
			if f := c.onSentError.Load(); f != nil {
				f.(func(error))(err)
			}
			continue // 继续处理下一个消息
		}

		// 处理已发送消息时加锁
		c.mu.Lock()
		// 根据消息类型处理后续逻辑
		c.handleSentMessage(msg)
		c.mu.Unlock()
	}
}

// handleSentMessage 处理已发送消息的后续逻辑
// 参数 msg: 发送的消息结构
func (c *Client) handleSentMessage(msg *ClientMessage) {
	switch msg.T {
	case websocket.CloseMessage:
		// 如果发送的是关闭消息，则退出写协程
		return
	case websocket.TextMessage:
		// 如果发送的是文本消息，调用文本消息发送成功的回调（如果已设置）
		if f := c.onTextMessageSent.Load(); f != nil {
			f.(func(string))(string(msg.Msg))
		}
	case websocket.BinaryMessage:
		// 如果发送的是二进制消息，调用二进制消息发送成功的回调（如果已设置）
		if f := c.onBinaryMessageSent.Load(); f != nil {
			f.(func([]byte))(msg.Msg)
		}
	}
}

// CloseAndReconnect 处理断线重连（公开用于测试）
func (c *Client) CloseAndReconnect() {
	if c.Closed() {
		return
	}
	c.clean()
	go c.Connect()
}

// closeAndRecConn 内部方法，调用公有方法
func (c *Client) closeAndRecConn() {
	c.CloseAndReconnect()
}

// Close 主动关闭连接
func (c *Client) Close() {
	c.CloseWithMsg("")
}

// CloseWithMsg 主动关闭连接并附带消息
func (c *Client) CloseWithMsg(msg string) {
	if c.Closed() {
		return
	}
	_ = c.send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, msg))
	c.clean()
	if f := c.onClose.Load(); f != nil {
		f.(func(int, string))(websocket.CloseNormalClosure, msg)
	}
}

// clean 清理资源
func (c *Client) clean() {
	syncx.WithLock(&c.mu, func() {
		// 先转换状态为Disconnected,确保Closed()立即返回true
		_ = c.stateMachine.TransitionTo(models.ConnectionStatusDisconnected)

		if c.WebSocket == nil {
			return
		}

		c.WebSocket.connMu.Lock()
		c.WebSocket.isConnected = false
		if c.WebSocket.Conn != nil {
			_ = c.WebSocket.Conn.Close()
		}
		// 原子关闭 sendChan（写锁保护）
		c.WebSocket.sendChanMu.Lock()
		c.WebSocket.sendChanOnce.Do(func() {
			atomic.StoreInt32(&c.WebSocket.sendChanClosed, 1)
			close(c.WebSocket.sendChan)
		})
		c.WebSocket.sendChanMu.Unlock()
		c.WebSocket.connMu.Unlock()
	})
}

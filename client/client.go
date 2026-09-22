/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 16:09:00
 * @FilePath: \go-wsc\client\client.go
 * @Description: Client 客户端门面 —— 回调装配、状态查询与消息发送
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package client

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/safe"
	"github.com/kamalyes/go-toolbox/pkg/syncx"

	"github.com/kamalyes/go-wsc/models"
)

// Client WebSocket 客户端门面
// 封装连接状态机、回调装配与消息发送，连接管理细节见 connection.go
type Client struct {
	mu           sync.Mutex                                   // 互斥锁，用于保护并发访问
	Config       *wscconfig.WSC                               // 配置信息，用于配置 WebSocket 客户端的参数
	WebSocket    *WebSocket                                   // 底层 WebSocket 连接，负责实际的网络通信
	stateMachine *syncx.StateMachine[models.ConnectionStatus] // 连接状态机

	// 连接相关的回调函数
	onConnected    atomic.Value // 连接成功回调 func()
	onConnectError atomic.Value // 连接错误回调 func(error)
	onDisconnected atomic.Value // 连接断开回调 func(error)
	onClose        atomic.Value // 连接关闭回调 func(int, string)

	// 消息相关的回调函数
	onTextMessageSent       atomic.Value // 文本消息发送成功回调 func(string)
	onBinaryMessageSent     atomic.Value // 二进制消息发送成功回调 func([]byte)
	onSentError             atomic.Value // 消息发送错误回调 func(error)
	onPingReceived          atomic.Value // 接收到Ping消息回调 func(string)
	onPongReceived          atomic.Value // 接收到Pong消息回调 func(string)
	onTextMessageReceived   atomic.Value // 接收到文本消息回调 func(string)
	onBinaryMessageReceived atomic.Value // 接收到二进制消息回调 func([]byte)
}

// New 创建一个新的 Client 客户端
// 参数 url: WebSocket 服务器的地址
// 返回: 返回一个新的 Client 实例
func New(url string) *Client {
	// 初始化状态机
	sm := syncx.NewStateMachine(models.ConnectionStatusDisconnected)
	// 配置允许的状态转换
	sm.AllowTransitions(models.ConnectionStatusDisconnected, models.ConnectionStatusConnecting, models.ConnectionStatusReconnecting)
	sm.AllowTransitions(models.ConnectionStatusConnecting, models.ConnectionStatusConnected, models.ConnectionStatusDisconnected, models.ConnectionStatusError)
	sm.AllowTransitions(models.ConnectionStatusConnected, models.ConnectionStatusDisconnected, models.ConnectionStatusError)
	sm.AllowTransitions(models.ConnectionStatusReconnecting, models.ConnectionStatusConnected, models.ConnectionStatusDisconnected, models.ConnectionStatusError)
	sm.AllowTransitions(models.ConnectionStatusError, models.ConnectionStatusDisconnected, models.ConnectionStatusReconnecting)

	// 初始化 Client，使用默认配置和指定的 URL
	return &Client{
		Config:       safe.MergeWithDefaults(nil, wscconfig.Default()), // 使用safe合并默认配置
		WebSocket:    NewWebSocket(url),                                // 创建新的 WebSocket 连接
		stateMachine: sm,                                               // 设置状态机
	}
}

// SetConfig 设置客户端配置
// 参数 config: 用户自定义的配置
func (c *Client) SetConfig(config *wscconfig.WSC) {
	c.Config = config // 更新 Client 实例的配置
}

// OnConnected 设置连接成功的回调
// 参数 f: 连接成功后调用的函数
func (c *Client) OnConnected(f func()) {
	c.onConnected.Store(f)
}

// OnConnectError 设置连接出错的回调
// 参数 f: 连接出错时调用的函数，参数为错误信息
func (c *Client) OnConnectError(f func(err error)) {
	c.onConnectError.Store(f)
}

// OnDisconnected 设置连接断开的回调
// 参数 f: 连接断开时调用的函数，参数为错误信息
func (c *Client) OnDisconnected(f func(err error)) {
	c.onDisconnected.Store(f)
}

// OnClose 设置连接关闭的回调
// 参数 f: 连接关闭时调用的函数，参数为关闭代码和关闭文本
func (c *Client) OnClose(f func(code int, text string)) {
	c.onClose.Store(f)
}

// OnTextMessageSent 设置发送文本消息成功的回调
// 参数 f: 发送成功时调用的函数，参数为发送的消息
func (c *Client) OnTextMessageSent(f func(message string)) {
	c.onTextMessageSent.Store(f)
}

// OnBinaryMessageSent 设置发送二进制消息成功的回调
// 参数 f: 发送成功时调用的函数，参数为发送的数据
func (c *Client) OnBinaryMessageSent(f func(data []byte)) {
	c.onBinaryMessageSent.Store(f)
}

// OnSentError 设置发送消息出错的回调
// 参数 f: 发送出错时调用的函数，参数为错误信息
func (c *Client) OnSentError(f func(err error)) {
	c.onSentError.Store(f)
}

// OnPingReceived 设置接收到 Ping 消息的回调
// 参数 f: 接收到 Ping 消息时调用的函数，参数为应用数据
func (c *Client) OnPingReceived(f func(appData string)) {
	c.onPingReceived.Store(f)
}

// OnPongReceived 设置接收到 Pong 消息的回调
// 参数 f: 接收到 Pong 消息时调用的函数，参数为应用数据
func (c *Client) OnPongReceived(f func(appData string)) {
	c.onPongReceived.Store(f)
}

// OnTextMessageReceived 设置接收到文本消息的回调
// 参数 f: 接收到文本消息时调用的函数，参数为接收到的消息
func (c *Client) OnTextMessageReceived(f func(message string)) {
	c.onTextMessageReceived.Store(f)
}

// OnBinaryMessageReceived 设置接收到二进制消息的回调
// 参数 f: 接收到二进制消息时调用的函数，参数为接收到的数据
func (c *Client) OnBinaryMessageReceived(f func(data []byte)) {
	c.onBinaryMessageReceived.Store(f)
}

// HasOnConnectedCallback 检查是否设置了连接成功回调
func (c *Client) HasOnConnectedCallback() bool {
	return c.onConnected.Load() != nil
}

// HasOnConnectErrorCallback 检查是否设置了连接错误回调
func (c *Client) HasOnConnectErrorCallback() bool {
	return c.onConnectError.Load() != nil
}

// HasOnDisconnectedCallback 检查是否设置了连接断开回调
func (c *Client) HasOnDisconnectedCallback() bool {
	return c.onDisconnected.Load() != nil
}

// HasOnCloseCallback 检查是否设置了连接关闭回调
func (c *Client) HasOnCloseCallback() bool {
	return c.onClose.Load() != nil
}

// HasOnTextMessageSentCallback 检查是否设置了文本消息发送成功回调
func (c *Client) HasOnTextMessageSentCallback() bool {
	return c.onTextMessageSent.Load() != nil
}

// HasOnBinaryMessageSentCallback 检查是否设置了二进制消息发送成功回调
func (c *Client) HasOnBinaryMessageSentCallback() bool {
	return c.onBinaryMessageSent.Load() != nil
}

// HasOnSentErrorCallback 检查是否设置了发送错误回调
func (c *Client) HasOnSentErrorCallback() bool {
	return c.onSentError.Load() != nil
}

// HasOnPingReceivedCallback 检查是否设置了Ping接收回调
func (c *Client) HasOnPingReceivedCallback() bool {
	return c.onPingReceived.Load() != nil
}

// HasOnPongReceivedCallback 检查是否设置了Pong接收回调
func (c *Client) HasOnPongReceivedCallback() bool {
	return c.onPongReceived.Load() != nil
}

// HasOnTextMessageReceivedCallback 检查是否设置了文本消息接收回调
func (c *Client) HasOnTextMessageReceivedCallback() bool {
	return c.onTextMessageReceived.Load() != nil
}

// HasOnBinaryMessageReceivedCallback 检查是否设置了二进制消息接收回调
func (c *Client) HasOnBinaryMessageReceivedCallback() bool {
	return c.onBinaryMessageReceived.Load() != nil
}

// GetConnectionStatus 获取当前连接状态
func (c *Client) GetConnectionStatus() models.ConnectionStatus {
	return c.stateMachine.CurrentState()
}

// IsConnected 检查是否已连接
func (c *Client) IsConnected() bool {
	return c.stateMachine.CurrentState() == models.ConnectionStatusConnected
}

// IsConnecting 检查是否正在连接
func (c *Client) IsConnecting() bool {
	state := c.stateMachine.CurrentState()
	return state == models.ConnectionStatusConnecting || state == models.ConnectionStatusReconnecting
}

// DefaultUpgrader 返回默认的WebSocket升级器
var DefaultUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源
	},
}

// IsNormalClose 检查WebSocket关闭是否为正常关闭
func IsNormalClose(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway)
}

// ClientMessage 结构体表示 WebSocket 消息(公开用于测试)
type ClientMessage struct {
	T   int    // 消息类型
	Msg []byte // 消息内容
}

// SendTextMessage 发送文本消息
func (c *Client) SendTextMessage(message string) error {
	if c.Closed() {
		return models.ErrConnectionClosed
	}
	// 读锁保护 sendChan 指针与关闭标志一致性
	c.WebSocket.sendChanMu.RLock()
	defer c.WebSocket.sendChanMu.RUnlock()
	if atomic.LoadInt32(&c.WebSocket.sendChanClosed) == 1 {
		return models.ErrConnectionClosed
	}
	select {
	case c.WebSocket.sendChan <- &ClientMessage{
		T:   websocket.TextMessage,
		Msg: []byte(message),
	}:
		return nil
	default:
		return models.ErrMessageBufferFull
	}
}

// SendBinaryMessage 发送二进制消息
func (c *Client) SendBinaryMessage(data []byte) error {
	if c.Closed() {
		return models.ErrConnectionClosed
	}
	// 读锁保护 sendChan 指针与关闭标志一致性
	c.WebSocket.sendChanMu.RLock()
	defer c.WebSocket.sendChanMu.RUnlock()
	if atomic.LoadInt32(&c.WebSocket.sendChanClosed) == 1 {
		return models.ErrConnectionClosed
	}
	select {
	case c.WebSocket.sendChan <- &ClientMessage{
		T:   websocket.BinaryMessage,
		Msg: data,
	}:
		return nil
	default:
		return models.ErrMessageBufferFull
	}
}

// send 发送消息到连接端
func (c *Client) send(messageType int, data []byte) error {
	c.WebSocket.sendMu.Lock()
	defer c.WebSocket.sendMu.Unlock()

	// 使用读锁保护连接状态和 Conn 的访问
	c.WebSocket.connMu.RLock()
	if !c.WebSocket.isConnected {
		c.WebSocket.connMu.RUnlock()
		return models.ErrConnectionClosed
	}
	conn := c.WebSocket.Conn
	c.WebSocket.connMu.RUnlock()

	// 设置写超时
	_ = conn.SetWriteDeadline(time.Now().Add(c.Config.WriteTimeout))
	return conn.WriteMessage(messageType, data)
}

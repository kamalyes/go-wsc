/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 20:05:53
 * @FilePath: \go-wsc\client\wsc.go
 * @Description: Wsc 结构体及其方法
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
)

// Wsc 结构体表示 WebSocket 客户端
// Wsc 结构体封装了 WebSocket 连接的管理及其相关操作
type Wsc struct {
	mu           sync.Mutex                            // 互斥锁，用于保护并发访问
	Config       *wscconfig.WSC                        // 配置信息，用于配置 WebSocket 客户端的参数
	WebSocket    *WebSocket                            // 底层 WebSocket 连接，负责实际的网络通信
	stateMachine *syncx.StateMachine[ConnectionStatus] // 连接状态机

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

// New 创建一个新的 Wsc 客户端
// 参数 url: WebSocket 服务器的地址
// 返回: 返回一个新的 Wsc 实例
func New(url string) *Wsc {
	// 初始化状态机
	sm := syncx.NewStateMachine(ConnectionStatusDisconnected)
	// 配置允许的状态转换
	sm.AllowTransitions(ConnectionStatusDisconnected, ConnectionStatusConnecting, ConnectionStatusReconnecting)
	sm.AllowTransitions(ConnectionStatusConnecting, ConnectionStatusConnected, ConnectionStatusDisconnected, ConnectionStatusError)
	sm.AllowTransitions(ConnectionStatusConnected, ConnectionStatusDisconnected, ConnectionStatusError)
	sm.AllowTransitions(ConnectionStatusReconnecting, ConnectionStatusConnected, ConnectionStatusDisconnected, ConnectionStatusError)
	sm.AllowTransitions(ConnectionStatusError, ConnectionStatusDisconnected, ConnectionStatusReconnecting)

	// 初始化 Wsc 客户端，使用默认配置和指定的 URL
	return &Wsc{
		Config:       safe.MergeWithDefaults(nil, wscconfig.Default()), // 使用safe合并默认配置
		WebSocket:    NewWebSocket(url),                                // 创建新的 WebSocket 连接
		stateMachine: sm,                                               // 设置状态机
	}
}

// SetConfig 设置客户端配置
// 参数 config: 用户自定义的配置
func (wsc *Wsc) SetConfig(config *wscconfig.WSC) {
	wsc.Config = config // 更新 Wsc 实例的配置
}

// OnConnected 设置连接成功的回调
// 参数 f: 连接成功后调用的函数
func (wsc *Wsc) OnConnected(f func()) {
	wsc.onConnected.Store(f)
}

// OnConnectError 设置连接出错的回调
// 参数 f: 连接出错时调用的函数，参数为错误信息
func (wsc *Wsc) OnConnectError(f func(err error)) {
	wsc.onConnectError.Store(f)
}

// OnDisconnected 设置连接断开的回调
// 参数 f: 连接断开时调用的函数，参数为错误信息
func (wsc *Wsc) OnDisconnected(f func(err error)) {
	wsc.onDisconnected.Store(f)
}

// OnClose 设置连接关闭的回调
// 参数 f: 连接关闭时调用的函数，参数为关闭代码和关闭文本
func (wsc *Wsc) OnClose(f func(code int, text string)) {
	wsc.onClose.Store(f)
}

// OnTextMessageSent 设置发送文本消息成功的回调
// 参数 f: 发送成功时调用的函数，参数为发送的消息
func (wsc *Wsc) OnTextMessageSent(f func(message string)) {
	wsc.onTextMessageSent.Store(f)
}

// OnBinaryMessageSent 设置发送二进制消息成功的回调
// 参数 f: 发送成功时调用的函数，参数为发送的数据
func (wsc *Wsc) OnBinaryMessageSent(f func(data []byte)) {
	wsc.onBinaryMessageSent.Store(f)
}

// OnSentError 设置发送消息出错的回调
// 参数 f: 发送出错时调用的函数，参数为错误信息
func (wsc *Wsc) OnSentError(f func(err error)) {
	wsc.onSentError.Store(f)
}

// OnPingReceived 设置接收到 Ping 消息的回调
// 参数 f: 接收到 Ping 消息时调用的函数，参数为应用数据
func (wsc *Wsc) OnPingReceived(f func(appData string)) {
	wsc.onPingReceived.Store(f)
}

// OnPongReceived 设置接收到 Pong 消息的回调
// 参数 f: 接收到 Pong 消息时调用的函数，参数为应用数据
func (wsc *Wsc) OnPongReceived(f func(appData string)) {
	wsc.onPongReceived.Store(f)
}

// OnTextMessageReceived 设置接收到文本消息的回调
// 参数 f: 接收到文本消息时调用的函数，参数为接收到的消息
func (wsc *Wsc) OnTextMessageReceived(f func(message string)) {
	wsc.onTextMessageReceived.Store(f)
}

// OnBinaryMessageReceived 设置接收到二进制消息的回调
// 参数 f: 接收到二进制消息时调用的函数，参数为接收到的数据
func (wsc *Wsc) OnBinaryMessageReceived(f func(data []byte)) {
	wsc.onBinaryMessageReceived.Store(f)
}

// HasOnConnectedCallback 检查是否设置了连接成功回调
func (wsc *Wsc) HasOnConnectedCallback() bool {
	return wsc.onConnected.Load() != nil
}

// HasOnConnectErrorCallback 检查是否设置了连接错误回调
func (wsc *Wsc) HasOnConnectErrorCallback() bool {
	return wsc.onConnectError.Load() != nil
}

// HasOnDisconnectedCallback 检查是否设置了连接断开回调
func (wsc *Wsc) HasOnDisconnectedCallback() bool {
	return wsc.onDisconnected.Load() != nil
}

// HasOnCloseCallback 检查是否设置了连接关闭回调
func (wsc *Wsc) HasOnCloseCallback() bool {
	return wsc.onClose.Load() != nil
}

// HasOnTextMessageSentCallback 检查是否设置了文本消息发送成功回调
func (wsc *Wsc) HasOnTextMessageSentCallback() bool {
	return wsc.onTextMessageSent.Load() != nil
}

// HasOnBinaryMessageSentCallback 检查是否设置了二进制消息发送成功回调
func (wsc *Wsc) HasOnBinaryMessageSentCallback() bool {
	return wsc.onBinaryMessageSent.Load() != nil
}

// HasOnSentErrorCallback 检查是否设置了发送错误回调
func (wsc *Wsc) HasOnSentErrorCallback() bool {
	return wsc.onSentError.Load() != nil
}

// HasOnPingReceivedCallback 检查是否设置了Ping接收回调
func (wsc *Wsc) HasOnPingReceivedCallback() bool {
	return wsc.onPingReceived.Load() != nil
}

// HasOnPongReceivedCallback 检查是否设置了Pong接收回调
func (wsc *Wsc) HasOnPongReceivedCallback() bool {
	return wsc.onPongReceived.Load() != nil
}

// HasOnTextMessageReceivedCallback 检查是否设置了文本消息接收回调
func (wsc *Wsc) HasOnTextMessageReceivedCallback() bool {
	return wsc.onTextMessageReceived.Load() != nil
}

// HasOnBinaryMessageReceivedCallback 检查是否设置了二进制消息接收回调
func (wsc *Wsc) HasOnBinaryMessageReceivedCallback() bool {
	return wsc.onBinaryMessageReceived.Load() != nil
}

// GetConnectionStatus 获取当前连接状态
func (wsc *Wsc) GetConnectionStatus() ConnectionStatus {
	return wsc.stateMachine.CurrentState()
}

// IsConnected 检查是否已连接
func (wsc *Wsc) IsConnected() bool {
	return wsc.stateMachine.CurrentState() == ConnectionStatusConnected
}

// IsConnecting 检查是否正在连接
func (wsc *Wsc) IsConnecting() bool {
	state := wsc.stateMachine.CurrentState()
	return state == ConnectionStatusConnecting || state == ConnectionStatusReconnecting
}

// DefaultUpgrader 返回默认的WebSocket升级器
var DefaultUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源
	},
}

// IsNormalClose 检查WebSocket关闭是否为正常关闭
func IsNormalClose(err error) bool {
	if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		return true
	}
	return false
}

// ClientMessage 结构体表示 WebSocket 消息(公开用于测试)
type ClientMessage struct {
	T   int    // 消息类型
	Msg []byte // 消息内容
}

// SendTextMessage 发送文本消息
func (wsc *Wsc) SendTextMessage(message string) error {
	if wsc.Closed() {
		return ErrConnectionClosed
	}
	// 读锁保护 sendChan 指针与关闭标志一致性
	wsc.WebSocket.sendChanMu.RLock()
	defer wsc.WebSocket.sendChanMu.RUnlock()
	if atomic.LoadInt32(&wsc.WebSocket.sendChanClosed) == 1 {
		return ErrConnectionClosed
	}
	select {
	case wsc.WebSocket.sendChan <- &ClientMessage{
		T:   websocket.TextMessage,
		Msg: []byte(message),
	}:
		return nil
	default:
		return ErrMessageBufferFull
	}
}

// SendBinaryMessage 发送二进制消息
func (wsc *Wsc) SendBinaryMessage(data []byte) error {
	if wsc.Closed() {
		return ErrConnectionClosed
	}
	// 读锁保护 sendChan 指针与关闭标志一致性
	wsc.WebSocket.sendChanMu.RLock()
	defer wsc.WebSocket.sendChanMu.RUnlock()
	if atomic.LoadInt32(&wsc.WebSocket.sendChanClosed) == 1 {
		return ErrConnectionClosed
	}
	select {
	case wsc.WebSocket.sendChan <- &ClientMessage{
		T:   websocket.BinaryMessage,
		Msg: data,
	}:
		return nil
	default:
		return ErrMessageBufferFull
	}
}

// send 发送消息到连接端
func (wsc *Wsc) send(messageType int, data []byte) error {
	wsc.WebSocket.sendMu.Lock()
	defer wsc.WebSocket.sendMu.Unlock()

	// 使用读锁保护连接状态和 Conn 的访问
	wsc.WebSocket.connMu.RLock()
	if !wsc.WebSocket.isConnected {
		wsc.WebSocket.connMu.RUnlock()
		return ErrConnectionClosed
	}
	conn := wsc.WebSocket.Conn
	wsc.WebSocket.connMu.RUnlock()

	// 设置写超时
	_ = conn.SetWriteDeadline(time.Now().Add(wsc.Config.WriteTimeout))
	return conn.WriteMessage(messageType, data)
}

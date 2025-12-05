/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-16 20:20:00
 * @FilePath: \go-wsc\wsc.go
 * @Description: Wsc 结构体及其方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/safe"
)

// Wsc 结构体表示 WebSocket 客户端
// Wsc 结构体封装了 WebSocket 连接的管理及其相关操作
type Wsc struct {
	mu        sync.Mutex     // 互斥锁，用于保护并发访问
	Config    *wscconfig.WSC // 配置信息，用于配置 WebSocket 客户端的参数
	WebSocket *WebSocket     // 底层 WebSocket 连接，负责实际的网络通信

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
	// 初始化 Wsc 客户端，使用默认配置和指定的 URL
	return &Wsc{
		Config:    safe.MergeWithDefaults[wscconfig.WSC](nil, wscconfig.Default()), // 使用safe合并默认配置
		WebSocket: NewWebSocket(url),                                               // 创建新的 WebSocket 连接
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

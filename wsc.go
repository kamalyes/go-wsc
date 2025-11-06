/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 09:55:20
 * @FilePath: \go-wsc\wsc.go
 * @Description: Wsc 结构体及其方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import "sync"

// Wsc 结构体表示 WebSocket 客户端
// Wsc 结构体封装了 WebSocket 连接的管理及其相关操作
type Wsc struct {
	mu        sync.Mutex // 互斥锁
	Config    *Config    // 配置信息，用于配置 WebSocket 客户端的参数
	WebSocket *WebSocket // 底层 WebSocket 连接，负责实际的网络通信

	// 连接相关的回调函数
	onConnected    func()                      // 连接成功时的回调函数
	onConnectError func(err error)             // 连接出错时的回调函数
	onDisconnected func(err error)             // 连接断开时的回调函数
	onClose        func(code int, text string) // 连接关闭时的回调函数

	// 消息相关的回调函数
	onTextMessageSent       func(message string) // 发送文本消息成功时的回调函数
	onBinaryMessageSent     func(data []byte)    // 发送二进制消息成功时的回调函数
	onSentError             func(err error)      // 发送消息出错时的回调函数
	onPingReceived          func(appData string) // 接收到 Ping 消息时的回调函数
	onPongReceived          func(appData string) // 接收到 Pong 消息时的回调函数
	onTextMessageReceived   func(message string) // 接收到文本消息时的回调函数
	onBinaryMessageReceived func(data []byte)    // 接收到二进制消息时的回调函数
}

// New 创建一个新的 Wsc 客户端
// 参数 url: WebSocket 服务器的地址
// 返回: 返回一个新的 Wsc 实例
func New(url string) *Wsc {
	// 初始化 Wsc 客户端，使用默认配置和指定的 URL
	return &Wsc{
		Config:    NewDefaultConfig(), // 使用默认配置
		WebSocket: NewWebSocket(url),  // 创建新的 WebSocket 连接
	}
}

// SetConfig 设置客户端配置
// 参数 config: 用户自定义的配置
func (wsc *Wsc) SetConfig(config *Config) {
	wsc.Config = config // 更新 Wsc 实例的配置
}

// OnConnected 设置连接成功的回调
// 参数 f: 连接成功后调用的函数
func (wsc *Wsc) OnConnected(f func()) {
	wsc.onConnected = f // 注册连接成功的回调
}

// OnConnectError 设置连接出错的回调
// 参数 f: 连接出错时调用的函数，参数为错误信息
func (wsc *Wsc) OnConnectError(f func(err error)) {
	wsc.onConnectError = f // 注册连接出错的回调
}

// OnDisconnected 设置连接断开的回调
// 参数 f: 连接断开时调用的函数，参数为错误信息
func (wsc *Wsc) OnDisconnected(f func(err error)) {
	wsc.onDisconnected = f // 注册连接断开的回调
}

// OnClose 设置连接关闭的回调
// 参数 f: 连接关闭时调用的函数，参数为关闭代码和关闭文本
func (wsc *Wsc) OnClose(f func(code int, text string)) {
	wsc.onClose = f // 注册连接关闭的回调
}

// OnTextMessageSent 设置发送文本消息成功的回调
// 参数 f: 发送成功时调用的函数，参数为发送的消息
func (wsc *Wsc) OnTextMessageSent(f func(message string)) {
	wsc.onTextMessageSent = f // 注册发送文本消息成功的回调
}

// OnBinaryMessageSent 设置发送二进制消息成功的回调
// 参数 f: 发送成功时调用的函数，参数为发送的数据
func (wsc *Wsc) OnBinaryMessageSent(f func(data []byte)) {
	wsc.onBinaryMessageSent = f // 注册发送二进制消息成功的回调
}

// OnSentError 设置发送消息出错的回调
// 参数 f: 发送出错时调用的函数，参数为错误信息
func (wsc *Wsc) OnSentError(f func(err error)) {
	wsc.onSentError = f // 注册发送消息出错的回调
}

// OnPingReceived 设置接收到 Ping 消息的回调
// 参数 f: 接收到 Ping 消息时调用的函数，参数为应用数据
func (wsc *Wsc) OnPingReceived(f func(appData string)) {
	wsc.onPingReceived = f // 注册接收到 Ping 消息的回调
}

// OnPongReceived 设置接收到 Pong 消息的回调
// 参数 f: 接收到 Pong 消息时调用的函数，参数为应用数据
func (wsc *Wsc) OnPongReceived(f func(appData string)) {
	wsc.onPongReceived = f // 注册接收到 Pong 消息的回调
}

// OnTextMessageReceived 设置接收到文本消息的回调
// 参数 f: 接收到文本消息时调用的函数，参数为接收到的消息
func (wsc *Wsc) OnTextMessageReceived(f func(message string)) {
	wsc.onTextMessageReceived = f // 注册接收到文本消息的回调
}

// OnBinaryMessageReceived 设置接收到二进制消息的回调
// 参数 f: 接收到二进制消息时调用的函数，参数为接收到的数据
func (wsc *Wsc) OnBinaryMessageReceived(f func(data []byte)) {
	wsc.onBinaryMessageReceived = f // 注册接收到二进制消息的回调
}

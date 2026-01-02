/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\exports_client.go
 * @Description: Client 包的类型和函数导出
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"github.com/kamalyes/go-wsc/client"
)

// ============================================================================
// Client 类型导出
// ============================================================================

type (
	Wsc           = client.Wsc
	WebSocket     = client.WebSocket
	ClientMessage = client.ClientMessage
)

// ============================================================================
// Client 函数导出
// ============================================================================

var (
	New             = client.New
	NewWebSocket    = client.NewWebSocket
	DefaultUpgrader = client.DefaultUpgrader
	IsNormalClose   = client.IsNormalClose
)

// ============================================================================
// Wsc 方法导出 - 这些方法通过 Wsc 实例调用
// ============================================================================

// 注意：以下是 Wsc 类型的方法列表，通过 Wsc 实例调用
// 例如：wsc := wsc.New(url); wsc.Connect()

// 配置方法：
// - SetConfig(config *wscconfig.WSC): 设置配置

// 回调设置方法：
// - OnConnected(f func()): 连接成功回调
// - OnConnectError(f func(err error)): 连接错误回调
// - OnDisconnected(f func(err error)): 断开连接回调
// - OnClose(f func(code int, text string)): 关闭连接回调
// - OnTextMessageSent(f func(message string)): 文本消息发送回调
// - OnBinaryMessageSent(f func(data []byte)): 二进制消息发送回调
// - OnSentError(f func(err error)): 发送错误回调
// - OnPingReceived(f func(appData string)): Ping接收回调
// - OnPongReceived(f func(appData string)): Pong接收回调
// - OnTextMessageReceived(f func(message string)): 文本消息接收回调
// - OnBinaryMessageReceived(f func(data []byte)): 二进制消息接收回调

// 回调检查方法：
// - HasOnConnectedCallback() bool: 检查是否设置了连接回调
// - HasOnConnectErrorCallback() bool: 检查是否设置了连接错误回调
// - HasOnDisconnectedCallback() bool: 检查是否设置了断开回调
// - HasOnCloseCallback() bool: 检查是否设置了关闭回调
// - HasOnTextMessageSentCallback() bool: 检查是否设置了文本消息发送回调
// - HasOnBinaryMessageSentCallback() bool: 检查是否设置了二进制消息发送回调
// - HasOnSentErrorCallback() bool: 检查是否设置了发送错误回调
// - HasOnPingReceivedCallback() bool: 检查是否设置了Ping回调
// - HasOnPongReceivedCallback() bool: 检查是否设置了Pong回调
// - HasOnTextMessageReceivedCallback() bool: 检查是否设置了文本消息接收回调
// - HasOnBinaryMessageReceivedCallback() bool: 检查是否设置了二进制消息接收回调

// 连接状态方法：
// - GetConnectionStatus() ConnectionStatus: 获取连接状态
// - IsConnected() bool: 是否已连接
// - IsConnecting() bool: 是否正在连接
// - Closed() bool: 是否已关闭

// 连接管理方法：
// - Connect(): 连接到服务器

// 消息发送方法：
// - SendTextMessage(message string) error: 发送文本消息
// - SendBinaryMessage(data []byte) error: 发送二进制消息

// ============================================================================
// WebSocket 方法导出 - 这些方法通过 WebSocket 实例调用
// ============================================================================

// 注意：以下是 WebSocket 类型的方法列表，通过 WebSocket 实例调用
// 例如：ws := wsc.NewWebSocket(url); ws.WithDialer(dialer)

// 链式配置方法：
// - WithDialer(dialer *websocket.Dialer) *WebSocket: 设置拨号器
// - WithRequestHeader(header http.Header) *WebSocket: 设置请求头
// - WithSendBufferSize(size int) *WebSocket: 设置发送缓冲区大小
// - WithCustomURL(url string) *WebSocket: 设置自定义URL

// 状态方法：
// - IsConnected() bool: 是否已连接
// - SetConnectedForTest(connected bool): 设置连接状态（测试用）

// 获取属性方法：
// - GetURL() string: 获取URL
// - GetDialer() *websocket.Dialer: 获取拨号器
// - GetRequestHeader() http.Header: 获取请求头
// - GetHttpResponse() *http.Response: 获取HTTP响应
// - GetConn() *websocket.Conn: 获取WebSocket连接
// - GetSendChan() chan *ClientMessage: 获取发送通道
// - GetSendChanCapacity() int: 获取发送通道容量
// - GetSendChanLength() int: 获取发送通道长度

// 测试方法：
// - SendMessageForTest(msgType int, data []byte) error: 发送消息（测试用）

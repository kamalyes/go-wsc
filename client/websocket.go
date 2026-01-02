/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 23:55:08
 * @FilePath: \go-wsc\client\websocket.go
 * @Description: WebSocket 结构体及其方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package client

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// WebSocket 结构体表示底层 WebSocket 连接
type WebSocket struct {
	Url            string              // 连接 URL
	Conn           *websocket.Conn     // WebSocket 连接
	Dialer         *websocket.Dialer   // WebSocket 拨号器
	RequestHeader  http.Header         // 请求头
	HttpResponse   *http.Response      // 响应体
	isConnected    bool                // 是否已连接
	connMu         *sync.RWMutex       // 连接状态锁
	sendMu         *sync.Mutex         // 发送消息锁
	sendChan       chan *ClientMessage // 发送消息缓冲池
	sendChanMu     *sync.RWMutex       // 保护 sendChan 指针和关闭操作
	sendChanClosed int32               // 发送通道关闭标记（原子）
	sendChanOnce   sync.Once           // 只关闭一次
}

// NewWebSocket 创建一个新的 WebSocket 连接
func NewWebSocket(url string) *WebSocket {
	return &WebSocket{
		Url:           url,
		Dialer:        websocket.DefaultDialer,
		RequestHeader: http.Header{},
		isConnected:   false,
		connMu:        &sync.RWMutex{},
		sendMu:        &sync.Mutex{},
		sendChanMu:    &sync.RWMutex{},
		sendChan:      make(chan *ClientMessage, 256), // 初始化发送消息缓冲池
	}
}

// WithDialer 设置自定义的 WebSocket 拨号器
func (ws *WebSocket) WithDialer(dialer *websocket.Dialer) *WebSocket {
	ws.Dialer = dialer
	return ws
}

// WithRequestHeader 设置请求头
func (ws *WebSocket) WithRequestHeader(header http.Header) *WebSocket {
	ws.RequestHeader = header
	return ws
}

// WithSendBufferSize 设置发送消息缓冲池大小
func (ws *WebSocket) WithSendBufferSize(size int) *WebSocket {
	if size > 0 {
		ws.sendChan = make(chan *ClientMessage, size)
	}
	return ws
}

// WithCustomURL 设置自定义 URL
func (ws *WebSocket) WithCustomURL(url string) *WebSocket {
	ws.Url = url
	return ws
}

// SetConnectedForTest 设置连接状态
func (ws *WebSocket) SetConnectedForTest(connected bool) {
	ws.connMu.Lock()
	defer ws.connMu.Unlock()
	ws.isConnected = connected
}

// IsConnected 获取连接状态
func (ws *WebSocket) IsConnected() bool {
	ws.connMu.RLock()
	defer ws.connMu.RUnlock()
	return ws.isConnected
}

// GetURL 获取连接 URL
func (ws *WebSocket) GetURL() string {
	return ws.Url
}

// GetDialer 获取 WebSocket 拨号器
func (ws *WebSocket) GetDialer() *websocket.Dialer {
	return ws.Dialer
}

// GetRequestHeader 获取请求头
func (ws *WebSocket) GetRequestHeader() http.Header {
	return ws.RequestHeader
}

// GetHttpResponse 获取 HTTP 响应
func (ws *WebSocket) GetHttpResponse() *http.Response {
	return ws.HttpResponse
}

// GetConn 获取 WebSocket 连接
func (ws *WebSocket) GetConn() *websocket.Conn {
	ws.connMu.RLock()
	defer ws.connMu.RUnlock()
	return ws.Conn
}

// GetSendChan 获取发送消息通道
func (ws *WebSocket) GetSendChan() chan *ClientMessage {
	ws.sendChanMu.RLock()
	defer ws.sendChanMu.RUnlock()
	return ws.sendChan
}

// GetSendChanCapacity 获取发送通道的容量
func (ws *WebSocket) GetSendChanCapacity() int {
	ws.sendChanMu.RLock()
	defer ws.sendChanMu.RUnlock()
	if ws.sendChan == nil {
		return 0
	}
	return cap(ws.sendChan)
}

// GetSendChanLength 获取发送通道的当前长度
func (ws *WebSocket) GetSendChanLength() int {
	ws.sendChanMu.RLock()
	defer ws.sendChanMu.RUnlock()
	if ws.sendChan == nil {
		return 0
	}
	return len(ws.sendChan)
}

// SendMessageForTest 向发送通道注入消息
func (ws *WebSocket) SendMessageForTest(msgType int, data []byte) error {
	ws.sendChanMu.RLock()
	defer ws.sendChanMu.RUnlock()

	select {
	case ws.sendChan <- &ClientMessage{T: msgType, Msg: data}:
		return nil
	default:
		return ErrSendChannelFull
	}
}

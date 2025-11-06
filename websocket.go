/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-06 11:21:55
 * @FilePath: \go-wsc\websocket.go
 * @Description: WebSocket 结构体及其方法
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

// WebSocket 结构体表示底层 WebSocket 连接
type WebSocket struct {
	Url           string            // 连接 URL
	Conn          *websocket.Conn   // WebSocket 连接
	Dialer        *websocket.Dialer // WebSocket 拨号器
	RequestHeader http.Header       // 请求头
	HttpResponse  *http.Response    // 响应体
	isConnected   bool              // 是否已连接
	connMu        *sync.RWMutex     // 连接状态锁
	sendMu        *sync.Mutex       // 发送消息锁
	sendChan      chan *wsMsg       // 发送消息缓冲池
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
		sendChan:      make(chan *wsMsg, 256), // 初始化发送消息缓冲池
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
		ws.sendChan = make(chan *wsMsg, size)
	}
	return ws
}

// WithCustomURL 设置自定义 URL
func (ws *WebSocket) WithCustomURL(url string) *WebSocket {
	ws.Url = url
	return ws
}

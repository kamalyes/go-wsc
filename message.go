/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-09-06 10:00:51
 * @FilePath: \go-wsc\message.go
 * @Description: 消息处理逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// wsMsg 结构体表示 WebSocket 消息
type wsMsg struct {
	t   int    // 消息类型
	msg []byte // 消息内容
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
	case wsc.WebSocket.sendChan <- &wsMsg{
		t:   websocket.TextMessage,
		msg: []byte(message),
	}:
	default:
		return ErrMessageBufferFull
	}
	return nil
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
	case wsc.WebSocket.sendChan <- &wsMsg{
		t:   websocket.BinaryMessage,
		msg: data,
	}:
	default:
		return ErrMessageBufferFull
	}
	return nil
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
	_ = conn.SetWriteDeadline(time.Now().Add(wsc.Config.WriteWait))
	return conn.WriteMessage(messageType, data)
}

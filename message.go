/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 10:00:51
 * @FilePath: \go-wsc\message.go
 * @Description: 消息处理逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
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
		return ErrClose
	}
	// 将消息放入发送缓冲区
	select {
	case wsc.WebSocket.sendChan <- &wsMsg{
		t:   websocket.TextMessage,
		msg: []byte(message),
	}:
	default:
		return ErrBufferFull
	}
	return nil
}

// SendBinaryMessage 发送二进制消息
func (wsc *Wsc) SendBinaryMessage(data []byte) error {
	if wsc.Closed() {
		return ErrClose
	}
	// 将消息放入发送缓冲区
	select {
	case wsc.WebSocket.sendChan <- &wsMsg{
		t:   websocket.BinaryMessage,
		msg: data,
	}:
	default:
		return ErrBufferFull
	}
	return nil
}

// send 发送消息到连接端
func (wsc *Wsc) send(messageType int, data []byte) error {
	wsc.WebSocket.sendMu.Lock()
	defer wsc.WebSocket.sendMu.Unlock()
	if wsc.Closed() {
		return ErrClose
	}
	// 设置写超时
	_ = wsc.WebSocket.Conn.SetWriteDeadline(time.Now().Add(wsc.Config.WriteWait))
	return wsc.WebSocket.Conn.WriteMessage(messageType, data)
}

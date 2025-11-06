/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 10:02:27
 * @FilePath: \go-wsc\connection.go
 * @Description: 连接管理逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"time"

	"github.com/gorilla/websocket"
	"github.com/jpillora/backoff"
)

// Closed 返回连接状态
func (wsc *Wsc) Closed() bool {
	wsc.WebSocket.connMu.RLock()
	defer wsc.WebSocket.connMu.RUnlock()
	return !wsc.WebSocket.isConnected
}

// Connect 发起连接
func (wsc *Wsc) Connect() {
	wsc.WebSocket.sendChan = make(chan *wsMsg, wsc.Config.MessageBufferSize) // 缓冲
	b := &backoff.Backoff{
		Min:    wsc.Config.MinRecTime,
		Max:    wsc.Config.MaxRecTime,
		Factor: wsc.Config.RecFactor,
		Jitter: true,
	}
	for {
		var err error
		nextRec := b.Duration()
		wsc.WebSocket.Conn, wsc.WebSocket.HttpResponse, err =
			wsc.WebSocket.Dialer.Dial(wsc.WebSocket.Url, wsc.WebSocket.RequestHeader)
		if err != nil {
			if wsc.onConnectError != nil {
				wsc.onConnectError(err)
			}
			// 重试
			time.Sleep(nextRec)
			continue
		}
		// 变更连接状态
		wsc.WebSocket.connMu.Lock()
		wsc.WebSocket.isConnected = true
		wsc.WebSocket.connMu.Unlock()
		// 连接成功回调
		if wsc.onConnected != nil {
			wsc.onConnected()
		}
		// 设置支持接受的消息最大长度
		wsc.WebSocket.Conn.SetReadLimit(wsc.Config.MaxMessageSize)
		// 设置关闭、ping 和 pong 处理
		wsc.setupHandlers()
		// 启动读写协程
		go wsc.readMessages()
		go wsc.writeMessages()
		return
	}
}

// setupHandlers 设置关闭、ping 和 pong 的处理函数
func (wsc *Wsc) setupHandlers() {
	// 收到连接关闭信号回调
	defaultCloseHandler := wsc.WebSocket.Conn.CloseHandler()
	wsc.WebSocket.Conn.SetCloseHandler(func(code int, text string) error {
		result := defaultCloseHandler(code, text)
		wsc.clean()
		if wsc.onClose != nil {
			wsc.onClose(code, text)
		}
		return result
	})

	// 收到 ping 回调
	defaultPingHandler := wsc.WebSocket.Conn.PingHandler()
	wsc.WebSocket.Conn.SetPingHandler(func(appData string) error {
		if wsc.onPingReceived != nil {
			wsc.onPingReceived(appData)
		}
		return defaultPingHandler(appData)
	})

	// 收到 pong 回调
	defaultPongHandler := wsc.WebSocket.Conn.PongHandler()
	wsc.WebSocket.Conn.SetPongHandler(func(appData string) error {
		if wsc.onPongReceived != nil {
			wsc.onPongReceived(appData)
		}
		return defaultPongHandler(appData)
	})
}

// readMessages 启动读消息的协程
func (wsc *Wsc) readMessages() {
	for {
		messageType, message, err := wsc.WebSocket.Conn.ReadMessage()
		if err != nil {
			// 异常断线重连
			if wsc.onDisconnected != nil {
				wsc.onDisconnected(err)
			}
			wsc.closeAndRecConn()
			return
		}
		switch messageType {
		case websocket.TextMessage:
			if wsc.onTextMessageReceived != nil {
				wsc.onTextMessageReceived(string(message))
			}
		case websocket.BinaryMessage:
			if wsc.onBinaryMessageReceived != nil {
				wsc.onBinaryMessageReceived(message)
			}
		}
	}
}

// writeMessages 启动写消息的协程
// 该方法不断从发送消息的通道中读取消息，并将其发送到 WebSocket 连接中
func (wsc *Wsc) writeMessages() {
	// 使用 for range 循环从 sendChan 中读取消息
	for wsMsg := range wsc.WebSocket.sendChan {
		// 尝试发送消息
		if err := wsc.send(wsMsg.t, wsMsg.msg); err != nil {
			// 如果发送出错，调用错误回调（如果已设置）
			if wsc.onSentError != nil {
				wsc.onSentError(err)
			}
			continue // 继续处理下一个消息
		}

		// 根据消息类型处理后续逻辑
		wsc.handleSentMessage(wsMsg)
	}
}

// handleSentMessage 处理已发送消息的后续逻辑
// 参数 wsMsg: 发送的消息结构
func (wsc *Wsc) handleSentMessage(wsMsg *wsMsg) {
	switch wsMsg.t {
	case websocket.CloseMessage:
		// 如果发送的是关闭消息，则退出写协程
		return
	case websocket.TextMessage:
		// 如果发送的是文本消息，调用文本消息发送成功的回调（如果已设置）
		if wsc.onTextMessageSent != nil {
			wsc.onTextMessageSent(string(wsMsg.msg))
		}
	case websocket.BinaryMessage:
		// 如果发送的是二进制消息，调用二进制消息发送成功的回调（如果已设置）
		if wsc.onBinaryMessageSent != nil {
			wsc.onBinaryMessageSent(wsMsg.msg)
		}
	}
}

// closeAndRecConn 处理断线重连
func (wsc *Wsc) closeAndRecConn() {
	if wsc.Closed() {
		return
	}
	wsc.clean()
	go wsc.Connect()
}

// Close 主动关闭连接
func (wsc *Wsc) Close() {
	wsc.CloseWithMsg("")
}

// CloseWithMsg 主动关闭连接并附带消息
func (wsc *Wsc) CloseWithMsg(msg string) {
	if wsc.Closed() {
		return
	}
	_ = wsc.send(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, msg))
	wsc.clean()
	if wsc.onClose != nil {
		wsc.onClose(websocket.CloseNormalClosure, msg)
	}
}

// clean 清理资源
func (wsc *Wsc) clean() {
	if wsc.Closed() {
		return
	}
	wsc.WebSocket.connMu.Lock()
	wsc.WebSocket.isConnected = false
	_ = wsc.WebSocket.Conn.Close()
	close(wsc.WebSocket.sendChan)
	wsc.WebSocket.connMu.Unlock()
}

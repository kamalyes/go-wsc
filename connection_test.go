/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2020-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2020-09-06 10:34:53
 * @FilePath: \go-wsc\connection_test.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许来自任何来源的请求
	},
}

func handleConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Println("Error while upgrading connection:", err)
		return
	}
	defer conn.Close()

	fmt.Println("Client connected")

	for {
		// 读取消息
		messageType, msg, err := conn.ReadMessage()
		if err != nil {
			fmt.Println("Error while reading message:", err)
			break
		}
		fmt.Printf("Received message: %s\n", msg)

		// 回显消息
		err = conn.WriteMessage(messageType, msg)
		if err != nil {
			fmt.Println("Error while writing message:", err)
			break
		}
	}
}

func startWebSocketServer(address string) {
	http.HandleFunc("/ws", handleConnection) // 设置处理器
	fmt.Printf("WebSocket server started at ws://%s\n", address)
	if err := http.ListenAndServe(address, nil); err != nil {
		fmt.Println("Error starting server:", err)
	}
}

// TestCloseConnection 测试连接成功&关闭连接
func TestCloseConnection(t *testing.T) {
	serverAddress := "localhost:8888"      // 服务器地址
	go startWebSocketServer(serverAddress) // 启动 WebSocket 服务器
	wsAddress := fmt.Sprintf("ws://%s/ws", serverAddress)
	client := New(wsAddress)
	client.Connect()

	assert.False(t, client.Closed())

	client.Close()

	assert.True(t, client.Closed())
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:55:57
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-09-06 10:59:57
 * @FilePath: \go-wsc\examples\wsc.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-wsc"
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

func main() {
	serverAddress := "localhost:7777"      // 服务器地址
	go startWebSocketServer(serverAddress) // 启动 WebSocket 服务器
	done := make(chan bool)
	ws := wsc.New(fmt.Sprintf("ws://%s/ws", serverAddress))

	// Optional: Custom configuration can be set here
	// ws.SetConfig(&wsc.Config{
	//     WriteWait: 10 * time.Second,
	//     MaxMessageSize: 2048,
	//     MinRecTime: 2 * time.Second,
	//     MaxRecTime: 60 * time.Second,
	//     RecFactor: 1.5,
	//     MessageBufferSize: 1024,
	// })

	// Set up callback handlers
	ws.OnConnected(func() {
		log.Println("OnConnected: ", ws.WebSocket.Url)
		// Send a message every 5 seconds after connection
		go func() {
			t := time.NewTicker(5 * time.Second)
			defer t.Stop() // Ensure the ticker is stopped when done
			for {
				select {
				case <-t.C:
					err := ws.SendTextMessage("hello")
					if err == wsc.ErrClose {
						return
					}
				}
			}
		}()
	})

	ws.OnConnectError(func(err error) {
		log.Println("OnConnectError: ", err.Error())
	})
	ws.OnDisconnected(func(err error) {
		log.Println("OnDisconnected: ", err.Error())
	})
	ws.OnClose(func(code int, text string) {
		log.Println("OnClose: ", code, text)
		done <- true
	})
	ws.OnTextMessageSent(func(message string) {
		log.Println("OnTextMessageSent: ", message)
	})
	ws.OnBinaryMessageSent(func(data []byte) {
		log.Println("OnBinaryMessageSent: ", string(data))
	})
	ws.OnSentError(func(err error) {
		log.Println("OnSentError: ", err.Error())
	})
	ws.OnPingReceived(func(appData string) {
		log.Println("OnPingReceived: ", appData)
	})
	ws.OnPongReceived(func(appData string) {
		log.Println("OnPongReceived: ", appData)
	})
	ws.OnTextMessageReceived(func(message string) {
		log.Println("OnTextMessageReceived: ", message)
	})
	ws.OnBinaryMessageReceived(func(data []byte) {
		log.Println("OnBinaryMessageReceived: ", string(data))
	})

	// Start the connection
	go ws.Connect()

	// Use a for range loop to wait for done signal
	for range done {
		return
	}
}

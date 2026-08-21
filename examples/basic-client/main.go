/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-31 10:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-31 10:30:00
 * @FilePath: \go-wsc\examples\basic-client\main.go
 * @Description: 基础 WebSocket 客户端示例
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package main

import (
	"fmt"
	"log"
	"time"

	"github.com/kamalyes/go-wsc/client"
)

func main() {
	// 2. 创建 Wsc 包装器
	wscClient := client.New("ws://localhost:8080/ws?user_id=client001")

	// 3. 设置回调处理
	wscClient.OnConnected(func() {
		fmt.Println("✅ 连接成功")
	})

	wscClient.OnTextMessageReceived(func(message string) {
		fmt.Printf("📨 收到消息: %s\n", message)
	})

	wscClient.OnDisconnected(func(err error) {
		log.Printf("❌ 连接断开: %v\n", err)
	})

	// 4. 连接到服务器（Connect 方法内部处理重连，无返回值）
	wscClient.Connect()

	// 5. 发送测试消息
	time.Sleep(1 * time.Second)
	wscClient.SendTextMessage("Hello WebSocket!")

	// 保持运行
	select {}
}

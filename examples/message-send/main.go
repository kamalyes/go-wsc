/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-31 11:25:18
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-31 11:25:18
 * @FilePath: \go-wsc\examples\message-send\main.go
 * @Description: 消息发送示例 - 单发、批量、广播
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	"fmt"
	"log"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/hub"
	"github.com/kamalyes/go-wsc/models"
)

func main() {
	ctx := context.Background()

	// 创建 Hub
	config := wscconfig.Default()
	h := hub.NewHub(config)
	go h.Run()
	h.WaitForStart()
	defer h.SafeShutdown()

	// 示例 1: 发送给单个用户
	sendToUser(ctx, h)

	// 示例 2: 批量发送
	batchSend(ctx, h)

	// 示例 3: 广播消息
	broadcast(ctx, h)

	// 示例 4: 发送给用户组
	sendToGroup(ctx, h)
}

// sendToUser 发送消息给单个用户
func sendToUser(ctx context.Context, h *hub.Hub) {
	msg := models.NewHubMessage()
	msg.MessageType = models.MessageTypeText
	msg.Content = "Hello User!"
	msg.Receiver = "user123"

	result := h.SendToUserWithRetry(ctx, "user123", msg)
	if result.Success {
		log.Printf("✅ 消息发送成功: %s\n", msg.ID)
	} else {
		log.Printf("❌ 消息发送失败: %v\n", result.FinalError)
	}
}

// batchSend 批量发送消息
func batchSend(ctx context.Context, h *hub.Hub) {
	// 创建批量发送器
	sender := h.NewBatchSender(ctx)

	// 添加消息
	for _, userID := range []string{"user1", "user2", "user3"} {
		msg := models.NewHubMessage()
		msg.MessageType = models.MessageTypeText
		msg.Content = fmt.Sprintf("Hello %s", userID)
		msg.Receiver = userID

		sender.AddMessage(userID, msg)
	}

	// 执行发送
	result := sender.Execute()
	log.Printf("📦 批量发送完成: 总用户=%d, 成功=%d, 失败=%d\n",
		result.TotalUsers, result.SuccessCount, result.FailureCount)
}

// broadcast 广播消息给所有客户端
func broadcast(ctx context.Context, h *hub.Hub) {
	msg := models.NewHubMessage()
	msg.MessageType = models.MessageTypeText
	msg.Content = "系统通知：服务器将在 10 分钟后维护"

	h.Broadcast(ctx, msg)
	log.Println("📢 广播消息已发送")
}

// sendToGroup 发送给用户组
func sendToGroup(ctx context.Context, h *hub.Hub) {
	userIDs := []string{"user1", "user2", "user3"}

	msg := models.NewHubMessage()
	msg.MessageType = models.MessageTypeText
	msg.Content = "群组消息"

	results := h.SendToMultipleUsers(ctx, userIDs, msg)

	successCount := 0
	for userID, err := range results {
		if err == nil {
			successCount++
		} else {
			log.Printf("❌ 发送给 %s 失败: %v\n", userID, err)
		}
	}

	log.Printf("✅ 群发完成: 成功=%d, 失败=%d\n", successCount, len(results)-successCount)
}

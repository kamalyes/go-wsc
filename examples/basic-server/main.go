/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-31 11:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-31 11:20:15
 * @FilePath: \go-wsc\examples\basic-server\main.go
 * @Description: 基础 WebSocket 服务端示例
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/hub"
	"github.com/kamalyes/go-wsc/models"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有来源（生产环境需要严格验证）
	},
}

func main() {
	// 1. 创建配置
	config := wscconfig.Default()
	config.NodeIP = "127.0.0.1"
	config.NodePort = 8080
	config.MessageBufferSize = 256

	// 2. 创建 Hub
	h := hub.NewHub(config)

	// 3. 设置回调处理
	h.OnClientConnect(func(ctx context.Context, client *models.Client) error {
		log.Printf("👤 客户端连接: %s (类型: %s)\n", client.UserID, client.UserType)
		return nil
	})

	h.OnClientDisconnect(func(ctx context.Context, client *models.Client, reason models.DisconnectReason) error {
		log.Printf("👋 客户端断开: %s (原因: %s)\n", client.UserID, reason)
		return nil
	})

	h.OnMessageReceived(func(ctx context.Context, client *models.Client, msg *models.HubMessage) error {
		log.Printf("📨 收到消息: %s -> %s\n", client.UserID, msg.Content)
		return nil
	})

	// 4. 启动 Hub
	go h.Run()
	h.WaitForStart()
	defer h.SafeShutdown()

	// 5. 配置 HTTP 路由
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(h, w, r)
	})

	log.Println("🚀 服务器启动: http://localhost:8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

// handleWebSocket 处理 WebSocket 连接
func handleWebSocket(h *hub.Hub, w http.ResponseWriter, r *http.Request) {
	// 获取用户ID
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "缺少user_id参数", http.StatusBadRequest)
		return
	}

	// 升级为 WebSocket 连接
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket 升级失败: %v", err)
		return
	}

	// 创建客户端
	client := &models.Client{
		ID:             "ws-" + userID + "-" + time.Now().Format("20060102150405"),
		UserID:         userID,
		UserType:       models.UserTypeCustomer,
		Conn:           conn,
		SendChan:       make(chan []byte, 256),
		ConnectedAt:    time.Now(),
		LastHeartbeat:  time.Now(),
		LastSeen:       time.Now(),
		ConnectionType: models.ConnectionTypeWebSocket,
		Context:        context.Background(),
	}

	// 注册客户端到 Hub
	h.Register(client)
}

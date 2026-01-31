/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-31 12:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-31 12:00:00
 * @FilePath: \go-wsc\examples\demo\server.go
 * @Description: 演示服务端 - 可以与客户端互相通信
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/hub"
	"github.com/kamalyes/go-wsc/models"
)

//go:embed index.html
var indexHTML []byte

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func main() {
	// 创建配置
	config := wscconfig.Default()
	config.NodeIP = "127.0.0.1"
	config.NodePort = 8080
	config.MessageBufferSize = 256

	// 创建 Hub
	h := hub.NewHub(config)

	// 设置客户端连接回调
	h.OnClientConnect(func(ctx context.Context, client *models.Client) error {
		log.Printf("✅ 客户端连接: %s\n", client.UserID)

		// 发送欢迎消息
		syncx.Go().
			OnError(func(err error) {
				log.Printf("❌ 发送欢迎消息失败: %v\n", err)
			}).
			ExecWithContext(func(ctx context.Context) error {
				time.Sleep(500 * time.Millisecond)
				msg := models.NewHubMessage()
				msg.MessageType = models.MessageTypeText
				msg.Content = "欢迎连接到服务器！"
				msg.Sender = "server"
				result := h.SendToUserWithRetry(ctx, client.UserID, msg)
				if result.FinalError != nil {
					return result.FinalError
				}
				log.Printf("📤 发送欢迎消息给 %s\n", client.UserID)
				return nil
			})

		return nil
	})

	// 设置客户端断开回调
	h.OnClientDisconnect(func(ctx context.Context, client *models.Client, reason models.DisconnectReason) error {
		log.Printf("❌ 客户端断开: %s (原因: %s)\n", client.UserID, reason)
		return nil
	})

	// 设置消息接收回调
	h.OnMessageReceived(func(ctx context.Context, client *models.Client, msg *models.HubMessage) error {
		log.Printf("📨 收到来自 %s 的消息: %s\n", client.UserID, msg.Content)

		// 回复消息
		syncx.Go().
			OnError(func(err error) {
				log.Printf("❌ 回复消息失败: %v\n", err)
			}).
			ExecWithContext(func(ctx context.Context) error {
				time.Sleep(200 * time.Millisecond)
				reply := models.NewHubMessage()
				reply.MessageType = models.MessageTypeText
				reply.Content = "服务器收到: " + msg.Content
				reply.Sender = "server"
				result := h.SendToUserWithRetry(ctx, client.UserID, reply)
				if result.FinalError != nil {
					return result.FinalError
				}
				log.Printf("📤 回复消息给 %s\n", client.UserID)
				return nil
			})

		return nil
	})

	// 启动 Hub
	go h.Run()
	h.WaitForStart()
	defer h.SafeShutdown()

	// 配置 HTTP 路由
	// 静态页面
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("📄 收到请求: %s %s", r.Method, r.URL.Path)
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		n, err := w.Write(indexHTML)
		if err != nil {
			log.Printf("❌ 写入响应失败: %v", err)
		} else {
			log.Printf("✅ 成功返回 HTML (%d bytes)", n)
		}
	})

	// WebSocket 连接
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(h, w, r)
	})

	log.Println("🚀 演示服务器启动: http://localhost:8080")
	log.Println("💡 提示: 在浏览器打开 http://localhost:8080 即可开始测试")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

// handleWebSocket 处理 WebSocket 连接
func handleWebSocket(h *hub.Hub, w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		http.Error(w, "缺少user_id参数", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket 升级失败: %v", err)
		return
	}

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

	h.Register(client)
}

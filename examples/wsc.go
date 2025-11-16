/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-16 20:40:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-16 20:40:00
 * @FilePath: \go-wsc\examples\wsc.go
 * @Description: WebSocket测试服务器示例
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package main

import (
	"context"
	"fmt"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	// 创建Hub配置
	config := wscconfig.Default().
		WithNodeIP("0.0.0.0").
		WithNodePort(8080).
		WithHeartbeatInterval(30).
		WithClientTimeout(300).
		WithMessageBufferSize(1000)

	// 创建Hub
	hub := wsc.NewHub(config)
	defer hub.Shutdown()

	// 启动Hub
	go hub.Run()

	// 等待Hub启动
	time.Sleep(100 * time.Millisecond)

	// 设置HTTP路由
	http.HandleFunc("/ws", handleWebSocket(hub))
	http.HandleFunc("/", handleHome)
	http.HandleFunc("/status", handleStatus(hub))

	// 创建HTTP服务器
	server := &http.Server{
		Addr:    ":8080",
		Handler: nil,
	}

	// 启动服务器
	go func() {
		log.Println("🚀 WebSocket服务器启动在 http://localhost:8080")
		log.Println("📡 WebSocket端点: ws://localhost:8080/ws")
		log.Println("📊 状态监控: http://localhost:8080/status")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("服务器启动失败:", err)
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("🛑 正在关闭服务器...")

	// 优雅关闭
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Fatal("服务器关闭失败:", err)
	}

	log.Println("✅ 服务器已关闭")
}

// handleWebSocket 处理WebSocket连接
func handleWebSocket(hub *wsc.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 检查WebSocket升级
		if r.Header.Get("Upgrade") != "websocket" {
			http.Error(w, "需要WebSocket连接", http.StatusBadRequest)
			return
		}

		// 获取用户ID
		userID := r.URL.Query().Get("user_id")
		if userID == "" {
			userID = fmt.Sprintf("user_%d", time.Now().UnixNano())
		}

		// 获取用户类型
		userType := wsc.UserTypeCustomer
		if r.URL.Query().Get("type") == "agent" {
			userType = wsc.UserTypeAgent
		}

		// 升级为WebSocket连接
		conn, err := wsc.DefaultUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket升级失败: %v", err)
			return
		}

		// 创建客户端
		client := &wsc.Client{
			ID:       fmt.Sprintf("client_%s_%d", userID, time.Now().UnixNano()),
			UserID:   userID,
			UserType: userType,
			Role:     wsc.UserRoleCustomer,
			Status:   wsc.UserStatusOnline,
			Conn:     conn,
			SendChan: make(chan []byte, 256),
			LastSeen: time.Now(),
			Context:  context.WithValue(context.Background(), wsc.ContextKeyUserID, userID),
		}

		if userType == wsc.UserTypeAgent {
			client.Role = wsc.UserRoleAgent
		}

		// 注册客户端到Hub
		hub.Register(client)

		log.Printf("👤 新用户连接: %s (类型: %s)", userID, userType)

		// 发送欢迎消息
		welcomeMsg := &wsc.HubMessage{
			ID:       fmt.Sprintf("welcome_%d", time.Now().UnixNano()),
			Type:     wsc.MessageTypeText,
			From:     "system",
			To:       userID,
			Content:  fmt.Sprintf("欢迎 %s! 连接成功。", userID),
			CreateAt: time.Now(),
		}

		if err := hub.SendToUser(context.Background(), userID, welcomeMsg); err != nil {
			log.Printf("发送欢迎消息失败: %v", err)
		}

		// 启动消息处理
		go handleClientMessages(hub, client)
	}
}

// handleClientMessages 处理客户端消息
func handleClientMessages(hub *wsc.Hub, client *wsc.Client) {
	defer func() {
		hub.Unregister(client)
		client.Conn.Close()
		log.Printf("👋 用户断开连接: %s", client.UserID)
	}()

	// 设置读取超时
	client.Conn.SetReadLimit(512)
	client.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	for {
		// 读取消息
		_, msgData, err := client.Conn.ReadMessage()
		if err != nil {
			if !wsc.IsNormalClose(err) {
				log.Printf("读取消息错误: %v", err)
			}
			break
		}

		// 重置读取超时
		client.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		// 处理收到的消息
		msg := &wsc.HubMessage{
			ID:       fmt.Sprintf("msg_%d", time.Now().UnixNano()),
			Type:     wsc.MessageTypeText,
			From:     client.UserID,
			Content:  string(msgData),
			CreateAt: time.Now(),
		}

		// 回显消息给发送者
		echoMsg := &wsc.HubMessage{
			ID:       fmt.Sprintf("echo_%d", time.Now().UnixNano()),
			Type:     wsc.MessageTypeText,
			From:     "system",
			To:       client.UserID,
			Content:  fmt.Sprintf("回显: %s", msg.Content),
			CreateAt: time.Now(),
		}

		if err := hub.SendToUser(context.Background(), client.UserID, echoMsg); err != nil {
			log.Printf("发送回显消息失败: %v", err)
		}

		log.Printf("📨 收到消息 [%s]: %s", client.UserID, msg.Content)
	}
}

// handleHome 处理主页
func handleHome(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html lang="zh">
<head>
    <meta http-equiv="Content-Type" content="text/html; charset=UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>go-wsc WebSocket框架演示</title>
    <style>
      * {
        margin: 0;
        padding: 0;
        box-sizing: border-box;
      }

      body {
        font-family: "Segoe UI", Tahoma, Geneva, Verdana, sans-serif;
        background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
        min-height: 100vh;
        display: flex;
        align-items: center;
        justify-content: center;
      }

      .container {
        background: rgba(255, 255, 255, 0.95);
        backdrop-filter: blur(10px);
        border-radius: 20px;
        padding: 40px;
        box-shadow: 0 15px 35px rgba(0, 0, 0, 0.1);
        max-width: 800px;
        width: 100%;
        text-align: center;
      }

      .header {
        margin-bottom: 30px;
      }

      .logo {
        font-size: 2.5em;
        font-weight: bold;
        background: linear-gradient(45deg, #667eea, #764ba2);
        -webkit-background-clip: text;
        -webkit-text-fill-color: transparent;
        margin-bottom: 10px;
      }

      .subtitle {
        color: #666;
        font-size: 1.1em;
      }

      .status-card {
        background: #f8f9fa;
        border-radius: 15px;
        padding: 20px;
        margin: 20px 0;
        display: flex;
        justify-content: space-around;
        text-align: center;
      }

      .status-item {
        flex: 1;
      }

      .status-value {
        font-size: 2em;
        font-weight: bold;
        color: #667eea;
        display: block;
      }

      .status-label {
        color: #666;
        font-size: 0.9em;
        margin-top: 5px;
      }

      .connection-config {
        background: #f8f9fa;
        border-radius: 15px;
        padding: 20px;
        margin: 20px 0;
        border: 2px solid #e9ecef;
      }

      .config-group {
        display: flex;
        gap: 10px;
        align-items: center;
        flex-wrap: wrap;
      }

      .config-group label {
        font-weight: bold;
        color: #555;
        min-width: 120px;
      }

      .url-input {
        flex: 1;
        min-width: 300px;
        padding: 10px 15px;
        border: 2px solid #e9ecef;
        border-radius: 8px;
        outline: none;
        transition: border-color 0.3s;
        font-family: monospace;
      }

      .url-input:focus {
        border-color: #667eea;
        box-shadow: 0 0 0 3px rgba(102, 126, 234, 0.1);
      }

      .connect-btn {
        padding: 10px 20px;
        background: #667eea;
        color: white;
        border: none;
        border-radius: 8px;
        cursor: pointer;
        font-weight: bold;
        transition: background 0.3s;
        min-width: 80px;
      }

      .connect-btn:hover {
        background: #5a67d8;
      }

      .connect-btn:disabled {
        background: #ccc;
        cursor: not-allowed;
      }

      .connect-btn.disconnect {
        background: #dc3545;
      }

      .connect-btn.disconnect:hover {
        background: #c82333;
      }

      .chat-container {
        background: #f8f9fa;
        border-radius: 15px;
        padding: 20px;
        margin: 20px 0;
        height: 400px;
        display: flex;
        flex-direction: column;
      }

      .messages {
        flex: 1;
        overflow-y: auto;
        border: 1px solid #e9ecef;
        border-radius: 10px;
        padding: 10px;
        margin-bottom: 15px;
        background: white;
      }

      .message {
        margin: 5px 0;
        padding: 8px 12px;
        border-radius: 8px;
        word-wrap: break-word;
      }

      .message.system {
        background: #e3f2fd;
        color: #1976d2;
      }

      .message.chat {
        background: #f3e5f5;
        color: #7b1fa2;
      }

      .message.echo {
        background: #e8f5e8;
        color: #388e3c;
      }

      .message.error {
        background: #ffebee;
        color: #d32f2f;
      }

      .input-group {
        display: flex;
        gap: 10px;
      }

      .message-input {
        flex: 1;
        padding: 12px 15px;
        border: 2px solid #e9ecef;
        border-radius: 25px;
        outline: none;
        transition: border-color 0.3s;
      }

      .message-input:focus {
        border-color: #667eea;
      }

      .send-btn {
        padding: 12px 25px;
        background: linear-gradient(45deg, #667eea, #764ba2);
        color: white;
        border: none;
        border-radius: 25px;
        cursor: pointer;
        transition: transform 0.2s;
        font-weight: bold;
      }

      .send-btn:hover {
        transform: translateY(-2px);
      }

      .send-btn:disabled {
        opacity: 0.6;
        cursor: not-allowed;
        transform: none;
      }

      .connection-status {
        display: inline-block;
        padding: 5px 15px;
        border-radius: 20px;
        font-size: 0.9em;
        font-weight: bold;
        margin: 10px 0;
      }

      .connection-status.connected {
        background: #c8e6c9;
        color: #2e7d32;
      }

      .connection-status.disconnected {
        background: #ffcdd2;
        color: #c62828;
      }

      .connection-status.connecting {
        background: #fff3e0;
        color: #ef6c00;
      }

      .endpoints {
        text-align: left;
        background: #f8f9fa;
        border-radius: 15px;
        padding: 20px;
        margin: 20px 0;
      }

      .endpoints h3 {
        color: #667eea;
        margin-bottom: 15px;
      }

      .endpoint {
        margin: 10px 0;
        font-family: monospace;
        background: white;
        padding: 10px;
        border-radius: 8px;
        border-left: 4px solid #667eea;
      }

      .method {
        display: inline-block;
        padding: 2px 8px;
        border-radius: 4px;
        font-size: 0.8em;
        font-weight: bold;
        margin-right: 10px;
      }

      .method.get {
        background: #e8f5e8;
        color: #388e3c;
      }

      .method.post {
        background: #fff3e0;
        color: #f57c00;
      }

      .method.ws {
        background: #e3f2fd;
        color: #1976d2;
      }

      @media (max-width: 768px) {
        .container {
          margin: 20px;
          padding: 20px;
        }

        .status-card {
          flex-direction: column;
          gap: 20px;
        }

        .config-group {
          flex-direction: column;
          align-items: stretch;
        }

        .config-group label {
          min-width: auto;
        }

        .url-input {
          min-width: auto;
        }

        .chat-container {
          height: 300px;
        }

        .input-group {
          flex-direction: column;
        }
      }
    </style>
  </head>
  <body>
    <div class="container">
      <div class="header">
        <div class="logo">go-wsc 🚀</div>
        <div class="subtitle">高性能 WebSocket 框架演示服务器</div>
      </div>

      <!-- 连接配置区域 -->
      <div class="connection-config">
        <div class="config-group">
          <label for="serverUrl">🌐 服务器地址:</label>
          <input type="text" id="serverUrl" class="url-input" placeholder="ws://localhost:8080/ws" />
          <button class="connect-btn" id="connectBtn" onclick="toggleConnection()">连接</button>
        </div>
      </div>

      <div class="connection-status disconnected" id="connectionStatus">🔴 未连接</div>

      <div class="status-card">
        <div class="status-item">
          <span class="status-value" id="clientCount">0</span>
          <div class="status-label">在线客户端</div>
        </div>
        <div class="status-item">
          <span class="status-value" id="messageCount">0</span>
          <div class="status-label">消息总数</div>
        </div>
        <div class="status-item">
          <span class="status-value" id="uptime">00:00:00</span>
          <div class="status-label">运行时间</div>
        </div>
      </div>

      <div class="chat-container">
        <div class="messages" id="messages"></div>
        <div class="input-group">
          <input type="text" class="message-input" id="messageInput" placeholder="输入消息..." disabled>
          <button class="send-btn" id="sendButton" disabled onclick="sendMessage()">
            发送
          </button>
        </div>
      </div>

      <div class="endpoints">
        <h3>📡 API 端点</h3>
        <div class="endpoint">
          <span class="method ws">WS</span>
          <code>ws://localhost:8080/ws</code> - WebSocket 连接
        </div>
        <div class="endpoint">
          <span class="method get">GET</span>
          <code>http://localhost:8080/status</code> - 服务器状态
        </div>
        <div class="endpoint">
          <span class="method get">GET</span>
          <code>http://localhost:8080/</code> - 主页面
        </div>
      </div>
    </div>

    <script>
      let ws = null;
      let messageCount = 0;
      let startTime = Date.now();
      let reconnectAttempts = 0;
      let reconnectTimer = null;
      let reconnectDelay = 2000;
      let isConnected = false;
      const maxReconnectAttempts = 5;

      // 获取默认WebSocket URL
      function getDefaultWebSocketUrl() {
        const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const host = window.location.host;
        return protocol + '//' + host + '/ws';
      }

      // 连接状态管理
      function updateConnectionStatus(status, message) {
        const statusElement = document.getElementById("connectionStatus");
        statusElement.className = 'connection-status ' + status;
        statusElement.textContent = message;
      }

      // 切换连接状态
      function toggleConnection() {
        if (isConnected) {
          disconnect();
        } else {
          connect();
        }
      }

      // 连接WebSocket
      function connect() {
        let url = document.getElementById('serverUrl').value.trim();
        if (!url) {
          url = getDefaultWebSocketUrl();
          document.getElementById('serverUrl').value = url;
        }

        try {
          updateConnectionStatus("connecting", "🔄 正在连接...");
          ws = new WebSocket(url);

          ws.onopen = function () {
            console.log("✅ WebSocket 连接已建立");
            updateConnectionStatus("connected", "🟢 已连接");
            isConnected = true;
            reconnectAttempts = 0;
            
            if (reconnectTimer) {
              clearTimeout(reconnectTimer);
              reconnectTimer = null;
            }

            document.getElementById("messageInput").disabled = false;
            document.getElementById("sendButton").disabled = false;
            document.getElementById("connectBtn").textContent = "断开";
            document.getElementById("connectBtn").className = "connect-btn disconnect";

            addMessage("system", '已连接到 ' + url);
          };

          ws.onmessage = function (event) {
            try {
              const message = JSON.parse(event.data);
              handleMessage(message);
              messageCount++;
              document.getElementById("messageCount").textContent = messageCount;
            } catch (e) {
              console.error("解析消息失败:", e);
              addMessage("error", "收到无效消息: " + event.data);
            }
          };

          ws.onclose = function (event) {
            console.log("🔒 WebSocket 连接关闭:", event.code, event.reason);
            
            if (!isConnected) {
              return;
            }
            
            updateConnectionStatus("disconnected", "🔴 连接已断开");
            isConnected = false;

            document.getElementById("messageInput").disabled = true;
            document.getElementById("sendButton").disabled = true;
            document.getElementById("connectBtn").textContent = "连接";
            document.getElementById("connectBtn").className = "connect-btn";

            if (event.code !== 1000 && event.code !== 1001) {
              reconnectAttempts++;
              const delay = Math.min(reconnectDelay * reconnectAttempts, 30000);
              updateConnectionStatus(
                "connecting",
                '⏱️ ' + Math.ceil(delay / 1000) + '秒后重连 (' + reconnectAttempts + '/5)'
              );

              reconnectTimer = setTimeout(() => {
                if (reconnectAttempts <= 5) {
                  connect();
                } else {
                  updateConnectionStatus("disconnected", "🔴 重连失败");
                  addMessage("error", "重连次数超限，请手动连接");
                }
              }, delay);
            }
          };

          ws.onerror = function (error) {
            console.error("❌ WebSocket 错误:", error);
            updateConnectionStatus("disconnected", "🔴 连接错误");
          };
        } catch (error) {
          console.error("创建 WebSocket 连接失败:", error);
          updateConnectionStatus("disconnected", "🔴 连接失败");
        }
      }

      // 断开连接
      function disconnect() {
        isConnected = false;
        if (reconnectTimer) {
          clearTimeout(reconnectTimer);
          reconnectTimer = null;
        }
        if (ws) {
          ws.close();
          ws = null;
        }
      }

      // 发送消息
      function sendMessage() {
        const input = document.getElementById("messageInput");
        const message = input.value.trim();

        if (message && ws && ws.readyState === WebSocket.OPEN) {
          ws.send(message);
          addMessage("user", "我: " + message);
          input.value = "";
        }
      }

      // 处理接收到的消息
      function handleMessage(message) {
        console.log("📨 收到消息:", message);

        switch (message.type) {
          case "system":
            addMessage("system", "⚙️ " + message.content);
            break;
          case "chat":
            addMessage("chat", "💬 " + message.content);
            break;
          case "echo":
            addMessage("echo", "🔄 " + message.content);
            break;
          default:
            addMessage("system", "📦 " + (message.content || JSON.stringify(message)));
        }
      }

      // 添加消息到聊天区域
      function addMessage(type, text) {
        const messagesDiv = document.getElementById("messages");
        const messageDiv = document.createElement("div");
        messageDiv.className = 'message ' + type;
        messageDiv.textContent = '[' + new Date().toLocaleTimeString() + '] ' + text;
        messagesDiv.appendChild(messageDiv);
        messagesDiv.scrollTop = messagesDiv.scrollHeight;
      }

      // 更新运行时间
      function updateUptime() {
        const elapsed = Date.now() - startTime;
        const hours = Math.floor(elapsed / 3600000);
        const minutes = Math.floor((elapsed % 3600000) / 60000);
        const seconds = Math.floor((elapsed % 60000) / 1000);

        document.getElementById("uptime").textContent = 
          hours.toString().padStart(2, '0') + ':' +
          minutes.toString().padStart(2, '0') + ':' +
          seconds.toString().padStart(2, '0');
      }

      // 获取服务器状态
      function fetchServerStatus() {
        fetch("/status")
          .then(response => response.json())
          .then(data => {
            document.getElementById("clientCount").textContent = data.clients.total || 0;
          })
          .catch(error => {
            console.error("获取服务器状态失败:", error);
          });
      }

      // 键盘事件处理
      document.getElementById("messageInput").addEventListener("keypress", function (e) {
        if (e.key === "Enter") {
          sendMessage();
        }
      });
        
      document.getElementById("serverUrl").addEventListener("keypress", function (e) {
        if (e.key === "Enter" && !isConnected) {
          toggleConnection();
        }
      });

      // 初始化
      document.addEventListener('DOMContentLoaded', function() {
        const defaultUrl = getDefaultWebSocketUrl();
        document.getElementById('serverUrl').value = defaultUrl;
        updateConnectionStatus("disconnected", "🔴 未连接");
        addMessage("system", "页面已加载，点击连接按钮开始");
      });

      // 定时更新
      setInterval(updateUptime, 1000);
      setInterval(fetchServerStatus, 5000);

      // 页面关闭时清理
      window.addEventListener("beforeunload", function () {
        disconnect();
      });
    </script>
  </body>
</html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

// handleStatus 处理状态监控
func handleStatus(hub *wsc.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := hub.GetDetailedStats()

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		fmt.Fprintf(w, `{
  "status": "running",
  "timestamp": "%s",
  "clients": {
    "total": %d,
    "websocket": %d,
    "sse": %d,
    "agents": %d,
    "tickets": %d
  },
  "messages": {
    "sent": %d,
    "received": %d,
    "broadcasts": %d,
    "queued": %d
  },
  "hub_stats": %+v,
  "uptime": %d
}`,
			time.Now().Format(time.RFC3339),
			stats.TotalClients,
			stats.WebSocketClients,
			stats.SSEClients,
			stats.AgentConnections,
			stats.TicketConnections,
			stats.MessagesSent,
			stats.MessagesReceived,
			stats.BroadcastsSent,
			stats.QueuedMessages,
			stats,
			stats.Uptime)
	}
}

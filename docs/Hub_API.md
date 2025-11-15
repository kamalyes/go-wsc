# 服务端 Hub API 参考 🏢

本文档提供 go-wsc 服务端 Hub 的完整 API 接口说明。

## Hub 创建与管理

### NewHub() *Hub

创建新的 Hub 实例。

```go
hub := wsc.NewHub()
```

### Run()

启动 Hub 运行。

```go
go hub.Run()
```

### Stop()

停止 Hub 运行。

```go
hub.Stop()
```

## 连接管理

### HandleWebSocket(hub *Hub, w http.ResponseWriter, r *http.Request)

处理 WebSocket 升级请求。

```go
http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
    wsc.HandleWebSocket(hub, w, r)
})
```

### GetConnectedClients() int

获取当前连接的客户端数量。

```go
count := hub.GetConnectedClients()
```

### GetClientByID(clientID string) *Client

根据 ID 获取客户端。

```go
client := hub.GetClientByID("client-123")
if client != nil {
    // 客户端存在
}
```

## 消息发送

### Broadcast(message []byte)

广播消息给所有连接的客户端。

```go
message := []byte("Hello Everyone!")
hub.Broadcast(message)
```

### BroadcastText(message string)

广播文本消息。

```go
hub.BroadcastText("系统公告：服务将在10分钟后维护")
```

### SendToClient(clientID string, message []byte) error

发送消息给特定客户端。

```go
err := hub.SendToClient("client-123", []byte("Hello Client!"))
if err != nil {
    log.Printf("发送失败: %v", err)
}
```

### SendTextToClient(clientID string, message string) error

发送文本消息给特定客户端。

```go
err := hub.SendTextToClient("client-123", "Hello!")
```

## 房间管理

### CreateRoom(roomID string) error

创建房间。

```go
err := hub.CreateRoom("room-001")
if err != nil {
    log.Printf("创建房间失败: %v", err)
}
```

### DeleteRoom(roomID string) error

删除房间。

```go
err := hub.DeleteRoom("room-001")
```

### JoinRoom(clientID, roomID string) error

客户端加入房间。

```go
err := hub.JoinRoom("client-123", "room-001")
```

### LeaveRoom(clientID, roomID string) error

客户端离开房间。

```go
err := hub.LeaveRoom("client-123", "room-001")
```

### BroadcastToRoom(roomID string, message []byte) error

向房间内所有客户端广播消息。

```go
message := []byte("房间公告")
err := hub.BroadcastToRoom("room-001", message)
```

### GetRoomClients(roomID string) []string

获取房间内的客户端列表。

```go
clients := hub.GetRoomClients("room-001")
for _, clientID := range clients {
    log.Printf("房间内客户端: %s", clientID)
}
```

## 事件处理

### OnClientConnected(fn func(client *Client))

设置客户端连接回调。

```go
hub.OnClientConnected(func(client *Client) {
    log.Printf("✅ 客户端连接: %s", client.ID)
})
```

### OnClientDisconnected(fn func(client *Client))

设置客户端断开连接回调。

```go
hub.OnClientDisconnected(func(client *Client) {
    log.Printf("❌ 客户端断开: %s", client.ID)
})
```

### OnMessageReceived(fn func(client *Client, message []byte))

设置消息接收回调。

```go
hub.OnMessageReceived(func(client *Client, message []byte) {
    log.Printf("📨 收到来自 %s 的消息: %s", client.ID, string(message))
})
```

### OnError(fn func(client *Client, err error))

设置错误处理回调。

```go
hub.OnError(func(client *Client, err error) {
    log.Printf("❌ 客户端 %s 发生错误: %v", client.ID, err)
})
```

## ACK 消息管理

### EnableACK(config ACKConfig)

启用 ACK 消息确认机制。

```go
ackConfig := wsc.ACKConfig{
    Timeout:        30 * time.Second,
    MaxRetries:     3,
    RetryInterval:  5 * time.Second,
}
hub.EnableACK(ackConfig)
```

### SendACKMessage(clientID string, message ACKMessage) error

发送需要确认的消息。

```go
ackMessage := wsc.ACKMessage{
    ID:      "msg-123",
    Content: "重要消息",
    Timeout: 30 * time.Second,
}
err := hub.SendACKMessage("client-123", ackMessage)
```

### GetPendingACKs(clientID string) []ACKMessage

获取客户端待确认的消息。

```go
pendingACKs := hub.GetPendingACKs("client-123")
log.Printf("客户端 %s 有 %d 条待确认消息", "client-123", len(pendingACKs))
```

## 统计信息

### GetStats() HubStats

获取 Hub 统计信息。

```go
stats := hub.GetStats()
log.Printf("连接数: %d, 房间数: %d, 消息数: %d", 
    stats.ConnectedClients, 
    stats.ActiveRooms, 
    stats.TotalMessages)
```

### ResetStats()

重置统计信息。

```go
hub.ResetStats()
```

## 配置结构

### HubConfig 结构体

```go
type HubConfig struct {
    // 最大连接数
    MaxConnections int
    
    // 读缓冲区大小
    ReadBufferSize int
    
    // 写缓冲区大小
    WriteBufferSize int
    
    // 连接超时时间
    HandshakeTimeout time.Duration
    
    // 启用压缩
    EnableCompression bool
    
    // 检查Origin
    CheckOrigin bool
}
```

### ACKConfig 结构体

```go
type ACKConfig struct {
    // ACK 超时时间
    Timeout time.Duration
    
    // 最大重试次数
    MaxRetries int
    
    // 重试间隔
    RetryInterval time.Duration
    
    // 启用离线消息
    EnableOfflineMessages bool
}
```

### HubStats 结构体

```go
type HubStats struct {
    // 连接的客户端数
    ConnectedClients int
    
    // 活跃房间数
    ActiveRooms int
    
    // 总消息数
    TotalMessages int64
    
    // 错误计数
    ErrorCount int64
    
    // 启动时间
    StartTime time.Time
}
```

## Client 结构体

### Client 属性

```go
type Client struct {
    // 客户端唯一ID
    ID string
    
    // WebSocket 连接
    Conn *websocket.Conn
    
    // 发送通道
    Send chan []byte
    
    // Hub 引用
    Hub *Hub
    
    // 连接时间
    ConnectedAt time.Time
    
    // 最后活跃时间
    LastActivity time.Time
    
    // 用户数据
    UserData map[string]interface{}
}
```

### IsConnected() bool

检查客户端是否连接。

```go
if client.IsConnected() {
    // 客户端在线
}
```

### Disconnect()

断开客户端连接。

```go
client.Disconnect()
```

### SetUserData(key string, value interface{})

设置用户数据。

```go
client.SetUserData("username", "alice")
client.SetUserData("role", "admin")
```

### GetUserData(key string) interface{}

获取用户数据。

```go
username := client.GetUserData("username")
if username != nil {
    log.Printf("用户名: %s", username.(string))
}
```

## 使用示例

### 基础 Hub 服务器

```go
package main

import (
    "log"
    "net/http"
    
    "github.com/kamalyes/go-wsc"
)

func main() {
    // 创建 Hub
    hub := wsc.NewHub()
    
    // 配置 Hub
    config := wsc.HubConfig{
        MaxConnections:    1000,
        ReadBufferSize:    1024,
        WriteBufferSize:   1024,
        HandshakeTimeout:  10 * time.Second,
        EnableCompression: true,
        CheckOrigin:       false,
    }
    hub.SetConfig(config)
    
    // 设置事件处理器
    setupHubHandlers(hub)
    
    // 启动 Hub
    go hub.Run()
    
    // 设置路由
    http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
        wsc.HandleWebSocket(hub, w, r)
    })
    
    // 启动服务器
    log.Println("🚀 WebSocket 服务器启动在端口 :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}

func setupHubHandlers(hub *wsc.Hub) {
    hub.OnClientConnected(func(client *wsc.Client) {
        log.Printf("✅ 客户端连接: %s", client.ID)
        
        // 发送欢迎消息
        hub.SendTextToClient(client.ID, "欢迎连接到 WebSocket 服务器!")
    })
    
    hub.OnClientDisconnected(func(client *wsc.Client) {
        log.Printf("❌ 客户端断开: %s", client.ID)
    })
    
    hub.OnMessageReceived(func(client *wsc.Client, message []byte) {
        log.Printf("📨 收到来自 %s 的消息: %s", client.ID, string(message))
        
        // 广播消息给所有客户端
        hub.BroadcastText(fmt.Sprintf("[%s]: %s", client.ID, string(message)))
    })
    
    hub.OnError(func(client *wsc.Client, err error) {
        log.Printf("❌ 客户端 %s 发生错误: %v", client.ID, err)
    })
}
```

### 房间聊天系统

```go
package main

import (
    "encoding/json"
    "log"
    "net/http"
    
    "github.com/kamalyes/go-wsc"
)

type ChatMessage struct {
    Type    string `json:"type"`
    Room    string `json:"room"`
    User    string `json:"user"`
    Content string `json:"content"`
}

func main() {
    hub := wsc.NewHub()
    go hub.Run()
    
    // 创建默认房间
    hub.CreateRoom("general")
    hub.CreateRoom("tech")
    hub.CreateRoom("random")
    
    hub.OnClientConnected(func(client *wsc.Client) {
        log.Printf("✅ 客户端连接: %s", client.ID)
        
        // 自动加入默认房间
        hub.JoinRoom(client.ID, "general")
        
        // 发送房间列表
        rooms := []string{"general", "tech", "random"}
        roomsJSON, _ := json.Marshal(map[string]interface{}{
            "type": "room_list",
            "rooms": rooms,
        })
        hub.SendToClient(client.ID, roomsJSON)
    })
    
    hub.OnMessageReceived(func(client *wsc.Client, message []byte) {
        var chatMsg ChatMessage
        if err := json.Unmarshal(message, &chatMsg); err != nil {
            log.Printf("❌ 解析消息失败: %v", err)
            return
        }
        
        switch chatMsg.Type {
        case "join_room":
            if err := hub.JoinRoom(client.ID, chatMsg.Room); err != nil {
                log.Printf("❌ 加入房间失败: %v", err)
                return
            }
            
            // 通知房间内其他用户
            notification := ChatMessage{
                Type:    "user_joined",
                Room:    chatMsg.Room,
                User:    chatMsg.User,
                Content: fmt.Sprintf("%s 加入了房间", chatMsg.User),
            }
            notificationJSON, _ := json.Marshal(notification)
            hub.BroadcastToRoom(chatMsg.Room, notificationJSON)
            
        case "leave_room":
            hub.LeaveRoom(client.ID, chatMsg.Room)
            
            // 通知房间内其他用户
            notification := ChatMessage{
                Type:    "user_left",
                Room:    chatMsg.Room,
                User:    chatMsg.User,
                Content: fmt.Sprintf("%s 离开了房间", chatMsg.User),
            }
            notificationJSON, _ := json.Marshal(notification)
            hub.BroadcastToRoom(chatMsg.Room, notificationJSON)
            
        case "chat_message":
            // 广播聊天消息到房间
            messageJSON, _ := json.Marshal(chatMsg)
            hub.BroadcastToRoom(chatMsg.Room, messageJSON)
            
        default:
            log.Printf("❌ 未知消息类型: %s", chatMsg.Type)
        }
    })
    
    http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
        wsc.HandleWebSocket(hub, w, r)
    })
    
    log.Println("🚀 聊天服务器启动在端口 :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

### ACK 消息示例

```go
package main

import (
    "log"
    "time"
    
    "github.com/kamalyes/go-wsc"
)

func main() {
    hub := wsc.NewHub()
    
    // 启用 ACK 机制
    ackConfig := wsc.ACKConfig{
        Timeout:        30 * time.Second,
        MaxRetries:     3,
        RetryInterval:  5 * time.Second,
        EnableOfflineMessages: true,
    }
    hub.EnableACK(ackConfig)
    
    go hub.Run()
    
    hub.OnClientConnected(func(client *wsc.Client) {
        log.Printf("✅ 客户端连接: %s", client.ID)
        
        // 发送需要确认的重要消息
        ackMessage := wsc.ACKMessage{
            ID:      fmt.Sprintf("msg-%d", time.Now().Unix()),
            Content: "这是一条重要消息，需要确认收到",
            Timeout: 30 * time.Second,
        }
        
        go func() {
            time.Sleep(2 * time.Second) // 等待连接稳定
            if err := hub.SendACKMessage(client.ID, ackMessage); err != nil {
                log.Printf("❌ 发送 ACK 消息失败: %v", err)
            } else {
                log.Printf("📤 已发送 ACK 消息: %s", ackMessage.ID)
            }
        }()
    })
    
    // 监控待确认消息
    go func() {
        ticker := time.NewTicker(10 * time.Second)
        defer ticker.Stop()
        
        for range ticker.C {
            clients := hub.GetConnectedClientsIDs()
            for _, clientID := range clients {
                pendingACKs := hub.GetPendingACKs(clientID)
                if len(pendingACKs) > 0 {
                    log.Printf("⏰ 客户端 %s 有 %d 条待确认消息", clientID, len(pendingACKs))
                }
            }
        }
    }()
    
    http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
        wsc.HandleWebSocket(hub, w, r)
    })
    
    log.Println("🚀 ACK 服务器启动在端口 :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

## 监控和调试

### 性能监控

```go
// 定期输出统计信息
go func() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    for range ticker.C {
        stats := hub.GetStats()
        log.Printf("📊 Hub 统计: 连接=%d, 房间=%d, 消息=%d, 错误=%d", 
            stats.ConnectedClients,
            stats.ActiveRooms,
            stats.TotalMessages,
            stats.ErrorCount)
    }
}()
```

### 健康检查

```go
http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
    stats := hub.GetStats()
    health := map[string]interface{}{
        "status": "healthy",
        "uptime": time.Since(stats.StartTime).String(),
        "connections": stats.ConnectedClients,
        "rooms": stats.ActiveRooms,
    }
    
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(health)
})
```

## 最佳实践

### 1. 连接数限制

```go
config := wsc.HubConfig{
    MaxConnections: 1000, // 限制最大连接数
}
hub.SetConfig(config)
```

### 2. 消息大小控制

```go
hub.OnMessageReceived(func(client *wsc.Client, message []byte) {
    if len(message) > 1024*1024 { // 1MB 限制
        log.Printf("❌ 消息过大，来自客户端: %s", client.ID)
        client.Disconnect()
        return
    }
    
    // 处理消息
})
```

### 3. 错误处理

```go
hub.OnError(func(client *wsc.Client, err error) {
    log.Printf("❌ 客户端错误: %s, 错误: %v", client.ID, err)
    
    // 根据错误类型采取相应措施
    if isNetworkError(err) {
        // 网络错误，可能需要清理资源
    }
})
```

### 4. 优雅关闭

```go
// 捕获系统信号
c := make(chan os.Signal, 1)
signal.Notify(c, os.Interrupt, syscall.SIGTERM)

go func() {
    <-c
    log.Println("🛑 收到关闭信号，正在优雅关闭...")
    
    // 通知所有客户端服务即将关闭
    hub.BroadcastText("服务器即将关闭，请保存您的工作")
    
    // 等待一段时间让客户端处理
    time.Sleep(5 * time.Second)
    
    // 停止 Hub
    hub.Stop()
    
    os.Exit(0)
}()
```
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

## 失败处理与重试机制 🔄

### 失败处理器接口

go-wsc 提供了五种专门的失败处理器接口，用于处理不同类型的消息发送失败：

```go
// SendFailureHandler 通用消息发送失败处理器
type SendFailureHandler interface {
    HandleSendFailure(msg *HubMessage, recipient string, reason string, err error)
}

// QueueFullHandler 队列满处理器
type QueueFullHandler interface {
    HandleQueueFull(msg *HubMessage, recipient string, queueType string, err error)
}

// UserOfflineHandler 用户离线处理器
type UserOfflineHandler interface {
    HandleUserOffline(msg *HubMessage, userID string, err error)
}

// ConnectionErrorHandler 连接错误处理器
type ConnectionErrorHandler interface {
    HandleConnectionError(msg *HubMessage, clientID string, err error)
}

// TimeoutHandler 超时处理器
type TimeoutHandler interface {
    HandleTimeout(msg *HubMessage, recipient string, timeoutType string, duration time.Duration, err error)
}
```

### 重试机制（集成 go-toolbox）

基于 go-toolbox/pkg/retry 模块的智能重试机制：

```go
// SendToUserWithRetry 带重试机制的消息发送
func (hub *Hub) SendToUserWithRetry(ctx context.Context, toUserID string, msg *HubMessage) *SendResult {
    // 返回详细的重试结果
}

// SendResult 发送结果结构
type SendResult struct {
    Success      bool          // 最终是否成功
    Attempts     []SendAttempt // 所有尝试记录
    TotalRetries int           // 总重试次数
    TotalTime    time.Duration // 总耗时
    FinalError   error         // 最终错误
}

// SendAttempt 单次尝试记录
type SendAttempt struct {
    AttemptNumber int           // 尝试次数
    StartTime     time.Time     // 开始时间
    Duration      time.Duration // 耗时
    Error         error         // 错误
    Success       bool          // 是否成功
}
```

### 失败处理器管理

#### 添加失败处理器

```go
// 添加通用失败处理器
hub.AddSendFailureHandler(myFailureHandler)

// 添加特定类型的处理器
hub.AddQueueFullHandler(myQueueHandler)
hub.AddUserOfflineHandler(myOfflineHandler)
hub.AddConnectionErrorHandler(myConnHandler)
hub.AddTimeoutHandler(myTimeoutHandler)
```

#### 移除失败处理器

```go
// 移除处理器（注意：只移除引用相同的处理器实例）
hub.RemoveSendFailureHandler(myFailureHandler)
```

### 失败原因常量

```go
const (
    SendFailureReasonQueueFull     = "queue_full"     // 队列满
    SendFailureReasonBroadcastFull = "broadcast_full" // 广播队列满
    SendFailureReasonPendingFull   = "pending_full"   // 待发送队列满
    SendFailureReasonUserOffline   = "user_offline"   // 用户离线
    SendFailureReasonTimeout       = "timeout"        // 超时
    SendFailureReasonSendTimeout   = "send_timeout"   // 发送超时
    SendFailureReasonAckTimeout    = "ack_timeout"    // ACK超时
    SendFailureReasonConnClosed    = "conn_closed"    // 连接关闭
    SendFailureReasonConnError     = "conn_error"     // 连接错误
    SendFailureReasonChannelClosed = "channel_closed" // 通道关闭
    SendFailureReasonUnknown       = "unknown"        // 未知错误
    SendFailureReasonValidation    = "validation"     // 验证失败
    SendFailureReasonPermission    = "permission"     // 权限不足
)
```

### 配置重试参数

重试配置通过 go-config/wsc 统一管理：

```go
// 在 go-config/wsc 包中配置
type WSC struct {
    MaxRetries         int             `yaml:"max_retries" json:"max_retries"`
    BaseDelay          time.Duration   `yaml:"base_delay" json:"base_delay"`
    BackoffFactor      float64         `yaml:"backoff_factor" json:"backoff_factor"`
    RetryableErrors    []string        `yaml:"retryable_errors" json:"retryable_errors"`
    NonRetryableErrors []string        `yaml:"non_retryable_errors" json:"non_retryable_errors"`
}

// 默认配置
MaxRetries: 3
BaseDelay: 100ms
BackoffFactor: 2.0
RetryableErrors: ["queue_full", "timeout", "conn_error", "channel_closed"]
NonRetryableErrors: ["user_offline", "permission", "validation"]
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
    // 设置失败处理器
    setupFailureHandlers(hub)
    
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
        
        // 使用重试机制发送消息
        msg := &wsc.HubMessage{
            ID:       generateMessageID(),
            Type:     wsc.TextMessage,
            Content:  fmt.Sprintf("[%s]: %s", client.ID, string(message)),
            CreateAt: time.Now(),
        }
        
        // 广播给所有在线用户（带重试）
        for userID := range hub.GetAllConnectedUsers() {
            if userID != client.UserID {
                go func(uid string) {
                    result := hub.SendToUserWithRetry(context.Background(), uid, msg)
                    if !result.Success {
                        log.Printf("❌ 发送给用户 %s 失败: %v (重试 %d 次)", 
                            uid, result.FinalError, result.TotalRetries)
                    }
                }(userID)
            }
        }
    })
    
    hub.OnError(func(client *wsc.Client, err error) {
        log.Printf("❌ 客户端 %s 发生错误: %v", client.ID, err)
    })
}

// setupFailureHandlers 配置失败处理器
func setupFailureHandlers(hub *wsc.Hub) {
    // 通用失败处理器
    hub.AddSendFailureHandler(&GeneralFailureHandler{})
    
    // 队列满处理器
    hub.AddQueueFullHandler(&QueueFullHandler{
        fallbackStorage: redis.NewClient(redisOptions),
    })
    
    // 用户离线处理器
    hub.AddUserOfflineHandler(&OfflineHandler{
        offlineDB: offlineMessageDB,
    })
    
    // 连接错误处理器
    hub.AddConnectionErrorHandler(&ConnectionErrorHandler{})
    
    // 超时处理器
    hub.AddTimeoutHandler(&TimeoutHandler{
        timeoutThreshold: 30 * time.Second,
    })
}

// GeneralFailureHandler 通用失败处理器实现
type GeneralFailureHandler struct{}

func (h *GeneralFailureHandler) HandleSendFailure(msg *wsc.HubMessage, recipient string, reason string, err error) {
    log.Printf("🚨 消息发送失败 - 接收者: %s, 原因: %s, 错误: %v, 消息ID: %s", 
        recipient, reason, err, msg.ID)
    
    // 记录到监控系统
    metrics.IncrementFailureCount(reason)
    
    // 发送告警通知
    if isHighPriorityMessage(msg) {
        sendAlert(fmt.Sprintf("高优先级消息发送失败: %s", msg.ID))
    }
}

// QueueFullHandler 队列满处理器实现
type QueueFullHandler struct {
    fallbackStorage redis.Client
}

func (h *QueueFullHandler) HandleQueueFull(msg *wsc.HubMessage, recipient string, queueType string, err error) {
    log.Printf("📦 队列满 - 接收者: %s, 队列类型: %s, 消息: %s", recipient, queueType, msg.ID)
    
    // 将消息存储到Redis作为备用
    msgData, _ := json.Marshal(msg)
    h.fallbackStorage.LPush(context.Background(), 
        fmt.Sprintf("fallback:queue:%s", recipient), msgData)
    
    // 记录队列满事件
    metrics.IncrementQueueFullCount(queueType)
}

// OfflineHandler 离线处理器实现
type OfflineHandler struct {
    offlineDB *sql.DB
}

func (h *OfflineHandler) HandleUserOffline(msg *wsc.HubMessage, userID string, err error) {
    log.Printf("👤 用户离线 - 用户: %s, 消息: %s", userID, msg.ID)
    
    // 存储离线消息到数据库
    query := `INSERT INTO offline_messages (user_id, message_id, content, created_at) VALUES (?, ?, ?, ?)`
    h.offlineDB.Exec(query, userID, msg.ID, msg.Content, msg.CreateAt)
    
    // 发送离线推送通知
    pushNotification(userID, msg.Content)
}

// ConnectionErrorHandler 连接错误处理器实现
type ConnectionErrorHandler struct{}

func (h *ConnectionErrorHandler) HandleConnectionError(msg *wsc.HubMessage, clientID string, err error) {
    log.Printf("🔌 连接错误 - 客户端: %s, 错误: %v, 消息: %s", clientID, err, msg.ID)
    
    // 标记连接为不稳定
    markConnectionUnstable(clientID)
    
    // 尝试重新建立连接
    go attemptReconnection(clientID)
}

// TimeoutHandler 超时处理器实现
type TimeoutHandler struct {
    timeoutThreshold time.Duration
}

func (h *TimeoutHandler) HandleTimeout(msg *wsc.HubMessage, recipient string, timeoutType string, duration time.Duration, err error) {
    log.Printf("⏰ 超时 - 接收者: %s, 类型: %s, 耗时: %v, 消息: %s", 
        recipient, timeoutType, duration, msg.ID)
    
    if duration > h.timeoutThreshold {
        // 严重超时，发送告警
        sendTimeoutAlert(recipient, duration, msg.ID)
    }
    
    // 记录超时指标
    metrics.RecordTimeout(timeoutType, duration)
}
```

### 重试机制使用示例

```go
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/kamalyes/go-wsc"
)

func main() {
    hub := wsc.NewHub()
    
    // 配置失败处理器
    setupAdvancedFailureHandlers(hub)
    
    go hub.Run()
    
    // 演示带重试的消息发送
    demonstrateRetryMechanism(hub)
    
    http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
        wsc.HandleWebSocket(hub, w, r)
    })
    
    log.Println("🚀 重试机制演示服务器启动在端口 :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}

func demonstrateRetryMechanism(hub *wsc.Hub) {
    // 创建测试消息
    msg := &wsc.HubMessage{
        ID:       "demo-msg-001",
        Type:     wsc.TextMessage,
        Content:  "这是一条重要的测试消息",
        CreateAt: time.Now(),
        Priority: wsc.HighPriority,
    }
    
    // 使用重试机制发送消息
    go func() {
        time.Sleep(5 * time.Second) // 等待可能的连接建立
        
        ctx := context.Background()
        result := hub.SendToUserWithRetry(ctx, "test-user-001", msg)
        
        log.Printf("📊 发送结果:")
        log.Printf("   成功: %v", result.Success)
        log.Printf("   总重试次数: %d", result.TotalRetries)
        log.Printf("   总耗时: %v", result.TotalTime)
        
        if result.FinalError != nil {
            log.Printf("   最终错误: %v", result.FinalError)
        }
        
        // 输出每次尝试的详细信息
        for i, attempt := range result.Attempts {
            log.Printf("   尝试 %d: 耗时=%v, 成功=%v, 错误=%v", 
                i+1, attempt.Duration, attempt.Success, attempt.Error)
        }
    }()
}

func setupAdvancedFailureHandlers(hub *wsc.Hub) {
    // 添加高级失败处理器
    hub.AddSendFailureHandler(&AdvancedFailureHandler{
        alertThreshold: 5, // 5次失败后发送告警
        failureCount:   make(map[string]int),
    })
    
    hub.AddQueueFullHandler(&AdvancedQueueHandler{
        maxBacklogSize: 1000,
        backlog:        make(map[string][]*wsc.HubMessage),
    })
    
    hub.AddUserOfflineHandler(&AdvancedOfflineHandler{
        maxOfflineMessages: 100,
        offlineStore:       make(map[string][]*wsc.HubMessage),
    })
}

// AdvancedFailureHandler 高级失败处理器
type AdvancedFailureHandler struct {
    alertThreshold int
    failureCount   map[string]int
    mutex          sync.Mutex
}

func (h *AdvancedFailureHandler) HandleSendFailure(msg *wsc.HubMessage, recipient string, reason string, err error) {
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    h.failureCount[recipient]++
    
    log.Printf("🚨 [高级失败处理] 用户: %s, 原因: %s, 累计失败: %d", 
        recipient, reason, h.failureCount[recipient])
    
    // 达到告警阈值
    if h.failureCount[recipient] >= h.alertThreshold {
        h.sendCriticalAlert(recipient, msg, reason, err)
        // 重置计数器，避免重复告警
        h.failureCount[recipient] = 0
    }
    
    // 特殊处理高优先级消息
    if msg.Priority == wsc.HighPriority {
        h.handleHighPriorityFailure(msg, recipient, reason, err)
    }
}

func (h *AdvancedFailureHandler) sendCriticalAlert(recipient string, msg *wsc.HubMessage, reason string, err error) {
    // 发送紧急告警
    alert := fmt.Sprintf("🚨 CRITICAL: 用户 %s 消息发送持续失败，原因: %s, 消息ID: %s", 
        recipient, reason, msg.ID)
    log.Printf(alert)
    
    // 这里可以集成实际的告警系统，如钉钉、企业微信、邮件等
    // sendDingTalkAlert(alert)
    // sendEmailAlert(alert)
}

func (h *AdvancedFailureHandler) handleHighPriorityFailure(msg *wsc.HubMessage, recipient string, reason string, err error) {
    // 高优先级消息失败的特殊处理
    log.Printf("⚡ [高优先级消息失败] 立即采取补救措施")
    
    // 可以尝试其他发送方式，如短信、邮件等
    // sendSMSFallback(recipient, msg.Content)
    // sendEmailFallback(recipient, msg.Content)
}

// AdvancedQueueHandler 高级队列处理器
type AdvancedQueueHandler struct {
    maxBacklogSize int
    backlog        map[string][]*wsc.HubMessage
    mutex          sync.RWMutex
}

func (h *AdvancedQueueHandler) HandleQueueFull(msg *wsc.HubMessage, recipient string, queueType string, err error) {
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    log.Printf("📦 [队列满处理] 类型: %s, 用户: %s", queueType, recipient)
    
    // 检查是否有积压空间
    if len(h.backlog[recipient]) < h.maxBacklogSize {
        // 添加到积压队列
        h.backlog[recipient] = append(h.backlog[recipient], msg)
        log.Printf("   已添加到积压队列，当前积压: %d", len(h.backlog[recipient]))
        
        // 启动异步处理来清理积压
        go h.processBacklog(recipient)
    } else {
        log.Printf("   积压队列已满，丢弃消息: %s", msg.ID)
        // 记录丢弃的消息用于后续分析
        h.logDroppedMessage(msg, recipient)
    }
}

func (h *AdvancedQueueHandler) processBacklog(recipient string) {
    // 等待一段时间后尝试重新发送积压的消息
    time.Sleep(5 * time.Second)
    
    h.mutex.Lock()
    messages := h.backlog[recipient]
    h.backlog[recipient] = nil // 清空积压
    h.mutex.Unlock()
    
    if len(messages) > 0 {
        log.Printf("🔄 开始处理用户 %s 的 %d 条积压消息", recipient, len(messages))
        // 这里需要访问hub来重新发送消息
        // for _, msg := range messages {
        //     hub.SendToUser(context.Background(), recipient, msg)
        // }
    }
}

func (h *AdvancedQueueHandler) logDroppedMessage(msg *wsc.HubMessage, recipient string) {
    // 记录被丢弃的消息，用于后续分析和恢复
    log.Printf("⚠️  消息丢弃: 用户=%s, 消息ID=%s, 内容预览=%s", 
        recipient, msg.ID, truncateString(msg.Content, 50))
}

// AdvancedOfflineHandler 高级离线处理器
type AdvancedOfflineHandler struct {
    maxOfflineMessages int
    offlineStore       map[string][]*wsc.HubMessage
    mutex              sync.RWMutex
}

func (h *AdvancedOfflineHandler) HandleUserOffline(msg *wsc.HubMessage, userID string, err error) {
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    log.Printf("👤 [用户离线处理] 用户: %s, 消息: %s", userID, msg.ID)
    
    // 检查离线消息数量限制
    if len(h.offlineStore[userID]) < h.maxOfflineMessages {
        h.offlineStore[userID] = append(h.offlineStore[userID], msg)
        log.Printf("   已存储离线消息，当前离线消息数: %d", len(h.offlineStore[userID]))
        
        // 发送推送通知
        h.sendPushNotification(userID, msg)
    } else {
        // 删除最旧的消息，添加新消息
        h.offlineStore[userID] = h.offlineStore[userID][1:]
        h.offlineStore[userID] = append(h.offlineStore[userID], msg)
        log.Printf("   离线消息数量超限，已替换最旧消息")
    }
}

func (h *AdvancedOfflineHandler) sendPushNotification(userID string, msg *wsc.HubMessage) {
    // 发送推送通知
    notification := fmt.Sprintf("您有新消息: %s", truncateString(msg.Content, 30))
    log.Printf("📱 [推送通知] 用户: %s, 内容: %s", userID, notification)
    
    // 集成实际的推送服务
    // pushService.Send(userID, notification)
}

// 辅助函数
func truncateString(s string, maxLen int) string {
    if len(s) <= maxLen {
        return s
    }
    return s[:maxLen] + "..."
}
```

### 重试配置管理

```go
// 在应用启动时配置重试参数
func configureRetrySettings() {
    // 可以通过环境变量或配置文件来调整重试设置
    config := &wscconfig.WSC{
        MaxRetries:    5,                          // 最大重试5次
        BaseDelay:     200 * time.Millisecond,     // 基础延迟200ms
        BackoffFactor: 1.5,                        // 退避因子1.5倍
        RetryableErrors: []string{
            "queue_full",
            "timeout", 
            "conn_error",
            "channel_closed",
            "network_unreachable",
        },
        NonRetryableErrors: []string{
            "user_offline",
            "permission",
            "validation",
            "authentication_failed",
            "message_too_large",
        },
    }
    
    // 应用配置到hub
    // hub.SetRetryConfig(config)
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
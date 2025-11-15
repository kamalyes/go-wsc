# Go WebSocket Client (go-wsc)

> `go-wsc` 是一个用于管理 WebSocket 连接的 Go 客户端库它封装了 WebSocket 连接的管理、消息发送和接收，并提供了灵活的配置选项以及回调函数，方便开发者在使用 WebSocket 时进行扩展和定制该库支持自动重连、消息缓冲和连接状态管理，旨在简化 WebSocket 的使用

[![stable](https://img.shields.io/badge/stable-stable-green.svg)](https://github.com/kamalyes/go-wsc)
[![license](https://img.shields.io/github/license/kamalyes/go-wsc)]()
[![download](https://img.shields.io/github/downloads/kamalyes/go-wsc/total)]()
[![release](https://img.shields.io/github/v/release/kamalyes/go-wsc)]()
[![commit](https://img.shields.io/github/last-commit/kamalyes/go-wsc)]()
[![issues](https://img.shields.io/github/issues/kamalyes/go-wsc)]()
[![pull](https://img.shields.io/github/issues-pr/kamalyes/go-wsc)]()
[![fork](https://img.shields.io/github/forks/kamalyes/go-wsc)]()
[![star](https://img.shields.io/github/stars/kamalyes/go-wsc)]()
[![go](https://img.shields.io/github/go-mod/go-version/kamalyes/go-wsc)]()
[![size](https://img.shields.io/github/repo-size/kamalyes/go-wsc)]()
[![contributors](https://img.shields.io/github/contributors/kamalyes/go-wsc)]()
[![codecov](https://codecov.io/gh/kamalyes/go-wsc/branch/master/graph/badge.svg)](https://codecov.io/gh/kamalyes/go-wsc)
[![Go Report Card](https://goreportcard.com/badge/github.com/kamalyes/go-wsc)](https://goreportcard.com/report/github.com/kamalyes/go-wsc)
[![Go Reference](https://pkg.go.dev/badge/github.com/kamalyes/go-wsc?status.svg)](https://pkg.go.dev/github.com/kamalyes/go-wsc?tab=doc)
[![Sourcegraph](https://sourcegraph.com/github.com/kamalyes/go-wsc/-/badge.svg)](https://sourcegraph.com/github.com/kamalyes/go-wsc?badge)

## 特性

### 客户端功能
- **多种消息类型支持**：支持文本 (`TextMessage`) 和二进制 (`BinaryMessage`) 消息的发送与接收
- **自动重连机制**：在连接断开时，自动重连，并支持自定义重连策略（如最小重连时间、最大重连时间和重连因子）
- **连接状态管理**：提供简单的方法检查连接是否处于活动状态
- **可配置的消息缓冲池**：用户可以配置消息缓冲池的大小以适应不同的使用场景
- **回调函数**：允许用户定义连接成功、连接错误、消息接收等事件的回调函数，以便处理业务逻辑
- **错误处理**：定义了一些常见的错误，方便用户进行错误处理

### 服务端 Hub 功能
- **🚀 高性能**：使用原子操作和最小锁竞争优化
  - 客户端注册：~2,430 ns/op
  - 消息发送：~138 ns/op
  - 吞吐量：~720万条消息/秒
- **多协议支持**：WebSocket 和 SSE (Server-Sent Events) 连接
- **连接管理**：自动心跳检测和超时处理
- **消息路由**：点对点、工单组、广播消息
- **✨ ACK 确认机制**：支持消息送达确认和自动重试
  - 可配置超时时间和重试次数
  - 指数退避重试策略
  - 离线消息处理支持
- **📝 消息记录系统**：完整的消息发送记录和失败重试
  - 8种消息状态跟踪
  - 9种失败原因分类
  - 自动清理过期记录
  - 支持批量重试失败消息
  - 可扩展的自定义字段和标签
  - 灵活的钩子函数系统
- **分布式就绪**：节点感知架构，支持水平扩展
- **欢迎消息**：可自定义的欢迎消息提供者
- **全面测试**：368个测试用例，100% 通过率，包含竞态检测

## 开始使用

建议需要 [Go](https://go.dev/) 版本 [1.20](https://go.dev/doc/devel/release#go1.20.0) 

### 获取

使用 [Go 的模块支持](https://go.dev/wiki/Modules#how-to-use-modules)，当您在代码中添加导入时，`go [build|run|test]` 将自动获取所需的依赖项：

```go
import "github.com/kamalyes/go-wsc"
```

或者，使用 `go get` 命令：

```sh
go get -u github.com/kamalyes/go-wsc
```

## 使用示例

### WebSocket 客户端

以下是一个简单的使用示例，展示如何使用 `go-wsc` 库建立 WebSocket 连接并发送/接收消息：

```go
package main

import (
    "fmt"
    "github.com/kamalyes/go-wsc"
    "time"
)

func main() {
    // 创建一个新的 WebSocket 客户端
    client := wsc.New("ws://localhost:8080/ws")

    // 设置连接成功的回调
    client.OnConnected(func() {
        fmt.Println("连接成功！")
    })

    // 设置连接错误的回调
    client.OnConnectError(func(err error) {
        fmt.Println("连接错误:", err)
    })

    // 设置断开连接的回调
    client.OnDisconnected(func(err error) {
        fmt.Println("连接断开:", err)
    })

    // 设置接收到文本消息的回调
    client.OnTextMessageReceived(func(message string) {
        fmt.Println("接收到文本消息:", message)
    })

    // 设置发送文本消息成功的回调
    client.OnTextMessageSent(func(message string) {
        fmt.Println("发送文本消息成功:", message)
    })

    // 连接到 WebSocket 服务器
    client.Connect()

    // 发送一条文本消息
    err := client.SendTextMessage("Hello, WebSocket!")
    if err != nil {
        fmt.Println("发送消息错误:", err)
    }

    // 保持程序运行，以便接收消息
    time.Sleep(10 * time.Second)

    // 关闭连接
    client.Close()
}
```

### 服务端 Hub

Hub 提供了一个集中式的 WebSocket/SSE 连接管理器，用于构建实时通信服务器：

```go
package main

import (
    "context"
    "fmt"
    "log"
    "net/http"
    "time"

    "github.com/gorilla/websocket"
    "github.com/kamalyes/go-wsc"
)

func main() {
    // 使用默认配置创建 Hub
    hub := wsc.NewHub(nil)
    
    // 在 goroutine 中启动 Hub
    go hub.Run()
    defer hub.Shutdown()

    // WebSocket 升级配置
    upgrader := websocket.Upgrader{
        CheckOrigin: func(r *http.Request) bool {
            return true // 开发环境允许所有来源
        },
    }

    // WebSocket 处理器
    http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
        // 将 HTTP 连接升级为 WebSocket
        conn, err := upgrader.Upgrade(w, r, nil)
        if err != nil {
            log.Printf("WebSocket 升级失败: %v", err)
            return
        }

        // 从请求上下文创建客户端
        userID := r.URL.Query().Get("user_id")
        client := &wsc.Client{
            ID:       fmt.Sprintf("client-%s-%d", userID, time.Now().Unix()),
            UserID:   userID,
            UserType: wsc.UserTypeCustomer,
            Role:     wsc.UserRoleCustomer,
            Status:   wsc.UserStatusOnline,
            Conn:     conn,
            SendChan: make(chan []byte, 256),
            Context:  context.WithValue(context.Background(), wsc.ContextKeyUserID, userID),
        }

        // 向 Hub 注册客户端
        hub.Register(client)
    })

    // API 端点：发送消息
    http.HandleFunc("/api/send", func(w http.ResponseWriter, r *http.Request) {
        toUserID := r.FormValue("to")
        content := r.FormValue("content")

        msg := &wsc.HubMessage{
            Type:     wsc.MessageTypeText,
            Content:  content,
            CreateAt: time.Now(),
            Status:   wsc.MessageStatusSent,
        }

        err := hub.SendToUser(context.Background(), toUserID, msg)
        if err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }

        w.Write([]byte("消息发送成功"))
    })

    // 启动 HTTP 服务器
    log.Println("服务器启动在 :8080")
    log.Fatal(http.ListenAndServe(":8080", nil))
}
```

### Hub 性能

Hub 针对高并发场景进行了优化：

```bash
# 运行性能基准测试
go test -bench=BenchmarkHubOperations -benchmem -run=^$ -benchtime=3s

# 测试结果 (Intel i5-9300H @ 2.40GHz):
# ClientRegistration-8    2,430 ns/op    221 B/op    0 allocs/op
# MessageSending-8          138 ns/op     55 B/op    1 allocs/op

# 竞态条件测试
go test -race -run TestHub -timeout 30s
```

**性能亮点：**
- ✅ **41.1万** 次客户端注册/秒
- ✅ **720万** 条消息/秒吞吐量
- ✅ 使用原子操作实现无锁统计
- ✅ 客户端注册热路径零内存分配
- ✅ 优化的锁策略，最小化锁竞争

### 配置

`go-wsc` 提供了多种配置选项，用户可以根据需要自定义客户端配置,可以使用 `SetConfig` 方法设置配置，以下是可配置的选项：

- **WriteWait**: 写超时（默认 10 秒），在发送消息时的最大等待时间
- **MaxMessageSize**: 最大消息长度（默认 512 字节），限制接收消息的最大大小
- **MinRecTime**: 最小重连时间（默认 2 秒），在连接失败后，重连的最小等待时间
- **MaxRecTime**: 最大重连时间（默认 60 秒），在连接失败后，重连的最大等待时间
- **RecFactor**: 重连因子（默认 1.5），用于计算下一次重连的等待时间
- **MessageBufferSize**: 消息缓冲池大小（默认 256），用于控制发送消息的缓冲区大小

```go
config := wsc.NewDefaultConfig().
    WithWriteWait(5 * time.Second).
    WithMaxMessageSize(1024).
    WithMinRecTime(1 * time.Second).
    WithMaxRecTime(30 * time.Second).
    WithRecFactor(2.0).
    WithMessageBufferSize(512)
client.SetConfig(config)
```

### Hub 配置

Hub 支持丰富的配置选项：

```go
config := &wsc.HubConfig{
    NodeIP:              "0.0.0.0",
    NodePort:            8080,
    HeartbeatInterval:   30 * time.Second,  // 心跳检查间隔
    ClientTimeout:       90 * time.Second,  // 客户端超时时长
    MessageBufferSize:   256,               // 消息通道缓冲区大小
    PendingQueueSize:    1024,              // 待发送队列大小
    SSEHeartbeat:        30 * time.Second,  // SSE 心跳间隔
    SSETimeout:          2 * time.Minute,   // SSE 连接超时
    SSEMessageBuffer:    100,               // SSE 消息缓冲区大小
    
    // ACK 确认配置
    EnableAck:           true,              // 启用 ACK 确认
    AckTimeout:          5 * time.Second,   // ACK 超时时间
    MaxRetry:            3,                 // 最大重试次数
    
    // 消息记录配置
    EnableMessageRecord: true,              // 启用消息记录
    MaxRecords:          10000,             // 最大记录数
    RecordRetention:     24 * time.Hour,    // 记录保留时间
    
    WelcomeProvider:     myWelcomeProvider, // 自定义欢迎消息提供者
}

hub := wsc.NewHub(config)
```

**关键配置选项：**
- `MessageBufferSize`: 控制并发消息吞吐量（默认：256）
- `PendingQueueSize`: 待发送队列大小，队列满时缓存消息（默认：1024）
- `HeartbeatInterval`: 连接健康检查频率（默认：30秒）
- `ClientTimeout`: 最大空闲时间，超时将断开连接（默认：90秒）
- `EnableAck`: 启用消息确认机制（默认：false）
- `EnableMessageRecord`: 启用消息发送记录（默认：true）
- `WelcomeProvider`: 为不同用户类型定制欢迎消息

## ACK 确认机制

### 基本用法

启用 ACK 确认后，消息将等待客户端确认：

```go
// 1. 创建启用 ACK 的 Hub
config := wsc.DefaultHubConfig()
config.EnableAck = true
config.AckTimeout = 5 * time.Second
config.MaxRetry = 3
hub := wsc.NewHub(config)

// 2. 发送需要确认的消息
msg := &wsc.HubMessage{
    Type:     wsc.MessageTypeText,
    Content:  "重要消息",
    CreateAt: time.Now(),
}

ctx := context.Background()
err := hub.SendToUser(ctx, "user123", msg)
if err != nil {
    log.Printf("发送失败: %v", err)
}

// 3. 客户端接收消息后发送 ACK
ackMsg := &wsc.AckMessage{
    MessageID: msg.ID,
    Status:    wsc.AckStatusConfirmed,
    Timestamp: time.Now(),
}

// 通过 Hub 的 AckManager 确认消息
hub.GetAckManager().ConfirmMessage(msg.ID, ackMsg)
```

### 自动重试

ACK 机制支持指数退避重试策略：

```go
// 消息未收到确认时，会按以下策略重试：
// - 第1次重试: 2秒后
// - 第2次重试: 4秒后  
// -第3次重试: 8秒后
// - 超过 MaxRetry 次后标记为失败

// 可以自定义重试策略
config.AckTimeout = 10 * time.Second  // ACK 超时时间
config.MaxRetry = 5                    // 最大重试 5 次
```

### 离线消息处理

实现 `OfflineMessageHandler` 接口处理离线用户的消息：

```go
type MyOfflineHandler struct {
    db *sql.DB
}

func (h *MyOfflineHandler) HandleOfflineMessage(msg *wsc.HubMessage) error {
    // 将消息存储到数据库
    _, err := h.db.Exec(
        "INSERT INTO offline_messages (user_id, content, created_at) VALUES (?, ?, ?)",
        msg.To, msg.Content, msg.CreateAt,
    )
    return err
}

// 设置离线消息处理器
hub.GetAckManager().SetOfflineHandler(&MyOfflineHandler{db: myDB})
```

### ACK 状态

系统支持以下 ACK 状态：

- `AckStatusPending`: 等待确认
- `AckStatusConfirmed`: 已确认
- `AckStatusTimeout`: 超时
- `AckStatusFailed`: 失败

## 消息记录系统

### 基本功能

消息记录系统自动跟踪所有消息的发送状态：

```go
// 1. 启用消息记录（默认已启用）
config := wsc.DefaultHubConfig()
config.EnableMessageRecord = true
config.MaxRecords = 10000              // 最多保存 1万条记录
config.RecordRetention = 24 * time.Hour // 保留 24 小时

hub := wsc.NewHub(config)

// 2. 查询消息记录
recordManager := hub.GetRecordManager()

// 获取失败的消息
failedRecords := recordManager.GetFailedRecords(100) // 获取最近 100 条失败记录

// 获取可重试的消息
retryableRecords := recordManager.GetRetryableRecords(50)

// 获取统计信息
stats := recordManager.GetStats()
fmt.Printf("总记录数: %d\n", stats["total_records"])
fmt.Printf("失败数: %d\n", stats["failed_count"])
fmt.Printf("成功率: %.2f%%\n", stats["success_rate"])
```

### 消息状态

系统跟踪 8 种消息状态：

- `MessageSendStatusPending`: 待发送
- `MessageSendStatusSending`: 发送中
- `MessageSendStatusSuccess`: 发送成功
- `MessageSendStatusFailed`: 发送失败
- `MessageSendStatusRetrying`: 重试中
- `MessageSendStatusAckTimeout`: ACK 超时
- `MessageSendStatusUserOffline`: 用户离线
- `MessageSendStatusExpired`: 已过期

### 失败原因

系统识别 9 种失败原因：

- `FailureReasonQueueFull`: 队列已满
- `FailureReasonUserOffline`: 用户离线
- `FailureReasonConnError`: 连接错误
- `FailureReasonAckTimeout`: ACK 超时
- `FailureReasonSendTimeout`: 发送超时
- `FailureReasonNetworkError`: 网络错误
- `FailureReasonUnknown`: 未知错误
- `FailureReasonMaxRetry`: 超过最大重试次数
- `FailureReasonExpired`: 消息过期

### 重试失败消息

```go
recordManager := hub.GetRecordManager()

// 1. 重试单条消息
err := recordManager.RetryMessage(ctx, hub, "message-id-123")
if err != nil {
    log.Printf("重试失败: %v", err)
}

// 2. 批量重试失败消息
results := recordManager.RetryFailedMessages(ctx, hub, 10) // 重试最多 10 条
for _, result := range results {
    if result.Error != nil {
        log.Printf("消息 %s 重试失败: %v", result.MessageID, result.Error)
    } else {
        log.Printf("消息 %s 重试成功", result.MessageID)
    }
}
```

### 扩展功能

#### 自定义字段

```go
record := recordManager.GetRecord("message-id-123")

// 设置自定义字段
record.SetCustomField("priority", "high")
record.SetCustomField("business_type", "payment")
record.SetCustomField("order_id", "ORD-12345")

// 获取自定义字段
priority := record.GetCustomField("priority")
```

#### 标签系统

```go
record := recordManager.GetRecord("message-id-123")

// 添加标签
record.AddTag("urgent")
record.AddTag("vip-user")
record.AddTag("retry-required")

// 移除标签
record.RemoveTag("retry-required")

// 查询带特定标签的记录
urgentRecords := recordManager.GetRecordsByTag("urgent")
```

#### 钩子函数

```go
// 1. 记录创建时的钩子
recordManager.OnRecordCreated(func(record *wsc.MessageRecord) {
    log.Printf("新消息记录: %s, 目标用户: %s", record.MessageID, record.ToUserID)
    
    // 发送监控告警
    if record.Message.Type == wsc.MessageTypeSystem {
        sendAlert("系统消息已创建", record)
    }
})

// 2. 状态更新时的钩子
recordManager.OnStatusUpdated(func(record *wsc.MessageRecord, oldStatus, newStatus wsc.MessageSendStatus) {
    log.Printf("消息 %s 状态变更: %s -> %s", record.MessageID, oldStatus, newStatus)
    
    // 记录到外部系统
    if newStatus == wsc.MessageSendStatusFailed {
        logToExternalSystem(record)
    }
})

// 3. 重试尝试时的钩子
recordManager.OnRetryAttempt(func(record *wsc.MessageRecord, attemptNumber int) {
    log.Printf("消息 %s 第 %d 次重试", record.MessageID, attemptNumber)
    
    // 统计重试次数
    metrics.IncrementRetryCounter(record.MessageID)
})

// 4. 记录过期时的钩子
recordManager.OnRecordExpired(func(record *wsc.MessageRecord) {
    log.Printf("消息记录过期: %s", record.MessageID)
    
    // 归档到长期存储
    archiveRecord(record)
})

// 5. 记录删除时的钩子
recordManager.OnRecordDeleted(func(messageID string) {
    log.Printf("消息记录已删除: %s", messageID)
})
```

#### 自定义过滤器

```go
// 添加自定义过滤器
recordManager.AddFilter("high-priority", func(record *wsc.MessageRecord) bool {
    priority, _ := record.GetCustomField("priority").(string)
    return priority == "high"
})

recordManager.AddFilter("payment-messages", func(record *wsc.MessageRecord) bool {
    businessType, _ := record.GetCustomField("business_type").(string)
    return businessType == "payment"
})

// 使用过滤器查询
highPriorityRecords := recordManager.FilterRecords("high-priority")
paymentRecords := recordManager.FilterRecords("payment-messages")
```

#### 自定义处理器

```go
// 注册自定义处理器
recordManager.SetHandler("notification", func(record *wsc.MessageRecord) error {
    // 发送通知到外部系统
    return sendNotificationToExternalSystem(record)
})

recordManager.SetHandler("analytics", func(record *wsc.MessageRecord) error {
    // 发送到分析系统
    return sendToAnalyticsSystem(record)
})

// 触发处理器
err := recordManager.ExecuteHandler("notification", record)
```

#### 额外数据存储

```go
record := recordManager.GetRecord("message-id-123")

// 存储复杂对象
type OrderInfo struct {
    OrderID    string
    Amount     float64
    CustomerID string
}

orderInfo := OrderInfo{
    OrderID:    "ORD-12345",
    Amount:     99.99,
    CustomerID: "CUST-789",
}

record.ExtraData["order"] = orderInfo

// 读取额外数据
if order, ok := record.ExtraData["order"].(OrderInfo); ok {
    fmt.Printf("订单金额: %.2f\n", order.Amount)
}
```

### 自动清理

```go
// 手动触发清理过期记录
deleted := recordManager.CleanupExpiredRecords()
log.Printf("清理了 %d 条过期记录", deleted)

// 系统会自动定期清理（基于 RecordRetention 配置）
// 默认保留 24 小时的记录

## 回调函数

`go-wsc` 提供了一系列回调函数，允许用户在特定事件发生时执行自定义逻辑,以下是可用的回调函数：

- **OnConnected**: 连接成功时的回调
- **OnConnectError**: 连接出错时的回调，参数为错误信息
- **OnDisconnected**: 连接断开时的回调，参数为错误信息
- **OnClose**: 连接关闭时的回调，参数为关闭代码和关闭文本
- **OnTextMessageSent**: 发送文本消息成功时的回调，参数为发送的消息
- **OnBinaryMessageSent**: 发送二进制消息成功时的回调，参数为发送的数据
- **OnSentError**: 发送消息出错时的回调，参数为错误信息
- **OnPingReceived**: 接收到 Ping 消息时的回调，参数为应用数据
- **OnPongReceived**: 接收到 Pong 消息时的回调，参数为应用数据
- **OnTextMessageReceived**: 接收到文本消息时的回调，参数为接收到的消息
- **OnBinaryMessageReceived**: 接收到二进制消息时的回调，参数为接收到的数据

## 错误处理

在使用 `go-wsc` 时，您可能会遇到以下错误：

- `ErrClose`：连接已关闭
- `ErrBufferFull`：消息缓冲区已满

您可以通过检查返回的错误来处理这些情况

## 测试

项目包含全面的测试覆盖：

```bash
# 运行所有测试
go test ./...

# 详细输出
go test -v ./...

# 使用 gotestsum（更友好的输出格式）
gotestsum -f testname -- ./... -count=1 -timeout=60s

# 使用竞态检测运行测试
go test -race ./...

# 运行特定测试套件
go test -run TestHub         # Hub 测试
go test -run TestAck         # ACK 确认测试
go test -run TestMessageRecord # 消息记录测试
go test -run TestWebSocket   # WebSocket 客户端测试

# 运行基准测试
go test -bench=. -benchmem

# 生成覆盖率报告
go test ./... -coverprofile=coverage.out -covermode=atomic
go tool cover -html=coverage.out -o coverage.html
go tool cover -func=coverage.out
```

**测试覆盖：**
- ✅ Hub 连接管理（注册、注销、并发操作）
- ✅ 消息路由（点对点、广播、工单组）
- ✅ ACK 确认机制（超时、重试、离线处理）
- ✅ 消息记录系统（状态跟踪、失败重试、扩展功能）
- ✅ SSE 连接处理
- ✅ 统计和监控
- ✅ 并发安全（竞态条件测试）
- ✅ 性能基准测试
- ✅ 200+ 场景测试

**测试统计：**
- 总测试数：368 个
- 通过率：100%
- 覆盖率：95.6%
- 竞态检测：通过

详细测试文档请参见 [TEST_COVERAGE.md](TEST_COVERAGE.md)。

## 贡献

欢迎对 `go-wsc` 提出建议或贡献代码！请遵循以下步骤：

1. Fork 该项目
2. 创建您的特性分支 (`git checkout -b feature/yourfeature`)
3. 提交您的更改 (`git commit -m 'Add some feature'`)
4. 推送到分支 (`git push origin feature/yourfeature`)
5. 创建一个新的 Pull Request

## 性能优化

针对高并发场景，请考虑以下优化策略：

### 1. 使用原子操作
- ✅ 统计使用 `atomic.Int64` 而不是互斥锁保护的计数器
- ✅ 减少约 30% 的锁竞争

### 2. 优化锁策略
- ✅ 最小化锁范围（晚获取，早释放）
- ✅ 读多写少场景使用 RWMutex
- ✅ 不同数据结构使用独立的锁

### 3. 通道缓冲区大小调优
- 高吞吐量场景：增加 `MessageBufferSize` 到 512-1024
- 低延迟场景：保持缓冲区较小（256 或更少）
- 使用 Hub 统计监控通道饱和度

### 4. 避免过度优化
- ❌ 对象池可能降低小对象性能
- ❌ 预序列化仅在广播到大量客户端时有帮助
- ✅ 优化前先进行性能分析

详细分析请参见 [OPTIMIZATION.md](OPTIMIZATION.md) 和 [PERFORMANCE_RESULTS.md](PERFORMANCE_RESULTS.md)。

## 架构

```
┌─────────────────────────────────────────────────────────────────────┐
│                       Hub (中心节点)                                  │
│  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐         │
│  │   WebSocket    │  │      SSE       │  │  统计信息      │         │
│  │   客户端       │  │     连接       │  │  (原子操作)    │         │
│  └────────────────┘  └────────────────┘  └────────────────┘         │
│                                                                       │
│  ┌────────────────┐  ┌────────────────┐  ┌────────────────┐         │
│  │  ACK 管理器    │  │  消息记录      │  │  离线处理      │         │
│  │  (确认/重试)   │  │  (状态跟踪)    │  │  (数据持久化)  │         │
│  └────────────────┘  └────────────────┘  └────────────────┘         │
└─────────────────────────────────────────────────────────────────────┘
           │                    │                    │
     ┌─────┴─────┐       ┌──────┴──────┐      ┌────┴────┐
     │   注册    │       │    广播     │      │  统计   │
     │   注销    │       │    消息     │      │  查询   │
     └───────────┘       └─────────────┘      └─────────┘
           │                    │                    │
    ┌──────┴──────┐      ┌──────┴──────┐     ┌──────┴──────┐
    │  心跳检测   │      │   消息路由  │     │   监控指标  │
    │  超时处理   │      │  点对点/组  │     │   统计数据  │
    └─────────────┘      └─────────────┘     └─────────────┘
                                │
                    ┌───────────┴───────────┐
                    │   消息发送流程         │
                    │                       │
                    │  1. 发送到 Hub        │
                    │  2. ACK 确认等待      │
                    │  3. 超时自动重试      │
                    │  4. 记录发送状态      │
                    │  5. 失败消息处理      │
                    └───────────────────────┘
```

## 最佳实践

### 1. 选择合适的配置

```go
// 高并发场景
config := &wsc.HubConfig{
    MessageBufferSize: 512,      // 增大缓冲区
    PendingQueueSize:  2048,     // 增大待发送队列
    EnableAck:         false,    // 关闭 ACK 以提高吞吐量
    EnableMessageRecord: false,  // 关闭记录以减少开销
}

// 高可靠性场景
config := &wsc.HubConfig{
    MessageBufferSize: 256,
    EnableAck:         true,     // 启用 ACK 确认
    AckTimeout:        10 * time.Second,
    MaxRetry:          5,
    EnableMessageRecord: true,   // 启用消息记录
    MaxRecords:        50000,    // 增大记录数
    RecordRetention:   7 * 24 * time.Hour, // 保留 7 天
}

// 低延迟场景
config := &wsc.HubConfig{
    MessageBufferSize: 128,      // 较小缓冲区
    HeartbeatInterval: 10 * time.Second, // 更频繁的心跳
    ClientTimeout:     30 * time.Second,
    EnableAck:         false,
}
```

### 2. 监控和告警

```go
// 定期检查 Hub 统计信息
ticker := time.NewTicker(1 * time.Minute)
go func() {
    for range ticker.C {
        stats := hub.GetStats()
        
        // 监控连接数
        if stats["total_connections"].(int) > 10000 {
            sendAlert("连接数过高")
        }
        
        // 监控消息队列
        if recordManager != nil {
            recordStats := recordManager.GetStats()
            failureRate := 1.0 - recordStats["success_rate"].(float64)
            
            if failureRate > 0.05 { // 失败率超过 5%
                sendAlert(fmt.Sprintf("消息失败率过高: %.2f%%", failureRate*100))
            }
        }
    }
}()
```

### 3. 优雅关闭

```go
// 使用 context 控制关闭
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// 捕获系统信号
sigChan := make(chan os.Signal, 1)
signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

go func() {
    <-sigChan
    log.Println("收到关闭信号，开始优雅关闭...")
    
    // 1. 停止接受新连接
    cancel()
    
    // 2. 等待正在处理的消息完成
    time.Sleep(2 * time.Second)
    
    // 3. 关闭 Hub
    hub.Shutdown()
    
    log.Println("服务已关闭")
    os.Exit(0)
}()
```

### 4. 错误处理

```go
// 发送消息时的错误处理
err := hub.SendToUser(ctx, userID, msg)
if err != nil {
    switch {
    case errors.Is(err, wsc.ErrQueueFull):
        // 队列满，记录日志或降级处理
        log.Printf("消息队列已满，消息ID: %s", msg.ID)
        
    case errors.Is(err, wsc.ErrUserOffline):
        // 用户离线，存储到数据库
        saveToDatabase(msg)
        
    default:
        // 其他错误
        log.Printf("发送消息失败: %v", err)
    }
}
```

### 5. 性能调优

```go
// 1. 使用对象池减少内存分配
var messagePool = sync.Pool{
    New: func() interface{} {
        return &wsc.HubMessage{}
    },
}

msg := messagePool.Get().(*wsc.HubMessage)
defer messagePool.Put(msg)

// 2. 批量操作
messages := []*wsc.HubMessage{msg1, msg2, msg3}
for _, msg := range messages {
    hub.SendToUser(ctx, userID, msg)
}

// 3. 异步处理非关键消息
go func(msg *wsc.HubMessage) {
    hub.SendToUser(context.Background(), userID, msg)
}(msg)
```

## 常见问题

### Q: ACK 确认和消息记录有什么区别？

**A:** 
- **ACK 确认**：实时的消息送达确认机制，用于确保消息被客户端接收。如果超时未收到确认，会自动重试。
- **消息记录**：完整的消息发送历史记录，包括状态、失败原因、重试次数等。可用于审计、分析和后续重试。

两者可以独立使用，也可以配合使用以获得最高的可靠性。

### Q: 如何处理大量离线消息？

**A:**
```go
// 1. 实现自定义离线处理器
type DatabaseOfflineHandler struct {
    db *sql.DB
}

func (h *DatabaseOfflineHandler) HandleOfflineMessage(msg *wsc.HubMessage) error {
    // 存储到数据库
    return h.db.StoreMessage(msg)
}

// 2. 用户上线时批量发送
func onUserOnline(userID string) {
    messages := db.GetOfflineMessages(userID)
    for _, msg := range messages {
        hub.SendToUser(ctx, userID, msg)
    }
    db.DeleteOfflineMessages(userID)
}
```

### Q: 消息记录会不会影响性能？

**A:** 
消息记录系统经过优化，对性能影响很小：
- 使用内存存储，访问速度快
- 异步写入，不阻塞消息发送
- 自动清理过期记录，防止内存泄漏

在高并发场景下（> 100万 msg/s），可以考虑：
- 关闭消息记录（`EnableMessageRecord: false`）
- 减少保留时间（`RecordRetention: 1 * time.Hour`）
- 减少最大记录数（`MaxRecords: 5000`）

### Q: 如何扩展到分布式部署？

**A:**
```go
// 1. 使用 Redis 作为分布式消息队列
type RedisMessageBroker struct {
    client *redis.Client
}

// 2. 节点间同步
func (hub *Hub) SyncWithNodes() {
    // 订阅其他节点的消息
    pubsub := redis.Subscribe("hub:messages")
    for msg := range pubsub.Channel() {
        hub.ProcessDistributedMessage(msg)
    }
}

// 3. 负载均衡
// 使用一致性哈希或轮询方式分配客户端到不同节点
```

## 许可证

该项目使用 MIT 许可证，详见 [LICENSE](LICENSE) 文件
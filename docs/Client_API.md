# 客户端 API 参考 🔌

本文档提供 go-wsc 客户端的完整 API 接口说明。

## 客户端创建

### New(url string) *Wsc

创建新的 WebSocket 客户端实例。

```go
client := wsc.New("ws://localhost:8080/ws")
```

**参数:**

- `url`: WebSocket 服务器地址

**返回值:**

- `*Wsc`: 客户端实例

## 连接管理

### Connect() error

建立 WebSocket 连接。

```go
err := client.Connect()
if err != nil {
    log.Fatal("连接失败:", err)
}
```

### Disconnect()

断开 WebSocket 连接。

```go
client.Disconnect()
```

### IsConnected() bool

检查连接状态。

```go
if client.IsConnected() {
    client.SendText("Hello")
}
```

### Closed() bool

检查连接是否已关闭。

```go
if !client.Closed() {
    // 连接仍然活跃
}
```

## 消息发送

### SendText(message string) error

发送文本消息。

```go
err := client.SendText("Hello WebSocket!")
if err != nil {
    log.Println("发送失败:", err)
}
```

### SendBinary(data []byte) error

发送二进制消息。

```go
data := []byte{0x01, 0x02, 0x03}
err := client.SendBinary(data)
```

### SendPing(data string) error

发送 Ping 消息。

```go
err := client.SendPing("ping")
```

### SendPong(data string) error

发送 Pong 消息。

```go
err := client.SendPong("pong")
```

## 配置管理

### SetConfig(config Config)

设置客户端配置。

```go
config := wsc.Config{
    WriteWait:          15 * time.Second,
    PongWait:           60 * time.Second,
    PingPeriod:         54 * time.Second,
    MaxMessageSize:     1024,
    MessageBufferSize:  512,
    AutoReconnect:      true,
    MinRecTime:         1 * time.Second,
    MaxRecTime:         30 * time.Second,
    RecFactor:          2.0,
}
client.SetConfig(config)
```

### GetConfig() Config

获取当前配置。

```go
config := client.GetConfig()
```

## 事件处理

### OnConnected(fn func())

设置连接成功回调。

```go
client.OnConnected(func() {
    log.Println("✅ WebSocket 连接已建立")
})
```

### OnConnectError(fn func(err error))

设置连接错误回调。

```go
client.OnConnectError(func(err error) {
    log.Printf("❌ 连接错误: %v", err)
})
```

### OnDisconnected(fn func(err error))

设置连接断开回调。

```go
client.OnDisconnected(func(err error) {
    log.Printf("⚠️ 连接断开: %v", err)
})
```

### OnClose(fn func(code int, text string))

设置连接关闭回调。

```go
client.OnClose(func(code int, text string) {
    log.Printf("🔒 连接关闭: code=%d, text=%s", code, text)
})
```

### OnTextMessageReceived(fn func(message string))

设置文本消息接收回调。

```go
client.OnTextMessageReceived(func(message string) {
    log.Printf("📨 收到文本消息: %s", message)
})
```

### OnBinaryMessageReceived(fn func(data []byte))

设置二进制消息接收回调。

```go
client.OnBinaryMessageReceived(func(data []byte) {
    log.Printf("📦 收到二进制消息: %d 字节", len(data))
})
```

### OnTextMessageSent(fn func(message string))

设置文本消息发送回调。

```go
client.OnTextMessageSent(func(message string) {
    log.Printf("📤 发送文本消息: %s", message)
})
```

### OnBinaryMessageSent(fn func(data []byte))

设置二进制消息发送回调。

```go
client.OnBinaryMessageSent(func(data []byte) {
    log.Printf("📤 发送二进制消息: %d 字节", len(data))
})
```

### OnSentError(fn func(err error))

设置发送错误回调。

```go
client.OnSentError(func(err error) {
    log.Printf("❌ 发送错误: %v", err)
})
```

### OnPingReceived(fn func(data string))

设置 Ping 接收回调。

```go
client.OnPingReceived(func(data string) {
    log.Printf("🏓 收到 Ping: %s", data)
})
```

### OnPongReceived(fn func(data string))

设置 Pong 接收回调。

```go
client.OnPongReceived(func(data string) {
    log.Printf("🏓 收到 Pong: %s", data)
})
```

## 配置结构

### Config 结构体

```go
type Config struct {
    // 写操作超时时间
    WriteWait time.Duration
    
    // Pong 消息等待超时时间
    PongWait time.Duration
    
    // Ping 消息发送间隔
    PingPeriod time.Duration
    
    // 最大消息大小 (字节)
    MaxMessageSize int64
    
    // 消息缓冲区大小
    MessageBufferSize int
    
    // 是否自动重连
    AutoReconnect bool
    
    // 最小重连时间间隔
    MinRecTime time.Duration
    
    // 最大重连时间间隔
    MaxRecTime time.Duration
    
    // 重连时间增长因子
    RecFactor float64
}
```

### 默认配置

```go
var DefaultConfig = Config{
    WriteWait:          15 * time.Second,
    PongWait:           60 * time.Second,
    PingPeriod:         54 * time.Second,
    MaxMessageSize:     1024,
    MessageBufferSize:  256,
    AutoReconnect:      true,
    MinRecTime:         1 * time.Second,
    MaxRecTime:         30 * time.Second,
    RecFactor:          2.0,
}
```

## 错误类型

### ErrAlreadyClosed

连接已关闭错误。

```go
if err == wsc.ErrAlreadyClosed {
    log.Println("连接已经关闭")
}
```

### ErrNotConnected

未连接错误。

```go
if err == wsc.ErrNotConnected {
    log.Println("客户端未连接")
}
```

## 使用示例

### 完整客户端示例

```go
package main

import (
    "fmt"
    "log"
    "time"
    
    "github.com/kamalyes/go-wsc"
)

func main() {
    // 创建客户端
    client := wsc.New("ws://localhost:8080/ws")
    
    // 配置客户端
    config := wsc.Config{
        WriteWait:          15 * time.Second,
        MaxMessageSize:     1024,
        MessageBufferSize:  512,
        AutoReconnect:      true,
        MinRecTime:         1 * time.Second,
        MaxRecTime:         30 * time.Second,
        RecFactor:          2.0,
    }
    client.SetConfig(config)
    
    // 设置事件处理器
    setupEventHandlers(client)
    
    // 连接
    if err := client.Connect(); err != nil {
        log.Fatal("连接失败:", err)
    }
    
    // 模拟发送消息
    go func() {
        ticker := time.NewTicker(5 * time.Second)
        defer ticker.Stop()
        
        for {
            select {
            case <-ticker.C:
                if client.IsConnected() {
                    client.SendText(fmt.Sprintf("心跳消息: %v", time.Now().Unix()))
                }
            }
        }
    }()
    
    // 等待
    select {}
}

func setupEventHandlers(client *wsc.Wsc) {
    client.OnConnected(func() {
        log.Println("✅ WebSocket 连接已建立")
    })

    client.OnConnectError(func(err error) {
        log.Printf("❌ 连接错误: %v", err)
    })
    
    client.OnDisconnected(func(err error) {
        log.Printf("⚠️ 连接断开: %v", err)
    })
    
    client.OnClose(func(code int, text string) {
        log.Printf("🔒 连接关闭: code=%d, text=%s", code, text)
    })
    
    client.OnTextMessageReceived(func(message string) {
        log.Printf("📨 收到文本消息: %s", message)
    })
    
    client.OnBinaryMessageReceived(func(data []byte) {
        log.Printf("📦 收到二进制消息: %d 字节", len(data))
    })
    
    client.OnSentError(func(err error) {
        log.Printf("❌ 发送错误: %v", err)
    })
}
```

### 重连机制示例

```go
func setupReconnectClient() {
    client := wsc.New("ws://localhost:8080/ws")
    
    // 配置重连策略
    config := wsc.Config{
        AutoReconnect: true,
        MinRecTime:    1 * time.Second,    // 初始重连间隔 1 秒
        MaxRecTime:    60 * time.Second,   // 最大重连间隔 60 秒
        RecFactor:     1.5,                // 重连间隔增长因子
    }
    client.SetConfig(config)
    
    // 监听重连事件
    client.OnConnected(func() {
        log.Println("✅ 连接成功 (可能是重连)")
    })
    
    client.OnDisconnected(func(err error) {
        log.Printf("⚠️ 连接断开: %v, 将自动重连", err)
    })
    
    client.Connect()
}
```

## 最佳实践

### 1. 错误处理

```go
// 始终检查发送错误
if err := client.SendText("message"); err != nil {
    if err == wsc.ErrNotConnected {
        // 处理未连接状态
        log.Println("客户端未连接，尝试重连")
    } else {
        // 其他错误处理
        log.Printf("发送失败: %v", err)
    }
}
```

### 2. 资源管理

```go
// 确保在程序退出时关闭连接
defer client.Disconnect()

// 或使用 context 控制
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

go func() {
    <-ctx.Done()
    client.Disconnect()
}()
```

### 3. 消息大小控制

```go
config := wsc.Config{
    MaxMessageSize: 1024 * 1024, // 1MB 限制
}
client.SetConfig(config)

// 发送前检查消息大小
message := "very long message..."
if len(message) > 1024*1024 {
    log.Println("消息过大，拒绝发送")
    return
}
```

### 4. 连接状态监控

```go
// 定期检查连接状态
ticker := time.NewTicker(30 * time.Second)
go func() {
    defer ticker.Stop()
    for range ticker.C {
        if !client.IsConnected() {
            log.Println("⚠️ 连接已断开")
        }
    }
}()
```

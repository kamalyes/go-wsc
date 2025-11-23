# WSC 日志集成说明

## 概述

go-wsc 已成功集成了 [go-logger](https://github.com/kamalyes/go-logger) 日志系统，并通过 [go-config](https://github.com/kamalyes/go-config) 进行配置管理。集成后的日志系统提供了丰富的日志记录能力，包括连接管理、消息发送、性能监控、错误处理等。

## 功能特性

### ✅ 已集成功能

1. **连接生命周期日志**
   - 客户端连接/断开
   - SSE 连接状态
   - 心跳超时监控

2. **消息传输日志**
   - 点对点消息发送
   - 广播消息分发
   - 消息发送成功/失败
   - 队列满处理

3. **性能监控日志**
   - 实时连接统计
   - 消息吞吐量
   - Hub 运行时指标
   - 定时性能报告（每5分钟）

4. **错误处理日志**
   - 重试机制日志
   - 序列化失败
   - 网络错误
   - 业务逻辑错误

5. **业务特定日志**
   - LogConnection: 连接事件记录
   - LogMessage: 消息发送记录
   - LogPerformance: 性能统计记录

## 配置方式

### 1. 使用 go-config 配置

```yaml
# config/wsc.yaml
wsc:
  node_ip: "127.0.0.1"
  node_port: 8080
  heartbeat_interval: 30
  client_timeout: 90
  
  # 日志配置
  logging:
    level: "info"
    format: "console"
    colorful: true
    show_caller: true
    output:
      console:
        enabled: true
        colorful: true
      file:
        enabled: true
        filename: "logs/wsc.log"
        max_size: 100
        max_backups: 10
        max_age: 30
        compress: true
```

### 2. 代码中使用

```go
package main

import (
    "context"
    "github.com/kamalyes/go-wsc"
    wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

func main() {
    // 加载配置
    config := wscconfig.Default()
    
    // 创建 Hub（自动初始化日志器）
    hub := wsc.NewHub(config)
    
    // 启动 Hub
    go hub.Run()
    hub.WaitForStart()
    
    // 发送消息（自动记录日志）
    msg := &wsc.HubMessage{
        ID:      "msg-001",
        Type:    wsc.MessageTypeText,
        Content: "Hello World",
        From:    "user1",
        To:      "user2",
    }
    
    err := hub.SendToUser(context.Background(), "user2", msg)
    if err != nil {
        // 错误会自动记录日志
        panic(err)
    }
}
```

## 日志输出示例

### 连接管理日志

```
[WSC] client_id=client-123 user_id=user-456 action=register_request 2025/11/22 20:27:12 ℹ️ [INFO] 连接事件
[WSC] client_id=client-123 user_id=user-456 user_type=normal total_connections=1 active_connections=1 2025/11/22 20:27:12 ℹ️ [INFO] 客户端连接成功
[WSC] client_id=client-123 user_id=user-456 action=heartbeat_timeout 2025/11/22 20:27:12 ℹ️ [INFO] 连接事件
[WSC] client_id=client-123 user_id=user-456 user_type=normal remaining_connections=0 2025/11/22 20:27:12 ℹ️ [INFO] 客户端断开连接
```

### 消息传输日志

```
[WSC] message_id=msg-001 from_user=user1 to_user=user2 message_type=text success=true 2025/11/22 20:27:12 ℹ️ [INFO] 消息发送成功
[WSC] message_id=msg-002 from=user1 type=text content_length=25 target_clients=10 2025/11/22 20:27:12 ℹ️ [INFO] 发送广播消息
[WSC] message_id=msg-003 from=user1 to=user2 type=text 2025/11/22 20:27:12 ⚠️ [WARN] 用户离线，消息发送失败
```

### 性能监控日志

```
[WSC] operation=hub_metrics duration=5m active_websocket_clients=15 active_sse_clients=5 total_connections=1024 total_messages_sent=50000 total_broadcasts_sent=1000 node_id=node-127.0.0.1-8080-xxx uptime_seconds=3600 2025/11/22 20:27:12 ℹ️ [INFO] 性能统计
```

### 错误处理日志

```
[WSC] message_id=msg-004 from=user1 to=user2 type=text total_retries=3 total_time=1.5s final_error=connection timeout 2025/11/22 20:27:12 ❌ [ERROR] 消息发送重试失败
[WSC] client_id=client-789 user_id=user-999 message_id=msg-005 message_type=text 2025/11/22 20:27:12 ⚠️ [WARN] 客户端发送队列已满，跳过消息
```

## 自定义日志器

```go
// 创建自定义 WSC 日志器
logger := wsc.NewDefaultWSCLogger()

// 带字段的日志
contextLogger := logger.WithField("module", "authentication").
                       WithField("session_id", "sess_12345")

contextLogger.Info("用户登录成功")
contextLogger.LogConnection("conn_123", "user_789", "authenticated")

// 全局日志器设置
wsc.SetDefaultLogger(logger)

// 使用全局方法
wsc.Info("系统启动")
wsc.InfoKV("服务器状态", "status", "running", "uptime", "1h30m")
```

## 业务特定日志方法

### LogConnection - 连接事件日志
```go
// 记录连接事件
hub.logger.LogConnection("client-123", "user-456", "connected")
hub.logger.LogConnection("client-123", "user-456", "disconnected") 
hub.logger.LogConnection("client-123", "user-456", "heartbeat_timeout")
```

### LogMessage - 消息发送日志
```go
// 记录消息发送
hub.logger.LogMessage("msg-001", "user1", "user2", wsc.MessageTypeText, true, nil)
hub.logger.LogMessage("msg-002", "user1", "user2", wsc.MessageTypeText, false, err)
```

### LogPerformance - 性能统计日志
```go
// 记录性能指标
hub.logger.LogPerformance("send_message", "10ms", map[string]interface{}{
    "message_count": 100,
    "queue_size": 50,
    "memory_usage": "10MB",
})
```

## 性能优化

1. **异步日志**：日志记录不阻塞业务逻辑
2. **结构化日志**：使用键值对格式便于查询分析
3. **日志级别控制**：生产环境可调整为 WARN 或 ERROR 级别
4. **文件轮转**：自动管理日志文件大小和数量
5. **缓冲输出**：减少 I/O 操作提升性能

## 监控和告警

### Prometheus 指标集成

```go
// 在性能日志基础上可以导出 Prometheus 指标
// 连接数指标
hub_active_connections{node_id="xxx"} 15
hub_total_connections{node_id="xxx"} 1024

// 消息指标  
hub_messages_sent_total{node_id="xxx"} 50000
hub_broadcasts_sent_total{node_id="xxx"} 1000

// 错误率指标
hub_send_failures_total{reason="timeout"} 10
hub_send_failures_total{reason="queue_full"} 5
```

### 日志分析建议

1. **ELK Stack**：使用 Elasticsearch + Logstash + Kibana 分析日志
2. **Grafana Loki**：轻量级日志聚合和查询
3. **自定义脚本**：基于结构化日志格式编写分析脚本

## 故障排查

### 常见问题和解决方案

1. **日志不输出**
   - 检查日志级别配置
   - 确认日志文件路径权限
   - 验证日志器初始化

2. **性能问题**
   - 调整日志级别到 WARN 以上
   - 启用异步日志输出
   - 检查磁盘 I/O 性能

3. **日志文件过大**
   - 配置日志轮转参数
   - 启用压缩选项
   - 定期清理历史日志

## 最佳实践

1. **生产环境配置**
   ```yaml
   logging:
     level: "warn"
     format: "json"
     colorful: false
     show_caller: false
   ```

2. **开发环境配置**
   ```yaml
   logging:
     level: "debug"
     format: "console"
     colorful: true
     show_caller: true
   ```

3. **性能敏感场景**
   - 使用 `NoOpLogger` 关闭日志
   - 只记录关键错误日志
   - 使用采样降低日志量

## 升级说明

### 从旧版本升级

如果您从没有日志集成的 go-wsc 版本升级，只需要：

1. 更新依赖：`go get github.com/kamalyes/go-wsc@latest`
2. 添加日志配置（可选，有默认配置）
3. 重新编译应用程序

日志功能是向后兼容的，不会影响现有功能。

---

## 相关链接

- [go-logger 文档](https://github.com/kamalyes/go-logger)
- [go-config 文档](https://github.com/kamalyes/go-config)
- [go-wsc 主文档](../README.md)
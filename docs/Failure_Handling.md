# 失败处理与重试机制 🔄

本文档详细介绍 go-wsc 的失败处理和重试机制，包括设计理念、使用方法和最佳实践。

## 🎯 设计理念

### 失败分类处理

go-wsc 采用分类处理的方式，针对不同类型的失败场景提供专门的处理器：

```
📊 失败处理体系
├── SendFailureHandler      (通用失败处理器)
├── QueueFullHandler       (队列满处理器)  
├── UserOfflineHandler     (用户离线处理器)
├── ConnectionErrorHandler (连接错误处理器)
└── TimeoutHandler         (超时处理器)
```

### 智能重试机制

基于 go-toolbox/pkg/retry 模块，提供：
- **指数退避**：延迟时间按指数增长
- **错误分类**：区分可重试和不可重试错误
- **详细记录**：记录每次重试的详细信息
- **配置驱动**：通过配置文件灵活调整参数

## 🛠️ 核心接口

### 失败处理器接口

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

### 重试结果结构

```go
// SendResult 发送结果
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

## ⚙️ 配置管理

### go-config/wsc 配置

重试和失败处理参数通过 `go-config/wsc` 包统一管理：

```yaml
# config.yaml
wsc:
  max_retries: 3
  base_delay: 100ms
  backoff_factor: 2.0
  retryable_errors:
    - "queue_full"
    - "timeout"
    - "conn_error"
    - "channel_closed"
    - "network_unreachable"
  non_retryable_errors:
    - "user_offline"
    - "permission"
    - "validation"
    - "authentication_failed"
    - "message_too_large"
```

### 配置参数说明

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `max_retries` | int | 3 | 最大重试次数 |
| `base_delay` | duration | 100ms | 基础延迟时间 |
| `backoff_factor` | float64 | 2.0 | 退避因子 |
| `retryable_errors` | []string | 见上 | 可重试的错误类型 |
| `non_retryable_errors` | []string | 见上 | 不可重试的错误类型 |

## 🚀 使用示例

### 基础失败处理器

```go
package main

import (
    "log"
    "github.com/kamalyes/go-wsc"
)

// 基础失败处理器实现
type BasicFailureHandler struct{}

func (h *BasicFailureHandler) HandleSendFailure(msg *wsc.HubMessage, recipient string, reason string, err error) {
    log.Printf("🚨 消息发送失败:")
    log.Printf("   接收者: %s", recipient)
    log.Printf("   失败原因: %s", reason)
    log.Printf("   消息ID: %s", msg.ID)
    log.Printf("   错误详情: %v", err)
    
    // 根据失败原因采取不同措施
    switch reason {
    case wsc.SendFailureReasonUserOffline:
        h.handleOfflineMessage(msg, recipient)
    case wsc.SendFailureReasonQueueFull:
        h.handleQueueFull(msg, recipient)
    case wsc.SendFailureReasonTimeout:
        h.handleTimeout(msg, recipient)
    default:
        h.handleGenericFailure(msg, recipient, err)
    }
}

func (h *BasicFailureHandler) handleOfflineMessage(msg *wsc.HubMessage, recipient string) {
    // 存储离线消息
    log.Printf("💾 存储离线消息: %s -> %s", msg.ID, recipient)
}

func (h *BasicFailureHandler) handleQueueFull(msg *wsc.HubMessage, recipient string) {
    // 队列满的处理逻辑
    log.Printf("📦 队列满，延迟处理: %s", msg.ID)
}

func (h *BasicFailureHandler) handleTimeout(msg *wsc.HubMessage, recipient string) {
    // 超时处理逻辑
    log.Printf("⏰ 消息超时: %s", msg.ID)
}

func (h *BasicFailureHandler) handleGenericFailure(msg *wsc.HubMessage, recipient string, err error) {
    // 通用失败处理
    log.Printf("❌ 通用失败处理: %v", err)
}

func main() {
    hub := wsc.NewHub()
    
    // 添加失败处理器
    hub.AddSendFailureHandler(&BasicFailureHandler{})
    
    go hub.Run()
    // ... 其他初始化代码
}
```

### 专业化失败处理器

```go
package main

import (
    "context"
    "database/sql"
    "encoding/json"
    "fmt"
    "log"
    "sync"
    "time"
    
    "github.com/go-redis/redis/v8"
    "github.com/kamalyes/go-wsc"
)

// 队列满处理器 - 使用Redis作为备用存储
type RedisQueueFullHandler struct {
    client    *redis.Client
    keyPrefix string
    ttl       time.Duration
}

func NewRedisQueueFullHandler(redisAddr, keyPrefix string, ttl time.Duration) *RedisQueueFullHandler {
    client := redis.NewClient(&redis.Options{
        Addr: redisAddr,
    })
    
    return &RedisQueueFullHandler{
        client:    client,
        keyPrefix: keyPrefix,
        ttl:       ttl,
    }
}

func (h *RedisQueueFullHandler) HandleQueueFull(msg *wsc.HubMessage, recipient string, queueType string, err error) {
    log.Printf("📦 队列满处理 - 类型: %s, 接收者: %s, 消息: %s", queueType, recipient, msg.ID)
    
    // 序列化消息
    msgData, err := json.Marshal(msg)
    if err != nil {
        log.Printf("❌ 序列化消息失败: %v", err)
        return
    }
    
    // 存储到Redis队列
    key := fmt.Sprintf("%s%s:%s", h.keyPrefix, recipient, queueType)
    ctx := context.Background()
    
    // 使用LPUSH添加到队列头部，确保FIFO顺序
    err = h.client.LPush(ctx, key, msgData).Err()
    if err != nil {
        log.Printf("❌ Redis存储失败: %v", err)
        return
    }
    
    // 设置过期时间
    h.client.Expire(ctx, key, h.ttl)
    
    log.Printf("✅ 消息已存储到Redis备用队列: %s", key)
}

// 用户离线处理器 - 使用数据库存储离线消息
type DatabaseOfflineHandler struct {
    db            *sql.DB
    maxOfflineMsg int
    mutex         sync.RWMutex
}

func NewDatabaseOfflineHandler(db *sql.DB, maxOfflineMsg int) *DatabaseOfflineHandler {
    return &DatabaseOfflineHandler{
        db:            db,
        maxOfflineMsg: maxOfflineMsg,
    }
}

func (h *DatabaseOfflineHandler) HandleUserOffline(msg *wsc.HubMessage, userID string, err error) {
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    log.Printf("👤 用户离线处理 - 用户: %s, 消息: %s", userID, msg.ID)
    
    // 检查用户离线消息数量
    var count int
    countQuery := `SELECT COUNT(*) FROM offline_messages WHERE user_id = ?`
    err = h.db.QueryRow(countQuery, userID).Scan(&count)
    if err != nil {
        log.Printf("❌ 查询离线消息数量失败: %v", err)
        return
    }
    
    // 如果超过最大限制，删除最旧的消息
    if count >= h.maxOfflineMsg {
        deleteQuery := `DELETE FROM offline_messages WHERE user_id = ? ORDER BY created_at ASC LIMIT 1`
        _, err = h.db.Exec(deleteQuery, userID)
        if err != nil {
            log.Printf("❌ 删除旧离线消息失败: %v", err)
        }
    }
    
    // 插入新的离线消息
    insertQuery := `INSERT INTO offline_messages (user_id, message_id, message_type, content, data, created_at) VALUES (?, ?, ?, ?, ?, ?)`
    
    dataJSON, _ := json.Marshal(msg.Data)
    _, err = h.db.Exec(insertQuery, userID, msg.ID, msg.Type, msg.Content, string(dataJSON), msg.CreateAt)
    if err != nil {
        log.Printf("❌ 存储离线消息失败: %v", err)
        return
    }
    
    log.Printf("✅ 离线消息已存储到数据库: 用户=%s, 消息=%s", userID, msg.ID)
    
    // 发送推送通知
    h.sendPushNotification(userID, msg)
}

func (h *DatabaseOfflineHandler) sendPushNotification(userID string, msg *wsc.HubMessage) {
    // 发送推送通知的逻辑
    log.Printf("📱 发送推送通知: 用户=%s, 内容=%s", userID, h.truncateContent(msg.Content, 50))
    // 这里可以集成APNs、FCM等推送服务
}

func (h *DatabaseOfflineHandler) truncateContent(content string, maxLen int) string {
    if len(content) <= maxLen {
        return content
    }
    return content[:maxLen] + "..."
}

// 连接错误处理器 - 处理网络连接问题
type ConnectionErrorHandler struct {
    reconnectAttempts map[string]int
    maxReconnect      int
    mutex             sync.RWMutex
}

func NewConnectionErrorHandler(maxReconnect int) *ConnectionErrorHandler {
    return &ConnectionErrorHandler{
        reconnectAttempts: make(map[string]int),
        maxReconnect:      maxReconnect,
    }
}

func (h *ConnectionErrorHandler) HandleConnectionError(msg *wsc.HubMessage, clientID string, err error) {
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    log.Printf("🔌 连接错误处理 - 客户端: %s, 错误: %v", clientID, err)
    
    h.reconnectAttempts[clientID]++
    attempts := h.reconnectAttempts[clientID]
    
    if attempts <= h.maxReconnect {
        log.Printf("🔄 尝试重新连接: 客户端=%s, 第%d次尝试", clientID, attempts)
        
        // 启动异步重连逻辑
        go h.attemptReconnection(clientID, msg)
    } else {
        log.Printf("❌ 重连次数超限，放弃重连: 客户端=%s", clientID)
        // 重置计数器
        delete(h.reconnectAttempts, clientID)
        
        // 将消息标记为永久失败
        h.markMessageAsPermanentlyFailed(msg, clientID)
    }
}

func (h *ConnectionErrorHandler) attemptReconnection(clientID string, msg *wsc.HubMessage) {
    // 等待一段时间后重试
    delay := time.Duration(h.reconnectAttempts[clientID]) * 2 * time.Second
    time.Sleep(delay)
    
    // 这里可以实现实际的重连逻辑
    log.Printf("🔄 执行重连: 客户端=%s, 延迟=%v", clientID, delay)
}

func (h *ConnectionErrorHandler) markMessageAsPermanentlyFailed(msg *wsc.HubMessage, clientID string) {
    log.Printf("💔 消息永久失败: 消息=%s, 客户端=%s", msg.ID, clientID)
    // 这里可以记录到失败消息表或发送告警
}

// 超时处理器 - 处理各种超时情况
type TimeoutHandler struct {
    timeoutCounts map[string]int
    alertThreshold int
    mutex         sync.RWMutex
}

func NewTimeoutHandler(alertThreshold int) *TimeoutHandler {
    return &TimeoutHandler{
        timeoutCounts:  make(map[string]int),
        alertThreshold: alertThreshold,
    }
}

func (h *TimeoutHandler) HandleTimeout(msg *wsc.HubMessage, recipient string, timeoutType string, duration time.Duration, err error) {
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    log.Printf("⏰ 超时处理 - 接收者: %s, 类型: %s, 耗时: %v", recipient, timeoutType, duration)
    
    key := fmt.Sprintf("%s:%s", recipient, timeoutType)
    h.timeoutCounts[key]++
    
    if h.timeoutCounts[key] >= h.alertThreshold {
        h.sendTimeoutAlert(recipient, timeoutType, h.timeoutCounts[key], duration)
        // 重置计数器
        h.timeoutCounts[key] = 0
    }
    
    // 根据超时类型采取不同措施
    switch timeoutType {
    case "ack_timeout":
        h.handleAckTimeout(msg, recipient)
    case "send_timeout":
        h.handleSendTimeout(msg, recipient)
    case "connection_timeout":
        h.handleConnectionTimeout(msg, recipient)
    default:
        h.handleGenericTimeout(msg, recipient, timeoutType)
    }
}

func (h *TimeoutHandler) sendTimeoutAlert(recipient string, timeoutType string, count int, duration time.Duration) {
    alert := fmt.Sprintf("🚨 超时告警: 接收者=%s, 类型=%s, 次数=%d, 最近耗时=%v", 
        recipient, timeoutType, count, duration)
    log.Printf(alert)
    // 这里可以发送到告警系统，如钉钉、企业微信等
}

func (h *TimeoutHandler) handleAckTimeout(msg *wsc.HubMessage, recipient string) {
    log.Printf("📝 ACK超时处理: 消息=%s, 接收者=%s", msg.ID, recipient)
    // 可以重新发送ACK请求或标记消息为未确认
}

func (h *TimeoutHandler) handleSendTimeout(msg *wsc.HubMessage, recipient string) {
    log.Printf("📤 发送超时处理: 消息=%s, 接收者=%s", msg.ID, recipient)
    // 可以加入重试队列
}

func (h *TimeoutHandler) handleConnectionTimeout(msg *wsc.HubMessage, recipient string) {
    log.Printf("🔗 连接超时处理: 消息=%s, 接收者=%s", msg.ID, recipient)
    // 可以标记连接为不稳定
}

func (h *TimeoutHandler) handleGenericTimeout(msg *wsc.HubMessage, recipient string, timeoutType string) {
    log.Printf("⏱️  通用超时处理: 类型=%s, 消息=%s, 接收者=%s", timeoutType, msg.ID, recipient)
}

// 主程序 - 演示完整的失败处理器配置
func main() {
    // 创建Hub
    hub := wsc.NewHub()
    
    // 配置数据库连接
    db, err := sql.Open("mysql", "user:password@tcp(localhost:3306)/wsc_db")
    if err != nil {
        log.Fatal("数据库连接失败:", err)
    }
    defer db.Close()
    
    // 配置所有类型的失败处理器
    setupAllFailureHandlers(hub, db)
    
    // 启动Hub
    go hub.Run()
    
    // ... 其他服务器配置
    log.Println("🚀 带完整失败处理的WebSocket服务器启动")
}

func setupAllFailureHandlers(hub *wsc.Hub, db *sql.DB) {
    // 1. 通用失败处理器
    hub.AddSendFailureHandler(&BasicFailureHandler{})
    
    // 2. Redis队列满处理器
    redisHandler := NewRedisQueueFullHandler("localhost:6379", "wsc:queue:", 24*time.Hour)
    hub.AddQueueFullHandler(redisHandler)
    
    // 3. 数据库离线处理器
    offlineHandler := NewDatabaseOfflineHandler(db, 1000)
    hub.AddUserOfflineHandler(offlineHandler)
    
    // 4. 连接错误处理器
    connHandler := NewConnectionErrorHandler(3)
    hub.AddConnectionErrorHandler(connHandler)
    
    // 5. 超时处理器
    timeoutHandler := NewTimeoutHandler(5)
    hub.AddTimeoutHandler(timeoutHandler)
    
    log.Println("✅ 所有失败处理器配置完成")
}
```

### 重试机制使用

```go
package main

import (
    "context"
    "log"
    "time"
    
    "github.com/kamalyes/go-wsc"
)

func demonstrateRetryMechanism(hub *wsc.Hub) {
    // 创建测试消息
    msg := &wsc.HubMessage{
        ID:       "retry-demo-001",
        Type:     wsc.TextMessage,
        Content:  "这是一条带重试的测试消息",
        CreateAt: time.Now(),
        Priority: wsc.HighPriority,
    }
    
    // 使用重试机制发送消息
    ctx := context.Background()
    result := hub.SendToUserWithRetry(ctx, "test-user", msg)
    
    // 分析发送结果
    log.Printf("📊 发送结果分析:")
    log.Printf("   最终结果: %v", result.Success)
    log.Printf("   重试次数: %d", result.TotalRetries)
    log.Printf("   总耗时: %v", result.TotalTime)
    
    if result.FinalError != nil {
        log.Printf("   最终错误: %v", result.FinalError)
    }
    
    // 详细的重试历史
    log.Printf("📋 重试历史:")
    for i, attempt := range result.Attempts {
        status := "❌ 失败"
        if attempt.Success {
            status = "✅ 成功"
        }
        log.Printf("   尝试 %d: %s (耗时: %v)", 
            attempt.AttemptNumber, status, attempt.Duration)
        if attempt.Error != nil {
            log.Printf("      错误: %v", attempt.Error)
        }
    }
    
    // 性能分析
    if len(result.Attempts) > 1 {
        avgDuration := result.TotalTime / time.Duration(len(result.Attempts))
        log.Printf("📈 性能指标:")
        log.Printf("   平均耗时: %v", avgDuration)
        log.Printf("   成功率: %.2f%%", 
            float64(countSuccessfulAttempts(result.Attempts))/float64(len(result.Attempts))*100)
    }
}

func countSuccessfulAttempts(attempts []wsc.SendAttempt) int {
    count := 0
    for _, attempt := range attempts {
        if attempt.Success {
            count++
        }
    }
    return count
}

// 批量消息重试示例
func sendBatchMessagesWithRetry(hub *wsc.Hub, userIDs []string, content string) {
    results := make(map[string]*wsc.SendResult)
    
    for _, userID := range userIDs {
        msg := &wsc.HubMessage{
            ID:       fmt.Sprintf("batch-%s-%d", userID, time.Now().Unix()),
            Type:     wsc.TextMessage,
            Content:  content,
            CreateAt: time.Now(),
        }
        
        // 并发发送
        go func(uid string, message *wsc.HubMessage) {
            result := hub.SendToUserWithRetry(context.Background(), uid, message)
            results[uid] = result
            
            if !result.Success {
                log.Printf("❌ 用户 %s 消息发送失败: %v (重试 %d 次)", 
                    uid, result.FinalError, result.TotalRetries)
            }
        }(userID, msg)
    }
    
    // 等待一段时间后统计结果
    time.Sleep(10 * time.Second)
    
    successCount := 0
    totalRetries := 0
    for userID, result := range results {
        if result.Success {
            successCount++
        }
        totalRetries += result.TotalRetries
        log.Printf("用户 %s: 成功=%v, 重试=%d", userID, result.Success, result.TotalRetries)
    }
    
    log.Printf("📊 批量发送统计:")
    log.Printf("   成功率: %.2f%% (%d/%d)", 
        float64(successCount)/float64(len(userIDs))*100, successCount, len(userIDs))
    log.Printf("   总重试次数: %d", totalRetries)
}
```

## 📊 监控与调试

### 失败统计和指标

```go
package main

import (
    "log"
    "sync/atomic"
    "time"
)

// 失败统计处理器
type MetricsFailureHandler struct {
    totalFailures   int64
    queueFullCount  int64
    offlineCount    int64
    timeoutCount    int64
    connErrorCount  int64
    startTime       time.Time
}

func NewMetricsFailureHandler() *MetricsFailureHandler {
    return &MetricsFailureHandler{
        startTime: time.Now(),
    }
}

func (h *MetricsFailureHandler) HandleSendFailure(msg *wsc.HubMessage, recipient string, reason string, err error) {
    atomic.AddInt64(&h.totalFailures, 1)
    
    switch reason {
    case wsc.SendFailureReasonQueueFull:
        atomic.AddInt64(&h.queueFullCount, 1)
    case wsc.SendFailureReasonUserOffline:
        atomic.AddInt64(&h.offlineCount, 1)
    case wsc.SendFailureReasonTimeout:
        atomic.AddInt64(&h.timeoutCount, 1)
    case wsc.SendFailureReasonConnError:
        atomic.AddInt64(&h.connErrorCount, 1)
    }
    
    // 每100次失败输出一次统计
    if atomic.LoadInt64(&h.totalFailures)%100 == 0 {
        h.printStatistics()
    }
}

func (h *MetricsFailureHandler) printStatistics() {
    uptime := time.Since(h.startTime)
    total := atomic.LoadInt64(&h.totalFailures)
    queueFull := atomic.LoadInt64(&h.queueFullCount)
    offline := atomic.LoadInt64(&h.offlineCount)
    timeout := atomic.LoadInt64(&h.timeoutCount)
    connError := atomic.LoadInt64(&h.connErrorCount)
    
    log.Printf("📊 失败统计 (运行时间: %v):", uptime)
    log.Printf("   总失败: %d", total)
    log.Printf("   队列满: %d (%.1f%%)", queueFull, float64(queueFull)/float64(total)*100)
    log.Printf("   用户离线: %d (%.1f%%)", offline, float64(offline)/float64(total)*100)
    log.Printf("   超时: %d (%.1f%%)", timeout, float64(timeout)/float64(total)*100)
    log.Printf("   连接错误: %d (%.1f%%)", connError, float64(connError)/float64(total)*100)
    log.Printf("   失败率: %.2f failures/min", float64(total)/uptime.Minutes())
}

// 获取实时统计信息
func (h *MetricsFailureHandler) GetStatistics() map[string]interface{} {
    return map[string]interface{}{
        "total_failures":   atomic.LoadInt64(&h.totalFailures),
        "queue_full_count": atomic.LoadInt64(&h.queueFullCount),
        "offline_count":    atomic.LoadInt64(&h.offlineCount),
        "timeout_count":    atomic.LoadInt64(&h.timeoutCount),
        "conn_error_count": atomic.LoadInt64(&h.connErrorCount),
        "uptime_seconds":   time.Since(h.startTime).Seconds(),
    }
}
```

### 健康检查和诊断

```go
// 健康检查端点
func setupHealthCheck(hub *wsc.Hub, metricsHandler *MetricsFailureHandler) {
    http.HandleFunc("/health/failure-stats", func(w http.ResponseWriter, r *http.Request) {
        stats := metricsHandler.GetStatistics()
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(stats)
    })
    
    http.HandleFunc("/health/retry-config", func(w http.ResponseWriter, r *http.Request) {
        config := map[string]interface{}{
            "max_retries":          hub.GetConfig().MaxRetries,
            "base_delay":           hub.GetConfig().BaseDelay.String(),
            "backoff_factor":       hub.GetConfig().BackoffFactor,
            "retryable_errors":     hub.GetConfig().RetryableErrors,
            "non_retryable_errors": hub.GetConfig().NonRetryableErrors,
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(config)
    })
}
```

## 🎯 最佳实践

### 1. 处理器设计原则

- **单一职责**：每个处理器只负责一种类型的失败
- **异步处理**：避免阻塞主要消息流
- **异常安全**：处理器中的panic不应影响主流程
- **性能考虑**：避免在处理器中执行耗时操作

### 2. 错误分类策略

```go
// 推荐的错误分类
var (
    RetryableErrors = []string{
        "queue_full",           // 队列满 - 临时问题
        "timeout",              // 超时 - 可能是网络问题
        "conn_error",           // 连接错误 - 可恢复
        "channel_closed",       // 通道关闭 - 临时问题
        "network_unreachable",  // 网络不可达 - 临时问题
        "temporary",            // 任何包含"temporary"的错误
    }
    
    NonRetryableErrors = []string{
        "user_offline",         // 用户离线 - 明确状态
        "permission",           // 权限问题 - 需要人工处理
        "validation",           // 验证失败 - 消息格式问题
        "authentication_failed", // 认证失败 - 权限问题
        "message_too_large",    // 消息过大 - 永久问题
        "rate_limit_permanent", // 永久限速 - 需要等待
    }
)
```

### 3. 配置优化建议

```yaml
# 开发环境配置
dev:
  wsc:
    max_retries: 2
    base_delay: 50ms
    backoff_factor: 1.5

# 生产环境配置
prod:
  wsc:
    max_retries: 5
    base_delay: 200ms
    backoff_factor: 2.0
    
# 高可靠性环境配置
high_reliability:
  wsc:
    max_retries: 10
    base_delay: 100ms
    backoff_factor: 1.2  # 较小的退避因子，更快重试
```

### 4. 性能优化

- **批量处理**：在处理器中批量处理多个消息
- **连接池**：复用数据库和Redis连接
- **缓存**：缓存频繁查询的数据
- **限流**：防止失败处理器本身成为瓶颈

```go
// 批量处理示例
type BatchedOfflineHandler struct {
    batch     []*OfflineMessage
    batchSize int
    ticker    *time.Ticker
    mutex     sync.Mutex
}

type OfflineMessage struct {
    UserID  string
    Message *wsc.HubMessage
}

func (h *BatchedOfflineHandler) HandleUserOffline(msg *wsc.HubMessage, userID string, err error) {
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    h.batch = append(h.batch, &OfflineMessage{
        UserID:  userID,
        Message: msg,
    })
    
    if len(h.batch) >= h.batchSize {
        h.flushBatch()
    }
}

func (h *BatchedOfflineHandler) flushBatch() {
    if len(h.batch) == 0 {
        return
    }
    
    // 批量插入数据库
    go func(messages []*OfflineMessage) {
        // 批量处理逻辑
        log.Printf("📦 批量处理 %d 条离线消息", len(messages))
    }(h.batch)
    
    h.batch = h.batch[:0] // 清空slice，但保留容量
}
```

### 5. 监控告警

- **关键指标**：失败率、重试成功率、平均重试次数
- **告警阈值**：根据业务特点设置合理阈值
- **分级告警**：区分警告和紧急告警
- **趋势分析**：关注失败模式的变化趋势

## 🔍 故障排查

### 常见问题和解决方案

1. **重试次数过多**
   - 检查网络连接稳定性
   - 调整退避参数
   - 优化错误分类配置

2. **处理器性能问题**
   - 使用异步处理
   - 实现批量操作
   - 添加性能监控

3. **内存泄漏**
   - 检查处理器中的goroutine泄漏
   - 正确关闭数据库连接和Redis客户端
   - 监控内存使用情况

4. **配置不生效**
   - 验证配置文件路径和格式
   - 检查配置加载时机
   - 使用配置验证工具

通过合理使用失败处理和重试机制，可以大大提高 WebSocket 服务的可靠性和用户体验。记住根据具体业务场景调整配置参数和处理策略。
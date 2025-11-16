# ACK 消息确认机制详解 📡

> 本文档深入介绍 go-wsc 的消息确认(ACK)机制，确保关键消息的可靠传输。

## 📖 目录

- [ACK 机制概述](#-ack-机制概述)
- [配置说明](#-配置说明)
- [使用示例](#-使用示例)
- [消息状态管理](#-消息状态管理)
- [失败重试策略](#-失败重试策略)
- [监控与调试](#-监控与调试)

## 🔄 ACK 机制概述

ACK (Acknowledgment) 确认机制确保重要消息能够被可靠传输。当启用 ACK 时，发送方会等待接收方的确认回复，如果在指定时间内未收到确认，将自动重试发送。

### 工作流程

```bash
发送端                           接收端
  │                               │
  │ ──── 发送消息(ID: msg_001) ───► │
  │                               │ 处理消息
  │                               │
  │ ◄── ACK确认(ID: msg_001) ──── │
  │                               │
  │ ✅ 标记为已确认               │
  
  
超时重试场景:
  │                               │
  │ ──── 发送消息(ID: msg_002) ───► │ (网络丢失)
  │                               │
  │ ⏰ 等待超时 (5秒)              │
  │                               │
  │ ──── 重试发送(ID: msg_002) ───► │ (通过go-toolbox重试引擎)
  │                               │ 处理消息
  │ ◄── ACK确认(ID: msg_002) ──── │
  │                               │
  │ ✅ 标记为已确认               │
```

### 核心特性

- **智能重试**: 基于 go-toolbox/pkg/retry 的重试引擎，支持指数退避
- **失败处理**: 集成5类专业化失败处理器，全面覆盖各种失败场景
- **状态跟踪**: 完整的消息状态生命周期管理
- **配置驱动**: 通过 go-config/wsc 统一管理ACK相关配置
- **批量处理**: 支持批量重试失败消息
- **离线缓存**: 连接断开时缓存待确认消息

## ⚙️ 配置说明

### go-config/wsc 统一配置

ACK 机制的配置现在通过 `go-config/wsc` 包统一管理：

```yaml
# config.yaml
wsc:
  # ACK 相关配置
  ack_timeout: 30s                    # ACK 超时时间
  ack_max_retries: 5                  # ACK 最大重试次数
  ack_retry_interval: 2s              # ACK 重试间隔
  enable_offline_ack: true            # 启用离线消息 ACK
  
  # 重试机制配置（与ACK集成）
  max_retries: 3                      # 通用重试次数
  base_delay: 100ms                   # 基础延迟
  backoff_factor: 2.0                 # 退避因子
  
  # ACK 错误分类
  retryable_errors:
    - "ack_timeout"                   # ACK 超时可重试
    - "network_error"                 # 网络错误可重试
    - "temporary_failure"             # 临时失败可重试
    
  non_retryable_errors:
    - "invalid_message_format"        # 消息格式错误不可重试
    - "permission_denied"             # 权限错误不可重试
```

### 代码配置

```go
package main

import (
    "time"
    "github.com/kamalyes/go-wsc"
    wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

func configureACK(hub *wsc.Hub) {
    // ACK 配置会自动从 go-config/wsc 加载
    // 也可以手动设置特定参数
    
    // 设置 ACK 超时时间
    hub.SetACKTimeout(30 * time.Second)
    
    // 设置最大重试次数
    hub.SetACKMaxRetries(5)
    
    // 启用离线消息 ACK
    hub.EnableOfflineACK(true)
}

// 自定义 ACK 配置
func setupCustomACKConfig() *wscconfig.WSC {
    return &wscconfig.WSC{
        AckTimeout:        30 * time.Second,
        AckMaxRetries:     5,
        AckRetryInterval:  2 * time.Second,
        EnableOfflineAck:  true,
        MaxRetries:        3,
        BaseDelay:         100 * time.Millisecond,
        BackoffFactor:     2.0,
    }
}
```

## 🔄 与失败处理器集成

ACK 机制与新的失败处理器系统深度集成，提供全面的错误处理能力：

### ACK 超时处理器

```go
// ACK 专门的超时处理器
type ACKTimeoutHandler struct {
    maxRetryAttempts int
    backoffDuration  time.Duration
}

func (h *ACKTimeoutHandler) HandleTimeout(msg *wsc.HubMessage, recipient string, timeoutType string, duration time.Duration, err error) {
    if timeoutType == "ack_timeout" {
        log.Printf("⏰ ACK 超时处理 - 消息: %s, 接收者: %s, 耗时: %v", msg.ID, recipient, duration)
        
        // 特殊的 ACK 超时处理逻辑
        h.handleACKTimeout(msg, recipient, duration)
    }
}

func (h *ACKTimeoutHandler) handleACKTimeout(msg *wsc.HubMessage, recipient string, duration time.Duration) {
    // 检查是否应该重试
    if h.shouldRetryACK(msg, duration) {
        // 延迟重试
        go func() {
            time.Sleep(h.backoffDuration)
            // 重新发送 ACK 消息
            log.Printf("🔄 重试 ACK 消息: %s -> %s", msg.ID, recipient)
        }()
    } else {
        // 标记为永久失败
        log.Printf("❌ ACK 消息永久失败: %s", msg.ID)
        h.handlePermanentACKFailure(msg, recipient)
    }
}

func (h *ACKTimeoutHandler) shouldRetryACK(msg *wsc.HubMessage, duration time.Duration) bool {
    // 根据消息优先级和耗时决定是否重试
    if msg.Priority == wsc.HighPriority {
        return true // 高优先级消息总是重试
    }
    return duration < 10*time.Second // 超时时间较短时重试
}

func (h *ACKTimeoutHandler) handlePermanentACKFailure(msg *wsc.HubMessage, recipient string) {
    // 记录永久失败的 ACK
    log.Printf("📝 记录 ACK 永久失败: 消息=%s, 接收者=%s", msg.ID, recipient)
    // 可以发送告警或记录到数据库
}
```

### ACK 离线处理器

```go
// ACK 用户离线处理器
type ACKOfflineHandler struct {
    offlineACKStore map[string][]*wsc.HubMessage
    maxOfflineACK   int
    mutex           sync.RWMutex
}

func NewACKOfflineHandler(maxOfflineACK int) *ACKOfflineHandler {
    return &ACKOfflineHandler{
        offlineACKStore: make(map[string][]*wsc.HubMessage),
        maxOfflineACK:   maxOfflineACK,
    }
}

func (h *ACKOfflineHandler) HandleUserOffline(msg *wsc.HubMessage, userID string, err error) {
    // 只处理需要 ACK 的消息
    if !msg.RequireAck {
        return
    }
    
    h.mutex.Lock()
    defer h.mutex.Unlock()
    
    log.Printf("📱 用户离线 ACK 处理 - 用户: %s, 消息: %s", userID, msg.ID)
    
    // 检查离线 ACK 消息数量限制
    if len(h.offlineACKStore[userID]) < h.maxOfflineACK {
        h.offlineACKStore[userID] = append(h.offlineACKStore[userID], msg)
        log.Printf("💾 存储离线 ACK 消息: 用户=%s, 消息=%s", userID, msg.ID)
    } else {
        // 删除最旧的 ACK 消息
        h.offlineACKStore[userID] = h.offlineACKStore[userID][1:]
        h.offlineACKStore[userID] = append(h.offlineACKStore[userID], msg)
        log.Printf("🔄 替换最旧的离线 ACK 消息: 用户=%s", userID)
    }
}

// 用户重新上线时处理离线 ACK 消息
func (h *ACKOfflineHandler) ProcessOfflineACKMessages(userID string, hub *wsc.Hub) {
    h.mutex.Lock()
    messages := h.offlineACKStore[userID]
    delete(h.offlineACKStore, userID) // 清空离线消息
    h.mutex.Unlock()
    
    if len(messages) == 0 {
        return
    }
    
    log.Printf("📬 处理用户 %s 的 %d 条离线 ACK 消息", userID, len(messages))
    
    for _, msg := range messages {
        // 重新发送需要 ACK 的消息
        go func(message *wsc.HubMessage) {
            ctx := context.Background()
            result := hub.SendToUserWithRetry(ctx, userID, message)
            if !result.Success {
                log.Printf("❌ 离线 ACK 消息重发失败: %s -> %s, 错误: %v", 
                    message.ID, userID, result.FinalError)
            }
        }(msg)
    }
}
```

### ACK 与重试引擎集成

```go
// ACK 消息的智能重试
func (hub *Hub) SendACKMessageWithRetry(ctx context.Context, toUserID string, msg *HubMessage) *SendResult {
    // 标记消息需要 ACK
    msg.RequireAck = true
    
    // 创建 ACK 管理器
    ackManager := hub.GetACKManager()
    
    // 使用重试机制发送消息
    result := hub.SendToUserWithRetry(ctx, toUserID, msg)
    
    if result.Success {
        // 发送成功，开始等待 ACK
        ackManager.WaitForACK(msg.ID, func(ackReceived bool) {
            if ackReceived {
                log.Printf("✅ ACK 确认收到: %s", msg.ID)
            } else {
                log.Printf("⏰ ACK 等待超时: %s", msg.ID)
                // 触发 ACK 超时处理器
                hub.notifyTimeout(msg, toUserID, "ack_timeout", hub.config.AckTimeout, 
                    fmt.Errorf("ACK timeout for message %s", msg.ID))
            }
        })
    }
    
    return result
}

// 扩展的 ACK 管理器
type EnhancedACKManager struct {
    pendingACKs    map[string]*ACKInfo
    mutex          sync.RWMutex
    timeoutHandler func(string, time.Duration) // ACK 超时回调
}

type ACKInfo struct {
    Message     *HubMessage
    UserID      string
    SentAt      time.Time
    RetryCount  int
    MaxRetries  int
    Timeout     time.Duration
}

func (m *EnhancedACKManager) WaitForACK(messageID string, callback func(bool)) {
    m.mutex.Lock()
    ackInfo, exists := m.pendingACKs[messageID]
    m.mutex.Unlock()
    
    if !exists {
        callback(false)
        return
    }
    
    // 设置超时处理
    go func() {
        select {
        case <-time.After(ackInfo.Timeout):
            // 超时处理
            m.mutex.Lock()
            delete(m.pendingACKs, messageID)
            m.mutex.Unlock()
            
            callback(false)
            
            // 触发超时处理器
            if m.timeoutHandler != nil {
                m.timeoutHandler(messageID, ackInfo.Timeout)
            }
            
        case <-m.waitForACKConfirmation(messageID):
            // 收到 ACK 确认
            callback(true)
        }
    }()
}

func (m *EnhancedACKManager) waitForACKConfirmation(messageID string) <-chan struct{} {
    // 返回一个通道，当收到 ACK 时关闭
    ch := make(chan struct{})
    // 实际实现中，这个通道会在 ConfirmMessage 方法中被关闭
    return ch
}
```

### 服务端 ACK 配置

```go
// ack.go - ACK 配置结构
type ACKConfig struct {
    Enabled         bool          `json:"enabled"`          // 是否启用 ACK
    Timeout         time.Duration `json:"timeout"`          // ACK 超时时间
    MaxRetries      int           `json:"max_retries"`      // 最大重试次数
    RetryInterval   time.Duration `json:"retry_interval"`   // 基础重试间隔
    BackoffFactor   float64       `json:"backoff_factor"`   // 退避因子
    CleanupInterval time.Duration `json:"cleanup_interval"` // 清理间隔
}

// 默认配置
func NewDefaultACKConfig() *ACKConfig {
    return &ACKConfig{
        Enabled:         true,
        Timeout:         5 * time.Second,
        MaxRetries:      3,
        RetryInterval:   1 * time.Second,
        BackoffFactor:   2.0,
        CleanupInterval: 30 * time.Second,
    }
}
```

### 使用配置

```go
package main

import (
    "time"
    "github.com/kamalyes/go-wsc"
)

func main() {
    // 创建 Hub 并配置 ACK
    hub := wsc.NewHub()
    
    // 自定义 ACK 配置
    ackConfig := &wsc.ACKConfig{
        Enabled:         true,
        Timeout:         10 * time.Second,  // 10秒超时
        MaxRetries:      5,                 // 最多重试5次
        RetryInterval:   2 * time.Second,   // 初始重试间隔2秒
        BackoffFactor:   1.5,              // 每次重试间隔增长50%
        CleanupInterval: 60 * time.Second,  // 60秒清理一次过期记录
    }
    
    hub.SetACKConfig(ackConfig)
    
    // 启动 Hub
    go hub.Run()
    
    // ... 其他代码
}
```

## 🎯 使用示例

### 服务端发送 ACK 消息

```go
// 发送需要确认的消息
func sendImportantMessage(hub *wsc.Hub, clientID string, data interface{}) error {
    message := &wsc.Message{
        ID:       wsc.GenerateMessageID(),
        Type:     wsc.MessageTypeText,
        From:     "system",
        To:       clientID,
        Content:  data,
        NeedsACK: true, // 标记为需要确认
        Timestamp: time.Now(),
    }
    
    // 发送消息
    err := hub.SendToClient(clientID, message)
    if err != nil {
        return fmt.Errorf("发送消息失败: %w", err)
    }
    
    log.Printf("发送ACK消息: ID=%s, To=%s", message.ID, clientID)
    return nil
}

// 批量发送消息
func broadcastImportantMessage(hub *wsc.Hub, groupID string, data interface{}) error {
    message := &wsc.Message{
        ID:       wsc.GenerateMessageID(),
        Type:     wsc.MessageTypeText,
        From:     "system",
        To:       groupID,
        Content:  data,
        NeedsACK: true,
        Timestamp: time.Now(),
    }
    
    // 广播到群组
    err := hub.BroadcastToGroup(groupID, message)
    if err != nil {
        return fmt.Errorf("广播消息失败: %w", err)
    }
    
    log.Printf("广播ACK消息: ID=%s, Group=%s", message.ID, groupID)
    return nil
}
```

### 客户端处理 ACK

```go
// 客户端接收并确认消息
func setupACKHandling(client *wsc.Wsc) {
    client.OnTextMessageReceived(func(messageStr string) {
        var message wsc.Message
        err := json.Unmarshal([]byte(messageStr), &message)
        if err != nil {
            log.Printf("解析消息失败: %v", err)
            return
        }
        
        // 处理消息内容
        handleMessage(&message)
        
        // 如果消息需要确认，发送 ACK
        if message.NeedsACK {
            ackMessage := &wsc.Message{
                Type:    wsc.MessageTypeACK,
                RefID:   message.ID,        // 引用原消息 ID
                From:    message.To,        // 发送者变为接收者
                To:      message.From,      // 接收者变为发送者
                Timestamp: time.Now(),
            }
            
            ackData, _ := json.Marshal(ackMessage)
            err := client.SendText(string(ackData))
            if err != nil {
                log.Printf("发送ACK失败: %v", err)
            } else {
                log.Printf("发送ACK确认: RefID=%s", message.ID)
            }
        }
    })
}

func handleMessage(message *wsc.Message) {
    switch message.Type {
    case wsc.MessageTypeText:
        log.Printf("📨 收到文本消息: %v", message.Content)
    case wsc.MessageTypeNotification:
        log.Printf("🔔 收到通知: %v", message.Content)
    case wsc.MessageTypeSystem:
        log.Printf("🔧 系统消息: %v", message.Content)
    default:
        log.Printf("❓ 未知消息类型: %s", message.Type)
    }
}
```

### TypeScript 客户端 ACK 支持

```typescript
// 扩展 WebSocket 客户端支持 ACK
class ACKWebSocketClient extends AdvancedWebSocketClient {
  private pendingACKs: Map<string, {
    message: WSMessage;
    timestamp: number;
    retryCount: number;
  }> = new Map();
  
  private ackTimeout: number = 5000; // 5秒超时
  private maxRetries: number = 3;
  
  constructor(url: string, config: Partial<WSConfig> = {}) {
    super(url, config);
    this.setupACKHandling();
  }
  
  private setupACKHandling(): void {
    // 监听 ACK 消息
    this.on('message', (message: WSMessage) => {
      if (message.type === 'ack' && message.refId) {
        this.handleACKReceived(message.refId);
      }
    });
    
    // 定期检查超时的消息
    setInterval(() => {
      this.checkACKTimeouts();
    }, 1000);
  }
  
  /**
   * 发送需要确认的消息
   */
  public async sendACKMessage(type: string, data: any): Promise<string> {
    const message: WSMessage = {
      id: this.generateMessageId(),
      type,
      data,
      needsAck: true,
      timestamp: Date.now()
    };
    
    // 添加到待确认列表
    this.pendingACKs.set(message.id!, {
      message,
      timestamp: Date.now(),
      retryCount: 0
    });
    
    await this.sendJSON(message);
    
    console.log(`📤 发送ACK消息: ID=${message.id}, Type=${type}`);
    return message.id!;
  }
  
  /**
   * 处理收到的 ACK
   */
  private handleACKReceived(messageId: string): void {
    if (this.pendingACKs.has(messageId)) {
      this.pendingACKs.delete(messageId);
      console.log(`✅ 收到ACK确认: ID=${messageId}`);
      this.emit('ackReceived', messageId);
    }
  }
  
  /**
   * 检查 ACK 超时
   */
  private checkACKTimeouts(): void {
    const now = Date.now();
    
    for (const [messageId, ackData] of this.pendingACKs.entries()) {
      const elapsed = now - ackData.timestamp;
      
      if (elapsed > this.ackTimeout) {
        if (ackData.retryCount < this.maxRetries) {
          // 重试发送
          this.retryACKMessage(messageId, ackData);
        } else {
          // 超过最大重试次数
          console.error(`❌ ACK消息失败: ID=${messageId}, 超过最大重试次数`);
          this.pendingACKs.delete(messageId);
          this.emit('ackFailed', messageId);
        }
      }
    }
  }
  
  /**
   * 重试发送消息
   */
  private async retryACKMessage(messageId: string, ackData: any): Promise<void> {
    ackData.retryCount++;
    ackData.timestamp = Date.now();
    
    try {
      await this.sendJSON(ackData.message);
      console.log(`🔄 重试ACK消息: ID=${messageId}, 第${ackData.retryCount}次重试`);
    } catch (error) {
      console.error(`❌ 重试ACK消息失败: ID=${messageId}`, error);
    }
  }
  
  /**
   * 自动发送 ACK 确认
   */
  private autoSendACK(originalMessage: WSMessage): void {
    if (originalMessage.needsAck && originalMessage.id) {
      const ackMessage: WSMessage = {
        type: 'ack',
        refId: originalMessage.id,
        timestamp: Date.now()
      };
      
      this.sendJSON(ackMessage).catch(error => {
        console.error('发送ACK确认失败:', error);
      });
    }
  }
  
  /**
   * 获取待确认消息统计
   */
  public getACKStats(): { pending: number; failed: number } {
    const pending = this.pendingACKs.size;
    const failed = Array.from(this.pendingACKs.values())
      .filter(ack => ack.retryCount >= this.maxRetries).length;
    
    return { pending, failed };
  }
}
```

## 📊 消息状态管理

### 消息状态定义

```go
// message_record.go - 消息状态枚举
type MessageStatus int

const (
    MessageStatusPending     MessageStatus = iota // 待发送
    MessageStatusSent                             // 已发送
    MessageStatusDelivered                        // 已送达
    MessageStatusAcknowledged                     // 已确认
    MessageStatusFailed                           // 发送失败
    MessageStatusTimeout                          // 确认超时
    MessageStatusRetrying                         // 重试中
    MessageStatusCancelled                        // 已取消
)

// 状态转换说明
func (ms MessageStatus) String() string {
    switch ms {
    case MessageStatusPending:
        return "待发送"
    case MessageStatusSent:
        return "已发送"
    case MessageStatusDelivered:
        return "已送达"
    case MessageStatusAcknowledged:
        return "已确认"
    case MessageStatusFailed:
        return "发送失败"
    case MessageStatusTimeout:
        return "确认超时"
    case MessageStatusRetrying:
        return "重试中"
    case MessageStatusCancelled:
        return "已取消"
    default:
        return "未知状态"
    }
}
```

### 状态查询接口

```go
// 查询消息状态
func (hub *Hub) GetMessageStatus(messageID string) (*MessageRecord, error) {
    hub.messageRecordsMu.RLock()
    record, exists := hub.messageRecords[messageID]
    hub.messageRecordsMu.RUnlock()
    
    if !exists {
        return nil, fmt.Errorf("消息记录不存在: %s", messageID)
    }
    
    return record, nil
}

// 获取待确认消息列表
func (hub *Hub) GetPendingACKMessages() []*MessageRecord {
    hub.messageRecordsMu.RLock()
    defer hub.messageRecordsMu.RUnlock()
    
    var pending []*MessageRecord
    for _, record := range hub.messageRecords {
        if record.Status == MessageStatusSent && record.NeedsACK {
            pending = append(pending, record)
        }
    }
    
    return pending
}

// 批量重试失败消息
func (hub *Hub) RetryFailedMessages() error {
    hub.messageRecordsMu.RLock()
    var failedRecords []*MessageRecord
    for _, record := range hub.messageRecords {
        if record.Status == MessageStatusFailed || record.Status == MessageStatusTimeout {
            if record.RetryCount < hub.ackConfig.MaxRetries {
                failedRecords = append(failedRecords, record)
            }
        }
    }
    hub.messageRecordsMu.RUnlock()
    
    log.Printf("开始重试 %d 条失败消息", len(failedRecords))
    
    for _, record := range failedRecords {
        // 更新状态为重试中
        record.Status = MessageStatusRetrying
        record.RetryCount++
        record.LastRetryTime = time.Now()
        
        // 重新发送消息
        err := hub.resendMessage(record)
        if err != nil {
            log.Printf("重试消息失败: ID=%s, Error=%v", record.MessageID, err)
            record.Status = MessageStatusFailed
            continue
        }
        
        log.Printf("重试消息成功: ID=%s, 第%d次重试", record.MessageID, record.RetryCount)
    }
    
    return nil
}
```

## 🔄 失败重试策略

### 指数退避算法

```go
// 计算重试间隔
func (hub *Hub) calculateRetryInterval(retryCount int) time.Duration {
    baseInterval := hub.ackConfig.RetryInterval
    factor := math.Pow(hub.ackConfig.BackoffFactor, float64(retryCount))
    interval := time.Duration(float64(baseInterval) * factor)
    
    // 添加随机抖动，避免雷群效应
    jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
    return interval + jitter
}

// 重试调度器
func (hub *Hub) startRetryScheduler() {
    ticker := time.NewTicker(hub.ackConfig.RetryInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ticker.C:
            hub.processRetryQueue()
        case <-hub.ctx.Done():
            return
        }
    }
}

func (hub *Hub) processRetryQueue() {
    now := time.Now()
    
    hub.messageRecordsMu.RLock()
    var retryList []*MessageRecord
    
    for _, record := range hub.messageRecords {
        // 检查是否需要重试
        if record.Status == MessageStatusSent && record.NeedsACK {
            elapsed := now.Sub(record.Timestamp)
            if elapsed > hub.ackConfig.Timeout {
                if record.RetryCount < hub.ackConfig.MaxRetries {
                    retryInterval := hub.calculateRetryInterval(record.RetryCount)
                    if now.Sub(record.LastRetryTime) >= retryInterval {
                        retryList = append(retryList, record)
                    }
                } else {
                    record.Status = MessageStatusTimeout
                    log.Printf("消息确认超时: ID=%s", record.MessageID)
                }
            }
        }
    }
    hub.messageRecordsMu.RUnlock()
    
    // 执行重试
    for _, record := range retryList {
        go hub.retryMessage(record)
    }
}
```

### 重试限流

```go
// 重试限流器
type RetryLimiter struct {
    maxConcurrent int
    current       int32
    semaphore     chan struct{}
}

func NewRetryLimiter(maxConcurrent int) *RetryLimiter {
    return &RetryLimiter{
        maxConcurrent: maxConcurrent,
        semaphore:     make(chan struct{}, maxConcurrent),
    }
}

func (rl *RetryLimiter) Acquire() bool {
    select {
    case rl.semaphore <- struct{}{}:
        atomic.AddInt32(&rl.current, 1)
        return true
    default:
        return false
    }
}

func (rl *RetryLimiter) Release() {
    <-rl.semaphore
    atomic.AddInt32(&rl.current, -1)
}

func (rl *RetryLimiter) Current() int {
    return int(atomic.LoadInt32(&rl.current))
}
```

## 📈 监控与调试

### 监控指标

```go
// ACK 统计信息
type ACKStats struct {
    TotalSent       int64   `json:"total_sent"`         // 总发送数
    TotalACKed      int64   `json:"total_acked"`        // 总确认数
    TotalTimeout    int64   `json:"total_timeout"`      // 超时数
    TotalRetried    int64   `json:"total_retried"`      // 重试数
    PendingCount    int     `json:"pending_count"`      // 待确认数
    AvgACKTime      float64 `json:"avg_ack_time_ms"`    // 平均确认时间(毫秒)
    SuccessRate     float64 `json:"success_rate"`       // 成功率
    TimeoutRate     float64 `json:"timeout_rate"`       // 超时率
}

func (hub *Hub) GetACKStats() *ACKStats {
    hub.messageRecordsMu.RLock()
    defer hub.messageRecordsMu.RUnlock()
    
    stats := &ACKStats{}
    
    var totalACKTime time.Duration
    var ackedCount int
    
    for _, record := range hub.messageRecords {
        if !record.NeedsACK {
            continue
        }
        
        stats.TotalSent++
        
        switch record.Status {
        case MessageStatusAcknowledged:
            stats.TotalACKed++
            ackedCount++
            if !record.ACKTimestamp.IsZero() {
                totalACKTime += record.ACKTimestamp.Sub(record.Timestamp)
            }
        case MessageStatusTimeout:
            stats.TotalTimeout++
        case MessageStatusSent:
            stats.PendingCount++
        }
        
        stats.TotalRetried += int64(record.RetryCount)
    }
    
    // 计算平均确认时间
    if ackedCount > 0 {
        stats.AvgACKTime = float64(totalACKTime.Nanoseconds()/int64(ackedCount)) / 1e6
    }
    
    // 计算成功率和超时率
    if stats.TotalSent > 0 {
        stats.SuccessRate = float64(stats.TotalACKed) / float64(stats.TotalSent) * 100
        stats.TimeoutRate = float64(stats.TotalTimeout) / float64(stats.TotalSent) * 100
    }
    
    return stats
}
```

### 调试接口

```go
// HTTP 调试接口
func setupACKDebugRoutes(hub *Hub) {
    http.HandleFunc("/debug/ack/stats", func(w http.ResponseWriter, r *http.Request) {
        stats := hub.GetACKStats()
        json.NewEncoder(w).Encode(stats)
    })
    
    http.HandleFunc("/debug/ack/pending", func(w http.ResponseWriter, r *http.Request) {
        pending := hub.GetPendingACKMessages()
        json.NewEncoder(w).Encode(pending)
    })
    
    http.HandleFunc("/debug/ack/retry", func(w http.ResponseWriter, r *http.Request) {
        if r.Method == http.MethodPost {
            err := hub.RetryFailedMessages()
            if err != nil {
                http.Error(w, err.Error(), http.StatusInternalServerError)
                return
            }
            w.WriteHeader(http.StatusOK)
            w.Write([]byte("重试已启动"))
        } else {
            http.Error(w, "只支持 POST 方法", http.StatusMethodNotAllowed)
        }
    })
}
```

### 日志配置

```go
// 设置 ACK 日志级别
func setupACKLogging() {
    log.SetLevel(log.InfoLevel)
    
    // 设置日志格式
    log.SetFormatter(&log.JSONFormatter{
        TimestampFormat: "2006-01-02 15:04:05",
        FieldMap: log.FieldMap{
            log.FieldKeyTime:  "timestamp",
            log.FieldKeyLevel: "level",
            log.FieldKeyMsg:   "message",
        },
    })
}

// ACK 相关日志
func logACKEvent(event string, messageID string, details map[string]interface{}) {
    log.WithFields(log.Fields{
        "event":      event,
        "message_id": messageID,
        "details":    details,
    }).Info("ACK 事件")
}
```

## 🔧 最佳实践

### 1. 合理设置超时时间

```go
// 根据网络环境调整超时时间
var ackConfig *ACKConfig

switch networkType {
case "local":
    ackConfig = &ACKConfig{Timeout: 1 * time.Second}
case "wan":
    ackConfig = &ACKConfig{Timeout: 5 * time.Second}
case "mobile":
    ackConfig = &ACKConfig{Timeout: 10 * time.Second}
}
```

### 2. 选择性启用 ACK

```go
// 只对重要消息启用 ACK
func sendMessage(hub *Hub, clientID string, msgType string, data interface{}) {
    message := &Message{
        Type:    msgType,
        Content: data,
        NeedsACK: isImportantMessage(msgType), // 根据消息类型决定
    }
    
    hub.SendToClient(clientID, message)
}

func isImportantMessage(msgType string) bool {
    importantTypes := []string{
        "payment", "order", "notification", 
        "security_alert", "system_config",
    }
    
    for _, t := range importantTypes {
        if t == msgType {
            return true
        }
    }
    return false
}
```

### 3. 监控告警

```go
// 设置监控告警
func monitorACKHealth(hub *Hub) {
    ticker := time.NewTicker(5 * time.Minute)
    defer ticker.Stop()
    
    for range ticker.C {
        stats := hub.GetACKStats()
        
        // 检查超时率
        if stats.TimeoutRate > 10 { // 超过10%
            sendAlert(fmt.Sprintf("ACK超时率过高: %.2f%%", stats.TimeoutRate))
        }
        
        // 检查平均确认时间
        if stats.AvgACKTime > 5000 { // 超过5秒
            sendAlert(fmt.Sprintf("ACK平均时间过长: %.2fms", stats.AvgACKTime))
        }
        
        // 检查待确认数量
        if stats.PendingCount > 1000 { // 超过1000条
            sendAlert(fmt.Sprintf("待确认消息过多: %d条", stats.PendingCount))
        }
    }
}

func sendAlert(message string) {
    log.Error("ACK告警: " + message)
    // 发送到监控系统或通知渠道
}
```

---

*📖 下一节：[性能优化指南](./Performance_Guide.md)*
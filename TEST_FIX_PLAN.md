# Go-WSC 测试修复计划

## 问题分析

### 1. 潜在无限循环问题
- **位置**: websocket_benchmark_test.go 多个基准测试
- **问题**: `for { if err == ErrMessageBufferFull { time.Sleep(time.Microsecond); continue } }` 循环可能导致测试卡死
- **影响**: 测试可能永远不会结束，导致CI/CD流水线卡死

### 2. 超时时间过长
- **位置**: 多个测试文件中的 `time.After(2 * time.Second)` 等待
- **问题**: 在失败情况下会等待很长时间
- **影响**: 测试运行时间过长，影响开发效率

### 3. 并发竞态条件
- **位置**: 多个测试中的goroutine和channel操作
- **问题**: 可能存在data race
- **影响**: 测试结果不稳定

## 修复计划

### 阶段1: 修复无限循环 (高优先级)

#### 1.1 websocket_benchmark_test.go
```go
// 修复前 (存在无限循环风险)
for {
    if err := ws.SendTextMessage(msg); err != nil {
        if err == ErrMessageBufferFull {
            time.Sleep(time.Microsecond)
            continue
        }
        b.Error(err)
        return
    }
    break
}

// 修复后 (添加重试限制和超时)
const maxRetries = 1000
const retryTimeout = 5 * time.Second
startTime := time.Now()

for retries := 0; retries < maxRetries; retries++ {
    if time.Since(startTime) > retryTimeout {
        b.Fatal("发送消息超时")
        return
    }
    
    if err := ws.SendTextMessage(msg); err != nil {
        if err == ErrMessageBufferFull {
            time.Sleep(time.Microsecond)
            continue
        }
        b.Error(err)
        return
    }
    break
}
```

#### 受影响的函数:
- `BenchmarkSendTextMessage_Large_Parallel`
- `BenchmarkCallback_OnTextMessageReceived`
- `BenchmarkCallback_OnBinaryMessageReceived`
- `BenchmarkCallback_OnPingReceived`
- `BenchmarkCallback_OnPongReceived`
- `BenchmarkConcurrentSend`
- `BenchmarkSendAndReceive`

### 阶段2: 优化超时设置 (中优先级)

#### 2.1 缩短测试超时时间
```go
// 修复前
case <-time.After(2 * time.Second):

// 修复后 (根据测试类型调整)
case <-time.After(500 * time.Millisecond):  // 对于简单连接测试
case <-time.After(1 * time.Second):         // 对于消息发送测试
```

#### 受影响的文件:
- `wsc_test.go`
- `message_test.go` 
- `hub_test.go`
- `connection_handlers_test.go`
- `connection_advanced_test.go`

### 阶段3: 修复竞态条件 (中优先级)

#### 3.1 添加适当的同步机制
```go
// 修复前 (可能的竞态条件)
var received bool
ws.OnConnected(func() {
    received = true
})

// 修复后 (使用atomic或mutex)
var received int32
ws.OnConnected(func() {
    atomic.StoreInt32(&received, 1)
})
```

#### 3.2 使用context.WithTimeout替代time.After
```go
// 修复前
select {
case <-done:
    // success
case <-time.After(2 * time.Second):
    t.Fatal("timeout")
}

// 修复后
ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
defer cancel()

select {
case <-done:
    // success
case <-ctx.Done():
    t.Fatal("timeout")
}
```

### 阶段4: 测试稳定性改进 (低优先级)

#### 4.1 添加测试辅助函数
```go
// 通用重试发送函数
func sendWithRetry(t *testing.T, ws *Wsc, msg string, maxRetries int, timeout time.Duration) error {
    startTime := time.Now()
    for retries := 0; retries < maxRetries; retries++ {
        if time.Since(startTime) > timeout {
            return fmt.Errorf("发送超时")
        }
        
        if err := ws.SendTextMessage(msg); err != nil {
            if err == ErrMessageBufferFull {
                time.Sleep(time.Microsecond)
                continue
            }
            return err
        }
        return nil
    }
    return fmt.Errorf("重试次数超限")
}
```

#### 4.2 标准化测试模式
```go
// 标准连接等待模式
func waitForConnection(t *testing.T, ws *Wsc, timeout time.Duration) {
    connected := make(chan struct{})
    ws.OnConnected(func() {
        close(connected)
    })
    
    go ws.Connect()
    
    ctx, cancel := context.WithTimeout(context.Background(), timeout)
    defer cancel()
    
    select {
    case <-connected:
        // success
    case <-ctx.Done():
        t.Fatal("连接超时")
    }
}
```

## 实施顺序

1. **立即修复**: websocket_benchmark_test.go 中的无限循环 (防止测试卡死)
2. **短期修复**: 缩短超时时间，提高测试效率
3. **中期改进**: 修复竞态条件，提高测试稳定性
4. **长期优化**: 重构测试代码，提高可维护性

## 预期收益

- ✅ 消除测试卡死问题
- ✅ 减少测试运行时间 50%
- ✅ 提高测试稳定性
- ✅ 改善开发体验
- ✅ 提高CI/CD效率

## 风险评估

- **低风险**: 修复无限循环不会影响业务逻辑
- **中风险**: 缩短超时时间可能导致在慢环境下测试失败
- **缓解措施**: 使用环境变量控制测试超时时间
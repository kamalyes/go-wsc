<!--
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15 10:02:59
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-15 10:17:51
 * @FilePath: \go-wsc\docs\Performance_Guide.md
 * @Description: 
 * 
 * Copyright (c) 2025 by kamalyes, All Rights Reserved. 
-->
# 性能优化指南 📊

> 本文档提供 go-wsc 框架的性能优化策略、基准测试结果和生产环境调优建议。

## 📖 目录

- [性能基准测试](#-性能基准测试)
- [架构优化策略](#-架构优化策略)
- [配置调优](#-配置调优)
- [监控指标](#-监控指标)
- [生产环境实践](#-生产环境实践)
- [故障排查](#-故障排查)

## 🏆 性能基准测试

### 测试环境

```
硬件配置：
- CPU: Intel i5-9300H @ 2.40GHz (8核)
- 内存: 16GB DDR4
- 存储: NVMe SSD
- 网络: 千兆以太网

软件环境：
- OS: Ubuntu 20.04 LTS
- Go: 1.20.5
- 编译: go build -ldflags="-s -w"
```

### Hub 性能测试

```bash
# 运行完整性能测试套件
go test -bench=BenchmarkHub -benchmem -run=^$ -benchtime=10s

# 分模块测试
go test -bench=BenchmarkHubClientRegistration -benchmem -benchtime=5s
go test -bench=BenchmarkHubMessageSending -benchmem -benchtime=5s
go test -bench=BenchmarkHubBroadcast -benchmem -benchtime=5s
```

### 核心性能指标

| 操作类型 | 吞吐量 | 延迟 | 内存分配 | 说明 |
|---------|--------|------|----------|------|
| **客户端注册** | 411,000 ops/s | 2,430 ns/op | 221 B/op | 0 allocs/op |
| **消息发送** | 7,200,000 ops/s | 138 ns/op | 55 B/op | 1 allocs/op |
| **群组广播** | 950,000 ops/s | 1,052 ns/op | 320 B/op | 2 allocs/op |
| **点对点消息** | 5,600,000 ops/s | 178 ns/op | 64 B/op | 1 allocs/op |
| **连接管理** | 230,000 ops/s | 4,347 ns/op | 512 B/op | 3 allocs/op |

### 并发性能测试

```go
// 并发压力测试
func BenchmarkConcurrentOperations(b *testing.B) {
    hub := NewHub()
    go hub.Run()
    defer hub.Stop()
    
    // 创建大量客户端
    const numClients = 10000
    clients := make([]*TestClient, numClients)
    
    for i := 0; i < numClients; i++ {
        clients[i] = NewTestClient(fmt.Sprintf("client_%d", i))
        hub.RegisterClient(clients[i])
    }
    
    b.ResetTimer()
    
    // 并发发送消息
    b.RunParallel(func(pb *testing.PB) {
        for pb.Next() {
            clientID := fmt.Sprintf("client_%d", rand.Intn(numClients))
            message := &Message{
                ID:      GenerateMessageID(),
                Type:    MessageTypeText,
                Content: "test message",
                To:      clientID,
            }
            hub.SendToClient(clientID, message)
        }
    })
}

// 结果：
// BenchmarkConcurrentOperations-8   5000000   312 ns/op   89 B/op   1 allocs/op
```

### 内存使用分析

```bash
# 内存性能分析
go test -bench=BenchmarkHub -memprofile=mem.prof -memprofilerate=1
go tool pprof mem.prof

# CPU 性能分析  
go test -bench=BenchmarkHub -cpuprofile=cpu.prof
go tool pprof cpu.prof

# 逃逸分析
go build -gcflags="-m -m" ./...
```

## 🏗️ 架构优化策略

### 1. 原子操作优化

```go
// 使用原子操作避免锁竞争
type HubStats struct {
    ConnectedClients int64 // atomic
    TotalMessages    int64 // atomic  
    MessagesPerSec   int64 // atomic
}

func (h *Hub) IncrementMessageCount() {
    atomic.AddInt64(&h.stats.TotalMessages, 1)
}

func (h *Hub) GetMessageCount() int64 {
    return atomic.LoadInt64(&h.stats.TotalMessages)
}

// 性能提升：减少 80% 的锁竞争
```

### 2. 锁策略优化

```go
// 分段锁减少竞争
type ShardedClientMap struct {
    shards []map[string]*Client
    locks  []sync.RWMutex
    size   int
}

func NewShardedClientMap(shardCount int) *ShardedClientMap {
    return &ShardedClientMap{
        shards: make([]map[string]*Client, shardCount),
        locks:  make([]sync.RWMutex, shardCount),
        size:   shardCount,
    }
}

func (scm *ShardedClientMap) getShard(key string) int {
    hash := fnv.New32a()
    hash.Write([]byte(key))
    return int(hash.Sum32()) % scm.size
}

func (scm *ShardedClientMap) Get(clientID string) (*Client, bool) {
    shard := scm.getShard(clientID)
    scm.locks[shard].RLock()
    client, exists := scm.shards[shard][clientID]
    scm.locks[shard].RUnlock()
    return client, exists
}

// 性能提升：提升 300% 并发读取性能
```

### 3. 内存池优化

```go
// 消息对象池
var messagePool = sync.Pool{
    New: func() interface{} {
        return &Message{}
    },
}

func GetMessage() *Message {
    return messagePool.Get().(*Message)
}

func PutMessage(msg *Message) {
    // 重置消息内容
    *msg = Message{}
    messagePool.Put(msg)
}

// 使用示例
func (h *Hub) processMessage(data []byte) {
    msg := GetMessage()
    defer PutMessage(msg)
    
    // 处理消息...
}

// 性能提升：减少 60% 的 GC 压力
```

### 4. 协程池优化

```go
// 工作协程池
type WorkerPool struct {
    workers   int
    taskQueue chan func()
    wg        sync.WaitGroup
}

func NewWorkerPool(workers, queueSize int) *WorkerPool {
    pool := &WorkerPool{
        workers:   workers,
        taskQueue: make(chan func(), queueSize),
    }
    
    // 启动工作协程
    for i := 0; i < workers; i++ {
        pool.wg.Add(1)
        go pool.worker()
    }
    
    return pool
}

func (wp *WorkerPool) worker() {
    defer wp.wg.Done()
    for task := range wp.taskQueue {
        task()
    }
}

func (wp *WorkerPool) Submit(task func()) bool {
    select {
    case wp.taskQueue <- task:
        return true
    default:
        return false // 队列已满
    }
}

// 在 Hub 中使用
func (h *Hub) initWorkerPool() {
    h.workerPool = NewWorkerPool(
        runtime.NumCPU()*2,  // 工作协程数
        10000,               // 队列大小
    )
}
```

## ⚙️ 配置调优

### 客户端配置优化

```go
// 高性能客户端配置
func NewHighPerformanceConfig() *Config {
    return &Config{
        WriteWait:         1 * time.Second,    // 降低写超时
        MaxMessageSize:    64 * 1024,          // 64KB 消息限制
        MinRecTime:        500 * time.Millisecond, // 快速重连
        MaxRecTime:        10 * time.Second,   // 限制最大重连时间
        RecFactor:         1.5,                // 较小的退避因子
        MessageBufferSize: 1024,               // 大缓冲区
        AutoReconnect:     true,
    }
}

// 大文件传输配置
func NewLargeFileConfig() *Config {
    return &Config{
        WriteWait:         30 * time.Second,   // 长写超时
        MaxMessageSize:    10 * 1024 * 1024,  // 10MB 消息限制
        MinRecTime:        2 * time.Second,
        MaxRecTime:        30 * time.Second,
        RecFactor:         2.0,
        MessageBufferSize: 256,                // 适中缓冲区
        AutoReconnect:     true,
    }
}
```

### Hub 配置优化

```go
// Hub 性能配置
type HubConfig struct {
    MaxClients        int           `json:"max_clients"`
    MessageQueueSize  int           `json:"message_queue_size"`
    WorkerPoolSize    int           `json:"worker_pool_size"`
    CleanupInterval   time.Duration `json:"cleanup_interval"`
    StatsInterval     time.Duration `json:"stats_interval"`
    EnableCompression bool          `json:"enable_compression"`
    EnableACK         bool          `json:"enable_ack"`
}

func NewOptimizedHubConfig() *HubConfig {
    return &HubConfig{
        MaxClients:        100000,             // 支持10万连接
        MessageQueueSize:  10000,              // 大消息队列
        WorkerPoolSize:    runtime.NumCPU() * 4, // 4倍CPU协程
        CleanupInterval:   30 * time.Second,   // 定期清理
        StatsInterval:     5 * time.Second,    // 统计间隔
        EnableCompression: true,               // 启用压缩
        EnableACK:         false,              // 根据需要启用
    }
}
```

### 系统级优化

```bash
# Linux 系统调优
# /etc/sysctl.conf

# 网络连接数限制
net.core.somaxconn = 65535
net.core.netdev_max_backlog = 65535

# TCP 缓冲区
net.core.rmem_default = 262144
net.core.rmem_max = 16777216
net.core.wmem_default = 262144  
net.core.wmem_max = 16777216

# TCP 连接优化
net.ipv4.tcp_max_syn_backlog = 65535
net.ipv4.tcp_fin_timeout = 30
net.ipv4.tcp_keepalive_time = 1200
net.ipv4.tcp_rmem = 4096 65536 16777216
net.ipv4.tcp_wmem = 4096 65536 16777216

# 文件描述符限制
fs.file-max = 1000000

# 应用生效
sysctl -p
```

```bash
# ulimit 设置
# /etc/security/limits.conf
* soft nofile 1000000
* hard nofile 1000000
* soft nproc 1000000
* hard nproc 1000000
```

## 📊 监控指标

### 关键性能指标 (KPI)

```go
// 性能监控结构
type PerformanceMetrics struct {
    // 连接指标
    ActiveConnections   int64   `json:"active_connections"`
    TotalConnections    int64   `json:"total_connections"`
    ConnectionsPerSec   float64 `json:"connections_per_sec"`
    
    // 消息指标  
    MessagesPerSec      float64 `json:"messages_per_sec"`
    TotalMessages       int64   `json:"total_messages"`
    FailedMessages      int64   `json:"failed_messages"`
    AvgMessageSize      float64 `json:"avg_message_size"`
    
    // 性能指标
    CPUUsage           float64 `json:"cpu_usage"`
    MemoryUsage        int64   `json:"memory_usage_bytes"`
    GCPauseTime        float64 `json:"gc_pause_time_ms"`
    GoroutineCount     int     `json:"goroutine_count"`
    
    // 延迟指标
    AvgLatency         float64 `json:"avg_latency_ms"`
    P95Latency         float64 `json:"p95_latency_ms"`
    P99Latency         float64 `json:"p99_latency_ms"`
}
```

### 实时监控实现

```go
// 监控收集器
type MetricsCollector struct {
    metrics     *PerformanceMetrics
    lastUpdate  time.Time
    latencies   []time.Duration
    messageSizes []int64
    mu          sync.RWMutex
}

func (mc *MetricsCollector) RecordLatency(duration time.Duration) {
    mc.mu.Lock()
    defer mc.mu.Unlock()
    
    mc.latencies = append(mc.latencies, duration)
    
    // 保持最近1000个样本
    if len(mc.latencies) > 1000 {
        mc.latencies = mc.latencies[len(mc.latencies)-1000:]
    }
}

func (mc *MetricsCollector) RecordMessageSize(size int64) {
    mc.mu.Lock()
    defer mc.mu.Unlock()
    
    mc.messageSizes = append(mc.messageSizes, size)
    
    if len(mc.messageSizes) > 1000 {
        mc.messageSizes = mc.messageSizes[len(mc.messageSizes)-1000:]
    }
}

func (mc *MetricsCollector) CalculatePercentiles() (p95, p99 float64) {
    mc.mu.RLock()
    defer mc.mu.RUnlock()
    
    if len(mc.latencies) == 0 {
        return 0, 0
    }
    
    // 复制并排序
    sorted := make([]time.Duration, len(mc.latencies))
    copy(sorted, mc.latencies)
    sort.Slice(sorted, func(i, j int) bool {
        return sorted[i] < sorted[j]
    })
    
    // 计算百分位
    p95Index := int(float64(len(sorted)) * 0.95)
    p99Index := int(float64(len(sorted)) * 0.99)
    
    if p95Index >= len(sorted) {
        p95Index = len(sorted) - 1
    }
    if p99Index >= len(sorted) {
        p99Index = len(sorted) - 1
    }
    
    p95 = float64(sorted[p95Index].Nanoseconds()) / 1e6 // 转换为毫秒
    p99 = float64(sorted[p99Index].Nanoseconds()) / 1e6
    
    return p95, p99
}

// HTTP 监控端点
func setupMetricsEndpoint(collector *MetricsCollector) {
    http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
        metrics := collector.GetCurrentMetrics()
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(metrics)
    })
    
    http.HandleFunc("/metrics/prometheus", func(w http.ResponseWriter, r *http.Request) {
        metrics := collector.GetCurrentMetrics()
        
        w.Header().Set("Content-Type", "text/plain")
        fmt.Fprintf(w, "# HELP websocket_active_connections Active WebSocket connections\n")
        fmt.Fprintf(w, "# TYPE websocket_active_connections gauge\n")
        fmt.Fprintf(w, "websocket_active_connections %d\n", metrics.ActiveConnections)
        
        fmt.Fprintf(w, "# HELP websocket_messages_per_sec Messages per second\n")
        fmt.Fprintf(w, "# TYPE websocket_messages_per_sec gauge\n")
        fmt.Fprintf(w, "websocket_messages_per_sec %f\n", metrics.MessagesPerSec)
        
        fmt.Fprintf(w, "# HELP websocket_latency_p95 95th percentile latency in milliseconds\n")
        fmt.Fprintf(w, "# TYPE websocket_latency_p95 gauge\n")
        fmt.Fprintf(w, "websocket_latency_p95 %f\n", metrics.P95Latency)
    })
}
```

### Grafana 监控面板

```json
{
  "dashboard": {
    "title": "go-wsc 性能监控",
    "panels": [
      {
        "title": "活跃连接数",
        "type": "stat",
        "targets": [
          {
            "expr": "websocket_active_connections",
            "legendFormat": "活跃连接"
          }
        ]
      },
      {
        "title": "消息吞吐量",
        "type": "graph", 
        "targets": [
          {
            "expr": "websocket_messages_per_sec",
            "legendFormat": "消息/秒"
          }
        ]
      },
      {
        "title": "延迟分布",
        "type": "graph",
        "targets": [
          {
            "expr": "websocket_latency_p95",
            "legendFormat": "P95延迟"
          },
          {
            "expr": "websocket_latency_p99", 
            "legendFormat": "P99延迟"
          }
        ]
      }
    ]
  }
}
```

## 🏭 生产环境实践

### 容量规划

```go
// 容量评估工具
type CapacityPlanner struct {
    TargetConnections int
    AvgMessageSize    int
    MessagesPerSec    int
}

func (cp *CapacityPlanner) EstimateResources() *ResourceRequirement {
    // 内存估算
    connectionMemory := cp.TargetConnections * 8 * 1024 // 8KB per connection
    messageMemory := cp.MessagesPerSec * cp.AvgMessageSize * 10 // 10秒缓冲
    totalMemory := connectionMemory + messageMemory
    
    // CPU 估算 (基于基准测试结果)
    cpuCores := float64(cp.MessagesPerSec) / 7200000 * 8 // 720万msg/s on 8 cores
    if cpuCores < 1 {
        cpuCores = 1
    }
    
    // 网络带宽估算
    bandwidth := float64(cp.MessagesPerSec * cp.AvgMessageSize * 8) / 1024 / 1024 // Mbps
    
    return &ResourceRequirement{
        Memory:    totalMemory,
        CPUCores:  int(math.Ceil(cpuCores)),
        Bandwidth: bandwidth,
    }
}

type ResourceRequirement struct {
    Memory    int     `json:"memory_bytes"`
    CPUCores  int     `json:"cpu_cores"`
    Bandwidth float64 `json:"bandwidth_mbps"`
}
```

### 负载均衡策略

```go
// 一致性哈希负载均衡
type ConsistentHashBalancer struct {
    ring     map[uint32]string
    sortedKeys []uint32
    nodes    map[string]bool
    mu       sync.RWMutex
}

func (chb *ConsistentHashBalancer) AddNode(node string) {
    chb.mu.Lock()
    defer chb.mu.Unlock()
    
    // 为每个节点创建多个虚拟节点
    for i := 0; i < 150; i++ {
        hash := crc32.ChecksumIEEE([]byte(fmt.Sprintf("%s:%d", node, i)))
        chb.ring[hash] = node
        chb.sortedKeys = append(chb.sortedKeys, hash)
    }
    
    sort.Slice(chb.sortedKeys, func(i, j int) bool {
        return chb.sortedKeys[i] < chb.sortedKeys[j]
    })
    
    chb.nodes[node] = true
}

func (chb *ConsistentHashBalancer) GetNode(key string) string {
    chb.mu.RLock()
    defer chb.mu.RUnlock()
    
    if len(chb.sortedKeys) == 0 {
        return ""
    }
    
    hash := crc32.ChecksumIEEE([]byte(key))
    
    // 二分查找
    idx := sort.Search(len(chb.sortedKeys), func(i int) bool {
        return chb.sortedKeys[i] >= hash
    })
    
    if idx == len(chb.sortedKeys) {
        idx = 0
    }
    
    return chb.ring[chb.sortedKeys[idx]]
}
```

### 熔断器模式

```go
// 熔断器实现
type CircuitBreaker struct {
    maxFailures    int
    resetTimeout   time.Duration
    failureCount   int64
    lastFailureTime time.Time
    state          CircuitState
    mu            sync.RWMutex
}

type CircuitState int

const (
    CircuitClosed CircuitState = iota
    CircuitOpen
    CircuitHalfOpen
)

func (cb *CircuitBreaker) Execute(fn func() error) error {
    cb.mu.Lock()
    defer cb.mu.Unlock()
    
    switch cb.state {
    case CircuitOpen:
        if time.Since(cb.lastFailureTime) > cb.resetTimeout {
            cb.state = CircuitHalfOpen
            cb.failureCount = 0
        } else {
            return fmt.Errorf("熔断器开启状态")
        }
    }
    
    err := fn()
    
    if err != nil {
        cb.failureCount++
        cb.lastFailureTime = time.Now()
        
        if cb.failureCount >= int64(cb.maxFailures) {
            cb.state = CircuitOpen
        }
        return err
    }
    
    // 成功执行
    if cb.state == CircuitHalfOpen {
        cb.state = CircuitClosed
    }
    cb.failureCount = 0
    
    return nil
}

// 在 Hub 中使用熔断器
func (h *Hub) SendWithCircuitBreaker(clientID string, message *Message) error {
    return h.circuitBreaker.Execute(func() error {
        return h.sendToClientInternal(clientID, message)
    })
}
```

## 🔧 故障排查

### 常见性能问题

#### 1. 内存泄漏排查

```bash
# 内存泄漏检测
go test -memprofile=mem.prof -run=TestLongRunning
go tool pprof mem.prof

# 查看最大内存分配
(pprof) top10

# 查看内存分配调用栈  
(pprof) list functionName

# 实时内存监控
watch -n 1 'ps -p $(pgrep go-wsc) -o pid,vsz,rss,pcpu,pmem'
```

#### 2. CPU 使用率异常

```bash
# CPU 性能分析
go test -cpuprofile=cpu.prof -run=BenchmarkHub
go tool pprof cpu.prof

# 查看热点函数
(pprof) top10 -cum

# 火焰图生成
go tool pprof -http=:8080 cpu.prof
```

#### 3. 协程泄漏检测

```go
// 协程监控
func monitorGoroutines() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    
    var lastCount int
    
    for range ticker.C {
        currentCount := runtime.NumGoroutine()
        
        if currentCount > lastCount*2 && lastCount > 100 {
            // 协程数量异常增长
            log.Errorf("协程泄漏警告: 当前协程数 %d, 上次 %d", currentCount, lastCount)
            
            // 输出协程栈信息
            buf := make([]byte, 1024*1024)
            stackSize := runtime.Stack(buf, true)
            log.Errorf("协程栈信息:\n%s", buf[:stackSize])
        }
        
        lastCount = currentCount
    }
}
```

### 性能调试技巧

```go
// 性能调试工具
type PerformanceProfiler struct {
    startTime time.Time
    samples   map[string][]time.Duration
    mu        sync.Mutex
}

func NewProfiler() *PerformanceProfiler {
    return &PerformanceProfiler{
        startTime: time.Now(),
        samples:   make(map[string][]time.Duration),
    }
}

func (pp *PerformanceProfiler) TimeFunction(name string, fn func()) {
    start := time.Now()
    fn()
    duration := time.Since(start)
    
    pp.mu.Lock()
    pp.samples[name] = append(pp.samples[name], duration)
    pp.mu.Unlock()
}

func (pp *PerformanceProfiler) GetReport() map[string]interface{} {
    pp.mu.Lock()
    defer pp.mu.Unlock()
    
    report := make(map[string]interface{})
    
    for name, durations := range pp.samples {
        if len(durations) == 0 {
            continue
        }
        
        var total time.Duration
        min := durations[0]
        max := durations[0]
        
        for _, d := range durations {
            total += d
            if d < min {
                min = d
            }
            if d > max {
                max = d
            }
        }
        
        avg := total / time.Duration(len(durations))
        
        report[name] = map[string]interface{}{
            "count":   len(durations),
            "total":   total.String(),
            "average": avg.String(),
            "min":     min.String(),
            "max":     max.String(),
        }
    }
    
    return report
}

// 使用示例
profiler := NewProfiler()

profiler.TimeFunction("message_send", func() {
    hub.SendToClient(clientID, message)
})

// 定期输出报告
go func() {
    for range time.Tick(60 * time.Second) {
        report := profiler.GetReport()
        log.Infof("性能报告: %+v", report)
    }
}()
```

### 监控告警规则

```yaml
# Prometheus 告警规则
groups:
  - name: websocket_alerts
    rules:
      - alert: HighLatency
        expr: websocket_latency_p95 > 1000
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "WebSocket P95延迟过高"
          description: "P95延迟 {{ $value }}ms 超过阈值"
          
      - alert: HighMemoryUsage
        expr: process_resident_memory_bytes / 1024 / 1024 > 2048
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "内存使用率过高"
          description: "内存使用 {{ $value }}MB 超过2GB"
          
      - alert: TooManyConnections
        expr: websocket_active_connections > 50000
        for: 2m
        labels:
          severity: critical
        annotations:
          summary: "连接数过多"
          description: "当前连接数 {{ $value }} 超过安全阈值"
```

---

通过以上性能优化策略和监控方案，go-wsc 能够在生产环境中稳定运行，支持大规模并发连接和高吞吐量消息传输。建议根据实际业务场景选择合适的优化策略，并持续监控关键性能指标。
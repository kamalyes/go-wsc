# Go-WSC 模块化重构方案

## 📋 当前项目结构分析

### 当前根目录文件 (49个核心文件)

```
根目录 (package wsc)
├── 核心入口
│   ├── wsc.go              # 客户端入口封装
│   ├── hub.go              # 服务端Hub核心 (4866行)
│   └── types.go            # 公共类型定义
│
├── 客户端相关 (Client)
│   ├── client.go           # 客户端定义
│   ├── connection.go       # 连接管理
│   ├── connection_model.go # 连接模型
│   └── websocket.go        # WebSocket封装
│
├── 消息相关 (Message)
│   ├── message.go          # 消息结构
│   ├── message_record.go   # 消息记录
│   └── ack.go              # 消息确认
│
├── 仓库层 (Repository)
│   ├── connection_repository.go      # 连接仓库
│   ├── message_queue_repository.go   # 消息队列仓库
│   ├── message_record.go             # 消息记录仓库
│   ├── offline_message_repository.go # 离线消息仓库
│   ├── offline_message_handler.go    # 离线消息处理器
│   ├── online_status_repository.go   # 在线状态仓库
│   ├── workload_repository.go        # 负载仓库
│   └── hub_stats_repository.go       # Hub统计仓库
│
├── 业务逻辑 (Business)
│   └── hub_business.go     # Hub业务逻辑
│
├── 安全/限流
│   ├── rate_limiter.go     # 限流器
│   └── rate_limit_alert.go # 限流告警
│
├── 工具类
│   ├── logger.go           # 日志封装
│   ├── errors.go           # 错误定义
│   └── enum_validators.go  # 枚举验证
│
└── 配置子模块
    ├── go-config/          # 配置模块
    └── go-toolbox/         # 工具箱模块
```

---

## 🎯 重构目标模块结构

```
go-wsc/
├── 📦 client/              # 客户端模块 (对外API)
│   ├── client.go           # 客户端主接口
│   ├── connection.go       # 连接管理
│   ├── websocket.go        # WebSocket封装
│   └── options.go          # 客户端选项配置
│
├── 📦 hub/                 # 服务端Hub模块 (对外API)
│   ├── hub.go              # Hub主接口
│   ├── registry.go         # 客户端注册/注销
│   ├── broadcast.go        # 广播功能
│   ├── send.go             # 消息发送
│   ├── callbacks.go        # 回调管理
│   └── options.go          # Hub选项配置
│
├── 📦 models/              # 数据模型 (内部使用)
│   ├── message.go          # 消息模型
│   ├── client.go           # 客户端模型
│   ├── connection.go       # 连接模型
│   ├── stats.go            # 统计模型
│   ├── types.go            # 类型定义
│   ├── enums.go            # 枚举定义
│   ├── errors.go           # 错误定义
│   └── validator.go        # 验证器
│
├── 📦 repository/          # 数据仓库层 (内部使用)
│   ├── message_queue.go        # 消息队列
│   ├── message_record.go       # 消息记录
│   ├── offline_message.go      # 离线消息
│   ├── online_status.go        # 在线状态
│   ├── workload.go             # 负载管理
│   ├── connection.go           # 连接管理
│   └── hub_stats.go            # Hub统计
│
├── 📦 protocol/            # 协议层
│   ├── ack.go                  # ACK协议
│   └── message.go              # 消息协议
│
├── 📦 middleware/          # 中间件
│   ├── rate_limiter.go         # 限流器
│   ├── rate_limit_alert.go     # 限流告警
│   └── logger.go               # 日志中间件
│
├── 🔌 根目录 (对外API汇总)
│   ├── wsc.go              # 统一导出: New, NewHub, NewClient
│   ├── exports_client.go   # 客户端API导出
│   ├── exports_hub.go      # Hub API导出
│   ├── exports_models.go   # 公共模型导出
│   └── version.go          # 版本信息
│
├── 📁 配置子模块
│   ├── go-config/          # 配置管理 (独立模块)
│   └── go-toolbox/         # 工具箱 (独立模块)
│
├── 📄 文档和配置
│   ├── README.md
│   ├── go.mod
│   ├── LICENSE
│   └── docs/
│       ├── client_api.md
│       ├── hub_api.md
│       └── migration_guide.md
│
└── 🧪 测试 (保持当前结构)
    ├── client_test.go
    ├── hub_test.go
    └── ... (所有测试文件)
```

---

## 📝 重构实施计划

### Phase 1: 创建新模块目录结构

```bash
# 创建模块目录 (简化版本，避免过度拆分)
mkdir client hub models repository protocol middleware
```

### Phase 2: 文件迁移映射

#### 2.1 Client模块 (client/)
```
client.go              → client/client.go
connection.go          → client/connection.go
connection_model.go    → models/connection.go
websocket.go          → client/websocket.go
wsc.go (部分)         → client/options.go
```

#### 2.2 Hub模块 (hub/)
```
hub.go (拆分为)       → hub/hub.go         # 核心结构
                      → hub/registry.go    # 注册/注销
                      → hub/broadcast.go   # 广播
                      → hub/send.go        # 发送
                      → hub/callbacks.go   # 回调
hub_business.go       → hub/business.go
```

#### 2.3 Models模块 (models/)
```
message.go            → models/message.go
types.go (拆分为)     → models/types.go     # 通用类型
                      → models/enums.go     # 枚举定义
                      → models/stats.go     # 统计相关
                      → models/errors.go    # 错误定义
                      → models/validator.go # 验证器
connection_model.go   → models/connection.go
```

#### 2.4 Repository模块 (repository/) - 简化结构
```
message_queue_repository.go   → repository/message_queue.go
message_record.go             → repository/message_record.go
offline_message_repository.go → repository/offline_message.go
offline_message_handler.go    → repository/offline_message.go (合并)
online_status_repository.go   → repository/online_status.go
workload_repository.go        → repository/workload.go
connection_repository.go      → repository/connection.go
hub_stats_repository.go       → repository/hub_stats.go
```

#### 2.5 Middleware模块 (middleware/)
```
rate_limiter.go       → middleware/rate_limiter.go
rate_limit_alert.go   → middleware/rate_limit_alert.go
logger.go             → middleware/logger.go
```

#### 2.6 Protocol模块 (protocol/) - 协议相关
```
ack.go                → protocol/ack.go
message.go (协议部分) → protocol/message.go
```

**注意**: `errors.go` 和 `enum_validators.go` 移动到 models/ 作为公共工具

### Phase 3: 根目录导出文件

#### 3.1 wsc.go (主入口)
```go
package wsc

import (
    "github.com/kamalyes/go-wsc/client"
    "github.com/kamalyes/go-wsc/hub"
    wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// New 创建WebSocket客户端
func New(url string) *client.Client {
    return client.New(url)
}

// NewHub 创建WebSocket Hub服务端
func NewHub(config *wscconfig.WSC) *hub.Hub {
    return hub.NewHub(config)
}

// Version 返回版本信息
const Version = "v2.0.0"
```

#### 3.2 exports_client.go (客户端API)
```go
package wsc

import "github.com/kamalyes/go-wsc/client"

// 导出客户端类型
type (
    Client     = client.Client
    Connection = client.Connection
    WebSocket  = client.WebSocket
)

// 导出客户端方法
var (
    NewClient     = client.New
    NewWebSocket  = client.NewWebSocket
)
```

#### 3.3 exports_hub.go (Hub API)
```go
package wsc

import "github.com/kamalyes/go-wsc/hub"

// 导出Hub类型
type (
    Hub                      = hub.Hub
    ClientConnectCallback    = hub.ClientConnectCallback
    ClientDisconnectCallback = hub.ClientDisconnectCallback
    MessageSendCallback      = hub.MessageSendCallback
)

// 导出Hub方法 (如果需要)
```

#### 3.4 exports_models.go (公共模型)
```go
package wsc

import "github.com/kamalyes/go-wsc/models"

// 导出公共模型
type (
    HubMessage         = models.HubMessage
    MessageType        = models.MessageType
    MessagePriority    = models.MessagePriority
    UserType           = models.UserType
    ConnectionStatus   = models.ConnectionStatus
    DisconnectReason   = models.DisconnectReason
)

// 导出枚举常量
const (
    MessageTypeText   = models.MessageTypeText
    MessageTypeImage  = models.MessageTypeImage
    // ... 其他常量
)
```

---

## 🔄 迁移策略

### 策略1: 渐进式迁移 (推荐)
1. **保留兼容性**: 根目录保留所有导出，通过type alias重定向
2. **分批迁移**: 每次迁移一个模块，确保测试通过
3. **文档更新**: 同步更新API文档
4. **废弃标记**: 对旧导出添加 `// Deprecated: use xxx instead`

### 策略2: 并行开发
1. **新目录结构**: 创建新的模块目录
2. **保持兼容**: 根目录文件保持不变
3. **逐步替换**: 新功能使用新结构，旧功能渐进迁移
4. **版本发布**: 作为v2.0.0发布，提供迁移指南

---

## ✅ 迁移检查清单

- [ ] 创建模块目录结构
- [ ] 迁移Client模块
- [ ] 迁移Hub模块  
- [ ] 迁移Models模块
- [ ] 迁移Repository模块
- [ ] 迁移Protocol模块
- [ ] 迁移Middleware模块
- [ ] 创建根目录导出文件
- [ ] 更新所有import路径
- [ ] 运行所有单元测试
- [ ] 更新文档
- [ ] 创建迁移指南
- [ ] 发布新版本

---

## 📊 预期收益

### 1. 代码组织
- ✅ 清晰的模块边界
- ✅ 更好的代码可读性
- ✅ 降低文件查找时间

### 2. 可维护性
- ✅ 职责更加单一
- ✅ 减少文件间耦合
- ✅ 更容易定位bug

### 3. 可扩展性
- ✅ 新增功能更容易
- ✅ 模块可独立演化
- ✅ 支持插件化扩展

### 4. 团队协作
- ✅ 并行开发更方便
- ✅ 代码冲突减少
- ✅ 新人上手更快

### 5. 对外API
- ✅ 保持根目录简洁
- ✅ 向后兼容性好
- ✅ 用户无感知升级

### 第一步: 创建目录结构 (简化版本)
```bash
cd e:\WorkSpaces\GoProjects\go-wsc
mkdir client hub models repository protocol middleware
# 简洁的6个核心目录，没有internal
```bash
cd e:\WorkSpaces\GoProjects\go-wsc
mkdir client hub models repository middleware internal
# 不再创建过多的子目录，保持结构简洁
```

### 第二步: 先迁移Models (影响最小)
1. 创建 `models/` 目录下的文件
2. 将 `types.go` 拆分并迁移
3. 在根目录创建 `exports_models.go` 做type alias
4. 运行测试确保兼容

### 第三步: 迁移Repository (独立性强)
1. 按子目录迁移各个repository
2. 更新import路径
3. 确保测试通过

### 第四步: 迁移Client和Hub (核心模块)
1. 先迁移Client (相对简单)
2. 再迁移Hub (需要拆分)
3. 创建根目录导出文件
### 第五步: 迁移Protocol和Middleware
1. 迁移协议层代码
2. 迁移中间件
3. 清理根目录
4. 最终测试模块
2. 清理根目录
3. 最终测试

---

## 💡 最佳实践
1. **每次迁移后立即运行测试**
2. **保持git提交粒度小，方便回滚**
3. **先在feature分支完成，测试通过后合并**
4. **更新文档与代码同步进行**
5. **不使用 `internal/` 包，保持模块间的灵活性**
5. **考虑添加 `internal/` 包，防止内部API被外部使用**

---

## 📞 需要决策的问题

1. **是否需要保持v1.x兼容性？**
   - 如果是 → 使用type alias在根目录导出
   - 如果否 → 直接breaking change

2. **迁移节奏？**
   - 激进：一次性全部迁移
   - 保守：分多个版本逐步迁移
3. **内部包访问控制？**
   - 不使用Go的 `internal/` 机制，所有模块都可以被引用
   - 通过清晰的包命名和文档说明哪些是公开APIl/` 机制
   - 哪些是公开API，哪些是内部实现

---

**建议**: 采用渐进式迁移策略，保持根目录API兼容性，用户无感知升级。

go-wsc/
├── hub.go (4887行) ← 根目录保留，逐步瘦身
├── hub_business.go (1021行) ← 根目录保留
└── hub/                    # 新建子模块目录
    ├── hub.go              # 核心结构和类型定义 (~300行)
    │   - Hub 结构体定义
    │   - Client/SSEConnection/NodeInfo 等类型
    │   - 回调函数类型定义
    │   - NewHub 构造函数
    │   - 基础 Getter/Setter
    │
    ├── lifecycle.go        # 生命周期管理 (~200行)
    │   - Run() 启动
    │   - SafeShutdown() 关闭
    │   - WaitForStart() 等待启动
    │   - processPendingMessages()
    │
    ├── registry.go         # 客户端注册/注销 (~400行)
    │   - Register() 注册
    │   - Unregister() 注销
    │   - handleRegister()
    │   - handleUnregister()
    │   - 多端登录策略处理
    │   - 踢人逻辑
    │
    ├── send.go            # 消息发送核心 (~500行)
    │   - sendToUser() 内部发送
    │   - SendToUserWithRetry() 带重试发送
    │   - sendToClient() 发送到客户端
    │   - 重试逻辑
    │   - 发送结果处理
    │
    ├── broadcast.go       # 广播相关 (~200行)
    │   - Broadcast() 广播
    │   - handleBroadcast()
    │   - BroadcastToGroup() 按组广播
    │   - BroadcastToRole() 按角色广播
    │
    ├── ack.go            # ACK确认机制 (~200行)
    │   - SendToUserWithAck()
    │   - HandleAck()
    │   - checkUserOnlineForAck()
    │   - ACK 重试逻辑
    │
    ├── sse.go            # SSE支持 (~150行)
    │   - RegisterSSE()
    │   - UnregisterSSE()
    │   - SendToUserViaSSE()
    │
    ├── callbacks.go      # 回调管理 (~200行)
    │   - OnMessageSend()
    │   - OnOfflineMessagePush()
    │   - OnQueueFull()
    │   - OnHeartbeatTimeout()
    │   - OnClientConnect()
    │   - OnClientDisconnect()
    │   - OnMessageReceived()
    │   - OnError()
    │
    ├── query.go          # 查询和统计 (~300行)
    │   - GetOnlineUsers()
    │   - GetStats()
    │   - GetClientsByUserID()
    │   - GetUserOnlineDetails()
    │   - GetConnectionInfo()
    │
    ├── handlers.go       # 客户端读写处理 (~400行)
    │   - handleClientRead()
    │   - handleClientWrite()
    │   - handleTextMessage()
    │   - handleBinaryMessage()
    │   - checkHeartbeat()
    │
    ├── repository.go     # 仓库管理 (~200行)
    │   - SetMessageRecordRepository()
    │   - SetOnlineStatusRepository()
    │   - SetWorkloadRepository()
    │   - SetHubStatsRepository()
    │   - 各种仓库的 Setter
    │
    ├── vip.go           # VIP功能 (~150行)
    │   - SendToVIPUsers()
    │   - GetVIPStatistics()
    │   - UpgradeVIPLevel()
    │   - FilterVIPClients()
    │
    └── utils.go         # 工具方法 (~200行)
        - 内部辅助函数
        - 类型转换
        - 数据处理工具
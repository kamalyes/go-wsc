# go-wsc 架构重构计划 🏗️

## 📋 重构目标

### 核心目标
1. **分离关注点** - 将臃肿的Hub拆分为职责清晰的模块
2. **简化错误处理** - 建立统一的错误处理体系
3. **改善可测试性** - 降低耦合，提高单元测试覆盖率
4. **提升可维护性** - 清晰的模块边界和接口定义
5. **保持API兼容** - 确保现有用户代码无需修改

## 🏛️ 新架构设计

### 分层架构
```
┌─────────────────────────────────────────────────┐
│                应用层 (Application)                │
│  - Hub Facade (对外API保持兼容)                    │
└─────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────┐
│                服务层 (Service)                   │
│  - MessageService  - ConnectionService           │
│  - ACKService     - RoomService                  │
└─────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────┐
│                管理层 (Manager)                   │
│  - ConnectionManager  - MessageRouter            │
│  - ACKManager        - SecurityManager           │
└─────────────────────────────────────────────────┘
┌─────────────────────────────────────────────────┐
│                基础层 (Infrastructure)             │
│  - EventBus      - ErrorHandler                  │
│  - MetricsCollector  - Logger                    │
└─────────────────────────────────────────────────┘
```

### 核心接口定义
```go
// 核心服务接口
type ConnectionService interface {
    Register(client *Client) error
    Unregister(clientID string) error
    GetClient(clientID string) (*Client, bool)
    GetUserClients(userID string) []*Client
}

type MessageService interface {
    Send(ctx context.Context, msg *Message) error
    SendToUser(ctx context.Context, userID string, msg *Message) error
    SendToRoom(ctx context.Context, roomID string, msg *Message) error
    Broadcast(ctx context.Context, msg *Message) error
}

type ACKService interface {
    SendWithACK(ctx context.Context, msg *Message, timeout time.Duration) error
    HandleACK(ackMsg *ACKMessage) error
}
```

## 📁 新目录结构

```
go-wsc/
├── internal/                    # 内部实现(不对外暴露)
│   ├── core/                   # 核心组件
│   │   ├── hub.go             # 重构后的轻量Hub
│   │   ├── client.go          # Client管理
│   │   └── message.go         # 消息定义
│   ├── service/               # 服务层
│   │   ├── connection.go      # 连接服务
│   │   ├── message.go         # 消息服务
│   │   ├── ack.go            # ACK服务
│   │   └── room.go           # 房间服务
│   ├── manager/               # 管理层
│   │   ├── connection.go      # 连接管理器
│   │   ├── router.go         # 消息路由器
│   │   ├── security.go       # 安全管理器
│   │   └── metrics.go        # 指标管理器
│   ├── infrastructure/        # 基础设施层
│   │   ├── events/           # 事件系统
│   │   ├── errors/           # 错误处理
│   │   ├── retry/            # 重试机制
│   │   ├── logger/           # 日志系统
│   │   └── config/           # 配置管理
│   └── transport/             # 传输层
│       ├── websocket.go      # WebSocket传输
│       └── sse.go           # SSE传输
├── pkg/                       # 对外API
│   ├── hub.go                # Hub门面(向后兼容)
│   ├── client.go             # 客户端API
│   ├── message.go            # 消息API
│   └── types.go              # 公共类型
├── examples/                  # 示例代码
└── docs/                      # 文档
```

## 🔄 重构步骤

### 阶段1: 基础重构 (Week 1-2)
1. **错误处理重构**
   - 重构errors.go，建立错误分类体系
   - 实现统一的错误处理中间件
   - 建立错误码规范

2. **事件系统重构** 
   - 提取EventBus作为独立组件
   - 重构回调机制使用事件驱动
   - 实现异步事件处理

3. **配置管理重构**
   - 提取配置验证逻辑
   - 实现配置热更新
   - 简化配置结构

### 阶段2: 核心重构 (Week 3-4)
1. **连接管理重构**
   - 提取ConnectionManager
   - 实现ConnectionService接口
   - 重构客户端注册/注销逻辑

2. **消息处理重构**
   - 提取MessageService
   - 实现消息路由器
   - 重构消息发送逻辑

3. **ACK系统重构**
   - 独立ACK服务
   - 简化ACK处理逻辑
   - 提升ACK性能

### 阶段3: 高级特性重构 (Week 5-6)
1. **安全系统重构**
   - 独立安全管理器
   - 实现安全中间件
   - 重构认证授权

2. **监控系统重构**
   - 独立监控组件
   - 实现插件化监控
   - 重构指标收集

3. **性能优化**
   - 内存池优化
   - 并发控制优化
   - 网络IO优化

### 阶段4: 测试与文档 (Week 7-8)
1. **全面测试**
   - 单元测试覆盖率>90%
   - 集成测试
   - 性能测试

2. **文档更新**
   - API文档更新
   - 架构文档
   - 迁移指南

## 🎯 重构原则

### 设计原则
1. **单一职责原则** - 每个组件只负责一个职责
2. **开放封闭原则** - 对扩展开放，对修改封闭
3. **依赖倒置原则** - 依赖抽象而不是具体实现
4. **接口隔离原则** - 使用小而专精的接口
5. **最少知识原则** - 组件间最小化依赖

### 技术原则
1. **向后兼容** - 保持现有API不变
2. **渐进式重构** - 分步骤实现，降低风险
3. **测试驱动** - 先写测试再重构
4. **性能优先** - 重构不能影响性能
5. **代码简洁** - 减少代码复杂度

## 📊 重构指标

### 代码质量指标
- **圈复杂度** < 10
- **代码行数** 每个文件 < 500行
- **函数长度** < 50行
- **参数个数** < 5个

### 架构指标
- **依赖深度** < 4层
- **包大小** < 20个文件
- **接口粒度** 方法数 < 10个
- **测试覆盖率** > 90%

### 性能指标
- **启动时间** < 100ms
- **内存使用** 减少30%
- **并发处理** 提升50%
- **延迟** 减少20%

## 🚀 执行计划

### 里程碑
- **M1** (Week 2): 基础重构完成
- **M2** (Week 4): 核心重构完成  
- **M3** (Week 6): 高级特性重构完成
- **M4** (Week 8): 测试与发布

### 风险控制
1. **分支管理** - 在feature分支进行重构
2. **向后兼容** - 保持API facade
3. **渐进发布** - 分阶段发布新版本
4. **回滚计划** - 准备快速回滚方案

## 📝 后续优化

### 长期目标
1. **微服务化** - 支持分布式部署
2. **插件化** - 支持第三方扩展
3. **云原生** - 支持Kubernetes部署
4. **多语言** - 支持多语言客户端

这个重构计划将把go-wsc从一个臃肿的单体架构转变为清晰、可维护、高性能的模块化架构。
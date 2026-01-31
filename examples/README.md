# go-wsc 示例代码

本目录包含 go-wsc 的可运行示例代码，展示各种使用场景

## 📁 示例列表

### 1. demo - 交互式演示 🎮
**文件**: `demo/server.go`

一个完整的交互式演示，展示客户端和服务端的实时通信：
- 服务端自动发送欢迎消息
- 服务端回复客户端消息
- 展示完整的消息收发流程
- 使用 `syncx.Go()` 进行异步处理

**运行**:
```bash
# 1. 启动服务端
cd examples/demo
go run server.go

# 2. 在另一个终端启动客户端（待创建）
go run client.go
```

**特点**:
- ✅ 真实的双向通信
- ✅ 自动欢迎消息
- ✅ 消息回显功能
- ✅ 完整的错误处理

### 2. basic-client - 基础客户端
**文件**: `basic-client/main.go`

展示如何创建一个简单的 WebSocket 客户端：
- 连接到服务器
- 设置消息接收回调
- 发送文本消息
- 处理连接断开

**运行**:
```bash
cd examples/basic-client
go run main.go
```

### 2. basic-client - 基础客户端
**文件**: `basic-client/main.go`

展示如何创建一个简单的 WebSocket 客户端：
- 连接到服务器
- 设置消息接收回调
- 发送文本消息
- 处理连接断开

**运行**:
```bash
cd examples/basic-client
go run main.go
```

### 3. basic-server - 基础服务端
**文件**: `basic-server/main.go`

展示如何创建一个单机 WebSocket 服务器：
- 创建和配置 Hub
- 设置客户端连接/断开回调
- 处理 WebSocket 连接升级
- 创建和注册客户端

**运行**:
```bash
cd examples/basic-server
go run main.go
```

然后使用客户端连接：
```bash
# 在另一个终端
cd examples/basic-client
go run main.go
```

### 3. basic-server - 基础服务端
**文件**: `basic-server/main.go`

展示如何创建一个单机 WebSocket 服务器：
- 创建和配置 Hub
- 设置客户端连接/断开回调
- 处理 WebSocket 连接升级
- 创建和注册客户端

**运行**:
```bash
cd examples/basic-server
go run main.go
```

然后使用客户端连接：
```bash
# 在另一个终端
cd examples/basic-client
go run main.go
```

### 4. distributed-server - 分布式服务端
**文件**: `distributed-server/main.go`

展示如何创建分布式 WebSocket 集群：
- 配置 Redis PubSub
- 启用分布式模式
- 设置在线状态仓储
- 跨节点消息路由

**前置条件**:
- Redis 服务器运行在 `localhost:6379`

**运行**:
```bash
# 启动节点1
cd examples/distributed-server
go run main.go

# 在另一个终端启动节点2（修改端口）
# 编辑 main.go，将 config.NodePort 改为 8081
go run main.go
```

### 4. distributed-server - 分布式服务端
**文件**: `distributed-server/main.go`

展示如何创建分布式 WebSocket 集群：
- 配置 Redis PubSub
- 启用分布式模式
- 设置在线状态仓储
- 跨节点消息路由

**前置条件**:
- Redis 服务器运行在 `localhost:6379`

**运行**:
```bash
# 启动节点1
cd examples/distributed-server
go run main.go

# 在另一个终端启动节点2（修改端口）
# 编辑 main.go，将 config.NodePort 改为 8081
go run main.go
```

### 5. message-send - 消息发送示例
**文件**: `message-send/main.go`

展示各种消息发送模式：
- 单用户发送（带重试）
- 批量发送
- 广播消息
- 群组发送

**运行**:
```bash
cd examples/message-send
go run main.go
```

## 🔧 依赖安装

所有示例需要以下依赖：

```bash
go get github.com/kamalyes/go-wsc
go get github.com/kamalyes/go-config
go get github.com/gorilla/websocket
go get github.com/redis/go-redis/v9  # 仅分布式示例需要
go get github.com/kamalyes/go-cachex # 仅分布式示例需要
```

## 📝 使用说明

### 客户端连接参数

客户端通过 URL 参数传递用户信息：
```
ws://localhost:8080/ws?user_id=user123
```

### 服务端配置

基础配置示例：
```go
config := wscconfig.Default()
config.NodeIP = "127.0.0.1"
config.NodePort = 8080
config.MessageBufferSize = 256
```

### 分布式配置

启用分布式只需两步：
```go
// 1. 设置 PubSub
pubsub := cachex.NewPubSub(redisClient)
hub.SetPubSub(pubsub)

// 2. 设置在线状态仓储
onlineStatusConfig := &wscconfig.OnlineStatus{
    KeyPrefix: "wsc:online:",
    TTL:       24 * time.Hour,
}
onlineStatusRepo := repository.NewRedisOnlineStatusRepository(redisClient, onlineStatusConfig)
hub.SetOnlineStatusRepository(onlineStatusRepo)
```

## 🎯 学习路径

建议按以下顺序学习：

1. **demo** - 🎮 快速体验完整的交互式通信（推荐从这里开始！）
2. **basic-client** - 了解客户端基础用法
3. **basic-server** - 了解服务端基础架构
4. **message-send** - 掌握各种消息发送模式
5. **distributed-server** - 学习分布式部署

## 📚 更多文档

- [分布式架构](../docs/DISTRIBUTED_ARCHITECTURE.md) - 分布式设计详解
- [性能优化](../docs/Performance_Guide.md) - 性能调优指南

## ⚠️ 注意事项

1. **生产环境**: 示例代码中的 `CheckOrigin` 允许所有来源，生产环境需要严格验证
2. **错误处理**: 示例代码简化了错误处理，生产环境需要完善
3. **资源清理**: 确保正确调用 `hub.SafeShutdown()` 和 `client.Close()`
4. **Redis 配置**: 分布式示例需要 Redis，请确保 Redis 服务可用

## 🤝 贡献

欢迎提交新的示例代码！请确保：
- 代码可以直接运行
- 包含必要的注释
- 遵循项目代码规范

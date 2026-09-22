# adapter/gorm（GORM 适配器）

> spi 存储契约的 GORM 实现——连接记录、连接质量、消息归档、离线消息持久化，一行装配钩子注入 Hub

## 这个域干什么

实现 spi 包定义的关系型存储契约目录名 `gorm`，包名 `gormadapter`——
消费方 import 时自取别名（同时引 gorm.io/gorm 时避免冲突）

**管**：连接身份与会话生命周期（connection_store.go）、连接运行时质量指标
（connection_quality.go）、消息发送记录归档与统计（message_sink.go）、离线消息
RDBMS 持久化（offline_queue.go）、装配钩子（hooks.go）

**不管**：Redis 侧契约（adapter/redis）；Redis 队列与 RDBMS 双写的混合离线
处理器编排（messaging）

## 核心概念

| 概念 | 说明 |
|------|------|
| `ConnectionStore` | 连接记录（wsc_connection_records）：connect 身份、会话生命周期、心跳时间戳；批量提交供 batcher 消费 |
| `ConnectionQualityStore` | 连接质量（wsc_connection_qualities）：Ping 统计/消息字节/错误/评分；首次连接建零值行，重连 reconnect_count+1，随 batcher 高频批量更新 |
| `MessageSink` | 消息发送记录：CRUD + 批量更新 + 按 appID/namespace 信封过滤的统计查询 |
| `OfflineStore` | 离线消息 RDBMS 增删查与推送状态维护，不含队列语义；按 message_id 唯一索引去重 |
| `NewHooks` | spi.StoreHooks 实现：一行注入全部关系型仓库 |

## 怎么用

```go
import (
    gormadapter  "github.com/kamalyes/go-wsc/adapter/gorm"
    redisadapter "github.com/kamalyes/go-wsc/adapter/redis"
    "github.com/kamalyes/go-wsc/messaging"
    "github.com/kamalyes/go-wsc/spi"
)

// 一行装配（db 由业务侧持有，本包不接管生命周期）
err := spi.Initialize(ctx, hub, redisClient, db,
    gormadapter.NewHooks(cfg.Database))

// 混合离线处理器（Redis 队列 + RDBMS 持久化）由业务侧组装
handler := messaging.NewHybridOfflineMessageHandler(
    redisadapter.NewOfflineQueue(redisClient, prefix, queueTTL),
    gormadapter.NewOfflineStoreFor(db, cfg.OfflineMessage),
    cfg.OfflineMessage, log)
```

单后端部署：只传本适配器 hook，Redis 侧传 nil；不部署关系型后端时整个 hook
传 nil，纯 Redis 部署照常工作

## 端口与依赖

- 实现 spi 的 `MessageSink` / `ConnectionStore` / `ConnectionQualityStore` /
  `OfflineStore` 契约（hooks.go 编译期断言）
- 依赖 models（契约载荷类型：HeartbeatUpdateEntry / StatsIncrementEntry 等）、
  spi（StoreTarget 能力面）
- 第三方库：gorm.io/gorm；Upsert 冲突子句由 dialect 包适配
- 方言：MySQL / PostgreSQL / CockroachDB，由 GORM Dialector 决定，本包不
  自行判断
- 配置复用 go-config 的 `wscconfig.Database` 配置节，不重复声明

## 性能与陷阱

- 高频写路径（心跳时间戳/Ping 统计/消息计数）为批量接口设计，配合批量提交
  落库，避免单条写放大
- 连接记录与连接质量拆表存储：connect 生命周期与质量指标读写特征不同，
  混表会互相拖累索引效率
- 陷阱：deps.DB 为 nil 时 hook 直接跳过不报错——「该后端未部署」与「装配
  失败」语义不同，排查缺库问题时先查 Initialize 传参

## 后端绑定

RDBMS，经 GORM Dialector：MySQL / PostgreSQL / CockroachDB

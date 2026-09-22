# adapter/redis（Redis 适配器）

> spi 存储契约的 Redis 实现——在线状态 bitmap、节点统计、群组、客服负载、离线队列，一行装配钩子注入 Hub

## 这个域干什么

实现 spi 包定义的 Redis 侧存储契约目录名 `redis`，包名 `redisadapter`——
消费方 import 时自取别名（同时引 go-redis 时避免冲突）

**管**：在线状态 bitmap 判定与多设备索引（online_store.go）、uid→offset 进程级
L1 缓存（uid_offset_cache.go）、节点/集群统计（hub_stats.go）、群组元信息与成员
关系（group_store.go）、客服负载 Redis+DB 双层存储（workload_store.go）、离线
消息 FIFO 队列（message_queue.go）、装配钩子（hooks.go）

**不管**：连接记录/消息记录等 RDBMS 侧契约（adapter/gorm）；Redis 队列与 RDBMS
双写的混合离线处理器编排（messaging）

## 核心概念

| 概念 | 说明 |
|------|------|
| `OnlineStore` | 在线状态：SETBIT/GETBIT 位即真相，无 ZSET 兜底；多设备按 clientID 独立，appID+namespace 信封隔离 |
| `uidOffsetCache` | L1 进程级 uid→offset 缓存：命中时在线判定零网络往返，分片局部清理防命中率雪崩 |
| `HubStats` | 节点统计：Lua 把多命令合并为原子单往返；条件续期（TTL 低于阈值才 EXPIRE，AOF 场景减少冗余写） |
| `GroupStore` | 群组元信息/成员关系/命名空间索引，appID > namespace > groupID 三层隔离 |
| `WorkloadStore` | 客服负载：realtime/hourly 维度 ZSet + DB 持久层，`AcquireLeastLoadedAgent` 单段 Lua 原子选人+全维度预扣减 |
| `MessageQueue` | 与业务无关的队列原语：入队/出队/长度/清空 |
| `NewHooks` | spi.StoreHooks 实现：一行注入全部 Redis 仓库 |

## 怎么用

```go
import (
    redisadapter "github.com/kamalyes/go-wsc/adapter/redis"
    "github.com/kamalyes/go-wsc/spi"
)

// 一行装配（redisClient / db 由业务侧持有，本包不接管生命周期）
err := spi.Initialize(ctx, hub, redisClient, db,
    redisadapter.NewHooks(cfg.Redis))

// 或单独构造某个仓库
store := redisadapter.NewOnlineStore(redisClient, cfg.Redis.OnlineStatus)
stats := redisadapter.NewHubStats(redisClient, cfg.Redis.Stats)
queue := redisadapter.NewMessageQueue(redisClient, "wsc:offline:", 7*24*time.Hour)
```

单后端部署：只传本适配器 hook，gorm 侧传 nil，未注入能力按 spi 契约 nil-safe 降级

## 端口与依赖

- 实现 spi 的 `OnlineStore` / `HubStats` / `GroupStore` / `WorkloadStore` /
  `MessageQueue` 契约（hooks.go 编译期断言）
- 依赖 constants（key 前缀/分桶常量）、models（契约载荷类型）、routing
  （NamespaceFromContext 信封提取）、spi（StoreTarget 能力面）
- 第三方库：github.com/redis/go-redis/v9；WorkloadStore 的 DB 恢复层需
  *gorm.DB（传 nil 时跳过 DB，仅 Redis 生效）
- 配置复用 go-config 的 `wscconfig.RedisRepository` 配置节，不重复声明

## 性能与陷阱

- 在线状态面向千万级连接：bitmap 判定 O(1)、约 1.25MB@1000 万 offset；
  批量在线判定走 Pipeline（offset 解析→GETBIT，两次往返覆盖 N 用户，L1 全命中时
  一次）；心跳续期用轻量路径 `RenewClientsOnline`（跳过序列化/压缩/全量 SETEX，
  client key 缺失时自愈走全量重建）
- L1 缓存容量按在线规模配置（`WS_ONLINE_MAX_CACHED_UIDS`），超限清理走分片
  局部清除
- 陷阱：`WorkloadStore` 仅在 `cfg.Workload != nil` 时由 hook 装配，未启用时
  Hub 侧返回明确的未初始化错误
- 陷阱：热 key（uid_map/all_users/type）按 hash 分桶打散；多 key Lua 脚本在
  Redis Cluster 下要求全部 key 同 slot，Cluster 部署前需评估 key 布局

## 后端绑定

Redis（经 go-redis v9 `UniversalClient`，单实例/主从/Sentinel 均可）；
WorkloadStore 的 DB 恢复层绑定 GORM（任一方言）

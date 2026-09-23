# spi（契约层）

> 核心与后端之间的一纸契约：Store / Sink / Queue 三分法 + 默认装配

## 这个域干什么

`spi` 定义核心库与所有后端之间的接口边界：核心域只认这里的契约，适配器仓库
（go-wsc-redis-adapter 等）只实现这里的契约两边可以独立演进

v4 起本域吸收原 `wiring/` 包（裁决记录 #12）：装配（Initialize 系列）是契约层的
默认组装实现——契约与默认装配放一起，不再单独立包

## 核心概念

### 命名三分法（消灭 Repository 等四种叫法，REFACTORING_DESIGN §7.3）

| 后缀 | 语义 | 契约 |
|------|------|------|
| `*Store` | 读写热点状态，失败可降级 | `OnlineStore` `GroupStore` `WorkloadStore` `ConnectionStore` `ConnectionQualityStore` `HubStats` |
| `*Sink` | 只写不读，记录 / 归档 | `MessageSink` `ArchiveSink` |
| `*Queue` | 进出队 + ack 语义 | `OfflineQueue` `MessageQueue` |

### 其余契约

| 契约 | 说明 |
|------|------|
| `ConnectionAuthenticator` | 认证：`Authenticate(r) (*ConnectionClaims, error)`；默认实现 AES-GCM 对称加解密，内置 transport 域（`transport.NewTokenAuthenticator`），第三方鉴权实现本契约后注入 |
| `Logger` | 日志契约（KV 风格，hub 内默认实现） |
| `StoreDeps` / `StoreHooks` / `StoreTarget` | wiring 装配的注入结构（原 wiring/ 并入） |

### 一等后端 → 契约映射（§7.1）

| 一等后端 | 承担契约 |
|---------|---------|
| Redis | OnlineStore（bitmap）、路由缓存、OfflineQueue、分布式锁 |
| NATS | MessageQueue（JetStream）、跨节点兜底 |
| ClickHouse | ArchiveSink（批量归档） |

## 怎么用

```go
package main

import (
    "github.com/kamalyes/go-wsc/hub"
    "github.com/kamalyes/go-wsc/spi"
)

// 实现契约（通常直接用适配器仓库）
var _ spi.OnlineStore = redisAdapter

// 注入：显式选择，不接即零成本
h := hub.NewHub(cfg,
    hub.WithOnlineStore(redisAdapter),
    hub.WithMessageSink(gormAdapter),
    hub.WithArchiveSink(clickhouseAdapter),
)

// 默认装配（原 wiring）：零注入时各域拿到的就是内存 no-op 实现
spi.Initialize(h, spi.StoreDeps{...})
```

## 端口与依赖

- spi 是依赖链的底：不 import 任何域包，零存储驱动依赖
- 所有契约方法第一参数 `ctx context.Context`（全链路 trace 传播）

## 性能与陷阱

- 契约方法在热路径上（如 OnlineStore.IsOnline）签名要瘦身：只传 key，不传结构体
- 陷阱：契约里出现 `Repository` 后缀即违规（`grep -rnE "Repository(Impl)?\b" spi/` 为空）
- 陷阱：给契约加方法要先问「是否两个以上后端都用」——单后端特有逻辑放适配器

## 后端绑定

本层零绑定：只定义契约一等后端实现矩阵见 §7.1 / §7.4

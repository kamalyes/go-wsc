# messaging（消息域）

> 收敛 → 投递 → ACK → 离线的完整闭环——每一级推送都有明确收敛路径，绝不全量扫描

## 这个域干什么

`messaging` 是消息的进出总管：入站解码分发、路由收敛、P2P / 广播投递、ACK 确认、
失败转离线、重试与查询、消息记录

v4 起本域吸收三个旧包（裁决记录 #10）：
- `inbound/`（dispatcher / mux / forward / probe）→ 入站半边
- `offline/`（HybridOfflineMessageHandler）→ 投递失败出口
- `protocol/`（AckManager / AckStatus / PendingMessage）→ 协议半边

## 核心概念

### 推送三级 API（路由收敛模型，REFACTORING_DESIGN §五.5）

| 级别 | API | 收敛路径 |
|------|-----|---------|
| P2P | `Send(ctx, msg, targets...)` | 本地 registry 命中直投；未命中查路由缓存跨节点 |
| 定向广播 | `BroadcastApps / BroadcastNamespaces / BroadcastGroups` | appID → namespace → groupID → userID 并集，层层索引收敛 |
| 全局广播 | `Broadcast(msg)` | RangeParallel 64 shard 并行扇出 + 合批 writev |

**收敛铁律**：定向广播在源节点完成收敛，远端收到的是 userID 列表，只做 P2P——
索引在内存（group 域三层订阅索引），每层收敛是一次查找而非全量过滤

### 其余核心概念

| 概念 | 说明 |
|------|------|
| `dispatcher` / `mux` | 入站解码与两级路由（MessageType → Topic） |
| 观察者投递 | `NotifyObservers` 攒批入口 → batcher 合并；`NotifyObserversDirect` 直投（三级索引查找 + 预序列化共享 + 跨节点广播），见 observer.go |
| SSE 投递 | `SendToUserViaSSE` 点对点（O(1) 用户索引 + ns 隔离）；`BroadcastToSSEClients` 全量广播（并行分片 + 信封隔离），见 sse.go |
| 上下文键 | `ContextKeySenderID` / `ContextKeyUserID`（连接归属，transport 注入）/ `ContextKeyOfflineBroadcastCollector`（扇出聚合器，context_keys.go） |
| `AckManager` | 发送 → 时间轮插入 (msgID, deadline)；收到 ACK O(1) 取消；超时标记 AckTimeout |
| `ackTimeoutSlack` | 跨节点 ACK 的抖动余量，防误判超时 |
| `HybridOfflineMessageHandler` | Redis + 本地双路径离线队列（按 P2P / group 维度一致性 key） |
| `DeliveryGuarantee` | 消息级保证：at-least-once / exactly-once（单节点内 Outbox） |
| worker_pool | 分池写，单连接单写 goroutine，天然防交叉写 |

## 怎么用

```go
package main

import (
    "context"

    "github.com/kamalyes/go-wsc/hub"
    "github.com/kamalyes/go-wsc/routing"
)

h := hub.NewHub(cfg)
ctx := routing.NewRoute().WithAppID("app-1").WithNamespace("ns-1").Inject(ctx)

// P2P
h.Send(ctx, msg, "user-1001")

// 定向广播：层层收敛（appID+namespace+多 groupID）
h.BroadcastGroups(ctx, msg, "app-1", "ns-1", "group-a", "group-b")

// 全局广播（全 appID 收）
h.Broadcast(ctx, msg)
```

## 端口与依赖

- 消费 group 域的三层订阅索引（收敛数据源）
- 消费 cluster 域的跨节点投递（收敛后的远端 P2P）
- 依赖 spi 契约：`OfflineQueue`（离线）、`MessageSink`（记录）、`ArchiveSink`（归档）

## 性能与陷阱

- P4：逐帧 writev → N 次 syscall；必须合批 + sync.Pool 帧复用
- P16：定向广播全量过滤 → 退化为 O(全连接)；索引必须在内存，Redis 只做跨节点兜底
- ACK 时间轮精度要大于跨节点 RTT 抖动（`node_ack_timeout.go` 兜底）
- 多设备：收敛到 userID 后展开该 user 的全部活跃连接（user/device 双维度）

## 后端绑定

- NATS：跨节点兜底投递通道（`wsc.node.<nodeID>`，JetStream 持久化重试）
- Redis：离线队列（HybridOfflineMessageHandler）
- 不启用时：单节点全内存，投递失败即转本地离线缓冲（可配丢弃）

# cluster（分布式域）

> 节点发现 / 跨节点投递 / 自愈 / 防乒乓——多 Pod 对等组网，无选主

## 这个域干什么

`cluster` 让多个 go-wsc 节点（K8s 里就是多个 Pod）表现得像一个逻辑服务：谁在线、
某用户在哪台节点、消息怎么跨节点送达、节点挂了谁接管、路由索引错了怎么修正

跨节点投递的路由信封经 gRPC metadata 传播（`routing.InjectToOutgoingMetadata` /
`routing.RestoreFromIncomingMetadata`，由独立 routing 包提供）——本域投递链路
只调用，不拥有

## 核心概念

### 节点发现（Redis Hash + 心跳租约，REFACTORING_DESIGN §八.1）

| 概念 | 说明 |
|------|------|
| `NodeRegistry` | `HSET nodes:grpc <nodeID> <addr>` 注册；`HDEL` 优雅摘除（SIGTERM 联动） |
| 心跳租约 | 每 5s `HSET nodes:heartbeat` + `EXPIRE`；TTL 过半未续摘流量，耗尽剔除 |
| 迟滞恢复 | 被摘节点需连续 N 周期正常续租才恢复接流，防 P17 乒乓 |

### 跨节点投递

```
主路径：gRPC 直连（GRPCClientPool + per-node 熔断）
兜底 1：Redis PubSub（最后通道，尽力而为）
```

| 概念 | 说明 |
|------|------|
| `RouterCache` | 三层路由缓存：进程内 LRU → Redis → 全节点广播查询 |
| `GRPCClientPool` | 连接池 + 每节点独立熔断（半开探测恢复） |
| 投递语义 | at-least-once，幂等由业务 messageID 保证 |

### 自愈四件套

| 机制 | 触发 → 动作 |
|------|------------|
| 死索引修正（self_heal） | 投递被拒 → 修正 user→node 映射 + 回源重投 |
| 幽灵连接回收 | 路由指向已剔除节点 → rerouteGuard 换节点 + 清理死索引 |
| ACK 超时兜底（node_ack_timeout） | 跨节点 ACK 超时 → 标记 + 转离线 + 在线状态核对 |
| 重路由防乒乓（reroute_guard） | 消息级拒绝集合：拒绝过的节点不再投，超限转离线 |

**ping-pong 死循环场景**：节点 A、B 互为对方死索引的兜底 → 无守卫时无限互弹；
rerouteGuard 用消息级拒绝集合切断环路

## 怎么用

```go
package main

import (
    "github.com/kamalyes/go-wsc/cluster"
)

// 节点注册（hub.Run 内部自动执行，这里示意手动用法）
nr := cluster.NewNodeRegistry(nodeID, grpcAddr, redisClient)
nr.Register(ctx)
defer nr.Unregister(ctx)   // 优雅退出时主动摘除

// 路由缓存
rc := cluster.NewRouterCache(redisClient)
rc.SetUser(ctx, "user-1001", []string{"node-a"})   // 写 user→node
nodes, _ := rc.GetUser(ctx, "user-1001")           // 查：LRU → Redis → 广播

// gRPC 客户端池（跨节点直连投递）
pool := cluster.NewGRPCClientPool(opts...)
```

## 端口与依赖

- 依赖 spi 契约：`NodeRegistry`（发现）、`MessageQueue`（NATS 兜底）
- gRPC 依赖在适配器仓库（go-wsc-grpc-adapter）：核心仓库 proto/ 仅生成期使用
- K8s：Pod IP 经 Downward API 注入，直连走 Pod 网络，不用 Headless Service（§八.4）

## 性能与陷阱

- P11：路由抖动 → 投错 → 重投 → 死循环；rerouteGuard 是唯一防线
- P17：租约误判——TTL 不能小于两次续期间隔 ×3；摘除要过半 + 迟滞恢复双保险
- 熔断 per-node 隔离：单节点故障不能熔断整个池

## 后端绑定

- Redis（一等）：节点发现租约 + 三层路由缓存的共享层
- NATS（一等）：兜底投递通道（JetStream 持久化 + Explicit ack）
- 不启用时：单节点模式，cluster 域空转（零连接、零租约）

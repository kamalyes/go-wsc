# stats（统计域）

> bitmap 在线状态 + 指标 + 健康——谁在线的 O(1) 位查询，在线规模的全量视图

## 这个域干什么

`stats` 回答两个问题：「某个用户现在在线吗」（点查询）和「现在有多少人在线、
分布在哪」（面查询），同时承载 Hub 的指标与健康度上报

**管**：在线状态（bitmap 点查 / 批查 / 全量游标分页）、指标聚合、健康检查
（HubHealthInfo）、在线状态与 Redis 的同步

**不管**：连接注册表（connection 管「进程内这条连接」，本域管「集群视角谁在线」）

## 核心概念

| 概念 | 说明 |
|------|------|
| bitmap 在线 | `uid → offset（hash 分桶）→ SETBIT/GETBIT`；1M 在线 ≈ 125 KB（REFACTORING_DESIGN §五.4） |
| Pipeline 批查 | `BatchIsUserOnline`：HGET offset + GETBIT 打包 Pipeline，一次往返 |
| uid→offset LRU | 进程内缓存，容量联动 `WS_ONLINE_MAX_CACHED_UIDS`；部分 shard 清理防命中率雪崩 |
| 游标分页 | `GetAllOnlineUsers` 用 SCAN/HSCAN 游标，不阻塞主循环（P8） |
| 本地兜底 | OnlineStore 未启用时回退本地注册表查询 |
| `Host` 端口 | ports.go 声明，hub 实现——后端异常时降级本地 |

## 怎么用

```go
package main

import (
    "context"

    "github.com/kamalyes/go-wsc/stats"
)

mgr := stats.NewManager(deps)

// 点查询
online, err := mgr.IsUserOnline(ctx, "user-1001")

// 批量查询（Pipeline 合并，一次往返）
results, err := mgr.BatchIsUserOnline(ctx, "user-1001", "user-1002", "user-1003")

// 全量在线（游标分页，绝不 ZRANGE 0 -1）
err := mgr.GetAllOnlineUsers(ctx, func(batch []string) error {
    // 每批处理
    return nil
})

// 健康与指标
health := mgr.Health()
```

## 端口与依赖

- 依赖 spi 契约 `OnlineStore`（一等 Redis 实现：go-wsc-redis-adapter 的 bitmap）
- 依赖 connection 注册表做本地兜底

## 性能与陷阱

- P7：uid→offset 频繁查映射 → 1M+ 次 RTT；必须 LRU 缓存 + 容量与在线规模联动
- P8：`ZRANGE 0 -1` 全量扫描 → Redis 阻塞 + 内存尖峰；游标分页是唯一正解
- P13：bitmap 冷启动缺 offset → 首查慢；先查一次映射再入 LRU
- 热点 key：bitmap 桶 `bm:<app>:<ns>` 已 hash 分桶 64，防单 slot 热点

## 后端绑定

- Redis（一等 OnlineStore）：bitmap 在线状态 + uid_map 分桶映射
- 不启用时：本地注册表兜底，只答本节点在线（单节点语义完整）

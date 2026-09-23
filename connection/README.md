# connection（连接域）

> 百万连接的注册 / 查找 / 心跳 / 容量——64 分片注册表是整个库的地基

## 这个域干什么

`connection` 管理连接的全生命周期数据结构：一个连接进来后它在哪注册、怎么按
userID 找到它、多设备怎么算一个人、心跳超时怎么管、慢消费者怎么隔离

**管**：分片注册表（注册/注销/按 userID 查找）、心跳管理（HeartbeatManager：
时间轮 O(1) 超时 + SSE 兜底扫描）、连接生命周期（LifecycleManager：多端登录
治理 / 踢出断链 / 精简移除）、连接记录（RecordManager：快照构造 / 异步落库 /
停机批量终态）、连接容量（capacity）、控制通道（control lane）、
慢消费者识别与降级

**不管**：消息语义（messaging）、接入握手（transport）、在线状态存储（stats + Redis）

## 核心概念

| 概念 | 说明 |
|------|------|
| `ShardedRegistry` | 64 个 `map[userID][]*Client` 分片，FNV-1a hash(userID) % 64 定位 |
| 分片数 | `max(CPU核数, 64)`：锁竞争比单 map 降 64 倍（REFACTORING_DESIGN §五.1） |
| `ClientMatchesEnvelope` | appID / namespace / groupID 信封匹配，广播过滤的判据 |
| `RegistryCapacity` | 容量上限 + 拒绝策略（满载时新连接直接拒绝） |
| `HeartbeatManager` | 心跳管理器：前置回调 → 时间轮续期 → Redis 续期入队 → 回调链 → 统计 |
| `LifecycleManager` | 生命周期管理器：多端登录策略（禁多端踢旧 / 上限踢最旧）、踢出断链（先通知后注销）、shutdown 精简移除 |
| `MultiLoginPolicy` | 多端登录策略快照：AllowMultiLogin + MaxConnectionsPerUser，按 appID+namespace 信封隔离 |
| `RecordManager` | 连接记录管理器：Client → ConnectionRecord 快照构造、异步 Upsert、停机批量标记断开（1001） |
| control lane | 必达通道：心跳/ACK/控制帧绕过普通队列，慢消费者也不丢 |
| 慢消费者 | 连续 N 次写超阈值的连接标记降级，防积压拖垮全 Hub |
| 多设备 | 同一 userID 多条连接：注册表按 `[]*Client` 存，user/device 双维度计数 |

## 怎么用

```go
package main

import (
    "github.com/kamalyes/go-wsc/connection"
)

reg := connection.NewShardedRegistry(connection.RegistryCapacity{
    MaxConnections: 1_000_000,
})

// 注册 / 注销
reg.Register(client)      // 连接建立
reg.Unregister(client)   // 连接关闭（引用计数，最后一条才清 user 身份）

// 查找
clients := reg.GetClients("user-1001")   // 该用户全部活跃连接（多设备）

// 遍历（广播用）：shard 级并行
reg.ForEachClientParallel(workerNum, func(c *connection.Client) {
    // writev 合批写……
})
```

## 端口与依赖

- 定义 `interfaces.go` 消费者端口（EvictHook / HeartbeatHost / LifecycleHost / RecordHost），由 hub 实现
- 连接记录仓储经 RecordHost 端口动态读取（spi.ConnectionStore），支持运行期注入；其余零外部依赖：纯内存数据结构（含时间轮），不 import 任何存储驱动

## 性能与陷阱

- 量级：100 万连接 ≈ 200 MB 注册表索引（不含帧缓冲，全量估算见 REFACTORING_DESIGN §五.1）
- P1：分片数不随核数——4 核机器跑 64 分片反而慢，用 `max(CPU核数, 64)`
- P2：慢消费者积压——必须 control_lane 优先 + 降级，否则一个慢连接拖垮全 Hub
- P3：伪共享——每 shard 独立 struct 填充到 cache line 边界（50 万连接以上再看）

## 后端绑定

纯内存在线状态的持久化视角（谁在线）归 stats 域 + Redis OnlineStore；
本域只管「进程内这条连接」

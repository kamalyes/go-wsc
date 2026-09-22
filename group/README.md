# group（群组域）

> 三层订阅索引 + VIP 分级 + 观察者 + 分组负载——定向广播收敛的数据源。

## 这个域干什么

`group` 维护「谁在哪个组」的全部内存索引，是 messaging 域定向广播层层收敛
（appID → namespace → groupID → userID）的数据源。同时提供 VIP 分级投递、
观察者旁路、分组负载三类派生能力。

**管**：三层订阅索引、群组成员增删与生命周期（含系统组自动进出）、VIP 分级、
观察者（旁路监听）、分组负载（选最闲的目标）。

**不管**：消息投递本身（messaging 消费本域索引）、群组持久化（GroupStore 可选）。

## 核心概念

| 概念 | 说明 |
|------|------|
| 三层索引 | `appID → namespace → groupID → set(userID)`，读多写少，无锁读 |
| `Manager` | 群组域门面：聚合 lifecycle / observer / vip / workload |
| `LifecycleManager` | 成员进出 + 系统组（全员组）自动加入/退出 + 回调 |
| 系统组 | 连接建立自动加入（如 appID 全员组）；最后一条连接断开才移除身份 |
| `ObserverManager` | 三层观察者：namespace 级 / group 级旁路监听（监管场景） |
| `VIPManager` | VIPLevel 分级：按 minLevel / exactLevel 过滤投递 + 优先级提升 |
| `WorkloadManager` | 分组负载：`GetLeastLoaded` / `AcquireLeastLoaded` 选最闲目标 |
| Host 端口 | `ports.go` 定义 `Host`，由 hub 实现（子包不持 `*Hub`） |

## 怎么用

```go
package main

import (
    "context"

    "github.com/kamalyes/go-wsc/group"
)

mgr := group.NewManager(deps)

// 成员管理（连接生命周期内自动驱动）
mgr.AddGroupMembers(ctx, appID, ns, "group-a", []string{"user-1001", "user-1002"})
mgr.RemoveGroupMembers(ctx, appID, ns, "group-a", []string{"user-1001"})

// 收敛查询：messaging 定向广播的底层数据源
// groupID → member userIDs → 逐个 P2P 投递

// VIP 分级投递
mgr.SendToVIPUsers(ctx, msg, group.VIPFilter{MinLevel: group.VIPLevelV3})

// 观察者（旁路）
mgr.GetObserversForMessage(ctx, msg)

// 分组负载：选最闲
target, err := mgr.AcquireLeastLoaded(ctx, appID, ns, "group-work")
```

## 端口与依赖

- `Host` 端口由 hub 实现；依赖 spi 契约 `GroupStore`（可选持久化）、`WorkloadStore`（可选共享负载）。
- 内存索引为主：广播路径上**零 RTT**，Redis 只在多节点共享时介入。

## 性能与陷阱

- 索引读路径无锁（读多写少），但连接频繁进出时写锁是热点——批量进出合并提交。
- 系统组移除必须引用计数：同 user 多设备时，最后一条连接断开才移除身份
  （`LeaveSystemGroupsKeepsIdentityWhenOtherConnectionsExist` 语义）。
- 陷阱：把群组实时落 Redis 再广播——广播路径加 1 次 RTT，收敛索引必须驻内存。

## 后端绑定

- Redis（可选）：GroupStore / WorkloadStore 多节点共享。
- 不启用时：纯内存群组，节点重启后靠重连重建索引。

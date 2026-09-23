# batcher（攒批域）

> 高频小写的攒批出口——把逐条落库合并成批量事务，抑制写放大

## 这个域干什么

`batcher` 承接五类高频小写场景：消息状态回报、消息记录落库（write-ahead
outbox）、消息收发统计、心跳统计、观察者通知。逐条写库在千万级连接下会
打爆后端，本域用统一的 `syncx.BatchProcessor` 攒批模型（攒满或到期 →
flush）把 N 次写合并为 1 次批量操作

## 核心概念

| 概念 | 说明 |
|------|------|
| `Manager` | 域管理器：持有五个攒批组件，统一构造（nil/零值参数兜底）与分段停机 |
| `MessageStatusUpdater` | 消息状态攒批：按 status+reason+errMsg 分组，合并同组 SQL |
| `MessageRecordOutbox` | 消息记录攒批 outbox：write-ahead INSERT 攒批化，早到状态更新合并进内存 |
| `MessageStatsBatcher` | 消息统计攒批：按 connectionID 聚合收发条数与字节数 |
| `HeartbeatStatsUpdater` | 心跳统计攒批：按 clientID 去重保留最新，批量刷连接质量表 |
| `ObserverNotificationBatcher` | 观察者通知攒批：合并同窗口的观察者回调 |
| `BatchProcessor` | go-toolbox 泛型攒批骨架：Submit 入队、条件触发 flush、Stop 冲刷余量 |

```
Submit(item) ──► 队列(有界) ──► 攒满 batchSize / 到期 flushInterval ──► flush(聚合分组) ──► 批量落库
     │
     └─ 队列满返回 false：调用方自行降级（记日志/丢弃），不阻塞热路径
```

## 怎么用

```go
package main

import (
    "github.com/kamalyes/go-config/pkg/wsc"
    "github.com/kamalyes/go-wsc/batcher"
)

// 编排层组装：一个 Manager 持有五个组件，参数 nil/零值时默认值兜底
mgr := batcher.NewManager(host, cfg.Batcher)

// 业务侧通常只感知 Submit
if !mgr.StatusUpdater().Submit(&batcher.StatusUpdateItem{
    MessageID: msgID,
    Receiver:  receiver,
    Status:    status,
}) {
    // 队列满：降级处理（日志 + 丢弃）
}

mgr.StopTracking() // 连接清理前：心跳/消息统计/观察者通知冲刷余量
mgr.StopRecords()  // 连接清理后：先 outbox 后状态更新，保 INSERT→UPDATE 顺序
```

## 端口与依赖

- 消费者端口：`Host`（Manager 聚合构造所需能力面，hub 实现）、
  `StorageBatchWriter`（消息状态/消息统计/心跳统计共用）、
  `ObserverNotifier`（观察者通知）、`MessageRecordSinkProvider`（记录 outbox）
- 落库目标经 spi 契约注入：`MessageSink`（消息记录）、`ConnectionStore`（连接记录）、
  `ConnectionQualityStore`（连接质量）
- 依赖 go-toolbox `syncx.BatchProcessor`（攒批骨架）与 `mathx`（分片计算）

## 性能与陷阱

- flush 必须用 `context.WithTimeout(context.Background(), 5s)`：
  带 cancel 的 ctx 会把 Hub 关闭时最后一批落库截断
- 队列有界：Submit 返回 false 即满，调用方必须处理满载语义，禁止阻塞等待
- 心跳攒批按 clientID 去重保留最新——重连期间的旧心跳不会覆盖新连接的统计
- 攒批参数（batchSize/worker/interval）与后端写入能力匹配：批过大触发后端
  锁等待，批过小退化为逐条写

## 后端绑定

- RDBMS（GORM 方言：MySQL / PG / CRDB）：消息状态、消息统计、心跳统计攒批落库
- Redis：心跳统计的 64 分片变更集刷写（时间戳续期路径）

# models（数据层）

> 纯 struct 零依赖——全部域共享的数据词汇表

## 这个域干什么

`models` 放所有域共享的数据结构：客户端、消息、群组、连接记录、枚举、错误哨兵
它是依赖链的最底层——**零外部依赖、零逻辑**，只有字段与简单方法

## 核心概念

| 概念 | 说明 |
|------|------|
| `Client` | 服务端视角的连接客户端：身份、状态、元数据（并发安全的 JSON 快照 marshal） |
| `HubMessage` | 消息主体：链式 Setter、Clone、trace 上下文注入（`InjectContext`） |
| `ConnectionRecord` / `ConnectionQuality` | 连接落库记录 / 质量打分（LiveScore 加权） |
| 枚举族 | MessageType、Priority、VIPLevel、UserStatus、NodeStatus 等（带 FromInt 解析） |
| 错误哨兵 | `ErrUserOffline` `ErrQueueFull` 等 + `IsRetryableError` 分类判定 |
| `DeliveryGuarantee` | 消息级投递保证解析：type 表 / 显式覆盖 / 分类分兜底 |
| `DeliverResult` / `BroadcastResult` | 投递与广播的结果结构 |

## 怎么用

```go
package main

import (
    "errors"

    "github.com/kamalyes/go-wsc/models"
)

msg := models.NewHubMessage().
    SetMessageType(models.MessageTypeText).
    SetContent("hello")

if err := models.ErrUserOffline; errors.Is(err, models.ErrUserOffline) {
    // 走离线路径
}

quality := models.ConnectionQuality{}
quality.UpdatePingStats(rtt)
score := quality.LiveScore()
```

## 端口与依赖

- 零依赖（import 链终点）；除标准库外不 import 任何包
- pb 相关测试随 `go-wsc-grpc-adapter` 走，本包不含生成代码

## 性能与陷阱

- `HubMessage` 的元数据 Map 并发读写有锁保护——高频字段尽量用具名列，别塞 Map
- JSON marshal 走安全快照（已验证并发正确），新增字段必须进快照测试
- 陷阱：往 models 加方法时先问「有没有逻辑」——有逻辑的去对应域

## 后端绑定

无纯数据结构，所有后端适配器共享本包词汇

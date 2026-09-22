# constants（常量域）

> 默认值、归一化——零依赖的共享基准源

## 这个域干什么

`constants` 提供两类零依赖的共享基准：协议常量与默认值、appID/namespace/groupID
归一化；路由上下文的构建与传播见独立的 `routing` 包（routing → constants 单向
依赖，原子归一化函数收口于此）

## 核心概念

| 概念 | 说明 |
|------|------|
| 归一化 | `NormalizeAppID` / `NormalizeNamespace` / `NormalizeGroupID`：构造 Redis key 前统一口径（自定义大写内容保持原样） |
| 路由默认值 | `DefaultAppID` / `DefaultNamespace` / `DefaultGroupID`（route.go，"__" 前缀系统保留） |
| gRPC metadata key | `MetadataKeyAppID` 等（metadata.go）：跨节点路由信封的 header 名 |
| Bitmap 常量 | `GlobalBitmapNS` / `DefaultMaxBitmapOffset` / `DefaultKeyBucketCount`（bitmap.go） |
| 其余默认值 | 准入水位、整形速率、合并容量、慢消费者阈值、消息 deadline 等（delivery.go） |

## 怎么用

```go
package main

import (
    "context"

    "github.com/kamalyes/go-wsc/constants"
    "github.com/kamalyes/go-wsc/routing"
)

// 归一化：进 Redis key 构造前必做（Go 层归一，Lua 不兜底）
appID := constants.NormalizeAppID("App-1")
ns := constants.NormalizeNamespace("NS-1")

// 路由构建与注入在 routing 包（链式构建器，每个维度显式命名）
ctx = routing.NewRoute().WithAppID(appID).WithNamespace(ns).WithGroup("group-a").Inject(ctx)

// 下游随时取
got := routing.RoutingFromContext(ctx)
groups := routing.GroupIDsFromContext(ctx)
```

## 端口与依赖

- 零依赖：不 import 任何域包（方向永远是自己 ← 别人）
- 被 routing 单向依赖：routing.NormalizeRoute 收口调用本包原子归一化
- 归一化是构造 Redis key 的强制前置（REFACTORING_DESIGN 硬约束：Go 层归一，脚本不兜底）

## 性能与陷阱

- 常量集中在此、config 引用在此——避免「常量与 config 各养一份默认值」的双头维护
- 归一化保持幂等：输入已归一化时零开销返回，别重复分配
- 陷阱：在 Lua / SQL 里做归一化兜底——归一化必须在 Go 层一次做对

## 后端绑定

无纯常量与纯函数

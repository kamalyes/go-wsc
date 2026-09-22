# routing（路由域）

> 路由信封的构建、注入与提取——appID/namespace/groupIDs 贯穿全链路的唯一载体

## 这个域干什么

`routing` 定义路由元数据（appID + namespace + groupIDs）的规范表达与传播：
本地调用链经 `Route` 链式构建器注入 ctx、`*FromContext` 系列提取；
跨节点 gRPC 经 metadata headers 传播（`x-routing-*` 三 header）

隔离层次：AppID(应用) > Namespace(租户) > GroupID(平台) > UserID；
空值归一化在入口层一次做对（`Inject` / `EnsureRouteDefaults`），下游不兜底

## 核心概念

| 概念 | 说明 |
|------|------|
| `RoutingContext` | 路由三元组（appID/namespace/groupIDs），一次 ctx 断言全取出 |
| `NewRoute` / `RouteFrom` | 链式构建器 / 从现有 ctx 继承派生（`With*` 显式命名每个维度） |
| `Inject` / `EnsureRouteDefaults` | 注入 ctx（appID 补默认）；严格场景 ns 空补默认（广播场景不补） |
| `*FromContext` 系列 | 提取侧：AppID/Namespace/GroupIDs/FirstGroupID，零分配热路径 |
| `NormalizeRoute` | appID+namespace 归一化（收口调用 constants 原子函数） |
| `InjectToOutgoingMetadata` / `RestoreFromIncomingMetadata` | gRPC 跨节点信封的注入与恢复 |

## 怎么用

```go
package main

import (
    "context"

    "github.com/kamalyes/go-wsc/routing"
)

// 注入：链式构建器，每个维度显式命名，漏传一目了然
ctx = routing.NewRoute().
    WithAppID("app-1").
    WithNamespace("ns-1").
    WithGroup("group-a").
    Inject(ctx)

// 严格场景兜底（P2P/群组发送；全局广播不要调用——ns 留空=全命名空间）
ctx = routing.EnsureRouteDefaults(ctx)

// 提取：下游随时取，不再兜底
appID := routing.AppIDFromContext(ctx)
ns := routing.NamespaceFromContext(ctx)
```

## 端口与依赖

- 单向依赖 constants：NormalizeAppID/Namespace 原子归一化、MetadataKey 常量、默认值
- 被 models/group/messaging/cluster 单向依赖；零存储、零 Web 框架依赖

## 性能与陷阱

- `*FromContext` 系列零分配热路径：ctx 无路由就是空串/nil，不做默认值兜底
- 陷阱：广播场景误调 `EnsureRouteDefaults`——namespace 空值是"全命名空间"语义，补默认即收窄
- 陷阱：绕过 Route 直接 `context.WithValue`——routingCtxKey 不可导出，外部无法伪造

## 后端绑定

无纯 context 传递与纯函数

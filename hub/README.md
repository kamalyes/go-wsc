# hub（编排层）

> 组装 7 个域、持有全部消费者端口的实现、对外暴露唯一入口——只编排，不实现

## 这个域干什么

`hub` 是 go-wsc 的对外唯一入口它把 connection / transport / messaging / group /
stats / cluster / overload 七个域装配成一个可运行的 `Hub`，并实现各域声明的
「消费者端口」（每个子包的 `ports.go`）

**管**：域的装配与生命周期（Run / SafeShutdown / WaitForStart）、端口实现集中处
（ports.go）、对外 API 的签名聚合、context key 定义

**不管**：任何业务逻辑注册表在 connection，投递在 messaging，削峰在 overload——
hub 里出现 `for` 循环处理消息就是设计倒退

## 核心概念

| 概念 | 说明 |
|------|------|
| `Hub` | 编排器本体，~30 字段（各域 Manager 的引用），无业务方法 |
| `NewHub(cfg)` | 构造入口，按配置装配各域；零注入时各域为纯内存实现 |
| 消费者端口 | 子包在 `ports.go` 声明最小接口，Hub 集中实现（见 `hub/ports.go`） |
| `With*` 选项 | `WithOnlineStore` / `WithMessageSink` 等注入后端适配器 |
| `Run` / `SafeShutdown` | 启动各域后台任务 / 排空并停止（联动 K8s SIGTERM drain） |

## 怎么用

```go
package main

import (
    "net/http"

    "github.com/kamalyes/go-wsc/hub"
)

func main() {
    h := hub.NewHub(cfg)          // 纯内存单节点：零注入即可跑
    // h := hub.NewHub(cfg, hub.WithOnlineStore(redisAdapter))  // 接后端是显式选择

    go h.Run()
    defer h.SafeShutdown()

    // 框架无关：标准 http 签名，随便嵌哪个路由
    http.HandleFunc("/ws", h.HandleWebSocketUpgrade)
    http.HandleFunc("/sse", h.HandleSSEUpgrade)
    http.ListenAndServe(":8080", nil)
}
```

## 端口与依赖

- hub 实现所有子包的消费者端口（`batcher.StorageBatchWriter`、`group.Host`、`stats.Host` 等）
- 只依赖 spi 契约与各域 Manager，**零** gorm / redis / grpc / nats / clickhouse import
- 铁律：`grep -rn "middleware.|repository." hub/` 必须为空（详见 REFACTORING_DESIGN §一.1）

## 性能与陷阱

- hub 本身无热路径——百万连接的吞吐在各域内部，hub 只做指针转发
- 陷阱：往 Hub 上继续堆方法就是回到 74 字段的上帝对象老路；新能力先进对应域，hub 只加端口

## 后端绑定

纯编排，无后端所有 `With*` 注入的都是适配器仓库（go-wsc-redis-adapter 等）的实例

# transport（接入层）

> 框架无关的 WS / SSE 接入层——升级握手、Token 解密、属性提取、连接预检，标准 net/http 签名，可嵌任何路由。

## 这个域干什么

`transport` 是连接进来的第一站：HTTP 升级成 WebSocket、降级成 SSE、连接合法性校验、
Token 解密。它只认 `net/http` 的标准签名，不 import gin / go-zero / echo / fiber。

**管**：WS 升级握手与编排（upgrader.go）、SSE 流式接入与写循环（sse.go）、连接参数
预检（validate.go）、Token 签发与解密（token.go）、编排层回调端口（ports.go）。

**不管**：连接后的读写泵（messaging）、连接注册与通道管理（connection）。鉴权算法的
契约在 spi.ConnectionAuthenticator；默认实现（AES-GCM 对称加解密）已内置本域 token.go，
第三方鉴权（OAuth / 私有 Token）实现契约后在适配器仓库注入。

## 核心概念

| 概念 | 说明 |
|------|------|
| `Upgrader` | WS 升级器组件：升级配置（Origin 白名单/缓冲区）、属性提取（Token 优先明文兜底）、客户端构造、升级编排 `HandleWebSocketUpgrade(w, r)` |
| `SSEHandler` | SSE 接入组件：`HandleSSEUpgrade(w, r)` 升级编排（同步注册后阻塞写循环）、`WriteLoop` 五重退出（消息通道关闭/关闭通道/请求取消/编排层关闭/心跳） |
| `Registrar` 端口 | 接入流程对编排层的回调契约：`Register`（异步）/ `RegisterSync`（同步）/ `Unregister` / `IsShutdown` / `SendRegisteredMessage`，由 hub 编排层实现注入，本域不持有 *Hub |
| `ValidateHandler` | 连接前的合法性探测 `HandleValidateConnection(w, r)` |
| `ClientAttributes` | 升级前的请求元数据提取结果（身份/设备/隔离维度/群组），Token 与明文两条提取路径共用同一结构 |
| `ConnectionAuthenticator` | spi 契约：`Authenticate(r) (*ConnectionClaims, error)`；默认实现为本域 `TokenAuthenticator`（AES-GCM） |
| `IssueConnectionToken(cfg, appID, claims)` | 签发连接 Token（登录服务调用后下发给客户端；按 appID 选密钥加密） |

## 怎么用

```go
package main

import (
    "net/http"

    wscconfig "github.com/kamalyes/go-config/pkg/wsc"
    "github.com/kamalyes/go-wsc/transport"
)

cfg := wscconfig.Default() // 业务侧完整配置
registrar := hub.NewHub(cfg) // 编排层实现 transport.Registrar 端口（P2 批4 装配）

upgrader := transport.NewUpgrader(cfg, registrar).
    WithNodeID("node-1").
    WithHubContext(hubCtx).
    WithAuthenticator(transport.NewTokenAuthenticator(cfg.Security.ConnectionToken, nil)) // 启用连接 Token 时注入

sse := transport.NewSSEHandler(cfg, registrar, upgrader).WithNodeID("node-1")

// 标准 net/http
mux := http.NewServeMux()
mux.HandleFunc("/ws", upgrader.HandleWebSocketUpgrade)
mux.HandleFunc("/sse", sse.HandleSSEUpgrade)
mux.HandleFunc("/ws/validate", transport.NewValidateHandler(cfg).HandleValidateConnection)

// gin
// r.GET("/ws", gin.WrapF(upgrader.HandleWebSocketUpgrade))

// go-zero
// engine.AddRoute(rest.Route{Method: http.MethodGet, Path: "/ws",
//     Handler: func(w http.ResponseWriter, r *http.Request) { upgrader.HandleWebSocketUpgrade(w, r) }})

http.ListenAndServe(":8080", mux)
```

## 端口与依赖

- 依赖 spi 的 `ConnectionAuthenticator` 契约；默认实现 `NewTokenAuthenticator`（AES-GCM
  对称加解密）内置本域。第三方鉴权（OAuth / 自定义 Token）实现契约后注入。
- 依赖 connection 的 `ChanPool`（升级路径预初始化客户端通道，可选项，注册路径幂等兜底）。
- 对编排层仅经 `Registrar` 端口交互（ports.go），无 hub import。
- 零框架依赖是铁律：`grep -rn "gin.|gozero.|echo." transport/` 为空。

## 性能与陷阱

- 升级是低频路径（相对消息收发），优化优先级低于 messaging；但握手期的 Token 校验
  不要做重 IO（查库类操作放适配器内缓存）。AES-GCM 解密按密钥遍历，密钥数量即遍历
  上限，多 appID 场景密钥集规模可控。
- SSE 写循环是 handler 级阻塞（一连接一 goroutine），退出五重 select 全部零成本；
  心跳注释行（`: ping`）浏览器 EventSource 自动忽略。
- 陷阱：gin / go-zero 适配需求一律走独立适配器仓库，别往核心塞一行框架 import。
- 陷阱：`WebSocketOrigins` 白名单未配置时默认放行所有来源，生产环境建议显式配置。

## 后端绑定

无。纯协议层。认证后端经 spi 契约按需注入。

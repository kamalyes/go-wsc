# client（客户端 SDK）

> 连接 go-wsc 服务端的客户端——WS 优先、SSE 降级、断线自动重连

## 这个域干什么

`client` 是给业务方用的连接 SDK：建连、认证、收发消息、心跳保活、断线重连
服务端能力（hub）之外的「另一半」——没有它，业务方要自己手写 WebSocket 客户端

## 核心概念

| 概念 | 说明 |
|------|------|
| `Client` | 客户端门面：连接管理 + 消息收发 + 重连策略（`New(url)` 构造） |
| `WebSocket` | WS 连接封装：`NewWebSocket` 建连，认证走 subprotocol 握手 |
| `DefaultUpgrader` | 默认升级器（连接参数基准） |
| `ClientMessage` | 客户端收发消息的统一结构 |
| `IsNormalClose` | 正常关闭判定（重连策略的判据：异常断开才重连） |
| SSE 降级 | WS 不可用时降级 SSE（单向收，上行走 HTTP） |

## 怎么用

```go
package main

import (
    "github.com/kamalyes/go-wsc/client"
)

c := client.New("wss://example.com/ws")

c.OnTextMessageReceived(func(message string) {
    // 处理服务端消息
    _ = message
})

c.OnDisconnected(func(err error) {
    // 异常断开：SDK 按 AutoReconnect 配置自动重连
    _ = err
})

go c.Connect()

_ = c.SendTextMessage("hello")
```

## 端口与依赖

- 只依赖标准库 + gorilla/websocket，不依赖服务端任何域包
- 认证 Token 经握手 subprotocol 传递，与 transport 域的认证子协议对齐

## 性能与陷阱

- 重连退避必须带抖动（jitter），否则服务端重启瞬间被客户端齐射打爆
- 读循环单 goroutine；业务处理慢就在 msgCh 后面自己挂 worker，别阻塞读循环
  （会卡心跳 pong，被服务端误判掉线）

## 后端绑定

无直连服务端，不经任何中间件

# # Go WebSocket Client (go-wsc)

> `go-wsc` 是一个用于管理 WebSocket 连接的 Go 客户端库它封装了 WebSocket 连接的管理、消息发送和接收，并提供了灵活的配置选项以及回调函数，方便开发者在使用 WebSocket 时进行扩展和定制该库支持自动重连、消息缓冲和连接状态管理，旨在简化 WebSocket 的使用

[![stable](https://img.shields.io/badge/stable-stable-green.svg)](https://github.com/kamalyes/go-wsc)
[![license](https://img.shields.io/github/license/kamalyes/go-wsc)]()
[![download](https://img.shields.io/github/downloads/kamalyes/go-wsc/total)]()
[![release](https://img.shields.io/github/v/release/kamalyes/go-wsc)]()
[![commit](https://img.shields.io/github/last-commit/kamalyes/go-wsc)]()
[![issues](https://img.shields.io/github/issues/kamalyes/go-wsc)]()
[![pull](https://img.shields.io/github/issues-pr/kamalyes/go-wsc)]()
[![fork](https://img.shields.io/github/forks/kamalyes/go-wsc)]()
[![star](https://img.shields.io/github/stars/kamalyes/go-wsc)]()
[![go](https://img.shields.io/github/go-mod/go-version/kamalyes/go-wsc)]()
[![size](https://img.shields.io/github/repo-size/kamalyes/go-wsc)]()
[![contributors](https://img.shields.io/github/contributors/kamalyes/go-wsc)]()
[![codecov](https://codecov.io/gh/kamalyes/go-wsc/branch/master/graph/badge.svg)](https://codecov.io/gh/kamalyes/go-wsc)
[![Go Report Card](https://goreportcard.com/badge/github.com/kamalyes/go-wsc)](https://goreportcard.com/report/github.com/kamalyes/go-wsc)
[![Go Reference](https://pkg.go.dev/badge/github.com/kamalyes/go-wsc?status.svg)](https://pkg.go.dev/github.com/kamalyes/go-wsc?tab=doc)
[![Sourcegraph](https://sourcegraph.com/github.com/kamalyes/go-wsc/-/badge.svg)](https://sourcegraph.com/github.com/kamalyes/go-wsc?badge)

## 特性

- **多种消息类型支持**：支持文本 (`TextMessage`) 和二进制 (`BinaryMessage`) 消息的发送与接收
- **自动重连机制**：在连接断开时，自动重连，并支持自定义重连策略（如最小重连时间、最大重连时间和重连因子）
- **连接状态管理**：提供简单的方法检查连接是否处于活动状态
- **可配置的消息缓冲池**：用户可以配置消息缓冲池的大小以适应不同的使用场景
- **回调函数**：允许用户定义连接成功、连接错误、消息接收等事件的回调函数，以便处理业务逻辑
- **错误处理**：定义了一些常见的错误，方便用户进行错误处理

## 开始使用

建议需要 [Go](https://go.dev/) 版本 [1.20](https://go.dev/doc/devel/release#go1.20.0) 

### 获取

使用 [Go 的模块支持](https://go.dev/wiki/Modules#how-to-use-modules)，当您在代码中添加导入时，`go [build|run|test]` 将自动获取所需的依赖项：

```go
import "github.com/kamalyes/go-wsc"
```

或者，使用 `go get` 命令：

```sh
go get -u github.com/kamalyes/go-wsc
```

## 使用示例

以下是一个简单的使用示例，展示如何使用 `go-wsc` 库建立 WebSocket 连接并发送/接收消息：

```go
package main

import (
    "fmt"
    "github.com/kamalyes/go-wsc"
    "time"
)

func main() {
    // 创建一个新的 WebSocket 客户端
    client := wsc.New("ws://localhost:8080/ws")

    // 设置连接成功的回调
    client.OnConnected(func() {
        fmt.Println("连接成功！")
    })

    // 设置连接错误的回调
    client.OnConnectError(func(err error) {
        fmt.Println("连接错误:", err)
    })

    // 设置断开连接的回调
    client.OnDisconnected(func(err error) {
        fmt.Println("连接断开:", err)
    })

    // 设置接收到文本消息的回调
    client.OnTextMessageReceived(func(message string) {
        fmt.Println("接收到文本消息:", message)
    })

    // 设置发送文本消息成功的回调
    client.OnTextMessageSent(func(message string) {
        fmt.Println("发送文本消息成功:", message)
    })

    // 连接到 WebSocket 服务器
    client.Connect()

    // 发送一条文本消息
    err := client.SendTextMessage("Hello, WebSocket!")
    if err != nil {
        fmt.Println("发送消息错误:", err)
    }

    // 保持程序运行，以便接收消息
    time.Sleep(10 * time.Second)

    // 关闭连接
    client.Close()
}
```

### 配置

`go-wsc` 提供了多种配置选项，用户可以根据需要自定义客户端配置,可以使用 `SetConfig` 方法设置配置，以下是可配置的选项：

- **WriteWait**: 写超时（默认 10 秒），在发送消息时的最大等待时间
- **MaxMessageSize**: 最大消息长度（默认 512 字节），限制接收消息的最大大小
- **MinRecTime**: 最小重连时间（默认 2 秒），在连接失败后，重连的最小等待时间
- **MaxRecTime**: 最大重连时间（默认 60 秒），在连接失败后，重连的最大等待时间
- **RecFactor**: 重连因子（默认 1.5），用于计算下一次重连的等待时间
- **MessageBufferSize**: 消息缓冲池大小（默认 256），用于控制发送消息的缓冲区大小

```go
config := wsc.NewDefaultConfig().
    WithWriteWait(5 * time.Second).
    WithMaxMessageSize(1024).
    WithMinRecTime(1 * time.Second).
    WithMaxRecTime(30 * time.Second).
    WithRecFactor(2.0).
    WithMessageBufferSize(512)
client.SetConfig(config)
```

## 回调函数

`go-wsc` 提供了一系列回调函数，允许用户在特定事件发生时执行自定义逻辑,以下是可用的回调函数：

- **OnConnected**: 连接成功时的回调
- **OnConnectError**: 连接出错时的回调，参数为错误信息
- **OnDisconnected**: 连接断开时的回调，参数为错误信息
- **OnClose**: 连接关闭时的回调，参数为关闭代码和关闭文本
- **OnTextMessageSent**: 发送文本消息成功时的回调，参数为发送的消息
- **OnBinaryMessageSent**: 发送二进制消息成功时的回调，参数为发送的数据
- **OnSentError**: 发送消息出错时的回调，参数为错误信息
- **OnPingReceived**: 接收到 Ping 消息时的回调，参数为应用数据
- **OnPongReceived**: 接收到 Pong 消息时的回调，参数为应用数据
- **OnTextMessageReceived**: 接收到文本消息时的回调，参数为接收到的消息
- **OnBinaryMessageReceived**: 接收到二进制消息时的回调，参数为接收到的数据

## 错误处理

在使用 `go-wsc` 时，您可能会遇到以下错误：

- `ErrClose`：连接已关闭
- `ErrBufferFull`：消息缓冲区已满

您可以通过检查返回的错误来处理这些情况

## 贡献

欢迎对 `go-wsc` 提出建议或贡献代码！请遵循以下步骤：

1. Fork 该项目
2. 创建您的特性分支 (`git checkout -b feature/yourfeature`)
3. 提交您的更改 (`git commit -m 'Add some feature'`)
4. 推送到分支 (`git push origin feature/yourfeature`)
5. 创建一个新的 Pull Request

## 许可证

该项目使用 MIT 许可证，详见 [LICENSE](LICENSE) 文件
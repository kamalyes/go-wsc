# Go WebSocket Client (go-wsc)
Chinese Documentation - [中文文档](./README-ZH.md)

> `go-wsc` is a Go client library for managing WebSocket connections. It encapsulates the management of WebSocket connections, message sending and receiving, and provides flexible configuration options and callback functions, making it easier for developers to extend and customize their WebSocket usage. This library supports automatic reconnection, message buffering, and connection state management, aiming to simplify the use of WebSocket.

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

## Features

- **Support for Multiple Message Types**: Supports sending and receiving text (`TextMessage`) and binary (`BinaryMessage`) messages.
- **Automatic Reconnection Mechanism**: Automatically reconnects when the connection is lost, with customizable reconnection strategies (such as minimum reconnection time, maximum reconnection time, and reconnection factor).
- **Connection State Management**: Provides simple methods to check if the connection is active.
- **Configurable Message Buffer Pool**: Users can configure the size of the message buffer pool to suit different usage scenarios.
- **Callback Functions**: Allows users to define callback functions for events such as successful connection, connection errors, and message reception, facilitating business logic handling.
- **Error Handling**: Defines common errors to help users manage error handling effectively.

## Getting Started

It is recommended to use [Go](https://go.dev/) version [1.20](https://go.dev/doc/devel/release#go1.20.0).

### Installation

With [Go's module support](https://go.dev/wiki/Modules#how-to-use-modules), when you add an import in your code, `go [build|run|test]` will automatically fetch the required dependencies:

```go
import "github.com/kamalyes/go-wsc"
```

Alternatively, use the `go get` command:

```sh
go get -u github.com/kamalyes/go-wsc
```

## Usage Example

Here is a simple usage example demonstrating how to establish a WebSocket connection and send/receive messages using the `go-wsc` library:

```go
package main

import (
    "fmt"
    "github.com/kamalyes/go-wsc"
    "time"
)

func main() {
    // Create a new WebSocket client
    client := wsc.New("ws://localhost:8080/ws")

    // Set the callback for successful connection
    client.OnConnected(func() {
        fmt.Println("Connection successful!")
    })

    // Set the callback for connection errors
    client.OnConnectError(func(err error) {
        fmt.Println("Connection error:", err)
    })

    // Set the callback for disconnection
    client.OnDisconnected(func(err error) {
        fmt.Println("Connection disconnected:", err)
    })

    // Set the callback for receiving text messages
    client.OnTextMessageReceived(func(message string) {
        fmt.Println("Received text message:", message)
    })

    // Set the callback for successful text message sending
    client.OnTextMessageSent(func(message string) {
        fmt.Println("Successfully sent text message:", message)
    })

    // Connect to the WebSocket server
    client.Connect()

    // Send a text message
    err := client.SendTextMessage("Hello, WebSocket!")
    if err != nil {
        fmt.Println("Error sending message:", err)
    }

    // Keep the program running to receive messages
    time.Sleep(10 * time.Second)

    // Close the connection
    client.Close()
}
```

### Configuration

`go-wsc` provides various configuration options that users can customize according to their needs. You can set the configuration using the `SetConfig` method. Here are the configurable options:

- **WriteWait**: Write timeout (default 10 seconds), the maximum wait time when sending messages.
- **MaxMessageSize**: Maximum message length (default 512 bytes), limits the maximum size of received messages.
- **MinRecTime**: Minimum reconnection time (default 2 seconds), the minimum wait time for reconnection after a connection failure.
- **MaxRecTime**: Maximum reconnection time (default 60 seconds), the maximum wait time for reconnection after a connection failure.
- **RecFactor**: Reconnection factor (default 1.5), used to calculate the wait time for the next reconnection.
- **MessageBufferSize**: Message buffer pool size (default 256), used to control the size of the message sending buffer.

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

## Callback Functions

`go-wsc` provides a series of callback functions that allow users to execute custom logic when specific events occur. Here are the available callback functions:

- **OnConnected**: Callback when the connection is successful.
- **OnConnectError**: Callback when there is a connection error, with the error information as a parameter.
- **OnDisconnected**: Callback when the connection is disconnected, with the error information as a parameter.
- **OnClose**: Callback when the connection is closed, with the close code and close text as parameters.
- **OnTextMessageSent**: Callback when a text message is successfully sent, with the sent message as a parameter.
- **OnBinaryMessageSent**: Callback when a binary message is successfully sent, with the sent data as a parameter.
- **OnSentError**: Callback when there is an error sending a message, with the error information as a parameter.
- **OnPingReceived**: Callback when a Ping message is received, with the application data as a parameter.
- **OnPongReceived**: Callback when a Pong message is received, with the application data as a parameter.
- **OnTextMessageReceived**: Callback when a text message is received, with the received message as a parameter.
- **OnBinaryMessageReceived**: Callback when a binary message is received, with the received data as a parameter.

## Error Handling

When using `go-wsc`, you may encounter the following errors:

- `ErrClose`: The connection has been closed.
- `ErrBufferFull`: The message buffer is full.

You can handle these situations by checking the returned errors.

## Contributing

Contributions and suggestions for `go-wsc` are welcome! Please follow these steps:

1. Fork the project
2. Create your feature branch (`git checkout -b feature/yourfeature`)
3. Commit your changes (`git commit -m 'Add some feature'`)
4. Push to the branch (`git push origin feature/yourfeature`)
5. Create a new Pull Request

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
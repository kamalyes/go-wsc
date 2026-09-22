/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 15:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 15:30:00
 * @FilePath: \go-wsc\spi\event_bus.go
 * @Description: 事件总线 SPI - EventBus 接口契约定义
 *
 * 发布/订阅契约，隔离具体消息中间件默认实现为 Redis PubSub
 * （cachex.PubSub，见 hub 包 SetPubSub 注入），后续可替换为
 * NATS / Kafka / 内存实现而不影响 events 层与 Hub 调用方
 *
 * 设计边界：本接口只承载「发布/订阅」语义，即事件总线的最小充分集
 * Redis 专属原语（分布式锁 EVAL、Pipeline 定向发布、原始客户端访问）
 * 不属于事件总线契约 —— 它们是 Redis 存储能力，由 spi 的其他接口或
 * 适配器内部消化，避免把接口绑死在某个中间件的 API 形状上
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import "context"

// EventHandler 事件处理函数
// 返回 error 时由实现决定是否重试（cachex 默认重试 3 次）
type EventHandler func(ctx context.Context, channel string, message string) error

// EventBus 事件总线接口
type EventBus interface {
	// Publish 发布消息到指定频道
	// message 为任意类型，实现负责序列化（cachex 对 string 透传，其余走 JSON）
	Publish(ctx context.Context, channel string, message any) error

	// Subscribe 订阅一个或多个频道
	// 返回的 Subscription 用于取消订阅；订阅失败时返回 error
	Subscribe(channels []string, handler EventHandler) (Subscription, error)

	// Close 关闭事件总线，释放订阅连接
	Close() error
}

// Subscription 订阅句柄
type Subscription interface {
	// Unsubscribe 取消订阅并停止消息循环
	Unsubscribe() error
}

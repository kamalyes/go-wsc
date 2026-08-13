/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:20:15
 * @FilePath: \go-wsc\events\common.go
 * @Description: 通用事件发布订阅方法
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package events

import (
	"context"
	"encoding/json"
	"time"
)

// PublishEvent 发布自定义事件（通用方法）
// 参数：
//   - ctx: 上下文（透传 trace_id）
//   - p: Publisher 发布器
//   - eventType: 事件类型（建议使用命名空间，如 "app.user.created"）
//   - data: 事件数据（任意类型，会自动序列化为JSON）
func PublishEvent(ctx context.Context, p Publisher, eventType string, data interface{}) error {
	pubsub := p.GetPubSub()
	if pubsub == nil {
		return ErrPubSubNotSet
	}

	pubCtx, cancel := context.WithTimeout(ctx, time.Second*5)
	defer cancel()

	if err := pubsub.Publish(pubCtx, eventType, data); err != nil {
		// 区分上下文取消和其他错误
		if pubCtx.Err() == context.Canceled || ctx.Err() != nil {
			p.GetLogger().DebugContextKV(ctx, "发布自定义事件被取消（Hub可能正在关闭）",
				"event_type", eventType,
			)
		} else {
			p.GetLogger().WarnContextKV(ctx, "发布自定义事件失败",
				"event_type", eventType,
				"error", err,
			)
		}
		return err
	}

	p.GetLogger().DebugContextKV(ctx, "📢 发布自定义事件",
		"event_type", eventType,
	)
	return nil
}

// SubscribeEvent 订阅自定义事件（通用方法）
// 参数：
//   - ctx: 上下文（透传 trace_id）
//   - p: Publisher 发布器
//   - eventTypes: 要订阅的事件类型列表
//   - handler: 事件处理函数，接收 (context, channel, message) 参数
//
// 返回：
//   - unsubscribe: 取消订阅函数
//   - error: 订阅失败时返回错误
//
// 使用示例：
//
//	unsubscribe, err := SubscribeEvent(ctx, publisher, []string{"app.user.created"}, func(ctx context.Context, channel string, message string) error {
//	    var event MyCustomEvent
//	    json.Unmarshal([]byte(message), &event)
//	    处理事件...
//	    return nil
//	})
//	if err != nil { return err }
//	defer unsubscribe() // 需要时取消订阅
func SubscribeEvent(ctx context.Context, p Publisher, eventTypes []string, handler func(ctx context.Context, channel string, message string) error) (func() error, error) {
	pubsub := p.GetPubSub()
	if pubsub == nil {
		return nil, ErrPubSubNotSet
	}

	p.GetLogger().InfoContextKV(ctx, "📡 订阅自定义事件", "event_types", eventTypes)

	subscriber, err := pubsub.Subscribe(eventTypes, handler)
	if err != nil {
		return nil, err
	}

	return func() error {
		return subscriber.Unsubscribe()
	}, nil
}

// SubscribeEventTyped 订阅自定义事件（类型安全版本，泛型函数）
// 参数：
//   - ctx: 上下文（透传 trace_id）
//   - p: Publisher 发布器
//   - eventTypes: 要订阅的事件类型列表
//   - handler: 类型安全的事件处理函数
//
// 返回：
//   - unsubscribe: 取消订阅函数
//   - error: 订阅失败时返回错误
//
// 使用示例：
//
//	type MyEvent struct { Name string `json:"name"` }
//	unsubscribe, err := SubscribeEventTyped[MyEvent](ctx, publisher, []string{"my.event"}, func(event *MyEvent) error {
//	    log.Printf("收到事件: %s", event.Name)
//	    return nil
//	})
//	if err != nil { return err }
//	defer unsubscribe() // 需要时取消订阅
func SubscribeEventTyped[T any](ctx context.Context, p Publisher, eventTypes []string, handler func(event *T) error) (func() error, error) {
	pubsub := p.GetPubSub()
	if pubsub == nil {
		return nil, ErrPubSubNotSet
	}

	p.GetLogger().InfoContextKV(ctx, "📡 订阅自定义事件（类型安全）", "event_types", eventTypes)

	subscriber, err := pubsub.Subscribe(
		eventTypes,
		func(subCtx context.Context, channel string, message string) error {
			var event T
			if err := json.Unmarshal([]byte(message), &event); err != nil {
				p.GetLogger().WarnContextKV(subCtx, "事件反序列化失败",
					"channel", channel,
					"error", err,
					"message", message,
				)
				return err
			}
			return handler(&event)
		},
	)
	if err != nil {
		return nil, err
	}

	return func() error {
		return subscriber.Unsubscribe()
	}, nil
}

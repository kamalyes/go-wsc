/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:05:15
 * @FilePath: \go-wsc\events\helpers.go
 * @Description: 事件发布订阅辅助函数 - 消除重复代码
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/convert"
)

// publishEventHelper 通用的事件发布辅助函数
// 参数：
//   - p: Publisher 发布器
//   - eventType: 事件类型
//   - event: 事件对象（任意类型）
//   - logFields: 日志字段键值对（用于调试和错误日志）
func publishEventHelper(p Publisher, eventType string, event interface{}, logFields map[string]interface{}) {
	pubsub := p.GetPubSub()
	if pubsub == nil {
		p.GetLogger().DebugKV("PubSub未设置,跳过事件发布", "event", eventType)
		return
	}

	ctx, cancel := context.WithTimeout(p.GetContext(), time.Second*5)
	defer cancel()

	if err := pubsub.Publish(ctx, eventType, event); err != nil {
		// 区分上下文取消和其他错误
		if ctx.Err() == context.Canceled || p.GetContext().Err() != nil {
			baseFields := map[string]interface{}{"event": eventType, "data": event}
			p.GetLogger().DebugKV("发布事件被取消（Hub可能正在关闭）", convert.MergeMapToKVPairs(baseFields, logFields)...)
		} else {
			baseFields := map[string]interface{}{"event": eventType, "error": err, "data": event}
			p.GetLogger().WarnKV("发布事件失败", convert.MergeMapToKVPairs(baseFields, logFields)...)
		}
		return
	}

	// 直接传递 map，go-logger 原生支持，包含事件内容
	baseFields := map[string]interface{}{"event": eventType, "data": event}
	p.GetLogger().DebugKV("📢 发布事件", convert.MergeMapToKVPairs(baseFields, logFields)...)
}

// subscribeEventHelper 通用的事件订阅辅助函数（泛型版本）
// 参数：
//   - p: Publisher 发布器
//   - eventTypes: 事件类型列表
//   - handler: 事件处理函数（类型安全）
//   - eventName: 事件名称（用于日志）
//
// 返回：
//   - unsubscribe: 取消订阅函数
//   - error: 订阅失败时返回错误
func subscribeEventHelper[T any](p Publisher, eventTypes []string, handler func(*T) error, eventName string) (func() error, error) {
	pubsub := p.GetPubSub()
	if pubsub == nil {
		return nil, ErrPubSubNotSet
	}

	p.GetLogger().InfoKV("📡 订阅事件", "event", eventName, "event_types", eventTypes)

	subscriber, err := pubsub.Subscribe(
		eventTypes,
		func(ctx context.Context, channel string, message string) error {
			var event T
			if err := json.Unmarshal([]byte(message), &event); err != nil {
				p.GetLogger().WarnKV("事件反序列化失败",
					"event", eventName,
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

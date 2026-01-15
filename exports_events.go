/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 13:05:15
 * @FilePath: \go-wsc\exports_events.go
 * @Description: 导出事件发布和订阅函数
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"github.com/kamalyes/go-wsc/events"
)

// ==================== 用户状态事件 ====================

// PublishUserOnline 发布用户上线事件
var PublishUserOnline = events.PublishUserOnline

// PublishUserOffline 发布用户下线事件
var PublishUserOffline = events.PublishUserOffline

// SubscribeUserOnline 订阅用户上线事件
var SubscribeUserOnline = events.SubscribeUserOnline

// SubscribeUserOffline 订阅用户下线事件
var SubscribeUserOffline = events.SubscribeUserOffline

// ==================== 工单入队事件 ====================

// PublishTicketQueuePushed 发布工单入队事件
var PublishTicketQueuePushed = events.PublishTicketQueuePushed

// SubscribeTicketQueuePushed 订阅工单入队事件
var SubscribeTicketQueuePushed = events.SubscribeTicketQueuePushed

// ==================== 工单分配事件 ====================

// PublishTicketAssigned 发布工单分配成功事件
var PublishTicketAssigned = events.PublishTicketAssigned

// SubscribeTicketAssigned 订阅工单分配成功事件
var SubscribeTicketAssigned = events.SubscribeTicketAssigned

// PublishTicketAssignmentFailed 发布工单分配失败事件
var PublishTicketAssignmentFailed = events.PublishTicketAssignmentFailed

// SubscribeTicketAssignmentFailed 订阅工单分配失败事件
var SubscribeTicketAssignmentFailed = events.SubscribeTicketAssignmentFailed

// ==================== 通用事件发布订阅 ====================

// PublishEvent 发布自定义事件
var PublishEvent = events.PublishEvent

// SubscribeEvent 订阅自定义事件
var SubscribeEvent = events.SubscribeEvent

// Publisher 事件发布器接口别名
type Publisher = events.Publisher

// SubscribeEventTyped 订阅自定义事件（类型安全版本，泛型函数）
// 由于是泛型函数，需要在调用时指定类型参数
// 使用示例: wsc.SubscribeEventTyped[MyEventType](publisher, eventTypes, handler)
func SubscribeEventTyped[T any](p Publisher, eventTypes []string, handler func(*T) error) (func() error, error) {
	return events.SubscribeEventTyped(p, eventTypes, handler)
}

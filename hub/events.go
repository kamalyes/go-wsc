/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:15:15
 * @FilePath: \go-wsc\hub\events_adapter.go
 * @Description: Hub 事件发布订阅适配器 - 对外暴露的事件接口
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"github.com/kamalyes/go-wsc/events"
	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 类型别名导出 - 让外部可以直接使用事件相关类型
// ============================================================================

type (
	UserStatusEvent         = models.UserStatusEvent
	TicketQueueEvent        = models.TicketQueueEvent
	UserStatusEventHandler  = models.UserStatusEventHandler
	TicketQueueEventHandler = models.TicketQueueEventHandler
	EventStatus             = models.EventStatus
)

// 事件类型常量
const (
	EventUserOnline        = models.EventUserOnline
	EventUserOffline       = models.EventUserOffline
	EventTicketQueuePushed = models.EventTicketQueuePushed
	EventTypeOnline        = models.EventTypeOnline
	EventTypeOffline       = models.EventTypeOffline
)

// ============================================================================
// 用户状态事件发布方法
// ============================================================================

// PublishUserOnline 发布用户上线事件
// 注意：此方法会在 handleRegister 中自动调用，通常不需要手动调用
func (h *Hub) PublishUserOnline(userID string, userType UserType, clientID string) {
	events.PublishUserOnline(h, userID, userType, clientID)
}

// PublishUserOffline 发布用户下线事件
// 注意：此方法会在 handleUnregister 中自动调用，通常不需要手动调用
func (h *Hub) PublishUserOffline(userID string, userType UserType, clientID string) {
	events.PublishUserOffline(h, userID, userType, clientID)
}

// ============================================================================
// 工单事件发布方法
// ============================================================================

// PublishTicketQueuePushed 发布工单入队事件
// 外部系统可以调用此方法通知工单入队
func (h *Hub) PublishTicketQueuePushed(ticketID, userID, sessionID string, priority int) {
	events.PublishTicketQueuePushed(h, ticketID, userID, sessionID, priority)
}

// ============================================================================
// 事件订阅方法
// ============================================================================

// SubscribeUserOnline 订阅用户上线事件
// 返回取消订阅函数，调用后将停止接收该事件
//
// 示例：
//
//	unsubscribe, err := hub.SubscribeUserOnline(func(event *UserStatusEvent) error {
//	    log.Printf("用户上线: %s", event.UserID)
//	    return nil
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer unsubscribe() // 取消订阅
func (h *Hub) SubscribeUserOnline(handler UserStatusEventHandler) (func() error, error) {
	return events.SubscribeUserOnline(h, handler)
}

// SubscribeUserOffline 订阅用户下线事件
// 返回取消订阅函数，调用后将停止接收该事件
//
// 示例：
//
//	unsubscribe, err := hub.SubscribeUserOffline(func(event *UserStatusEvent) error {
//	    log.Printf("用户下线: %s", event.UserID)
//	    return nil
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer unsubscribe() // 取消订阅
func (h *Hub) SubscribeUserOffline(handler UserStatusEventHandler) (func() error, error) {
	return events.SubscribeUserOffline(h, handler)
}

// SubscribeTicketQueuePushed 订阅工单入队事件
// 返回取消订阅函数，调用后将停止接收该事件
//
// 示例：
//
//	unsubscribe, err := hub.SubscribeTicketQueuePushed(func(event *TicketQueueEvent) error {
//	    log.Printf("工单入队: %s", event.TicketID)
//	    处理工单分配逻辑
//	    return nil
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer unsubscribe() // 取消订阅
func (h *Hub) SubscribeTicketQueuePushed(handler TicketQueueEventHandler) (func() error, error) {
	return events.SubscribeTicketQueuePushed(h, handler)
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 09:57:05
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:17:55
 * @FilePath: \go-wsc\models\event.go
 * @Description:
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"time"
)

// 事件类型常量
const (
	// EventUserOnline 用户上线事件（包括客服、客户、访客等所有用户）
	EventUserOnline = "user.online"
	// EventUserOffline 用户下线事件
	EventUserOffline = "user.offline"

	// EventTicketQueuePushed 工单入队事件（由外部系统发布）
	EventTicketQueuePushed = "ticket.queue.pushed"

	// EventTicketAssigned 工单分配事件（工单已分配给客服）
	EventTicketAssigned = "ticket.assigned"
	// EventTicketAssignmentFailed 工单分配失败事件
	EventTicketAssignmentFailed = "ticket.assignment.failed"
)

// EventStatus 事件状态类型
type EventStatus string

const (
	// EventTypeOnline 上线状态
	EventTypeOnline EventStatus = "online"
	// EventTypeOffline 下线状态
	EventTypeOffline EventStatus = "offline"
)

// UserStatusEvent 用户状态事件（统一的上下线事件）
type UserStatusEvent struct {
	UserID    string      `json:"user_id"`
	UserType  UserType    `json:"user_type"`
	EventType EventStatus `json:"event_type"`
	Timestamp time.Time   `json:"timestamp"`
	NodeID    string      `json:"node_id"`
	ClientID  string      `json:"client_id,omitempty"` // 可选：客户端ID
}

// TicketQueueEvent 工单入队事件
type TicketQueueEvent struct {
	TicketID  string    `json:"ticket_id"`
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	Priority  int       `json:"priority"`
	Timestamp time.Time `json:"timestamp"`
	NodeID    string    `json:"node_id"`
}

// TicketAssignedEvent 工单分配事件
type TicketAssignedEvent struct {
	TicketID   string    `json:"ticket_id"`
	UserID     string    `json:"user_id"`
	AgentID    string    `json:"agent_id"`
	SessionID  string    `json:"session_id"`
	Priority   int       `json:"priority"`
	AssignedAt time.Time `json:"assigned_at"`
	NodeID     string    `json:"node_id"`
}

// TicketAssignmentFailedEvent 工单分配失败事件
type TicketAssignmentFailedEvent struct {
	TicketID  string    `json:"ticket_id"`
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
	NodeID    string    `json:"node_id"`
}

// UserStatusEventHandler 用户状态事件处理器
type UserStatusEventHandler func(event *UserStatusEvent) error

// TicketQueueEventHandler 工单入队事件处理器
type TicketQueueEventHandler func(event *TicketQueueEvent) error

// TicketAssignedEventHandler 工单分配事件处理器
type TicketAssignedEventHandler func(event *TicketAssignedEvent) error

// TicketAssignmentFailedEventHandler 工单分配失败事件处理器
type TicketAssignmentFailedEventHandler func(event *TicketAssignmentFailedEvent) error

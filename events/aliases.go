/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 09:58:55
 * @FilePath: \go-wsc\events\aliases.go
 * @Description: 事件类型定义
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package events

import (
	"github.com/kamalyes/go-wsc/models"
)

// 错误类型别名（从 models 包导入）
type ErrorType = models.ErrorType

const (
	ErrTypePubSubNotSet           = models.ErrTypePubSubNotSet
	ErrTypePubSubPublishFailed    = models.ErrTypePubSubPublishFailed
	ErrTypeEventSerializeFailed   = models.ErrTypeEventSerializeFailed
	ErrTypeEventDeserializeFailed = models.ErrTypeEventDeserializeFailed
)

// 错误变量别名（从 models 包导入）
var (
	ErrPubSubNotSet           = models.ErrPubSubNotSet
	ErrPubSubPublishFailed    = models.ErrPubSubPublishFailed
	ErrEventSerializeFailed   = models.ErrEventSerializeFailed
	ErrEventDeserializeFailed = models.ErrEventDeserializeFailed
)

type UserType = models.UserType

// 事件类型常量
const (
	// EventUserOnline 用户上线事件（包括客服、客户、访客等所有用户）
	EventUserOnline = models.EventUserOnline
	// EventUserOffline 用户下线事件
	EventUserOffline = models.EventUserOffline

	// EventTicketQueuePushed 工单入队事件（由外部系统发布）
	EventTicketQueuePushed = models.EventTicketQueuePushed

	// EventTicketAssigned 工单分配事件（工单已分配给客服）
	EventTicketAssigned = models.EventTicketAssigned

	// EventTicketAssignmentFailed 工单分配失败事件
	EventTicketAssignmentFailed = models.EventTicketAssignmentFailed
)

// EventStatus 事件状态类型
type EventStatus = models.EventStatus

const (
	// EventTypeOnline 上线状态
	EventTypeOnline EventStatus = models.EventTypeOnline
	// EventTypeOffline 下线状态
	EventTypeOffline EventStatus = models.EventTypeOffline
)

// UserStatusEvent 用户状态事件（统一的上下线事件）
type UserStatusEvent = models.UserStatusEvent

// TicketQueueEvent 工单入队事件
type TicketQueueEvent = models.TicketQueueEvent

// TicketAssignedEvent 工单分配事件
type TicketAssignedEvent = models.TicketAssignedEvent

// TicketAssignmentFailedEvent 工单分配失败事件
type TicketAssignmentFailedEvent = models.TicketAssignmentFailedEvent

// UserStatusEventHandler 用户状态事件处理器
type UserStatusEventHandler = models.UserStatusEventHandler

// TicketQueueEventHandler 工单入队事件处理器
type TicketQueueEventHandler = models.TicketQueueEventHandler

// TicketAssignedEventHandler 工单分配事件处理器
type TicketAssignedEventHandler = models.TicketAssignedEventHandler

// TicketAssignmentFailedEventHandler 工单分配失败事件处理器
type TicketAssignmentFailedEventHandler = models.TicketAssignmentFailedEventHandler

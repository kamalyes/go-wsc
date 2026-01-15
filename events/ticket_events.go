/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:00:00
 * @FilePath: \go-wsc\events\ticket_events.go
 * @Description: 工单相关事件发布和订阅
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package events

import (
	"time"
)

// PublishTicketQueuePushed 发布工单入队事件
func PublishTicketQueuePushed(p Publisher, ticketID, userID, sessionID string, priority int) {
	event := TicketQueueEvent{
		TicketID:  ticketID,
		UserID:    userID,
		SessionID: sessionID,
		Priority:  priority,
		Timestamp: time.Now(),
		NodeID:    p.GetNodeID(),
	}

	publishEventHelper(p, EventTicketQueuePushed, event, map[string]interface{}{
		"ticket_id":  ticketID,
		"user_id":    userID,
		"session_id": sessionID,
		"priority":   priority,
	})
}

// SubscribeTicketQueuePushed 订阅工单入队事件（支持多个callback注册）
// 返回取消订阅函数，调用后将停止接收该事件
func SubscribeTicketQueuePushed(p Publisher, handler TicketQueueEventHandler) (func() error, error) {
	return subscribeEventHelper(p, []string{EventTicketQueuePushed}, handler, "工单入队事件")
}

// PublishTicketAssigned 发布工单分配成功事件
func PublishTicketAssigned(p Publisher, ticketID, userID, agentID, sessionID string, priority int) {
	event := TicketAssignedEvent{
		TicketID:   ticketID,
		UserID:     userID,
		AgentID:    agentID,
		SessionID:  sessionID,
		Priority:   priority,
		AssignedAt: time.Now(),
		NodeID:     p.GetNodeID(),
	}

	publishEventHelper(p, EventTicketAssigned, event, map[string]interface{}{
		"ticket_id":  ticketID,
		"user_id":    userID,
		"agent_id":   agentID,
		"session_id": sessionID,
		"priority":   priority,
	})
}

// SubscribeTicketAssigned 订阅工单分配成功事件
// 返回取消订阅函数，调用后将停止接收该事件
func SubscribeTicketAssigned(p Publisher, handler TicketAssignedEventHandler) (func() error, error) {
	return subscribeEventHelper(p, []string{EventTicketAssigned}, handler, "工单分配事件")
}

// PublishTicketAssignmentFailed 发布工单分配失败事件
func PublishTicketAssignmentFailed(p Publisher, ticketID, userID, sessionID, reason string) {
	event := TicketAssignmentFailedEvent{
		TicketID:  ticketID,
		UserID:    userID,
		SessionID: sessionID,
		Reason:    reason,
		Timestamp: time.Now(),
		NodeID:    p.GetNodeID(),
	}

	publishEventHelper(p, EventTicketAssignmentFailed, event, map[string]interface{}{
		"ticket_id":  ticketID,
		"user_id":    userID,
		"session_id": sessionID,
		"reason":     reason,
	})
}

// SubscribeTicketAssignmentFailed 订阅工单分配失败事件
// 返回取消订阅函数，调用后将停止接收该事件
func SubscribeTicketAssignmentFailed(p Publisher, handler TicketAssignmentFailedEventHandler) (func() error, error) {
	return subscribeEventHelper(p, []string{EventTicketAssignmentFailed}, handler, "工单分配失败事件")
}

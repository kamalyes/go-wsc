/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-13 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-13 10:35:36
 * @FilePath: \go-wsc\events\user_events.go
 * @Description: 用户状态事件发布和订阅
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package events

import (
	"time"
)

// PublishUserOnline 发布用户上线事件（支持所有用户类型）
func PublishUserOnline(p Publisher, userID string, userType UserType, clientID string) {
	event := UserStatusEvent{
		UserID:    userID,
		UserType:  userType,
		EventType: EventTypeOnline,
		Timestamp: time.Now(),
		NodeID:    p.GetNodeID(),
		ClientID:  clientID,
	}

	publishEventHelper(p, EventUserOnline, event, map[string]interface{}{
		"user_id":   userID,
		"user_type": userType,
		"client_id": clientID,
	})
}

// PublishUserOffline 发布用户下线事件（支持所有用户类型）
func PublishUserOffline(p Publisher, userID string, userType UserType, clientID string) {
	event := UserStatusEvent{
		UserID:    userID,
		UserType:  userType,
		EventType: EventTypeOffline,
		Timestamp: time.Now(),
		NodeID:    p.GetNodeID(),
		ClientID:  clientID,
	}

	publishEventHelper(p, EventUserOffline, event, map[string]interface{}{
		"user_id":   userID,
		"user_type": userType,
		"client_id": clientID,
	})
}

// SubscribeUserOnline 订阅用户上线事件（支持多个callback注册）
// 返回取消订阅函数，调用后将停止接收该事件
func SubscribeUserOnline(p Publisher, handler UserStatusEventHandler) (func() error, error) {
	return subscribeEventHelper(p, []string{EventUserOnline}, handler, "用户上线事件")
}

// SubscribeUserOffline 订阅用户下线事件（支持多个callback注册）
// 返回取消订阅函数，调用后将停止接收该事件
func SubscribeUserOffline(p Publisher, handler UserStatusEventHandler) (func() error, error) {
	return subscribeEventHelper(p, []string{EventUserOffline}, handler, "用户下线事件")
}

// SubscribeAllUserEvents 订阅所有用户事件（上线+下线）
// 返回取消订阅函数，调用后将停止接收所有用户事件
func SubscribeAllUserEvents(p Publisher, handler UserStatusEventHandler) (func() error, error) {
	return subscribeEventHelper(p, []string{EventUserOnline, EventUserOffline}, handler, "所有用户事件")
}

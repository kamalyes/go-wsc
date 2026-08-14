/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-03 22:15:33
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-03 22:15:33
 * @FilePath: \go-wsc\models\observer.go
 * @Description: 观察者相关数据模型
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import "time"

// ObserverManagerStats 观察者管理器统计信息
type ObserverManagerStats struct {
	TotalObservers      int                                `json:"total_observers"`      // 观察者用户总数
	TotalDevices        int                                `json:"total_devices"`        // 观察者设备总数
	TotalNotifications  int64                              `json:"total_notifications"`  // 总通知次数
	FailedNotifications int64                              `json:"failed_notifications"` // 失败通知次数
	DroppedMessages     int64                              `json:"dropped_messages"`     // 丢弃消息数（缓冲区满）
	NamespaceStats      map[string]*NamespaceObserverStats `json:"namespace_stats"`      // 按命名空间分组的观察者统计
}

// NamespaceObserverStats 命名空间级观察者统计
type NamespaceObserverStats struct {
	Namespace     string               `json:"namespace"`      // 命名空间
	TotalUsers    int                  `json:"total_users"`    // 该命名空间下观察者用户数
	TotalDevices  int                  `json:"total_devices"`  // 该命名空间下观察者设备数
	GroupStats    []GroupObserverStats `json:"group_stats"`    // 按群组分组的观察者统计
	ObserverStats []*ObserverStats     `json:"observer_stats"` // 命名空间级（非群组级）观察者详细统计
}

// GroupObserverStats 群组级观察者统计
type GroupObserverStats struct {
	GroupID       string           `json:"group_id"`       // 群组ID
	TotalUsers    int              `json:"total_users"`    // 该群组下观察者用户数
	TotalDevices  int              `json:"total_devices"`  // 该群组下观察者设备数
	ObserverStats []*ObserverStats `json:"observer_stats"` // 群组级观察者详细统计
}

// ObserverStats 观察者统计信息
type ObserverStats struct {
	ObserverID  string    `json:"observer_id"`
	ClientID    string    `json:"client_id"`
	Namespace   string    `json:"namespace"`          // 命名空间
	GroupID     string    `json:"group_id,omitempty"` // 群组ID（空=命名空间级观察者）
	ConnectedAt time.Time `json:"connected_at"`
	BufferSize  int       `json:"buffer_size"`
	BufferUsage int       `json:"buffer_usage"`
	IsConnected bool      `json:"is_connected"`
	UserType    string    `json:"user_type"`
	ClientType  string    `json:"client_type"`
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-31 00:15:57
 * @FilePath: \go-wsc\models\types.go
 * @Description: 基础类型定义
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import (
	"time"
)

// IDGenerator ID生成器接口
// 用于生成消息ID、请求ID等唯一标识符
type IDGenerator interface {
	GenerateTraceID() string
	GenerateSpanID() string
	GenerateRequestID() string
	GenerateCorrelationID() string
}

// HubStats Hub统计信息结构体
type HubStats struct {
	// 连接统计
	TotalClients     int `json:"total_clients"`     // 总客户端数
	WebSocketClients int `json:"websocket_clients"` // WebSocket客户端数
	SSEClients       int `json:"sse_clients"`       // SSE客户端数
	AgentConnections int `json:"agent_connections"` // 座席连接数

	// 消息统计
	MessagesSent     int64 `json:"messages_sent"`     // 已发送消息数
	MessagesReceived int64 `json:"messages_received"` // 已接收消息数
	BroadcastsSent   int64 `json:"broadcasts_sent"`   // 已发送广播数
	QueuedMessages   int   `json:"queued_messages"`   // 排队消息数

	// 其他统计
	OnlineUsers int   `json:"online_users"` // 在线用户数
	Uptime      int64 `json:"uptime"`       // 运行时间(秒)
}

// DistributedMessage 分布式消息结构
type DistributedMessage struct {
	Type      OperationType          `json:"type"`      // 操作类型
	NodeID    string                 `json:"node_id"`   // 源节点ID
	TargetID  string                 `json:"target_id"` // 目标ID（用户ID、节点ID等）
	Data      map[string]interface{} `json:"data"`      // 消息数据
	Timestamp time.Time              `json:"timestamp"` // 时间戳
}

// SendAttempt 发送尝试记录
type SendAttempt struct {
	AttemptNumber int
	StartTime     time.Time
	Duration      time.Duration
	Error         error
	Success       bool
}

// SendResult 发送结果
type SendResult struct {
	Success       bool
	Attempts      []SendAttempt
	TotalRetries  int
	TotalDuration time.Duration
	FinalError    error
	DeliveredAt   time.Time
}

// NodeInfo 节点信息
type NodeInfo struct {
	ID          string     `json:"id"`
	IPAddress   string     `json:"ip_address"`
	Port        int        `json:"port"`
	Status      NodeStatus `json:"status"`
	LoadScore   float64    `json:"load_score"`
	LastSeen    time.Time  `json:"last_seen"`
	Connections int        `json:"connections"`
}

// KickUserResult 踢人结果
type KickUserResult struct {
	UserID            string
	Reason            string
	KickedConnections int
	NotificationSent  bool
	Success           bool
	Error             error
	KickedAt          time.Time
}

// HubHealthInfo Hub健康状态信息
type HubHealthInfo struct {
	Status           string `json:"status"`
	IsRunning        bool   `json:"is_running"`
	WebSocketCount   int    `json:"websocket_count"`
	SSECount         int    `json:"sse_count"`
	TotalConnections int    `json:"total_connections"`
	NodeID           string `json:"node_id"`
}

// BroadcastResult 广播发送结果
type BroadcastResult struct {
	Total      int              // 总用户数量
	Success    int              // 成功发送数量
	Offline    int              // 离线用户数量
	Failed     int              // 发送失败数量
	Errors     map[string]error // 错误详情 map[userID]error
	OfflineIDs []string         // 离线用户ID列表
	FailedIDs  []string         // 发送失败的用户ID列表
}

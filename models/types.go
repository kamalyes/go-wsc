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
	"context"
	"time"

	"github.com/kamalyes/go-logger"
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
	TotalClients     int64 `json:"total_clients"`     // 总客户端数
	WebSocketClients int64 `json:"websocket_clients"` // WebSocket客户端数
	SSEClients       int64 `json:"sse_clients"`       // SSE客户端数
	AgentConnections int64 `json:"agent_connections"` // 座席连接数

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
	Type          OperationType `json:"type"`                     // 操作类型
	NodeID        string        `json:"node_id"`                  // 源节点ID
	TargetID      string        `json:"target_id"`                // 目标ID（用户ID、节点ID等）
	TraceID       string        `json:"trace_id,omitempty"`       // 全链路追踪ID（从 ctx 自动注入，跨节点序列化携带）
	Message       *HubMessage   `json:"message"`                  // 消息数据（用于 send_message, broadcast, observer_notify）
	Reason        string        `json:"reason"`                   // 原因
	Timestamp     time.Time     `json:"timestamp"`                // 时间戳
	AppID         string        `json:"app_id,omitempty"`         // 应用ID（最上层隔离维度，路由信封携带，空=全局共享）
	Namespace     string        `json:"namespace,omitempty"`      // 命名空间ID（路由信封携带，空=全命名空间广播，非空=指定命名空间）
	GroupIDs      []string      `json:"group_ids,omitempty"`      // 群组ID列表（支持多群组，观察者可订阅多个组；空表示无群组操作）
	ExcludeSender bool          `json:"exclude_sender,omitempty"` // 是否排除发送者（跨节点群组广播 PubSub 兜底携带，与 gRPC BroadcastGroupRequest 对齐）
	SenderID      string        `json:"sender_id,omitempty"`      // 发送者ID（排除发送者时用，跨节点 PubSub 兜底场景）
}

// InjectContext 从 ctx 注入上下文信息到分布式消息（trace_id 等）
// 优先从 OTel span 提取 trace_id，fallback 到 ctx.Value(logger.ContextKeyTraceID)
// 已有 trace_id 时不覆盖（跨节点消息保留源 trace）
func (dm *DistributedMessage) InjectContext(ctx context.Context) *DistributedMessage {
	if dm.TraceID != "" {
		return dm // 已有则不覆盖
	}
	dm.TraceID = logger.ExtractTraceID(ctx)
	return dm
}

// ContextFrom 基于分布式消息的 trace_id 创建一个携带 trace 信息的 context
// 用于消息流转路径中恢复 ctx（如 PubSub 消费端、回调等场景）
func (dm *DistributedMessage) ContextFrom(parent context.Context) context.Context {
	if dm.TraceID == "" {
		return parent
	}
	return logger.ContextWithTraceID(parent, dm.TraceID)
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
	StoredOffline bool // 消息因用户离线而存储到离线队列（SendToGroup 据此分类在线/离线，避免预检查 N 次 Redis）
}

// NodeInfo 节点信息
type NodeInfo struct {
	ID          string     `json:"id"`
	IPAddress   string     `json:"ip_address"`
	Port        int        `json:"port"`
	Status      NodeStatus `json:"status"`
	LoadScore   float64    `json:"load_score"`
	LastSeen    time.Time  `json:"last_seen"`
	Connections int64      `json:"connections"`
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

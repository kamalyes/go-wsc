/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 16:02:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 16:02:00
 * @FilePath: \go-wsc\models\stats.go
 * @Description: Hub 统计与健康快照
 *
 * stats 域对外暴露的只读快照结构（GetStats / GetHubHealth 返回值）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

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

// HubHealthInfo Hub健康状态信息
type HubHealthInfo struct {
	Status           string `json:"status"`
	IsRunning        bool   `json:"is_running"`
	WebSocketCount   int    `json:"websocket_count"`
	SSECount         int    `json:"sse_count"`
	TotalConnections int    `json:"total_connections"`
	NodeID           string `json:"node_id"`
}

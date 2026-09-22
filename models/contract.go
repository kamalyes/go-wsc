/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 14:12:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 14:12:00
 * @FilePath: \go-wsc\models\contract.go
 * @Description: SPI 契约载荷类型 - 接口签名直接引用的纯数据结构
 *
 * 这些类型是 spi 包接口签名的一部分（如 MessageSink.QueryRecords 的过滤器、
 * HubStats.GetNodeStats 的返回值）。放在 models 而非 adapter，是为了让
 * spi 只依赖 models —— 接口层不反向依赖任何存储实现包，spi 的导入图保持稳定。
 *
 * 本文件只含纯数据，零外部依赖。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import "time"

// NodeStats 节点统计信息
type NodeStats struct {
	NodeID            string    `json:"node_id" redis:"-"`
	TotalConnections  int64     `json:"total_connections" redis:"total_connections"`
	ActiveConnections int64     `json:"active_connections" redis:"active_connections"`
	MessagesSent      int64     `json:"messages_sent" redis:"messages_sent"`
	MessagesReceived  int64     `json:"messages_received" redis:"messages_received"`
	BroadcastsSent    int64     `json:"broadcasts_sent" redis:"broadcasts_sent"`
	StartTime         int64     `json:"start_time" redis:"start_time"`
	LastHeartbeat     time.Time `json:"last_heartbeat" redis:"-"`
	Uptime            int64     `json:"uptime" redis:"-"` // 运行时间(秒),计算字段
}

// ClusterStats 集群统计信息
type ClusterStats struct {
	TotalNodes        int          `json:"total_nodes"`
	ActiveNodes       int          `json:"active_nodes"`
	TotalConnections  int64        `json:"total_connections"`
	ActiveConnections int64        `json:"active_connections"`
	MessagesSent      int64        `json:"messages_sent"`
	MessagesReceived  int64        `json:"messages_received"`
	BroadcastsSent    int64        `json:"broadcasts_sent"`
	NodesStats        []*NodeStats `json:"nodes_stats"`
	UpdateTime        time.Time    `json:"update_time"`
}

// MessageRecordFilter 消息记录查询过滤器
type MessageRecordFilter struct {
	// Status 按状态查询（可选）
	Status *MessageSendStatus
	// Sender 按发送者查询（可选）
	Sender string
	// Receiver 按接收者查询（可选）
	Receiver string
	// NodeIP 按节点IP查询（可选）
	NodeIP string
	// ClientIP 按客户端IP查询（可选）
	ClientIP string
	// Limit 查询数量限制
	Limit int
	// OrderDesc 是否降序排序（默认降序，false为升序）
	OrderDesc bool
}

// MessageRecordKey 消息记录复合键（MessageSink 定位/更新/认领的统一寻址维度）
//
// P2P 场景下同一 message_id 会为每个 receiver 各建一条记录（群组可靠投递
// per-member 落库），仅按 message_id 寻址会造成多 receiver 状态相互覆盖
// （如 A 的 success 覆盖 B 的 failed）。所有按记录粒度的状态操作
// （UpdateStatus/ClaimStaleSending/IncrementRetry/FindByMessageID）均以复合键精确定位。
// 广播类记录 Receiver 为空串，同样满足复合键等值匹配。
type MessageRecordKey struct {
	// MessageID 消息ID（全局唯一，群发场景同一消息的所有成员记录共享）
	MessageID string
	// Receiver 接收者ID（广播类记录为空串）
	Receiver string
}

// WorkloadInfo 负载信息
type WorkloadInfo struct {
	AgentID      string    `json:"agent_id"`    // 客服ID
	Workload     int64     `json:"workload"`    // 当前工作负载
	LastUpdateAt time.Time `json:"last_update"` // 最后更新时间
}

// HeartbeatUpdateEntry 心跳更新条目（由连接记录仓库批量消费）
type HeartbeatUpdateEntry struct {
	ConnectionID string
	PingTime     *time.Time
	PongTime     *time.Time
	PingMs       float64 // >0 时更新 average/max/min_ping_ms
}

// StatsIncrementEntry 统计递增条目（由连接质量仓库批量消费）
type StatsIncrementEntry struct {
	ConnectionID     string
	MessagesSent     int64
	MessagesReceived int64
	BytesSent        int64
	BytesReceived    int64
}

// ConnectionQueryOptions 连接查询选项
type ConnectionQueryOptions struct {
	UserID     string // 用户ID过滤
	NodeID     string // 节点ID过滤
	IsActive   *bool  // 是否活跃（nil表示不过滤）
	IsAbnormal *bool  // 是否异常（nil表示不过滤）
	ClientIP   string // 客户端IP过滤
	Limit      int    // 限制数量
	Offset     int    // 偏移量
	OrderBy    string // 排序字段（默认 connected_at DESC）
}

// ConnectionStats 连接统计信息
// 质量维度字段(TotalMessages*/TotalBytes*/AveragePingMs/AverageReconnectCount)保留兼容调用方
// 拆表后由本方法零填充，跨表补充由调用方按需 JOIN qualityRepo
type ConnectionStats struct {
	TotalConnections      int64   `json:"total_connections"`       // 总连接数
	ActiveConnections     int64   `json:"active_connections"`      // 活跃连接数
	AverageDuration       float64 `json:"average_duration"`        // 平均连接时长(秒)
	TotalMessagesSent     int64   `json:"total_messages_sent"`     // 总发送消息数（拆表后零填充，跨表补充由调用方）
	TotalMessagesReceived int64   `json:"total_messages_received"` // 总接收消息数（拆表后零填充）
	TotalBytesSent        int64   `json:"total_bytes_sent"`        // 总发送字节数（拆表后零填充）
	TotalBytesReceived    int64   `json:"total_bytes_received"`    // 总接收字节数（拆表后零填充）
	AveragePingMs         float64 `json:"average_ping_ms"`         // 平均Ping延迟（拆表后零填充）
	AbnormalRate          float64 `json:"abnormal_rate"`           // 异常断开率
	AverageReconnectCount float64 `json:"average_reconnect_count"` // 平均重连次数（拆表后零填充）
}

// UserConnectionStats 用户连接统计
// 质量维度字段(ReconnectCount/ErrorCount/MessagesSent/MessagesReceived/AveragePingMs/ConnectionQuality)
// 拆表后由本方法零填充，跨表补充由调用方按需从 qualityRepo 取
type UserConnectionStats struct {
	UserID            string     `json:"user_id"`
	IsActive          bool       `json:"is_active"`
	ConnectedAt       time.Time  `json:"connected_at"`
	DisconnectedAt    *time.Time `json:"disconnected_at,omitempty"`
	Duration          int64      `json:"duration"`
	ReconnectCount    int        `json:"reconnect_count"`    // 拆表后零填充
	ErrorCount        int        `json:"error_count"`        // 拆表后零填充
	MessagesSent      int64      `json:"messages_sent"`      // 拆表后零填充
	MessagesReceived  int64      `json:"messages_received"`  // 拆表后零填充
	AveragePingMs     float64    `json:"average_ping_ms"`    // 拆表后零填充
	ConnectionQuality float64    `json:"connection_quality"` // 拆表后零填充
}

// NodeConnectionStats 节点连接统计
// 质量维度字段(TotalMessages*/TotalBytes*/TotalErrors/ErrorRate/AveragePingMs/MaxPingMs/MinPingMs/
// TotalReconnects/AverageReconnectCount/ConnectionQuality)拆表后由本方法零填充，跨表补充由调用方按需从 qualityRepo 取
type NodeConnectionStats struct {
	NodeID                string  `json:"node_id"`                 // 节点ID
	NodeIP                string  `json:"node_ip"`                 // 节点IP
	NodePort              int     `json:"node_port"`               // 节点端口
	TotalConnections      int64   `json:"total_connections"`       // 总连接数
	ActiveConnections     int64   `json:"active_connections"`      // 活跃连接数
	DisconnectedCount     int64   `json:"disconnected_count"`      // 已断开连接数
	AbnormalCount         int64   `json:"abnormal_count"`          // 异常断开数
	AbnormalRate          float64 `json:"abnormal_rate"`           // 异常断开率(%)
	TotalMessagesSent     int64   `json:"total_messages_sent"`     // 拆表后零填充
	TotalMessagesReceived int64   `json:"total_messages_received"` // 拆表后零填充
	TotalBytesSent        int64   `json:"total_bytes_sent"`        // 拆表后零填充
	TotalBytesReceived    int64   `json:"total_bytes_received"`    // 拆表后零填充
	TotalErrors           int64   `json:"total_errors"`            // 拆表后零填充
	ErrorRate             float64 `json:"error_rate"`              // 拆表后零填充
	AveragePingMs         float64 `json:"average_ping_ms"`         // 拆表后零填充
	MaxPingMs             float64 `json:"max_ping_ms"`             // 拆表后零填充
	MinPingMs             float64 `json:"min_ping_ms"`             // 拆表后零填充
	AverageDuration       float64 `json:"average_duration"`        // 平均连接时长(秒)
	TotalReconnects       int64   `json:"total_reconnects"`        // 拆表后零填充
	AverageReconnectCount float64 `json:"average_reconnect_count"` // 拆表后零填充
	ConnectionQuality     float64 `json:"connection_quality"`      // 拆表后零填充
}

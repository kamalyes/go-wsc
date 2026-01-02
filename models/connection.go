/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\connection.go
 * @Description: WebSocket连接记录模型 - 用于持久化连接历史
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package models

import (
	"time"

	"github.com/kamalyes/go-sqlbuilder"
)

// ConnectionRecord WebSocket连接记录模型 - 详细记录每次连接的完整信息
type ConnectionRecord struct {
	// ========== 基础标识信息 ==========
	ID           uint64 `gorm:"primaryKey;autoIncrement;comment:自增主键" json:"id"`
	ConnectionID string `gorm:"column:connection_id;size:64;uniqueIndex;not null;comment:连接ID(唯一)" json:"connection_id"`
	UserID       string `gorm:"column:user_id;size:64;not null;index;comment:用户ID" json:"user_id"`

	// ========== 服务器节点信息 ==========
	NodeID   string `gorm:"column:node_id;size:100;index;comment:服务器节点ID" json:"node_id"`
	NodeIP   string `gorm:"column:node_ip;size:45;comment:服务器IP" json:"node_ip"`
	NodePort int    `gorm:"column:node_port;comment:服务器端口" json:"node_port"`

	// ========== 客户端信息 ==========
	ClientIP   string `gorm:"column:client_ip;size:45;index;comment:客户端IP地址(用于索引查询)" json:"client_ip"`
	ClientType string `gorm:"column:client_type;size:20;comment:客户端类型(web/mobile/desktop/sdk)" json:"client_type"`

	// ========== 连接协议信息 ==========
	Protocol string `gorm:"column:protocol;size:20;default:websocket;comment:协议类型(websocket/sse/http)" json:"protocol"`

	// ========== 连接时间信息 ==========
	ConnectedAt    time.Time  `gorm:"column:connected_at;index;not null;comment:连接建立时间" json:"connected_at"`
	DisconnectedAt *time.Time `gorm:"column:disconnected_at;index;comment:断开连接时间" json:"disconnected_at,omitempty"`
	Duration       int64      `gorm:"column:duration;comment:连接持续时长(秒)" json:"duration,omitempty"`
	LastPingAt     *time.Time `gorm:"column:last_ping_at;comment:最后心跳时间" json:"last_ping_at,omitempty"`
	LastPongAt     *time.Time `gorm:"column:last_pong_at;comment:最后Pong响应时间" json:"last_pong_at,omitempty"`

	// ========== 断开连接信息 ==========
	DisconnectReason  string `gorm:"column:disconnect_reason;size:50;comment:断开原因(normal/timeout/error/force_offline/network等)" json:"disconnect_reason,omitempty"`
	DisconnectCode    int    `gorm:"column:disconnect_code;comment:断开代码" json:"disconnect_code,omitempty"`
	DisconnectMessage string `gorm:"column:disconnect_message;type:text;comment:断开消息/错误信息" json:"disconnect_message,omitempty"`

	// ========== 连接质量与性能指标 ==========
	ReconnectCount   int     `gorm:"column:reconnect_count;default:0;comment:重连次数" json:"reconnect_count"`
	MessagesSent     int64   `gorm:"column:messages_sent;default:0;comment:发送消息总数" json:"messages_sent"`
	MessagesReceived int64   `gorm:"column:messages_received;default:0;comment:接收消息总数" json:"messages_received"`
	BytesSent        int64   `gorm:"column:bytes_sent;default:0;comment:发送字节数" json:"bytes_sent"`
	BytesReceived    int64   `gorm:"column:bytes_received;default:0;comment:接收字节数" json:"bytes_received"`
	AveragePingMs    float64 `gorm:"column:average_ping_ms;comment:平均Ping延迟(毫秒)" json:"average_ping_ms,omitempty"`
	MaxPingMs        float64 `gorm:"column:max_ping_ms;comment:最大Ping延迟(毫秒)" json:"max_ping_ms,omitempty"`
	MinPingMs        float64 `gorm:"column:min_ping_ms;comment:最小Ping延迟(毫秒)" json:"min_ping_ms,omitempty"`
	PacketLossRate   float64 `gorm:"column:packet_loss_rate;comment:丢包率(%)" json:"packet_loss_rate,omitempty"`

	// ========== 错误与异常信息 ==========
	ErrorCount  int        `gorm:"column:error_count;default:0;comment:错误次数" json:"error_count"`
	LastError   string     `gorm:"column:last_error;type:text;comment:最后错误信息" json:"last_error,omitempty"`
	LastErrorAt *time.Time `gorm:"column:last_error_at;comment:最后错误时间" json:"last_error_at,omitempty"`

	// ========== 状态标识 ==========
	IsActive        bool `gorm:"column:is_active;default:true;index;comment:是否活跃连接" json:"is_active"`
	IsForcedOffline bool `gorm:"column:is_forced_offline;default:false;comment:是否被强制下线" json:"is_forced_offline"`
	IsAbnormal      bool `gorm:"column:is_abnormal;default:false;index;comment:是否异常断开" json:"is_abnormal"`

	// ========== 元数据 ==========
	Metadata sqlbuilder.MapAny `gorm:"column:metadata;type:json;comment:请求元数据JSON(包含所有HTTP头信息)" json:"metadata,omitempty"`

	// ========== 系统字段 ==========
	CreatedAt time.Time `gorm:"autoCreateTime;comment:记录创建时间" json:"created_at"`
	UpdatedAt time.Time `gorm:"autoUpdateTime;comment:记录更新时间" json:"updated_at"`
}

// TableName 指定表名
func (ConnectionRecord) TableName() string {
	return "wsc_connection_records"
}

// TableComment 表注释
func (ConnectionRecord) TableComment() string {
	return "WebSocket连接历史记录表-详细记录每次连接的完整信息用于审计和分析"
}

// ========== 辅助方法 ==========

// CalculateDuration 计算连接持续时长
func (c *ConnectionRecord) CalculateDuration() {
	if c.DisconnectedAt != nil {
		c.Duration = int64(c.DisconnectedAt.Sub(c.ConnectedAt).Seconds())
	}
}

// MarkDisconnected 标记为已断开
func (c *ConnectionRecord) MarkDisconnected(reason DisconnectReason, code int, message string) {
	now := time.Now()
	c.DisconnectedAt = &now
	c.DisconnectReason = string(reason)
	c.DisconnectCode = code
	c.DisconnectMessage = message
	c.CalculateDuration()

	// 判断是否异常断开
	c.IsAbnormal = reason != DisconnectReasonClientRequest &&
		reason != DisconnectReasonServerShutdown
}

// IncrementReconnect 增加重连次数
func (c *ConnectionRecord) IncrementReconnect() {
	c.ReconnectCount++
}

// AddError 添加错误记录
func (c *ConnectionRecord) AddError(err error) {
	if err != nil {
		c.ErrorCount++
		c.LastError = err.Error()
		now := time.Now()
		c.LastErrorAt = &now
	}
}

// UpdateMessageStats 更新消息统计
func (c *ConnectionRecord) UpdateMessageStats(sent, received int64) {
	c.MessagesSent += sent
	c.MessagesReceived += received
}

// UpdateBytesStats 更新字节统计
func (c *ConnectionRecord) UpdateBytesStats(sent, received int64) {
	c.BytesSent += sent
	c.BytesReceived += received
}

// UpdatePingStats 更新Ping延迟统计
func (c *ConnectionRecord) UpdatePingStats(pingMs float64) {
	// 更新平均值
	if c.AveragePingMs == 0 {
		c.AveragePingMs = pingMs
	} else {
		c.AveragePingMs = (c.AveragePingMs + pingMs) / 2
	}

	// 更新最大最小值
	if c.MaxPingMs == 0 || pingMs > c.MaxPingMs {
		c.MaxPingMs = pingMs
	}
	if c.MinPingMs == 0 || pingMs < c.MinPingMs {
		c.MinPingMs = pingMs
	}
}

// IsOnline 判断是否在线
func (c *ConnectionRecord) IsOnline() bool {
	return c.IsActive && c.DisconnectedAt == nil
}

// GetConnectionQuality 获取连接质量评分 (0-100)
func (c *ConnectionRecord) GetConnectionQuality() float64 {
	score := 100.0

	// 丢包率影响 (最多扣30分)
	if c.PacketLossRate > 0 {
		score -= c.PacketLossRate * 30
	}

	// 延迟影响 (最多扣30分)
	if c.AveragePingMs > 0 {
		if c.AveragePingMs > 500 {
			score -= 30
		} else if c.AveragePingMs > 200 {
			score -= 20
		} else if c.AveragePingMs > 100 {
			score -= 10
		}
	}

	// 错误率影响 (最多扣20分)
	totalMessages := c.MessagesSent + c.MessagesReceived
	if totalMessages > 0 {
		errorRate := float64(c.ErrorCount) / float64(totalMessages)
		score -= errorRate * 20
	}

	// 重连次数影响 (最多扣20分)
	if c.ReconnectCount > 0 {
		score -= float64(c.ReconnectCount) * 5
		if score < 0 {
			score = 0
		}
	}

	if score < 0 {
		score = 0
	}
	return score
}

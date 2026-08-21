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
// 设计说明：支持多设备登录，每个连接维护一条独立记录
type ConnectionRecord struct {
	// ========== 基础标识信息 ==========
	ID           uint64 `gorm:"primaryKey;autoIncrement;comment:自增主键" json:"id"`
	ConnectionID string `gorm:"column:connection_id;size:64;uniqueIndex;not null;comment:连接ID(唯一标识,支持多设备登录)" json:"connection_id"`
	UserID       string `gorm:"column:user_id;size:64;not null;index;comment:用户ID(同一用户可有多条记录)" json:"user_id"`

	// ========== 多租户/命名空间隔离 ==========
	// 与 Bitmap/ZSET 在线状态层分桶维度一致，支持按 app+namespace 过滤连接
	AppID     string `gorm:"column:app_id;size:64;index:idx_app_namespace;comment:应用ID(多租户隔离,与 Bitmap/ZSET 分桶一致)" json:"app_id"`
	Namespace string `gorm:"column:namespace;size:64;index:idx_app_namespace;comment:命名空间(与 Bitmap/ZSET 分桶一致)" json:"namespace"`

	// ========== 服务器节点信息 ==========
	NodeID   string `gorm:"column:node_id;size:100;index;comment:服务器节点ID" json:"node_id"`
	NodeIP   string `gorm:"column:node_ip;size:45;comment:服务器IP" json:"node_ip"`
	NodePort int    `gorm:"column:node_port;comment:服务器端口" json:"node_port"`

	// ========== 客户端信息 ==========
	ClientIP   string     `gorm:"column:client_ip;size:45;index;comment:客户端IP地址(用于索引查询)" json:"client_ip"`
	ClientType ClientType `gorm:"column:client_type;size:20;comment:客户端类型(web/mobile/desktop/sdk)" json:"client_type"`

	// ========== 连接协议信息 ==========
	Protocol ConnectionType `gorm:"column:protocol;size:20;default:websocket;comment:协议类型(websocket/sse/http)" json:"protocol"`

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

	// ========== 状态标识 ==========
	IsActive        bool `gorm:"column:is_active;index;comment:是否活跃连接" json:"is_active"`
	IsForcedOffline bool `gorm:"column:is_forced_offline;comment:是否被强制下线" json:"is_forced_offline"`
	IsAbnormal      bool `gorm:"column:is_abnormal;index;comment:是否异常断开" json:"is_abnormal"`

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
	return "WebSocket连接记录表-支持多设备登录-每个连接独立记录-用于审计和分析"
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

// IsOnline 判断是否在线
func (c *ConnectionRecord) IsOnline() bool {
	return c.IsActive && c.DisconnectedAt == nil
}

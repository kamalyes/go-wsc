/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-21
 * @FilePath: \go-wsc\types.go
 * @Description: WebSocket 系统类型定义
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"strings"
	"time"
)

// UserRole 用户角色类型
type UserRole string

const (
	UserRoleCustomer UserRole = "customer" // 客户
	UserRoleAgent    UserRole = "agent"    // 客服
	UserRoleAdmin    UserRole = "admin"    // 管理员
)

// String 实现Stringer接口
func (r UserRole) String() string {
	return string(r)
}

// IsValid 检查角色是否有效
func (r UserRole) IsValid() bool {
	switch r {
	case UserRoleCustomer, UserRoleAgent, UserRoleAdmin:
		return true
	default:
		return false
	}
}

// UserType 用户类型（与角色可能不同，提供更细分的分类）
type UserType string

const (
	UserTypeCustomer UserType = "customer" // 普通客户
	UserTypeAgent    UserType = "agent"    // 人工客服
	UserTypeAdmin    UserType = "admin"    // 系统管理员
	UserTypeBot      UserType = "bot"      // 机器人客服
	UserTypeVIP      UserType = "vip"      // VIP客户
)

// String 实现Stringer接口
func (t UserType) String() string {
	return string(t)
}

// IsValid 检查用户类型是否有效
func (t UserType) IsValid() bool {
	switch t {
	case UserTypeCustomer, UserTypeAgent, UserTypeAdmin, UserTypeBot, UserTypeVIP:
		return true
	default:
		return false
	}
}

// UserStatus 用户状态
type UserStatus string

const (
	UserStatusOnline    UserStatus = "online"    // 在线
	UserStatusOffline   UserStatus = "offline"   // 离线
	UserStatusBusy      UserStatus = "busy"      // 忙碌
	UserStatusAway      UserStatus = "away"      // 离开
	UserStatusInvisible UserStatus = "invisible" // 隐身
)

// String 实现Stringer接口
func (s UserStatus) String() string {
	return string(s)
}

// IsValid 检查用户状态是否有效
func (s UserStatus) IsValid() bool {
	switch s {
	case UserStatusOnline, UserStatusOffline, UserStatusBusy, UserStatusAway, UserStatusInvisible:
		return true
	default:
		return false
	}
}

// MessageType 消息类型
type MessageType string

const (
	MessageTypeText   MessageType = "text"   // 文本消息
	MessageTypeImage  MessageType = "image"  // 图片消息
	MessageTypeFile   MessageType = "file"   // 文件消息
	MessageTypeAudio  MessageType = "audio"  // 音频消息
	MessageTypeVideo  MessageType = "video"  // 视频消息
	MessageTypeSystem MessageType = "system" // 系统消息
	MessageTypeNotice MessageType = "notice" // 通知消息
	MessageTypeEvent  MessageType = "event"  // 事件消息
	MessageTypeAck    MessageType = "ack"    // ACK确认消息
)

// String 实现Stringer接口
func (t MessageType) String() string {
	return string(t)
}

// IsValid 检查消息类型是否有效
func (t MessageType) IsValid() bool {
	switch t {
	case MessageTypeText, MessageTypeImage, MessageTypeFile,
		MessageTypeAudio, MessageTypeVideo, MessageTypeSystem,
		MessageTypeNotice, MessageTypeEvent, MessageTypeAck:
		return true
	default:
		return false
	}
}

// MessageStatus 消息状态
type MessageStatus string

const (
	MessageStatusPending   MessageStatus = "pending"   // 待发送
	MessageStatusSent      MessageStatus = "sent"      // 已发送
	MessageStatusDelivered MessageStatus = "delivered" // 已送达
	MessageStatusRead      MessageStatus = "read"      // 已读
	MessageStatusFailed    MessageStatus = "failed"    // 发送失败
)

// String 实现Stringer接口
func (s MessageStatus) String() string {
	return string(s)
}

// IsValid 检查消息状态是否有效
func (s MessageStatus) IsValid() bool {
	switch s {
	case MessageStatusPending, MessageStatusSent, MessageStatusDelivered,
		MessageStatusRead, MessageStatusFailed:
		return true
	default:
		return false
	}
}

// TicketStatus 工单状态
type TicketStatus string

const (
	TicketStatusOpen       TicketStatus = "open"       // 打开
	TicketStatusPending    TicketStatus = "pending"    // 待处理
	TicketStatusProcessing TicketStatus = "processing" // 处理中
	TicketStatusResolved   TicketStatus = "resolved"   // 已解决
	TicketStatusClosed     TicketStatus = "closed"     // 已关闭
)

// String 实现Stringer接口
func (s TicketStatus) String() string {
	return string(s)
}

// IsValid 检查工单状态是否有效
func (s TicketStatus) IsValid() bool {
	switch s {
	case TicketStatusOpen, TicketStatusPending, TicketStatusProcessing,
		TicketStatusResolved, TicketStatusClosed:
		return true
	default:
		return false
	}
}

// TicketPriority 工单优先级
type TicketPriority string

const (
	TicketPriorityLow      TicketPriority = "low"      // 低优先级
	TicketPriorityNormal   TicketPriority = "normal"   // 普通优先级
	TicketPriorityHigh     TicketPriority = "high"     // 高优先级
	TicketPriorityUrgent   TicketPriority = "urgent"   // 紧急优先级
	TicketPriorityCritical TicketPriority = "critical" // 关键优先级
)

// String 实现Stringer接口
func (p TicketPriority) String() string {
	return string(p)
}

// IsValid 检查工单优先级是否有效
func (p TicketPriority) IsValid() bool {
	switch p {
	case TicketPriorityLow, TicketPriorityNormal, TicketPriorityHigh,
		TicketPriorityUrgent, TicketPriorityCritical:
		return true
	default:
		return false
	}
}

// NodeStatus 节点状态
type NodeStatus string

const (
	NodeStatusActive   NodeStatus = "active"   // 活跃
	NodeStatusInactive NodeStatus = "inactive" // 非活跃
	NodeStatusOffline  NodeStatus = "offline"  // 离线
)

// String 实现Stringer接口
func (s NodeStatus) String() string {
	return string(s)
}

// IsValid 检查节点状态是否有效
func (s NodeStatus) IsValid() bool {
	switch s {
	case NodeStatusActive, NodeStatusInactive, NodeStatusOffline:
		return true
	default:
		return false
	}
}

// ConnectionStatus 连接状态
type ConnectionStatus string

const (
	ConnectionStatusConnecting   ConnectionStatus = "connecting"   // 连接中
	ConnectionStatusConnected    ConnectionStatus = "connected"    // 已连接
	ConnectionStatusDisconnected ConnectionStatus = "disconnected" // 已断开
	ConnectionStatusReconnecting ConnectionStatus = "reconnecting" // 重连中
	ConnectionStatusError        ConnectionStatus = "error"        // 连接错误
)

// String 实现Stringer接口
func (s ConnectionStatus) String() string {
	return string(s)
}

// IsValid 检查连接状态是否有效
func (s ConnectionStatus) IsValid() bool {
	switch s {
	case ConnectionStatusConnecting, ConnectionStatusConnected,
		ConnectionStatusDisconnected, ConnectionStatusReconnecting,
		ConnectionStatusError:
		return true
	default:
		return false
	}
}

// OperationType 操作类型
type OperationType string

const (
	OperationTypeJoin      OperationType = "join"      // 加入
	OperationTypeLeave     OperationType = "leave"     // 离开
	OperationTypeMessage   OperationType = "message"   // 消息
	OperationTypeBroadcast OperationType = "broadcast" // 广播
	OperationTypeNotify    OperationType = "notify"    // 通知
	OperationTypeHeartbeat OperationType = "heartbeat" // 心跳
	OperationTypeAuth      OperationType = "auth"      // 认证
	OperationTypeSync      OperationType = "sync"      // 同步
)

// String 实现Stringer接口
func (o OperationType) String() string {
	return string(o)
}

// IsValid 检查操作类型是否有效
func (o OperationType) IsValid() bool {
	switch o {
	case OperationTypeJoin, OperationTypeLeave, OperationTypeMessage,
		OperationTypeBroadcast, OperationTypeNotify, OperationTypeHeartbeat,
		OperationTypeAuth, OperationTypeSync:
		return true
	default:
		return false
	}
}

// ClientType 客户端类型
type ClientType string

const (
	ClientTypeWeb     ClientType = "web"     // Web客户端
	ClientTypeMobile  ClientType = "mobile"  // 移动端
	ClientTypeDesktop ClientType = "desktop" // 桌面客户端
	ClientTypeAPI     ClientType = "api"     // API客户端
)

// String 实现Stringer接口
func (c ClientType) String() string {
	return string(c)
}

// IsValid 检查客户端类型是否有效
func (c ClientType) IsValid() bool {
	switch c {
	case ClientTypeWeb, ClientTypeMobile, ClientTypeDesktop, ClientTypeAPI:
		return true
	default:
		return false
	}
}

// Priority 优先级类型
type Priority string

const (
	PriorityLow      Priority = "low"      // 低优先级
	PriorityNormal   Priority = "normal"   // 普通优先级
	PriorityHigh     Priority = "high"     // 高优先级
	PriorityUrgent   Priority = "urgent"   // 紧急优先级
	PriorityCritical Priority = "critical" // 关键优先级
)

// String 实现Stringer接口
func (p Priority) String() string {
	return string(p)
}

// IsValid 检查优先级是否有效
func (p Priority) IsValid() bool {
	switch p {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent, PriorityCritical:
		return true
	default:
		return false
	}
}

// Department 部门类型
type Department string

const (
	DepartmentSales     Department = "sales"     // 销售部门
	DepartmentSupport   Department = "support"   // 技术支持
	DepartmentBilling   Department = "billing"   // 计费部门
	DepartmentGeneral   Department = "general"   // 综合部门
	DepartmentTechnical Department = "technical" // 技术部门
)

// String 实现Stringer接口
func (d Department) String() string {
	return string(d)
}

// IsValid 检查部门是否有效
func (d Department) IsValid() bool {
	switch d {
	case DepartmentSales, DepartmentSupport, DepartmentBilling, DepartmentGeneral, DepartmentTechnical:
		return true
	default:
		return false
	}
}

// Skill 技能标签类型
type Skill string

const (
	SkillTechnical  Skill = "technical" // 技术支持
	SkillSales      Skill = "sales"     // 销售
	SkillBilling    Skill = "billing"   // 计费
	SkillGeneral    Skill = "general"   // 一般客服
	SkillLanguageEN Skill = "english"   // 英语
	SkillLanguageZH Skill = "chinese"   // 中文
	SkillVIP        Skill = "vip"       // VIP客服
)

// String 实现Stringer接口
func (s Skill) String() string {
	return string(s)
}

// IsValid 检查技能是否有效
func (s Skill) IsValid() bool {
	switch s {
	case SkillTechnical, SkillSales, SkillBilling, SkillGeneral,
		SkillLanguageEN, SkillLanguageZH, SkillVIP:
		return true
	default:
		return false
	}
}

// DistributedMessage 分布式消息结构
type DistributedMessage struct {
	Type      OperationType          `json:"type"`      // 操作类型
	NodeID    string                 `json:"node_id"`   // 源节点ID
	TargetID  string                 `json:"target_id"` // 目标ID（用户ID、节点ID等）
	Data      map[string]interface{} `json:"data"`      // 消息数据
	Timestamp time.Time              `json:"timestamp"` // 时间戳
}

// WelcomeMessageProvider 欢迎消息提供者接口
type WelcomeMessageProvider interface {
	// GetWelcomeMessage 获取欢迎消息
	// 参数: userID 用户ID, userRole 用户角色, userType 用户类型, ticketID 工单ID, extraData 扩展数据
	// 返回: 欢迎消息内容, 是否启用欢迎消息, 错误信息
	GetWelcomeMessage(userID string, userRole UserRole, userType UserType, ticketID string, extraData map[string]interface{}) (*WelcomeMessage, bool, error)

	// RefreshConfig 刷新配置 - 当数据库配置更新时调用
	RefreshConfig() error
}

// WelcomeMessage 欢迎消息
type WelcomeMessage struct {
	Title       string                 `json:"title"`        // 欢迎标题
	Content     string                 `json:"content"`      // 欢迎内容
	MessageType MessageType            `json:"message_type"` // 消息类型
	Data        map[string]interface{} `json:"data"`         // 扩展数据
	Priority    Priority               `json:"priority"`     // 消息优先级
}

// WelcomeTemplate 欢迎消息模板
type WelcomeTemplate struct {
	Title       string                 `json:"title"`        // 欢迎标题
	Content     string                 `json:"content"`      // 欢迎内容
	MessageType MessageType            `json:"message_type"` // 消息类型
	Data        map[string]interface{} `json:"data"`         // 扩展数据
	Enabled     bool                   `json:"enabled"`      // 是否启用
	Variables   []string               `json:"variables"`    // 支持的变量列表，如: {user_name}, {ticket_id}, {time}
}

// ReplaceVariables 替换模板中的变量
func (wt *WelcomeTemplate) ReplaceVariables(variables map[string]interface{}) WelcomeTemplate {
	result := *wt

	for key, value := range variables {
		placeholder := "{" + key + "}"
		if val, ok := value.(string); ok {
			result.Title = strings.ReplaceAll(result.Title, placeholder, val)
			result.Content = strings.ReplaceAll(result.Content, placeholder, val)
		}
	}

	return result
}

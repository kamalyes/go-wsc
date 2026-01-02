/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\enums.go
 * @Description: 枚举类型定义
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

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
	return UserRoleValidator.IsValid(r)
}

// UserType 用户类型（与角色可能不同，提供更细分的分类）
type UserType string

const (
	UserTypeVisitor  UserType = "visitor"  // 访客用户
	UserTypeCustomer UserType = "customer" // 普通客户
	UserTypeAgent    UserType = "agent"    // 人工客服
	UserTypeAdmin    UserType = "admin"    // 系统管理员
	UserTypeBot      UserType = "bot"      // 机器人客服
	UserTypeVIP      UserType = "vip"      // VIP客户
	UserTypeSystem   UserType = "system"   // 系统用户
)

// String 实现Stringer接口
func (t UserType) String() string {
	return string(t)
}

// IsValid 检查用户类型是否有效
func (t UserType) IsValid() bool {
	return UserTypeValidator.IsValid(t)
}

// IsCustomerType 检查是否为客户类型（访客、普通客户、VIP客户）
func (t UserType) IsCustomerType() bool {
	switch t {
	case UserTypeVisitor, UserTypeCustomer, UserTypeVIP:
		return true
	default:
		return false
	}
}

// IsAgentType 检查是否为客服类型
func (t UserType) IsAgentType() bool {
	switch t {
	case UserTypeAgent:
		return true
	default:
		return false
	}
}

// IsSystemType 检查是否为系统类型（系统用户、管理员）
func (t UserType) IsSystemType() bool {
	switch t {
	case UserTypeSystem:
		return true
	default:
		return false
	}
}

// IsHumanType 检查是否为人类用户（排除机器人和系统）
func (t UserType) IsHumanType() bool {
	switch t {
	case UserTypeVisitor, UserTypeCustomer, UserTypeAgent, UserTypeAdmin, UserTypeVIP:
		return true
	default:
		return false
	}
}

// IsVIPType 检查是否为VIP用户
func (t UserType) IsVIPType() bool {
	return t == UserTypeVIP
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
	return UserStatusValidator.IsValid(s)
}

// DisconnectReason 断开连接原因
type DisconnectReason string

const (
	DisconnectReasonReadError      DisconnectReason = "read_error"      // 读取错误
	DisconnectReasonWriteError     DisconnectReason = "write_error"     // 写入错误
	DisconnectReasonContextDone    DisconnectReason = "context_done"    // 上下文结束
	DisconnectReasonCloseMessage   DisconnectReason = "close_message"   // 关闭消息
	DisconnectReasonHeartbeatFail  DisconnectReason = "heartbeat_fail"  // 心跳失败
	DisconnectReasonKickOut        DisconnectReason = "kick_out"        // 被踢出
	DisconnectReasonForceOffline   DisconnectReason = "force_offline"   // 强制下线
	DisconnectReasonTimeout        DisconnectReason = "timeout"         // 超时
	DisconnectReasonClientRequest  DisconnectReason = "client_request"  // 客户端主动断开
	DisconnectReasonServerShutdown DisconnectReason = "server_shutdown" // 服务器关闭
	DisconnectReasonUnknown        DisconnectReason = "unknown"         // 未知原因
)

// String 实现Stringer接口
func (r DisconnectReason) String() string {
	return string(r)
}

// IsValid 检查断开原因是否有效
func (r DisconnectReason) IsValid() bool {
	return DisconnectReasonValidator.IsValid(r)
}

// ErrorSeverity 错误严重程度
type ErrorSeverity string

const (
	ErrorSeverityInfo     ErrorSeverity = "info"     // 信息
	ErrorSeverityWarning  ErrorSeverity = "warning"  // 警告
	ErrorSeverityError    ErrorSeverity = "error"    // 错误
	ErrorSeverityCritical ErrorSeverity = "critical" // 严重错误
	ErrorSeverityFatal    ErrorSeverity = "fatal"    // 致命错误
)

// String 实现Stringer接口
func (s ErrorSeverity) String() string {
	return string(s)
}

// IsValid 检查严重程度是否有效
func (s ErrorSeverity) IsValid() bool {
	return ErrorSeverityValidator.IsValid(s)
}

// QueueType 队列类型
type QueueType string

const (
	QueueTypeBroadcast    QueueType = "broadcast_full" // 广播队列
	QueueTypePending      QueueType = "pending_full"   // 待发送队列
	QueueTypeAllQueues    QueueType = "all_queues"     // 所有队列
	QueueTypeMessageQueue QueueType = "message_queue"  // 消息队列
	QueueTypeClientBuffer QueueType = "client_buffer"  // 客户端缓冲区
)

// String 实现Stringer接口
func (q QueueType) String() string {
	return string(q)
}

// IsValid 检查队列类型是否有效
func (q QueueType) IsValid() bool {
	return QueueTypeValidator.IsValid(q)
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
	return MessageStatusValidator.IsValid(s)
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
	return NodeStatusValidator.IsValid(s)
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
	return ConnectionStatusValidator.IsValid(s)
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
	return OperationTypeValidator.IsValid(o)
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
	return ClientTypeValidator.IsValid(c)
}

// ConnectionType 连接类型（WebSocket/SSE）
type ConnectionType string

const (
	ConnectionTypeWebSocket ConnectionType = "websocket" // WebSocket连接
	ConnectionTypeSSE       ConnectionType = "sse"       // SSE连接
)

// String 实现Stringer接口
func (c ConnectionType) String() string {
	return string(c)
}

// IsValid 检查连接类型是否有效
func (c ConnectionType) IsValid() bool {
	return c == ConnectionTypeWebSocket || c == ConnectionTypeSSE
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
	return PriorityValidator.IsValid(p)
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
	return DepartmentValidator.IsValid(d)
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
	return SkillValidator.IsValid(s)
}

// PushType 推送类型
type PushType string

const (
	PushTypeNone    PushType = "none"    // 不推送 (仅保存/处理业务)
	PushTypeDirect  PushType = "direct"  // 在线直推 (尝试WebSocket，失败转离线)
	PushTypeQueue   PushType = "queue"   // 队列推送 (推送到Redis Stream)
	PushTypeOffline PushType = "offline" // 离线推送 (仅离线用户)
	PushTypeUnicast PushType = "unicast" // 单播推送 (仅在线用户)
)

// String 实现Stringer接口
func (p PushType) String() string {
	return string(p)
}

// IsValid 检查推送类型是否有效
func (p PushType) IsValid() bool {
	return PushTypeValidator.IsValid(p)
}

// BroadcastType 广播类型
type BroadcastType string

const (
	BroadcastTypeNone    BroadcastType = "none"    // 不广播（单播）
	BroadcastTypeSession BroadcastType = "session" // 会话成员广播
	BroadcastTypeGlobal  BroadcastType = "global"  // 全站广播
)

// String 实现Stringer接口
func (b BroadcastType) String() string {
	return string(b)
}

// IsValid 检查广播类型是否有效
func (b BroadcastType) IsValid() bool {
	return BroadcastTypeValidator.IsValid(b)
}

// VIPLevel VIP等级
type VIPLevel string

const (
	VIPLevelV0 VIPLevel = "v0" // 普通用户
	VIPLevelV1 VIPLevel = "v1" // VIP1级
	VIPLevelV2 VIPLevel = "v2" // VIP2级
	VIPLevelV3 VIPLevel = "v3" // VIP3级
	VIPLevelV4 VIPLevel = "v4" // VIP4级
	VIPLevelV5 VIPLevel = "v5" // VIP5级
	VIPLevelV6 VIPLevel = "v6" // VIP6级
	VIPLevelV7 VIPLevel = "v7" // VIP7级
	VIPLevelV8 VIPLevel = "v8" // VIP8级（最高级）
)

// String 实现Stringer接口
func (v VIPLevel) String() string {
	return string(v)
}

// IsValid 检查VIP等级是否有效
func (v VIPLevel) IsValid() bool {
	return VIPLevelValidator.IsValid(v)
}

// GetLevel 获取VIP等级数值 (0-8)
func (v VIPLevel) GetLevel() int {
	switch v {
	case VIPLevelV0:
		return 0
	case VIPLevelV1:
		return 1
	case VIPLevelV2:
		return 2
	case VIPLevelV3:
		return 3
	case VIPLevelV4:
		return 4
	case VIPLevelV5:
		return 5
	case VIPLevelV6:
		return 6
	case VIPLevelV7:
		return 7
	case VIPLevelV8:
		return 8
	default:
		return 0
	}
}

// IsHigherThan 比较VIP等级
func (v VIPLevel) IsHigherThan(other VIPLevel) bool {
	return v.GetLevel() > other.GetLevel()
}

// UrgencyLevel 紧急等级
type UrgencyLevel string

const (
	UrgencyLevelLow    UrgencyLevel = "low"    // 低紧急
	UrgencyLevelNormal UrgencyLevel = "normal" // 正常
	UrgencyLevelHigh   UrgencyLevel = "high"   // 高紧急
)

// String 实现Stringer接口
func (u UrgencyLevel) String() string {
	return string(u)
}

// IsValid 检查紧急等级是否有效
func (u UrgencyLevel) IsValid() bool {
	return UrgencyLevelValidator.IsValid(u)
}

// GetLevel 获取紧急等级数值 (0-2)
func (u UrgencyLevel) GetLevel() int {
	switch u {
	case UrgencyLevelLow:
		return 0
	case UrgencyLevelNormal:
		return 1
	case UrgencyLevelHigh:
		return 2
	default:
		return 1
	}
}

// IsMoreUrgentThan 比较紧急程度
func (u UrgencyLevel) IsMoreUrgentThan(other UrgencyLevel) bool {
	return u.GetLevel() > other.GetLevel()
}

// BusinessCategory 业务分类
type BusinessCategory string

const (
	// 基本分类
	BusinessCategoryGeneral    BusinessCategory = "general"    // 通用
	BusinessCategoryCustomer   BusinessCategory = "customer"   // 客户服务
	BusinessCategorySales      BusinessCategory = "sales"      // 销售
	BusinessCategoryTechnical  BusinessCategory = "technical"  // 技术
	BusinessCategoryFinance    BusinessCategory = "finance"    // 财务
	BusinessCategorySecurity   BusinessCategory = "security"   // 安全
	BusinessCategoryOperations BusinessCategory = "operations" // 运营
	BusinessCategorySupport    BusinessCategory = "support"    // 支持
	BusinessCategoryIT         BusinessCategory = "it"         // 信息技术
	BusinessCategoryQuality    BusinessCategory = "quality"    // 质量
	BusinessCategoryOther      BusinessCategory = "other"      // 其他
)

// String 实现Stringer接口
func (b BusinessCategory) String() string {
	return string(b)
}

// IsValid 检查业务分类是否有效
func (b BusinessCategory) IsValid() bool {
	return BusinessCategoryValidator.IsValid(b)
}

// GetCategoryType 获取业务分类类型
func (b BusinessCategory) GetCategoryType() string {
	return string(b)
}

// GetAllVIPLevels 获取所有VIP等级
func GetAllVIPLevels() []VIPLevel {
	return []VIPLevel{
		VIPLevelV0,
		VIPLevelV1,
		VIPLevelV2,
		VIPLevelV3,
		VIPLevelV4,
		VIPLevelV5,
		VIPLevelV6,
		VIPLevelV7,
		VIPLevelV8,
	}
}

// GetAllUrgencyLevels 获取所有紧急等级
func GetAllUrgencyLevels() []UrgencyLevel {
	return []UrgencyLevel{
		UrgencyLevelLow,
		UrgencyLevelNormal,
		UrgencyLevelHigh,
	}
}

// GetAllBusinessCategories 获取所有业务分类
func GetAllBusinessCategories() []BusinessCategory {
	return []BusinessCategory{
		BusinessCategoryGeneral,
		BusinessCategoryCustomer,
		BusinessCategorySales,
		BusinessCategoryTechnical,
		BusinessCategoryFinance,
		BusinessCategorySecurity,
		BusinessCategoryOperations,
	}
}

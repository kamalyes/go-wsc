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
	"fmt"
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
	MessageTypeText         MessageType = "text"         // 文本消息
	MessageTypeImage        MessageType = "image"        // 图片消息
	MessageTypeFile         MessageType = "file"         // 文件消息
	MessageTypeAudio        MessageType = "audio"        // 音频消息
	MessageTypeVideo        MessageType = "video"        // 视频消息
	MessageTypeSystem       MessageType = "system"       // 系统消息
	MessageTypeNotice       MessageType = "notice"       // 通知消息
	MessageTypeEvent        MessageType = "event"        // 事件消息
	MessageTypeAck          MessageType = "ack"          // ACK确认消息
	MessageTypeLocation     MessageType = "location"     // 位置消息
	MessageTypeCard         MessageType = "card"         // 卡片消息
	MessageTypeEmoji        MessageType = "emoji"        // 表情消息
	MessageTypeSticker      MessageType = "sticker"      // 贴纸消息
	MessageTypeLink         MessageType = "link"         // 链接消息
	MessageTypeQuote        MessageType = "quote"        // 引用回复消息
	MessageTypeForward      MessageType = "forward"      // 转发消息
	MessageTypeCommand      MessageType = "command"      // 命令消息
	MessageTypeMarkdown     MessageType = "markdown"     // Markdown格式消息
	MessageTypeRichText     MessageType = "rich_text"    // 富文本消息
	MessageTypeCode         MessageType = "code"         // 代码消息
	MessageTypeJson         MessageType = "json"         // JSON数据消息
	MessageTypeXML          MessageType = "xml"          // XML数据消息
	MessageTypeBinary       MessageType = "binary"       // 二进制数据消息
	MessageTypeVoice        MessageType = "voice"        // 语音消息
	MessageTypeGIF          MessageType = "gif"          // GIF动图消息
	MessageTypeDocument     MessageType = "document"     // 文档消息
	MessageTypeSpreadsheet  MessageType = "spreadsheet"  // 电子表格消息
	MessageTypePresentation MessageType = "presentation" // 演示文稿消息
	MessageTypeContact      MessageType = "contact"      // 联系人卡片消息
	MessageTypeCalendar     MessageType = "calendar"     // 日历事件消息
	MessageTypeTask         MessageType = "task"         // 任务消息
	MessageTypePoll         MessageType = "poll"         // 投票消息
	MessageTypeForm         MessageType = "form"         // 表单消息
	MessageTypePayment      MessageType = "payment"      // 支付消息
	MessageTypeOrder        MessageType = "order"        // 订单消息
	MessageTypeProduct      MessageType = "product"      // 产品消息
	MessageTypeInvite       MessageType = "invite"       // 邀请消息
	MessageTypeAnnouncement MessageType = "announcement" // 公告消息
	MessageTypeAlert        MessageType = "alert"        // 警告消息
	MessageTypeError        MessageType = "error"        // 错误消息
	MessageTypeInfo         MessageType = "info"         // 信息消息
	MessageTypeSuccess      MessageType = "success"      // 成功消息
	MessageTypeWarning      MessageType = "warning"      // 警告消息
	MessageTypeHeartbeat    MessageType = "heartbeat"    // 心跳消息
	MessageTypePing         MessageType = "ping"         // Ping消息
	MessageTypePong         MessageType = "pong"         // Pong消息
	MessageTypeTyping       MessageType = "typing"       // 正在输入状态消息
	MessageTypeRead         MessageType = "read"         // 已读消息
	MessageTypeDelivered    MessageType = "delivered"    // 已送达消息
	MessageTypeRecall       MessageType = "recall"       // 消息撤回
	MessageTypeEdit         MessageType = "edit"         // 消息编辑
	MessageTypeReaction     MessageType = "reaction"     // 消息反应/表态
	MessageTypeThread       MessageType = "thread"       // 线程消息
	MessageTypeReply        MessageType = "reply"        // 回复消息
	MessageTypeMention      MessageType = "mention"      // @提及消息
	MessageTypeCustom       MessageType = "custom"       // 自定义类型消息
)

// String 实现Stringer接口
func (t MessageType) String() string {
	return string(t)
}

// IsValid 检查消息类型是否有效
func (t MessageType) IsValid() bool {
	switch t {
	case MessageTypeText, MessageTypeImage, MessageTypeFile, MessageTypeAudio, MessageTypeVideo,
		MessageTypeSystem, MessageTypeNotice, MessageTypeEvent, MessageTypeAck, MessageTypeLocation,
		MessageTypeCard, MessageTypeEmoji, MessageTypeSticker, MessageTypeLink, MessageTypeQuote,
		MessageTypeForward, MessageTypeCommand, MessageTypeMarkdown, MessageTypeRichText, MessageTypeCode,
		MessageTypeJson, MessageTypeXML, MessageTypeBinary, MessageTypeVoice, MessageTypeGIF,
		MessageTypeDocument, MessageTypeSpreadsheet, MessageTypePresentation, MessageTypeContact,
		MessageTypeCalendar, MessageTypeTask, MessageTypePoll, MessageTypeForm, MessageTypePayment,
		MessageTypeOrder, MessageTypeProduct, MessageTypeInvite, MessageTypeAnnouncement, MessageTypeAlert,
		MessageTypeError, MessageTypeInfo, MessageTypeSuccess, MessageTypeWarning, MessageTypeHeartbeat,
		MessageTypePing, MessageTypePong, MessageTypeTyping, MessageTypeRead, MessageTypeDelivered,
		MessageTypeRecall, MessageTypeEdit, MessageTypeReaction, MessageTypeThread, MessageTypeReply,
		MessageTypeMention, MessageTypeCustom:
		return true
	default:
		return false
	}
}

// IsMediaType 检查是否为媒体类型消息
func (t MessageType) IsMediaType() bool {
	switch t {
	case MessageTypeImage, MessageTypeAudio, MessageTypeVideo, MessageTypeFile,
		MessageTypeVoice, MessageTypeGIF, MessageTypeDocument, MessageTypeSpreadsheet,
		MessageTypePresentation:
		return true
	default:
		return false
	}
}

// IsTextType 检查是否为文本类型消息
func (t MessageType) IsTextType() bool {
	switch t {
	case MessageTypeText, MessageTypeMarkdown, MessageTypeRichText, MessageTypeCode:
		return true
	default:
		return false
	}
}

// IsSystemType 检查是否为系统类型消息
func (t MessageType) IsSystemType() bool {
	switch t {
	case MessageTypeSystem, MessageTypeNotice, MessageTypeEvent, MessageTypeAnnouncement,
		MessageTypeAlert, MessageTypeError, MessageTypeInfo, MessageTypeSuccess,
		MessageTypeWarning, MessageTypeHeartbeat, MessageTypePing, MessageTypePong:
		return true
	default:
		return false
	}
}

// IsInteractiveType 检查是否为交互类型消息
func (t MessageType) IsInteractiveType() bool {
	switch t {
	case MessageTypeCard, MessageTypeLink, MessageTypeQuote, MessageTypeCommand,
		MessageTypePoll, MessageTypeForm, MessageTypeTask, MessageTypeInvite:
		return true
	default:
		return false
	}
}

// IsStatusType 检查是否为状态类型消息
func (t MessageType) IsStatusType() bool {
	switch t {
	case MessageTypeTyping, MessageTypeRead, MessageTypeDelivered, MessageTypeAck:
		return true
	default:
		return false
	}
}

// GetCategory 获取消息类型分类
func (t MessageType) GetCategory() string {
	if t.IsMediaType() {
		return "media"
	}
	if t.IsTextType() {
		return "text"
	}
	if t.IsSystemType() {
		return "system"
	}
	if t.IsInteractiveType() {
		return "interactive"
	}
	if t.IsStatusType() {
		return "status"
	}
	return "other"
}

// GetAllMessageTypes 返回所有可用的消息类型
func GetAllMessageTypes() []MessageType {
	return []MessageType{
		MessageTypeText, MessageTypeImage, MessageTypeFile, MessageTypeAudio, MessageTypeVideo,
		MessageTypeSystem, MessageTypeNotice, MessageTypeEvent, MessageTypeAck, MessageTypeLocation,
		MessageTypeCard, MessageTypeEmoji, MessageTypeSticker, MessageTypeLink, MessageTypeQuote,
		MessageTypeForward, MessageTypeCommand, MessageTypeMarkdown, MessageTypeRichText, MessageTypeCode,
		MessageTypeJson, MessageTypeXML, MessageTypeBinary, MessageTypeVoice, MessageTypeGIF,
		MessageTypeDocument, MessageTypeSpreadsheet, MessageTypePresentation, MessageTypeContact,
		MessageTypeCalendar, MessageTypeTask, MessageTypePoll, MessageTypeForm, MessageTypePayment,
		MessageTypeOrder, MessageTypeProduct, MessageTypeInvite, MessageTypeAnnouncement, MessageTypeAlert,
		MessageTypeError, MessageTypeInfo, MessageTypeSuccess, MessageTypeWarning, MessageTypeHeartbeat,
		MessageTypePing, MessageTypePong, MessageTypeTyping, MessageTypeRead, MessageTypeDelivered,
		MessageTypeRecall, MessageTypeEdit, MessageTypeReaction, MessageTypeThread, MessageTypeReply,
		MessageTypeMention, MessageTypeCustom,
	}
}

// GetMessageTypesByCategory 根据分类返回消息类型
func GetMessageTypesByCategory(category string) []MessageType {
	var types []MessageType
	for _, msgType := range GetAllMessageTypes() {
		if msgType.GetCategory() == category {
			types = append(types, msgType)
		}
	}
	return types
}

// MessagePriority 消息优先级
type MessagePriority int

const (
	// 低优先级：一般信息、日志、统计数据等
	MessagePriorityLow MessagePriority = iota + 1
	// 普通优先级：常规用户消息、文件传输等
	MessagePriorityNormal
	// 高优先级：重要通知、系统消息等
	MessagePriorityHigh
	// 紧急优先级：安全警告、错误信息等
	MessagePriorityUrgent
	// 关键优先级：系统故障、紧急维护等
	MessagePriorityCritical
)

// String 实现Stringer接口
func (p MessagePriority) String() string {
	switch p {
	case MessagePriorityLow:
		return "low"
	case MessagePriorityNormal:
		return "normal"
	case MessagePriorityHigh:
		return "high"
	case MessagePriorityUrgent:
		return "urgent"
	case MessagePriorityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// GetWeight 获取优先级权重值（数值越大优先级越高）
func (p MessagePriority) GetWeight() int {
	return int(p)
}

// IsHigherThan 判断是否比另一个优先级高
func (p MessagePriority) IsHigherThan(other MessagePriority) bool {
	return p > other
}

// GetPriorityByMessageType 根据消息类型获取默认优先级
func (t MessageType) GetDefaultPriority() MessagePriority {
	switch {
	// 关键优先级 - 系统故障、安全相关
	case t == MessageTypeError || t == MessageTypeAlert:
		return MessagePriorityCritical

	// 紧急优先级 - 重要系统消息
	case t == MessageTypeSystem || t == MessageTypeAnnouncement || t == MessageTypeWarning:
		return MessagePriorityUrgent

	// 高优先级 - 重要业务消息
	case t == MessageTypeNotice || t == MessageTypeEvent || t == MessageTypeSuccess ||
		t == MessageTypePayment || t == MessageTypeOrder || t == MessageTypeInvite ||
		t == MessageTypeTask || t == MessageTypeRecall:
		return MessagePriorityHigh

	// 普通优先级 - 常规交互消息
	case t == MessageTypeText || t == MessageTypeImage || t == MessageTypeAudio ||
		t == MessageTypeVideo || t == MessageTypeFile || t == MessageTypeCard ||
		t == MessageTypeLink || t == MessageTypeQuote || t == MessageTypeForward ||
		t == MessageTypeMarkdown || t == MessageTypeRichText || t == MessageTypeCode ||
		t == MessageTypeVoice || t == MessageTypeLocation || t == MessageTypeContact ||
		t == MessageTypeDocument || t == MessageTypeCalendar || t == MessageTypePoll ||
		t == MessageTypeForm || t == MessageTypeProduct || t == MessageTypeEmoji ||
		t == MessageTypeSticker || t == MessageTypeGIF || t == MessageTypeReply ||
		t == MessageTypeThread || t == MessageTypeMention:
		return MessagePriorityNormal

	// 低优先级 - 状态、统计、心跳等
	case t == MessageTypeTyping || t == MessageTypeRead || t == MessageTypeDelivered ||
		t == MessageTypeAck || t == MessageTypeHeartbeat || t == MessageTypePing ||
		t == MessageTypePong || t == MessageTypeInfo || t == MessageTypeEdit ||
		t == MessageTypeReaction || t == MessageTypeJson || t == MessageTypeXML ||
		t == MessageTypeBinary || t == MessageTypeSpreadsheet || t == MessageTypePresentation ||
		t == MessageTypeCustom || t == MessageTypeCommand:
		return MessagePriorityLow

	// 默认普通优先级
	default:
		return MessagePriorityNormal
	}
}

// GetMessageTypesByPriority 根据优先级获取消息类型列表
func GetMessageTypesByPriority(priority MessagePriority) []MessageType {
	var types []MessageType
	for _, msgType := range GetAllMessageTypes() {
		if msgType.GetDefaultPriority() == priority {
			types = append(types, msgType)
		}
	}
	return types
}

// PriorityStats 优先级统计
type PriorityStats struct {
	Critical int `json:"critical"` // 关键优先级数量
	Urgent   int `json:"urgent"`   // 紧急优先级数量
	High     int `json:"high"`     // 高优先级数量
	Normal   int `json:"normal"`   // 普通优先级数量
	Low      int `json:"low"`      // 低优先级数量
	Total    int `json:"total"`    // 总数量
}

// GetPriorityStats 获取所有消息类型的优先级统计
func GetPriorityStats() *PriorityStats {
	stats := &PriorityStats{}

	for _, msgType := range GetAllMessageTypes() {
		priority := msgType.GetDefaultPriority()
		switch priority {
		case MessagePriorityCritical:
			stats.Critical++
		case MessagePriorityUrgent:
			stats.Urgent++
		case MessagePriorityHigh:
			stats.High++
		case MessagePriorityNormal:
			stats.Normal++
		case MessagePriorityLow:
			stats.Low++
		}
		stats.Total++
	}

	return stats
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
	switch v {
	case VIPLevelV0, VIPLevelV1, VIPLevelV2, VIPLevelV3,
		VIPLevelV4, VIPLevelV5, VIPLevelV6, VIPLevelV7, VIPLevelV8:
		return true
	default:
		return false
	}
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
	switch u {
	case UrgencyLevelLow, UrgencyLevelNormal, UrgencyLevelHigh:
		return true
	default:
		return false
	}
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
)

// String 实现Stringer接口
func (b BusinessCategory) String() string {
	return string(b)
}

// IsValid 检查业务分类是否有效
func (b BusinessCategory) IsValid() bool {
	switch b {
	case BusinessCategoryGeneral, BusinessCategoryCustomer, BusinessCategorySales,
		BusinessCategoryTechnical, BusinessCategoryFinance, BusinessCategorySecurity,
		BusinessCategoryOperations:
		return true
	default:
		return false
	}
}

// GetCategoryType 获取业务分类类型
func (b BusinessCategory) GetCategoryType() string {
	return string(b)
}

// MessageClassification 消息综合分类
type MessageClassification struct {
	Type             MessageType      `json:"type"`
	Priority         MessagePriority  `json:"priority"`
	VIPLevel         VIPLevel         `json:"vip_level"`
	UrgencyLevel     UrgencyLevel     `json:"urgency_level"`
	BusinessCategory BusinessCategory `json:"business_category"`
	Timestamp        int64            `json:"timestamp"`
	UserID           string           `json:"user_id,omitempty"`
}

// GetFinalPriority 获取综合优先级分数 (0-100)
func (mc *MessageClassification) GetFinalPriority() int {
	// 基础优先级分数 (0-25)
	basePriority := mc.Priority.GetWeight() * 5

	// VIP等级加分 (0-40)
	vipBonus := mc.VIPLevel.GetLevel() * 5

	// 紧急等级加分 (0-20)
	urgencyBonus := mc.UrgencyLevel.GetLevel() * 10

	// 业务分类加分 (0-15)
	var categoryBonus int
	switch mc.BusinessCategory {
	case BusinessCategorySecurity:
		categoryBonus = 15
	case BusinessCategoryFinance:
		categoryBonus = 10
	case BusinessCategoryCustomer:
		categoryBonus = 8
	case BusinessCategoryTechnical:
		categoryBonus = 5
	case BusinessCategorySales:
		categoryBonus = 3
	default:
		categoryBonus = 0
	}

	totalScore := basePriority + vipBonus + urgencyBonus + categoryBonus

	// 确保分数在50-100之间
	if totalScore > 100 {
		return 100
	}
	return totalScore
}

// GetDisplayName 获取分类显示名称
func (mc *MessageClassification) GetDisplayName() string {
	return fmt.Sprintf("[%s][%s][%s][%s]",
		mc.Type,
		mc.Priority,
		mc.VIPLevel,
		mc.UrgencyLevel,
	)
}

// IsHighPriorityMessage 判断是否为高优先级消息
func (mc *MessageClassification) IsHighPriorityMessage() bool {
	return mc.GetFinalPriority() >= 70
}

// IsCriticalMessage 判断是否为关键消息
func (mc *MessageClassification) IsCriticalMessage() bool {
	return mc.GetFinalPriority() >= 90 ||
		mc.Priority == MessagePriorityCritical ||
		mc.UrgencyLevel == UrgencyLevelHigh ||
		mc.BusinessCategory.GetCategoryType() == "security"
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

// HubStats Hub统计信息结构体
type HubStats struct {
	// 连接统计
	TotalClients      int `json:"total_clients"`      // 总客户端数
	WebSocketClients  int `json:"websocket_clients"`  // WebSocket客户端数
	SSEClients        int `json:"sse_clients"`        // SSE客户端数
	AgentConnections  int `json:"agent_connections"`  // 座席连接数
	TicketConnections int `json:"ticket_connections"` // 工单连接数

	// 消息统计
	MessagesSent     int64 `json:"messages_sent"`     // 已发送消息数
	MessagesReceived int64 `json:"messages_received"` // 已接收消息数
	BroadcastsSent   int64 `json:"broadcasts_sent"`   // 已发送广播数
	QueuedMessages   int   `json:"queued_messages"`   // 排队消息数

	// 其他统计
	OnlineUsers int   `json:"online_users"` // 在线用户数
	Uptime      int64 `json:"uptime"`       // 运行时间(秒)
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

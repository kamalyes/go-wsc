/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\message_type.go
 * @Description: 消息类型和优先级定义
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

// MessageType 消息类型
type MessageType string

const (
	MessageTypeText                 MessageType = "text"                   // 文本消息
	MessageTypeImage                MessageType = "image"                  // 图片消息
	MessageTypeFile                 MessageType = "file"                   // 文件消息
	MessageTypeAudio                MessageType = "audio"                  // 音频消息
	MessageTypeVideo                MessageType = "video"                  // 视频消息
	MessageTypeSystem               MessageType = "system"                 // 系统消息
	MessageTypeNotice               MessageType = "notice"                 // 通知消息
	MessageTypeEvent                MessageType = "event"                  // 事件消息
	MessageTypeAck                  MessageType = "ack"                    // ACK确认消息
	MessageTypeLocation             MessageType = "location"               // 位置消息
	MessageTypeCard                 MessageType = "card"                   // 卡片消息
	MessageTypeEmoji                MessageType = "emoji"                  // 表情消息
	MessageTypeSticker              MessageType = "sticker"                // 贴纸消息
	MessageTypeLink                 MessageType = "link"                   // 链接消息
	MessageTypeQuote                MessageType = "quote"                  // 引用回复消息
	MessageTypeForward              MessageType = "forward"                // 转发消息
	MessageTypeCommand              MessageType = "command"                // 命令消息
	MessageTypeMarkdown             MessageType = "markdown"               // Markdown格式消息
	MessageTypeRichText             MessageType = "rich_text"              // 富文本消息
	MessageTypeCode                 MessageType = "code"                   // 代码消息
	MessageTypeJson                 MessageType = "json"                   // JSON数据消息
	MessageTypeXML                  MessageType = "xml"                    // XML数据消息
	MessageTypeBinary               MessageType = "binary"                 // 二进制数据消息
	MessageTypeVoice                MessageType = "voice"                  // 语音消息
	MessageTypeGIF                  MessageType = "gif"                    // GIF动图消息
	MessageTypeDocument             MessageType = "document"               // 文档消息
	MessageTypeSpreadsheet          MessageType = "spreadsheet"            // 电子表格消息
	MessageTypePresentation         MessageType = "presentation"           // 演示文稿消息
	MessageTypeContact              MessageType = "contact"                // 联系人卡片消息
	MessageTypeCalendar             MessageType = "calendar"               // 日历事件消息
	MessageTypeTask                 MessageType = "task"                   // 任务消息
	MessageTypePoll                 MessageType = "poll"                   // 投票消息
	MessageTypeForm                 MessageType = "form"                   // 表单消息
	MessageTypePayment              MessageType = "payment"                // 支付消息
	MessageTypeOrder                MessageType = "order"                  // 订单消息
	MessageTypeProduct              MessageType = "product"                // 产品消息
	MessageTypeInvite               MessageType = "invite"                 // 邀请消息
	MessageTypeAnnouncement         MessageType = "announcement"           // 公告消息
	MessageTypeAlert                MessageType = "alert"                  // 警告消息
	MessageTypeError                MessageType = "error"                  // 错误消息
	MessageTypeInfo                 MessageType = "info"                   // 信息消息
	MessageTypeSuccess              MessageType = "success"                // 成功消息
	MessageTypeWarning              MessageType = "warning"                // 警告消息
	MessageTypeHeartbeat            MessageType = "heartbeat"              // 心跳消息
	MessageTypePing                 MessageType = "ping"                   // Ping消息
	MessageTypePong                 MessageType = "pong"                   // Pong消息
	MessageTypeTyping               MessageType = "typing"                 // 正在输入状态消息
	MessageTypeRead                 MessageType = "read"                   // 已读消息
	MessageTypeDelivered            MessageType = "delivered"              // 已送达消息
	MessageTypeRecall               MessageType = "recall"                 // 消息撤回
	MessageTypeEdit                 MessageType = "edit"                   // 消息编辑
	MessageTypeReaction             MessageType = "reaction"               // 消息反应/表态
	MessageTypeThread               MessageType = "thread"                 // 线程消息
	MessageTypeReply                MessageType = "reply"                  // 回复消息
	MessageTypeMention              MessageType = "mention"                // @提及消息
	MessageTypeCustom               MessageType = "custom"                 // 自定义类型消息
	MessageTypeUnknown              MessageType = "unknown"                // 未知类型消息
	MessageTypeTicketCreated        MessageType = "ticket_created"         // 工单创建消息
	MessageTypeTicketAssigned       MessageType = "ticket_assigned"        // 分配工单消息
	MessageTypeTicketClosed         MessageType = "ticket_closed"          // 手动关闭工单消息
	MessageTypeTicketTimeoutClosed  MessageType = "ticket_timeout_closed"  // 超时关闭工单消息
	MessageTypeTicketTransfer       MessageType = "ticket_transfer"        // 转移工单消息
	MessageTypeTicketActive         MessageType = "ticket_active"          // 活跃工单列表
	MessageTypeTest                 MessageType = "test"                   // 测试消息
	MessageTypeWelcome              MessageType = "welcome"                // 欢迎消息
	MessageTypeTerminate            MessageType = "terminate"              // 结束消息
	MessageTypeTransferred          MessageType = "transferred"            // 转发消息
	MessageTypeSessionCreated       MessageType = "session_created"        // 会话创建消息
	MessageTypeSessionClosed        MessageType = "session_closed"         // 会话关闭消息
	MessageTypeSessionQueued        MessageType = "session_queued"         // 会话排队消息
	MessageTypeSessionTimeout       MessageType = "session_timeout"        // 会话超时消息
	MessageTypeSessionPaused        MessageType = "session_paused"         // 会话暂停消息
	MessageTypeSessionResumed       MessageType = "session_resumed"        // 会话恢复消息
	MessageTypeSessionTransferred   MessageType = "session_transferred"    // 会话转接消息
	MessageTypeSessionMemberJoined  MessageType = "session_member_joined"  // 会话成员加入消息
	MessageTypeSessionMemberLeft    MessageType = "session_member_left"    // 会话成员离开消息
	MessageTypeSessionStatusChanged MessageType = "session_status_changed" // 会话状态变更消息
	MessageTypeCheckUserStatus      MessageType = "check_user_status"      // 检查用户状态消息
	MessageTypeUserStatusResponse   MessageType = "user_status_response"   // 用户状态响应消息
	MessageTypeGetOnlineUsers       MessageType = "get_online_users"       // 获取在线用户列表
	MessageTypeOnlineUsersList      MessageType = "online_users_list"      // 在线用户列表响应
	MessageTypeGetUserInfo          MessageType = "get_user_info"          // 获取用户信息
	MessageTypeUserInfoResponse     MessageType = "user_info_response"     // 用户信息响应
	MessageTypeSystemQuery          MessageType = "system_query"           // 系统查询消息
	MessageTypeSystemResponse       MessageType = "system_response"        // 系统响应消息
	MessageTypeUserJoined           MessageType = "user_joined"            // 用户加入通知
	MessageTypeUserLeft             MessageType = "user_left"              // 用户离开通知
	MessageTypeUserStatusChanged    MessageType = "user_status_changed"    // 用户状态变更通知
	MessageTypeServerStatus         MessageType = "server_status"          // 服务器状态消息
	MessageTypeServerStats          MessageType = "server_stats"           // 服务器统计消息
	MessageTypeClientConfig         MessageType = "client_config"          // 客户端配置消息
	MessageTypeConfigUpdate         MessageType = "config_update"          // 配置更新通知
	MessageTypeHealthCheck          MessageType = "health_check"           // 健康检查消息
	MessageTypeHealthResponse       MessageType = "health_response"        // 健康检查响应
	MessageTypeConnected            MessageType = "connected"              // 连接成功消息（发给刚连接的客户端）
	MessageTypeClientRegistered     MessageType = "client_registered"      // 客户端注册成功消息（连接建立并注册完成）
	MessageTypeDisconnected         MessageType = "disconnected"           // 用户断线通知（广播给会话其他成员）
	MessageTypeReconnected          MessageType = "reconnected"            // 重连成功消息（发给重连的客户端）
	MessageTypeConnectionError      MessageType = "connection_error"       // 连接错误消息（发给连接失败的客户端）
	MessageTypeConnectionTimeout    MessageType = "connection_timeout"     // 连接超时消息（发给超时的客户端）
	MessageTypeKickOut              MessageType = "kick_out"               // 被踢出消息（发给被踢的客户端，之后断开）
	MessageTypeForceOffline         MessageType = "force_offline"          // 强制下线通知（异地登录等，发给被下线的客户端）
	MessageTypeOpenWindow           MessageType = "open_window"            // 打开窗口消息
	MessageTypeCloseWindow          MessageType = "close_window"           // 关闭窗口消息
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
		MessageTypeMention, MessageTypeCustom, MessageTypeTicketCreated, MessageTypeTicketAssigned, MessageTypeTicketClosed, MessageTypeTicketTimeoutClosed,
		MessageTypeTicketTransfer, MessageTypeTicketActive, MessageTypeTest, MessageTypeWelcome, MessageTypeTerminate, MessageTypeTransferred,
		MessageTypeSessionCreated, MessageTypeSessionClosed, MessageTypeSessionQueued, MessageTypeSessionTimeout,
		MessageTypeSessionPaused, MessageTypeSessionResumed, MessageTypeSessionTransferred, MessageTypeSessionMemberJoined,
		MessageTypeSessionMemberLeft, MessageTypeSessionStatusChanged,
		MessageTypeCheckUserStatus, MessageTypeUserStatusResponse, MessageTypeGetOnlineUsers, MessageTypeOnlineUsersList,
		MessageTypeGetUserInfo, MessageTypeUserInfoResponse, MessageTypeSystemQuery, MessageTypeSystemResponse,
		MessageTypeUserJoined, MessageTypeUserLeft, MessageTypeUserStatusChanged, MessageTypeServerStatus,
		MessageTypeServerStats, MessageTypeClientConfig, MessageTypeConfigUpdate, MessageTypeHealthCheck,
		MessageTypeHealthResponse, MessageTypeConnected, MessageTypeClientRegistered, MessageTypeDisconnected, MessageTypeReconnected,
		MessageTypeConnectionError, MessageTypeConnectionTimeout, MessageTypeKickOut, MessageTypeForceOffline,
		MessageTypeCloseWindow, MessageTypeOpenWindow:
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
	case MessageTypeTyping, // 正在输入状态
		MessageTypeRead,      // 已读状态
		MessageTypeDelivered, // 送达状态
		MessageTypeAck,       // 确认状态
		MessageTypeReaction,  // 消息反应/表态
		MessageTypeEdit,      // 消息编辑
		MessageTypeRecall:    // 消息撤回
		return true
	default:
		return false
	}
}

// IsHeartbeatType 检查是否为心跳类型消息
func (t MessageType) IsHeartbeatType() bool {
	switch t {
	case MessageTypePing, MessageTypePong, MessageTypeHeartbeat:
		return true
	default:
		return false
	}
}

// IsSessionType 检查是否为会话类型消息
func (t MessageType) IsSessionType() bool {
	switch t {
	case MessageTypeSessionCreated, MessageTypeSessionClosed, MessageTypeSessionQueued,
		MessageTypeSessionTimeout, MessageTypeSessionPaused, MessageTypeSessionResumed,
		MessageTypeSessionTransferred, MessageTypeSessionMemberJoined, MessageTypeSessionMemberLeft,
		MessageTypeSessionStatusChanged:
		return true
	default:
		return false
	}
}

// IsConnectionType 检查是否为连接相关类型消息
func (t MessageType) IsConnectionType() bool {
	switch t {
	case MessageTypeConnected, MessageTypeClientRegistered, MessageTypeDisconnected, MessageTypeReconnected,
		MessageTypeConnectionError, MessageTypeConnectionTimeout, MessageTypeKickOut,
		MessageTypeForceOffline:
		return true
	default:
		return false
	}
}

// ShouldSkipDatabaseRecord 判断是否应该跳过数据库记录
// 某些消息类型（如心跳消息、状态消息、系统消息）频繁发送但不需要持久化，跳过记录可以减轻数据库压力
func (t MessageType) ShouldSkipDatabaseRecord() bool {
	// 状态消息、连接消息和系统消息(含心跳)不需要记录到数据库
	return t.IsStatusType() || t.IsConnectionType() || t.IsSystemType()
}

// IsBusinessType 检查是否为业务相关类型消息
func (t MessageType) IsBusinessType() bool {
	switch t {
	case MessageTypePayment, MessageTypeOrder, MessageTypeProduct, MessageTypeTicketCreated, MessageTypeTicketAssigned,
		MessageTypeTicketClosed, MessageTypeTicketTimeoutClosed, MessageTypeTicketTransfer, MessageTypeTicketActive:
		return true
	default:
		return false
	}
}

// IsWindowType 检查是否为窗口相关类型消息
func (t MessageType) IsWindowType() bool {
	switch t {
	case MessageTypeOpenWindow, MessageTypeCloseWindow:
		return true
	default:
		return false
	}
}

// MessageTypeEmojiMap 消息类型对应的日志 emoji 映射表
var MessageTypeEmojiMap = map[MessageType]string{
	// 窗口消息
	MessageTypeOpenWindow:  "🟢",
	MessageTypeCloseWindow: "🔴",
	// 状态消息
	MessageTypeTyping:    "⌨️",
	MessageTypeRead:      "👁️",
	MessageTypeDelivered: "✅",
	MessageTypeAck:       "✔️",
	MessageTypeReaction:  "❤️",
	MessageTypeEdit:      "✏️",
	MessageTypeRecall:    "↩️",
}

// GetEmoji 获取消息类型对应的 emoji，未找到返回默认值
func (t MessageType) GetEmoji() string {
	if emoji, ok := MessageTypeEmojiMap[t]; ok {
		return emoji
	}
	return "🔄"
}

// IsForwardableType 检查是否为需要转发的消息类型
// 可转发消息：客户端发送的消息需要直接转发给接收者，不需要经过业务处理
// 包括：窗口消息（打开/关闭）、状态消息（输入状态、已读、送达、确认、反应、编辑、撤回）等需要实时转发的消息
func (t MessageType) IsForwardableType() bool {
	// 窗口消息需要转发
	if t.IsWindowType() {
		return true
	}

	// 状态消息需要实时转发
	if t.IsStatusType() {
		return true
	}

	return false
}

// IsUserType 检查是否为用户相关类型消息
func (t MessageType) IsUserType() bool {
	switch t {
	case MessageTypeUserJoined, MessageTypeUserLeft, MessageTypeUserStatusChanged, MessageTypeCheckUserStatus,
		MessageTypeUserStatusResponse, MessageTypeGetOnlineUsers, MessageTypeOnlineUsersList, MessageTypeGetUserInfo,
		MessageTypeUserInfoResponse:
		return true
	default:
		return false
	}
}

// IsConfigType 检查是否为配置相关类型消息
func (t MessageType) IsConfigType() bool {
	switch t {
	case MessageTypeClientConfig, MessageTypeConfigUpdate:
		return true
	default:
		return false
	}
}

// IsHealthType 检查是否为健康检查相关类型消息
func (t MessageType) IsHealthType() bool {
	switch t {
	case MessageTypeHealthCheck, MessageTypeHealthResponse:
		return true
	default:
		return false
	}
}

// IsServerType 检查是否为服务器相关类型消息
func (t MessageType) IsServerType() bool {
	switch t {
	case MessageTypeServerStatus, MessageTypeServerStats:
		return true
	default:
		return false
	}
}

// IsRecallType 检查是否为消息撤回/编辑/反应等类型
func (t MessageType) IsRecallType() bool {
	switch t {
	case MessageTypeRecall, MessageTypeEdit, MessageTypeReaction:
		return true
	default:
		return false
	}
}

// IsThreadType 检查是否为线程/回复相关类型
func (t MessageType) IsThreadType() bool {
	switch t {
	case MessageTypeThread, MessageTypeReply:
		return true
	default:
		return false
	}
}

// IsCustomType 检查是否为自定义/未知类型
func (t MessageType) IsCustomType() bool {
	switch t {
	case MessageTypeCustom, MessageTypeUnknown:
		return true
	default:
		return false
	}
}

// GetCategory 获取消息类型分类（扩展）
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
	if t.IsBusinessType() {
		return "business"
	}
	if t.IsSessionType() {
		return "session"
	}
	if t.IsWindowType() {
		return "window"
	}
	if t.IsUserType() {
		return "user"
	}
	if t.IsConfigType() {
		return "config"
	}
	if t.IsHealthType() {
		return "health"
	}
	if t.IsServerType() {
		return "server"
	}
	if t.IsRecallType() {
		return "recall"
	}
	if t.IsThreadType() {
		return "thread"
	}
	if t.IsCustomType() {
		return "custom"
	}
	return "other"
}

// GetDefaultPriority 根据消息类型获取默认优先级
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
		t == MessageTypeTask || t == MessageTypeRecall || t == MessageTypeTicketAssigned ||
		t == MessageTypeTicketCreated || t == MessageTypeTicketTimeoutClosed ||
		t == MessageTypeTicketClosed || t == MessageTypeTicketTransfer || t == MessageTypeTicketActive || t == MessageTypeTest ||
		t == MessageTypeWelcome || t == MessageTypeTerminate || t == MessageTypeTransferred || t == MessageTypeSessionCreated ||
		t == MessageTypeSessionClosed || t == MessageTypeSessionQueued || t == MessageTypeSessionTimeout ||
		t == MessageTypeSessionPaused || t == MessageTypeSessionResumed || t == MessageTypeSessionTransferred ||
		t == MessageTypeSessionMemberJoined || t == MessageTypeSessionMemberLeft || t == MessageTypeSessionStatusChanged:
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
		t == MessageTypeThread || t == MessageTypeMention || t == MessageTypeOpenWindow ||
		t == MessageTypeCloseWindow:
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
		MessageTypeMention, MessageTypeCustom, MessageTypeTicketAssigned, MessageTypeTicketClosed,
		MessageTypeTicketTransfer, MessageTypeTicketActive, MessageTypeTest, MessageTypeWelcome, MessageTypeTerminate, MessageTypeTransferred,
		MessageTypeSessionCreated, MessageTypeSessionClosed, MessageTypeSessionQueued, MessageTypeSessionTimeout,
		MessageTypeCheckUserStatus, MessageTypeUserStatusResponse, MessageTypeGetOnlineUsers, MessageTypeOnlineUsersList,
		MessageTypeGetUserInfo, MessageTypeUserInfoResponse, MessageTypeSystemQuery, MessageTypeSystemResponse,
		MessageTypeUserJoined, MessageTypeUserLeft, MessageTypeUserStatusChanged, MessageTypeServerStatus,
		MessageTypeServerStats, MessageTypeClientConfig, MessageTypeConfigUpdate, MessageTypeHealthCheck,
		MessageTypeHealthResponse, MessageTypeConnected, MessageTypeClientRegistered, MessageTypeDisconnected, MessageTypeReconnected, MessageTypeConnectionError,
		MessageTypeConnectionTimeout, MessageTypeKickOut, MessageTypeForceOffline, MessageTypeCloseWindow, MessageTypeOpenWindow,
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

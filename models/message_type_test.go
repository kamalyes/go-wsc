/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-10 00:00:00
 * @FilePath: \go-wsc\models\message_type_test.go
 * @Description: 消息类型相关测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMessageType_StringAndIsValid 验证 String 与 IsValid
func TestMessageType_StringAndIsValid(t *testing.T) {
	// String
	assert.Equal(t, "text", MessageTypeText.String())
	assert.Equal(t, "image", MessageTypeImage.String())

	// IsValid 有效类型
	validTypes := []MessageType{
		MessageTypeText, MessageTypeImage, MessageTypeFile, MessageTypeAudio, MessageTypeVideo,
		MessageTypeSystem, MessageTypeNotice, MessageTypeEvent, MessageTypeAck, MessageTypeLocation,
		MessageTypeHeartbeat, MessageTypePing, MessageTypePong, MessageTypeTyping, MessageTypeRead,
		MessageTypeDelivered, MessageTypeRecall, MessageTypeEdit, MessageTypeReaction,
		MessageTypeOpenWindow, MessageTypeCloseWindow, MessageTypeBStatusReminder,
		MessageTypeConnected, MessageTypeDisconnected, MessageTypeKickOut, MessageTypeForceOffline,
		MessageTypeSessionCreated, MessageTypeSessionClosed, MessageTypePayment, MessageTypeOrder,
		MessageTypeClientRegistered, MessageTypeConnectionRejected, MessageTypeReconnected,
		MessageTypeConnectionError, MessageTypeConnectionTimeout, MessageTypeUserJoined,
		MessageTypeUserLeft, MessageTypeUserStatusChanged, MessageTypeServerStatus, MessageTypeServerStats,
		MessageTypeClientConfig, MessageTypeConfigUpdate, MessageTypeHealthCheck, MessageTypeHealthResponse,
	}
	for _, mt := range validTypes {
		assert.True(t, mt.IsValid(), "%s 应有效", mt)
	}

	// 无效类型
	assert.False(t, MessageType("invalid").IsValid())
	assert.False(t, MessageType("").IsValid())
}

// TestMessageType_IsMediaType 验证媒体类型判定
func TestMessageType_IsMediaType(t *testing.T) {
	mediaTypes := []MessageType{
		MessageTypeImage, MessageTypeAudio, MessageTypeVideo, MessageTypeFile,
		MessageTypeVoice, MessageTypeGIF, MessageTypeDocument, MessageTypeSpreadsheet,
		MessageTypePresentation,
	}
	for _, mt := range mediaTypes {
		assert.True(t, mt.IsMediaType(), "%s 应为媒体类型", mt)
	}
	// 非媒体类型
	assert.False(t, MessageTypeText.IsMediaType())
	assert.False(t, MessageTypeSystem.IsMediaType())
	assert.False(t, MessageTypeHeartbeat.IsMediaType())
}

// TestMessageType_IsTextType 验证文本类型判定
func TestMessageType_IsTextType(t *testing.T) {
	textTypes := []MessageType{
		MessageTypeText, MessageTypeMarkdown, MessageTypeRichText, MessageTypeCode,
	}
	for _, mt := range textTypes {
		assert.True(t, mt.IsTextType(), "%s 应为文本类型", mt)
	}
	assert.False(t, MessageTypeImage.IsTextType())
	assert.False(t, MessageTypeSystem.IsTextType())
}

// TestMessageType_IsSystemType 验证系统类型判定
func TestMessageType_IsSystemType(t *testing.T) {
	systemTypes := []MessageType{
		MessageTypeSystem, MessageTypeNotice, MessageTypeEvent, MessageTypeAnnouncement,
		MessageTypeAlert, MessageTypeError, MessageTypeInfo, MessageTypeSuccess,
		MessageTypeWarning, MessageTypeHeartbeat, MessageTypePing, MessageTypePong, MessageTypeBStatusReminder,
	}
	for _, mt := range systemTypes {
		assert.True(t, mt.IsSystemType(), "%s 应为系统类型", mt)
	}
	assert.False(t, MessageTypeText.IsSystemType())
	assert.False(t, MessageTypeImage.IsSystemType())
}

// TestMessageType_IsInteractiveType 验证交互类型判定
func TestMessageType_IsInteractiveType(t *testing.T) {
	interactiveTypes := []MessageType{
		MessageTypeCard, MessageTypeLink, MessageTypeQuote, MessageTypeCommand,
		MessageTypePoll, MessageTypeForm, MessageTypeTask, MessageTypeInvite,
	}
	for _, mt := range interactiveTypes {
		assert.True(t, mt.IsInteractiveType(), "%s 应为交互类型", mt)
	}
	assert.False(t, MessageTypeText.IsInteractiveType())
	assert.False(t, MessageTypeImage.IsInteractiveType())
}

// TestMessageType_IsStatusType 验证状态类型判定
func TestMessageType_IsStatusType(t *testing.T) {
	statusTypes := []MessageType{
		MessageTypeTyping, MessageTypeRead, MessageTypeDelivered, MessageTypeAck,
		MessageTypeReaction, MessageTypeEdit, MessageTypeRecall,
	}
	for _, mt := range statusTypes {
		assert.True(t, mt.IsStatusType(), "%s 应为状态类型", mt)
	}
	assert.False(t, MessageTypeText.IsStatusType())
	assert.False(t, MessageTypeSystem.IsStatusType())
}

// TestMessageType_IsHeartbeatType 验证心跳类型判定
func TestMessageType_IsHeartbeatType(t *testing.T) {
	heartbeatTypes := []MessageType{MessageTypePing, MessageTypePong, MessageTypeHeartbeat}
	for _, mt := range heartbeatTypes {
		assert.True(t, mt.IsHeartbeatType(), "%s 应为心跳类型", mt)
	}
	assert.False(t, MessageTypeText.IsHeartbeatType())
	assert.False(t, MessageTypeSystem.IsHeartbeatType())
}

// TestMessageType_IsSessionType 验证会话类型判定
func TestMessageType_IsSessionType(t *testing.T) {
	sessionTypes := []MessageType{
		MessageTypeSessionCreated, MessageTypeSessionClosed, MessageTypeSessionQueued,
		MessageTypeSessionTimeout, MessageTypeSessionPaused, MessageTypeSessionResumed,
		MessageTypeSessionTransferred, MessageTypeSessionMemberJoined, MessageTypeSessionMemberLeft,
		MessageTypeSessionStatusChanged,
	}
	for _, mt := range sessionTypes {
		assert.True(t, mt.IsSessionType(), "%s 应为会话类型", mt)
	}
	assert.False(t, MessageTypeText.IsSessionType())
	assert.False(t, MessageTypeSystem.IsSessionType())
}

// TestMessageType_IsConnectionType 验证连接类型判定
func TestMessageType_IsConnectionType(t *testing.T) {
	connTypes := []MessageType{
		MessageTypeConnected, MessageTypeClientRegistered, MessageTypeConnectionRejected,
		MessageTypeDisconnected, MessageTypeReconnected, MessageTypeConnectionError,
		MessageTypeConnectionTimeout, MessageTypeKickOut, MessageTypeForceOffline,
	}
	for _, mt := range connTypes {
		assert.True(t, mt.IsConnectionType(), "%s 应为连接类型", mt)
	}
	assert.False(t, MessageTypeText.IsConnectionType())
	assert.False(t, MessageTypeSystem.IsConnectionType())
}

// TestMessageType_ShouldSkipDatabaseRecord 验证是否跳过数据库记录
func TestMessageType_ShouldSkipDatabaseRecord(t *testing.T) {
	// 状态、连接、系统类型都应跳过
	skipTypes := []MessageType{
		MessageTypeTyping, MessageTypeRead, MessageTypeAck, // 状态
		MessageTypeConnected, MessageTypeDisconnected, MessageTypeKickOut, // 连接
		MessageTypeHeartbeat, MessageTypePing, MessageTypePong, MessageTypeSystem, // 系统
	}
	for _, mt := range skipTypes {
		assert.True(t, mt.ShouldSkipDatabaseRecord(), "%s 应跳过数据库记录", mt)
	}
	// 业务消息不应跳过
	assert.False(t, MessageTypeText.ShouldSkipDatabaseRecord())
	assert.False(t, MessageTypeImage.ShouldSkipDatabaseRecord())
	assert.False(t, MessageTypePayment.ShouldSkipDatabaseRecord())
}

// TestMessageType_IsBusinessType 验证业务类型判定
func TestMessageType_IsBusinessType(t *testing.T) {
	businessTypes := []MessageType{
		MessageTypePayment, MessageTypeOrder, MessageTypeProduct, MessageTypeTicketCreated,
		MessageTypeTicketAssigned, MessageTypeTicketClosed, MessageTypeTicketTimeoutClosed,
		MessageTypeTicketTransfer, MessageTypeTicketActive, MessageTypeBStatusReminder,
	}
	for _, mt := range businessTypes {
		assert.True(t, mt.IsBusinessType(), "%s 应为业务类型", mt)
	}
	assert.False(t, MessageTypeText.IsBusinessType())
	assert.False(t, MessageTypeSystem.IsBusinessType())
}

// TestMessageType_IsWindowType 验证窗口类型判定
func TestMessageType_IsWindowType(t *testing.T) {
	assert.True(t, MessageTypeOpenWindow.IsWindowType())
	assert.True(t, MessageTypeCloseWindow.IsWindowType())
	assert.False(t, MessageTypeText.IsWindowType())
	assert.False(t, MessageTypeSystem.IsWindowType())
}

// TestMessageType_GetEmoji 验证 emoji 获取
func TestMessageType_GetEmoji(t *testing.T) {
	// 已注册的 emoji
	assert.Equal(t, "🟢", MessageTypeOpenWindow.GetEmoji())
	assert.Equal(t, "🔴", MessageTypeCloseWindow.GetEmoji())
	assert.Equal(t, "⌨️", MessageTypeTyping.GetEmoji())
	assert.Equal(t, "👁️", MessageTypeRead.GetEmoji())
	assert.Equal(t, "✅", MessageTypeDelivered.GetEmoji())
	assert.Equal(t, "✔️", MessageTypeAck.GetEmoji())
	assert.Equal(t, "❤️", MessageTypeReaction.GetEmoji())
	assert.Equal(t, "✏️", MessageTypeEdit.GetEmoji())
	assert.Equal(t, "↩️", MessageTypeRecall.GetEmoji())
	assert.Equal(t, "⏰", MessageTypeBStatusReminder.GetEmoji())

	// 未注册返回默认值
	assert.Equal(t, "🔄", MessageTypeText.GetEmoji())
	assert.Equal(t, "🔄", MessageTypeImage.GetEmoji())
	assert.Equal(t, "🔄", MessageType("invalid").GetEmoji())
}

// TestMessageType_IsForwardableType 验证可转发类型
func TestMessageType_IsForwardableType(t *testing.T) {
	// 窗口消息和状态消息可转发
	forwardable := []MessageType{
		MessageTypeOpenWindow, MessageTypeCloseWindow,
		MessageTypeTyping, MessageTypeRead, MessageTypeDelivered, MessageTypeAck,
		MessageTypeReaction, MessageTypeEdit, MessageTypeRecall,
	}
	for _, mt := range forwardable {
		assert.True(t, mt.IsForwardableType(), "%s 应可转发", mt)
	}
	// 非可转发
	assert.False(t, MessageTypeText.IsForwardableType())
	assert.False(t, MessageTypeImage.IsForwardableType())
	assert.False(t, MessageTypeSystem.IsForwardableType())
}

// TestMessageType_IsUserType 验证用户相关类型
func TestMessageType_IsUserType(t *testing.T) {
	userTypes := []MessageType{
		MessageTypeUserJoined, MessageTypeUserLeft, MessageTypeUserStatusChanged,
		MessageTypeCheckUserStatus, MessageTypeUserStatusResponse, MessageTypeGetOnlineUsers,
		MessageTypeOnlineUsersList, MessageTypeGetUserInfo, MessageTypeUserInfoResponse,
	}
	for _, mt := range userTypes {
		assert.True(t, mt.IsUserType(), "%s 应为用户类型", mt)
	}
	assert.False(t, MessageTypeText.IsUserType())
	assert.False(t, MessageTypeSystem.IsUserType())
}

// TestMessageType_IsConfigType 验证配置类型
func TestMessageType_IsConfigType(t *testing.T) {
	assert.True(t, MessageTypeClientConfig.IsConfigType())
	assert.True(t, MessageTypeConfigUpdate.IsConfigType())
	assert.False(t, MessageTypeText.IsConfigType())
}

// TestMessageType_IsHealthType 验证健康检查类型
func TestMessageType_IsHealthType(t *testing.T) {
	assert.True(t, MessageTypeHealthCheck.IsHealthType())
	assert.True(t, MessageTypeHealthResponse.IsHealthType())
	assert.False(t, MessageTypeText.IsHealthType())
}

// TestMessageType_IsServerType 验证服务器类型
func TestMessageType_IsServerType(t *testing.T) {
	assert.True(t, MessageTypeServerStatus.IsServerType())
	assert.True(t, MessageTypeServerStats.IsServerType())
	assert.False(t, MessageTypeText.IsServerType())
}

// TestMessageType_IsRecallType 验证撤回类型
func TestMessageType_IsRecallType(t *testing.T) {
	assert.True(t, MessageTypeRecall.IsRecallType())
	assert.True(t, MessageTypeEdit.IsRecallType())
	assert.True(t, MessageTypeReaction.IsRecallType())
	assert.False(t, MessageTypeText.IsRecallType())
}

// TestMessageType_IsThreadType 验证线程类型
func TestMessageType_IsThreadType(t *testing.T) {
	assert.True(t, MessageTypeThread.IsThreadType())
	assert.True(t, MessageTypeReply.IsThreadType())
	assert.False(t, MessageTypeText.IsThreadType())
}

// TestMessageType_IsCustomType 验证自定义类型
func TestMessageType_IsCustomType(t *testing.T) {
	assert.True(t, MessageTypeCustom.IsCustomType())
	assert.True(t, MessageTypeUnknown.IsCustomType())
	assert.False(t, MessageTypeText.IsCustomType())
}

// TestMessageType_GetCategory 验证分类获取
func TestMessageType_GetCategory(t *testing.T) {
	cases := []struct {
		mt       MessageType
		category string
	}{
		{MessageTypeImage, "media"},
		{MessageTypeText, "text"},
		{MessageTypeSystem, "system"},
		{MessageTypeCard, "interactive"},
		{MessageTypeTyping, "status"},
		{MessageTypePayment, "business"},
		{MessageTypeSessionCreated, "session"},
		{MessageTypeOpenWindow, "window"},
		{MessageTypeUserJoined, "user"},
		{MessageTypeClientConfig, "config"},
		{MessageTypeHealthCheck, "health"},
		{MessageTypeServerStatus, "server"},
		{MessageTypeRecall, "recall"},
		{MessageTypeThread, "thread"},
		{MessageTypeCustom, "custom"},
		{MessageType("invalid"), "other"},
	}
	for _, c := range cases {
		assert.Equal(t, c.category, c.mt.GetCategory(), "%s 分类应为 %s", c.mt, c.category)
	}
}

// TestMessageType_GetDefaultPriority 验证默认优先级
func TestMessageType_GetDefaultPriority(t *testing.T) {
	cases := []struct {
		mt       MessageType
		priority MessagePriority
	}{
		// 关键优先级
		{MessageTypeError, MessagePriorityCritical},
		{MessageTypeAlert, MessagePriorityCritical},
		// 紧急优先级
		{MessageTypeSystem, MessagePriorityUrgent},
		{MessageTypeAnnouncement, MessagePriorityUrgent},
		{MessageTypeWarning, MessagePriorityUrgent},
		// 高优先级
		{MessageTypeNotice, MessagePriorityHigh},
		{MessageTypeEvent, MessagePriorityHigh},
		{MessageTypePayment, MessagePriorityHigh},
		{MessageTypeTicketCreated, MessagePriorityHigh},
		{MessageTypeSessionCreated, MessagePriorityHigh},
		{MessageTypeWelcome, MessagePriorityHigh},
		// 普通优先级
		{MessageTypeText, MessagePriorityNormal},
		{MessageTypeImage, MessagePriorityNormal},
		{MessageTypeMarkdown, MessagePriorityNormal},
		{MessageTypeOpenWindow, MessagePriorityNormal},
		{MessageTypeCloseWindow, MessagePriorityNormal},
		// 低优先级
		{MessageTypeTyping, MessagePriorityLow},
		{MessageTypeRead, MessagePriorityLow},
		{MessageTypeAck, MessagePriorityLow},
		{MessageTypeHeartbeat, MessagePriorityLow},
		{MessageTypePing, MessagePriorityLow},
		{MessageTypePong, MessagePriorityLow},
		{MessageTypeInfo, MessagePriorityLow},
		{MessageTypeEdit, MessagePriorityLow},
		{MessageTypeReaction, MessagePriorityLow},
		{MessageTypeJson, MessagePriorityLow},
		{MessageTypeCustom, MessagePriorityLow},
		// 默认普通优先级
		{MessageType("invalid"), MessagePriorityNormal},
	}
	for _, c := range cases {
		assert.Equal(t, c.priority, c.mt.GetDefaultPriority(), "%s 优先级应为 %s", c.mt, c.priority)
	}
}

// TestMessagePriority 验证消息优先级方法
func TestMessagePriority(t *testing.T) {
	// String
	assert.Equal(t, "low", MessagePriorityLow.String())
	assert.Equal(t, "normal", MessagePriorityNormal.String())
	assert.Equal(t, "high", MessagePriorityHigh.String())
	assert.Equal(t, "urgent", MessagePriorityUrgent.String())
	assert.Equal(t, "critical", MessagePriorityCritical.String())
	assert.Equal(t, "unknown", MessagePriority(99).String())

	// GetWeight
	assert.Equal(t, 1, MessagePriorityLow.GetWeight())
	assert.Equal(t, 2, MessagePriorityNormal.GetWeight())
	assert.Equal(t, 3, MessagePriorityHigh.GetWeight())
	assert.Equal(t, 4, MessagePriorityUrgent.GetWeight())
	assert.Equal(t, 5, MessagePriorityCritical.GetWeight())

	// IsHigherThan
	assert.True(t, MessagePriorityCritical.IsHigherThan(MessagePriorityLow))
	assert.True(t, MessagePriorityHigh.IsHigherThan(MessagePriorityNormal))
	assert.False(t, MessagePriorityLow.IsHigherThan(MessagePriorityCritical))
	assert.False(t, MessagePriorityNormal.IsHigherThan(MessagePriorityNormal))
}

// TestGetAllMessageTypes 验证获取所有消息类型
func TestGetAllMessageTypes(t *testing.T) {
	types := GetAllMessageTypes()
	assert.NotEmpty(t, types)
	// 验证所有类型都有效
	for _, mt := range types {
		assert.True(t, mt.IsValid(), "%s 应有效", mt)
	}
}

// TestGetMessageTypesByCategory 验证按分类获取消息类型
func TestGetMessageTypesByCategory(t *testing.T) {
	categories := []string{"media", "text", "system", "interactive", "status", "business", "session", "window", "user", "config", "health", "server", "recall", "thread", "custom", "other"}
	for _, cat := range categories {
		types := GetMessageTypesByCategory(cat)
		for _, mt := range types {
			assert.Equal(t, cat, mt.GetCategory())
		}
	}

	// media 分类应包含图片
	mediaTypes := GetMessageTypesByCategory("media")
	found := false
	for _, mt := range mediaTypes {
		if mt == MessageTypeImage {
			found = true
			break
		}
	}
	assert.True(t, found, "media 分类应包含 MessageTypeImage")
}

// TestGetMessageTypesByPriority 验证按优先级获取消息类型
func TestGetMessageTypesByPriority(t *testing.T) {
	// 关键优先级应包含 error 和 alert
	criticalTypes := GetMessageTypesByPriority(MessagePriorityCritical)
	assert.Contains(t, criticalTypes, MessageTypeError)
	assert.Contains(t, criticalTypes, MessageTypeAlert)

	// 紧急优先级应包含 system
	urgentTypes := GetMessageTypesByPriority(MessagePriorityUrgent)
	assert.Contains(t, urgentTypes, MessageTypeSystem)

	// 低优先级应包含 heartbeat
	lowTypes := GetMessageTypesByPriority(MessagePriorityLow)
	assert.Contains(t, lowTypes, MessageTypeHeartbeat)
}

// TestGetPriorityStats 验证优先级统计
func TestGetPriorityStats(t *testing.T) {
	stats := GetPriorityStats()
	assert.NotNil(t, stats)
	// 总数等于所有消息类型数
	allTypes := GetAllMessageTypes()
	assert.Equal(t, len(allTypes), stats.Total)
	// 各分类之和等于总数
	assert.Equal(t, stats.Total, stats.Critical+stats.Urgent+stats.High+stats.Normal+stats.Low)
	// 关键优先级应有 error 和 alert
	assert.GreaterOrEqual(t, stats.Critical, 2)
}

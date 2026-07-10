/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\validator.go
 * @Description: 枚举验证器集中管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import validator "github.com/kamalyes/go-argus/validate"

// 全局枚举验证器实例
var (
	// UserRoleValidator 用户角色验证器
	UserRoleValidator = validator.NewEnumValidator(
		UserRoleCustomer,
		UserRoleAgent,
		UserRoleAdmin,
	)

	// UserTypeValidator 用户类型验证器
	UserTypeValidator = validator.NewEnumValidator(
		UserTypeVisitor,
		UserTypeCustomer,
		UserTypeAgent,
		UserTypeAdmin,
		UserTypeBot,
		UserTypeVIP,
		UserTypeSystem,
	)

	// UserStatusValidator 用户状态验证器
	UserStatusValidator = validator.NewEnumValidator(
		UserStatusOnline,
		UserStatusOffline,
		UserStatusBusy,
		UserStatusAway,
		UserStatusInvisible,
	)

	// DisconnectReasonValidator 断开原因验证器
	DisconnectReasonValidator = validator.NewEnumValidator(
		DisconnectReasonReadError,
		DisconnectReasonWriteError,
		DisconnectReasonContextDone,
		DisconnectReasonCloseMessage,
		DisconnectReasonHeartbeatFail,
		DisconnectReasonKickOut,
		DisconnectReasonForceOffline,
		DisconnectReasonTimeout,
		DisconnectReasonClientRequest,
		DisconnectReasonServerShutdown,
		DisconnectReasonUnknown,
	)

	// ErrorSeverityValidator 错误严重程度验证器
	ErrorSeverityValidator = validator.NewEnumValidator(
		ErrorSeverityInfo,
		ErrorSeverityWarning,
		ErrorSeverityError,
		ErrorSeverityCritical,
		ErrorSeverityFatal,
	)

	// QueueTypeValidator 队列类型验证器
	QueueTypeValidator = validator.NewEnumValidator(
		QueueTypeBroadcast,
		QueueTypePending,
		QueueTypeAllQueues,
		QueueTypeMessageQueue,
		QueueTypeClientBuffer,
	)

	// MessageTypeValidator 消息类型验证器
	MessageTypeValidator = validator.NewEnumValidator(
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
		MessageTypeMention, MessageTypeCustom, MessageTypeUnknown, MessageTypeTicketAssigned,
		MessageTypeTicketClosed, MessageTypeTicketTransfer, MessageTypeTicketActive, MessageTypeTest,
		MessageTypeWelcome, MessageTypeTerminate, MessageTypeTransferred, MessageTypeSessionCreated,
		MessageTypeSessionClosed, MessageTypeSessionQueued, MessageTypeSessionTimeout, MessageTypeSessionPaused,
		MessageTypeSessionResumed, MessageTypeSessionTransferred, MessageTypeSessionMemberJoined,
		MessageTypeSessionMemberLeft, MessageTypeSessionStatusChanged, MessageTypeCheckUserStatus,
		MessageTypeUserStatusResponse, MessageTypeGetOnlineUsers, MessageTypeOnlineUsersList,
		MessageTypeGetUserInfo, MessageTypeUserInfoResponse, MessageTypeSystemQuery, MessageTypeSystemResponse,
		MessageTypeUserJoined, MessageTypeUserLeft, MessageTypeUserStatusChanged, MessageTypeServerStatus,
		MessageTypeServerStats, MessageTypeClientConfig, MessageTypeConfigUpdate, MessageTypeHealthCheck,
		MessageTypeHealthResponse, MessageTypeConnected, MessageTypeDisconnected, MessageTypeReconnected,
		MessageTypeConnectionError, MessageTypeConnectionTimeout, MessageTypeKickOut, MessageTypeForceOffline,
	)

	// PushTypeValidator 推送类型验证器
	PushTypeValidator = validator.NewEnumValidator(
		PushTypeDirect,
		PushTypeQueue,
		PushTypeOffline,
		PushTypeNone,
	)

	// BroadcastTypeValidator 广播类型验证器
	BroadcastTypeValidator = validator.NewEnumValidator(
		BroadcastTypeNone,
		BroadcastTypeSession,
		BroadcastTypeGlobal,
	)

	// VIPLevelValidator VIP等级验证器
	VIPLevelValidator = validator.NewEnumValidator(
		VIPLevelV0, VIPLevelV1, VIPLevelV2, VIPLevelV3,
		VIPLevelV4, VIPLevelV5, VIPLevelV6, VIPLevelV7, VIPLevelV8,
	)

	// UrgencyLevelValidator 紧急等级验证器
	UrgencyLevelValidator = validator.NewEnumValidator(
		UrgencyLevelLow,
		UrgencyLevelNormal,
		UrgencyLevelHigh,
	)

	// BusinessCategoryValidator 业务分类验证器
	BusinessCategoryValidator = validator.NewEnumValidator(
		BusinessCategoryGeneral,
		BusinessCategoryCustomer,
		BusinessCategorySales,
		BusinessCategoryTechnical,
		BusinessCategoryFinance,
		BusinessCategorySecurity,
		BusinessCategoryOperations,
		BusinessCategorySupport,
		BusinessCategoryIT,
		BusinessCategoryQuality,
		BusinessCategoryOther,
	)

	// MessageStatusValidator 消息状态验证器
	MessageStatusValidator = validator.NewEnumValidator(
		MessageStatusPending,
		MessageStatusSent,
		MessageStatusDelivered,
		MessageStatusRead,
		MessageStatusFailed,
	)

	// NodeStatusValidator 节点状态验证器
	NodeStatusValidator = validator.NewEnumValidator(
		NodeStatusActive,
		NodeStatusInactive,
		NodeStatusOffline,
	)

	// ConnectionStatusValidator 连接状态验证器
	ConnectionStatusValidator = validator.NewEnumValidator(
		ConnectionStatusConnecting,
		ConnectionStatusConnected,
		ConnectionStatusDisconnected,
		ConnectionStatusReconnecting,
		ConnectionStatusError,
	)

	// OperationTypeValidator 操作类型验证器
	OperationTypeValidator = validator.NewEnumValidator(
		OperationTypeJoin,
		OperationTypeLeave,
		OperationTypeMessage,
		OperationTypeBroadcast,
		OperationTypeNotify,
		OperationTypeHeartbeat,
		OperationTypeAuth,
		OperationTypeSync,
	)

	// ClientTypeValidator 客户端类型验证器
	ClientTypeValidator = validator.NewEnumValidator(
		ClientTypeWeb,
		ClientTypeMobile,
		ClientTypeDesktop,
		ClientTypeAPI,
	)

	// PriorityValidator 优先级验证器
	PriorityValidator = validator.NewEnumValidator(
		PriorityLow,
		PriorityNormal,
		PriorityHigh,
		PriorityUrgent,
		PriorityCritical,
	)

	// DepartmentValidator 部门验证器
	DepartmentValidator = validator.NewEnumValidator(
		DepartmentSales,
		DepartmentSupport,
		DepartmentBilling,
		DepartmentGeneral,
		DepartmentTechnical,
	)

	// SkillValidator 技能验证器
	SkillValidator = validator.NewEnumValidator(
		SkillTechnical,
		SkillSales,
		SkillBilling,
		SkillGeneral,
		SkillLanguageEN,
		SkillLanguageZH,
		SkillVIP,
	)

	// MessageSendStatusValidator 消息发送状态验证器
	MessageSendStatusValidator = validator.NewEnumValidator(
		MessageSendStatusPending,
		MessageSendStatusSending,
		MessageSendStatusSuccess,
		MessageSendStatusFailed,
		MessageSendStatusRetrying,
		MessageSendStatusAckTimeout,
		MessageSendStatusUserOffline,
		MessageSendStatusExpired,
	)

	// MessageSourceValidator 消息来源验证器
	MessageSourceValidator = validator.NewEnumValidator(
		MessageSourceOnline,
		MessageSourceOffline,
	)

	// PendingOfflineStatuses 待推送的离线消息状态列表
	// 用于查询未完成推送的离线消息
	PendingOfflineStatuses = []MessageSendStatus{
		MessageSendStatusUserOffline, // 用户离线(未推送)
		MessageSendStatusPending,     // 待发送
		MessageSendStatusFailed,      // 发送失败(待重试)
	}
)

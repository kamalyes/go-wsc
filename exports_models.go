/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 21:20:20
 * @FilePath: \go-wsc\exports_models.go
 * @Description: Models模块类型导出 - 保持向后兼容
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"github.com/kamalyes/go-wsc/models"
)

// ==================== 基础类型 ====================
type (
	IDGenerator        = models.IDGenerator
	HubStats           = models.HubStats
	DistributedMessage = models.DistributedMessage
)

// ==================== 枚举类型 ====================
type (
	UserRole         = models.UserRole
	UserType         = models.UserType
	UserStatus       = models.UserStatus
	DisconnectReason = models.DisconnectReason
	ErrorSeverity    = models.ErrorSeverity
	QueueType        = models.QueueType
	MessageStatus    = models.MessageStatus
	NodeStatus       = models.NodeStatus
	ConnectionStatus = models.ConnectionStatus
	OperationType    = models.OperationType
	ClientType       = models.ClientType
	ConnectionType   = models.ConnectionType
	Priority         = models.Priority
	Department       = models.Department
	Skill            = models.Skill
	PushType         = models.PushType
	BroadcastType    = models.BroadcastType
	VIPLevel         = models.VIPLevel
	UrgencyLevel     = models.UrgencyLevel
	BusinessCategory = models.BusinessCategory
)

// ==================== 枚举常量 - UserRole ====================
const (
	UserRoleCustomer = models.UserRoleCustomer
	UserRoleAgent    = models.UserRoleAgent
	UserRoleAdmin    = models.UserRoleAdmin
)

// ==================== 枚举常量 - UserType ====================
const (
	UserTypeVisitor  = models.UserTypeVisitor
	UserTypeCustomer = models.UserTypeCustomer
	UserTypeAgent    = models.UserTypeAgent
	UserTypeAdmin    = models.UserTypeAdmin
	UserTypeBot      = models.UserTypeBot
	UserTypeVIP      = models.UserTypeVIP
	UserTypeSystem   = models.UserTypeSystem
)

// ==================== 枚举常量 - UserStatus ====================
const (
	UserStatusOnline    = models.UserStatusOnline
	UserStatusOffline   = models.UserStatusOffline
	UserStatusBusy      = models.UserStatusBusy
	UserStatusAway      = models.UserStatusAway
	UserStatusInvisible = models.UserStatusInvisible
)

// ==================== 枚举常量 - DisconnectReason ====================
const (
	DisconnectReasonReadError      = models.DisconnectReasonReadError
	DisconnectReasonWriteError     = models.DisconnectReasonWriteError
	DisconnectReasonContextDone    = models.DisconnectReasonContextDone
	DisconnectReasonCloseMessage   = models.DisconnectReasonCloseMessage
	DisconnectReasonHeartbeatFail  = models.DisconnectReasonHeartbeatFail
	DisconnectReasonKickOut        = models.DisconnectReasonKickOut
	DisconnectReasonForceOffline   = models.DisconnectReasonForceOffline
	DisconnectReasonTimeout        = models.DisconnectReasonTimeout
	DisconnectReasonClientRequest  = models.DisconnectReasonClientRequest
	DisconnectReasonServerShutdown = models.DisconnectReasonServerShutdown
	DisconnectReasonUnknown        = models.DisconnectReasonUnknown
)

// ==================== 枚举常量 - ErrorSeverity ====================
const (
	ErrorSeverityInfo     = models.ErrorSeverityInfo
	ErrorSeverityWarning  = models.ErrorSeverityWarning
	ErrorSeverityError    = models.ErrorSeverityError
	ErrorSeverityCritical = models.ErrorSeverityCritical
	ErrorSeverityFatal    = models.ErrorSeverityFatal
)

// ==================== 枚举常量 - QueueType ====================
const (
	QueueTypeBroadcast    = models.QueueTypeBroadcast
	QueueTypePending      = models.QueueTypePending
	QueueTypeAllQueues    = models.QueueTypeAllQueues
	QueueTypeMessageQueue = models.QueueTypeMessageQueue
	QueueTypeClientBuffer = models.QueueTypeClientBuffer
)

// ==================== 枚举常量 - MessageStatus ====================
const (
	MessageStatusPending   = models.MessageStatusPending
	MessageStatusSent      = models.MessageStatusSent
	MessageStatusDelivered = models.MessageStatusDelivered
	MessageStatusRead      = models.MessageStatusRead
	MessageStatusFailed    = models.MessageStatusFailed
)

// ==================== 枚举常量 - NodeStatus ====================
const (
	NodeStatusActive   = models.NodeStatusActive
	NodeStatusInactive = models.NodeStatusInactive
	NodeStatusOffline  = models.NodeStatusOffline
)

// ==================== 枚举常量 - ConnectionStatus ====================
const (
	ConnectionStatusConnecting   = models.ConnectionStatusConnecting
	ConnectionStatusConnected    = models.ConnectionStatusConnected
	ConnectionStatusDisconnected = models.ConnectionStatusDisconnected
	ConnectionStatusReconnecting = models.ConnectionStatusReconnecting
	ConnectionStatusError        = models.ConnectionStatusError
)

// ==================== 枚举常量 - OperationType ====================
const (
	OperationTypeJoin      = models.OperationTypeJoin
	OperationTypeLeave     = models.OperationTypeLeave
	OperationTypeMessage   = models.OperationTypeMessage
	OperationTypeBroadcast = models.OperationTypeBroadcast
	OperationTypeNotify    = models.OperationTypeNotify
	OperationTypeHeartbeat = models.OperationTypeHeartbeat
	OperationTypeAuth      = models.OperationTypeAuth
	OperationTypeSync      = models.OperationTypeSync
)

// ==================== 枚举常量 - ClientType ====================
const (
	ClientTypeWeb     = models.ClientTypeWeb
	ClientTypeMobile  = models.ClientTypeMobile
	ClientTypeDesktop = models.ClientTypeDesktop
	ClientTypeAPI     = models.ClientTypeAPI
)

// ==================== 枚举常量 - ConnectionType ====================
const (
	ConnectionTypeWebSocket = models.ConnectionTypeWebSocket
	ConnectionTypeSSE       = models.ConnectionTypeSSE
)

// ==================== 枚举常量 - Priority ====================
const (
	PriorityLow      = models.PriorityLow
	PriorityNormal   = models.PriorityNormal
	PriorityHigh     = models.PriorityHigh
	PriorityUrgent   = models.PriorityUrgent
	PriorityCritical = models.PriorityCritical
)

// ==================== 枚举常量 - Department ====================
const (
	DepartmentSales     = models.DepartmentSales
	DepartmentSupport   = models.DepartmentSupport
	DepartmentBilling   = models.DepartmentBilling
	DepartmentGeneral   = models.DepartmentGeneral
	DepartmentTechnical = models.DepartmentTechnical
)

// ==================== 枚举常量 - Skill ====================
const (
	SkillTechnical  = models.SkillTechnical
	SkillSales      = models.SkillSales
	SkillBilling    = models.SkillBilling
	SkillGeneral    = models.SkillGeneral
	SkillLanguageEN = models.SkillLanguageEN
	SkillLanguageZH = models.SkillLanguageZH
	SkillVIP        = models.SkillVIP
)

// ==================== 枚举常量 - PushType ====================
const (
	PushTypeNone    = models.PushTypeNone
	PushTypeDirect  = models.PushTypeDirect
	PushTypeQueue   = models.PushTypeQueue
	PushTypeOffline = models.PushTypeOffline
	PushTypeUnicast = models.PushTypeUnicast
)

// ==================== 枚举常量 - BroadcastType ====================
const (
	BroadcastTypeNone    = models.BroadcastTypeNone
	BroadcastTypeSession = models.BroadcastTypeSession
	BroadcastTypeGlobal  = models.BroadcastTypeGlobal
)

// ==================== 枚举常量 - VIPLevel ====================
const (
	VIPLevelV0 = models.VIPLevelV0
	VIPLevelV1 = models.VIPLevelV1
	VIPLevelV2 = models.VIPLevelV2
	VIPLevelV3 = models.VIPLevelV3
	VIPLevelV4 = models.VIPLevelV4
	VIPLevelV5 = models.VIPLevelV5
	VIPLevelV6 = models.VIPLevelV6
	VIPLevelV7 = models.VIPLevelV7
	VIPLevelV8 = models.VIPLevelV8
)

// ==================== 枚举常量 - UrgencyLevel ====================
const (
	UrgencyLevelLow    = models.UrgencyLevelLow
	UrgencyLevelNormal = models.UrgencyLevelNormal
	UrgencyLevelHigh   = models.UrgencyLevelHigh
)

// ==================== 枚举常量 - BusinessCategory ====================
const (
	BusinessCategoryGeneral    = models.BusinessCategoryGeneral
	BusinessCategoryCustomer   = models.BusinessCategoryCustomer
	BusinessCategorySales      = models.BusinessCategorySales
	BusinessCategoryTechnical  = models.BusinessCategoryTechnical
	BusinessCategoryFinance    = models.BusinessCategoryFinance
	BusinessCategorySecurity   = models.BusinessCategorySecurity
	BusinessCategoryOperations = models.BusinessCategoryOperations
	BusinessCategorySupport    = models.BusinessCategorySupport
	BusinessCategoryIT         = models.BusinessCategoryIT
	BusinessCategoryQuality    = models.BusinessCategoryQuality
	BusinessCategoryOther      = models.BusinessCategoryOther
)

// ==================== 枚举工具函数 ====================
var (
	GetAllVIPLevels          = models.GetAllVIPLevels
	GetAllUrgencyLevels      = models.GetAllUrgencyLevels
	GetAllBusinessCategories = models.GetAllBusinessCategories
)

// ==================== 消息类型 ====================
type (
	MessageType     = models.MessageType
	MessagePriority = models.MessagePriority
	PriorityStats   = models.PriorityStats
)

// ==================== 消息类型常量 (100+ constants) ====================
const (
	MessageTypeText                 = models.MessageTypeText
	MessageTypeImage                = models.MessageTypeImage
	MessageTypeFile                 = models.MessageTypeFile
	MessageTypeAudio                = models.MessageTypeAudio
	MessageTypeVideo                = models.MessageTypeVideo
	MessageTypeSystem               = models.MessageTypeSystem
	MessageTypeNotice               = models.MessageTypeNotice
	MessageTypeEvent                = models.MessageTypeEvent
	MessageTypeAck                  = models.MessageTypeAck
	MessageTypeLocation             = models.MessageTypeLocation
	MessageTypeCard                 = models.MessageTypeCard
	MessageTypeEmoji                = models.MessageTypeEmoji
	MessageTypeSticker              = models.MessageTypeSticker
	MessageTypeLink                 = models.MessageTypeLink
	MessageTypeQuote                = models.MessageTypeQuote
	MessageTypeForward              = models.MessageTypeForward
	MessageTypeCommand              = models.MessageTypeCommand
	MessageTypeMarkdown             = models.MessageTypeMarkdown
	MessageTypeRichText             = models.MessageTypeRichText
	MessageTypeCode                 = models.MessageTypeCode
	MessageTypeJson                 = models.MessageTypeJson
	MessageTypeXML                  = models.MessageTypeXML
	MessageTypeBinary               = models.MessageTypeBinary
	MessageTypeVoice                = models.MessageTypeVoice
	MessageTypeGIF                  = models.MessageTypeGIF
	MessageTypeDocument             = models.MessageTypeDocument
	MessageTypeSpreadsheet          = models.MessageTypeSpreadsheet
	MessageTypePresentation         = models.MessageTypePresentation
	MessageTypeContact              = models.MessageTypeContact
	MessageTypeCalendar             = models.MessageTypeCalendar
	MessageTypeTask                 = models.MessageTypeTask
	MessageTypePoll                 = models.MessageTypePoll
	MessageTypeForm                 = models.MessageTypeForm
	MessageTypePayment              = models.MessageTypePayment
	MessageTypeOrder                = models.MessageTypeOrder
	MessageTypeProduct              = models.MessageTypeProduct
	MessageTypeInvite               = models.MessageTypeInvite
	MessageTypeAnnouncement         = models.MessageTypeAnnouncement
	MessageTypeAlert                = models.MessageTypeAlert
	MessageTypeError                = models.MessageTypeError
	MessageTypeInfo                 = models.MessageTypeInfo
	MessageTypeSuccess              = models.MessageTypeSuccess
	MessageTypeWarning              = models.MessageTypeWarning
	MessageTypeHeartbeat            = models.MessageTypeHeartbeat
	MessageTypePing                 = models.MessageTypePing
	MessageTypePong                 = models.MessageTypePong
	MessageTypeTyping               = models.MessageTypeTyping
	MessageTypeRead                 = models.MessageTypeRead
	MessageTypeDelivered            = models.MessageTypeDelivered
	MessageTypeRecall               = models.MessageTypeRecall
	MessageTypeEdit                 = models.MessageTypeEdit
	MessageTypeReaction             = models.MessageTypeReaction
	MessageTypeThread               = models.MessageTypeThread
	MessageTypeReply                = models.MessageTypeReply
	MessageTypeMention              = models.MessageTypeMention
	MessageTypeCustom               = models.MessageTypeCustom
	MessageTypeUnknown              = models.MessageTypeUnknown
	MessageTypeTicketCreated        = models.MessageTypeTicketCreated
	MessageTypeTicketAssigned       = models.MessageTypeTicketAssigned
	MessageTypeTicketClosed         = models.MessageTypeTicketClosed
	MessageTypeTicketTimeoutClosed  = models.MessageTypeTicketTimeoutClosed
	MessageTypeTicketTransfer       = models.MessageTypeTicketTransfer
	MessageTypeTicketActive         = models.MessageTypeTicketActive
	MessageTypeTest                 = models.MessageTypeTest
	MessageTypeWelcome              = models.MessageTypeWelcome
	MessageTypeTerminate            = models.MessageTypeTerminate
	MessageTypeTransferred          = models.MessageTypeTransferred
	MessageTypeSessionCreated       = models.MessageTypeSessionCreated
	MessageTypeSessionClosed        = models.MessageTypeSessionClosed
	MessageTypeSessionQueued        = models.MessageTypeSessionQueued
	MessageTypeSessionTimeout       = models.MessageTypeSessionTimeout
	MessageTypeSessionPaused        = models.MessageTypeSessionPaused
	MessageTypeSessionResumed       = models.MessageTypeSessionResumed
	MessageTypeSessionTransferred   = models.MessageTypeSessionTransferred
	MessageTypeSessionMemberJoined  = models.MessageTypeSessionMemberJoined
	MessageTypeSessionMemberLeft    = models.MessageTypeSessionMemberLeft
	MessageTypeSessionStatusChanged = models.MessageTypeSessionStatusChanged
	MessageTypeCheckUserStatus      = models.MessageTypeCheckUserStatus
	MessageTypeUserStatusResponse   = models.MessageTypeUserStatusResponse
	MessageTypeGetOnlineUsers       = models.MessageTypeGetOnlineUsers
	MessageTypeOnlineUsersList      = models.MessageTypeOnlineUsersList
	MessageTypeGetUserInfo          = models.MessageTypeGetUserInfo
	MessageTypeUserInfoResponse     = models.MessageTypeUserInfoResponse
	MessageTypeSystemQuery          = models.MessageTypeSystemQuery
	MessageTypeSystemResponse       = models.MessageTypeSystemResponse
	MessageTypeUserJoined           = models.MessageTypeUserJoined
	MessageTypeUserLeft             = models.MessageTypeUserLeft
	MessageTypeUserStatusChanged    = models.MessageTypeUserStatusChanged
	MessageTypeServerStatus         = models.MessageTypeServerStatus
	MessageTypeServerStats          = models.MessageTypeServerStats
	MessageTypeClientConfig         = models.MessageTypeClientConfig
	MessageTypeConfigUpdate         = models.MessageTypeConfigUpdate
	MessageTypeHealthCheck          = models.MessageTypeHealthCheck
	MessageTypeHealthResponse       = models.MessageTypeHealthResponse
	MessageTypeConnected            = models.MessageTypeConnected
	MessageTypeDisconnected         = models.MessageTypeDisconnected
	MessageTypeReconnected          = models.MessageTypeReconnected
	MessageTypeConnectionError      = models.MessageTypeConnectionError
	MessageTypeConnectionTimeout    = models.MessageTypeConnectionTimeout
	MessageTypeKickOut              = models.MessageTypeKickOut
	MessageTypeForceOffline         = models.MessageTypeForceOffline
	MessageTypeOpenWindow           = models.MessageTypeOpenWindow
	MessageTypeCloseWindow          = models.MessageTypeCloseWindow
)

// ==================== 消息优先级常量 ====================
const (
	MessagePriorityLow      = models.MessagePriorityLow
	MessagePriorityNormal   = models.MessagePriorityNormal
	MessagePriorityHigh     = models.MessagePriorityHigh
	MessagePriorityUrgent   = models.MessagePriorityUrgent
	MessagePriorityCritical = models.MessagePriorityCritical
)

// ==================== 消息工具函数 ====================
var (
	GetAllMessageTypes        = models.GetAllMessageTypes
	GetMessageTypesByCategory = models.GetMessageTypesByCategory
	GetMessageTypesByPriority = models.GetMessageTypesByPriority
	GetPriorityStats          = models.GetPriorityStats
)

// ==================== 分类相关 ====================
type (
	MessageClassification = models.MessageClassification
)

// ==================== 模板相关 ====================
type (
	WelcomeMessageProvider = models.WelcomeMessageProvider
	WelcomeMessage         = models.WelcomeMessage
	WelcomeTemplate        = models.WelcomeTemplate
)

// ==================== 消息和记录 ====================
type (
	HubMessage        = models.HubMessage
	MessageSendRecord = models.MessageSendRecord
	MessageSendStatus = models.MessageSendStatus
	FailureReason     = models.FailureReason
	RetryAttempt      = models.RetryAttempt
	RetryAttemptList  = models.RetryAttemptList
)

// ==================== 消息常量 ====================
const (
	DataKeyContentExtra = models.DataKeyContentExtra
	DataKeyMetadata     = models.DataKeyMetadata
	DataKeyMediaInfo    = models.DataKeyMediaInfo
)

// ==================== 消息工具函数 ====================
var (
	NewHubMessage = models.NewHubMessage
)

// ==================== 消息记录相关常量 ====================
const (
	MessageSendStatusPending     = models.MessageSendStatusPending
	MessageSendStatusSending     = models.MessageSendStatusSending
	MessageSendStatusSuccess     = models.MessageSendStatusSuccess
	MessageSendStatusFailed      = models.MessageSendStatusFailed
	MessageSendStatusRetrying    = models.MessageSendStatusRetrying
	MessageSendStatusAckTimeout  = models.MessageSendStatusAckTimeout
	MessageSendStatusUserOffline = models.MessageSendStatusUserOffline
	MessageSendStatusExpired     = models.MessageSendStatusExpired

	FailureReasonQueueFull    = models.FailureReasonQueueFull
	FailureReasonUserOffline  = models.FailureReasonUserOffline
	FailureReasonConnError    = models.FailureReasonConnError
	FailureReasonAckTimeout   = models.FailureReasonAckTimeout
	FailureReasonSendTimeout  = models.FailureReasonSendTimeout
	FailureReasonNetworkError = models.FailureReasonNetworkError
	FailureReasonUnknown      = models.FailureReasonUnknown
	FailureReasonMaxRetry     = models.FailureReasonMaxRetry
	FailureReasonExpired      = models.FailureReasonExpired

	QueryMessageIDWhere   = models.QueryMessageIDWhere
	OrderByCreateTimeDesc = models.OrderByCreateTimeDesc
	OrderByCreateTimeAsc  = models.OrderByCreateTimeAsc
	OrderByExpiresAtAsc   = models.OrderByExpiresAtAsc
)

// ==================== 连接模型 ====================
type (
	ConnectionRecord = models.ConnectionRecord
)

// ==================== 错误类型 ====================
type (
	ErrorType = models.ErrorType
)

// ==================== 错误类型常量 ====================
const (
	// 基础错误恢复类型
	ErrorTypeConnection    = models.ErrorTypeConnection
	ErrorTypeMessage       = models.ErrorTypeMessage
	ErrorTypeSystem        = models.ErrorTypeSystem
	ErrorTypeNetwork       = models.ErrorTypeNetwork
	ErrorTypeConcurrency   = models.ErrorTypeConcurrency
	ErrorTypeMemory        = models.ErrorTypeMemory
	ErrorTypeConfiguration = models.ErrorTypeConfiguration

	// 连接相关错误
	ErrTypeConnectionClosed   = models.ErrTypeConnectionClosed
	ErrTypeConnectionReset    = models.ErrTypeConnectionReset
	ErrTypeConnectionTimeout  = models.ErrTypeConnectionTimeout
	ErrTypeNetworkUnreachable = models.ErrTypeNetworkUnreachable
	ErrTypeServiceUnavailable = models.ErrTypeServiceUnavailable

	// 队列和缓冲区错误
	ErrTypeQueueFull           = models.ErrTypeQueueFull
	ErrTypeMessageBufferFull   = models.ErrTypeMessageBufferFull
	ErrTypePendingQueueFull    = models.ErrTypePendingQueueFull
	ErrTypeQueueAndPendingFull = models.ErrTypeQueueAndPendingFull

	// 用户和认证错误
	ErrTypeUserOffline          = models.ErrTypeUserOffline
	ErrTypeUserNotFound         = models.ErrTypeUserNotFound
	ErrTypePermissionDenied     = models.ErrTypePermissionDenied
	ErrTypeAuthenticationFailed = models.ErrTypeAuthenticationFailed
	ErrTypeUnauthorized         = models.ErrTypeUnauthorized

	// 消息错误
	ErrTypeInvalidMessageFormat   = models.ErrTypeInvalidMessageFormat
	ErrTypeMessageTooLarge        = models.ErrTypeMessageTooLarge
	ErrTypeMessageTargetMissing   = models.ErrTypeMessageTargetMissing
	ErrTypeMessageFiltered        = models.ErrTypeMessageFiltered
	ErrTypeMessageDeliveryTimeout = models.ErrTypeMessageDeliveryTimeout

	// 客户端错误
	ErrTypeClientNotFound     = models.ErrTypeClientNotFound
	ErrTypeClientDisconnected = models.ErrTypeClientDisconnected
	ErrTypeNoAvailableAgents  = models.ErrTypeNoAvailableAgents

	// 集线器操作错误
	ErrTypeHubStartupTimeout  = models.ErrTypeHubStartupTimeout
	ErrTypeHubShutdownTimeout = models.ErrTypeHubShutdownTimeout
	ErrTypeHubNotRunning      = models.ErrTypeHubNotRunning
	ErrTypeCircuitBreakerOpen = models.ErrTypeCircuitBreakerOpen

	// 记录管理错误
	ErrTypeRecordManagerDisabled        = models.ErrTypeRecordManagerDisabled
	ErrTypeMessageRecordNotFound        = models.ErrTypeMessageRecordNotFound
	ErrTypeMessageAlreadySent           = models.ErrTypeMessageAlreadySent
	ErrTypeMaxRetriesExceeded           = models.ErrTypeMaxRetriesExceeded
	ErrTypeRecordManagerNotInitialized  = models.ErrTypeRecordManagerNotInitialized
	ErrTypeMaxRetriesExceededForMessage = models.ErrTypeMaxRetriesExceededForMessage
	ErrTypeRecordRepositoryNotSet       = models.ErrTypeRecordRepositoryNotSet
	ErrTypeOnlineStatusRepositoryNotSet = models.ErrTypeOnlineStatusRepositoryNotSet
	ErrTypeStatsRepositoryNotSet        = models.ErrTypeStatsRepositoryNotSet

	// 速率限制错误
	ErrTypeRateLimitExceeded     = models.ErrTypeRateLimitExceeded
	ErrTypeFrequencyLimitReached = models.ErrTypeFrequencyLimitReached

	// 操作错误
	ErrTypeOperationTimeout = models.ErrTypeOperationTimeout
	ErrTypeTemporaryFailure = models.ErrTypeTemporaryFailure
	ErrTypeResourceBusy     = models.ErrTypeResourceBusy
	ErrTypeUnknownError     = models.ErrTypeUnknownError

	// ACK相关错误
	ErrTypeAckTimeout        = models.ErrTypeAckTimeout
	ErrTypeAckTimeoutRetries = models.ErrTypeAckTimeoutRetries
	ErrTypeContextCancelled  = models.ErrTypeContextCancelled

	// 配置相关错误
	ErrTypeConfigValidatorNotInitialized = models.ErrTypeConfigValidatorNotInitialized
	ErrTypeConfigValidationFailed        = models.ErrTypeConfigValidationFailed
	ErrTypeConfigAutoFixFailed           = models.ErrTypeConfigAutoFixFailed

	// 安全相关错误
	ErrTypeIPInBlacklist      = models.ErrTypeIPInBlacklist
	ErrTypeBruteForceDetected = models.ErrTypeBruteForceDetected
	ErrTypeThreatDetected     = models.ErrTypeThreatDetected
	ErrTypeAccessDeniedByRule = models.ErrTypeAccessDeniedByRule

	// PubSub相关错误
	ErrTypePubSubNotSet           = models.ErrTypePubSubNotSet
	ErrTypePubSubPublishFailed    = models.ErrTypePubSubPublishFailed
	ErrTypeEventSerializeFailed   = models.ErrTypeEventSerializeFailed
	ErrTypeEventDeserializeFailed = models.ErrTypeEventDeserializeFailed

	// 错误消息格式常量
	ErrMsgClientIDFormat   = models.ErrMsgClientIDFormat
	ErrMsgDecompressFailed = models.ErrMsgDecompressFailed
)

// ==================== 错误变量 ====================
var (
	// 连接相关错误
	ErrConnectionClosed       = models.ErrConnectionClosed
	ErrMessageBufferFull      = models.ErrMessageBufferFull
	ErrHubStartupTimeout      = models.ErrHubStartupTimeout
	ErrHubShutdownTimeout     = models.ErrHubShutdownTimeout
	ErrQueueAndPendingFull    = models.ErrQueueAndPendingFull
	ErrMessageTargetMissing   = models.ErrMessageTargetMissing
	ErrUserOffline            = models.ErrUserOffline
	ErrMessageDeliveryTimeout = models.ErrMessageDeliveryTimeout
	ErrCircuitBreakerOpen     = models.ErrCircuitBreakerOpen

	// ACK相关错误
	ErrAckTimeout        = models.ErrAckTimeout
	ErrAckTimeoutRetries = models.ErrAckTimeoutRetries
	ErrContextCancelled  = models.ErrContextCancelled

	// 记录管理相关错误
	ErrRecordManagerNotInitialized = models.ErrRecordManagerNotInitialized
	ErrMaxRetriesExceeded          = models.ErrMaxRetriesExceeded

	// 配置相关错误
	ErrConfigValidatorNotInitialized = models.ErrConfigValidatorNotInitialized

	// 业务逻辑错误
	ErrMessageFiltered              = models.ErrMessageFiltered
	ErrNoAvailableAgents            = models.ErrNoAvailableAgents
	ErrQueueFull                    = models.ErrQueueFull
	ErrRecordRepositoryNotSet       = models.ErrRecordRepositoryNotSet
	ErrOnlineStatusRepositoryNotSet = models.ErrOnlineStatusRepositoryNotSet
	ErrStatsRepositoryNotSet        = models.ErrStatsRepositoryNotSet

	// PubSub相关错误
	ErrPubSubNotSet           = models.ErrPubSubNotSet
	ErrPubSubPublishFailed    = models.ErrPubSubPublishFailed
	ErrEventSerializeFailed   = models.ErrEventSerializeFailed
	ErrEventDeserializeFailed = models.ErrEventDeserializeFailed

	// 错误判断辅助函数
	IsRetryableError     = models.IsRetryableError
	IsRetryableErrorType = models.IsRetryableErrorType
	IsQueueFullError     = models.IsQueueFullError
	IsUserOfflineError   = models.IsUserOfflineError
	IsSendTimeoutError   = models.IsSendTimeoutError
	IsAckTimeoutError    = models.IsAckTimeoutError
)

// ==================== 验证器 ====================
var (
	UserRoleValidator         = models.UserRoleValidator
	UserTypeValidator         = models.UserTypeValidator
	UserStatusValidator       = models.UserStatusValidator
	DisconnectReasonValidator = models.DisconnectReasonValidator
	ErrorSeverityValidator    = models.ErrorSeverityValidator
	QueueTypeValidator        = models.QueueTypeValidator
	MessageStatusValidator    = models.MessageStatusValidator
	NodeStatusValidator       = models.NodeStatusValidator
	ConnectionStatusValidator = models.ConnectionStatusValidator
	OperationTypeValidator    = models.OperationTypeValidator
	ClientTypeValidator       = models.ClientTypeValidator
	PriorityValidator         = models.PriorityValidator
	DepartmentValidator       = models.DepartmentValidator
	SkillValidator            = models.SkillValidator
	PushTypeValidator         = models.PushTypeValidator
	BroadcastTypeValidator    = models.BroadcastTypeValidator
	VIPLevelValidator         = models.VIPLevelValidator
	UrgencyLevelValidator     = models.UrgencyLevelValidator
	BusinessCategoryValidator = models.BusinessCategoryValidator
)

// ==================== 事件相关类型 ====================
type (
	EventStatus                        = models.EventStatus
	UserStatusEvent                    = models.UserStatusEvent
	TicketQueueEvent                   = models.TicketQueueEvent
	TicketAssignedEvent                = models.TicketAssignedEvent
	TicketAssignmentFailedEvent        = models.TicketAssignmentFailedEvent
	UserStatusEventHandler             = models.UserStatusEventHandler
	TicketQueueEventHandler            = models.TicketQueueEventHandler
	TicketAssignedEventHandler         = models.TicketAssignedEventHandler
	TicketAssignmentFailedEventHandler = models.TicketAssignmentFailedEventHandler
)

// ==================== 事件类型常量 ====================
const (
	// 事件类型
	EventUserOnline             = models.EventUserOnline
	EventUserOffline            = models.EventUserOffline
	EventTicketQueuePushed      = models.EventTicketQueuePushed
	EventTicketAssigned         = models.EventTicketAssigned
	EventTicketAssignmentFailed = models.EventTicketAssignmentFailed

	// 事件状态
	EventTypeOnline  = models.EventTypeOnline
	EventTypeOffline = models.EventTypeOffline
)

// ============================================================================
// HubMessage 方法导出 - 这些方法通过 HubMessage 实例调用
// ============================================================================

// 注意：以下是 HubMessage 类型的方法列表，通过 HubMessage 实例调用
// 例如：msg := wsc.NewHubMessage(); msg.SetSender("user1").SetContent("hello")

// 基础字段设置方法（链式调用）：
// - SetID(id string) *HubMessage: 设置消息ID
// - SetMessageType(messageType MessageType) *HubMessage: 设置消息类型
// - SetSender(sender string) *HubMessage: 设置发送者
// - SetSenderType(senderType UserType) *HubMessage: 设置发送者类型
// - SetReceiver(receiver string) *HubMessage: 设置接收者
// - SetReceiverType(receiverType UserType) *HubMessage: 设置接收者类型
// - SetReceiverClient(clientID string) *HubMessage: 设置接收客户端
// - SetReceiverNode(nodeID string) *HubMessage: 设置接收节点
// - SetSessionID(sessionID string) *HubMessage: 设置会话ID
// - SetContent(content string) *HubMessage: 设置内容
// - SetMessageID(messageID string) *HubMessage: 设置消息ID
// - SetSeqNo(seqNo int64) *HubMessage: 设置序列号
// - SetPriority(priority Priority) *HubMessage: 设置优先级
// - SetReplyToMsgID(replyToMsgID string) *HubMessage: 设置回复消息ID
// - SetRequireAck(requireAck bool) *HubMessage: 设置是否需要确认
// - SetPushType(pushType PushType) *HubMessage: 设置推送类型
// - SetBroadcastType(broadcastType BroadcastType) *HubMessage: 设置广播类型
// - SetSkipDatabaseStorage(skip bool) *HubMessage: 设置是否跳过数据库存储

// 扩展字段方法：
// - WithOption(key string, value interface{}) *HubMessage: 添加选项
// - GetOption(key string) (interface{}, bool): 获取选项
// - WithContentExtra(key string, value interface{}) *HubMessage: 添加内容扩展
// - GetContentExtra(key string) (interface{}, bool): 获取内容扩展
// - WithMetadata(key string, value string) *HubMessage: 添加元数据
// - GetMetadata(key string) (string, bool): 获取元数据

// 工具方法：
// - Clone() *HubMessage: 克隆消息

// ============================================================================
// MessageType 方法导出 - 这些方法通过 MessageType 实例调用
// ============================================================================

// 注意：以下是 MessageType 类型的方法列表，通过 MessageType 实例调用
// 例如：msgType := wsc.MessageTypeText; msgType.IsValid()

// 验证与检查方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效
// - IsMediaType() bool: 是否为媒体类型
// - IsTextType() bool: 是否为文本类型
// - IsSystemType() bool: 是否为系统类型
// - IsInteractiveType() bool: 是否为交互类型
// - IsStatusType() bool: 是否为状态类型
// - IsHeartbeatType() bool: 是否为心跳类型
// - IsSessionType() bool: 是否为会话类型
// - IsConnectionType() bool: 是否为连接类型
// - ShouldSkipDatabaseRecord() bool: 是否应跳过数据库记录
// - GetCategory() string: 获取分类
// - GetDefaultPriority() MessagePriority: 获取默认优先级

// ============================================================================
// 枚举类型方法导出 - 这些方法通过枚举实例调用
// ============================================================================

// UserRole 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// UserType 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效
// - IsCustomerType() bool: 是否为客户类型
// - IsAgentType() bool: 是否为客服类型
// - IsSystemType() bool: 是否为系统类型
// - IsHumanType() bool: 是否为人类类型
// - IsVIPType() bool: 是否为VIP类型

// UserStatus 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// DisconnectReason 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// ErrorSeverity 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// QueueType 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// MessageStatus 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// NodeStatus 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// ConnectionStatus 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// OperationType 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// ClientType 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// ConnectionType 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// Priority 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// Department 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// Skill 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// PushType 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// BroadcastType 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// VIPLevel 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// UrgencyLevel 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// BusinessCategory 方法：
// - String() string: 返回字符串表示
// - IsValid() bool: 验证是否有效

// MessagePriority 方法：
// - String() string: 返回字符串表示
// - GetWeight() int: 获取权重
// - IsHigherThan(other MessagePriority) bool: 是否高于另一个优先级

// ============================================================================
// MessageSendRecord 方法导出 - 这些方法通过 MessageSendRecord 实例调用
// ============================================================================

// 注意：以下是 MessageSendRecord 类型的方法列表
// 例如：record := &wsc.MessageSendRecord{}; record.SetMessage(msg)

// 表名方法：
// - TableName() string: 返回表名
// - TableComment() string: 返回表注释

// 消息操作方法：
// - SetMessage(msg *HubMessage) error: 设置消息
// - GetMessage() (*HubMessage, error): 获取消息

// GORM钩子方法：
// - BeforeCreate(tx *gorm.DB) error: 创建前钩子

// ============================================================================
// OfflineMessageRecord 方法导出
// ============================================================================

// 注意：以下是 OfflineMessageRecord 类型的方法列表
// 例如：record := &wsc.OfflineMessageRecord{}

// 表名方法：
// - TableName() string: 返回表名
// - TableComment() string: 返回表注释

// ============================================================================
// WelcomeTemplate 方法导出
// ============================================================================

// 注意：以下是 WelcomeTemplate 类型的方法列表
// 例如：template := &wsc.WelcomeTemplate{}; template.ReplaceVariables(vars)

// 模板方法：
// - ReplaceVariables(variables map[string]interface{}) WelcomeTemplate: 替换变量

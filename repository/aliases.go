/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-29 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 23:56:18
 * @FilePath: \go-wsc\repository\aliases.go
 * @Description: 类型别名 - 为 models 包中的类型创建别名，便于在 repository 层使用
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package repository

import "github.com/kamalyes/go-wsc/models"

// 类型别名 - 消息相关
type (
	// Client 客户端连接信息
	Client = models.Client

	// HubMessage Hub消息
	HubMessage = models.HubMessage

	// MessageSendRecord 消息发送记录
	MessageSendRecord = models.MessageSendRecord

	// MessageSendStatus 消息发送状态
	MessageSendStatus = models.MessageSendStatus

	// FailureReason 失败原因
	FailureReason = models.FailureReason

	// RetryAttempt 重试尝试记录
	RetryAttempt = models.RetryAttempt

	// RetryAttemptList 重试尝试列表
	RetryAttemptList = models.RetryAttemptList

	// OfflineMessageRecord 离线消息记录
	OfflineMessageRecord = models.OfflineMessageRecord
)

// 变量别名 - 待推送的离线消息状态列表
var (
	PendingOfflineStatuses = models.PendingOfflineStatuses
)

// 常量别名 - 消息发送状态
const (
	MessageSendStatusPending     = models.MessageSendStatusPending
	MessageSendStatusSending     = models.MessageSendStatusSending
	MessageSendStatusSuccess     = models.MessageSendStatusSuccess
	MessageSendStatusFailed      = models.MessageSendStatusFailed
	MessageSendStatusRetrying    = models.MessageSendStatusRetrying
	MessageSendStatusAckTimeout  = models.MessageSendStatusAckTimeout
	MessageSendStatusUserOffline = models.MessageSendStatusUserOffline
	MessageSendStatusExpired     = models.MessageSendStatusExpired
)

// 常量别名 - 失败原因
const (
	FailureReasonQueueFull    = models.FailureReasonQueueFull
	FailureReasonUserOffline  = models.FailureReasonUserOffline
	FailureReasonConnError    = models.FailureReasonConnError
	FailureReasonAckTimeout   = models.FailureReasonAckTimeout
	FailureReasonSendTimeout  = models.FailureReasonSendTimeout
	FailureReasonNetworkError = models.FailureReasonNetworkError
	FailureReasonUnknown      = models.FailureReasonUnknown
	FailureReasonMaxRetry     = models.FailureReasonMaxRetry
	FailureReasonExpired      = models.FailureReasonExpired
)

// 常量别名 - 查询相关
const (
	QueryMessageIDWhere   = models.QueryMessageIDWhere
	OrderByCreateTimeDesc = models.OrderByCreateTimeDesc
	OrderByCreateTimeAsc  = models.OrderByCreateTimeAsc
	OrderByExpiresAtAsc   = models.OrderByExpiresAtAsc
)

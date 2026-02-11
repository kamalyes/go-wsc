/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-30 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-30 00:15:15
 * @FilePath: \go-wsc\handler\aliases.go
 * @Description: Handler 模块类型别名 - 从其他模块导入常用类型
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package handler

import (
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/middleware"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
)

// ============================================================================
// 类型别名 - Logger
// ============================================================================

// WSCLogger 日志器类型别名
type WSCLogger = logger.ILogger

// 日志器构造函数别名
var (
	NewDefaultWSCLogger = middleware.NewDefaultWSCLogger
	NewWSCLogger        = middleware.NewWSCLogger
)

// ============================================================================
// 类型别名 - Models
// ============================================================================

type (
	// HubMessage Hub消息
	HubMessage = models.HubMessage

	// MessageType 消息类型
	MessageType = models.MessageType

	// MessageSendStatus 消息发送状态类型别名
	MessageSendStatus = models.MessageSendStatus
)

// ============================================================================
// 类型别名 - Repository
// ============================================================================

type (
	// MessageQueueRepository 消息队列仓储接口
	MessageQueueRepository = repository.MessageQueueRepository

	// RedisMessageQueueRepository Redis 消息队列仓储实现
	RedisMessageQueueRepository = repository.RedisMessageQueueRepository

	// OfflineMessageDBRepository 离线消息数据库仓储接口
	OfflineMessageDBRepository = repository.OfflineMessageDBRepository

	// GormOfflineMessageRepository Gorm 离线消息仓储实现
	GormOfflineMessageRepository = repository.GormOfflineMessageRepository

	// OfflineMessageRecord 离线消息记录
	OfflineMessageRecord = repository.OfflineMessageRecord

	// OfflineMessageFilter 离线消息查询过滤器
	OfflineMessageFilter = repository.OfflineMessageFilter

	// MessageRole 消息查询角色
	MessageRole = repository.MessageRole
)

// 仓储构造函数别名
var (
	// NewRedisMessageQueueRepository 创建 Redis 消息队列仓储
	NewRedisMessageQueueRepository = repository.NewRedisMessageQueueRepository

	// NewGormOfflineMessageRepository 创建 Gorm 离线消息仓储
	NewGormOfflineMessageRepository = repository.NewGormOfflineMessageRepository
)

// 消息查询角色常量
const (
	// MessageRoleReceiver 作为接收者查询
	MessageRoleReceiver = repository.MessageRoleReceiver
	// MessageRoleSender 作为发送者查询
	MessageRoleSender = repository.MessageRoleSender
)

// 消息发送状态别名
var (
	// 消息发生成功
	MessageSendStatusSuccess = models.MessageSendStatusSuccess
	// 消息发送失败
	MessageSendStatusFailed = models.MessageSendStatusFailed
)

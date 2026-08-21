/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-29 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 23:56:18
 * @FilePath: \go-wsc\repository\aliases.go
 * @Description: 类型别名 - 为 models 包中的类型创建别名，便于在 repository 层使用
 *
 * 仅保留类型别名（= models.X 的 type alias，零运行时成本，调用方免写 models. 前缀）。
 * 常量/变量/函数别名已全部删除：业务枚举（WorkloadDimension 系列、MessageSendStatus 系列、
 * FailureReason 系列）、错误（Err 系列）、查询常量（QueryMessageIDWhere、OrderBy 系列）、
 * IsSystemGroup 等请直接用 models.X 引用，避免多层 const 别名中转造成真相源不清。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
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

	// OfflineMessageRecord 离线消息记录
	OfflineMessageRecord = models.OfflineMessageRecord

	// AgentWorkloadModel 客服负载模型
	AgentWorkloadModel = models.AgentWorkloadModel

	// WorkloadDimension 客服负载统计维度
	WorkloadDimension = models.WorkloadDimension

	// Group 群组模型
	Group = models.Group

	// GroupSendResult 群组消息投递结果
	GroupSendResult = models.GroupSendResult
)

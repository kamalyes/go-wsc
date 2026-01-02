/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\exports_protocol.go
 * @Description: Protocol 模块类型导出 - 保持向后兼容
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import "github.com/kamalyes/go-wsc/protocol"

// ============================================
// ACK Protocol - 消息确认协议
// ============================================

// AckStatus ACK状态
type AckStatus = protocol.AckStatus

// AckStatus 常量
const (
	AckStatusPending   = protocol.AckStatusPending
	AckStatusConfirmed = protocol.AckStatusConfirmed
	AckStatusTimeout   = protocol.AckStatusTimeout
	AckStatusFailed    = protocol.AckStatusFailed
)

// AckMessage ACK消息结构
type AckMessage = protocol.AckMessage

// PendingMessage 待确认消息
type PendingMessage = protocol.PendingMessage

// AckManager ACK管理器
type AckManager = protocol.AckManager

// NewAckManager 创建ACK管理器
var NewAckManager = protocol.NewAckManager

// NewAckManagerWithOptions 创建ACK管理器（带选项）
var NewAckManagerWithOptions = protocol.NewAckManagerWithOptions

// ============================================================================
// AckManager 方法导出 - 这些方法通过 AckManager 实例调用
// ============================================================================

// 注意：以下是 AckManager 类型的方法列表，通过 AckManager 实例调用
// 例如：ackMgr := wsc.NewAckManager(timeout, maxRetry)

// 待确认消息管理方法：
// - AddPendingMessage(msg *HubMessage) *PendingMessage: 添加待确认消息
// - AddPendingMessageWithExpire(msg *HubMessage, timeout time.Duration, maxRetry int) *PendingMessage: 添加待确认消息（带过期时间）
// - ConfirmMessage(messageID string, ack *AckMessage) bool: 确认消息
// - RemovePendingMessage(messageID string): 移除待确认消息
// - GetPendingMessage(messageID string) (*PendingMessage, bool): 获取待确认消息
// - GetPendingCount() int: 获取待确认消息数量
// - CleanupExpired() int: 清理过期消息

// 配置方法：
// - SetOfflineRepo(handler repository.OfflineMessageDBRepository): 设置离线消息仓储
// - SetExpireDuration(duration time.Duration): 设置过期时长
// - GetTimeout() time.Duration: 获取超时时间
// - GetMaxRetry() int: 获取最大重试次数
// - Shutdown(): 关闭ACK管理器

// ============================================================================
// PendingMessage 方法导出 - 这些方法通过 PendingMessage 实例调用
// ============================================================================

// 注意：以下是 PendingMessage 类型的方法列表
// 例如：pending := ackMgr.AddPendingMessage(msg)

// 等待确认方法：
// - WaitForAck() (*AckMessage, error): 等待ACK确认
// - WaitForAckWithRetry(retryFunc func() error) (*AckMessage, error): 等待ACK确认（带重试）

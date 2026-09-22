/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 09:35:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 09:35:00
 * @FilePath: \go-wsc\spi\offline_store.go
 * @Description: 离线消息持久化 SPI - OfflineStore 接口契约定义

 * 离线消息在 RDBMS 侧的持久化契约（与 Redis 队列双写中的「持久」那一半）
 * 与 spi.OfflineQueue 的分工：
 *   - OfflineQueue 是面向 Hub 的离线消息处理器（同时编排 Redis 队列与 RDBMS，
 *     暴露 StoreOfflineMessage / DrainOfflineQueue / UpdatePushStatus 等业务语义）；
 *   - OfflineStore 只负责 RDBMS 这一侧的增删查，不含任何队列语义，
 *     供协议层（protocol.AckManager）与队列实现复用

 * 实现见 adapter/gorm 的 OfflineStore

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// MessageRole 查询视角 —— 用户是消息的接收者还是发送者
type MessageRole string

const (
	// MessageRoleReceiver 作为接收者查询
	MessageRoleReceiver MessageRole = "receiver"
	// MessageRoleSender 作为发送者查询
	MessageRoleSender MessageRole = "sender"
)

// OfflineMessageFilter 离线消息查询条件
type OfflineMessageFilter struct {
	// UserID 用户ID
	UserID string
	// Role 查询视角（接收者/发送者）
	Role MessageRole
	// AppID 应用ID（空表示查询所有应用）
	AppID string
	// Namespace 命名空间ID（空表示所有命名空间）
	Namespace string
	// GroupID 群组ID（空表示非群组消息或所有消息）
	GroupID string
	// Limit 数量限制
	Limit int
	// Cursor 分页游标（message_id）
	Cursor string
	// Statuses 消息状态列表，为空则待处理状态
	Statuses []models.MessageSendStatus
}

// OfflineStore 离线消息的 RDBMS 持久化契约
//
// 所有查询/删除均按 (appID, namespace, userID) 三元组隔离；
// appID 是最上层隔离维度，跨 app 绝不串扰
type OfflineStore interface {
	// Save 保存离线消息
	Save(ctx context.Context, record *models.OfflineMessageRecord) error

	// BatchSave 批量保存离线消息
	BatchSave(ctx context.Context, records []*models.OfflineMessageRecord) error

	// QueryMessages 查询离线消息（支持按接收者/发送者、分页、状态过滤）
	QueryMessages(ctx context.Context, filter *OfflineMessageFilter) ([]*models.OfflineMessageRecord, error)

	// DeleteByMessageIDs 批量删除离线消息（按应用+命名空间+接收者）
	DeleteByMessageIDs(ctx context.Context, appID, namespace, receiverID string, messageIDs []string) error

	// GetCountByReceiver 获取用户作为接收者的离线消息数量
	GetCountByReceiver(ctx context.Context, appID, namespace, receiverID string) (int64, error)

	// GetCountBySender 获取用户作为发送者的离线消息数量
	GetCountBySender(ctx context.Context, appID, namespace, senderID string) (int64, error)

	// ClearByReceiver 清空用户作为接收者的所有离线消息
	ClearByReceiver(ctx context.Context, appID, namespace, receiverID string) error

	// DeleteExpired 删除过期的离线消息
	DeleteExpired(ctx context.Context) (int64, error)

	// UpdatePushStatus 更新离线消息推送状态
	// status: 消息状态(pending/success/failed)
	// errorMsg: 错误信息(失败时)
	UpdatePushStatus(ctx context.Context, messageIDs []string, status models.MessageSendStatus, errorMsg string) error

	// CleanupOld 清理 before 之前的旧记录
	CleanupOld(ctx context.Context, before time.Time) (int64, error)

	// Close 关闭仓库，停止后台任务
	Close() error
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\offline_message.go
 * @Description: 消息处理逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import "time"

// OfflineMessageRecord 离线消息记录
// 用于存储用户离线时接收到的消息信息，包含发送者、接收者及消息内容等关键数据。
type OfflineMessageRecord struct {
	ID             uint              `gorm:"primaryKey;autoIncrement;comment:主键,唯一标识离线消息记录" json:"id"`
	MessageID      string            `gorm:"column:message_id;size:64;not null;uniqueIndex:idx_message_receiver;comment:业务消息ID,与receiver组成唯一索引" json:"message_id"`
	Sender         string            `gorm:"index;size:255;comment:发送者ID" json:"sender"`
	Receiver       string            `gorm:"index;size:255;not null;uniqueIndex:idx_message_receiver;comment:接收者ID,与message_id组成唯一索引" json:"receiver"`
	SessionID      string            `gorm:"column:session_id;size:64;not null;index;comment:会话ID" json:"session_id"`
	CompressedData []byte            `gorm:"type:longblob;not null;comment:压缩完整的HubMessage JSON数据" json:"-"`
	Status         MessageSendStatus `gorm:"column:status;size:20;not null;default:'user_offline';index;comment:消息状态,复用MessageSendStatus" json:"status"`
	RetryCount     int               `gorm:"column:retry_count;default:0;comment:推送重试次数" json:"retry_count"`
	MaxRetry       int               `gorm:"column:max_retry;default:3;comment:最大重试次数" json:"max_retry"`
	ScheduledAt    time.Time         `gorm:"column:scheduled_at;not null;comment:消息计划发送的时间" json:"scheduled_at"`
	ExpireAt       time.Time         `gorm:"column:expire_at;not null;index;comment:消息过期时间" json:"expire_at"`
	FirstPushAt    *time.Time        `gorm:"column:first_push_at;index;comment:首次推送时间" json:"first_push_at,omitempty"`
	LastPushAt     *time.Time        `gorm:"column:last_push_at;index;comment:最后推送时间(成功或失败都更新)" json:"last_push_at,omitempty"`
	ErrorMessage   string            `gorm:"column:error_message;type:text;comment:推送失败的错误信息" json:"error_message,omitempty"`
	CreatedAt      time.Time         `gorm:"autoCreateTime;comment:记录创建时间" json:"created_at"`
	UpdatedAt      time.Time         `gorm:"autoUpdateTime;comment:记录最后更新时间" json:"updated_at"`
}

// TableName 指定表名
func (OfflineMessageRecord) TableName() string {
	return "wsc_offline_messages"
}

// TableComment 表注释
func (OfflineMessageRecord) TableComment() string {
	return "WebSocket离线消息记录表-存储用户离线时接收到的消息信息用于消息投递和管理"
}

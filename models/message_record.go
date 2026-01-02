/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\message_record.go
 * @Description: 消息发送记录管理 - 使用 GORM 数据库持久化
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import (
	"database/sql/driver"
	"encoding/json"
	"time"

	"github.com/kamalyes/go-sqlbuilder"
	"gorm.io/gorm"
)

// 数据库查询常量
const (
	QueryMessageIDWhere   = "message_id = ?"
	OrderByCreateTimeDesc = "create_time DESC"
	OrderByCreateTimeAsc  = "create_time ASC"
	OrderByExpiresAtAsc   = "expires_at ASC"
)

// MessageSendStatus 消息发送状态
type MessageSendStatus string

const (
	MessageSendStatusPending     MessageSendStatus = "pending"      // 待发送
	MessageSendStatusSending     MessageSendStatus = "sending"      // 发送中
	MessageSendStatusSuccess     MessageSendStatus = "success"      // 发送成功
	MessageSendStatusFailed      MessageSendStatus = "failed"       // 发送失败
	MessageSendStatusRetrying    MessageSendStatus = "retrying"     // 重试中
	MessageSendStatusAckTimeout  MessageSendStatus = "ack_timeout"  // ACK超时
	MessageSendStatusUserOffline MessageSendStatus = "user_offline" // 用户离线
	MessageSendStatusExpired     MessageSendStatus = "expired"      // 已过期
)

// FailureReason 失败原因
type FailureReason string

const (
	FailureReasonQueueFull    FailureReason = "queue_full"    // 队列已满
	FailureReasonUserOffline  FailureReason = "user_offline"  // 用户离线
	FailureReasonConnError    FailureReason = "conn_error"    // 连接错误
	FailureReasonAckTimeout   FailureReason = "ack_timeout"   // ACK超时
	FailureReasonSendTimeout  FailureReason = "send_timeout"  // 发送超时
	FailureReasonNetworkError FailureReason = "network_error" // 网络错误
	FailureReasonUnknown      FailureReason = "unknown"       // 未知错误
	FailureReasonMaxRetry     FailureReason = "max_retry"     // 超过最大重试次数
	FailureReasonExpired      FailureReason = "expired"       // 消息过期
)

// MessageSource 消息来源类型
type MessageSource string

const (
	MessageSourceOnline  MessageSource = "online"  // 实时在线发送
	MessageSourceOffline MessageSource = "offline" // 离线消息推送
)

// RetryAttempt 重试记录
type RetryAttempt struct {
	AttemptNumber int           `json:"attempt_number"` // 第几次重试
	Timestamp     time.Time     `json:"timestamp"`      // 重试时间
	Duration      time.Duration `json:"duration"`       // 本次重试耗时
	Error         string        `json:"error"`          // 错误信息
	Success       bool          `json:"success"`        // 是否成功
}

// RetryAttemptList 重试记录列表（用于 GORM JSON 序列化）
type RetryAttemptList []RetryAttempt

// Scan 实现 sql.Scanner 接口
func (r *RetryAttemptList) Scan(value interface{}) error {
	if value == nil {
		*r = []RetryAttempt{}
		return nil
	}
	bytes, ok := value.([]byte)
	if !ok {
		return nil
	}
	return json.Unmarshal(bytes, r)
}

// Value 实现 driver.Valuer 接口
func (r RetryAttemptList) Value() (driver.Value, error) {
	if len(r) == 0 {
		return nil, nil
	}
	return json.Marshal(r)
}

// MessageSendRecord 消息发送记录（GORM 模型）
type MessageSendRecord struct {
	ID            uint                   `gorm:"primaryKey;autoIncrement;comment:主键,唯一标识每条发送记录" json:"id"`                           // 主键(唯一)
	SessionID     string                 `gorm:"column:session_id;size:255;not null;index;comment:会话ID" json:"session_id"`           // 会话ID
	MessageID     string                 `gorm:"index;size:255;not null;comment:业务消息ID,用于关联业务系统" json:"message_id"`                  // 业务消息ID(可重复,支持多次发送记录)
	HubID         string                 `gorm:"index;size:255;not null;comment:Hub内部消息ID,用于ACK确认和日志追踪" json:"hub_id"`               // Hub内部消息ID(可重复)
	MessageData   string                 `gorm:"type:text;comment:原始消息数据,类型为文本" json:"message_data"`                                 // 原始消息数据
	Sender        string                 `gorm:"index;size:255;comment:发送者ID" json:"sender"`                                         // 发送者ID
	Receiver      string                 `gorm:"index;size:255;comment:接收者ID" json:"receiver"`                                       // 接收者ID
	MessageType   MessageType            `gorm:"index;size:50;comment:消息类型" json:"message_type"`                                     // 消息类型
	Source        MessageSource          `gorm:"index;size:20;not null;default:'online';comment:消息来源(online/offline)" json:"source"` // 消息来源
	NodeIP        string                 `gorm:"index;size:100;comment:服务器节点IP地址" json:"node_ip"`                                    // 服务器节点IP地址
	ClientIP      string                 `gorm:"index;size:100;comment:客户端IP地址" json:"client_ip"`                                    // 客户端IP地址
	Status        MessageSendStatus      `gorm:"index;size:50;not null;default:'pending';comment:当前状态,不能为空" json:"status"`           // 当前状态
	CreateTime    time.Time              `gorm:"index;not null;comment:创建时间" json:"create_time"`                                     // 创建时间
	FirstSendTime *time.Time             `gorm:"index;comment:首次发送时间" json:"first_send_time"`                                        // 首次发送时间
	LastSendTime  *time.Time             `gorm:"index;comment:最后发送时间" json:"last_send_time"`                                         // 最后发送时间
	SuccessTime   *time.Time             `gorm:"index;comment:成功时间" json:"success_time"`                                             // 成功时间
	RetryCount    int                    `gorm:"default:0;comment:重试次数" json:"retry_count"`                                          // 重试次数
	MaxRetry      int                    `gorm:"default:3;comment:最大重试次数" json:"max_retry"`                                          // 最大重试次数
	FailureReason FailureReason          `gorm:"size:50;comment:失败原因" json:"failure_reason"`                                         // 失败原因
	ErrorMessage  string                 `gorm:"type:text;comment:错误信息,类型为文本" json:"error_message"`                                  // 错误信息
	RetryHistory  RetryAttemptList       `gorm:"type:json;comment:重试历史,类型为JSON" json:"retry_history"`                                // 重试历史
	ExpiresAt     *time.Time             `gorm:"index;comment:过期时间" json:"expires_at"`                                               // 过期时间
	Metadata      sqlbuilder.MapAny      `gorm:"type:json;comment:扩展元数据,类型为JSON" json:"metadata"`                                    // 扩展元数据
	CustomFields  sqlbuilder.MapAny      `gorm:"type:json;comment:用户自定义字段,类型为JSON" json:"custom_fields"`                             // 用户自定义字段
	Tags          sqlbuilder.StringSlice `gorm:"type:json;comment:标签,类型为JSON" json:"tags"`                                           // 标签
	ExtraData     string                 `gorm:"type:text;comment:额外数据,类型为文本" json:"extra_data"`                                     // 额外数据
	CreatedAt     time.Time              `gorm:"comment:记录创建时间" json:"created_at"`                                                   // 创建时间
	UpdatedAt     time.Time              `gorm:"comment:记录最后更新时间" json:"updated_at"`                                                 // 最后更新时间
	DeletedAt     gorm.DeletedAt         `gorm:"index;comment:记录删除时间,支持软删除" json:"deleted_at,omitempty"`                             // 删除时间
}

// TableName 指定表名
func (MessageSendRecord) TableName() string {
	return "wsc_message_send_records"
}

// TableComment 表注释
func (MessageSendRecord) TableComment() string {
	return "WebSocket消息发送记录表-存储每条消息的发送状态和历史用于审计和重试"
}

// BeforeCreate GORM 钩子：创建前
func (m *MessageSendRecord) BeforeCreate(tx *gorm.DB) error {
	if m.CreateTime.IsZero() {
		m.CreateTime = time.Now()
	}
	if m.RetryHistory == nil {
		m.RetryHistory = []RetryAttempt{}
	}
	if m.Metadata == nil {
		m.Metadata = make(sqlbuilder.MapAny)
	}
	if m.CustomFields == nil {
		m.CustomFields = make(sqlbuilder.MapAny)
	}
	if m.Tags == nil {
		m.Tags = []string{}
	}
	return nil
}

// SetMessage 设置消息数据（将 HubMessage 序列化存储）
func (m *MessageSendRecord) SetMessage(msg *HubMessage) error {
	if msg == nil {
		return nil
	}

	// 序列化 HubMessage
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	m.MessageData = string(data)

	// 提取关键字段用于索引和查询
	// 使用 msg.MessageID (业务消息ID) 和 msg.ID (Hub 内部ID)
	m.MessageID = msg.MessageID
	m.HubID = msg.ID
	m.SessionID = msg.SessionID
	m.Sender = msg.Sender
	m.Receiver = msg.Receiver
	m.MessageType = msg.MessageType

	return nil
}

// GetMessage 获取消息数据（反序列化 HubMessage）
func (m *MessageSendRecord) GetMessage() (*HubMessage, error) {
	if m.MessageData == "" {
		return nil, nil
	}

	var msg HubMessage
	err := json.Unmarshal([]byte(m.MessageData), &msg)
	if err != nil {
		return nil, err
	}

	return &msg, nil
}

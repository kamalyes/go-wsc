/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-04 15:35:36
 * @FilePath: \go-wsc\message_record.go
 * @Description: 消息发送记录管理 - 使用 GORM 数据库持久化
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

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
	ID            uint                   `gorm:"primaryKey;autoIncrement;comment:主键,唯一标识每条发送记录" json:"id"`                 // 主键(唯一)
	SessionID     string                 `gorm:"column:session_id;size:255;not null;index;comment:会话ID" json:"session_id"` // 会话ID
	MessageID     string                 `gorm:"index;size:255;not null;comment:业务消息ID,用于关联业务系统" json:"message_id"`        // 业务消息ID(可重复,支持多次发送记录)
	HubID         string                 `gorm:"index;size:255;not null;comment:Hub内部消息ID,用于ACK确认和日志追踪" json:"hub_id"`     // Hub内部消息ID(可重复)
	MessageData   string                 `gorm:"type:text;comment:原始消息数据,类型为文本" json:"message_data"`                       // 原始消息数据
	Sender        string                 `gorm:"index;size:255;comment:发送者ID" json:"sender"`                               // 发送者ID
	Receiver      string                 `gorm:"index;size:255;comment:接收者ID" json:"receiver"`                             // 接收者ID
	MessageType   MessageType            `gorm:"index;size:50;comment:消息类型" json:"message_type"`                           // 消息类型
	NodeIP        string                 `gorm:"index;size:100;comment:服务器节点IP地址" json:"node_ip"`                          // 服务器节点IP地址
	ClientIP      string                 `gorm:"index;size:100;comment:客户端IP地址" json:"client_ip"`                          // 客户端IP地址
	Status        MessageSendStatus      `gorm:"index;size:50;not null;default:'pending';comment:当前状态,不能为空" json:"status"` // 当前状态
	CreateTime    time.Time              `gorm:"index;not null;comment:创建时间" json:"create_time"`                           // 创建时间
	FirstSendTime *time.Time             `gorm:"index;comment:首次发送时间" json:"first_send_time"`                              // 首次发送时间
	LastSendTime  *time.Time             `gorm:"index;comment:最后发送时间" json:"last_send_time"`                               // 最后发送时间
	SuccessTime   *time.Time             `gorm:"index;comment:成功时间" json:"success_time"`                                   // 成功时间
	RetryCount    int                    `gorm:"default:0;comment:重试次数" json:"retry_count"`                                // 重试次数
	MaxRetry      int                    `gorm:"default:3;comment:最大重试次数" json:"max_retry"`                                // 最大重试次数
	FailureReason FailureReason          `gorm:"size:50;comment:失败原因" json:"failure_reason"`                               // 失败原因
	ErrorMessage  string                 `gorm:"type:text;comment:错误信息,类型为文本" json:"error_message"`                        // 错误信息
	RetryHistory  RetryAttemptList       `gorm:"type:json;comment:重试历史,类型为JSON" json:"retry_history"`                      // 重试历史
	ExpiresAt     *time.Time             `gorm:"index;comment:过期时间" json:"expires_at"`                                     // 过期时间
	UserOffline   bool                   `gorm:"default:false;comment:用户是否离线,默认值为false" json:"user_offline"`               // 用户是否离线
	Metadata      sqlbuilder.MapAny      `gorm:"type:json;comment:扩展元数据,类型为JSON" json:"metadata"`                          // 扩展元数据
	CustomFields  sqlbuilder.MapAny      `gorm:"type:json;comment:用户自定义字段,类型为JSON" json:"custom_fields"`                   // 用户自定义字段
	Tags          sqlbuilder.StringSlice `gorm:"type:json;comment:标签,类型为JSON" json:"tags"`                                 // 标签
	ExtraData     string                 `gorm:"type:text;comment:额外数据,类型为文本" json:"extra_data"`                           // 额外数据
	CreatedAt     time.Time              `gorm:"comment:记录创建时间" json:"created_at"`                                         // 创建时间
	UpdatedAt     time.Time              `gorm:"comment:记录最后更新时间" json:"updated_at"`                                       // 最后更新时间
	DeletedAt     gorm.DeletedAt         `gorm:"index;comment:记录删除时间,支持软删除" json:"deleted_at,omitempty"`                   // 删除时间
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

// MessageRecordRepository 消息记录仓库接口
type MessageRecordRepository interface {
	// Create 创建记录
	Create(record *MessageSendRecord) error

	// CreateFromMessage 从 HubMessage 创建记录
	CreateFromMessage(msg *HubMessage, maxRetry int, expiresAt *time.Time) (*MessageSendRecord, error)

	// Update 更新记录
	Update(record *MessageSendRecord) error

	// FindByID 根据ID查找
	FindByID(id uint) (*MessageSendRecord, error)

	// FindByMessageID 根据消息ID查找
	FindByMessageID(messageID string) (*MessageSendRecord, error)

	// FindByStatus 根据状态查找
	FindByStatus(status MessageSendStatus, limit int) ([]*MessageSendRecord, error)

	// FindBySender 根据发送者查找
	FindBySender(sender string, limit int) ([]*MessageSendRecord, error)

	// FindByReceiver 根据接收者查找
	FindByReceiver(receiver string, limit int) ([]*MessageSendRecord, error)

	// FindByNodeIP 根据节点IP查找
	FindByNodeIP(nodeIP string, limit int) ([]*MessageSendRecord, error)

	// FindByClientIP 根据客户端IP查找
	FindByClientIP(clientIP string, limit int) ([]*MessageSendRecord, error)

	// FindRetryable 查找可重试的记录
	FindRetryable(limit int) ([]*MessageSendRecord, error)

	// FindExpired 查找过期的记录
	FindExpired(limit int) ([]*MessageSendRecord, error)

	// Delete 删除记录
	Delete(id uint) error

	// DeleteByMessageID 根据消息ID删除
	DeleteByMessageID(messageID string) error

	// UpdateStatus 更新状态
	UpdateStatus(messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) error

	// IncrementRetry 增加重试次数
	IncrementRetry(messageID string, attempt RetryAttempt) error

	// GetStatistics 获取统计信息
	GetStatistics() (map[string]int64, error)

	// CleanupOld 清理旧记录
	CleanupOld(before time.Time) (int64, error)

	// GetDB 获取底层 GORM DB（用于复杂查询）
	GetDB() *gorm.DB
}

// MessageRecordGormRepository GORM 实现
type MessageRecordGormRepository struct {
	db *gorm.DB
}

// NewMessageRecordRepository 创建消息记录仓库
func NewMessageRecordRepository(db *gorm.DB) MessageRecordRepository {
	return &MessageRecordGormRepository{db: db}
}

// Create 创建记录
func (r *MessageRecordGormRepository) Create(record *MessageSendRecord) error {
	return r.db.Create(record).Error
}

// CreateFromMessage 从 HubMessage 创建记录
func (r *MessageRecordGormRepository) CreateFromMessage(msg *HubMessage, maxRetry int, expiresAt *time.Time) (*MessageSendRecord, error) {
	record := &MessageSendRecord{
		Status:     MessageSendStatusPending,
		CreateTime: time.Now(),
		MaxRetry:   maxRetry,
		ExpiresAt:  expiresAt,
	}

	// 序列化 HubMessage
	if err := record.SetMessage(msg); err != nil {
		return nil, err
	}

	if err := r.db.Create(record).Error; err != nil {
		return nil, err
	}

	return record, nil
}

// Update 更新记录
func (r *MessageRecordGormRepository) Update(record *MessageSendRecord) error {
	return r.db.Save(record).Error
}

// FindByID 根据ID查找
func (r *MessageRecordGormRepository) FindByID(id uint) (*MessageSendRecord, error) {
	var record MessageSendRecord
	err := r.db.First(&record, id).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// FindByMessageID 根据消息ID查找
func (r *MessageRecordGormRepository) FindByMessageID(messageID string) (*MessageSendRecord, error) {
	var record MessageSendRecord
	err := r.db.Where(QueryMessageIDWhere, messageID).First(&record).Error
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// FindByStatus 根据状态查找
func (r *MessageRecordGormRepository) FindByStatus(status MessageSendStatus, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.Where("status = ?", status).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindBySender 根据发送者查找
func (r *MessageRecordGormRepository) FindBySender(sender string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.Where("sender = ?", sender).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindByReceiver 根据接收者查找
func (r *MessageRecordGormRepository) FindByReceiver(receiver string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.Where("receiver = ?", receiver).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindByNodeIP 根据节点IP查找
func (r *MessageRecordGormRepository) FindByNodeIP(nodeIP string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.Where("node_ip = ?", nodeIP).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindByClientIP 根据客户端IP查找
func (r *MessageRecordGormRepository) FindByClientIP(clientIP string, limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	query := r.db.Where("client_ip = ?", clientIP).Order(OrderByCreateTimeDesc)
	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindRetryable 查找可重试的记录
func (r *MessageRecordGormRepository) FindRetryable(limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	now := time.Now()
	query := r.db.Where("status IN ? AND retry_count < max_retry", []MessageSendStatus{
		MessageSendStatusFailed,
		MessageSendStatusAckTimeout,
	}).Where("expires_at IS NULL OR expires_at > ?", now).
		Order(OrderByCreateTimeAsc)

	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// FindExpired 查找过期的记录
func (r *MessageRecordGormRepository) FindExpired(limit int) ([]*MessageSendRecord, error) {
	var records []*MessageSendRecord
	now := time.Now()
	query := r.db.Where("expires_at IS NOT NULL AND expires_at < ? AND status != ?",
		now, MessageSendStatusExpired).Order(OrderByExpiresAtAsc)

	if limit > 0 {
		query = query.Limit(limit)
	}
	err := query.Find(&records).Error
	return records, err
}

// Delete 删除记录
func (r *MessageRecordGormRepository) Delete(id uint) error {
	return r.db.Delete(&MessageSendRecord{}, id).Error
}

// DeleteByMessageID 根据消息ID删除
func (r *MessageRecordGormRepository) DeleteByMessageID(messageID string) error {
	return r.db.Where(QueryMessageIDWhere, messageID).Delete(&MessageSendRecord{}).Error
}

// UpdateStatus 更新状态
func (r *MessageRecordGormRepository) UpdateStatus(messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) error {
	now := time.Now()

	// 🔥 先查询记录,如果不存在则跳过更新（广播消息等不需要记录的场景）
	var record MessageSendRecord
	if err := r.db.Where(QueryMessageIDWhere, messageID).First(&record).Error; err != nil {
		// 记录不存在,静默返回（不是错误）
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}

	updates := map[string]interface{}{
		"status":         status,
		"last_send_time": &now,
	}

	// 🔥 只有在状态为发送中时才设置首次发送时间（避免重复设置）
	if record.FirstSendTime == nil && status == MessageSendStatusSending {
		updates["first_send_time"] = &now
	}

	// 设置失败原因和错误信息
	if reason != "" {
		updates["failure_reason"] = reason
	}
	if errorMsg != "" {
		updates["error_message"] = errorMsg
	}

	// 🔥 如果发送成功,设置成功时间
	if status == MessageSendStatusSuccess {
		updates["success_time"] = &now
	}

	return r.db.Model(&MessageSendRecord{}).
		Where(QueryMessageIDWhere, messageID).
		Updates(updates).Error
}

// IncrementRetry 增加重试次数
func (r *MessageRecordGormRepository) IncrementRetry(messageID string, attempt RetryAttempt) error {
	var record MessageSendRecord
	err := r.db.Where(QueryMessageIDWhere, messageID).First(&record).Error
	if err != nil {
		return err
	}

	now := time.Now()
	record.RetryHistory = append(record.RetryHistory, attempt)
	record.RetryCount = attempt.AttemptNumber

	updates := map[string]interface{}{
		"retry_count":    record.RetryCount,
		"retry_history":  record.RetryHistory,
		"status":         MessageSendStatusRetrying,
		"last_send_time": &now, // 🔥 每次重试都更新最后发送时间
	}

	// 🔥 如果是首次重试（first_send_time 为 NULL）,设置首次发送时间
	if record.FirstSendTime == nil {
		updates["first_send_time"] = &now
	}

	if attempt.Success {
		// 🔥 重试成功,设置成功时间
		updates["status"] = MessageSendStatusSuccess
		updates["success_time"] = &now
	} else if record.RetryCount >= record.MaxRetry {
		// 🔥 超过最大重试次数,设置失败状态和原因
		updates["status"] = MessageSendStatusFailed
		updates["failure_reason"] = FailureReasonMaxRetry
		if attempt.Error != "" {
			updates["error_message"] = attempt.Error
		}
	} else {
		// 🔥 重试中但未达到最大次数,记录错误信息
		if attempt.Error != "" {
			updates["error_message"] = attempt.Error
		}
	}

	return r.db.Model(&record).Updates(updates).Error
}

// GetStatistics 获取统计信息
func (r *MessageRecordGormRepository) GetStatistics() (map[string]int64, error) {
	stats := make(map[string]int64)

	// 总数
	var total int64
	r.db.Model(&MessageSendRecord{}).Count(&total)
	stats["total"] = total

	// 按状态统计
	statuses := []MessageSendStatus{
		MessageSendStatusPending,
		MessageSendStatusSending,
		MessageSendStatusSuccess,
		MessageSendStatusFailed,
		MessageSendStatusRetrying,
		MessageSendStatusAckTimeout,
		MessageSendStatusUserOffline,
		MessageSendStatusExpired,
	}

	for _, status := range statuses {
		var count int64
		r.db.Model(&MessageSendRecord{}).Where("status = ?", status).Count(&count)
		stats[string(status)] = count
	}

	return stats, nil
}

// CleanupOld 清理旧记录
func (r *MessageRecordGormRepository) CleanupOld(before time.Time) (int64, error) {
	result := r.db.Where("create_time < ? AND status IN ?", before, []MessageSendStatus{
		MessageSendStatusSuccess,
		MessageSendStatusFailed,
		MessageSendStatusExpired,
	}).Delete(&MessageSendRecord{})

	return result.RowsAffected, result.Error
}

// GetDB 获取底层 GORM DB
func (r *MessageRecordGormRepository) GetDB() *gorm.DB {
	return r.db
}

// MessageRecordHooks 消息记录钩子函数接口
type MessageRecordHooks interface {
	// OnRecordCreated 记录创建时调用
	OnRecordCreated(record *MessageSendRecord) error

	// OnRecordUpdated 记录更新时调用
	OnRecordUpdated(record *MessageSendRecord, oldStatus MessageSendStatus, newStatus MessageSendStatus) error

	// OnRetryAttempt 重试尝试时调用
	OnRetryAttempt(record *MessageSendRecord, attempt *RetryAttempt) error

	// OnRecordDeleted 记录删除前调用
	OnRecordDeleted(record *MessageSendRecord) error

	// OnRecordExpired 记录过期时调用
	OnRecordExpired(record *MessageSendRecord) error
}

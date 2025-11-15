/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-15
 * @FilePath: \go-wsc\message_record.go
 * @Description: 消息发送失败记录和重发管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"sync"
	"time"
)

// MessageSendStatus 消息发送状态
type MessageSendStatus string

const (
	MessageSendStatusPending    MessageSendStatus = "pending"     // 待发送
	MessageSendStatusSending    MessageSendStatus = "sending"     // 发送中
	MessageSendStatusSuccess    MessageSendStatus = "success"     // 发送成功
	MessageSendStatusFailed     MessageSendStatus = "failed"      // 发送失败
	MessageSendStatusRetrying   MessageSendStatus = "retrying"    // 重试中
	MessageSendStatusAckTimeout MessageSendStatus = "ack_timeout" // ACK超时
	MessageSendStatusUserOffline MessageSendStatus = "user_offline" // 用户离线
	MessageSendStatusExpired    MessageSendStatus = "expired"     // 已过期
)

// FailureReason 失败原因
type FailureReason string

const (
	FailureReasonQueueFull     FailureReason = "queue_full"      // 队列已满
	FailureReasonUserOffline   FailureReason = "user_offline"    // 用户离线
	FailureReasonConnError     FailureReason = "conn_error"      // 连接错误
	FailureReasonAckTimeout    FailureReason = "ack_timeout"     // ACK超时
	FailureReasonSendTimeout   FailureReason = "send_timeout"    // 发送超时
	FailureReasonNetworkError  FailureReason = "network_error"   // 网络错误
	FailureReasonUnknown       FailureReason = "unknown"         // 未知错误
	FailureReasonMaxRetry      FailureReason = "max_retry"       // 超过最大重试次数
	FailureReasonExpired       FailureReason = "expired"         // 消息过期
)

// RetryAttempt 重试记录
type RetryAttempt struct {
	AttemptNumber int           `json:"attempt_number"` // 第几次重试
	Timestamp     time.Time     `json:"timestamp"`      // 重试时间
	Duration      time.Duration `json:"duration"`       // 本次重试耗时
	Error         string        `json:"error"`          // 错误信息
	Success       bool          `json:"success"`        // 是否成功
}

// MessageSendRecord 消息发送记录
type MessageSendRecord struct {
	MessageID     string            `json:"message_id"`     // 消息ID
	Message       *HubMessage       `json:"message"`        // 原始消息
	Status        MessageSendStatus `json:"status"`         // 当前状态
	CreateTime    time.Time         `json:"create_time"`    // 创建时间
	FirstSendTime time.Time         `json:"first_send_time"` // 首次发送时间
	LastSendTime  time.Time         `json:"last_send_time"` // 最后发送时间
	SuccessTime   time.Time         `json:"success_time"`   // 成功时间
	RetryCount    int               `json:"retry_count"`    // 重试次数
	MaxRetry      int               `json:"max_retry"`      // 最大重试次数
	FailureReason FailureReason     `json:"failure_reason"` // 失败原因
	ErrorMessage  string            `json:"error_message"`  // 错误详情
	RetryHistory  []RetryAttempt    `json:"retry_history"`  // 重试历史
	ExpiresAt     time.Time         `json:"expires_at"`     // 过期时间
	UserOffline   bool              `json:"user_offline"`   // 用户是否离线
	Metadata      map[string]interface{} `json:"metadata"` // 扩展元数据
	CustomFields  map[string]interface{} `json:"custom_fields,omitempty"` // 用户自定义字段
	Tags          []string          `json:"tags,omitempty"`  // 标签（用于分类和查询）
	ExtraData     interface{}       `json:"extra_data,omitempty"` // 额外数据（可存储任意类型）
}

// MessageRecordManager 消息记录管理器
type MessageRecordManager struct {
	records         map[string]*MessageSendRecord // 消息记录映射
	mu              sync.RWMutex                  // 读写锁
	maxRecords      int                           // 最大记录数
	recordRetention time.Duration                 // 记录保留时间
	persistence     MessageRecordPersistence      // 持久化接口
	hooks           MessageRecordHooks            // 钩子函数
	filters         []MessageRecordFilter         // 自定义过滤器列表
	customHandlers  map[string]interface{}        // 自定义处理器映射
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

// MessageRecordFilter 消息记录过滤器接口
type MessageRecordFilter interface {
	// Match 判断记录是否匹配过滤条件
	Match(record *MessageSendRecord) bool
}

// MessageRecordPersistence 消息记录持久化接口
type MessageRecordPersistence interface {
	// SaveRecord 保存消息记录
	SaveRecord(record *MessageSendRecord) error
	
	// UpdateRecord 更新消息记录
	UpdateRecord(record *MessageSendRecord) error
	
	// GetRecord 获取消息记录
	GetRecord(messageID string) (*MessageSendRecord, error)
	
	// DeleteRecord 删除消息记录
	DeleteRecord(messageID string) error
	
	// ListFailedRecords 列出失败的消息记录
	ListFailedRecords(limit int) ([]*MessageSendRecord, error)
	
	// ListRetryableRecords 列出可重试的消息记录
	ListRetryableRecords(limit int) ([]*MessageSendRecord, error)
}

// NewMessageRecordManager 创建消息记录管理器
func NewMessageRecordManager(maxRecords int, retention time.Duration, persistence MessageRecordPersistence) *MessageRecordManager {
	if maxRecords <= 0 {
		maxRecords = 10000 // 默认最多保留1万条记录
	}
	if retention <= 0 {
		retention = 24 * time.Hour // 默认保留24小时
	}

	return &MessageRecordManager{
		records:         make(map[string]*MessageSendRecord),
		maxRecords:      maxRecords,
		recordRetention: retention,
		persistence:     persistence,
	}
}

// CreateRecord 创建消息记录
func (mrm *MessageRecordManager) CreateRecord(msg *HubMessage, maxRetry int, expiresAt time.Time) *MessageSendRecord {
	record := &MessageSendRecord{
		MessageID:     msg.ID,
		Message:       msg,
		Status:        MessageSendStatusPending,
		CreateTime:    time.Now(),
		RetryCount:    0,
		MaxRetry:      maxRetry,
		RetryHistory:  make([]RetryAttempt, 0),
		ExpiresAt:     expiresAt,
		Metadata:      make(map[string]interface{}),
		CustomFields:  make(map[string]interface{}),
		Tags:          make([]string, 0),
	}

	mrm.mu.Lock()
	mrm.records[msg.ID] = record
	hooks := mrm.hooks
	mrm.mu.Unlock()

	// 调用钩子
	if hooks != nil {
		_ = hooks.OnRecordCreated(record)
	}

	// 持久化
	if mrm.persistence != nil {
		_ = mrm.persistence.SaveRecord(record)
	}

	return record
}

// UpdateRecordStatus 更新记录状态
func (mrm *MessageRecordManager) UpdateRecordStatus(messageID string, status MessageSendStatus, reason FailureReason, errorMsg string) {
	mrm.mu.Lock()
	record, exists := mrm.records[messageID]
	if !exists {
		mrm.mu.Unlock()
		return
	}

	oldStatus := record.Status
	record.Status = status
	record.LastSendTime = time.Now()

	if status == MessageSendStatusFailed || status == MessageSendStatusAckTimeout {
		record.FailureReason = reason
		record.ErrorMessage = errorMsg
	} else if status == MessageSendStatusSuccess {
		record.SuccessTime = time.Now()
	}

	hooks := mrm.hooks
	persistence := mrm.persistence
	mrm.mu.Unlock()

	// 调用钩子
	if hooks != nil {
		_ = hooks.OnRecordUpdated(record, oldStatus, status)
	}

	// 持久化
	if persistence != nil {
		_ = persistence.UpdateRecord(record)
	}
}

// RecordRetryAttempt 记录重试尝试
func (mrm *MessageRecordManager) RecordRetryAttempt(messageID string, attemptNum int, duration time.Duration, err error, success bool) {
	mrm.mu.Lock()

	record, exists := mrm.records[messageID]
	if !exists {
		mrm.mu.Unlock()
		return
	}

	attempt := RetryAttempt{
		AttemptNumber: attemptNum,
		Timestamp:     time.Now(),
		Duration:      duration,
		Success:       success,
	}

	if err != nil {
		attempt.Error = err.Error()
	}

	record.RetryHistory = append(record.RetryHistory, attempt)
	record.RetryCount = attemptNum

	if success {
		record.Status = MessageSendStatusSuccess
		record.SuccessTime = time.Now()
	} else {
		record.Status = MessageSendStatusRetrying
		if attemptNum >= record.MaxRetry {
			record.Status = MessageSendStatusFailed
			record.FailureReason = FailureReasonMaxRetry
		}
	}

	hooks := mrm.hooks
	persistence := mrm.persistence
	mrm.mu.Unlock()

	// 调用钩子
	if hooks != nil {
		_ = hooks.OnRetryAttempt(record, &attempt)
	}

	// 持久化
	if persistence != nil {
		_ = persistence.UpdateRecord(record)
	}
	return
}

// RecordRetryAttemptWithUnlock 内部使用，已持有锁
func (mrm *MessageRecordManager) recordRetryAttemptWithUnlock(record *MessageSendRecord, attemptNum int, duration time.Duration, err error, success bool) {
	attempt := RetryAttempt{
		AttemptNumber: attemptNum,
		Timestamp:     time.Now(),
		Duration:      duration,
		Success:       success,
	}

	if err != nil {
		attempt.Error = err.Error()
	}

	record.RetryHistory = append(record.RetryHistory, attempt)
	record.RetryCount = attemptNum

	if success {
		record.Status = MessageSendStatusSuccess
		record.SuccessTime = time.Now()
	} else {
		record.Status = MessageSendStatusRetrying
		if attemptNum >= record.MaxRetry {
			record.Status = MessageSendStatusFailed
			record.FailureReason = FailureReasonMaxRetry
		}
	}

	// 持久化
	if mrm.persistence != nil {
		_ = mrm.persistence.UpdateRecord(record)
	}
}

// MarkUserOffline 标记用户离线
func (mrm *MessageRecordManager) MarkUserOffline(messageID string) {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()

	record, exists := mrm.records[messageID]
	if !exists {
		return
	}

	record.UserOffline = true
	record.Status = MessageSendStatusUserOffline
	record.FailureReason = FailureReasonUserOffline

	// 持久化
	if mrm.persistence != nil {
		_ = mrm.persistence.UpdateRecord(record)
	}
}

// GetRecord 获取记录
func (mrm *MessageRecordManager) GetRecord(messageID string) (*MessageSendRecord, bool) {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	record, exists := mrm.records[messageID]
	return record, exists
}

// GetFailedRecords 获取失败的记录
func (mrm *MessageRecordManager) GetFailedRecords() []*MessageSendRecord {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	var failed []*MessageSendRecord
	for _, record := range mrm.records {
		if record.Status == MessageSendStatusFailed || 
		   record.Status == MessageSendStatusAckTimeout {
			failed = append(failed, record)
		}
	}

	return failed
}

// GetRetryableRecords 获取可重试的记录
func (mrm *MessageRecordManager) GetRetryableRecords() []*MessageSendRecord {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	now := time.Now()
	var retryable []*MessageSendRecord

	for _, record := range mrm.records {
		// 条件：失败或超时，未过期，未达到最大重试次数
		if (record.Status == MessageSendStatusFailed || 
		    record.Status == MessageSendStatusAckTimeout) &&
		   record.RetryCount < record.MaxRetry &&
		   (record.ExpiresAt.IsZero() || record.ExpiresAt.After(now)) {
			retryable = append(retryable, record)
		}
	}

	return retryable
}

// CleanupExpiredRecords 清理过期记录
func (mrm *MessageRecordManager) CleanupExpiredRecords() int {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()

	now := time.Now()
	cleaned := 0

	for msgID, record := range mrm.records {
		// 清理条件：已过期或超过保留时间
		shouldClean := false

		if !record.ExpiresAt.IsZero() && record.ExpiresAt.Before(now) {
			shouldClean = true
			record.Status = MessageSendStatusExpired
			record.FailureReason = FailureReasonExpired
		} else if now.Sub(record.CreateTime) > mrm.recordRetention {
			shouldClean = true
		}

		if shouldClean {
			// 调用过期钩子
			if record.Status == MessageSendStatusExpired && mrm.hooks != nil {
				_ = mrm.hooks.OnRecordExpired(record)
			}
			
			// 调用删除钩子
			if mrm.hooks != nil {
				_ = mrm.hooks.OnRecordDeleted(record)
			}
			
			// 删除前持久化最终状态
			if mrm.persistence != nil {
				_ = mrm.persistence.UpdateRecord(record)
			}
			delete(mrm.records, msgID)
			cleaned++
		}
	}

	return cleaned
}

// GetStatistics 获取统计信息
func (mrm *MessageRecordManager) GetStatistics() map[string]int {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	stats := map[string]int{
		"total":        len(mrm.records),
		"pending":      0,
		"sending":      0,
		"success":      0,
		"failed":       0,
		"retrying":     0,
		"ack_timeout":  0,
		"user_offline": 0,
		"expired":      0,
	}

	for _, record := range mrm.records {
		switch record.Status {
		case MessageSendStatusPending:
			stats["pending"]++
		case MessageSendStatusSending:
			stats["sending"]++
		case MessageSendStatusSuccess:
			stats["success"]++
		case MessageSendStatusFailed:
			stats["failed"]++
		case MessageSendStatusRetrying:
			stats["retrying"]++
		case MessageSendStatusAckTimeout:
			stats["ack_timeout"]++
		case MessageSendStatusUserOffline:
			stats["user_offline"]++
		case MessageSendStatusExpired:
			stats["expired"]++
		}
	}

	return stats
}

// EnforceMaxRecords 强制执行最大记录数限制
func (mrm *MessageRecordManager) EnforceMaxRecords() int {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()

	if len(mrm.records) <= mrm.maxRecords {
		return 0
	}

	// 按创建时间排序，删除最旧的记录
	type recordWithTime struct {
		id   string
		time time.Time
	}

	var sorted []recordWithTime
	for id, record := range mrm.records {
		// 保留未完成的记录
		if record.Status != MessageSendStatusSuccess &&
		   record.Status != MessageSendStatusFailed &&
		   record.Status != MessageSendStatusExpired {
			continue
		}
		sorted = append(sorted, recordWithTime{id: id, time: record.CreateTime})
	}

	// 简单排序：冒泡排序
	for i := 0; i < len(sorted)-1; i++ {
		for j := 0; j < len(sorted)-i-1; j++ {
			if sorted[j].time.After(sorted[j+1].time) {
				sorted[j], sorted[j+1] = sorted[j+1], sorted[j]
			}
		}
	}

	// 删除多余的记录
	deleteCount := len(mrm.records) - mrm.maxRecords
	if deleteCount > len(sorted) {
		deleteCount = len(sorted)
	}

	removed := 0
	for i := 0; i < deleteCount; i++ {
		if mrm.persistence != nil {
			_ = mrm.persistence.DeleteRecord(sorted[i].id)
		}
		delete(mrm.records, sorted[i].id)
		removed++
	}

	return removed
}

// Clear 清空所有记录
func (mrm *MessageRecordManager) Clear() {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()

	mrm.records = make(map[string]*MessageSendRecord)
}

// GetRecordCount 获取记录总数
func (mrm *MessageRecordManager) GetRecordCount() int {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	return len(mrm.records)
}

// SetHooks 设置钩子函数
func (mrm *MessageRecordManager) SetHooks(hooks MessageRecordHooks) {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()
	mrm.hooks = hooks
}

// AddFilter 添加自定义过滤器
func (mrm *MessageRecordManager) AddFilter(filter MessageRecordFilter) {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()
	if mrm.filters == nil {
		mrm.filters = make([]MessageRecordFilter, 0)
	}
	mrm.filters = append(mrm.filters, filter)
}

// SetCustomHandler 设置自定义处理器
func (mrm *MessageRecordManager) SetCustomHandler(name string, handler interface{}) {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()
	if mrm.customHandlers == nil {
		mrm.customHandlers = make(map[string]interface{})
	}
	mrm.customHandlers[name] = handler
}

// GetCustomHandler 获取自定义处理器
func (mrm *MessageRecordManager) GetCustomHandler(name string) (interface{}, bool) {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()
	handler, exists := mrm.customHandlers[name]
	return handler, exists
}

// QueryRecords 使用自定义过滤器查询记录
func (mrm *MessageRecordManager) QueryRecords(filter MessageRecordFilter) []*MessageSendRecord {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	var results []*MessageSendRecord
	for _, record := range mrm.records {
		if filter.Match(record) {
			results = append(results, record)
		}
	}
	return results
}

// SetRecordCustomField 设置记录的自定义字段
func (mrm *MessageRecordManager) SetRecordCustomField(messageID string, key string, value interface{}) bool {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()

	record, exists := mrm.records[messageID]
	if !exists {
		return false
	}

	if record.CustomFields == nil {
		record.CustomFields = make(map[string]interface{})
	}
	record.CustomFields[key] = value
	return true
}

// GetRecordCustomField 获取记录的自定义字段
func (mrm *MessageRecordManager) GetRecordCustomField(messageID string, key string) (interface{}, bool) {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	record, exists := mrm.records[messageID]
	if !exists {
		return nil, false
	}

	if record.CustomFields == nil {
		return nil, false
	}

	value, exists := record.CustomFields[key]
	return value, exists
}

// AddRecordTag 添加记录标签
func (mrm *MessageRecordManager) AddRecordTag(messageID string, tag string) bool {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()

	record, exists := mrm.records[messageID]
	if !exists {
		return false
	}

	// 检查标签是否已存在
	for _, t := range record.Tags {
		if t == tag {
			return true
		}
	}

	record.Tags = append(record.Tags, tag)
	return true
}

// RemoveRecordTag 移除记录标签
func (mrm *MessageRecordManager) RemoveRecordTag(messageID string, tag string) bool {
	mrm.mu.Lock()
	defer mrm.mu.Unlock()

	record, exists := mrm.records[messageID]
	if !exists {
		return false
	}

	for i, t := range record.Tags {
		if t == tag {
			record.Tags = append(record.Tags[:i], record.Tags[i+1:]...)
			return true
		}
	}

	return false
}

// GetRecordsByTag 根据标签获取记录
func (mrm *MessageRecordManager) GetRecordsByTag(tag string) []*MessageSendRecord {
	mrm.mu.RLock()
	defer mrm.mu.RUnlock()

	var results []*MessageSendRecord
	for _, record := range mrm.records {
		for _, t := range record.Tags {
			if t == tag {
				results = append(results, record)
				break
			}
		}
	}
	return results
}

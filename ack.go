/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-22 19:03:45
 * @FilePath: \go-wsc\ack.go
 * @Description: ACK消息确认机制
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"fmt"
	"github.com/jpillora/backoff"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"sync"
	"time"
)

// OfflineMessageHandler 离线消息处理器接口
type OfflineMessageHandler interface {
	// HandleOfflineMessage 处理离线用户的消息
	// 返回值：是否成功处理（例如存储到数据库）
	HandleOfflineMessage(msg *HubMessage) error
}

// AckStatus ACK状态
type AckStatus string

const (
	AckStatusPending   AckStatus = "pending"   // 等待确认
	AckStatusConfirmed AckStatus = "confirmed" // 已确认
	AckStatusTimeout   AckStatus = "timeout"   // 超时
	AckStatusFailed    AckStatus = "failed"    // 失败
)

// AckMessage ACK消息结构
type AckMessage struct {
	MessageID string    `json:"message_id"` // 原消息ID
	Status    AckStatus `json:"status"`     // ACK状态
	Timestamp time.Time `json:"timestamp"`  // 时间戳
	Error     string    `json:"error"`      // 错误信息
}

// PendingMessage 待确认消息
type PendingMessage struct {
	Message   *HubMessage        // 原始消息
	AckChan   chan *AckMessage   // ACK确认通道
	Timestamp time.Time          // 发送时间
	Timeout   time.Duration      // 超时时间
	Retry     int                // 重试次数
	MaxRetry  int                // 最大重试次数
	ctx       context.Context    // 上下文
	cancel    context.CancelFunc // 取消函数
}

// AckManager ACK管理器
type AckManager struct {
	pending           map[string]*PendingMessage // 待确认消息映射
	mu                sync.RWMutex               // 读写锁
	defaultAckTimeout time.Duration              // 默认ACK超时时间
	maxRetry          int                        // 最大重试次数
	backoff           *backoff.Backoff           // 重试退避策略
	expireDuration    time.Duration              // 消息过期时间（超过此时间自动清理）
	offlineHandler    OfflineMessageHandler      // 离线消息处理器
}

// NewAckManager 创建ACK管理器
func NewAckManager(ackTimeout time.Duration, maxRetry int) *AckManager {
	return NewAckManagerWithOptions(ackTimeout, maxRetry, 0, nil)
}

// NewAckManagerWithOptions 创建ACK管理器（带选项）
func NewAckManagerWithOptions(ackTimeout time.Duration, maxRetry int, expireDuration time.Duration, offlineHandler OfflineMessageHandler) *AckManager {
	// 设置合理的默认ACK超时时间
	if ackTimeout <= 0 {
		ackTimeout = 5 * time.Second
	}

	// 设置合理的默认最大重试次数
	if maxRetry < 0 {
		maxRetry = 3
	}

	// 设置合理的默认过期时间
	if expireDuration <= 0 {
		expireDuration = 5 * time.Minute // 默认5分钟过期
	}

	// 创建退避策略
	b := &backoff.Backoff{
		Min:    100 * time.Millisecond,
		Max:    2 * time.Second,
		Factor: 2,
		Jitter: true,
	}

	return &AckManager{
		pending:           make(map[string]*PendingMessage),
		defaultAckTimeout: ackTimeout,
		maxRetry:          maxRetry,
		backoff:           b,
		expireDuration:    expireDuration,
		offlineHandler:    offlineHandler,
	}
}

// AddPendingMessage 添加待确认消息
func (am *AckManager) AddPendingMessage(msg *HubMessage, timeout time.Duration, maxRetry int) *PendingMessage {
	return am.AddPendingMessageWithExpire(msg, timeout, maxRetry, 0)
}

// AddPendingMessageWithExpire 添加待确认消息(带过期时间)
func (am *AckManager) AddPendingMessageWithExpire(msg *HubMessage, timeout time.Duration, maxRetry int, expireDuration time.Duration) *PendingMessage {
	if timeout <= 0 {
		timeout = am.defaultAckTimeout
	}
	if maxRetry < 0 {
		maxRetry = am.maxRetry
	}
	if expireDuration <= 0 {
		expireDuration = am.expireDuration
	}

	// 使用expireDuration作为context超时(给予足够时间进行重试)
	ctx, cancel := context.WithTimeout(context.Background(), expireDuration)

	pm := &PendingMessage{
		Message:   msg,
		AckChan:   make(chan *AckMessage, 1),
		Timestamp: time.Now(),
		Timeout:   timeout,
		Retry:     0,
		MaxRetry:  maxRetry,
		ctx:       ctx,
		cancel:    cancel,
	}

	am.mu.Lock()
	am.pending[msg.ID] = pm
	am.mu.Unlock()

	return pm
}

// ConfirmMessage 确认消息
func (am *AckManager) ConfirmMessage(messageID string, ack *AckMessage) bool {
	am.mu.Lock()
	pm, exists := am.pending[messageID]
	if exists {
		delete(am.pending, messageID)
	}
	am.mu.Unlock()

	if !exists {
		return false
	}

	// 先发送ACK到channel
	select {
	case pm.AckChan <- ack:
		// 成功发送后再cancel context
		pm.cancel()
		return true
	default:
		pm.cancel()
		return false
	}
}

// RemovePendingMessage 移除待确认消息
func (am *AckManager) RemovePendingMessage(messageID string) {
	am.mu.Lock()
	if pm, exists := am.pending[messageID]; exists {
		pm.cancel()
		delete(am.pending, messageID)
	}
	am.mu.Unlock()
}

// GetPendingMessage 获取待确认消息
func (am *AckManager) GetPendingMessage(messageID string) (*PendingMessage, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()
	pm, exists := am.pending[messageID]
	return pm, exists
}

// WaitForAck 等待ACK确认
func (pm *PendingMessage) WaitForAck() (*AckMessage, error) {
	select {
	case ack := <-pm.AckChan:
		return ack, nil
	case <-pm.ctx.Done():
		return &AckMessage{
			MessageID: pm.Message.ID,
			Status:    AckStatusTimeout,
			Timestamp: time.Now(),
			Error:     "ACK timeout",
		}, errorx.NewError(ErrTypeAckTimeout, pm.Message.ID)
	}
}

// WaitForAckWithRetry 等待ACK确认并支持重试
func (pm *PendingMessage) WaitForAckWithRetry(retryFunc func() error) (*AckMessage, error) {
	ticker := time.NewTicker(pm.Timeout)
	defer ticker.Stop()

	for {
		select {
		case ack := <-pm.AckChan:
			return ack, nil
		case <-ticker.C:
			// 超时，尝试重试
			if pm.Retry >= pm.MaxRetry {
				return &AckMessage{
					MessageID: pm.Message.ID,
					Status:    AckStatusTimeout,
					Timestamp: time.Now(),
					Error:     fmt.Sprintf("ACK timeout after %d retries", pm.Retry),
				}, errorx.NewError(ErrTypeAckTimeoutRetries, pm.Retry, pm.Message.ID)
			}

			pm.Retry++
			if retryFunc != nil {
				if err := retryFunc(); err != nil {
					return &AckMessage{
						MessageID: pm.Message.ID,
						Status:    AckStatusFailed,
						Timestamp: time.Now(),
						Error:     err.Error(),
					}, err
				}
			}
			ticker.Reset(pm.Timeout)
		case <-pm.ctx.Done():
			return &AckMessage{
				MessageID: pm.Message.ID,
				Status:    AckStatusTimeout,
				Timestamp: time.Now(),
				Error:     "Context cancelled",
			}, errorx.NewError(ErrTypeContextCancelled, pm.Message.ID)
		}
	}
}

// CleanupExpired 清理过期的待确认消息
func (am *AckManager) CleanupExpired() int {
	am.mu.Lock()
	defer am.mu.Unlock()

	cleaned := 0

	for msgID, pm := range am.pending {
		// 检查context是否已经过期
		select {
		case <-pm.ctx.Done():
			// Context已过期,清理消息
			if am.offlineHandler != nil {
				_ = am.offlineHandler.HandleOfflineMessage(pm.Message)
			}
			pm.cancel()
			delete(am.pending, msgID)
			cleaned++
		default:
			// Context还未过期
		}
	}

	return cleaned
}

// GetPendingCount 获取待确认消息数量
func (am *AckManager) GetPendingCount() int {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return len(am.pending)
}

// Shutdown 关闭ACK管理器
func (am *AckManager) Shutdown() {
	am.mu.Lock()
	defer am.mu.Unlock()

	for _, pm := range am.pending {
		pm.cancel()
	}
	am.pending = make(map[string]*PendingMessage)
}

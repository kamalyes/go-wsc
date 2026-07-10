/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\protocol\ack.go
 * @Description: ACK消息确认机制
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package protocol

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jpillora/backoff"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
)

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
	MessageID string    `json:"message_id"` // HubMessage消息ID
	Status    AckStatus `json:"status"`     // ACK状态
	Timestamp time.Time `json:"timestamp"`  // 时间戳
	Error     string    `json:"error"`      // 错误信息
}

// PendingMessage 待确认消息
type PendingMessage struct {
	Message   *models.HubMessage // 原始消息
	AckChan   chan *AckMessage   // ACK确认通道
	Timestamp time.Time          // 发送时间
	Timeout   time.Duration      // 超时时间
	Retry     int                // 重试次数
	MaxRetry  int                // 最大重试次数
	ctx       context.Context    // 上下文
	cancel    context.CancelFunc // 取消函数
}

// ackShardCount ACK 待确认消息的分片数量
// 64 个分片将锁竞争降低 64 倍，适用于高并发消息确认场景
const ackShardCount = 64

// AckManager ACK管理器
// 基于 syncx.ShardedMap 实现待确认消息的分片存储，替代单一 mutex + map
// 高并发下 AddPending/Confirm/Remove 操作完全并行，锁竞争降低 64 倍
type AckManager struct {
	// pending 待确认消息映射（分片存储，零全局锁）
	// key: messageID (string)，按 FNV-1a hash 分散到 64 个 shard
	pending *syncx.ShardedMap[string, *PendingMessage]

	// cfgMu 仅保护配置字段的并发读写（初始化后基本不变）
	// pending map 不受此锁保护（由 ShardedMap 内部分片锁保护）
	cfgMu          sync.RWMutex
	timeout        time.Duration                         // 默认ACK超时时间
	maxRetry       int                                   // 最大重试次数
	backoff        *backoff.Backoff                      // 重试退避策略
	offlineRepo    repository.OfflineMessageDBRepository // 离线消息处理器
	expireDuration time.Duration                         // 消息过期时间
}

// NewAckManager 创建ACK管理器
func NewAckManager(ackTimeout time.Duration, maxRetry int) *AckManager {
	return NewAckManagerWithOptions(ackTimeout, maxRetry, 0, nil)
}

// NewAckManagerWithOptions 创建ACK管理器（带选项）
func NewAckManagerWithOptions(ackTimeout time.Duration, maxRetry int, expireDuration time.Duration, offlineRepo repository.OfflineMessageDBRepository) *AckManager {
	// 设置合理的默认ACK超时时间
	ackTimeout = mathx.IF(ackTimeout > 0, ackTimeout, 5*time.Second)

	// 设置合理的最大重试次数
	maxRetry = mathx.IF(maxRetry >= 0, maxRetry, 3)

	// 设置合理的消息过期时间（默认 5 分钟）
	expireDuration = mathx.IF(expireDuration > 0, expireDuration, 5*time.Minute)

	// 创建退避策略
	b := &backoff.Backoff{
		Min:    100 * time.Millisecond,
		Max:    2 * time.Second,
		Factor: 2,
		Jitter: true,
	}

	return &AckManager{
		pending:        syncx.NewShardedMap[string, *PendingMessage](ackShardCount),
		timeout:        ackTimeout,
		maxRetry:       maxRetry,
		backoff:        b,
		expireDuration: expireDuration,
		offlineRepo:    offlineRepo,
	}
}

// AddPendingMessage 添加待确认消息
func (am *AckManager) AddPendingMessage(msg *models.HubMessage) *PendingMessage {
	return am.AddPendingMessageWithExpire(msg, am.timeout, am.maxRetry)
}

// AddPendingMessageWithExpire 添加待确认消息(带过期时间)
func (am *AckManager) AddPendingMessageWithExpire(msg *models.HubMessage, timeout time.Duration, maxRetry int) *PendingMessage {
	// 设置合理的默认ACK超时时间
	timeout = mathx.IF(timeout > 0, timeout, am.timeout)

	// 设置合理的最大重试次数
	maxRetry = mathx.IF(maxRetry >= 0, maxRetry, am.maxRetry)

	// 读取配置（加读锁）
	am.cfgMu.RLock()
	expireDuration := am.expireDuration
	am.cfgMu.RUnlock()

	// 计算 context 超时时间：timeout * (maxRetry + 1) + 额外缓冲时间
	contextTimeout := timeout*time.Duration(maxRetry+1) + 1*time.Second
	if expireDuration > 0 && contextTimeout > expireDuration {
		contextTimeout = expireDuration
	}

	ctx, cancel := context.WithTimeout(context.Background(), contextTimeout)

	pm := &PendingMessage{
		Message:   msg,
		AckChan:   make(chan *AckMessage, 1),
		Timestamp: time.Now(),
		Timeout:   timeout,
		MaxRetry:  maxRetry,
		ctx:       ctx,
		cancel:    cancel,
	}

	am.pending.Store(msg.MessageID, pm)

	return pm
}

// ConfirmMessage 确认消息
func (am *AckManager) ConfirmMessage(messageID string, ack *AckMessage) bool {
	// 原子加载并删除，仅持有单个 shard 的写锁
	pm, exists := am.pending.LoadAndDelete(messageID)
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
	pm, exists := am.pending.LoadAndDelete(messageID)
	if exists {
		pm.cancel()
	}
}

// GetPendingMessage 获取待确认消息
func (am *AckManager) GetPendingMessage(messageID string) (*PendingMessage, bool) {
	return am.pending.Load(messageID)
}

// WaitForAck 等待ACK确认
func (pm *PendingMessage) WaitForAck() (*AckMessage, error) {
	select {
	case ack := <-pm.AckChan:
		return ack, nil
	case <-pm.ctx.Done():
		return &AckMessage{
			MessageID: pm.Message.MessageID,
			Status:    AckStatusTimeout,
			Timestamp: time.Now(),
			Error:     "ACK timeout",
		}, errorx.NewError(models.ErrTypeAckTimeout, pm.Message.MessageID)
	}
}

// WaitForAckWithRetry 等待ACK确认并支持重试
func (pm *PendingMessage) WaitForAckWithRetry(retryFunc func() error) (*AckMessage, error) {
	timer := time.NewTimer(pm.Timeout)
	defer timer.Stop()

	// 辅助函数：尝试非阻塞接收 ACK
	tryReceiveAck := func() (*AckMessage, bool) {
		select {
		case ack := <-pm.AckChan:
			return ack, true
		default:
			return nil, false
		}
	}

	// 辅助函数：创建错误响应
	newErrorAck := func(status AckStatus, errMsg string, err error) (*AckMessage, error) {
		return &AckMessage{
			MessageID: pm.Message.MessageID,
			Status:    status,
			Timestamp: time.Now(),
			Error:     errMsg,
		}, err
	}

	for {
		select {
		case ack := <-pm.AckChan:
			return ack, nil

		case <-timer.C:
			// 超时后双重检查是否有 ACK（避免竞态）
			if ack, ok := tryReceiveAck(); ok {
				return ack, nil
			}

			// 检查是否达到重试上限
			if pm.Retry >= pm.MaxRetry {
				return newErrorAck(
					AckStatusTimeout,
					fmt.Sprintf("ACK timeout after %d retries", pm.Retry),
					errorx.NewError(models.ErrTypeAckTimeoutRetries, pm.Retry, pm.Message.MessageID),
				)
			}

			// 执行重试
			pm.Retry++
			if retryFunc != nil {
				if err := retryFunc(); err != nil {
					return newErrorAck(AckStatusFailed, err.Error(), err)
				}
			}

			// 重置timer等待下一次ACK
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(pm.Timeout)

			// 重置后立即检查是否有ACK（处理同步发送的情况）
			if ack, ok := tryReceiveAck(); ok {
				return ack, nil
			}

		case <-pm.ctx.Done():
			// Context 取消时双重检查 ACK
			if ack, ok := tryReceiveAck(); ok {
				return ack, nil
			}
			return newErrorAck(
				AckStatusTimeout,
				"Context cancelled",
				errorx.NewError(models.ErrTypeContextCancelled, pm.Message.MessageID),
			)
		}
	}
}

// CleanupExpired 清理过期的待确认消息
// 使用 ShardedMap.Range 遍历（分片读锁粒度），收集过期 key 后批量删除
func (am *AckManager) CleanupExpired() int {
	var expired []string
	am.pending.Range(func(msgID string, pm *PendingMessage) bool {
		// 检查context是否已经过期（使用 Err() 更可靠）
		if pm.ctx.Err() != nil {
			pm.cancel()
			expired = append(expired, msgID)
		}
		return true
	})

	// 批量删除（Range 期间持有读锁，不能直接 delete）
	for _, msgID := range expired {
		am.pending.Delete(msgID)
	}
	return len(expired)
}

// GetPendingCount 获取待确认消息数量
func (am *AckManager) GetPendingCount() int {
	return am.pending.Len()
}

// SetOfflineRepo 设置离线消息处理器
// 用于统一使用 Hub 的离线消息处理器
func (am *AckManager) SetOfflineRepo(handler repository.OfflineMessageDBRepository) {
	am.cfgMu.Lock()
	am.offlineRepo = handler
	am.cfgMu.Unlock()
}

// SetExpireDuration 设置消息过期时间
func (am *AckManager) SetExpireDuration(duration time.Duration) {
	am.cfgMu.Lock()
	am.expireDuration = duration
	am.cfgMu.Unlock()
}

// GetTimeout 获取ACK超时时间
func (am *AckManager) GetTimeout() time.Duration {
	am.cfgMu.RLock()
	defer am.cfgMu.RUnlock()
	return am.timeout
}

// GetMaxRetry 获取最大重试次数
func (am *AckManager) GetMaxRetry() int {
	am.cfgMu.RLock()
	defer am.cfgMu.RUnlock()
	return am.maxRetry
}

// Shutdown 关闭ACK管理器
// 遍历所有待确认消息取消 context，然后清空分片存储
func (am *AckManager) Shutdown() {
	am.pending.Range(func(_ string, pm *PendingMessage) bool {
		pm.cancel()
		return true
	})
	am.pending.Clear()
}

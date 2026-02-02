/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-29 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 00:00:00
 * @FilePath: \go-wsc\hub\batch_sender.go
 * @Description: Hub 批量消息发送器
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// BatchSender 批量消息发送器
type BatchSender struct {
	hub      *Hub
	ctx      context.Context
	messages map[string][]*HubMessage // userID -> messages
	mu       sync.RWMutex
}

// BatchSendResult 批量发送结果
type BatchSendResult struct {
	TotalUsers    int                    // 总用户数
	TotalMessages int                    // 总消息数
	SuccessCount  int32                  // 成功发送的消息数
	FailureCount  int32                  // 失败的消息数
	UserResults   map[string]*UserResult // 每个用户的发送结果
}

// UserResult 用户发送结果
type UserResult struct {
	UserID        string
	TotalMessages int
	SuccessCount  int
	FailureCount  int
	Errors        []error
}

// NewBatchSender 创建批量发送器
func (h *Hub) NewBatchSender(ctx context.Context) *BatchSender {
	if ctx == nil {
		ctx = context.Background()
	}
	return &BatchSender{
		hub:      h,
		ctx:      ctx,
		messages: make(map[string][]*HubMessage),
	}
}

// AddMessage 为用户添加一条消息（支持链式调用）
func (bs *BatchSender) AddMessage(userID string, msg *HubMessage) *BatchSender {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if bs.messages[userID] == nil {
		bs.messages[userID] = make([]*HubMessage, 0)
	}
	bs.messages[userID] = append(bs.messages[userID], msg)
	return bs
}

// AddMessages 为用户添加多条消息（支持链式调用）
func (bs *BatchSender) AddMessages(userID string, msgs ...*HubMessage) *BatchSender {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if bs.messages[userID] == nil {
		bs.messages[userID] = make([]*HubMessage, 0, len(msgs))
	}
	bs.messages[userID] = append(bs.messages[userID], msgs...)
	return bs
}

// AddUserMessages 批量添加用户消息映射（支持链式调用）
func (bs *BatchSender) AddUserMessages(userMessages map[string][]*HubMessage) *BatchSender {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	for userID, msgs := range userMessages {
		if bs.messages[userID] == nil {
			bs.messages[userID] = make([]*HubMessage, 0, len(msgs))
		}
		bs.messages[userID] = append(bs.messages[userID], msgs...)
	}
	return bs
}

// Count 获取当前待发送的消息统计
func (bs *BatchSender) Count() (users int, messages int) {
	bs.mu.RLock()
	defer bs.mu.RUnlock()

	users = len(bs.messages)
	for _, msgs := range bs.messages {
		messages += len(msgs)
	}
	return
}

// Clear 清空所有待发送消息
func (bs *BatchSender) Clear() *BatchSender {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.messages = make(map[string][]*HubMessage)
	return bs
}

// Execute 执行批量发送
func (bs *BatchSender) Execute() *BatchSendResult {
	bs.mu.RLock()
	// 复制一份数据，避免发送过程中被修改
	messagesCopy := make(map[string][]*HubMessage, len(bs.messages))
	for userID, msgs := range bs.messages {
		messagesCopy[userID] = make([]*HubMessage, len(msgs))
		copy(messagesCopy[userID], msgs)
	}
	bs.mu.RUnlock()

	result := &BatchSendResult{
		TotalUsers:  len(messagesCopy),
		UserResults: make(map[string]*UserResult, len(messagesCopy)),
	}

	// 计算总消息数
	for _, msgs := range messagesCopy {
		result.TotalMessages += len(msgs)
	}

	if result.TotalMessages == 0 {
		return result
	}

	// 使用 WaitGroup 等待所有用户的消息发送完成
	var wg sync.WaitGroup
	wg.Add(len(messagesCopy))

	// 并发发送每个用户的消息
	for userID, msgs := range messagesCopy {
		syncx.Go(bs.ctx).Exec(func() {
			defer wg.Done()
			bs.sendUserMessages(userID, msgs, result)
		})
	}

	wg.Wait()
	return result
}

// sendUserMessages 发送单个用户的所有消息
func (bs *BatchSender) sendUserMessages(userID string, msgs []*HubMessage, result *BatchSendResult) {
	userResult := &UserResult{
		UserID:        userID,
		TotalMessages: len(msgs),
		Errors:        make([]error, 0),
	}

	// 顺序发送该用户的所有消息
	for _, msg := range msgs {
		sendResult := bs.hub.SendToUserWithRetry(bs.ctx, userID, msg)
		if sendResult.Success {
			userResult.SuccessCount++
			atomic.AddInt32(&result.SuccessCount, 1)
		} else {
			userResult.FailureCount++
			atomic.AddInt32(&result.FailureCount, 1)
			if sendResult.FinalError != nil {
				userResult.Errors = append(userResult.Errors, sendResult.FinalError)

				// 调用批量发送失败回调
				if bs.hub.batchSendFailureCallback != nil {
					bs.hub.batchSendFailureCallback(userID, msg, sendResult.FinalError)
				}
			}
		}
	}

	// 保存用户结果（需要加锁）
	bs.mu.Lock()
	result.UserResults[userID] = userResult
	bs.mu.Unlock()
}

// ExecuteAsync 异步执行批量发送（不等待结果）
func (bs *BatchSender) ExecuteAsync(callback func(*BatchSendResult)) {
	syncx.Go(bs.ctx).Exec(func() {
		result := bs.Execute()
		if callback != nil {
			callback(result)
		}
	})
}

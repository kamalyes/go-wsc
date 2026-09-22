/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 10:25:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 10:25:00
 * @FilePath: \go-wsc\overload\interfaces.go
 * @Description: 过载保护包依赖端口
 *
 * 接口定义在消费方（本包），实现由 hub.Hub 提供 —— 「消费者定义接口」原则，
 * 使本包不依赖 hub 的具体类型，仅依赖其能力。
 *
 * 批量写器端口（StorageBatchWriter/ObserverNotifier）已迁至 batcher 包，
 * 本包仅保留过载与投递相关端口。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package overload

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
)

// BatchSendFailureCallback 批量发送单条消息失败时的回调
type BatchSendFailureCallback func(userID string, msg *models.HubMessage, err error)

// UserMessageSender 用户消息发送能力（BatchSender 所需）
type UserMessageSender interface {
	// SendToUserWithRetry 向指定用户发送消息（带重试）
	SendToUserWithRetry(ctx context.Context, toUserID string, msg *models.HubMessage) *models.SendResult
}

// ShaperInterval 整形器速率查询（interval 返回当前令牌间隔，纳秒）
type ShaperInterval interface {
	Interval() int64
}

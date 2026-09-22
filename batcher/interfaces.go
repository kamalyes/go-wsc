/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 19:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-20 19:00:00
 * @FilePath: \go-wsc\batcher\interfaces.go
 * @Description: 批量写器包依赖端口
 *
 * 接口定义在消费方（本包），实现由 hub.Hub 提供 —— 「消费者定义接口」原则，
 * 使本包不依赖 hub 的具体类型，仅依赖其能力。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package batcher

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ObserverNotifier 观察者通知能力（ObserverNotificationBatcher 所需）
type ObserverNotifier interface {
	// NotifyObserversDirect 直接通知观察者（不经批处理队列，避免递归入队）
	NotifyObserversDirect(msg *models.HubMessage, namespace string, groupIDs []string)
}

// StorageBatchWriter 存储批量写入能力
// （HeartbeatStatsUpdater / MessageStatsBatcher / MessageStatusUpdater 共同所需）
//
// 三者都是「攒批 → 单事务落库」的写放大抑制剂，能力面完全一致，故共用一个端口。
//
// flush 刻意不使用 Context()：三个更新器都用
// context.WithTimeout(context.Background(), 5s)，避免 Hub 关闭时最后一批
// 落库被 cancel 截断。Context() 仅供日志关联。
type StorageBatchWriter interface {
	// Context 返回 Hub 生命周期上下文（仅用于日志关联）
	Context() context.Context
	// GetLogger 日志器
	GetLogger() spi.Logger

	// GetConnectionRecordRepo 连接记录仓储（心跳时间戳落 connect 表）
	GetConnectionRecordRepo() spi.ConnectionStore
	// GetConnectionQualityRepository 连接质量仓储（心跳统计 / 消息统计）
	GetConnectionQualityRepository() spi.ConnectionQualityStore
	// GetMessageRecordRepo 消息记录仓储（消息状态更新）
	GetMessageRecordRepo() spi.MessageSink
}

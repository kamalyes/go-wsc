/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 09:21:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 09:21:00
 * @FilePath: \go-wsc\spi\message_queue.go
 * @Description: 消息队列 SPI - MessageQueue 接口契约定义

 * 低层 FIFO 队列契约，供离线消息等「先落队、后消费」场景使用
 * 与 spi.OfflineQueue 的分工：
 *   - OfflineQueue 是面向业务的离线消息处理器（Redis 队列 + RDBMS 双写、
 *     推送状态管理、按用户/分组语义组织）；
 *   - MessageQueue 是它底层依赖的通用队列原语，只有入队/出队/长度/清空
 *     这类与业务无关的操作

 * Redis 实现见 adapter/redis 的 MessageQueue；
 * NATS / Kafka 适配器可各自实现本契约接入

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// MessageQueue 通用消息队列契约
//
// 消费者按 queueName 分区，各分区互不干扰实现需保证同一 queueName 内
// 先进先出；跨 queueName 不保证顺序
type MessageQueue interface {
	// Enqueue 入队消息
	Enqueue(ctx context.Context, queueName string, msg *models.HubMessage) error

	// Dequeue 出队消息（阻塞式，带看门狗锁保证消息不会因消费者崩溃而丢失）
	// timeout 为最长阻塞等待时间，超时返回 (nil, nil)
	Dequeue(ctx context.Context, queueName string, timeout time.Duration) (*models.HubMessage, error)

	// DequeueBatch 批量出队（非阻塞，单次往返）
	// 返回条数不足 count 表示队列已空
	DequeueBatch(ctx context.Context, queueName string, count int) ([]*models.HubMessage, error)

	// GetLength 获取队列当前长度
	GetLength(ctx context.Context, queueName string) (int64, error)

	// Clear 清空队列
	Clear(ctx context.Context, queueName string) error

	// Peek 查看队列头部消息（不移除）
	Peek(ctx context.Context, queueName string) (*models.HubMessage, error)
}

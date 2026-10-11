/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:23:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:23:00
 * @FilePath: \go-wsc\publisher\envelope.go
 * @Description: 投递信封构建器 - 发布侧链式 API
 *
 * NewEnvelope(msg).Deliver() / .Broadcast() 一行构造投递信封；
 * MessageID 空值自动补随机 ID（幂等去重键前提），PublishedAt 自动填充
 *
 * 发布包独立于 Hub：只依赖 spi + models，任意 Go 业务服务引入即可；
 * 异构语言发布者按信封 JSON 结构直投 MQ（字段契约见 spi.DeliverEnvelope）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package publisher

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// EnvelopeBuilder 投递信封链式构建器
type EnvelopeBuilder struct {
	msg           *models.HubMessage
	excludeSender bool
}

// NewEnvelope 从业务消息创建信封构建器
func NewEnvelope(msg *models.HubMessage) *EnvelopeBuilder {
	return &EnvelopeBuilder{msg: msg}
}

// ExcludeSender 广播排除发送者自身连接（多端同步场景）
func (b *EnvelopeBuilder) ExcludeSender() *EnvelopeBuilder {
	b.excludeSender = true
	return b
}

// Deliver 构造投递语义信封（P2P / 群组，queue 消费）
func (b *EnvelopeBuilder) Deliver() *spi.DeliverEnvelope {
	return b.build(spi.IntentDeliver)
}

// Broadcast 构造广播语义信封（fanout 消费，每节点各一份）
func (b *EnvelopeBuilder) Broadcast() *spi.DeliverEnvelope {
	return b.build(spi.IntentBroadcast)
}

// build 填充信封公共字段（MessageID 空值自动补随机 ID）
func (b *EnvelopeBuilder) build(intent spi.DeliverIntent) *spi.DeliverEnvelope {
	msg := b.msg
	if msg == nil {
		return &spi.DeliverEnvelope{Intent: intent, PublishedAt: time.Now()}
	}
	if msg.MessageID == "" {
		msg.MessageID = newMessageID()
	}
	return &spi.DeliverEnvelope{
		Intent:        intent,
		PublishedAt:   time.Now(),
		ExcludeSender: b.excludeSender,
		Message:       msg,
	}
}

// newMessageID 生成发布侧默认消息 ID（128bit 随机 hex，无碰撞量级）
//
// 业务方有自己的消息 ID 体系时应显式设置 MessageID（幂等键以业务语义优先）
func newMessageID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败仅在系统熵池异常时发生，退化为纳秒时间戳保证唯一性下限
		return time.Now().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(buf)
}

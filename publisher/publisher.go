/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:28:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:28:00
 * @FilePath: \go-wsc\publisher\publisher.go
 * @Description: 投递指令发布器 - 业务侧统一 publish 入口
 *
 * 所有生产者（含宿主服务）统一经此 publish，Deliver 直调入口逐步退役。
 * 发布器不依赖 Hub、不依赖 go-wsc 运行时——任意 Go 业务服务引入即用
 *
 * RouteAdvisor（路由感知定向，粘性 L0）在 NATS 适配器落地时接入：
 * 契约已就位于 spi.RouteAdvisor，发布侧定向是纯性能优化不影响正确性
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package publisher

import (
	"context"
	"errors"

	"github.com/kamalyes/go-wsc/spi"
)

// 发布侧校验错误（调用方 bug 即刻暴露，不进 MQ 浪费带宽）
var (
	ErrBusMissing      = errors.New("publisher: event bus is nil")
	ErrPublisherName   = errors.New("publisher: publisher name is required (audit subject)")
	ErrEnvelopeInvalid = errors.New("publisher: envelope is nil or missing message")
)

// Publisher 投递指令发布器（线程安全：无状态转发，字段只读）
type Publisher struct {
	bus  spi.EventBus
	name string
}

// Builder 发布器链式构建器（对齐项目 builder 装配惯例）
type Builder struct {
	bus  spi.EventBus
	name string
}

// New 从事件总线创建构建器
func New(bus spi.EventBus) *Builder {
	return &Builder{bus: bus}
}

// WithPublisher 设置发布者标识（审计与 ACL 主体，必填）
func (b *Builder) WithPublisher(name string) *Builder {
	b.name = name
	return b
}

// Build 构造发布器（bus 与 name 缺失即报错，装配期失败优于运行期）
func (b *Builder) Build() (*Publisher, error) {
	if b.bus == nil {
		return nil, ErrBusMissing
	}
	if b.name == "" {
		return nil, ErrPublisherName
	}
	return &Publisher{bus: b.bus, name: b.name}, nil
}

// Name 发布者标识（审计回显）
func (p *Publisher) Name() string {
	return p.name
}

// Publish 发布投递信封
//
// 信封 Publisher 字段由发布器统一盖戳（调用方无须设置也无法伪造），
// PublishedAt / MessageID 空值由 EnvelopeBuilder 兜底，此处只做终检
func (p *Publisher) Publish(ctx context.Context, env *spi.DeliverEnvelope) error {
	if env == nil || env.Message == nil {
		return ErrEnvelopeInvalid
	}
	if env.Message.MessageID == "" {
		env.Message.MessageID = newMessageID()
	}
	env.Publisher = p.name
	return p.bus.Publish(ctx, env)
}

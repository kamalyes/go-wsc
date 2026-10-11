/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 21:58:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 21:58:00
 * @FilePath: \go-wsc\broker\envelope.go
 * @Description: 投递信封消费侧校验与路由恢复
 *
 * 消费入口三道防线：
 *   1. 结构校验：Message/MessageID/Publisher/AppID 非空 + 载荷上限
 *   2. 路由恢复：HubMessage 路由字段（AppID/Namespace/GroupIDs）重注入 ctx
 *      （ctx 路由元数据不跨进程序列化，信封随消息体流转，消费侧恢复）
 *   3. 意图推导：Intent 未设置时按路由字段推导（GroupIDs/空 Receiver → 广播）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"context"
	"errors"
	"fmt"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
)

// MaxEnvelopeBytes 信封编码后大小上限（保护 MQ 带宽与写泵；超大载荷走引用模式）
const MaxEnvelopeBytes = 256 * 1024

// 信封校验错误（哨兵值，消费侧按错误类别决定 ack 丢弃还是 NACK 重投）
var (
	ErrEnvelopeNilMessage   = errors.New("envelope message is nil")
	ErrEnvelopeMessageID    = errors.New("envelope message_id is empty (dedup key invalid)")
	ErrEnvelopePublisher    = errors.New("envelope publisher is empty (audit subject missing)")
	ErrEnvelopeAppID        = errors.New("envelope app_id is empty (tenant isolation invalid)")
	ErrEnvelopeHopsExceeded = errors.New("envelope hops exceeded forwarding limit")
)

// ValidateEnvelope 消费入口结构校验（三道防线的第一道）
//
// 校验失败的信封属于发布方 bug 或恶意构造：NACK 重投无意义
// （重投一万次也还是非法的），消费侧应 ack 丢弃并计数告警
func ValidateEnvelope(env *spi.DeliverEnvelope) error {
	if env == nil || env.Message == nil {
		return ErrEnvelopeNilMessage
	}
	if env.Message.MessageID == "" {
		return ErrEnvelopeMessageID
	}
	if env.Publisher == "" {
		return ErrEnvelopePublisher
	}
	if env.Message.AppID == "" {
		return ErrEnvelopeAppID
	}
	if env.Hops > DefaultMaxHops {
		return ErrEnvelopeHopsExceeded
	}
	return nil
}

// RestoreRouteCtx 信封路由字段重注入 ctx（第二道防线）
//
// Deliver 决策树从 ctx 信封取路由维度（appID/namespace/groupIDs），
// MQ 传输只序列化消息体不序列化 ctx——消费侧必须从 msg 字段重建路由信封：
//
//	严格场景（P2P/群组）经 EnsureRouteDefaults 补全 namespace；
//	广播场景保留空 namespace（全命名空间语义）
func RestoreRouteCtx(ctx context.Context, msg *models.HubMessage) context.Context {
	route := routing.NewRoute().
		WithAppID(msg.AppID).
		WithNamespace(msg.Namespace).
		WithGroupIDs(msg.GroupIDs)
	return route.Inject(ctx)
}

// ResolveIntent 意图推导（第三道防线）
//
// 显式 Intent 优先；未设置时按路由字段推导——
// 无 Receiver 且无 GroupIDs 视为广播（namespace 非空=命名空间广播，空=全局），
// 其余（有 Receiver 或有 GroupIDs）视为投递
func ResolveIntent(env *spi.DeliverEnvelope) spi.DeliverIntent {
	if env.Intent != spi.IntentUnset {
		return env.Intent
	}
	msg := env.Message
	if msg.Receiver == "" && len(msg.GroupIDs) == 0 {
		return spi.IntentBroadcast
	}
	return spi.IntentDeliver
}

// EnvelopeSizeOf 信封编码尺寸估算（载荷上限判定，JSON 序列化前的廉价预检）
//
// 精确尺寸由序列化后判定（适配器 Publish 侧），此处提供消费侧的
// 二次防线：Content + Data map 估算值超限即拒绝
func EnvelopeSizeOf(env *spi.DeliverEnvelope) int {
	if env == nil || env.Message == nil {
		return 0
	}
	size := len(env.Message.Content)
	for k := range env.Message.Data {
		size += len(k) + 64 // 值按引用语义估算（map 值编码后量级）
	}
	return size
}

// FormatDedupKey 幂等去重键（MessageID + Receiver 复合键）
//
// 广播信封无 Receiver 语义，按 MessageID 单键去重（fanout 语义下
// 每节点各消费一份，去重窗防的是同节点的 redelivery 重复）
func FormatDedupKey(env *spi.DeliverEnvelope) string {
	msg := env.Message
	if msg.Receiver == "" {
		return msg.MessageID
	}
	return fmt.Sprintf("%s\x00%s", msg.MessageID, msg.Receiver)
}

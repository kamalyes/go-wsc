/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 22:09:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 22:09:00
 * @FilePath: \go-wsc\broker\consumer.go
 * @Description: 消息总线消费循环 - 订阅→校验→去重→路由恢复→Deliver→ack
 *
 * 消费收敛点：投递逻辑零新增——全部复用 hub.Deliver 决策树
 * （P2P/群组可靠/广播/离线/重试），consumer 只做入口治理：
 *
 *   express（IsExpress） → 跳过去重（latest-wins 重复无害）
 *   reliable / 必达      → 本地两代 TTL 去重窗（零 Redis 热路径）
 *   校验失败             → ack 丢弃 + 计数（重投无意义的非法信封）
 *   Deliver 返回错误     → NACK（error 返回，适配器按语义重投）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import (
	"context"
	"errors"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// DeliverFunc 投递收敛端口（hub.Deliver 的函数签名，注入避免依赖 hub 包）
type DeliverFunc func(ctx context.Context, msg *models.HubMessage, excludeSender bool) *models.DeliverResult

// Consumer 消息总线消费者（订阅循环的入口治理层）
type Consumer struct {
	deliver DeliverFunc
	dedup   *DedupWindow
	logger  spi.Logger
}

// NewConsumer 创建消费者（dedupTTL/dedupShards 零值走默认参数）
func NewConsumer(deliver DeliverFunc, dedupTTL time.Duration, dedupShards int, logger spi.Logger) *Consumer {
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	return &Consumer{
		deliver: deliver,
		dedup:   NewDedupWindow(dedupTTL, dedupShards),
		logger:  logger,
	}
}

// HandleDeliver 构造投递语义（queue）消费回调
func (c *Consumer) HandleDeliver() spi.EventHandler {
	return c.handle
}

// HandleBroadcast 构造广播语义（fanout）消费回调
//
// 两种 topic 的入口治理相同（差异在适配器的分发语义）：
// deliver 一条消息单节点消费，broadcast 每节点各消费一份
func (c *Consumer) HandleBroadcast() spi.EventHandler {
	return c.handle
}

// handle 消费主路径：校验 → 去重 → 路由恢复 → Deliver → 结果判定
func (c *Consumer) handle(ctx context.Context, env *spi.DeliverEnvelope) error {
	// 三道防线之一：结构校验（非法信封 ack 丢弃，重投无意义）
	if err := ValidateEnvelope(env); err != nil {
		c.logger.WarnKV("非法投递信封已丢弃",
			"error", err.Error(),
			"publisher", envPublisher(env),
			"message_id", envMessageID(env),
		)
		return nil
	}

	msg := env.Message

	// 等级分流去重：express 跳过，可靠/必达过本地两代窗
	// 标记先行保留 Seen 原子首见语义（拦并发重复副本）；投递受理失败 Unmark 回滚——否则重投副本被去重窗拦截，消息永久丢失（见下方）
	dedupKey := FormatDedupKey(env)
	marked := false
	if !msg.ResolveGuarantee().IsExpress() {
		if !c.dedup.Seen(dedupKey) {
			c.logger.DebugKV("重复投递信封已跳过",
				"message_id", msg.MessageID,
				"receiver", msg.Receiver,
			)
			return nil
		}
		marked = true
	}

	// 三道防线之二：路由恢复（msg 路由字段重注入 ctx，Deliver 决策树依赖 ctx 信封）
	deliverCtx := RestoreRouteCtx(ctx, msg)

	// 三道防线之三：意图推导（fanout topic 的消息由决策树广播分支承接，
	// intent 显式值供路由统计与 P1 reroute 使用）
	_ = ResolveIntent(env)

	result := c.deliver(deliverCtx, msg, env.ExcludeSender)
	if result == nil || !result.HasError() {
		return nil
	}
	// 投递受理失败：回滚去重标记后 NACK 触发适配器重投（At-Least-Once 语义）
	// 去重标记先于投递存在"标记→失败→重投被拦"窗口：重投副本 Seen 判定
	// 为重复直接 ack，消息从未成功投递却被确认消费，必达档永久丢失
	if marked {
		c.dedup.Unmark(dedupKey)
	}
	return errors.Join(result.Errors...)
}

// envPublisher / envMessageID nil 安全的日志取值辅助
func envPublisher(env *spi.DeliverEnvelope) string {
	if env == nil {
		return ""
	}
	return env.Publisher
}

func envMessageID(env *spi.DeliverEnvelope) string {
	if env == nil || env.Message == nil {
		return ""
	}
	return env.Message.MessageID
}

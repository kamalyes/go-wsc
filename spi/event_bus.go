/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 21:52:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 21:52:00
 * @FilePath: \go-wsc\spi\event_bus.go
 * @Description: 事件总线 SPI - EventBus 投递指令发布/订阅契约定义
 *
 * 与 spi.MessageQueue 的分工：
 *   - MessageQueue 是离线消息的低层 FIFO 原语（拉模式，先落队后消费）
 *   - EventBus 是投递指令的发布订阅总线（推模式），入口总线与内部传输
 *     共用的单一消息通道
 *
 * 适配器实现：adapter/nats（core + JetStream 分级）、adapter/memory（契约测试）
 * 依赖方向恒为 adapter → spi（对齐 wiring.go 的 StoreHooks 装配哲学）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// EventBusTopic 订阅语义（Publish 侧由信封 Intent 自动路由，无需指定）
type EventBusTopic string

const (
	// TopicDeliver 投递语义：queue 消费（consumer group 负载均衡，任一节点消费）
	TopicDeliver EventBusTopic = "deliver"
	// TopicBroadcast 广播语义：fanout 消费（每节点独立消费一份，只投本地连接）
	TopicBroadcast EventBusTopic = "broadcast"
)

// DeliverIntent 投递意图（决定 topic 路由与消费语义）
type DeliverIntent uint8

const (
	// IntentUnset 未设置（零值）：消费端按 Message 路由字段（GroupIDs/Namespace）推导
	IntentUnset DeliverIntent = iota
	// IntentDeliver P2P / 群组投递（queue 语义消费）
	IntentDeliver
	// IntentBroadcast 全局/命名空间广播（fanout 语义消费）
	IntentBroadcast
)

// DeliverEnvelope 投递信封 - MQ 传输的外层包装
//
// 路由维度复用 HubMessage 自带字段（AppID/Namespace/GroupIDs/Receiver），
// 信封只补 ctx 侧参数、溯源与转发防环：
//   - MessageID 必须非空：Go 发布者由 publisher 自动生成，异构发布者空值直接拒绝
//     （MessageID 是幂等去重键的一半，空值即去重失效）
//   - Hops 转发跳数防环：消费端本地 miss 转投定向 topic 时 +1，超限按等级兜底
//     （必达走离线库 / 可靠记失败态 / express 丢弃）
type DeliverEnvelope struct {
	Intent        DeliverIntent      `json:"intent"`                   // 投递意图，空值消费端按路由字段推导
	Publisher     string             `json:"publisher"`                // 发布者标识（审计与 ACL 主体，必填）
	PublishedAt   time.Time          `json:"published_at"`             // 发布时刻（消费端时延监控）
	ExcludeSender bool               `json:"exclude_sender,omitempty"` // 广播排除发送者连接
	Hops          uint8              `json:"hops,omitempty"`           // 转发跳数防环
	Message       *models.HubMessage `json:"message"`
}

// EventBus 事件总线契约 - 下行投递指令的外部消息通道
//
// 语义分级（NATS 落地形态，其他适配器等价映射）：
//   - express（IsExpress）→ core subject：at-most-once，~0.3ms
//   - reliable / 必达     → JetStream 分片：持久化 + 重投，分片内串行保序
//   - broadcast           → core fanout：每节点一份，只投本地连接
type EventBus interface {
	// Publish 发布投递指令，同步返回表示 MQ 已接收（At-Least-Once 前提）
	// topic 路由由信封 Intent + Message 投递等级自动决定
	Publish(ctx context.Context, env *DeliverEnvelope) error

	// Subscribe 订阅投递指令，handler 返回 error 时按实现的 NACK 语义重投
	// queue 语义（TopicDeliver）与 fanout 语义（TopicBroadcast）由 topic 参数区分
	Subscribe(ctx context.Context, topic EventBusTopic, handler EventHandler) error

	// Close 优雅关闭：排空在途消息、退出订阅循环
	Close() error
}

// EventHandler 投递指令消费回调
//
// 返回 nil 表示消费成功（适配器侧 ack）；返回 error 触发 NACK 重投
// （At-Least-Once 下消费方必须幂等，broker 域按投递等级分流去重）
type EventHandler func(ctx context.Context, env *DeliverEnvelope) error

// RouteAdvisor 路由查询端口 - 发布侧粘性定向（L0）的依据
//
// 实现：redisadapter 基于在线索引（GetUserNodes）+ 本地短 TTL 缓存；
// 未注入时 publisher 自动降级投共享通道，行为不变——粘性是性能优化
// 不是正确性前提
type RouteAdvisor interface {
	// NodeOf 返回用户连接所在节点；空列表 = 离线/未知（投共享 topic 或直接离线）
	NodeOf(ctx context.Context, appID, namespace, userID string) []string
}

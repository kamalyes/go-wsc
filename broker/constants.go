/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-11 21:52:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-11 21:52:00
 * @FilePath: \go-wsc\broker\constants.go
 * @Description: 消息总线消费域常量 - topic 命名与去重窗默认参数
 *
 * topic 命名规范（{prefix}.{appID}.{channel}，适配器内部映射真实 subject）：
 *   - deliver   可靠/必达投递（JetStream 分片，queue 语义）
 *   - xpress    express 共享兜底（core，queue 语义）
 *   - x.{nodeID} express 定向（core，粘性 L0，目标节点独占）
 *   - broadcast 广播（core fanout，每节点一份）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package broker

import "time"

// topic 通道名（适配器按 driver 映射真实 subject，channel 语义跨驱动一致）
const (
	// ChannelDeliver 可靠/必达投递通道（queue 语义，分区键 = Receiver）
	ChannelDeliver = "deliver"
	// ChannelExpress express 共享兜底通道（queue 语义，latest-wins）
	ChannelExpress = "xpress"
	// ChannelExpressDirectedPrefix express 定向通道前缀（x.{nodeID}，目标节点独占）
	ChannelExpressDirectedPrefix = "x"
	// ChannelBroadcast 广播通道（fanout 语义，每节点各消费一份）
	ChannelBroadcast = "broadcast"
)

// 去重窗默认参数（不开放配置：去重行为由投递等级自动决定，避免死配置）
const (
	// DefaultDedupTTL 可靠档本地两代去重窗的覆盖窗口
	// （MQ redelivery 与发布者超时重试的重复间隔上限量级）
	DefaultDedupTTL = 60 * time.Second
	// DefaultDedupShards 去重窗分片数（降低锁竞争，百核以下免调）
	DefaultDedupShards = 64
)

// DefaultMaxHops 转发跳数上限（Hops 防环：L1 miss 转投定向 topic 的深度上限）
const DefaultMaxHops = 2

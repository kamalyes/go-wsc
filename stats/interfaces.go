/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 12:21:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 12:21:00
 * @FilePath: \go-wsc\stats\interfaces.go
 * @Description: 统计域依赖端口
 *
 * 接口定义在消费方（本包），实现由 hub.Hub 提供 —— 「消费者定义接口」原则。
 * 本包不依赖 hub 的具体类型，仅依赖其能力，因此 hub 可以反向 import 本包。
 *
 * 端口按消费者实际能力面裁剪，不抄 Hub 方法表（镜像式接口零收益）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package stats

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/spi"
)

// Host 统计域所需的 Hub 能力面。
//
// 追踪方法（Track*）是「事件 → 统计」的单向漏斗：由连接生命周期与消息
// 投递路径调用，本包据此判断是否采样、并把增量交给批量聚合器。
type Host interface {
	// Context 返回 Hub 生命周期上下文（仅用于日志关联；追踪路径优先用
	// client.Context 以保留连接级 trace_id）
	Context() context.Context
	// GetLogger 日志器
	GetLogger() spi.Logger
	// GetNodeID 当前节点 ID（统计上报的维度之一）
	GetNodeID() string
	// GetStartTime Hub 启动时间（运行时长统计）
	GetStartTime() time.Time
	// IsStarted Hub 是否已启动（健康状态）
	IsStarted() bool

	// GetShardedRegistry 连接注册表（读取实时连接数）
	GetShardedRegistry() *connection.ShardedRegistry

	// GetStatsRepo 节点统计仓储；未注入时返回 nil，追踪退化为 no-op
	GetStatsRepo() spi.HubStats
	// GetOnlineStatusRepo 在线状态仓储；未注入时返回 nil
	GetOnlineStatusRepo() spi.OnlineStore
	// GetConnectionQualityRepository 连接质量仓储；未注入时返回 nil
	GetConnectionQualityRepository() spi.ConnectionQualityStore
	// GetConnectionRecordRepo 连接记录仓储；未注入时返回 nil
	GetConnectionRecordRepo() spi.ConnectionStore

	// GetMessageStatsBatcher 消息统计批量聚合器；未启用时返回 nil
	GetMessageStatsBatcher() *batcher.MessageStatsBatcher
	// GetHeartbeatBatcher 心跳统计批量聚合器；未启用时返回 nil
	GetHeartbeatBatcher() *batcher.HeartbeatStatsUpdater
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 16:12:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 16:12:00
 * @FilePath: \go-wsc\connection\interfaces.go
 * @Description: 连接域依赖端口
 *
 * 接口定义在消费方（本包），实现由 hub.Hub 提供 —— 「消费者定义接口」原则，
 * 使本包不依赖 hub 的具体类型，仅依赖其能力。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package connection

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// PumpHost 读写泵所需的 Hub 能力面（消费者定义接口，hub 实现）
type PumpHost interface {
	// Context Hub 生命周期上下文（写泵退出信号之一）
	Context() context.Context
	// GetLogger 日志器
	GetLogger() spi.Logger
	// GetBatchWriter 数据 lane 的 writev 合批写器（frame_writer.go）
	GetBatchWriter() *BatchWriter
	// IsShuttingDown Hub 是否正在关闭（读错误短路，避免误判为异常断开）
	IsShuttingDown() bool
	// UnregisterClient 读泵退出时注销客户端（defer 兜底清理）
	UnregisterClient(client *models.Client)
	// TrackConnectionError 异常断开时记录错误到连接记录（stats 域漏斗）
	TrackConnectionError(ctx context.Context, connectionID string, userType models.UserType, err error)
	// OnWriteBatch 准入闸门埋点：每写批 1 次 atomic add（在途量回收）
	OnWriteBatch(batch int)
	// HandleTextMessage 文本消息分发（消息域）
	HandleTextMessage(ctx context.Context, client *models.Client, data []byte)
	// HandleBinaryMessage 二进制消息分发（消息域）
	HandleBinaryMessage(client *models.Client, data []byte)
}

// HeartbeatHost 心跳保活所需的 Hub 能力面（消费者定义接口，hub 实现）
type HeartbeatHost interface {
	// Context Hub 生命周期上下文（续期 worker 退出信号）
	Context() context.Context
	// GetLogger 日志器
	GetLogger() spi.Logger
	// GetShardedRegistry 连接注册表（SSE 超时扫描遍历）
	GetShardedRegistry() *ShardedRegistry
	// GetOnlineStatusRepo 在线状态仓储；未注入时续期退化为 no-op
	GetOnlineStatusRepo() spi.OnlineStore
	// GetClientTimeout 心跳超时阈值（时间轮调度与 SSE 扫描共用）
	GetClientTimeout() time.Duration
	// SetHeartbeatConfig 运行时更新心跳间隔与超时阈值
	SetHeartbeatConfig(interval, timeout time.Duration)
	// GetHeartbeatRefreshInterval 续期 worker 的 flush 间隔（默认 2s）
	GetHeartbeatRefreshInterval() time.Duration
	// UnregisterClient 注销客户端（超时/外部心跳更新失败时调用）
	UnregisterClient(client *models.Client)
	// TrackHeartbeatStats 心跳统计追踪（stats 域漏斗）
	TrackHeartbeatStats(client *models.Client)
}

// EvictHook 驱逐回调端口（消费者侧端口：Hub 实现，扫描器不感知驱逐细节）
//
// 实现方职责（见 hub 编排层）：
//   - 慢消费者驱逐埋点（overload 域指标）
//   - KickOut 控制消息通知客户端驱逐理由
//   - Unregister 断链（断链不丢消息）
type EvictHook interface {
	OnSlowConsumerEvicted(client *models.Client, consecutive int, ratio float64)
}

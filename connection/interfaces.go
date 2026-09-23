/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 16:12:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 17:52:31
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

// EvictHook 驱逐回调端口（消费者侧端口：Hub 实现，扫描器不感知驱逐细节）
//
// 实现方职责（见 hub 编排层）：
//   - 慢消费者驱逐埋点（overload 域指标）
//   - KickOut 控制消息通知客户端驱逐理由
//   - Unregister 断链（断链不丢消息）
type EvictHook interface {
	OnSlowConsumerEvicted(client *models.Client, consecutive int, ratio float64)
}

// HeartbeatHost 心跳子域所需的 Hub 能力面
//
// 心跳链路（前置拦截 → 时间轮续期 → Redis 续期入队 → 回调链 → 统计追踪）
// 的外部依赖经本端口注入，由 hub 编排层实现；回调 getter 用结构类型声明，
// 避免 hub ⇄ connection 循环 import（与 group.Host 回调端口同款约定），
// 未注册时返回 nil，调用方跳过（不可假定非空）
type HeartbeatHost interface {
	// Context 返回 Hub 生命周期上下文（仅用于日志关联）
	Context() context.Context
	// GetLogger 日志器
	GetLogger() spi.Logger
	// Unregister 异步注销客户端（心跳超时与 SSE 兜底触发断链）
	Unregister(client *models.Client)
	// TrackHeartbeatStats 心跳统计追踪（stats 域漏斗，不阻塞主流程）
	TrackHeartbeatStats(client *models.Client)
	// EnqueueHeartbeatRenew 心跳 Redis 在线索引续期入队（满则丢弃，下次心跳补投）
	EnqueueHeartbeatRenew(client *models.Client)

	// GetBeforeHeartbeatCallback 心跳前置回调（返回 false 跳过后续心跳处理）
	GetBeforeHeartbeatCallback() func(client *models.Client) bool
	// GetHeartbeatReportCallback 心跳上报回调（业务侧按心跳周期感知活跃度）
	GetHeartbeatReportCallback() func(client *models.Client)
	// GetAfterHeartbeatCallback 心跳后置回调
	GetAfterHeartbeatCallback() func(client *models.Client)
	// GetHeartbeatTimeoutCallback 心跳超时回调（超时注销时触发）
	GetHeartbeatTimeoutCallback() func(clientID string, userID string, lastHeartbeat time.Time)
}

// LifecycleHost 连接生命周期子域所需的 Hub 能力面
//
// 多端登录治理与踢出断链的外部依赖经本端口注入，由 hub 编排层实现
type LifecycleHost interface {
	// GetLogger 日志器
	GetLogger() spi.Logger
	// IsShuttingDown 编排层是否正在关闭（决定断链是否先发 1001 GoingAway 控制帧）
	IsShuttingDown() bool
	// Unregister 异步注销客户端（踢出路径断链）
	Unregister(client *models.Client)
	// SendToClient 经消息域向客户端投递（强制下线通知等控制类直达消息）
	SendToClient(ctx context.Context, client *models.Client, msg *models.HubMessage)
}

// RecordHost 连接记录子域所需的 Hub 能力面
//
// 仓储经端口动态读取而非构造期快照：SetConnectionRecordRepository 支持
// 运行期装配（NewHub 后经 adapter 注入），每次读写实时反映注入状态
type RecordHost interface {
	// GetLogger 日志器
	GetLogger() spi.Logger
	// GetConnectionRecordRepo 连接记录仓储（未注入返回 nil，调用方自行降级）
	GetConnectionRecordRepo() spi.ConnectionStore
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-01 10:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-01 10:30:00
 * @FilePath: \go-wsc\messaging\interfaces.go
 * @Description: 消息域依赖端口
 *
 * 接口定义在消费方（本包），实现由 hub 编排层提供 —— 「消费者定义接口」原则，
 * 使本包不依赖 hub 的具体类型，仅依赖其能力。
 *
 * 各访问器在对应组件未注入时返回 nil / 零值，消息域内部自行判空降级，
 * 编排层不因此 panic。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/batcher"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/spi"
)

// Host 消息域宿主端口
//
// 消息收发链路（发送/广播/分发/ACK 超时/离线转存）运行时依赖的
// 全部外部能力，由 hub 编排层实现并注入。
type Host interface {
	// ========== 基础环境 ==========

	// Context 返回 Hub 生命周期上下文
	Context() context.Context
	// GetLogger 日志器
	GetLogger() spi.Logger
	// GetNodeID 当前节点 ID
	GetNodeID() string
	// GetConfig 全局配置
	GetConfig() *wscconfig.WSC
	// IsShuttingDown Hub 是否正在关闭（读泵据此区分服务端主动断开与异常断开）
	IsShuttingDown() bool

	// ========== 注册表与连接域 ==========

	// GetShardedRegistry 分片连接注册表（未注入时返回 nil）
	GetShardedRegistry() *connection.ShardedRegistry
	// Unregister 注销客户端连接（连接生命周期归编排层）
	Unregister(client *models.Client)
	// HandleHeartbeat 处理心跳消息（连接域时间轮续期 + 统计刷新）
	HandleHeartbeat(client *models.Client)

	// ========== 仓储 ==========

	// GetMessageSink 消息记录仓储（未注入时返回 nil）
	GetMessageSink() spi.MessageSink
	// GetGroupRepo 群组仓储（未注入时返回 nil）
	GetGroupRepo() spi.GroupStore
	// GetStatsRepo 节点统计仓储（未注入时返回 nil，兼作消息计数开关）
	GetStatsRepo() spi.HubStats
	// HasPubsub 是否配置分布式发布订阅（与 gRPC 共同构成跨节点通道开关）
	HasPubsub() bool

	// ========== 集群域 ==========

	// IsGRPCEnabled 集群 gRPC 通道是否启用
	IsGRPCEnabled() bool
	// RouteToCluster 路由消息到集群其他节点
	RouteToCluster(ctx context.Context, msg *models.HubMessage, opts cluster.ClusterDispatchOptions) error
	// CheckAndRouteToNode 检查用户在线节点并按需跨节点投递
	// 返回：是否命中跨节点路由、目标节点列表、错误
	CheckAndRouteToNode(ctx context.Context, userID string, msg *models.HubMessage) (bool, []string, error)
	// GetAllClusterNodeIDs 获取集群全部节点 ID
	GetAllClusterNodeIDs() []string
	// MarkRerouteAttempted 标记消息已尝试重路由（防循环投递）
	MarkRerouteAttempted(messageID string, targetNodes []string, p2p bool)
	// DeleteRerouteGuard 清理重路由守卫条目（消息到达终态时调用）
	DeleteRerouteGuard(messageID string)
	// SubmitClusterDispatch 提交集群批量分发任务（队列满时返回 false）
	SubmitClusterDispatch(msg *models.HubMessage, opts cluster.ClusterDispatchOptions) bool

	// ========== 统计域服务 ==========

	// CheckUserOnline 检查用户是否在线（跨节点汇总判定）
	CheckUserOnline(ctx context.Context, userID string) bool
	// TrackReceiverMessageStats 投递接收方消息统计
	TrackReceiverMessageStats(connectionID string, receiverType models.UserType, dataSize int)
	// TrackConnectionError 记录连接错误（异常断开排查）
	TrackConnectionError(ctx context.Context, connectionID string, userType models.UserType, err error)
	// NotifyObservers 通知观察者（观察者未启用时为 no-op）
	NotifyObservers(ctx context.Context, msg *models.HubMessage)

	// ========== 过载保护域 ==========

	// AdmitMessage 消息级准入裁决（闸门未启用时恒放行）
	AdmitMessage(msg *models.HubMessage, isBroadcast bool) overload.AdmitVerdict
	// AdmissionOnDelivered 投递完成埋点（闸门未启用时 no-op）
	AdmissionOnDelivered()
	// OnWriteBatch 写泵批量埋点（n = 本批消息数，闸门/指标未启用时 no-op）
	OnWriteBatch(n int)
	// DeferBroadcast 广播延迟重投（延迟队列，拒绝不等于丢弃）
	DeferBroadcast(ctx context.Context, msg *models.HubMessage, retry func(context.Context, *models.HubMessage))
	// GetBroadcastShaper 广播出向整形器（未启用时返回 nil）
	GetBroadcastShaper() *overload.Shaper
	// GetEphemeralCoalescer 高频消息合并器（未启用时返回 nil）
	GetEphemeralCoalescer() *overload.Coalescer
	// GetOverloadMetrics 过载漏斗指标（编排层恒注入）
	GetOverloadMetrics() *overload.OverloadMetrics
	// GetMessageStatusUpdater 消息状态批量更新器（未注入时返回 nil）
	GetMessageStatusUpdater() *batcher.MessageStatusUpdater
	// GetAdmissionLevel 当前过载水位（闸门未启用时返回 LevelNormal）
	GetAdmissionLevel() overload.OverloadLevel

	// ========== SSE 通道 ==========

	// SendToUserViaSSE 经 SSE 通道向用户投递（SSE 未启用或用户无订阅时返回 false）
	SendToUserViaSSE(userID string, msg *models.HubMessage) bool
	// BroadcastToSSEClients 广播给全部 SSE 客户端
	BroadcastToSSEClients(msg *models.HubMessage)
}

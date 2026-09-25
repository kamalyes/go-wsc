/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 21:56:05
 * @FilePath: \go-wsc\hub\options.go
 * @Description: 链式注入选项 —— 仓储 / 基础设施 / 回调的 builder 式组装
 *
 * NewHub 返回 *Hub 后逐项 With 注入（链式返回自身），注入语义沿用旧
 * god-object Hub 的仓储初始化与回调注册：仓储与基础设施未注入即 nil，
 * 域内按 spi 契约 nil-safe 降级；回调未注入时对应环节跳过。
 * 运行期替换回调请用 accessors.go 的 Set*Callback 系列。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"

	"github.com/kamalyes/go-cachex"

	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/messaging"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// SPI 仓储注入
// ============================================================================

// WithMessageSink 注入消息发送记录仓储（MySQL 归档 + 统计；须在 Run() 前注入，
// Run 后注入不会启动记录清理等 IfTicker 且存在数据竞争）
//
// 注意：离线消息处理器与本仓储无关 —— 经 WithOfflineMessageHandler 注入，
// 或由 messaging.InitializeOfflineQueue 经 spi.StoreTarget 的
// SetOfflineMessageHandler 装配（见 interfaces.go 的 StoreTarget 区块）。
func (h *Hub) WithMessageSink(sink spi.MessageSink) *Hub {
	h.messageSink = sink
	return h
}

// WithGroupStore 注入群组仓储（Redis；须在 Run() 前注入）
func (h *Hub) WithGroupStore(store spi.GroupStore) *Hub {
	h.groupStore = store
	return h
}

// WithStatsRepo 注入 Hub 统计仓储（Redis；须在 Run() 前注入）
func (h *Hub) WithStatsRepo(repo spi.HubStats) *Hub {
	h.statsRepo = repo
	return h
}

// WithOnlineStatusRepo 注入在线状态仓储（Redis）
//
// 须在 Run() 前注入：Run 启动时按该字段是否非空决定是否拉起心跳 Redis worker
// （processHeartbeatRedisUpdates），Run 后注入不会启动该 worker 且存在数据竞争
func (h *Hub) WithOnlineStatusRepo(repo spi.OnlineStore) *Hub {
	h.onlineStatusRepo = repo
	return h
}

// WithConnectionStore 注入连接记录仓储（MySQL；须在 Run() 前注入）
func (h *Hub) WithConnectionStore(store spi.ConnectionStore) *Hub {
	h.connectionStore = store
	return h
}

// WithConnectionQualityStore 注入连接质量仓储（MySQL；须在 Run() 前注入）
func (h *Hub) WithConnectionQualityStore(store spi.ConnectionQualityStore) *Hub {
	h.connectionQualityStore = store
	return h
}

// WithWorkloadStore 注入客服负载管理仓储（Redis + MySQL；须在 Run() 前注入）
func (h *Hub) WithWorkloadStore(store spi.WorkloadStore) *Hub {
	h.workloadStore = store
	return h
}

// WithOfflineMessageHandler 注入离线消息处理器（须在 Run() 前注入）
//
// 处理器实现 spi.OfflineQueue 契约（混合实现见 messaging.HybridOfflineMessageHandler：
// Redis 队列 + RDBMS 双写），注入后挂接到 messagingMgr —— 投递失败转存、用户全端
// 离线预存等消费链路随即生效。持久化全部经注入的共享存储，Pod 本地无状态；
// 两半依赖的便捷组装见 messaging.InitializeOfflineQueue（业务侧亦可直接传构造好的 handler）
func (h *Hub) WithOfflineMessageHandler(handler spi.OfflineQueue) *Hub {
	h.messagingMgr.WithOfflineHandler(handler)
	return h
}

// ============================================================================
// 基础设施注入
// ============================================================================

// WithLogger 注入日志器（NewHub 已用 spi.InitLogger 构造默认值）
func (h *Hub) WithLogger(logger spi.Logger) *Hub {
	h.logger = logger
	return h
}

// WithPubsub 注入发布订阅（跨节点事件通道，单机模式可缺省）
func (h *Hub) WithPubsub(pubsub *cachex.PubSub) *Hub {
	h.pubsub = pubsub
	return h
}

// WithNodeRegistry 注入节点注册表（集群成员发现与心跳）
func (h *Hub) WithNodeRegistry(registry *cluster.NodeRegistry) *Hub {
	h.nodeRegistry = registry
	return h
}

// WithGRPCClientPool 注入节点间 gRPC 客户端连接池
func (h *Hub) WithGRPCClientPool(pool *cluster.GRPCClientPool) *Hub {
	h.grpcClientPool = pool
	return h
}

// WithConnectionTokenDecoder 注入连接 Token 鉴权器（nil 时握手走明文参数）
func (h *Hub) WithConnectionTokenDecoder(decoder spi.ConnectionAuthenticator) *Hub {
	h.connectionTokenDecoder = decoder
	return h
}

// WithWelcomeProvider 注入欢迎消息提供者
func (h *Hub) WithWelcomeProvider(provider models.WelcomeMessageProvider) *Hub {
	h.welcomeProvider = provider
	return h
}

// ============================================================================
// 应用层回调注入（构造期；运行期替换见 accessors.go 的 Set*Callback）
//
// hub 侧回调写入 h.callbacks（见 callbacks.go）；消息域回调（发送完成 /
// 上行消息 / 错误处理）委托 messagingMgr，消费者在消息域内
// ============================================================================

// WithOfflineMessagePushCallback 注入离线消息推送完成回调（上游据此删除已推送消息）
func (h *Hub) WithOfflineMessagePushCallback(cb OfflineMessagePushCallback) *Hub {
	h.callbacks.OfflineMessagePush = cb
	return h
}

// WithMessageSendCallback 注入消息发送完成回调（含重试信息与最终错误，委托消息域）
func (h *Hub) WithMessageSendCallback(cb messaging.MessageSendCallback) *Hub {
	h.messagingMgr.WithMessageSendCallback(cb)
	return h
}

// WithHeartbeatTimeoutCallback 注入心跳超时回调
func (h *Hub) WithHeartbeatTimeoutCallback(cb HeartbeatTimeoutCallback) *Hub {
	h.callbacks.HeartbeatTimeout = cb
	return h
}

// WithHeartbeatReportCallback 注入心跳上报回调
func (h *Hub) WithHeartbeatReportCallback(cb HeartbeatReportCallback) *Hub {
	h.callbacks.HeartbeatReport = cb
	return h
}

// WithBeforeHeartbeatCallback 注入心跳处理前回调（返回 false 跳过后续心跳处理）
func (h *Hub) WithBeforeHeartbeatCallback(cb BeforeHeartbeatCallback) *Hub {
	h.callbacks.BeforeHeartbeat = cb
	return h
}

// WithAfterHeartbeatCallback 注入心跳处理后回调
func (h *Hub) WithAfterHeartbeatCallback(cb AfterHeartbeatCallback) *Hub {
	h.callbacks.AfterHeartbeat = cb
	return h
}

// WithClientConnectCallback 注入客户端连接回调（权限验证 / 会话初始化等）
func (h *Hub) WithClientConnectCallback(cb ClientConnectCallback) *Hub {
	h.callbacks.ClientConnect = cb
	return h
}

// WithClientDisconnectCallback 注入客户端断开回调（资源清理 / 在线状态更新等）
func (h *Hub) WithClientDisconnectCallback(cb ClientDisconnectCallback) *Hub {
	h.callbacks.ClientDisconnect = cb
	return h
}

// WithMessageReceivedCallback 注入客户端上行消息回调（业务逻辑处理 / 消息路由，委托消息域）
func (h *Hub) WithMessageReceivedCallback(cb messaging.MessageReceivedCallback) *Hub {
	h.messagingMgr.WithMessageReceivedCallback(cb)
	return h
}

// WithErrorCallback 注入统一错误处理回调（日志记录 / 告警通知等，委托消息域）
func (h *Hub) WithErrorCallback(cb messaging.ErrorCallback) *Hub {
	h.messagingMgr.WithErrorCallback(cb)
	return h
}

// WithGroupDisbandCallback 注入群组解散回调（DisbandGroup 成功后异步触发）
func (h *Hub) WithGroupDisbandCallback(cb func(ctx context.Context, namespace, groupID string)) *Hub {
	h.callbacks.GroupDisband = cb
	return h
}

// WithGroupMemberJoinCallback 注入群组成员加入回调（连接时自动加群成功后异步触发）
func (h *Hub) WithGroupMemberJoinCallback(cb func(ctx context.Context, namespace, groupID string, userIDs []string)) *Hub {
	h.callbacks.GroupMemberJoin = cb
	return h
}

// WithGroupMemberLeaveCallback 注入群组成员离开回调（RemoveGroupMembers 成功后异步触发）
func (h *Hub) WithGroupMemberLeaveCallback(cb func(ctx context.Context, namespace, groupID string, userIDs []string)) *Hub {
	h.callbacks.GroupMemberLeave = cb
	return h
}

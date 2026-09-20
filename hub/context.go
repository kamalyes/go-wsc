/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-09 00:16:29
 * @FilePath: \go-wsc\hub\context.go
 * @Description: hub 层上下文扩展
 *
 * 路由元数据（namespace/groupIDs + gRPC metadata 传播）已抽离到独立的 routing 包，
 * 全项目共用，无循环依赖。hub 层通过 routing.NewRoute().WithAppID(...).Inject(ctx) /
 * routing.NamespaceFromContext / routing.InjectToOutgoingMetadata 等直接调用。
 *
 * 本文件仅保留 hub 专用的 context key（UserID/SenderID）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

// ContextKey 上下文键类型（hub 层专用键，如 UserID/SenderID）
type ContextKey string

const (
	ContextKeyUserID   ContextKey = "user_id"   // 用户ID
	ContextKeySenderID ContextKey = "sender_id" // 发送者ID
	// ContextKeyOfflineBroadcastCollector 批量扇出路径的离线广播聚合器
	// 群组成员可达数万，扇出 goroutine 内逐人同步广播会造成 N 倍集群流量放大与
	// Redis 连接池耗尽（OOM 教训）；但完全跳过又会丢失"索引滞后但实际在线"用户的实时投递。
	// 折中：扇出期间仅聚合收集未命中用户，扇出结束后统一逐人跨节点推送（真的推送），
	// 真正离线的用户由离线存储 + 重连上线拉取兜底
	ContextKeyOfflineBroadcastCollector ContextKey = "offline_broadcast_collector"
)

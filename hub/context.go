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
)

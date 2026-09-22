/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 18:36:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 18:36:00
 * @FilePath: \go-wsc\models\distributed.go
 * @Description: 跨节点分布式消息信封
 *
 * 节点间传输的消息载体（gRPC 直连与 PubSub 兜底共用），
 * 路由维度（appID/namespace/groupIDs）随信封携带。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"context"
	"time"

	"github.com/kamalyes/go-logger"
)

// DistributedMessage 分布式消息结构
type DistributedMessage struct {
	Type          OperationType `json:"type"`                     // 操作类型
	NodeID        string        `json:"node_id"`                  // 源节点ID
	TargetID      string        `json:"target_id"`                // 目标ID（用户ID、节点ID等）
	TraceID       string        `json:"trace_id,omitempty"`       // 全链路追踪ID（从 ctx 自动注入，跨节点序列化携带）
	Message       *HubMessage   `json:"message"`                  // 消息数据（用于 send_message, broadcast, observer_notify）
	Reason        string        `json:"reason"`                   // 原因
	Timestamp     time.Time     `json:"timestamp"`                // 时间戳
	AppID         string        `json:"app_id,omitempty"`         // 应用ID（最上层隔离维度，路由信封携带，空=全局共享）
	Namespace     string        `json:"namespace,omitempty"`      // 命名空间ID（路由信封携带，空=全命名空间广播，非空=指定命名空间）
	GroupIDs      []string      `json:"group_ids,omitempty"`      // 群组ID列表（支持多群组，观察者可订阅多个组；空表示无群组操作）
	ExcludeSender bool          `json:"exclude_sender,omitempty"` // 是否排除发送者（跨节点群组广播 PubSub 兜底携带，与 gRPC BroadcastGroupRequest 对齐）
	SenderID      string        `json:"sender_id,omitempty"`      // 发送者ID（排除发送者时用，跨节点 PubSub 兜底场景）
}

// InjectContext 从 ctx 注入上下文信息到分布式消息（trace_id 等）
// 优先从 OTel span 提取 trace_id，fallback 到 ctx.Value(logger.ContextKeyTraceID)
// 已有 trace_id 时不覆盖（跨节点消息保留源 trace）
func (dm *DistributedMessage) InjectContext(ctx context.Context) *DistributedMessage {
	if dm.TraceID != "" {
		return dm // 已有则不覆盖
	}
	dm.TraceID = logger.ExtractTraceID(ctx)
	return dm
}

// ContextFrom 基于分布式消息的 trace_id 创建一个携带 trace 信息的 context
// 用于消息流转路径中恢复 ctx（如 PubSub 消费端、回调等场景）
func (dm *DistributedMessage) ContextFrom(parent context.Context) context.Context {
	if dm.TraceID == "" {
		return parent
	}
	return logger.ContextWithTraceID(parent, dm.TraceID)
}

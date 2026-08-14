/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-31 00:15:57
 * @FilePath: \go-wsc\routing\routing.go
 * @Description: 路由元数据 context 传递（独立模块，全项目共用）
 *
 * 路由元数据（namespace + groupIDs）是路由信封，不属于消息内容：
 *   - 本地调用链：通过 context 注入/提取（WithNamespaceGroupIDs / NamespaceFromContext）
 *   - 跨节点 gRPC：通过 metadata headers 传播（Inject/Restore）
 *   - 跨节点 PubSub：通过 DistributedMessage.GroupIDs 信封字段携带
 *
 * 作为独立底层包，hub/handler/models 等均可直接 import，无循环依赖。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package routing

import (
	"context"
	"strings"

	"google.golang.org/grpc/metadata"
)

// routingCtxKey 路由元数据的 context key 类型（不可导出，避免外部伪造）
type routingCtxKey struct{}

// gRPC metadata 键名（跨节点传播路由元数据）
const (
	MetadataKeyNamespace = "x-routing-namespace" // 命名空间
	MetadataKeyGroupIDs  = "x-routing-group-ids" // 群组ID列表（逗号分隔）
)

// RoutingContext 路由元数据（namespace + groupIDs）
// 打包存入 context，一次类型断言获取全部，避免多次 ctx.Value + interface 断言
type RoutingContext struct {
	Namespace string   // 命名空间ID（空表示全局）
	GroupIDs  []string // 群组ID列表（len==1 单群组，len>1 批量，len==0 无群组）
}

// WithRoutingContext 将路由元数据注入 context
func WithRoutingContext(ctx context.Context, rc *RoutingContext) context.Context {
	if rc == nil {
		return ctx
	}
	return context.WithValue(ctx, routingCtxKey{}, rc)
}

// WithNamespaceGroupIDs 便捷方法：将 namespace + groupIDs 打包注入 context
func WithNamespaceGroupIDs(ctx context.Context, namespace string, groupIDs []string) context.Context {
	return context.WithValue(ctx, routingCtxKey{}, &RoutingContext{
		Namespace: namespace,
		GroupIDs:  groupIDs,
	})
}

// RoutingFromContext 从 context 提取路由元数据（一次断言，零分配热路径）
func RoutingFromContext(ctx context.Context) *RoutingContext {
	v, _ := ctx.Value(routingCtxKey{}).(*RoutingContext)
	return v
}

// NamespaceFromContext 从 context 提取命名空间
//
// 设计原则：存储/查询层不做默认值归一化，namespace 没有就是空串。
// 归一化（如 client.Namespace → DefaultNamespace）统一由入口层（hub 注册、group 创建）完成，
// 避免存储层到处兜底导致行为不一致与维护负担。
func NamespaceFromContext(ctx context.Context) string {
	if rc := RoutingFromContext(ctx); rc != nil {
		return rc.Namespace
	}
	return ""
}

// GroupIDsFromContext 从 context 提取群组ID列表
func GroupIDsFromContext(ctx context.Context) []string {
	if rc := RoutingFromContext(ctx); rc != nil {
		return rc.GroupIDs
	}
	return nil
}

// FirstGroupIDFromContext 从 context 提取首个群组ID（无群组时返回空字符串）
// 离线消息按单条消息维度存储，群组消息取首个 groupID，点对点消息为空
func FirstGroupIDFromContext(ctx context.Context) string {
	if gids := GroupIDsFromContext(ctx); len(gids) > 0 {
		return gids[0]
	}
	return ""
}

// ============================================================================
// gRPC metadata 跨节点传播
// ============================================================================

// InjectToOutgoingMetadata 将路由元数据注入 gRPC outgoing metadata
// groupIDs 以逗号分隔存入单个 metadata 值，避免 repeated header 开销
func InjectToOutgoingMetadata(ctx context.Context) context.Context {
	rc := RoutingFromContext(ctx)
	if rc == nil {
		return ctx
	}

	pairs := make([]string, 0, 4)
	if rc.Namespace != "" {
		pairs = append(pairs, MetadataKeyNamespace, rc.Namespace)
	}
	if len(rc.GroupIDs) > 0 {
		pairs = append(pairs, MetadataKeyGroupIDs, strings.Join(rc.GroupIDs, ","))
	}

	if len(pairs) == 0 {
		return ctx
	}

	md, ok := metadata.FromOutgoingContext(ctx)
	if ok {
		md = md.Copy()
	} else {
		md = metadata.New(nil)
	}
	for i := 0; i < len(pairs); i += 2 {
		md.Set(pairs[i], pairs[i+1])
	}
	return metadata.NewOutgoingContext(ctx, md)
}

// RestoreFromIncomingMetadata 从 gRPC incoming metadata 恢复路由元数据到 ctx
func RestoreFromIncomingMetadata(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}

	rc := &RoutingContext{}

	if vals := md.Get(MetadataKeyNamespace); len(vals) > 0 {
		rc.Namespace = vals[0]
	}
	if vals := md.Get(MetadataKeyGroupIDs); len(vals) > 0 && vals[0] != "" {
		rc.GroupIDs = strings.Split(vals[0], ",")
	}

	return WithRoutingContext(ctx, rc)
}

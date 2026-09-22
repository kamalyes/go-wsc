/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 16:06:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 16:06:00
 * @FilePath: \go-wsc\routing\propagation.go
 * @Description: 路由元数据传播 —— ctx 提取与 gRPC metadata 跨节点传播
 *
* 提取侧设计原则：存储/查询层不做默认值归一化，没有就是空串。
 * 归一化统一由入口层（route.go 的 Route.Inject / EnsureRouteDefaults）完成，
 * 避免下游到处兜底导致行为不一致与维护负担。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
*/
package routing

import (
	"context"
	"strings"

	"github.com/kamalyes/go-wsc/constants"
	"google.golang.org/grpc/metadata"
)

// ============================================================================
// 提取侧（*FromContext 系列）
// ============================================================================

// RoutingFromContext 从 context 提取路由元数据（一次断言，零分配热路径）
func RoutingFromContext(ctx context.Context) *RoutingContext {
	v, _ := ctx.Value(routingCtxKey{}).(*RoutingContext)
	return v
}

// AppIDFromContext 从 context 提取应用ID（零分配热路径，与 NamespaceFromContext 共享 RoutingFromContext 断言）
//
// appID 为必填维度（无广播语义），此处统一归一化：ctx 无路由或 appID 为空时返回 DefaultAppID
// 调用方直接使用返回值即可，无需再 NormalizeAppID/NormalizeRoute 二次包装，消除散落归一化
func AppIDFromContext(ctx context.Context) string {
	var raw string
	if rc := RoutingFromContext(ctx); rc != nil {
		raw = rc.AppID
	}
	return constants.NormalizeAppID(raw)
}

// NamespaceFromContext 从 context 提取命名空间
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

	pairs := make([]string, 0, 6)
	if rc.AppID != "" {
		pairs = append(pairs, constants.MetadataKeyAppID, rc.AppID)
	}
	if rc.Namespace != "" {
		pairs = append(pairs, constants.MetadataKeyNamespace, rc.Namespace)
	}
	if len(rc.GroupIDs) > 0 {
		pairs = append(pairs, constants.MetadataKeyGroupIDs, strings.Join(rc.GroupIDs, ","))
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
// 归一化 appID（空值补 DefaultAppID），与入口层策略一致
func RestoreFromIncomingMetadata(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}

	r := NewRoute()
	if vals := md.Get(constants.MetadataKeyAppID); len(vals) > 0 {
		appID, _ := NormalizeRoute(vals[0], "")
		r.WithAppID(appID)
	}
	if vals := md.Get(constants.MetadataKeyNamespace); len(vals) > 0 {
		r.WithNamespace(vals[0])
	}
	if vals := md.Get(constants.MetadataKeyGroupIDs); len(vals) > 0 && vals[0] != "" {
		r.WithGroupIDs(strings.Split(vals[0], ","))
	}

	return r.Inject(ctx)
}

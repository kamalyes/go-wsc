/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-31 00:15:57
 * @FilePath: \go-wsc\routing\routing.go
 * @Description: 路由元数据 context 传递（独立模块，全项目共用）
 *
 * 路由元数据（appID + namespace + groupIDs）是路由信封，不属于消息内容：
 *   - 本地调用链：通过 Route 链式构建器注入（NewRoute().WithAppID(...).Inject(ctx)），
 *     通过 *FromContext 系列函数提取（AppIDFromContext 等）
 *   - 跨节点 gRPC：通过 metadata headers 传播（InjectToOutgoing/RestoreFromIncoming）
 *   - 跨节点 PubSub：通过 DistributedMessage.GroupIDs 信封字段携带
 *
 * 隔离层次：AppID(应用) > Namespace(租户) > GroupID(平台) > UserID
 * appID 是最上层隔离维度，但语义上从属于 namespace+group 路由体系：
 *   - 空值在入口层归一化为 DefaultAppID（与 namespace 归一化策略一致）
 *   - 业务代码应通过 routing.NewRoute().WithAppID(...).WithNamespace(...).WithGroupIDs(...).Inject(ctx)
 *     一次性注入全维度，每个维度显式命名，避免位置参数记错或漏传 appID
 *
 * 设计原则：appid/namespace/group 贯穿整个生命周期都从 ctx 取
 *   - 注入侧：Route 链式构建器（显式命名每个维度，漏传一目了然）
 *   - 提取侧：*FromContext 系列函数（零分配热路径，不做默认值兜底）
 *   - 归一化：仅在入口层（Route.Inject / EnsureRouteDefaults）完成，下游不再兜底
 *
 * 作为独立底层包，hub/handler/models 等均可直接 import，无循环依赖
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

// routingCtxKey 路由元数据的 context key 类型（不可导出，避免外部伪造）
type routingCtxKey struct{}

// RoutingContext 路由元数据（appID + namespace + groupIDs）
// 打包存入 context，一次类型断言获取全部，避免多次 ctx.Value + interface 断言
type RoutingContext struct {
	AppID     string   // 应用ID（最上层隔离维度，Inject 时归一化为 constants.DefaultAppID）
	Namespace string   // 命名空间ID（严格场景经 NormalizeRoute 归一化为 DefaultNamespace；广播场景保留空值=全命名空间）
	GroupIDs  []string // 群组ID列表（len==1 单群组，len>1 批量，len==0 无群组）
}

// NormalizeRoute 统一归一化路由隔离维度：appID 空补 constants.DefaultAppID，namespace 空补 constants.DefaultNamespace
//
// 设计原则：
//   - appID 与 namespace 同为入口层必填维度，归一化策略一致（空=默认值，而非空=全局共享）
//   - groupIDs 不在此归一化（P2P 消息 groupIDs=nil 是合法语义，存储层按需补 DefaultGroupID）
//   - 严格场景（P2P 发送、群组发送、客户端注册、跨节点恢复）调用此方法一次性归一化两个维度
//   - 广播场景不应调用此方法（namespace 故意留空表示全命名空间广播，需保留空值）
//
// 返回归一化后的 (appID, namespace)，调用方按需取用：
//
//	appID, ns := routing.NormalizeRoute(appID, ns)       // 两个都要（严格场景）
//	appID, _ := routing.NormalizeRoute(appID, "")        // 只需 appID（赋值给字段，namespace 按 Broadcast 语义保留）
func NormalizeRoute(appID, namespace string) (string, string) {
	if appID == "" {
		appID = constants.DefaultAppID
	}
	if namespace == "" {
		namespace = constants.DefaultNamespace
	}
	return appID, namespace
}

// ============================================================================
// Route 链式构建器（注入侧）
//
// 替代旧的位置参数式 WithNamespaceGroupIDs(ctx, appID, ns, gids)：
//   - 每个维度通过 With* 方法显式命名，漏传 appID 一目了然（不再是位置参数记错）
//   - 零值维度由 Inject 兜底归一化，语义不变
//   - RouteFrom(ctx) 支持从现有 ctx 继承路由后局部修改，简化"提取-改-重注入"场景
// ============================================================================

// Route 路由构建器，链式设置 appID/namespace/groupIDs 后通过 Inject 注入 context
type Route struct {
	appID     string
	namespace string
	groupIDs  []string
}

// NewRoute 创建空的路由构建器
func NewRoute() *Route { return &Route{} }

// RouteFrom 从现有 context 继承路由维度，便于局部修改后重新注入
//
// 适用场景：下游需要改 namespace 但保留 appID/groupIDs 时：
//
//	ctx = routing.RouteFrom(ctx).WithNamespace("new-ns").Inject(ctx)
//
// 若 ctx 无路由元数据，返回空 Route（后续 With* 再补维度）
func RouteFrom(ctx context.Context) *Route {
	if rc := RoutingFromContext(ctx); rc != nil {
		gids := rc.GroupIDs
		if gids != nil {
			gids = append([]string(nil), gids...) // 防御性拷贝，避免与 ctx 内切片共享底层数组
		}
		return &Route{appID: rc.AppID, namespace: rc.Namespace, groupIDs: gids}
	}
	return &Route{}
}

// WithAppID 设置应用ID（最上层隔离维度）
func (r *Route) WithAppID(appID string) *Route {
	r.appID = appID
	return r
}

// WithNamespace 设置命名空间ID（租户隔离维度；广播场景可省略，表示全命名空间）
func (r *Route) WithNamespace(namespace string) *Route {
	r.namespace = namespace
	return r
}

// WithGroupIDs 设置群组ID列表（覆盖已有值；P2P 消息传 nil 是合法语义）
func (r *Route) WithGroupIDs(groupIDs []string) *Route {
	r.groupIDs = groupIDs
	return r
}

// WithGroup 追加单个群组ID（累加，不覆盖已有值；用于逐个添加群组场景）
func (r *Route) WithGroup(groupID string) *Route {
	r.groupIDs = append(r.groupIDs, groupID)
	return r
}

// Inject 将构建好的路由元数据注入 context
//
// 归一化策略：
//   - appID 空补 DefaultAppID（appID 无广播语义，必填，与旧 WithNamespaceGroupIDs 一致）
//   - namespace 保持原值（广播场景 namespace="" 表示全命名空间，不归一化；严格场景调用方用 EnsureRouteDefaults 补默认）
//   - groupIDs 保持原值（P2P 消息 nil 合法，存储层按需补 DefaultGroupID）
func (r *Route) Inject(ctx context.Context) context.Context {
	appID, _ := NormalizeRoute(r.appID, r.namespace) // appID 归一化，namespace 保持原值（兼容广播）
	return context.WithValue(ctx, routingCtxKey{}, &RoutingContext{
		AppID:     appID,
		Namespace: r.namespace,
		GroupIDs:  r.groupIDs,
	})
}

// EnsureRouteDefaults 严格场景的路由兜底归一化：appID/namespace 空值补默认值
//
// 适用场景：P2P 发送、群组发送等需要完整路由维度的入口（namespace 不允许空）
// 不适用场景：全局广播（namespace 故意留空表示全命名空间，调用方不应调用此函数）
//
// 幂等：已归一化的 ctx 再次调用无副作用（NormalizeRoute 对非空值不覆盖，groupIDs 保留原值）
// 调用方一行搞定兜底，替代各处散落的 `if appID == "" || namespace == ""` 模板：
//
//	ctx = routing.EnsureRouteDefaults(ctx)
func EnsureRouteDefaults(ctx context.Context) context.Context {
	r := RouteFrom(ctx)
	appID, ns := NormalizeRoute(r.appID, r.namespace)
	r.appID = appID
	r.namespace = ns
	return context.WithValue(ctx, routingCtxKey{}, &RoutingContext{
		AppID:     r.appID,
		Namespace: r.namespace,
		GroupIDs:  r.groupIDs,
	})
}

// ============================================================================
// 提取侧（*FromContext 系列）
//
// 设计原则：存储/查询层不做默认值归一化，没有就是空串。
// 归一化统一由入口层（Route.Inject / EnsureRouteDefaults）完成，
// 避免下游到处兜底导致行为不一致与维护负担。
// ============================================================================

// RoutingFromContext 从 context 提取路由元数据（一次断言，零分配热路径）
func RoutingFromContext(ctx context.Context) *RoutingContext {
	v, _ := ctx.Value(routingCtxKey{}).(*RoutingContext)
	return v
}

// AppIDFromContext 从 context 提取应用ID（零分配热路径，与 NamespaceFromContext 共享 RoutingFromContext 断言）
//
// 注意：返回值不归一化——入口层（Route.Inject）已统一归一化，此处直接返回存储值
// 若调用方需要确保非空（防御性场景），应使用 NormalizeRoute 包装；正常路径无需再归一化
func AppIDFromContext(ctx context.Context) string {
	if rc := RoutingFromContext(ctx); rc != nil {
		return rc.AppID
	}
	return ""
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

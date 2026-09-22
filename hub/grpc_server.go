/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-18 00:00:00
 * @FilePath: \go-wsc\hub\grpc_server.go
 * @Description: Hub gRPC 服务端与生命周期 - 实现节点间 NodeService 接口及启停管理
 *
 * 每个 WebSocket Hub 节点运行 gRPC 服务端，接收来自其他节点的点对点请求，
 * 包括消息投递、在线检查、群组广播、观察者通知、踢人与健康检查
 *
 * 生命周期（原 grpc_lifecycle.go 并入）：
 *   启用 node-grpc 配置后：
 *   - InitNodeGRPC 创建 NodeRegistry/GRPCServer/GRPCClientPool 三件套
 *   - startNodeGRPC 在 Run 中启动服务端并注册到 Redis，其他节点可通过发现机制直连
 *   - stopNodeGRPC 在 SafeShutdown 中优雅停止
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"net"
	"runtime/debug"
	"sync"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/netx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ============================================================================
// GRPCServer - 节点间 gRPC 服务端
// ============================================================================

// GRPCServer gRPC 服务端，实现 wscpb.NodeServiceServer 接口
// 将远端节点的 gRPC 请求转交给本地 Hub 处理，实现跨节点精确路由
type GRPCServer struct {
	wscpb.UnimplementedNodeServiceServer
	hub      *Hub
	server   *grpc.Server
	listener net.Listener
}

// grpcServers Hub → gRPC 服务端引用（*Hub → *GRPCServer）
// hub.go（slim 编排层）不持有 grpcServer 字段，服务端引用由本包级映射保存；
// 键为 *Hub 实例，多 Hub 测试实例互不干扰，stopNodeGRPC 时 LoadAndDelete 释放
var grpcServers sync.Map

// NewGRPCServer 创建新的 gRPC 服务端
func NewGRPCServer(hub *Hub) *GRPCServer {
	return &GRPCServer{
		hub: hub,
	}
}

// Start 启动 gRPC 服务端，监听指定地址并异步提供服务
// addr 支持 IPv4（host:port）和 IPv6（[host]:port 或裸 IPv6 地址）
func (s *GRPCServer) Start(ctx context.Context, addr string) error {
	// 规范化监听地址：裸 IPv6 地址（含冒号但无方括号）自动加方括号
	addr = netx.NormalizeListenAddr(addr)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听地址 %s 失败: %w", addr, err)
	}
	s.listener = listener

	s.server = grpc.NewServer()
	wscpb.RegisterNodeServiceServer(s.server, s)

	go func() {
		if err := s.server.Serve(listener); err != nil {
			s.hub.logger.ErrorContextKV(ctx, "gRPC 服务端运行异常", "error", err, "addr", addr)
		}
	}()

	s.hub.logger.InfoContextKV(ctx, "gRPC 服务端已启动", "addr", addr, "node_id", s.hub.GetNodeID())
	return nil
}

// Stop 优雅停止 gRPC 服务端
func (s *GRPCServer) Stop() {
	if s.server != nil {
		s.server.GracefulStop()
	}
	if s.listener != nil {
		s.listener.Close()
	}
}

// ============================================================================
// NodeServiceServer 接口实现
// ============================================================================

// SendToUser 向本节点的指定用户发送消息（点对点投递）
// 使用 ForEachUserClientFiltered 零拷贝遍历 + 预序列化，替代 GetClientsByUserID 切片拷贝 + 逐客户端序列化
func (s *GRPCServer) SendToUser(ctx context.Context, req *wscpb.SendToUserRequest) (*wscpb.SendToUserResponse, error) {
	// 从 gRPC incoming metadata 恢复 trace_id 到 ctx（跨节点链路串联）
	ctx = logger.RestoreTraceFromIncoming(ctx)
	// 从 gRPC incoming metadata 恢复路由元数据（namespace/groupIDs）
	// 与 DistributedMessage 外层信封 / HubMessage 自身信封 三处路由来源互为兜底
	ctx = routing.RestoreFromIncomingMetadata(ctx)

	// 反序列化消息
	msg, err := wscpb.UnmarshalHubMessage(req.GetMessageData())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "反序列化消息失败: %v", err)
	}

	// 消息体也携带 trace_id，补充恢复（metadata 优先，消息体 fallback）
	ctx = msg.ContextFrom(ctx)
	// 🔏 路由信封兜底同步：
	//   新节点：HubMessage protobuf 自带 namespace/group_ids → InjectRoute 幂等（不覆盖已有有效值）
	ctx = msg.InjectRoute(ctx)

	userID := req.GetUserId()

	// 快速检查用户是否在线（O(1)，避免无用户时序列化开销）
	// 按 ctx 路由信封 appID+namespace 隔离（路由来自 msg.InjectRoute 注入）
	if !s.hub.shardedRegistry.HasUserClient(ctx, userID) {
		// 🔥 gRPC 定向投递扑空 = 在线索引死条目（用户断连未清理/已迁移到其他节点）：
		// 异步自愈清理指向本节点的死索引（与 PubSub 定向路径行为一致，见 distributed.go）
		// 发送方收到 Success=false 响应即触发重路由决策（routeToCluster userMiss 分支），
		// 无需像 PubSub 路径那样经回告频道绕圈
		appID, _ := routing.NormalizeRoute(msg.AppID, "")
		s.hub.selfHealDeadIndexEntries(ctx, userID, appID, msg.Namespace)
		return &wscpb.SendToUserResponse{
			Success:    false,
			Error:      "用户不在线",
			UserOnline: false,
		}, nil
	}

	// 预序列化一次消息（所有客户端复用，消除逐客户端 json.Marshal 开销）
	preSerialized, err := json.Marshal(msg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "消息序列化失败: %v", err)
	}

	// 零拷贝遍历：仅对路由匹配的设备投递，避免跨 app/namespace 串扰
	// （路由信封来自 msg 自身，跨节点 gRPC 调用链已丢失原 ctx 路由信息）
	s.hub.shardedRegistry.ForEachUserClientFiltered(userID, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *models.Client) bool {
		s.hub.messagingMgr.SendToClientSerialized(ctx, client, msg, preSerialized)
		return true
	})

	// 🔔 通知本节点观察者（跨节点 gRPC 消息也需要通知观察者，与本地 broadcast 流程一致）
	// 路由来源：直接从 msg 信封取（不再"猜"接收者 client 的 ns/group，跨节点场景下 user 可能本节点无 client）
	observerCtx := routing.NewRoute().WithAppID(msg.AppID).WithNamespace(msg.Namespace).WithGroupIDs(msg.GroupIDs).Inject(ctx)
	s.hub.NotifyObservers(observerCtx, msg)

	return &wscpb.SendToUserResponse{
		Success:    true,
		UserOnline: true,
	}, nil
}

// CheckUsersOnline 批量检查用户是否在本节点在线（路由探测）
// 使用 HasUserClient O(1) 检查，替代 GetClientsByUserID 切片分配
// 按 ctx 路由信封 appID+namespace 隔离（路由从 gRPC metadata 恢复）
func (s *GRPCServer) CheckUsersOnline(ctx context.Context, req *wscpb.CheckUsersOnlineRequest) (*wscpb.CheckUsersOnlineResponse, error) {
	// 从 gRPC incoming metadata 恢复路由元数据到 ctx（跨节点链路串联）
	ctx = routing.RestoreFromIncomingMetadata(ctx)
	onlineUsers := make(map[string]bool, len(req.GetUserIds()))
	for _, userID := range req.GetUserIds() {
		onlineUsers[userID] = s.hub.shardedRegistry.HasUserClient(ctx, userID)
	}

	return &wscpb.CheckUsersOnlineResponse{
		OnlineUsers: onlineUsers,
	}, nil
}

// BroadcastGroup 向本节点的群组成员广播消息
func (s *GRPCServer) BroadcastGroup(ctx context.Context, req *wscpb.BroadcastGroupRequest) (*wscpb.BroadcastGroupResponse, error) {
	// 从 gRPC incoming metadata 恢复 trace_id + 路由元数据 到 ctx（跨节点链路串联）
	ctx = logger.RestoreTraceFromIncoming(ctx)
	ctx = routing.RestoreFromIncomingMetadata(ctx)

	// 群组仓储未配置，无法获取成员
	if s.hub.groupStore == nil {
		return &wscpb.BroadcastGroupResponse{Delivered: 0}, nil
	}

	appID, namespace := routing.NormalizeRoute(routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx))
	groupIDs := routing.GroupIDsFromContext(ctx)
	// BroadcastGroup RPC 语义为单群组广播（cluster_dispatch 每次传单群组），取首元素
	groupID := ""
	if len(groupIDs) > 0 {
		groupID = groupIDs[0]
	}

	// 获取群组成员列表（appID 隔离，跨 app 不串扰）
	members, err := s.hub.groupStore.GetMembers(ctx, appID, namespace, groupID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "获取群组成员失败: %v", err)
	}

	if len(members) == 0 {
		return &wscpb.BroadcastGroupResponse{Delivered: 0}, nil
	}

	// 反序列化消息
	msg, err := wscpb.UnmarshalHubMessage(req.GetMessageData())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "反序列化消息失败: %v", err)
	}

	// 消息体也携带 trace_id，补充恢复（metadata 优先，消息体 fallback）
	ctx = msg.ContextFrom(ctx)

	// ⚠️ 此处【不】调用 msg.InjectRoute(ctx)：
	// BroadcastGroup 的 ctx 携带的是"业务群组ID"（如 g-srv），而 broadcastToFiltered →
	// ClientMatchesEnvelope 会用 msg.GroupIDs 去匹配 client 的"连接级系统组"（如 __default_gp__），
	// 两个维度不同，强行注入会导致群成员设备全部被过滤（delivered=0）。
	// 群组成员过滤已由 groupStore.GetMembers + memberSet 完成，下面清除 ctx 的 groupIDs 后，
	// 下游 BroadcastToFiltered 调 InjectRoute 时只会注入 appId+namespace（msg.GroupIDs 保持 nil），
	// ClientMatchesEnvelope 仅做 appId+namespace 隔离，不再触碰系统组维度。
	ctx = routing.RouteFrom(ctx).WithNamespace(namespace).WithGroupIDs(nil).Inject(ctx)

	// 构建成员集合用于 O(1) 过滤
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m] = struct{}{}
	}

	// 过滤广播：只投递给群组成员，按需排除发送者
	excludeSender := req.GetExcludeSender()
	senderID := req.GetSenderId()
	delivered := s.hub.messagingMgr.BroadcastToFiltered(ctx, func(client *models.Client) bool {
		// 只投递给群组成员
		if _, ok := memberSet[client.UserID]; !ok {
			return false
		}
		// 排除发送者（用于多端同步场景）
		if excludeSender && client.UserID == senderID {
			return false
		}
		return true
	}, msg)

	return &wscpb.BroadcastGroupResponse{
		Delivered: int32(delivered),
	}, nil
}

// NotifyObservers 通知本节点的观察者
// namespace/groupID 从 gRPC incoming metadata 恢复到 ctx 后提取
func (s *GRPCServer) NotifyObservers(ctx context.Context, req *wscpb.NotifyObserversRequest) (*wscpb.NotifyObserversResponse, error) {
	// 从 gRPC incoming metadata 恢复 trace_id + 路由元数据（跨节点链路串联）
	ctx = logger.RestoreTraceFromIncoming(ctx)
	ctx = routing.RestoreFromIncomingMetadata(ctx)

	// 反序列化消息
	msg, err := wscpb.UnmarshalHubMessage(req.GetMessageData())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "反序列化消息失败: %v", err)
	}

	// 消息体也携带 trace_id，补充恢复（metadata 优先，消息体 fallback）
	ctx = msg.ContextFrom(ctx)
	// 🔏 路由信封兜底同步：与 SendToUser 一致，确保 msg 信封恒有路由值
	// InjectRoute 同时回写 ctx，保证下游 ctx 与信封一致
	ctx = msg.InjectRoute(ctx)

	// 三级索引查找：全局 + 命名空间 + 命名空间+群组
	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	observers := s.hub.shardedRegistry.GetObserversForMessage(namespace, groupIDs...)

	// 逐个投递
	var notified int32
	for _, client := range observers {
		s.hub.messagingMgr.SendToClient(ctx, client, msg)
		notified++
	}

	return &wscpb.NotifyObserversResponse{
		Notified: notified,
	}, nil
}

// KickUser 踢出本节点上的用户
func (s *GRPCServer) KickUser(ctx context.Context, req *wscpb.KickUserRequest) (*wscpb.KickUserResponse, error) {
	// 从 gRPC incoming metadata 恢复 trace_id 到 ctx（跨节点链路串联）
	ctx = logger.RestoreTraceFromIncoming(ctx)

	kicked := s.hub.KickUserSimple(ctx, req.GetUserId(), req.GetReason())
	return &wscpb.KickUserResponse{
		Success:           true,
		KickedConnections: int32(kicked),
	}, nil
}

// Ping 节点健康检查
func (s *GRPCServer) Ping(ctx context.Context, req *wscpb.PingRequest) (*wscpb.PingResponse, error) {
	// 获取活跃连接数（总连接数，原子读取零锁开销）
	activeConnections := s.hub.shardedRegistry.GetClientCount()
	return &wscpb.PingResponse{
		NodeId:            s.hub.GetNodeID(),
		ActiveConnections: activeConnections,
		Healthy:           !s.hub.IsShutdown(),
	}, nil
}

// ============================================================================
// gRPC 生命周期管理（原 grpc_lifecycle.go 并入）
// ============================================================================

// InitNodeGRPC 初始化节点间 gRPC 通信组件
//
// 在 SetPubSub 之后调用：节点发现依赖 Redis（从 PubSub 获取客户端）
// 若未启用 node-grpc 配置或 PubSub 未设置，则跳过初始化，Hub 退化为 Redis PubSub 模式
func (h *Hub) InitNodeGRPC() {
	if !h.config.NodeGRPC.IsEnabled() {
		h.logger.InfoKV("节点 gRPC 通信未启用，使用 Redis PubSub 模式", "node_id", h.nodeID)
		return
	}
	if h.pubsub == nil {
		h.logger.WarnKV("PubSub 未设置，无法启用节点 gRPC 通信（节点发现依赖 Redis）", "node_id", h.nodeID)
		return
	}

	grpcAddr := h.config.NodeGRPC.GetAddress()
	redisClient := h.pubsub.GetClient()

	h.nodeRegistry = cluster.NewNodeRegistry(redisClient, h.nodeID, grpcAddr,
		h.config.NodeGRPC.GetNodeGRPCKey(),
		h.config.NodeGRPC.GetNodeHeartbeatKey(),
		h.logger,
	)
	// hub.go 不持有 grpcServer 字段，服务端引用存于包级 grpcServers 映射（见上）
	grpcServers.Store(h, NewGRPCServer(h))
	h.grpcClientPool = cluster.NewGRPCClientPool()

	h.logger.InfoKV("节点 gRPC 通信组件已初始化",
		"node_id", h.nodeID,
		"grpc_addr", grpcAddr,
		"tls_enabled", h.config.NodeGRPC.TLSEnabled,
	)
}

// startNodeGRPC 启动 gRPC 服务端并注册本节点到 Redis
//
// 在 Hub.Run 中调用，启动顺序：
//  1. 启动 gRPC 服务端监听（接收远端节点请求）
//  2. 注册本节点 gRPC 地址到 Redis（供其他节点发现）
//
// 任一步失败仅记录错误不中断 Hub 启动，保证 gRPC 不可用时仍可降级到 PubSub
func (h *Hub) startNodeGRPC() {
	if !h.IsGRPCEnabled() || h.nodeRegistry == nil {
		return
	}

	v, ok := grpcServers.Load(h)
	if !ok {
		return
	}
	server := v.(*GRPCServer)

	// 1. 启动 gRPC 服务端
	grpcAddr := h.config.NodeGRPC.GetAddress()
	if err := server.Start(h.ctx, grpcAddr); err != nil {
		h.logger.ErrorKV("启动 gRPC 服务端失败，降级到 Redis PubSub",
			"error", err, "addr", grpcAddr, "node_id", h.nodeID)
		return
	}

	// 更新 nodeRegistry 的实际监听地址
	// 配置端口为 0（随机端口）时，listener 绑定后才知实际端口；
	// Register 写入 Redis 和 GetNodeAddr 返回本节点地址都需用实际地址
	if server.listener != nil {
		h.nodeRegistry.SetGRPCAddr(server.listener.Addr().String())
	}

	// 2. 注册本节点到 Redis 节点发现表
	syncx.Go(h.ctx).
		WithTimeout(5 * time.Second).
		OnPanic(func(r any) {
			h.logger.ErrorKV("注册节点到 Redis panic", "panic", r, "stack", string(debug.Stack()), "node_id", h.nodeID)
		}).
		OnError(func(err error) {
			h.logger.ErrorKV("注册节点到 Redis 失败，gRPC 路由可能受影响",
				"error", err, "node_id", h.nodeID)
		}).
		ExecWithContext(func(ctx context.Context) error {
			return h.nodeRegistry.Register(ctx)
		})

	h.logger.InfoKV("🔗 节点 gRPC 服务已启动", "node_id", h.nodeID, "addr", grpcAddr)
}

// stopNodeGRPC 停止节点间 gRPC 通信组件
//
// 在 SafeShutdown 中调用，停止顺序：
//  1. 注销本节点（从 Redis 节点表移除，避免其他节点路由到已下线节点）
//  2. 停止 gRPC 服务端（优雅关闭，等待在途请求完成）
//  3. 停止节点注册中心（停止心跳刷新循环）
//  4. 关闭 gRPC 客户端连接池（关闭到所有节点的连接）
func (h *Hub) stopNodeGRPC() {
	if !h.IsGRPCEnabled() {
		return
	}

	// 1. 先停止节点注册中心（终止 refreshLoop）
	// 必须先于 Unregister：若先注销再停止，refreshLoop 的 ticker 恰好触发时会
	// registerNode 把已注销节点重新写回 Redis，残留注册信息最长 90s（TTL），
	// 期间其他节点 gRPC 路由持续向已下线节点发起连接
	if h.nodeRegistry != nil {
		h.nodeRegistry.Stop()
	}

	// 2. 注销本节点（短超时，避免 shutdown 阻塞过久）
	if h.nodeRegistry != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := h.nodeRegistry.Unregister(ctx); err != nil {
			h.logger.WarnKV("注销节点失败", "error", err, "node_id", h.nodeID)
		}
		cancel()
	}

	// 3. 停止 gRPC 服务端（LoadAndDelete 同时释放包级映射中的引用）
	if v, ok := grpcServers.LoadAndDelete(h); ok {
		v.(*GRPCServer).Stop()
	}

	// 4. 关闭 gRPC 客户端连接池
	if h.grpcClientPool != nil {
		h.grpcClientPool.Close()
	}

	h.logger.InfoKV("🔗 节点 gRPC 服务已停止", "node_id", h.nodeID)
}

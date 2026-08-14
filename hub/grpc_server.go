/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-18 00:00:00
 * @FilePath: \go-wsc\hub\grpc_server.go
 * @Description: Hub gRPC 服务端 - 实现节点间 NodeService 接口
 *
 * 每个 WebSocket Hub 节点运行 gRPC 服务端，接收来自其他节点的点对点请求，
 * 包括消息投递、在线检查、群组广播、观察者通知、踢人与健康检查
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/netx"
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
// 使用 ForEachUserClient 零拷贝遍历 + 预序列化，替代 GetClientsByUserID 切片拷贝 + 逐客户端序列化
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
	if !s.hub.HasUserClient(userID) {
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

	// 零拷贝遍历：仅对路由匹配的设备投递，避免跨 namespace 串扰
	// （路由信封来自 msg 自身，跨节点 gRPC 调用链已丢失原 ctx 路由信息）
	s.hub.shardedRegistry.ForEachUserClientFiltered(userID, msg.Namespace, msg.GroupIDs, func(_ string, client *Client) bool {
		s.hub.sendToClientSerialized(ctx, client, msg, preSerialized)
		return true
	})

	// 🔔 通知本节点观察者（跨节点 gRPC 消息也需要通知观察者，与本地 broadcast 流程一致）
	// 路由来源：直接从 msg 信封取（不再"猜"接收者 client 的 ns/group，跨节点场景下 user 可能本节点无 client）
	observerCtx := routing.WithNamespaceGroupIDs(ctx, msg.Namespace, msg.GroupIDs)
	s.hub.notifyObservers(observerCtx, msg)

	return &wscpb.SendToUserResponse{
		Success:    true,
		UserOnline: true,
	}, nil
}

// CheckUsersOnline 批量检查用户是否在本节点在线（路由探测）
// 使用 HasUserClient O(1) 检查，替代 GetClientsByUserID 切片分配
func (s *GRPCServer) CheckUsersOnline(ctx context.Context, req *wscpb.CheckUsersOnlineRequest) (*wscpb.CheckUsersOnlineResponse, error) {
	onlineUsers := make(map[string]bool, len(req.GetUserIds()))
	for _, userID := range req.GetUserIds() {
		onlineUsers[userID] = s.hub.HasUserClient(userID)
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

	// 群组仓库未配置，无法获取成员
	if s.hub.groupRepo == nil {
		return &wscpb.BroadcastGroupResponse{Delivered: 0}, nil
	}

	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	// BroadcastGroup RPC 语义为单群组广播（cluster_dispatch 每次传单群组），取首元素
	groupID := ""
	if len(groupIDs) > 0 {
		groupID = groupIDs[0]
	}

	// 获取群组成员列表
	members, err := s.hub.groupRepo.GetMembers(ctx, namespace, groupID)
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
	// 群组成员过滤已由 groupRepo.GetMembers + memberSet 完成，下面清除 ctx 的 groupIDs 后，
	// 下游 broadcastToFiltered 调 InjectRoute 时只会注入 namespace（msg.GroupIDs 保持 nil），
	// ClientMatchesEnvelope 仅做 namespace 隔离，不再触碰系统组维度。
	ctx = routing.WithNamespaceGroupIDs(ctx, namespace, nil)

	// 构建成员集合用于 O(1) 过滤
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m] = struct{}{}
	}

	// 过滤广播：只投递给群组成员，按需排除发送者
	excludeSender := req.GetExcludeSender()
	senderID := req.GetSenderId()
	delivered := s.hub.broadcastToFiltered(ctx, func(client *Client) bool {
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
	// 从 gRPC incoming metadata 恢复 trace_id + 路由元数据 到 ctx（跨节点链路串联）
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
	observers := s.hub.GetObserversForMessage(namespace, groupIDs...)

	// 逐个投递
	var notified int32
	for _, client := range observers {
		s.hub.sendToClient(ctx, client, msg)
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

	kicked := s.hub.KickUserSimple(req.GetUserId(), req.GetReason())
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

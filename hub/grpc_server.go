/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-16 12:06:56
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-15 23:20:30
 * @FilePath: \go-wsc\hub\grpc_server.go
 * @Description:
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
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
	"fmt"
	"net"

	wscpb "github.com/kamalyes/go-wsc/models/pb"
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
func (s *GRPCServer) Start(ctx context.Context, addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("监听地址 %s 失败: %w", addr, err)
	}
	s.listener = listener

	s.server = grpc.NewServer()
	wscpb.RegisterNodeServiceServer(s.server, s)

	go func() {
		if err := s.server.Serve(listener); err != nil {
			s.hub.logger.ErrorKV("gRPC 服务端运行异常", "error", err, "addr", addr)
		}
	}()

	s.hub.logger.InfoKV("gRPC 服务端已启动", "addr", addr, "node_id", s.hub.GetNodeID())
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
func (s *GRPCServer) SendToUser(ctx context.Context, req *wscpb.SendToUserRequest) (*wscpb.SendToUserResponse, error) {
	// 反序列化消息
	msg, err := wscpb.UnmarshalHubMessage(req.GetMessageData())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "反序列化消息失败: %v", err)
	}

	// 查找本地客户端
	clients := s.hub.GetClientsByUserID(req.GetUserId())
	if len(clients) == 0 {
		return &wscpb.SendToUserResponse{
			Success:    false,
			Error:      "用户不在线",
			UserOnline: false,
		}, nil
	}

	// 发送到该用户的所有客户端连接（多端登录）
	for _, client := range clients {
		s.hub.sendToClient(client, msg)
	}

	return &wscpb.SendToUserResponse{
		Success:    true,
		UserOnline: true,
	}, nil
}

// CheckUsersOnline 批量检查用户是否在本节点在线（路由探测）
func (s *GRPCServer) CheckUsersOnline(ctx context.Context, req *wscpb.CheckUsersOnlineRequest) (*wscpb.CheckUsersOnlineResponse, error) {
	onlineUsers := make(map[string]bool, len(req.GetUserIds()))
	for _, userID := range req.GetUserIds() {
		clients := s.hub.GetClientsByUserID(userID)
		onlineUsers[userID] = len(clients) > 0
	}

	return &wscpb.CheckUsersOnlineResponse{
		OnlineUsers: onlineUsers,
	}, nil
}

// BroadcastGroup 向本节点的群组成员广播消息
func (s *GRPCServer) BroadcastGroup(ctx context.Context, req *wscpb.BroadcastGroupRequest) (*wscpb.BroadcastGroupResponse, error) {
	// 群组仓库未配置，无法获取成员
	if s.hub.groupRepo == nil {
		return &wscpb.BroadcastGroupResponse{Delivered: 0}, nil
	}

	// 获取群组成员列表
	members, err := s.hub.groupRepo.GetMembers(ctx, req.GetNamespace(), req.GetGroupId())
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

	// 构建成员集合用于 O(1) 过滤
	memberSet := make(map[string]struct{}, len(members))
	for _, m := range members {
		memberSet[m] = struct{}{}
	}

	// 过滤广播：只投递给群组成员，按需排除发送者
	excludeSender := req.GetExcludeSender()
	senderID := req.GetSenderId()
	delivered := s.hub.broadcastToFiltered(func(client *Client) bool {
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
// group_id 非空时通过三级索引查找订阅该群组的观察者，为空时查找命名空间级观察者
func (s *GRPCServer) NotifyObservers(ctx context.Context, req *wscpb.NotifyObserversRequest) (*wscpb.NotifyObserversResponse, error) {
	// 反序列化消息
	msg, err := wscpb.UnmarshalHubMessage(req.GetMessageData())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "反序列化消息失败: %v", err)
	}

	// 三级索引查找：全局 + 命名空间 + 命名空间+群组
	observers := s.hub.GetObserversForMessage(req.GetNamespace(), req.GetGroupId())

	// 逐个投递
	var notified int32
	for _, client := range observers {
		s.hub.sendToClient(client, msg)
		notified++
	}

	return &wscpb.NotifyObserversResponse{
		Notified: notified,
	}, nil
}

// KickUser 踢出本节点上的用户
func (s *GRPCServer) KickUser(ctx context.Context, req *wscpb.KickUserRequest) (*wscpb.KickUserResponse, error) {
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

/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-11 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-18 00:00:00
 * @FilePath: \go-wsc\hub\grpc_client.go
 * @Description: Hub gRPC 客户端连接池 - 管理到其他节点的 gRPC 连接
 *
 * 使用 sync.Map 缓存到各节点的 *grpc.ClientConn，避免重复建连；
 * 提供高级方法封装 NodeServiceClient 调用，简化跨节点通信
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"sync"

	"github.com/kamalyes/go-logger"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ============================================================================
// GRPCClientPool - 节点间 gRPC 客户端连接池
// ============================================================================

// GRPCClientPool gRPC 客户端连接池，管理到其他节点的连接
// 使用 sync.Map 按 addr 缓存连接，LoadOrStore 避免并发重复创建
type GRPCClientPool struct {
	connections sync.Map // addr → *grpc.ClientConn
}

// NewGRPCClientPool 创建新的 gRPC 客户端连接池
func NewGRPCClientPool() *GRPCClientPool {
	return &GRPCClientPool{}
}

// GetClient 获取或创建到指定地址的 gRPC 连接
// 同一地址复用同一连接，避免重复建连
func (p *GRPCClientPool) GetClient(addr string) (wscpb.NodeServiceClient, error) {
	// 1. 先尝试从缓存加载已有连接
	if val, ok := p.connections.Load(addr); ok {
		conn := val.(*grpc.ClientConn)
		return wscpb.NewNodeServiceClient(conn), nil
	}

	// 2. 缓存未命中，创建新连接
	// 使用 insecure 凭证（节点间内网通信），并设置 4MB 最大接收消息体
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(4*1024*1024)),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 gRPC 连接失败 (addr=%s): %w", addr, err)
	}

	// 3. LoadOrStore 保证并发场景下同一地址只保留一个连接
	actual, loaded := p.connections.LoadOrStore(addr, conn)
	if loaded {
		// 已有其他 goroutine 率先创建了连接，关闭本次冗余连接
		conn.Close()
		conn = actual.(*grpc.ClientConn)
	}

	return wscpb.NewNodeServiceClient(conn), nil
}

// Close 关闭所有连接并清空连接池
func (p *GRPCClientPool) Close() {
	p.connections.Range(func(key, value any) bool {
		if conn, ok := value.(*grpc.ClientConn); ok {
			conn.Close()
		}
		p.connections.Delete(key)
		return true
	})
}

// ============================================================================
// 高级封装方法
// ============================================================================

// SendToUser 向指定节点的用户发送消息
func (p *GRPCClientPool) SendToUser(ctx context.Context, addr, userID string, msgData []byte) (*wscpb.SendToUserResponse, error) {
	client, err := p.GetClient(addr)
	if err != nil {
		return nil, err
	}
	// 注入 trace_id 到 gRPC metadata（跨节点传播）
	ctx = logger.InjectTraceToOutgoing(ctx, logger.ExtractTraceID(ctx))
	return client.SendToUser(ctx, &wscpb.SendToUserRequest{
		UserId:      userID,
		MessageData: msgData,
	})
}

// CheckUsersOnline 批量检查用户是否在指定节点在线
func (p *GRPCClientPool) CheckUsersOnline(ctx context.Context, addr string, userIDs []string) (map[string]bool, error) {
	client, err := p.GetClient(addr)
	if err != nil {
		return nil, err
	}
	resp, err := client.CheckUsersOnline(ctx, &wscpb.CheckUsersOnlineRequest{
		UserIds: userIDs,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetOnlineUsers(), nil
}

// BroadcastGroup 向指定节点的群组成员广播消息
// namespace/groupID 从 ctx 提取并注入 gRPC metadata 跨节点传播
func (p *GRPCClientPool) BroadcastGroup(ctx context.Context, addr string, msgData []byte, excludeSender bool, senderID string) (int32, error) {
	client, err := p.GetClient(addr)
	if err != nil {
		return 0, err
	}
	// 注入 trace_id + 路由元数据 到 gRPC metadata（跨节点传播）
	ctx = logger.InjectTraceToOutgoing(ctx, logger.ExtractTraceID(ctx))
	ctx = routing.InjectToOutgoingMetadata(ctx)
	resp, err := client.BroadcastGroup(ctx, &wscpb.BroadcastGroupRequest{
		MessageData:   msgData,
		ExcludeSender: excludeSender,
		SenderId:      senderID,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetDelivered(), nil
}

// NotifyObservers 通知指定节点的观察者
// namespace/groupID 从 ctx 提取并注入 gRPC metadata 跨节点传播
func (p *GRPCClientPool) NotifyObservers(ctx context.Context, addr string, msgData []byte) (int32, error) {
	client, err := p.GetClient(addr)
	if err != nil {
		return 0, err
	}
	// 注入 trace_id + 路由元数据 到 gRPC metadata（跨节点传播）
	ctx = logger.InjectTraceToOutgoing(ctx, logger.ExtractTraceID(ctx))
	ctx = routing.InjectToOutgoingMetadata(ctx)
	resp, err := client.NotifyObservers(ctx, &wscpb.NotifyObserversRequest{
		MessageData: msgData,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetNotified(), nil
}

// Ping 对指定节点进行健康检查
func (p *GRPCClientPool) Ping(ctx context.Context, addr string) (*wscpb.PingResponse, error) {
	client, err := p.GetClient(addr)
	if err != nil {
		return nil, err
	}
	return client.Ping(ctx, &wscpb.PingRequest{})
}

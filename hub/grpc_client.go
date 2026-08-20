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
 * 每个 addr 维护独立的熔断器（breaker.Circuit），连续失败后自动熔断，
 * 避免向故障节点持续发送请求导致级联雪崩
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/breaker"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ============================================================================
// GRPCClientPool - 节点间 gRPC 客户端连接池
// ============================================================================

// 默认熔断器配置：5 次连续失败后熔断，30s 后进入半开试探，2 次成功后恢复
const (
	defaultBreakerMaxFailures       int32 = 5
	defaultBreakerResetTimeout            = 30 * time.Second
	defaultBreakerHalfOpenSuccesses int32 = 2
)

// GRPCClientPool gRPC 客户端连接池，管理到其他节点的连接
// 使用 sync.Map 按 addr 缓存连接，LoadOrStore 避免并发重复创建
type GRPCClientPool struct {
	connections   sync.Map        // addr → *grpc.ClientConn
	breakers      sync.Map        // addr → *breaker.Circuit（per-node 熔断器）
	breakerConfig *breaker.Config // 可选自定义熔断器配置（nil 用默认）
}

// NewGRPCClientPool 创建新的 gRPC 客户端连接池
func NewGRPCClientPool() *GRPCClientPool {
	return &GRPCClientPool{}
}

// SetBreakerConfig 设置自定义熔断器配置（用于测试或生产调优）
// 必须在首次调用任何 gRPC 方法前设置，否则已创建的熔断器不受影响
func (p *GRPCClientPool) SetBreakerConfig(config breaker.Config) {
	p.breakerConfig = &config
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

// getOrCreateBreaker 获取或创建指定 addr 的熔断器
// 每个节点维护独立熔断器，故障节点不会影响对其他节点的调用
func (p *GRPCClientPool) getOrCreateBreaker(addr string) *breaker.Circuit {
	if val, ok := p.breakers.Load(addr); ok {
		return val.(*breaker.Circuit)
	}
	// 优先用自定义配置（测试或生产调优），否则用默认值
	cfg := breaker.Config{
		MaxFailures:       defaultBreakerMaxFailures,
		ResetTimeout:      defaultBreakerResetTimeout,
		HalfOpenSuccesses: defaultBreakerHalfOpenSuccesses,
		OnStateChange: func(from, to breaker.State) {
			// 使用标准库 log 避免循环依赖 go-logger 实例；
			// 状态变更属于运维事件，标准日志即可满足审计需求
			log.Printf("[WSC] gRPC 节点熔断器状态变更 addr=%s from=%s to=%s", addr, from.String(), to.String())
		},
	}
	if p.breakerConfig != nil {
		cfg = *p.breakerConfig
	}
	cb := breaker.New(fmt.Sprintf("grpc-node-%s", addr), cfg)
	actual, _ := p.breakers.LoadOrStore(addr, cb)
	return actual.(*breaker.Circuit)
}

// GetBreakerStats 获取指定节点的熔断器统计信息（用于监控/运维）
func (p *GRPCClientPool) GetBreakerStats(addr string) breaker.CircuitStats {
	cb := p.getOrCreateBreaker(addr)
	return cb.Stats()
}

// IsCircuitOpen 判断指定节点的熔断器是否处于开启状态（快速短路）
func (p *GRPCClientPool) IsCircuitOpen(addr string) bool {
	cb := p.getOrCreateBreaker(addr)
	return cb.GetState() == breaker.StateOpen
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
	// 熔断器无需显式关闭，仅清空引用
	p.breakers.Range(func(key, value any) bool {
		p.breakers.Delete(key)
		return true
	})
}

// ============================================================================
// 高级封装方法（带熔断保护）
// ============================================================================

// SendToUser 向指定节点的用户发送消息
// 带熔断保护：连续失败后自动熔断，避免向故障节点持续发请求
func (p *GRPCClientPool) SendToUser(ctx context.Context, addr, userID string, msgData []byte) (*wscpb.SendToUserResponse, error) {
	cb := p.getOrCreateBreaker(addr)
	var resp *wscpb.SendToUserResponse
	err := cb.Execute(func() error {
		client, err := p.GetClient(addr)
		if err != nil {
			return err
		}
		// 注入 trace_id 到 gRPC metadata（跨节点传播）
		ctx = logger.InjectTraceToOutgoing(ctx, logger.ExtractTraceID(ctx))
		resp, err = client.SendToUser(ctx, &wscpb.SendToUserRequest{
			UserId:      userID,
			MessageData: msgData,
		})
		return err
	})
	return resp, err
}

// CheckUsersOnline 批量检查用户是否在指定节点在线
// 带熔断保护：读操作也需保护，避免故障节点拖慢批量查询
func (p *GRPCClientPool) CheckUsersOnline(ctx context.Context, addr string, userIDs []string) (map[string]bool, error) {
	cb := p.getOrCreateBreaker(addr)
	var result map[string]bool
	err := cb.Execute(func() error {
		client, err := p.GetClient(addr)
		if err != nil {
			return err
		}
		resp, err := client.CheckUsersOnline(ctx, &wscpb.CheckUsersOnlineRequest{
			UserIds: userIDs,
		})
		if err != nil {
			return err
		}
		result = resp.GetOnlineUsers()
		return nil
	})
	return result, err
}

// BroadcastGroup 向指定节点的群组成员广播消息
// namespace/groupID 从 ctx 提取并注入 gRPC metadata 跨节点传播
// 带熔断保护：广播失败（节点不可达）不应阻塞调用方
func (p *GRPCClientPool) BroadcastGroup(ctx context.Context, addr string, msgData []byte, excludeSender bool, senderID string) (int32, error) {
	cb := p.getOrCreateBreaker(addr)
	var delivered int32
	err := cb.Execute(func() error {
		client, err := p.GetClient(addr)
		if err != nil {
			return err
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
			return err
		}
		delivered = resp.GetDelivered()
		return nil
	})
	return delivered, err
}

// NotifyObservers 通知指定节点的观察者
// namespace/groupID 从 ctx 提取并注入 gRPC metadata 跨节点传播
// 带熔断保护：observer 通知失败不应阻塞主流程
func (p *GRPCClientPool) NotifyObservers(ctx context.Context, addr string, msgData []byte) (int32, error) {
	cb := p.getOrCreateBreaker(addr)
	var notified int32
	err := cb.Execute(func() error {
		client, err := p.GetClient(addr)
		if err != nil {
			return err
		}
		// 注入 trace_id + 路由元数据 到 gRPC metadata（跨节点传播）
		ctx = logger.InjectTraceToOutgoing(ctx, logger.ExtractTraceID(ctx))
		ctx = routing.InjectToOutgoingMetadata(ctx)
		resp, err := client.NotifyObservers(ctx, &wscpb.NotifyObserversRequest{
			MessageData: msgData,
		})
		if err != nil {
			return err
		}
		notified = resp.GetNotified()
		return nil
	})
	return notified, err
}

// Ping 对指定节点进行健康检查
// 不经过熔断器——Ping 是探测节点是否恢复的手段，即使在半开状态也需直接调用
func (p *GRPCClientPool) Ping(ctx context.Context, addr string) (*wscpb.PingResponse, error) {
	client, err := p.GetClient(addr)
	if err != nil {
		return nil, err
	}
	return client.Ping(ctx, &wscpb.PingRequest{})
}

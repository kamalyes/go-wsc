/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-18 12:54:51
 * @FilePath: \go-wsc\hub\grpc_lifecycle.go
 * @Description: Hub gRPC 生命周期管理 - 初始化、启动与关闭
 *
 * 启用 node-grpc 配置后：
 *   - InitNodeGRPC 创建 NodeRegistry/GRPCServer/GRPCClientPool 三件套
 *   - startNodeGRPC 在 Run 中启动服务端并注册到 Redis，其他节点可通过发现机制直连
 *   - stopNodeGRPC 在 SafeShutdown 中优雅停止
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"runtime/debug"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

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

	h.nodeRegistry = NewNodeRegistry(redisClient, h.nodeID, grpcAddr,
		h.config.NodeGRPC.GetNodeGRPCKey(),
		h.config.NodeGRPC.GetNodeHeartbeatKey(),
		h.logger,
	)
	h.grpcServer = NewGRPCServer(h)
	h.grpcClientPool = NewGRPCClientPool()

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
	if !h.IsGRPCEnabled() {
		return
	}

	// 1. 启动 gRPC 服务端
	grpcAddr := h.config.NodeGRPC.GetAddress()
	if err := h.grpcServer.Start(h.ctx, grpcAddr); err != nil {
		h.logger.ErrorKV("启动 gRPC 服务端失败，降级到 Redis PubSub",
			"error", err, "addr", grpcAddr, "node_id", h.nodeID)
		return
	}

	// 更新 nodeRegistry 的实际监听地址
	// 配置端口为 0（随机端口）时，listener 绑定后才知实际端口；
	// Register 写入 Redis 和 GetNodeAddr 返回本节点地址都需用实际地址
	if h.nodeRegistry != nil && h.grpcServer.listener != nil {
		h.nodeRegistry.grpcAddr = h.grpcServer.listener.Addr().String()
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

	// 3. 停止 gRPC 服务端
	if h.grpcServer != nil {
		h.grpcServer.Stop()
	}

	// 4. 关闭 gRPC 客户端连接池
	if h.grpcClientPool != nil {
		h.grpcClientPool.Close()
	}

	h.logger.InfoKV("🔗 节点 gRPC 服务已停止", "node_id", h.nodeID)
}

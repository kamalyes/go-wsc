/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-17 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-18 00:00:00
 * @FilePath: \go-wsc\hub\node_registry.go
 * @Description: 节点注册与发现 - 基于 Redis 的节点 gRPC 地址管理
 *
 * 每个节点启动时将自身的 gRPC 地址注册到 Redis，其他节点通过 Redis 发现所有活跃节点
 * 节点定期刷新心跳，超时自动淘汰
 *
 * Redis Key 设计：
 *   - wsc:nodes:grpc     → Hash { nodeID → grpc_address }
 *   - wsc:nodes:heartbeat → Hash { nodeID → unix_timestamp }
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// ============================================================================
// 常量
// ============================================================================

const (
	// nodeRegistryTTL 节点注册信息的 TTL（超过此时间未刷新视为离线）
	nodeRegistryTTL = 90 * time.Second
	// nodeRefreshInterval 节点列表刷新间隔
	nodeRefreshInterval = 30 * time.Second
)

// ============================================================================
// NodeRegistry 节点注册中心
// ============================================================================

// NodeRegistry 管理节点发现与 gRPC 地址映射
type NodeRegistry struct {
	redisClient  redis.UniversalClient
	localNodeID  string
	grpcAddr     string
	grpcKey      string // 节点 gRPC 地址 Hash key（从配置获取）
	heartbeatKey string // 节点心跳 Hash key（从配置获取）
	logger       WSCLogger

	// nodes 缓存所有活跃节点的 gRPC 地址（nodeID → addr）
	nodes sync.Map

	// stopCh 停止信号
	stopCh chan struct{}
	wg     sync.WaitGroup

	// mu 保护 stopped/wg，防止 Stop 与 Register 的 wg.Add/Wait 并发竞争
	mu      sync.Mutex
	stopped bool
}

// NewNodeRegistry 创建节点注册中心
// grpcKey/heartbeatKey 来自 go-config NodeGRPC 配置，默认 "wsc:nodes:grpc" / "wsc:nodes:heartbeat"
func NewNodeRegistry(redisClient redis.UniversalClient, nodeID, grpcAddr, grpcKey, heartbeatKey string, logger WSCLogger) *NodeRegistry {
	return &NodeRegistry{
		redisClient:  redisClient,
		localNodeID:  nodeID,
		grpcAddr:     grpcAddr,
		grpcKey:      grpcKey,
		heartbeatKey: heartbeatKey,
		logger:       logger,
		stopCh:       make(chan struct{}),
	}
}

// Register 注册本节点到 Redis 并启动定期刷新
func (r *NodeRegistry) Register(ctx context.Context) error {
	if r.redisClient == nil || r.grpcAddr == "" {
		return nil
	}

	// 注册 gRPC 地址和心跳
	if err := r.registerNode(ctx); err != nil {
		return fmt.Errorf("注册节点失败: %w", err)
	}

	// 首次拉取全量节点列表
	if err := r.refreshNodes(ctx); err != nil {
		return fmt.Errorf("首次刷新节点列表失败: %w", err)
	}

	// 启动定期刷新（加锁防止与 Stop 并发导致 wg.Add/Wait 竞争）
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return nil
	}
	r.wg.Add(1)
	r.mu.Unlock()
	go r.refreshLoop()

	return nil
}

// Unregister 注销本节点
func (r *NodeRegistry) Unregister(ctx context.Context) error {
	if r.redisClient == nil {
		return nil
	}

	pipe := r.redisClient.Pipeline()
	pipe.HDel(ctx, r.grpcKey, r.localNodeID)
	pipe.HDel(ctx, r.heartbeatKey, r.localNodeID)
	_, err := pipe.Exec(ctx)
	return err
}

// Stop 停止节点注册中心
func (r *NodeRegistry) Stop() {
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	close(r.stopCh)
	r.mu.Unlock()
	r.wg.Wait()
}

// GetNodeAddr 获取指定节点的 gRPC 地址
func (r *NodeRegistry) GetNodeAddr(nodeID string) (string, bool) {
	if nodeID == r.localNodeID {
		return r.grpcAddr, true
	}
	addr, ok := r.nodes.Load(nodeID)
	if !ok {
		return "", false
	}
	return addr.(string), true
}

// GetAllNodes 获取所有已知节点（不含本节点）
func (r *NodeRegistry) GetAllNodes() map[string]string {
	result := make(map[string]string)
	r.nodes.Range(func(key, value any) bool {
		nodeID := key.(string)
		if nodeID != r.localNodeID {
			result[nodeID] = value.(string)
		}
		return true
	})
	return result
}

// registerNode 向 Redis 写入本节点的 gRPC 地址与心跳，并刷新 key 的 TTL
// 注册与周期刷新共用，保证 key 被删除或过期后能自动恢复上报
func (r *NodeRegistry) registerNode(ctx context.Context) error {
	pipe := r.redisClient.Pipeline()
	pipe.HSet(ctx, r.grpcKey, r.localNodeID, r.grpcAddr)
	pipe.HSet(ctx, r.heartbeatKey, r.localNodeID, time.Now().Unix())
	pipe.Expire(ctx, r.grpcKey, nodeRegistryTTL)
	pipe.Expire(ctx, r.heartbeatKey, nodeRegistryTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// refreshLoop 定期刷新本节点心跳和全量节点列表
func (r *NodeRegistry) refreshLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(nodeRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			// 重新注册本节点（重写 grpc 地址 + 心跳 + TTL，key 被删除后可自动恢复）
			// 失败必须记日志：Redis 持续故障时本节点会静默从发现表消失，
			// 其他节点 gRPC 路由全部扑空（降级 PubSub），无日志无法定位根因
			if err := r.registerNode(ctx); err != nil && r.logger != nil {
				r.logger.WarnKV("刷新节点注册失败", "node_id", r.localNodeID, "error", err)
			}
			// 刷新全量节点列表
			if err := r.refreshNodes(ctx); err != nil && r.logger != nil {
				r.logger.WarnKV("刷新节点列表失败", "node_id", r.localNodeID, "error", err)
			}
			cancel()
		}
	}
}

// refreshNodes 从 Redis 拉取全量节点列表并清理过期节点
func (r *NodeRegistry) refreshNodes(ctx context.Context) error {
	// 获取所有 gRPC 地址
	addrMap, err := r.redisClient.HGetAll(ctx, r.grpcKey).Result()
	if err != nil {
		return err
	}

	// 获取所有心跳时间
	heartbeatMap, err := r.redisClient.HGetAll(ctx, r.heartbeatKey).Result()
	if err != nil {
		return err
	}

	now := time.Now().Unix()
	expireThreshold := int64(nodeRegistryTTL / time.Second)

	// 更新本地缓存，清理过期节点
	r.nodes.Range(func(key, _ any) bool {
		nodeID := key.(string)
		if _, exists := addrMap[nodeID]; !exists {
			r.nodes.Delete(nodeID)
		}
		return true
	})

	for nodeID, addr := range addrMap {
		// 检查心跳是否过期
		if heartbeatStr, ok := heartbeatMap[nodeID]; ok {
			var heartbeat int64
			fmt.Sscanf(heartbeatStr, "%d", &heartbeat)
			if now-heartbeat > expireThreshold {
				// 心跳过期，清理
				r.redisClient.HDel(ctx, r.grpcKey, nodeID)
				r.redisClient.HDel(ctx, r.heartbeatKey, nodeID)
				r.nodes.Delete(nodeID)
				continue
			}
		}
		r.nodes.Store(nodeID, addr)
	}

	return nil
}

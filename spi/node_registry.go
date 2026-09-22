/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 10:15:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-11 11:25:00
 * @FilePath: \go-wsc\spi\node_registry.go
 * @Description: 节点注册与发现 SPI - NodeRegistry 接口契约定义
 *
 * 节点注册中心抽象：管理节点发现与 gRPC 地址映射
 * Redis 实现见 hub 包 NodeRegistry，后续随适配器拆分迁至 go-wsc-grpc-adapter
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
)

// NodeRegistry 节点注册中心接口
//
// 职责：
//  1. 注册本节点到注册中心（如 Redis Hash）
//  2. 发现所有活跃节点（节点列表 + gRPC 地址）
//  3. 定期刷新心跳，超时自动淘汰
//  4. 支持节点元数据存储（启动时间、负载等）
//
// 设计原则：
//   - 注册中心可插拔：Redis/etcd/Consul/静态配置等
//   - 节点信息 TTL 管理：心跳续期 + 过期自动清理
//   - 支持主动注销和被动超时淘汰
type NodeRegistry interface {
	// ========== 节点生命周期 ==========

	// Register 注册本节点到注册中心并启动定期刷新
	// 节点注册信息包含 gRPC 地址、心跳时间、启动时间等
	Register(ctx context.Context) error

	// Unregister 主动注销本节点（从注册中心移除）
	Unregister(ctx context.Context) error

	// Stop 停止节点注册中心（停止刷新循环）
	Stop()

	// ========== 节点查询 ==========

	// GetNodeAddr 获取指定节点的 gRPC 地址
	GetNodeAddr(nodeID string) (string, bool)

	// GetAllNodes 获取所有已知节点（不含本节点）返回 map[nodeID]gRPC地址
	GetAllNodes() map[string]string

	// ========== 监听地址回填 ==========

	// SetGRPCAddr 回填本节点对外的 gRPC 监听地址
	// 配置端口为 0（随机端口）时，监听器绑定后才能得知实际端口，必须在 Register 之前回填，否则注册进发现表的是无效地址
	SetGRPCAddr(addr string)
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 09:50:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 09:50:00
 * @FilePath: \go-wsc\adapter\redis\hooks.go
 * @Description: Redis 适配器装配钩子 - 实现 spi.StoreHooks

 * 业务侧用一行把本适配器负责的全部 Redis 仓库注入 Hub：

 *	err := spi.Initialize(ctx, hub, redisClient, db, redisadapter.NewHooks(cfg))

 * redisClient / db 是基础设施句柄，由调用方持有；spi.Initialize 校验 Redis 连通性后
 * 包成 spi.StoreDeps 交给本钩子。单后端部署时把另一侧的 hook 传 nil 即可。

 * 本包不认识 Hub 具体类型（只认 spi.StoreTarget 能力面），也不被 core 依赖
 * —— 依赖方向恒为 adapter -> core。

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package redisadapter

import (
	"context"
	"fmt"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"

	"github.com/kamalyes/go-wsc/spi"
	"github.com/redis/go-redis/v9"
)

// hooks 实现 spi.StoreHooks
type hooks struct {
	repo *wscconfig.RedisRepository
}

// NewHooks 创建 Redis 装配钩子
//
// repo 为 go-config 的 RedisRepository 配置节，nil 时各仓库使用内置默认值。
// 直接复用 go-config 的配置类型而非在本包重复声明一套：适配器构造函数的入参
// 本就是 *wscconfig.Xxx，重复声明只会引入两层结构间的字段搬运。
func NewHooks(repo *wscconfig.RedisRepository) spi.StoreHooks {
	return &hooks{repo: repo}
}

// Configure 装配本适配器负责的 Redis 仓库
//
// 覆盖：在线状态、集群统计、群组、客服负载、离线消息（Redis 队列侧）。
// deps.Redis 为 nil 表示业务侧未部署 Redis（纯 RDBMS 场景），直接跳过。
func (h *hooks) Configure(_ context.Context, hub spi.StoreTarget, deps spi.StoreDeps) error {
	if deps.Redis == nil {
		return nil
	}
	if hub == nil {
		return fmt.Errorf("redisadapter: store target is nil")
	}

	cfg := h.repo
	if cfg == nil {
		cfg = &wscconfig.RedisRepository{}
	}

	hub.SetOnlineStatusRepository(NewOnlineStore(deps.Redis, cfg.OnlineStatus))
	hub.SetHubStatsRepository(NewHubStats(deps.Redis, cfg.Stats))
	// 群组仓储套拓扑缓存：群消息投递热路径 0 回源（参数零值走 constants 默认值）
	hub.SetGroupRepository(NewGroupMemberCache(NewGroupStore(deps.Redis, groupKeyPrefix(cfg)), 0, 0, 0))

	// 客服负载：仅在启用时装配（未启用时 Hub 侧方法返回明确的未初始化错误）
	if cfg.Workload != nil {
		hub.SetWorkloadRepository(NewWorkloadStore(deps.Redis, deps.DB, cfg.Workload, spi.NewDefaultLogger()))
	}

	// 离线消息处理器不在此装配：它是 Redis 队列 + RDBMS 持久化的混合体，
	// 两半分别由本适配器（NewOfflineQueue）与 gorm 适配器（NewOfflineStore）
	// 提供，由业务侧组装 messaging.NewHybridOfflineMessageHandler。
	return nil
}

// NewOfflineQueue 构造离线消息队列（供业务侧组装混合处理器使用）
func NewOfflineQueue(client redis.UniversalClient, prefix string, ttl time.Duration) spi.MessageQueue {
	return NewMessageQueue(client, prefix, ttl)
}

// groupKeyPrefix 取群组 key 前缀（未配置时由 GroupStore 内部用默认值）
func groupKeyPrefix(cfg *wscconfig.RedisRepository) string {
	if cfg.Group != nil {
		return cfg.Group.KeyPrefix
	}
	return ""
}

// 编译期断言：本适配器必须始终满足 spi.StoreHooks
var _ spi.StoreHooks = (*hooks)(nil)

// 编译期断言：各仓库实现必须始终满足对应的 spi 契约
var (
	_ spi.OnlineStore   = (*OnlineStore)(nil)
	_ spi.HubStats      = (*HubStats)(nil)
	_ spi.GroupStore    = (*GroupStore)(nil)
	_ spi.WorkloadStore = (*WorkloadStore)(nil)
	_ spi.MessageQueue  = (*MessageQueue)(nil)
)

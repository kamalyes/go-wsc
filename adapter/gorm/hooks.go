/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 09:58:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 09:58:00
 * @FilePath: \go-wsc\adapter\gorm\hooks.go
 * @Description: GORM 适配器装配钩子 - 实现 spi.StoreHooks

 * 业务侧用一行把本适配器负责的全部关系型仓库注入 Hub：

 *	err := spi.Initialize(ctx, hub, redisClient, db, gormadapter.NewHooks(cfg))

 * db 是基础设施句柄，由调用方持有；spi.Initialize 包成 spi.StoreDeps 交给本钩子。
 * 不部署关系型后端时把本 hook 传 nil 即可，纯 Redis 部署照常工作。

 * 覆盖方言：MySQL / PostgreSQL / CockroachDB（由 GORM Dialector 决定，
 * 本包不自行判断，仅通过 Upsert 冲突子句由 dialect 包适配）。

 * Copyright (c) 2026 by kamalyes. All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"fmt"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/spi"
	"gorm.io/gorm"
)

// hooks 实现 spi.StoreHooks
type hooks struct {
	cfg *wscconfig.Database
}

// NewHooks 创建 GORM 装配钩子
// cfg 为 nil 时各仓库使用内置默认值
func NewHooks(cfg *wscconfig.Database) spi.StoreHooks {
	return &hooks{cfg: cfg}
}

// Configure 装配本适配器负责的关系型仓库
//
// 覆盖：消息记录、连接记录、连接质量。
// deps.DB 为 nil 表示业务侧未部署关系型库（纯 Redis 场景），直接跳过。
func (h *hooks) Configure(_ context.Context, hub spi.StoreTarget, deps spi.StoreDeps) error {
	if deps.DB == nil {
		return nil
	}
	if hub == nil {
		return fmt.Errorf("gormadapter: store target is nil")
	}

	cfg := h.cfg
	if cfg == nil {
		cfg = &wscconfig.Database{}
	}
	log := spi.NewDefaultLogger()

	hub.SetMessageRecordRepository(NewMessageSink(deps.DB, cfg.MessageRecord, log))
	hub.SetConnectionRecordRepository(NewConnectionStore(deps.DB, cfg.ConnectionRecord, log))
	// 连接质量复用 ConnectionRecord 配置（清理策略待定，暂不启用质量表自动清理）
	hub.SetConnectionQualityRepository(NewConnectionQualityStore(deps.DB, cfg.ConnectionRecord, log))

	return nil
}

// NewOfflineStoreFor 供 wiring 组装混合离线处理器使用
func NewOfflineStoreFor(db *gorm.DB, cfg *wscconfig.OfflineMessage) spi.OfflineStore {
	return NewOfflineStore(db, cfg, spi.NewDefaultLogger())
}

// 编译期断言：本适配器必须始终满足 spi.StoreHooks
var _ spi.StoreHooks = (*hooks)(nil)

// 编译期断言：各仓库实现必须始终满足对应的 spi 契约
var (
	_ spi.MessageSink            = (*MessageSink)(nil)
	_ spi.ConnectionStore        = (*ConnectionStore)(nil)
	_ spi.ConnectionQualityStore = (*ConnectionQualityStore)(nil)
	_ spi.OfflineStore           = (*OfflineStore)(nil)
)

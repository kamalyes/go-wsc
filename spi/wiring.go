/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 09:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 09:56:00
 * @FilePath: \go-wsc\spi\wiring.go
 * @Description: 存储装配 - 把适配器提供的实现注入 Hub

 * Hub 本身不认识任何具体存储实现（字段全是 spi 契约）本包负责「谁来构造
 * 实现、按什么顺序注入」这一步编排，但同样不 import 适配器包 ——
 * 依赖方向恒为 adapter → spi，spi 反向 import 适配器会构成循环

 * 因此装配走依赖倒置：调用方（适配器包或最终业务侧）传入一个
 * StoreHooks，本包负责校验前置条件、调用 hook、记录装配结果

 * 用法（见 adapter/redis 与 adapter/gorm）：

 *	redisHooks := redisadapter.NewHooks(cfg)
 *	gormHooks  := gormadapter.NewHooks(cfg)
 *	err := spi.Initialize(ctx, hub, redisClient, db, redisHooks, gormHooks)

 * 单后端部署时对应参数传 nil 即可 —— 未注入的能力按 spi 契约的 nil-safe
 * 约定降级（存储类 no-op，注册表/日志类为无条件依赖会在 NewHub 时构造）

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Initialize 装配全部存储后端
//
// 参数:
//   - ctx: 装配超时上下文（用于 Redis Ping 连通性校验）
//   - hub: 注入目标（StoreTarget 能力面）
//   - redisClient / db: 基础设施句柄，由调用方持有，本函数不接管其生命周期
//   - hooks: 各适配器提供的装配钩子，可为 nil（表示该后端未部署）
//
// 返回:
//   - error: 前置校验失败或任一 hook 装配失败
func Initialize(
	ctx context.Context,
	hub StoreTarget,
	redisClient redis.UniversalClient,
	db *gorm.DB,
	hooks ...StoreHooks,
) error {
	if hub == nil {
		return fmt.Errorf("spi: hub target is nil")
	}

	// Redis 连通性预检：不做这一步，坏连接会在首个连接注册时才暴露，
	// 表现为「客户端连上来就报错」，比启动期失败难定位得多
	if redisClient != nil {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := redisClient.Ping(pingCtx).Err(); err != nil {
			return fmt.Errorf("spi: redis ping failed: %w", err)
		}
	}

	deps := StoreDeps{Redis: redisClient, DB: db}

	active := 0
	for _, h := range hooks {
		if h == nil {
			continue
		}
		active++
		if err := h.Configure(ctx, hub, deps); err != nil {
			return fmt.Errorf("spi: configure storage failed: %w", err)
		}
	}

	if active == 0 {
		return fmt.Errorf("spi: no storage hooks provided, hub would run without persistence")
	}
	return nil
}

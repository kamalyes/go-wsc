/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 09:16:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 10:26:00
 * @FilePath: \go-wsc\spi\adapters.go
 * @Description: 存储适配器装配契约 - 由 adapter/redis 与 adapter/gorm 实现

 * core 只声明「需要哪些能力」以及「如何把已构造的实现注入 Hub」，不引用任何
 * 具体存储实现原因：依赖方向恒为 adapter → core（适配器实现 spi 契约），
 * 若 core 反过来 import 适配器即构成循环依赖，Go 编译器不允许

 * 因此原先 hub.InitializeRepositories(redisClient, db) 这一便捷方法（内部
 * 硬编码构造 Redis/GORM 仓库）必须迁出 core —— 它本质上就是装配适配器的
 * 职责，属于适配器包或最终业务侧的 composition root

 * 本文件保留 Hooks 抽象，使「一行完成全部注入」的能力以依赖倒置的方式存续：
 *   spi.Initialize(ctx, hub, redisClient, db, redisadapter.NewHooks(), gormadapter.NewHooks())

 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// StoreDeps 装配依赖 —— 一次交给适配器，由适配器各取所需
//
// 不拆成「Redis 一份、RDBMS 一份」两个方法：离线消息处理器这类组件
// 同时需要队列（Redis）与持久化（RDBMS），拆开后任何单个 hook 都拿不到
// 完整依赖，只能靠调用方在外部手工补齐，反而把装配逻辑漏回业务侧
type StoreDeps struct {
	// Redis Redis 客户端（纯 RDBMS 部署时为 nil）
	Redis redis.UniversalClient
	// DB GORM 实例（纯 Redis 部署时为 nil）
	DB *gorm.DB
}

// StoreHooks 存储层装配钩子
//
// 每个适配器包提供一个实现，用自己的依赖构造仓库实现并注入 Hub
// Hub 侧只认本接口，不认识任何具体适配器类型
//
// 依赖缺失时应跳过而非报错：纯 Redis 部署下 gorm 适配器的 hook 只要
// 检测到 deps.DB == nil 就返回 nil，不影响另一侧装配
type StoreHooks interface {
	// Configure 用给定依赖装配该适配器负责的全部仓库
	//
	// 返回:
	//   - error: 仅在该适配器所需依赖齐备却构造失败时返回；
	//     依赖缺失（如纯 Redis 场景无 DB）应直接跳过并返回 nil
	Configure(ctx context.Context, hub StoreTarget, deps StoreDeps) error
}

// StoreTarget 装配目标 —— 适配器通过这些 setter 把实现交给 Hub
//
// 能力面按「适配器实际需要注入哪些仓库」裁剪，而非抄 Hub 的完整 setter 表
type StoreTarget interface {
	SetOnlineStatusRepository(store OnlineStore)
	SetHubStatsRepository(store HubStats)
	SetGroupRepository(store GroupStore)
	SetWorkloadRepository(store WorkloadStore)
	SetMessageRecordRepository(sink MessageSink)
	SetConnectionRecordRepository(store ConnectionStore)
	SetConnectionQualityRepository(store ConnectionQualityStore)
	SetOfflineMessageHandler(queue OfflineQueue)
}

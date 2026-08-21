/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-31 09:08:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-31 11:08:55
 * @FilePath: \go-wsc\repository\constants.go
 * @Description: Repository 层私有常量（实现细节，不跨包共享）
 *
 * 跨包共享的常量已迁 constants 包：
 *   - Redis key 前缀 → constants/redis_key.go
 *   - Bitmap 分层常量（GlobalBitmapNS + 默认配置）→ constants/bitmap.go
 *   - 统计字段 StatsField* 为 dead constants 已删（hub_stats_repository.go 用自己的 Field*）
 * 本文件仅保留 repository 实现层私有常量（key 拼接后缀、环境变量名）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package repository

const (
	// ============================================================================
	// Bitmap key 拼接后缀（实现细节，仅 online_status_repository.go 用）
	// 完整 key: <keyPrefix> + bitmapKeySuffix + <appID> + ":" + <ns>
	// ============================================================================

	// bitmapKeySuffix Bitmap key 的后缀(接在 keyPrefix 之后)
	bitmapKeySuffix = "bm:"

	// uidMapKeySuffix userID→数字 offset 的 Hash key 后缀
	// 完整 key: <keyPrefix> + uidMapKeySuffix
	uidMapKeySuffix = "uid_map"

	// uidCounterKeySuffix offset 自增计数器 key 后缀
	// 完整 key: <keyPrefix> + uidCounterKeySuffix
	uidCounterKeySuffix = "uid_counter"

	// ============================================================================
	// Bitmap 配置环境变量名 - 短期方案(避免立即升级 go-config 模块)
	//
	// 长期方案:在 go-config 的 wscconfig.OnlineStatus 结构体正式声明字段,
	// 短期通过环境变量覆盖默认值,未设置时用 constants.DefaultXxx 常量
	// ============================================================================

	// envEnableBitmap 是否启用 bitmap 快速路径(灰度开关)
	envEnableBitmap = "WSC_ONLINE_ENABLE_BITMAP"

	// envBitmapTTL bitmap EXPIRE(字符串,如 "8s"),默认 HeartbeatRefreshInterval × 4
	envBitmapTTL = "WSC_ONLINE_BITMAP_TTL"

	// envMaxBitmapOffset offset 上限(防恶意膨胀,0=不限制)
	envMaxBitmapOffset = "WSC_ONLINE_MAX_BITMAP_OFFSET"

	// envMaxCachedUIDs 本地 L1 缓存容量上限
	envMaxCachedUIDs = "WSC_ONLINE_MAX_CACHED_UIDS"

	// envBitmapMigrationPhase 灰度阶段:dual-write|new-only|disabled
	envBitmapMigrationPhase = "WSC_ONLINE_BITMAP_MIGRATION_PHASE"
)

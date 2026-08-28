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

import "github.com/kamalyes/go-wsc/constants"

const (
	// ============================================================================
	// Bitmap key 拼接后缀（实现细节，仅 online_status_repository.go 用）
	// 完整 key: <keyPrefix> + bitmapKeySuffix + <appID> + ":" + <ns>
	// ============================================================================

	// bitmapKeySuffix Bitmap key 的后缀(接在 keyPrefix 之后)
	bitmapKeySuffix = "bm:"

	// uidMapKeySuffix userID→数字 offset 的 Hash key 后缀（分桶）
	// 完整 key: <keyPrefix> + uidMapKeySuffix + ":" + <bucket>
	uidMapKeySuffix = "uid_map"

	// uidCounterKeySuffix offset 自增计数器 key 后缀（保持全局单 key：
	// 仅新用户首次分配 offset 时 INCR 一次，写频可忽略；分桶反而破坏 offset 唯一性）
	uidCounterKeySuffix = "uid_counter"

	// allUsersKeySuffix 全体在线用户 ZSET key 后缀（分桶）
	// 完整 key: <keyPrefix> + allUsersKeySuffix + ":" + <bucket>
	allUsersKeySuffix = "all_users"

	// typesKeySuffix userType 登记集合 key 后缀（不分桶：userType 种类有限，
	// CleanupExpired 据此 SMEMBERS 枚举所有 type ZSET）
	typesKeySuffix = "types"

	// keyBucketMask 分桶位掩码（DefaultKeyBucketCount-1，hash 位与取桶）
	keyBucketMask = constants.DefaultKeyBucketCount - 1

	// ============================================================================
	// Bitmap 配置环境变量名 - 短期方案(避免立即升级 go-config 模块)
	//
	// 长期方案:在 go-config 的 wscconfig.OnlineStatus 结构体正式声明字段,
	// 短期通过环境变量覆盖默认值,未设置时用 constants.DefaultXxx 常量
	// ============================================================================

	// envBitmapTTL bitmap EXPIRE(字符串,如 "8s"),默认 HeartbeatRefreshInterval × 4
	envBitmapTTL = "WSC_ONLINE_BITMAP_TTL"

	// envMaxBitmapOffset offset 上限(防恶意膨胀,0=不限制)
	envMaxBitmapOffset = "WSC_ONLINE_MAX_BITMAP_OFFSET"

	// envMaxCachedUIDs 本地 L1 缓存容量上限,超限触发清半驱逐(每 shard 随机一半)
	envMaxCachedUIDs = "WSC_ONLINE_MAX_CACHED_UIDS"
)

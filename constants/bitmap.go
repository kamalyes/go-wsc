/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\constants\bitmap.go
 * @Description: Bitmap 分层常量（用户在线状态快速判否层）
 *
 * L0 Bitmap 判否层(SETBIT/GETBIT) + L1 uid→offset 映射 + L2 ZSET 详情层
 * GlobalBitmapNS 为全局广播 bitmap 的 ns 段，与 DefaultNamespace 区分
 *
 * 热点 key 分桶：uid_map/all_users/type 按 hash(userID)%256 分桶，
 * 消除亿级用户下单 key 写热点（详见 repository 的 keyBucket）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

const (
	// GlobalBitmapNS 全局广播 bitmap 的 ns 段
	// 路由信封 ns="" 表示全局广播，bitmap 用专用 ns 段(__global__)避免与 DefaultNamespace(__default_ns__) 混淆
	// Lua 脚本(luaBatchSetClientsOnline/Offline)硬编码 "__global__" 须与本常量同值，修改时同步
	GlobalBitmapNS = "__global__"

	// DefaultMaxBitmapOffset offset 默认上限（1000 万，bitmap 约 1.25MB）
	DefaultMaxBitmapOffset = 10_000_000

	// DefaultMaxCachedUIDs 本地 offset 缓存默认容量上限
	DefaultMaxCachedUIDs = 2_000_000

	// DefaultBitmapTTLSeconds bitmap EXPIRE 兜底秒数（TTL 未配置时使用；
	// 正常路径 bitmapTTL 对齐 client TTL，见 repository.loadBitmapTTL）
	DefaultBitmapTTLSeconds = 60

	// DefaultKeyBucketCount 热点 key 分桶数（2 的幂，按 hash(userID) 位与取桶）
	// uid_map/all_users/type 三类 key 按此分桶：亿级用户下单 key 写频从数百万 ops/s
	// 降至数千 ops/s，Redis Cluster 下桶天然散落到不同 slot
	DefaultKeyBucketCount = 256
)

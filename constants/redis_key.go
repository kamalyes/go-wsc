/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-23 00:00:00
 * @FilePath: \go-wsc\constants\redis_key.go
 * @Description: Redis key 前缀默认值（跨模块共享）
 *
 * 统一管理各模块 Redis key 默认前缀，避免散落在 repository 各文件。
 * repository 层引用 constants.DefaultXxxKeyPrefix 构造 key，调用方可覆盖。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

// Redis Key 前缀默认值（各模块默认前缀，调用方可覆盖）
const (
	// DefaultOnlineKeyPrefix 在线状态默认 key 前缀
	DefaultOnlineKeyPrefix = "wsc:online:"

	// DefaultWorkloadKeyPrefix 负载管理默认 key 前缀
	DefaultWorkloadKeyPrefix = "wsc:workload:"

	// DefaultQueueKeyPrefix 消息队列默认 key 前缀
	DefaultQueueKeyPrefix = "wsc:queue:"

	// DefaultStatsKeyPrefix 统计信息默认 key 前缀
	DefaultStatsKeyPrefix = "wsc:stats:"

	// DefaultGroupKeyPrefix 群组默认 key 前缀
	DefaultGroupKeyPrefix = "wsc:group:"
)

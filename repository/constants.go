/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-31 09:08:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-31 11:08:55
 * @FilePath: \go-wsc\repository\constants.go
 * @Description: Repository 层常量定义 - 统一管理 Redis key 前缀和字段名
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package repository

const (
	// ============================================================================
	// Redis Key 前缀常量 - 各模块默认前缀
	// ============================================================================

	// DefaultOnlineKeyPrefix 在线状态默认 key 前缀
	DefaultOnlineKeyPrefix = "wsc:online:"

	// DefaultWorkloadKeyPrefix 负载管理默认 key 前缀
	DefaultWorkloadKeyPrefix = "wsc:workload:"

	// DefaultQueueKeyPrefix 消息队列默认 key 前缀
	DefaultQueueKeyPrefix = "wsc:queue:"

	// DefaultStatsKeyPrefix 统计信息默认 key 前缀
	DefaultStatsKeyPrefix = "wsc:stats:"

	// ============================================================================
	// Redis Hash 字段名常量 - 统计信息
	// ============================================================================

	// StatsFieldTotalConnections 总连接数字段
	StatsFieldTotalConnections = "total_connections"

	// StatsFieldActiveConnections 活跃连接数字段
	StatsFieldActiveConnections = "active_connections"

	// StatsFieldMessagesSent 已发送消息数字段
	StatsFieldMessagesSent = "messages_sent"

	// StatsFieldMessagesReceived 已接收消息数字段
	StatsFieldMessagesReceived = "messages_received"

	// StatsFieldBroadcastsSent 已发送广播数字段
	StatsFieldBroadcastsSent = "broadcasts_sent"

	// StatsFieldStartTime 启动时间字段
	StatsFieldStartTime = "start_time"
)

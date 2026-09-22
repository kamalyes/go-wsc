/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 16:02:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 16:02:00
 * @FilePath: \go-wsc\models\send_result.go
 * @Description: 发送结果类型
 *
 * P2P 重试发送（SendResult/SendAttempt）与群组成员广播（BroadcastResult）
 * 的结果结构。统一投递结果见 deliver_result.go 的 DeliverResult。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import "time"

// SendAttempt 发送尝试记录
type SendAttempt struct {
	AttemptNumber int
	StartTime     time.Time
	Duration      time.Duration
	Error         error
	Success       bool
}

// SendResult 发送结果
type SendResult struct {
	Success       bool
	Attempts      []SendAttempt
	TotalRetries  int
	TotalDuration time.Duration
	FinalError    error
	DeliveredAt   time.Time
	StoredOffline bool // 消息因用户离线而存储到离线队列（SendToGroup 据此分类在线/离线，避免预检查 N 次 Redis）
}

// BroadcastResult 广播发送结果
type BroadcastResult struct {
	Total      int              // 总用户数量
	Success    int              // 成功发送数量
	Offline    int              // 离线用户数量
	Failed     int              // 发送失败数量
	Errors     map[string]error // 错误详情 map[userID]error
	OfflineIDs []string         // 离线用户ID列表
	FailedIDs  []string         // 发送失败的用户ID列表
}

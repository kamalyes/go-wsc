/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-07 21:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 21:57:19
 * @FilePath: \go-wsc\overload\delivery_fallback.go
 * @Description: 投递兜底路由 —— 拒绝≠丢弃（P0 分级送达基座）
 *
 * TrySend 返回 false（SendChan 满/客户端关闭）时按消息分级路由：
 *   - 必达级（Guaranteed）：立即转离线补发（ACK 超时链路仍作双保险——时间轮到期后
 *     ClaimStaleSending 转离线，此处提前到"入队失败即转"，不等 5min 兜底）
 *   - 普通级（Standard）：立即转离线（用户上线时推送——修复"广播丢弃彻底丢失"）
 *   - 高频级（Ephemeral）：丢弃计数（latest-wins 语义：下一条同 key 消息自然覆盖，
 *     旧值过期是正确行为而非丢失）
 *
 * 广播路径的成员级兜底（broadcastToFiltered/broadcastToUserIDs 的 TrySend false 分支）
 * 复用同一路由：消息 Receiver 为空时补写目标用户再转离线，修复广播离线丢失缺陷
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package overload

import (
	"errors"
)

type FallbackAction int

const (
	// FallbackNone 无需兜底（不应出现：调用方误用）
	FallbackNone FallbackAction = iota
	// FallbackOffline 转离线补发（必达/普通级）
	FallbackOffline
	// FallbackEphemeralDrop 高频级丢弃（latest-wins 语义正确行为）
	FallbackEphemeralDrop
)

// errDeliveryFallback 兜底转存的触发错误（语义标记，供状态更新/日志使用）
var ErrDeliveryFallback = errors.New("delivery fallback: send channel full")

// routeDeliveryFallback 投递失败的分级兜底路由（内部统一入口）
//
// client/userID 二选一提供（P2P 有 client；广播扇出有 client + 目标 userID）
// 返回实际执行的动作（观测埋点用）
//
// 性能：仅在 TrySend 返回 false 后执行（热路径成功路径零开销）
// 实现：补写 Receiver（广播消息原 Receiver 为空）后复用 tryStoreOfflineOnDeliveryFailure
// 的异步转存（离线推送不阻塞扇出 goroutine）

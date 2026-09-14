/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-07 21:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 21:57:19
 * @FilePath: \go-wsc\hub\delivery_fallback.go
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
package hub

import (
	"errors"

	"github.com/kamalyes/go-wsc/models"
)

// FallbackAction 兜底路由动作（观测/测试断言用）
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
var errDeliveryFallback = errors.New("delivery fallback: send channel full")

// routeDeliveryFallback 投递失败的分级兜底路由（内部统一入口）
//
// client/userID 二选一提供（P2P 有 client；广播扇出有 client + 目标 userID）
// 返回实际执行的动作（观测埋点用）
//
// 性能：仅在 TrySend 返回 false 后执行（热路径成功路径零开销）
// 实现：补写 Receiver（广播消息原 Receiver 为空）后复用 tryStoreOfflineOnDeliveryFailure
// 的异步转存（离线推送不阻塞扇出 goroutine）
func (h *Hub) routeDeliveryFallback(msg *models.HubMessage, client *Client, targetUserID string) FallbackAction {
	if msg == nil {
		return FallbackNone
	}

	guarantee := msg.ResolveGuarantee()

	// 高频级：丢弃计数（语义正确——同 key 下一条自然覆盖）
	if guarantee == models.GuaranteeEphemeral {
		h.overloadMetrics.recordEphemeralDrop()
		return FallbackEphemeralDrop
	}

	// 必达/普通级：立即转离线补发（送达保证）
	userID := targetUserID
	if userID == "" && client != nil {
		userID = client.UserID
	}
	if userID == "" {
		// 无接收者信息（防御性）：消息本身无 Receiver 也无 client —— 无法转离线
		h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "投递兜底失败：消息无接收者信息",
			"message_id", msg.MessageID,
			"guarantee", guarantee,
		)
		h.overloadMetrics.recordUnrecoverable(guarantee)
		return FallbackNone
	}

	if h.offlineMessageHandler == nil {
		// 未配置离线处理器：记录后返回（无法兜底，仅计数）
		h.overloadMetrics.recordUnrecoverable(guarantee)
		h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "投递兜底失败：未配置离线消息处理器",
			"message_id", msg.MessageID,
			"user_id", userID,
			"guarantee", guarantee,
		)
		return FallbackNone
	}

	// 广播消息 Receiver 为空：Clone 补写（P2P 已有 Receiver 直接用原 msg）
	offlineMsg := msg
	if offlineMsg.Receiver == "" {
		offlineMsg = msg.Clone()
		offlineMsg.Receiver = userID
	}

	h.tryStoreOfflineOnDeliveryFailure(offlineMsg, errDeliveryFallback)
	h.overloadMetrics.recordOfflineFallback(guarantee)
	return FallbackOffline
}

// TrySendWithFallback 带分级兜底的非阻塞投递（广播扇出内部循环用）
//
// 性能契约：成功路径 = TrySend 一次（零新增指令）；失败路径才走兜底路由
// 返回：是否实时送达（false = 已转离线/按高频语义处理，消息不会静默丢失）
func (h *Hub) TrySendWithFallback(client *Client, data []byte, msg *models.HubMessage) bool {
	if client == nil {
		return false
	}
	if client.TrySend(data) {
		// 📊 送达漏斗埋点：实时送达 + 在途量出队（与 sendToClientSerialized 对齐，守恒不变量）
		if msg != nil {
			h.overloadMetrics.recordRealtime(msg.ResolveGuarantee())
		}
		h.admissionOnDelivered()
		return true
	}
	h.routeDeliveryFallback(msg, client, "")
	return false
}

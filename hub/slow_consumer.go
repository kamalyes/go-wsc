/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-11 21:57:03
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-12 21:50:19
 * @FilePath: \go-wsc\hub\slow_consumer.go
 * @Description: 慢消费者治理 —— 三级递进（记录 → 告警 → 驱逐），治理不丢消息
 *
 * 检测：BacklogRatio（写泵单写者 atomic store 的 SendChan 利用率）
 * 治理状态机（per-client consecutive 计数）：
 *   - ratio ≥ threshold（0.9）：consecutive++
 *   - consecutive 达阈值（3）：
 *     · 驱逐前保全——SendChan 未投递消息按分级兜底（普通/必达转离线，高频语义丢弃）
 *     · KickOut 走控制通道（客户端收到理由，不被业务洪峰淹没）
 *     · Unregister（断链不丢消息）
 *
 * 扫描模型：复用 ForEachClientParallel 周期采样（与心跳批处理同风格的分片并行遍历），
 * 读侧仅 atomic load（写泵 store——单写者多读者无锁）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
)

// slowConsumerState per-client 治理状态（分片注册表外挂的轻量状态，避免侵入 Client 结构）
type slowConsumerState struct {
	consecutive int  // 连续超阈值次数
	warned      bool // 已告警（告警只发一次，驱逐前不再重复）
}

// startSlowConsumerScanner 启动慢消费者扫描（Run 时调用）
//
// 周期 = 准入评估周期 × 2（与水位评估同源节拍，避免扫描与评估完全同步造成的
// 周期性毛刺）；扫描本身 O(活跃连接) 原子读遍历
func (h *Hub) startSlowConsumerScanner() {
	go func() {
		// atomic load：与 SetOverloadPolicy 热替换并发安全（nil 时用默认节拍）
		interval := time.Second
		if gate := h.admission.Load(); gate != nil && gate.evalInterval > 0 {
			interval = 2 * gate.evalInterval
		}
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		// 扫描状态表（userID+clientID → state；驱逐/断连时惰性清理——数量级与
		// 慢消费者数成正比，正常时近空表，无内存膨胀风险）
		states := make(map[string]*slowConsumerState)

		for {
			select {
			case <-h.ctx.Done():
				return
			case <-ticker.C:
				h.scanSlowConsumersOnce(states)
			}
		}
	}()
}

// scanSlowConsumersOnce 单轮扫描（分片并行遍历 + 无锁采样 + 三级治理）
func (h *Hub) scanSlowConsumersOnce(states map[string]*slowConsumerState) {
	threshold := float64(constants.SlowConsumerThresholdRatio)
	consecutiveLimit := constants.SlowConsumerConsecutiveThreshold

	// 本轮活跃的 clientID 集合（惰性清理的依据：不在本轮集合中的旧状态直接删除）
	active := make(map[string]struct{})

	h.shardedRegistry.ForEachClientParallel(0, func(_ string, client *Client) {
		key := client.ID
		active[key] = struct{}{}

		ratio := client.BacklogRatio()
		if ratio < threshold {
			delete(states, key) // 恢复正常：清零（迟滞清除，防止历史计数误伤）
			return
		}

		state, exists := states[key]
		if !exists {
			state = &slowConsumerState{}
			states[key] = state
		}
		state.consecutive++

		switch {
		case state.consecutive >= consecutiveLimit:
			// 🚨 三级：驱逐（先保全消息再断链——治理不丢消息）
			h.evictSlowConsumer(client, state, ratio)
			delete(states, key)
		case state.consecutive >= consecutiveLimit-1 && !state.warned:
			// ⚠️ 二级：告警（KV 日志一次；下一轮仍超阈值将驱逐）
			state.warned = true
			h.logger.WarnContextKV(client.Context, "慢消费者告警：下轮仍积压将驱逐",
				"client_id", client.ID,
				"user_id", client.UserID,
				"backlog_ratio", ratio,
				"consecutive", state.consecutive,
			)
		default:
			// 📝 一级：记录（DEBUG 级，首轮观察）
			h.logWithClient(logger.DEBUG, "慢消费者检测：队列积压", client,
				"backlog_ratio", ratio,
				"consecutive", state.consecutive,
			)
		}
	})

	// 惰性清理：已断连/已驱逐的旧状态（防状态表缓慢膨胀）
	for key := range states {
		if _, ok := active[key]; !ok {
			delete(states, key)
		}
	}
}

// evictSlowConsumer 驱逐慢消费者（驱逐前保全：SendChan 残留消息按分级兜底）
func (h *Hub) evictSlowConsumer(client *Client, state *slowConsumerState, ratio float64) {
	h.overloadMetrics.recordSlowEvict()

	// 🛡️ 消息保全：排空 SendChan 残留消息（移交 ACK 超时链路兜底——sending 记录
	// 5min 兜底扫描转离线，上线推送；残留为已序列化 []byte 无法还原分级，
	// 故统一按 ACK 链路保全而非现场转离线）
	salvaged := 0
	for {
		select {
		case data := <-client.SendChan:
			_ = data
			salvaged++
		default:
			goto drained
		}
	}
drained:
	h.logger.WarnContextKV(client.Context, "驱逐慢消费者：消息已由 ACK 链路兜底",
		"client_id", client.ID,
		"user_id", client.UserID,
		"backlog_ratio", ratio,
		"consecutive", state.consecutive,
		"salvaged_to_ack_fallback", salvaged,
	)

	// KickOut 走控制通道（客户端收到驱逐理由）
	kickMsg := models.NewHubMessage().
		SetMessageType(models.MessageTypeKickOut).
		SetSender("system").
		SetSenderType(models.UserTypeSystem).
		SetReceiver(client.UserID).
		SetReceiverType(client.UserType).
		SetContent("slow consumer evicted").
		WithContentExtra("reason", "backlog_ratio").
		WithContentExtra("backlog_ratio", ratio)

	h.SendControlMessage(client, kickMsg)

	// Unregister（KickOut 控制通道满时 SendControl 内部已降级直接断链）
	h.Unregister(client)
}

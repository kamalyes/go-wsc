/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-17 15:36:16
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-05-17 15:36:16
 * @FilePath: \go-wsc\messaging\client_delivery.go
 * @Description: 客户端直投与高级发送入口 —— 预序列化投递核心 / 分级投递接线 / 多端同步
 *
 * 拆分自 send.go：sendToClientCore 是所有本地客户端投递的漏斗（WS/SSE/Coalescer/
 * 准入闸门/状态回报），高级发送入口（回调/优先级/条件）与多端同步围绕该漏斗组合；
 * 与 P2P 路由链（send.go）分文件，投递语义与路由决策各归其位
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
)

// ============================================================================
// 高级发送入口
// ============================================================================

// SendWithCallback 发送消息并在完成时执行回调
func (m *Manager) SendWithCallback(ctx context.Context, userID string, msg *models.HubMessage,
	onSuccess func(*models.SendResult), onError func(error)) {

	syncx.Go().
		OnPanic(func(r interface{}) {
			m.host.GetLogger().ErrorContextKV(ctx, "SendWithCallback panic",
				"user_id", userID,
				"message_id", msg.MessageID,
				"panic", r,
				"stack", string(debug.Stack()),
			)
		}).
		Exec(func() {
			result := m.SendToUserWithRetry(ctx, userID, msg)
			if result.Success && onSuccess != nil {
				onSuccess(result)
			} else if !result.Success && onError != nil {
				onError(result.FinalError)
			}
		})
}

// SendPriority 根据优先级发送消息
func (m *Manager) SendPriority(ctx context.Context, userID string, msg *models.HubMessage, priority models.Priority) {
	msg.Priority = priority

	// 高优先级消息直接发送，不使用队列
	if priority >= models.PriorityHigh {
		syncx.Go(ctx).Exec(func() {
			m.SendToUserWithRetry(ctx, userID, msg)
		})
		return
	}

	// 普通优先级使用标准流程
	m.SendToUserWithRetry(ctx, userID, msg)
}

// SendConditional 根据条件发送消息给符合条件的客户端
// 复用 BroadcastToFiltered：预序列化一次 + 直接 TrySend，避免逐客户端 Clone/序列化/入队/DB 记录
func (m *Manager) SendConditional(ctx context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int {
	return m.BroadcastToFiltered(ctx, condition, msg)
}

// SendToAllClientsInMap 发送消息到映射中的所有客户端
// 预序列化一次消息，避免对每个客户端重复 json.Marshal
// trace 恢复：ctx 无 trace 时从消息信封恢复（msg.ContextFrom），保证投递日志
// 与消息原始链路同一 trace_id（此前的 host.Context() 是 trace 断点）
func (m *Manager) SendToAllClientsInMap(ctx context.Context, clientMap map[string]*models.Client, msg *models.HubMessage) {
	// 复制客户端列表,避免在遍历时map被修改导致竞争
	clients := connection.CopyClientsFromMap(clientMap)
	if len(clients) == 0 {
		return
	}
	ctx = msg.ContextFrom(ctx)

	// 预序列化一次（WebSocket 客户端共用；SSE 走 TrySendSSE(msg) 不用 []byte）
	// 序列化失败时 preSerialized=nil，由 SendToClientSerialized 内部兜底
	preSerialized, _ := json.Marshal(msg)

	// 遍历复制后的列表发送消息
	for _, client := range clients {
		m.SendToClientSerialized(ctx, client, msg, preSerialized)
	}
}

// ============================================================================
// 客户端直投漏斗
// ============================================================================

// SendToClient 发送消息到客户端（内部序列化）
func (m *Manager) SendToClient(ctx context.Context, client *models.Client, msg *models.HubMessage) {
	m.SendToClientSerialized(ctx, client, msg, nil)
}

// SendToClientSerialized 发送消息到客户端（支持预序列化数据）
// preSerialized 为预序列化的 []byte，为 nil 时内部序列化
// SSE 客户端忽略 preSerialized，直接发送 msg 对象
// 返回是否成功投递到客户端通道（跨节点 PubSub 路径据此统计投递成败）
//
// 分级投递接线：
//   - 高频级（Ephemeral）→ Coalescer latest-wins 合并（50ms 周期投递最新值）
//   - Admit 拒绝（Delay/Offline，P2P 场景仅极端水位触发）→ 转离线补发 + 上层重试兜底
//   - 实时路径零改动（TrySend 成功路径）
func (m *Manager) SendToClientSerialized(ctx context.Context, client *models.Client, msg *models.HubMessage, preSerialized []byte) bool {
	ok := m.TryDeliverToClient(ctx, client, msg, preSerialized)
	if !ok && msg != nil {
		m.StoreOfflineOnDeliveryFailure(msg, fmt.Errorf("client delivery rejected"))
	}
	return ok
}

// TryDeliverToClient 返回实际准入或入队结果，离线兜底由汇总投递结果的调用方负责
func (m *Manager) TryDeliverToClient(ctx context.Context, client *models.Client, msg *models.HubMessage, data []byte) bool {
	return m.sendToClientCore(ctx, client, msg, data, true)
}

// sendToClientCore 投递核心体（带状态回报开关）
// reportStatus=false 跳过状态回报与离线转存——多端同步/观察者通知等"旁路投递"专用：
// 消息状态归属真正接收者（msg.Receiver），发送者其他设备的投递成败不得污染接收者的
// message_record（成功误标 Success=接收者尚未收到即"已送达"；失败误转离线=接收者
// 明明在线却在上线后收到重复推送）；负载指标（Admitted/Realtime/OnDelivered）照常
// 记录——旁路投递同样占用节点投递资源，漏斗口径不缺位
func (m *Manager) sendToClientCore(ctx context.Context, client *models.Client, msg *models.HubMessage, preSerialized []byte, reportStatus bool) bool {
	// 检查客户端是否已关闭
	if client.IsClosed() {
		return false
	}

	// 如果 MessageID 为空，使用 HubID
	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	// 状态更新按 (msgID, receiver) 定位：P2P 带 receiver、广播为空，各得其所
	receiver := msg.Receiver
	// 送达分级解析一次复用：ResolveGuarantee 含读锁 + 决策树（含 Data map 断言）
	// 本函数内 msg 为上游 Clone 的私有副本、分级字段不变，coalescer 判断与送达埋点共用同一结果，消除热路径每消息第二次无谓解析
	guarantee := msg.ResolveGuarantee()

	// 高频级：latest-wins 合并（同用户同类型只保最新；50ms drain 周期投递）
	if coalescer := m.host.GetEphemeralCoalescer(); coalescer != nil &&
		guarantee == models.GuaranteeEphemeral &&
		client.ConnectionType != models.ConnectionTypeSSE {
		m.host.GetOverloadMetrics().RecordAdmitted(models.GuaranteeEphemeral)
		if accepted, merged := coalescer.Offer(overload.EphemeralKey{UserID: client.UserID, MessageType: msg.MessageType}, msg); accepted {
			if merged {
				// 覆盖同 key 旧消息：latest-wins 合并削峰埋点（漏斗 merged 轨道）
				m.host.GetOverloadMetrics().RecordEphemeralMerged()
			}
			return true // 已并入最新值（将投递）
		}
		// 合并器容量满：语义丢弃（latest-wins 尽头的容量保护）
		m.host.GetOverloadMetrics().RecordEphemeralDrop()
		if reportStatus {
			m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, "coalescer full")
		}
		return false
	}

	// 准入闸门（P2P 场景：L3/L4 极端水位才拒绝；必达级恒放行）
	if verdict := m.host.AdmitMessage(msg, false); verdict != overload.VerdictAdmit {
		// 水位读取（GetAdmissionLevel 内含闸门 nil 防御，热替换窗口安全）
		level := m.host.GetAdmissionLevel()
		rejectErr := fmt.Errorf("admission rejected at %s", level)
		if reportStatus {
			m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, rejectErr.Error())
			// 离线兜底由持有整体投递结果的调用方处理
		}
		return false
	}

	// SSE 客户端使用专用的消息通道
	if client.ConnectionType == models.ConnectionTypeSSE {
		if client.TrySendSSE(msg) {
			client.SetLastSeen(time.Now())
			// 逐消息成功路径走 DEBUG（千万连接规模下逐消息 INFO 是吞吐反模式）
			m.host.GetLogger().DebugContextKV(ctx, "消息已投递到本地客户端",
				"message_id", msgID,
				"user_id", client.UserID,
				"client_id", client.ID,
				"connection_type", "sse",
				"node_id", m.host.GetNodeID(),
			)
			// SSE消息成功发送，更新为成功状态
			if reportStatus {
				m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusSuccess, "", "")
			}
			return true
		}
		sseErr := fmt.Errorf("SSE channel full or closed")
		m.host.GetLogger().WarnContextKV(ctx, "SSE客户端消息通道已满或已关闭", "client_id", client.ID, "user_id", client.UserID)
		if reportStatus {
			// SSE通道已满或已关闭，更新为失败状态
			m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, sseErr.Error())
			// 此处只报告投递失败，调用方负责离线兜底
		}
		return false
	}

	// WebSocket 客户端：使用预序列化数据或现场序列化
	var data []byte
	if preSerialized != nil {
		data = preSerialized
	} else {
		var err error
		data, err = json.Marshal(msg)
		if err != nil {
			m.host.GetLogger().ErrorContextKV(ctx, "消息序列化失败", "error", err)
			if reportStatus {
				// 更新为失败状态
				m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonUnknown, err.Error())
			}
			// 序列化失败无法转存离线（msg 无法被存储），只标记 Failed
			return false
		}
	}

	if client.TrySend(data) {
		// 消息成功发送到客户端通道，更新为成功状态
		if reportStatus {
			m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusSuccess, "", "")
		}
		// 送达漏斗埋点：实时送达 + 在途量出队
		m.host.GetOverloadMetrics().RecordRealtime(guarantee)
		m.host.AdmissionOnDelivered()

		// 链路闭环日志：与跨 Pod 路径（distributed.go "[跨Pod] 消息已投递到本地客户端"）统一，
		// trace_id 可从 NotifySend 入口一路串到本节点最终投递（ctx 由 msg.ContextFrom 恢复携带 trace）
		// 逐消息成功路径走 DEBUG（千万连接规模下逐消息 INFO 是吞吐反模式）
		m.host.GetLogger().DebugContextKV(ctx, "消息已投递到本地客户端",
			"message_id", msgID,
			"user_id", client.UserID,
			"client_id", client.ID,
			"node_id", m.host.GetNodeID(),
		)

		// 更新接收者的消息统计和字节统计
		m.host.TrackReceiverMessageStats(client.ID, client.UserType, len(data))
		return true
	}

	queueErr := fmt.Errorf("client send channel full or closed")
	m.host.GetLogger().WarnContextKV(ctx, "客户端发送通道已满或已关闭", "client_id", client.ID)
	if reportStatus {
		// 发送通道已满或已关闭，更新为失败状态
		m.updateMessageStatusAsync(ctx, msgID, receiver, models.MessageSendStatusFailed, models.FailureReasonQueueFull, queueErr.Error())
		// 此处只报告投递失败，调用方负责离线兜底
	}
	return false
}

// ============================================================================
// 多端同步
// ============================================================================

// syncToSenderDevices 同步消息给发送者的其他设备（多端同步）
// 场景：用户A在设备B、C、D登录，设备B发送消息给用户F，设备C和D应该收到此消息
//
// 性能：使用 ForEachUserClient 零拷贝遍历 + 内联过滤，
// 替代旧版 GetClientsCopyForUser（拷贝1）+ FilterSlice（拷贝2）双重拷贝
//
// data：调用方 handleDirectMessage 首次预序列化的消息字节，多端同步直接复用
// 消除同一 msg 的二次 json.Marshal，两次序列化之间 msg 未被修改，字节等价
// data 为 nil 时兜底重新序列化，即调用方序列化失败或测试直调未预序列化场景
func (m *Manager) syncToSenderDevices(ctx context.Context, msg *models.HubMessage, data []byte) {
	if msg.Sender == "" {
		return
	}

	// 零拷贝遍历：一次遍历完成计数+收集其他设备，避免中间切片拷贝
	// 过滤：只收集路由匹配（namespace）的设备，且排除当前发送端
	// （发送者多端同步不检查 group，同一 user 在不同 group 的设备都应该同步到）
	var otherDevices []*models.Client
	deviceCount := 0
	m.host.GetShardedRegistry().ForEachUserClientFiltered(msg.Sender, msg.AppID, msg.Namespace, nil, func(_ string, client *models.Client) bool {
		deviceCount++
		if client.ID != msg.SenderClient {
			otherDevices = append(otherDevices, client)
		}
		return true
	})

	// 只有发送者自己一个设备（或无设备），无需同步
	if deviceCount <= 1 || len(otherDevices) == 0 {
		return
	}

	// 复用调用方（handleDirectMessage）首次预序列化的字节，避免同一 msg 二次 Marshal
	// （多端同步场景发送方与接收方共用一套序列化结果，msg 未被修改，字节等价）
	if data == nil {
		var err error
		data, err = json.Marshal(msg)
		if err != nil {
			m.host.GetLogger().ErrorContextKV(ctx, "多端同步消息序列化失败", "error", err, "message_id", msg.MessageID)
			return
		}
	}

	m.host.GetLogger().DebugContextKV(ctx, "多端同步消息给发送者的其他设备",
		"sender", msg.Sender,
		"sender_client", msg.SenderClient,
		"other_devices_count", len(otherDevices),
		"message_id", msg.MessageID,
	)

	// 发送给发送者的其他设备（旁路投递：不走状态回报与离线转存）
	// 状态归属真正接收者（msg.Receiver）：其他设备的投递成败若写回状态，
	// 会把接收者的 message_record 误标 Success（其尚未收到）或误转离线
	// （其在线却在上线后收到重复推送），见 sendToClientCore 的 reportStatus 语义
	for _, device := range otherDevices {
		m.sendToClientCore(ctx, device, msg, data, false)
	}
}

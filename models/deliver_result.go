/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 00:00:00
 * @FilePath: \go-wsc\models\deliver_result.go
 * @Description: 统一投递结果类型 — 覆盖 P2P/群组/命名空间/全局广播所有场景
 *
 * 由 Hub.Deliver(ctx, msg, excludeSender) 统一入口返回，替代历史 GroupSendResult /
 * SendResult / BroadcastResult 三套割裂的返回类型。调用方按 result.Mode + 计数字段判断结果。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

// DeliveryMode 投递模式（Deliver 内部根据 ctx+msg 路由决策）
type DeliveryMode int

const (
	DeliveryModeP2P            DeliveryMode = iota // 点对点（msg.Receiver 非空）
	DeliveryModeGroupReliable                      // 群组可靠投递（RequireAck=true，per-member SendToUserWithRetry + 离线存储）
	DeliveryModeGroupBroadcast                     // 群组广播（RequireAck=false，fire-and-forget）
	DeliveryModeNamespace                          // 命名空间广播（namespace 非空，无 groupIDs）
	DeliveryModeGlobal                             // 全局广播（namespace 为空，无 groupIDs）
)

// String 返回投递模式的可读名称（日志/调试用）
func (m DeliveryMode) String() string {
	switch m {
	case DeliveryModeP2P:
		return "p2p"
	case DeliveryModeGroupReliable:
		return "group_reliable"
	case DeliveryModeGroupBroadcast:
		return "group_broadcast"
	case DeliveryModeNamespace:
		return "namespace"
	case DeliveryModeGlobal:
		return "global"
	default:
		return "unknown"
	}
}

// DeliverResult 统一投递结果（覆盖 P2P/群组/广播所有场景）
//
// 设计原则：
//   - 始终非 nil：错误收集到 Errors，调用方按 Mode + 计数字段判断结果
//   - 字段语义按 Mode 分组：
//     P2P / GroupReliable：TotalMembers/OnlineMembers/OfflineMembers/Sent/StoredOffline/Failed 有效
//     GroupBroadcast / Namespace / Global：LocalDelivered 有效（本地成功投递数）
//   - 跨节点投递为异步，不计入返回值（与历史 BroadcastToGroupMembers/BroadcastToNamespace 语义一致）
type DeliverResult struct {
	Mode      DeliveryMode // 投递模式
	AppID     string       // 应用ID（已归一化）
	Namespace string       // 命名空间ID（全局广播时为空）
	GroupIDs  []string     // 群组ID列表（非群组场景为空）

	// P2P / 群组可靠投递场景（per-member 统计）
	TotalMembers   int // 目标成员数（P2P=1，群组=去重后成员数，广播=0）
	OnlineMembers  int // 在线成员数（至少在一个节点有连接）
	OfflineMembers int // 离线成员数
	Sent           int // 在线且成功投递数（含跨节点路由）
	StoredOffline  int // 离线成员存储离线消息数
	Failed         int // 投递失败数（在线但队列满等）

	// 广播场景（fire-and-forget 本地成功投递数）
	// 群组广播/命名空间广播/全局广播的本地送达数；全局广播为异步，此值为 0
	LocalDelivered int

	Errors []error // 投递过程中的错误
}

// AddError 安全追加错误（避免 nil 追加）
func (r *DeliverResult) AddError(err error) {
	if err == nil {
		return
	}
	r.Errors = append(r.Errors, err)
}

// HasError 是否有错误
func (r *DeliverResult) HasError() bool {
	return len(r.Errors) > 0
}

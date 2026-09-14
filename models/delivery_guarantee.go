/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-06 20:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-06 20:00:00
 * @FilePath: \go-wsc\models\delivery_guarantee.go
 * @Description: 消息送达分级模型 —— 分级送达保证的地基
 *
 * 三级送达保证（DeliveryGuarantee）：
 *   - GuaranteeGuaranteed 必达：控制/资金/安全类消息，控制通道直发 + ACK + 离线兜底，任何情况可达
 *   - GuaranteeStandard    普通：聊天/通知类消息，实时投递，队列满/过载时转离线补发（拒绝≠丢弃）
 *   - GuaranteeEphemeral   高频流式：输入状态/已读/实时状态类消息，latest-wins 合并，只保证最新送达
 *
 * 决策树（ResolveGuarantee）：显式设置(WithGuarantee) > 消息类型默认表 > Classification 评分(≥90 必达)
 * > PriorityCritical 提级 > 默认普通
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

// DeliveryGuarantee 送达分级
//
// int8 底层：仅 4 个枚举值，1 字节存储塞入结构体末尾标志区（与 bool 同区），
// 零 padding 净增长（结构体大小守护测试 432 字节门限）
//
// 零值为 GuaranteeUnset：未显设置时走 ResolveGuarantee 决策树（类型默认表/分类评分），
// 保证零值构造的消息（&HubMessage{}）也能得到正确的分级，无需强制初始化
type DeliveryGuarantee int8

const (
	// GuaranteeUnset 未设置（零值，走决策树推导）
	GuaranteeUnset DeliveryGuarantee = iota
	// GuaranteeStandard 普通：实时投递 + 离线兜底（默认档）
	GuaranteeStandard
	// GuaranteeEphemeral 高频流式：latest-wins 合并，只保证最新送达
	GuaranteeEphemeral
	// GuaranteeGuaranteed 必达：控制通道 + ACK + 离线多级兜底，零丢失
	GuaranteeGuaranteed
)

// String 送达分级的日志/序列化友好表示
func (g DeliveryGuarantee) String() string {
	switch g {
	case GuaranteeGuaranteed:
		return "guaranteed"
	case GuaranteeEphemeral:
		return "ephemeral"
	case GuaranteeStandard:
		return "standard"
	default:
		return "unset"
	}
}

// ClassificationGuaranteedScore 分类评分必达阈值
// GetFinalPriority() 评分 ≥ 90（IsCriticalMessage 判定）的消息提升为必达级
const ClassificationGuaranteedScore = 90

// guaranteeTypeDefaults 消息类型默认分级表（单一事实源，勿在别处散落判定）
//
// 必达（控制面 + 资金/安全）：连接生命周期控制与资金安全消息，丢了直接损害业务正确性
// 高频流式（可合并/可覆盖）：状态信号类消息，语义上只需最新值到达
// 其余（含 text/image/file 等业务消息）：普通
var guaranteeTypeDefaults = map[MessageType]DeliveryGuarantee{
	// 必达：连接控制类
	MessageTypeKickOut:            GuaranteeGuaranteed, // 被踢通知（丢=客户端不知为何断连）
	MessageTypeForceOffline:       GuaranteeGuaranteed, // 强制下线（异地登录）
	MessageTypeTerminate:          GuaranteeGuaranteed, // 结束消息
	MessageTypeConnectionRejected: GuaranteeGuaranteed, // 连接拒绝（丢=客户端不知连接失败原因）
	MessageTypeConnectionError:    GuaranteeGuaranteed, // 连接错误
	MessageTypeConnectionTimeout:  GuaranteeGuaranteed, // 连接超时
	MessageTypeAck:                GuaranteeGuaranteed, // ACK 确认（可靠投递闭环的一环）
	MessageTypeRecall:             GuaranteeGuaranteed, // 消息撤回（丢=撤回失效，客户端显示脏数据）
	// 必达：资金/安全类
	MessageTypePayment: GuaranteeGuaranteed,
	MessageTypeOrder:   GuaranteeGuaranteed,
	MessageTypeAlert:   GuaranteeGuaranteed,

	// 高频流式：状态信号（latest-wins）
	MessageTypeTyping:            GuaranteeEphemeral,
	MessageTypeRead:              GuaranteeEphemeral,
	MessageTypeDelivered:         GuaranteeEphemeral,
	MessageTypeReaction:          GuaranteeEphemeral,
	MessageTypeUserStatusChanged: GuaranteeEphemeral,
	MessageTypeHeartbeat:         GuaranteeEphemeral,
}

// GuaranteeForType 查询消息类型的默认送达分级
// 第二返回值表示该类型是否在默认表中（未登记的类型默认走普通档）
func GuaranteeForType(mt MessageType) (DeliveryGuarantee, bool) {
	g, ok := guaranteeTypeDefaults[mt]
	return g, ok
}

// WithGuarantee 链式设置显式送达分级（优先级最高，覆盖决策树推导）
func (m *HubMessage) WithGuarantee(g DeliveryGuarantee) *HubMessage {
	defer m.lockWrite()()
	m.DeliveryGuarantee = g
	return m
}

// ResolveGuarantee 解析消息的最终送达分级
//
// 决策树（优先级从高到低）：
//  1. 显式设置（WithGuarantee / 字段赋值，非 Unset）
//  2. 消息类型默认表（guaranteeTypeDefaults）
//  3. Classification 评分 ≥ ClassificationGuaranteedScore → 必达
//     （消费 SendToUserWithClassification 写入 Data["classification"] 的打分，让分类系统从 dead data 变活）
//  4. PriorityCritical → 必达（与现有优先级体系打通）
//  5. 兜底普通档
//
// 并发安全：持读锁一次性完成全部字段读取（不调用其他持锁方法——锁不可重入）
func (m *HubMessage) ResolveGuarantee() DeliveryGuarantee {
	defer m.lockRead()()

	// 1. 显式设置优先
	if m.DeliveryGuarantee != GuaranteeUnset {
		return m.DeliveryGuarantee
	}

	// 2. 类型默认表
	if g, ok := guaranteeTypeDefaults[m.MessageType]; ok {
		return g
	}

	// 3. 分类评分（IsCriticalMessage 判定 ≥ 90）——锁内直接读 Data，不嵌套持锁方法
	if m.Data != nil {
		if classification, ok := m.Data[DataKeyClassification].(*MessageClassification); ok && classification != nil {
			if classification.GetFinalPriority() >= ClassificationGuaranteedScore {
				return GuaranteeGuaranteed
			}
		}
	}

	// 4. 关键优先级提级
	if m.Priority == PriorityCritical {
		return GuaranteeGuaranteed
	}

	// 5. 默认普通
	return GuaranteeStandard
}

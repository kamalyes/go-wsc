/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-06 20:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-06 20:00:00
 * @FilePath: \go-wsc\models\delivery_guarantee_test.go
 * @Description: 送达分级模型测试 —— 决策树优先级与类型默认表
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeliveryGuaranteeString(t *testing.T) {
	assert.Equal(t, "guaranteed", GuaranteeGuaranteed.String())
	assert.Equal(t, "standard", GuaranteeStandard.String())
	assert.Equal(t, "ephemeral", GuaranteeEphemeral.String())
	assert.Equal(t, "unset", GuaranteeUnset.String())
}

func TestGuaranteeForType(t *testing.T) {
	// 必达：控制类
	for _, mt := range []MessageType{MessageTypeKickOut, MessageTypeForceOffline, MessageTypeAck, MessageTypeTerminate, MessageTypePayment} {
		g, ok := GuaranteeForType(mt)
		assert.True(t, ok, "类型 %s 应登记在默认表", mt)
		assert.Equal(t, GuaranteeGuaranteed, g, "类型 %s 应为必达级", mt)
	}

	// 高频流式：状态信号类
	for _, mt := range []MessageType{MessageTypeTyping, MessageTypeRead, MessageTypeDelivered, MessageTypeUserStatusChanged} {
		g, ok := GuaranteeForType(mt)
		assert.True(t, ok, "类型 %s 应登记在默认表", mt)
		assert.Equal(t, GuaranteeEphemeral, g, "类型 %s 应为高频流式级", mt)
	}

	// 未登记类型：普通档
	g, ok := GuaranteeForType(MessageTypeText)
	assert.False(t, ok, "text 未登记在默认表")
	assert.Equal(t, GuaranteeUnset, g)
}

func TestResolveGuarantee_Default(t *testing.T) {
	// 零值构造 + 未登记类型 → 兜底普通档
	msg := &HubMessage{MessageType: MessageTypeText}
	assert.Equal(t, GuaranteeStandard, msg.ResolveGuarantee())
}

func TestResolveGuarantee_TypeTable(t *testing.T) {
	// 类型默认表优先于兜底（零值构造无需初始化）
	msg := &HubMessage{MessageType: MessageTypeKickOut}
	assert.Equal(t, GuaranteeGuaranteed, msg.ResolveGuarantee())

	msg = &HubMessage{MessageType: MessageTypeTyping}
	assert.Equal(t, GuaranteeEphemeral, msg.ResolveGuarantee())
}

func TestResolveGuarantee_ExplicitWins(t *testing.T) {
	// 显式设置优先级最高：覆盖类型默认表
	msg := NewHubMessage().
		SetMessageType(MessageTypeTyping).
		WithGuarantee(GuaranteeGuaranteed)
	assert.Equal(t, GuaranteeGuaranteed, msg.ResolveGuarantee())

	// 链式返回自身
	assert.Same(t, msg, msg.WithGuarantee(GuaranteeStandard))
	assert.Equal(t, GuaranteeStandard, msg.ResolveGuarantee())
}

func TestResolveGuarantee_ClassificationScore(t *testing.T) {
	// 分类评分 ≥ 90 → 必达（消费 SendToUserWithClassification 的打分写入）
	classification := &MessageClassification{
		Type:             MessageTypeText,
		Priority:         MessagePriorityCritical,
		VIPLevel:         VIPLevel("v8"),
		UrgencyLevel:     UrgencyLevelHigh,
		BusinessCategory: BusinessCategorySecurity,
	}
	assert.GreaterOrEqual(t, classification.GetFinalPriority(), ClassificationGuaranteedScore, "构造的分类应达到必达阈值")

	msg := NewHubMessage().SetMessageType(MessageTypeText)
	msg.WithClassification(classification)
	assert.Equal(t, GuaranteeGuaranteed, msg.ResolveGuarantee())
}

func TestResolveGuarantee_PriorityCritical(t *testing.T) {
	// PriorityCritical 提级（分类未达标时兜底提级路径）
	msg := NewHubMessage().
		SetMessageType(MessageTypeText).
		SetPriority(PriorityCritical)
	assert.Equal(t, GuaranteeGuaranteed, msg.ResolveGuarantee())

	// 普通优先级不提级
	msg = NewHubMessage().
		SetMessageType(MessageTypeText).
		SetPriority(PriorityNormal)
	assert.Equal(t, GuaranteeStandard, msg.ResolveGuarantee())
}

func TestResolveGuarantee_ClonePreserves(t *testing.T) {
	// Clone 保留显式分级与类型推导语义（值拷贝覆盖 DeliveryGuarantee 字段）
	msg := NewHubMessage().
		SetMessageType(MessageTypeText).
		WithGuarantee(GuaranteeEphemeral)
	cloned := msg.Clone()
	assert.Equal(t, GuaranteeEphemeral, cloned.ResolveGuarantee())
}

func TestResolveGuarantee_Concurrent(t *testing.T) {
	// 并发 ResolveGuarantee 与 WithGuarantee（-race 下读写锁互斥）
	msg := NewHubMessage().SetMessageType(MessageTypeText)
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			msg.ResolveGuarantee()
		}
	}()

	for i := 0; i < 1000; i++ {
		msg.WithGuarantee(GuaranteeEphemeral)
		msg.WithGuarantee(GuaranteeUnset)
	}
	<-done
}

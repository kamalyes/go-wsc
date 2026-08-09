/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-10 00:00:00
 * @FilePath: \go-wsc\models\enums_test.go
 * @Description: 枚举类型测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestUserRole 验证用户角色的 String 与 IsValid
func TestUserRole(t *testing.T) {
	// String
	assert.Equal(t, "customer", UserRoleCustomer.String())
	assert.Equal(t, "agent", UserRoleAgent.String())
	assert.Equal(t, "admin", UserRoleAdmin.String())

	// IsValid 有效值
	assert.True(t, UserRoleCustomer.IsValid())
	assert.True(t, UserRoleAgent.IsValid())
	assert.True(t, UserRoleAdmin.IsValid())

	// IsValid 无效值
	assert.False(t, UserRole("invalid").IsValid())
	assert.False(t, UserRole("").IsValid())
}

// TestUserType 验证用户类型的各类判定方法
func TestUserType(t *testing.T) {
	// String
	for _, ut := range []UserType{
		UserTypeVisitor, UserTypeCustomer, UserTypeAgent, UserTypeAdmin,
		UserTypeBot, UserTypeVIP, UserTypeSystem, UserTypeObserver,
	} {
		assert.Equal(t, string(ut), ut.String())
		assert.True(t, ut.IsValid(), "%s 应有效", ut)
	}

	// 无效值
	assert.False(t, UserType("invalid").IsValid())
	assert.False(t, UserType("").IsValid())

	// IsCustomerType（访客、普通客户、VIP客户）
	assert.True(t, UserTypeVisitor.IsCustomerType())
	assert.True(t, UserTypeCustomer.IsCustomerType())
	assert.True(t, UserTypeVIP.IsCustomerType())
	assert.False(t, UserTypeAgent.IsCustomerType())
	assert.False(t, UserTypeAdmin.IsCustomerType())
	assert.False(t, UserTypeBot.IsCustomerType())
	assert.False(t, UserTypeSystem.IsCustomerType())
	assert.False(t, UserTypeObserver.IsCustomerType())

	// IsAgentType
	assert.True(t, UserTypeAgent.IsAgentType())
	assert.False(t, UserTypeCustomer.IsAgentType())
	assert.False(t, UserTypeBot.IsAgentType())

	// IsSystemType（仅系统用户）
	assert.True(t, UserTypeSystem.IsSystemType())
	assert.False(t, UserTypeAdmin.IsSystemType())
	assert.False(t, UserTypeAgent.IsSystemType())

	// IsHumanType（排除机器人和系统）
	assert.True(t, UserTypeVisitor.IsHumanType())
	assert.True(t, UserTypeCustomer.IsHumanType())
	assert.True(t, UserTypeAgent.IsHumanType())
	assert.True(t, UserTypeAdmin.IsHumanType())
	assert.True(t, UserTypeVIP.IsHumanType())
	assert.False(t, UserTypeBot.IsHumanType())
	assert.False(t, UserTypeSystem.IsHumanType())
	assert.False(t, UserTypeObserver.IsHumanType())

	// IsVIPType
	assert.True(t, UserTypeVIP.IsVIPType())
	assert.False(t, UserTypeCustomer.IsVIPType())
}

// TestUserStatus 验证用户状态转换与判定
func TestUserStatus(t *testing.T) {
	// String
	for _, us := range []UserStatus{
		UserStatusOnline, UserStatusOffline, UserStatusBusy, UserStatusAway, UserStatusInvisible,
	} {
		assert.Equal(t, string(us), us.String())
		assert.True(t, us.IsValid(), "%s 应有效", us)
	}

	assert.False(t, UserStatus("invalid").IsValid())

	// ToInt / UserStatusFromInt 往返
	cases := []struct {
		status UserStatus
		val    int
	}{
		{UserStatusOnline, 0},
		{UserStatusBusy, 1},
		{UserStatusAway, 2},
		{UserStatusInvisible, 3},
		{UserStatusOffline, 4},
	}
	for _, c := range cases {
		assert.Equal(t, c.val, c.status.ToInt())
		assert.Equal(t, c.status, UserStatusFromInt(c.val))
	}

	// 越界返回 offline
	assert.Equal(t, UserStatusOffline, UserStatusFromInt(-1))
	assert.Equal(t, UserStatusOffline, UserStatusFromInt(99))

	// 无效状态 ToInt 返回 4
	assert.Equal(t, 4, UserStatus("invalid").ToInt())
}

// TestDisconnectReason 验证断开连接原因
func TestDisconnectReason(t *testing.T) {
	reasons := []DisconnectReason{
		DisconnectReasonReadError, DisconnectReasonWriteError, DisconnectReasonContextDone,
		DisconnectReasonCloseMessage, DisconnectReasonHeartbeatFail, DisconnectReasonKickOut,
		DisconnectReasonForceOffline, DisconnectReasonTimeout, DisconnectReasonClientRequest,
		DisconnectReasonServerShutdown, DisconnectReasonUnknown,
	}
	for _, r := range reasons {
		assert.Equal(t, string(r), r.String())
		assert.True(t, r.IsValid(), "%s 应有效", r)
	}
	assert.False(t, DisconnectReason("invalid").IsValid())
}

// TestErrorSeverity 验证错误严重程度
func TestErrorSeverity(t *testing.T) {
	severities := []ErrorSeverity{
		ErrorSeverityInfo, ErrorSeverityWarning, ErrorSeverityError,
		ErrorSeverityCritical, ErrorSeverityFatal,
	}
	for _, s := range severities {
		assert.Equal(t, string(s), s.String())
		assert.True(t, s.IsValid(), "%s 应有效", s)
	}
	assert.False(t, ErrorSeverity("invalid").IsValid())
}

// TestQueueType 验证队列类型
func TestQueueType(t *testing.T) {
	qts := []QueueType{
		QueueTypeBroadcast, QueueTypePending, QueueTypeAllQueues,
		QueueTypeMessageQueue, QueueTypeClientBuffer,
	}
	for _, q := range qts {
		assert.Equal(t, string(q), q.String())
		assert.True(t, q.IsValid(), "%s 应有效", q)
	}
	assert.False(t, QueueType("invalid").IsValid())
}

// TestMessageStatus 验证消息状态
func TestMessageStatus(t *testing.T) {
	statuses := []MessageStatus{
		MessageStatusPending, MessageStatusSent, MessageStatusDelivered,
		MessageStatusRead, MessageStatusFailed,
	}
	for _, s := range statuses {
		assert.Equal(t, string(s), s.String())
		assert.True(t, s.IsValid(), "%s 应有效", s)
	}
	assert.False(t, MessageStatus("invalid").IsValid())
}

// TestNodeStatus 验证节点状态
func TestNodeStatus(t *testing.T) {
	statuses := []NodeStatus{NodeStatusActive, NodeStatusInactive, NodeStatusOffline}
	for _, s := range statuses {
		assert.Equal(t, string(s), s.String())
		assert.True(t, s.IsValid(), "%s 应有效", s)
	}
	assert.False(t, NodeStatus("invalid").IsValid())
}

// TestConnectionStatus 验证连接状态
func TestConnectionStatus(t *testing.T) {
	statuses := []ConnectionStatus{
		ConnectionStatusConnecting, ConnectionStatusConnected,
		ConnectionStatusDisconnected, ConnectionStatusReconnecting, ConnectionStatusError,
	}
	for _, s := range statuses {
		assert.Equal(t, string(s), s.String())
		assert.True(t, s.IsValid(), "%s 应有效", s)
	}
	assert.False(t, ConnectionStatus("invalid").IsValid())
}

// TestOperationType 验证操作类型
func TestOperationType(t *testing.T) {
	ops := []OperationType{
		OperationTypeJoin, OperationTypeLeave, OperationTypeMessage, OperationTypeBroadcast,
		OperationTypeNotify, OperationTypeHeartbeat, OperationTypeAuth, OperationTypeSync,
		OperationTypeSendMessage, OperationTypeKickUser, OperationTypeNodeRegister,
		OperationTypeObserverNotify, OperationTypeGroupsBroadcast,
	}
	for _, o := range ops {
		assert.Equal(t, string(o), o.String())
		assert.True(t, o.IsValid(), "%s 应有效", o)
	}
	assert.False(t, OperationType("invalid").IsValid())
}

// TestClientType 验证客户端类型
func TestClientType(t *testing.T) {
	cts := []ClientType{ClientTypeWeb, ClientTypeMobile, ClientTypeDesktop, ClientTypeAPI}
	for _, c := range cts {
		assert.Equal(t, string(c), c.String())
		assert.True(t, c.IsValid(), "%s 应有效", c)
	}
	assert.False(t, ClientType("invalid").IsValid())
}

// TestConnectionType 验证连接类型（IsValid 内联实现，不走验证器）
func TestConnectionType(t *testing.T) {
	assert.Equal(t, "websocket", ConnectionTypeWebSocket.String())
	assert.Equal(t, "sse", ConnectionTypeSSE.String())
	assert.True(t, ConnectionTypeWebSocket.IsValid())
	assert.True(t, ConnectionTypeSSE.IsValid())
	assert.False(t, ConnectionType("invalid").IsValid())
	assert.False(t, ConnectionType("").IsValid())
}

// TestPriority 验证优先级
func TestPriority(t *testing.T) {
	ps := []Priority{PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent, PriorityCritical}
	for _, p := range ps {
		assert.Equal(t, string(p), p.String())
		assert.True(t, p.IsValid(), "%s 应有效", p)
	}
	assert.False(t, Priority("invalid").IsValid())
}

// TestDepartment 验证部门类型
func TestDepartment(t *testing.T) {
	depts := []Department{
		DepartmentSales, DepartmentSupport, DepartmentBilling,
		DepartmentGeneral, DepartmentTechnical,
	}
	for _, d := range depts {
		assert.Equal(t, string(d), d.String())
		assert.True(t, d.IsValid(), "%s 应有效", d)
	}
	assert.False(t, Department("invalid").IsValid())
}

// TestSkill 验证技能类型
func TestSkill(t *testing.T) {
	skills := []Skill{
		SkillTechnical, SkillSales, SkillBilling, SkillGeneral,
		SkillLanguageEN, SkillLanguageZH, SkillVIP,
	}
	for _, s := range skills {
		assert.Equal(t, string(s), s.String())
		assert.True(t, s.IsValid(), "%s 应有效", s)
	}
	assert.False(t, Skill("invalid").IsValid())
}

// TestPushType 验证推送类型
func TestPushType(t *testing.T) {
	pts := []PushType{PushTypeNone, PushTypeDirect, PushTypeQueue, PushTypeOffline, PushTypeUnicast}
	for _, p := range pts {
		assert.Equal(t, string(p), p.String())
	}
	// 注意：PushTypeValidator 只注册了 none/direct/queue/offline，unicast 未注册
	assert.True(t, PushTypeNone.IsValid())
	assert.True(t, PushTypeDirect.IsValid())
	assert.True(t, PushTypeQueue.IsValid())
	assert.True(t, PushTypeOffline.IsValid())
	assert.False(t, PushTypeUnicast.IsValid(), "unicast 未在验证器中注册")
	assert.False(t, PushType("invalid").IsValid())
}

// TestBroadcastType 验证广播类型
func TestBroadcastType(t *testing.T) {
	bts := []BroadcastType{BroadcastTypeNone, BroadcastTypeSession, BroadcastTypeGlobal}
	for _, b := range bts {
		assert.Equal(t, string(b), b.String())
		assert.True(t, b.IsValid(), "%s 应有效", b)
	}
	assert.False(t, BroadcastType("invalid").IsValid())
}

// TestVIPLevel 验证 VIP 等级
func TestVIPLevel(t *testing.T) {
	levels := []VIPLevel{
		VIPLevelV0, VIPLevelV1, VIPLevelV2, VIPLevelV3, VIPLevelV4,
		VIPLevelV5, VIPLevelV6, VIPLevelV7, VIPLevelV8,
	}
	for i, v := range levels {
		assert.Equal(t, string(v), v.String())
		assert.True(t, v.IsValid(), "%s 应有效", v)
		assert.Equal(t, i, v.GetLevel())
	}
	assert.False(t, VIPLevel("invalid").IsValid())
	assert.Equal(t, 0, VIPLevel("invalid").GetLevel())

	// VIPLevelFromLevel 往返
	for i, v := range levels {
		assert.Equal(t, v, VIPLevelFromLevel(i))
	}
	// 越界返回 v0
	assert.Equal(t, VIPLevelV0, VIPLevelFromLevel(-1))
	assert.Equal(t, VIPLevelV0, VIPLevelFromLevel(9))

	// IsHigherThan
	assert.True(t, VIPLevelV8.IsHigherThan(VIPLevelV0))
	assert.False(t, VIPLevelV0.IsHigherThan(VIPLevelV8))
	assert.False(t, VIPLevelV5.IsHigherThan(VIPLevelV5))
}

// TestUrgencyLevel 验证紧急等级
func TestUrgencyLevel(t *testing.T) {
	levels := []UrgencyLevel{UrgencyLevelLow, UrgencyLevelNormal, UrgencyLevelHigh}
	for i, u := range levels {
		assert.Equal(t, string(u), u.String())
		assert.True(t, u.IsValid(), "%s 应有效", u)
		assert.Equal(t, i, u.GetLevel())
	}
	assert.False(t, UrgencyLevel("invalid").IsValid())
	// 无效值 GetLevel 默认返回 1（normal）
	assert.Equal(t, 1, UrgencyLevel("invalid").GetLevel())

	// IsMoreUrgentThan
	assert.True(t, UrgencyLevelHigh.IsMoreUrgentThan(UrgencyLevelLow))
	assert.False(t, UrgencyLevelLow.IsMoreUrgentThan(UrgencyLevelHigh))
	assert.False(t, UrgencyLevelNormal.IsMoreUrgentThan(UrgencyLevelNormal))
}

// TestBusinessCategory 验证业务分类
func TestBusinessCategory(t *testing.T) {
	cats := []BusinessCategory{
		BusinessCategoryGeneral, BusinessCategoryCustomer, BusinessCategorySales,
		BusinessCategoryTechnical, BusinessCategoryFinance, BusinessCategorySecurity,
		BusinessCategoryOperations, BusinessCategorySupport, BusinessCategoryIT,
		BusinessCategoryQuality, BusinessCategoryOther,
	}
	for _, c := range cats {
		assert.Equal(t, string(c), c.String())
		assert.True(t, c.IsValid(), "%s 应有效", c)
		assert.Equal(t, string(c), c.GetCategoryType())
	}
	assert.False(t, BusinessCategory("invalid").IsValid())
}

// TestGetAllVIPLevels 验证 GetAllVIPLevels 返回完整列表
func TestGetAllVIPLevels(t *testing.T) {
	levels := GetAllVIPLevels()
	assert.Len(t, levels, 9)
	assert.Equal(t, VIPLevelV0, levels[0])
	assert.Equal(t, VIPLevelV8, levels[8])
}

// TestGetAllUrgencyLevels 验证 GetAllUrgencyLevels
func TestGetAllUrgencyLevels(t *testing.T) {
	levels := GetAllUrgencyLevels()
	assert.Len(t, levels, 3)
	assert.Equal(t, UrgencyLevelLow, levels[0])
	assert.Equal(t, UrgencyLevelHigh, levels[2])
}

// TestGetAllBusinessCategories 验证 GetAllBusinessCategories
func TestGetAllBusinessCategories(t *testing.T) {
	cats := GetAllBusinessCategories()
	assert.Len(t, cats, 7)
	assert.Equal(t, BusinessCategoryGeneral, cats[0])
	assert.Equal(t, BusinessCategoryOperations, cats[6])
}

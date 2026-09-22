/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 15:22:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 15:22:00
 * @FilePath: \go-wsc\group\lifecycle.go
 * @Description: 群组生命周期 —— CRUD、成员管理与系统组自动维护
 *
 * 本文件只负责群组本身的生命周期（建群/解散/加成员/查成员/系统组自动装配）。
 * 消息投递与广播统一走 hub.Deliver（见 broadcast.go），路由元数据全由 ctx 传递，
 * 因此历史 SendToGroup / BroadcastToGroupMembers / BroadcastToGroup /
 * BroadcastToAllGroups / BroadcastToAllNamespacesAllGroups / BroadcastToGroups /
 * BroadcastToNamespace 七个割裂方法以及对应的跨节点辅助均不在此文件。
 *
 * 路由全 ctx 驱动：appID（最上层隔离维度，默认 __default_app__）+ namespace（默认 default）
 * + groupID 均从 ctx 提取（routing.NewRoute().WithAppID(...).Inject(ctx) 注入）。
 *   - 单群组读方法（GetGroup/GetGroupMembers/IsGroupMember/GetGroupMemberCount）取 ctx 首个 groupID
 *   - 写/破坏方法（AddGroupMembers/RemoveGroupMembers/DisbandGroup）多群批量：遍历 ctx groupIDs，best-effort，返回最后错误
 *   - 命名空间方法（GetUserGroups/GetNamespaceGroups）只取 (appID, namespace)
 *
 * 层级结构：AppID（应用隔离）→ Namespace（默认 "default"，类似 k8s namespace）→ Group → Members
 * 群组成员关系持久化于后端（key 含 appID 前缀），跨节点共享，跨 app 严格隔离。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"
	"errors"
	"time"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// LifecycleManager 群组生命周期管理器
type LifecycleManager struct {
	host Host
}

// NewLifecycleManager 创建群组生命周期管理器
func NewLifecycleManager(host Host) *LifecycleManager {
	return &LifecycleManager{host: host}
}

// routeFromCtx 从 ctx 提取并归一化路由维度（appID + namespace）
// 群组操作为严格场景（非广播），namespace 不允许空：空补 DefaultNamespace；appID 空补 DefaultAppID
// 入口层（Route.Inject）已归一化 appID，此处 NormalizeRoute 幂等；namespace 在广播场景可能留空，此处补默认
func routeFromCtx(ctx context.Context) (appID, namespace string) {
	return routing.NormalizeRoute(routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx))
}

// ============================================================================
// 群组 CRUD 与成员管理（路由全 ctx 驱动）
// ============================================================================

// GetGroup 获取群组元信息（appID/namespace/groupID 全从 ctx 取，groupID 取首个）
func (m *LifecycleManager) GetGroup(ctx context.Context) (*models.Group, error) {
	if m.host.GetGroupStore() == nil {
		return nil, models.ErrGroupRepoNotSet
	}
	appID, ns := routeFromCtx(ctx)
	groupID := routing.FirstGroupIDFromContext(ctx)
	return m.host.GetGroupStore().GetGroup(ctx, appID, ns, groupID)
}

// DisbandGroup 解散群组（多群批量：遍历 ctx groupIDs，best-effort，返回最后错误）
// 同时清理群组元信息、成员集合、命名空间索引及各成员的反向索引；逐群触发 OnGroupDisband 回调
func (m *LifecycleManager) DisbandGroup(ctx context.Context) error {
	if m.host.GetGroupStore() == nil {
		return models.ErrGroupRepoNotSet
	}
	appID, ns := routeFromCtx(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	var lastErr error
	for _, gid := range groupIDs {
		if err := m.host.GetGroupStore().DisbandGroup(ctx, appID, ns, gid); err != nil {
			m.host.GetLogger().ErrorContextKV(ctx, "解散群组失败",
				"app_id", appID, "namespace", ns, "group_id", gid, "error", err)
			lastErr = err
			continue
		}
		m.host.GetLogger().InfoContextKV(ctx, "群组已解散",
			"app_id", appID, "namespace", ns, "group_id", gid)
		// 🔔 异步触发群组解散回调（cbCtx 注入路由供回调内 routing.AppIDFromContext 取 appID）
		m.TriggerGroupDisband(ctx, ns, gid)
	}
	return lastErr
}

// AddGroupMembers 添加成员到群组（多群批量：遍历 ctx groupIDs，best-effort，返回最后错误）
// 群组不存在时自动创建（register 自动装配场景，无需手动 CreateGroup）；同时更新成员的反向索引
// 注意：手动 AddGroupMembers 不触发 OnGroupMemberJoin 回调（仅 register 自动装配触发）
func (m *LifecycleManager) AddGroupMembers(ctx context.Context, userIDs []string) error {
	if m.host.GetGroupStore() == nil {
		return models.ErrGroupRepoNotSet
	}
	if len(userIDs) == 0 {
		return nil
	}
	appID, ns := routeFromCtx(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	var lastErr error
	for _, gid := range groupIDs {
		if err := m.addGroupMembersSingle(ctx, appID, ns, gid, userIDs); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// addGroupMembersSingle 单群添加成员（含自动建群 + MaxMembers 校验）
// 抽自原 AddGroupMembers 主体，供多群批量与连接自动入群复用；不触发回调（回调由调用方按需触发）
// 群组不存在时自动创建（register 自动装配时无需业务方手动建群）
func (m *LifecycleManager) addGroupMembersSingle(ctx context.Context, appID, namespace, groupID string, userIDs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	store := m.host.GetGroupStore()
	// 校验群组是否存在，不存在则自动创建
	group, err := store.GetGroup(ctx, appID, namespace, groupID)
	if err != nil {
		if !errors.Is(err, models.ErrGroupNotFound) {
			return err
		}
		// 群组不存在，自动创建（register 自动装配时无需业务方手动建群）
		newGroup := &models.Group{
			AppID:     appID,
			GroupID:   groupID,
			Namespace: namespace,
			CreatedAt: time.Now(),
		}
		if err := store.CreateGroup(ctx, newGroup); err != nil && !errors.Is(err, models.ErrGroupExisted) {
			m.host.GetLogger().ErrorContextKV(ctx, "自动创建群组失败",
				"app_id", appID, "namespace", namespace, "group_id", groupID, "error", err)
			return err
		}
		m.host.GetLogger().InfoContextKV(ctx, "群组自动创建成功",
			"app_id", appID, "namespace", namespace, "group_id", groupID)
		group = newGroup
	}
	// 校验群组人数上限（排除已存在成员，避免重连用户被误判超限）
	// 重连场景：用户成员关系在离线时保留，IsMember 返回 true 不计入新增
	if group.MaxMembers > 0 {
		current, err := store.GetMemberCount(ctx, appID, namespace, groupID)
		if err != nil {
			return err
		}
		newCount := 0
		for _, uid := range userIDs {
			exists, err := store.IsMember(ctx, appID, namespace, groupID, uid)
			if err != nil {
				return err
			}
			if !exists {
				newCount++
			}
		}
		if int(current)+newCount > group.MaxMembers {
			return models.ErrGroupFull
		}
	}
	if err := store.AddMembers(ctx, appID, namespace, groupID, userIDs); err != nil {
		m.host.GetLogger().ErrorContextKV(ctx, "添加群组成员失败",
			"app_id", appID, "namespace", namespace, "group_id", groupID, "users", userIDs, "error", err)
		return err
	}
	m.host.GetLogger().InfoContextKV(ctx, "群组成员添加成功",
		"app_id", appID, "namespace", namespace, "group_id", groupID, "users", userIDs)
	return nil
}

// RemoveGroupMembers 从群组移除成员（多群批量：遍历 ctx groupIDs，best-effort，返回最后错误）
// 同时清理成员的反向索引；逐群触发 OnGroupMemberLeave 回调
func (m *LifecycleManager) RemoveGroupMembers(ctx context.Context, userIDs []string) error {
	if m.host.GetGroupStore() == nil {
		return models.ErrGroupRepoNotSet
	}
	if len(userIDs) == 0 {
		return nil
	}
	appID, ns := routeFromCtx(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	var lastErr error
	for _, gid := range groupIDs {
		if err := m.host.GetGroupStore().RemoveMembers(ctx, appID, ns, gid, userIDs); err != nil {
			m.host.GetLogger().ErrorContextKV(ctx, "移除群组成员失败",
				"app_id", appID, "namespace", ns, "group_id", gid, "users", userIDs, "error", err)
			lastErr = err
			continue
		}
		m.host.GetLogger().InfoContextKV(ctx, "群组成员移除成功",
			"app_id", appID, "namespace", ns, "group_id", gid, "users", userIDs)
		// 🔔 异步触发群组成员离开回调（cbCtx 注入路由供回调内 routing.AppIDFromContext 取 appID）
		m.TriggerGroupMemberLeave(ctx, ns, gid, userIDs)
	}
	return lastErr
}

// GetGroupMembers 获取群组所有成员ID（groupID 取 ctx 首个 groupID）
func (m *LifecycleManager) GetGroupMembers(ctx context.Context) ([]string, error) {
	if m.host.GetGroupStore() == nil {
		return nil, models.ErrGroupRepoNotSet
	}
	appID, ns := routeFromCtx(ctx)
	groupID := routing.FirstGroupIDFromContext(ctx)
	return m.host.GetGroupStore().GetMembers(ctx, appID, ns, groupID)
}

// GetUserGroups 获取用户在 ctx 的 (appID, namespace) 下加入的所有群组ID
func (m *LifecycleManager) GetUserGroups(ctx context.Context, userID string) ([]string, error) {
	if m.host.GetGroupStore() == nil {
		return nil, models.ErrGroupRepoNotSet
	}
	appID, ns := routeFromCtx(ctx)
	return m.host.GetGroupStore().GetUserGroups(ctx, appID, ns, userID)
}

// IsGroupMember 判断用户是否为群组成员（groupID 取 ctx 首个 groupID）
func (m *LifecycleManager) IsGroupMember(ctx context.Context, userID string) (bool, error) {
	if m.host.GetGroupStore() == nil {
		return false, models.ErrGroupRepoNotSet
	}
	appID, ns := routeFromCtx(ctx)
	groupID := routing.FirstGroupIDFromContext(ctx)
	return m.host.GetGroupStore().IsMember(ctx, appID, ns, groupID, userID)
}

// GetGroupMemberCount 获取群组成员数量（groupID 取 ctx 首个 groupID）
func (m *LifecycleManager) GetGroupMemberCount(ctx context.Context) (int64, error) {
	if m.host.GetGroupStore() == nil {
		return 0, models.ErrGroupRepoNotSet
	}
	appID, ns := routeFromCtx(ctx)
	groupID := routing.FirstGroupIDFromContext(ctx)
	return m.host.GetGroupStore().GetMemberCount(ctx, appID, ns, groupID)
}

// GetNamespaceGroups 获取 ctx 的 (appID, namespace) 下所有群组ID
// 调用方需要「向命名空间所有群组广播」时，先调本方法取得 groupIDs，
// 再通过 routing.NewRoute().WithAppID(...).WithNamespace(...).Inject(ctx) 注入 ctx 后调一次 hub.Deliver。
func (m *LifecycleManager) GetNamespaceGroups(ctx context.Context) ([]string, error) {
	if m.host.GetGroupStore() == nil {
		return nil, models.ErrGroupRepoNotSet
	}
	appID, ns := routeFromCtx(ctx)
	return m.host.GetGroupStore().GetNamespaceGroups(ctx, appID, ns)
}

// ============================================================================
// 群组生命周期回调触发
//
// 签名保持 func(ctx, namespace, groupID, ...)，appID 注入 cbCtx 供业务层 ctx 取；
// 回调契约不变，业务层通过 routing.AppIDFromContext 在回调内获取 appID，无需改签名。
// ============================================================================

// TriggerGroupDisband 异步触发群组解散回调
// 在 DisbandGroup 成功后逐群调用；cbCtx 注入路由供回调内 routing.AppIDFromContext 取 appID
func (m *LifecycleManager) TriggerGroupDisband(ctx context.Context, namespace, groupID string) {
	callback := m.host.GetGroupDisbandCallback()
	if callback == nil {
		return
	}
	appID := routing.AppIDFromContext(ctx)
	ns, gid := namespace, groupID
	m.host.TrySubmitCallback(func() {
		cbCtx, cbCancel := context.WithTimeout(m.host.Context(), 5*time.Second)
		defer cbCancel()
		cbCtx = routing.NewRoute().WithAppID(appID).WithNamespace(ns).WithGroup(gid).Inject(cbCtx)
		callback(cbCtx, ns, gid)
	})
}

// TriggerGroupMemberJoin 异步触发群组成员加入回调
// 在客户端连接时自动加群成功后调用（register 自动装配 + 系统组自动加入），手动 AddGroupMembers 不触发
// 复制切片避免调用方后续修改影响异步回调；cbCtx 注入路由供回调内 routing.AppIDFromContext 取 appID
func (m *LifecycleManager) TriggerGroupMemberJoin(ctx context.Context, namespace, groupID string, userIDs []string) {
	callback := m.host.GetGroupMemberJoinCallback()
	if callback == nil {
		return
	}
	appID := routing.AppIDFromContext(ctx)
	ns, gid := namespace, groupID
	uids := append([]string(nil), userIDs...)
	m.host.TrySubmitCallback(func() {
		cbCtx, cbCancel := context.WithTimeout(m.host.Context(), 5*time.Second)
		defer cbCancel()
		cbCtx = routing.NewRoute().WithAppID(appID).WithNamespace(ns).WithGroup(gid).Inject(cbCtx)
		callback(cbCtx, ns, gid, uids)
	})
}

// TriggerGroupMemberLeave 异步触发群组成员离开回调
// 在 RemoveGroupMembers 成功后逐群调用；cbCtx 注入路由供回调内 routing.AppIDFromContext 取 appID
// 复制切片避免调用方后续修改影响异步回调
func (m *LifecycleManager) TriggerGroupMemberLeave(ctx context.Context, namespace, groupID string, userIDs []string) {
	callback := m.host.GetGroupMemberLeaveCallback()
	if callback == nil {
		return
	}
	appID := routing.AppIDFromContext(ctx)
	ns, gid := namespace, groupID
	uids := append([]string(nil), userIDs...)
	m.host.TrySubmitCallback(func() {
		cbCtx, cbCancel := context.WithTimeout(m.host.Context(), 5*time.Second)
		defer cbCancel()
		cbCtx = routing.NewRoute().WithAppID(appID).WithNamespace(ns).WithGroup(gid).Inject(cbCtx)
		callback(cbCtx, ns, gid, uids)
	})
}

// ============================================================================
// 系统保留组自动管理（agent/observer 统一到 group 体系）
//
// 设计：本地分片索引（agentShards/observerShards）保留做 O(1) 缓存，
//   后端系统组（__agents__/__observers__）用于跨节点共享成员关系与显式广播
//   连接注册时自动加入系统组，断开时自动离开，业务无感
// appID 隔离：系统组从每 namespace 一份变为每 (appID, namespace) 一份，严格按 app 隔离
// ============================================================================

// systemGroupOfUserType 返回用户类型对应的系统保留组（无则空）
// agent/bot → __agents__，observer → __observers__，其余不加入系统组
func systemGroupOfUserType(ut models.UserType) string {
	switch ut {
	case models.UserTypeAgent, models.UserTypeBot:
		return constants.SystemGroupAgents
	case models.UserTypeObserver:
		return constants.SystemGroupObservers
	default:
		return ""
	}
}

// JoinSystemGroupsOnConnect 客户端连接时自动加入系统保留组
// 注入路由（appID + namespace + 系统组 groupID）供 EnsureAndJoinSystemGroup 从 ctx 读取
// namespace 用 client.Namespace 原值（全局观察者保持 ""，EnsureAndJoinSystemGroup 不归一化 namespace）
func (m *LifecycleManager) JoinSystemGroupsOnConnect(ctx context.Context, client *models.Client) {
	if m.host.GetGroupStore() == nil {
		return
	}
	groupID := systemGroupOfUserType(client.UserType)
	if groupID == "" {
		return
	}
	// registry 传入的 ctx 来自 client.Context，未设置时为 nil；路由元数据全在 client 上，nil ctx 兜底为 Background 避免 panic
	if ctx == nil {
		ctx = context.Background()
	}
	// 系统组支持 namespace="" 全局语义（全局观察者），用 client.Namespace 原值而非 GetNamespace()（后者会把 "" 补成 DefaultNamespace）
	ctx = routing.NewRoute().WithAppID(client.GetAppID()).WithNamespace(client.Namespace).WithGroup(groupID).Inject(ctx)
	m.EnsureAndJoinSystemGroup(ctx, groupID, client.UserID)
}

// JoinMemberGroupOnConnect 客户端连接时自动加入成员组（多群组）
//
// 普通用户（非观察者）连接时遍历 client.GetGroupIDs() 逐群加入：
//   - 系统保留组名（__ 前缀，如默认组 __default_gp__）走 ensureAndJoinSystemGroup（CreateGroup 拒绝 __ 前缀）
//   - 业务组名走 addGroupMembersSingle（自动创建 + MaxMembers 校验）
//
// 观察者不作为成员加入群组（仅观察，不接收成员消息）
// GetGroupIDs 回退链保证非空（空→[DefaultGroupID]，保留单值时代加入默认组行为）
// 每群加入成功后触发 OnGroupMemberJoin 回调
//
// 逐群注入单群 ctx（routing.NewRoute().WithAppID(...).WithGroup(gid).Inject(ctx)），避免与 AddGroupMembers 多群 best-effort 语义叠加
func (m *LifecycleManager) JoinMemberGroupOnConnect(ctx context.Context, client *models.Client) {
	if m.host.GetGroupStore() == nil {
		return
	}
	// 观察者不作为成员加入群组
	if client.UserType == models.UserTypeObserver {
		return
	}
	// registry 传入的 ctx 来自 client.Context，未设置时为 nil；路由元数据全在 client 上，nil ctx 兜底为 Background 避免 panic
	if ctx == nil {
		ctx = context.Background()
	}
	appID := client.GetAppID()
	namespace := client.GetNamespace()
	for _, groupID := range client.GetGroupIDs() {
		// 逐群注入单群 ctx，供 addGroupMembersSingle/EnsureAndJoinSystemGroup 从 ctx 读取
		groupCtx := routing.NewRoute().WithAppID(appID).WithNamespace(namespace).WithGroup(groupID).Inject(ctx)
		if models.IsSystemGroup(groupID) {
			// 系统保留组名（如默认组 __default_gp__）：走 EnsureSystemGroup 路径（CreateGroup 会拒绝 __ 前缀）
			m.EnsureAndJoinSystemGroup(groupCtx, groupID, client.UserID)
			continue
		}
		// 业务组：走 addGroupMembersSingle（自动创建 + MaxMembers 校验）
		if err := m.addGroupMembersSingle(groupCtx, appID, namespace, groupID, []string{client.UserID}); err != nil {
			m.host.GetLogger().WarnContextKV(groupCtx, "连接时自动加入成员组失败",
				"namespace", namespace, "group_id", groupID,
				"user_id", client.UserID, "error", err)
			continue
		}
		// 触发群组成员加入回调（register 自动装配时触发，手动 AddGroupMembers 不触发）
		m.TriggerGroupMemberJoin(groupCtx, namespace, groupID, []string{client.UserID})
	}
}

// LeaveSystemGroupsOnDisconnect 客户端断开时自动离开系统保留组
//
// 多端登录保护：仅当该 userID 已无任何在线连接时才离开系统组
// 调用时当前 client 已由 removeClientUnsafe（handleUnregister Phase 1）从注册表移除，
// 因此 HasUser 查询的是"移除当前连接后是否还存在其他在线连接"
// 竞态可接受：RemoveMembers 对不存在成员幂等，重连时 JoinSystemGroupsOnConnect 会重新加入
//
// 系统组单值，appID/namespace 直接从 client 取（与 JoinSystemGroupsOnConnect 的归一化一致，
// 保证 leave 的后端 key 与 join 时写入的 key 同分区）
func (m *LifecycleManager) LeaveSystemGroupsOnDisconnect(ctx context.Context, client *models.Client) {
	if m.host.GetGroupStore() == nil {
		return
	}
	groupID := systemGroupOfUserType(client.UserType)
	if groupID == "" {
		return
	}
	// 该 userID 仍有其他同 appID+namespace 的在线连接时保留系统组成员身份，避免多端场景下其他端收不到系统组广播
	// 按 client 自身信封过滤：跨 app/ns 的连接不算（不同应用/命名空间各自管理系统组成员生命周期）
	if m.host.GetShardedRegistry().HasUser(client.UserID, client.GetAppID(), client.GetNamespace()) {
		m.host.GetLogger().DebugContextKV(ctx, "用户仍有其他同信封在线连接，保留系统组成员身份",
			"user_id", client.UserID, "group_id", groupID,
			"app_id", client.GetAppID(), "namespace", client.GetNamespace())
		return
	}
	if err := m.host.GetGroupStore().RemoveMembers(ctx, client.GetAppID(), client.GetNamespace(), groupID, []string{client.UserID}); err != nil {
		m.host.GetLogger().DebugContextKV(ctx, "离开系统组失败",
			"user_id", client.UserID, "group_id", groupID, "error", err)
	}
}

// EnsureAndJoinSystemGroup 确保系统组存在并加入成员
// appID/namespace 从 ctx 读取（调用方 JoinSystemGroupsOnConnect/JoinMemberGroupOnConnect 已注入路由）
// 加入成功后触发 OnGroupMemberJoin 回调（observer/agent 自动入群也通知业务层）
func (m *LifecycleManager) EnsureAndJoinSystemGroup(ctx context.Context, groupID, userID string) {
	// 系统组（observer/agent）支持 namespace="" 全局语义，不调用 routeFromCtx（后者会把 "" 补成 DefaultNamespace）
	// appID 仍归一化（最上层隔离维度必填），namespace 保持 ctx 原值
	appID := routing.AppIDFromContext(ctx)
	if appID == "" {
		appID = constants.DefaultAppID
	}
	namespace := routing.NamespaceFromContext(ctx)
	store := m.host.GetGroupStore()
	if err := store.EnsureSystemGroup(ctx, appID, namespace, groupID); err != nil {
		m.host.GetLogger().WarnContextKV(ctx, "ensureSystemGroup 失败",
			"namespace", namespace, "group_id", groupID, "error", err)
		return
	}
	if err := store.AddMembers(ctx, appID, namespace, groupID, []string{userID}); err != nil {
		m.host.GetLogger().WarnContextKV(ctx, "加入系统组失败",
			"namespace", namespace, "group_id", groupID, "user_id", userID, "error", err)
		return
	}
	// 🔔 触发群组成员加入回调（observer/agent 自动入群也通知业务层）
	m.TriggerGroupMemberJoin(ctx, namespace, groupID, []string{userID})
}

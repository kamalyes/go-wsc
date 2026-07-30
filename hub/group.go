/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-22 15:00:00
 * @FilePath: \go-wsc\hub\group.go
 * @Description: Hub 群组管理 - 命名空间隔离的群组 CRUD、成员管理与群组消息投递
 *
 * 层级结构：Namespace（默认 "default"，类似 k8s namespace）→ Group → Members
 * 群组成员关系持久化于 Redis，跨节点共享：
 *   - 在线成员：通过 SendToUserWithRetry 投递（自动支持跨节点路由）
 *   - 离线成员：通过离线消息处理器存储，上线后自动推送
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
)

// ============================================================================
// 群组管理方法
// ============================================================================

// GetGroup 获取群组元信息
func (h *Hub) GetGroup(ctx context.Context, namespace, groupID string) (*Group, error) {
	if h.groupRepo == nil {
		return nil, ErrGroupRepoNotSet
	}
	return h.groupRepo.GetGroup(ctx, namespace, groupID)
}

// DisbandGroup 解散群组
// 同时清理群组元信息、成员集合、命名空间索引及各成员的反向索引
func (h *Hub) DisbandGroup(ctx context.Context, namespace, groupID string) error {
	if h.groupRepo == nil {
		return ErrGroupRepoNotSet
	}
	if err := h.groupRepo.DisbandGroup(ctx, namespace, groupID); err != nil {
		h.logger.ErrorKV("解散群组失败",
			"namespace", namespace, "group_id", groupID, "error", err)
		return err
	}
	h.logger.InfoKV("群组已解散", "namespace", namespace, "group_id", groupID)

	// 🔔 异步触发群组解散回调
	if h.groupDisbandCallback != nil {
		ns, gid := namespace, groupID
		h.workerPool.TrySubmitCallback(func() {
			h.groupDisbandCallback(context.Background(), ns, gid)
		})
	}
	return nil
}

// AddGroupMembers 添加成员到群组
// 同时更新成员的反向索引（user→groups）
// 群组不存在时自动创建（register 自动装配场景，无需手动 CreateGroup）
func (h *Hub) AddGroupMembers(ctx context.Context, namespace, groupID string, userIDs []string) error {
	if h.groupRepo == nil {
		return ErrGroupRepoNotSet
	}
	if len(userIDs) == 0 {
		return nil
	}
	// 校验群组是否存在，不存在则自动创建
	group, err := h.groupRepo.GetGroup(ctx, namespace, groupID)
	if err != nil {
		if !errors.Is(err, ErrGroupNotFound) {
			return err
		}
		// 群组不存在，自动创建（register 自动装配时无需业务方手动建群）
		newGroup := &Group{
			GroupID:   groupID,
			Namespace: namespace,
			CreatedAt: time.Now(),
		}
		if err := h.groupRepo.CreateGroup(ctx, newGroup); err != nil && !errors.Is(err, ErrGroupExisted) {
			h.logger.ErrorKV("自动创建群组失败",
				"namespace", namespace, "group_id", groupID, "error", err)
			return err
		}
		h.logger.InfoKV("群组自动创建成功", "namespace", namespace, "group_id", groupID)
		group = newGroup
	}
	// 校验群组人数上限（排除已存在成员，避免重连用户被误判超限）
	// 重连场景：用户成员关系在离线时保留，IsMember 返回 true 不计入新增
	if group.MaxMembers > 0 {
		current, err := h.groupRepo.GetMemberCount(ctx, namespace, groupID)
		if err != nil {
			return err
		}
		newCount := 0
		for _, uid := range userIDs {
			exists, err := h.groupRepo.IsMember(ctx, namespace, groupID, uid)
			if err != nil {
				return err
			}
			if !exists {
				newCount++
			}
		}
		if int(current)+newCount > group.MaxMembers {
			return ErrGroupFull
		}
	}
	if err := h.groupRepo.AddMembers(ctx, namespace, groupID, userIDs); err != nil {
		h.logger.ErrorKV("添加群组成员失败",
			"namespace", namespace, "group_id", groupID, "users", userIDs, "error", err)
		return err
	}
	h.logger.InfoKV("群组成员添加成功",
		"namespace", namespace, "group_id", groupID, "users", userIDs)
	return nil
}

// triggerGroupMemberJoinCallback 异步触发群组成员加入回调
// 在客户端连接时自动加群成功后调用（register 自动装配 + 系统组自动加入），手动 AddGroupMembers 不触发
// 复制切片避免调用方后续修改影响异步回调
func (h *Hub) triggerGroupMemberJoinCallback(namespace, groupID string, userIDs []string) {
	if h.groupMemberJoinCallback == nil {
		return
	}
	ns, gid := namespace, groupID
	uids := append([]string(nil), userIDs...)
	h.workerPool.TrySubmitCallback(func() {
		h.groupMemberJoinCallback(context.Background(), ns, gid, uids)
	})
}

// RemoveGroupMembers 从群组移除成员
// 同时清理成员的反向索引
func (h *Hub) RemoveGroupMembers(ctx context.Context, namespace, groupID string, userIDs []string) error {
	if h.groupRepo == nil {
		return ErrGroupRepoNotSet
	}
	if len(userIDs) == 0 {
		return nil
	}
	if err := h.groupRepo.RemoveMembers(ctx, namespace, groupID, userIDs); err != nil {
		h.logger.ErrorKV("移除群组成员失败",
			"namespace", namespace, "group_id", groupID, "users", userIDs, "error", err)
		return err
	}
	h.logger.InfoKV("群组成员移除成功",
		"namespace", namespace, "group_id", groupID, "users", userIDs)

	// 🔔 异步触发群组成员离开回调（复制切片避免调用方后续修改）
	if h.groupMemberLeaveCallback != nil {
		ns, gid := namespace, groupID
		uids := append([]string(nil), userIDs...)
		h.workerPool.TrySubmitCallback(func() {
			h.groupMemberLeaveCallback(context.Background(), ns, gid, uids)
		})
	}
	return nil
}

// GetGroupMembers 获取群组所有成员ID
func (h *Hub) GetGroupMembers(ctx context.Context, namespace, groupID string) ([]string, error) {
	if h.groupRepo == nil {
		return nil, ErrGroupRepoNotSet
	}
	return h.groupRepo.GetMembers(ctx, namespace, groupID)
}

// GetUserGroups 获取用户在指定命名空间下加入的所有群组ID
func (h *Hub) GetUserGroups(ctx context.Context, namespace, userID string) ([]string, error) {
	if h.groupRepo == nil {
		return nil, ErrGroupRepoNotSet
	}
	return h.groupRepo.GetUserGroups(ctx, namespace, userID)
}

// IsGroupMember 判断用户是否为群组成员
func (h *Hub) IsGroupMember(ctx context.Context, namespace, groupID, userID string) (bool, error) {
	if h.groupRepo == nil {
		return false, ErrGroupRepoNotSet
	}
	return h.groupRepo.IsMember(ctx, namespace, groupID, userID)
}

// GetGroupMemberCount 获取群组成员数量
func (h *Hub) GetGroupMemberCount(ctx context.Context, namespace, groupID string) (int64, error) {
	if h.groupRepo == nil {
		return 0, ErrGroupRepoNotSet
	}
	return h.groupRepo.GetMemberCount(ctx, namespace, groupID)
}

// GetNamespaceGroups 获取命名空间下所有群组ID
func (h *Hub) GetNamespaceGroups(ctx context.Context, namespace string) ([]string, error) {
	if h.groupRepo == nil {
		return nil, ErrGroupRepoNotSet
	}
	return h.groupRepo.GetNamespaceGroups(ctx, namespace)
}

// ============================================================================
// 群组消息投递
// ============================================================================

// SendToGroup 向群组发送消息（可靠投递）
//
// namespace 与 groupID 共同定位群组（空值默认 "default"）
//
// 投递策略：
//   - 在线成员：通过 SendToUserWithRetry 投递（自动支持跨节点路由与重试）
//   - 离线成员：通过离线消息处理器存储，上线后自动推送
//   - excludeSender 为 true 时跳过消息发送者本人
//
// 返回 GroupSendResult 包含详细的投递统计
func (h *Hub) SendToGroup(ctx context.Context, namespace, groupID string, msg *HubMessage, excludeSender bool) *GroupSendResult {
	result := &GroupSendResult{
		GroupID: groupID,
		Errors:  make([]error, 0),
	}

	if h.groupRepo == nil {
		result.Errors = append(result.Errors, ErrGroupRepoNotSet)
		return result
	}

	// 1. 获取群组成员列表
	members, err := h.groupRepo.GetMembers(ctx, namespace, groupID)
	if err != nil {
		result.Errors = append(result.Errors, err)
		h.logger.ErrorKV("获取群组成员失败",
			"namespace", namespace, "group_id", groupID, "error", err)
		return result
	}

	result.TotalMembers = len(members)
	if result.TotalMembers == 0 {
		return result
	}

	// 2. 过滤发送者（如需）
	filteredMembers := members
	if excludeSender && msg.Sender != "" {
		filteredMembers = mathx.FilterSlice(members, func(id string) bool {
			return id != msg.Sender
		})
		h.logger.DebugContextKV(ctx, "🔄 过滤发送者后的群组成员列表",
			"namespace", namespace,
			"group_id", groupID,
			"original_count", len(members),
			"filtered_count", len(filteredMembers),
			"excluded_sender", msg.Sender,
		)
	}

	if len(filteredMembers) == 0 {
		return result
	}

	// 3. 并发投递消息 + 原子计数（消除序列化预检查 N 次 Redis 在线探测）
	// SendToUserWithRetry 内部已处理在线/离线逻辑，并通过 StoredOffline 标志返回分类信息
	var (
		sent          int64
		storedOffline int64
		failed        int64
		onlineCount   int64
		offlineCount  int64
		errMu         sync.Mutex
	)

	syncx.NewParallelSliceExecutor[string, *SendResult](filteredMembers).
		Execute(func(idx int, uid string) (*SendResult, error) {
			sendResult := h.SendToUserWithRetry(ctx, uid, msg)

			// 原子分类，无锁开销
			if sendResult.StoredOffline {
				atomic.AddInt64(&offlineCount, 1)
				if sendResult.Success {
					atomic.AddInt64(&storedOffline, 1)
				} else {
					atomic.AddInt64(&failed, 1)
				}
			} else {
				atomic.AddInt64(&onlineCount, 1)
				if sendResult.Success {
					atomic.AddInt64(&sent, 1)
				} else {
					atomic.AddInt64(&failed, 1)
				}
			}

			// 仅在有错误时加锁收集错误信息（错误是少数，锁竞争极低）
			if sendResult.FinalError != nil {
				errMu.Lock()
				result.Errors = append(result.Errors, fmt.Errorf("user %s: %w", uid, sendResult.FinalError))
				errMu.Unlock()
			}

			return sendResult, nil
		})

	result.OnlineMembers = int(atomic.LoadInt64(&onlineCount))
	result.OfflineMembers = int(atomic.LoadInt64(&offlineCount))
	result.Sent = int(atomic.LoadInt64(&sent))
	result.StoredOffline = int(atomic.LoadInt64(&storedOffline))
	result.Failed = int(atomic.LoadInt64(&failed))

	// 🔔 通知观察者（异步，三级索引 O(k) 查找命名空间+群组级观察者）
	h.notifyObservers(ctx, msg, namespace, groupID)

	h.logger.InfoKV("✅ 群组消息投递完成",
		"namespace", namespace,
		"group_id", groupID,
		"message_id", msg.MessageID,
		"total_members", result.TotalMembers,
		"online_members", result.OnlineMembers,
		"offline_members", result.OfflineMembers,
		"sent", result.Sent,
		"stored_offline", result.StoredOffline,
		"failed", result.Failed,
		"duration", time.Since(msg.CreateAt),
	)

	return result
}

// ============================================================================
// 群组广播方法
// ============================================================================

// BroadcastToGroupMembers 广播消息给群组在线成员（fire-and-forget）
//
// namespace 与 groupID 共同定位群组（空值默认 "default"）
//
// 与 SendToGroup 的区别：
//   - 广播模式：仅投递当前在线成员，不存储离线消息，无重试，性能最优
//   - 发送模式（SendToGroup）：在线投递 + 离线存储 + 重试，保证可靠送达
//
// 投递策略：
//   - 本地：通过 broadcastToFiltered 预序列化一次，直接 TrySend 给在线群组成员
//   - 跨节点：通过 PubSub 发布到所有节点，各节点本地过滤群组成员后广播
//   - excludeSender 为 true 时跳过消息发送者本人
//
// 返回本地成功投递数（跨节点投递为异步，不计入返回值）
func (h *Hub) BroadcastToGroupMembers(ctx context.Context, namespace, groupID string, msg *HubMessage, excludeSender bool) int {
	if h.groupRepo == nil {
		h.logger.WarnKV("群组仓库未设置，无法广播",
			"namespace", namespace, "group_id", groupID)
		return 0
	}

	// 克隆消息避免并发修改
	msg = msg.Clone()
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 1. 获取群组成员列表
	members, err := h.groupRepo.GetMembers(ctx, namespace, groupID)
	if err != nil {
		h.logger.ErrorKV("群组广播：获取群组成员失败",
			"namespace", namespace, "group_id", groupID, "error", err)
		return 0
	}

	if len(members) == 0 {
		return 0
	}

	// 2. 排除发送者后得到目标成员列表
	targetMembers := members
	if excludeSender && msg.Sender != "" {
		targetMembers = mathx.FilterSlice(members, func(id string) bool {
			return id != msg.Sender
		})
	}

	if len(targetMembers) == 0 {
		return 0
	}

	// 3. 按成员ID查找本地连接并投递（O(m)，m=成员数，不遍历全部连接）
	localCount := h.broadcastToUserIDs(ctx, targetMembers, msg)

	// 🔔 通知观察者（异步，三级索引 O(k) 查找命名空间+群组级观察者）
	h.notifyObservers(ctx, msg, namespace, groupID)

	// 4. 跨节点广播：优先 gRPC 直连，降级 PubSub
	h.crossNodeGroupBroadcast(ctx, namespace, groupID, msg, excludeSender)

	h.logger.InfoKV("📢 群组广播已发起",
		"namespace", namespace,
		"group_id", groupID,
		"message_id", msg.MessageID,
		"total_members", len(members),
		"local_delivered", localCount,
		"grpc_enabled", h.IsGRPCEnabled(),
		"pubsub_enabled", h.pubsub != nil,
	)

	return localCount
}

// crossNodeGroupBroadcast 跨节点群组广播（单群组）
//
// 统一走 OperationTypeGroupsBroadcast 批量路径，单群组作为 GroupIDs=[groupID] 的特例
// 提交到 clusterBatcher 批量处理，消除 per-message goroutine
func (h *Hub) crossNodeGroupBroadcast(ctx context.Context, namespace, groupID string, msg *HubMessage, excludeSender bool) {
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return // 单机模式，无需跨节点
	}

	senderID := ""
	if excludeSender {
		senderID = msg.Sender
	}

	opts := ClusterDispatchOptions{
		Operation:     OperationTypeGroupsBroadcast,
		Namespace:     namespace,
		GroupIDs:      []string{groupID}, // 单群组是批量的特例，统一走批量路径
		ExcludeSender: excludeSender,
		SenderID:      senderID,
	}

	if !h.clusterBatcher.Submit(msg, opts) {
		h.logger.WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点群组广播",
			"namespace", namespace, "group_id", groupID,
			"message_id", msg.MessageID)
	}
}

// ============================================================================
// 群组广播快捷方法 - 统一走 BroadcastToGroupMembers，一套逻辑
// ============================================================================

// BroadcastToGroup 向指定命名空间的指定群组广播消息
// 便捷方法：显式传入 namespace，委托给 BroadcastToGroupMembers
// namespace 为空时自动填充 "default"
func (h *Hub) BroadcastToGroup(ctx context.Context, namespace, groupID string, msg *HubMessage, excludeSender bool) int {
	return h.BroadcastToGroupMembers(ctx, namespace, groupID, msg, excludeSender)
}

// batchGetGroupMembers 批量获取多个群组成员并合并去重
// 使用 Redis Pipeline 一次 RTT 获取所有群组成员，O(totalMembers) 去重
// 相比逐群组 N 次 GetMembers（N 次 RTT），降为 1 次 RTT
func (h *Hub) batchGetGroupMembers(ctx context.Context, namespace string, groupIDs []string) map[string]struct{} {
	memberSet := make(map[string]struct{})
	if len(groupIDs) == 0 || h.groupRepo == nil {
		return memberSet
	}

	groupMembers, err := h.groupRepo.GetMultiGroupMembers(ctx, namespace, groupIDs)
	if err != nil {
		h.logger.WarnKV("批量获取群组成员失败",
			"namespace", namespace, "group_count", len(groupIDs), "error", err)
		return memberSet
	}

	for _, members := range groupMembers {
		for _, uid := range members {
			memberSet[uid] = struct{}{}
		}
	}
	return memberSet
}

// BroadcastToAllGroups 向指定命名空间的所有群组广播消息（高性能版）
//
// 性能优化（相比逐群组串行广播）：
//   - Redis Pipeline 批量查询：N 群组从 N 次 RTT 降为 1 次 RTT
//   - 成员去重：用户在多个群组只收一条消息
//   - 一次本地过滤：N 次 broadcastToFiltered 降为 1 次
//   - 一次跨节点路由：N 次 routeToCluster 降为 1 次（携带所有 groupIDs）
//
// namespace 为空时自动填充 "default"
func (h *Hub) BroadcastToAllGroups(ctx context.Context, namespace string, msg *HubMessage) int {
	if h.groupRepo == nil {
		h.logger.WarnContextKV(ctx, "群组仓库未设置，无法广播", "namespace", namespace)
		return 0
	}

	// 1. 获取命名空间所有群组ID
	groupIDs, err := h.groupRepo.GetNamespaceGroups(ctx, namespace)
	if err != nil {
		h.logger.WarnKV("获取命名空间群组列表失败", "namespace", namespace, "error", err)
		return 0
	}
	if len(groupIDs) == 0 {
		return 0
	}

	// 2. Pipeline 批量获取所有群组成员，合并去重
	memberSet := h.batchGetGroupMembers(ctx, namespace, groupIDs)
	if len(memberSet) == 0 {
		return 0
	}

	// 3. 准备消息
	msg = msg.Clone()
	if msg.CreateAt.IsZero() {
		msg.CreateAt = time.Now()
	}

	// 4. 按成员ID查找本地连接并投递（O(m) 替代 O(n) 全连接扫描）
	memberList := make([]string, 0, len(memberSet))
	for uid := range memberSet {
		memberList = append(memberList, uid)
	}
	localCount := h.broadcastToUserIDs(ctx, memberList, msg)

	// 5. 一次跨节点路由（携带所有 groupIDs，接收端批量处理）
	h.crossNodeGroupsBroadcast(ctx, namespace, groupIDs, msg)

	h.logger.DebugContextKV(ctx, "命名空间全群组广播完成",
		"namespace", namespace,
		"group_count", len(groupIDs),
		"unique_members", len(memberSet),
		"local_delivered", localCount,
		"message_id", msg.MessageID,
	)

	return localCount
}

// crossNodeGroupsBroadcast 跨节点批量群组广播（一次路由）
// 携带所有 groupIDs，接收端 Pipeline 批量查询 + 去重 + 一次过滤
// 提交到 clusterBatcher 批量处理，消除 per-message goroutine
func (h *Hub) crossNodeGroupsBroadcast(ctx context.Context, namespace string, groupIDs []string, msg *HubMessage) {
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return // 单机模式，无需跨节点
	}

	opts := ClusterDispatchOptions{
		Operation: OperationTypeGroupsBroadcast,
		Namespace: namespace,
		GroupIDs:  groupIDs,
	}

	if !h.clusterBatcher.Submit(msg, opts) {
		h.logger.WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点批量群组广播",
			"namespace", namespace, "group_count", len(groupIDs),
			"message_id", msg.MessageID)
	}
}

// BroadcastToAllNamespacesAllGroups 向所有命名空间的所有群组广播消息（高性能版）
//
// 性能优化：
//   - 并行处理各命名空间（背压控制：最大并发 10，避免打满 Redis 连接池）
//   - 每个命名空间走 BroadcastToAllGroups（Pipeline + 去重 + 一次路由）
//   - 故障隔离：单个命名空间失败不影响其他命名空间
func (h *Hub) BroadcastToAllNamespacesAllGroups(ctx context.Context, msg *HubMessage) int {
	if h.groupRepo == nil {
		h.logger.WarnKV("群组仓库未设置，无法广播")
		return 0
	}

	namespaces, err := h.groupRepo.GetAllNamespaces(ctx)
	if err != nil {
		h.logger.WarnKV("获取所有命名空间失败", "error", err)
		return 0
	}
	if len(namespaces) == 0 {
		return 0
	}

	// 并行处理各命名空间（背压控制：最大并发 10）
	var (
		totalDelivered int64
		wg             sync.WaitGroup
		sem            = make(chan struct{}, 10)
	)

	for _, namespace := range namespaces {
		wg.Add(1)
		sem <- struct{}{}
		go func(ns string) {
			defer wg.Done()
			defer func() { <-sem }()

			msgCopy := msg.Clone()
			delivered := h.BroadcastToAllGroups(ctx, ns, msgCopy)
			atomic.AddInt64(&totalDelivered, int64(delivered))
		}(namespace)
	}
	wg.Wait()

	total := int(atomic.LoadInt64(&totalDelivered))
	h.logger.DebugContextKV(ctx, "全命名空间全群组广播完成",
		"namespace_count", len(namespaces),
		"total_delivered", total,
		"message_id", msg.MessageID,
	)
	return total
}

// BroadcastToGroups 统一批量群组广播（namespaces 和 groupIDs 可选传，支持多种组合）
//
// 组合语义：
//   - 两者都传：广播给指定命名空间的指定群组（通过反向映射校验 groupID 归属）
//   - 只传 groupIDs：通过反向映射 group:{groupID}→namespace 反查命名空间，广播给这些群组
//   - 只传 namespaces：广播给这些命名空间的所有群组
//   - 都不传：广播给所有命名空间的所有群组（等价 BroadcastToAllNamespacesAllGroups）
//
// 性能优化：
//   - 按命名空间 Pipeline 批量获取成员（每命名空间 1 次 RTT）
//   - 合并去重（用户跨群组只收一条）
//   - 一次本地过滤广播
//   - 按命名空间分组跨节点路由（每命名空间一条消息，携带该命名空间的 GroupIDs）
func (h *Hub) BroadcastToGroups(ctx context.Context, namespaces, groupIDs []string, msg *HubMessage) int {
	if h.groupRepo == nil {
		h.logger.WarnKV("群组仓库未设置，无法广播")
		return 0
	}

	// 1. 解析目标 (namespace → []groupID) 映射
	namespaceGroups, err := h.resolveTargetGroups(ctx, namespaces, groupIDs)
	if err != nil {
		h.logger.WarnKV("BroadcastToGroups 解析目标群组失败", "error", err)
		return 0
	}
	if len(namespaceGroups) == 0 {
		return 0
	}

	// 2. 按命名空间 Pipeline 批量获取成员，合并去重（用户跨群组只收一条）
	memberSet := make(map[string]struct{})
	for namespace, gids := range namespaceGroups {
		members, mErr := h.groupRepo.GetMultiGroupMembers(ctx, namespace, gids)
		if mErr != nil {
			h.logger.DebugKV("批量获取群组成员失败，跳过该命名空间",
				"namespace", namespace, "error", mErr)
			continue
		}
		for _, members := range members {
			for _, uid := range members {
				memberSet[uid] = struct{}{}
			}
		}
	}
	if len(memberSet) == 0 {
		return 0
	}

	// 3. 按成员ID查找本地连接并投递（O(m) 替代 O(n) 全连接扫描）
	memberList := make([]string, 0, len(memberSet))
	for uid := range memberSet {
		memberList = append(memberList, uid)
	}
	localCount := h.broadcastToUserIDs(ctx, memberList, msg)

	// 4. 按命名空间分组跨节点路由（每命名空间一条消息，携带该命名空间的 GroupIDs）
	h.crossNodeMultiNamespaceGroupsBroadcast(ctx, namespaceGroups, msg)

	h.logger.DebugKV("BroadcastToGroups 完成",
		"namespace_count", len(namespaceGroups),
		"local_delivered", localCount,
		"message_id", msg.MessageID,
	)
	return localCount
}

// resolveTargetGroups 解析目标群组映射（namespace → []groupID）
// 根据 namespaces 和 groupIDs 的四种组合，返回需要广播的群组按命名空间分组
func (h *Hub) resolveTargetGroups(ctx context.Context, namespaces, groupIDs []string) (map[string][]string, error) {
	result := make(map[string][]string)

	switch {
	case len(namespaces) == 0 && len(groupIDs) == 0:
		// 都不传：所有命名空间的所有群组
		namespaces, err := h.groupRepo.GetAllNamespaces(ctx)
		if err != nil {
			return nil, err
		}
		for _, ns := range namespaces {
			gids, err := h.groupRepo.GetNamespaceGroups(ctx, ns)
			if err != nil {
				continue
			}
			if len(gids) > 0 {
				result[ns] = gids
			}
		}

	case len(namespaces) > 0 && len(groupIDs) == 0:
		// 只传 namespaces：这些命名空间的所有群组
		for _, ns := range namespaces {
			gids, err := h.groupRepo.GetNamespaceGroups(ctx, ns)
			if err != nil {
				continue
			}
			if len(gids) > 0 {
				result[ns] = gids
			}
		}

	case len(namespaces) == 0 && len(groupIDs) > 0:
		// 只传 groupIDs：通过反向映射反查命名空间
		groupNamespaces, err := h.groupRepo.GetMultiGroupNamespaces(ctx, groupIDs)
		if err != nil {
			return nil, err
		}
		for gid, ns := range groupNamespaces {
			result[ns] = append(result[ns], gid)
		}

	default:
		// 都传：指定命名空间的指定群组（通过反向映射校验 groupID 归属）
		groupNamespaces, err := h.groupRepo.GetMultiGroupNamespaces(ctx, groupIDs)
		if err != nil {
			return nil, err
		}
		namespaceSet := make(map[string]struct{}, len(namespaces))
		for _, ns := range namespaces {
			namespaceSet[ns] = struct{}{}
		}
		for gid, ns := range groupNamespaces {
			if _, ok := namespaceSet[ns]; ok {
				result[ns] = append(result[ns], gid)
			}
		}
	}

	return result, nil
}

// crossNodeMultiNamespaceGroupsBroadcast 按命名空间分组跨节点路由
// 每个命名空间提交一条到 clusterBatcher，携带该命名空间的 GroupIDs（接收端批量处理）
func (h *Hub) crossNodeMultiNamespaceGroupsBroadcast(ctx context.Context, namespaceGroups map[string][]string, msg *HubMessage) {
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return // 单机模式，无需跨节点
	}
	for namespace, groupIDs := range namespaceGroups {
		opts := ClusterDispatchOptions{
			Operation: OperationTypeGroupsBroadcast,
			Namespace: namespace,
			GroupIDs:  groupIDs,
		}
		if !h.clusterBatcher.Submit(msg, opts) {
			h.logger.WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点多命名空间群组广播",
				"namespace", namespace, "message_id", msg.MessageID)
		}
	}
}

// ============================================================================
// 系统保留组自动管理（agent/observer 统一到 group 体系）
//
// 设计：本地分片索引（agentShards/observerShards）保留做 O(1) 缓存，
//   Redis 系统组（__agents__/__observers__）用于跨节点共享成员关系与显式广播
//   连接注册时自动加入系统组，断开时自动离开，业务无感
// ============================================================================

// systemGroupOfUserType 返回用户类型对应的系统保留组（无则空）
// agent/bot → __agents__，observer → __observers__，其余不加入系统组
func systemGroupOfUserType(ut models.UserType) string {
	switch ut {
	case models.UserTypeAgent, models.UserTypeBot:
		return models.SystemGroupAgents
	case models.UserTypeObserver:
		return models.SystemGroupObservers
	default:
		return ""
	}
}

// joinSystemGroupsOnConnect 客户端连接时自动加入系统保留组
// client.Namespace 已在 handleRegister 归一化（全局观察者保持 ""），此处直接使用
func (h *Hub) joinSystemGroupsOnConnect(ctx context.Context, client *Client) {
	if h.groupRepo == nil {
		return
	}
	groupID := systemGroupOfUserType(client.UserType)
	if groupID == "" {
		return
	}
	h.ensureAndJoinSystemGroup(ctx, client.Namespace, groupID, client.UserID)
}

// leaveSystemGroupsOnDisconnect 客户端断开时自动离开系统保留组
//
// 多端登录保护：仅当该 userID 已无任何在线连接时才离开系统组
// 调用时当前 client 已由 removeClientUnsafe（handleUnregister Phase 1）从注册表移除，
// 因此 HasUser 查询的是"移除当前连接后是否还存在其他在线连接"
// 竞态可接受：RemoveMembers 对不存在成员幂等，重连时 joinSystemGroupsOnConnect 会重新加入
func (h *Hub) leaveSystemGroupsOnDisconnect(ctx context.Context, client *Client) {
	if h.groupRepo == nil {
		return
	}
	groupID := systemGroupOfUserType(client.UserType)
	if groupID == "" {
		return
	}
	// 该 userID 仍有其他在线连接时保留系统组成员身份，避免多端场景下其他端收不到系统组广播
	if h.shardedRegistry.HasUser(client.UserID) {
		h.logger.DebugContextKV(ctx, "用户仍有其他在线连接，保留系统组成员身份",
			"user_id", client.UserID, "group_id", groupID)
		return
	}
	if err := h.groupRepo.RemoveMembers(ctx, client.Namespace, groupID, []string{client.UserID}); err != nil {
		h.logger.DebugContextKV(ctx, "离开系统组失败",
			"user_id", client.UserID, "group_id", groupID, "error", err)
	}
}

// ensureAndJoinSystemGroup 确保系统组存在并加入成员
// namespace 已在 register 时归一化（全局观察者保持 ""），此处直接使用
// 加入成功后触发 OnGroupMemberJoin 回调（observer/agent 自动入群也通知业务层）
func (h *Hub) ensureAndJoinSystemGroup(ctx context.Context, namespace, groupID, userID string) {
	if err := h.groupRepo.EnsureSystemGroup(ctx, namespace, groupID); err != nil {
		h.logger.WarnKV("ensureSystemGroup 失败",
			"namespace", namespace, "group_id", groupID, "error", err)
		return
	}
	if err := h.groupRepo.AddMembers(ctx, namespace, groupID, []string{userID}); err != nil {
		h.logger.WarnKV("加入系统组失败",
			"namespace", namespace, "group_id", groupID, "user_id", userID, "error", err)
		return
	}
	// 🔔 触发群组成员加入回调（observer/agent 自动入群也通知业务层）
	h.triggerGroupMemberJoinCallback(namespace, groupID, []string{userID})
}

// BroadcastToNamespace 向指定命名空间的所有连接广播消息（不限群组）
// namespace 为空时自动填充 "default"
// 本地按命名空间过滤广播 + 跨节点命名空间广播（提交到 clusterBatcher）
func (h *Hub) BroadcastToNamespace(ctx context.Context, namespace string, msg *HubMessage) int {
	msg = msg.Clone()
	// 本地按命名空间过滤广播
	count := h.broadcastToFiltered(ctx, func(c *Client) bool {
		return c.Namespace == namespace
	}, msg)
	// 跨节点命名空间广播（提交到 clusterBatcher 批量处理）
	opts := ClusterDispatchOptions{
		Operation: OperationTypeBroadcast,
		Namespace: namespace,
	}
	if !h.clusterBatcher.Submit(msg, opts) {
		h.logger.WarnContextKV(ctx, "集群分发队列已满，丢弃跨节点命名空间广播",
			"namespace", namespace, "message_id", msg.MessageID)
	}
	return count
}

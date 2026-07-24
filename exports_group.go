/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-22 15:00:00
 * @FilePath: \go-wsc\exports_group.go
 * @Description: 群组模块类型与函数导出
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/repository"
)

// ============================================
// 命名空间常量
// ============================================

// DefaultNamespace 默认命名空间ID（类似 k8s default namespace）
const DefaultNamespace = models.DefaultNamespace

// ============================================
// 系统保留组（agent/observer 统一到 group 体系）
// ============================================

// 系统保留组常量（__ 前缀，业务组禁止使用）
// agent 连接自动加入 __agents__，observer 连接自动加入 __observers__
// 本地分片索引（agentShards/observerShards）保留做 O(1) 缓存，
// Redis 系统组用于跨节点共享成员关系与显式广播
const (
	SystemGroupAgents    = models.SystemGroupAgents    // 客服系统组（每命名空间一个）
	SystemGroupObservers = models.SystemGroupObservers // 观察者系统组（全局 namespace="" 或命名空间级）
)

// IsSystemGroup 判断 groupID 是否为系统保留组（__ 前缀）
var IsSystemGroup = models.IsSystemGroup

// ============================================
// 群组模型类型
// ============================================

// Group 群组模型
type Group = models.Group

// GroupSendResult 群组消息投递结果
type GroupSendResult = models.GroupSendResult

// ============================================
// 群组仓库类型与构造函数
// ============================================

// GroupRepository 群组仓库接口
type GroupRepository = repository.GroupRepository

// RedisGroupRepository Redis 群组仓库实现
type RedisGroupRepository = repository.RedisGroupRepository

// NewRedisGroupRepository 创建 Redis 群组仓库
var NewRedisGroupRepository = repository.NewRedisGroupRepository

// ============================================
// 群组相关错误
// ============================================

var (
	// ErrGroupNotFound 群组未找到
	ErrGroupNotFound = models.ErrGroupNotFound
	// ErrGroupMemberExisted 用户已是群组成员
	ErrGroupMemberExisted = models.ErrGroupMemberExisted
	// ErrGroupFull 群组已满
	ErrGroupFull = models.ErrGroupFull
	// ErrGroupRepoNotSet 群组仓库未设置
	ErrGroupRepoNotSet = models.ErrGroupRepoNotSet
	// ErrGroupExisted 群组已存在（同命名空间下 groupID 唯一）
	ErrGroupExisted = models.ErrGroupExisted
	// ErrGroupReserved 群组名为系统保留名（__ 前缀）
	ErrGroupReserved = models.ErrGroupReserved
)

// ============================================
// Hub 群组方法（通过 Hub 实例调用）
// ============================================

// 注意：以下是 Hub 类型的群组方法列表，通过 Hub 实例调用
// 例如：hub := wsc.NewHub(config); hub.CreateGroup(ctx, group)
//
// 层级结构：Namespace（默认 "default"，类似 k8s namespace）→ Group → Members
// 群组管理方法中 namespace 为空时自动使用 "default"
// 消息投递方法中 namespace 由参数显式传入（HubMessage 不携带路由元数据）
//
// 群组管理方法：
// - CreateGroup(ctx context.Context, group *Group) error: 创建群组（group.Namespace 为空时默认 "default"）
// - GetGroup(ctx context.Context, namespace, groupID string) (*Group, error): 获取群组元信息
// - DisbandGroup(ctx context.Context, namespace, groupID string) error: 解散群组
// - AddGroupMembers(ctx context.Context, namespace, groupID string, userIDs []string) error: 添加群组成员
// - RemoveGroupMembers(ctx context.Context, namespace, groupID string, userIDs []string) error: 移除群组成员
// - GetGroupMembers(ctx context.Context, namespace, groupID string) ([]string, error): 获取群组所有成员ID
// - GetUserGroups(ctx context.Context, namespace, userID string) ([]string, error): 获取用户在指定命名空间下加入的所有群组ID
// - IsGroupMember(ctx context.Context, namespace, groupID, userID string) (bool, error): 判断用户是否为群组成员
// - GetGroupMemberCount(ctx context.Context, namespace, groupID string) (int64, error): 获取群组成员数量
// - GetNamespaceGroups(ctx context.Context, namespace string) ([]string, error): 获取命名空间下所有群组ID
// - GetGroupRepository() GroupRepository: 获取群组仓库
//
// 群组消息投递方法（namespace 由参数传入，HubMessage 不携带路由元数据）：
// - SendToGroup(ctx context.Context, namespace, groupID string, msg *HubMessage, excludeSender bool) *GroupSendResult: 向群组发送消息（可靠投递，含离线存储与重试）
// - BroadcastToGroupMembers(ctx context.Context, namespace, groupID string, msg *HubMessage, excludeSender bool) int: 向群组在线成员广播（fire-and-forget，性能最优）
// - BroadcastToGroup(ctx context.Context, namespace, groupID string, msg *HubMessage, excludeSender bool) int: 向指定命名空间的指定群组广播（便捷方法，委托给 BroadcastToGroupMembers）
// - BroadcastToAllGroups(ctx context.Context, namespace string, msg *HubMessage) int: 向指定命名空间的所有群组广播（Pipeline 批量查询+成员去重+一次路由）
// - BroadcastToAllNamespacesAllGroups(ctx context.Context, msg *HubMessage) int: 向所有命名空间的所有群组广播（并行命名空间处理，背压控制最大并发 10）
// - BroadcastToGroups(ctx context.Context, namespaces, groupIDs []string, msg *HubMessage) int: 统一批量群组广播（namespaces/groupIDs 可选传，反向映射支持只传 groupID 反查命名空间）
// - BroadcastToNamespace(ctx context.Context, namespace string, msg *HubMessage) int: 向指定命名空间的所有连接广播（不限群组）
//
// 观察者方法（支持命名空间隔离）：
// - GetObserverClientsByNamespace(namespace string) []*Client: 获取指定命名空间的观察者客户端（含全局观察者，Namespace 为空）

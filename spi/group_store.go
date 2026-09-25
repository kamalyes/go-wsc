/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 00:30:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 00:30:00
 * @FilePath: \go-wsc\spi\group_store.go
 * @Description: 群组存储 SPI - GroupStore 接口契约定义
 *
 * 群组元信息、成员关系、命名空间索引的读写契约
 * Redis 实现见 adapter/redis 包 GroupStore
 *
 * 隔离维度：appID（最上层）> namespace > groupID
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"context"

	"github.com/kamalyes/go-wsc/models"
)

// GroupStore 群组存储接口
type GroupStore interface {
	// CreateGroup 创建群组（存储元信息，若已存在则覆盖）
	// group.AppID/Namespace 为空时自动填充默认值
	CreateGroup(ctx context.Context, group *models.Group) error

	// GetGroup 获取群组元信息
	GetGroup(ctx context.Context, appID, namespace, groupID string) (*models.Group, error)

	// DisbandGroup 解散群组（删除元信息、成员集合、命名空间索引及各成员的反向索引）
	DisbandGroup(ctx context.Context, appID, namespace, groupID string) error

	// AddMembers 添加成员到群组（同时更新成员的反向索引与命名空间索引）
	AddMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error

	// RemoveMembers 从群组移除成员（同时清理成员的反向索引）
	RemoveMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error

	// GetMembers 获取群组所有成员ID
	GetMembers(ctx context.Context, appID, namespace, groupID string) ([]string, error)

	// GetUserGroups 获取用户在指定 (appID, namespace) 下加入的所有群组ID
	GetUserGroups(ctx context.Context, appID, namespace, userID string) ([]string, error)

	// IsMember 判断用户是否为群组成员
	IsMember(ctx context.Context, appID, namespace, groupID, userID string) (bool, error)

	// GetMemberCount 获取群组成员数量
	GetMemberCount(ctx context.Context, appID, namespace, groupID string) (int64, error)

	// GetNamespaceGroups 获取 (appID, namespace) 下所有群组ID
	GetNamespaceGroups(ctx context.Context, appID, namespace string) ([]string, error)

	// GetAllNamespaces 获取指定 appID 下所有有群组的命名空间ID（用于该 app 的全命名空间广播）
	GetAllNamespaces(ctx context.Context, appID string) ([]string, error)

	// GetMultiGroupMembers 批量获取多个群组的成员（跨 namespace 聚合，两段 Pipeline 共 2 次网络往返）
	// 同一 groupID 可在多个 namespace 下各建实例（业务侧按租户建组），本方法按 (appID, groupIDs)
	// 跨 ns 聚合所有实例的成员并合并去重；返回 map[groupID][]memberIDs，无实例的 gid 该 key 缺失
	GetMultiGroupMembers(ctx context.Context, appID string, groupIDs []string) (map[string][]string, error)

	// EnsureSystemGroup 确保系统保留组存在（__ 前缀，agent/observer 自动加入前初始化）
	// 幂等：不存在则创建，已存在则返回 nil
	EnsureSystemGroup(ctx context.Context, appID, namespace, groupID string) error
}

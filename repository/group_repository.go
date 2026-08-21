/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-22 15:00:00
 * @FilePath: \go-wsc\repository\group_repository.go
 * @Description: 群组仓库 - 基于 Redis 实现群组成员关系持久化与跨节点共享
 *
 * 应用隔离 Key 设计（appID 默认 "__default_app__"，最上层隔离维度）：
 *   - {prefix}info:{appID}:{namespace}:{groupID}        → String(JSON) 群组元信息
 *   - {prefix}members:{appID}:{namespace}:{groupID}     → Set 成员 userID 集合
 *   - {prefix}user:{appID}:{namespace}:{userID}         → Set 用户在该 (app,ns) 下加入的 groupID 集合（反向索引）
 *   - {prefix}ns:{appID}:{namespace}:groups             → Set (app,ns) 下所有 groupID 集合
 *   - {prefix}group:{appID}:{groupID}                   → String 反向映射 groupID→namespace（仅 (app) 内唯一）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package repository

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/redis/go-redis/v9"
)

// GroupRepository 群组仓库接口
// appID 是最上层隔离维度（默认 "__default_app__"），namespace 为租户维度，groupID 在 (appID, namespace) 内唯一
type GroupRepository interface {
	// CreateGroup 创建群组（存储元信息，若已存在则覆盖）
	// group.AppID/Namespace 为空时自动填充默认值
	CreateGroup(ctx context.Context, group *Group) error

	// GetGroup 获取群组元信息
	GetGroup(ctx context.Context, appID, namespace, groupID string) (*Group, error)

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

	// GetMultiGroupMembers 批量获取多个群组的成员（Redis Pipeline 一次网络往返）
	// 返回 map[groupID][]memberIDs，单个群组查询失败时该 key 缺失
	GetMultiGroupMembers(ctx context.Context, appID, namespace string, groupIDs []string) (map[string][]string, error)

	// EnsureSystemGroup 确保系统保留组存在（__ 前缀，agent/observer 自动加入前初始化）
	// 幂等：不存在则创建，已存在则返回 nil
	EnsureSystemGroup(ctx context.Context, appID, namespace, groupID string) error

	// GetGroupNamespace 通过 (appID, groupID) 反查命名空间ID（反向映射 group:{appID}:{groupID} → namespace）
	GetGroupNamespace(ctx context.Context, appID, groupID string) (string, error)

	// GetMultiGroupNamespaces 批量反查多个 (appID, groupID) 的命名空间ID（Pipeline 一次往返）
	GetMultiGroupNamespaces(ctx context.Context, appID string, groupIDs []string) (map[string]string, error)
}

// RedisGroupRepository Redis 群组仓库实现
type RedisGroupRepository struct {
	client    redis.UniversalClient
	keyPrefix string
}

// NewRedisGroupRepository 创建 Redis 群组仓库
// keyPrefix 为空时使用 DefaultGroupKeyPrefix
func NewRedisGroupRepository(client redis.UniversalClient, keyPrefix string) GroupRepository {
	return &RedisGroupRepository{
		client:    client,
		keyPrefix: mathx.IfNotEmpty(keyPrefix, DefaultGroupKeyPrefix),
	}
}

// ============================================================================
// Redis Key 生成（应用隔离：appID 为最上层维度）
// ============================================================================

func (r *RedisGroupRepository) infoKey(appID, namespace, groupID string) string {
	return r.keyPrefix + "info:" + appID + ":" + namespace + ":" + groupID
}

func (r *RedisGroupRepository) membersKey(appID, namespace, groupID string) string {
	return r.keyPrefix + "members:" + appID + ":" + namespace + ":" + groupID
}

func (r *RedisGroupRepository) userGroupsKey(appID, namespace, userID string) string {
	return r.keyPrefix + "user:" + appID + ":" + namespace + ":" + userID
}

func (r *RedisGroupRepository) namespaceGroupsKey(appID, namespace string) string {
	return r.keyPrefix + "ns:" + appID + ":" + namespace + ":groups"
}

// groupNamespaceKey 群组反向映射 key：(appID, groupID) → namespace（groupID 现在仅在 (appID, namespace) 内唯一）
func (r *RedisGroupRepository) groupNamespaceKey(appID, groupID string) string {
	return r.keyPrefix + "group:" + appID + ":" + groupID
}

// ============================================================================
// 群组元信息管理
// ============================================================================

// createGroupScript Lua 脚本：原子性地校验同 (appID, namespace) 下 groupID 唯一并写入元信息与命名空间索引
// 返回 1 表示创建成功，0 表示群组已存在
const createGroupScript = `
if redis.call("exists", KEYS[1]) == 1 then
	return 0
end
redis.call("set", KEYS[1], ARGV[1])
redis.call("sadd", KEYS[2], ARGV[2])
return 1
`

// CreateGroup 创建业务群组
// 禁止使用系统保留名（__ 前缀），同 (appID, namespace) groupID 唯一，重复创建返回 ErrGroupExisted
func (r *RedisGroupRepository) CreateGroup(ctx context.Context, group *Group) error {
	if group == nil || group.GroupID == "" {
		return errorx.WrapError("group or groupID cannot be empty")
	}
	// appID 归一化：空→DefaultAppID（最上层隔离维度必填）
	if group.AppID == "" {
		group.AppID = models.DefaultAppID
	}
	// 命名空间归一化：空→"default"
	if group.Namespace == "" {
		group.Namespace = models.DefaultNamespace
	}
	// 业务组禁止使用系统保留名（__ 前缀）
	if IsSystemGroup(group.GroupID) {
		return ErrGroupReserved
	}
	return r.createGroupUnchecked(ctx, group)
}

// createGroupUnchecked 创建群组（不校验保留名，系统组专用）
func (r *RedisGroupRepository) createGroupUnchecked(ctx context.Context, group *Group) error {
	if group.CreatedAt.IsZero() {
		group.CreatedAt = time.Now()
	}
	appID := group.GetAppID()
	namespace := group.GetNamespace()
	data, err := json.Marshal(group)
	if err != nil {
		return errorx.WrapError("marshal group failed", err)
	}
	result, err := r.client.Eval(ctx, createGroupScript,
		[]string{r.infoKey(appID, namespace, group.GroupID), r.namespaceGroupsKey(appID, namespace)},
		data, group.GroupID,
	).Result()
	if err != nil {
		return errorx.WrapError("create group failed", err)
	}
	n, ok := result.(int64)
	if !ok || n == 0 {
		return ErrGroupExisted
	}
	// 写入反向映射 ((appID, groupID)→namespace)，供只传 groupID 的批量广播反查命名空间
	if err := r.client.Set(ctx, r.groupNamespaceKey(appID, group.GroupID), namespace, 0).Err(); err != nil {
		return errorx.WrapError("set group namespace mapping failed", err)
	}
	return nil
}

// EnsureSystemGroup 确保系统保留组存在（agent/observer 自动加入前初始化）
//
// 幂等：不存在则创建，已存在则返回 nil仅允许 __ 前缀系统组名
// 复用 createGroupScript，返回 0（已存在）/1（新建）均视为成功，天然处理并发竞态
func (r *RedisGroupRepository) EnsureSystemGroup(ctx context.Context, appID, namespace, groupID string) error {
	if !IsSystemGroup(groupID) {
		return ErrGroupReserved
	}
	// appID 归一化（空→DefaultAppID，最上层隔离维度必填）；namespace 保持原值不归一化
	// 系统组支持 namespace="" 全局语义（全局观察者 tenant=""），归一化会破坏全局与 default 命名空间的隔离
	appID = mathx.IfEmpty(appID, models.DefaultAppID)
	group := &Group{
		AppID:     appID,
		GroupID:   groupID,
		Namespace: namespace,
		Name:      groupID,
		OwnerID:   models.UserTypeSystem.String(),
		CreatedAt: time.Now(),
	}
	data, err := json.Marshal(group)
	if err != nil {
		return errorx.WrapError("marshal system group failed", err)
	}
	if _, err := r.client.Eval(ctx, createGroupScript,
		[]string{r.infoKey(appID, namespace, groupID), r.namespaceGroupsKey(appID, namespace)},
		data, groupID,
	).Result(); err != nil {
		return errorx.WrapError("ensure system group failed", err)
	}
	// 写入反向映射 ((appID, groupID)→namespace)；幂等，已存在时也补写以兼容旧数据
	if err := r.client.Set(ctx, r.groupNamespaceKey(appID, groupID), namespace, 0).Err(); err != nil {
		return errorx.WrapError("set system group namespace mapping failed", err)
	}
	return nil // 0=已存在（幂等） 1=新建，均成功
}

// GetGroup 获取群组元信息
func (r *RedisGroupRepository) GetGroup(ctx context.Context, appID, namespace, groupID string) (*Group, error) {
	data, err := r.client.Get(ctx, r.infoKey(appID, namespace, groupID)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	var group Group
	if err := json.Unmarshal([]byte(data), &group); err != nil {
		return nil, errorx.WrapError("unmarshal group failed", err)
	}
	return &group, nil
}

// DisbandGroup 解散群组
func (r *RedisGroupRepository) DisbandGroup(ctx context.Context, appID, namespace, groupID string) error {
	// 先获取成员列表，用于清理各成员的反向索引
	members, err := r.GetMembers(ctx, appID, namespace, groupID)
	if err != nil && err != ErrGroupNotFound {
		return err
	}

	// Pipeline 批量删除：元信息 + 成员集合 + 命名空间索引 + 反向映射 + 各成员反向索引
	pipe := r.client.Pipeline()
	pipe.Del(ctx, r.infoKey(appID, namespace, groupID))
	pipe.Del(ctx, r.membersKey(appID, namespace, groupID))
	pipe.Del(ctx, r.groupNamespaceKey(appID, groupID))
	pipe.SRem(ctx, r.namespaceGroupsKey(appID, namespace), groupID)
	for _, userID := range members {
		pipe.SRem(ctx, r.userGroupsKey(appID, namespace, userID), groupID)
	}
	_, err = pipe.Exec(ctx)
	return err
}

// ============================================================================
// 成员管理
// ============================================================================

// AddMembers 添加成员到群组
func (r *RedisGroupRepository) AddMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error {
	if groupID == "" {
		return errorx.WrapError("groupID cannot be empty")
	}
	if len(userIDs) == 0 {
		return nil
	}
	pipe := r.client.Pipeline()
	// 成员集合
	membersArgs := make([]any, 0, len(userIDs))
	for _, uid := range userIDs {
		membersArgs = append(membersArgs, uid)
	}
	pipe.SAdd(ctx, r.membersKey(appID, namespace, groupID), membersArgs...)
	// 各成员反向索引
	for _, uid := range userIDs {
		pipe.SAdd(ctx, r.userGroupsKey(appID, namespace, uid), groupID)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// RemoveMembers 从群组移除成员
func (r *RedisGroupRepository) RemoveMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error {
	if groupID == "" {
		return errorx.WrapError("groupID cannot be empty")
	}
	if len(userIDs) == 0 {
		return nil
	}
	pipe := r.client.Pipeline()
	membersArgs := make([]any, 0, len(userIDs))
	for _, uid := range userIDs {
		membersArgs = append(membersArgs, uid)
	}
	pipe.SRem(ctx, r.membersKey(appID, namespace, groupID), membersArgs...)
	for _, uid := range userIDs {
		pipe.SRem(ctx, r.userGroupsKey(appID, namespace, uid), groupID)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// GetMembers 获取群组所有成员ID
func (r *RedisGroupRepository) GetMembers(ctx context.Context, appID, namespace, groupID string) ([]string, error) {
	members, err := r.client.SMembers(ctx, r.membersKey(appID, namespace, groupID)).Result()
	if err != nil {
		return nil, err
	}
	return members, nil
}

// GetUserGroups 获取用户在指定 (appID, namespace) 下加入的所有群组ID
func (r *RedisGroupRepository) GetUserGroups(ctx context.Context, appID, namespace, userID string) ([]string, error) {
	return r.client.SMembers(ctx, r.userGroupsKey(appID, namespace, userID)).Result()
}

// IsMember 判断用户是否为群组成员
func (r *RedisGroupRepository) IsMember(ctx context.Context, appID, namespace, groupID, userID string) (bool, error) {
	n, err := r.client.SIsMember(ctx, r.membersKey(appID, namespace, groupID), userID).Result()
	return n, err
}

// GetMemberCount 获取群组成员数量
func (r *RedisGroupRepository) GetMemberCount(ctx context.Context, appID, namespace, groupID string) (int64, error) {
	return r.client.SCard(ctx, r.membersKey(appID, namespace, groupID)).Result()
}

// GetNamespaceGroups 获取 (appID, namespace) 下所有群组ID
func (r *RedisGroupRepository) GetNamespaceGroups(ctx context.Context, appID, namespace string) ([]string, error) {
	return r.client.SMembers(ctx, r.namespaceGroupsKey(appID, namespace)).Result()
}

// GetAllNamespaces 获取指定 appID 下所有有群组的命名空间ID
// 通过 SCAN {prefix}ns:{appID}:*:groups 提取 namespace
func (r *RedisGroupRepository) GetAllNamespaces(ctx context.Context, appID string) ([]string, error) {
	appID = mathx.IfEmpty(appID, models.DefaultAppID)
	// pattern {prefix}ns:{appID}:*:groups，中段 * 匹配 namespace
	pattern := r.keyPrefix + "ns:" + appID + ":*:groups"
	var namespaces []string
	iter := r.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		key := iter.Val()
		// 从 key {prefix}ns:{appID}:{namespace}:groups 中提取 namespace
		// 先去前缀 {prefix}ns:{appID}: 再去后缀 :groups
		trimmed := strings.TrimPrefix(key, r.keyPrefix+"ns:"+appID+":")
		namespace := strings.TrimSuffix(trimmed, ":groups")
		if namespace != "" {
			namespaces = append(namespaces, namespace)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scan namespaces failed: %w", err)
	}
	return namespaces, nil
}

// GetMultiGroupMembers 批量获取多个群组的成员（Redis Pipeline 一次网络往返）
// 相比逐群组 SMEMBERS，N 个群组从 N 次 RTT 降为 1 次 RTT
// 返回 map[groupID][]memberIDs，单个群组查询失败时该 key 缺失
func (r *RedisGroupRepository) GetMultiGroupMembers(ctx context.Context, appID, namespace string, groupIDs []string) (map[string][]string, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}

	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringSliceCmd, len(groupIDs))
	for i, gid := range groupIDs {
		cmds[i] = pipe.SMembers(ctx, r.membersKey(appID, namespace, gid))
	}
	// Pipeline Exec 返回 redis.Nil 表示某些 key 不存在，不是错误
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("pipeline get multi group members failed: %w", err)
	}

	result := make(map[string][]string, len(groupIDs))
	for i, cmd := range cmds {
		members, err := cmd.Result()
		if err != nil && err != redis.Nil {
			continue // 单个群组失败跳过，不影响其他群组
		}
		result[groupIDs[i]] = members
	}
	return result, nil
}

// GetGroupNamespace 通过 (appID, groupID) 反查命名空间ID（反向映射 group:{appID}:{groupID} → namespace）
// 群组不存在返回 ErrGroupNotFound
func (r *RedisGroupRepository) GetGroupNamespace(ctx context.Context, appID, groupID string) (string, error) {
	namespace, err := r.client.Get(ctx, r.groupNamespaceKey(appID, groupID)).Result()
	if err != nil {
		if err == redis.Nil {
			return "", ErrGroupNotFound
		}
		return "", err
	}
	return namespace, nil
}

// GetMultiGroupNamespaces 批量反查 (appID, groupID) → namespace（Pipeline 一次网络往返）
// 单个 groupID 不存在时该 key 缺失，不影响其他
func (r *RedisGroupRepository) GetMultiGroupNamespaces(ctx context.Context, appID string, groupIDs []string) (map[string]string, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(groupIDs))
	for i, gid := range groupIDs {
		cmds[i] = pipe.Get(ctx, r.groupNamespaceKey(appID, gid))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("pipeline get multi group namespaces failed: %w", err)
	}
	result := make(map[string]string, len(groupIDs))
	for i, cmd := range cmds {
		namespace, err := cmd.Result()
		if err != nil { // redis.Nil（不存在）或其他错误均跳过，不加入结果
			continue
		}
		result[groupIDs[i]] = namespace
	}
	return result, nil
}

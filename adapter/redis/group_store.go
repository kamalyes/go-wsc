/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-22 15:00:00
 * @FilePath: \go-wsc\adapter\redis\group_store.go
 * @Description: 群组仓库 - 基于 Redis 实现群组成员关系持久化与跨节点共享
 *
 * 应用隔离 Key 设计（appID 默认 "__default_app__"，最上层隔离维度）：
 *   - {prefix}info:{appID}:{namespace}:{groupID}        → String(JSON) 群组元信息
 *   - {prefix}members:{appID}:{namespace}:{groupID}     → Set 成员 userID 集合
 *   - {prefix}user:{appID}:{namespace}:{userID}         → Set 用户在该 (app,ns) 下加入的 groupID 集合（反向索引）
 *   - {prefix}ns:{appID}:{namespace}:groups             → Set (app,ns) 下所有 groupID 集合
 *   - {prefix}gns:{appID}:{groupID}                     → Set 该 groupID 存在实例的 namespace 集合（跨 ns 实例索引）
 *   - {prefix}nss:{appID}                               → Set 该 app 下有群组的 namespace 集合（全命名空间广播定位索引）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package redisadapter

import (
	"context"
	"fmt"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/redis/go-redis/v9"
)

// GroupStore Redis 群组仓储，实现 spi.GroupStore 契约
// appID 是最上层隔离维度（默认 "__default_app__"），namespace 为租户维度，groupID 在 (appID, namespace) 内唯一
type GroupStore struct {
	client    redis.UniversalClient
	keyPrefix string
}

// NewGroupStore 创建 Redis 群组仓库
// keyPrefix 为空时使用 constants.DefaultGroupKeyPrefix
func NewGroupStore(client redis.UniversalClient, keyPrefix string) *GroupStore {
	return &GroupStore{
		client:    client,
		keyPrefix: mathx.IfNotEmpty(keyPrefix, constants.DefaultGroupKeyPrefix),
	}
}

// ============================================================================
// Redis Key 生成（应用隔离：appID 为最上层维度）
// ============================================================================

func (r *GroupStore) infoKey(appID, namespace, groupID string) string {
	return r.keyPrefix + "info:" + appID + ":" + namespace + ":" + groupID
}

func (r *GroupStore) membersKey(appID, namespace, groupID string) string {
	return r.keyPrefix + "members:" + appID + ":" + namespace + ":" + groupID
}

func (r *GroupStore) userGroupsKey(appID, namespace, userID string) string {
	return r.keyPrefix + "user:" + appID + ":" + namespace + ":" + userID
}

func (r *GroupStore) namespaceGroupsKey(appID, namespace string) string {
	return r.keyPrefix + "ns:" + appID + ":" + namespace + ":groups"
}

// groupInstancesKey 群组实例索引 key：(appID, groupID) → namespace 实例集合（Set）
// 同一 groupID 可在多个 namespace 下各建一个实例（业务侧按租户各自建组，同 gid 跨 ns 不混，
// members 桶仍按三维隔离）；投递时通过该索引一步拿到 gid 的全部 ns 实例再聚合成员，无需 SCAN
func (r *GroupStore) groupInstancesKey(appID, groupID string) string {
	return r.keyPrefix + "gns:" + appID + ":" + groupID
}

// namespacesKey 命名空间显式索引 key：appID → 有群组的 namespace 集合（Set）
// 全命名空间广播的定位来源，建组写入、解散收缩，O(成员数) 直查取代 keyspace SCAN
func (r *GroupStore) namespacesKey(appID string) string {
	return r.keyPrefix + "nss:" + appID
}

// ============================================================================
// 群组元信息管理
// ============================================================================

// createGroupScript Lua 脚本：原子性的校验同 (appID, namespace) 下 groupID 唯一并写入元信息、命名空间索引、实例索引与 nss 显式索引
// KEYS[1]=info 三维元信息 / KEYS[2]=ns 归属索引 / KEYS[3]=gns 跨 ns 实例索引 / KEYS[4]=nss 命名空间显式索引；ARGV[1]=元信息 JSON / ARGV[2]=groupID / ARGV[3]=namespace
// 返回 1 表示创建成功，0 表示群组已存在
const createGroupScript = `
if redis.call("exists", KEYS[1]) == 1 then
	return 0
end
redis.call("set", KEYS[1], ARGV[1])
redis.call("sadd", KEYS[2], ARGV[2])
redis.call("sadd", KEYS[3], ARGV[3])
redis.call("sadd", KEYS[4], ARGV[3])
return 1
`

// disbandNsIndexScript Lua 脚本：原子维护解散侧的命名空间索引
// KEYS[1]=ns 归属索引 / KEYS[2]=nss 命名空间显式索引；ARGV[1]=groupID / ARGV[2]=namespace
// ns 下已无群组（SCARD==0）时同步从 nss 显式索引移除，保证 GetAllNamespaces 零空残留
// SCARD 判定必须与 SREM 同脚本原子：拆两步会留下"ns 已空但 nss 仍登记"的广播侧幻影租户
// 返回 ns 剩余群组数（脚本必须显式 return，nil 返回会被 go-redis 转为 redis.Nil 错误）
const disbandNsIndexScript = `
redis.call("srem", KEYS[1], ARGV[1])
local remaining = redis.call("scard", KEYS[1])
if remaining == 0 then
	redis.call("srem", KEYS[2], ARGV[2])
end
return remaining
`

// CreateGroup 创建业务群组
// 禁止使用系统保留名（__ 前缀），同 (appID, namespace) groupID 唯一，重复创建返回 models.ErrGroupExisted
func (r *GroupStore) CreateGroup(ctx context.Context, group *models.Group) error {
	if group == nil || group.GroupID == "" {
		return errorx.WrapError("group or groupID cannot be empty")
	}
	// appID/namespace 归一化：空→默认值（最上层隔离维度必填）
	group.AppID = constants.NormalizeAppID(group.AppID)
	group.Namespace = constants.NormalizeNamespace(group.Namespace)
	// 业务组禁止使用系统保留名（__ 前缀）
	if models.IsSystemGroup(group.GroupID) {
		return models.ErrGroupReserved
	}
	return r.createGroupUnchecked(ctx, group)
}

// createGroupUnchecked 创建群组（不校验保留名，系统组专用）
func (r *GroupStore) createGroupUnchecked(ctx context.Context, group *models.Group) error {
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
		[]string{r.infoKey(appID, namespace, group.GroupID), r.namespaceGroupsKey(appID, namespace), r.groupInstancesKey(appID, group.GroupID), r.namespacesKey(appID)},
		data, group.GroupID, namespace,
	).Result()
	if err != nil {
		return errorx.WrapError("create group failed", err)
	}
	n, ok := result.(int64)
	if !ok || n == 0 {
		return models.ErrGroupExisted
	}
	return nil
}

// EnsureSystemGroup 确保系统保留组存在（agent/observer 自动加入前初始化）
//
// 幂等：不存在则创建，已存在则返回 nil仅允许 __ 前缀系统组名
// 复用 createGroupScript，返回 0（已存在）/1（新建）均视为成功，天然处理并发竞态
func (r *GroupStore) EnsureSystemGroup(ctx context.Context, appID, namespace, groupID string) error {
	if !models.IsSystemGroup(groupID) {
		return models.ErrGroupReserved
	}
	// appID 归一化（空→DefaultAppID，最上层隔离维度必填）；namespace 保持原值不归一化
	// 系统组支持 namespace="" 全局语义（全局观察者 tenant=""），归一化会破坏全局与 default 命名空间的隔离
	appID = mathx.IfEmpty(appID, constants.DefaultAppID)
	group := &models.Group{
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
		[]string{r.infoKey(appID, namespace, groupID), r.namespaceGroupsKey(appID, namespace), r.groupInstancesKey(appID, groupID), r.namespacesKey(appID)},
		data, groupID, namespace,
	).Result(); err != nil {
		return errorx.WrapError("ensure system group failed", err)
	}
	return nil // 0=已存在（幂等，实例索引首建时已写入） 1=新建，均成功
}

// GetGroup 获取群组元信息
func (r *GroupStore) GetGroup(ctx context.Context, appID, namespace, groupID string) (*models.Group, error) {
	data, err := r.client.Get(ctx, r.infoKey(appID, namespace, groupID)).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, models.ErrGroupNotFound
		}
		return nil, err
	}
	var group models.Group
	if err := json.Unmarshal([]byte(data), &group); err != nil {
		return nil, errorx.WrapError("unmarshal group failed", err)
	}
	return &group, nil
}

// DisbandGroup 解散群组
func (r *GroupStore) DisbandGroup(ctx context.Context, appID, namespace, groupID string) error {
	// 先获取成员列表，用于清理各成员的反向索引
	members, err := r.GetMembers(ctx, appID, namespace, groupID)
	if err != nil && err != models.ErrGroupNotFound {
		return err
	}

	// Pipeline 批量删除：元信息 + 成员集合 + 实例索引中该 ns 的记录 + 各成员反向索引
	pipe := r.client.Pipeline()
	pipe.Del(ctx, r.infoKey(appID, namespace, groupID))
	pipe.Del(ctx, r.membersKey(appID, namespace, groupID))
	// 实例索引按 ns 移除（同 gid 其他租户实例不受影响），集合空后由 Redis 自动回收
	pipe.SRem(ctx, r.groupInstancesKey(appID, groupID), namespace)
	for _, userID := range members {
		pipe.SRem(ctx, r.userGroupsKey(appID, namespace, userID), groupID)
	}
	if _, err = pipe.Exec(ctx); err != nil {
		return err
	}
	// ns 归属索引与 nss 显式索引的收缩经 Lua 原子完成（SCARD==0 时同步从 nss 移除），
	// 保证 GetAllNamespaces 零空残留
	_, err = r.client.Eval(ctx, disbandNsIndexScript,
		[]string{r.namespaceGroupsKey(appID, namespace), r.namespacesKey(appID)},
		groupID, namespace,
	).Result()
	return err
}

// ============================================================================
// 成员管理
// ============================================================================

// AddMembers 添加成员到群组
func (r *GroupStore) AddMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error {
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
func (r *GroupStore) RemoveMembers(ctx context.Context, appID, namespace, groupID string, userIDs []string) error {
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
func (r *GroupStore) GetMembers(ctx context.Context, appID, namespace, groupID string) ([]string, error) {
	members, err := r.client.SMembers(ctx, r.membersKey(appID, namespace, groupID)).Result()
	if err != nil {
		return nil, err
	}
	return members, nil
}

// GetUserGroups 获取用户在指定 (appID, namespace) 下加入的所有群组ID
func (r *GroupStore) GetUserGroups(ctx context.Context, appID, namespace, userID string) ([]string, error) {
	return r.client.SMembers(ctx, r.userGroupsKey(appID, namespace, userID)).Result()
}

// IsMember 判断用户是否为群组成员
func (r *GroupStore) IsMember(ctx context.Context, appID, namespace, groupID, userID string) (bool, error) {
	n, err := r.client.SIsMember(ctx, r.membersKey(appID, namespace, groupID), userID).Result()
	return n, err
}

// GetMemberCount 获取群组成员数量
func (r *GroupStore) GetMemberCount(ctx context.Context, appID, namespace, groupID string) (int64, error) {
	return r.client.SCard(ctx, r.membersKey(appID, namespace, groupID)).Result()
}

// GetNamespaceGroups 获取 (appID, namespace) 下所有群组ID
func (r *GroupStore) GetNamespaceGroups(ctx context.Context, appID, namespace string) ([]string, error) {
	return r.client.SMembers(ctx, r.namespaceGroupsKey(appID, namespace)).Result()
}

// GetAllNamespaces 获取指定 appID 下所有有群组的命名空间ID
// 直查 nss:{appID} 显式索引（建组写入、解散原子收缩），O(成员数) 单次 RTT 取代 keyspace SCAN
// 破坏性变更：老数据不迁移，冷启动索引为空，随建组逐步重建
func (r *GroupStore) GetAllNamespaces(ctx context.Context, appID string) ([]string, error) {
	appID = mathx.IfEmpty(appID, constants.DefaultAppID)
	return r.client.SMembers(ctx, r.namespacesKey(appID)).Result()
}

// GetMultiGroupMembers 批量获取多个群组的成员（跨 namespace 聚合，两段 Pipeline 共 2 次网络往返）
//
// 同一 groupID 可在多个 namespace 下各建实例（业务侧按租户建组，members 桶按三维隔离），
// 投递按 (appID, groupIDs) 定位时跨 ns 聚合：第一段 Pipeline 查各 gid 的 ns 实例集合
// （gns 索引），第二段 Pipeline 批量取所有 (gid, ns) 实例的成员并按 gid 合并去重
// 相比逐实例 SMEMBERS，N 个实例从 N 次 RTT 降为 2 次
// 返回 map[groupID][]memberIDs（已跨 ns 合并去重），无实例的 gid 该 key 缺失，单实例失败不影响其他
func (r *GroupStore) GetMultiGroupMembers(ctx context.Context, appID string, groupIDs []string) (map[string][]string, error) {
	if len(groupIDs) == 0 {
		return nil, nil
	}
	appID = mathx.IfEmpty(appID, constants.DefaultAppID)

	// 第一段：Pipeline 批量查各 gid 的 ns 实例集合（gns:{app}:{gid}）
	pipe := r.client.Pipeline()
	instanceCmds := make([]*redis.StringSliceCmd, len(groupIDs))
	for i, gid := range groupIDs {
		instanceCmds[i] = pipe.SMembers(ctx, r.groupInstancesKey(appID, gid))
	}
	// Pipeline Exec 返回 redis.Nil 表示某些 key 不存在，不是错误
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("pipeline get group instances failed: %w", err)
	}

	type groupInstance struct{ gid, ns string }
	var instances []groupInstance
	for i, cmd := range instanceCmds {
		namespaces, err := cmd.Result()
		if err != nil && err != redis.Nil {
			continue // 单个 gid 实例索引查询失败跳过，不影响其他
		}
		for _, ns := range namespaces {
			instances = append(instances, groupInstance{gid: groupIDs[i], ns: ns})
		}
	}
	if len(instances) == 0 {
		return map[string][]string{}, nil
	}

	// 第二段：Pipeline 批量取所有 (gid, ns) 实例的成员
	pipe = r.client.Pipeline()
	memberCmds := make([]*redis.StringSliceCmd, len(instances))
	for i, inst := range instances {
		memberCmds[i] = pipe.SMembers(ctx, r.membersKey(appID, inst.ns, inst.gid))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, fmt.Errorf("pipeline get multi group members failed: %w", err)
	}

	// 按 gid 合并去重（同 gid 跨 ns 实例的成员可能交叉，重复出现只保留一份）
	result := make(map[string][]string, len(groupIDs))
	dedup := make(map[string]map[string]struct{}, len(groupIDs))
	for i, inst := range instances {
		members, err := memberCmds[i].Result()
		if err != nil && err != redis.Nil {
			continue // 单个实例失败跳过，不影响其他实例
		}
		set, ok := dedup[inst.gid]
		if !ok {
			set = make(map[string]struct{}, len(members))
			dedup[inst.gid] = set
			result[inst.gid] = make([]string, 0, len(members))
		}
		for _, uid := range members {
			if _, dup := set[uid]; !dup {
				set[uid] = struct{}{}
				result[inst.gid] = append(result[inst.gid], uid)
			}
		}
	}
	return result, nil
}

// 编译期断言：repository 实现必须满足 spi 契约（Phase 4 迁仓后适配器同样受此约束）
var _ spi.GroupStore = (*GroupStore)(nil)

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-22 00:54:10
 * @FilePath: \go-wsc\models\group.go
 * @Description: 群组模型 - 群组成员关系持久化于 Redis，支持跨节点共享
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"strings"
	"time"

	"github.com/kamalyes/go-wsc/constants"
)

// 系统保留组（`__` 前缀为系统组，业务组禁止使用）
// agent 连接时自动加入 __agents__，observer 连接时自动加入 __observers__
// 本地分片索引（agentShards/observerShards）仍保留做 O(1) 缓存，
// Redis 系统组用于跨节点共享成员关系与显式广播
//
// 路由隔离维度默认值与系统组常量真相源统一在 constants 包，引用方直接用 constants.X
// （models 不再保留 const 别名中间层，避免多层中转）

// IsSystemGroup 判断 groupID 是否为系统保留组（`__` 前缀）
func IsSystemGroup(groupID string) bool {
	return strings.HasPrefix(groupID, constants.SystemGroupPrefix)
}

// Group 群组模型
// 群组成员关系为"用户级"持久关系，存于 Redis，与连接生命周期无关：
//   - 用户加入群组后即使下线仍是成员，上线后通过离线消息补发群组消息
//   - 跨节点共享，任意节点均可查询/管理群组成员
//   - 群组隶属于命名空间（Namespace），默认为 "default"，类似 k8s namespace
type Group struct {
	AppID      string                 `json:"app_id"`             // 应用ID（默认 "__default_app__"）
	Namespace  string                 `json:"namespace"`          // 命名空间ID（默认 "default"）
	GroupID    string                 `json:"group_id"`           // 群组ID（命名空间内唯一）
	Name       string                 `json:"name"`               // 群组名称
	OwnerID    string                 `json:"owner_id"`           // 群主用户ID
	MaxMembers int                    `json:"max_members"`        // 最大成员数（0 表示不限）
	CreatedAt  time.Time              `json:"created_at"`         // 创建时间
	Metadata   map[string]interface{} `json:"metadata,omitempty"` // 扩展元数据
}

// GetAppID 获取应用ID，空值返回默认应用ID
func (g *Group) GetAppID() string {
	if g.AppID == "" {
		return constants.DefaultAppID
	}
	return g.AppID
}

// GetNamespace 获取命名空间ID，空值返回默认命名空间
func (g *Group) GetNamespace() string {
	if g.Namespace == "" {
		return constants.DefaultNamespace
	}
	return g.Namespace
}

// GroupSendResult 群组消息投递结果
type GroupSendResult struct {
	AppID          string  // 应用ID
	GroupID        string  // 群组ID
	TotalMembers   int     // 群组总成员数
	OnlineMembers  int     // 在线成员数（至少在一个节点有连接）
	OfflineMembers int     // 离线成员数
	Sent           int     // 在线且成功投递数（含跨节点路由）
	StoredOffline  int     // 离线成员存储离线消息数
	Failed         int     // 投递失败数（在线但队列满等）
	Errors         []error // 投递过程中的错误
}

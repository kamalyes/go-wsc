/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 13:20:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 13:20:00
 * @FilePath: \go-wsc\group\interfaces.go
 * @Description: 群组域依赖端口
 *
 * 接口定义在消费方（本包），实现由 hub.Hub 提供 —— 「消费者定义接口」原则。
 * 本包不依赖 hub 的具体类型，仅依赖其能力，因此 hub 可以反向 import 本包。
 *
 * 端口按消费者实际能力面裁剪，不抄 Hub 方法表。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package group

import (
	"context"

	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// Host 群组域所需的 Hub 能力面。
type Host interface {
	// Context 返回 Hub 生命周期上下文（用于日志关联）
	Context() context.Context
	// GetLogger 日志器
	GetLogger() spi.Logger

	// GetWorkloadStore 客服负载存储；未注入时返回 nil，调用方须报错而非 no-op
	// （负载分配静默失败会导致工单扎堆，必须让调用方感知配置缺失）
	GetWorkloadStore() spi.WorkloadStore

	// GetOnlineUsersByType 按用户类型查询在线用户（负载分配需要在线客服列表）
	// 走本地分片注册表，无 ctx 参数
	GetOnlineUsersByType(userType models.UserType) ([]string, error)

	// GetShardedRegistry 分片客户端注册表（VIP/群组判定都要遍历本地连接）。
	// 注册表是核心运行时结构而非可插拔后端，故直接用具体类型，
	// 与 hub/stats 的端口保持一致。
	GetShardedRegistry() *connection.ShardedRegistry

	// GetGroupStore 群组仓储；未注入时返回 nil，调用方须报错
	// （群组成员关系是业务语义，静默 no-op 会让业务方以为入群成功）
	GetGroupStore() spi.GroupStore

	// PublishGroupInvalidations 批量发布群拓扑失效通知到集群广播频道（拓扑写路径失效的跨节点传播）
	// 单机形态（pubsub 未部署）由实现侧短路返回 nil；事件信封封装与传输细节由编排层收口
	PublishGroupInvalidations(ctx context.Context, appID string, groupIDs []string) error

	// TrySubmitCallback 提交一个带超时保护的异步回调任务，队列满时返回 false。
	// 群组生命周期回调走此处，避免每条连接一个 goroutine。
	TrySubmitCallback(task func()) bool

	// GetGroupDisbandCallback / GetGroupMemberJoinCallback / GetGroupMemberLeaveCallback
	// 群组生命周期回调。
	//
	// 这里用**结构类型**而非 hub 包里的具名下类型（GroupDisbandCallback 等）声明：
	// 那些具名类型定义在 hub 包内，子包引用它们即形成 hub ⇄ hub/group 循环 import。
	// 结构类型与具名类型可赋值兼容，Hub 侧实现 getter 时做一次具名→结构转换。
	// 返回 nil 表示业务方未注册回调，调用方须跳过（不可假定非空）。
	GetGroupDisbandCallback() func(ctx context.Context, namespace, groupID string)
	GetGroupMemberJoinCallback() func(ctx context.Context, namespace, groupID string, userIDs []string)
	GetGroupMemberLeaveCallback() func(ctx context.Context, namespace, groupID string, userIDs []string)

	// SendConditional 条件广播：对满足条件的在线客户端各投递一份，返回投递数。
	// 条件在发送路径内对每个客户端求值，避免先在 Hub 侧物化客户端切片再发。
	SendConditional(ctx context.Context, condition func(*models.Client) bool, msg *models.HubMessage) int

	// SendToUserWithRetry 点对点发送（带重试与路由注入），失败详情在返回值中
	SendToUserWithRetry(ctx context.Context, userID string, msg *models.HubMessage) *models.SendResult
}

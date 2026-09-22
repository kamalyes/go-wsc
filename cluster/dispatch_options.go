/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-26 00:00:00
 * @FilePath: \go-wsc\cluster\dispatch_options.go
 * @Description: 跨节点分发选项 —— 全部跨节点通信的路由参数信封
 *
 * 供 messaging（send/broadcast）、cluster（dispatch）两侧共用
 *
 * 设计理念：一套逻辑，namespace 贯穿，传输透明
 *   - 调用方只关心「发什么、发给谁」，不关心走 gRPC 还是 PubSub
 *   - 调用方爱传什么传什么，单/多元素统一走切片，不做单值字段冗余
 *
 * 调用方式：
 *   - 用户消息：RouteToCluster(op=SendMessage, targetUserID=xxx)
 *   - 群组广播：RouteToCluster(op=GroupsBroadcast, groupIDs=xxx, namespace=xxx)
 *   - 全局广播：RouteToCluster(op=Broadcast, namespace="" 表示全命名空间)
 *   - 观察者通知：RouteToCluster(op=ObserverNotify, namespace=xxx)
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package cluster

import (
	"github.com/kamalyes/go-wsc/models"
)

// ClusterOperation 集群操作类型（统一别名，消除 DistributedMessage 历史命名歧义）
type ClusterOperation = models.OperationType

// ClusterDispatchOptions 跨节点分发选项
// 封装所有跨节点通信的路由参数，由统一的跨节点路由入口消费
type ClusterDispatchOptions struct {
	Operation     ClusterOperation // 操作类型（SendMessage/GroupsBroadcast/Broadcast/ObserverNotify/KickUser）
	AppID         string           // 应用ID（最上层隔离维度，空=全局共享）
	Namespace     string           // 命名空间ID（空="default"；Broadcast 时空表示全命名空间）
	TargetNodeID  string           // 目标节点ID（精确路由，空=所有已知节点广播）
	TargetNodeIDs []string         // 已知目标节点列表（P2P 跨节点路由用，gRPC 未启用时优先定向 PubSub 而非广播频道）
	TargetUserID  string           // 目标用户ID（Operation=SendMessage 时使用）
	GroupIDs      []string         // 群组ID列表（len==1 单群组广播，len>1 批量广播，len==0 不广播）
	ExcludeSender bool             // 是否排除发送者（群组广播时使用）
	SenderID      string           // 发送者ID（排除发送者时使用）
	Reason        string           // 辅助信息（踢人原因等）
}

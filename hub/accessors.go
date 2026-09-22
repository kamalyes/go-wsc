/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:15:30
 * @FilePath: \go-wsc\hub\accessors.go
 * @Description: 域访问器与运行期策略 / 回调设置
 *
 * 域访问器暴露编排层持有的各域管理器（messaging/stats/group/registry）
 * 与节点标识；SetOverloadPolicy 迁移自旧 hub.go：对 admission/broadcastShaper/
 * ephemeralCoalescer 三个 atomic.Pointer 做运行期热替换；Set*Callback
 * 迁移自旧 callbacks.go 的 On* 注册方法，改为链式返回 *Hub。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"

	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/group"
	"github.com/kamalyes/go-wsc/messaging"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/stats"
)

// ============================================================================
// 域访问器
// ============================================================================

// Messaging 消息域管理器（发送 / 广播 / 分发 / ACK / 离线转存）
func (h *Hub) Messaging() *messaging.Manager { return h.messagingMgr }

// Stats 统计域管理器
func (h *Hub) Stats() *stats.Manager { return h.statsMgr }

// Group 群组域管理器
func (h *Hub) Group() *group.Manager { return h.groupMgr }

// Registry 分片连接注册表
func (h *Hub) Registry() *connection.ShardedRegistry { return h.shardedRegistry }

// NodeID 节点 ID（K8s 兼容生成，见 hub.go 的 generateNodeID）
func (h *Hub) NodeID() string { return h.nodeID }

// NodeInfo 节点信息（集群注册与路由用）
func (h *Hub) NodeInfo() *models.NodeInfo { return h.nodeInfo }

// ============================================================================
// 运行期过载策略热替换（迁移自旧 hub.go 的 SetOverloadPolicy）
// ============================================================================

// SetOverloadPolicy 链式配置过载保护策略（运行期热替换）
//
// 参数为 nil 表示保留该组件现状（不替换）；闸门替换时旧实例 Stop、
// 新实例 Store 后立即 Start —— 已 Run 的 Hub 调用时新闸门直接进入
// 水位评估循环（atomic Store 发布：后台循环并发 Load 读到完整初始化的实例）。
// 组件构造见 overload.NewAdmissionGate / NewShaper / NewCoalescer
// （零值参数由 constants 包默认值兜底）。
func (h *Hub) SetOverloadPolicy(gate *overload.AdmissionGate, shaper *overload.Shaper, coalescer *overload.Coalescer) *Hub {
	if gate != nil {
		if old := h.admission.Load(); old != nil {
			old.Stop()
		}
		h.admission.Store(gate)
		gate.Start()
	}
	if shaper != nil {
		h.broadcastShaper.Store(shaper)
	}
	if coalescer != nil {
		h.ephemeralCoalescer.Store(coalescer)
	}
	return h
}

// ============================================================================
// 运行期回调设置（迁移自旧 callbacks.go 的 On* 注册方法，链式返回 *Hub）
// ============================================================================

// SetOfflineMessagePushCallback 设置离线消息推送回调（运行期可替换）
func (h *Hub) SetOfflineMessagePushCallback(cb OfflineMessagePushCallback) *Hub {
	h.offlineMessagePushCallback = cb
	return h
}

// SetMessageSendCallback 设置消息发送完成回调（运行期可替换）
func (h *Hub) SetMessageSendCallback(cb messaging.MessageSendCallback) *Hub {
	h.messageSendCallback = cb
	return h
}

// SetQueueFullCallback 设置队列满回调（运行期可替换）
func (h *Hub) SetQueueFullCallback(cb QueueFullCallback) *Hub {
	h.queueFullCallback = cb
	return h
}

// SetHeartbeatTimeoutCallback 设置心跳超时回调（运行期可替换）
func (h *Hub) SetHeartbeatTimeoutCallback(cb HeartbeatTimeoutCallback) *Hub {
	h.heartbeatTimeoutCallback = cb
	return h
}

// SetHeartbeatReportCallback 设置心跳上报回调（运行期可替换）
func (h *Hub) SetHeartbeatReportCallback(cb HeartbeatReportCallback) *Hub {
	h.heartbeatReportCallback = cb
	return h
}

// SetBeforeHeartbeatCallback 设置心跳处理前回调（运行期可替换）
func (h *Hub) SetBeforeHeartbeatCallback(cb BeforeHeartbeatCallback) *Hub {
	h.beforeHeartbeatCallback = cb
	return h
}

// SetAfterHeartbeatCallback 设置心跳处理后回调（运行期可替换）
func (h *Hub) SetAfterHeartbeatCallback(cb AfterHeartbeatCallback) *Hub {
	h.afterHeartbeatCallback = cb
	return h
}

// SetClientConnectCallback 设置客户端连接回调（运行期可替换）
func (h *Hub) SetClientConnectCallback(cb ClientConnectCallback) *Hub {
	h.clientConnectCallback = cb
	return h
}

// SetClientDisconnectCallback 设置客户端断开回调（运行期可替换）
func (h *Hub) SetClientDisconnectCallback(cb ClientDisconnectCallback) *Hub {
	h.clientDisconnectCallback = cb
	return h
}

// SetMessageReceivedCallback 设置客户端上行消息回调（运行期可替换）
func (h *Hub) SetMessageReceivedCallback(cb messaging.MessageReceivedCallback) *Hub {
	h.messageReceivedCallback = cb
	return h
}

// SetErrorCallback 设置统一错误处理回调（运行期可替换）
func (h *Hub) SetErrorCallback(cb messaging.ErrorCallback) *Hub {
	h.errorCallback = cb
	return h
}

// SetBatchSendFailureCallback 设置批量发送单条失败回调（运行期可替换）
func (h *Hub) SetBatchSendFailureCallback(cb overload.BatchSendFailureCallback) *Hub {
	h.batchSendFailureCallback = cb
	return h
}

// SetGroupDisbandCallback 设置群组解散回调（运行期可替换）
func (h *Hub) SetGroupDisbandCallback(cb func(ctx context.Context, namespace, groupID string)) *Hub {
	h.groupDisbandCallback = cb
	return h
}

// SetGroupMemberJoinCallback 设置群组成员加入回调（运行期可替换）
func (h *Hub) SetGroupMemberJoinCallback(cb func(ctx context.Context, namespace, groupID string, userIDs []string)) *Hub {
	h.groupMemberJoinCallback = cb
	return h
}

// SetGroupMemberLeaveCallback 设置群组成员离开回调（运行期可替换）
func (h *Hub) SetGroupMemberLeaveCallback(cb func(ctx context.Context, namespace, groupID string, userIDs []string)) *Hub {
	h.groupMemberLeaveCallback = cb
	return h
}

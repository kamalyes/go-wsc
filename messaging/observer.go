/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 19:28:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 19:28:00
 * @FilePath: \go-wsc\messaging\observer.go
 * @Description: 消息域观察者投递 —— 攒批入口 / 本地直投 / 跨节点广播
 *
 * 从 hub/interfaces.go 域化下沉：观察者（全量旁听连接）的消息旁路投递
 * 语义归消息域。两级投递：NotifyObservers 攒批入口（消息路径经批处理器
 * 合并，消除 per-message goroutine）与 NotifyObserversDirect 直投
 * （batcher flush 回调，本地三级索引查找 + 预序列化共享 + 跨节点广播）。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/json"

	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// observerNotifyEnabled 观察者通知管道是否就绪（notifier 已注入且 observer 索引已启用）
// handleBroadcast 的发送者补路由查询与 NotifyObservers 共用此守卫：
// 管道未就绪时跳过敏捷路径的冗余 sender 遍历（P2P 每消息一次分片读锁 + ctx 构造）
func (m *Manager) observerNotifyEnabled() bool {
	registry := m.host.GetShardedRegistry()
	return registry != nil && registry.ObserverEnabled() && m.host.GetObserverNotifier() != nil
}

// NotifyObservers 通知观察者（观察者未启用时为 no-op）
// 从 ctx 提取 namespace+groupIDs 定位观察范围，提交批量处理器攒批投递
func (m *Manager) NotifyObservers(ctx context.Context, msg *models.HubMessage) {
	if msg == nil || !m.observerNotifyEnabled() {
		return
	}
	notifier := m.host.GetObserverNotifier()
	namespace := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	// msg 会在 Submit 内 Clone，避免调用方修改影响异步 flush
	if !notifier.Submit(msg, namespace, groupIDs) {
		m.host.GetLogger().DebugContextKV(ctx, "观察者通知队列已满，丢弃",
			"message_id", msg.MessageID,
			"namespace", namespace,
			"group_ids", groupIDs,
		)
	}
}

// NotifyObserversDirect 直接通知观察者（不经批处理队列，避免递归入队）
// 由 observerBatcher flush 调用：本地观察者投递 + 跨节点广播，无 per-message goroutine
func (m *Manager) NotifyObserversDirect(msg *models.HubMessage, namespace string, groupIDs []string) {
	if msg == nil {
		return
	}
	registry := m.host.GetShardedRegistry()
	if registry == nil {
		return
	}
	ctx := routing.NewRoute().WithAppID(msg.AppID).WithNamespace(namespace).WithGroupIDs(groupIDs).Inject(m.host.Context())

	// 快速检查：无观察者时仅跨节点广播 - O(1)
	if registry.GetObserverUserCount() == 0 {
		m.broadcastObserverNotification(ctx, msg)
		return
	}

	// 三级索引查找：合并所有 groupIDs 的观察者并去重
	observers := registry.GetObserversForMessage(namespace, groupIDs...)

	// 预构建观察者专用消息（Clone + metadata），所有观察者共享同一份
	observerMsg := msg.Clone()
	observerMsg.WithMetadata("observer_mode", "true")
	observerMsg.WithMetadata("original_sender", msg.Sender)
	observerMsg.WithMetadata("original_receiver", msg.Receiver)

	// 预序列化一次（所有观察者复用，消除逐个 Clone+Marshal 开销）
	msgData, err := json.Marshal(observerMsg)
	if err != nil {
		m.host.GetLogger().ErrorContextKV(ctx, "序列化观察者消息失败",
			"message_id", msg.MessageID,
			"error", err,
		)
		return
	}

	delivered := 0
	for _, observer := range observers {
		if observer.TrySend(msgData) {
			delivered++
		} else {
			m.host.GetLogger().WarnContextKV(ctx, "观察者缓冲区已满或已关闭，丢弃消息",
				"observer_id", observer.UserID,
				"client_id", observer.ID,
				"message_id", observerMsg.MessageID,
			)
		}
	}

	m.host.GetLogger().DebugContextKV(ctx, "已通知本地观察者",
		"message_id", observerMsg.MessageID,
		"total_devices", len(observers),
		"delivered", delivered,
	)

	m.broadcastObserverNotification(ctx, msg)
}

// broadcastObserverNotification 广播观察者通知到其他节点
// 统一走 RouteToCluster 端口，由编排层集中决策 gRPC 直连与 PubSub 兜底
func (m *Manager) broadcastObserverNotification(ctx context.Context, msg *models.HubMessage) {
	// 单机模式：无 PubSub 且无 gRPC，不跨节点
	if !m.host.HasPubsub() && !m.host.IsGRPCEnabled() {
		return
	}

	// 从传入 ctx 派生超时 ctx，保留 trace_id 等元数据
	// 如果传入 ctx 已取消（如 Hub 关闭场景），fallback 到 Background 确保 dispatch 能完成
	parentCtx := ctx
	if parentCtx == nil || parentCtx.Err() != nil {
		parentCtx = context.Background()
	}
	dispatchCtx, cancel := context.WithTimeout(parentCtx, 3*time.Second)
	defer cancel()

	opts := cluster.ClusterDispatchOptions{
		Operation: models.OperationTypeObserverNotify,
		Namespace: routing.NamespaceFromContext(ctx),
		GroupIDs:  routing.GroupIDsFromContext(ctx),
	}

	if err := m.host.RouteToCluster(dispatchCtx, msg, opts); err != nil {
		m.host.GetLogger().WarnContextKV(ctx, "广播观察者通知失败",
			"error", err,
			"message_id", msg.MessageID,
		)
	}
}

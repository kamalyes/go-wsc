/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-02-03 11:35:17
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-02-03 11:58:55
 * @FilePath: \go-wsc\hub\observer.go
 * @Description: Hub 观察者功能 - 三级索引 O(k) 查找，支持多端登录
 *
 * 观察者三级模型（类似 k8s namespace 隔离）：
 *   1. 全局观察者（Namespace=""）：接收所有命名空间的消息
 *   2. 命名空间观察者（Namespace="ns1"）：接收指定命名空间的所有消息
 *   3. 群组观察者（Namespace="ns1", GroupID="g1"）：仅接收指定命名空间+群组的消息
 *
 * 性能：通过 observerIdx 三级索引 O(k) 查找（k=匹配的观察者数），
 * 替代旧版 ForEachObserver O(n) 全量扫描
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 观察者查询 - 基于 observerIdx 三级索引，O(k) 查找
// ============================================================================

// GetObserverClients 获取所有观察者客户端（所有设备）- O(n) n=观察者设备数
// 仅用于统计场景，消息通知走 GetObserversForMessage
func (h *Hub) GetObserverClients() []*Client {
	observers := make([]*Client, 0)
	h.shardedRegistry.ForEachObserver(func(_, _ string, client *Client) bool {
		if !client.IsClosed() {
			observers = append(observers, client)
		}
		return true
	})
	return observers
}

// GetObserverClientsByNamespace 获取指定命名空间的观察者客户端（兼容接口）
// 使用三级索引查找：全局观察者 + 命名空间级观察者（不含群组级）
// 群组级观察者请使用 GetObserversForMessage(namespace, groupID)
func (h *Hub) GetObserverClientsByNamespace(namespace string) []*Client {
	return h.shardedRegistry.GetObserversForMessage(namespace, "")
}

// GetObserversForMessage 获取应接收指定命名空间+群组消息的观察者
// 合并三级：全局 + 命名空间 + 命名空间+群组，O(k) k=匹配的观察者数
func (h *Hub) GetObserversForMessage(namespace, groupID string) []*Client {
	return h.shardedRegistry.GetObserversForMessage(namespace, groupID)
}

// GetObserverCount 获取观察者数量（用户数，非设备数）- O(1)
func (h *Hub) GetObserverCount() int {
	return h.shardedRegistry.GetObserverUserCount()
}

// GetObserverDeviceCount 获取观察者设备总数 - O(n) n=观察者数量
func (h *Hub) GetObserverDeviceCount() int {
	return h.shardedRegistry.GetObserverDeviceCount()
}

// IsObserver 检查用户是否为观察者 - O(1)
func (h *Hub) IsObserver(userID string) bool {
	return h.shardedRegistry.HasObserver(userID)
}

// ============================================================================
// 观察者通知 - 基于 observerIdx 三级索引
// ============================================================================

// notifyObservers 通知观察者（内部方法）
// namespace+groupID 定位观察范围，通过三级索引 O(k) 查找匹配的观察者
// 提交到 observerBatcher 批量处理，消除 per-message goroutine
func (h *Hub) notifyObservers(ctx context.Context, msg *HubMessage, namespace, groupID string) {
	// 观察者模块未启用时直接返回
	if !h.shardedRegistry.ObserverEnabled() {
		return
	}
	// 提交到批量处理器（msg 会在 Submit 内 Clone，避免调用方修改影响异步 flush）
	if !h.observerBatcher.Submit(msg, namespace, groupID) {
		h.logger.DebugContextKV(ctx, "观察者通知队列已满，丢弃",
			"message_id", msg.MessageID,
			"namespace", namespace,
			"group_id", groupID,
		)
	}
}

// notifyObserversDirect 同步执行观察者通知（由 observerBatcher.flush 调用）
// 本地观察者投递 + 跨节点广播，无 per-message goroutine
func (h *Hub) notifyObserversDirect(msg *HubMessage, namespace, groupID string) {
	ctx := h.ctx

	// 快速检查：无观察者时仅跨节点广播 - O(1)
	observerCount := h.shardedRegistry.GetObserverUserCount()

	h.logger.DebugContextKV(ctx, "开始通知观察者",
		"message_id", msg.MessageID,
		"namespace", namespace,
		"group_id", groupID,
		"sender", msg.Sender,
		"receiver", msg.Receiver,
		"message_type", msg.MessageType,
		"observer_count", observerCount,
	)

	if observerCount == 0 {
		h.logger.DebugContextKV(ctx, "本节点无观察者，仅广播到其他节点",
			"message_id", msg.MessageID,
		)
		h.broadcastObserverNotification(ctx, msg, namespace, groupID)
		return
	}

	// 三级索引查找：O(k) k=匹配的观察者设备数
	observers := h.shardedRegistry.GetObserversForMessage(namespace, groupID)
	h.logger.DebugContextKV(ctx, "准备通知本地观察者",
		"message_id", msg.MessageID,
		"namespace", namespace,
		"group_id", groupID,
		"observer_devices", len(observers),
	)

	// 预构建观察者专用消息（Clone + metadata），所有观察者共享同一份
	observerMsg := msg.Clone()
	observerMsg.WithMetadata("observer_mode", "true")
	observerMsg.WithMetadata("original_sender", msg.Sender)
	observerMsg.WithMetadata("original_receiver", msg.Receiver)

	// 预序列化一次（所有观察者复用同一份 msgData）
	msgData, err := json.Marshal(observerMsg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "序列化观察者消息失败",
			"message_id", msg.MessageID, "error", err)
		return
	}
	msgID := observerMsg.MessageID

	var successCount atomic.Int32

	syncx.NewParallelSliceExecutor[*Client, error](observers).
		OnSuccess(func(idx int, client *Client, result error) {
			successCount.Add(1)
		}).
		OnError(func(idx int, client *Client, err error) {
			h.logger.WarnContextKV(ctx, "通知观察者失败",
				"observer_id", client.UserID,
				"client_id", client.ID,
				"message_id", msgID,
				"error", err,
			)
		}).
		OnPanic(func(idx int, client *Client, panicVal any) {
			h.logger.WarnContextKV(ctx, "向观察者发送消息时发生 panic(通道可能已关闭)",
				"observer_id", client.UserID,
				"client_id", client.ID,
				"message_id", msgID,
				"panic", panicVal,
				"stack", string(debug.Stack()),
			)
		}).
		Execute(func(idx int, observer *Client) (error, error) {
			return h.sendToObserver(ctx, observer, msgID, msgData), nil
		})

	h.logger.DebugContextKV(ctx, "已通知本地观察者",
		"message_id", msgID,
		"sender", msg.Sender,
		"receiver", msg.Receiver,
		"message_type", msg.MessageType,
		"total_devices", len(observers),
		"success_count", successCount.Load(),
	)

	// 跨节点广播观察者通知
	h.broadcastObserverNotification(ctx, msg, namespace, groupID)
}

// broadcastObserverNotification 广播观察者通知到其他节点
//
// 统一走 routeToCluster 入口，由其集中决策 gRPC 直连与 PubSub 兜底
// groupID 通过 GroupIDs 字段传递，接收端从 distMsg.GroupIDs 提取
// 同步执行（由 observerBatcher.flush 调用，无需额外 goroutine）
func (h *Hub) broadcastObserverNotification(ctx context.Context, msg *HubMessage, namespace, groupID string) {
	// 单机模式：无 PubSub 且无 gRPC，不跨节点
	if h.pubsub == nil && !h.IsGRPCEnabled() {
		return
	}

	dispatchCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	opts := ClusterDispatchOptions{
		Operation: OperationTypeObserverNotify,
		Namespace: namespace,
	}
	// 通过 GroupIDs 携带 groupID，接收端从 distMsg.GroupIDs[0] 提取
	if groupID != "" {
		opts.GroupIDs = []string{groupID}
	}

	if err := h.routeToCluster(dispatchCtx, msg, opts); err != nil {
		h.logger.WarnContextKV(ctx, "广播观察者通知失败",
			"error", err,
			"message_id", msg.MessageID,
		)
	} else {
		h.logger.DebugContextKV(ctx, "已广播观察者通知",
			"message_id", msg.MessageID,
		)
	}
}

// sendToObserver 发送预序列化消息给单个观察者设备 - O(1)
// msgData 由调用方预序列化一次，所有观察者共享，消除逐个 Clone+Marshal 开销
func (h *Hub) sendToObserver(ctx context.Context, observer *Client, msgID string, msgData []byte) error {
	if observer == nil {
		return ErrClientNotFound
	}

	if observer.SendChan == nil {
		h.logger.WarnContextKV(ctx, "观察者发送通道为空",
			"observer_id", observer.UserID,
			"client_id", observer.ID,
			"message_id", msgID,
		)
		return ErrClientNotFound
	}

	if observer.IsClosed() {
		h.logger.DebugContextKV(ctx, "观察者已关闭，跳过发送",
			"observer_id", observer.UserID,
			"client_id", observer.ID,
			"message_id", msgID,
		)
		return ErrClientNotFound
	}

	if observer.TrySend(msgData) {
		return nil
	}

	h.logger.WarnContextKV(ctx, "观察者缓冲区已满或已关闭，丢弃消息",
		"observer_id", observer.UserID,
		"client_id", observer.ID,
		"message_id", msgID,
		"buffer_size", cap(observer.SendChan),
	)
	return ErrQueueAndPendingFull
}

// ============================================================================
// 观察者统计信息
// ============================================================================

// GetObserverStats 获取所有观察者的统计信息 - O(n) n=观察者设备数
func (h *Hub) GetObserverStats() []*ObserverStats {
	observers := h.GetObserverClients()
	stats := make([]*ObserverStats, 0, len(observers))

	for _, observer := range observers {
		stats = append(stats, &ObserverStats{
			ObserverID:  observer.UserID,
			ClientID:    observer.ID,
			ConnectedAt: observer.ConnectedAt,
			BufferSize:  cap(observer.SendChan),
			BufferUsage: len(observer.SendChan),
			IsConnected: true,
			UserType:    observer.UserType.String(),
			ClientType:  observer.ClientType.String(),
		})
	}

	return stats
}

// GetObserverManagerStats 获取观察者管理器统计信息 - O(n) n=观察者设备数
func (h *Hub) GetObserverManagerStats() *ObserverManagerStats {
	observerStats := h.GetObserverStats()

	statsAny := make([]any, len(observerStats))
	for i, stat := range observerStats {
		statsAny[i] = stat
	}

	return &ObserverManagerStats{
		TotalObservers:      int(h.GetObserverCount()),
		TotalDevices:        h.GetObserverDeviceCount(),
		TotalNotifications:  0,
		FailedNotifications: 0,
		DroppedMessages:     0,
		ObserverStats:       statsAny,
	}
}

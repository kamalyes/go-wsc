/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-02-03 11:35:17
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-02-03 11:58:55
 * @FilePath: \go-wsc\hub\observer.go
 * @Description: Hub 观察者功能 - 支持多端登录的高性能实时消息监听（O(1)）
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// ============================================================================
// 观察者管理 - O(1) 性能，支持多端登录
// ============================================================================

// GetObserverClients 获取所有观察者客户端（所有设备）- O(n) n=观察者数量
// 自动过滤已关闭的客户端，避免并发场景下的 "send on closed channel" panic
func (h *Hub) GetObserverClients() []*Client {
	return syncx.WithRLockReturnValue(&h.mutex, func() []*Client {
		observers := make([]*Client, 0)
		for _, clientMap := range h.observerClients {
			for _, client := range clientMap {
				// 只返回未关闭的客户端
				if !client.IsClosed() {
					observers = append(observers, client)
				}
			}
		}
		return observers
	})
}

// GetObserverCount 获取观察者数量（用户数，非设备数）- O(1)
func (h *Hub) GetObserverCount() int {
	return syncx.WithRLockReturnValue(&h.mutex, func() int {
		return len(h.observerClients)
	})
}

// GetObserverDeviceCount 获取观察者设备总数 - O(n) n=观察者数量
func (h *Hub) GetObserverDeviceCount() int {
	return syncx.WithRLockReturnValue(&h.mutex, func() int {
		count := 0
		for _, clientMap := range h.observerClients {
			count += len(clientMap)
		}
		return count
	})
}

// IsObserver 检查用户是否为观察者 - O(1)
func (h *Hub) IsObserver(userID string) bool {
	return syncx.WithRLockReturnValue(&h.mutex, func() bool {
		_, exists := h.observerClients[userID]
		return exists
	})
}

// addObserver 添加观察者到专用映射 - O(1)
// 注意：此方法不加锁，需要外部调用者持有锁
func (h *Hub) addObserver(client *Client) {
	if h.observerClients[client.UserID] == nil {
		h.observerClients[client.UserID] = make(map[string]*Client)
	}
	h.observerClients[client.UserID][client.ID] = client

	h.logger.DebugKV("✅ 观察者已添加",
		"observer_id", client.UserID,
		"client_id", client.ID,
		"device_count", len(h.observerClients[client.UserID]),
		"total_observers", len(h.observerClients),
		"user_type", client.UserType.String(),
		"client_type", client.ClientType.String(),
	)
}

// removeObserver 从专用映射移除观察者 - O(1)
// 注意：此方法不加锁，需要外部调用者持有锁
func (h *Hub) removeObserver(client *Client) {
	if clientMap, exists := h.observerClients[client.UserID]; exists {
		delete(clientMap, client.ID)

		// 如果该观察者没有其他设备了，删除整个映射
		if len(clientMap) == 0 {
			delete(h.observerClients, client.UserID)
		}

		h.logger.DebugKV("❌ 观察者已移除",
			"observer_id", client.UserID,
			"client_id", client.ID,
			"remaining_devices", len(clientMap),
			"total_observers", len(h.observerClients),
		)
	}
}

// notifyObservers 通知所有观察者（内部方法）- O(n) 但 n 是观察者设备数，通常很小
// 在消息发送时自动调用，将消息推送给所有观察者的所有设备
func (h *Hub) notifyObservers(msg *HubMessage) {
	// 快速检查：无观察者时直接返回 - O(1)
	observerCount := syncx.WithRLockReturnValue(&h.mutex, func() int {
		return len(h.observerClients)
	})

	h.logger.DebugKV("开始通知观察者",
		"message_id", msg.MessageID,
		"sender", msg.Sender,
		"receiver", msg.Receiver,
		"message_type", msg.MessageType,
		"observer_count", observerCount,
	)

	if observerCount == 0 {
		h.logger.DebugKV("本节点无观察者，仅广播到其他节点",
			"message_id", msg.MessageID,
		)
		// 即使本节点没有观察者，也要广播到其他节点
		h.broadcastObserverNotification(msg)
		return
	}

	// 获取所有观察者设备列表
	observers := h.GetObserverClients()
	h.logger.DebugKV("准备通知本地观察者",
		"message_id", msg.MessageID,
		"observer_devices", len(observers),
	)

	// 异步并发通知所有观察者，不阻塞主流程
	syncx.Go().
		OnPanic(func(r any) {
			h.logger.ErrorKV("通知观察者 panic", "panic", r, "message_id", msg.MessageID)
		}).
		Exec(func() {
			var successCount atomic.Int32

			syncx.NewParallelSliceExecutor[*Client, error](observers).
				OnSuccess(func(idx int, client *Client, result error) {
					successCount.Add(1)
				}).
				OnError(func(idx int, client *Client, err error) {
					h.logger.WarnKV("通知观察者失败",
						"observer_id", client.UserID,
						"client_id", client.ID,
						"message_id", msg.MessageID,
						"error", err,
					)
				}).
				OnPanic(func(idx int, client *Client, panicVal any) {
					h.logger.WarnKV("向观察者发送消息时发生 panic(通道可能已关闭)",
						"observer_id", client.UserID,
						"client_id", client.ID,
						"message_id", msg.MessageID,
						"panic", panicVal,
					)
				}).
				Execute(func(idx int, observer *Client) (error, error) {
					return h.sendToObserver(observer, msg), nil
				})

			h.logger.DebugKV("已通知本地观察者",
				"message_id", msg.MessageID,
				"sender", msg.Sender,
				"receiver", msg.Receiver,
				"message_type", msg.MessageType,
				"total_devices", len(observers),
				"success_count", successCount.Load(),
			)
		})

	// 广播观察者通知到其他节点
	h.broadcastObserverNotification(msg)
}

// broadcastObserverNotification 广播观察者通知到其他节点
func (h *Hub) broadcastObserverNotification(msg *HubMessage) {
	// 如果没有启用 PubSub，说明是单机模式，不需要广播
	if h.pubsub == nil {
		return
	}

	// 异步广播，不阻塞主流程
	syncx.Go().
		OnPanic(func(r any) {
			h.logger.ErrorKV("广播观察者通知 panic", "panic", r, "message_id", msg.MessageID)
		}).
		Exec(func() {
			distMsg := &DistributedMessage{
				Type:      OperationTypeObserverNotify,
				NodeID:    h.nodeID,
				Message:   msg,
				Timestamp: time.Now(),
			}

			// 发布到观察者专用频道
			channel := "wsc:observers"
			data, _ := json.Marshal(distMsg)

			ctx := context.Background()
			if err := h.pubsub.Publish(ctx, channel, string(data)); err != nil {
				h.logger.ErrorKV("广播观察者通知失败",
					"error", err,
					"message_id", msg.MessageID,
					"channel", channel,
				)
			} else {
				h.logger.DebugKV("已广播观察者通知",
					"message_id", msg.MessageID,
					"channel", channel,
				)
			}
		})
}

// sendToObserver 发送消息给单个观察者设备 - O(1)
func (h *Hub) sendToObserver(observer *Client, msg *HubMessage) error {
	// 参数验证
	if observer == nil {
		return ErrClientNotFound
	}

	if observer.SendChan == nil {
		h.logger.WarnKV("观察者发送通道为空",
			"observer_id", observer.UserID,
			"client_id", observer.ID,
			"message_id", msg.MessageID,
		)
		return ErrClientNotFound
	}

	// 检查客户端是否已关闭
	if observer.IsClosed() {
		h.logger.DebugKV("观察者已关闭，跳过发送",
			"observer_id", observer.UserID,
			"client_id", observer.ID,
			"message_id", msg.MessageID,
		)
		return ErrClientNotFound
	}

	// 创建观察者专用消息副本
	observerMsg := msg.Clone()

	// 添加观察者标识（保留原始发送者和接收者信息）
	observerMsg.WithMetadata("observer_mode", "true")
	observerMsg.WithMetadata("original_sender", msg.Sender)
	observerMsg.WithMetadata("original_receiver", msg.Receiver)

	// 序列化消息
	msgData, err := json.Marshal(observerMsg)
	if err != nil {
		h.logger.ErrorKV("序列化观察者消息失败",
			"observer_id", observer.UserID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return err
	}

	// 发送到观察者的发送通道（非阻塞）
	select {
	case observer.SendChan <- msgData:
		return nil
	default:
		// 观察者缓冲区满，丢弃消息（观察者不应阻塞正常业务）
		h.logger.WarnKV("观察者缓冲区已满，丢弃消息",
			"observer_id", observer.UserID,
			"client_id", observer.ID,
			"message_id", msg.MessageID,
			"buffer_size", cap(observer.SendChan),
		)
		return ErrQueueAndPendingFull
	}
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

	// 转换为 []any 类型
	statsAny := make([]any, len(observerStats))
	for i, stat := range observerStats {
		statsAny[i] = stat
	}

	return &ObserverManagerStats{
		TotalObservers:      h.GetObserverCount(),       // 观察者用户数
		TotalDevices:        h.GetObserverDeviceCount(), // 观察者设备总数
		TotalNotifications:  0,                          // 可以从全局计数器获取
		FailedNotifications: 0,                          // 可以从全局计数器获取
		DroppedMessages:     0,                          // 可以从全局计数器获取
		ObserverStats:       statsAny,
	}
}

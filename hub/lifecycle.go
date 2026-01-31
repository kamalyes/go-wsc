/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-30 23:55:51
 * @FilePath: \go-wsc\hub\lifecycle.go
 * @Description: Hub 生命周期管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// Run 启动Hub
func (h *Hub) Run() {
	h.wg.Add(1)
	defer h.wg.Done()

	// 使用 Console 分组记录 Hub 启动日志
	cg := h.logger.NewConsoleGroup()
	cg.Group("🚀 WebSocket Hub 启动")

	startTimer := cg.Time("Hub 启动耗时")

	// 显示启动配置
	config := map[string]interface{}{
		"节点ID":     h.nodeID,
		"节点IP":     h.config.NodeIP,
		"节点端口":     h.config.NodePort,
		"消息缓冲大小":   h.config.MessageBufferSize,
		"心跳间隔(秒)":  h.config.HeartbeatInterval,
		"客户端超时(秒)": h.config.ClientTimeout,
	}
	cg.Table(config)

	// 设置已启动标志并通知等待的goroutine
	if h.started.CompareAndSwap(false, true) {
		// 设置启动时间到 Redis
		if h.statsRepo != nil {
			syncx.Go().
				WithTimeout(2 * time.Second).
				OnError(func(err error) {
					h.logger.ErrorKV("设置启动时间到Redis失败", "error", err)
				}).
				ExecWithContext(func(execCtx context.Context) error {
					return h.statsRepo.SetStartTime(execCtx, h.nodeID, time.Now().Unix())
				})
		}

		startTimer.End()
		cg.Info("✅ Hub 启动成功")
		cg.GroupEnd()

		// 启动指标收集器（如果已配置）
		close(h.startCh)
	}

	// 启动待发送消息处理goroutine
	go h.processPendingMessages()

	// 🌐 启动分布式服务（如果启用了 PubSub）
	if h.pubsub != nil {
		// 启动节点心跳
		go h.StartNodeHeartbeat(h.ctx)

		// 订阅节点间消息
		go func() {
			if err := h.SubscribeNodeMessages(h.ctx); err != nil {
				h.logger.ErrorKV("订阅节点消息失败", "error", err)
			}
		}()

		// 订阅全局广播频道
		go func() {
			if err := h.SubscribeBroadcastChannel(h.ctx); err != nil {
				h.logger.ErrorKV("订阅广播频道失败", "error", err)
			}
		}()

		h.logger.InfoKV("🌐 分布式服务已启动", "node_id", h.nodeID)
	}

	// 使用 EventLoop 管理事件循环
	// 统一处理客户端注册/注销、消息广播和定时任务
	syncx.NewEventLoop(h.ctx).
		// 客户端注册事件：处理新客户端连接
		OnChannel(h.register, h.handleRegister).
		// 客户端注销事件：处理客户端断开连接
		OnChannel(h.unregister, h.handleUnregister).
		// 广播消息事件：处理需要广播的消息
		OnChannel(h.broadcast, h.handleBroadcast).
		// 心跳检查定时器：定期检查客户端心跳，清理超时连接
		OnTicker(h.config.HeartbeatInterval, h.checkHeartbeat).
		// 性能监控定时器：定期报告性能指标
		// 使用配置中的 PerformanceMetricsInterval (默认5分钟)
		OnTicker(h.config.PerformanceMetricsInterval, h.reportPerformanceMetrics).
		// ACK清理定时器：定期清理过期的ACK记录
		// 使用配置中的 AckCleanupInterval (默认1分钟)
		OnTicker(h.config.AckCleanupInterval, h.cleanupExpiredAck).
		// 添加消息记录清理定时器（如果启用了消息记录仓库）
		IfTicker(h.messageRecordRepo != nil,
			mathx.IfNotZero(h.config.RecordCleanupInterval, 30*time.Minute),
			h.cleanupExpiredMessageRecords).
		// Panic处理：捕获事件处理过程中的panic，防止整个Hub崩溃
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("Hub事件循环panic", "panic", r, "node_id", h.nodeID)
		}).
		// 优雅关闭：事件循环停止时记录日志
		OnShutdown(func() {
			h.logger.InfoKV("Hub事件循环已停止", "node_id", h.nodeID)
		}).
		// 运行事件循环（阻塞），直到context被取消
		Run()
}

// reportPerformanceMetrics 报告性能指标
func (h *Hub) reportPerformanceMetrics() {
	// 使用原子计数器快速获取连接数，避免加锁
	activeClients := h.activeClientsCount.Load()
	sseClients := h.sseClientsCount.Load()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 从 Redis 获取统计信息
	if h.statsRepo == nil {
		return
	}

	stats, err := h.statsRepo.GetNodeStats(ctx, h.nodeID)
	if err != nil {
		h.logger.WarnKV("获取节点统计失败", "error", err)
		return
	}

	// 使用 Console 表格展示性能指标
	cg := h.logger.NewConsoleGroup()
	cg.Group("📊 Hub 性能指标报告 [节点: %s]", h.nodeID)

	// 连接统计
	connectionStats := map[string]any{
		"WebSocket 连接数": activeClients,
		"SSE 连接数":       sseClients,
		"历史总连接数":        stats.TotalConnections,
	}
	cg.Table(connectionStats)

	// 消息统计
	messageStats := map[string]any{
		"已发送消息数":  stats.MessagesSent,
		"已广播消息数":  stats.BroadcastsSent,
		"运行时长(秒)": stats.Uptime,
	}
	cg.Table(messageStats)

	cg.GroupEnd()
}

// cleanupExpiredAck 清理过期的ACK消息
func (h *Hub) cleanupExpiredAck() {
	if h.ackManager == nil {
		return
	}

	cleaned := h.ackManager.CleanupExpired()
	if cleaned > 0 {
		h.logger.InfoKV("清理过期ACK消息",
			"count", cleaned,
			"node_id", h.nodeID,
		)
	}
}

// cleanupExpiredMessageRecords 清理过期的消息记录
func (h *Hub) cleanupExpiredMessageRecords() {
	if h.messageRecordRepo == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	deletedCount, err := h.messageRecordRepo.DeleteExpired(ctx)
	if err != nil {
		h.logger.WarnKV("清理过期消息记录失败",
			"error", err,
			"node_id", h.nodeID,
		)
		return
	}

	if deletedCount > 0 {
		h.logger.InfoKV("清理过期消息记录",
			"count", deletedCount,
			"node_id", h.nodeID,
		)
	}
}

// WaitForStart 等待Hub启动完成
// 这个方法对于用户来说很重要，确保Hub完全启动后再进行操作
func (h *Hub) WaitForStart() {
	<-h.startCh
}

// WaitForStartWithTimeout 带超时的等待Hub启动
func (h *Hub) WaitForStartWithTimeout(timeout time.Duration) error {
	select {
	case <-h.startCh:
		return nil
	case <-time.After(timeout):
		return ErrHubStartupTimeout
	}
}

// SafeShutdown 安全关闭Hub，确保所有操作完成
func (h *Hub) SafeShutdown() error {
	// 检查是否已经关闭
	if h.shutdown.Load() {
		h.logger.Debug("Hub已经关闭，跳过重复关闭操作")
		return nil
	}

	// 设置关闭标志（先标记避免新操作进入）
	if !h.shutdown.CompareAndSwap(false, true) {
		return nil // 已经在关闭中
	}

	// 使用 Console 分组记录关闭流程
	cg := h.logger.NewConsoleGroup()
	cg.Group("🛑 WebSocket Hub 安全关闭流程")
	shutdownTimer := cg.Time("Hub 关闭耗时")

	cg.Info("开始安全关闭 Hub [节点: %s]", h.nodeID)

	// 关闭所有客户端连接
	cg.Info("→ 关闭所有客户端连接...")
	h.mutex.Lock()
	for _, client := range h.clients {
		h.removeClientUnsafe(client)
	}
	h.mutex.Unlock()

	// 取消context（通知所有 goroutine 停止）
	cg.Info("→ 取消所有上下文...")
	h.cancel()

	// 等待一小段时间让goroutine有机会响应取消信号
	time.Sleep(10 * time.Millisecond)

	// 使用原子计数器快速计算超时时间
	// 基础超时：从配置读取（默认5秒）
	// 最大超时：从配置读取（默认60秒）
	// 动态计算：基础超时 + (连接数 * 10ms)，但不超过最大超时
	baseTimeout := mathx.IfNotZero(h.config.ShutdownBaseTimeout, 5*time.Second)
	maxTimeout := mathx.IfNotZero(h.config.ShutdownMaxTimeout, 60*time.Second)

	// 使用原子计数器获取连接数（无需加锁）
	totalClients := h.activeClientsCount.Load() + h.sseClientsCount.Load()
	
	// 每个连接增加10ms超时时间
	calculatedTimeout := baseTimeout + time.Duration(totalClients)*10*time.Millisecond
	if calculatedTimeout > maxTimeout {
		calculatedTimeout = maxTimeout
	}

	// 等待所有goroutine完成，带超时保护
	cg.Info("→ 等待所有协程完成...")
	done := make(chan struct{})
	syncx.Go(h.ctx).
		OnPanic(func(r any) {
			h.logger.ErrorKV("WaitGroup等待崩溃", "panic", r)
		}).
		Exec(func() {
			h.wg.Wait()
			close(done)
		})

	select {
	case <-done:
		// 正常关闭
		finalStats := map[string]any{
			"total_connections": int64(0),
			"messages_sent":     int64(0),
			"broadcasts_sent":   int64(0),
		}

		if h.statsRepo != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			stats, _ := h.statsRepo.GetNodeStats(ctx, h.nodeID)
			cancel()

			if stats != nil {
				finalStats["total_connections"] = stats.TotalConnections
				finalStats["messages_sent"] = stats.MessagesSent
				finalStats["broadcasts_sent"] = stats.BroadcastsSent
			}
		}

		shutdownTimer.End()
		cg.Info("→ 显示最终统计...")
		cg.Table(finalStats)
		cg.Info("✅ Hub 安全关闭成功")
		cg.GroupEnd()
		return nil

	case <-time.After(calculatedTimeout):
		// 超时关闭
		shutdownTimer.End()
		cg.Info("⚠️ Hub 关闭超时（超时时间: %v）", calculatedTimeout)
		cg.GroupEnd()
		return ErrHubShutdownTimeout
	}
}

// processPendingMessages 处理待发送消息队列
func (h *Hub) processPendingMessages() {
	h.wg.Add(1)
	defer h.wg.Done()

	// 记录待发送消息处理器启动
	h.logger.InfoKV("待发送消息处理器启动",
		"node_id", h.nodeID,
		"check_interval", "100ms",
	)

	processedCount := 0
	timeoutCount := 0

	// 使用 EventLoop 统一管理事件循环
	syncx.NewEventLoop(h.ctx).
		// 处理待发送消息
		OnChannel(h.pendingMessages, func(msg *HubMessage) {
			// 尝试将消息放入broadcast队列（带超时）
			select {
			case h.broadcast <- msg:
				processedCount++
			case <-time.After(5 * time.Second):
				timeoutCount++
				h.logger.WarnKV("待发送消息处理超时",
					"message_id", msg.ID,
					"sender", msg.Sender,
					"receiver", msg.Receiver,
					"message_type", msg.MessageType,
					"timeout", "5s",
				)
			}
		}).
		// 定期统计进度
		OnTicker(100*time.Millisecond, func() {
			if processedCount%100 == 0 && processedCount > 0 {
				h.logger.InfoKV("待发送消息处理进度",
					"processed_count", processedCount,
					"timeout_count", timeoutCount,
					"success_rate", fmt.Sprintf("%.2f%%", float64(processedCount)/float64(processedCount+timeoutCount)*100),
				)
			}
		}).
		// Panic 保护
		OnPanic(func(r interface{}) {
			h.logger.ErrorKV("待发送消息处理器panic", "panic", r, "node_id", h.nodeID)
		}).
		// 优雅关闭
		OnShutdown(func() {
			h.logger.InfoKV("待发送消息处理器关闭",
				"processed_count", processedCount,
				"timeout_count", timeoutCount,
			)
		}).
		Run()
}

// Shutdown 关闭Hub（旧API，兼容性方法）
func (h *Hub) Shutdown() {
	_ = h.SafeShutdown()
}

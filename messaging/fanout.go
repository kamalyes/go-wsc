/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-05-17 15:36:16
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-27 10:26:55
 * @FilePath: \go-wsc\messaging\fanout.go
 * @Description: 批量扇出投递 —— 有界并发执行器 / 离线广播聚合 / 节点索引批量预取
 *
 * 拆分自 send.go：per-user 扇出类能力（批量发送/群组成员投递/客户端列表投递）与
 * P2P 主链分文件，扇出公共设施（并发度解析/执行器构造/离线广播聚合器/批量预取）
 * 与三个公开扇出入口集中于此，P2P 链路（send.go）不再承载扇出语义
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"sync"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// fanoutConcurrency 解析扇出并发上限
// 复用 WorkerPool.MessageWorkers（默认 64），成员数可达数万的群广播无界扇出会
// 瞬时打爆 Redis 连接池并滞留大量消息副本（OOM 教训），必须限制在途 goroutine 数
func (m *Manager) fanoutConcurrency() int {
	if m.host.GetConfig() != nil && m.host.GetConfig().WorkerPool != nil && m.host.GetConfig().WorkerPool.MessageWorkers > 0 {
		return m.host.GetConfig().WorkerPool.MessageWorkers
	}
	return 64
}

// newFanoutExecutor 创建有界并发的扇出执行器（所有 per-user 批量投递统一入口）
func newFanoutExecutor[T any](items []T, concurrency int) *syncx.ParallelSliceExecutor[T, *models.SendResult] {
	return syncx.NewParallelSliceExecutor[T, *models.SendResult](items).WithConcurrency(concurrency)
}

// ============================================================================
// 离线广播聚合器（扇出内收集、扇出后统一跨节点推送）
// ============================================================================

// offlineBroadcastEntry 聚合收集的离线广播条目
type offlineBroadcastEntry struct {
	userID string
	msg    *models.HubMessage
}

// offlineBroadcastCollector 批量扇出路径的离线广播聚合器
// 扇出 goroutine 内仅收集（O(1) 追加，不碰 Redis），扇出结束后统一 flush 推送：
//   - 索引滞后但实际在线的用户仍能实时收到消息（不跳过、真推送）
//   - 真正离线的用户由离线存储 + 重连上线拉取兜底
//   - 广播移出扇出关键路径，消除 goroutine 阻塞堆积（OOM 根因）
type offlineBroadcastCollector struct {
	mu      sync.Mutex
	entries []offlineBroadcastEntry
}

// add 收集一条离线广播条目（扇出 goroutine 并发调用安全）
func (c *offlineBroadcastCollector) add(userID string, msg *models.HubMessage) {
	c.mu.Lock()
	c.entries = append(c.entries, offlineBroadcastEntry{userID: userID, msg: msg})
	c.mu.Unlock()
}

// withOfflineBroadcastCollector 为批量扇出路径注入聚合器，返回新上下文与聚合器
func withOfflineBroadcastCollector(ctx context.Context) (context.Context, *offlineBroadcastCollector) {
	collector := &offlineBroadcastCollector{}
	return context.WithValue(ctx, ContextKeyOfflineBroadcastCollector, collector), collector
}

// flushOfflineBroadcasts 扇出结束后统一跨节点推送收集到的未命中用户（有界并发，复用扇出并发度）
func (m *Manager) flushOfflineBroadcasts(ctx context.Context, collector *offlineBroadcastCollector) {
	collector.mu.Lock()
	entries := collector.entries
	collector.entries = nil
	collector.mu.Unlock()

	if len(entries) == 0 {
		return
	}
	m.host.GetLogger().InfoContextKV(ctx, "[跨Pod] 批量扇出完成，统一推送聚合的离线兜底广播",
		"pending_count", len(entries),
		"node_id", m.host.GetNodeID(),
	)
	newFanoutExecutor(entries, m.fanoutConcurrency()).
		Execute(func(idx int, entry offlineBroadcastEntry) (*models.SendResult, error) {
			// 直接走底层广播（绕过 collector 检查，避免重新收集造成死循环）
			m.doOfflineBroadcast(ctx, entry.userID, entry.msg)
			return nil, nil
		})
}

// ============================================================================
// 节点索引批量预取
// ============================================================================

// prefetchFanoutNodes 批量预取扇出目标的节点索引（Pipeline 单次往返）
//
// 扇出路径优化：群组可靠投递/批量发送对每个成员都要一次节点查询来判定在线与跨节点路由目标
// （N 个成员 = N 次 Redis 往返）。此前仅预取本地 miss 成员，本地在线成员在 sendToUser 内仍经
// checkAndRouteToNode → queryUserNodes 逐用户 GetUserNodes 判定多端跨节点路由，同样存在 N+1；
// 本方法改为对全部去重成员一次 Pipeline 预取，本地/远端成员统一零回源。
//
// 返回 nil 表示不适用（单机模式/目标过少/批量查询失败），扇出回退原有逐用户查询路径。
// 不修改调用方 msg：路由构造直接读 msg 信封字段（零 Clone），原始消息保持私有
func (m *Manager) prefetchFanoutNodes(ctx context.Context, msg *models.HubMessage, userIDs []string) map[string][]string {
	// 目标过少批量预取无收益（1 个用户 Pipeline 与单次查询等价）；单机模式无跨节点索引
	if len(userIDs) < 2 || (!m.host.HasPubsub() && !m.host.IsGRPCEnabled()) {
		return nil
	}
	// 路由信封构造（不归一化 ns，保留调用方 ctx 原值）：群组扇出路径信封 ns="" 为跨 ns 通配语义
	// BatchGetUserNodes 走 unscoped 桶 + appID 过滤定位节点
	// 零 Clone：批量预取仅消费路由维度（appID+ns），直接从 msg 信封构造 routeCtx
	// 消除整消息深拷贝（与 sendToUserWithRetry 的 InjectRoute 同参同序：msg 优先、ctx 兜底）
	appID := mathx.IfEmpty(msg.AppID, routing.AppIDFromContext(ctx))
	ns := mathx.IfEmpty(msg.Namespace, routing.NamespaceFromContext(ctx))
	routeCtx := routing.NewRoute().WithAppID(appID).WithNamespace(ns).Inject(ctx)

	// 去重 userID（含本地在线成员：其节点解析用于 sendToUser 的多端跨节点路由，一并预取消除 N+1）
	seen := make(map[string]struct{}, len(userIDs))
	uniques := make([]string, 0, len(userIDs))
	for _, uid := range userIDs {
		if uid == "" {
			continue
		}
		if _, dup := seen[uid]; dup {
			continue
		}
		seen[uid] = struct{}{}
		uniques = append(uniques, uid)
	}
	if len(uniques) == 0 {
		return nil
	}

	nodes := m.host.BatchGetUserNodes(routeCtx, uniques)
	if len(nodes) == 0 {
		return nil // 批量查询失败（含全部无活跃节点），扇出回退逐用户查询
	}
	return nodes
}

// ============================================================================
// 批量扇出入口
// ============================================================================

// SendToMultipleUsers 并发发送消息给多个用户
// 使用 ParallelSliceExecutor 并行投递 + 预分配 slice + 索引写入，消除 mutex 竞争
func (m *Manager) SendToMultipleUsers(ctx context.Context, userIDs []string, msg *models.HubMessage) map[string]error {
	errs := make(map[string]error, len(userIDs))
	if len(userIDs) == 0 {
		return errs
	}

	// 预分配结果 slice，每个 goroutine 只写自己的索引（无数据竞争）
	errList := make([]error, len(userIDs))

	ctx, collector := withOfflineBroadcastCollector(ctx)
	defer m.flushOfflineBroadcasts(ctx, collector)
	// 批量预取本地 miss 用户的节点索引（1 次 Pipeline 替代扇出内 N 次单查）
	presetNodes := m.prefetchFanoutNodes(ctx, msg, userIDs)
	newFanoutExecutor(userIDs, m.fanoutConcurrency()).
		Execute(func(idx int, userID string) (*models.SendResult, error) {
			result := m.sendToUserWithRetry(ctx, userID, msg, presetNodes[userID])
			if result.FinalError != nil {
				errList[idx] = result.FinalError // 索引写入，无需锁
			}
			return result, nil
		})

	// Execute 同步返回后，无竞争地转为 map
	for i, userID := range userIDs {
		if errList[i] != nil {
			errs[userID] = errList[i]
		}
	}

	return errs
}

// SendToGroupMembers 向会话成员批量发送消息（兼容旧版本接口）
// 参数:
//   - ctx: 上下文
//   - memberIDs: 成员ID列表
//   - msg: 要发送的消息
//   - excludeSender: 是否排除发送者本身
//
// 返回:
//   - models.BroadcastResult: 广播结果，包含成功、失败、离线统计
//
// 示例:
//
//	向会话成员广播，排除发送者自己
//	result := hub.SendToGroupMembers(ctx, memberIDs, msg, true)
//	简单批量发送（不排除发送者）
//	result := hub.SendToGroupMembers(ctx, userIDs, msg, false)
func (m *Manager) SendToGroupMembers(ctx context.Context, memberIDs []string, msg *models.HubMessage, excludeSender bool) *models.BroadcastResult {
	// 如果需要排除发送者，从列表中移除
	filteredIDs := memberIDs
	if excludeSender && msg.Sender != "" {
		filteredIDs = mathx.FilterSlice(memberIDs, func(id string) bool {
			return id != msg.Sender
		})
		m.host.GetLogger().DebugContextKV(ctx, "过滤发送者后的成员列表",
			"original_count", len(memberIDs),
			"filtered_count", len(filteredIDs),
			"excluded_sender", msg.Sender,
		)
	}

	// 并发批量发送
	result := &models.BroadcastResult{
		Total:      len(filteredIDs),
		Success:    0,
		Offline:    0,
		Failed:     0,
		Errors:     make(map[string]error),
		OfflineIDs: make([]string, 0),
		FailedIDs:  make([]string, 0),
	}

	ctx, collector := withOfflineBroadcastCollector(ctx)
	defer m.flushOfflineBroadcasts(ctx, collector)
	// 批量预取本地 miss 成员的节点索引（1 次 Pipeline 替代扇出内 N 次单查）
	presetNodes := m.prefetchFanoutNodes(ctx, msg, filteredIDs)
	newFanoutExecutor(filteredIDs, m.fanoutConcurrency()).
		OnComplete(func(results []*models.SendResult, errors []error) {
			for i, sendResult := range results {
				// 优先判失败（FinalError != nil 即为失败）
				if sendResult.FinalError != nil {
					result.Failed++
					result.FailedIDs = append(result.FailedIDs, filteredIDs[i])
					result.Errors[filteredIDs[i]] = sendResult.FinalError
					continue
				}
				// 成功路径需区分在线送达 vs 离线存储
				if sendResult.Success {
					if sendResult.StoredOffline {
						// 离线存储成功（用户不在线，消息入离线队列）
						result.Offline++
						result.OfflineIDs = append(result.OfflineIDs, filteredIDs[i])
					} else {
						// 在线送达成功
						result.Success++
					}
				}
			}
		}).
		Execute(func(idx int, uid string) (*models.SendResult, error) {
			// sendToUserWithRetry 内部已经处理了在线/离线逻辑
			// - 在线用户：直接发送（presetNodes 批量预取，免逐成员单查）
			// - 离线用户：自动存储到离线队列，上线后推送
			sendResult := m.sendToUserWithRetry(ctx, uid, msg, presetNodes[uid])
			return sendResult, nil
		})

	if lg := m.host.GetLogger(); lg.IsLevelEnabled(logger.DEBUG) {
		lg.DebugContextKV(ctx, "会话消息发送完成",
			"session_id", msg.SessionID,
			"message_id", msg.MessageID,
			"total", result.Total,
			"success", result.Success,
			"offline", result.Offline,
			"failed", result.Failed,
		)
	}

	// 通知观察者（群组级别统一通知）
	// handleBroadcast 对 GroupIDs 非空的消息跳过观察者通知，此处补齐
	m.NotifyObservers(ctx, msg)

	return result
}

// SendToClientsWithRetry 发送消息给多个客户端（带重试）
// 使用预分配 slice + 索引定位写入，消除 mutex（每个 goroutine 写不同索引，无竞争）
func (m *Manager) SendToClientsWithRetry(ctx context.Context, clients []*models.Client, msg *models.HubMessage, maxRetries int) map[string]*models.SendResult {
	results := make(map[string]*models.SendResult, len(clients))
	if len(clients) == 0 {
		return results
	}

	// 预分配结果 slice，每个 goroutine 只写自己的索引（无数据竞争）
	resultsSlice := make([]*models.SendResult, len(clients))

	ctx, collector := withOfflineBroadcastCollector(ctx)
	defer m.flushOfflineBroadcasts(ctx, collector)
	// 批量预取本地 miss 用户的节点索引（客户端列表按 userID 去重后预取，多端同用户共享单条索引）
	userIDs := make([]string, len(clients))
	for i, client := range clients {
		userIDs[i] = client.UserID
	}
	presetNodes := m.prefetchFanoutNodes(ctx, msg, userIDs)
	newFanoutExecutor(clients, m.fanoutConcurrency()).
		OnSuccess(func(idx int, client *models.Client, result *models.SendResult) {
			resultsSlice[idx] = result // 各 goroutine 写不同索引，无需锁
		}).
		Execute(func(idx int, client *models.Client) (*models.SendResult, error) {
			return m.sendToUserWithRetry(ctx, client.UserID, msg, presetNodes[client.UserID]), nil
		})

	// Execute 同步返回后，所有写入已完成，无竞争地转为 map
	for i, client := range clients {
		if resultsSlice[i] != nil {
			results[client.UserID] = resultsSlice[i]
		}
	}

	return results
}

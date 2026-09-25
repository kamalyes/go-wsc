/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-25 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-25 00:00:00
 * @FilePath: \go-wsc\cluster\grpc_batch.go
 * @Description: 跨节点 gRPC 微批合帧分派器
 *
 * 将「同一目标节点」的多条投递指令在窗口期 / 批上限内合并为单次 BatchDispatch RPC，
 * RPC 次数 O(消息数) → O(批次数)，显著降低小消息高扇出场景的跨节点 RPC 开销。
 *
 * 设计要点：
 *   - per-addr 累积缓冲：不同目标节点的指令互不阻隔，各自独立成批
 *   - 首条指令触发窗口定时器，达到批上限立即 flush（优先于窗口）
 *   - Submit 同步阻塞等待结果（flush 完成后回写每条结果），保留调用方既有
 *     delivered / fallback / user_miss 的语义，PubSub 兜底与重路由决策不受影响
 *   - 路由信封（appID / namespace / groupIDs）内嵌于 DispatchItem，
 *     不依赖 gRPC metadata，支持同批多租户 / 多群组异构合帧
 *   - 窗口为 0 时禁用微批，调用方回退到逐消息单发（延迟敏感场景可关闭）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package cluster

import (
	"context"
	"sync"
	"time"

	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
)

// batchFlushTimeout 单批投递 RPC 超时（与 dispatchViaGRPC 的单条 gRPC 超时对齐）
const batchFlushTimeout = 3 * time.Second

// DispatchItem 单条跨节点投递指令（微批合帧最小单元）
// 路由信封内嵌，不依赖 gRPC metadata
type DispatchItem struct {
	Operation     models.OperationType
	AppID         string
	Namespace     string
	GroupIDs      []string
	TargetUserID  string
	Reason        string
	MessageData   []byte
	ExcludeSender bool
	SenderID      string
	TraceID       string
}

// DispatchOutcome 单条投递结果分类（与 hub 内 grpcDispatchOutcome 语义对齐）
type DispatchOutcome int

const (
	// OutcomeDelivered 投递成功
	OutcomeDelivered DispatchOutcome = iota
	// OutcomeFallback 调用失败或结果未知 → 交由 PubSub 兜底
	OutcomeFallback
	// OutcomeUserMiss 目标节点明确用户不在 → 跳过 PubSub 兜底
	OutcomeUserMiss
)

// GRPCBatchDispatcher 跨节点 gRPC 微批分派器
type GRPCBatchDispatcher struct {
	pool     *GRPCClientPool // 复用连接池 + per-node 熔断器
	window   time.Duration   // 批量窗口（0=禁用微批）
	maxBatch int             // 单批累计上限

	mu      sync.Mutex
	buffers map[string]*nodeBatch // addr → 累积缓冲
	closed  bool
}

// nodeBatch 单个目标节点的累积缓冲
type nodeBatch struct {
	pending []*pendingItem
	timer   *time.Timer
}

// pendingItem 缓冲中的单条指令（附带结果回传通道）
type pendingItem struct {
	item DispatchItem
	ch   chan DispatchOutcome
}

// NewGRPCBatchDispatcher 创建跨节点 gRPC 微批分派器
// window 为批量窗口（<=0 表示禁用微批）；maxBatch<=0 时默认不设批上限（仅靠窗口）
func NewGRPCBatchDispatcher(pool *GRPCClientPool, window time.Duration, maxBatch int) *GRPCBatchDispatcher {
	return &GRPCBatchDispatcher{
		pool:     pool,
		window:   window,
		maxBatch: maxBatch,
		buffers:  make(map[string]*nodeBatch),
	}
}

// Enabled 是否启用微批（窗口 > 0 且已注入连接池）
func (d *GRPCBatchDispatcher) Enabled() bool {
	return d != nil && d.window > 0 && d.pool != nil
}

// Submit 提交单条投递指令并同步等待结果
// 返回该指令的投递分类；调用方（dispatchViaGRPC）据此决定 PubSub 兜底
func (d *GRPCBatchDispatcher) Submit(ctx context.Context, addr string, item DispatchItem) DispatchOutcome {
	if d == nil || d.pool == nil || d.window <= 0 {
		return OutcomeFallback
	}

	p := &pendingItem{
		item: item,
		ch:   make(chan DispatchOutcome, 1),
	}

	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return OutcomeFallback
	}
	batch, ok := d.buffers[addr]
	if !ok {
		batch = &nodeBatch{}
		d.buffers[addr] = batch
	}
	batch.pending = append(batch.pending, p)
	flushNow := d.maxBatch > 0 && len(batch.pending) >= d.maxBatch
	if len(batch.pending) == 1 {
		// 首条触发窗口定时器（窗口到期后由回调 flush）
		batch.timer = time.AfterFunc(d.window, func() { d.flush(addr) })
	}
	d.mu.Unlock()

	if flushNow {
		d.flush(addr) // 达到批上限，立即合帧发出
	}

	select {
	case outcome := <-p.ch:
		return outcome
	case <-ctx.Done():
		// 调用方超时放弃等待：结果回退 PubSub 兜底（在途批仍可能送达，与 unary 超时降级语义一致）
		return OutcomeFallback
	}
}

// Close 关闭分派器：排空所有缓冲区并拒绝后续提交（优雅停机）
func (d *GRPCBatchDispatcher) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	addrs := make([]string, 0, len(d.buffers))
	for addr := range d.buffers {
		addrs = append(addrs, addr)
	}
	d.mu.Unlock()

	for _, addr := range addrs {
		d.flush(addr)
	}
}

// flush 排空并投递指定节点的累积缓冲（并发安全，空缓冲直接返回）
func (d *GRPCBatchDispatcher) flush(addr string) {
	d.mu.Lock()
	batch, ok := d.buffers[addr]
	if !ok || len(batch.pending) == 0 {
		d.mu.Unlock()
		return
	}
	if batch.timer != nil {
		batch.timer.Stop()
		batch.timer = nil
	}
	pending := batch.pending
	batch.pending = nil
	d.mu.Unlock()

	d.sendAndResolve(addr, pending)
}

// sendAndResolve 组装单批 BatchDispatch 请求、发送并逐条回写结果
func (d *GRPCBatchDispatcher) sendAndResolve(addr string, pending []*pendingItem) {
	items := make([]*wscpb.DispatchItem, len(pending))
	for i, p := range pending {
		items[i] = toProtoDispatchItem(p.item)
	}

	results, err := d.sendBatch(addr, items)

	for i, p := range pending {
		var outcome DispatchOutcome
		if err != nil {
			outcome = OutcomeFallback
		} else if i < len(results) {
			outcome = classifyOutcome(p.item.Operation, results[i])
		} else {
			outcome = OutcomeFallback
		}
		// 非阻塞回写：调用方可能已 ctx.Done 放弃等待（通道缓冲 1，避免泄漏）
		select {
		case p.ch <- outcome:
		default:
		}
	}
}

// sendBatch 经熔断器发送单批 BatchDispatch RPC（独立超时 ctx，不受单调用方 ctx 影响）
func (d *GRPCBatchDispatcher) sendBatch(addr string, items []*wscpb.DispatchItem) ([]*wscpb.DispatchResult, error) {
	cb := d.pool.getOrCreateBreaker(addr)
	var resp *wscpb.BatchDispatchResponse
	err := cb.Execute(func() error {
		client, err := d.pool.GetClient(addr)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), batchFlushTimeout)
		defer cancel()
		r, err := client.BatchDispatch(ctx, &wscpb.BatchDispatchRequest{Items: items})
		if err != nil {
			return err
		}
		resp = r
		return nil
	})
	if err != nil {
		return nil, err
	}
	return resp.GetResults(), nil
}

// ToProtoDispatchOperation 将领域操作类型映射为 proto DispatchOperation 枚举
// 双向转换的唯一实现（服务端 FromProtoDispatchOperation 反向映射），避免两处散落 switch
func ToProtoDispatchOperation(op models.OperationType) wscpb.DispatchOperation {
	switch op {
	case models.OperationTypeSendMessage:
		return wscpb.DispatchOperation_DISPATCH_SEND_MESSAGE
	case models.OperationTypeKickUser:
		return wscpb.DispatchOperation_DISPATCH_KICK_USER
	case models.OperationTypeGroupBroadcast, models.OperationTypeGroupsBroadcast:
		// 两种群组 op 在批量路径语义一致：发送端逐 gid 拆分为单群组 item
		return wscpb.DispatchOperation_DISPATCH_GROUP_BROADCAST
	case models.OperationTypeObserverNotify:
		return wscpb.DispatchOperation_DISPATCH_OBSERVER_NOTIFY
	default:
		return wscpb.DispatchOperation_DISPATCH_OPERATION_UNSPECIFIED
	}
}

// FromProtoDispatchOperation 将 proto DispatchOperation 枚举映射回领域操作类型
// 与 ToProtoDispatchOperation 互逆
func FromProtoDispatchOperation(op wscpb.DispatchOperation) models.OperationType {
	switch op {
	case wscpb.DispatchOperation_DISPATCH_SEND_MESSAGE:
		return models.OperationTypeSendMessage
	case wscpb.DispatchOperation_DISPATCH_KICK_USER:
		return models.OperationTypeKickUser
	case wscpb.DispatchOperation_DISPATCH_GROUP_BROADCAST:
		return models.OperationTypeGroupBroadcast
	case wscpb.DispatchOperation_DISPATCH_OBSERVER_NOTIFY:
		return models.OperationTypeObserverNotify
	default:
		return ""
	}
}

// toProtoDispatchItem 将内部分派指令转换为 protobuf 指令体
func toProtoDispatchItem(item DispatchItem) *wscpb.DispatchItem {
	return &wscpb.DispatchItem{
		Operation:     ToProtoDispatchOperation(item.Operation),
		AppId:         item.AppID,
		Namespace:     item.Namespace,
		GroupIds:      item.GroupIDs,
		TargetUserId:  item.TargetUserID,
		Reason:        item.Reason,
		MessageData:   item.MessageData,
		ExcludeSender: item.ExcludeSender,
		SenderId:      item.SenderID,
		TraceId:       item.TraceID,
	}
}

// classifyOutcome 将单条 protobuf 结果映射为投递分类
func classifyOutcome(op models.OperationType, r *wscpb.DispatchResult) DispatchOutcome {
	if r == nil || r.GetError() != "" {
		return OutcomeFallback
	}
	switch op {
	case models.OperationTypeSendMessage:
		if r.GetSuccess() {
			return OutcomeDelivered
		}
		// success=false 且无错误 = 目标节点明确用户不在（原 SendToUser 的 Success 语义）
		return OutcomeUserMiss
	case models.OperationTypeKickUser:
		if r.GetCount() > 0 {
			return OutcomeDelivered
		}
		// kicked=0 且无错误 = 目标节点无该用户连接（原 KickUser 扑空语义）
		return OutcomeUserMiss
	default:
		// 群组广播 / 观察者通知 / 全局广播：RPC 成功即视为送达（count 可为 0 = 无成员）
		return OutcomeDelivered
	}
}

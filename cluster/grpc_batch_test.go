/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-25 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-25 00:00:00
 * @FilePath: \go-wsc\cluster\grpc_batch_test.go
 * @Description: 跨节点 gRPC 微批分派器单元测试
 *
 * 覆盖：
 *   1. DispatchOperation 枚举与领域 OperationType 双向映射（含群组双 op 归一）
 *   2. classifyOutcome 三态分类（delivered / user_miss / fallback）
 *   3. toProtoDispatchItem 字段映射与枚举转换
 *   4. GRPCBatchDispatcher.Enabled 判定（nil pool / 零窗口 / 正常）
 *   5. 禁用微批时 Submit 收敛为 fallback
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package cluster

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// ============================================================================
// 1. 枚举双向映射
// ============================================================================

// TestDispatchOperationRoundTrip 领域 OperationType ↔ proto DispatchOperation 互逆
func TestDispatchOperationRoundTrip(t *testing.T) {
	ops := []models.OperationType{
		models.OperationTypeSendMessage,
		models.OperationTypeKickUser,
		models.OperationTypeGroupBroadcast,
		models.OperationTypeGroupsBroadcast,
		models.OperationTypeObserverNotify,
	}

	for _, op := range ops {
		proto := ToProtoDispatchOperation(op)
		assert.NotEqualf(t, wscpb.DispatchOperation_DISPATCH_OPERATION_UNSPECIFIED, proto,
			"操作 %q 不应映射为 UNSPECIFIED", op)
		back := FromProtoDispatchOperation(proto)
		// 群组双 op 在批量路径语义一致，统一归一为单群组 GroupBroadcast
		if op == models.OperationTypeGroupBroadcast || op == models.OperationTypeGroupsBroadcast {
			assert.Equal(t, models.OperationTypeGroupBroadcast, back, "群组 op 应归一为 GroupBroadcast")
		} else {
			assert.Equalf(t, op, back, "操作 %q 双向映射应互逆", op)
		}
	}

	// 未知枚举（UNSPECIFIED）→ 空操作类型（服务端 switch 落 default）
	assert.Equal(t, models.OperationType(""),
		FromProtoDispatchOperation(wscpb.DispatchOperation_DISPATCH_OPERATION_UNSPECIFIED))
	// 未知领域操作 → UNSPECIFIED
	assert.Equal(t, wscpb.DispatchOperation_DISPATCH_OPERATION_UNSPECIFIED,
		ToProtoDispatchOperation(models.OperationTypeHeartbeat))
	// 全局/命名空间广播在 dispatchViaGRPC 已短路走 PubSub，不再进入微批 gRPC 路径，
	// 故不映射为任何 proto 枚举（落 UNSPECIFIED，服务端 default 跳过）
	assert.Equal(t, wscpb.DispatchOperation_DISPATCH_OPERATION_UNSPECIFIED,
		ToProtoDispatchOperation(models.OperationTypeBroadcast))
}

// ============================================================================
// 2. classifyOutcome 三态分类
// ============================================================================

// TestClassifyOutcome 校验单条投递结果到投递分类的映射
func TestClassifyOutcome(t *testing.T) {
	// nil 结果 → fallback
	assert.Equal(t, OutcomeFallback, classifyOutcome(models.OperationTypeSendMessage, nil))
	// 显式错误 → fallback（send/kick/group 通用）
	assert.Equal(t, OutcomeFallback,
		classifyOutcome(models.OperationTypeSendMessage, &wscpb.DispatchResult{Error: "boom"}))

	// SendMessage：success=true → delivered；success=false 无错误 → user_miss
	assert.Equal(t, OutcomeDelivered,
		classifyOutcome(models.OperationTypeSendMessage, &wscpb.DispatchResult{Success: true}))
	assert.Equal(t, OutcomeUserMiss,
		classifyOutcome(models.OperationTypeSendMessage, &wscpb.DispatchResult{Success: false}))

	// KickUser：count>0 → delivered；count=0 无错误 → user_miss
	assert.Equal(t, OutcomeDelivered,
		classifyOutcome(models.OperationTypeKickUser, &wscpb.DispatchResult{Success: true, Count: 2}))
	assert.Equal(t, OutcomeUserMiss,
		classifyOutcome(models.OperationTypeKickUser, &wscpb.DispatchResult{Success: true, Count: 0}))

	// 群组广播/观察者通知/全局广播：RPC 成功即 delivered（count 可为 0）
	assert.Equal(t, OutcomeDelivered,
		classifyOutcome(models.OperationTypeGroupsBroadcast, &wscpb.DispatchResult{Success: true, Count: 0}))
	assert.Equal(t, OutcomeDelivered,
		classifyOutcome(models.OperationTypeObserverNotify, &wscpb.DispatchResult{Success: true}))
	assert.Equal(t, OutcomeDelivered,
		classifyOutcome(models.OperationTypeBroadcast, &wscpb.DispatchResult{Success: true}))
}

// ============================================================================
// 3. toProtoDispatchItem 字段映射
// ============================================================================

// TestToProtoDispatchItem 内部分派指令 → protobuf 指令体字段逐一映射
func TestToProtoDispatchItem(t *testing.T) {
	item := DispatchItem{
		Operation:     models.OperationTypeSendMessage,
		AppID:         "app1",
		Namespace:     "ns1",
		GroupIDs:      []string{"g1", "g2"},
		TargetUserID:  "u1",
		Reason:        "r1",
		MessageData:   []byte("m1"),
		ExcludeSender: true,
		SenderID:      "s1",
		TraceID:       "trace-x",
	}

	p := toProtoDispatchItem(item)
	assert.Equal(t, wscpb.DispatchOperation_DISPATCH_SEND_MESSAGE, p.GetOperation())
	assert.Equal(t, "app1", p.GetAppId())
	assert.Equal(t, "ns1", p.GetNamespace())
	assert.Equal(t, []string{"g1", "g2"}, p.GetGroupIds())
	assert.Equal(t, "u1", p.GetTargetUserId())
	assert.Equal(t, "r1", p.GetReason())
	assert.Equal(t, []byte("m1"), p.GetMessageData())
	assert.True(t, p.GetExcludeSender())
	assert.Equal(t, "s1", p.GetSenderId())
	assert.Equal(t, "trace-x", p.GetTraceId())
}

// ============================================================================
// 4. Enabled 判定
// ============================================================================

// TestGRPCBatchDispatcher_Enabled 微批启用条件：非 nil + 窗口 > 0 + 已注入连接池
func TestGRPCBatchDispatcher_Enabled(t *testing.T) {
	// nil 分派器
	assert.False(t, (*GRPCBatchDispatcher)(nil).Enabled())
	// nil 连接池
	assert.False(t, NewGRPCBatchDispatcher(nil, time.Millisecond, 10).Enabled())
	// 零窗口（禁用微批）
	assert.False(t, NewGRPCBatchDispatcher(NewGRPCClientPool(), 0, 10).Enabled())
	// 正常启用
	assert.True(t, NewGRPCBatchDispatcher(NewGRPCClientPool(), time.Millisecond, 10).Enabled())
}

// ============================================================================
// 5. 禁用微批时 Submit 收敛
// ============================================================================

// TestGRPCBatchDispatcher_SubmitDisabled 未启用时 Submit 直接返回 fallback
func TestGRPCBatchDispatcher_SubmitDisabled(t *testing.T) {
	d := NewGRPCBatchDispatcher(nil, time.Millisecond, 10)
	assert.Equal(t, OutcomeFallback, d.Submit(context.Background(), "addr", DispatchItem{}))

	// 关闭后提交应拒绝并返回 fallback
	d2 := NewGRPCBatchDispatcher(NewGRPCClientPool(), time.Millisecond, 10)
	d2.Close()
	assert.Equal(t, OutcomeFallback, d2.Submit(context.Background(), "addr", DispatchItem{}))
}

// ============================================================================
// 6. 微批合帧 flush 路径（mock gRPC server）
// ============================================================================

// batchMockServer 记录微批调用并回写成功结果的 mock NodeServiceServer
type batchMockServer struct {
	wscpb.UnimplementedNodeServiceServer
	mu      sync.Mutex
	batches int                   // 收到的 BatchDispatch RPC 次数
	items   []*wscpb.DispatchItem // 累计收到的全部 item（跨批追加）
	traces  []string              // 收到的逐条 trace_id
}

// BatchDispatch 记录单批请求并逐条回写 success 结果
func (m *batchMockServer) BatchDispatch(_ context.Context, req *wscpb.BatchDispatchRequest) (*wscpb.BatchDispatchResponse, error) {
	n := len(req.GetItems())
	m.mu.Lock()
	m.batches++
	m.items = append(m.items, req.GetItems()...)
	for _, it := range req.GetItems() {
		m.traces = append(m.traces, it.GetTraceId())
	}
	m.mu.Unlock()

	results := make([]*wscpb.DispatchResult, n)
	for i := range results {
		results[i] = &wscpb.DispatchResult{Success: true, Count: 1}
	}
	return &wscpb.BatchDispatchResponse{Results: results}, nil
}

// startBatchMockServer 在随机回环端口启动 mock gRPC server，返回 addr 与清理函数
func startBatchMockServer(t *testing.T, mock *batchMockServer) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	wscpb.RegisterNodeServiceServer(srv, mock)
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), func() {
		srv.Stop()
		_ = lis.Close()
	}
}

// TestGRPCBatchDispatcher_FlushAndResolve 两条指令合帧为单次 RPC，逐条回写结果并透传 trace
func TestGRPCBatchDispatcher_FlushAndResolve(t *testing.T) {
	mock := &batchMockServer{}
	addr, cleanup := startBatchMockServer(t, mock)
	defer cleanup()

	pool := NewGRPCClientPool()
	defer pool.Close()
	// maxBatch=2：第二条到达即触发立即 flush（不依赖窗口定时器，测试无时序抖动）
	d := NewGRPCBatchDispatcher(pool, 200*time.Millisecond, 2)
	defer d.Close()

	item1 := DispatchItem{Operation: models.OperationTypeSendMessage, TargetUserID: "u1", TraceID: "trace-1"}
	item2 := DispatchItem{Operation: models.OperationTypeSendMessage, TargetUserID: "u2", TraceID: "trace-2"}

	// Submit 同步阻塞，需并发提交才能在同一窗口内攒批
	var o1, o2 DispatchOutcome
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); o1 = d.Submit(context.Background(), addr, item1) }()
	go func() { defer wg.Done(); o2 = d.Submit(context.Background(), addr, item2) }()
	wg.Wait()

	assert.Equal(t, OutcomeDelivered, o1, "第一条应投递成功")
	assert.Equal(t, OutcomeDelivered, o2, "第二条应投递成功")

	mock.mu.Lock()
	defer mock.mu.Unlock()
	assert.Equal(t, 1, mock.batches, "两条指令应合帧为单次 RPC")
	assert.Len(t, mock.items, 2, "单批应包含 2 条 item")
	assert.ElementsMatch(t, []string{"trace-1", "trace-2"}, mock.traces, "应逐条透传 trace_id")
}

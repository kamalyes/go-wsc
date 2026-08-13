/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-26 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-13 19:02:35
 * @FilePath: \go-wsc\models\trace_test.go
 * @Description: trace_id 链路串联测试（models 内部）
 *   - HubMessage.InjectContext / ContextFromMessage
 *   - DistributedMessage.InjectContext / ContextFromMessage
 *   - JSON 序列化后 trace_id 保留
 *   - 同一 trace_id 不重复注入
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kamalyes/go-logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// ============================================================================
// HubMessage trace 测试
// ============================================================================

// TestHubMessage_InjectContext_FromCtxValue 从 ctx.Value 注入 trace_id
func TestHubMessage_InjectContext_FromCtxValue(t *testing.T) {
	traceID := "test-trace-abc123"
	ctx := context.WithValue(context.Background(), logger.ContextKeyTraceID, traceID)

	msg := NewHubMessage()
	msg.InjectContext(ctx)

	assert.Equal(t, traceID, msg.TraceID, "TraceID should be extracted from ctx.Value")
}

// TestHubMessage_InjectContext_NoOverwrite 已有 trace_id 时不覆盖
func TestHubMessage_InjectContext_NoOverwrite(t *testing.T) {
	originalTraceID := "original-trace-id"
	ctx := context.WithValue(context.Background(), logger.ContextKeyTraceID, "new-trace-id")

	msg := NewHubMessage()
	msg.TraceID = originalTraceID
	msg.InjectContext(ctx)

	assert.Equal(t, originalTraceID, msg.TraceID, "existing TraceID should not be overwritten")
}

// TestHubMessage_InjectContext_NilCtx nil ctx 不 panic
func TestHubMessage_InjectContext_NilCtx(t *testing.T) {
	msg := NewHubMessage()
	assert.NotPanics(t, func() {
		msg.InjectContext(nil)
	}, "InjectContext should not panic with nil ctx")
	assert.Equal(t, "", msg.TraceID, "TraceID should be empty with nil ctx")
}

// TestHubMessage_InjectContext_EmptyCtx 空 ctx 返回空 trace_id
func TestHubMessage_InjectContext_EmptyCtx(t *testing.T) {
	msg := NewHubMessage()
	msg.InjectContext(context.Background())
	assert.Equal(t, "", msg.TraceID, "TraceID should be empty with background ctx")
}

// TestHubMessage_ContextFromMessage 从消息恢复 trace_id 到 ctx
func TestHubMessage_ContextFromMessage(t *testing.T) {
	traceID := "trace-from-message-xyz"
	msg := NewHubMessage()
	msg.TraceID = traceID

	parent := context.Background()
	ctx := msg.ContextFromMessage(parent)

	// 验证 ctx 中有 trace_id
	extracted := ctx.Value(logger.ContextKeyTraceID)
	assert.Equal(t, traceID, extracted, "trace_id should be restored to ctx")
}

// TestHubMessage_ContextFromMessage_EmptyTraceID 空 trace_id 不修改 ctx
func TestHubMessage_ContextFromMessage_EmptyTraceID(t *testing.T) {
	msg := NewHubMessage()
	assert.Equal(t, "", msg.TraceID)

	parent := context.Background()
	ctx := msg.ContextFromMessage(parent)

	// 空 TraceID 时 ctx 中不应有 trace_id
	assert.Nil(t, ctx.Value(logger.ContextKeyTraceID), "should not have trace_id in ctx when TraceID is empty")
}

// TestHubMessage_TraceID_JSONRoundTrip JSON 序列化/反序列化保留 trace_id
func TestHubMessage_TraceID_JSONRoundTrip(t *testing.T) {
	traceID := "json-round-trip-trace"
	msg := NewHubMessage()
	msg.TraceID = traceID
	msg.ID = "msg-1"
	msg.Content = "hello"

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	var decoded HubMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, traceID, decoded.TraceID, "TraceID should survive JSON round-trip")
}

// TestHubMessage_TraceID_OmitEmpty 空 trace_id 不序列化到 JSON
func TestHubMessage_TraceID_OmitEmpty(t *testing.T) {
	msg := NewHubMessage()
	assert.Equal(t, "", msg.TraceID)

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// trace_id 有 omitempty，空值不应出现在 JSON 中
	var m map[string]interface{}
	err = json.Unmarshal(data, &m)
	require.NoError(t, err)
	_, exists := m["trace_id"]
	assert.False(t, exists, "empty trace_id should be omitted from JSON")
}

// TestHubMessage_GetTraceID 获取 trace_id
func TestHubMessage_GetTraceID(t *testing.T) {
	msg := NewHubMessage()
	assert.Equal(t, "", msg.GetTraceID())

	msg.TraceID = "trace-123"
	assert.Equal(t, "trace-123", msg.GetTraceID())
}

// TestHubMessage_Clone_TraceID Clone 保留 trace_id
func TestHubMessage_Clone_TraceID(t *testing.T) {
	msg := NewHubMessage()
	msg.TraceID = "clone-trace"
	msg.ID = "msg-1"

	cloned := msg.Clone()
	assert.Equal(t, msg.TraceID, cloned.TraceID, "Clone should preserve TraceID")

	// 修改 clone 不影响原始
	cloned.TraceID = "modified-trace"
	assert.Equal(t, "clone-trace", msg.TraceID, "original TraceID should not be affected")
}

// ============================================================================
// DistributedMessage trace 测试
// ============================================================================

// TestDistributedMessage_InjectContext 从 ctx 注入 trace_id
func TestDistributedMessage_InjectContext(t *testing.T) {
	traceID := "dist-trace-456"
	ctx := context.WithValue(context.Background(), logger.ContextKeyTraceID, traceID)

	dm := &DistributedMessage{
		Type:     OperationTypeSendMessage,
		NodeID:   "node-1",
		TargetID: "user-1",
		Message:  NewHubMessage(),
	}
	dm.InjectContext(ctx)

	assert.Equal(t, traceID, dm.TraceID, "TraceID should be injected from ctx")
}

// TestDistributedMessage_InjectContext_NoOverwrite 已有 trace_id 不覆盖
func TestDistributedMessage_InjectContext_NoOverwrite(t *testing.T) {
	originalTrace := "original-dist-trace"
	ctx := context.WithValue(context.Background(), logger.ContextKeyTraceID, "new-dist-trace")

	dm := &DistributedMessage{
		Type:    OperationTypeSendMessage,
		NodeID:  "node-1",
		TraceID: originalTrace,
		Message: NewHubMessage(),
	}
	dm.InjectContext(ctx)

	assert.Equal(t, originalTrace, dm.TraceID, "existing TraceID should not be overwritten")
}

// TestDistributedMessage_ContextFromMessage 从分布式消息恢复 trace_id 到 ctx
func TestDistributedMessage_ContextFromMessage(t *testing.T) {
	traceID := "dist-restore-trace"
	dm := &DistributedMessage{
		Type:     OperationTypeSendMessage,
		NodeID:   "node-1",
		TargetID: "user-1",
		TraceID:  traceID,
		Message:  NewHubMessage(),
	}

	parent := context.Background()
	ctx := dm.ContextFromMessage(parent)

	extracted := ctx.Value(logger.ContextKeyTraceID)
	assert.Equal(t, traceID, extracted, "trace_id should be restored from DistributedMessage")
}

// TestDistributedMessage_ContextFromMessage_EmptyTraceID 空 trace_id 返回原 ctx
func TestDistributedMessage_ContextFromMessage_EmptyTraceID(t *testing.T) {
	dm := &DistributedMessage{
		Type:   OperationTypeSendMessage,
		NodeID: "node-1",
	}

	parent := context.Background()
	ctx := dm.ContextFromMessage(parent)

	// 空 TraceID 时 ctx 中不应有 trace_id
	assert.Nil(t, ctx.Value(logger.ContextKeyTraceID), "should not have trace_id in ctx when TraceID is empty")
}

// TestDistributedMessage_TraceID_JSONRoundTrip JSON 序列化保留 trace_id
func TestDistributedMessage_TraceID_JSONRoundTrip(t *testing.T) {
	traceID := "dist-json-trace"
	dm := &DistributedMessage{
		Type:     OperationTypeSendMessage,
		NodeID:   "node-1",
		TargetID: "user-1",
		TraceID:  traceID,
		Message:  NewHubMessage(),
	}

	data, err := json.Marshal(dm)
	require.NoError(t, err)

	var decoded DistributedMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, traceID, decoded.TraceID, "TraceID should survive JSON round-trip")
}

// ============================================================================
// gRPC metadata trace 传播测试
// ============================================================================

// TestGRPCMetadata_TraceIDPropagation gRPC metadata 注入/提取 trace_id
func TestGRPCMetadata_TraceIDPropagation(t *testing.T) {
	traceID := "grpc-trace-propagation"

	// 1. 注入到 outgoing metadata
	ctx := context.Background()
	ctx = logger.ContextWithTraceID(ctx, traceID)
	outgoingCtx := logger.InjectTraceToOutgoing(ctx, traceID)

	// 验证 metadata 中有 trace_id
	md, ok := metadata.FromOutgoingContext(outgoingCtx)
	assert.True(t, ok, "outgoing context should have metadata")
	vals := md.Get(logger.ContextKeyTraceID)
	assert.NotEmpty(t, vals, "metadata should contain trace_id")
	assert.Equal(t, traceID, vals[0], "metadata trace_id should match")

	// 2. 模拟 gRPC 传输：从 outgoing metadata 构造 incoming metadata
	incomingMD := metadata.MD{}
	for k, v := range md {
		incomingMD[k] = v
	}
	incomingCtx := metadata.NewIncomingContext(context.Background(), incomingMD)

	// 3. 从 incoming metadata 提取 trace_id
	extractedTraceID := logger.ExtractTraceFromIncoming(incomingCtx)
	assert.Equal(t, traceID, extractedTraceID, "should extract trace_id from incoming metadata")

	// 4. 恢复到 ctx
	restoredCtx := logger.RestoreTraceFromIncoming(incomingCtx)
	extractedFromCtx := restoredCtx.Value(logger.ContextKeyTraceID)
	assert.Equal(t, traceID, extractedFromCtx, "trace_id should be restored to ctx")
}

// TestGRPCMetadata_EmptyTraceID 空 trace_id 不注入 metadata
func TestGRPCMetadata_EmptyTraceID(t *testing.T) {
	ctx := context.Background()
	result := logger.InjectTraceToOutgoing(ctx, "")
	// 空 trace_id 时 metadata 中不应有 trace_id
	md, ok := metadata.FromOutgoingContext(result)
	if ok {
		vals := md.Get(logger.ContextKeyTraceID)
		assert.Empty(t, vals, "should not inject trace_id to metadata when empty")
	}
}

// ============================================================================
// 端到端链路测试
// ============================================================================

// TestE2E_TraceChain_HubMessage 完整链路: ctx → HubMessage → JSON → 反序列化 → ctx
func TestE2E_TraceChain_HubMessage(t *testing.T) {
	traceID := "e2e-hub-trace-789"

	// 1. 原始 ctx 携带 trace_id
	originalCtx := context.WithValue(context.Background(), logger.ContextKeyTraceID, traceID)

	// 2. 注入到消息
	msg := NewHubMessage()
	msg.InjectContext(originalCtx)
	assert.Equal(t, traceID, msg.TraceID)

	// 3. JSON 序列化
	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// 4. 反序列化（模拟跨节点传输）
	var decoded HubMessage
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, traceID, decoded.TraceID, "trace_id should survive serialization")

	// 5. 恢复到新 ctx（模拟接收端）
	receiverCtx := decoded.ContextFromMessage(context.Background())
	extracted := receiverCtx.Value(logger.ContextKeyTraceID)
	assert.Equal(t, traceID, extracted, "trace_id should be restored on receiver side")
}

// TestE2E_TraceChain_GRPCCrossNode gRPC 跨节点完整链路
func TestE2E_TraceChain_GRPCCrossNode(t *testing.T) {
	traceID := "e2e-grpc-cross-node-345"

	// 1. 发送端: ctx 携带 trace_id
	senderCtx := context.WithValue(context.Background(), logger.ContextKeyTraceID, traceID)

	// 2. 注入到消息
	msg := NewHubMessage()
	msg.InjectContext(senderCtx)

	// 3. 构建 DistributedMessage 并注入
	dm := &DistributedMessage{
		Type:     OperationTypeSendMessage,
		NodeID:   "node-sender",
		TargetID: "user-receiver",
		Message:  msg,
	}
	dm.InjectContext(senderCtx)

	// 4. 模拟 gRPC 客户端: 注入 trace_id 到 outgoing metadata
	outgoingCtx := logger.InjectTraceToOutgoing(senderCtx, traceID)

	// 5. 模拟网络传输: outgoing → incoming metadata
	outMD, _ := metadata.FromOutgoingContext(outgoingCtx)
	inMD := metadata.MD{}
	for k, v := range outMD {
		inMD[k] = v
	}
	incomingCtx := metadata.NewIncomingContext(context.Background(), inMD)

	// 6. 模拟 gRPC 服务端: 从 incoming metadata 恢复 trace_id
	restoredCtx := logger.RestoreTraceFromIncoming(incomingCtx)
	extractedFromMD := restoredCtx.Value(logger.ContextKeyTraceID)
	assert.Equal(t, traceID, extractedFromMD, "trace_id should be restored from gRPC metadata")

	// 7. 消息体也恢复 trace_id（双重保障）
	restoredCtx = msg.ContextFromMessage(restoredCtx)
	assert.Equal(t, traceID, restoredCtx.Value(logger.ContextKeyTraceID), "trace_id should also be in ctx from message body")
}

// TestE2E_TraceChain_NoDuplication 同一 trace_id 不重复注入
func TestE2E_TraceChain_NoDuplication(t *testing.T) {
	traceID := "no-dup-trace"

	ctx := context.WithValue(context.Background(), logger.ContextKeyTraceID, traceID)

	// 多次调用 InjectContext 不改变 trace_id
	msg := NewHubMessage()
	msg.InjectContext(ctx)
	firstTraceID := msg.TraceID

	msg.InjectContext(ctx)
	assert.Equal(t, firstTraceID, msg.TraceID, "repeated InjectContext should not change TraceID")
}

// TestE2E_TraceChain_LoggerContext logger 从 ctx 自动提取 trace_id
func TestE2E_TraceChain_LoggerContext(t *testing.T) {
	traceID := "logger-auto-trace"

	// 1. 通过 HubMessage 恢复 ctx
	msg := NewHubMessage()
	msg.TraceID = traceID
	ctx := msg.ContextFromMessage(context.Background())

	// 2. 验证 logger.ExtractTraceID 可以从恢复后的 ctx 提取
	extracted := logger.ExtractTraceID(ctx)
	assert.Equal(t, traceID, extracted, "logger.ExtractTraceID should extract from restored ctx")

	// 3. 通过 DistributedMessage 恢复 ctx
	dm := &DistributedMessage{
		Type:    OperationTypeSendMessage,
		TraceID: traceID,
	}
	ctx2 := dm.ContextFromMessage(context.Background())
	extracted2 := logger.ExtractTraceID(ctx2)
	assert.Equal(t, traceID, extracted2, "logger.ExtractTraceID should extract from DistributedMessage restored ctx")
}

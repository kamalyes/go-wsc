/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-26 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-13 19:02:35
 * @FilePath: \go-wsc\models\trace_pb_test.go
 * @Description: trace_id Protobuf 序列化链路测试（外部测试包，避免循环导入）
 *   - HubMessage protobuf round-trip 保留 trace_id
 *   - DistributedMessage protobuf round-trip 保留 trace_id
 *   - 端到端: ctx → 消息 → protobuf → 反序列化 → ctx 恢复
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models_test

import (
	"context"
	"testing"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/metadata"
)

// TestProtobuf_HubMessage_TraceID protobuf 序列化/反序列化保留 trace_id
func TestProtobuf_HubMessage_TraceID(t *testing.T) {
	traceID := "pb-hub-trace-abc"
	msg := models.NewHubMessage()
	msg.TraceID = traceID
	msg.ID = "msg-pb-1"
	msg.MessageID = "msg-id-pb-1"
	msg.SessionID = "session-1"
	msg.Content = "hello pb"

	data, err := wscpb.MarshalHubMessage(msg)
	require.NoError(t, err)

	decoded, err := wscpb.UnmarshalHubMessage(data)
	require.NoError(t, err)

	assert.Equal(t, traceID, decoded.TraceID, "TraceID should survive protobuf round-trip")
}

// TestProtobuf_DistributedMessage_TraceID protobuf 序列化保留分布式消息 trace_id
func TestProtobuf_DistributedMessage_TraceID(t *testing.T) {
	traceID := "pb-dist-trace-xyz"
	msg := models.NewHubMessage()
	msg.TraceID = "inner-msg-trace"
	msg.ID = "msg-dist-1"
	msg.MessageID = "msg-id-dist-1"
	msg.SessionID = "session-1"
	msg.Content = "hello"

	dm := &models.DistributedMessage{
		Type:     models.OperationTypeSendMessage,
		NodeID:   "node-1",
		TargetID: "user-1",
		TraceID:  traceID,
		Message:  msg,
	}

	data, err := wscpb.MarshalDistributedMessage(dm)
	require.NoError(t, err)

	decoded, err := wscpb.UnmarshalDistributedMessage(data)
	require.NoError(t, err)

	assert.Equal(t, traceID, decoded.TraceID, "DistributedMessage TraceID should survive protobuf round-trip")
	assert.Equal(t, "inner-msg-trace", decoded.Message.TraceID, "nested HubMessage TraceID should survive protobuf round-trip")
}

// TestE2E_Protobuf_TraceChain 完整 protobuf 链路: ctx → 消息 → protobuf → 反序列化 → ctx
func TestE2E_Protobuf_TraceChain(t *testing.T) {
	traceID := "e2e-pb-trace-full"

	// 1. 原始 ctx 携带 trace_id
	originalCtx := context.WithValue(context.Background(), logger.ContextKeyTraceID, traceID)

	// 2. 注入到消息
	msg := models.NewHubMessage()
	msg.InjectContext(originalCtx)

	// 3. 构建 DistributedMessage 并注入
	dm := &models.DistributedMessage{
		Type:     models.OperationTypeSendMessage,
		NodeID:   "node-sender",
		TargetID: "user-receiver",
		Message:  msg,
	}
	dm.InjectContext(originalCtx)

	// 4. protobuf 序列化
	data, err := wscpb.MarshalDistributedMessage(dm)
	require.NoError(t, err)

	// 5. 反序列化（模拟接收端）
	decoded, err := wscpb.UnmarshalDistributedMessage(data)
	require.NoError(t, err)
	assert.Equal(t, traceID, decoded.TraceID, "DistributedMessage trace_id should survive protobuf")
	assert.Equal(t, traceID, decoded.Message.TraceID, "HubMessage trace_id should survive protobuf")

	// 6. 恢复到接收端 ctx
	receiverCtx := decoded.ContextFrom(context.Background())
	extracted := receiverCtx.Value(logger.ContextKeyTraceID)
	assert.Equal(t, traceID, extracted, "trace_id should be restored on receiver side")

	// 7. 验证 logger.ExtractTraceID 也能提取
	loggerExtracted := logger.ExtractTraceID(receiverCtx)
	assert.Equal(t, traceID, loggerExtracted, "logger.ExtractTraceID should work with restored ctx")
}

// TestE2E_Protobuf_GRPCCrossNode 完整 gRPC + protobuf 跨节点链路
func TestE2E_Protobuf_GRPCCrossNode(t *testing.T) {
	traceID := "e2e-pb-grpc-full"

	// 1. 发送端: ctx → 消息注入 → metadata 注入
	senderCtx := context.WithValue(context.Background(), logger.ContextKeyTraceID, traceID)

	msg := models.NewHubMessage()
	msg.InjectContext(senderCtx)
	assert.Equal(t, traceID, msg.TraceID)

	dm := &models.DistributedMessage{
		Type:     models.OperationTypeSendMessage,
		NodeID:   "node-sender",
		TargetID: "user-receiver",
		Message:  msg,
	}
	dm.InjectContext(senderCtx)

	// gRPC metadata 注入
	outgoingCtx := logger.InjectTraceToOutgoing(senderCtx, traceID)

	// 2. 模拟 gRPC 传输
	outMD, _ := metadata.FromOutgoingContext(outgoingCtx)
	inMD := metadata.MD{}
	for k, v := range outMD {
		inMD[k] = v
	}
	incomingCtx := metadata.NewIncomingContext(context.Background(), inMD)

	// 3. 模拟 protobuf 传输
	wscpbData, err := wscpb.MarshalDistributedMessage(dm)
	require.NoError(t, err)
	decodedDM, err := wscpb.UnmarshalDistributedMessage(wscpbData)
	require.NoError(t, err)

	// 4. 接收端: metadata 恢复 + 消息体恢复（双重保障）
	restoredCtx := logger.RestoreTraceFromIncoming(incomingCtx)
	assert.Equal(t, traceID, restoredCtx.Value(logger.ContextKeyTraceID), "trace_id from gRPC metadata")

	restoredCtx = decodedDM.Message.ContextFrom(restoredCtx)
	assert.Equal(t, traceID, restoredCtx.Value(logger.ContextKeyTraceID), "trace_id from message body (double guarantee)")

	// 5. 验证 DistributedMessage 的 trace_id 也保留了
	assert.Equal(t, traceID, decodedDM.TraceID, "DistributedMessage trace_id preserved")
}

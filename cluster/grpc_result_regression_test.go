/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-26 02:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 20:31:08
 * @FilePath: \go-wsc\cluster\\grpc_result_regression_test.go
 * @Description: 跨节点投递结果回归：grpc 结果语义与死索引自愈
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package cluster

import (
	"testing"

	"github.com/kamalyes/go-wsc/models"
	pb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/stretchr/testify/require"
)

func TestBatchOnlineRefusalPreservesIndex(t *testing.T) {
	require.Equal(t, OutcomeRejected, classifyOutcome(models.OperationTypeSendMessage, &pb.DispatchResult{UserOnline: true, Success: false, Error: "admission rejected"}))
	require.Equal(t, OutcomeUserMiss, classifyOutcome(models.OperationTypeSendMessage, &pb.DispatchResult{}))
	require.Equal(t, OutcomeDelivered, classifyOutcome(models.OperationTypeSendMessage, &pb.DispatchResult{Success: true, UserOnline: true}))
}

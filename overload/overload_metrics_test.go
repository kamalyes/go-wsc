/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-25 23:50:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-25 23:50:00
 * @FilePath: \go-wsc\overload\overload_metrics_test.go
 * @Description: 投递路由分支埋点测试（deliverModes / cluster gRPC / PubSub 兜底）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package overload

import (
	"testing"

	"github.com/kamalyes/go-wsc/models"
)

// TestDeliverModeIndex 决策树五模式桶下标与枚举顺序一一对应
func TestDeliverModeIndex(t *testing.T) {
	cases := []struct {
		mode models.DeliveryMode
		want int
	}{
		{models.DeliveryModeP2P, 0},
		{models.DeliveryModeGroupReliable, 1},
		{models.DeliveryModeGroupBroadcast, 2},
		{models.DeliveryModeNamespace, 3},
		{models.DeliveryModeGlobal, 4},
	}
	for _, c := range cases {
		if got := deliverModeIndex(c.mode); got != c.want {
			t.Fatalf("deliverModeIndex(%v) = %d, want %d", c.mode, got, c.want)
		}
	}
}

// TestRecordDeliverMode 各分支计数归入独立桶，公开快照可见
func TestRecordDeliverMode(t *testing.T) {
	var m OverloadMetrics
	m.RecordDeliverMode(models.DeliveryModeP2P)
	m.RecordDeliverMode(models.DeliveryModeP2P)
	m.RecordDeliverMode(models.DeliveryModeGlobal)

	stats := m.OverloadStats()
	modes := stats["deliver_modes"].(map[string]int64)
	if modes["p2p"] != 2 {
		t.Fatalf("p2p = %d, want 2", modes["p2p"])
	}
	if modes["global"] != 1 {
		t.Fatalf("global = %d, want 1", modes["global"])
	}
	if modes["group_broadcast"] != 0 {
		t.Fatalf("group_broadcast = %d, want 0", modes["group_broadcast"])
	}
}

// TestRecordClusterMetrics 跨节点 gRPC 消息/节点数与 PubSub 兜底计数独立累加
func TestRecordClusterMetrics(t *testing.T) {
	var m OverloadMetrics
	m.RecordClusterGRPC(3)
	m.RecordClusterGRPC(1)
	m.RecordClusterPubSubFallback()
	m.RecordClusterPubSubFallback()

	snap := m.MetricsSnapshot()
	if snap["cluster_grpc_messages"] != 2 {
		t.Fatalf("cluster_grpc_messages = %d, want 2", snap["cluster_grpc_messages"])
	}
	if snap["cluster_grpc_nodes"] != 4 {
		t.Fatalf("cluster_grpc_nodes = %d, want 4", snap["cluster_grpc_nodes"])
	}
	if snap["cluster_pubsub_fallbacks"] != 2 {
		t.Fatalf("cluster_pubsub_fallbacks = %d, want 2", snap["cluster_pubsub_fallbacks"])
	}
}

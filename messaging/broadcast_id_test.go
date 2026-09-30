/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-30 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-30 00:00:00
 * @FilePath: \go-wsc\messaging\broadcast_id_test.go
 * @Description: 广播消息 Hub 内部 ID 生成测试
 *
 * 契约：每条消息都有唯一标识，不因非 ACK 路径缺席——
 *   - 广播类（群组/命名空间/全局）：Deliver 入口生成纯雪花 ID（无单一接收者）
 *   - P2P：send 路径生成 userID 前缀 ID（ACK 对账语义），Deliver 入口不抢先
 *   - HandleBroadcastMessage（跨节点/队列消费入口）：幂等兜底，已有不覆盖
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
)

// receiveJSON 从客户端 SendChan 取一条序列化消息并解析为 map
// （fanout 路径预序列化，客户端视角验证投递副本的最终形态）
func receiveJSON(t *testing.T, c *models.Client) map[string]any {
	t.Helper()
	select {
	case data := <-c.SendChan:
		var payload map[string]any
		require.NoError(t, json.Unmarshal(data, &payload))
		return payload
	default:
		t.Fatalf("客户端 %s 应收到消息", c.UserID)
		return nil
	}
}

// TestDeliverBroadcastGeneratesHubID fire-and-forget 群组广播（跑马灯同型）生成 Hub 内部 ID
// Deliver 入口 Clone：ID 生成在投递副本上，调用方原对象零侵入——
// 经在线成员收到的序列化数据验证（修复前跑马灯消息 id 恒为空串）
// fire-and-forget 的成员定位走本地反向索引（客户端 GroupID 标签），不走 GroupStore
func TestDeliverBroadcastGeneratesHubID(t *testing.T) {
	m, host := newTestManager()

	cRecv := makeTestClient("c-recv", "u-recv", "tenantA", "g-idgen")
	host.GetShardedRegistry().AddClient(cRecv)

	msg := makeGroupMessage("owner1")
	require.Empty(t, msg.ID, "前置：构造时无 ID")

	groupCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantA").WithGroupIDs([]string{"g-idgen"}).Inject(context.Background())
	result := m.Deliver(groupCtx, msg, false)
	require.NotNil(t, result)

	payload := receiveJSON(t, cRecv)
	assert.NotEmpty(t, payload["id"], "广播消息应生成 Hub 内部 ID（非 ACK 路径同样有唯一标识）")
	assert.Empty(t, msg.ID, "调用方原对象不受影响（Deliver 入口 Clone，ID 在投递副本上生成）")
}

// TestHandleBroadcastMessageGeneratesHubID 本地直投/跨节点消费入口：空 ID 就地生成，已有幂等不覆盖
func TestHandleBroadcastMessageGeneratesHubID(t *testing.T) {
	m, _ := newTestManager()

	msg := makeGroupMessage("owner2")
	require.Empty(t, msg.ID, "前置：构造时无 ID")

	m.HandleBroadcastMessage(context.Background(), msg)
	assert.NotEmpty(t, msg.ID, "广播入口应为空 ID 消息生成 Hub 内部 ID")

	idFirst := msg.ID
	m.HandleBroadcastMessage(context.Background(), msg)
	assert.Equal(t, idFirst, msg.ID, "已生成的 ID 不应被覆盖（幂等）")
}

// TestDeliverP2PKeepsSendPathPrefix P2P 分支不在 Deliver 入口生成：
// 客户端收到的 id 保持 send 路径的 userID 前缀格式（ACK 对账语义），非入口纯雪花
// 在线判定按 appID+namespace 匹配本地注册表，路由 ctx 须与客户端标签对齐
func TestDeliverP2PKeepsSendPathPrefix(t *testing.T) {
	m, host := newTestManager()

	cRecv := makeTestClient("c-p2p", "u-p2p", "tenantA")
	host.GetShardedRegistry().AddClient(cRecv)

	msg := makeGroupMessage("owner3")
	msg.Receiver = "u-p2p"

	p2pCtx := routing.NewRoute().WithAppID(constants.DefaultAppID).WithNamespace("tenantA").Inject(context.Background())
	result := m.Deliver(p2pCtx, msg, false)
	require.NotNil(t, result)

	payload := receiveJSON(t, cRecv)
	id, _ := payload["id"].(string)
	assert.True(t, strings.HasPrefix(id, "u-p2p-"),
		"P2P 的 ID 应为 send 路径的 userID 前缀格式（实际: %q）", id)
}

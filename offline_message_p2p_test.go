/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-09 00:16:29
 * @FilePath: \go-wsc\offline_message_p2p_test.go
 * @Description: P2P 离线消息补默认组维度一致性集成测试
 *
 * 验证 P2P（group=nil）场景下，store/drain/clear 三处复用 normalizeGroupID 补默认组后：
 *   - Redis store 落点与 drain 取点 key 一致（不丢消息）
 *   - Redis clear key 与 store key 一致（清得掉）
 *   - MySQL group_id 字段补 DefaultGroupID（与 Redis key 维度一致）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"testing"

	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOfflineMessageHandler_P2PStoreDrainDimension P2P 消息 store/drain 维度一致
// newTestOfflineHandlerContext 的 ctx 已注入 DefaultNamespace + group=nil（P2P）
// store 与 drain 均经 queueKey 补默认组，落点/取点 key 必须一致才能取回消息
func TestOfflineMessageHandler_P2PStoreDrainDimension(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	// 1. 存储 3 条 P2P 消息（显式设置唯一 MessageID 避免唯一索引冲突）
	for i := 0; i < 3; i++ {
		msg := tc.createTestMessage(userID)
		msg.MessageID = tc.idGen.GenerateCorrelationID()
		require.NoError(t, tc.handler.StoreOfflineMessage(tc.ctx, userID, msg))
	}

	// 2. MySQL group_id 应补 DefaultGroupID（storeToDatabase 与 Redis key 维度一致）
	db := GetTestDBWithMigration(t, &OfflineMessageRecord{})
	var records []OfflineMessageRecord
	require.NoError(t, db.Where("receiver = ?", userID).Find(&records).Error)
	require.Len(t, records, 3, "MySQL 应有 3 条记录")
	for _, r := range records {
		assert.Equal(t, models.DefaultGroupID, r.GroupID,
			"P2P 消息 MySQL group_id 应补 DefaultGroupID，与 Redis key 维度一致")
	}

	// 3. drain 取回全部（store 落点 = drain 取点，补默认组后 key 一致）
	msgs, err := tc.handler.DrainOfflineQueue(tc.ctx, userID, 0)
	require.NoError(t, err)
	assert.Len(t, msgs, 3, "drain 应取回全部 P2P 消息（store/drain key 一致）")

	// 4. Redis 排空后再 drain 应无消息
	msgs2, err := tc.handler.DrainOfflineQueue(tc.ctx, userID, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs2, "Redis 排空后再 drain 应无消息")
}

// TestOfflineMessageHandler_P2PClearDimension P2P 消息 clear 维度一致
// clear 的 groupIDs=[""]（P2P）经 normalizeGroupID 补默认组后，clear key = store key
func TestOfflineMessageHandler_P2PClearDimension(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	// 存储 1 条 P2P 消息
	msg := tc.createTestMessage(userID)
	msg.MessageID = tc.idGen.GenerateCorrelationID()
	require.NoError(t, tc.handler.StoreOfflineMessage(tc.ctx, userID, msg))

	// clear P2P（groupIDs 含 "" 表示 P2P 队列，补默认组后 clear key = store key）
	require.NoError(t, tc.handler.ClearOfflineMessages(tc.ctx, userID, []string{""}))

	// drain 取不到（Redis 已被 clear 清空，clear key = store key）
	msgs, err := tc.handler.DrainOfflineQueue(tc.ctx, userID, 0)
	require.NoError(t, err)
	assert.Empty(t, msgs, "clear 后 drain 应无消息（clear key = store key）")

	// count 也应为 0（ClearOfflineMessages 同时清 MySQL）
	count, err := tc.handler.GetOfflineMessageCount(tc.ctx, userID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count, "clear 后 MySQL 离线消息计数应为 0")
}

// TestOfflineMessageHandler_P2PAndGroupIsolation P2P(默认组) 与真实群组消息隔离
// 同一用户、同一 namespace 下，P2P 消息（默认组）与群组消息落到不同 key，互不干扰
func TestOfflineMessageHandler_P2PAndGroupIsolation(t *testing.T) {
	tc := newTestOfflineHandlerContext(t)
	defer tc.cleanup()

	userID := tc.idGen.GenerateCorrelationID()
	tc.cleanupUserIDs = append(tc.cleanupUserIDs, userID)

	// P2P 消息（tc.ctx: group=nil → 默认组）
	p2pMsg := tc.createTestMessage(userID)
	p2pMsg.MessageID = tc.idGen.GenerateCorrelationID()
	require.NoError(t, tc.handler.StoreOfflineMessage(tc.ctx, userID, p2pMsg))

	// 群组消息（注入真实 group，namespace 保持 DefaultNamespace）
	groupCtx := routing.NewRoute().WithAppID("").WithNamespace(models.DefaultNamespace).WithGroupIDs([]string{"g-real"}).Inject(tc.ctx)
	groupMsg := tc.createTestMessage(userID)
	groupMsg.MessageID = tc.idGen.GenerateCorrelationID()
	require.NoError(t, tc.handler.StoreOfflineMessage(groupCtx, userID, groupMsg))

	// drain P2P 队列：只取到 P2P 消息（默认组），不含群组消息
	p2pMsgs, err := tc.handler.DrainOfflineQueue(tc.ctx, userID, 0)
	require.NoError(t, err)
	assert.Len(t, p2pMsgs, 1, "P2P 队列只应有 1 条 P2P 消息，群组消息隔离")

	// drain 群组队列：只取到群组消息
	groupMsgs, err := tc.handler.DrainOfflineQueue(groupCtx, userID, 0)
	require.NoError(t, err)
	assert.Len(t, groupMsgs, 1, "群组队列只应有 1 条群组消息，P2P 消息隔离")
}

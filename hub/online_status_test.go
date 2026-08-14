/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-08 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-11 10:08:16
 * @FilePath: \go-wsc\hub\online_status_test.go
 * @Description: Hub 在线状态方法白盒单元测试（覆盖 hub/online_status.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetAllOnlineUserIDs_NoRepo 验证无 repository 时返回本地在线用户
func TestGetAllOnlineUserIDs_NoRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c1", "u-os1"))
	hub.shardedRegistry.AddClient(makeTestClient("c2", "u-os2"))

	ids, err := hub.GetAllOnlineUserIDs()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"u-os1", "u-os2"}, ids)
}

// TestGetOnlineUsersByNode_NoRepo_LocalNode 验证查询本节点且无 repo 时返回本地用户
func TestGetOnlineUsersByNode_NoRepo_LocalNode(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c1", "u-os1"))

	ids, err := hub.GetOnlineUsersByNode(hub.GetNodeID())
	require.NoError(t, err)
	assert.Contains(t, ids, "u-os1")
}

// TestGetOnlineUsersByNode_NoRepo_OtherNode 验证查询其他节点且无 repo 时返回错误
func TestGetOnlineUsersByNode_NoRepo_OtherNode(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	ids, err := hub.GetOnlineUsersByNode("other-node")
	require.Error(t, err)
	assert.Equal(t, ErrOnlineStatusRepositoryNotSet, err)
	assert.Nil(t, ids)
}

// TestGetOnlineUserCount_NoRepo 验证无 repository 时返回本地在线用户数
func TestGetOnlineUserCount_NoRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.shardedRegistry.AddClient(makeTestClient("c1", "u-os1"))
	hub.shardedRegistry.AddClient(makeTestClient("c2", "u-os2"))

	count, err := hub.GetOnlineUserCount()
	require.NoError(t, err)
	assert.Equal(t, int64(2), count)
}

// TestSyncOnlineStatusToRedis_NoRepo 验证无 repository 时返回错误
func TestSyncOnlineStatusToRedis_NoRepo(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	err := hub.SyncOnlineStatusToRedis()
	require.Error(t, err)
	assert.Equal(t, ErrOnlineStatusRepositoryNotSet, err)
}

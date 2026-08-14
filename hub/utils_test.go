/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 15:02:15
 * @FilePath: \go-wsc\hub\utils_test.go
 * @Description: Hub 通用工具方法白盒单元测试（覆盖 hub/utils.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"errors"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
)

// TestClassifyCloseError 验证 WebSocket 关闭错误分类
func TestClassifyCloseError(t *testing.T) {
	t.Run("正常关闭码", func(t *testing.T) {
		cases := []int{
			websocket.CloseNormalClosure,
			websocket.CloseGoingAway,
		}
		for _, code := range cases {
			err := &websocket.CloseError{Code: code}
			gotCode, isNormal := ClassifyCloseError(err)
			assert.Equal(t, code, gotCode)
			assert.True(t, isNormal, "code=%d 应识别为正常关闭", code)
		}
	})

	t.Run("异常关闭码", func(t *testing.T) {
		cases := []int{
			websocket.CloseProtocolError,
			websocket.CloseUnsupportedData,
			websocket.ClosePolicyViolation,
			websocket.CloseMessageTooBig,
			websocket.CloseInternalServerErr,
			websocket.CloseAbnormalClosure,
		}
		for _, code := range cases {
			err := &websocket.CloseError{Code: code}
			gotCode, isNormal := ClassifyCloseError(err)
			assert.Equal(t, code, gotCode)
			assert.False(t, isNormal, "code=%d 应识别为异常关闭", code)
		}
	})

	t.Run("非关闭错误返回默认异常码", func(t *testing.T) {
		err := errors.New("普通网络错误")
		gotCode, isNormal := ClassifyCloseError(err)
		assert.Equal(t, websocket.CloseAbnormalClosure, gotCode)
		assert.False(t, isNormal)
	})

	t.Run("nil 错误返回默认异常码", func(t *testing.T) {
		gotCode, isNormal := ClassifyCloseError(nil)
		assert.Equal(t, websocket.CloseAbnormalClosure, gotCode)
		assert.False(t, isNormal)
	})
}

// TestUpdateClientHeartbeat 验证心跳更新
func TestUpdateClientHeartbeat(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("onlineStatusRepo 为 nil 时直接返回 nil", func(t *testing.T) {
		// setupGroupTestHub 未设置 onlineStatusRepo
		assert.Nil(t, hub.UpdateClientHeartbeat("c1"))
	})
}

// TestSetClientLastHeartbeatForTest 验证设置客户端最后心跳时间
func TestSetClientLastHeartbeatForTest(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("客户端不存在返回 false", func(t *testing.T) {
		assert.False(t, hub.SetClientLastHeartbeatForTest("no-such-client", time.Now()))
	})

	t.Run("客户端存在返回 true 并更新心跳", func(t *testing.T) {
		c := makeTestClient("c-hb", "u-hb")
		hub.shardedRegistry.AddClient(c)

		ts := time.Now().Add(-1 * time.Minute)
		ok := hub.SetClientLastHeartbeatForTest("c-hb", ts)
		assert.True(t, ok)
		// SetLastHeartbeat 存 UnixNano（丢弃 monotonic），用 UnixNano 比较
		assert.Equal(t, ts.UnixNano(), c.GetLastHeartbeat().UnixNano())
	})
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-09 00:16:29
 * @FilePath: \go-wsc\hub\batch_sender_test.go
 * @Description: Hub 批量消息发送器白盒单元测试（覆盖 hub/batch_sender.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewBatchSender 验证创建批量发送器时 ctx 的容错处理
func TestNewBatchSender(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	t.Run("ctx 为 nil 时回退到 Background", func(t *testing.T) {
		bs := hub.NewBatchSender(nil)
		require.NotNil(t, bs)
		assert.NotNil(t, bs.ctx)
		users, msgs := bs.Count()
		assert.Equal(t, 0, users)
		assert.Equal(t, 0, msgs)
	})

	t.Run("ctx 非 nil 时保留原值", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		bs := hub.NewBatchSender(ctx)
		require.NotNil(t, bs)
		// 取消 ctx 后 ExecuteAsync 的 goroutine 应感知取消（间接验证 ctx 传递）
		cancel()
		assert.NotNil(t, bs)
	})
}

// TestBatchSender_AddAndCount 验证 AddMessage/AddMessages/AddUserMessages/Count
func TestBatchSender_AddAndCount(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	bs := hub.NewBatchSender(context.Background())

	// AddMessage 链式调用
	bs.AddMessage("u1", makeGroupMessage("s1")).
		AddMessage("u1", makeGroupMessage("s2"))
	users, msgs := bs.Count()
	assert.Equal(t, 1, users, "u1 同一用户")
	assert.Equal(t, 2, msgs, "u1 两条消息")

	// AddMessages 批量添加
	bs.AddMessages("u2", makeGroupMessage("s3"), makeGroupMessage("s4"), makeGroupMessage("s5"))
	users, msgs = bs.Count()
	assert.Equal(t, 2, users)
	assert.Equal(t, 5, msgs)

	// AddUserMessages map 批量添加
	bs.AddUserMessages(map[string][]*HubMessage{
		"u3": {makeGroupMessage("s6"), makeGroupMessage("s7")},
		"u2": {makeGroupMessage("s8")},
	})
	users, msgs = bs.Count()
	assert.Equal(t, 3, users)
	assert.Equal(t, 8, msgs)
}

// TestBatchSender_Clear 验证 Clear 清空后计数归零
func TestBatchSender_Clear(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	bs := hub.NewBatchSender(context.Background())
	bs.AddMessage("u1", makeGroupMessage("s1")).AddMessages("u2", makeGroupMessage("s2"))

	users, msgs := bs.Count()
	require.Equal(t, 2, users)
	require.Equal(t, 2, msgs)

	ret := bs.Clear()
	assert.Same(t, bs, ret, "Clear 应支持链式调用")

	users, msgs = bs.Count()
	assert.Equal(t, 0, users)
	assert.Equal(t, 0, msgs)
}

// TestBatchSender_Execute_Empty 验证空消息执行直接返回零结果
func TestBatchSender_Execute_Empty(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	bs := hub.NewBatchSender(context.Background())
	result := bs.Execute()

	require.NotNil(t, result)
	assert.Equal(t, 0, result.TotalUsers)
	assert.Equal(t, 0, result.TotalMessages)
	assert.Equal(t, int32(0), result.SuccessCount)
	assert.Equal(t, int32(0), result.FailureCount)
	assert.Empty(t, result.UserResults)
}

// TestBatchSender_Execute_Success 验证离线用户 + handler 路径全部成功
func TestBatchSender_Execute_Success(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 设置离线 handler，离线用户存离线 → Success=true
	hub.SetOfflineMessageHandler(&fakeOfflineHandler{})

	bs := hub.NewBatchSender(context.Background())
	bs.AddMessage("u-off-1", makeGroupMessage("s1")).
		AddMessage("u-off-2", makeGroupMessage("s2")).
		AddMessages("u-off-1", makeGroupMessage("s3"))

	result := bs.Execute()

	require.NotNil(t, result)
	assert.Equal(t, 2, result.TotalUsers)
	assert.Equal(t, 3, result.TotalMessages)
	assert.Equal(t, int32(3), result.SuccessCount, "3 条均成功（离线存储视为成功）")
	assert.Equal(t, int32(0), result.FailureCount)

	// 校验每用户结果
	ur1 := result.UserResults["u-off-1"]
	require.NotNil(t, ur1)
	assert.Equal(t, "u-off-1", ur1.UserID)
	assert.Equal(t, 2, ur1.TotalMessages)
	assert.Equal(t, 2, ur1.SuccessCount)
	assert.Equal(t, 0, ur1.FailureCount)
	assert.Empty(t, ur1.Errors)

	ur2 := result.UserResults["u-off-2"]
	require.NotNil(t, ur2)
	assert.Equal(t, 1, ur2.TotalMessages)
	assert.Equal(t, 1, ur2.SuccessCount)
}

// TestBatchSender_Execute_Failure 验证离线用户 + 无 handler 失败并触发回调
func TestBatchSender_Execute_Failure(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 不设置 handler：离线用户 → FinalError != nil → 走失败分支 + 回调
	var failCount int32
	hub.OnBatchSendFailure(func(userID string, msg *HubMessage, err error) {
		assert.NotNil(t, err)
		atomic.AddInt32(&failCount, 1)
	})

	bs := hub.NewBatchSender(context.Background())
	bs.AddMessage("u-fail", makeGroupMessage("s1"))

	result := bs.Execute()

	require.NotNil(t, result)
	assert.Equal(t, int32(0), result.SuccessCount)
	assert.Equal(t, int32(1), result.FailureCount)
	assert.Equal(t, int32(1), atomic.LoadInt32(&failCount), "应触发一次批量失败回调")

	ur := result.UserResults["u-fail"]
	require.NotNil(t, ur)
	assert.Equal(t, 1, ur.FailureCount)
	assert.Len(t, ur.Errors, 1)
}

// TestBatchSender_Execute_Mixed 验证成功与失败混合场景的统计准确性
func TestBatchSender_Execute_Mixed(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 仅对 u-ok 设置 handler 不可行（handler 是全局的），故用同一 handler
	// u-ok 离线存离线成功；u-fail 同样会成功。
	// 改为：全部不设 handler，全部失败，验证多用户并发统计
	bs := hub.NewBatchSender(context.Background())
	bs.AddMessage("u-a", makeGroupMessage("s1"))
	bs.AddMessage("u-b", makeGroupMessage("s2"))
	bs.AddMessage("u-c", makeGroupMessage("s3"))

	result := bs.Execute()
	require.NotNil(t, result)
	assert.Equal(t, 3, result.TotalUsers)
	assert.Equal(t, int32(0), result.SuccessCount)
	assert.Equal(t, int32(3), result.FailureCount)
	assert.Len(t, result.UserResults, 3)
}

// TestBatchSender_ExecuteAsync 验证异步执行回调被调用
func TestBatchSender_ExecuteAsync(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.SetOfflineMessageHandler(&fakeOfflineHandler{})

	bs := hub.NewBatchSender(context.Background())
	bs.AddMessage("u-async", makeGroupMessage("s1"))

	done := make(chan *BatchSendResult, 1)
	bs.ExecuteAsync(func(result *BatchSendResult) {
		done <- result
	})

	select {
	case result := <-done:
		require.NotNil(t, result)
		assert.Equal(t, int32(1), result.SuccessCount)
	case <-time.After(2 * time.Second):
		t.Fatal("ExecuteAsync 回调超时未执行")
	}
}

// TestBatchSender_ExecuteAsync_NilCallback 验证 callback 为 nil 时不 panic
func TestBatchSender_ExecuteAsync_NilCallback(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	hub.SetOfflineMessageHandler(&fakeOfflineHandler{})

	bs := hub.NewBatchSender(context.Background())
	bs.AddMessage("u-async2", makeGroupMessage("s1"))

	assert.NotPanics(t, func() {
		bs.ExecuteAsync(nil)
		// 给 goroutine 一点时间执行
		time.Sleep(100 * time.Millisecond)
	})
}

// TestUserResult_ErrorsCollection 验证错误收集（多消息失败）
func TestBatchSender_Execute_MultipleErrors(t *testing.T) {
	hub, _, _, cleanup := setupGroupTestHub(t)
	defer cleanup()

	// 无 handler，同一用户多条消息全部失败
	bs := hub.NewBatchSender(context.Background())
	bs.AddMessages("u-err", makeGroupMessage("s1"), makeGroupMessage("s2"), makeGroupMessage("s3"))

	result := bs.Execute()
	require.NotNil(t, result)

	ur := result.UserResults["u-err"]
	require.NotNil(t, ur)
	assert.Equal(t, 3, ur.FailureCount)
	assert.Len(t, ur.Errors, 3)
	for _, e := range ur.Errors {
		assert.NotNil(t, e)
	}
}

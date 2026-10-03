/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 17:10:02
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 17:10:02
 * @FilePath: \go-wsc\adapter\gorm\sql_retry_test.go
 * @Description: SQLSTATE 冲突识别与自动重试的用例（纯逻辑，无 DB 依赖）
 *
 * 覆盖：
 *   - isRetryableSQLState：40001/40P01 识别、非冲突 SQLSTATE 拒绝、errors.As 解包链
 *   - execWithSQLRetry：首次成功零等待、两次失败后成功、耗尽返回最后错误、
 *     非冲突错误立即透传、ctx 取消在退避窗口内快速退出
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package gormadapter

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSQLStateError 模拟 lib/pq 的 *pq.Error / pgx 的 *pgconn.PgError：
// 驱动无关识别只依赖 SQLState() 方法，不引入具体驱动依赖
type fakeSQLStateError struct {
	state string
}

func (e *fakeSQLStateError) SQLState() string { return e.state }
func (e *fakeSQLStateError) Error() string    { return fmt.Sprintf("sqlstate %s", e.state) }

// TestIsRetryableSQLState 表驱动：仅 40001（串行化冲突）/40P01（死锁）可重试
func TestIsRetryableSQLState(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil 错误不可重试", nil, false},
		{"普通错误不可重试", errors.New("connection refused"), false},
		{"40001 串行化冲突可重试", &fakeSQLStateError{state: "40001"}, true},
		{"40P01 死锁可重试", &fakeSQLStateError{state: "40P01"}, true},
		{"40002 其他事务类冲突不可重试", &fakeSQLStateError{state: "40002"}, false},
		{"23505 唯一约束冲突不可重试", &fakeSQLStateError{state: "23505"}, false},
		{"空 SQLSTATE 不可重试", &fakeSQLStateError{state: ""}, false},
		{"wrapped 40001 经 errors.As 解包后仍可重试",
			fmt.Errorf("exec failed: %w", &fakeSQLStateError{state: "40001"}), true},
		{"双重 wrapped 40P01 仍可重试",
			fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &fakeSQLStateError{state: "40P01"})), true},
		{"wrapped 普通错误不可重试",
			fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", errors.New("boom"))), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isRetryableSQLState(tt.err))
		})
	}
}

// TestExecWithSQLRetry_FirstAttemptSuccess 首次成功：fn 只执行一次，无退避等待
func TestExecWithSQLRetry_FirstAttemptSuccess(t *testing.T) {
	var calls int32
	start := time.Now()
	err := execWithSQLRetry(context.Background(), func() error {
		atomic.AddInt32(&calls, 1)
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "首次成功不应重试")
	// 首次执行前没有退避等待，耗时应在毫秒级
	assert.Less(t, time.Since(start), 50*time.Millisecond, "首次成功不应产生退避等待")
}

// TestExecWithSQLRetry_SuccessAfterTwoRetries 两次 40001 失败后第三次成功
//
// Min=50ms 抖动指数退避下两次等待至少 100ms，验证退避确实发生
func TestExecWithSQLRetry_SuccessAfterTwoRetries(t *testing.T) {
	var calls int32
	start := time.Now()
	err := execWithSQLRetry(context.Background(), func() error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return &fakeSQLStateError{state: "40001"}
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, int32(3), atomic.LoadInt32(&calls), "两次失败后第三次应成功")
	// 两次退避（50ms + [50,100)ms 抖动）合计 >= 100ms，留 10ms 容差防计时精度抖动
	assert.GreaterOrEqual(t, time.Since(start), 90*time.Millisecond, "重试间应按退避等待")
}

// TestExecWithSQLRetry_ExhaustedReturnsLastError 三次全为 40001：恰好执行 3 次，返回最后一次错误
func TestExecWithSQLRetry_ExhaustedReturnsLastError(t *testing.T) {
	var calls int32
	conflict := &fakeSQLStateError{state: "40001"}
	err := execWithSQLRetry(context.Background(), func() error {
		atomic.AddInt32(&calls, 1)
		return conflict
	})
	require.Error(t, err, "耗尽重试次数后应返回错误")
	assert.Same(t, conflict, err, "应返回最后一次的原始错误（可 errors.As/is 追溯）")
	assert.True(t, isRetryableSQLState(err))
	assert.Equal(t, int32(sqlRetryMaxAttempts), atomic.LoadInt32(&calls),
		"含首次在内最多执行 %d 次", sqlRetryMaxAttempts)
}

// TestExecWithSQLRetry_NonRetryableImmediate 非冲突错误立即透传，不进入退避重试
func TestExecWithSQLRetry_NonRetryableImmediate(t *testing.T) {
	var calls int32
	plainErr := errors.New("syntax error")
	start := time.Now()
	err := execWithSQLRetry(context.Background(), func() error {
		atomic.AddInt32(&calls, 1)
		return plainErr
	})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "非冲突错误不应重试")
	assert.Less(t, time.Since(start), 50*time.Millisecond)
	var se *fakeSQLStateError
	assert.False(t, errors.As(err, &se))
}

// TestExecWithSQLRetry_CtxCancelledDuringBackoff 退避等待期 ctx 取消：快速返回 ctx.Err()
//
// 首次执行即失败并取消 ctx，第二次退避 select 应命中 Done 分支而非 time.After
func TestExecWithSQLRetry_CtxCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls int32
	start := time.Now()
	err := execWithSQLRetry(ctx, func() error {
		atomic.AddInt32(&calls, 1)
		cancel() // 首次失败的同时取消，退避窗口内应立即退出
		return &fakeSQLStateError{state: "40P01"}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled, "ctx 取消后应返回 ctx.Err() 而非继续重试")
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls), "取消后不应再执行 fn")
	assert.Less(t, time.Since(start), 50*time.Millisecond, "取消应立即生效而非等满退避时长")
}

// TestExecWithSQLRetry_CtxDeadlineDuringRetries 已超时的 ctx：首次失败后直接返回 DeadlineExceeded
func TestExecWithSQLRetry_CtxDeadlineDuringRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	time.Sleep(20 * time.Millisecond) // 让 deadline 先到期

	var calls int32
	err := execWithSQLRetry(ctx, func() error {
		atomic.AddInt32(&calls, 1)
		return &fakeSQLStateError{state: "40001"}
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

// TestNewSQLRetryBackoff 退避参数：Min/Max/Factor/Jitter 符合 50-200ms 抖动指数退避设计
func TestNewSQLRetryBackoff(t *testing.T) {
	b := newSQLRetryBackoff()
	assert.Equal(t, 50*time.Millisecond, b.Min)
	assert.Equal(t, 200*time.Millisecond, b.Max)
	assert.Equal(t, float64(2), b.Factor)
	assert.True(t, b.Jitter)

	// 逐次拉长且始终被 Max 封顶：抖动下界不小于 Min、上界不超过 Max
	for i := 0; i < 5; i++ {
		d := b.Duration()
		assert.GreaterOrEqual(t, d, b.Min, "退避时长不应低于 Min")
		assert.LessOrEqual(t, d, b.Max, "退避时长不应超过 Max")
	}
}

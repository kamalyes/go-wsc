/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 00:00:00
 * @FilePath: \go-wsc\repository\sql_retry_test.go
 * @Description: SQLSTATE 冲突识别与自动重试的单元测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeSQLStateError 模拟驱动层 SQLSTATE 错误
// pgx 的 *pgconn.PgError 与 lib/pq 的 *pq.Error 均实现 SQLState() string
type fakeSQLStateError struct{ code string }

func (e *fakeSQLStateError) Error() string   { return "SQLSTATE " + e.code }
func (e *fakeSQLStateError) SQLState() string { return e.code }

// TestIsRetryableSQLState 验证冲突识别：40001/40P01 可重试，其他状态码与普通错误不可重试
func TestIsRetryableSQLState(t *testing.T) {
	assert.True(t, isRetryableSQLState(&fakeSQLStateError{code: "40001"}), "CRDB Serializable 冲突应可重试")
	assert.True(t, isRetryableSQLState(&fakeSQLStateError{code: "40P01"}), "PG 死锁应可重试")
	assert.False(t, isRetryableSQLState(&fakeSQLStateError{code: "23505"}), "唯一键冲突不应重试")
	assert.False(t, isRetryableSQLState(errors.New("plain error")), "普通错误不应重试")
	assert.False(t, isRetryableSQLState(nil), "nil 不应重试")
	// GORM/上层常见 fmt.Errorf("%w") 包装后仍需识别
	assert.True(t, isRetryableSQLState(fmt.Errorf("更新失败: %w", &fakeSQLStateError{code: "40001"})), "包装后的 40001 应可重试")
}

// TestExecWithSQLRetryRetriesOnConflict 验证冲突后自动重试成功
// 模拟 CRDB 生产场景：前两次 UPDATE 与并发事务时序交叉被 abort（40001），第三次重试成功
func TestExecWithSQLRetryRetriesOnConflict(t *testing.T) {
	calls := 0
	err := execWithSQLRetry(context.Background(), func() error {
		calls++
		if calls < 3 {
			return &fakeSQLStateError{code: "40001"}
		}
		return nil
	})

	assert.NoError(t, err)
	assert.Equal(t, 3, calls, "应在前两次 40001 后第三次成功")
}

// TestExecWithSQLRetryFailsFastOnNonRetryable 验证非冲突错误立即返回不重试
func TestExecWithSQLRetryFailsFastOnNonRetryable(t *testing.T) {
	calls := 0
	nonRetryable := errors.New("connection refused")
	err := execWithSQLRetry(context.Background(), func() error {
		calls++
		return nonRetryable
	})

	assert.ErrorIs(t, err, nonRetryable)
	assert.Equal(t, 1, calls, "非冲突错误不应重试")
}

// TestExecWithSQLRetryExhausted 验证持续冲突时耗尽重试次数后返回最后一次错误
func TestExecWithSQLRetryExhausted(t *testing.T) {
	calls := 0
	finalErr := &fakeSQLStateError{code: "40001"}
	err := execWithSQLRetry(context.Background(), func() error {
		calls++
		return finalErr
	})

	assert.ErrorIs(t, err, finalErr)
	assert.Equal(t, sqlRetryMaxAttempts, calls, "重试次数应等于上限")
}

// TestExecWithSQLRetryContextCancelled 验证退避等待期间 ctx 取消时及时退出
func TestExecWithSQLRetryContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	// 首次执行冲突后取消 ctx，退避等待应被中断
	err := execWithSQLRetry(ctx, func() error {
		calls++
		cancel()
		return &fakeSQLStateError{code: "40001"}
	})

	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, calls, "ctx 取消后不应继续重试")
}

// TestExecWithSQLRetryBackoffDuration 验证退避时长有界（防止重试风暴拖垮调用方）
func TestExecWithSQLRetryBackoffDuration(t *testing.T) {
	start := time.Now()
	_ = execWithSQLRetry(context.Background(), func() error {
		return &fakeSQLStateError{code: "40001"}
	})
	// 3 次尝试 + 2 次退避（每次 ≤ Max 200ms + 抖动），上界 1s 足够宽松
	assert.Less(t, time.Since(start), time.Second, "重试总耗时应保持有界")
}

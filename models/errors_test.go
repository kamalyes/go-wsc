/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 20:10:18
 * @FilePath: \go-wsc\models\errors_test.go
 * @Description:
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import (
	"errors"
	"fmt"
	"testing"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/stretchr/testify/assert"
)

// TestSentinelsInitializedCorrectly 回归 models/errors.go 的两个严重 bug：
//
//  1. 包级错误变量初始化顺序 bug：errorx.NewError 依赖 init() 中 RegisterError 填充的
//     errorMessages 映射，但 Go 包级变量初始化先于 init() 执行，导致所有 sentinel 都回退
//     为 BaseError{Msg:"unknown error", Type:0}，彼此相等
//  2. Is*Error 类型断言 bug：断言用 interface{ Type() ErrorType }，而 BaseError 只暴露
//     GetType()，断言永远失败，Is*Error 对运行时创建的错误全部误判
//
// 修复后：sentinel 在 init() 末尾赋值（Type/消息正确），Is*Error 改用 errorx.ClassifyError
func TestSentinelsInitializedCorrectly(t *testing.T) {
	// 1. sentinel 必须携带正确的 ErrorType（非 0）与有意义的消息（非 "unknown error"）
	assert.NotEqual(t, ErrorType(0), ErrUserOffline.Type, "ErrUserOffline.Type 不应为 0")
	assert.NotEqual(t, ErrorType(0), ErrQueueFull.Type, "ErrQueueFull.Type 不应为 0")
	assert.NotEqual(t, ErrorType(0), ErrClientNotFound.Type, "ErrClientNotFound.Type 不应为 0")
	assert.NotEqual(t, ErrorType(0), ErrMessageBufferFull.Type, "ErrMessageBufferFull.Type 不应为 0")

	assert.NotEqual(t, "unknown error", ErrUserOffline.Error(), "ErrUserOffline 消息不应为 unknown error")
	assert.Equal(t, ErrTypeUserOffline, ErrUserOffline.Type)
	assert.Equal(t, ErrTypeQueueFull, ErrQueueFull.Type)
	assert.Equal(t, ErrTypeClientNotFound, ErrClientNotFound.Type)
	assert.Equal(t, ErrTypeMessageBufferFull, ErrMessageBufferFull.Type)

	// 2. 不同 sentinel 之间不应相等（修复前全部 Type=0、消息相同，彼此 ==）
	assert.NotEqual(t, ErrUserOffline, ErrQueueFull)
	assert.NotEqual(t, ErrUserOffline, ErrClientNotFound)
	assert.NotEqual(t, ErrQueueFull, ErrMessageBufferFull)
}

// TestIsRetryableErrorSentinelAndRuntime 验证 IsRetryableError 对 sentinel 与运行时错误都正确判定
// 修复前：IsRetryableError(ErrClientNotFound) 因 sentinel 互相相等而命中 ErrQueueAndPendingFull，
// 错误返回 true
func TestIsRetryableErrorSentinelAndRuntime(t *testing.T) {
	// 可重试 sentinel
	assert.True(t, IsRetryableError(ErrQueueAndPendingFull))
	assert.True(t, IsRetryableError(ErrMessageBufferFull))
	assert.True(t, IsRetryableError(ErrAckTimeout))
	assert.True(t, IsRetryableError(ErrQueueFull))
	assert.True(t, IsRetryableError(ErrMessageDeliveryTimeout))

	// 不可重试 sentinel（修复前 ErrClientNotFound 会被误判为 true）
	assert.False(t, IsRetryableError(ErrClientNotFound), "ErrClientNotFound 不应可重试")
	assert.False(t, IsRetryableError(ErrUserOffline), "ErrUserOffline 不应可重试")
	assert.False(t, IsRetryableError(ErrUserNotFound), "ErrUserNotFound 不应可重试")

	// 运行时创建的错误（带格式化参数）
	assert.True(t, IsRetryableError(errorx.NewError(ErrTypeQueueFull)))
	assert.False(t, IsRetryableError(errorx.NewError(ErrTypeClientNotFound, "c1")))

	// nil 与普通 error
	assert.False(t, IsRetryableError(nil))
	assert.False(t, IsRetryableError(errors.New("plain")))
}

// TestIsUserOfflineErrorWorks 验证 IsUserOfflineError 对 sentinel 与运行时错误均生效
// 修复前：Type() 断言失败，== 兜底只对 sentinel 自身成立，对运行时错误返回 false
func TestIsUserOfflineErrorWorks(t *testing.T) {
	assert.True(t, IsUserOfflineError(ErrUserOffline), "sentinel 应被识别为离线错误")
	assert.True(t, IsUserOfflineError(errorx.NewError(ErrTypeUserOffline, "uid-123")), "运行时离线错误应被识别")
	assert.False(t, IsUserOfflineError(ErrQueueFull))
	assert.False(t, IsUserOfflineError(errorx.NewError(ErrTypeQueueFull)))
	assert.False(t, IsUserOfflineError(nil))
}

// TestIsQueueFullErrorWorks 验证 IsQueueFullError 对各类队列满错误生效
func TestIsQueueFullErrorWorks(t *testing.T) {
	assert.True(t, IsQueueFullError(ErrQueueFull))
	assert.True(t, IsQueueFullError(ErrMessageBufferFull))
	assert.True(t, IsQueueFullError(ErrQueueAndPendingFull))
	assert.True(t, IsQueueFullError(errorx.NewError(ErrTypePendingQueueFull)))
	assert.False(t, IsQueueFullError(ErrUserOffline))
	assert.False(t, IsQueueFullError(nil))
}

// TestIsAckTimeoutErrorWorks 验证 IsAckTimeoutError
func TestIsAckTimeoutErrorWorks(t *testing.T) {
	assert.True(t, IsAckTimeoutError(ErrAckTimeout))
	assert.True(t, IsAckTimeoutError(ErrAckTimeoutRetries))
	assert.True(t, IsAckTimeoutError(errorx.NewError(ErrTypeAckTimeout, "msg-1")))
	assert.False(t, IsAckTimeoutError(ErrQueueFull))
	assert.False(t, IsAckTimeoutError(nil))
}

// TestIsSendTimeoutErrorWorks 验证 IsSendTimeoutError
func TestIsSendTimeoutErrorWorks(t *testing.T) {
	assert.True(t, IsSendTimeoutError(ErrMessageDeliveryTimeout))
	assert.True(t, IsSendTimeoutError(errorx.NewError(ErrTypeOperationTimeout)))
	assert.False(t, IsSendTimeoutError(ErrQueueFull))
	assert.False(t, IsSendTimeoutError(nil))
}

// TestClassifyErrorHandlesWrappedErrors 验证 ClassifyError 可穿透 fmt.Errorf %w 包装
func TestClassifyErrorHandlesWrappedErrors(t *testing.T) {
	inner := errorx.NewError(ErrTypeUserOffline, "uid-9")
	wrapped := fmt.Errorf("send failed: %w", inner)
	assert.True(t, IsUserOfflineError(wrapped), "包装后的离线错误应被识别")

	// 直接返回 sentinel 也能被识别（核心路径 client/wsc.go、hub/observer.go 直接 return sentinel）
	assert.True(t, IsQueueFullError(ErrMessageBufferFull))
	assert.True(t, IsUserOfflineError(ErrUserOffline))
}

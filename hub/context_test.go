/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 10:20:26
 * @FilePath: \go-wsc\hub\context_test.go
 * @Description: hub 层上下文扩展测试
 *   - 验证 ContextKeyUserID 和 ContextKeySenderID 常量的字符串值
 *   - 验证作为 context.WithValue 的 key 使用时能正确存取 value
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextKey_ConstantValues 验证 ContextKey 常量的字符串值正确
func TestContextKey_ConstantValues(t *testing.T) {
	assert.Equal(t, ContextKey("user_id"), ContextKeyUserID,
		"ContextKeyUserID 常量值应为 'user_id'")
	assert.Equal(t, ContextKey("sender_id"), ContextKeySenderID,
		"ContextKeySenderID 常量值应为 'sender_id'")

	assert.Equal(t, "user_id", string(ContextKeyUserID),
		"ContextKeyUserID 转字符串应为 'user_id'")
	assert.Equal(t, "sender_id", string(ContextKeySenderID),
		"ContextKeySenderID 转字符串应为 'sender_id'")
}

// TestContextKey_WithValueUserID 验证 ContextKeyUserID 作为 context key 能正确存取 value
func TestContextKey_WithValueUserID(t *testing.T) {
	expectedUserID := "user-12345"

	ctx := context.WithValue(context.Background(), ContextKeyUserID, expectedUserID)

	got, ok := ctx.Value(ContextKeyUserID).(string)
	require.True(t, ok, "从 context 取出的 value 应为 string 类型")
	assert.Equal(t, expectedUserID, got, "存取的 user_id 值应一致")
}

// TestContextKey_WithValueSenderID 验证 ContextKeySenderID 作为 context key 能正确存取 value
func TestContextKey_WithValueSenderID(t *testing.T) {
	expectedSenderID := "sender-67890"

	ctx := context.WithValue(context.Background(), ContextKeySenderID, expectedSenderID)

	got, ok := ctx.Value(ContextKeySenderID).(string)
	require.True(t, ok, "从 context 取出的 value 应为 string 类型")
	assert.Equal(t, expectedSenderID, got, "存取的 sender_id 值应一致")
}

// TestContextKey_BothKeysIsolation 验证两个 key 相互独立，互不干扰
func TestContextKey_BothKeysIsolation(t *testing.T) {
	userID := "user-abc"
	senderID := "sender-xyz"

	ctx := context.Background()
	ctx = context.WithValue(ctx, ContextKeyUserID, userID)
	ctx = context.WithValue(ctx, ContextKeySenderID, senderID)

	gotUserID, ok1 := ctx.Value(ContextKeyUserID).(string)
	gotSenderID, ok2 := ctx.Value(ContextKeySenderID).(string)

	require.True(t, ok1, "ContextKeyUserID 对应 value 应为 string")
	require.True(t, ok2, "ContextKeySenderID 对应 value 应为 string")
	assert.Equal(t, userID, gotUserID, "ContextKeyUserID 取值应正确")
	assert.Equal(t, senderID, gotSenderID, "ContextKeySenderID 取值应正确")
}

// TestContextKey_NonExistentKey 验证未设置的 key 返回 nil
func TestContextKey_NonExistentKey(t *testing.T) {
	ctx := context.Background()

	assert.Nil(t, ctx.Value(ContextKeyUserID), "未设置 ContextKeyUserID 时应返回 nil")
	assert.Nil(t, ctx.Value(ContextKeySenderID), "未设置 ContextKeySenderID 时应返回 nil")
}

// TestContextKey_OverwriteValue 验证后续 WithValue 可以覆盖同 key 的值
func TestContextKey_OverwriteValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), ContextKeyUserID, "old-user")
	ctx = context.WithValue(ctx, ContextKeyUserID, "new-user")

	got, ok := ctx.Value(ContextKeyUserID).(string)
	require.True(t, ok)
	assert.Equal(t, "new-user", got, "后写入的值应覆盖先写入的值")
}

// TestContextKey_DerivedContext 验证通过 context 派生（WithCancel/WithTimeout）不丢失 value
func TestContextKey_DerivedContext(t *testing.T) {
	base := context.WithValue(context.Background(), ContextKeyUserID, "user-derived")

	ctxCancel, cancel1 := context.WithCancel(base)
	defer cancel1()

	ctxTimeout, cancel2 := context.WithTimeout(base, 5*time.Second)
	defer cancel2()

	gotCancel, _ := ctxCancel.Value(ContextKeyUserID).(string)
	assert.Equal(t, "user-derived", gotCancel, "WithCancel 派生的 context 应保留 value")

	gotTimeout, _ := ctxTimeout.Value(ContextKeyUserID).(string)
	assert.Equal(t, "user-derived", gotTimeout, "WithTimeout 派生的 context 应保留 value")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditTime: 2026-07-21 00:00:00
 * @FilePath: \go-wsc\hub\connection_token_test.go
 * @Description: 连接 Token 编解码测试 - 覆盖 Namespace/GroupID 字段
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"net/http/httptest"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTokenCfg 构造测试用的 ConnectionToken 配置（不启用 Redis）
func newTestTokenCfg() *wscconfig.ConnectionToken {
	return &wscconfig.ConnectionToken{
		Enabled:        true,
		SigningKey:     "test-secret-key",
		Algorithm:      "HS256",
		ExpiresTime:    time.Hour,
		TokenSource:    "query",
		TokenParamName: "token",
	}
}

// TestConnectionTokenWithNamespace 测试 Token 携带 Namespace 的编解码
func TestConnectionTokenWithNamespace(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID:    "user-001",
		UserType:  "agent",
		DeviceID:  "device-001",
		Namespace: "tenantA",
	}

	token, err := IssueConnectionToken(cfg, nil, claims)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// 解码验证
	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, claims.UserID, decoded.UserID)
	assert.Equal(t, claims.UserType, decoded.UserType)
	assert.Equal(t, claims.DeviceID, decoded.DeviceID)
	assert.Equal(t, claims.Namespace, decoded.Namespace, "Namespace 应一致")
}

// TestConnectionTokenWithoutNamespace 测试 Token 不带 Namespace 时解码为空
func TestConnectionTokenWithoutNamespace(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID: "user-002",
	}

	token, err := IssueConnectionToken(cfg, nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "user-002", decoded.UserID)
	assert.Empty(t, decoded.Namespace, "未设置 Namespace 时应为空")
}

// TestConnectionTokenFromHeader 测试从 Header 提取 Token 并还原 Namespace
func TestConnectionTokenFromHeader(t *testing.T) {
	cfg := newTestTokenCfg()
	cfg.TokenSource = "header"
	cfg.TokenParamName = "X-Connection-Token"

	claims := &ConnectionClaims{
		UserID:    "user-003",
		Namespace: "tenantB",
	}

	token, err := IssueConnectionToken(cfg, nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("X-Connection-Token", token)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "tenantB", decoded.Namespace)
}

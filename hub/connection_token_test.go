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

// TestConnectionTokenWithGroupID 测试 Token 携带 GroupID 的编解码
func TestConnectionTokenWithGroupID(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID:    "user-gid-001",
		UserType:  "customer",
		DeviceID:  "device-gid-001",
		Namespace: "tenantA",
		GroupID:   "chat-room-42",
	}

	token, err := IssueConnectionToken(cfg, nil, claims)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, claims.UserID, decoded.UserID)
	assert.Equal(t, claims.UserType, decoded.UserType)
	assert.Equal(t, claims.DeviceID, decoded.DeviceID)
	assert.Equal(t, claims.Namespace, decoded.Namespace, "Namespace 应一致")
	assert.Equal(t, claims.GroupID, decoded.GroupID, "GroupID 应一致")
}

// TestConnectionTokenWithoutGroupID 测试 Token 不带 GroupID 时解码为空
func TestConnectionTokenWithoutGroupID(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID:    "user-gid-002",
		Namespace: "tenantB",
	}

	token, err := IssueConnectionToken(cfg, nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "user-gid-002", decoded.UserID)
	assert.Empty(t, decoded.GroupID, "未设置 GroupID 时应为空")
}

// TestConnectionTokenGroupIDAndNamespaceCombo 测试 Token 同时携带 Namespace 和 GroupID 的组合场景
func TestConnectionTokenGroupIDAndNamespaceCombo(t *testing.T) {
	cfg := newTestTokenCfg()

	tests := []struct {
		name      string
		namespace string
		groupID   string
	}{
		{"有命名空间有群组", "tenantA", "room-1"},
		{"有命名空间无群组", "tenantB", ""},
		{"无命名空间有群组", "", "room-2"},
		{"无命名空间无群组", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := &ConnectionClaims{
				UserID:    "user-combo",
				Namespace: tt.namespace,
				GroupID:   tt.groupID,
			}

			token, err := IssueConnectionToken(cfg, nil, claims)
			require.NoError(t, err)

			decoder := NewConnectionTokenDecoder(cfg, nil, nil)
			req := httptest.NewRequest("GET", "/ws?token="+token, nil)
			decoded, err := decoder.Decode(req)
			require.NoError(t, err)
			assert.Equal(t, tt.namespace, decoded.Namespace, "Namespace 应一致")
			assert.Equal(t, tt.groupID, decoded.GroupID, "GroupID 应一致")
		})
	}
}

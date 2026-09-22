/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-03-03 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 10:16:00
 * @FilePath: \go-wsc\transport\validate_test.go
 * @Description: 连接参数预检测试 - 验证通过 / 必填缺失 / 禁用验证 / header 来源提取
 *
 * 迁移自 hub/http_validate_test.go（P2 批1 测试随源归位，Hub 方法 → ValidateHandler 组件）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// newValidateConfig 构造预检测试配置（默认全启用必填校验，用例内按需调整）
func newValidateConfig() *wscconfig.WSC {
	cfg := wscconfig.Default()
	cfg.ConnectionValidation = wscconfig.DefaultConnectionValidation()
	return cfg
}

// TestValidateHandlerDisabled 验证禁用连接验证时恒返回 200
func TestValidateHandlerDisabled(t *testing.T) {
	cfg := newValidateConfig()
	cfg.ConnectionValidation.Enabled = false
	handler := NewValidateHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/ws/validate", nil)
	rec := httptest.NewRecorder()
	handler.HandleValidateConnection(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["valid"])
	assert.Equal(t, "参数验证通过", resp["message"])
}

// TestValidateHandlerMissingUserID 启用验证且缺少 user_id 时返回 400 + 拒绝原因
func TestValidateHandlerMissingUserID(t *testing.T) {
	cfg := newValidateConfig()
	cfg.ConnectionValidation.RequireUserType = false // 仅要求 UserID，隔离单一缺失维度
	handler := NewValidateHandler(cfg)

	// 请求不带 user_id
	req := httptest.NewRequest(http.MethodGet, "/ws/validate", nil)
	rec := httptest.NewRecorder()
	handler.HandleValidateConnection(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["valid"])
	assert.Equal(t, "connection_rejected", resp["error"])
	assert.NotNil(t, resp["reason"])
	assert.NotNil(t, resp["time"])
}

// TestValidateHandlerMissingBoth 同时缺少 user_id 和 user_type 返回 400
func TestValidateHandlerMissingBoth(t *testing.T) {
	cfg := newValidateConfig()
	handler := NewValidateHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/ws/validate", nil)
	rec := httptest.NewRecorder()
	handler.HandleValidateConnection(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["valid"])
	assert.NotNil(t, resp["reason"])
}

// TestValidateHandlerPass 参数齐全时返回 200
func TestValidateHandlerPass(t *testing.T) {
	cfg := newValidateConfig()
	handler := NewValidateHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/ws/validate?user_id=123&user_type=customer", nil)
	rec := httptest.NewRecorder()
	handler.HandleValidateConnection(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["valid"])
}

// TestValidateHandlerHeaderSources header 来源提取（X-User-ID / X-User-Type）
func TestValidateHandlerHeaderSources(t *testing.T) {
	cfg := newValidateConfig()
	handler := NewValidateHandler(cfg)

	req := httptest.NewRequest(http.MethodGet, "/ws/validate", nil)
	req.Header.Set("X-User-ID", "u-hdr")
	req.Header.Set("X-User-Type", "agent")
	rec := httptest.NewRecorder()
	handler.HandleValidateConnection(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

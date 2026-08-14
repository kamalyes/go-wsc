/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-15 10:07:20
 * @FilePath: \go-wsc\hub\http_validate_test.go
 * @Description: WebSocket 连接参数验证白盒单元测试（覆盖 hub/http_validate.go）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
)

// newValidateHub 构造用于验证连接测试的 Hub，并返回其 config 以便调整 ConnectionValidation
func newValidateHub(t *testing.T) *Hub {
	t.Helper()
	hub := NewHub(wscconfig.Default())
	return hub
}

// TestHandleValidateConnection_Disabled 验证禁用连接验证时返回 200
func TestHandleValidateConnection_Disabled(t *testing.T) {
	hub := newValidateHub(t)
	defer hub.SafeShutdown()
	// 显式禁用连接验证
	hub.config.ConnectionValidation.Enabled = false

	req := httptest.NewRequest("GET", "/ws/validate", nil)
	rec := httptest.NewRecorder()
	hub.HandleValidateConnection(rec, req)

	assert.Equal(t, 200, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["valid"])
	assert.Equal(t, "参数验证通过", resp["message"])
}

// TestHandleValidateConnection_MissingUserID 验证启用验证且缺少 user_id 时返回 400
func TestHandleValidateConnection_MissingUserID(t *testing.T) {
	hub := newValidateHub(t)
	defer hub.SafeShutdown()
	hub.config.ConnectionValidation.Enabled = true
	hub.config.ConnectionValidation.RequireUserID = true
	hub.config.ConnectionValidation.RequireUserType = false

	// 请求不带 user_id
	req := httptest.NewRequest("GET", "/ws/validate", nil)
	rec := httptest.NewRecorder()
	hub.HandleValidateConnection(rec, req)

	assert.Equal(t, 400, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["valid"])
	assert.NotNil(t, resp["reason"])
	assert.NotNil(t, resp["time"])
}

// TestHandleValidateConnection_MissingBoth 验证同时缺少 user_id 和 user_type 返回 400
func TestHandleValidateConnection_MissingBoth(t *testing.T) {
	hub := newValidateHub(t)
	defer hub.SafeShutdown()
	hub.config.ConnectionValidation.Enabled = true
	hub.config.ConnectionValidation.RequireUserID = true
	hub.config.ConnectionValidation.RequireUserType = true

	req := httptest.NewRequest("GET", "/ws/validate", nil)
	rec := httptest.NewRecorder()
	hub.HandleValidateConnection(rec, req)

	assert.Equal(t, 400, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["valid"])
}

// TestHandleValidateConnection_Valid 验证参数齐全时返回 200
func TestHandleValidateConnection_Valid(t *testing.T) {
	hub := newValidateHub(t)
	defer hub.SafeShutdown()
	hub.config.ConnectionValidation.Enabled = true
	hub.config.ConnectionValidation.RequireUserID = true
	hub.config.ConnectionValidation.RequireUserType = true

	// 请求带 user_id 和 user_type
	req := httptest.NewRequest("GET", "/ws/validate?user_id=u1&user_type=customer", nil)
	rec := httptest.NewRecorder()
	hub.HandleValidateConnection(rec, req)

	assert.Equal(t, 200, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, true, resp["valid"])
}

// TestHandleValidateConnection_MissingUserType 验证仅缺少 user_type 时返回 400
func TestHandleValidateConnection_MissingUserType(t *testing.T) {
	hub := newValidateHub(t)
	defer hub.SafeShutdown()
	hub.config.ConnectionValidation.Enabled = true
	hub.config.ConnectionValidation.RequireUserID = false
	hub.config.ConnectionValidation.RequireUserType = true

	// 请求带 user_id 但不带 user_type
	req := httptest.NewRequest("GET", "/ws/validate?user_id=u1", nil)
	rec := httptest.NewRecorder()
	hub.HandleValidateConnection(rec, req)

	assert.Equal(t, 400, rec.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, false, resp["valid"])
}

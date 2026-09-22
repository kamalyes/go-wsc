/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-03-03 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-18 13:50:22
 * @FilePath: \go-wsc\transport\validate.go
 * @Description: WebSocket 连接参数验证接口
 *
 * 迁移自 hub/http_validate.go（P2 批1 域化）：原 *Hub 方法重组为 ValidateHandler
 * 组件，config 经构造注入
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"net/http"
	"time"

	gccommon "github.com/kamalyes/go-config/pkg/common"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-wsc/models"
)

// ValidateHandler 连接参数预检器
// 客户端在连接 WebSocket 前可先调用验证接口，提前暴露参数错误（避免升级握手后才失败）
type ValidateHandler struct {
	cfg *wscconfig.WSC
}

// NewValidateHandler 创建连接参数预检器
func NewValidateHandler(cfg *wscconfig.WSC) *ValidateHandler {
	return &ValidateHandler{cfg: cfg}
}

// HandleValidateConnection 验证 WebSocket 连接参数
//
// 使用示例：
//
//	GET /ws/validate?user_id=123&user_type=customer
//
// 成功响应 (200):
//
//	{"valid": true, "message": "参数验证通过"}
//
// 失败响应 (400):
//
//	{"valid": false, "error": "connection_rejected", "reason": "缺少必需参数: user_id", "time": 1234567890}
func (v *ValidateHandler) HandleValidateConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// 提取参数
	userID := gccommon.ExtractAttribute(r, v.cfg.ClientAttributes.UserIDSources)
	userType := gccommon.ExtractAttribute(r, v.cfg.ClientAttributes.UserTypeSources)

	// 验证参数
	if v.cfg.ConnectionValidation.Enabled {
		valid, reason := v.cfg.ConnectionValidation.ValidateConnection(userID, userType)
		if !valid {
			// 验证失败
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"valid":  false,
				"error":  models.MessageTypeConnectionRejected.String(),
				"reason": reason,
				"time":   time.Now().Unix(),
			})
			return
		}
	}

	// 验证成功
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"valid":   true,
		"message": "参数验证通过",
	})
}

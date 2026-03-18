/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-03-03 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-03-03 00:00:00
 * @FilePath: \go-wsc\hub\http_validate.go
 * @Description: WebSocket 连接参数验证接口
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"encoding/json"
	"net/http"
	"time"

	gccommon "github.com/kamalyes/go-config/pkg/common"
)

// HandleValidateConnection 验证 WebSocket 连接参数
// 客户端在连接 WebSocket 前可以先调用此接口验证参数是否正确
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
func (h *Hub) HandleValidateConnection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// 提取参数
	userID := gccommon.ExtractAttribute(r, h.config.ClientAttributes.UserIDSources)
	userType := gccommon.ExtractAttribute(r, h.config.ClientAttributes.UserTypeSources)

	// 验证参数
	if h.config.ConnectionValidation.Enabled {
		valid, reason := h.config.ConnectionValidation.ValidateConnection(userID, userType)
		if !valid {
			// 验证失败
			w.WriteHeader(http.StatusBadRequest)
			response := map[string]any{
				"valid":  false,
				"error":  MessageTypeConnectionRejected.String(),
				"reason": reason,
				"time":   time.Now().Unix(),
			}
			json.NewEncoder(w).Encode(response)
			return
		}
	}

	// 验证成功
	w.WriteHeader(http.StatusOK)
	response := map[string]any{
		"valid":   true,
		"message": "参数验证通过",
	}
	json.NewEncoder(w).Encode(response)
}

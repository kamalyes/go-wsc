/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-21 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 17:15:25
 * @FilePath: \go-wsc\models\templates.go
 * @Description: 模板相关定义
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import (
	"strings"
)

// WelcomeMessage 欢迎消息
type WelcomeMessage struct {
	Title    string                 `json:"title"`    // 欢迎标题
	Content  string                 `json:"content"`  // 欢迎内容
	Data     map[string]interface{} `json:"data"`     // 扩展数据
	Priority Priority               `json:"priority"` // 消息优先级
}

// WelcomeTemplate 欢迎消息模板
type WelcomeTemplate struct {
	Title       string                 `json:"title"`        // 欢迎标题
	Content     string                 `json:"content"`      // 欢迎内容
	MessageType MessageType            `json:"message_type"` // 消息类型
	Data        map[string]interface{} `json:"data"`         // 扩展数据
	Enabled     bool                   `json:"enabled"`      // 是否启用
	Variables   []string               `json:"variables"`    // 支持的变量列表，如: {user_name}, {time}
}

// ReplaceVariables 替换模板中的变量
func (wt *WelcomeTemplate) ReplaceVariables(variables map[string]interface{}) WelcomeTemplate {
	result := *wt

	for key, value := range variables {
		placeholder := "{" + key + "}"
		if val, ok := value.(string); ok {
			result.Title = strings.ReplaceAll(result.Title, placeholder, val)
			result.Content = strings.ReplaceAll(result.Content, placeholder, val)
		}
	}

	return result
}

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

// WelcomeMessageProvider 欢迎消息提供者接口
type WelcomeMessageProvider interface {
	// GetWelcomeMessage 获取欢迎消息
	// 参数: userID 用户ID, userRole 用户角色, userType 用户类型, extraData 扩展数据
	// 返回: 欢迎消息内容, 是否启用欢迎消息, 错误信息
	GetWelcomeMessage(userID string, userRole UserRole, userType UserType, extraData map[string]interface{}) (*WelcomeMessage, bool, error)

	// RefreshConfig 刷新配置 - 当数据库配置更新时调用
	RefreshConfig() error
}

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

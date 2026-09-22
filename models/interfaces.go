/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 11:36:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 18:36:00
 * @FilePath: \go-wsc\models\interfaces.go
 * @Description: 模型层行为端口
 *
 * ID 生成与欢迎消息的能力端口，实现由宿主应用注入。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

// IDGenerator ID生成器接口
// 用于生成消息ID、请求ID等唯一标识符
type IDGenerator interface {
	GenerateTraceID() string
	GenerateSpanID() string
	GenerateRequestID() string
	GenerateCorrelationID() string
}

// WelcomeMessageProvider 欢迎消息提供者接口
type WelcomeMessageProvider interface {
	// GetWelcomeMessage 获取欢迎消息
	// 参数: userID 用户ID, userRole 用户角色, userType 用户类型, extraData 扩展数据
	// 返回: 欢迎消息内容, 是否启用欢迎消息, 错误信息
	GetWelcomeMessage(userID string, userRole UserRole, userType UserType, extraData map[string]interface{}) (*WelcomeMessage, bool, error)

	// RefreshConfig 刷新配置 - 当数据库配置更新时调用
	RefreshConfig() error
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 09:07:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 10:30:00
 * @FilePath: \go-wsc\spi\auth.go
 * @Description: 连接鉴权 SPI - ConnectionAuthenticator 接口与 ConnectionClaims 契约定义
 *
 * spi 包是 go-wsc 的能力接口层（go-casbin 范式）：只放接口与接口直接引用的契约类型，
 * 不含任何基础设施实现默认鉴权实现（AES-GCM 对称加解密）见 transport 包
 * TokenAuthenticator；业务侧自定义鉴权（OAuth/私有 Token 等）实现本接口后注入 Hub
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

import (
	"net/http"
)

// ConnectionClaims 连接 Token 的身份声明（加密载荷）
// 将原本明文暴露的 user_id/user_type/device_id/app_id/namespace/group_id 加密到 token 中
// 字段名采用短缩写以减小 token 体积
// 注意：
//   - app_id 为应用ID（最上层隔离维度，默认 "__default_app__"）
//   - group_id 为连接时自动加入的成员组标识（支持逗号分隔多群组"g1,g2,g3"；空则加入默认组）
//   - 群组成员关系的后续变更仍由业务层 API 管理
type ConnectionClaims struct {
	UserID    string `json:"uid"`           // 用户ID（必填）
	UserType  string `json:"utp,omitempty"` // 用户类型（默认 visitor）
	DeviceID  string `json:"did,omitempty"` // 设备ID
	AppID     string `json:"aid,omitempty"` // 应用ID（最上层隔离维度，默认 "__default_app__"）
	Namespace string `json:"tid,omitempty"` // 命名空间ID（默认 "default"，用于命名空间隔离与消息过滤）
	GroupID   string `json:"gid,omitempty"` // 群组ID（可选，支持逗号分隔多群组"g1,g2,g3"；用于群组消息过滤与连接时自动加入）
	ExpiresAt int64  `json:"exp,omitempty"` // 过期时间（Unix 秒；签发时自动填充，解密后校验）
}

// ConnectionAuthenticator 连接鉴权器 SPI
// 从 HTTP 握手请求中提取并解密连接凭证，返回连接身份声明
// 内置实现见 transport 包 TokenAuthenticator（AES-GCM 对称加解密）；
// 业务侧可实现本接口替换默认鉴权（如 OAuth/私有 Token），经 Hub 注入
type ConnectionAuthenticator interface {
	// Authenticate 从 HTTP 请求中提取并解密 token
	// 返回解密后的连接身份声明；token 不存在/无效/篡改/过期时返回 error
	Authenticate(r *http.Request) (*ConnectionClaims, error)
}

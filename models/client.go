/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 12:20:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:56:15
 * @FilePath: \go-wsc\models\types.go
 * @Description:
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Client 客户端连接（统一管理 WebSocket 和 SSE 连接）
type Client struct {
	ID             string                 `json:"id"`              // 客户端ID
	UserID         string                 `json:"user_id"`         // 用户ID
	UserType       UserType               `json:"user_type"`       // 用户类型
	VIPLevel       VIPLevel               `json:"vip_level"`       // VIP等级
	Role           UserRole               `json:"role"`            // 用户角色
	ClientIP       string                 `json:"client_ip"`       // 客户端IP
	Conn           *websocket.Conn        `json:"-"`               // WebSocket连接（不序列化，仅WS使用）
	LastSeen       time.Time              `json:"last_seen"`       // 最后活跃时间
	LastHeartbeat  time.Time              `json:"last_heartbeat"`  // 最后心跳时间
	Status         UserStatus             `json:"status"`          // 用户状态
	Department     Department             `json:"department"`      // 部门
	Skills         []Skill                `json:"skills"`          // 技能列表
	MaxTickets     int                    `json:"max_tickets"`     // 最大工单数
	NodeID         string                 `json:"node_id"`         // 所在节点ID
	ClientType     ClientType             `json:"client_type"`     // 客户端类型（web/mobile/desktop）
	ConnectionType ConnectionType         `json:"connection_type"` // 连接类型（websocket/sse）
	Metadata       map[string]interface{} `json:"metadata"`        // 元数据
	SendChan       chan []byte            `json:"-"`               // 发送通道（不序列化，仅WS使用）
	Context        context.Context        `json:"-"`               // 上下文（不序列化）
	closed         atomic.Bool            `json:"-"`               // channel关闭标志（不序列化）
	CloseMu        sync.Mutex             `json:"-"`               // 保护channel关闭的互斥锁（不序列化）

	// SSE 专用字段（仅当 ConnectionType 为 SSE 时使用）
	SSEWriter    http.ResponseWriter `json:"-"` // SSE Writer（不序列化）
	SSEFlusher   http.Flusher        `json:"-"` // SSE Flusher（不序列化）
	SSEMessageCh chan *HubMessage    `json:"-"` // SSE 消息通道（不序列化）
	SSECloseCh   chan struct{}       `json:"-"` // SSE 关闭通道（不序列化）
}

// GetClientIP 获取客户端IP地址
func (c *Client) GetClientIP() string {
	// 1. 优先从ClientIP字段获取
	if c.ClientIP != "" {
		return c.ClientIP
	}

	// 2. 从WebSocket连接直接获取
	if c.Conn != nil {
		if remoteAddr := c.Conn.RemoteAddr(); remoteAddr != nil {
			// 提取IP地址（去除端口号）
			if host, _, err := net.SplitHostPort(remoteAddr.String()); err == nil {
				return host
			}
			return remoteAddr.String()
		}
	}

	// 3. 从Metadata中获取
	if c.Metadata != nil {
		if ip, ok := c.Metadata["client_ip"].(string); ok && ip != "" {
			return ip
		}
		if ip, ok := c.Metadata["x-forwarded-for"].(string); ok && ip != "" {
			// X-Forwarded-For 可能包含多个IP，取第一个
			if parts := strings.Split(ip, ","); len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
		if ip, ok := c.Metadata["x-real-ip"].(string); ok && ip != "" {
			return ip
		}
	}

	// 4. 从Context中获取
	if c.Context != nil {
		if ip := c.Context.Value("client_ip"); ip != nil {
			if ipStr, ok := ip.(string); ok && ipStr != "" {
				return ipStr
			}
		}
	}

	return "unknown"
}

// GetUserAgent 获取用户代理
func (c *Client) GetUserAgent() string {
	// 从 Metadata 中获取用户代理
	if c.Metadata != nil {
		if ua, ok := c.Metadata["user_agent"].(string); ok && ua != "" {
			return ua
		}
		if ua, ok := c.Metadata["user-agent"].(string); ok && ua != "" {
			return ua
		}
	}
	// 从 Context 中获取
	if c.Context != nil {
		if ua := c.Context.Value("user_agent"); ua != nil {
			if uaStr, ok := ua.(string); ok && uaStr != "" {
				return uaStr
			}
		}
		if ua := c.Context.Value("user-agent"); ua != nil {
			if uaStr, ok := ua.(string); ok && uaStr != "" {
				return uaStr
			}
		}
	}
	return "unknown"
}

// IsClosed 检查客户端channel是否已关闭
func (c *Client) IsClosed() bool {
	return c.closed.Load()
}

// MarkClosed 标记客户端channel为已关闭
func (c *Client) MarkClosed() {
	c.closed.Store(true)
}

// TrySend 尝试向客户端发送数据（WebSocket），如果已关闭或失败则返回false
func (c *Client) TrySend(data []byte) bool {
	c.CloseMu.Lock()
	defer c.CloseMu.Unlock()

	if c.IsClosed() || c.SendChan == nil {
		return false
	}

	select {
	case c.SendChan <- data:
		return true
	default:
		return false
	}
}

// TrySendSSE 尝试向SSE客户端发送消息，如果已关闭或失败则返回false
func (c *Client) TrySendSSE(msg *HubMessage) bool {
	c.CloseMu.Lock()
	defer c.CloseMu.Unlock()

	if c.IsClosed() || c.SSEMessageCh == nil {
		return false
	}

	select {
	case c.SSEMessageCh <- msg:
		return true
	default:
		return false
	}
}

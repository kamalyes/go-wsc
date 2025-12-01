package wsc

import (
	"net"
	"strings"
)

// ============================================================================
// Client 扩展方法 - 获取客户端信息
// ============================================================================

// GetClientIP 获取客户端IP地址
func (c *Client) GetClientIP() string {
	// 1. 优先从WebSocket连接直接获取（内置方法）
	if c.Conn != nil {
		if remoteAddr := c.Conn.RemoteAddr(); remoteAddr != nil {
			// 提取IP地址（去除端口号）
			if host, _, err := net.SplitHostPort(remoteAddr.String()); err == nil {
				return host
			}
			// 如果没有端口号，直接返回
			return remoteAddr.String()
		}
	}

	// 2. 备用方案：从Metadata中获取（代理/负载均衡器设置）
	if c.Metadata != nil {
		// X-Forwarded-For 或 X-Real-IP 头
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
		if ip, ok := c.Metadata["remote_addr"].(string); ok && ip != "" {
			return ip
		}
	}

	// 3. 最后备用：从Context中获取
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
	// 尝试从 Metadata 中获取用户代理
	if c.Metadata != nil {
		if ua, ok := c.Metadata["user_agent"].(string); ok && ua != "" {
			return ua
		}
		if ua, ok := c.Metadata["user-agent"].(string); ok && ua != "" {
			return ua
		}
	}
	// 尝试从 Context 中获取
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

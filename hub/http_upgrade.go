/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-10 00:00:00
 * @FilePath: \go-wsc\hub\http_upgrade.go
 * @Description: HTTP WebSocket 升级处理
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/metadata"
)

// ============================================================================
// WebSocket 升级器配置
// ============================================================================

// ConfigureUpgrader 配置 WebSocket 升级器
// 根据 Hub 配置创建升级器，支持自定义缓冲区大小和 Origin 检查
func (h *Hub) ConfigureUpgrader() *websocket.Upgrader {
	upgrader := &websocket.Upgrader{
		ReadBufferSize:  h.config.MessageBufferSize,
		WriteBufferSize: h.config.MessageBufferSize,
		CheckOrigin: func(r *http.Request) bool {
			return true // 默认允许所有来源
		},
	}

	// 自定义 Origin 检查
	if len(h.config.WebSocketOrigins) > 0 {
		upgrader.CheckOrigin = h.createOriginChecker()
	}

	return upgrader
}

// createOriginChecker 创建 Origin 检查器
// 根据配置的允许来源列表检查请求的 Origin
func (h *Hub) createOriginChecker() func(*http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		for _, allowedOrigin := range h.config.WebSocketOrigins {
			if allowedOrigin == "*" || allowedOrigin == origin {
				return true
			}
		}
		return false
	}
}

// ============================================================================
// 客户端创建
// ============================================================================

// CreateClientFromRequest 从 HTTP 请求创建 WebSocket 客户端
// 提取请求元数据并创建 Client 实例
//
// 参数:
//   - r: HTTP 请求
//   - conn: WebSocket 连接
//
// 返回:
//   - *Client: 创建的客户端实例
func (h *Hub) CreateClientFromRequest(r *http.Request, conn *websocket.Conn) *Client {
	clientID, userID, userType := h.extractClientAttributes(r)
	// 若终端未传入则自动生成一个
	clientID = mathx.IfEmpty(clientID, h.idGenerator.GenerateRequestID())
	clientUserType := UserType(userType)

	// 使用 metadata 提取所有请求元数据
	requestMeta := metadata.ExtractRequestMetadata(r)
	metaMap := requestMeta.ToMap()

	now := time.Now()

	client := &Client{
		// 基础标识字段
		ID:       clientID,
		UserID:   userID,
		UserType: clientUserType,

		// 连接信息字段
		ClientIP:       requestMeta.ClientIP,                              // 从 metadata 提取 ClientIP
		ClientType:     MapDeviceTypeToClientType(requestMeta.DeviceType), // 映射设备类型到客户端类型
		ConnectionType: ConnectionTypeWebSocket,                           // 连接类型为 WebSocket

		// 设置客户端所在节点信息
		NodeID:   h.nodeID,
		NodeIP:   h.config.NodeIP,
		NodePort: h.config.NodePort,

		// WebSocket 连接字段
		Conn: conn,

		// 时间字段
		ConnectedAt:   now,
		LastSeen:      now,
		LastHeartbeat: now,

		// 状态字段
		Status: UserStatusOnline,

		// 上下文和元数据
		Context:  context.WithValue(r.Context(), ContextKeySenderID, userID),
		Metadata: metaMap,
	}

	// 初始化客户端 SendChan（根据客户端类型使用配置的容量）
	h.initClientSendChan(client)
	return client
}

// extractClientAttributes 从请求中提取客户端属性
// 根据配置的来源列表按优先级提取属性
//
// 返回: clientID, userID, userType
func (h *Hub) extractClientAttributes(r *http.Request) (string, string, string) {
	clientID := h.extractAttribute(r, h.config.ClientAttributes.ClientIDSources)
	userID := h.extractAttribute(r, h.config.ClientAttributes.UserIDSources)
	userType := h.extractAttribute(r, h.config.ClientAttributes.UserTypeSources)

	return clientID, userID, userType
}

// extractAttribute 从请求中提取单个属性
// 按配置的来源列表顺序查找，返回第一个非空值
func (h *Hub) extractAttribute(r *http.Request, sources []wscconfig.AttributeSource) string {
	for _, source := range sources {
		value := h.extractFromSource(r, source)
		if value != "" {
			return value
		}
	}
	return ""
}

// extractFromSource 从指定来源提取值
func (h *Hub) extractFromSource(r *http.Request, source wscconfig.AttributeSource) string {
	switch source.Type {
	case wscconfig.AttributeSourceQuery:
		return r.URL.Query().Get(source.Key)
	case wscconfig.AttributeSourceHeader:
		return r.Header.Get(source.Key)
	case wscconfig.AttributeSourceCookie:
		if cookie, err := r.Cookie(source.Key); err == nil {
			return cookie.Value
		}
		return ""
	case wscconfig.AttributeSourcePath:
		// 从 URL 路径中提取（需要路由支持，这里暂时返回空）
		// 实际使用时可以配合 gorilla/mux 等路由库的 Vars 功能
		return ""
	default:
		return ""
	}
}

// ============================================================================
// HTTP WebSocket 升级处理
// ============================================================================

// HandleWebSocketUpgrade 处理 WebSocket 升级请求
// 此函数负责：升级连接 -> 创建客户端 -> 注册到 Hub
// 所有消息处理都由 go-wsc Hub 完成
//
// 参数:
//   - w: HTTP 响应写入器
//   - r: HTTP 请求
func (h *Hub) HandleWebSocketUpgrade(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()

	// 提取客户端属性
	clientID, userID, userType := h.extractClientAttributes(r)

	// 记录 WebSocket 升级请求开始（包含完整的请求信息）
	h.logger.InfoContextKV(ctx, "[WebSocket] 升级请求",
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"client_id", clientID,
		"user_id", userID,
		"user_type", userType,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
		"origin", r.Header.Get("Origin"),
		"sec_websocket_key", r.Header.Get("Sec-WebSocket-Key"),
		"sec_websocket_version", r.Header.Get("Sec-WebSocket-Version"),
		"sec_websocket_protocol", r.Header.Get("Sec-WebSocket-Protocol"),
		"connection", r.Header.Get("Connection"),
		"upgrade", r.Header.Get("Upgrade"),
	)

	// 配置并升级 WebSocket 连接
	upgrader := h.ConfigureUpgrader()
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// 记录升级失败日志
		h.logger.WithError(err).ErrorContextKV(ctx, "[WebSocket] 升级失败",
			"client_id", clientID,
			"user_id", userID,
			"duration_ms", time.Since(start).Milliseconds(),
			"error", err.Error(),
			"upgrade_failed", true,
		)
		return
	}

	// 记录升级成功日志（升级后响应已发送，记录连接信息）
	h.logger.InfoContextKV(ctx, "[WebSocket] 升级成功",
		"client_id", clientID,
		"user_id", userID,
		"user_type", userType,
		"status_code", 101, // WebSocket 升级成功状态码固定为 101
		"protocol", conn.Subprotocol(),
		"remote_addr", conn.RemoteAddr().String(),
		"local_addr", conn.LocalAddr().String(),
		"duration_ms", time.Since(start).Milliseconds(),
		"upgrade_success", true,
	)

	// 创建客户端
	client := h.CreateClientFromRequest(r, conn)

	// 注册到 Hub（go-wsc 接管后续所有处理，包括消息读取）
	h.Register(client)

	// 记录客户端注册成功日志
	h.logger.InfoContextKV(ctx, "[WebSocket] 客户端注册成功",
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", string(client.UserType),
	)
}

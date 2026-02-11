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

	// 使用 metadata 提取所有请求元数据
	requestMeta := metadata.ExtractRequestMetadata(r)
	metaMap := requestMeta.ToMap()

	// 使用 NewClient 构造函数创建客户端（自动初始化时间和状态）
	client := NewClient(clientID, userID, UserType(userType)).
		WithClientIP(requestMeta.ClientIP).
		WithClientType(MapDeviceTypeToClientType(requestMeta.DeviceType)).
		WithWebSocketConn(conn).
		WithNodeInfo(h.nodeID, h.config.NodeIP, h.config.NodePort).
		WithMetadataMap(metaMap).
		WithContext(context.WithValue(r.Context(), ContextKeySenderID, userID))

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

	var (
		client  *Client
		err     error
		stage   string = "upgrade" // 当前阶段：upgrade/register
		success bool
	)

	// defer 统一记录日志
	defer func() {
		logFields := h.buildWebSocketUpgradeLogFields(r, start, client, stage, success)
		if err != nil {
			h.logger.WithError(err).ErrorContextKV(ctx, "[WebSocket] 处理失败", logFields...)
		} else {
			h.logger.InfoContextKV(ctx, "[WebSocket] 处理成功", logFields...)
		}
	}()

	// 提取客户端属性
	clientID := h.extractAttribute(r, h.config.ClientAttributes.ClientIDSources)
	// 若终端未传入则自动生成一个
	clientID = mathx.IfEmpty(clientID, h.idGenerator.GenerateRequestID())

	// 配置升级器并设置响应头
	upgrader := h.ConfigureUpgrader()
	responseHeader := http.Header{}

	// 如果启用了响应头配置，则添加服务端信息
	if h.config.ResponseHeaders != nil && h.config.ResponseHeaders.Enabled {
		respHeaders := h.config.ResponseHeaders

		// 服务端分配的客户端ID
		responseHeader.Set(respHeaders.GetClientIDKey(), clientID)

		// 服务端节点信息（分布式场景下很有用）
		responseHeader.Set(respHeaders.GetNodeIDKey(), h.nodeID)

		// 添加自定义响应头
		for key, value := range respHeaders.CustomHeaders {
			responseHeader.Set(key, value)
		}
	}

	// 升级连接
	conn, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return
	}

	// 创建并注册客户端
	stage = "register"
	client = h.CreateClientFromRequest(r, conn)
	client.ID = clientID
	h.Register(client)
	success = true

	// 发送客户端注册成功确认消息（如果配置启用）
	if h.config.ResponseHeaders != nil && h.config.ResponseHeaders.SendRegisteredMessage {
		h.sendClientRegisteredMessage(client)
	}
}

// buildWebSocketUpgradeLogFields 构建 WebSocket 升级日志字段
func (h *Hub) buildWebSocketUpgradeLogFields(r *http.Request, start time.Time, client *Client, stage string, success bool) []any {
	logFields := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
		"origin", r.Header.Get("Origin"),
		"duration_ms", time.Since(start).Milliseconds(),
		"stage", stage,
		"success", success,
	}

	if client != nil {
		logFields = append(logFields,
			"client_id", client.ID,
			"user_id", client.UserID,
			"user_type", client.UserType.String(),
			"protocol", client.Conn.Subprotocol(),
			"conn_remote_addr", client.Conn.RemoteAddr().String(),
			"conn_local_addr", client.Conn.LocalAddr().String(),
		)
	}

	return logFields
}

// sendClientRegisteredMessage 发送客户端注册成功确认消息
func (h *Hub) sendClientRegisteredMessage(client *Client) {
	// 获取自定义消息内容
	content := "Client registered successfully"
	if h.config.ResponseHeaders != nil && h.config.ResponseHeaders.RegisteredMessageContent != "" {
		content = h.config.ResponseHeaders.RegisteredMessageContent
	}

	// 构建客户端注册成功消息
	registeredMsg := NewHubMessage().
		SetID(h.idGenerator.GenerateRequestID()).
		SetMessageType(MessageTypeClientRegistered).
		SetSender(UserTypeSystem.String()).
		SetSenderType(UserTypeSystem).
		SetReceiver(client.UserID).
		SetReceiverType(client.UserType).
		SetReceiverClient(client.ID).
		SetContent(content).
		WithOption("status", "ok").
		WithOption("client_id", client.ID).
		WithOption("user_id", client.UserID).
		WithOption("user_type", client.UserType.String()).
		WithOption("node_id", client.NodeID).
		WithOption("timestamp", time.Now().Unix())

	// 添加服务端信息（来自 CustomHeaders）
	if h.config.ResponseHeaders != nil && h.config.ResponseHeaders.Enabled {
		for key, value := range h.config.ResponseHeaders.CustomHeaders {
			registeredMsg.WithOption(key, value)
		}
	}

	// 发送注册成功消息
	h.sendToClient(client, registeredMsg)
}

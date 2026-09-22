/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 09:36:00
 * @FilePath: \go-wsc\transport\upgrader.go
 * @Description: WebSocket 升级器 —— 升级配置 / 客户端属性提取 / 客户端构造 / 升级编排
 *
 * 迁移自 hub/http_upgrade.go（P2 批1 域化）：原 *Hub 方法重组为 Upgrader 组件，
 * config/logger/编排端口经构造与链式注入；深编排（注册/确认消息）经 Registrar 端口回调
 *
 * 升级链路：HandleWebSocketUpgrade = 健康检查 → 属性提取（连接 Token 优先，明文兜底）
 *       → 连接验证 → 关闭检查 → 协议升级 → 客户端构造 → 异步注册 → 注册确认消息
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	gccommon "github.com/kamalyes/go-config/pkg/common"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/metadata"
	"github.com/kamalyes/go-toolbox/pkg/safe"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/messaging"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ClientAttributes 客户端属性（升级前的请求元数据提取结果）
type ClientAttributes struct {
	ClientID  string          // 客户端ID
	UserID    string          // 用户ID
	UserType  models.UserType // 用户类型
	DeviceID  string          // 设备ID
	AppID     string          // 应用ID（最上层隔离维度，默认 "__default_app__"，用于应用间消息隔离）
	Namespace string          // 命名空间ID（默认 "default"，用于命名空间隔离与消息过滤）
	GroupID   string          // 群组ID原始字符串（支持逗号分隔多群组"g1,g2,g3"；观察者表示观察范围；普通用户表示连接后自动加入的成员组；空则加入默认组）
	GroupIDs  []string        // 解析后的群组ID列表（由 GroupID 逗号解析得到；单值时为 [GroupID]）
}

// Upgrader WebSocket 升级器
// 职责：升级器配置（Origin 检查/缓冲区）、客户端属性提取（Token/明文）、
// 客户端构造（元数据/节点信息/连接级 ctx）与完整升级编排
type Upgrader struct {
	cfg       *wscconfig.WSC              // 全量配置（升级缓冲/来源/健康检查/验证/响应头）
	registrar Registrar                   // 编排层端口（注册/注销/关闭态/确认消息）
	logger    spi.Logger                  // 日志器（nil 时构造兜底默认实例）
	hasher    *safe.TemporalHasher        // 时间窗口哈希（ClientID 生成，由 cfg.TemporalHasher 构造）
	auth      spi.ConnectionAuthenticator // 连接鉴权器（nil=未启用连接 Token，纯明文提取）
	chanPool  *connection.ChanPool        // 客户端通道池（nil=跳过预初始化，注册路径兜底）
	idGen     RequestIDGenerator          // 请求 ID 生成器（nil=健康检查消息 ID 留空）
	hubCtx    context.Context             // 编排层生命周期 ctx（连接级 ctx 的父）
	nodeID    string                      // 本节点标识（响应头/注册确认消息回显）

	upgraderOnce sync.Once
	wsUpgrader   *websocket.Upgrader
}

// NewUpgrader 创建 WebSocket 升级器
// cfg 为全量配置（nil 直接 panic，fail-fast，与 NewHub 对 nil config 同策略）
func NewUpgrader(cfg *wscconfig.WSC, registrar Registrar) *Upgrader {
	if cfg == nil {
		panic("transport: NewUpgrader 依赖非 nil 配置")
	}

	// 时间窗口哈希生成器（用于生成 ClientID），配置缺失时回退默认
	thConfig := mathx.IfEmpty(cfg.TemporalHasher, wscconfig.DefaultTemporalHasher())
	return &Upgrader{
		cfg:       cfg,
		registrar: registrar,
		logger:    spi.NewDefaultLogger(),
		hasher: safe.NewTemporalHasher(
			safe.WithWindow(time.Duration(thConfig.GetWindowMinutes())*time.Minute),
			safe.WithLength(thConfig.GetHashLength()),
			safe.WithSeparator(thConfig.GetSeparator()),
		),
		hubCtx: context.Background(),
	}
}

// WithLogger 注入日志器
func (u *Upgrader) WithLogger(logger spi.Logger) *Upgrader {
	if logger != nil {
		u.logger = logger
	}
	return u
}

// WithAuthenticator 注入连接鉴权器（连接 Token 启用时必须注入，nil=纯明文提取）
func (u *Upgrader) WithAuthenticator(auth spi.ConnectionAuthenticator) *Upgrader {
	u.auth = auth
	return u
}

// WithChanPool 注入客户端通道池（升级路径预初始化 SendChan；nil=注册路径兜底初始化）
func (u *Upgrader) WithChanPool(pool *connection.ChanPool) *Upgrader {
	u.chanPool = pool
	return u
}

// WithIDGenerator 注入请求 ID 生成器（健康检查响应消息 ID）
func (u *Upgrader) WithIDGenerator(gen RequestIDGenerator) *Upgrader {
	u.idGen = gen
	return u
}

// WithHubContext 注入编排层生命周期 ctx（连接级 ctx 的父；默认 Background）
func (u *Upgrader) WithHubContext(ctx context.Context) *Upgrader {
	if ctx != nil {
		u.hubCtx = ctx
	}
	return u
}

// WithNodeID 注入节点标识
func (u *Upgrader) WithNodeID(nodeID string) *Upgrader {
	u.nodeID = nodeID
	return u
}

// ============================================================================
// 升级器配置
// ============================================================================

// ConfigureUpgrader 配置 WebSocket 升级器（sync.Once 惰性初始化，可安全并发调用）
// 根据配置创建升级器，支持自定义缓冲区大小和 Origin 检查
func (u *Upgrader) ConfigureUpgrader() *websocket.Upgrader {
	u.upgraderOnce.Do(func() {
		u.wsUpgrader = &websocket.Upgrader{
			ReadBufferSize:  u.cfg.MessageBufferSize,
			WriteBufferSize: u.cfg.MessageBufferSize,
			CheckOrigin: func(r *http.Request) bool {
				return true // 默认允许所有来源
			},
		}

		// 自定义 Origin 检查
		if len(u.cfg.WebSocketOrigins) > 0 {
			u.wsUpgrader.CheckOrigin = u.createOriginChecker()
		}
	})
	return u.wsUpgrader
}

// createOriginChecker 创建 Origin 检查器
// 根据配置的允许来源列表检查请求的 Origin
func (u *Upgrader) createOriginChecker() func(*http.Request) bool {
	return func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		for _, allowedOrigin := range u.cfg.WebSocketOrigins {
			if allowedOrigin == "*" || allowedOrigin == origin {
				return true
			}
		}
		return false
	}
}

// ============================================================================
// 客户端属性提取
// ============================================================================

// ExtractClientAttributes 从请求中提取客户端属性
// 若启用连接 Token（Security.ConnectionToken.Enabled 且注入了鉴权器），优先从 Token 解码；
// 解码失败时按 AllowFallback 决定是否回退明文提取，不允许回退则返回 nil（调用方拒绝连接）
func (u *Upgrader) ExtractClientAttributes(r *http.Request) *ClientAttributes {
	// 优先尝试连接 Token 解码（若注入了鉴权器）
	if u.auth != nil {
		claims, err := u.auth.Authenticate(r)
		if err != nil {
			cfg := u.cfg.Security.ConnectionToken
			if cfg != nil && cfg.AllowFallback {
				// 允许回退：记录警告后继续走明文提取
				u.logger.WarnContextKV(r.Context(), "[WebSocket] 连接 Token 解码失败，回退到明文参数",
					"error", err, "remote_addr", r.RemoteAddr)
				// 继续走下面的明文流程
			} else {
				// 不允许回退：拒绝连接（返回 nil，调用方需检查并拒绝）
				u.logger.WarnContextKV(r.Context(), "[WebSocket] 连接 Token 解码失败，拒绝连接",
					"error", err, "remote_addr", r.RemoteAddr, "query", r.URL.RawQuery)
				return nil
			}
		} else {
			// 解码成功
			userID := claims.UserID
			userType := claims.UserType
			deviceID := claims.DeviceID
			appID := claims.AppID
			namespace := claims.Namespace
			groupID := claims.GroupID
			// UserType 默认值为 visitor
			userType = mathx.IfEmpty(userType, string(models.UserTypeVisitor))
			// 基于 UserID + DeviceID + UserType 时间窗口哈希生成 ClientID
			clientID := u.hasher.Hash(userID, deviceID, userType)
			// GroupID 逗号解析为 GroupIDs（兼容单值与多值，多值时 GroupID 取首项）
			groupIDs := parseGroupIDs(groupID)
			return &ClientAttributes{
				ClientID:  clientID,
				UserID:    userID,
				UserType:  models.UserType(userType),
				DeviceID:  deviceID,
				AppID:     appID,
				Namespace: namespace,
				GroupID:   groupID,
				GroupIDs:  groupIDs,
			}
		}
	}

	// 明文提取（连接 Token 未启用或允许回退）
	clientID := gccommon.ExtractAttribute(r, u.cfg.ClientAttributes.ClientIDSources)
	userID := gccommon.ExtractAttribute(r, u.cfg.ClientAttributes.UserIDSources)
	deviceID := gccommon.ExtractAttribute(r, u.cfg.ClientAttributes.DeviceIdSources)
	userType := gccommon.ExtractAttribute(r, u.cfg.ClientAttributes.UserTypeSources)
	// AppID/命名空间从配置来源提取；GroupID 支持逗号分隔多群组（从 query/header 提取）
	appID := gccommon.ExtractAttribute(r, u.cfg.ClientAttributes.AppIDSources)
	namespace := gccommon.ExtractAttribute(r, u.cfg.ClientAttributes.NamespaceSources)
	groupID := gccommon.ExtractAttribute(r, u.cfg.ClientAttributes.GroupIDSources)

	// UserType 默认值为 visitor
	userType = mathx.IfEmpty(userType, string(models.UserTypeVisitor))

	// 仅在请求未提供 ClientID 时，基于 UserID + DeviceID + UserType 时间窗口哈希生成
	// 请求显式传入的 client_id 优先使用，避免覆盖调用方指定的标识
	clientID = mathx.IfEmpty(clientID, u.hasher.Hash(userID, deviceID, userType))

	// GroupID 逗号解析为 GroupIDs（兼容单值与多值，多值时 GroupID 取首项）
	groupIDs := parseGroupIDs(groupID)
	return &ClientAttributes{
		ClientID:  clientID,
		UserID:    userID,
		UserType:  models.UserType(userType),
		DeviceID:  deviceID,
		AppID:     appID,
		Namespace: namespace,
		GroupID:   groupID,
		GroupIDs:  groupIDs,
	}
}

// parseGroupIDs 将逗号分隔的群组ID字符串解析为列表（向后兼容单值）
// 与 routing 包 gRPC metadata 的 strings.Join(gids, ",") 约定一致：
//   - 单值 "g-1"（无逗号）→ ["g-1"]（向后兼容）
//   - 多值 "g1,g2,g3" → ["g1","g2","g3"]
//   - 空串 "" → nil（调用方走默认组兜底）
//   - 带空白 "g1, g2 , g3" → ["g1","g2","g3"]（TrimSpace 容错）
func parseGroupIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	gids := make([]string, 0, len(parts))
	for _, p := range parts {
		if g := strings.TrimSpace(p); g != "" {
			gids = append(gids, g)
		}
	}
	if len(gids) == 0 {
		return nil
	}
	return gids
}

// ============================================================================
// 客户端构造
// ============================================================================

// CreateClientFromRequest 从 HTTP 请求创建 WebSocket 客户端
// 提取请求元数据并创建 Client 实例（链式构造，保证路由隔离/归一化与注册路径一致）
func (u *Upgrader) CreateClientFromRequest(r *http.Request, conn *websocket.Conn, attrs *ClientAttributes) *models.Client {
	// 使用 metadata 提取所有请求元数据
	requestMeta := metadata.ExtractRequestMetadata(r)
	metaMap := requestMeta.ToMap()

	// 将 deviceID 存储到 metadata 中
	metaMap["x-device-id"] = attrs.DeviceID

	// appID：最上层隔离维度（入口层归一化在注册路径兜底，此处透传请求值）
	// 观察者：GroupID 表示观察范围；普通用户：GroupIDs 表示连接后自动加入的全部成员组（空则加入默认组）
	// 群组成员关系：连接时自动加入成员组，业务层仍可通过 API（AddGroupMembers/RemoveGroupMembers）管理
	client := models.NewClient(attrs.ClientID, attrs.UserID, attrs.UserType).
		WithClientIP(requestMeta.ClientIP).
		WithClientType(models.MapDeviceTypeToClientType(requestMeta.DeviceType)).
		WithWebSocketConn(conn).
		WithNodeInfo(u.nodeID, u.cfg.NodeIP, u.cfg.NodePort).
		WithAppID(attrs.AppID).
		WithNamespace(attrs.Namespace).
		WithGroupIDs(attrs.GroupIDs).
		WithMetadataMap(metaMap).
		// 从编排层生命周期 ctx 派生连接级 ctx（r.Context() 在 WebSocket 升级后会取消，不适合长连接）
		// 但需要从 r.Context() 提取 trace_id 注入，保证客户端生命周期内的日志都有 trace_id
		WithContext(func() context.Context {
			connCtx := context.WithValue(u.hubCtx, messaging.ContextKeySenderID, attrs.UserID)
			// 从 HTTP 请求 ctx 提取 trace_id（OTel span > ctx.Value fallback），注入到连接级 ctx
			if traceID := logger.ExtractTraceID(r.Context()); traceID != "" {
				connCtx = logger.ContextWithTraceID(connCtx, traceID)
			}
			return connCtx
		}())

	// 预初始化客户端 SendChan（根据客户端类型使用配置的容量；注册路径幂等兜底）
	if u.chanPool != nil {
		u.chanPool.InitClientSendChan(client)
	}
	return client
}

// ============================================================================
// HTTP WebSocket 升级编排
// ============================================================================

// HandleWebSocketUpgrade 处理 WebSocket 升级请求（网关层 /ws 路由的 HTTP handler）
// 此函数负责：健康检查 → 属性提取 → 连接验证 → 协议升级 → 创建客户端 → 注册
// 所有消息处理都由编排层完成（经 Registrar 端口回调）
func (u *Upgrader) HandleWebSocketUpgrade(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()

	var (
		client        *models.Client
		err           error
		success       bool
		isHealthCheck bool
	)

	// defer 统一记录日志
	defer func() {
		logFields := u.buildWebSocketUpgradeLogFields(r, start, client, success)
		logFields = append(logFields, "health_check", isHealthCheck)
		if err != nil {
			u.logger.WithError(err).ErrorContextKV(ctx, "[WebSocket] 处理失败", logFields...)
		} else {
			u.logger.InfoContextKV(ctx, "[WebSocket] 处理成功", logFields...)
		}
	}()

	// 检查是否为健康检查请求（使用配置）
	if u.cfg.HealthCheck.Enabled {
		queryValue := r.URL.Query().Get(u.cfg.HealthCheck.GetQueryParamName())
		isHealthCheck = u.cfg.HealthCheck.IsHealthCheckRequest(queryValue)

		if isHealthCheck {
			u.handleHealthCheck(w, r)
			success = true
			return
		}
	}

	// 提取客户端属性
	attrs := u.ExtractClientAttributes(r)

	// 启用连接 Token 且不允许回退时，attrs 可能为 nil（已记录拒绝日志）
	if attrs == nil {
		return
	}

	// 使用配置进行连接验证
	if u.cfg.ConnectionValidation.Enabled {
		valid, reason := u.cfg.ConnectionValidation.ValidateConnection(attrs.UserID, attrs.UserType.String())
		if !valid {
			// 缺少必需参数，拒绝连接
			u.logger.WarnContextKV(r.Context(), "[WebSocket] 连接被拒绝",
				"reason", reason,
				"remote_addr", r.RemoteAddr,
				"query", r.URL.RawQuery,
			)
			return
		}
	}

	// 检查编排层是否正在关闭（在升级连接之前）
	if u.registrar.IsShutdown() {
		u.logger.WarnContextKV(r.Context(), "[WebSocket] 正在关闭，拒绝新连接",
			"client_id", attrs.ClientID,
			"user_id", attrs.UserID,
			"remote_addr", r.RemoteAddr,
		)
		return
	}

	// 配置升级器并设置响应头
	upgrader := u.ConfigureUpgrader()
	responseHeader := http.Header{}

	// 如果启用了响应头配置，则添加服务端信息
	if u.cfg.ResponseHeaders.Enabled {
		respHeaders := u.cfg.ResponseHeaders

		// 服务端分配的客户端ID
		responseHeader.Set(respHeaders.GetClientIDKey(), attrs.ClientID)

		// 服务端节点信息（分布式场景下很有用）
		responseHeader.Set(respHeaders.GetNodeIDKey(), u.nodeID)

		// 添加自定义响应头
		for key, value := range respHeaders.CustomHeaders {
			responseHeader.Set(key, value)
		}
	}

	// 升级连接
	conn, upgradeErr := upgrader.Upgrade(w, r, responseHeader)
	if upgradeErr != nil {
		err = upgradeErr
		return
	}

	// 创建并注册客户端
	client = u.CreateClientFromRequest(r, conn, attrs)
	u.registrar.Register(client)
	success = true

	// 发送客户端注册成功确认消息（如果配置启用）
	if u.cfg.ResponseHeaders.SendRegisteredMessage {
		u.registrar.SendRegisteredMessage(client)
	}
}

// buildWebSocketUpgradeLogFields 构建 WebSocket 升级日志字段
func (u *Upgrader) buildWebSocketUpgradeLogFields(r *http.Request, start time.Time, client *models.Client, success bool) []any {
	logFields := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"query", r.URL.RawQuery,
		"remote_addr", r.RemoteAddr,
		"user_agent", r.Header.Get("User-Agent"),
		"origin", r.Header.Get("Origin"),
		"duration_ms", time.Since(start).Milliseconds(),
		"success", success,
	}

	if client != nil && client.Conn != nil {
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

// handleHealthCheck 处理健康检查请求
// 升级到 WebSocket 连接后根据配置决定是否立即关闭，不创建客户端记录
func (u *Upgrader) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	upgrader := u.ConfigureUpgrader()

	// 设置健康检查响应头（使用配置）
	responseHeader := http.Header{}

	// 升级连接
	conn, err := upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		return
	}

	// 根据配置决定是否立即关闭连接
	if u.cfg.HealthCheck.CloseImmediately {
		defer conn.Close()
	}

	// 根据配置决定是否发送响应消息
	if u.cfg.HealthCheck.SendResponseMessage {
		// 构建健康检查响应消息（使用 HubMessage）
		healthMsg := models.NewHubMessage().
			SetID(u.requestID()).
			SetMessageType(models.MessageTypeHealthCheck).
			SetSender(models.UserTypeSystem.String()).
			SetSenderType(models.UserTypeSystem).
			SetContent("Health check OK").
			WithOption("status", "ok").
			WithOption("node_id", u.nodeID).
			WithOption("timestamp", time.Now().Unix())

		_ = conn.WriteJSON(healthMsg)
	}
}

// requestID 生成请求 ID（未注入生成器时返回空串，健康检查响应即发即弃无消费者依赖 ID）
func (u *Upgrader) requestID() string {
	if u.idGen != nil {
		return u.idGen.GenerateRequestID()
	}
	return ""
}

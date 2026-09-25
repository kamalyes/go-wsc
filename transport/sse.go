/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 09:58:00
 * @FilePath: \go-wsc\transport\sse.go
 * @Description: SSE 接入处理器 —— 客户端构造 / 升级编排 / 写循环 / 协议写
 *
 * 迁移自 hub/sse.go（P2 批1 域化）：SSE 协议能力与写循环归位 transport，
 * 深编排（注册/注销/关闭态）经 Registrar 端口回调；SSE 投递原语
 * （SendToUserViaSSE/BroadcastToSSEClients）属 SSE 协议语义，随本文件留驻
 * （经 ShardedRegistry 只读遍历投递 SSEMessageCh）；纯查询门面（GetSSEClients 等）
 * 由 registry 遍历原语直接组合，不再另设门面
 *
 * SSE 协议：单向服务端→客户端推送，格式 `data: <json>\n\n`，心跳用注释行 `: ping\n\n`
 * 链路：HTTP 请求 → HandleSSEUpgrade（token 解码 + 鉴权）→ CreateSSEClient → 同步注册
 *       → WriteLoop（阻塞消费 SSEMessageCh 写到 ResponseWriter，直到连接断开/编排层关闭）
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/messaging"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// SSE 协议常量（单一公开方法 + 预定义常量，符合项目偏好）
const (
	sseDataPrefix        = "data: "         // SSE 数据行前缀
	sseEventSuffix       = "\n\n"           // SSE 事件结束符
	sseHeartbeatMsg      = ": ping\n\n"     // SSE 心跳注释行（浏览器 EventSource 自动忽略）
	sseHeartbeatFallback = 30 * time.Second // 心跳间隔兜底值
)

// sseBufPool 复用 bytes.Buffer，避免每条消息分配（零依赖 sync.Pool）
var sseBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// SSEHandler SSE 接入处理器
// 职责：SSE 客户端构造（响应头/专用通道）、升级编排（健康检查/鉴权/验证/注册）、
// 写循环（消息事件/心跳写出与 Flush）
type SSEHandler struct {
	cfg       *wscconfig.WSC              // 全量配置
	logger    spi.Logger                  // 日志器（nil 时构造兜底默认实例）
	registrar Registrar                   // 编排层端口（同步注册/注销/关闭态）
	upgrader  *Upgrader                   // 复用属性提取（ExtractClientAttributes，token 解码与明文兜底同一套逻辑）
	registry  *connection.ShardedRegistry // 连接注册表（SSE 投递原语遍历读用）
	hubCtx    context.Context
	nodeID    string

	wg sync.WaitGroup // 在途写循环计数（Wait 供编排层优雅关闭等待）
}

// NewSSEHandler 创建 SSE 接入处理器
// upgrader 复用其属性提取逻辑（SSE 与 WS 共用同一套 token 解码 + 明文兜底），nil 直接 panic
func NewSSEHandler(cfg *wscconfig.WSC, registrar Registrar, upgrader *Upgrader) *SSEHandler {
	if cfg == nil {
		panic("transport: NewSSEHandler 依赖非 nil 配置")
	}
	if upgrader == nil {
		panic("transport: NewSSEHandler 依赖非 nil Upgrader（复用属性提取）")
	}
	return &SSEHandler{
		cfg:       cfg,
		logger:    spi.NewDefaultLogger(),
		registrar: registrar,
		upgrader:  upgrader,
		hubCtx:    context.Background(),
	}
}

// WithLogger 注入日志器
func (h *SSEHandler) WithLogger(logger spi.Logger) *SSEHandler {
	if logger != nil {
		h.logger = logger
	}
	return h
}

// WithHubContext 注入编排层生命周期 ctx（连接级 ctx 的父；默认 Background）
func (h *SSEHandler) WithHubContext(ctx context.Context) *SSEHandler {
	if ctx != nil {
		h.hubCtx = ctx
	}
	return h
}

// WithNodeID 注入节点标识
func (h *SSEHandler) WithNodeID(nodeID string) *SSEHandler {
	h.nodeID = nodeID
	return h
}

// WithShardedRegistry 注入连接注册表（SSE 投递原语遍历读用）
func (h *SSEHandler) WithShardedRegistry(registry *connection.ShardedRegistry) *SSEHandler {
	if registry != nil {
		h.registry = registry
	}
	return h
}

// Wait 等待全部在途写循环退出（编排层优雅关闭时调用；ctx 取消后写循环自行退出，不会死锁）
func (h *SSEHandler) Wait() {
	h.wg.Wait()
}

// ============================================================================
// SSE 客户端构造
// ============================================================================

// CreateSSEClient 构造 SSE 客户端（设置响应头 + 链式构造 Client + 专用通道）
// r 非 nil 时从请求 ctx 提取 trace_id 注入连接级 ctx；r 为 nil 时降级用编排层 ctx
func (h *SSEHandler) CreateSSEClient(r *http.Request, w http.ResponseWriter, attrs *ClientAttributes) (*models.Client, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported: ResponseWriter does not implement http.Flusher")
	}

	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// 构造连接级 ctx（与 Upgrader.CreateClientFromRequest 对齐：派生自编排层 ctx + 注入 trace_id）
	connCtx := context.WithValue(h.hubCtx, messaging.ContextKeySenderID, attrs.UserID)
	if r != nil {
		if traceID := logger.ExtractTraceID(r.Context()); traceID != "" {
			connCtx = logger.ContextWithTraceID(connCtx, traceID)
		}
	}

	// SSE 消息通道容量：优先用 SSEMessageBuffer，未配置时回退 MessageBufferSize
	sseBufCap := mathx.IfLeZero(h.cfg.SSEMessageBuffer, h.cfg.MessageBufferSize)

	// 使用 NewClient 链式构造（与 WS 路径对齐，保证路由隔离/归一化一致）
	client := models.NewClient(attrs.ClientID, attrs.UserID, attrs.UserType).
		WithSSEWriter(w, flusher).
		WithNodeInfo(h.nodeID, h.cfg.NodeIP, h.cfg.NodePort).
		WithAppID(attrs.AppID).
		WithNamespace(attrs.Namespace).
		WithGroupIDs(attrs.GroupIDs).
		WithContext(connCtx).
		WithSSEChannels(make(chan *models.HubMessage, sseBufCap), make(chan struct{}))

	return client, nil
}

// ============================================================================
// SSE 升级编排（网关层 /sse 路由的 HTTP handler）
// ============================================================================

// HandleSSEUpgrade 处理 SSE 升级请求
// 流程复刻 HandleWebSocketUpgrade：健康检查 → token 解码 → 连接验证 → 创建客户端 → 同步注册 → 阻塞在写循环
// 与 WS 不同：不升级协议、不 Hijack，handler 自身阻塞在 WriteLoop 上直到连接结束
func (h *SSEHandler) HandleSSEUpgrade(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()

	var (
		client  *models.Client
		err     error
		success bool
	)
	isHealthCheck := false

	// defer 统一记录日志
	defer func() {
		logFields := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"remote_addr", r.RemoteAddr,
			"user_agent", r.Header.Get("User-Agent"),
			"origin", r.Header.Get("Origin"),
			"duration_ms", time.Since(start).Milliseconds(),
			"success", success,
			"health_check", isHealthCheck,
		}
		if client != nil {
			logFields = append(logFields,
				"client_id", client.ID,
				"user_id", client.UserID,
				"user_type", client.UserType.String(),
			)
		}
		if err != nil {
			h.logger.WithError(err).ErrorContextKV(ctx, "[SSE] 处理失败", logFields...)
		} else {
			h.logger.InfoContextKV(ctx, "[SSE] 处理成功", logFields...)
		}
	}()

	// 1. 健康检查（SSE 不升级协议，直接返回 200 + JSON）
	if h.cfg.HealthCheck.Enabled {
		queryValue := r.URL.Query().Get(h.cfg.HealthCheck.GetQueryParamName())
		if h.cfg.HealthCheck.IsHealthCheckRequest(queryValue) {
			isHealthCheck = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","node_id":"` + h.nodeID + `"}`))
			success = true
			return
		}
	}

	// 2. 提取客户端属性（token 解码 + AllowFallback 降级，与 WS 完全一致）
	attrs := h.upgrader.ExtractClientAttributes(r)
	if attrs == nil {
		// token 解码失败且不允许降级（ExtractClientAttributes 已记录拒绝日志）
		return
	}

	// 3. 连接验证（与 WS 一致）
	if h.cfg.ConnectionValidation.Enabled {
		valid, reason := h.cfg.ConnectionValidation.ValidateConnection(attrs.UserID, attrs.UserType.String())
		if !valid {
			h.logger.WarnContextKV(ctx, "[SSE] 连接被拒绝",
				"reason", reason,
				"remote_addr", r.RemoteAddr,
				"query", r.URL.RawQuery,
			)
			return
		}
	}

	// 4. 编排层关闭中，拒绝新连接
	if h.registrar.IsShutdown() {
		h.logger.WarnContextKV(ctx, "[SSE] 正在关闭，拒绝新连接",
			"client_id", attrs.ClientID,
			"user_id", attrs.UserID,
			"remote_addr", r.RemoteAddr,
		)
		return
	}

	// 5. 创建 SSE 客户端（设置响应头 + 构造 Client）
	client, err = h.CreateSSEClient(r, w, attrs)
	if err != nil {
		h.logger.WarnContextKV(ctx, "[SSE] 创建客户端失败",
			"error", err, "remote_addr", r.RemoteAddr,
		)
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// 6. 清除 http.Server 写超时（关键：避免 WriteTimeout 杀掉 SSE 长连接）
	// http.NewResponseController 支持 ResponseWriter/Flusher 扩展接口，零依赖
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})
	_ = rc.Flush() // 立即发送响应头，让客户端拿到 200 + Content-Type: text/event-stream

	// 7. 同步注册（不 go，确保注册完成才进写循环，避免首条消息竞态被丢）
	h.registrar.RegisterSync(client)
	success = true

	// 8. 阻塞在写循环（handler 不返回，直到连接断开/编排层关闭）
	// 写循环退出后 defer Unregister 兜底清理
	h.WriteLoop(client, r)
}

// ============================================================================
// SSE 写循环（消费 SSEMessageCh → 写 ResponseWriter → Flush）
// ============================================================================

// WriteLoop SSE 写循环（由 HandleSSEUpgrade 同步调用，非独立 goroutine）
// select 五重退出：SSEMessageCh 关闭 / SSECloseCh 关闭 / r.Context().Done() / 编排层 ctx.Done()
// 退出后 defer Unregister 清理（幂等：注销路径已做 removed==nil 早返回）
func (h *SSEHandler) WriteLoop(client *models.Client, r *http.Request) {
	h.wg.Add(1)
	defer h.wg.Done()

	// panic 兜底（防止 goroutine 泄漏）
	defer func() {
		if rv := recover(); rv != nil {
			h.logger.ErrorContextKV(r.Context(), "SSE写循环panic",
				"client_id", client.ID,
				"user_id", client.UserID,
				"panic", rv,
				"stack", string(debug.Stack()),
			)
		}
	}()

	// 退出时确保注销（与 WS 读循环的 defer Unregister 对称）
	defer h.registrar.Unregister(client)

	// 心跳间隔：优先用配置，<=0 时回退 30s
	interval := h.cfg.SSEHeartbeat
	if interval <= 0 {
		interval = sseHeartbeatFallback
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	ctx := r.Context()
	for {
		select {
		case msg, ok := <-client.SSEMessageCh:
			if !ok {
				// 通道关闭（编排层优雅关闭/踢人触发的 closeClientChannel）
				return
			}
			if err := h.writeSSEEvent(client, msg); err != nil {
				h.logger.WarnContextKV(ctx, "SSE写失败，注销客户端",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
				return
			}
			client.SetLastSeen(time.Now())
		case <-ticker.C:
			if err := h.writeSSEHeartbeat(client); err != nil {
				h.logger.WarnContextKV(ctx, "SSE心跳写失败，注销客户端",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
				return
			}
			client.SetLastSeen(time.Now())
		case <-client.SSECloseCh:
			// 主动关闭（如踢人/编排层关闭，closeClientChannel 触发）
			return
		case <-ctx.Done():
			// 客户端断开（http.Server 检测到连接关闭）
			return
		case <-h.hubCtx.Done():
			// 编排层关闭
			return
		}
	}
}

// writeSSEEvent 写一条消息事件（data: <json>\n\n）
// 为防 JSON 含嵌入换行，按 bytes.Split 逐行加 data: 前缀（SSE 协议要求）
func (h *SSEHandler) writeSSEEvent(client *models.Client, msg *models.HubMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		// 单条消息序列化失败不应杀连接，记 WARN 后跳过
		h.logger.WarnContextKV(msg.ContextFrom(h.hubCtx), "SSE消息序列化失败",
			"client_id", client.ID,
			"user_id", client.UserID,
			"message_id", msg.MessageID,
			"error", err,
		)
		return nil
	}

	buf := sseBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer sseBufPool.Put(buf)

	// SSE 协议：多行 data 每行都要加前缀，最后以 \n\n 结束
	lines := bytes.Split(data, []byte("\n"))
	for _, line := range lines {
		buf.WriteString(sseDataPrefix)
		buf.Write(line)
		buf.WriteByte('\n')
	}
	buf.WriteString(sseEventSuffix)

	if _, err := client.SSEWriter.Write(buf.Bytes()); err != nil {
		return err
	}
	client.SSEFlusher.Flush()
	return nil
}

// writeSSEHeartbeat 写心跳注释行（: ping\n\n，浏览器 EventSource 自动忽略）
func (h *SSEHandler) writeSSEHeartbeat(client *models.Client) error {
	if _, err := client.SSEWriter.Write([]byte(sseHeartbeatMsg)); err != nil {
		return err
	}
	client.SSEFlusher.Flush()
	return nil
}

// ============================================================================
// SSE 消息投递原语（messaging 域 send/broadcast 经端口复用，协议语义留驻本域）
// ============================================================================

// SendToUserViaSSE 通过 SSE 发送消息给指定用户（支持多设备，按 namespace 隔离）
// 使用 ForEachSSEUserClient 持读锁零拷贝遍历，替代 GetSSEUserClients 锁外遍历的数据竞争
// 🔏 namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的 SSE 设备，
// 避免同一 userID 跨 ns 串扰
func (h *SSEHandler) SendToUserViaSSE(userID string, msg *models.HubMessage) bool {
	// 快速检查用户是否有 SSE 连接（O(1)）
	if !h.registry.HasSSEUser(userID) {
		h.logger.WarnContextKV(msg.ContextFrom(h.hubCtx), "SSE用户不存在",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
		)
		return false
	}

	// 持读锁零拷贝遍历发送
	successCount := 0
	totalDevices := 0
	h.registry.ForEachSSEUserClient(userID, func(clientID string, client *models.Client) bool {
		// 🔏 namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的设备
		if msg.Namespace != "" && client.Namespace != msg.Namespace {
			return true
		}
		totalDevices++
		select {
		case client.SSEMessageCh <- msg:
			client.SetLastSeen(time.Now())
			successCount++
			h.logger.DebugContextKV(msg.ContextFrom(h.hubCtx), "SSE消息发送",
				"message_id", msg.MessageID,
				"from", msg.Sender,
				"to", userID,
				"client_id", clientID,
				"type", msg.MessageType,
			)
		default:
			// SSE 消息队列满
			h.logger.WarnContextKV(msg.ContextFrom(h.hubCtx), "SSE消息队列已满",
				"user_id", userID,
				"client_id", clientID,
				"message_id", msg.MessageID,
				"message_type", msg.MessageType,
			)
		}
		return true
	})

	if successCount > 0 {
		// 每 SSE 消息必经，与 WS 消息日志同口径降为 Debug
		h.logger.DebugContextKV(msg.ContextFrom(h.hubCtx), "SSE消息发送成功",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
			"success_devices", successCount,
			"total_devices", totalDevices,
		)
		return true
	}

	return false
}

// BroadcastToSSEClients 广播消息到所有 SSE 客户端（按 app/namespace 信封隔离）
// 通过 ForEachSSEClientParallel 并行分片读锁遍历（百万级优化）
// 🔏 appId/namespace 隔离：与 WebSocket 路径保持一致，
// msg.AppID/msg.Namespace 非空时仅投递给同 app/ns 的 SSE 客户端，避免跨应用/租户串扰
func (h *SSEHandler) BroadcastToSSEClients(msg *models.HubMessage) {
	// 🔏 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	// namespace 保持原值（空=全局广播，ClientMatchesEnvelope 跳过 ns 过滤匹配所有）
	msg.InjectRoute(h.hubCtx)

	start := time.Now()
	totalSSEClients := h.registry.GetSSEClientCount()

	var sent, skipped, scanned int64
	h.registry.ForEachSSEClientParallel(0, func(userID, clientID string, client *models.Client) {
		atomic.AddInt64(&scanned, 1)
		if !connection.ClientMatchesEnvelope(client, msg.AppID, msg.Namespace, msg.GroupIDs) {
			return
		}
		select {
		case client.SSEMessageCh <- msg:
			client.SetLastSeen(time.Now())
			atomic.AddInt64(&sent, 1)
		default:
			atomic.AddInt64(&skipped, 1)
			h.logger.WarnContextKV(msg.ContextFrom(h.hubCtx), "SSE客户端消息通道已满，跳过",
				"user_id", userID,
				"client_id", clientID,
				"message_id", msg.MessageID,
			)
		}
	})

	h.logger.DebugContextKV(msg.ContextFrom(h.hubCtx), "SSE广播完成",
		"message_id", msg.MessageID,
		"namespace", msg.Namespace,
		"total_sse_clients", totalSSEClients,
		"scanned", atomic.LoadInt64(&scanned),
		"sent", atomic.LoadInt64(&sent),
		"skipped", atomic.LoadInt64(&skipped),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

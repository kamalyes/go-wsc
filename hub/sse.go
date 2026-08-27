/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:37:26
 * @FilePath: \go-wsc\hub\sse.go
 * @Description: Hub SSE 连接支持（含写循环 + 网关 HTTP handler）
 *
 * SSE 协议：单向服务端→客户端推送，格式 `data: <json>\n\n`，心跳用注释行 `: ping\n\n`
 * 链路：HTTP 请求 → HandleSSEUpgrade（token 解码 + 鉴权）→ createSSEClient → handleRegister（同步）
 *       → handleSSEWriteLoop（阻塞消费 SSEMessageCh 写到 ResponseWriter，直到连接断开/Hub 关闭）
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
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

// ============================================================================
// SSE 注册/注销方法
// ============================================================================

// RegisterSSE 注册SSE连接（向后兼容旧签名）
// 内部构造最小 ClientAttributes，调用 createSSEClient 后异步注册
// 注意：此方法不启动写循环，仅供测试/集成直接使用；
//
//	网关层真实接入应使用 HandleSSEUpgrade（含写循环 + token 鉴权）
func (h *Hub) RegisterSSE(userID string, w http.ResponseWriter, userType UserType) (*Client, error) {
	attrs := &ClientAttributes{
		ClientID: "sse-" + userID + "-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		UserID:   userID,
		UserType: userType,
	}
	client, err := h.createSSEClient(nil, w, attrs)
	if err != nil {
		return nil, err
	}
	// 异步注册（与 WS 连接一致，不经过 EventLoop channel）
	go h.handleRegister(client)

	h.logger.InfoContextKV(client.Context, "SSE连接已创建",
		"user_id", userID,
		"client_id", client.ID,
		"client_type", "sse",
	)
	return client, nil
}

// createSSEClient 构造 SSE 客户端（RegisterSSE 和 HandleSSEUpgrade 共用，避免两套实现）
// r 非 nil 时从请求 ctx 提取 trace_id 注入连接级 ctx；r 为 nil 时降级用 h.ctx
func (h *Hub) createSSEClient(r *http.Request, w http.ResponseWriter, attrs *ClientAttributes) (*Client, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported: ResponseWriter does not implement http.Flusher")
	}

	// 设置 SSE 响应头
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// 构造连接级 ctx（与 CreateClientFromRequest 对齐：派生自 h.ctx + 注入 trace_id）
	connCtx := context.WithValue(h.ctx, ContextKeySenderID, attrs.UserID)
	if r != nil {
		if traceID := logger.ExtractTraceID(r.Context()); traceID != "" {
			connCtx = logger.ContextWithTraceID(connCtx, traceID)
		}
	}

	// SSE 消息通道容量：优先用 SSEMessageBuffer，未配置时回退 MessageBufferSize
	sseBufCap := mathx.IfLeZero(h.config.SSEMessageBuffer, h.config.MessageBufferSize)

	// 使用 NewClient 链式构造（与 CreateClientFromRequest 对齐，保证路由隔离/归一化一致）
	client := NewClient(attrs.ClientID, attrs.UserID, attrs.UserType).
		WithSSEWriter(w, flusher).
		WithNodeInfo(h.nodeID, h.config.NodeIP, h.config.NodePort).
		WithAppID(attrs.AppID).
		WithNamespace(attrs.Namespace).
		WithGroupIDs(attrs.GroupIDs).
		WithContext(connCtx)
	// SSE 专用通道
	client.SSEMessageCh = make(chan *HubMessage, sseBufCap)
	client.SSECloseCh = make(chan struct{})

	return client, nil
}

// UnregisterSSE 注销SSE连接
func (h *Hub) UnregisterSSE(clientID string) {
	client, exists := h.shardedRegistry.GetClient(clientID)
	if exists && client.ConnectionType == ConnectionTypeSSE {
		go h.handleUnregister(client)
		h.logger.InfoContextKV(client.Context, "SSE连接已注销",
			"user_id", client.UserID,
			"client_id", clientID,
		)
	}
}

// ============================================================================
// SSE HTTP 网关入口（网关层注册的路由 handler）
// ============================================================================

// HandleSSEUpgrade 处理 SSE 升级请求（网关层 /sse 路由的 HTTP handler）
// 流程复刻 HandleWebSocketUpgrade：健康检查 → token 解码 → 连接验证 → 创建客户端 → 同步注册 → 阻塞在写循环
// 与 WS 不同：不升级协议、不 Hijack，handler 自身阻塞在 handleSSEWriteLoop 上直到连接结束
func (h *Hub) HandleSSEUpgrade(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	ctx := r.Context()

	var (
		client  *Client
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
	if h.config.HealthCheck.Enabled {
		queryValue := r.URL.Query().Get(h.config.HealthCheck.GetQueryParamName())
		if h.config.HealthCheck.IsHealthCheckRequest(queryValue) {
			isHealthCheck = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok","node_id":"` + h.nodeID + `"}`))
			success = true
			return
		}
	}

	// 2. 提取客户端属性（token 解码 + AllowFallback 降级，与 WS 完全一致）
	attrs := h.extractClientAttributes(r)
	if attrs == nil {
		// token 解码失败且不允许降级（extractClientAttributes 已记录拒绝日志）
		return
	}

	// 3. 连接验证（与 WS 一致）
	if h.config.ConnectionValidation.Enabled {
		valid, reason := h.config.ConnectionValidation.ValidateConnection(attrs.UserID, attrs.UserType.String())
		if !valid {
			h.logger.WarnContextKV(ctx, "[SSE] 连接被拒绝",
				"reason", reason,
				"remote_addr", r.RemoteAddr,
				"query", r.URL.RawQuery,
			)
			return
		}
	}

	// 4. Hub 关闭中，拒绝新连接
	if h.shutdown.Load() {
		h.logger.WarnContextKV(ctx, "[SSE] Hub 正在关闭，拒绝新连接",
			"client_id", attrs.ClientID,
			"user_id", attrs.UserID,
			"remote_addr", r.RemoteAddr,
		)
		return
	}

	// 5. 创建 SSE 客户端（设置响应头 + 构造 Client）
	client, err = h.createSSEClient(r, w, attrs)
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
	h.handleRegister(client)
	success = true

	// 8. 阻塞在写循环（handler 不返回，直到连接断开/Hub 关闭）
	// 写循环退出后 defer Unregister 兜底清理
	h.handleSSEWriteLoop(client, r)
}

// ============================================================================
// SSE 写循环（消费 SSEMessageCh → 写 ResponseWriter → Flush）
// ============================================================================

// handleSSEWriteLoop SSE 写循环 goroutine（由 HandleSSEUpgrade 同步调用，非独立 goroutine）
// select 五重退出：SSEMessageCh 关闭 / SSECloseCh 关闭 / r.Context().Done() / h.ctx.Done()
// 退出后 defer Unregister 清理（幂等：removeClientUnsafe 已做 removed==nil 早返回）
func (h *Hub) handleSSEWriteLoop(client *Client, r *http.Request) {
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

	// 退出时确保从注册表移除（与 WS handleClientRead 的 defer Unregister 对称）
	defer h.Unregister(client)

	// 心跳间隔：优先用配置，<=0 时回退 30s
	interval := h.config.SSEHeartbeat
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
				// closeClientChannel 关闭了通道（Hub 优雅关闭/踢人）
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
			// 主动关闭（如踢人/Hub关闭，closeClientChannel 触发）
			return
		case <-ctx.Done():
			// 客户端断开（http.Server 检测到连接关闭）
			return
		case <-h.ctx.Done():
			// Hub 关闭
			return
		}
	}
}

// writeSSEEvent 写一条消息事件（data: <json>\n\n）
// 为防 JSON 含嵌入换行，按 bytes.Split 逐行加 data: 前缀（SSE 协议要求）
func (h *Hub) writeSSEEvent(client *Client, msg *HubMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		// 单条消息序列化失败不应杀连接，记 WARN 后跳过
		h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "SSE消息序列化失败",
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
func (h *Hub) writeSSEHeartbeat(client *Client) error {
	if _, err := client.SSEWriter.Write([]byte(sseHeartbeatMsg)); err != nil {
		return err
	}
	client.SSEFlusher.Flush()
	return nil
}

// ============================================================================
// SSE 消息发送方法
// ============================================================================

// SendToUserViaSSE 通过SSE发送消息给指定用户（支持多设备，按 namespace 隔离）
// 使用 ForEachSSEUserClient 持读锁零拷贝遍历，替代 GetSSEUserClients 锁外遍历的数据竞争
// 🔏 namespace 隔离：与 ForEachUserClientFiltered 保持一致，
// msg.Namespace 非空时仅投递给同 ns 的 SSE 设备，避免同一 userID 跨 ns 串扰
func (h *Hub) SendToUserViaSSE(userID string, msg *HubMessage) bool {
	// 快速检查用户是否有 SSE 连接（O(1)）
	if !h.shardedRegistry.HasSSEUser(userID) {
		h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "SSE用户不存在",
			"user_id", userID,
			"message_id", msg.MessageID,
			"message_type", msg.MessageType,
		)
		return false
	}

	// 持读锁零拷贝遍历发送
	successCount := 0
	totalDevices := 0
	h.shardedRegistry.ForEachSSEUserClient(userID, func(clientID string, client *Client) bool {
		// 🔏 namespace 隔离：msg.Namespace 非空时仅投递给同 ns 的设备
		if msg.Namespace != "" && client.Namespace != msg.Namespace {
			return true
		}
		totalDevices++
		select {
		case client.SSEMessageCh <- msg:
			client.SetLastSeen(time.Now())
			successCount++
			h.logger.DebugContextKV(msg.ContextFrom(h.ctx), "SSE消息发送",
				"message_id", msg.MessageID,
				"from", msg.Sender,
				"to", userID,
				"client_id", clientID,
				"type", msg.MessageType,
			)
		default:
			// SSE消息队列满
			h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "SSE消息队列已满",
				"user_id", userID,
				"client_id", clientID,
				"message_id", msg.MessageID,
				"message_type", msg.MessageType,
			)
		}
		return true
	})

	if successCount > 0 {
		h.logger.InfoContextKV(msg.ContextFrom(h.ctx), "SSE消息发送成功",
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

// broadcastToSSEClients 广播消息到所有SSE客户端（按 namespace 隔离）
// 通过 shardedRegistry.ForEachSSEClientParallel 并行分片读锁遍历（百万级优化）
// 🔏 appId/namespace 隔离：与 WebSocket 路径 ForEachClientFilteredParallel 保持一致，
// msg.AppID/msg.Namespace 非空时仅投递给同 app/ns 的 SSE 客户端，避免跨应用/租户串扰
func (h *Hub) broadcastToSSEClients(msg *HubMessage) {
	// 🔏 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	// namespace 保持原值（空=全局广播，ClientMatchesEnvelope 跳过 ns 过滤匹配所有）
	msg.InjectRoute(h.ctx)

	start := time.Now()
	totalSSEClients := h.shardedRegistry.GetSSEClientCount()

	var sent, skipped, scanned int64
	h.shardedRegistry.ForEachSSEClientParallel(0, func(userID, clientID string, client *Client) {
		atomic.AddInt64(&scanned, 1)
		if !ClientMatchesEnvelope(client, msg.AppID, msg.Namespace, msg.GroupIDs) {
			return
		}
		select {
		case client.SSEMessageCh <- msg:
			client.SetLastSeen(time.Now())
			atomic.AddInt64(&sent, 1)
		default:
			atomic.AddInt64(&skipped, 1)
			h.logger.WarnContextKV(msg.ContextFrom(h.ctx), "SSE客户端消息通道已满，跳过",
				"user_id", userID,
				"client_id", clientID,
				"message_id", msg.MessageID,
			)
		}
	})

	h.logger.DebugContextKV(msg.ContextFrom(h.ctx), "📡 SSE广播完成",
		"message_id", msg.MessageID,
		"namespace", msg.Namespace,
		"total_sse_clients", totalSSEClients,
		"scanned", atomic.LoadInt64(&scanned),
		"sent", atomic.LoadInt64(&sent),
		"skipped", atomic.LoadInt64(&skipped),
		"duration_ms", time.Since(start).Milliseconds(),
	)
}

// ============================================================================
// SSE 查询方法
// ============================================================================

// GetSSEClientCount 获取SSE客户端数量（原子计数器，零锁开销）
func (h *Hub) GetSSEClientCount() int {
	return int(h.shardedRegistry.GetSSEClientCount())
}

// GetSSEClients 获取所有SSE客户端列表
// 通过 shardedRegistry.ForEachSSEClient 收集（分片读锁粒度细）
func (h *Hub) GetSSEClients() []*Client {
	clients := make([]*Client, 0)
	h.shardedRegistry.ForEachSSEClient(func(_, _ string, client *Client) bool {
		clients = append(clients, client)
		return true
	})
	return clients
}

// IsSSEClientOnline 检查SSE客户端是否在线 - O(1)
func (h *Hub) IsSSEClientOnline(userID string) bool {
	return h.shardedRegistry.HasSSEUser(userID)
}

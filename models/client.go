/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 12:20:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 12:56:15
 * @FilePath: \go-wsc\models\client.go
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

// 默认 overflow buffer 容量
const DefaultOverflowBufferSize = 64

// Client 客户端连接（统一管理 WebSocket 和 SSE 连接）
type Client struct {
	ID             string                 `json:"id"`              // 客户端ID
	UserID         string                 `json:"user_id"`         // 用户ID
	UserType       UserType               `json:"user_type"`       // 用户类型
	VIPLevel       VIPLevel               `json:"vip_level"`       // VIP等级
	Role           UserRole               `json:"role"`            // 用户角色
	ClientIP       string                 `json:"client_ip"`       // 客户端IP
	Conn           *websocket.Conn        `json:"-"`               // WebSocket连接（不序列化，仅WS使用）
	ConnectedAt    time.Time              `json:"connected_at"`    // 连接时间
	LastSeen       time.Time              `json:"last_seen"`       // 最后活跃时间
	LastHeartbeat  time.Time              `json:"last_heartbeat"`  // 最后心跳时间
	LastPong       time.Time              `json:"last_pong"`       // 最后心跳响应时间
	Status         UserStatus             `json:"status"`          // 用户状态
	Department     Department             `json:"department"`      // 部门
	Skills         []Skill                `json:"skills"`          // 技能列表
	MaxTickets     int                    `json:"max_tickets"`     // 最大工单数
	NodeID         string                 `json:"node_id"`         // 所在节点ID
	NodeIP         string                 `json:"node_ip"`         // 所在节点IP
	NodePort       int                    `json:"node_port"`       // 所在节点端口
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

	// Overflow buffer：当 SendChan 满时暂存消息，drain goroutine 异步回灌
	overflowMu      sync.Mutex    `json:"-"`
	overflowBuf     [][]byte      `json:"-"` // WebSocket overflow 缓冲
	overflowDrainCh chan struct{} `json:"-"` // 通知 drain goroutine 有新数据
	overflowStopCh  chan struct{} `json:"-"` // 停止 drain goroutine
	overflowStarted atomic.Bool   `json:"-"` // drain goroutine 是否已启动

	// SSE overflow buffer
	sseOverflowMu      sync.Mutex    `json:"-"`
	sseOverflowBuf     []*HubMessage `json:"-"` // SSE overflow 缓冲
	sseOverflowDrainCh chan struct{} `json:"-"`
	sseOverflowStopCh  chan struct{} `json:"-"`
	sseOverflowStarted atomic.Bool   `json:"-"`

	// Overflow 容量（0 表示不启用 overflow）
	OverflowBufferSize int `json:"-"`

	// 心跳重连：已关闭客户端收到心跳的累计次数，超过阈值触发重连
	closedHeartbeatCount atomic.Int32 `json:"-"`
	reconnecting         atomic.Bool  `json:"-"` // 防重：重连进行中标记

	// Pong 失败计数：心跳 pong 响应连续失败次数，超过阈值注销客户端
	pongFailCount atomic.Int32 `json:"-"`
}

// NewClient 创建新的客户端实例
func NewClient(id, userID string, userType UserType) *Client {
	now := time.Now()
	return &Client{
		ID:            id,
		UserID:        userID,
		UserType:      userType,
		ConnectedAt:   now,
		LastSeen:      now,
		LastHeartbeat: now,
		LastPong:      now,
		Status:        UserStatusOnline,
		Metadata:      make(map[string]interface{}),
		Context:       context.Background(),
	}
}

// WithVIPLevel 设置VIP等级
func (c *Client) WithVIPLevel(level VIPLevel) *Client {
	c.VIPLevel = level
	return c
}

// WithRole 设置用户角色
func (c *Client) WithRole(role UserRole) *Client {
	c.Role = role
	return c
}

// WithClientIP 设置客户端IP
func (c *Client) WithClientIP(ip string) *Client {
	c.ClientIP = ip
	return c
}

// WithWebSocketConn 设置WebSocket连接
func (c *Client) WithWebSocketConn(conn *websocket.Conn) *Client {
	c.Conn = conn
	c.ConnectionType = ConnectionTypeWebSocket
	return c
}

// WithSSEWriter 设置SSE Writer
func (c *Client) WithSSEWriter(w http.ResponseWriter, flusher http.Flusher) *Client {
	c.SSEWriter = w
	c.SSEFlusher = flusher
	c.ConnectionType = ConnectionTypeSSE
	return c
}

// WithStatus 设置用户状态
func (c *Client) WithStatus(status UserStatus) *Client {
	c.Status = status
	return c
}

// WithDepartment 设置部门
func (c *Client) WithDepartment(dept Department) *Client {
	c.Department = dept
	return c
}

// WithSkills 设置技能列表
func (c *Client) WithSkills(skills []Skill) *Client {
	c.Skills = skills
	return c
}

// WithMaxTickets 设置最大工单数
func (c *Client) WithMaxTickets(max int) *Client {
	c.MaxTickets = max
	return c
}

// WithNodeInfo 设置节点信息
func (c *Client) WithNodeInfo(nodeID, nodeIP string, nodePort int) *Client {
	c.NodeID = nodeID
	c.NodeIP = nodeIP
	c.NodePort = nodePort
	return c
}

// WithClientType 设置客户端类型
func (c *Client) WithClientType(clientType ClientType) *Client {
	c.ClientType = clientType
	return c
}

// WithMetadata 设置元数据
func (c *Client) WithMetadata(key string, value interface{}) *Client {
	if c.Metadata == nil {
		c.Metadata = make(map[string]interface{})
	}
	c.Metadata[key] = value
	return c
}

// WithMetadataMap 批量设置元数据
func (c *Client) WithMetadataMap(metadata map[string]interface{}) *Client {
	if c.Metadata == nil {
		c.Metadata = make(map[string]interface{})
	}
	for k, v := range metadata {
		c.Metadata[k] = v
	}
	return c
}

// WithSendChan 设置发送通道
func (c *Client) WithSendChan(ch chan []byte) *Client {
	c.SendChan = ch
	return c
}

// WithSSEChannels 设置SSE通道
func (c *Client) WithSSEChannels(messageCh chan *HubMessage, closeCh chan struct{}) *Client {
	c.SSEMessageCh = messageCh
	c.SSECloseCh = closeCh
	return c
}

// WithContext 设置上下文
func (c *Client) WithContext(ctx context.Context) *Client {
	c.Context = ctx
	return c
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

// Reopen 重新打开客户端（用于心跳重连恢复），将 closed 状态重置为 false
func (c *Client) Reopen() {
	c.closed.Store(false)
}

// IncrClosedHeartbeatCount 已关闭客户端收到心跳时递增计数，返回递增后的值
func (c *Client) IncrClosedHeartbeatCount() int32 {
	return c.closedHeartbeatCount.Add(1)
}

// ResetClosedHeartbeatCount 重置已关闭客户端的心跳计数
func (c *Client) ResetClosedHeartbeatCount() {
	c.closedHeartbeatCount.Store(0)
}

// GetClosedHeartbeatCount 获取已关闭客户端的心跳计数
func (c *Client) GetClosedHeartbeatCount() int32 {
	return c.closedHeartbeatCount.Load()
}

// IncrPongFailCount 递增 pong 失败计数，返回递增后的值
func (c *Client) IncrPongFailCount() int32 {
	return c.pongFailCount.Add(1)
}

// ResetPongFailCount 重置 pong 失败计数
func (c *Client) ResetPongFailCount() {
	c.pongFailCount.Store(0)
}

// GetPongFailCount 获取 pong 失败计数
func (c *Client) GetPongFailCount() int32 {
	return c.pongFailCount.Load()
}

// TryStartReconnect 尝试进入重连状态（防重），成功返回 true
func (c *Client) TryStartReconnect() bool {
	return c.reconnecting.CompareAndSwap(false, true)
}

// FinishReconnect 结束重连状态
func (c *Client) FinishReconnect() {
	c.reconnecting.Store(false)
}

// IsReconnecting 是否正在重连中
func (c *Client) IsReconnecting() bool {
	return c.reconnecting.Load()
}

// SendChanRef 在 CloseMu 保护下返回当前 SendChan 引用
// 用于写协程启动时快照，或与 closeClientChannel / releaseClientSendChan 并发时安全读取
func (c *Client) SendChanRef() chan []byte {
	c.CloseMu.Lock()
	ch := c.SendChan
	c.CloseMu.Unlock()
	return ch
}

// DrainSendChan 消费 SendChan 直至关闭；handler 为 nil 时仅丢弃消息
func (c *Client) DrainSendChan(handler func([]byte)) {
	ch := c.SendChanRef()
	if ch == nil {
		return
	}
	for data := range ch {
		if handler != nil {
			handler(data)
		}
	}
}

// DrainSendChanBuffer 非阻塞清空 SendChan 中已缓冲的消息
func (c *Client) DrainSendChanBuffer() {
	ch := c.SendChanRef()
	if ch == nil {
		return
	}
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// WatchSendChan 在 SendChan 快照上监听消息，直到 channel 关闭或 stop 信号触发
func (c *Client) WatchSendChan(stop <-chan struct{}, handler func()) {
	ch := c.SendChanRef()
	if ch == nil {
		return
	}
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
			if handler != nil {
				handler()
			}
		case <-stop:
			return
		}
	}
}

// TrySend 尝试向客户端发送数据（WebSocket），非阻塞写入 SendChan
// 如果已关闭、SendChan 为 nil 或通道已满，返回 false
// overflow 逻辑由 Hub 层管理
func (c *Client) TrySend(data []byte) bool {
	c.CloseMu.Lock()
	defer c.CloseMu.Unlock()

	if c.IsClosed() || c.SendChan == nil {
		return false
	}

	// 使用 defer recover 捕获可能的 send on closed channel panic
	defer func() {
		if r := recover(); r != nil {
			c.closed.Store(true)
		}
	}()

	select {
	case c.SendChan <- data:
		return true
	default:
		return false
	}
}

// TrySendSSE 尝试向SSE客户端发送消息，非阻塞写入 SSEMessageCh
// 如果已关闭、SSEMessageCh 为 nil 或通道已满，返回 false
// overflow 逻辑由 Hub 层管理
func (c *Client) TrySendSSE(msg *HubMessage) bool {
	c.CloseMu.Lock()
	defer c.CloseMu.Unlock()

	if c.IsClosed() || c.SSEMessageCh == nil {
		return false
	}

	// 使用 defer recover 捕获可能的 send on closed channel panic
	defer func() {
		if r := recover(); r != nil {
			c.closed.Store(true)
		}
	}()

	select {
	case c.SSEMessageCh <- msg:
		return true
	default:
		return false
	}
}

// StopOverflowDrain 停止 overflow drain goroutine
func (c *Client) StopOverflowDrain() {
	if c.overflowStarted.CompareAndSwap(true, false) {
		close(c.overflowStopCh)
	}
}

// OverflowLen 返回 overflow buffer 中的消息数量
func (c *Client) OverflowLen() int {
	c.overflowMu.Lock()
	defer c.overflowMu.Unlock()
	return len(c.overflowBuf)
}

// StopSSEOverflowDrain 停止 SSE overflow drain goroutine
func (c *Client) StopSSEOverflowDrain() {
	if c.sseOverflowStarted.CompareAndSwap(true, false) {
		close(c.sseOverflowStopCh)
	}
}

// SSEOverflowLen 返回 SSE overflow buffer 中的消息数量
func (c *Client) SSEOverflowLen() int {
	c.sseOverflowMu.Lock()
	defer c.sseOverflowMu.Unlock()
	return len(c.sseOverflowBuf)
}

// ============================================================================
// Overflow 字段访问方法（供 Hub 层管理 overflow 逻辑使用）
// ============================================================================

// --- WebSocket overflow ---

// OverflowMu 返回 overflow 互斥锁
func (c *Client) OverflowMu() *sync.Mutex { return &c.overflowMu }

// OverflowBuf 返回 overflow 缓冲切片
func (c *Client) OverflowBuf() [][]byte { return c.overflowBuf }

// SetOverflowBuf 设置 overflow 缓冲切片
func (c *Client) SetOverflowBuf(buf [][]byte) { c.overflowBuf = buf }

// OverflowDrainCh 返回 overflow drain 通知通道
func (c *Client) OverflowDrainCh() chan struct{} { return c.overflowDrainCh }

// SetOverflowDrainCh 设置 overflow drain 通知通道
func (c *Client) SetOverflowDrainCh(ch chan struct{}) { c.overflowDrainCh = ch }

// OverflowStopCh 返回 overflow drain 停止通道
func (c *Client) OverflowStopCh() chan struct{} { return c.overflowStopCh }

// SetOverflowStopCh 设置 overflow drain 停止通道
func (c *Client) SetOverflowStopCh(ch chan struct{}) { c.overflowStopCh = ch }

// OverflowStarted 返回 overflow drain 是否已启动
func (c *Client) OverflowStarted() *atomic.Bool { return &c.overflowStarted }

// --- SSE overflow ---

// SSEOverflowMu 返回 SSE overflow 互斥锁
func (c *Client) SSEOverflowMu() *sync.Mutex { return &c.sseOverflowMu }

// SSEOverflowBuf 返回 SSE overflow 缓冲切片
func (c *Client) SSEOverflowBuf() []*HubMessage { return c.sseOverflowBuf }

// SetSSEOverflowBuf 设置 SSE overflow 缓冲切片
func (c *Client) SetSSEOverflowBuf(buf []*HubMessage) { c.sseOverflowBuf = buf }

// SSEOverflowDrainCh 返回 SSE overflow drain 通知通道
func (c *Client) SSEOverflowDrainCh() chan struct{} { return c.sseOverflowDrainCh }

// SetSSEOverflowDrainCh 设置 SSE overflow drain 通知通道
func (c *Client) SetSSEOverflowDrainCh(ch chan struct{}) { c.sseOverflowDrainCh = ch }

// SSEOverflowStopCh 返回 SSE overflow drain 停止通道
func (c *Client) SSEOverflowStopCh() chan struct{} { return c.sseOverflowStopCh }

// SetSSEOverflowStopCh 设置 SSE overflow drain 停止通道
func (c *Client) SetSSEOverflowStopCh(ch chan struct{}) { c.sseOverflowStopCh = ch }

// SSEOverflowStarted 返回 SSE overflow drain 是否已启动
func (c *Client) SSEOverflowStarted() *atomic.Bool { return &c.sseOverflowStarted }

// ============================================================================
// WebSocket Close Code 配置
// ============================================================================

// WsCloseCodeMap WebSocket 关闭码映射表 (RFC 6455, section 11.7)
var WsCloseCodeMap = map[int]struct {
	IsNormal bool   // 是否正常关闭
	Desc     string // 描述
}{
	// 正常关闭
	websocket.CloseNormalClosure: {IsNormal: true, Desc: "正常关闭"},
	websocket.CloseGoingAway:     {IsNormal: true, Desc: "客户端离开（关闭标签页/浏览器）"},

	// 协议/数据错误
	websocket.CloseProtocolError:           {IsNormal: false, Desc: "协议错误"},
	websocket.CloseUnsupportedData:         {IsNormal: false, Desc: "不支持的数据类型"},
	websocket.CloseNoStatusReceived:        {IsNormal: false, Desc: "未收到状态码"},
	websocket.CloseInvalidFramePayloadData: {IsNormal: false, Desc: "无效的帧数据"},

	// 策略/配置错误
	websocket.ClosePolicyViolation:    {IsNormal: false, Desc: "策略违规"},
	websocket.CloseMessageTooBig:      {IsNormal: false, Desc: "消息过大"},
	websocket.CloseMandatoryExtension: {IsNormal: false, Desc: "强制扩展未协商"},

	// 服务器错误
	websocket.CloseInternalServerErr: {IsNormal: false, Desc: "服务器内部错误"},
	websocket.CloseServiceRestart:    {IsNormal: false, Desc: "服务重启"},
	websocket.CloseTryAgainLater:     {IsNormal: false, Desc: "稍后重试"},

	// 连接/网络错误
	websocket.CloseAbnormalClosure: {IsNormal: false, Desc: "异常关闭（网络中断/连接丢失）"},
	websocket.CloseTLSHandshake:    {IsNormal: false, Desc: "TLS握手失败"},
}

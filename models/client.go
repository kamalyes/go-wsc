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

// Client 客户端连接（统一管理 WebSocket 和 SSE 连接）
type Client struct {
	ID             string                 `json:"id"`                 // 客户端ID
	UserID         string                 `json:"user_id"`            // 用户ID
	Namespace      string                 `json:"namespace"`          // 命名空间ID（默认 "default"，类似 k8s namespace，用于观察者过滤与命名空间隔离）
	GroupID        string                 `json:"group_id,omitempty"` // 默认群组ID（普通用户=连接后自动加入；观察者=订阅的观察群组，空=观察整个命名空间）
	UserType       UserType               `json:"user_type"`          // 用户类型
	VIPLevel       VIPLevel               `json:"vip_level"`          // VIP等级
	Role           UserRole               `json:"role"`               // 用户角色
	ClientIP       string                 `json:"client_ip"`          // 客户端IP
	Conn           *websocket.Conn        `json:"-"`                  // WebSocket连接（不序列化，仅WS使用）
	ConnectedAt    time.Time              `json:"connected_at"`       // 连接时间
	LastSeen       time.Time              `json:"last_seen"`          // 最后活跃时间
	LastHeartbeat  time.Time              `json:"last_heartbeat"`     // 最后心跳时间
	LastPong       time.Time              `json:"last_pong"`          // 最后心跳响应时间
	Status         UserStatus             `json:"status"`             // 用户状态
	Department     Department             `json:"department"`         // 部门
	Skills         []Skill                `json:"skills"`             // 技能列表
	MaxTickets     int                    `json:"max_tickets"`        // 最大工单数
	NodeID         string                 `json:"node_id"`            // 所在节点ID
	NodeIP         string                 `json:"node_ip"`            // 所在节点IP
	NodePort       int                    `json:"node_port"`          // 所在节点端口
	ClientType     ClientType             `json:"client_type"`        // 客户端类型（web/mobile/desktop）
	ConnectionType ConnectionType         `json:"connection_type"`    // 连接类型（websocket/sse）
	Metadata       map[string]interface{} `json:"metadata"`           // 元数据
	SendChan       chan []byte            `json:"-"`                  // 发送通道（不序列化，仅WS使用）
	Context        context.Context        `json:"-"`                  // 上下文（不序列化）
	closed         atomic.Bool            `json:"-"`                  // channel关闭标志（不序列化）
	CloseMu        sync.Mutex             `json:"-"`                  // 保护channel关闭的互斥锁（不序列化）

	// 原子时间戳（消除 LastHeartbeat/LastSeen/LastPong 的并发读写数据竞争）
	// 与 time.Time 字段同步更新，并发读通过原子读获取
	lastHeartbeatUnix atomic.Int64 `json:"-"` // LastHeartbeat 的 UnixNano 原子镜像
	lastSeenUnix      atomic.Int64 `json:"-"` // LastSeen 的 UnixNano 原子镜像
	lastPongUnix      atomic.Int64 `json:"-"` // LastPong 的 UnixNano 原子镜像

	// VIPLevel 的原子镜像（消除 UpgradeVIPLevel 并发写与广播遍历并发读的数据竞争）
	vipLevelVal atomic.Int32 `json:"-"`

	// Status 的原子镜像（消除 ResetClientStatus 并发写与 GetUserStatus 并发读的数据竞争）
	statusVal atomic.Int32 `json:"-"`

	// Metadata 互斥锁（保护 map[string]interface{} 的并发读写）
	metadataMu sync.RWMutex `json:"-"`

	// SSE 专用字段（仅当 ConnectionType 为 SSE 时使用）
	SSEWriter    http.ResponseWriter `json:"-"` // SSE Writer（不序列化）
	SSEFlusher   http.Flusher        `json:"-"` // SSE Flusher（不序列化）
	SSEMessageCh chan *HubMessage    `json:"-"` // SSE 消息通道（不序列化）
	SSECloseCh   chan struct{}       `json:"-"` // SSE 关闭通道（不序列化）
}

// NewClient 创建新的客户端实例
func NewClient(id, userID string, userType UserType) *Client {
	now := time.Now()
	unix := now.UnixNano()
	c := &Client{
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
	c.lastHeartbeatUnix.Store(unix)
	c.lastSeenUnix.Store(unix)
	c.lastPongUnix.Store(unix)
	c.vipLevelVal.Store(int32(c.VIPLevel.GetLevel()))
	c.statusVal.Store(int32(c.Status.ToInt()))
	return c
}

// SetStatus 原子更新用户状态（同时更新 Status 字段和原子镜像，并发安全）
func (c *Client) SetStatus(status UserStatus) {
	c.Status = status
	c.statusVal.Store(int32(status.ToInt()))
}

// GetStatus 原子读用户状态（并发安全，广播遍历中使用）
func (c *Client) GetStatus() UserStatus {
	return UserStatusFromInt(int(c.statusVal.Load()))
}

// GetMetadataValue 线程安全地读取元数据值
func (c *Client) GetMetadataValue(key string) (interface{}, bool) {
	c.metadataMu.RLock()
	defer c.metadataMu.RUnlock()
	if c.Metadata == nil {
		return nil, false
	}
	val, ok := c.Metadata[key]
	return val, ok
}

// SetMetadataValue 线程安全地写入元数据值
func (c *Client) SetMetadataValue(key string, value interface{}) {
	c.metadataMu.Lock()
	defer c.metadataMu.Unlock()
	if c.Metadata == nil {
		c.Metadata = make(map[string]interface{})
	}
	c.Metadata[key] = value
}

// GetMetadataSnapshot 线程安全地获取元数据的只读副本（用于序列化/日志）
func (c *Client) GetMetadataSnapshot() map[string]interface{} {
	c.metadataMu.RLock()
	defer c.metadataMu.RUnlock()
	if c.Metadata == nil {
		return nil
	}
	snapshot := make(map[string]interface{}, len(c.Metadata))
	for k, v := range c.Metadata {
		snapshot[k] = v
	}
	return snapshot
}

// SetLastHeartbeat 原子更新最后心跳时间（仅写原子镜像，消除 time.Time 并发写数据竞争）
func (c *Client) SetLastHeartbeat(t time.Time) {
	c.lastHeartbeatUnix.Store(t.UnixNano())
}

// SetLastSeen 原子更新最后活跃时间（仅写原子镜像，消除 time.Time 并发写数据竞争）
func (c *Client) SetLastSeen(t time.Time) {
	c.lastSeenUnix.Store(t.UnixNano())
}

// SetLastPong 原子更新最后 Pong 响应时间（仅写原子镜像，消除 time.Time 并发写数据竞争）
func (c *Client) SetLastPong(t time.Time) {
	c.lastPongUnix.Store(t.UnixNano())
}

// GetLastHeartbeat 原子读最后心跳时间（并发安全）
func (c *Client) GetLastHeartbeat() time.Time {
	nano := c.lastHeartbeatUnix.Load()
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// GetLastSeen 原子读最后活跃时间（并发安全）
func (c *Client) GetLastSeen() time.Time {
	nano := c.lastSeenUnix.Load()
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// GetLastPong 原子读最后 Pong 响应时间（并发安全）
func (c *Client) GetLastPong() time.Time {
	nano := c.lastPongUnix.Load()
	if nano == 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}

// WithVIPLevel 设置VIP等级（仅用于构造期，并发场景使用 SetVIPLevel）
func (c *Client) WithVIPLevel(level VIPLevel) *Client {
	c.VIPLevel = level
	c.vipLevelVal.Store(int32(level.GetLevel()))
	return c
}

// SetVIPLevel 原子更新VIP等级（同时更新 VIPLevel 字段和原子镜像，并发安全）
func (c *Client) SetVIPLevel(level VIPLevel) {
	c.VIPLevel = level
	c.vipLevelVal.Store(int32(level.GetLevel()))
}

// GetVIPLevel 原子读VIP等级（并发安全，广播遍历中使用）
func (c *Client) GetVIPLevel() VIPLevel {
	return VIPLevelFromLevel(int(c.vipLevelVal.Load()))
}

// WithNamespace 设置命名空间ID
func (c *Client) WithNamespace(namespace string) *Client {
	c.Namespace = namespace
	return c
}

// GetNamespace 获取命名空间ID，空值返回默认命名空间
func (c *Client) GetNamespace() string {
	if c.Namespace == "" {
		return DefaultNamespace
	}
	return c.Namespace
}

// WithGroupID 设置群组ID
// 普通用户：连接后自动加入该群组；观察者：只接收指定命名空间+群组的消息通知
func (c *Client) WithGroupID(groupID string) *Client {
	c.GroupID = groupID
	return c
}

// SetGroupID 并发安全更新群组ID（用于 MoveGroupMember 等运行时变更场景）
func (c *Client) SetGroupID(groupID string) {
	c.metadataMu.Lock()
	c.GroupID = groupID
	c.metadataMu.Unlock()
}

// GetGroupIDRaw 并发安全获取原始群组ID（不加默认值，用于索引判断）
func (c *Client) GetGroupIDRaw() string {
	c.metadataMu.RLock()
	defer c.metadataMu.RUnlock()
	return c.GroupID
}

// GetGroupID 获取群组ID，空值返回默认群组ID
func (c *Client) GetGroupID() string {
	if gid := c.GetGroupIDRaw(); gid != "" {
		return gid
	}
	return DefaultGroupID
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

	// 3. 从Metadata中获取（线程安全，使用快照避免并发读写）
	metadata := c.GetMetadataSnapshot()
	if metadata != nil {
		if ip, ok := metadata["client_ip"].(string); ok && ip != "" {
			return ip
		}
		if ip, ok := metadata["x-forwarded-for"].(string); ok && ip != "" {
			// X-Forwarded-For 可能包含多个IP，取第一个
			if parts := strings.Split(ip, ","); len(parts) > 0 {
				return strings.TrimSpace(parts[0])
			}
		}
		if ip, ok := metadata["x-real-ip"].(string); ok && ip != "" {
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
	// 从 Metadata 中获取用户代理（线程安全，使用快照避免并发读写）
	metadata := c.GetMetadataSnapshot()
	if metadata != nil {
		if ua, ok := metadata["user_agent"].(string); ok && ua != "" {
			return ua
		}
		if ua, ok := metadata["user-agent"].(string); ok && ua != "" {
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

	// 使用 defer recover 捕获可能的 send on closed channel panic
	defer func() {
		if r := recover(); r != nil {
			// Channel 已关闭，标记为已关闭状态
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

// TrySendSSE 尝试向SSE客户端发送消息，如果已关闭或失败则返回false
func (c *Client) TrySendSSE(msg *HubMessage) bool {
	c.CloseMu.Lock()
	defer c.CloseMu.Unlock()

	if c.IsClosed() || c.SSEMessageCh == nil {
		return false
	}

	// 使用 defer recover 捕获可能的 send on closed channel panic
	defer func() {
		if r := recover(); r != nil {
			// Channel 已关闭，标记为已关闭状态
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

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-09-06 09:50:55
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 00:00:00
 * @FilePath: \go-wsc\models\message.go
 * @Description: 消息处理逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package models

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/routing"
)

// Data 字段的常量 key
const (
	DataKeyContentExtra = "content_extra" // 扩展内容
	DataKeyMetadata     = "metadata"      // 元数据
	DataKeyMediaInfo    = "media_info"    // 媒体信息
)

// HubMessage Hub消息结构
//
// 字段对齐优化说明：
//   - 所有 16 字节 string 字段集中在结构体前部（8 字节对齐，无 padding）
//   - map/time.Time/int64 等其他 8 字节对齐字段紧随其后
//   - 3 个 bool 字段（1 字节对齐）集中放在末尾，避免散落导致的 7 字节 padding
//   - 优化后结构体大小：360 → 352 字节（每实例节省 8 字节，海量消息场景下可观）
//   - 热点字段（ID/MessageType/Sender/Receiver）保持在结构体头部，位于首个 cache line 内
//
// 并发安全说明：
//   - mu 为 *sync.RWMutex 指针（非值字段），避免 Clone/浅拷贝时的 go vet copylocks
//   - 所有 Set*/With* 写方法持 Lock，所有 Get* 读方法持 RLock
//   - 内部 setMapValue/getMapValue 不持锁，由调用方（With*/Get*）持锁保护，避免 RLock 不可重入导致的死锁
//   - Clone 持 RLock 深拷贝 Data，副本使用独立 mu（避免与原对象共享锁互相阻塞）
//   - mu 为 nil（如直接 &HubMessage{} 构造）时退化为无锁，兼容测试与零值构造场景
type HubMessage struct {
	// ========== 标识与路由（热点字段，置于头部以利用 cache line） ==========
	ID           string      `json:"id"`                      // 消息ID（用于ACK）
	TraceID      string      `json:"trace_id,omitempty"`      // 全链路追踪ID（从 ctx 自动注入，跨节点序列化携带）
	MessageType  MessageType `json:"message_type"`            // 消息类型
	Sender       string      `json:"sender"`                  // 发送者 (从上下文获取)
	SenderName   string      `json:"sender_name"`             // 发送者昵称
	SenderType   UserType    `json:"sender_type"`             // 发送者类型
	SenderClient string      `json:"sender_client,omitempty"` // 发送者客户端ID（多端同步标识）
	Receiver     string      `json:"receiver"`                // 接收者用户ID
	ReceiverName string      `json:"receiver_name"`           // 接收者昵称
	ReceiverType UserType    `json:"receiver_type"`           // 接收者用户类型

	// ========== 接收与节点路由 ==========
	ReceiverClient string `json:"receiver_client,omitempty"` // 接收者客户端ID
	ReceiverNode   string `json:"receiver_node,omitempty"`   // 接收者所在节点ID
	SessionID      string `json:"session_id"`                // 会话ID

	// ========== 内容 ==========
	Content string `json:"content"` // 消息内容

	// ========== 扩展数据（8 字节对齐字段） ==========
	Data     map[string]interface{} `json:"data,omitempty"` // 扩展数据（包含 content_extra、metadata、media_info）
	CreateAt time.Time              `json:"create_at"`      // 创建时间

	// ========== 消息ID与序列 ==========
	MessageID    string `json:"message_id"`                // 业务消息ID
	ReplyToMsgID string `json:"reply_to_msg_id,omitempty"` // 回复的消息ID
	SeqNo        int64  `json:"seq_no"`                    // 消息序列号

	// ========== 类型与策略（string，16 字节对齐） ==========
	Priority      Priority      `json:"priority"`                 // 优先级
	Source        MessageSource `json:"source,omitempty"`         // 消息来源(online/offline)
	PushType      PushType      `json:"push_type,omitempty"`      // 推送类型
	BroadcastType BroadcastType `json:"broadcast_type,omitempty"` // 广播类型（会话成员/全站）

	// ========== 路由信封（投递精确隔离，跨节点随消息体携带） ==========
	// ctx 路由元数据在异步队列消费时会丢失，故信封必须随消息体流转。
	// 入口层注入（InjectRoute），投递/跨节点/离线回放统一从 msg 取。
	// 隔离层次：AppID(应用) > Namespace(租户) > GroupID(平台) > UserID
	// 空 AppID = 全局共享（老部署兼容，不参与 appId 严格匹配过滤）
	AppID     string   `json:"app_id,omitempty"`    // 应用ID（最上层隔离维度，空=全局共享）
	Namespace string   `json:"namespace,omitempty"` // 命名空间ID（空=全局，已归一化时非空）
	GroupIDs  []string `json:"group_ids,omitempty"` // 群组ID列表（P2P 为 nil；单群组 len==1；多群组 len>1）

	// mu 保护所有可变字段的并发读写。指针类型避免 Clone/*m 值拷贝触发 copylocks。
	// nil 时退化为无锁（兼容直接 &HubMessage{} 构造）。NewHubMessage 初始化。
	// 置于布尔区之前，保持 3 个 bool 集中在结构体末尾（对齐优化）
	mu *sync.RWMutex `json:"-"`

	// ========== 布尔标志（1 字节对齐，集中置于末尾避免 padding） ==========
	RequireAck          bool `json:"require_ack,omitempty"`           // 是否需要ACK确认
	SkipDatabaseStorage bool `json:"skip_database_storage,omitempty"` // 是否跳过主数据库存储
	SkipSendToClient    bool `json:"skip_send_to_client,omitempty"`   // 是否跳过发送到客户端
}

// lockWrite 获取写锁并返回解锁函数；mu 为 nil 时返回 no-op（兼容直接构造的零值消息）
func (m *HubMessage) lockWrite() func() {
	if m.mu != nil {
		m.mu.Lock()
		return m.mu.Unlock
	}
	return func() {}
}

// lockRead 获取读锁并返回解锁函数；mu 为 nil 时返回 no-op
func (m *HubMessage) lockRead() func() {
	if m.mu != nil {
		m.mu.RLock()
		return m.mu.RUnlock
	}
	return func() {}
}

// SetID 设置消息ID
func (m *HubMessage) SetID(id string) *HubMessage {
	defer m.lockWrite()()
	m.ID = id
	return m
}

// InjectContext 从 ctx 注入上下文信息到消息（trace_id 等）
// 优先从 OTel span 提取 trace_id，fallback 到 ctx.Value(logger.ContextKeyTraceID)
// 已有 trace_id 时不覆盖（跨节点消息保留源 trace）
func (m *HubMessage) InjectContext(ctx context.Context) *HubMessage {
	defer m.lockWrite()()
	if m.TraceID != "" {
		return m // 已有则不覆盖
	}
	m.TraceID = logger.ExtractTraceID(ctx)
	return m
}

// ContextFrom 基于消息的 trace_id 创建一个携带 trace 信息的 context
// 用于消息流转路径中恢复 ctx（如 PubSub 消费端、回调等场景）
func (m *HubMessage) ContextFrom(parent context.Context) context.Context {
	defer m.lockRead()()
	if m.TraceID == "" {
		return parent
	}
	return logger.ContextWithTraceID(parent, m.TraceID)
}

// SetMessageType 设置消息类型
func (m *HubMessage) SetMessageType(messageType MessageType) *HubMessage {
	defer m.lockWrite()()
	m.MessageType = messageType
	return m
}

// SetSender 设置发送者
func (m *HubMessage) SetSender(sender string) *HubMessage {
	defer m.lockWrite()()
	m.Sender = sender
	return m
}

// SetSenderName 设置发送者昵称
func (m *HubMessage) SetSenderName(name string) *HubMessage {
	defer m.lockWrite()()
	m.SenderName = name
	return m
}

// SetSenderType 设置发送者类型
func (m *HubMessage) SetSenderType(senderType UserType) *HubMessage {
	defer m.lockWrite()()
	m.SenderType = senderType
	return m
}

// SetReceiver 设置接收者
func (m *HubMessage) SetReceiver(receiver string) *HubMessage {
	defer m.lockWrite()()
	m.Receiver = receiver
	return m
}

// SetReceiverName 设置接收者昵称
func (m *HubMessage) SetReceiverName(name string) *HubMessage {
	defer m.lockWrite()()
	m.ReceiverName = name
	return m
}

// SetReceiverType 设置接收者用户类型
func (m *HubMessage) SetReceiverType(receiverType UserType) *HubMessage {
	defer m.lockWrite()()
	m.ReceiverType = receiverType
	return m
}

// SetReceiverClient 设置接收者客户端ID
func (m *HubMessage) SetReceiverClient(clientID string) *HubMessage {
	defer m.lockWrite()()
	m.ReceiverClient = clientID
	return m
}

// SetReceiverNode 设置接收者所在节点ID
func (m *HubMessage) SetReceiverNode(nodeID string) *HubMessage {
	defer m.lockWrite()()
	m.ReceiverNode = nodeID
	return m
}

// SetSessionID 设置会话ID
func (m *HubMessage) SetSessionID(sessionID string) *HubMessage {
	defer m.lockWrite()()
	m.SessionID = sessionID
	return m
}

// SetContent 设置消息内容
func (m *HubMessage) SetContent(content string) *HubMessage {
	defer m.lockWrite()()
	m.Content = content
	return m
}

// SetMessageID 设置业务消息ID
func (m *HubMessage) SetMessageID(messageID string) *HubMessage {
	defer m.lockWrite()()
	m.MessageID = messageID
	return m
}

// SetSeqNo 设置消息序列号
func (m *HubMessage) SetSeqNo(seqNo int64) *HubMessage {
	defer m.lockWrite()()
	m.SeqNo = seqNo
	return m
}

// SetPriority 设置优先级
func (m *HubMessage) SetPriority(priority Priority) *HubMessage {
	defer m.lockWrite()()
	m.Priority = priority
	return m
}

// SetReplyToMsgID 设置回复的消息ID
func (m *HubMessage) SetReplyToMsgID(replyToMsgID string) *HubMessage {
	defer m.lockWrite()()
	m.ReplyToMsgID = replyToMsgID
	return m
}

// SetRequireAck 设置是否需要ACK确认
func (m *HubMessage) SetRequireAck(requireAck bool) *HubMessage {
	defer m.lockWrite()()
	m.RequireAck = requireAck
	return m
}

// SetPushType 设置推送类型
func (m *HubMessage) SetPushType(pushType PushType) *HubMessage {
	defer m.lockWrite()()
	m.PushType = pushType
	return m
}

// SetBroadcastType 设置广播类型
func (m *HubMessage) SetBroadcastType(broadcastType BroadcastType) *HubMessage {
	defer m.lockWrite()()
	m.BroadcastType = broadcastType
	return m
}

// WithMediaInfo 设置媒体信息（接受任意类型，自动序列化为 JSON 字符串，nil 安全）
func (m *HubMessage) WithMediaInfo(mediaInfo any) *HubMessage {
	defer m.lockWrite()()
	if mediaInfo == nil {
		return m
	}
	if m.Data == nil {
		m.Data = make(map[string]any)
	}
	m.Data[DataKeyMediaInfo] = mediaInfo
	return m
}

// GetMediaInfo 获取媒体信息（返回原始值）
func (m *HubMessage) GetMediaInfo() (any, bool) {
	defer m.lockRead()()
	if m.Data == nil {
		return nil, false
	}
	value, exists := m.Data[DataKeyMediaInfo]
	return value, exists
}

// GetMediaInfoJSON 获取媒体信息的 JSON 字符串表示
func (m *HubMessage) GetMediaInfoJSON() string {
	defer m.lockRead()()
	if m.Data == nil {
		return "{}"
	}
	value, exists := m.Data[DataKeyMediaInfo]
	if !exists || value == nil {
		return "{}"
	}
	if str, ok := value.(string); ok {
		return str
	}
	if jsonBytes, err := json.Marshal(value); err == nil {
		return string(jsonBytes)
	}
	return "{}"
}

// SetSkipDatabaseStorage 设置是否跳过主数据库存储
func (m *HubMessage) SetSkipDatabaseStorage(skip bool) *HubMessage {
	defer m.lockWrite()()
	m.SkipDatabaseStorage = skip
	return m
}

// SetSkipSendToClient 设置是否跳过发送到客户端
func (m *HubMessage) SetSkipSendToClient(skip bool) *HubMessage {
	defer m.lockWrite()()
	m.SkipSendToClient = skip
	return m
}

// setMapValue 是一个通用的设置方法，用于设置嵌套 map 的值
// 注意：本方法不持锁，由调用方（With*/Set*）持锁保护，避免 RLock 不可重入死锁
func (m *HubMessage) setMapValue(key string, subKey string, value interface{}) *HubMessage {
	if m.Data == nil {
		m.Data = make(map[string]interface{})
	}
	subMap, ok := m.Data[key].(map[string]interface{})
	if !ok {
		subMap = make(map[string]interface{})
		m.Data[key] = subMap
	}
	subMap[subKey] = value
	return m
}

// getMapValue 是一个通用的获取方法，用于从嵌套 map 中获取值
// 注意：本方法不持锁，由调用方（Get*）持锁保护
func (m *HubMessage) getMapValue(key string, subKey string) (interface{}, bool) {
	if m.Data == nil {
		return nil, false
	}
	subMap, ok := m.Data[key].(map[string]interface{})
	if !ok {
		return nil, false
	}
	value, exists := subMap[subKey]
	return value, exists
}

// WithOption 设置扩展数据选项
func (m *HubMessage) WithOption(key string, value interface{}) *HubMessage {
	defer m.lockWrite()()
	if m.Data == nil {
		m.Data = make(map[string]interface{})
	}
	m.Data[key] = value
	return m
}

// GetOption 获取扩展数据选项
func (m *HubMessage) GetOption(key string) (interface{}, bool) {
	defer m.lockRead()()
	if m.Data == nil {
		return nil, false
	}
	value, exists := m.Data[key]
	return value, exists
}

// WithContentExtra 设置单个 content_extra 字段
func (m *HubMessage) WithContentExtra(key string, value any) *HubMessage {
	defer m.lockWrite()()
	return m.setMapValue(DataKeyContentExtra, key, value)
}

// WithAllContentExtra 批量设置 content_extra（接受任意类型，自动序列化，nil 安全）
func (m *HubMessage) WithAllContentExtra(contentExtra any) *HubMessage {
	defer m.lockWrite()()
	if contentExtra == nil {
		return m
	}
	if m.Data == nil {
		m.Data = make(map[string]any)
	}
	m.Data[DataKeyContentExtra] = contentExtra
	return m
}

// GetContentExtra 获取 content_extra 字段值
func (m *HubMessage) GetContentExtra(key string) (any, bool) {
	defer m.lockRead()()
	return m.getMapValue(DataKeyContentExtra, key)
}

// GetAllContentExtra 获取整个 content_extra map
func (m *HubMessage) GetAllContentExtra() map[string]any {
	defer m.lockRead()()
	if m.Data == nil {
		return make(map[string]any)
	}
	contentExtra, ok := m.Data[DataKeyContentExtra].(map[string]any)
	if !ok || contentExtra == nil {
		return make(map[string]any)
	}
	return contentExtra
}

// GetContentExtraJSON 获取 content_extra 的 JSON 字符串表示
func (m *HubMessage) GetContentExtraJSON() string {
	defer m.lockRead()()
	if m.Data == nil {
		return "{}"
	}
	value, exists := m.Data[DataKeyContentExtra]
	if !exists || value == nil {
		return "{}"
	}
	if str, ok := value.(string); ok {
		return str
	}
	if jsonBytes, err := json.Marshal(value); err == nil {
		return string(jsonBytes)
	}
	return "{}"
}

// WithMetadata 设置单个 metadata 字段
func (m *HubMessage) WithMetadata(key string, value string) *HubMessage {
	defer m.lockWrite()()
	return m.setMapValue(DataKeyMetadata, key, value)
}

// WithAllMetadata 批量设置 metadata（接受任意类型，自动序列化，nil 安全）
func (m *HubMessage) WithAllMetadata(metadata any) *HubMessage {
	defer m.lockWrite()()
	if metadata == nil {
		return m
	}
	if m.Data == nil {
		m.Data = make(map[string]any)
	}
	m.Data[DataKeyMetadata] = metadata
	return m
}

// GetMetadata 获取 metadata 字段值
// 安全：非 string 类型返回 ("", false)，不再 panic
func (m *HubMessage) GetMetadata(key string) (string, bool) {
	defer m.lockRead()()
	value, exists := m.getMapValue(DataKeyMetadata, key)
	if !exists {
		return "", false
	}
	s, ok := value.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// GetAllMetadata 获取整个 metadata map
func (m *HubMessage) GetAllMetadata() map[string]any {
	defer m.lockRead()()
	if m.Data == nil {
		return make(map[string]any)
	}
	metadata, ok := m.Data[DataKeyMetadata].(map[string]any)
	if !ok || metadata == nil {
		return make(map[string]any)
	}
	return metadata
}

// GetMetadataJSON 获取 metadata 的 JSON 字符串表示
func (m *HubMessage) GetMetadataJSON() string {
	defer m.lockRead()()
	if m.Data == nil {
		return "{}"
	}
	value, exists := m.Data[DataKeyMetadata]
	if !exists || value == nil {
		return "{}"
	}
	if str, ok := value.(string); ok {
		return str
	}
	if jsonBytes, err := json.Marshal(value); err == nil {
		return string(jsonBytes)
	}
	return "{}"
}

// GetTraceID 获取 trace_id，优先自身字段，fallback 到 metadata
func (m *HubMessage) GetTraceID() string {
	defer m.lockRead()()
	return m.TraceID
}

// ========== 路由信封 helper（统一从 msg 取，不依赖 ctx，避免异步/跨节点 ctx 丢失） ==========

// SetAppID 设置应用ID（链式调用，最上层隔离维度）
func (m *HubMessage) SetAppID(appID string) *HubMessage {
	defer m.lockWrite()()
	m.AppID = appID
	return m
}

// SetNamespace 设置命名空间（链式调用）
func (m *HubMessage) SetNamespace(ns string) *HubMessage {
	defer m.lockWrite()()
	m.Namespace = ns
	return m
}

// SetGroupIDs 设置群组ID列表（链式调用，P2P 传 nil）
func (m *HubMessage) SetGroupIDs(groupIDs []string) *HubMessage {
	defer m.lockWrite()()
	m.GroupIDs = groupIDs
	return m
}

// GetAppID 获取应用ID（空值返回空串，不补默认值，保持「空=全局共享」语义）
func (m *HubMessage) GetAppID() string {
	defer m.lockRead()()
	return m.AppID
}

// GetNamespace 获取命名空间
func (m *HubMessage) GetNamespace() string {
	defer m.lockRead()()
	return m.Namespace
}

// GetGroupIDs 获取群组ID列表（返回副本，避免外部修改污染内部）
func (m *HubMessage) GetGroupIDs() []string {
	defer m.lockRead()()
	if m.GroupIDs == nil {
		return nil
	}
	return append([]string(nil), m.GroupIDs...)
}

// FirstGroupID 获取第一个群组ID（单群组场景用；多群组/无群组返回空）
func (m *HubMessage) FirstGroupID() string {
	defer m.lockRead()()
	if len(m.GroupIDs) == 0 {
		return ""
	}
	return m.GroupIDs[0]
}

// InjectRoute 从 ctx 提取路由元数据 + trace_id 注入信封（入口层统一调用，一次性写入 msg+ctx）
//
// 一个方法搞定四件事（广播与 P2P 共用同一套路由提取逻辑，无特殊分支）：
//  1. 注入 trace_id：已有不覆盖（跨节点消息保留源 trace），优先 OTel span，fallback ctx.Value
//  2. 归一化 appID：空值补 DefaultAppID（appID 无全局语义，必填）
//  3. 注入信封：已有值不覆盖（跨节点消息保留源路由），空值从 ctx 补；namespace 保持 ctx 原值
//  4. 回写 ctx：保证下游 routing.FromContext 与信封一致
//
// namespace 不归一化的设计意图（一套逻辑打通广播与 P2P）：
//   - 全局广播：ctx 无 namespace → msg.Namespace="" → ClientMatchesEnvelope 跳过 ns 过滤匹配所有
//   - 命名空间广播/P2P：ctx 有 namespace → msg.Namespace=ns → 严格匹配
//   - P2P 严格场景需 namespace 归一化时，调用方在入口 EnsureRouteDefaults 或 NormalizeRoute
func (m *HubMessage) InjectRoute(ctx context.Context) context.Context {
	defer m.lockWrite()()
	// 1. trace_id 注入（与 InjectContext 逻辑一致，已有不覆盖）
	if m.TraceID == "" {
		m.TraceID = logger.ExtractTraceID(ctx)
	}
	// 2-3. 路由信封注入（appID 归一化，namespace 保持 ctx 原值）
	appID, _ := routing.NormalizeRoute(routing.AppIDFromContext(ctx), "")
	ns := routing.NamespaceFromContext(ctx)
	groupIDs := routing.GroupIDsFromContext(ctx)
	if m.AppID == "" {
		m.AppID = appID
	}
	if m.Namespace == "" {
		m.Namespace = ns // 保留 ctx 原值（空=全局广播，非空=命名空间隔离）
	}
	if m.GroupIDs == nil && len(groupIDs) > 0 {
		m.GroupIDs = append([]string(nil), groupIDs...)
	}
	// 4. 回写 ctx，确保信封值与 ctx 一致（下游路由模块从 ctx 取）
	return routing.NewRoute().WithAppID(m.AppID).WithNamespace(m.Namespace).WithGroupIDs(m.GroupIDs).Inject(ctx)
}

// RouteContext 从信封恢复路由 ctx（异步队列/跨节点/离线回放场景，ctx 已丢失时重建）
// 如果信封为空则返回原 ctx；非空则归一化 appID 后重建 ctx
func (m *HubMessage) RouteContext(parent context.Context) context.Context {
	defer m.lockRead()()
	if m.AppID == "" && m.Namespace == "" && len(m.GroupIDs) == 0 {
		return parent
	}
	return routing.NewRoute().WithAppID(m.AppID).WithNamespace(m.Namespace).WithGroupIDs(m.GroupIDs).Inject(parent)
}

// ContextWithRoute 将给定 appID+namespace+groupIDs + trace_id 同时写入 msg 信封和 ctx
//
// 与 InjectRoute 的区别：路由参数显式传入（不从 ctx 取），适用于 BroadcastToGroupMembers 等
// 带命名空间参数的公开 API；InjectRoute 适用于从 ctx 取路由的内部发送链路
//
// 入口层首选；没有上下文时单独调用 SetAppID/SetNamespace/SetGroupIDs
// 空值 appID 归一化为 DefaultAppID；namespace 保持原值（广播场景可传空）
func (m *HubMessage) ContextWithRoute(parent context.Context, appID, ns string, groupIDs []string) context.Context {
	defer m.lockWrite()()
	// trace_id 注入（已有不覆盖）
	if m.TraceID == "" {
		m.TraceID = logger.ExtractTraceID(parent)
	}
	m.AppID, _ = routing.NormalizeRoute(appID, "")
	m.Namespace = ns
	if groupIDs == nil {
		m.GroupIDs = nil
	} else {
		m.GroupIDs = append([]string(nil), groupIDs...)
	}
	return routing.NewRoute().WithAppID(m.AppID).WithNamespace(ns).WithGroupIDs(groupIDs).Inject(parent)
}

// Clone 创建消息的深拷贝，避免并发修改问题
// 实现 syncx.Cloner 接口，DeepCopy 调用时走零反射快速路径
// 持 RLock 读取全部字段（与 Set*/With* 的 Lock 互斥），Data map 深拷贝（含嵌套子 map），
// 副本使用独立 mu（避免与原对象共享锁互相阻塞）
func (m *HubMessage) Clone() *HubMessage {
	if m.mu != nil {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	msg := *m // 值拷贝所有字段（string/int/bool/time.Time 等），此时与 Set* 互斥，读取安全

	// 副本使用独立锁，避免与原对象共享锁
	msg.mu = &sync.RWMutex{}

	// 仅 Data map 和 GroupIDs slice 需要深拷贝，其他字段均为值类型
	// （AppID/Namespace 为 string 值类型，*m 值拷贝已自动复制，无需显式深拷贝）
	if m.Data != nil {
		msg.Data = make(map[string]interface{}, len(m.Data))
		for k, v := range m.Data {
			// setMapValue 创建的子 map（metadata/content_extra）为可变结构，
			// 需深拷贝避免副本修改子 map 时通过共享引用污染原对象
			if subMap, ok := v.(map[string]interface{}); ok {
				copied := make(map[string]interface{}, len(subMap))
				for sk, sv := range subMap {
					copied[sk] = sv
				}
				msg.Data[k] = copied
			} else {
				msg.Data[k] = v // 基本类型/不可变值直接引用
			}
		}
	}
	// GroupIDs 为 slice，*m 值拷贝只复制 header（共享底层数组），需显式深拷贝
	// 避免副本 append/修改污染原对象（与 Data map 深拷贝同理）
	if m.GroupIDs != nil {
		msg.GroupIDs = append([]string(nil), m.GroupIDs...)
	}

	return &msg
}

// CloneDeep 实现 syncx.Cloner 接口
// DeepCopy(&dst, src) 检测到 src 实现 Cloner 时，直接调用此方法跳过反射
func (m *HubMessage) CloneDeep() any {
	return m.Clone()
}

// GetMessageID 获取消息ID，空值返回默认消息ID
func (m *HubMessage) GetMessageID() string {
	defer m.lockRead()()
	return m.MessageID
}

// MarshalJSON 自定义序列化：持 RLock 贯穿 json.Marshal，与所有 Set*/With* 的 Lock 互斥，
// 消除反射遍历 Data map 与并发写入导致的 "concurrent map iteration and map write" fatal
func (m *HubMessage) MarshalJSON() ([]byte, error) {
	if m.mu != nil {
		m.mu.RLock()
		defer m.mu.RUnlock()
	}
	// 用别名避免递归调用 MarshalJSON；mu 为 json:"-" 不会序列化
	type msgAlias HubMessage
	alias := msgAlias(*m)
	alias.mu = nil // 防御性置空，避免别名结构意外携带锁
	return json.Marshal(alias)
}

// NewHubMessage 创建通用消息结构
func NewHubMessage() *HubMessage {
	return &HubMessage{
		Sender:     UserTypeSystem.String(),
		SenderType: UserTypeSystem,
		CreateAt:   time.Now(),
		Data:       make(map[string]interface{}),
		PushType:   PushTypeDirect, // 默认直发
		Priority:   PriorityNormal,
		Source:     MessageSourceOnline,
		mu:         &sync.RWMutex{},
	}
}

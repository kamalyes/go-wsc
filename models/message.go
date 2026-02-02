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
	"time"

	"github.com/kamalyes/go-toolbox/pkg/syncx"
)

// Data 字段的常量 key
const (
	DataKeyContentExtra = "content_extra" // 扩展内容
	DataKeyMetadata     = "metadata"      // 元数据
	DataKeyMediaInfo    = "media_info"    // 媒体信息
)

// HubMessage Hub消息结构
type HubMessage struct {
	ID                  string                 `json:"id"`                              // 消息ID（用于ACK）
	MessageType         MessageType            `json:"message_type"`                    // 消息类型
	Sender              string                 `json:"sender"`                          // 发送者 (从上下文获取)
	SenderName          string                 `json:"sender_name"`                     // 发送者昵称
	SenderType          UserType               `json:"sender_type"`                     // 发送者类型
	SenderClient        string                 `json:"sender_client,omitempty"`         // 发送者客户端ID（多端同步标识）
	Receiver            string                 `json:"receiver"`                        // 接收者用户ID
	ReceiverName        string                 `json:"receiver_name"`                   // 接收者昵称
	ReceiverType        UserType               `json:"receiver_type"`                   // 接收者用户类型
	ReceiverClient      string                 `json:"receiver_client,omitempty"`       // 接收者客户端ID
	ReceiverNode        string                 `json:"receiver_node,omitempty"`         // 接收者所在节点ID
	SessionID           string                 `json:"session_id"`                      // 会话ID
	Content             string                 `json:"content"`                         // 消息内容
	Data                map[string]interface{} `json:"data,omitempty"`                  // 扩展数据（包含 content_extra 和 metadata）
	CreateAt            time.Time              `json:"create_at"`                       // 创建时间
	MessageID           string                 `json:"message_id"`                      // 业务消息ID
	SeqNo               int64                  `json:"seq_no"`                          // 消息序列号
	Priority            Priority               `json:"priority"`                        // 优先级
	ReplyToMsgID        string                 `json:"reply_to_msg_id,omitempty"`       // 回复的消息ID
	RequireAck          bool                   `json:"require_ack,omitempty"`           // 是否需要ACK确认
	Source              MessageSource          `json:"source,omitempty"`                // 消息来源(online/offline)
	PushType            PushType               `json:"push_type,omitempty"`             // 推送类型
	BroadcastType       BroadcastType          `json:"broadcast_type,omitempty"`        // 广播类型（会话成员/全站）
	SkipDatabaseStorage bool                   `json:"skip_database_storage,omitempty"` // 是否跳过主数据库存储
	SkipSendToClient    bool                   `json:"skip_send_to_client,omitempty"`   // 是否跳过发送到客户端
}

// SetID 设置消息ID
func (m *HubMessage) SetID(id string) *HubMessage {
	m.ID = id
	return m
}

// SetMessageType 设置消息类型
func (m *HubMessage) SetMessageType(messageType MessageType) *HubMessage {
	m.MessageType = messageType
	return m
}

// SetSender 设置发送者
func (m *HubMessage) SetSender(sender string) *HubMessage {
	m.Sender = sender
	return m
}

// SetSenderName 设置发送者昵称
func (m *HubMessage) SetSenderName(name string) *HubMessage {
	m.SenderName = name
	return m
}

// SetSenderType 设置发送者类型
func (m *HubMessage) SetSenderType(senderType UserType) *HubMessage {
	m.SenderType = senderType
	return m
}

// SetReceiver 设置接收者
func (m *HubMessage) SetReceiver(receiver string) *HubMessage {
	m.Receiver = receiver
	return m
}

// SetReceiverName 设置接收者昵称
func (m *HubMessage) SetReceiverName(name string) *HubMessage {
	m.ReceiverName = name
	return m
}

// SetReceiverType 设置接收者类型
func (m *HubMessage) SetReceiverType(receiverType UserType) *HubMessage {
	m.ReceiverType = receiverType
	return m
}

// SetReceiverClient 设置接收者客户端ID
func (m *HubMessage) SetReceiverClient(clientID string) *HubMessage {
	m.ReceiverClient = clientID
	return m
}

// SetReceiverNode 设置接收者所在节点ID
func (m *HubMessage) SetReceiverNode(nodeID string) *HubMessage {
	m.ReceiverNode = nodeID
	return m
}

// SetSessionID 设置会话ID
func (m *HubMessage) SetSessionID(sessionID string) *HubMessage {
	m.SessionID = sessionID
	return m
}

// SetContent 设置消息内容
func (m *HubMessage) SetContent(content string) *HubMessage {
	m.Content = content
	return m
}

// SetMessageID 设置业务消息ID
func (m *HubMessage) SetMessageID(messageID string) *HubMessage {
	m.MessageID = messageID
	return m
}

// SetSeqNo 设置消息序列号
func (m *HubMessage) SetSeqNo(seqNo int64) *HubMessage {
	m.SeqNo = seqNo
	return m
}

// SetPriority 设置优先级
func (m *HubMessage) SetPriority(priority Priority) *HubMessage {
	m.Priority = priority
	return m
}

// SetReplyToMsgID 设置回复的消息ID
func (m *HubMessage) SetReplyToMsgID(replyToMsgID string) *HubMessage {
	m.ReplyToMsgID = replyToMsgID
	return m
}

// SetRequireAck 设置是否需要ACK确认
func (m *HubMessage) SetRequireAck(requireAck bool) *HubMessage {
	m.RequireAck = requireAck
	return m
}

// SetPushType 设置推送类型
func (m *HubMessage) SetPushType(pushType PushType) *HubMessage {
	m.PushType = pushType
	return m
}

// SetBroadcastType 设置广播类型
func (m *HubMessage) SetBroadcastType(broadcastType BroadcastType) *HubMessage {
	m.BroadcastType = broadcastType
	return m
}

// SetSkipDatabaseStorage 设置是否跳过主数据库存储
func (m *HubMessage) SetSkipDatabaseStorage(skip bool) *HubMessage {
	m.SkipDatabaseStorage = skip
	return m
}

// SkipSendToClient 设置是否跳过发送到客户端
func (m *HubMessage) SetSkipSendToClient(skip bool) *HubMessage {
	m.SkipSendToClient = skip
	return m
}

// setMapValue 是一个通用的设置方法，用于设置嵌套 map 的值
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
	if m.Data == nil {
		m.Data = make(map[string]interface{})
	}
	m.Data[key] = value
	return m
}

// GetOption 获取扩展数据选项
func (m *HubMessage) GetOption(key string) (interface{}, bool) {
	if m.Data == nil {
		return nil, false
	}
	value, exists := m.Data[key]
	return value, exists
}

// WithContentExtra 设置 content_extra 字段
func (m *HubMessage) WithContentExtra(key string, value interface{}) *HubMessage {
	return m.setMapValue(DataKeyContentExtra, key, value)
}

// GetContentExtra 获取 content_extra 字段值
func (m *HubMessage) GetContentExtra(key string) (interface{}, bool) {
	return m.getMapValue(DataKeyContentExtra, key)
}

// WithMetadata 设置 metadata 字段
func (m *HubMessage) WithMetadata(key string, value string) *HubMessage {
	return m.setMapValue(DataKeyMetadata, key, value)
}

// GetMetadata 获取 metadata 字段值
func (m *HubMessage) GetMetadata(key string) (string, bool) {
	value, exists := m.getMapValue(DataKeyMetadata, key)
	if !exists {
		return "", false
	}
	return value.(string), true
}

// Clone 创建消息的深拷贝，避免并发修改问题
func (m *HubMessage) Clone() *HubMessage {
	var msg HubMessage
	syncx.DeepCopy(&msg, m)
	return &msg
}

// NewHubMessage 创建通用消息结构
func NewHubMessage() *HubMessage {
	return &HubMessage{
		Sender:   UserTypeSystem.String(),
		CreateAt: time.Now(),
		Data:     make(map[string]interface{}),
		PushType: PushTypeDirect, // 默认直发
		Priority: PriorityNormal,
		Source:   MessageSourceOnline,
	}
}

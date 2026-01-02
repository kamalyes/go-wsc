/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 09:15:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 17:05:16
 * @FilePath: \go-wsc\base_helpers.go
 * @Description: 测试辅助函数 - 统一管理测试中的公共方法
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Hub 测试辅助函数
// ============================================================================

// CreateTestHub 创建测试用的 Hub
func CreateTestHub(t *testing.T, config *wscconfig.WSC) *Hub {
	if config == nil {
		config = wscconfig.Default()
	}
	hub := NewHub(config)
	return hub
}

// CreateTestHubWithRedis 创建带 Redis 的测试 Hub
func CreateTestHubWithRedis(t *testing.T, redisClient *redis.Client, configMod func(*wscconfig.WSC)) *Hub {
	config := wscconfig.Default()
	if configMod != nil {
		configMod(config)
	}
	hub := NewHub(config)
	return hub
}

// StartTestHub 启动测试 Hub 并等待就绪
func StartTestHub(t *testing.T, hub *Hub) {
	go hub.Run()
	hub.WaitForStart()
}

// CleanupTestHub 清理测试 Hub
func CleanupTestHub(t *testing.T, hub *Hub, redisClient *redis.Client) {
	if hub != nil {
		hub.Shutdown()
	}
	if redisClient != nil {
		CleanupTestRedis(t, redisClient)
	}
}

// ============================================================================
// Client 测试辅助函数
// ============================================================================

// CreateTestClient 创建测试客户端
func CreateTestClient(id, userID string, userType UserType) *Client {
	return &Client{
		ID:       id,
		UserID:   userID,
		UserType: userType,
		SendChan: make(chan []byte, 10),
	}
}

// CreateTestWSClient 创建 WebSocket 测试客户端
func CreateTestWSClient(id, userID string, userType UserType) *Client {
	client := CreateTestClient(id, userID, userType)
	client.ConnectionType = ConnectionTypeWebSocket
	return client
}

// CreateTestSSEClient 创建 SSE 测试客户端
func CreateTestSSEClient(id, userID string, userType UserType) *Client {
	client := CreateTestClient(id, userID, userType)
	client.ConnectionType = ConnectionTypeSSE
	client.SSEWriter = httptest.NewRecorder()
	return client
}

// RegisterTestClient 注册测试客户端到 Hub
func RegisterTestClient(t *testing.T, hub *Hub, client *Client) {
	hub.Register(client)
	time.Sleep(50 * time.Millisecond) // 等待注册完成
}

// UnregisterTestClient 从 Hub 注销测试客户端
func UnregisterTestClient(t *testing.T, hub *Hub, client *Client) {
	hub.Unregister(client)
	time.Sleep(50 * time.Millisecond) // 等待注销完成
}

// ============================================================================
// Message 测试辅助函数
// ============================================================================

// CreateTestMessage 创建测试消息
func CreateTestMessage(from, to string, msgType MessageType, content string) *HubMessage {
	return NewHubMessage().
		SetID(fmt.Sprintf("msg-%d", time.Now().UnixNano())).
		SetSender(from).
		SetReceiver(to).
		SetMessageType(msgType).
		SetContent(content)
}

// CreateTestTextMessage 创建文本测试消息
func CreateTestTextMessage(from, to, content string) *HubMessage {
	return CreateTestMessage(from, to, MessageTypeText, content)
}

// CreateTestSystemMessage 创建系统测试消息
func CreateTestSystemMessage(to, content string) *HubMessage {
	return CreateTestMessage(UserTypeSystem.String(), to, MessageTypeSystem, content)
}

// ============================================================================
// Mock 对象
// ============================================================================

// MockWelcomeProvider 模拟欢迎消息提供者
type MockWelcomeProvider struct {
	enabled  bool
	template *WelcomeTemplate
	mu       sync.RWMutex
}

// NewMockWelcomeProvider 创建模拟欢迎消息提供者
func NewMockWelcomeProvider() *MockWelcomeProvider {
	return &MockWelcomeProvider{
		enabled: true,
		template: &WelcomeTemplate{
			Title:       "欢迎使用客服系统",
			Content:     "您好 {user_id}，欢迎使用我们的客服系统！",
			MessageType: MessageTypeSystem,
			Enabled:     true,
			Variables:   []string{"user_id", "time"},
		},
	}
}

// GetWelcomeMessage 获取欢迎消息
func (m *MockWelcomeProvider) GetWelcomeMessage(userID string, userRole UserRole, userType UserType, extraData map[string]interface{}) (*WelcomeMessage, bool, error) {
	type result struct {
		msg *WelcomeMessage
		ok  bool
	}
	res := syncx.WithRLockReturnFunc(&m.mu, func() result {
		if !m.enabled || !m.template.Enabled {
			return result{nil, false}
		}

		variables := map[string]interface{}{
			"user_id": userID,
		}
		for key, value := range extraData {
			variables[key] = value
		}

		temp := m.template.ReplaceVariables(variables)
		return result{
			msg: &WelcomeMessage{
				Title:    temp.Title,
				Content:  temp.Content,
				Priority: PriorityNormal,
				Data:     map[string]interface{}{"type": "welcome"},
			},
			ok: true,
		}
	})
	return res.msg, res.ok, nil
}

// SetEnabled 设置是否启用
func (m *MockWelcomeProvider) SetEnabled(enabled bool) {
	syncx.WithLock(&m.mu, func() {
		m.enabled = enabled
	})
}

// ============================================================================
// 等待和重试工具
// ============================================================================

// WaitForCondition 等待条件满足
func WaitForCondition(t *testing.T, condition func() bool, timeout time.Duration, message string) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	if message != "" {
		t.Logf("等待条件超时: %s", message)
	}
	return false
}

// WaitForConditionOrFail 等待条件满足，失败则报错
func WaitForConditionOrFail(t *testing.T, condition func() bool, timeout time.Duration, message string) {
	if !WaitForCondition(t, condition, timeout, message) {
		t.Fatalf("等待条件超时: %s", message)
	}
}

// Eventually 最终满足条件（带重试）
func Eventually(condition func() bool, timeout, interval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(interval)
	}
	return condition()
}

// WaitShort 短暂等待（50ms）
func WaitShort() {
	time.Sleep(50 * time.Millisecond)
}

// WaitMedium 中等等待（100ms）
func WaitMedium() {
	time.Sleep(100 * time.Millisecond)
}

// WaitLong 长时间等待（500ms）
func WaitLong() {
	time.Sleep(500 * time.Millisecond)
}

// ============================================================================
// Redis 数据清理工具
// ============================================================================

// CleanupRedisKeys 清理指定前缀的 Redis 键
func CleanupRedisKeys(t *testing.T, client *redis.Client, keyPrefixes ...string) {
	ctx := context.Background()
	for _, prefix := range keyPrefixes {
		pattern := prefix + "*"
		var cursor uint64
		for {
			keys, nextCursor, err := client.Scan(ctx, cursor, pattern, 100).Result()
			require.NoError(t, err)
			if len(keys) > 0 {
				err = client.Del(ctx, keys...).Err()
				require.NoError(t, err)
			}
			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}
	}
}

// SetupRedisWithCleanup 设置 Redis 并返回清理函数
func SetupRedisWithCleanup(t *testing.T, keyPrefixes ...string) (*redis.Client, func()) {
	client := GetTestRedisClient(t)
	CleanupRedisKeys(t, client, keyPrefixes...)
	return client, func() {
		CleanupRedisKeys(t, client, keyPrefixes...)
	}
}

// ============================================================================
// 测试场景辅助函数
// ============================================================================

// SetupTestScenario 设置完整的测试场景
type TestScenario struct {
	Hub         *Hub
	RedisClient *redis.Client
	Cleanup     func()
}

// SetupBasicTestScenario 设置基础测试场景
func SetupBasicTestScenario(t *testing.T) *TestScenario {
	redisClient, cleanup := SetupRedisWithCleanup(t, "wsc:", "test:", "connection:", "user:")
	config := wscconfig.Default()
	hub := CreateTestHub(t, config)
	StartTestHub(t, hub)

	return &TestScenario{
		Hub:         hub,
		RedisClient: redisClient,
		Cleanup: func() {
			hub.Shutdown()
			cleanup()
		},
	}
}

// SetupMultiLoginTestScenario 设置多端登录测试场景
func SetupMultiLoginTestScenario(t *testing.T, allowMultiLogin bool, maxConnections int) *TestScenario {
	redisClient, cleanup := SetupRedisWithCleanup(t, "wsc:", "test:", "connection:", "user:")
	config := wscconfig.Default()
	config.AllowMultiLogin = allowMultiLogin
	config.MaxConnectionsPerUser = maxConnections
	hub := CreateTestHub(t, config)
	StartTestHub(t, hub)

	return &TestScenario{
		Hub:         hub,
		RedisClient: redisClient,
		Cleanup: func() {
			hub.Shutdown()
			cleanup()
		},
	}
}

// ============================================================================
// 断言辅助函数
// ============================================================================

// AssertClientRegistered 断言客户端已注册
func AssertClientRegistered(t *testing.T, hub *Hub, clientID string) {
	WaitForConditionOrFail(t, func() bool {
		client := hub.GetClientByID(clientID)
		return client != nil
	}, 2*time.Second, fmt.Sprintf("客户端 %s 未注册", clientID))
}

// AssertClientNotRegistered 断言客户端未注册
func AssertClientNotRegistered(t *testing.T, hub *Hub, clientID string) {
	WaitForConditionOrFail(t, func() bool {
		client := hub.GetClientByID(clientID)
		return client == nil
	}, 2*time.Second, fmt.Sprintf("客户端 %s 仍然注册", clientID))
}

// AssertMessageReceived 断言消息已接收
func AssertMessageReceived(t *testing.T, client *Client, timeout time.Duration) []byte {
	select {
	case msg := <-client.SendChan:
		return msg
	case <-time.After(timeout):
		t.Fatal("超时：未收到消息")
		return nil
	}
}

// AssertNoMessageReceived 断言没有收到消息
func AssertNoMessageReceived(t *testing.T, client *Client, timeout time.Duration) {
	select {
	case msg := <-client.SendChan:
		t.Fatalf("不应该收到消息，但收到了: %s", string(msg))
	case <-time.After(timeout):
		// 正常，没有收到消息
	}
}

// ============================================================================
// 通用测试消息创建函数
// ============================================================================

// CreateTestHubMessage 创建一个测试用的 HubMessage（带自动生成的字段）
// 参数:
//   - receiver: 接收者ID
//   - sessionID: 会话ID（可选，如果为空则自动生成）
//
// 返回:
//   - messageID: 生成的消息ID
//   - message: 创建的 HubMessage 对象
func CreateTestHubMessage(receiver string, sessionID string) (string, *HubMessage) {
	msgID := osx.HashUnixMicroCipherText()

	if sessionID == "" {
		sessionID = "session_" + msgID[:8]
	}

	now := time.Now()
	return msgID, &HubMessage{
		ID:                  "msg_test_" + msgID,
		MessageID:           msgID,
		MessageType:         MessageTypeText,
		Sender:              "test_sender_" + msgID[:8],
		SenderType:          UserTypeCustomer,
		Receiver:            receiver,
		ReceiverType:        UserTypeAgent,
		ReceiverClient:      "client_" + msgID[:8],
		ReceiverNode:        "node_test_01",
		SessionID:           sessionID,
		Content:             "Test message: " + msgID[:16],
		Data:                map[string]interface{}{"test": "data"},
		CreateAt:            now,
		SeqNo:               now.UnixMicro(),
		Priority:            PriorityNormal,
		RequireAck:          true,
		PushType:            PushTypeDirect,
		BroadcastType:       BroadcastTypeNone,
		SkipDatabaseStorage: false,
	}
}

// CreateTestOfflineMessageRecord 创建一个测试用的离线消息记录
// 参数:
//   - messageID: 消息ID
//   - receiver: 接收者ID
//   - sessionID: 会话ID
//
// 返回:
//   - 创建的 OfflineMessageRecord 对象
func CreateTestOfflineMessageRecord(messageID, receiver, sessionID string) *OfflineMessageRecord {
	now := time.Now()
	hubMsg := &HubMessage{
		ID:                  "msg_test_" + messageID,
		MessageID:           messageID,
		MessageType:         MessageTypeText,
		Sender:              "test_sender_" + messageID[:8],
		SenderType:          UserTypeCustomer,
		Receiver:            receiver,
		ReceiverType:        UserTypeAgent,
		ReceiverClient:      "client_" + messageID[:8],
		ReceiverNode:        "node_test_01",
		SessionID:           sessionID,
		Content:             "Test offline message: " + messageID[:16],
		Data:                map[string]interface{}{"test": "data"},
		CreateAt:            now,
		SeqNo:               now.UnixMicro(),
		Priority:            PriorityNormal,
		RequireAck:          true,
		PushType:            PushTypeDirect,
		BroadcastType:       BroadcastTypeNone,
		SkipDatabaseStorage: false,
	}

	compressedData, _, err := zipx.ZlibCompressObjectWithSize(hubMsg)
	if err != nil {
		panic("compress test message failed: " + err.Error())
	}

	return &OfflineMessageRecord{
		MessageID:      messageID,
		Receiver:       receiver,
		Sender:         hubMsg.Sender,
		SessionID:      sessionID,
		CompressedData: compressedData,
		Status:         MessageSendStatusUserOffline, // 初始状态：用户离线
		RetryCount:     0,
		MaxRetry:       3,
		ScheduledAt:    now,
		ExpireAt:       now.Add(7 * 24 * time.Hour),
		CreatedAt:      now,
	}
}

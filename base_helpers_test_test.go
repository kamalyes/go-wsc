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
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/osx"
	"github.com/kamalyes/go-toolbox/pkg/random"
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

// StartTestHub 启动测试 Hub 并等待就绪
func StartTestHub(t *testing.T, hub *Hub) {
	go hub.Run()
	hub.WaitForStart()
}

// ============================================================================
// Redis 数据清理工具
// ============================================================================

// cleanupRedisKeys 清理指定前缀的 Redis 键
func cleanupRedisKeys(t *testing.T, client *redis.Client, keyPrefixes ...string) {
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

// ============================================================================
// 通用测试消息创建函数
// ============================================================================

// createTestHubMessage 使用 ID 生成器创建测试用的 HubMessage
func createTestHubMessage(msgType MessageType) *HubMessage {
	idGenerator := getTestIDGenerator()
	businessMsgID := idGenerator.GenerateRequestID()            // 业务消息ID
	hubID := "msg_test_node_" + idGenerator.GenerateRequestID() // Hub 内部ID（带节点前缀）
	senderID := idGenerator.GenerateCorrelationID()
	receiverID := idGenerator.GenerateCorrelationID()
	sessionID := idGenerator.GenerateTraceID()
	clientID := idGenerator.GenerateSpanID()
	receiverNode := idGenerator.GenerateSpanID()
	testData := idGenerator.GenerateTraceID()
	now := time.Now()

	return &HubMessage{
		ID:                  hubID,         // Hub 内部ID（带节点前缀）
		MessageID:           businessMsgID, // 业务消息ID（不带前缀）
		MessageType:         msgType,
		Sender:              senderID,
		SenderType:          UserTypeCustomer,
		Receiver:            receiverID,
		ReceiverType:        UserTypeAgent,
		ReceiverClient:      clientID,
		ReceiverNode:        receiverNode,
		SessionID:           sessionID,
		Content:             "Test message: " + businessMsgID,
		Data:                map[string]interface{}{"test": testData},
		CreateAt:            now,
		SeqNo:               now.UnixMicro(),
		Priority:            PriorityNormal,
		RequireAck:          true,
		PushType:            PushTypeDirect,
		BroadcastType:       BroadcastTypeNone,
		SkipDatabaseStorage: false,
	}
}

// ====================================================================
// 测试辅助函数
// ====================================================================

var (
	// 全局测试ID生成器，避免并发调用时生成重复ID
	testIDGenerator   *idgen.ShortFlakeGenerator
	testIDGeneratorMu sync.Mutex
)

// getTestIDGenerator 获取或创建全局测试ID生成器
func getTestIDGenerator() *idgen.ShortFlakeGenerator {
	testIDGeneratorMu.Lock()
	defer testIDGeneratorMu.Unlock()
	if testIDGenerator == nil {
		workerID := osx.GetWorkerIdForSnowflake()
		testIDGenerator = idgen.NewShortFlakeGenerator(workerID)
	}
	return testIDGenerator
}

// createTestClientWithIDGen 使用 ID 生成器创建测试用客户端
func createTestClientWithIDGen(userType UserType, bufferSize ...int) *Client {
	idGenerator := getTestIDGenerator()
	clientID := idGenerator.GenerateSpanID()
	userID := idGenerator.GenerateCorrelationID()
	bfSize := 100
	if len(bufferSize) > 0 {
		bfSize = bufferSize[0]
	}
	role := mathx.IF(userType == UserTypeCustomer, UserRoleCustomer, UserRoleAgent)
	now := time.Now()
	return &Client{
		ID:             clientID,
		UserID:         userID,
		UserType:       userType,
		Role:           role,
		ClientType:     ClientTypeWeb,
		Status:         UserStatusOnline,
		ConnectionType: ConnectionTypeWebSocket, // 明确设置为WebSocket连接
		SendChan:       make(chan []byte, bfSize),
		Context:        context.WithValue(context.Background(), ContextKeyUserID, userID),
		LastSeen:       now,
		LastHeartbeat:  now,
		NodeID:         idGenerator.GenerateCorrelationID() + random.FRandAlphaString(30),
	}
}

// createTestClientWithUserID 创建指定 UserID 的测试客户端
func createTestClientWithUserID(userId string, userType UserType, count int, bufferSize ...int) []*Client {
	clients := make([]*Client, 0, count)
	for range count {
		client := createTestClientWithIDGen(userType, bufferSize...)
		client.UserID = userId
		clients = append(clients, client)
	}
	return clients
}

// 辅助函数：批量注册客户端
func registerClients(hub *Hub, clients []*Client) {
	for _, client := range clients {
		hub.Register(client)
	}
}

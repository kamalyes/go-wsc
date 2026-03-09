/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-10 00:00:00
 * @FilePath: \go-wsc\hub\http_upgrade_test.go
 * @Description: HTTP WebSocket 升级处理测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/idgen"
	"github.com/stretchr/testify/assert"
)

// TestConfigureUpgrader 测试配置 WebSocket 升级器
func TestConfigureUpgrader(t *testing.T) {
	var size = 1028
	var wscConfig = wscconfig.Default()
	tests := []struct {
		name               string
		config             *wscconfig.WSC
		expectedBufferSize int
	}{
		{
			name: "自定义缓冲区大小",
			config: func() *wscconfig.WSC {
				cfg := wscConfig.WithMessageBufferSize(size)
				return cfg
			}(),
			expectedBufferSize: size,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := NewHub(tt.config)
			upgrader := hub.ConfigureUpgrader()

			assert.Equal(t, tt.expectedBufferSize, upgrader.ReadBufferSize, "ReadBufferSize 不匹配")
			assert.Equal(t, tt.expectedBufferSize, upgrader.WriteBufferSize, "WriteBufferSize 不匹配")
		})
	}
}

// TestConfigureUpgraderWithOrigins 测试带 Origin 检查的升级器配置
func TestConfigureUpgraderWithOrigins(t *testing.T) {
	tests := []struct {
		name           string
		allowedOrigins []string
		testOrigin     string
		shouldAllow    bool
	}{
		{"允许所有来源", []string{"*"}, "http://example.com", true},
		{"允许特定来源", []string{"http://example.com"}, "http://example.com", true},
		{"拒绝未授权来源", []string{"http://example.com"}, "http://evil.com", false},
		{"空配置允许所有", []string{}, "http://example.com", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := wscconfig.Default()
			config.WebSocketOrigins = tt.allowedOrigins
			hub := NewHub(config)
			upgrader := hub.ConfigureUpgrader()

			req := httptest.NewRequest("GET", "/ws", nil)
			req.Header.Set("Origin", tt.testOrigin)

			allowed := upgrader.CheckOrigin(req)
			assert.Equal(t, tt.shouldAllow, allowed, "CheckOrigin 结果不匹配")
		})
	}
}

// TestExtractClientAttributes 测试提取客户端属性
func TestExtractClientAttributes(t *testing.T) {
	idGenerator := idgen.NewDefaultIDGenerator()
	clientID := idGenerator.GenerateRequestID()
	userID := idGenerator.GenerateRequestID()
	deviceID := idGenerator.GenerateRequestID()

	// 场景1：从查询参数提取
	req := httptest.NewRequest("GET", "/ws?client_id="+clientID+"&user_id="+userID+"&user_type=customer", nil)
	hub := NewHub(wscconfig.Default())
	attrs := hub.extractClientAttributes(req)
	assert.Equal(t, clientID, attrs.ClientID)
	assert.Equal(t, userID, attrs.UserID)
	assert.Equal(t, UserTypeCustomer, attrs.UserType)

	// 场景2：从 Header 提取
	req = httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("X-Client-ID", clientID)
	req.Header.Set("X-User-ID", userID)
	req.Header.Set("X-User-Type", "agent")
	attrs = hub.extractClientAttributes(req)
	assert.Equal(t, clientID, attrs.ClientID)
	assert.Equal(t, userID, attrs.UserID)
	assert.Equal(t, UserTypeAgent, attrs.UserType)

	// 场景3：查询参数优先于 Header
	req = httptest.NewRequest("GET", "/ws?client_id="+clientID+"&user_id="+userID, nil)
	req.Header.Set("X-Client-ID", "ignored")
	req.Header.Set("X-User-ID", "ignored")
	attrs = hub.extractClientAttributes(req)
	assert.Equal(t, clientID, attrs.ClientID)
	assert.Equal(t, userID, attrs.UserID)

	// 场景4：ClientID 自动生成
	req = httptest.NewRequest("GET", "/ws?user_id="+userID+"&device_id="+deviceID+"&user_type=customer", nil)
	attrs = hub.extractClientAttributes(req)
	assert.NotEmpty(t, attrs.ClientID)
	assert.Equal(t, userID, attrs.UserID)
	assert.Equal(t, deviceID, attrs.DeviceID)

	// 场景5：UserType 默认值
	req = httptest.NewRequest("GET", "/ws", nil)
	attrs = hub.extractClientAttributes(req)
	assert.NotEmpty(t, attrs.ClientID)
	assert.Equal(t, UserTypeVisitor, attrs.UserType)

	// 场景6：自定义配置（仅从 header 提取）
	config := wscconfig.Default()
	config.ClientAttributes = &wscconfig.ClientAttributes{
		ClientIDSources: []wscconfig.AttributeSource{
			{Type: wscconfig.AttributeSourceHeader, Key: "X-Custom-Client-ID"},
		},
		UserIDSources: []wscconfig.AttributeSource{
			{Type: wscconfig.AttributeSourceHeader, Key: "X-Custom-User-ID"},
		},
	}
	hub = NewHub(config)
	req = httptest.NewRequest("GET", "/ws?client_id=ignored", nil)
	req.Header.Set("X-Custom-Client-ID", clientID)
	req.Header.Set("X-Custom-User-ID", userID)
	attrs = hub.extractClientAttributes(req)
	assert.Equal(t, clientID, attrs.ClientID)
	assert.Equal(t, userID, attrs.UserID)

	// 场景7：从 Cookie 提取
	config.ClientAttributes = &wscconfig.ClientAttributes{
		ClientIDSources: []wscconfig.AttributeSource{
			{Type: wscconfig.AttributeSourceCookie, Key: "cid"},
		},
		UserIDSources: []wscconfig.AttributeSource{
			{Type: wscconfig.AttributeSourceCookie, Key: "uid"},
		},
	}
	hub = NewHub(config)
	req = httptest.NewRequest("GET", "/ws", nil)
	req.AddCookie(&http.Cookie{Name: "cid", Value: clientID})
	req.AddCookie(&http.Cookie{Name: "uid", Value: userID})
	attrs = hub.extractClientAttributes(req)
	assert.Equal(t, clientID, attrs.ClientID)
	assert.Equal(t, userID, attrs.UserID)

	// 场景8：配置为 nil 时的向后兼容
	config = wscconfig.Default()
	config.ClientAttributes = nil
	hub = NewHub(config)
	req = httptest.NewRequest("GET", "/ws?client_id="+clientID+"&user_id="+userID, nil)
	attrs = hub.extractClientAttributes(req)
	assert.Equal(t, clientID, attrs.ClientID)
	assert.Equal(t, userID, attrs.UserID)
}

// TestCreateClientFromRequest 测试从请求创建客户端
func TestCreateClientFromRequest(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	// 随机生成测试数据
	idGenerator := idgen.NewDefaultIDGenerator()
	clientID := idGenerator.GenerateRequestID()
	userID := idGenerator.GenerateRequestID()
	userTypes := []string{"customer", "agent", "visitor", "admin"}
	userType := userTypes[len(clientID)%len(userTypes)]

	// 用于传递客户端信息的 channel
	clientChan := make(chan *Client, 1)

	// 创建模拟的 WebSocket 连接
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("WebSocket 升级失败: %v", err)
			return
		}
		defer conn.Close()

		// 提取客户端属性并创建客户端
		attrs := hub.extractClientAttributes(r)
		client := hub.CreateClientFromRequest(r, conn, attrs)
		clientChan <- client

		// 保持连接直到测试完成
		<-r.Context().Done()
	}))
	defer server.Close()

	// 连接到测试服务器
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?client_id=" + clientID + "&user_id=" + userID + "&user_type=" + userType
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	assert.NoError(t, err, "WebSocket 连接失败")
	defer conn.Close()

	// 从 channel 接收客户端信息
	client := <-clientChan

	// 验证客户端属性
	assert.Equal(t, clientID, client.ID, "client.ID 不匹配")
	assert.Equal(t, userID, client.UserID, "client.UserID 不匹配")
	assert.Equal(t, userType, string(client.UserType), "client.UserType 不匹配")
	assert.Equal(t, ConnectionTypeWebSocket, client.ConnectionType, "client.ConnectionType 不匹配")
	assert.Equal(t, hub.GetNodeID(), client.NodeID, "client.NodeID 不匹配")

	// 验证 SendChan 容量（根据配置的 Customer 容量）
	expectedCapacity := config.ClientCapacity.Customer
	assert.Equal(t, expectedCapacity, cap(client.SendChan), "client.SendChan 容量不匹配")
}

// TestHandleWebSocketUpgrade 测试 WebSocket 升级处理
func TestHandleWebSocketUpgrade(t *testing.T) {
	config := wscconfig.Default()
	hub := NewHub(config)

	// 启动 Hub
	go hub.Run()
	defer hub.Shutdown()

	// 等待 Hub 启动完成
	hub.WaitForStart()

	// 随机生成测试数据
	idGenerator := idgen.NewDefaultIDGenerator()
	clientID := idGenerator.GenerateRequestID()
	userID := idGenerator.GenerateRequestID()
	userTypes := []string{"customer", "agent", "visitor", "admin"}
	userType := userTypes[len(clientID)%len(userTypes)]

	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(hub.HandleWebSocketUpgrade))
	defer server.Close()

	// 连接到测试服务器
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "?client_id=" + clientID + "&user_id=" + userID + "&user_type=" + userType
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	assert.NoError(t, err, "WebSocket 连接失败")
	defer conn.Close()

	// 验证升级成功
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode, "StatusCode 不匹配")

	// 等待客户端注册（使用重试机制，最多等待 500ms）
	var clients []*Client
	for i := 0; i < 50; i++ {
		clients = hub.GetClientsCopy()
		if len(clients) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 验证客户端已注册
	assert.Len(t, clients, 1, "客户端数量不匹配")

	// 验证注册的客户端信息
	if len(clients) > 0 {
		client := clients[0]
		assert.Equal(t, clientID, client.ID, "client.ID 不匹配")
		assert.Equal(t, userID, client.UserID, "client.UserID 不匹配")
		assert.Equal(t, userType, string(client.UserType), "client.UserType 不匹配")
	}
}

// TestHandleWebSocketUpgradeWithInvalidOrigin 测试拒绝无效 Origin
func TestHandleWebSocketUpgradeWithInvalidOrigin(t *testing.T) {
	config := wscconfig.Default()
	config.WebSocketOrigins = []string{"http://allowed.com"}
	hub := NewHub(config)

	server := httptest.NewServer(http.HandlerFunc(hub.HandleWebSocketUpgrade))
	defer server.Close()

	// 尝试使用无效 Origin 连接
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	headers := http.Header{}
	headers.Set("Origin", "http://evil.com")

	_, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	assert.Error(t, err, "期望连接失败")

	if resp != nil {
		assert.NotEqual(t, http.StatusSwitchingProtocols, resp.StatusCode, "不应该允许无效 Origin 的连接")
	}
}

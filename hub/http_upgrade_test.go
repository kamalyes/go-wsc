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
	tests := []struct {
		name               string
		config             *wscconfig.WSC
		expectedBufferSize int
	}{
		{"默认配置", &wscconfig.WSC{MessageBufferSize: 0}, 1024},
		{"自定义缓冲区大小", &wscconfig.WSC{MessageBufferSize: 2048}, 2048},
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
			config := &wscconfig.WSC{WebSocketOrigins: tt.allowedOrigins}
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
	userTypes := []string{"customer", "agent", "visitor", "admin"}
	userType := userTypes[len(clientID)%len(userTypes)]

	queryClientID := idGenerator.GenerateRequestID()
	queryUserID := idGenerator.GenerateRequestID()
	headerClientID := idGenerator.GenerateRequestID()
	headerUserID := idGenerator.GenerateRequestID()

	tests := []struct {
		name             string
		queryParams      map[string]string
		headers          map[string]string
		expectedClientID string
		expectedUserID   string
		expectedUserType string
	}{
		{"从查询参数提取", map[string]string{"client_id": clientID, "user_id": userID, "user_type": userType}, map[string]string{}, clientID, userID, userType},
		{"从 Header 提取", map[string]string{}, map[string]string{"X-Client-ID": clientID, "X-User-ID": userID, "X-User-Type": userType}, clientID, userID, userType},
		{"查询参数优先于 Header", map[string]string{"client_id": queryClientID, "user_id": queryUserID}, map[string]string{"X-Client-ID": headerClientID, "X-User-ID": headerUserID}, queryClientID, queryUserID, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := "/ws?"
			params := []string{}
			for k, v := range tt.queryParams {
				params = append(params, k+"="+v)
			}
			url += strings.Join(params, "&")

			req := httptest.NewRequest("GET", url, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			hub := NewHub(&wscconfig.WSC{})
			extractedClientID, extractedUserID, extractedUserType := hub.extractClientAttributes(req)

			assert.Equal(t, tt.expectedClientID, extractedClientID, "clientID 不匹配")
			assert.Equal(t, tt.expectedUserID, extractedUserID, "userID 不匹配")
			assert.Equal(t, tt.expectedUserType, extractedUserType, "userType 不匹配")
		})
	}
}

// TestExtractClientAttributesWithConfig 测试使用配置提取客户端属性
func TestExtractClientAttributesWithConfig(t *testing.T) {
	idGenerator := idgen.NewDefaultIDGenerator()
	clientID := idGenerator.GenerateRequestID()
	userID := idGenerator.GenerateRequestID()
	userType := "customer"

	tests := []struct {
		name             string
		config           *wscconfig.ClientAttributes
		queryParams      map[string]string
		headers          map[string]string
		cookies          map[string]string
		expectedClientID string
		expectedUserID   string
		expectedUserType string
	}{
		{
			name:             "使用默认配置（query优先）",
			config:           wscconfig.DefaultClientAttributes(),
			queryParams:      map[string]string{"client_id": clientID, "user_id": userID, "user_type": userType},
			headers:          map[string]string{},
			cookies:          map[string]string{},
			expectedClientID: clientID,
			expectedUserID:   userID,
			expectedUserType: userType,
		},
		{
			name:             "使用默认配置（header作为备选）",
			config:           wscconfig.DefaultClientAttributes(),
			queryParams:      map[string]string{},
			headers:          map[string]string{"X-Client-ID": clientID, "X-User-ID": userID, "X-User-Type": userType},
			cookies:          map[string]string{},
			expectedClientID: clientID,
			expectedUserID:   userID,
			expectedUserType: userType,
		},
		{
			name: "自定义配置（仅从header提取）",
			config: &wscconfig.ClientAttributes{
				ClientIDSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceHeader, Key: "X-Custom-Client-ID"},
				},
				UserIDSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceHeader, Key: "X-Custom-User-ID"},
				},
				UserTypeSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceHeader, Key: "X-Custom-User-Type"},
				},
			},
			queryParams:      map[string]string{"client_id": "should-be-ignored", "user_id": "should-be-ignored"},
			headers:          map[string]string{"X-Custom-Client-ID": clientID, "X-Custom-User-ID": userID, "X-Custom-User-Type": userType},
			cookies:          map[string]string{},
			expectedClientID: clientID,
			expectedUserID:   userID,
			expectedUserType: userType,
		},
		{
			name: "多来源配置（按优先级）",
			config: &wscconfig.ClientAttributes{
				ClientIDSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceQuery, Key: "cid"},
					{Type: wscconfig.AttributeSourceHeader, Key: "X-Client-ID"},
					{Type: wscconfig.AttributeSourceCookie, Key: "client_id"},
				},
				UserIDSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceQuery, Key: "uid"},
					{Type: wscconfig.AttributeSourceHeader, Key: "X-User-ID"},
				},
				UserTypeSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceQuery, Key: "type"},
				},
			},
			queryParams:      map[string]string{"cid": clientID, "uid": userID, "type": userType},
			headers:          map[string]string{"X-Client-ID": "should-be-ignored", "X-User-ID": "should-be-ignored"},
			cookies:          map[string]string{},
			expectedClientID: clientID,
			expectedUserID:   userID,
			expectedUserType: userType,
		},
		{
			name: "从Cookie提取",
			config: &wscconfig.ClientAttributes{
				ClientIDSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceCookie, Key: "session_client_id"},
				},
				UserIDSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceCookie, Key: "session_user_id"},
				},
				UserTypeSources: []wscconfig.AttributeSource{
					{Type: wscconfig.AttributeSourceCookie, Key: "session_user_type"},
				},
			},
			queryParams:      map[string]string{},
			headers:          map[string]string{},
			cookies:          map[string]string{"session_client_id": clientID, "session_user_id": userID, "session_user_type": userType},
			expectedClientID: clientID,
			expectedUserID:   userID,
			expectedUserType: userType,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 构建 URL
			url := "/ws?"
			params := []string{}
			for k, v := range tt.queryParams {
				params = append(params, k+"="+v)
			}
			url += strings.Join(params, "&")

			// 创建请求
			req := httptest.NewRequest("GET", url, nil)

			// 设置 Headers
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}

			// 设置 Cookies
			for k, v := range tt.cookies {
				req.AddCookie(&http.Cookie{Name: k, Value: v})
			}

			// 创建带配置的 Hub
			config := wscconfig.Default()
			config.ClientAttributes = tt.config
			hub := NewHub(config)

			// 提取属性
			extractedClientID, extractedUserID, extractedUserType := hub.extractClientAttributes(req)

			// 验证结果
			assert.Equal(t, tt.expectedClientID, extractedClientID, "clientID 不匹配")
			assert.Equal(t, tt.expectedUserID, extractedUserID, "userID 不匹配")
			assert.Equal(t, tt.expectedUserType, extractedUserType, "userType 不匹配")
		})
	}
}

// TestExtractClientAttributesWithNilConfig 测试配置为 nil 时的向后兼容性
func TestExtractClientAttributesWithNilConfig(t *testing.T) {
	idGenerator := idgen.NewDefaultIDGenerator()
	clientID := idGenerator.GenerateRequestID()
	userID := idGenerator.GenerateRequestID()
	userType := "customer"

	// 创建不带 ClientAttributes 配置的 Hub
	config := &wscconfig.WSC{
		ClientAttributes: nil, // 显式设置为 nil
	}
	hub := NewHub(config)

	// 测试默认提取逻辑（向后兼容）
	req := httptest.NewRequest("GET", "/ws?client_id="+clientID+"&user_id="+userID+"&user_type="+userType, nil)

	extractedClientID, extractedUserID, extractedUserType := hub.extractClientAttributes(req)

	assert.Equal(t, clientID, extractedClientID, "clientID 不匹配")
	assert.Equal(t, userID, extractedUserID, "userID 不匹配")
	assert.Equal(t, userType, extractedUserType, "userType 不匹配")
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

		// 创建客户端并发送到 channel
		client := hub.CreateClientFromRequest(r, conn)
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
	assert.Equal(t, 256, cap(client.SendChan), "client.SendChan 容量不匹配")
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
	config := &wscconfig.WSC{
		WebSocketOrigins: []string{"http://allowed.com"},
	}
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

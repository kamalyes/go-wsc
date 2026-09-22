/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-19 10:28:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-19 10:28:00
 * @FilePath: \go-wsc\transport\upgrader_test.go
 * @Description: WebSocket 升级器测试 - 纯函数 / Origin 白名单 / 属性提取（明文+Token）/ 客户端构造 / 升级编排
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/messaging"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/spi"
)

// ============================================================================
// 纯函数
// ============================================================================

// TestParseGroupIDs 群组ID逗号解析（单值/多值/空串/空白容错/全空白）
func TestParseGroupIDs(t *testing.T) {
	assert.Equal(t, []string{"g-1"}, parseGroupIDs("g-1"), "单值应保持列表形式")
	assert.Equal(t, []string{"g1", "g2", "g3"}, parseGroupIDs("g1,g2,g3"), "多值应逗号拆分")
	assert.Nil(t, parseGroupIDs(""), "空串应返回 nil（默认组兜底）")
	assert.Equal(t, []string{"g1", "g2", "g3"}, parseGroupIDs(" g1 , g2 , g3 "), "空白应 TrimSpace 容错")
	assert.Nil(t, parseGroupIDs(" , , "), "全空白片段应过滤为 nil")
}

// TestNewUpgraderPanicOnNilConfig nil 配置 fail-fast（与 NewHub 同策略）
func TestNewUpgraderPanicOnNilConfig(t *testing.T) {
	assert.Panics(t, func() { NewUpgrader(nil, &fakeRegistrar{}) })
}

// ============================================================================
// 升级器配置
// ============================================================================

// TestConfigureUpgraderDefaultOrigin 默认配置：允许所有来源 + 缓冲区取配置
func TestConfigureUpgraderDefaultOrigin(t *testing.T) {
	cfg := newUpgraderTestConfig()
	u := NewUpgrader(cfg, &fakeRegistrar{})

	up := u.ConfigureUpgrader()
	require.NotNil(t, up)
	assert.Equal(t, 1024, up.ReadBufferSize)
	assert.Equal(t, 1024, up.WriteBufferSize)

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	assert.True(t, up.CheckOrigin(req), "默认应允许所有来源")
}

// TestConfigureUpgraderOriginWhitelist 白名单：精确匹配 / 通配 / 未匹配拒绝
func TestConfigureUpgraderOriginWhitelist(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.WebSocketOrigins = []string{"https://trusted.example.com", "*"}
	u := NewUpgrader(cfg, &fakeRegistrar{})
	up := u.ConfigureUpgrader()

	trusted := httptest.NewRequest(http.MethodGet, "/ws", nil)
	trusted.Header.Set("Origin", "https://trusted.example.com")
	assert.True(t, up.CheckOrigin(trusted), "白名单来源应通过")

	unknown := httptest.NewRequest(http.MethodGet, "/ws", nil)
	unknown.Header.Set("Origin", "https://evil.example.com")
	assert.True(t, up.CheckOrigin(unknown), "白名单含 * 时未知来源应通过")

	cfgStrict := newUpgraderTestConfig()
	cfgStrict.WebSocketOrigins = []string{"https://trusted.example.com"}
	strict := NewUpgrader(cfgStrict, &fakeRegistrar{}).ConfigureUpgrader()
	assert.False(t, strict.CheckOrigin(unknown), "无通配白名单应拒绝未知来源")
}

// ============================================================================
// 客户端属性提取
// ============================================================================

// TestExtractClientAttributesPlaintext 明文提取：query 全量来源 + 显式 client_id 优先
func TestExtractClientAttributesPlaintext(t *testing.T) {
	u := NewUpgrader(newUpgraderTestConfig(), &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet,
		"/ws?client_id=c-1&user_id=u-1&user_type=agent&device_id=d-1&app_id=app-1&namespace=ns-1&group_id=g1,g2,g3", nil)
	attrs := u.ExtractClientAttributes(req)

	require.NotNil(t, attrs)
	assert.Equal(t, "c-1", attrs.ClientID, "显式 client_id 应优先不被哈希覆盖")
	assert.Equal(t, "u-1", attrs.UserID)
	assert.Equal(t, models.UserTypeAgent, attrs.UserType)
	assert.Equal(t, "d-1", attrs.DeviceID)
	assert.Equal(t, "app-1", attrs.AppID)
	assert.Equal(t, "ns-1", attrs.Namespace)
	assert.Equal(t, "g1,g2,g3", attrs.GroupID)
	assert.Equal(t, []string{"g1", "g2", "g3"}, attrs.GroupIDs, "多群组应逗号解析")
}

// TestExtractClientAttributesDefaults 缺省兜底：user_type 默认 visitor，client_id 时间窗口哈希生成
func TestExtractClientAttributesDefaults(t *testing.T) {
	u := NewUpgrader(newUpgraderTestConfig(), &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/ws?user_id=u-2&device_id=d-2", nil)
	attrs := u.ExtractClientAttributes(req)

	require.NotNil(t, attrs)
	assert.Equal(t, models.UserTypeVisitor, attrs.UserType, "缺省 user_type 应兜底 visitor")
	assert.NotEmpty(t, attrs.ClientID, "缺省 client_id 应由时间窗口哈希生成")
	assert.Nil(t, attrs.GroupIDs, "无 group_id 应返回 nil（默认组兜底）")
}

// TestExtractClientAttributesHeaderSources header 来源提取（X-Client-ID / X-User-ID）
func TestExtractClientAttributesHeaderSources(t *testing.T) {
	u := NewUpgrader(newUpgraderTestConfig(), &fakeRegistrar{})

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("X-Client-ID", "c-hdr")
	req.Header.Set("X-User-ID", "u-hdr")
	attrs := u.ExtractClientAttributes(req)

	require.NotNil(t, attrs)
	assert.Equal(t, "c-hdr", attrs.ClientID)
	assert.Equal(t, "u-hdr", attrs.UserID)
}

// TestExtractClientAttributesFromToken 连接 Token 启用：claims 提取 + 哈希 clientID + 群组解析
// AppID 归属以密钥命中的 set 为准（载荷 aid 仅供追溯，见 token.go Authenticate）
func TestExtractClientAttributesFromToken(t *testing.T) {
	cfg := newUpgraderTestConfig()
	tokenCfg := &wscconfig.ConnectionToken{
		Enabled:        true,
		DefaultAppID:   "app-token",
		TokenParamName: "token",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"app-token": {SigningKey: "upgrader-token-key-0123456789abcdef"},
		},
	}
	cfg.Security.ConnectionToken = tokenCfg

	auth := NewTokenAuthenticator(tokenCfg, nil)
	u := NewUpgrader(cfg, &fakeRegistrar{}).WithAuthenticator(auth)

	token, err := IssueConnectionToken(tokenCfg, "app-token", &spi.ConnectionClaims{
		UserID:    "u-token",
		UserType:  "agent",
		DeviceID:  "d-token",
		AppID:     "app-token",
		Namespace: "ns-token",
		GroupID:   "g1,g2",
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ws?token="+token, nil)
	attrs := u.ExtractClientAttributes(req)

	require.NotNil(t, attrs)
	assert.Equal(t, "u-token", attrs.UserID)
	assert.Equal(t, models.UserTypeAgent, attrs.UserType)
	assert.Equal(t, "d-token", attrs.DeviceID)
	assert.Equal(t, "app-token", attrs.AppID, "归属应为密钥命中的 appid")
	assert.Equal(t, "ns-token", attrs.Namespace)
	assert.Equal(t, []string{"g1", "g2"}, attrs.GroupIDs)
	assert.NotEmpty(t, attrs.ClientID, "Token 路径 clientID 应由时间窗口哈希生成")
	assert.Empty(t, req.URL.Query().Get("user_id"), "身份应来自加密载荷而非明文参数")
}

// TestExtractClientAttributesTokenFallback Token 解码失败 + AllowFallback → 回退明文
func TestExtractClientAttributesTokenFallback(t *testing.T) {
	cfg := newUpgraderTestConfig()
	tokenCfg := newTestTokenConfig("fallback-key-0123456789abcdef")
	tokenCfg.AllowFallback = true
	cfg.Security.ConnectionToken = tokenCfg

	u := NewUpgrader(cfg, &fakeRegistrar{}).WithAuthenticator(NewTokenAuthenticator(tokenCfg, nil))

	req := httptest.NewRequest(http.MethodGet, "/ws?token=tampered&user_id=u-plain", nil)
	attrs := u.ExtractClientAttributes(req)

	require.NotNil(t, attrs)
	assert.Equal(t, "u-plain", attrs.UserID, "解码失败应回退明文提取")
}

// TestExtractClientAttributesTokenReject Token 解码失败 + 禁止回退 → 拒绝（nil）
func TestExtractClientAttributesTokenReject(t *testing.T) {
	cfg := newUpgraderTestConfig()
	tokenCfg := newTestTokenConfig("reject-key-0123456789abcdef")
	cfg.Security.ConnectionToken = tokenCfg

	u := NewUpgrader(cfg, &fakeRegistrar{}).WithAuthenticator(NewTokenAuthenticator(tokenCfg, nil))

	req := httptest.NewRequest(http.MethodGet, "/ws?token=tampered&user_id=u-plain", nil)
	assert.Nil(t, u.ExtractClientAttributes(req), "解码失败且禁止回退应返回 nil")
}

// ============================================================================
// 客户端构造
// ============================================================================

// TestCreateClientFromRequest 全字段构造：元数据 / 节点信息 / 隔离维度 / 连接级 ctx
func TestCreateClientFromRequest(t *testing.T) {
	cfg := newUpgraderTestConfig()
	reg := &fakeRegistrar{}
	u := NewUpgrader(cfg, reg).WithNodeID("node-1")

	sConn, cConn := newWSConnPair(t)
	defer cConn.Close()
	defer sConn.Close()

	req := httptest.NewRequest(http.MethodGet, "/ws?user_id=u-9&user_type=customer&device_id=d-9&app_id=app-9&namespace=ns-9", nil)
	req.Header.Set("User-Agent", "unit-test-agent")
	attrs := u.ExtractClientAttributes(req)
	require.NotNil(t, attrs)

	client := u.CreateClientFromRequest(req, sConn, attrs)

	// 基础属性
	assert.Equal(t, attrs.ClientID, client.ID)
	assert.Equal(t, "u-9", client.UserID)
	assert.Equal(t, models.UserTypeCustomer, client.UserType)
	assert.Equal(t, models.ConnectionTypeWebSocket, client.ConnectionType)
	// 节点信息
	assert.Equal(t, "node-1", client.NodeID)
	// 隔离维度（归一化前透传，注册路径统一归一化）
	assert.Equal(t, "app-9", client.AppID)
	assert.Equal(t, "ns-9", client.Namespace)
	// 元数据（含 x-device-id 注入）
	assert.Equal(t, "d-9", client.Metadata["x-device-id"], "deviceID 应注入元数据")
	assert.Equal(t, "unit-test-agent", client.Metadata["user_agent"])
	// 连接级 ctx：SenderID 注入
	require.NotNil(t, client.Context)
	assert.Equal(t, "u-9", client.Context.Value(messaging.ContextKeySenderID))
}

// TestCreateClientFromRequestTraceID 请求 ctx 中的 trace_id 应注入连接级 ctx
func TestCreateClientFromRequestTraceID(t *testing.T) {
	u := NewUpgrader(newUpgraderTestConfig(), &fakeRegistrar{})

	sConn, cConn := newWSConnPair(t)
	defer cConn.Close()
	defer sConn.Close()

	req := httptest.NewRequest(http.MethodGet, "/ws?user_id=u-tr", nil)
	traced := logger.ContextWithTraceID(req.Context(), "trace-abc-123")
	req = req.WithContext(traced)

	attrs := u.ExtractClientAttributes(req)
	require.NotNil(t, attrs)
	client := u.CreateClientFromRequest(req, sConn, attrs)

	assert.Equal(t, "trace-abc-123", logger.ExtractTraceID(client.Context), "连接级 ctx 应携带请求 trace_id")
}

// ============================================================================
// 升级编排（真握手链路）
// ============================================================================

// TestHandleWebSocketUpgradeSuccess 完整链路：握手升级 → 响应头 → 客户端构造 → 异步注册
func TestHandleWebSocketUpgradeSuccess(t *testing.T) {
	cfg := newUpgraderTestConfig()
	reg := &fakeRegistrar{}
	u := NewUpgrader(cfg, reg).WithNodeID("node-ws")
	srv := httptest.NewServer(http.HandlerFunc(u.HandleWebSocketUpgrade))
	defer srv.Close()

	dialURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "?user_id=u-ws&user_type=customer"
	conn, resp, err := websocket.DefaultDialer.Dial(dialURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	// 响应头：服务端分配的客户端ID + 节点信息
	require.NotNil(t, resp)
	assert.Equal(t, "node-ws", resp.Header.Get("X-WSC-Node-ID"))
	clientID := resp.Header.Get("X-WSC-Client-ID")
	assert.NotEmpty(t, clientID)

	// 异步注册回调
	require.Eventually(t, func() bool {
		regCnt, _, _, _ := reg.counts()
		return regCnt == 1
	}, 2*time.Second, 10*time.Millisecond, "升级成功应触发一次异步注册")

	client := reg.lastRegistered()
	require.NotNil(t, client)
	assert.Equal(t, clientID, client.ID, "响应头回显的 clientID 应与注册客户端一致")
	assert.Equal(t, "u-ws", client.UserID)
	assert.NotNil(t, client.Conn, "WS 客户端应持有连接")
}

// TestHandleWebSocketUpgradeRegisteredMessage 配置启用注册确认消息 → 编排层确认回调
func TestHandleWebSocketUpgradeRegisteredMessage(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.ResponseHeaders.SendRegisteredMessage = true
	reg := &fakeRegistrar{}
	u := NewUpgrader(cfg, reg)
	srv := httptest.NewServer(http.HandlerFunc(u.HandleWebSocketUpgrade))
	defer srv.Close()

	dialURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "?user_id=u-msg&user_type=customer"
	conn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	require.NoError(t, err)
	defer conn.Close()

	require.Eventually(t, func() bool {
		_, _, _, confirmCnt := reg.counts()
		return confirmCnt == 1
	}, 2*time.Second, 10*time.Millisecond, "配置启用时应回调一次确认消息")
}

// TestHandleWebSocketUpgradeRejected 连接验证失败 → 拒绝握手（非 101）
func TestHandleWebSocketUpgradeRejected(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.ConnectionValidation.Enabled = true
	cfg.ConnectionValidation.RequireUserID = true
	reg := &fakeRegistrar{}
	u := NewUpgrader(cfg, reg)
	srv := httptest.NewServer(http.HandlerFunc(u.HandleWebSocketUpgrade))
	defer srv.Close()

	// 无 user_id 参数
	dialURL := strings.Replace(srv.URL, "http://", "ws://", 1)
	_, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	require.Error(t, err, "验证失败应拒绝升级")
	assert.Regexp(t, "bad handshake", err.Error())

	regCnt, _, _, _ := reg.counts()
	assert.Zero(t, regCnt, "被拒连接不应触发注册")
}

// TestHandleWebSocketUpgradeShutdown 编排层关闭中 → 拒绝新连接
func TestHandleWebSocketUpgradeShutdown(t *testing.T) {
	cfg := newUpgraderTestConfig()
	reg := &fakeRegistrar{}
	reg.shutdown.Store(true)
	u := NewUpgrader(cfg, reg)
	srv := httptest.NewServer(http.HandlerFunc(u.HandleWebSocketUpgrade))
	defer srv.Close()

	dialURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "?user_id=u-off&user_type=customer"
	_, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	require.Error(t, err, "关闭中应拒绝升级")

	regCnt, _, _, _ := reg.counts()
	assert.Zero(t, regCnt)
}

// TestHandleWebSocketUpgradeHealthCheck 健康检查：升级即关闭（CloseImmediately），不注册客户端
func TestHandleWebSocketUpgradeHealthCheck(t *testing.T) {
	cfg := newUpgraderTestConfig()
	cfg.HealthCheck.Enabled = true
	cfg.HealthCheck.SendResponseMessage = true
	reg := &fakeRegistrar{}
	u := NewUpgrader(cfg, reg)
	srv := httptest.NewServer(http.HandlerFunc(u.HandleWebSocketUpgrade))
	defer srv.Close()

	dialURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "?health=true"
	conn, _, err := websocket.DefaultDialer.Dial(dialURL, nil)
	require.NoError(t, err, "健康检查请求应升级成功")
	defer conn.Close()

	// 响应消息可读回（SendResponseMessage=true）
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := conn.ReadMessage()
	require.NoError(t, err)
	assert.Contains(t, string(data), "health_check")

	// CloseImmediately=true → 服务端随后关闭，客户端读失败
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _, err = conn.ReadMessage()
	require.Error(t, err, "CloseImmediately 应使服务端升级后立即关闭")

	regCnt, _, _, _ := reg.counts()
	assert.Zero(t, regCnt, "健康检查不应创建客户端")
}

// newWSConnPair 建立真 WebSocket 连接对（服务端/客户端各持一端，客户端构造测试用）
// 与 connection 包同款语义：阻塞等待服务端就绪，避免 Dial 返回与 handler 投递竞态
func newWSConnPair(t testing.TB) (serverConn, clientConn *websocket.Conn) {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	serverConnCh := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverConnCh <- c
	}))
	t.Cleanup(srv.Close)

	var err error
	clientConn, _, err = websocket.DefaultDialer.Dial(strings.Replace(srv.URL, "http://", "ws://", 1), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientConn.Close() })

	select {
	case serverConn = <-serverConnCh:
	case <-time.After(2 * time.Second):
		t.Fatal("服务端连接未就绪")
	}
	return serverConn, clientConn
}

// TestUpgraderDefaultContext 未注入编排 ctx 时连接级 ctx 应派生自 Background（防御验证）
func TestUpgraderDefaultContext(t *testing.T) {
	u := NewUpgrader(newUpgraderTestConfig(), &fakeRegistrar{})
	assert.NotNil(t, u.hubCtx)

	// 显式注入生效
	ctx := context.WithValue(context.Background(), struct{ k string }{k: "x"}, 1)
	u.WithHubContext(ctx)
	assert.Equal(t, ctx, u.hubCtx)
}

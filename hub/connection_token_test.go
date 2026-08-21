/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-18 00:00:00
 * @LastEditTime: 2026-07-21 00:00:00
 * @FilePath: \go-wsc\hub\connection_token_test.go
 * @Description: 连接 Token 编解码测试 - 覆盖 Namespace/GroupID 字段
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestTokenCfg 构造测试用的 ConnectionToken 配置（不启用 Redis）
func newTestTokenCfg() *wscconfig.ConnectionToken {
	return &wscconfig.ConnectionToken{
		Enabled:        true,
		SigningKey:     "test-secret-key",
		Algorithm:      "HS256",
		ExpiresTime:    time.Hour,
		TokenSource:    "query",
		TokenParamName: "token",
	}
}

// TestConnectionTokenWithNamespace 测试 Token 携带 Namespace 的编解码
func TestConnectionTokenWithNamespace(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID:    "user-001",
		UserType:  "agent",
		DeviceID:  "device-001",
		Namespace: "tenantA",
	}

	token, err := IssueConnectionToken(context.Background(), cfg, "", nil, claims)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// 解码验证
	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, claims.UserID, decoded.UserID)
	assert.Equal(t, claims.UserType, decoded.UserType)
	assert.Equal(t, claims.DeviceID, decoded.DeviceID)
	assert.Equal(t, claims.Namespace, decoded.Namespace, "Namespace 应一致")
}

// TestConnectionTokenWithoutNamespace 测试 Token 不带 Namespace 时解码为空
func TestConnectionTokenWithoutNamespace(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID: "user-002",
	}

	token, err := IssueConnectionToken(context.Background(), cfg, "", nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "user-002", decoded.UserID)
	assert.Empty(t, decoded.Namespace, "未设置 Namespace 时应为空")
}

// TestConnectionTokenFromHeader 测试从 Header 提取 Token 并还原 Namespace
func TestConnectionTokenFromHeader(t *testing.T) {
	cfg := newTestTokenCfg()
	cfg.TokenSource = "header"
	cfg.TokenParamName = "X-Connection-Token"

	claims := &ConnectionClaims{
		UserID:    "user-003",
		Namespace: "tenantB",
	}

	token, err := IssueConnectionToken(context.Background(), cfg, "", nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("X-Connection-Token", token)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "tenantB", decoded.Namespace)
}

// TestConnectionTokenWithGroupID 测试 Token 携带 GroupID 的编解码
func TestConnectionTokenWithGroupID(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID:    "user-gid-001",
		UserType:  "customer",
		DeviceID:  "device-gid-001",
		Namespace: "tenantA",
		GroupID:   "chat-room-42",
	}

	token, err := IssueConnectionToken(context.Background(), cfg, "", nil, claims)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, claims.UserID, decoded.UserID)
	assert.Equal(t, claims.UserType, decoded.UserType)
	assert.Equal(t, claims.DeviceID, decoded.DeviceID)
	assert.Equal(t, claims.Namespace, decoded.Namespace, "Namespace 应一致")
	assert.Equal(t, claims.GroupID, decoded.GroupID, "GroupID 应一致")
}

// TestConnectionTokenWithoutGroupID 测试 Token 不带 GroupID 时解码为空
func TestConnectionTokenWithoutGroupID(t *testing.T) {
	cfg := newTestTokenCfg()

	claims := &ConnectionClaims{
		UserID:    "user-gid-002",
		Namespace: "tenantB",
	}

	token, err := IssueConnectionToken(context.Background(), cfg, "", nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "user-gid-002", decoded.UserID)
	assert.Empty(t, decoded.GroupID, "未设置 GroupID 时应为空")
}

// TestConnectionTokenGroupIDAndNamespaceCombo 测试 Token 同时携带 Namespace 和 GroupID 的组合场景
func TestConnectionTokenGroupIDAndNamespaceCombo(t *testing.T) {
	cfg := newTestTokenCfg()

	tests := []struct {
		name      string
		namespace string
		groupID   string
	}{
		{"有命名空间有群组", "tenantA", "room-1"},
		{"有命名空间无群组", "tenantB", ""},
		{"无命名空间有群组", "", "room-2"},
		{"无命名空间无群组", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := &ConnectionClaims{
				UserID:    "user-combo",
				Namespace: tt.namespace,
				GroupID:   tt.groupID,
			}

			token, err := IssueConnectionToken(context.Background(), cfg, "", nil, claims)
			require.NoError(t, err)

			decoder := NewConnectionTokenDecoder(cfg, nil, nil)
			req := httptest.NewRequest("GET", "/ws?token="+token, nil)
			decoded, err := decoder.Decode(req)
			require.NoError(t, err)
			assert.Equal(t, tt.namespace, decoded.Namespace, "Namespace 应一致")
			assert.Equal(t, tt.groupID, decoded.GroupID, "GroupID 应一致")
		})
	}
}

// ============================================================================
// 多 appid 连接 Token 测试
// ============================================================================

// newMultiAppTokenCfg 构造多 appid 测试配置（两套独立密钥）
func newMultiAppTokenCfg() *wscconfig.ConnectionToken {
	return &wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "default-secret", Algorithm: "HS256", ExpiresTime: time.Hour},
			"app-A":   {SigningKey: "appA-secret", Algorithm: "HS512", ExpiresTime: time.Hour},
		},
	}
}

// TestMultiAppTokenDecode_RouteByAppID claims.aid=A 用 A 的密钥验签成功
func TestMultiAppTokenDecode_RouteByAppID(t *testing.T) {
	cfg := newMultiAppTokenCfg()

	// 用 app-A 的密钥签发，claims 带 aid=app-A
	claims := &ConnectionClaims{UserID: "u-A", AppID: "app-A"}
	token, err := IssueConnectionToken(context.Background(), cfg, "app-A", nil, claims)
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "u-A", decoded.UserID)
	assert.Equal(t, "app-A", decoded.AppID, "应路由到 app-A")
}

// TestMultiAppTokenDecode_DefaultFallback claims.aid 为空时走 default 兜底
func TestMultiAppTokenDecode_DefaultFallback(t *testing.T) {
	cfg := newMultiAppTokenCfg()

	// 用 default 密钥签发，claims 不带 aid（空）
	claims := &ConnectionClaims{UserID: "u-default"} // AppID 为空
	token, err := IssueConnectionToken(context.Background(), cfg, "default", nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "u-default", decoded.UserID)
	assert.Equal(t, "default", decoded.AppID, "空 aid 应走 default 兜底并回填")
}

// TestMultiAppTokenDecode_UnknownAppID_FallbackToDefault 未知 aid 走 default 兜底（若 default 密钥能验）
func TestMultiAppTokenDecode_UnknownAppID_FallbackToDefault(t *testing.T) {
	cfg := newMultiAppTokenCfg()

	// 用 default 密钥签发，但 claims 带 aid=unknown（不在 tokens 中）
	claims := &ConnectionClaims{UserID: "u-x", AppID: "unknown"}
	token, err := IssueConnectionToken(context.Background(), cfg, "default", nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	// aid=unknown 走 default 兜底，default 密钥能验签 → 成功
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "u-x", decoded.UserID)
}

// TestMultiAppTokenDecode_WrongKey 用 A 的 token 验 B 的密钥失败
func TestMultiAppTokenDecode_WrongKey(t *testing.T) {
	cfg := newMultiAppTokenCfg()

	// 用 app-A 密钥签发
	claims := &ConnectionClaims{UserID: "u-A", AppID: "app-A"}
	token, err := IssueConnectionToken(context.Background(), cfg, "app-A", nil, claims)
	require.NoError(t, err)

	// 构造一个只有 default 密钥的 decoder，app-A 的 token 用 default 密钥验签应失败
	cfgDefaultOnly := &wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "default-secret", Algorithm: "HS256", ExpiresTime: time.Hour},
		},
	}
	decoder := NewConnectionTokenDecoder(cfgDefaultOnly, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	_, err = decoder.Decode(req)
	assert.Error(t, err, "app-A 的 token 在仅含 default 的 decoder 上应验签失败")
}

// TestMultiAppTokenDecode_RedisWhitelistPerApp 每 appid 独立 Redis 前缀
func TestMultiAppTokenDecode_RedisWhitelistPerApp(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cfg := &wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "d-secret", Algorithm: "HS256", ExpiresTime: time.Hour, UseRedis: true, RedisKeyPrefix: "wsc:d:"},
			"app-A":   {SigningKey: "a-secret", Algorithm: "HS256", ExpiresTime: time.Hour, UseRedis: true, RedisKeyPrefix: "wsc:a:"},
		},
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	// 用 app-A 签发 → 写入 wsc:a: 前缀
	tokenA, err := IssueConnectionToken(context.Background(), cfg, "app-A", rdb, &ConnectionClaims{UserID: "u-A", AppID: "app-A"})
	require.NoError(t, err)
	assert.True(t, mr.Exists(whitelistKey("wsc:a:", tokenA)), "app-A token 应在 wsc:a: 前缀下")

	// app-A token 的白名单 key 不应在 default 前缀下
	assert.False(t, mr.Exists(whitelistKey("wsc:d:", tokenA)), "app-A token 不应在 default 前缀下")

	// app-A token 在带 Redis 的 decoder 上 Decode 成功
	decoder := NewConnectionTokenDecoder(cfg, rdb, nil)
	req := httptest.NewRequest("GET", "/ws?token="+tokenA, nil)
	_, err = decoder.Decode(req)
	require.NoError(t, err, "app-A token 在白名单中应 Decode 成功")

	// 手动删除 app-A 前缀的白名单 → Decode 应被拒
	mr.Del(whitelistKey("wsc:a:", tokenA))
	req2 := httptest.NewRequest("GET", "/ws?token="+tokenA, nil)
	_, err = decoder.Decode(req2)
	assert.Error(t, err, "删除 app-A 前缀白名单后应被拒绝")
}

// TestMultiAppTokenDecode_MalformedToken malformed token 错误分类
func TestMultiAppTokenDecode_MalformedToken(t *testing.T) {
	cfg := newMultiAppTokenCfg()
	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token=not.a.valid.jwt", nil)
	_, err := decoder.Decode(req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "malformed token")
}

// TestMultiAppTokenDecode_ExpiredToken 过期 token 拒绝
func TestMultiAppTokenDecode_ExpiredToken(t *testing.T) {
	cfg := &wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "secret", Algorithm: "HS256", ExpiresTime: time.Hour},
		},
	}
	// 显式设置已过期的 ExpiresAt 生成过期 token（不依赖负 ExpiresTime 配置，避免触发 NewConnectionTokenDecoder 的配置校验 panic）
	claims := &ConnectionClaims{
		UserID: "u-exp",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)), // JWT exp 在过去 → 已过期
		},
	}
	token, err := IssueConnectionToken(context.Background(), cfg, "default", nil, claims)
	require.NoError(t, err)

	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	_, err = decoder.Decode(req)
	assert.Error(t, err, "过期 token 应被拒绝")
}

// TestMultiAppTokenDecode_TokenNotFound token 不存在报错
func TestMultiAppTokenDecode_TokenNotFound(t *testing.T) {
	cfg := newMultiAppTokenCfg()
	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws", nil) // 无 token 参数
	_, err := decoder.Decode(req)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// TestIssueConnectionToken_PerApp 按 appid 生成 token
func TestIssueConnectionToken_PerApp(t *testing.T) {
	cfg := newMultiAppTokenCfg()

	tokenA, err := IssueConnectionToken(context.Background(), cfg, "app-A", nil, &ConnectionClaims{UserID: "u-A"})
	require.NoError(t, err)
	tokenD, err := IssueConnectionToken(context.Background(), cfg, "default", nil, &ConnectionClaims{UserID: "u-D"})
	require.NoError(t, err)

	assert.NotEqual(t, tokenA, tokenD, "不同 appid 的 token 应不同")

	// 交叉验证：app-A 的 token 在 default decoder 上应验签失败
	decoderD := NewConnectionTokenDecoder(&wscconfig.ConnectionToken{
		Enabled: true, DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "default-secret", Algorithm: "HS256", ExpiresTime: time.Hour},
		},
	}, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+tokenA, nil)
	_, err = decoderD.Decode(req)
	assert.Error(t, err, "app-A token(HS512) 在 default decoder(HS256) 上应验签失败")
}

// TestRevokeConnectionToken_PerApp 按 appid 吊销 token
func TestRevokeConnectionToken_PerApp(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	defer mr.Close()

	cfg := &wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "d-secret", Algorithm: "HS256", ExpiresTime: time.Hour, UseRedis: true, RedisKeyPrefix: "wsc:d:"},
			"app-A":   {SigningKey: "a-secret", Algorithm: "HS256", ExpiresTime: time.Hour, UseRedis: true, RedisKeyPrefix: "wsc:a:"},
		},
	}
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	tokenA, err := IssueConnectionToken(context.Background(), cfg, "app-A", rdb, &ConnectionClaims{UserID: "u-A", AppID: "app-A"})
	require.NoError(t, err)
	assert.True(t, mr.Exists(whitelistKey("wsc:a:", tokenA)))

	// 按 app-A 吊销 → 删除 wsc:a: 前缀的 key
	err = RevokeConnectionToken(context.Background(), cfg, "app-A", rdb, tokenA)
	require.NoError(t, err)
	assert.False(t, mr.Exists(whitelistKey("wsc:a:", tokenA)), "app-A token 应从 wsc:a: 前缀移除")
}

// TestNewConnectionTokenDecoder_LegacyConfig 旧单套配置仍可用（向后兼容）
func TestNewConnectionTokenDecoder_LegacyConfig(t *testing.T) {
	cfg := newTestTokenCfg() // 旧配置：无 Tokens，仅顶层 SigningKey
	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	require.NotNil(t, decoder)

	// 用旧配置签发 + 解码
	token, err := IssueConnectionToken(context.Background(), cfg, "", nil, &ConnectionClaims{UserID: "u-legacy"})
	require.NoError(t, err)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "u-legacy", decoded.UserID)
}

// TestIssueConnectionToken_InvalidAppID 未知 appid 走 default 兜底签发
func TestIssueConnectionToken_InvalidAppID(t *testing.T) {
	cfg := newMultiAppTokenCfg()
	// 传未知 appid，应走 default 兜底
	token, err := IssueConnectionToken(context.Background(), cfg, "nonexistent", nil, &ConnectionClaims{UserID: "u-x"})
	require.NoError(t, err)
	assert.NotEmpty(t, token)

	// 用 default 密钥的 decoder 能解码
	decoder := NewConnectionTokenDecoder(cfg, nil, nil)
	req := httptest.NewRequest("GET", "/ws?token="+token, nil)
	decoded, err := decoder.Decode(req)
	require.NoError(t, err)
	assert.Equal(t, "u-x", decoded.UserID)
}

// TestIssueConnectionToken_CtxPropagation ctx 透传
func TestIssueConnectionToken_CtxPropagation(t *testing.T) {
	cfg := newMultiAppTokenCfg()
	// 已取消的 ctx 应使 Redis 写入失败（这里不启用 Redis，仅验证 ctx 不 panic）
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// 不启用 Redis，ctx 取消不影响签发本身
	token, err := IssueConnectionToken(ctx, cfg, "default", nil, &ConnectionClaims{UserID: "u-ctx"})
	require.NoError(t, err)
	assert.NotEmpty(t, token)
}

// TestRevokeConnectionToken_CtxPropagation ctx 透传
func TestRevokeConnectionToken_CtxPropagation(t *testing.T) {
	cfg := newMultiAppTokenCfg()
	// 未启用 Redis，Revoke 直接返回 nil
	err := RevokeConnectionToken(context.Background(), cfg, "default", nil, "any-token")
	assert.NoError(t, err)
}

// ============================================================================
// NewHub 多 appid 配置校验测试
// ============================================================================

// newHubWithTokenConfig 构造带 ConnectionToken 的 WSC 配置
func newHubWithTokenConfig(tokenCfg *wscconfig.ConnectionToken) *wscconfig.WSC {
	cfg := wscconfig.Default()
	cfg.Security.ConnectionToken = tokenCfg
	return cfg
}

// expectPanic 辅助：断言 fn 执行时 panic 且消息包含 substr
func expectPanic(t *testing.T, fn func(), substr string) {
	t.Helper()
	defer func() {
		r := recover()
		require.NotNil(t, r, "期望 panic 但未发生")
		msg := fmt.Sprintf("%v", r)
		assert.True(t, strings.Contains(msg, substr),
			"panic 消息 %q 不包含期望子串 %q", msg, substr)
	}()
	fn()
}

// TestNewHub_InvalidConfig_EmptySigningKey SigningKey 空时 panic
func TestNewHub_InvalidConfig_EmptySigningKey(t *testing.T) {
	cfg := newHubWithTokenConfig(&wscconfig.ConnectionToken{
		Enabled: true,
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "", Algorithm: "HS256"}, // 空 key
		},
	})
	expectPanic(t, func() { NewHub(cfg) }, "signing-key is required")
}

// TestNewHub_InvalidConfig_InvalidAlgorithm 非法算法时 panic
func TestNewHub_InvalidConfig_InvalidAlgorithm(t *testing.T) {
	cfg := newHubWithTokenConfig(&wscconfig.ConnectionToken{
		Enabled: true,
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "secret", Algorithm: "RS256"}, // 非法
		},
	})
	expectPanic(t, func() { NewHub(cfg) }, "invalid algorithm")
}

// TestNewHub_InvalidConfig_DefaultMissing Default 缺失时 panic
func TestNewHub_InvalidConfig_DefaultMissing(t *testing.T) {
	cfg := newHubWithTokenConfig(&wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "nonexistent",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "secret", Algorithm: "HS256"},
		},
	})
	expectPanic(t, func() { NewHub(cfg) }, "not found in tokens")
}

// TestNewHub_InvalidConfig_DuplicateIssuerKey 重复 issuer+key 时 panic
func TestNewHub_InvalidConfig_DuplicateIssuerKey(t *testing.T) {
	cfg := newHubWithTokenConfig(&wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "dup", Algorithm: "HS256", Issuer: "same"},
			"app-A":   {SigningKey: "dup", Algorithm: "HS256", Issuer: "same"},
		},
	})
	expectPanic(t, func() { NewHub(cfg) }, "duplicate (issuer, signing-key)")
}

// TestNewHub_ValidMultiApp 合法多 appid 配置成功创建 decoder
func TestNewHub_ValidMultiApp(t *testing.T) {
	cfg := newHubWithTokenConfig(&wscconfig.ConnectionToken{
		Enabled:      true,
		DefaultAppID: "default",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"default": {SigningKey: "d-secret", Algorithm: "HS256", ExpiresTime: time.Hour},
			"app-A":   {SigningKey: "a-secret", Algorithm: "HS512", ExpiresTime: time.Hour},
		},
	})
	hub := NewHub(cfg)
	require.NotNil(t, hub)
	assert.NotNil(t, hub.connectionTokenDecoder, "decoder 应已创建")
	hub.Shutdown()
}

// TestNewHub_ValidLegacyConfig 合法旧单套配置成功创建 decoder（向后兼容）
func TestNewHub_ValidLegacyConfig(t *testing.T) {
	cfg := newHubWithTokenConfig(&wscconfig.ConnectionToken{
		Enabled:    true,
		SigningKey: "legacy-secret",
		Algorithm:  "HS256",
	})
	hub := NewHub(cfg)
	require.NotNil(t, hub)
	assert.NotNil(t, hub.connectionTokenDecoder, "旧配置也应创建 decoder")
	hub.Shutdown()
}

// TestNewHub_TokenDisabled 无 panic
func TestNewHub_TokenDisabled(t *testing.T) {
	cfg := wscconfig.Default() // ConnectionToken.Enabled 默认 false
	hub := NewHub(cfg)
	require.NotNil(t, hub)
	assert.Nil(t, hub.connectionTokenDecoder, "未启用时 decoder 应为 nil")
	hub.Shutdown()
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-15 00:00:00
 * @FilePath: \go-wsc\transport\token_test.go
 * @Description: 连接 Token 鉴权器测试 - AES-GCM 签发/解密/防篡改/过期/多 appid
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/spi"
)

// newTestTokenConfig 构造旧式单套配置（顶层字段，Tokens 为空走 ResolveTokens 包装）
func newTestTokenConfig(key string) *wscconfig.ConnectionToken {
	return &wscconfig.ConnectionToken{
		Enabled:    true,
		SigningKey: key,
	}
}

// issueAndBuildRequest 签发 token 并构造携带该 token 的 HTTP 请求（query 来源）
// appID 为目标签发 appid（空串走 default 兜底）
func issueAndBuildRequest(t *testing.T, cfg *wscconfig.ConnectionToken, appID string, claims *spi.ConnectionClaims) *http.Request {
	t.Helper()
	token, err := IssueConnectionToken(cfg, appID, claims)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/ws?token="+token, nil)
	return req
}

// TestIssueAndAuthenticate 签发→解密还原：全字段往返 + appid 回填 + exp 自动填充
func TestIssueAndAuthenticate(t *testing.T) {
	cfg := newTestTokenConfig("unit-test-signing-key-0123456789")
	auth := NewTokenAuthenticator(cfg, nil)
	require.NotNil(t, auth)

	claims := &spi.ConnectionClaims{
		UserID:    "u-10086",
		UserType:  "agent",
		DeviceID:  "device-abc",
		Namespace: "tenant-1",
		GroupID:   "g1,g2",
	}
	got, err := auth.Authenticate(issueAndBuildRequest(t, cfg, "", claims))
	require.NoError(t, err)
	assert.Equal(t, "u-10086", got.UserID)
	assert.Equal(t, "agent", got.UserType)
	assert.Equal(t, "device-abc", got.DeviceID)
	assert.Equal(t, "tenant-1", got.Namespace)
	assert.Equal(t, "g1,g2", got.GroupID)
	// 单套旧配置包装为 Default set → appid 回填
	assert.Equal(t, "__default_app__", got.AppID)
	// exp 自动填充（默认 5min）且未过期
	assert.Greater(t, got.ExpiresAt, time.Now().Unix())
}

// TestAuthenticateTokenNotFound 请求无 token → 明确报错
func TestAuthenticateTokenNotFound(t *testing.T) {
	auth := NewTokenAuthenticator(newTestTokenConfig("k"), nil)
	_, err := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws", nil))
	assert.ErrorContains(t, err, "connection token not found")
}

// TestAuthenticateGarbageToken 非 base64 / 过短载荷 → 解码失败
func TestAuthenticateGarbageToken(t *testing.T) {
	auth := NewTokenAuthenticator(newTestTokenConfig("k"), nil)

	// 非法 base64 字符
	req := httptest.NewRequest(http.MethodGet, "/ws?token=@@not-base64@@", nil)
	_, err := auth.Authenticate(req)
	assert.ErrorContains(t, err, "invalid connection token encoding")

	// 合法 base64 但载荷过短（≤ nonce 12B）
	req = httptest.NewRequest(http.MethodGet, "/ws?token=YWJjZA", nil)
	_, err = auth.Authenticate(req)
	assert.ErrorContains(t, err, "payload too short")
}

// TestAuthenticateTamperedToken 篡改密文任一字节 → GCM 认证失败，全密钥不可解
func TestAuthenticateTamperedToken(t *testing.T) {
	cfg := newTestTokenConfig("unit-test-signing-key-0123456789")
	auth := NewTokenAuthenticator(cfg, nil)

	token, err := IssueConnectionToken(cfg, "", &spi.ConnectionClaims{UserID: "u-1"})
	require.NoError(t, err)

	// 解码翻转密文中段一个字节后重编码（nonce 与载荷均可能被翻转，GCM 全覆盖）
	raw, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	raw[len(raw)/2] ^= 0xFF
	req := httptest.NewRequest(http.MethodGet, "/ws?token="+base64.RawURLEncoding.EncodeToString(raw), nil)

	_, err = auth.Authenticate(req)
	assert.ErrorContains(t, err, "decrypt failed with all keys")
}

// TestAuthenticateExpiredToken exp 在过去 → 过期拒绝（exp 加密在载荷内，无法伪造延期）
func TestAuthenticateExpiredToken(t *testing.T) {
	cfg := newTestTokenConfig("unit-test-signing-key-0123456789")
	auth := NewTokenAuthenticator(cfg, nil)

	req := issueAndBuildRequest(t, cfg, "", &spi.ConnectionClaims{
		UserID:    "u-1",
		ExpiresAt: time.Now().Add(-time.Hour).Unix(),
	})
	_, err := auth.Authenticate(req)
	assert.ErrorContains(t, err, "connection token expired")
}

// TestMultiAppIDKeyRouting 多 appid：各 set 独立签发/命中归属；未知 appid 走 default 兜底
func TestMultiAppIDKeyRouting(t *testing.T) {
	cfg := &wscconfig.ConnectionToken{
		Enabled:        true,
		DefaultAppID:   "app-a",
		TokenParamName: "token",
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"app-a": {SigningKey: "key-of-app-a"},
			"app-b": {SigningKey: "key-of-app-b"},
		},
	}
	auth := NewTokenAuthenticator(cfg, nil)
	require.NotNil(t, auth)

	// app-b 签发 → 命中 key-of-app-b → 归属 app-b
	got, err := auth.Authenticate(issueAndBuildRequest(t, cfg, "app-b", &spi.ConnectionClaims{UserID: "u-b"}))
	require.NoError(t, err)
	assert.Equal(t, "app-b", got.AppID)

	// app-a 签发 → 归属 app-a
	got, err = auth.Authenticate(issueAndBuildRequest(t, cfg, "app-a", &spi.ConnectionClaims{UserID: "u-a"}))
	require.NoError(t, err)
	assert.Equal(t, "app-a", got.AppID)

	// 未知 appid 走 default（app-a）兜底签发，仍可解密
	token, err := IssueConnectionToken(cfg, "app-not-exist", &spi.ConnectionClaims{UserID: "u-x"})
	require.NoError(t, err)
	got, err = auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws?token="+token, nil))
	require.NoError(t, err)
	assert.Equal(t, "app-a", got.AppID)
}

// TestCrossAppKeyIsolation 用 app-b 的 authenticator（无 a 密钥）解 app-a 签发的 token → 失败
func TestCrossAppKeyIsolation(t *testing.T) {
	cfgA := &wscconfig.ConnectionToken{
		Enabled: true,
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"app-a": {SigningKey: "key-of-app-a"},
		},
	}
	cfgB := &wscconfig.ConnectionToken{
		Enabled: true,
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"app-b": {SigningKey: "key-of-app-b"},
		},
	}
	authB := NewTokenAuthenticator(cfgB, nil)

	req := issueAndBuildRequest(t, cfgA, "app-a", &spi.ConnectionClaims{UserID: "u-a"})
	_, err := authB.Authenticate(req)
	assert.ErrorContains(t, err, "decrypt failed with all keys")
}

// TestHeaderSourceToken TokenSource=header → 从请求头提取
func TestHeaderSourceToken(t *testing.T) {
	cfg := newTestTokenConfig("unit-test-signing-key-0123456789")
	cfg.TokenSource = "header"
	cfg.TokenParamName = "X-Conn-Token"
	auth := NewTokenAuthenticator(cfg, nil)

	token, err := IssueConnectionToken(cfg, "", &spi.ConnectionClaims{UserID: "u-1", UserType: "customer"})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("X-Conn-Token", token)
	got, err := auth.Authenticate(req)
	require.NoError(t, err)
	assert.Equal(t, "u-1", got.UserID)
	assert.Equal(t, "customer", got.UserType)
}

// TestIssueConnectionTokenValidation 签发入参校验
func TestIssueConnectionTokenValidation(t *testing.T) {
	cfg := newTestTokenConfig("k")

	_, err := IssueConnectionToken(nil, "", &spi.ConnectionClaims{UserID: "u"})
	assert.ErrorContains(t, err, "config is nil")
	_, err = IssueConnectionToken(cfg, "", nil)
	assert.ErrorContains(t, err, "claims is nil")
	_, err = IssueConnectionToken(cfg, "", &spi.ConnectionClaims{})
	assert.ErrorContains(t, err, "UserID is required")
}

// TestNewTokenAuthenticatorPanicOnEmptyKey 启动期 fail-fast：空密钥 panic（部署期错误不静默）
func TestNewTokenAuthenticatorPanicOnEmptyKey(t *testing.T) {
	cfg := &wscconfig.ConnectionToken{
		Enabled: true,
		Tokens: map[string]*wscconfig.ConnectionTokenSet{
			"app-a": {SigningKey: "key-a"},
			"app-b": {SigningKey: ""}, // 部署错误
		},
	}
	assert.Panics(t, func() { NewTokenAuthenticator(cfg, nil) })
}

// TestNewTokenAuthenticatorNilSafe cfg/解析失败 → 返回 nil（调用方判 nil 走明文降级）
func TestNewTokenAuthenticatorNilSafe(t *testing.T) {
	assert.Nil(t, NewTokenAuthenticator(nil, nil))
	assert.Nil(t, NewTokenAuthenticator(&wscconfig.ConnectionToken{Enabled: false}, nil))
}

// TestNonceRandomness 同一 claims 两次签发 → nonce 随机 token 不同，均可解密（幂等往返）
func TestNonceRandomness(t *testing.T) {
	cfg := newTestTokenConfig("unit-test-signing-key-0123456789")
	auth := NewTokenAuthenticator(cfg, nil)
	claims := &spi.ConnectionClaims{UserID: "u-1", UserType: "visitor"}

	t1, err := IssueConnectionToken(cfg, "", claims)
	require.NoError(t, err)
	t2, err := IssueConnectionToken(cfg, "", claims)
	require.NoError(t, err)
	assert.NotEqual(t, t1, t2, "nonce 随机，同 claims 两次签发 token 必不同")

	// claims 被第一次 Issue 回填了 AppID/exp，两次载荷不一致属预期；
	// 两个 token 均应能独立解密出 UserID
	for _, token := range []string{t1, t2} {
		got, err := auth.Authenticate(httptest.NewRequest(http.MethodGet, "/ws?token="+token, nil))
		require.NoError(t, err)
		assert.Equal(t, "u-1", got.UserID)
	}
}

// TestTokenNotPlaintextReversible token 对外不可读：载荷字段不以明文出现在 token 中
func TestTokenNotPlaintextReversible(t *testing.T) {
	cfg := newTestTokenConfig("unit-test-signing-key-0123456789")
	token, err := IssueConnectionToken(cfg, "", &spi.ConnectionClaims{
		UserID:  "secret-user-10086",
		GroupID: "secret-group",
	})
	require.NoError(t, err)

	assert.NotContains(t, token, "secret-user-10086")
	assert.NotContains(t, token, "secret-group")
	assert.False(t, strings.Contains(token, "uid"), "载荷已整体加密，字段名不可见")
}

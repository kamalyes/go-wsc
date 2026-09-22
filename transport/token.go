/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-16 00:00:00
 * @FilePath: \go-wsc\transport\token.go
 * @Description: 连接 Token 鉴权器 - AES-GCM 对称加解密
 *
 * 安全模型（内部协议，简单自洽）:
 *   1. 对称加密自包含: user_id/user_type/device_id/app_id/namespace/group_id
 *      JSON 序列化后 AES-256-GCM 加密，密文即 token，对外不可读不可伪造
 *   2. GCM 认证标签: 任何篡改（含换字段、换过期时间）解密直接失败，无需独立验签
 *   3. 过期内置: exp 字段随载荷加密，解密后校验，无法伪造延期
 *
 * 多 appid 支持:
 *   持有 map[appid]*ConnectionTokenSet（go-config ResolveTokens 解析），
 *   解密时按密钥遍历尝试（GCM 认证失败自然跳过），命中即归属该 appid。
 *   密钥数量级为个位数（appid 数），遍历开销可忽略。
 *
 * token 格式: base64url( nonce(12B) || AES-256-GCM 密文+认证标签 )
 * 密钥派生:   sha256(SigningKey) 固定 32 字节（密钥长度不受限，任意非空字符串可用）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"sync"
	"time"

	gccommon "github.com/kamalyes/go-config/pkg/common"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-wsc/spi"
)

// tokenNonceSize GCM nonce 长度（标准推荐 12 字节）
const tokenNonceSize = 12

// tokenCipherPool 复用 GCM 实例（aead 构造含 key schedule，避免每 token 重复派生）
var tokenCipherPool = sync.Map{} // 密钥指纹 → cipher.AEAD

// tokenAuthenticator 基于 AES-GCM 的连接 Token 鉴权器
// 持有 map[appid]*ConnectionTokenSet，解密时按密钥遍历尝试，命中即归属
type tokenAuthenticator struct {
	sets       map[string]*wscconfig.ConnectionTokenSet // appid → 配置（含密钥）
	defaultSet *wscconfig.ConnectionTokenSet            // 兜底 set（提取来源/参数名以它为准）
	logger     spi.Logger
}

// NewTokenAuthenticator 创建连接 Token 鉴权器（实现 spi.ConnectionAuthenticator）
//
// cfg 内部调用 ResolveTokens 解析为 sets map（旧单套配置自动包装为单 Default set）
// 启动期校验（fail-fast）：每套 set 的 SigningKey 必须非空——
// 密钥缺失是部署期错误，静默降级为"鉴权不可用"会导致启用 token 却全部连接被拒
func NewTokenAuthenticator(cfg *wscconfig.ConnectionToken, logger spi.Logger) spi.ConnectionAuthenticator {
	if cfg == nil || !cfg.IsEnabled() {
		return nil
	}
	if logger == nil {
		logger = spi.NewDefaultLogger()
	}
	sets, defaultID, err := cfg.ResolveTokens()
	if err != nil {
		logger.ErrorKV("[ConnectionToken] 解析配置失败，鉴权器不可用", "error", err)
		return nil
	}
	for appID, set := range sets {
		if set.GetSigningKey() == "" {
			panic(fmt.Sprintf("[ConnectionToken] app_id=%q 的 signing-key 为空，启用 token 前必须配置密钥", appID))
		}
	}
	return &tokenAuthenticator{
		sets:       sets,
		defaultSet: sets[defaultID],
		logger:     logger,
	}
}

// Authenticate 从请求中提取并解密 token
// 流程：按配置来源提取 → base64 解码 → 按密钥遍历 GCM 解密 → 过期校验
func (a *tokenAuthenticator) Authenticate(r *http.Request) (*spi.ConnectionClaims, error) {
	if a.defaultSet == nil {
		return nil, fmt.Errorf("connection token default set not found")
	}

	// 1. 提取 token（所有 set 的来源应一致，以 default set 配置为准）
	tokenStr := extractToken(r, a.defaultSet)
	if tokenStr == "" {
		return nil, fmt.Errorf("connection token not found in request (source=%s, name=%s)",
			a.defaultSet.GetTokenSource(), a.defaultSet.GetTokenParamName())
	}

	// 2. base64 解码
	nonceAndCipher, err := base64.RawURLEncoding.DecodeString(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("invalid connection token encoding: %w", err)
	}
	if len(nonceAndCipher) <= tokenNonceSize {
		return nil, fmt.Errorf("connection token payload too short")
	}
	nonce, ciphertext := nonceAndCipher[:tokenNonceSize], nonceAndCipher[tokenNonceSize:]

	// 3. 按密钥遍历解密（GCM 认证失败自然跳过，命中即归属该 appid）
	var claims *spi.ConnectionClaims
	var matchedAppID string
	for appID, set := range a.sets {
		aead, err := tokenAEAD(set.GetSigningKey())
		if err != nil {
			continue
		}
		plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
		if err != nil {
			continue // 密钥不匹配或密文被篡改，尝试下一把
		}
		if err := json.Unmarshal(plaintext, &claims); err != nil {
			return nil, fmt.Errorf("invalid connection token payload: %w", err)
		}
		matchedAppID = appID
		break
	}
	if claims == nil {
		return nil, fmt.Errorf("invalid connection token: decrypt failed with all keys")
	}

	// 4. 过期校验（exp 随载荷加密，无法伪造延期）
	if claims.ExpiresAt > 0 && claims.ExpiresAt <= time.Now().Unix() {
		return nil, fmt.Errorf("connection token expired (exp=%d)", claims.ExpiresAt)
	}

	// 5. 回填 appid（以命中的 set 为准；载荷内 aid 仅供业务追溯，归属以密钥命中为准）
	claims.AppID = matchedAppID
	return claims, nil
}

// IssueConnectionToken 生成连接 Token（业务层如登录服务调用后下发给客户端）
// 按 appID 选择对应 set 加密；未命中或载荷为空 aid 时以目标 set 回填
//
// 参数:
//   - cfg: 连接 Token 配置（含多 appid）
//   - appID: 目标 appid（不存在时走 default 兜底）
//   - claims: 连接信息（UserID 必填；ExpiresAt 缺省时按 set 配置的 ExpiresTime 自动填充）
//
// 返回:
//   - string: base64url 编码的加密 token
//   - error: 配置/载荷/加密失败时返回
func IssueConnectionToken(cfg *wscconfig.ConnectionToken, appID string, claims *spi.ConnectionClaims) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("connection token config is nil")
	}
	if claims == nil {
		return "", fmt.Errorf("claims is nil")
	}
	if claims.UserID == "" {
		return "", fmt.Errorf("claims.UserID is required")
	}

	tokens, defaultID, err := cfg.ResolveTokens()
	if err != nil {
		return "", fmt.Errorf("resolve tokens failed: %w", err)
	}

	set, ok := tokens[appID]
	if !ok {
		// 未知 appid 走 default 兜底
		set = tokens[defaultID]
		appID = defaultID
	}
	if set == nil || set.GetSigningKey() == "" {
		return "", fmt.Errorf("signing key is required for app_id=%q", appID)
	}

	// 回填归属 + 过期时间
	claims.AppID = appID
	if claims.ExpiresAt <= 0 {
		claims.ExpiresAt = time.Now().Add(set.GetExpiresTime()).Unix()
	}

	// 序列化 + 加密
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal claims failed: %w", err)
	}
	nonceAndCipher, err := encryptToken(set.GetSigningKey(), payload)
	if err != nil {
		return "", fmt.Errorf("encrypt token failed: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(nonceAndCipher), nil
}

// extractToken 按配置的来源（query/header）提取 token
func extractToken(r *http.Request, set *wscconfig.ConnectionTokenSet) string {
	src := gccommon.AttributeSource{
		Type: gccommon.AttributeSourceType(set.GetTokenSource()),
		Key:  set.GetTokenParamName(),
	}
	return gccommon.ExtractFromSource(r, src)
}

// encryptToken AES-256-GCM 加密：nonce(12B) + 密文+认证标签
func encryptToken(signingKey string, plaintext []byte) ([]byte, error) {
	aead, err := tokenAEAD(signingKey)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, tokenNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, nil)
	return append(nonce, ciphertext...), nil
}

// tokenAEAD 取密钥对应的 GCM 实例（池化复用，密钥派生只做一次）
func tokenAEAD(signingKey string) (cipher.AEAD, error) {
	fingerprint := sha256.Sum256([]byte(signingKey))
	if cached, ok := tokenCipherPool.Load(fingerprint); ok {
		return cached.(cipher.AEAD), nil
	}
	// sha256(SigningKey) 派生固定 32 字节 AES-256 密钥（密钥长度不受限）
	key := sha256.Sum256([]byte(signingKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	tokenCipherPool.Store(fingerprint, aead)
	return aead, nil
}

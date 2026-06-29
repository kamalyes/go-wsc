/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-06-29 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-06-29 00:00:00
 * @FilePath: \go-wsc\hub\connection_token.go
 * @Description: 连接 Token 解码器 - 将 user_id/user_type/device_id 加密为单一 JWT token
 *
 * 安全模型:
 *   1. JWT 自包含: user_id/user_type/device_id 编码在 JWT claims 中（签名防篡改）
 *   2. Redis 白名单(可选): 多节点共享会话状态，支持主动吊销（一处登出，全局生效）
 *   3. 兼容模式: ConnectionToken.Enabled=false 时走原明文参数提取（向后兼容）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	gccommon "github.com/kamalyes/go-config/pkg/common"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// Claims 定义
// ============================================================================

// ConnectionClaims 连接 Token 的 JWT Claims
// 将原本明文暴露的 user_id/user_type/device_id 三个连接参数加密到 JWT 中
// 字段名采用短缩写以减小 token 体积
type ConnectionClaims struct {
	UserID   string `json:"uid"`           // 用户ID（必填）
	UserType string `json:"utp,omitempty"` // 用户类型（默认 visitor）
	DeviceID string `json:"did,omitempty"` // 设备ID
	jwt.RegisteredClaims
}

// ============================================================================
// Decoder 接口与实现
// ============================================================================

// ConnectionTokenDecoder 连接 Token 解码器接口
// Hub 在 extractClientAttributes 中根据配置选择是否启用
type ConnectionTokenDecoder interface {
	// Decode 从 HTTP 请求中提取并解码 token
	// 返回解码后的连接信息；token 不存在/无效/被吊销时返回 error
	Decode(r *http.Request) (*ConnectionClaims, error)
}

// jwtConnectionTokenDecoder 基于 JWT 的连接 Token 解码器实现
type jwtConnectionTokenDecoder struct {
	config   *wscconfig.ConnectionToken
	redisCli *redis.Client
	logger   WSCLogger
}

// NewConnectionTokenDecoder 创建连接 Token 解码器
// redisCli 可为 nil（当 UseRedis=false 时不需要 Redis）
func NewConnectionTokenDecoder(cfg *wscconfig.ConnectionToken, redisCli *redis.Client, logger WSCLogger) ConnectionTokenDecoder {
	return &jwtConnectionTokenDecoder{
		config:   cfg,
		redisCli: redisCli,
		logger:   logger,
	}
}

// Decode 从请求中提取并解码 token
func (d *jwtConnectionTokenDecoder) Decode(r *http.Request) (*ConnectionClaims, error) {
	// 1. 提取 token
	tokenStr := d.extractToken(r)
	if tokenStr == "" {
		return nil, fmt.Errorf("connection token not found in request (source=%s, name=%s)", d.config.GetTokenSource(), d.config.GetTokenParamName())
	}

	// 2. 解析并验证 JWT（签名 + exp + 可选 iss/aud）
	claims, err := d.parseToken(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("invalid connection token: %w", err)
	}

	// 3. 可选 Redis 白名单校验（多节点共享会话状态）
	if d.config.IsRedisEnabled() && d.redisCli != nil {
		if err := d.checkWhitelist(r.Context(), tokenStr); err != nil {
			return nil, fmt.Errorf("token revoked or not in whitelist: %w", err)
		}
	}

	return claims, nil
}

// extractToken 按配置的来源（query/header）提取 token
func (d *jwtConnectionTokenDecoder) extractToken(r *http.Request) string {
	source := gccommon.AttributeSourceType(d.config.GetTokenSource())
	key := d.config.GetTokenParamName()
	src := gccommon.AttributeSource{Type: source, Key: key}
	return gccommon.ExtractFromSource(r, src)
}

// parseToken 解析并验证 JWT
func (d *jwtConnectionTokenDecoder) parseToken(tokenStr string) (*ConnectionClaims, error) {
	claims := &ConnectionClaims{}

	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{d.config.GetAlgorithm()}),
	}
	if issuer := d.config.GetIssuer(); issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(issuer))
	}
	if audience := d.config.GetAudience(); audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(audience))
	}

	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(d.config.GetSigningKey()), nil
	}, parserOpts...)
	if err != nil {
		return nil, err
	}
	return claims, nil
}

// checkWhitelist 校验 Redis 白名单
// 设计取舍: Redis 故障时选择降级放行（避免 Redis 抖动锁死所有连接）
// 如需更严格策略，可在配置中关闭 UseRedis，强制仅依赖 JWT 自身验证
func (d *jwtConnectionTokenDecoder) checkWhitelist(ctx context.Context, tokenStr string) error {
	key := whitelistKey(d.config.GetRedisKeyPrefix(), tokenStr)

	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	n, err := d.redisCli.Exists(cctx, key).Result()
	if err != nil {
		d.logger.WarnKV("[ConnectionToken] Redis 白名单校验失败，降级放行",
			"error", err, "key", key)
		return nil
	}
	if n == 0 {
		return fmt.Errorf("token not in whitelist (key=%s)", key)
	}
	return nil
}

// ============================================================================
// Token 工具函数（业务层调用）
// ============================================================================

// IssueConnectionToken 生成连接 Token
// 业务层（如登录服务）调用此函数生成 token 下发给客户端
// 若 cfg.UseRedis=true，会同时将 token 写入 Redis 白名单（带 TTL）
//
// 参数:
//   - cfg: 连接 Token 配置
//   - redisCli: Redis 客户端（UseRedis=false 时可为 nil）
//   - claims: 连接信息（UserID/UserType/DeviceID；ExpiresAt/IssuedAt/ID 缺省时自动填充）
//
// 返回:
//   - string: 签名后的 JWT token
//   - error: 生成或写入 Redis 失败时返回
func IssueConnectionToken(cfg *wscconfig.ConnectionToken, redisCli *redis.Client, claims *ConnectionClaims) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("connection token config is nil")
	}
	if claims == nil {
		return "", fmt.Errorf("claims is nil")
	}
	if claims.UserID == "" {
		return "", fmt.Errorf("claims.UserID is required")
	}
	if cfg.GetSigningKey() == "" {
		return "", fmt.Errorf("signing key is required")
	}

	// 补充默认 RegisteredClaims
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(cfg.GetExpiresTime()))
	}
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwt.NewNumericDate(time.Now())
	}
	if claims.ID == "" {
		claims.ID = fmt.Sprintf("%d", time.Now().UnixNano())
	}

	method := jwt.GetSigningMethod(cfg.GetAlgorithm())
	if method == nil {
		return "", fmt.Errorf("unsupported signing algorithm: %s", cfg.GetAlgorithm())
	}

	tok := jwt.NewWithClaims(method, claims)
	tokenStr, err := tok.SignedString([]byte(cfg.GetSigningKey()))
	if err != nil {
		return "", fmt.Errorf("sign token failed: %w", err)
	}

	// 写入 Redis 白名单（若启用）
	if cfg.IsRedisEnabled() && redisCli != nil {
		key := whitelistKey(cfg.GetRedisKeyPrefix(), tokenStr)
		ttl := claims.ExpiresAt.Sub(time.Now())
		if ttl <= 0 {
			ttl = cfg.GetExpiresTime()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := redisCli.Set(ctx, key, "1", ttl).Err(); err != nil {
			return "", fmt.Errorf("write token to redis whitelist failed: %w", err)
		}
	}

	return tokenStr, nil
}

// RevokeConnectionToken 吊销连接 Token
// 多节点环境下，吊销后所有节点立即生效（依赖 Redis 白名单）
//
// 参数:
//   - cfg: 连接 Token 配置
//   - redisCli: Redis 客户端
//   - tokenStr: 要吊销的 token
//
// 返回:
//   - error: 删除失败时返回（token 本身仍有效，直到自然过期）
func RevokeConnectionToken(cfg *wscconfig.ConnectionToken, redisCli *redis.Client, tokenStr string) error {
	if cfg == nil || !cfg.IsRedisEnabled() || redisCli == nil {
		return nil
	}
	key := whitelistKey(cfg.GetRedisKeyPrefix(), tokenStr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return redisCli.Del(ctx, key).Err()
}

// whitelistKey 生成 Redis 白名单 key
// 使用 token 的 SHA256 哈希作为标识，避免在 key 中暴露原始 token
func whitelistKey(prefix, tokenStr string) string {
	sum := sha256.Sum256([]byte(tokenStr))
	return prefix + "whitelist:" + hex.EncodeToString(sum[:])
}

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
 * 多 appid 支持:
 *   decoder 持有 map[appid]*ConnectionTokenSet，按 JWT claims.aid 路由到对应 set 验签。
 *   两段式解码：先 ParseUnverified 拿 aid 选 set，再用 set 密钥严格验签。
 *   旧单套配置由 ConnectionToken.ResolveTokens() 自动包装为单 Default set，行为不变。
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
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
	gccommon "github.com/kamalyes/go-config/pkg/common"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/contextx"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-wsc/middleware"
	"github.com/redis/go-redis/v9"
)

// ============================================================================
// Claims 定义
// ============================================================================

// ConnectionClaims 连接 Token 的 JWT Claims
// 将原本明文暴露的 user_id/user_type/device_id/app_id/namespace/group_id 加密到 JWT 中
// 字段名采用短缩写以减小 token 体积
// 注意：
//   - app_id 为应用ID（最上层隔离维度，默认 "__default_app__"）
//   - group_id 为连接时自动加入的成员组标识（支持逗号分隔多群组"g1,g2,g3"；空则加入默认组）
//   - 群组成员关系的后续变更仍由业务层 API 管理
type ConnectionClaims struct {
	UserID    string `json:"uid"`           // 用户ID（必填）
	UserType  string `json:"utp,omitempty"` // 用户类型（默认 visitor）
	DeviceID  string `json:"did,omitempty"` // 设备ID
	AppID     string `json:"aid,omitempty"` // 应用ID（最上层隔离维度，默认 "__default_app__"）
	Namespace string `json:"tid,omitempty"` // 命名空间ID（默认 "default"，用于命名空间隔离与消息过滤）
	GroupID   string `json:"gid,omitempty"` // 群组ID（可选，支持逗号分隔多群组"g1,g2,g3"；用于群组消息过滤与连接时自动加入）
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

// tokenRedisTimeout Token 相关 Redis 操作统一超时
const tokenRedisTimeout = 2 * time.Second

// jwtConnectionTokenDecoder 基于 JWT 的连接 Token 解码器实现
// 持有 map[appid]*ConnectionTokenSet，按 claims.aid 路由选 set 验签
// 旧单套配置由 ResolveTokens 包装为单 Default set，decoder 逻辑统一
type jwtConnectionTokenDecoder struct {
	sets         map[string]*wscconfig.ConnectionTokenSet // appid → 配置
	defaultAppID string                                   // 兜底 appid
	redisCli     redis.UniversalClient                    // 可为 nil（所有 set 均未启用 Redis 时）
	logger       WSCLogger
}

// NewConnectionTokenDecoder 创建连接 Token 解码器
// cfg 内部调用 ResolveTokens 解析为 sets map；redisCli 可为 nil
// 单一实现：无论旧单套配置还是新多 appid 配置，都走同一套两段式解码逻辑
//
// 启动期配置校验（fail-fast）：调用 ValidateMultiAppID 校验
//   - 每套 set 的 SigningKey/Algorithm/ExpiresTime 合法
//   - DefaultAppID 存在于 tokens map
//   - 跨 appid 的 (Issuer, SigningKey) 不重复（防止误用同一密钥签不同 appid）
//   - 启用 Redis 时 RedisKeyPrefix 每套独立
//   - TokenSource ∈ {query, header}
//
// 校验失败直接 panic：连接 token 配置错误是部署/启动期错误，不应降级为"decoder 不可用"静默运行，
// 否则线上启用 token 却因配置错误导致所有连接被拒（decoder=nil）或安全降级。
func NewConnectionTokenDecoder(cfg *wscconfig.ConnectionToken, redisCli redis.UniversalClient, logger WSCLogger) ConnectionTokenDecoder {
	if cfg == nil {
		return nil
	}
	// 未传入 logger 时使用默认日志器（与 handler 层惯例一致，调用点无需判空）
	if logger == nil {
		logger = middleware.NewDefaultWSCLogger()
	}
	if err := cfg.ValidateMultiAppID(); err != nil {
		panic(fmt.Sprintf("[ConnectionToken] 配置校验失败: %v", err))
	}
	sets, defaultID, err := cfg.ResolveTokens()
	if err != nil {
		logger.ErrorKV("[ConnectionToken] 解析配置失败，decoder 不可用", "error", err)
		return nil
	}
	return &jwtConnectionTokenDecoder{
		sets:         sets,
		defaultAppID: defaultID,
		redisCli:     redisCli,
		logger:       logger,
	}
}

// Decode 从请求中提取并解码 token
// 流程：提取 token → ParseUnverified 拿 aid 选 set → 严格验签 → Redis 白名单
func (d *jwtConnectionTokenDecoder) Decode(r *http.Request) (*ConnectionClaims, error) {
	// 用 default set 的提取配置（所有 set 的 token 来源应一致）
	defaultSet := d.sets[d.defaultAppID]
	if defaultSet == nil {
		return nil, fmt.Errorf("connection token decoder default set %q not found", d.defaultAppID)
	}

	// 1. 提取 token
	tokenStr := d.extractToken(r, defaultSet)
	if tokenStr == "" {
		return nil, fmt.Errorf("connection token not found in request (source=%s, name=%s)",
			defaultSet.GetTokenSource(), defaultSet.GetTokenParamName())
	}

	// 2. 两段式解码：先 ParseUnverified 拿 aid 选 set，再用 set 密钥严格验签
	claims, set, err := d.parseAndVerifyToken(tokenStr)
	if err != nil {
		return nil, fmt.Errorf("invalid connection token: %w", err)
	}

	// 3. 可选 Redis 白名单校验（按 set 独立前缀）
	if set.IsRedisEnabled() && d.redisCli != nil {
		if err := d.checkWhitelist(r.Context(), set, tokenStr); err != nil {
			return nil, fmt.Errorf("token revoked or not in whitelist: %w", err)
		}
	}

	return claims, nil
}

// extractToken 按配置的来源（query/header）提取 token
func (d *jwtConnectionTokenDecoder) extractToken(r *http.Request, set *wscconfig.ConnectionTokenSet) string {
	source := gccommon.AttributeSourceType(set.GetTokenSource())
	key := set.GetTokenParamName()
	src := gccommon.AttributeSource{Type: source, Key: key}
	return gccommon.ExtractFromSource(r, src)
}

// parseAndVerifyToken 两段式解码：
//  1. jwt.ParseUnverified 拿 claims.aid（无验签，仅用于路由选 set）
//  2. aid → set 查找；找不到走 defaultAppID；按 set 密钥严格验签
//
// 安全说明：ParseUnverified 不校验签名，但紧接着第二步用对应 set 密钥严格验签，不存在降级风险
func (d *jwtConnectionTokenDecoder) parseAndVerifyToken(tokenStr string) (*ConnectionClaims, *wscconfig.ConnectionTokenSet, error) {
	// 第一段：无验签解析，仅拿 claims.aid 用于路由
	// ParseUnverified 是 *Parser 方法，不校验签名，紧接着第二段会用对应 set 密钥严格验签
	unverifiedClaims := &ConnectionClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(tokenStr, unverifiedClaims); err != nil {
		return nil, nil, fmt.Errorf("malformed token: %w", err)
	}

	// 按 aid 选 set；aid 为空或未知走 defaultAppID
	appID := unverifiedClaims.AppID
	if appID == "" {
		appID = d.defaultAppID
	}
	set, ok := d.sets[appID]
	if !ok {
		// 未知 appid 走 default 兜底
		set = d.sets[d.defaultAppID]
		appID = d.defaultAppID
	}
	if set == nil {
		return nil, nil, fmt.Errorf("no token set for app_id=%q and default missing", unverifiedClaims.AppID)
	}

	// 第二段：用 set 的密钥严格验签
	claims := &ConnectionClaims{}
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{set.GetAlgorithm()}),
	}
	if issuer := set.GetIssuer(); issuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(issuer))
	}
	if audience := set.GetAudience(); audience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(audience))
	}

	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (interface{}, error) {
		return []byte(set.GetSigningKey()), nil
	}, parserOpts...)
	if err != nil {
		return nil, nil, err
	}

	// 回填 appid（若 claims.aid 为空则用 default）
	if claims.AppID == "" {
		claims.AppID = appID
	}

	return claims, set, nil
}

// checkWhitelist 校验 Redis 白名单（按 set 独立前缀）
// 设计取舍: Redis 故障时选择降级放行（避免 Redis 抖动锁死所有连接）
// 如需更严格策略，可在配置中关闭 UseRedis，强制仅依赖 JWT 自身验证
func (d *jwtConnectionTokenDecoder) checkWhitelist(ctx context.Context, set *wscconfig.ConnectionTokenSet, tokenStr string) error {
	key := whitelistKey(set.GetRedisKeyPrefix(), tokenStr)

	var n int64
	err := contextx.WithTimeoutOrBackground(ctx, tokenRedisTimeout, func(wctx context.Context) error {
		var e error
		n, e = d.redisCli.Exists(wctx, key).Result()
		return e
	})
	if err != nil {
		d.logger.WarnKV("[ConnectionToken] Redis 白名单校验失败，降级放行",
			"error", err, "key", key, "app_id", set.GetAppID())
		return nil
	}
	if n == 0 {
		return fmt.Errorf("token not in whitelist (key=%s, app_id=%s)", key, set.GetAppID())
	}
	return nil
}

// ============================================================================
// Token 工具函数（业务层调用）
// ============================================================================

// IssueConnectionToken 生成连接 Token
// 业务层（如登录服务）调用此函数生成 token 下发给客户端
// 按 appID 选择对应 set 签发；若启用 Redis，写入对应 set 前缀的白名单（带 TTL）
//
// 参数:
//   - ctx: 上下文（透传，由调用方控制超时；不再内部 context.Background）
//   - cfg: 连接 Token 配置（含多 appid）
//   - appID: 目标 appid（不存在时走 default 兜底）
//   - redisCli: Redis 客户端（对应 set 未启用 Redis 时可为 nil）
//   - claims: 连接信息（UserID 必填；AppID 为空时自动填充 appID；ExpiresAt/IssuedAt/ID 缺省时自动填充）
//
// 返回:
//   - string: 签名后的 JWT token
//   - error: 生成或写入 Redis 失败时返回
func IssueConnectionToken(ctx context.Context, cfg *wscconfig.ConnectionToken, appID string, redisCli redis.UniversalClient, claims *ConnectionClaims) (string, error) {
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
	if set == nil {
		return "", fmt.Errorf("no token set for app_id=%q", appID)
	}
	if set.GetSigningKey() == "" {
		return "", fmt.Errorf("signing key is required for app_id=%q", appID)
	}

	// 回填 appid 到 claims
	if claims.AppID == "" {
		claims.AppID = appID
	}

	// 补充默认 RegisteredClaims
	if claims.ExpiresAt == nil {
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(set.GetExpiresTime()))
	}
	if claims.IssuedAt == nil {
		claims.IssuedAt = jwt.NewNumericDate(time.Now())
	}
	if claims.ID == "" {
		claims.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}

	method := jwt.GetSigningMethod(set.GetAlgorithm())
	if method == nil {
		return "", fmt.Errorf("unsupported signing algorithm: %s", set.GetAlgorithm())
	}

	tok := jwt.NewWithClaims(method, claims)
	tokenStr, err := tok.SignedString([]byte(set.GetSigningKey()))
	if err != nil {
		return "", fmt.Errorf("sign token failed: %w", err)
	}

	// 写入 Redis 白名单（若启用，用 set 独立前缀）
	if set.IsRedisEnabled() && redisCli != nil {
		key := whitelistKey(set.GetRedisKeyPrefix(), tokenStr)
		ttl := time.Until(claims.ExpiresAt.Time)
		ttl = mathx.IfLeZero(ttl, set.GetExpiresTime())
		if err := contextx.WithTimeoutOrBackground(ctx, tokenRedisTimeout, func(wctx context.Context) error {
			return redisCli.Set(wctx, key, "1", ttl).Err()
		}); err != nil {
			return "", fmt.Errorf("write token to redis whitelist failed (app_id=%q): %w", appID, err)
		}
	}

	return tokenStr, nil
}

// RevokeConnectionToken 吊销连接 Token
// 多节点环境下，吊销后所有节点立即生效（依赖 Redis 白名单）
// 按 appID 选择对应 set，删除其前缀下的白名单 key
//
// 参数:
//   - ctx: 上下文（透传）
//   - cfg: 连接 Token 配置（含多 appid）
//   - appID: 目标 appid（不存在时走 default 兜底）
//   - redisCli: Redis 客户端
//   - tokenStr: 要吊销的 token
//
// 返回:
//   - error: 删除失败时返回（token 本身仍有效，直到自然过期）
func RevokeConnectionToken(ctx context.Context, cfg *wscconfig.ConnectionToken, appID string, redisCli redis.UniversalClient, tokenStr string) error {
	if cfg == nil || redisCli == nil {
		return nil
	}

	tokens, defaultID, err := cfg.ResolveTokens()
	if err != nil {
		return err
	}

	set, ok := tokens[appID]
	if !ok {
		set = tokens[defaultID]
	}
	// Redis 是否启用由目标 set 决定（支持新旧两种配置）
	if set == nil || !set.IsRedisEnabled() {
		return nil
	}

	key := whitelistKey(set.GetRedisKeyPrefix(), tokenStr)
	return contextx.WithTimeoutOrBackground(ctx, tokenRedisTimeout, func(wctx context.Context) error {
		return redisCli.Del(wctx, key).Err()
	})
}

// whitelistKey 生成 Redis 白名单 key
// 使用 token 的 SHA256 哈希作为标识，避免在 key 中暴露原始 token
// prefix 由 set 提供，实现 per-appid 隔离
func whitelistKey(prefix, tokenStr string) string {
	sum := sha256.Sum256([]byte(tokenStr))
	return prefix + "whitelist:" + hex.EncodeToString(sum[:])
}

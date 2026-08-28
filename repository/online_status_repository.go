/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-19 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-28 18:16:26
 * @FilePath: \go-wsc\repository\online_status_repository.go
 * @Description: 客户端在线状态管理 - 支持 Redis 分布式存储
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package repository

import (
	"bytes"
	"compress/zlib"
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/zipx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/redis/go-redis/v9"
)

// clientMatchesRouteEnvelope 检查客户端是否匹配 ctx 路由信封的 appID+namespace
//
// 语义与 hub.ClientMatchesEnvelope 保持一致（单一真相源）：
//   - appID 严格相等（入口层已归一化为 DefaultAppID，无空值兼容）
//   - namespace 非空时严格相等；空=全局广播，跳过 ns 过滤匹配所有
//
// 注意：调用方应先判断 hasRoute（routing.RoutingFromContext(ctx) != nil）再调用本函数，
// 无路由信封时退化为不过滤，与 hub.ForEachUserClientFiltered 的空值语义对称，兼容无路由 ctx 边界场景
func clientMatchesRouteEnvelope(client *Client, appID, namespace string) bool {
	if client == nil {
		return false
	}
	if client.AppID != appID {
		return false
	}
	if namespace != "" && client.Namespace != namespace {
		return false
	}
	return true
}

const (
	// maxBatchSize 单次 Lua 脚本处理的最大客户端数量，避免 Redis 阻塞
	maxBatchSize = 100
	// compressionThreshold 压缩阈值，低于此大小不压缩（小数据压缩反而增大）
	compressionThreshold = 512
)

// zlibWriterPool 复用 zlib.Writer，避免每次压缩都分配新对象
var zlibWriterPool = sync.Pool{
	New: func() any {
		return zlib.NewWriter(nil)
	},
}

// zlibCompressWithPool 使用 sync.Pool 复用 zlib.Writer 进行压缩
func zlibCompressWithPool(data []byte) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, len(data)/2))
	writer := zlibWriterPool.Get().(*zlib.Writer)
	defer zlibWriterPool.Put(writer)

	writer.Reset(buf)
	if _, err := writer.Write(data); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result, nil
}

// luaBatchSetClientsOnline Lua 脚本：批量设置客户端在线（使用 ZSET 存储过期时间）
//
// KEYS[1] = keyPrefix (用于构建其他 key)
//
// ARGV[1] = ttl (秒)
// ARGV[2] = currentTime (当前时间戳，用于清理过期数据)
// ARGV[3] = clientCount (客户端数量)
// ARGV[4..] = 每个客户端的数据，格式：clientID|userID|nodeID|userType|expireTime|clientData
//
// 返回值: 成功处理的客户端数量
const luaBatchSetClientsOnline = `
local keyPrefix = KEYS[1]
local ttl = tonumber(ARGV[1])
local currentTime = tonumber(ARGV[2])
local clientCount = tonumber(ARGV[3])
local successCount = 0

for i = 1, clientCount do
    local idx = 3 + i
    local data = ARGV[idx]
    
    -- 解析数据: clientID|userID|nodeID|userType|expireTime|clientData
    local sep = string.byte("|")
    local parts = {}
    local lastPos = 1
    
    -- 手动分割字符串（只分割前5个字段）
    for j = 1, 5 do
        local pos = string.find(data, "|", lastPos, true)
        if pos then
            table.insert(parts, string.sub(data, lastPos, pos - 1))
            lastPos = pos + 1
        end
    end
    -- 剩余部分是 clientData
    table.insert(parts, string.sub(data, lastPos))
    
    if #parts == 6 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local expireTime = tonumber(parts[5])
        local clientData = parts[6]
        
        local clientKey = keyPrefix .. "client:" .. clientID
        local userClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local allUsersKey = keyPrefix .. "all_users"
        local typeKey = keyPrefix .. "type:" .. userType
        
        -- 清理该用户的过期客户端（在添加新客户端之前）
        redis.call('ZREMRANGEBYSCORE', userClientsKey, '-inf', currentTime)
        
        -- 存储客户端信息
        redis.call('SETEX', clientKey, ttl, clientData)
        -- 节点归属标记：离线清理时校验，防止旧节点延迟清理误删已迁移到其他节点的同名条目
        redis.call('SETEX', keyPrefix .. "owner:" .. clientID, ttl, nodeID)
        
        -- 添加到集合（全部使用 ZADD 存储过期时间）
        redis.call('ZADD', userClientsKey, expireTime, clientID)
        redis.call('EXPIRE', userClientsKey, ttl)
        redis.call('ZADD', nodeClientsKey, expireTime, clientID)
        redis.call('ZADD', allUsersKey, expireTime, userID)
        redis.call('ZADD', typeKey, expireTime, userID)
        
        successCount = successCount + 1
    end
end

return successCount
`

// luaBatchSetClientsOffline Lua 脚本：批量设置客户端离线（使用 ZSET）
//
// KEYS[1] = keyPrefix (用于构建其他 key)
//
// ARGV[1] = currentTime (当前时间戳，用于清理过期数据)
// ARGV[2] = clientCount (客户端数量)
// ARGV[3..] = 每个客户端的数据，格式：clientID|userID|nodeID|userType
//
// 返回值: 成功处理的客户端数量
const luaBatchSetClientsOffline = `
local keyPrefix = KEYS[1]
local currentTime = tonumber(ARGV[1])
local clientCount = tonumber(ARGV[2])
local successCount = 0

for i = 1, clientCount do
    local idx = 2 + i
    local data = ARGV[idx]
    
    -- 解析数据: clientID|userID|nodeID|userType
    local parts = {}
    for part in string.gmatch(data, "([^|]+)") do
        table.insert(parts, part)
    end
    
    if #parts == 4 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        
        -- 构建 keys
        local clientKey = keyPrefix .. "client:" .. clientID
        local ownerKey = keyPrefix .. "owner:" .. clientID
        local userClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local allUsersKey = keyPrefix .. "all_users"
        local typeKey = keyPrefix .. "type:" .. userType

        -- 🔒 节点归属校验：clientID 已被其他节点接管（断线重连迁移）时，
        -- 仅清理本节点集合，不删共享索引（client/user_clients/all_users/type），避免误删新节点的条目
        -- owner 不存在（旧版本数据，未写 owner key）视为本节点归属，保持向后兼容
        local owner = redis.call('GET', ownerKey)
        local migrated = (owner ~= false) and (owner ~= nodeID)

        -- 本节点集合总是清理（无论归属如何，node_clients:<nodeID> 只含本节点自己的条目）
        redis.call('ZREM', nodeClientsKey, clientID)

        if not migrated then
            -- 删除客户端信息与归属标记
            redis.call('DEL', clientKey)
            redis.call('DEL', ownerKey)

            -- 从集合中移除（ZREM 用于 ZSET）
            redis.call('ZREM', userClientsKey, clientID)

            -- 清理该用户的过期客户端
            redis.call('ZREMRANGEBYSCORE', userClientsKey, '-inf', currentTime)

            -- 检查用户是否还有其他未过期的客户端（使用 ZCOUNT 而不是 ZCARD）
            local remainingCount = redis.call('ZCOUNT', userClientsKey, currentTime, '+inf')
            if remainingCount == 0 then
                redis.call('DEL', userClientsKey)
                redis.call('ZREM', allUsersKey, userID)
                redis.call('ZREM', typeKey, userID)
            end
        end

        successCount = successCount + 1
    end
end

return successCount
`

// luaCleanupExpiredClients Lua 脚本：清理当前节点的过期客户端（使用 ZSET）
//
// KEYS[1] = keyPrefix (用于构建其他 key)
// KEYS[2] = nodeClientsKey (当前节点的客户端集合)
//
// ARGV[1] = nodeID (当前节点ID)
// ARGV[2] = currentTime (当前时间戳，用于清理 ZSET 中的过期数据)
//
// 返回值: 清理的客户端数量
const luaCleanupExpiredClients = `
local keyPrefix = KEYS[1]
local nodeClientsKey = KEYS[2]
local nodeID = ARGV[1]
local currentTime = tonumber(ARGV[2])

-- 清理 node_clients ZSET 中的过期数据
local cleaned = redis.call('ZREMRANGEBYSCORE', nodeClientsKey, '-inf', currentTime)

-- 清理 all_users 和 type ZSET 中的过期数据
local allUsersKey = keyPrefix .. "all_users"
redis.call('ZREMRANGEBYSCORE', allUsersKey, '-inf', currentTime)

-- 清理所有 type ZSET（这里需要知道所有可能的 userType）
-- 简化处理：只清理常见的两种类型
local typeCustomerKey = keyPrefix .. "type:customer"
local typeAgentKey = keyPrefix .. "type:agent"
redis.call('ZREMRANGEBYSCORE', typeCustomerKey, '-inf', currentTime)
redis.call('ZREMRANGEBYSCORE', typeAgentKey, '-inf', currentTime)

return cleaned
`

// OnlineStatusRepository 在线状态仓库接口
type OnlineStatusRepository interface {
	// ========== 客户端连接管理 ==========

	// SetClientOnline 设置客户端在线（支持多设备）
	SetClientOnline(ctx context.Context, client *Client) error

	// SetClientOffline 设置指定客户端离线
	SetClientOffline(ctx context.Context, client *Client) error

	// SetOffline 设置用户所有客户端离线
	SetOffline(ctx context.Context, userID string) error

	// GetClient 获取客户端信息
	GetClient(ctx context.Context, clientID string) (*Client, error)

	// GetClientOwner 获取 clientID 当前归属节点（上线脚本写入 owner key）
	// 返回空串表示无归属记录（旧数据或已过期）；用于检测同 clientID 跨节点迁移
	GetClientOwner(ctx context.Context, clientID string) (string, error)

	// GetUserClients 获取用户的所有在线客户端
	GetUserClients(ctx context.Context, userID string) ([]*Client, error)

	// UpdateClientHeartbeat 更新客户端心跳
	UpdateClientHeartbeat(ctx context.Context, clientID string) error

	// ========== 用户在线状态查询 ==========

	// IsUserOnline 检查用户是否在线（任意设备，按 ctx 路由信封 appID+namespace 隔离）
	// 路径自适应：bitmap 启用 + 有路由信封时走 HGET→GETBIT 单次 Lua 往返，
	// 否则回退 unscoped ZCount / GetUserClients 全量过滤，永远最终一致
	IsUserOnline(ctx context.Context, userID string) (bool, error)

	// BatchIsUserOnline 批量在线判定，返回 map[userID]bool（含全部查询的 userID）
	BatchIsUserOnline(ctx context.Context, userIDs []string) (map[string]bool, error)

	// GetAllOnlineUsers 获取所有在线用户ID列表
	GetAllOnlineUsers(ctx context.Context) ([]string, error)

	// GetOnlineCount 获取在线用户总数
	GetOnlineCount(ctx context.Context) (int64, error)

	// GetOnlineUsersByType 根据用户类型获取在线用户
	GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error)

	// ========== 分布式节点查询 ==========

	// GetUserNodes 获取用户所在的所有节点（支持多设备）
	GetUserNodes(ctx context.Context, userID string) ([]string, error)

	// BatchGetUserNodes 批量获取多个用户所在的所有节点（Pipeline 优化，避免 N+1 查询）
	// 返回 map[userID][]nodeIDs，未找到的 userID 不在 map 中
	BatchGetUserNodes(ctx context.Context, userIDs []string) (map[string][]string, error)

	// GetNodeClients 获取节点的所有在线客户端
	GetNodeClients(ctx context.Context, nodeID string) ([]*Client, error)

	// GetNodeUsers 获取节点的所有在线用户ID
	GetNodeUsers(ctx context.Context, nodeID string) ([]string, error)

	// ========== 批量操作 ==========

	// BatchSetClientsOnline 批量设置客户端在线
	BatchSetClientsOnline(ctx context.Context, clients []*Client) error

	// BatchSetClientsOffline 批量设置客户端离线
	BatchSetClientsOffline(ctx context.Context, clientIDs []string) error

	// BatchSetClientsOfflineWithInfo 批量设置客户端离线（使用已知的客户端信息）
	// 客户端信息已知时使用，避免从 Redis 查询，确保即使 client key 已被删除也能清理 ZSET
	BatchSetClientsOfflineWithInfo(ctx context.Context, clients []*Client) error

	// ========== 维护清理 ==========

	// CleanupExpired 清理当前节点的过期客户端
	CleanupExpired(ctx context.Context, nodeID string) (int64, error)
}

// RedisOnlineStatusRepository Redis 实现
type RedisOnlineStatusRepository struct {
	client             redis.UniversalClient
	keyPrefix          string        // key 前缀
	ttl                time.Duration // 过期时间
	enableCompression  bool          // 是否启用压缩
	compressionMinSize int           // 压缩阈值（字节）

	// Bitmap 分层配置（短期通过环境变量读取，长期迁移到 wscconfig.OnlineStatus）
	// 设计见 .trae/documents/bitmap-online-status-refactor.md
	bitmapEnabled   bool            // 是否启用 bitmap 快速路径（灰度开关，false 时走旧 Lua 路径）
	bitmapTTL       time.Duration   // bitmap EXPIRE（心跳间续期，过期则回退 ZSET 兜底）
	maxBitmapOffset int64           // offset 上限（防恶意膨胀，0=不限制）
	maxCachedUIDs   int             // L1 缓存容量上限
	migrationPhase  string          // 灰度阶段：dual-write|new-only|disabled
	uidCache        *uidOffsetCache // userID→offset 进程内 L1 缓存（命中零网络）
}

// NewRedisOnlineStatusRepository 创建 Redis 在线状态仓库
//
// Bitmap 配置短期通过环境变量读取（见 constants.go 的 env* 常量），
// 未设置时用默认值。长期方案是在 wscconfig.OnlineStatus 正式声明字段后从此处读取。
func NewRedisOnlineStatusRepository(client redis.UniversalClient, config *wscconfig.OnlineStatus) OnlineStatusRepository {
	maxCachedUIDs := loadEnvInt(envMaxCachedUIDs, constants.DefaultMaxCachedUIDs)
	repo := &RedisOnlineStatusRepository{
		client:             client,
		keyPrefix:          mathx.IfNotEmpty(config.KeyPrefix, constants.DefaultOnlineKeyPrefix),
		ttl:                config.TTL,
		enableCompression:  config.EnableCompression,
		compressionMinSize: mathx.IfNotZero(config.CompressionMinSize, 512),
		bitmapEnabled:      loadEnvBool(envEnableBitmap, false),
		bitmapTTL:          loadBitmapTTL(config),
		maxBitmapOffset:    int64(loadEnvInt(envMaxBitmapOffset, constants.DefaultMaxBitmapOffset)),
		maxCachedUIDs:      maxCachedUIDs,
		migrationPhase:     loadEnvString(envBitmapMigrationPhase, constants.DefaultBitmapMigrationPhase),
		uidCache:           newUIDOffsetCache(maxCachedUIDs),
	}
	return repo
}

// loadEnvBool 读取布尔环境变量，未设置或解析失败返回默认值
func loadEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return defaultVal
}

// loadEnvInt 读取整型环境变量，未设置或解析失败返回默认值
func loadEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

// loadEnvString 读取字符串环境变量，未设置返回默认值
func loadEnvString(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

// loadBitmapTTL 读取 bitmap TTL：优先环境变量，未设置时按 HeartbeatRefreshInterval × 4 推导
func loadBitmapTTL(config *wscconfig.OnlineStatus) time.Duration {
	if v := os.Getenv(envBitmapTTL); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	// 默认 HeartbeatRefreshInterval × 4，HeartbeatRefreshInterval 未配置时用 2s
	hbInterval := 2 * time.Second
	if config != nil && config.HeartbeatRefreshInterval > 0 {
		hbInterval = config.HeartbeatRefreshInterval
	}
	ttl := hbInterval * 4
	if ttl <= 0 {
		ttl = time.Duration(constants.DefaultBitmapTTLSeconds) * time.Second
	}
	return ttl
}

// isDualWrite 是否处于双写阶段（scoped + unscoped ZSET 同时写）
func (r *RedisOnlineStatusRepository) isDualWrite() bool {
	return r.bitmapEnabled && r.migrationPhase == "dual-write"
}

// ============================================================================
// Redis Key 生成方法
// ============================================================================

// GetClientKey 获取客户端详细信息的 key
// 性能：字符串拼接替代 fmt.Sprintf，减少分配
func (r *RedisOnlineStatusRepository) GetClientKey(clientID string) string {
	return r.keyPrefix + "client:" + clientID
}

// GetUserClientsKey 获取用户客户端集合的 key
func (r *RedisOnlineStatusRepository) GetUserClientsKey(userID string) string {
	return r.keyPrefix + "user_clients:" + userID
}

// GetNodeClientsKey 获取节点客户端集合的 key
func (r *RedisOnlineStatusRepository) GetNodeClientsKey(nodeID string) string {
	return r.keyPrefix + "node_clients:" + nodeID
}

// GetUserTypeSetKey 获取用户类型集合的 key
func (r *RedisOnlineStatusRepository) GetUserTypeSetKey(userType models.UserType) string {
	return r.keyPrefix + "type:" + userType.String()
}

// GetAllUsersSetKey 获取所有在线用户集合的 key
func (r *RedisOnlineStatusRepository) GetAllUsersSetKey() string {
	return r.keyPrefix + "all_users"
}

// ============================================================================
// Bitmap 分层 Key 生成方法
//
// Key 命名规范（前缀 wsc:online:）：
//   - user_clients:<appID>:<ns>:<userID>   scoped ZSET（按信封分桶，让 IsUserOnline 永远走 ZCount 快路径）
//   - user_clients:<userID>                  unscoped ZSET（兼容旧 key，dual-write 阶段双写，SetOffline 跨信封用）
//   - bm:<appID>:<ns>                        scoped bitmap（GETBIT 判否）
//   - bm:<appID>:__global__                  global bitmap（全局广播查询 ns="" 时命中）
//   - uid_map                                Hash：field=userID, value=数字 offset
//   - uid_counter                            String：INCR 自增计数器（首次分配 offset）
// ============================================================================

// GetScopedUserClientsKey 获取信封分桶的用户客户端 ZSET key
// ns=="" 归一化为 constants.DefaultNamespace（与 Lua 脚本 ns 兜底一致）
func (r *RedisOnlineStatusRepository) GetScopedUserClientsKey(userID, appID, ns string) string {
	if ns == "" {
		ns = constants.DefaultNamespace
	}
	return r.keyPrefix + "user_clients:" + appID + ":" + ns + ":" + userID
}

// GetScopedBitmapKey 获取信封范围的 bitmap key
// ns=="" 归一化为 constants.DefaultNamespace（非广播场景的默认命名空间）
func (r *RedisOnlineStatusRepository) GetScopedBitmapKey(appID, ns string) string {
	if ns == "" {
		ns = constants.DefaultNamespace
	}
	return r.keyPrefix + bitmapKeySuffix + appID + ":" + ns
}

// GetGlobalBitmapKey 获取 appID 范围的全局广播 bitmap key
// 全局广播（ns=""）查询时命中此 bitmap，覆盖该 appID 下所有 ns 的在线用户
func (r *RedisOnlineStatusRepository) GetGlobalBitmapKey(appID string) string {
	return r.keyPrefix + bitmapKeySuffix + appID + ":" + constants.GlobalBitmapNS
}

// GetUIDMapKey 获取 userID→offset 的 Hash key
func (r *RedisOnlineStatusRepository) GetUIDMapKey() string {
	return r.keyPrefix + uidMapKeySuffix
}

// GetUIDCounterKey 获取 offset 自增计数器 key
func (r *RedisOnlineStatusRepository) GetUIDCounterKey() string {
	return r.keyPrefix + uidCounterKeySuffix
}

// normalizeAppID 归一化 appID：空值补 constants.DefaultAppID（仅 bitmap/scoped key 构造兜底）
// 注意：此处不改动 ctx 内路由信封，实际 appID 归一化在入口层（routing.Route.Inject / handleRegister）完成，
// 此处仅兜底防止空 appID 生成畸形 key（如 "bm::ns"），与 Lua 写入的 appID（入口层已归一化为 DefaultAppID）一致
func (r *RedisOnlineStatusRepository) normalizeAppID(appID string) string {
	if appID == "" {
		return constants.DefaultAppID
	}
	return appID
}

// ============================================================================
// 客户端连接管理
// ============================================================================

// SetClientOnline 设置客户端在线（调用批量方法）
func (r *RedisOnlineStatusRepository) SetClientOnline(ctx context.Context, client *Client) error {
	return r.BatchSetClientsOnline(ctx, []*Client{client})
}

// buildOfflineBatchArg 构造单条离线 ARGV 项
// bitmapEnabled=true: clientID|userID|nodeID|userType|appID|ns（6 段，供 luaBatchSetClientsOfflineV2 清 scoped/global bitmap）
// bitmapEnabled=false: clientID|userID|nodeID|userType（4 段，供原 luaBatchSetClientsOffline）
func (r *RedisOnlineStatusRepository) buildOfflineBatchArg(clientID, userID, nodeID string, userType models.UserType, appID, ns string) string {
	base := clientID + "|" + userID + "|" + nodeID + "|" + string(userType)
	if r.bitmapEnabled {
		return base + "|" + r.normalizeAppID(appID) + "|" + ns
	}
	return base
}

// offlineScript 根据 bitmap 开关选择离线 Lua 脚本（回滚开关）
func (r *RedisOnlineStatusRepository) offlineScript() string {
	if r.bitmapEnabled {
		return luaBatchSetClientsOfflineV2
	}
	return luaBatchSetClientsOffline
}

// SetClientOffline 设置指定客户端离线（调用批量方法）
func (r *RedisOnlineStatusRepository) SetClientOffline(ctx context.Context, client *Client) error {
	if client == nil {
		return errorx.WrapError("client cannot be nil")
	}

	currentTime := time.Now().Unix()

	// 直接使用提供的客户端信息构建参数，避免从 Redis 查询
	// 性能：字符串拼接替代 fmt.Sprintf，减少分配
	batchData := r.buildOfflineBatchArg(client.ID, client.UserID, client.NodeID, client.UserType, client.AppID, client.Namespace)

	args := []any{currentTime, 1, batchData}

	// 使用 Lua 脚本删除
	keys := []string{r.keyPrefix}
	_, err := r.client.Eval(ctx, r.offlineScript(), keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute lua script", err)
	}

	return nil
}

// SetOffline 设置用户所有客户端离线
func (r *RedisOnlineStatusRepository) SetOffline(ctx context.Context, userID string) error {
	if userID == "" {
		return errorx.WrapError("userID cannot be empty")
	}

	// 获取用户所有客户端ID（使用 ZRANGE 获取 ZSET 中的所有成员）
	clientIDs, err := r.client.ZRange(ctx, r.GetUserClientsKey(userID), 0, -1).Result()
	if err != nil {
		return err
	}

	if len(clientIDs) == 0 {
		return nil
	}

	// 批量下线
	return r.BatchSetClientsOffline(ctx, clientIDs)
}

// GetClient 获取客户端信息
func (r *RedisOnlineStatusRepository) GetClient(ctx context.Context, clientID string) (*Client, error) {
	data, err := r.client.Get(ctx, r.GetClientKey(clientID)).Result()
	if err != nil {
		return nil, err
	}

	return zipx.ZlibSmartDecompressObject[*Client]([]byte(data))
}

// GetClientOwner 获取 clientID 当前归属节点
// owner key 由上线 Lua 脚本写入（TTL 与 client key 一致）；redis.Nil（旧数据/已过期）返回空串不报错
func (r *RedisOnlineStatusRepository) GetClientOwner(ctx context.Context, clientID string) (string, error) {
	val, err := r.client.Get(ctx, r.keyPrefix+"owner:"+clientID).Result()
	if err == redis.Nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return val, nil
}

// GetUserClients 获取用户的所有在线客户端
//
// 路由隔离：按 ctx 路由信封的 appID+namespace 过滤，只返回匹配路由信封的客户端
// 即使同名 userID 跨 app/namespace 同时在线，也只返回当前路由信封下的设备
// 无路由信封（ctx 无 RoutingContext）时退化为不过滤，返回该 userID 的全部在线设备
func (r *RedisOnlineStatusRepository) GetUserClients(ctx context.Context, userID string) ([]*Client, error) {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	hasRoute := routing.RoutingFromContext(ctx) != nil

	// 选 ZRANGE key：
	//   - bitmap 启用 + 有路由信封：scoped key（ZSET 已按 appID/ns 分桶，无需逐客户端过滤）
	//   - 其他：unscoped key（需逐客户端按 appID/ns 过滤，原逻辑）
	scoped := r.bitmapEnabled && hasRoute
	var zsetKey string
	if scoped {
		zsetKey = r.GetScopedUserClientsKey(userID, r.normalizeAppID(appID), ns)
	} else {
		zsetKey = r.GetUserClientsKey(userID)
	}

	clientIDs, err := r.client.ZRange(ctx, zsetKey, 0, -1).Result()
	if err != nil {
		return nil, err
	}

	// dual-write 阶段 scoped miss 回退 unscoped（兼容存量连接，切换前只有 unscoped 数据）
	if len(clientIDs) == 0 && scoped && r.isDualWrite() {
		zsetKey = r.GetUserClientsKey(userID)
		clientIDs, err = r.client.ZRange(ctx, zsetKey, 0, -1).Result()
		if err != nil {
			return nil, err
		}
	}

	if len(clientIDs) == 0 {
		// NewError(type, userID)：userID 作为注册模板 "user not found: %s" 的格式化参数
		return nil, errorx.NewError(models.ErrTypeUserNotFound, userID)
	}

	// 批量获取客户端信息
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(clientIDs))
	for i, clientID := range clientIDs {
		cmds[i] = pipe.Get(ctx, r.GetClientKey(clientID))
	}

	_, err = pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, err
	}

	clients := make([]*Client, 0, len(clientIDs))
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}

		client, err := zipx.ZlibSmartDecompressObject[*Client]([]byte(data))
		if err != nil {
			continue
		}
		// scoped 已分桶无需过滤；unscoped + 有路由信封需逐客户端按 appID+namespace 过滤
		// （无路由信封退化为不过滤，兼容无路由 ctx 边界场景）
		if !scoped && hasRoute && !clientMatchesRouteEnvelope(client, appID, ns) {
			continue
		}
		clients = append(clients, client)
	}

	return clients, nil
}

// UpdateClientHeartbeat 更新客户端心跳
func (r *RedisOnlineStatusRepository) UpdateClientHeartbeat(ctx context.Context, clientID string) error {
	client, err := r.GetClient(ctx, clientID)
	if err != nil {
		// redis.Nil 表示客户端不存在（可能已过期或被清理），这是正常情况
		if err == redis.Nil {
			return nil
		}
		return err
	}

	now := time.Now()
	client.SetLastHeartbeat(now)
	client.SetLastSeen(now)

	return r.SetClientOnline(ctx, client)
}

// ============================================================================
// 用户在线状态查询
// ============================================================================

// zcountScoped scoped ZCount 兜底（bitmap miss 时确认在线状态）
// 有路由信封走 scoped key（已分桶），无路由信封退化 unscoped key（兼容）
func (r *RedisOnlineStatusRepository) zcountScoped(ctx context.Context, userID, appID, ns string) (bool, error) {
	currentTime := time.Now().Unix()
	var key string
	if appID == "" {
		key = r.GetUserClientsKey(userID)
	} else {
		key = r.GetScopedUserClientsKey(userID, r.normalizeAppID(appID), ns)
	}
	count, err := r.client.ZCount(ctx, key, strconv.FormatInt(currentTime, 10), "+inf").Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// BatchIsUserOnline 批量在线判定
//
// 当前实现：逐个调用 IsUserOnline（每个 1 次 Lua 往返），正确性优先。
// 后续可优化为 Pipeline 批量 HGET uid_map + 批量 GETBIT（2 次往返覆盖 N 个用户）。
// 返回 map[userID]bool，所有查询的 userID 都在 map 中（离线为 false）
func (r *RedisOnlineStatusRepository) BatchIsUserOnline(ctx context.Context, userIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(userIDs))
	for _, uid := range userIDs {
		online, err := r.IsUserOnline(ctx, uid)
		if err != nil {
			// 单个查询失败不中断批量，记为离线（保守，避免误判在线触发跨节点路由）
			result[uid] = false
			continue
		}
		result[uid] = online
	}
	return result, nil
}

// IsUserOnline 检查用户是否在线（任意设备，按 ctx 路由信封 appID+namespace 隔离）
//
// 路径自适应（调用方无需感知 bitmap 开关，单一入口）：
//   - bitmap 启用 + 有路由信封：HGET uid_map → GETBIT 单次 Lua 往返
//     （1=在线 / 0=确定离线 uid_map 未分配 offset / -1=miss 兜底）
//   - bitmap 未启用 或 无路由信封：unscoped ZCount（无信封）/ GetUserClients 全量过滤（有信封）
//
// 性能：bitmap 命中时 1 次 Lua（GETBIT），远优于 GetUserClients 的 ZRANGE + GET ×N + N 次解压
// 兜底保证：bitmap 是判否加速层非真相源，任何 miss 都回退 ZSET ZCount，最终一致
func (r *RedisOnlineStatusRepository) IsUserOnline(ctx context.Context, userID string) (bool, error) {
	appID := routing.AppIDFromContext(ctx)
	ns := routing.NamespaceFromContext(ctx)
	hasRoute := routing.RoutingFromContext(ctx) != nil

	// bitmap 快速路径：启用 + 有路由信封时走 GETBIT
	if r.bitmapEnabled && hasRoute {
		normalizedAppID := r.normalizeAppID(appID)
		// 全局广播（ns=""）用 global bitmap，否则 scoped bitmap
		bitmapKey := r.GetScopedBitmapKey(normalizedAppID, ns)
		if ns == "" {
			bitmapKey = r.GetGlobalBitmapKey(normalizedAppID)
		}
		// Lua 原子查询：HGET uid_map → GETBIT
		keys := []string{r.GetUIDMapKey(), bitmapKey}
		args := []any{userID, r.maxBitmapOffset}
		result, err := r.client.Eval(ctx, luaIsUserOnline, keys, args...).Result()
		if err == nil {
			if ret, ok := result.(int64); ok {
				switch ret {
				case 1:
					return true, nil
				case 0:
					// uid_map 未分配 offset，用户从未上线，确定离线
					return false, nil
				case -1:
					// bitmap miss（offset 超限 或 bit=0 可能过期/淘汰/已下线），回退 ZCount 兜底
				}
			}
		}
		// Lua 失败或 miss，回退 zcountScoped 兜底（保守，宁可多查不误判）
		return r.zcountScoped(ctx, userID, appID, ns)
	}

	// 兜底路径：bitmap 未启用 或 无路由信封
	// 无路由信封时走 unscoped ZCount（兼容无路由 ctx 边界场景）
	if !hasRoute {
		currentTime := time.Now().Unix()
		count, err := r.client.ZCount(ctx, r.GetUserClientsKey(userID), strconv.FormatInt(currentTime, 10), "+inf").Result()
		if err != nil {
			return false, err
		}
		return count > 0, nil
	}
	// 有路由信封但 bitmap 未启用：加载客户端按 appID+namespace 过滤（scoped ZSET 无数据）
	clients, err := r.GetUserClients(ctx, userID)
	if err != nil {
		// 按错误类型判定（GetUserClients 返回带 userID 的新实例，值相等比较不可靠）
		if errorx.ClassifyError(err) == models.ErrTypeUserNotFound {
			return false, nil
		}
		return false, err
	}
	return len(clients) > 0, nil
}

// GetAllOnlineUsers 获取所有在线用户ID列表（使用 ZSET，自动过滤过期数据）
func (r *RedisOnlineStatusRepository) GetAllOnlineUsers(ctx context.Context) ([]string, error) {
	// 使用 ZRangeArgs 只获取未过期的用户（score > 当前时间）
	currentTime := time.Now().Unix()
	return r.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     r.GetAllUsersSetKey(),
		ByScore: true,
		Start:   strconv.FormatInt(currentTime, 10),
		Stop:    "+inf",
	}).Result()
}

// GetOnlineCount 获取在线用户总数（使用 ZCOUNT 统计未过期的用户）
func (r *RedisOnlineStatusRepository) GetOnlineCount(ctx context.Context) (int64, error) {
	currentTime := time.Now().Unix()
	return r.client.ZCount(ctx, r.GetAllUsersSetKey(), strconv.FormatInt(currentTime, 10), "+inf").Result()
}

// GetOnlineUsersByType 根据用户类型获取在线用户（使用 ZSET，自动过滤过期数据）
func (r *RedisOnlineStatusRepository) GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error) {
	// 使用 ZRangeArgs 只获取未过期的用户（score > 当前时间）
	currentTime := time.Now().Unix()
	return r.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key:     r.GetUserTypeSetKey(userType),
		ByScore: true,
		Start:   strconv.FormatInt(currentTime, 10),
		Stop:    "+inf",
	}).Result()
}

// ============================================================================
// 分布式节点查询
// ============================================================================

// GetUserNodes 获取用户所在的所有节点（支持多设备）
//
// 路由隔离：通过 GetUserClients 继承 appID+namespace 过滤，只返回当前路由信封下
// 用户在线的节点（同名 userID 跨 app/ns 在不同节点在线时，不会返回其他信封的节点）
func (r *RedisOnlineStatusRepository) GetUserNodes(ctx context.Context, userID string) ([]string, error) {
	clients, err := r.GetUserClients(ctx, userID)
	if err != nil {
		return nil, err
	}

	// 去重节点ID
	nodeSet := make(map[string]struct{})
	for _, client := range clients {
		if client.NodeID != "" {
			nodeSet[client.NodeID] = struct{}{}
		}
	}

	nodes := make([]string, 0, len(nodeSet))
	for nodeID := range nodeSet {
		nodes = append(nodes, nodeID)
	}

	return nodes, nil
}

// BatchGetUserNodes 批量获取多个用户所在的所有节点
// 使用 Redis Pipeline 批量查询，将 N 次网络往返压缩为 1 次（Pipeline 模式）
// 对每个 userID：ZRANGE 拿 clientIDs → GET 拿 NodeID → 去重
//
// Bitmap 分层：bitmapEnabled=true + 有路由信封时，每个用户用 scoped key（ZSET 已分桶，
// 无需逐客户端过滤）；dual-write 阶段 scoped miss 回退 unscoped（兼容存量连接，回退路径需按信封过滤）。
// 未启用或无路由信封时走 unscoped（需逐客户端按 appID+ns 过滤，原逻辑）。
func (r *RedisOnlineStatusRepository) BatchGetUserNodes(ctx context.Context, userIDs []string) (map[string][]string, error) {
	if len(userIDs) == 0 {
		return make(map[string][]string), nil
	}

	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	hasRoute := routing.RoutingFromContext(ctx) != nil
	scoped := r.bitmapEnabled && hasRoute
	normalizedAppID := r.normalizeAppID(appID)

	// needFilter[i]=true 表示该用户最终命中 unscoped ZSET（或 scoped fallback），需按信封过滤
	// scoped 命中时为 false（ZSET 已分桶，无需逐客户端过滤）
	needFilter := make([]bool, len(userIDs))
	if !scoped {
		// 全部走 unscoped，无路由信封时 needFilter 保持 false（不过滤）
		if hasRoute {
			for i := range needFilter {
				needFilter[i] = true
			}
		}
	}

	// Phase 1: Pipeline 并发 ZRANGE 拿到每个用户的 clientIDs
	pipe1 := r.client.Pipeline()
	zrangeCmds := make([]*redis.StringSliceCmd, len(userIDs))
	for i, userID := range userIDs {
		if scoped {
			zrangeCmds[i] = pipe1.ZRange(ctx, r.GetScopedUserClientsKey(userID, normalizedAppID, ns), 0, -1)
		} else {
			zrangeCmds[i] = pipe1.ZRange(ctx, r.GetUserClientsKey(userID), 0, -1)
		}
	}
	if _, err := pipe1.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	// userClientIDs[idx] = 该用户的 clientIDs
	userClientIDs := make([][]string, len(userIDs))
	for i, cmd := range zrangeCmds {
		clientIDs, err := cmd.Result()
		if err != nil || len(clientIDs) == 0 {
			userClientIDs[i] = nil
			continue
		}
		userClientIDs[i] = clientIDs
	}

	// Phase 1b: scoped 命中为空且处于 dual-write 阶段，回退 unscoped（兼容切换前存量连接）
	// 回退命中的用户需按信封过滤（unscoped ZSET 跨信封，未分桶）
	if scoped && r.isDualWrite() {
		var fallbackIdxs []int
		for i, cids := range userClientIDs {
			if len(cids) == 0 {
				fallbackIdxs = append(fallbackIdxs, i)
			}
		}
		if len(fallbackIdxs) > 0 {
			pipeFb := r.client.Pipeline()
			fallbackCmds := make([]*redis.StringSliceCmd, len(fallbackIdxs))
			for j, idx := range fallbackIdxs {
				fallbackCmds[j] = pipeFb.ZRange(ctx, r.GetUserClientsKey(userIDs[idx]), 0, -1)
			}
			if _, err := pipeFb.Exec(ctx); err != nil && err != redis.Nil {
				return nil, err
			}
			for j, cmd := range fallbackCmds {
				clientIDs, err := cmd.Result()
				if err != nil || len(clientIDs) == 0 {
					continue
				}
				idx := fallbackIdxs[j]
				userClientIDs[idx] = clientIDs
				// 回退到 unscoped，需按信封过滤
				needFilter[idx] = true
			}
		}
	}

	// 收集所有需要 GET 的 clientID → (userID, idx) 映射
	type clientRef struct {
		userID string
		idx    int // 在 userIDs 中的索引
	}
	allClientIDs := make([]string, 0, len(userIDs)*2)
	clientRefMap := make(map[string]clientRef, len(userIDs)*2)
	for i, clientIDs := range userClientIDs {
		if len(clientIDs) == 0 {
			continue
		}
		for _, cid := range clientIDs {
			if _, exists := clientRefMap[cid]; !exists {
				clientRefMap[cid] = clientRef{userID: userIDs[i], idx: i}
				allClientIDs = append(allClientIDs, cid)
			}
		}
	}

	if len(allClientIDs) == 0 {
		return make(map[string][]string), nil
	}

	// Phase 2: Pipeline 并发 GET 拿到每个 client 的数据（含 NodeID）
	pipe2 := r.client.Pipeline()
	getCmds := make([]*redis.StringCmd, len(allClientIDs))
	for i, cid := range allClientIDs {
		getCmds[i] = pipe2.Get(ctx, r.GetClientKey(cid))
	}
	// pipeline.Exec 返回 redis.Nil 是正常的（某些 key 可能已过期）
	if _, err := pipe2.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	// 解析结果，按 userID 聚合去重 NodeID
	result := make(map[string][]string, len(userIDs))
	userNodeSets := make([]map[string]struct{}, len(userIDs))
	for i := range userIDs {
		userNodeSets[i] = make(map[string]struct{})
	}

	for i, cmd := range getCmds {
		data, err := cmd.Result()
		if err != nil {
			continue // key 不存在或已过期，跳过
		}
		client, err := zipx.ZlibSmartDecompressObject[*Client]([]byte(data))
		if err != nil {
			continue
		}
		if client.NodeID == "" {
			continue
		}
		ref := clientRefMap[allClientIDs[i]]
		// needFilter=true 表示该用户命中 unscoped（或 scoped fallback），ZSET 未分桶需按信封过滤；
		// needFilter=false 表示 scoped 命中，ZSET 已分桶，直接收集
		if needFilter[ref.idx] && !clientMatchesRouteEnvelope(client, appID, ns) {
			continue
		}
		userNodeSets[ref.idx][client.NodeID] = struct{}{}
	}

	// 转换 map[string]struct{} → []string
	for i, nodeSet := range userNodeSets {
		if len(nodeSet) == 0 {
			continue
		}
		nodes := make([]string, 0, len(nodeSet))
		for nodeID := range nodeSet {
			nodes = append(nodes, nodeID)
		}
		result[userIDs[i]] = nodes
	}

	return result, nil
}

// GetNodeClients 获取节点的所有在线客户端
func (r *RedisOnlineStatusRepository) GetNodeClients(ctx context.Context, nodeID string) ([]*Client, error) {
	// 使用 ZRANGE 获取所有客户端（ZSET 存储）
	clientIDs, err := r.client.ZRange(ctx, r.GetNodeClientsKey(nodeID), 0, -1).Result()
	if err != nil {
		return nil, err
	}

	if len(clientIDs) == 0 {
		return []*Client{}, nil
	}

	// 批量获取客户端信息
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(clientIDs))
	for i, clientID := range clientIDs {
		cmds[i] = pipe.Get(ctx, r.GetClientKey(clientID))
	}

	_, err = pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return nil, err
	}

	// 解析客户端信息
	clients := make([]*Client, 0, len(clientIDs))
	for _, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}

		client, err := zipx.ZlibSmartDecompressObject[*Client]([]byte(data))
		if err != nil {
			continue
		}
		clients = append(clients, client)
	}

	return clients, nil
}

// GetNodeUsers 获取节点的所有在线用户ID
func (r *RedisOnlineStatusRepository) GetNodeUsers(ctx context.Context, nodeID string) ([]string, error) {
	clients, err := r.GetNodeClients(ctx, nodeID)
	if err != nil {
		return nil, err
	}

	// 去重用户ID
	userSet := make(map[string]struct{})
	for _, client := range clients {
		userSet[client.UserID] = struct{}{}
	}

	users := make([]string, 0, len(userSet))
	for userID := range userSet {
		users = append(users, userID)
	}

	return users, nil
}

// ============================================================================
// 批量操作（使用 Lua 脚本）
// ============================================================================

// BatchSetClientsOnline 批量设置客户端在线（使用 Lua 脚本）
// 优化：使用 sync.Pool 复用 zlib.Writer + 条件压缩 + 分批处理
//
// Bitmap 分层：bitmapEnabled=true 时走 luaBatchSetClientsOnlineV2，
// 在同一 Lua 内原子完成 offset 分配 + scoped/unscoped ZSET 双写 + bitmap SETBIT + EXPIRE，
// 不增加额外网络往返。bitmapEnabled=false 时走原 luaBatchSetClientsOnline（回滚开关）。
func (r *RedisOnlineStatusRepository) BatchSetClientsOnline(ctx context.Context, clients []*Client) error {
	if len(clients) == 0 {
		return nil
	}

	now := time.Now()
	currentTime := now.Unix()
	expireTime := now.Add(r.ttl).Unix()

	// 准备批量数据
	validClients := make([]string, 0, len(clients))

	for _, client := range clients {
		if client.ID == "" || client.UserID == "" || client.NodeID == "" {
			continue
		}

		data, err := json.Marshal(client)
		if err != nil {
			continue
		}

		// 优化：条件压缩 - 小数据不压缩，大数据使用 Pool 复用的 Writer
		clientData := data
		if r.enableCompression && len(data) >= compressionThreshold {
			compressed, compressErr := zlibCompressWithPool(data)
			if compressErr == nil && len(compressed)+zipx.ZlibPrefixLen < len(data) {
				// 添加 ZLIB: 前缀
				prefixed := make([]byte, zipx.ZlibPrefixLen+len(compressed))
				copy(prefixed, []byte(zipx.ZlibPrefix))
				copy(prefixed[zipx.ZlibPrefixLen:], compressed)
				clientData = prefixed
			}
		}

		// 性能：字符串拼接 + strconv.FormatInt 替代 fmt.Sprintf
		if r.bitmapEnabled {
			// V2 格式：clientID|userID|nodeID|userType|expireTime|appID|ns|clientData（8 段）
			// appID 兜底归一化（client.AppID 在 handleRegister 已归一化，此处兜底测试场景）
			// ns 保留原值（广播场景为空，Lua 内归一化为 __default_ns__）
			batchData := client.ID + "|" + client.UserID + "|" + client.NodeID + "|" +
				string(client.UserType) + "|" + strconv.FormatInt(expireTime, 10) + "|" +
				r.normalizeAppID(client.AppID) + "|" + client.Namespace + "|" + string(clientData)
			validClients = append(validClients, batchData)
		} else {
			// legacy 格式：clientID|userID|nodeID|userType|expireTime|clientData（6 段）
			batchData := client.ID + "|" + client.UserID + "|" + client.NodeID + "|" +
				string(client.UserType) + "|" + strconv.FormatInt(expireTime, 10) + "|" + string(clientData)
			validClients = append(validClients, batchData)
		}
	}

	if len(validClients) == 0 {
		return nil
	}

	// 优化：分批处理，避免单次 Lua 脚本参数过多导致 Redis 阻塞
	for batchStart := 0; batchStart < len(validClients); batchStart += maxBatchSize {
		batchEnd := batchStart + maxBatchSize
		if batchEnd > len(validClients) {
			batchEnd = len(validClients)
		}
		batch := validClients[batchStart:batchEnd]

		keys := []string{r.keyPrefix}
		var script string
		var args []any
		if r.bitmapEnabled {
			// V2 ARGV 头：ttl, currentTime, clientCount, bitmapTTL, maxOffset, ...clientData
			args = make([]any, 0, 5+len(batch))
			args = append(args, int(r.ttl.Seconds()))
			args = append(args, currentTime)
			args = append(args, len(batch))
			args = append(args, int(r.bitmapTTL.Seconds()))
			args = append(args, r.maxBitmapOffset)
			script = luaBatchSetClientsOnlineV2
		} else {
			// legacy ARGV 头：ttl, currentTime, clientCount, ...clientData
			args = make([]any, 0, 3+len(batch))
			args = append(args, int(r.ttl.Seconds()))
			args = append(args, currentTime)
			args = append(args, len(batch))
			script = luaBatchSetClientsOnline
		}
		for _, clientData := range batch {
			args = append(args, clientData)
		}

		if _, err := r.client.Eval(ctx, script, keys, args...).Result(); err != nil {
			return errorx.WrapError("failed to execute batch lua script", err)
		}
	}

	return nil
}

// BatchSetClientsOffline 批量设置客户端离线（使用 Lua 脚本）
func (r *RedisOnlineStatusRepository) BatchSetClientsOffline(ctx context.Context, clientIDs []string) error {
	if len(clientIDs) == 0 {
		return nil
	}

	currentTime := time.Now().Unix()

	// 先批量获取客户端信息
	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(clientIDs))
	for i, clientID := range clientIDs {
		cmds[i] = pipe.Get(ctx, r.GetClientKey(clientID))
	}

	_, err := pipe.Exec(ctx)
	if err != nil && err != redis.Nil {
		return err
	}

	// 准备批量数据：currentTime, clientCount, ...clientData
	args := []any{currentTime, 0} // 先占位，后面更新数量

	validCount := 0
	for i, cmd := range cmds {
		data, err := cmd.Result()
		if err != nil {
			continue
		}

		client, err := zipx.ZlibSmartDecompressObject[*Client]([]byte(data))
		if err != nil {
			continue
		}

		// V2 含 appID|ns（bitmap 启用时），legacy 4 段
		// 用 clientIDs[i] 作 clientID（与原逻辑一致，不信任解压出的 client.ID）
		batchData := r.buildOfflineBatchArg(clientIDs[i], client.UserID, client.NodeID, client.UserType, client.AppID, client.Namespace)
		args = append(args, batchData)
		validCount++
	}

	if validCount == 0 {
		return nil
	}

	// 更新客户端数量
	args[1] = validCount

	// 使用 Lua 脚本批量删除
	keys := []string{r.keyPrefix}
	_, err = r.client.Eval(ctx, r.offlineScript(), keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute batch lua script", err)
	}

	return nil
}

// BatchSetClientsOfflineWithInfo 批量设置客户端离线（使用已知的客户端信息）
// 当客户端信息已知时使用此方法，避免从 Redis 查询，确保即使客户端key已被删除也能清理ZSET
func (r *RedisOnlineStatusRepository) BatchSetClientsOfflineWithInfo(ctx context.Context, clients []*Client) error {
	if len(clients) == 0 {
		return nil
	}

	currentTime := time.Now().Unix()
	args := []any{currentTime, len(clients)}

	for _, client := range clients {
		// V2 含 appID|ns（bitmap 启用时），legacy 4 段
		batchData := r.buildOfflineBatchArg(client.ID, client.UserID, client.NodeID, client.UserType, client.AppID, client.Namespace)
		args = append(args, batchData)
	}

	// 使用 Lua 脚本批量删除
	keys := []string{r.keyPrefix}
	_, err := r.client.Eval(ctx, r.offlineScript(), keys, args...).Result()
	if err != nil {
		return errorx.WrapError("failed to execute batch lua script", err)
	}

	return nil
}

// ============================================================================
// 维护清理
// ============================================================================

// CleanupExpired 清理当前节点的过期客户端（使用 ZSET 自动清理过期数据）
//
// 清理策略：
// 1. 清理 node_clients 中不存在的客户端
// 2. 使用 ZREMRANGEBYSCORE 清理 all_users 和 type ZSET 中的过期数据
//
// 注意：
// - user_clients 使用 ZSET 存储，设置了 TTL 会自动过期
// - all_users 和 type 使用 ZSET，通过 score（过期时间戳）清理
func (r *RedisOnlineStatusRepository) CleanupExpired(ctx context.Context, nodeID string) (int64, error) {
	if nodeID == "" {
		return 0, fmt.Errorf("nodeID cannot be empty")
	}

	nodeClientsKey := r.GetNodeClientsKey(nodeID)
	currentTime := time.Now().Unix()

	result, err := r.client.Eval(ctx, luaCleanupExpiredClients, []string{r.keyPrefix, nodeClientsKey}, nodeID, currentTime).Result()
	if err != nil {
		return 0, fmt.Errorf("执行清理脚本失败: %w", err)
	}

	cleaned, ok := result.(int64)
	if !ok {
		return 0, fmt.Errorf("脚本返回值类型错误")
	}

	return cleaned, nil
}

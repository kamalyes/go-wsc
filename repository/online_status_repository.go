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
	"github.com/kamalyes/go-toolbox/pkg/syncx"
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

// luaCleanupExpiredClients Lua 脚本：清理当前节点的过期客户端（使用 ZSET）
//
// KEYS[1] = keyPrefix (用于构建其他 key)
// KEYS[2] = nodeClientsKey (当前节点的客户端集合)
//
// ARGV[1] = nodeID (当前节点ID)
// ARGV[2] = currentTime (当前时间戳，用于清理 ZSET 中的过期数据)
// ARGV[3] = bucketCount (热点 key 分桶数)
//
// 返回值: 清理的客户端数量
const luaCleanupExpiredClients = `
local keyPrefix = KEYS[1]
local nodeClientsKey = KEYS[2]
local nodeID = ARGV[1]
local currentTime = tonumber(ARGV[2])
local bucketCount = tonumber(ARGV[3])

-- 清理 node_clients ZSET 中的过期数据
local cleaned = redis.call('ZREMRANGEBYSCORE', nodeClientsKey, '-inf', currentTime)

-- 清理所有 type ZSET（types 集合由上线/续期脚本 SADD 登记所有出现过的 userType，
-- 替代硬编码 customer/agent，新增 userType 自动纳入清理，无泄漏）
local typesKey = keyPrefix .. "types"
local userTypes = redis.call('SMEMBERS', typesKey)

-- 分桶布局：all_users/type 按桶遍历（空桶 ZREMRANGEBYSCORE 为 O(1) 空转，开销可忽略）
for b = 0, bucketCount - 1 do
    redis.call('ZREMRANGEBYSCORE', keyPrefix .. "all_users:" .. b, '-inf', currentTime)
    for _, userType in ipairs(userTypes) do
        redis.call('ZREMRANGEBYSCORE', keyPrefix .. "type:" .. userType .. ":" .. b, '-inf', currentTime)
    end
end

return cleaned
`

// ============================================================================
// Bitmap 分层 Lua 脚本
//
// 三个脚本:
//   1. luaIsUserOnline          - 只读路径:HGET uid_map → GETBIT,供 IsUserOnline 单次往返判定
//   2. luaBatchSetClientsOnline  - 写路径:offset 分配 + ZSET 双写 + bitmap SETBIT
//   3. luaBatchSetClientsOffline - 写路径:ZSET 清理 + bitmap SETBIT 0
//
// luaIsUserOnline 返回 {ret, offset} 数组:
//   1  = bitmap 命中,用户在线
//   0  = uid_map 未分配 offset,用户确定离线(从未上线或 offset 已被清理)
//   -1 = bitmap miss(offset 超限 或 bit=0 可能过期/淘汰/已下线),调用方走 ZSET ZCount 兜底
//   offset 随结果返回供 L1 回填,-1 表示 uid_map 无记录
// ============================================================================

// luaIsUserOnline Bitmap 快速在线判定(只读,不分配 offset)
//
// 设计:offset 分配在写路径(SetClientOnline)完成,本脚本仅查询:
//   - HGET uid_map 拿 offset,nil → 用户从未上线,确定离线(返回 {0, -1})
//   - offset 超过 maxOffset → bitmap 未写入,走 ZSET 兜底(返回 {-1, offset})
//   - GETBIT 命中 → 在线(返回 {1, offset})
//   - GETBIT 未命中 → bitmap 可能过期/淘汰/用户已下线,走 ZSET 兜底(返回 {-1, offset})
//
// 返回 {ret, offset} 数组:offset 随结果返回,调用方据此回填 L1 缓存(进程内 userID→offset),
// 后续查询 L1 命中后直接 GETBIT,免 Lua/HGET
//
// KEYS[1] = uidMapKey      (wsc:online:uid_map)
// KEYS[2] = scopedBitmapKey (wsc:online:bm:<appID>:<ns>)
//
// ARGV[1] = userID
// ARGV[2] = maxOffset       (offset 上限,0 表示不限制)
//
// 返回:{1=在线 / 0=确定离线, -1=需 ZSET 兜底} + offset(-1 表示 uid_map 无记录,不回填)
const luaIsUserOnline = `
local offset = redis.call('HGET', KEYS[1], ARGV[1])
if not offset then
    return {0, -1}
end
offset = tonumber(offset)
if offset == nil then
    return {0, -1}
end
local maxOffset = tonumber(ARGV[2])
if maxOffset and maxOffset > 0 and offset >= maxOffset then
    return {-1, offset}
end
local bit = redis.call('GETBIT', KEYS[2], offset)
if bit == 1 then
    return {1, offset}
end
return {-1, offset}
`

// luaBatchSetClientsOnline 批量设置客户端在线(offset 分配 + bitmap + scoped 双写)
//
// 要点:
//  1. 在 Lua 内原子分配 offset(HGET → INCR → HSETNX),避免多节点首次分配竞态
//  2. 按 appID/ns 分桶写 scoped ZSET,同时双写 unscoped ZSET
//  3. SETBIT scoped/global bitmap 并 EXPIRE 续期（全量路径无条件写，保证缺失位补齐）
//  4. offset 超限时跳过 SETBIT(只写 ZSET,查询走 ZSET 兜底)
//  5. uid_map/all_users/type 按 bucket 段分桶（Go 侧 keyBucket 预计算传入，防亿级单 key 热点）
//
// KEYS[1] = keyPrefix (用于构建所有 key)
//
// ARGV[1] = ttl           (client:<id> 的 SETEX TTL,秒)
// ARGV[2] = currentTime    (当前时间戳,清理 ZSET 过期项)
// ARGV[3] = clientCount    (客户端数量)
// ARGV[4] = bitmapTTL      (bitmap EXPIRE,秒)
// ARGV[5] = maxOffset      (offset 上限,0=不限制)
// ARGV[6..] = 每个客户端的数据,格式:clientID|userID|nodeID|userType|expireTime|appID|ns|bucket|clientData（9 段）
//
// 返回值:成功处理的客户端数量
const luaBatchSetClientsOnline = `
local keyPrefix = KEYS[1]
local uidCounterKey = keyPrefix .. "uid_counter"

local ttl = tonumber(ARGV[1])
local currentTime = tonumber(ARGV[2])
local clientCount = tonumber(ARGV[3])
local bitmapTTL = tonumber(ARGV[4])
local maxOffset = tonumber(ARGV[5])
local successCount = 0

for i = 1, clientCount do
    local idx = 5 + i
    local data = ARGV[idx]

    -- 解析 9 段: clientID|userID|nodeID|userType|expireTime|appID|ns|bucket|clientData
    local parts = {}
    local lastPos = 1
    for j = 1, 8 do
        local pos = string.find(data, "|", lastPos, true)
        if pos then
            table.insert(parts, string.sub(data, lastPos, pos - 1))
            lastPos = pos + 1
        end
    end
    table.insert(parts, string.sub(data, lastPos))

    if #parts == 9 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local expireTime = tonumber(parts[5])
        local appID = parts[6]
        local ns = parts[7]
        local bucket = parts[8]
        local clientData = parts[9]

        local clientKey = keyPrefix .. "client:" .. clientID
        local scopedUserClientsKey = keyPrefix .. "user_clients:" .. appID .. ":" .. ns .. ":" .. userID
        local unscopedUserClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local uidMapKey = keyPrefix .. "uid_map:" .. bucket
        local allUsersKey = keyPrefix .. "all_users:" .. bucket
        local typeKey = keyPrefix .. "type:" .. userType .. ":" .. bucket
        local scopedBitmapKey = keyPrefix .. "bm:" .. appID .. ":" .. ns
        local globalBitmapKey = keyPrefix .. "bm:" .. appID .. ":__global__" -- __global__ 与 constants.GlobalBitmapNS 保持一致

        -- 1. Offset 分配(原子,HSETNX 保证多节点首次分配竞态下 offset 唯一;uid_counter 全局不分桶)
        local offset = redis.call('HGET', uidMapKey, userID)
        if not offset then
            offset = redis.call('INCR', uidCounterKey) - 1
            local ok = redis.call('HSETNX', uidMapKey, userID, offset)
            if ok == 0 then
                -- 已被其他节点设置,重读拿到正确值(本次 INCR 的 offset 形成空洞,bitmap 位永远 0,无影响)
                offset = redis.call('HGET', uidMapKey, userID)
            end
        end
        offset = tonumber(offset)

        -- 2. 清理 scoped + unscoped ZSET 的过期项
        redis.call('ZREMRANGEBYSCORE', scopedUserClientsKey, '-inf', currentTime)
        redis.call('ZREMRANGEBYSCORE', unscopedUserClientsKey, '-inf', currentTime)

        -- 3. 写 client 详情 + 节点归属标记（离线清理时校验，防止旧节点误删已迁移条目）
        redis.call('SETEX', clientKey, ttl, clientData)
        redis.call('SETEX', keyPrefix .. "owner:" .. clientID, ttl, nodeID)

        -- 4. ZADD scoped + unscoped + node + all_users + type
        redis.call('ZADD', scopedUserClientsKey, expireTime, clientID)
        redis.call('EXPIRE', scopedUserClientsKey, ttl)
        redis.call('ZADD', unscopedUserClientsKey, expireTime, clientID)
        redis.call('EXPIRE', unscopedUserClientsKey, ttl)
        redis.call('ZADD', nodeClientsKey, expireTime, clientID)
        redis.call('ZADD', allUsersKey, expireTime, userID)
        redis.call('ZADD', typeKey, expireTime, userID)
        -- types 集合登记（CleanupExpired 据此遍历所有 type ZSET，幂等）
        redis.call('SADD', keyPrefix .. "types", userType)

        -- 5. Bitmap SETBIT + EXPIRE(offset 在上限内才写)
        if offset and (not maxOffset or maxOffset == 0 or offset < maxOffset) then
            redis.call('SETBIT', scopedBitmapKey, offset, 1)
            redis.call('SETBIT', globalBitmapKey, offset, 1)
            redis.call('EXPIRE', scopedBitmapKey, bitmapTTL)
            redis.call('EXPIRE', globalBitmapKey, bitmapTTL)
        end

        successCount = successCount + 1
    end
end

return successCount
`

// luaRenewClientsOnline 心跳续期专用脚本（轻量路径）
//
// 与 luaBatchSetClientsOnline 的分工：
//   - 上线/重建（BatchSetClientsOnline）：JSON 序列化 + 压缩 + SETEX 全量写 client 详情
//   - 心跳续期（本脚本）：client 数据未变，跳过序列化/压缩/SETEX，仅做
//     EXPIRE client/owner + ZADD 刷新 score + bitmap 续期
//
// Bitmap TTL 摊薄刷新：同一 bitmap key 在 TTL 前半窗口内至多 SETBIT+EXPIRE 一次
// （批内以 bmRefreshed 表去重，跨批靠 TTL 判断），将 bitmap 写频从"每客户端每心跳"
// 降为"每 key 每 TTL/2 一次"，与在线用户数解耦。bitmap=1 位由全量上线路径保证写入，
// 此处仅续期，跳过不影响正确性（bitmap miss 时查询走 ZSET 兜底）
//
// 自愈保留：EXISTS 检测 client:<id> 键是否存在（可能过期/被 maxmemory 淘汰），
// 缺失的客户端索引收集在返回数组中，由 Go 侧走全量 BatchSetClientsOnline 重建
//
// KEYS[1] = keyPrefix
//
// ARGV[1] = ttl           (client/owner/ZSET 的续期秒数)
// ARGV[2] = currentTime    (当前时间戳)
// ARGV[3] = clientCount
// ARGV[4] = bitmapTTL      (bitmap EXPIRE,秒)
// ARGV[5] = maxOffset      (offset 上限,0=不限制)
// ARGV[6..] = 每个客户端数据,格式:clientID|userID|nodeID|userType|appID|ns|bucket（7 段）
//
// 返回值:client:<id> 键缺失的客户端在输入中的序号数组（1-based），空数组表示全部续期成功
const luaRenewClientsOnline = `
local keyPrefix = KEYS[1]

local ttl = tonumber(ARGV[1])
local currentTime = tonumber(ARGV[2])
local clientCount = tonumber(ARGV[3])
local bitmapTTL = tonumber(ARGV[4])
local maxOffset = tonumber(ARGV[5])
local expireTime = currentTime + ttl
local missing = {}
local bmRefreshed = {}

for i = 1, clientCount do
    local data = ARGV[5 + i]

    -- 解析 7 段: clientID|userID|nodeID|userType|appID|ns|bucket
    -- 手动分割（find 前移）而非 gmatch：gmatch 的 ([^|]+) 会丢弃空段，ns 为空串时段数不足
    local parts = {}
    local lastPos = 1
    for j = 1, 6 do
        local pos = string.find(data, "|", lastPos, true)
        if pos then
            table.insert(parts, string.sub(data, lastPos, pos - 1))
            lastPos = pos + 1
        end
    end
    table.insert(parts, string.sub(data, lastPos)) -- ns（可能为空串）

    if #parts == 7 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local appID = parts[5]
        local ns = parts[6]
        local bucket = parts[7]

        local clientKey = keyPrefix .. "client:" .. clientID

        if redis.call('EXISTS', clientKey) == 1 then
            local scopedUserClientsKey = keyPrefix .. "user_clients:" .. appID .. ":" .. ns .. ":" .. userID
            local unscopedUserClientsKey = keyPrefix .. "user_clients:" .. userID
            local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
            local uidMapKey = keyPrefix .. "uid_map:" .. bucket
            local allUsersKey = keyPrefix .. "all_users:" .. bucket
            local typeKey = keyPrefix .. "type:" .. userType .. ":" .. bucket
            local scopedBitmapKey = keyPrefix .. "bm:" .. appID .. ":" .. ns
            local globalBitmapKey = keyPrefix .. "bm:" .. appID .. ":__global__" -- __global__ 与 constants.GlobalBitmapNS 保持一致

            -- 1. 续期 client 详情 + 节点归属标记
            redis.call('EXPIRE', clientKey, ttl)
            redis.call('EXPIRE', keyPrefix .. "owner:" .. clientID, ttl)

            -- 2. ZADD 刷新 score（过期时间戳随本次续期前移）
            redis.call('ZADD', scopedUserClientsKey, expireTime, clientID)
            redis.call('EXPIRE', scopedUserClientsKey, ttl)
            redis.call('ZADD', unscopedUserClientsKey, expireTime, clientID)
            redis.call('EXPIRE', unscopedUserClientsKey, ttl)
            redis.call('ZADD', nodeClientsKey, expireTime, clientID)
            redis.call('ZADD', allUsersKey, expireTime, userID)
            redis.call('ZADD', typeKey, expireTime, userID)

            -- 3. types 集合登记（CleanupExpired 据此遍历所有 type ZSET，幂等）
            redis.call('SADD', keyPrefix .. "types", userType)

            -- 4. bitmap 续期（摊薄刷新：TTL 剩余超过半程且本批未刷新过则跳过，
            --    bit 已为 1（上线路径写入），跳过仅推迟 EXPIRE，不影响判否正确性）
            local offset = redis.call('HGET', uidMapKey, userID)
            if offset and (not maxOffset or maxOffset == 0 or tonumber(offset) < maxOffset) then
                local off = tonumber(offset)
                if not bmRefreshed[scopedBitmapKey] and redis.call('TTL', scopedBitmapKey) < bitmapTTL / 2 then
                    redis.call('SETBIT', scopedBitmapKey, off, 1)
                    redis.call('EXPIRE', scopedBitmapKey, bitmapTTL)
                    bmRefreshed[scopedBitmapKey] = true
                end
                if not bmRefreshed[globalBitmapKey] and redis.call('TTL', globalBitmapKey) < bitmapTTL / 2 then
                    redis.call('SETBIT', globalBitmapKey, off, 1)
                    redis.call('EXPIRE', globalBitmapKey, bitmapTTL)
                    bmRefreshed[globalBitmapKey] = true
                end
            end
        else
            -- client 详情缺失（过期/被淘汰），交由 Go 侧全量重建
            table.insert(missing, i)
        end
    end
end

return missing
`

// luaBatchSetClientsOffline 批量设置客户端离线(bitmap SETBIT 0 + scoped/unscoped 双清理)
//
// 要点:
//  1. 同时 ZREM scoped + unscoped
//  2. scoped 下线后:若 unscoped 仍有其他信封连接,仅清 scoped bitmap(global 保留);
//     若 unscoped 也空,清 global bitmap + all_users/type
//  3. 不 HDEL uid_map(offset 永久保留,用户再上线时复用,避免 INCR 空洞累积)
//  4. uid_map/all_users/type 按 bucket 段分桶（Go 侧 keyBucket 预计算传入）
//
// KEYS[1] = keyPrefix
//
// ARGV[1] = currentTime    (清理 ZSET 过期项)
// ARGV[2] = clientCount
// ARGV[3..] = 每个客户端数据,格式:clientID|userID|nodeID|userType|appID|ns|bucket（7 段）
//
// 返回值:成功处理的客户端数量
const luaBatchSetClientsOffline = `
local keyPrefix = KEYS[1]

local currentTime = tonumber(ARGV[1])
local clientCount = tonumber(ARGV[2])
local successCount = 0

for i = 1, clientCount do
    local idx = 2 + i
    local data = ARGV[idx]

    -- 解析 7 段: clientID|userID|nodeID|userType|appID|ns|bucket
    -- 手动分割（find 前移）而非 gmatch：gmatch 的 ([^|]+) 会丢弃空段，
    -- 字段为空串时会导致段数不足整条记录被跳过（离线清理失效）
    local parts = {}
    local lastPos = 1
    for j = 1, 6 do
        local pos = string.find(data, "|", lastPos, true)
        if pos then
            table.insert(parts, string.sub(data, lastPos, pos - 1))
            lastPos = pos + 1
        end
    end
    table.insert(parts, string.sub(data, lastPos)) -- ns（可能为空串）

    if #parts == 7 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local appID = parts[5]
        local ns = parts[6]
        local bucket = parts[7]

        local clientKey = keyPrefix .. "client:" .. clientID
        local ownerKey = keyPrefix .. "owner:" .. clientID
        local scopedUserClientsKey = keyPrefix .. "user_clients:" .. appID .. ":" .. ns .. ":" .. userID
        local unscopedUserClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local uidMapKey = keyPrefix .. "uid_map:" .. bucket
        local allUsersKey = keyPrefix .. "all_users:" .. bucket
        local typeKey = keyPrefix .. "type:" .. userType .. ":" .. bucket
        local scopedBitmapKey = keyPrefix .. "bm:" .. appID .. ":" .. ns
        local globalBitmapKey = keyPrefix .. "bm:" .. appID .. ":__global__" -- __global__ 与 constants.GlobalBitmapNS 保持一致

        -- 🔒 节点归属校验：clientID 已被其他节点接管（断线重连迁移）时，
        -- 仅清理本节点集合，不删共享索引与 bitmap（用户仍在线于新节点），避免误删新节点的条目
        -- owner 不存在（旧版本数据，未写 owner key）视为本节点归属，保持向后兼容
        local owner = redis.call('GET', ownerKey)
        local migrated = (owner ~= false) and (owner ~= nodeID)

        -- 本节点集合总是清理（无论归属如何，node_clients:<nodeID> 只含本节点自己的条目）
        redis.call('ZREM', nodeClientsKey, clientID)

        if not migrated then
            -- 1. 删 client 详情与归属标记
            redis.call('DEL', clientKey)
            redis.call('DEL', ownerKey)

            -- 2. ZREM scoped + unscoped
            redis.call('ZREM', scopedUserClientsKey, clientID)
            redis.call('ZREM', unscopedUserClientsKey, clientID)

            -- 3. 清理 scoped + unscoped 过期项
            redis.call('ZREMRANGEBYSCORE', scopedUserClientsKey, '-inf', currentTime)
            redis.call('ZREMRANGEBYSCORE', unscopedUserClientsKey, '-inf', currentTime)

            -- 4. 检查 scoped 是否空,空则清理 scoped bitmap
            local scopedRemaining = redis.call('ZCOUNT', scopedUserClientsKey, currentTime, '+inf')
            if scopedRemaining == 0 then
                redis.call('DEL', scopedUserClientsKey)
                -- scoped 下线,清 scoped bitmap 该位
                local offset = redis.call('HGET', uidMapKey, userID)
                if offset then
                    redis.call('SETBIT', scopedBitmapKey, tonumber(offset), 0)
                end

                -- 检查 unscoped(跨所有信封)是否也空
                local unscopedRemaining = redis.call('ZCOUNT', unscopedUserClientsKey, currentTime, '+inf')
                if unscopedRemaining == 0 then
                    -- 用户在所有信封下都离线,清 global bitmap + all_users/type
                    redis.call('DEL', unscopedUserClientsKey)
                    redis.call('ZREM', allUsersKey, userID)
                    redis.call('ZREM', typeKey, userID)
                    if offset then
                        redis.call('SETBIT', globalBitmapKey, tonumber(offset), 0)
                    end
                end
                -- 否则:scoped 离线但其他信封仍在线,global bitmap 保留
            end
        end

        successCount = successCount + 1
    end
end

return successCount
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
	// 路径自适应：有路由信封时走 L1 offset 缓存 → GETBIT（命中零网络）→ ZSET ZCount 兜底，
	// 无路由信封走 unscoped ZCount，永远最终一致
	IsUserOnline(ctx context.Context, userID string) (bool, error)

	// BatchIsUserOnline 批量在线判定，返回 map[userID]bool（含全部查询的 userID）
	// Pipeline 实现：offset 解析（L1+HGET）→ GETBIT → ZCount 兜底，3 次往返覆盖 N 个用户
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

	// BatchSetClientsOnline 批量设置客户端在线（全量：JSON 序列化 + SETEX client 详情）
	BatchSetClientsOnline(ctx context.Context, clients []*Client) error

	// RenewClientsOnline 心跳批量续期（轻量路径：跳过序列化/压缩/SETEX，仅续期索引与 bitmap）
	// client:<id> 键缺失（过期/淘汰）的客户端内部自动走全量重建，自愈语义与 BatchSetClientsOnline 一致
	// 供心跳高频刷新使用，千万级连接下避免每周期全量重写 client 详情
	RenewClientsOnline(ctx context.Context, clients []*Client) error

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

	// Bitmap 分层（单一路径，无开关）
	bitmapTTL       time.Duration   // bitmap EXPIRE（心跳间续期，过期则回退 ZSET 兜底）
	maxBitmapOffset int64           // offset 上限（防恶意膨胀，0=不限制）
	maxCachedUIDs   int             // L1 缓存容量上限
	uidCache        *uidOffsetCache // userID→offset 进程内 L1 缓存（命中零网络）
}

// NewRedisOnlineStatusRepository 创建 Redis 在线状态仓库
//
// bitmapTTL / maxBitmapOffset / maxCachedUIDs 通过环境变量覆盖默认值（见 constants.go 的 env* 常量）
func NewRedisOnlineStatusRepository(client redis.UniversalClient, config *wscconfig.OnlineStatus) OnlineStatusRepository {
	maxCachedUIDs := loadEnvInt(envMaxCachedUIDs, constants.DefaultMaxCachedUIDs)
	repo := &RedisOnlineStatusRepository{
		client:             client,
		keyPrefix:          mathx.IfNotEmpty(config.KeyPrefix, constants.DefaultOnlineKeyPrefix),
		ttl:                config.TTL,
		enableCompression:  config.EnableCompression,
		compressionMinSize: mathx.IfNotZero(config.CompressionMinSize, 512),
		bitmapTTL:          loadBitmapTTL(config),
		maxBitmapOffset:    int64(loadEnvInt(envMaxBitmapOffset, constants.DefaultMaxBitmapOffset)),
		maxCachedUIDs:      maxCachedUIDs,
		uidCache:           newUIDOffsetCache(maxCachedUIDs),
	}
	return repo
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

// loadBitmapTTL 读取 bitmap TTL：优先环境变量，未设置时对齐 client TTL
//
// 对齐理由：bitmap 是判否加速层（miss 走 ZSET 兜底），其 EXPIRE 由心跳续期摊薄刷新
// （TTL 前半窗口内至多刷一次，见 luaRenewClientsOnline），刷新节奏跟随心跳（间隔通常为
// TTL 量级）。若 bitmapTTL 远小于 client TTL，两次心跳之间 bitmap 必然过期，L0 层形同虚设；
// 对齐后 bitmap 的陈旧窗口与 ZSET 一致，语义自洽
func loadBitmapTTL(config *wscconfig.OnlineStatus) time.Duration {
	if v := os.Getenv(envBitmapTTL); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	if config != nil && config.TTL > 0 {
		return config.TTL
	}
	return time.Duration(constants.DefaultBitmapTTLSeconds) * time.Second
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

// keyBucket 计算 userID 的热点 key 分桶号（0 ~ DefaultKeyBucketCount-1）
// FNV-1a 均匀性足以支撑 256 桶；同一 userID 的 uid_map/all_users/type 三类 key 落同一桶
// 注意：本函数为 uid_map/all_users/type 的唯一桶计算来源，Go 侧（查询/清理）与 Lua 侧（读写，由
// Go 构造数据段传入）必须使用同一结果，桶不一致会导致索引永久不一致
func (r *RedisOnlineStatusRepository) keyBucket(userID string) int {
	return int(syncx.FNVHashString32(userID)) & keyBucketMask
}

// GetUserTypeSetKey 获取用户类型集合的 key（分桶）
// 完整 key: <keyPrefix>type:<userType>:<bucket>
func (r *RedisOnlineStatusRepository) GetUserTypeSetKey(userType models.UserType, userID string) string {
	return r.keyPrefix + "type:" + userType.String() + ":" + strconv.Itoa(r.keyBucket(userID))
}

// GetAllUsersSetKey 获取所有在线用户集合的 key（分桶）
// 完整 key: <keyPrefix>all_users:<bucket>
func (r *RedisOnlineStatusRepository) GetAllUsersSetKey(userID string) string {
	return r.keyPrefix + allUsersKeySuffix + ":" + strconv.Itoa(r.keyBucket(userID))
}

// GetAllUsersBucketKey 按桶号构造全体用户 ZSET key（跨桶遍历/清理用）
func (r *RedisOnlineStatusRepository) GetAllUsersBucketKey(bucket int) string {
	return r.keyPrefix + allUsersKeySuffix + ":" + strconv.Itoa(bucket)
}

// GetUserTypeBucketKey 按桶号构造类型 ZSET key（跨桶遍历/清理用）
func (r *RedisOnlineStatusRepository) GetUserTypeBucketKey(userType models.UserType, bucket int) string {
	return r.keyPrefix + "type:" + userType.String() + ":" + strconv.Itoa(bucket)
}

// GetTypesSetKey 获取 userType 登记集合的 key
func (r *RedisOnlineStatusRepository) GetTypesSetKey() string {
	return r.keyPrefix + typesKeySuffix
}

// ============================================================================
// Bitmap 分层 Key 生成方法
//
// Key 命名规范（前缀 wsc:online:）：
//   - user_clients:<appID>:<ns>:<userID>   scoped ZSET（按信封分桶，让 IsUserOnline 永远走 ZCount 快路径）
//   - user_clients:<userID>                  unscoped ZSET（跨信封全量视图：SetOffline 全端下线判定、
//                                           广播信封 ns="" 查询、离线脚本双清依赖）
//   - bm:<appID>:<ns>                        scoped bitmap（GETBIT 判否）
//   - bm:<appID>:__global__                  global bitmap（全局广播查询 ns="" 时命中）
//   - uid_map:<bucket>                        Hash：field=userID, value=数字 offset（分桶防热点）
//   - uid_counter                            String：INCR 自增计数器（首次分配 offset，全局唯一故不分桶）
//   - all_users:<bucket>                     ZSET：全体在线用户（分桶防热点）
//   - type:<userType>:<bucket>               ZSET：按类型的在线用户（分桶防热点）
//   - types                                  Set：所有出现过的 userType（CleanupExpired 据此遍历 type ZSET）
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

// GetUIDMapKey 获取 userID→offset 的 Hash key（按 userID 分桶）
func (r *RedisOnlineStatusRepository) GetUIDMapKey(userID string) string {
	return r.keyPrefix + uidMapKeySuffix + ":" + strconv.Itoa(r.keyBucket(userID))
}

// GetUIDMapBucketKey 按桶号构造 uid_map key（跨桶遍历用）
func (r *RedisOnlineStatusRepository) GetUIDMapBucketKey(bucket int) string {
	return r.keyPrefix + uidMapKeySuffix + ":" + strconv.Itoa(bucket)
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
// 格式：clientID|userID|nodeID|userType|appID|ns|bucket（7 段，供 luaBatchSetClientsOffline 清 scoped/global bitmap）
// appID/ns 兜底归一化（入口层 handleRegister 已归一化，此处兜底测试/历史数据场景），与在线写入的 key 构造一致
func (r *RedisOnlineStatusRepository) buildOfflineBatchArg(clientID, userID, nodeID string, userType models.UserType, appID, ns string) string {
	base := clientID + "|" + userID + "|" + nodeID + "|" + string(userType)
	return base + "|" + r.normalizeAppID(appID) + "|" + constants.NormalizeNamespace(ns) +
		"|" + strconv.Itoa(r.keyBucket(userID))
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
	_, err := r.client.Eval(ctx, luaBatchSetClientsOffline, keys, args...).Result()
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
// 信封 ns=""（全局广播）匹配该 appID 下所有 namespace；无路由信封退化为不过滤
func (r *RedisOnlineStatusRepository) GetUserClients(ctx context.Context, userID string) ([]*Client, error) {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	hasRoute := routing.RoutingFromContext(ctx) != nil

	// 有路由信封且 ns 非空：scoped key（ZSET 已按 appID/ns 分桶，无需逐客户端过滤）
	// 信封 ns=""（全局广播）或无路由信封：unscoped key + 信封过滤（广播按 appID 匹配所有 ns）
	scoped := hasRoute && ns != ""
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

	// scoped 分桶未命中：该信封下无客户端，返回空列表（非错误，与 unscoped+过滤路径行为一致）
	if len(clientIDs) == 0 {
		if scoped {
			return []*Client{}, nil
		}
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
// 有路由信封且 ns 非空走 scoped key（已分桶）；
// 广播信封（ns=""）scoped 无该分桶，退化为 unscoped 全量 + 信封过滤（与 GetUserClients 一致）；
// 无路由信封退化 unscoped key（兼容）
func (r *RedisOnlineStatusRepository) zcountScoped(ctx context.Context, userID, appID, ns string) (bool, error) {
	currentTime := time.Now().Unix()
	var key string
	if appID == "" {
		key = r.GetUserClientsKey(userID)
	} else if ns == "" {
		// 广播信封：加载 unscoped 客户端按 appID 过滤（clientMatchesRouteEnvelope 的 ns="" 为广播语义）
		clients, err := r.GetUserClients(ctx, userID)
		if err != nil {
			if errorx.ClassifyError(err) == models.ErrTypeUserNotFound {
				return false, nil
			}
			return false, err
		}
		return len(clients) > 0, nil
	} else {
		key = r.GetScopedUserClientsKey(userID, r.normalizeAppID(appID), ns)
	}
	count, err := r.client.ZCount(ctx, key, strconv.FormatInt(currentTime, 10), "+inf").Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// BatchIsUserOnline 批量在线判定（Pipeline 实现）
//
// 三阶段 Pipeline，3 次网络往返覆盖 N 个用户（替代旧的逐个查询 N 次往返）：
//  1. offset 解析：L1 缓存命中零网络，未命中的批量 pipeline HGET uid_map
//  2. GETBIT：有效 offset 的批量 pipeline GETBIT，bit=1 → 在线
//  3. ZCount 兜底：bit=0（bitmap 过期/已下线）或 offset 超限的批量 pipeline ZCount
//
// 返回 map[userID]bool，所有查询的 userID 都在 map 中（离线为 false）
// 单个查询失败不中断批量，记为离线（保守，避免误判在线触发跨节点路由）
func (r *RedisOnlineStatusRepository) BatchIsUserOnline(ctx context.Context, userIDs []string) (map[string]bool, error) {
	result := make(map[string]bool, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}

	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	hasRoute := routing.RoutingFromContext(ctx) != nil

	// 无路由信封：无 scoped key 可用，退化为逐个 unscoped ZCount
	if !hasRoute {
		for _, uid := range userIDs {
			online, err := r.IsUserOnline(ctx, uid)
			if err != nil {
				result[uid] = false
				continue
			}
			result[uid] = online
		}
		return result, nil
	}

	normalizedAppID := r.normalizeAppID(appID)
	// 全局广播（ns=""）用 global bitmap，否则 scoped bitmap
	bitmapKey := r.GetScopedBitmapKey(normalizedAppID, ns)
	if ns == "" {
		bitmapKey = r.GetGlobalBitmapKey(normalizedAppID)
	}

	// userState 记录每个用户的 offset 解析结果
	// offset=-1 表示 uid_map 无记录（从未上线，确定离线）；overLimit 表示 offset 超限（bitmap 未写入，需 ZSET 兜底）
	type userState struct {
		offset    int64
		overLimit bool
	}
	states := make([]userState, len(userIDs))
	var hgetIdx []int
	for i, uid := range userIDs {
		if off, ok := r.uidCache.Load(uid); ok {
			over := r.maxBitmapOffset > 0 && off >= r.maxBitmapOffset
			if over {
				r.uidCache.IncOverflow()
			}
			states[i] = userState{offset: off, overLimit: over}
		} else {
			states[i] = userState{offset: -1}
			hgetIdx = append(hgetIdx, i)
		}
	}

	// 阶段 1：L1 未命中的批量 HGET uid_map（1 次往返），命中后回填 L1
	if len(hgetIdx) > 0 {
		pipe := r.client.Pipeline()
		cmds := make([]*redis.StringCmd, len(hgetIdx))
		for j, i := range hgetIdx {
			cmds[j] = pipe.HGet(ctx, r.GetUIDMapKey(userIDs[i]), userIDs[i])
		}
		if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
			return nil, err
		}
		for j, cmd := range cmds {
			off, err := cmd.Int64()
			if err != nil {
				continue // uid_map 无记录：保持 offset=-1，确定离线
			}
			i := hgetIdx[j]
			over := r.maxBitmapOffset > 0 && off >= r.maxBitmapOffset
			if over {
				r.uidCache.IncOverflow()
			}
			states[i] = userState{offset: off, overLimit: over}
			r.uidCache.Store(userIDs[i], off)
		}
	}

	// needFallback[i]=true 表示该用户需走 ZCount 兜底（bit=0 / GETBIT 失败）
	needFallback := make([]bool, len(userIDs))

	// 阶段 2：有效 offset 的批量 GETBIT（1 次往返）
	var getbitIdx []int
	for i, st := range states {
		if st.offset >= 0 && !st.overLimit {
			getbitIdx = append(getbitIdx, i)
		}
	}
	if len(getbitIdx) > 0 {
		pipe := r.client.Pipeline()
		cmds := make([]*redis.IntCmd, len(getbitIdx))
		for j, i := range getbitIdx {
			cmds[j] = pipe.GetBit(ctx, bitmapKey, states[i].offset)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
		for j, cmd := range cmds {
			i := getbitIdx[j]
			bit, err := cmd.Result()
			if err != nil || bit != 1 {
				// bit=0：bitmap 可能过期/已下线，不能直接判离线，走 ZCount 兜底
				needFallback[i] = true
				continue
			}
			result[userIDs[i]] = true
		}
	}

	// 阶段 3：兜底用户批量 ZCount（1 次往返），单个失败记为离线（保守）
	// 广播信封（ns=""）无法用 scoped ZCount 表达，退化为逐个 IsUserOnline（内部走信封过滤兜底）
	var zcountIdx []int
	for i, st := range states {
		if needFallback[i] || st.overLimit {
			zcountIdx = append(zcountIdx, i)
		}
	}
	if len(zcountIdx) > 0 {
		if ns == "" {
			for _, i := range zcountIdx {
				if online, err := r.IsUserOnline(ctx, userIDs[i]); err == nil && online {
					result[userIDs[i]] = true
				}
			}
		} else {
			currentTime := strconv.FormatInt(time.Now().Unix(), 10)
			pipe := r.client.Pipeline()
			cmds := make([]*redis.IntCmd, len(zcountIdx))
			for j, i := range zcountIdx {
				cmds[j] = pipe.ZCount(ctx, r.GetScopedUserClientsKey(userIDs[i], normalizedAppID, ns), currentTime, "+inf")
			}
			if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
				return nil, err
			}
			for j, cmd := range cmds {
				count, err := cmd.Result()
				if err != nil {
					continue // 失败保持 false（默认值）
				}
				if count > 0 {
					result[userIDs[zcountIdx[j]]] = true
				}
			}
		}
	}

	// 未在 result 中的（uid_map 无记录 / 兜底 count=0 / 失败）默认离线
	for _, uid := range userIDs {
		if _, ok := result[uid]; !ok {
			result[uid] = false
		}
	}

	return result, nil
}

// IsUserOnline 检查用户是否在线（任意设备，按 ctx 路由信封 appID+namespace 隔离）
//
// 路径自适应（单一入口）：
//   - 有路由信封：L1 offset 缓存命中 → 直接 GETBIT（1 次往返，无 Lua 无 HGET）
//     L1 未命中 → Lua 原子查询 HGET uid_map → GETBIT（1 次往返）并回填 L1
//     （1=在线 / 0=确定离线 uid_map 未分配 offset / -1=miss 兜底）
//   - 无路由信封：unscoped ZCount
//
// 性能：L1 命中时仅 1 次 GETBIT（Redis O(1)），远优于 GetUserClients 的 ZRANGE + GET ×N + N 次解压
// 兜底保证：bitmap 是判否加速层非真相源，任何 miss 都回退 ZSET ZCount，最终一致
func (r *RedisOnlineStatusRepository) IsUserOnline(ctx context.Context, userID string) (bool, error) {
	appID := routing.AppIDFromContext(ctx)
	ns := routing.NamespaceFromContext(ctx)
	hasRoute := routing.RoutingFromContext(ctx) != nil

	// bitmap 快速路径：有路由信封时走 GETBIT
	if hasRoute {
		normalizedAppID := r.normalizeAppID(appID)
		// 全局广播（ns=""）用 global bitmap，否则 scoped bitmap
		bitmapKey := r.GetScopedBitmapKey(normalizedAppID, ns)
		if ns == "" {
			bitmapKey = r.GetGlobalBitmapKey(normalizedAppID)
		}

		// L1 命中：offset 已知，直接 GETBIT（免 Lua/HGET）
		if offset, ok := r.uidCache.Load(userID); ok {
			if r.maxBitmapOffset > 0 && offset >= r.maxBitmapOffset {
				// offset 超限：bitmap 从未写入该位，直接走 ZSET 兜底
				r.uidCache.IncOverflow()
				return r.zcountScoped(ctx, userID, appID, ns)
			}
			bit, err := r.client.GetBit(ctx, bitmapKey, offset).Result()
			if err == nil {
				if bit == 1 {
					return true, nil
				}
				// bit=0：bitmap 可能过期/淘汰/已下线，回退 ZCount 兜底（保守，宁可多查不误判）
				return r.zcountScoped(ctx, userID, appID, ns)
			}
			return r.zcountScoped(ctx, userID, appID, ns)
		}

		// L1 未命中：Lua 原子查询 HGET uid_map → GETBIT，成功时回填 offset
		keys := []string{r.GetUIDMapKey(userID), bitmapKey}
		args := []any{userID, r.maxBitmapOffset}
		luaResult, err := r.client.Eval(ctx, luaIsUserOnline, keys, args...).Result()
		if err == nil {
			if arr, ok := luaResult.([]interface{}); ok && len(arr) == 2 {
				ret, _ := arr[0].(int64)
				offset, offOK := arr[1].(int64)
				if offOK && offset >= 0 {
					r.uidCache.Store(userID, offset)
				}
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

	// 兜底路径：无路由信封时走 unscoped ZCount（兼容无路由 ctx 边界场景）
	currentTime := time.Now().Unix()
	count, err := r.client.ZCount(ctx, r.GetUserClientsKey(userID), strconv.FormatInt(currentTime, 10), "+inf").Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// GetAllOnlineUsers 获取所有在线用户ID列表
// ZScan 游标分页遍历（千万级 member 下 ZRANGE 全量会阻塞 Redis 单线程并造成 Go 侧内存尖峰）
// 分桶布局下逐桶遍历后合并，桶间天然无重复（同一 userID 恒定落同一桶）
func (r *RedisOnlineStatusRepository) GetAllOnlineUsers(ctx context.Context) ([]string, error) {
	users := make([]string, 0, 1024)
	for bucket := 0; bucket < constants.DefaultKeyBucketCount; bucket++ {
		page, err := r.zscanValidUsers(ctx, r.GetAllUsersBucketKey(bucket))
		if err != nil {
			return nil, err
		}
		users = append(users, page...)
	}
	return users, nil
}

// zscanValidUsers ZScan 游标分页遍历 ZSET，仅返回 score > 当前时间的未过期 member
//
// SCAN 语义只保证遍历期间元素至少被返回一次（可能重复），用 seen 去重；
// 每次 count 条（服务端建议值，实际可能多返回），游标为 0 时遍历结束。
// ZScan 结果为扁平数组 [member, score, member, score, ...]，score 过滤在 Go 侧完成
func (r *RedisOnlineStatusRepository) zscanValidUsers(ctx context.Context, key string) ([]string, error) {
	currentTime := time.Now().Unix()
	users := make([]string, 0, 1024)
	seen := make(map[string]struct{}, 1024)

	var cursor uint64
	for {
		page, next, err := r.client.ZScan(ctx, key, cursor, "", 1000).Result()
		if err != nil {
			return nil, err
		}
		for i := 0; i+1 < len(page); i += 2 {
			score, err := strconv.ParseFloat(page[i+1], 64)
			if err != nil || int64(score) <= currentTime {
				continue // 已过期：ZREMRANGEBYSCORE 惰性清理前的残留
			}
			member := page[i]
			if _, dup := seen[member]; dup {
				continue // SCAN 语义可能重复返回
			}
			seen[member] = struct{}{}
			users = append(users, member)
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return users, nil
}

// GetOnlineCount 获取在线用户总数（使用 ZCOUNT 统计未过期的用户）
// 分桶布局下 Pipeline 并行统计 256 桶求和（1 次网络往返）
func (r *RedisOnlineStatusRepository) GetOnlineCount(ctx context.Context) (int64, error) {
	currentTime := strconv.FormatInt(time.Now().Unix(), 10)

	pipe := r.client.Pipeline()
	cmds := make([]*redis.IntCmd, constants.DefaultKeyBucketCount)
	for bucket := range cmds {
		cmds[bucket] = pipe.ZCount(ctx, r.GetAllUsersBucketKey(bucket), currentTime, "+inf")
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return 0, err
	}

	var total int64
	for _, cmd := range cmds {
		n, err := cmd.Result()
		if err != nil && err != redis.Nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// GetOnlineUsersByType 根据用户类型获取在线用户
// ZScan 游标分页遍历（同 GetAllOnlineUsers，避免千万级 member 全量 ZRANGE）
// 分桶布局下逐桶遍历后合并，桶间天然无重复（同一 userID 恒定落同一桶）
func (r *RedisOnlineStatusRepository) GetOnlineUsersByType(ctx context.Context, userType models.UserType) ([]string, error) {
	users := make([]string, 0, 1024)
	for bucket := 0; bucket < constants.DefaultKeyBucketCount; bucket++ {
		page, err := r.zscanValidUsers(ctx, r.GetUserTypeBucketKey(userType, bucket))
		if err != nil {
			return nil, err
		}
		users = append(users, page...)
	}
	return users, nil
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
// 有路由信封且 ns 非空时每个用户用 scoped key（ZSET 已分桶，无需逐客户端过滤），
// 信封 ns=""（全局广播）或无路由信封走 unscoped（需逐客户端按 appID 过滤）
func (r *RedisOnlineStatusRepository) BatchGetUserNodes(ctx context.Context, userIDs []string) (map[string][]string, error) {
	if len(userIDs) == 0 {
		return make(map[string][]string), nil
	}

	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	hasRoute := routing.RoutingFromContext(ctx) != nil
	scoped := hasRoute && ns != ""
	normalizedAppID := r.normalizeAppID(appID)

	// needFilter[i]=true 表示该用户命中 unscoped ZSET，需按信封过滤（广播按 appID 匹配）
	// scoped 命中时为 false（ZSET 已分桶，无需逐客户端过滤）
	needFilter := make([]bool, len(userIDs))
	if !scoped && hasRoute {
		for i := range needFilter {
			needFilter[i] = true
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
// 走 luaBatchSetClientsOnline，在同一 Lua 内原子完成 offset 分配 +
// scoped/unscoped ZSET 双写 + bitmap SETBIT + EXPIRE，单次网络往返。
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
		// 格式：clientID|userID|nodeID|userType|expireTime|appID|ns|bucket|clientData（9 段）
		// appID/ns 兜底归一化（client 在 handleRegister 已归一化，此处兜底测试场景），
		// 保证 Lua 收到的 ns 必非空，key 构造无空段；bucket 为热点 key 分桶号（keyBucket 预计算）
		batchData := client.ID + "|" + client.UserID + "|" + client.NodeID + "|" +
			string(client.UserType) + "|" + strconv.FormatInt(expireTime, 10) + "|" +
			r.normalizeAppID(client.AppID) + "|" + constants.NormalizeNamespace(client.Namespace) + "|" +
			strconv.Itoa(r.keyBucket(client.UserID)) + "|" + string(clientData)
		validClients = append(validClients, batchData)
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
		// ARGV 头：ttl, currentTime, clientCount, bitmapTTL, maxOffset, ...clientData
		args := make([]any, 0, 5+len(batch))
		args = append(args, int(r.ttl.Seconds()))
		args = append(args, currentTime)
		args = append(args, len(batch))
		args = append(args, int(r.bitmapTTL.Seconds()))
		args = append(args, r.maxBitmapOffset)
		for _, clientData := range batch {
			args = append(args, clientData)
		}

		if _, err := r.client.Eval(ctx, luaBatchSetClientsOnline, keys, args...).Result(); err != nil {
			return errorx.WrapError("failed to execute batch lua script", err)
		}
	}

	return nil
}

// RenewClientsOnline 心跳批量续期（轻量路径，luaRenewClientsOnline）
//
// 与 BatchSetClientsOnline 的分工：client 数据在连接期间不变，心跳仅需刷新
// TTL 与 ZSET score——本方法跳过 JSON 序列化/压缩/SETEX，单客户端 ~9 条命令，
// 配合 hub 层并行 flush 支撑千万级连接的心跳刷新。
// client:<id> 键缺失（过期/被 maxmemory 淘汰）的客户端由脚本返回序号，
// 此处走 BatchSetClientsOnline 全量重建，保留原自愈语义
func (r *RedisOnlineStatusRepository) RenewClientsOnline(ctx context.Context, clients []*Client) error {
	if len(clients) == 0 {
		return nil
	}

	currentTime := time.Now().Unix()

	// 构造 7 段轻量数据（无 clientData），validIdx 记录 valid[i] 对应 clients 的下标
	valid := make([]string, 0, len(clients))
	validIdx := make([]int, 0, len(clients))
	for i, client := range clients {
		if client == nil || client.ID == "" || client.UserID == "" || client.NodeID == "" {
			continue
		}
		// appID/ns 归一化与 BatchSetClientsOnline 一致，保证 Lua 内 key 构造无空段
		data := client.ID + "|" + client.UserID + "|" + client.NodeID + "|" +
			string(client.UserType) + "|" + r.normalizeAppID(client.AppID) + "|" + constants.NormalizeNamespace(client.Namespace) +
			"|" + strconv.Itoa(r.keyBucket(client.UserID))
		valid = append(valid, data)
		validIdx = append(validIdx, i)
	}
	if len(valid) == 0 {
		return nil
	}

	for batchStart := 0; batchStart < len(valid); batchStart += maxBatchSize {
		batchEnd := batchStart + maxBatchSize
		if batchEnd > len(valid) {
			batchEnd = len(valid)
		}
		batch := valid[batchStart:batchEnd]

		keys := []string{r.keyPrefix}
		// ARGV 头：ttl, currentTime, clientCount, bitmapTTL, maxOffset, ...clientData
		args := make([]any, 0, 5+len(batch))
		args = append(args, int(r.ttl.Seconds()), currentTime, len(batch), int(r.bitmapTTL.Seconds()), r.maxBitmapOffset)
		for _, clientData := range batch {
			args = append(args, clientData)
		}

		result, err := r.client.Eval(ctx, luaRenewClientsOnline, keys, args...).Result()
		if err != nil {
			return errorx.WrapError("failed to execute renew lua script", err)
		}

		// 解析 missing 序号数组（1-based），对这些客户端走全量重建自愈
		missingIdx, ok := result.([]any)
		if !ok || len(missingIdx) == 0 {
			continue
		}
		rebuild := make([]*Client, 0, len(missingIdx))
		for _, v := range missingIdx {
			pos, ok := v.(int64)
			if !ok || pos < 1 || int(pos) > len(batch) {
				continue
			}
			rebuild = append(rebuild, clients[validIdx[batchStart+int(pos)-1]])
		}
		if len(rebuild) > 0 {
			if err := r.BatchSetClientsOnline(ctx, rebuild); err != nil {
				return err
			}
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

		// 格式含 appID|ns，与在线写入路径对称
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
	_, err = r.client.Eval(ctx, luaBatchSetClientsOffline, keys, args...).Result()
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
		// 格式含 appID|ns，与在线写入路径对称
		batchData := r.buildOfflineBatchArg(client.ID, client.UserID, client.NodeID, client.UserType, client.AppID, client.Namespace)
		args = append(args, batchData)
	}

	// 使用 Lua 脚本批量删除
	keys := []string{r.keyPrefix}
	_, err := r.client.Eval(ctx, luaBatchSetClientsOffline, keys, args...).Result()
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

	result, err := r.client.Eval(ctx, luaCleanupExpiredClients, []string{r.keyPrefix, nodeClientsKey}, nodeID, currentTime, constants.DefaultKeyBucketCount).Result()
	if err != nil {
		return 0, fmt.Errorf("执行清理脚本失败: %w", err)
	}

	cleaned, ok := result.(int64)
	if !ok {
		return 0, fmt.Errorf("脚本返回值类型错误")
	}

	return cleaned, nil
}

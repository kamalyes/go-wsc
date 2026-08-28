/*
 * @Author: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-22 00:00:00
 * @FilePath: \go-wsc\repository\lua_scripts_bitmap.go
 * @Description: Bitmap 分层 Lua 脚本常量
 *
 * 三个脚本:
 *   1. luaIsUserOnline   - 只读路径:HGET uid_map → GETBIT,供 IsUserOnline 单次往返判定
 *   2. luaBatchSetClientsOnlineV2  - 写路径:offset 分配 + ZSET 双写 + bitmap SETBIT(替换原 luaBatchSetClientsOnline)
 *   3. luaBatchSetClientsOfflineV2 - 写路径:ZSET 清理 + bitmap SETBIT 0(替换原 luaBatchSetClientsOffline)
 *
 * 返回值约定(luaIsUserOnline):
 *   1  = bitmap 命中,用户在线
 *   0  = uid_map 未分配 offset,用户确定离线(从未上线或 offset 已被清理)
 *   -1 = bitmap miss(offset 超限 或 bit=0 可能过期/淘汰/已下线),调用方走 ZSET ZCount 兜底
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package repository

// luaIsUserOnline Bitmap 快速在线判定(只读,不分配 offset)
//
// 设计:offset 分配在写路径(SetClientOnline)完成,本脚本仅查询:
//   - HGET uid_map 拿 offset,nil → 用户从未上线,确定离线(返回 0)
//   - offset 超过 maxOffset → bitmap 未写入,走 ZSET 兜底(返回 -1)
//   - GETBIT 命中 → 在线(返回 1)
//   - GETBIT 未命中 → bitmap 可能过期/淘汰/用户已下线,走 ZSET 兜底(返回 -1)
//
// KEYS[1] = uidMapKey      (wsc:online:uid_map)
// KEYS[2] = scopedBitmapKey (wsc:online:bm:<appID>:<ns>)
//
// ARGV[1] = userID
// ARGV[2] = maxOffset       (offset 上限,0 表示不限制)
//
// 返回:1=在线 / 0=确定离线 / -1=需 ZSET 兜底
const luaIsUserOnline = `
local offset = redis.call('HGET', KEYS[1], ARGV[1])
if not offset then
    return 0
end
offset = tonumber(offset)
if offset == nil then
    return 0
end
local maxOffset = tonumber(ARGV[2])
if maxOffset and maxOffset > 0 and offset >= maxOffset then
    return -1
end
local bit = redis.call('GETBIT', KEYS[2], offset)
if bit == 1 then
    return 1
end
return -1
`

// luaBatchSetClientsOnlineV2 批量设置客户端在线(V2:含 offset 分配 + bitmap + scoped 双写)
//
// 相比原 luaBatchSetClientsOnline 增加:
//  1. 在 Lua 内原子分配 offset(HGET → INCR → HSETNX),避免多节点首次分配竞态
//  2. 按 appID/ns 分桶写 scoped ZSET,同时双写 unscoped ZSET(dual-write 兼容期)
//  3. SETBIT scoped/global bitmap 并 EXPIRE 续期
//  4. offset 超限时跳过 SETBIT(只写 ZSET,查询走 ZSET 兜底)
//
// KEYS[1] = keyPrefix (用于构建所有 key)
//
// ARGV[1] = ttl           (client:<id> 的 SETEX TTL,秒)
// ARGV[2] = currentTime    (当前时间戳,清理 ZSET 过期项)
// ARGV[3] = clientCount    (客户端数量)
// ARGV[4] = bitmapTTL      (bitmap EXPIRE,秒)
// ARGV[5] = maxOffset      (offset 上限,0=不限制)
// ARGV[6..] = 每个客户端的数据,格式:clientID|userID|nodeID|userType|expireTime|appID|ns|clientData
//
// 返回值:成功处理的客户端数量
const luaBatchSetClientsOnlineV2 = `
local keyPrefix = KEYS[1]
local uidMapKey = keyPrefix .. "uid_map"
local uidCounterKey = keyPrefix .. "uid_counter"

local ttl = tonumber(ARGV[1])
local currentTime = tonumber(ARGV[2])
local clientCount = tonumber(ARGV[3])
local bitmapTTL = tonumber(ARGV[4])
local maxOffset = tonumber(ARGV[5])
local successCount = 0

local sep = string.byte("|")

for i = 1, clientCount do
    local idx = 5 + i
    local data = ARGV[idx]

    -- 解析 8 段: clientID|userID|nodeID|userType|expireTime|appID|ns|clientData
    local parts = {}
    local lastPos = 1
    for j = 1, 7 do
        local pos = string.find(data, "|", lastPos, true)
        if pos then
            table.insert(parts, string.sub(data, lastPos, pos - 1))
            lastPos = pos + 1
        end
    end
    table.insert(parts, string.sub(data, lastPos))

    if #parts == 8 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local expireTime = tonumber(parts[5])
        local appID = parts[6]
        local ns = parts[7]
        local clientData = parts[8]

        if ns == "" then ns = "__default_ns__" end -- 与 constants.DefaultNamespace 保持一致（Lua 无法引用 Go 常量，硬编码字面量须与 Go 侧同值）

        local clientKey = keyPrefix .. "client:" .. clientID
        local scopedUserClientsKey = keyPrefix .. "user_clients:" .. appID .. ":" .. ns .. ":" .. userID
        local unscopedUserClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local allUsersKey = keyPrefix .. "all_users"
        local typeKey = keyPrefix .. "type:" .. userType
        local scopedBitmapKey = keyPrefix .. "bm:" .. appID .. ":" .. ns
        local globalBitmapKey = keyPrefix .. "bm:" .. appID .. ":__global__" -- __global__ 与 constants.GlobalBitmapNS 保持一致

        -- 1. Offset 分配(原子,HSETNX 保证多节点首次分配竞态下 offset 唯一)
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

        -- 4. ZADD scoped + unscoped(dual-write)+ node + all_users + type
        redis.call('ZADD', scopedUserClientsKey, expireTime, clientID)
        redis.call('EXPIRE', scopedUserClientsKey, ttl)
        redis.call('ZADD', unscopedUserClientsKey, expireTime, clientID)
        redis.call('EXPIRE', unscopedUserClientsKey, ttl)
        redis.call('ZADD', nodeClientsKey, expireTime, clientID)
        redis.call('ZADD', allUsersKey, expireTime, userID)
        redis.call('ZADD', typeKey, expireTime, userID)

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

// luaBatchSetClientsOfflineV2 批量设置客户端离线(V2:含 bitmap SETBIT 0 + scoped/unscoped 双清理)
//
// 相比原 luaBatchSetClientsOffline 增加:
//  1. 同时 ZREM scoped + unscoped(dual-write 兼容期)
//  2. scoped 下线后:若 unscoped 仍有其他信封连接,仅清 scoped bitmap(global 保留);
//     若 unscoped 也空,清 global bitmap + all_users/type
//  3. 不 HDEL uid_map(offset 永久保留,用户再上线时复用,避免 INCR 空洞累积)
//
// KEYS[1] = keyPrefix
//
// ARGV[1] = currentTime    (清理 ZSET 过期项)
// ARGV[2] = clientCount
// ARGV[3..] = 每个客户端数据,格式:clientID|userID|nodeID|userType|appID|ns
//
// 返回值:成功处理的客户端数量
const luaBatchSetClientsOfflineV2 = `
local keyPrefix = KEYS[1]
local uidMapKey = keyPrefix .. "uid_map"

local currentTime = tonumber(ARGV[1])
local clientCount = tonumber(ARGV[2])
local successCount = 0

for i = 1, clientCount do
    local idx = 2 + i
    local data = ARGV[idx]

    -- 解析 6 段: clientID|userID|nodeID|userType|appID|ns
    local parts = {}
    for part in string.gmatch(data, "([^|]+)") do
        table.insert(parts, part)
    end

    if #parts == 6 then
        local clientID = parts[1]
        local userID = parts[2]
        local nodeID = parts[3]
        local userType = parts[4]
        local appID = parts[5]
        local ns = parts[6]

        if ns == "" then ns = "__default_ns__" end -- 与 constants.DefaultNamespace 保持一致（Lua 无法引用 Go 常量，硬编码字面量须与 Go 侧同值）

        local clientKey = keyPrefix .. "client:" .. clientID
        local ownerKey = keyPrefix .. "owner:" .. clientID
        local scopedUserClientsKey = keyPrefix .. "user_clients:" .. appID .. ":" .. ns .. ":" .. userID
        local unscopedUserClientsKey = keyPrefix .. "user_clients:" .. userID
        local nodeClientsKey = keyPrefix .. "node_clients:" .. nodeID
        local allUsersKey = keyPrefix .. "all_users"
        local typeKey = keyPrefix .. "type:" .. userType
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

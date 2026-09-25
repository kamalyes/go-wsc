/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-09 20:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-10 21:30:00
 * @FilePath: \go-wsc\constants\delivery.go
 * @Description: 送达分级与控制通道常量 —— 分级送达保证体系的单一事实源
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

import "time"

// CtrlChanCapacity 控制通道（CtrlCh）默认容量
// 控制消息量级低（连接生命周期事件），16 足以吸收瞬时突发；
// 满时走 SendControl 的降级语义（KickOut/Close 直接断链），不阻塞投递方
const CtrlChanCapacity = 16

// DefaultAdmissionHighWatermark 准入闸门默认高水位线（在途消息数）
// backlog = 写泵写入数 - 业务投递数；超过高水位触发过载升级（L++）
// 基准：单节点万级连接 × 每连接 SendChan 256 缓冲的合理在途量级
const DefaultAdmissionHighWatermark = 8000

// DefaultAdmissionLowWatermark 准入闸门默认低水位线（在途消息数）
// 连续 3 个评估周期低于低水位触发过载降级（L--，防抖动）
const DefaultAdmissionLowWatermark = 2000

// DefaultAdmissionEvalInterval 准入闸门评估周期（水位判定 + AIMD 调整）
const DefaultAdmissionEvalInterval = "500ms"

// DefaultAdmissionCooldownLevels 过载降级所需的连续低水位周期数（防抖动）
const DefaultAdmissionCooldownLevels = 3

// DefaultBroadcastShaperRate 广播出向整形默认速率（条/秒）
// 每条广播消息消耗 1 个令牌（非每客户端）；洪峰时平滑扇出速率，填谷靠 AIMD 恢复
const DefaultBroadcastShaperRate = 10000

// DefaultCoalescerCapacity 高频合并器默认容量（同 key 并存的最大消息数）
const DefaultCoalescerCapacity = 4096

// SlowConsumerThresholdRatio 慢消费者判定的队列利用率阈值（BacklogRatio 超过即计一次）
const SlowConsumerThresholdRatio = 0.9

// SlowConsumerConsecutiveThreshold 慢消费者驱逐所需的连续超阈值次数
const SlowConsumerConsecutiveThreshold = 3

// DefaultMessageDeadline 普通级消息默认过期时间上限（0=不过期）
// 入队前 deadline-aware 检查用；高频级由 latest-wins 合并天然去旧
const DefaultMessageDeadline = "0s"

// ClientWriteBatchSize 写泵单批最大帧数（writev 合批）
// 首条直写（无积压时 1 次 syscall 低延迟）后非阻塞排空积压合并写出：
// 突发 N 条 → 2 次 syscall（原 N 次）
const ClientWriteBatchSize = 64

// ClientWriteTimeout 写泵单批写超时（整批共享一次 deadline）
// 突发场景 N 次期限设置收敛为 1 次
const ClientWriteTimeout = 10 * time.Second

// DefaultGroupMemberCacheTTL 群组成员拓扑缓存默认存活期
// 群消息投递热路径 0 回源的一致性窗口：本地写路径即时逐出，跨节点写最多滞后一个 TTL
const DefaultGroupMemberCacheTTL = 30 * time.Second

// DefaultGroupMemberCacheEntries 群组成员拓扑缓存默认条目上限（LRU 容量）
const DefaultGroupMemberCacheEntries = 1024

// DefaultGroupMemberCacheMaxMembers 群组成员拓扑缓存单条目成员数预算默认上限
// 超出则该群组不入缓存：100w 成员大群单条可达数十 MB，缓存反而挤爆内存，回源保持两段 Pipeline
const DefaultGroupMemberCacheMaxMembers = 10000

// DefaultGroupMemberCacheNegativeTTL 群组成员拓扑缓存负缓存（确认无实例条目）默认存活期
// 短于正缓存：压缩"解散后误投"边界窗口的同时，控制无效群组的重复回源频率
const DefaultGroupMemberCacheNegativeTTL = 5 * time.Second

// DefaultGroupInvalidationFlushInterval 群拓扑失效广播聚合窗口
// 窗口内多次拓扑写（如上线风暴的注册入组）合并为单条批量广播，广播量 O(写次数) → O(群组数/窗口)
const DefaultGroupInvalidationFlushInterval = 100 * time.Millisecond

// DefaultGRPCBatchWindow 跨节点 gRPC 微批合帧窗口
// 窗口内发往同一节点的多消息合并为单次 BatchDispatch RPC，RPC 次数 O(消息数) → O(批次数)；
// 窗口越小排队延迟越低、越大合帧率越高；0 表示禁用微批（逐消息单发，延迟敏感场景）
const DefaultGRPCBatchWindow = 2 * time.Millisecond

// DefaultGRPCBatchMaxItems 跨节点 gRPC 微批单批累计上限（达到即触发立即 flush，优先于窗口）
const DefaultGRPCBatchMaxItems = 32

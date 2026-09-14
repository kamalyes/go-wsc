/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-09 20:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-10 21:30:00
 * @FilePath: \go-wsc\constants\delivery.go
 * @Description: 送达分级与控制通道常量 —— 分级送达保证体系的单一事实源
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

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

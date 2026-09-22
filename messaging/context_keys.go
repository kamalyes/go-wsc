/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 10:08:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 10:08:00
 * @FilePath: \go-wsc\messaging\context_keys.go
 * @Description: 消息域上下文键 —— 连接归属与离线广播聚合器的单一事实源
 *
 * 迁移自 constants/context.go（P2 批2 域化）：
 *   - SenderID/UserID 键由 transport 升级器在连接 ctx 注入、消息域消费，
 *     键与消费语义同属消息链路，归位消息域（transport 单向依赖 messaging，无循环）
 *   - 离线广播聚合器键供批量扇出路径聚合未命中用户，扇出结束后统一跨节点推送
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package messaging

// ContextKey 上下文键类型（消息域：连接归属用户 + 离线广播聚合器）
type ContextKey string

const (
	// ContextKeyUserID 用户ID
	ContextKeyUserID ContextKey = "user_id"
	// ContextKeySenderID 发送者ID（连接级 ctx 注入，标识连接归属用户）
	ContextKeySenderID ContextKey = "sender_id"
	// ContextKeyOfflineBroadcastCollector 批量扇出路径的离线广播聚合器
	//
	// 群组成员可达数万，扇出 goroutine 内逐人同步广播会造成 N 倍集群流量放大与
	// Redis 连接池耗尽（OOM 教训）；但完全跳过又会丢失"索引滞后但实际在线"用户的实时投递。
	// 折中：扇出期间仅聚合收集未命中用户，扇出结束后统一逐人跨节点推送（真的推送），
	// 真正离线的用户由离线存储 + 重连上线拉取兜底
	ContextKeyOfflineBroadcastCollector ContextKey = "offline_broadcast_collector"
)

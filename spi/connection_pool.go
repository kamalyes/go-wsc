/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-16 09:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 10:52:00
 * @FilePath: \go-wsc\spi\connection_pool.go
 * @Description: 连接池资源 SPI - ConnectionPoolManager 接口契约

 * Hub 在发送路径上需要「按需取连接池资源（SMTP 等）」的能力，
 * 但连接池是可替换的横切关注点，实现不应硬编码在核心：
 * 连接池实现由业务侧自备（SMTP、短信网关等），core 只认接口
 * 业务侧注入任意实现，不注入则以 nil 安全降级运行
 *
 * 历史上本文件还定义了 RateLimiter 契约（按用户维度限制消息速率）
 * 该契约连同其中间件实现一并删除：Hub 的消息流向是「服务端 → 终端」的下行推送，
 * 终端上行只有心跳与控制帧，不存在「终端刷屏」这一需要限流的场景；
 * 且 Hub 持有 rateLimiter 字段期间从未在发送路径上调用 CheckLimit，
 * 接口自始未接入发送管线，属无实际意义的摆设
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package spi

// ConnectionPoolManager 连接池管理契约
//
// 用于管理需要复用且创建代价高的外部连接（当前唯一消费者是 SMTP）
// 返回 interface{} 而非具体类型，是因为不同业务接入的连接类型差异极大
// （*smtp.Client / *redis.Client / 自研网关句柄），core 不对其做任何假设，
// 仅负责持有与透传
type ConnectionPoolManager interface {
	// GetSMTPClient 获取 SMTP 客户端（未配置时返回 nil）
	GetSMTPClient() interface{}
}

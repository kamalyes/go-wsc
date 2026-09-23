/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-23 09:33:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-23 09:33:00
 * @FilePath: \go-wsc\hub\callbacks.go
 * @Description: 应用层回调类型与集合 —— 连接/心跳/离线推送/群组生命周期
 *
 * HubCallbacks 收敛编排层持有的全部应用回调（hub 只持一个字段）：
 * 构造期注入见 options.go 的 With 系列，运行期替换见 accessors.go 的 Set 系列。
 * 消息域回调（发送完成/上行消息/错误处理）不经本结构——hub 的注入
 * 方法直接委托 messagingMgr，消费者在消息域内。
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"time"

	"github.com/kamalyes/go-wsc/models"
)

// OfflineMessagePushCallback 离线消息推送回调
type OfflineMessagePushCallback func(userID string, pushedMessageIDs []string, failedMessageIDs []string)

// HeartbeatTimeoutCallback 心跳超时回调
type HeartbeatTimeoutCallback func(clientID string, userID string, lastHeartbeat time.Time)

// HeartbeatReportCallback 心跳上报回调
type HeartbeatReportCallback func(client *models.Client)

// BeforeHeartbeatCallback 心跳处理前回调，返回 false 则跳过后续心跳处理
type BeforeHeartbeatCallback func(client *models.Client) bool

// AfterHeartbeatCallback 心跳处理后回调
type AfterHeartbeatCallback func(client *models.Client)

// ClientConnectCallback 客户端连接回调
type ClientConnectCallback func(ctx context.Context, client *models.Client, record *models.ConnectionRecord) error

// ClientDisconnectCallback 客户端断开回调
type ClientDisconnectCallback func(ctx context.Context, client *models.Client, reason models.DisconnectReason) error

// HubCallbacks 应用层回调集合（连接/心跳/离线推送/群组生命周期）
//
// 回调未注入（nil）时对应环节直接跳过；跨 goroutine 替换单个回调存在
// 数据竞争，运行期替换仅限启动装配阶段完成后的确定性场景
type HubCallbacks struct {
	// 连接生命周期
	ClientConnect    ClientConnectCallback
	ClientDisconnect ClientDisconnectCallback

	// 离线推送完成（上游据此删除已推送消息）
	OfflineMessagePush OfflineMessagePushCallback

	// 心跳生命周期（BeforeHeartbeat 返回 false 跳过后续心跳处理）
	HeartbeatTimeout HeartbeatTimeoutCallback
	HeartbeatReport  HeartbeatReportCallback
	BeforeHeartbeat  BeforeHeartbeatCallback
	AfterHeartbeat   AfterHeartbeatCallback

	// 群组生命周期（结构类型与 group.Host 端口一致）
	GroupDisband     func(ctx context.Context, namespace, groupID string)
	GroupMemberJoin  func(ctx context.Context, namespace, groupID string, userIDs []string)
	GroupMemberLeave func(ctx context.Context, namespace, groupID string, userIDs []string)
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-28 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-29 21:56:05
 * @FilePath: \go-wsc\hub\callbacks.go
 * @Description: Hub 回调管理
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package hub

// ============================================================================
// 回调注册方法
// ============================================================================

// OnOfflineMessagePush 注册离线消息推送回调函数
// 当离线消息推送完成时会调用此回调，由上游决定是否删除消息
//
// 参数:
//   - userID: 用户ID
//   - pushedMessageIDs: 成功推送的消息ID列表
//   - failedMessageIDs: 推送失败的消息ID列表
//
// 示例:
//
//	hub.OnOfflineMessagePush(func(userID string, pushedMessageIDs, failedMessageIDs []string) {
//	    log.Printf("用户 %s 推送完成，成功: %d, 失败: %d", userID, len(pushedMessageIDs), len(failedMessageIDs))
//	    // 删除已推送的消息
//	    offlineRepo.DeleteOfflineMessages(ctx, userID, pushedMessageIDs)
//	})
func (h *Hub) OnOfflineMessagePush(callback OfflineMessagePushCallback) {
	h.offlineMessagePushCallback = callback
}

// OnMessageSend 注册消息发送完成回调函数
// 当消息发送完成（无论成功还是失败）时会调用此回调
//
// 参数:
//   - msg: 发送的消息
//   - result: 发送结果，包含重试信息和最终错误
//
// 示例:
//
//	hub.OnMessageSend(func(msg *HubMessage, result *SendResult) {
//	    if result.FinalError != nil {
//	        log.Printf("消息发送失败: %s, 错误: %v", msg.ID, result.FinalError)
//	        // 更新消息状态为失败
//	        messageRepo.BatchUpdateMessageStatus(ctx, []string{msg.ID}, MESSAGE_STATUS_FAILED)
//	    } else {
//	        log.Printf("消息发送成功: %s, 重试次数: %d", msg.ID, len(result.Attempts)-1)
//	        // 更新消息状态为已发送
//	        messageRepo.BatchUpdateMessageStatus(ctx, []string{msg.ID}, MESSAGE_STATUS_SENT)
//	    }
//	})
func (h *Hub) OnMessageSend(callback MessageSendCallback) {
	h.messageSendCallback = callback
}

// OnQueueFull 注册队列满回调
// 当消息队列满时会调用此回调
//
// 参数:
//   - msg: 发送的消息
//   - recipient: 接收者ID
//   - queueType: 队列类型
//   - err: 队列满错误
func (h *Hub) OnQueueFull(callback QueueFullCallback) {
	h.queueFullCallback = callback
}

// OnHeartbeatTimeout 注册心跳超时回调
// 当客户端心跳超时时会调用此回调
//
// 参数:
//   - clientID: 客户端ID
//   - userID: 用户ID
//   - lastHeartbeat: 最后心跳时间
//
// 示例:
//
//	hub.OnHeartbeatTimeout(func(clientID, userID string, lastHeartbeat time.Time) {
//	    log.Printf("客户端 %s 心跳超时", clientID)
//	    // 更新数据库、清理缓存等
//	})
func (h *Hub) OnHeartbeatTimeout(callback HeartbeatTimeoutCallback) {
	h.heartbeatTimeoutCallback = callback
}

// ============================================================================
// 应用层回调注册方法
// ============================================================================

// OnClientConnect 注册客户端连接回调
// 在客户端成功建立连接时调用
// 用途：执行权限验证、记录连接日志、初始化用户会话等
func (h *Hub) OnClientConnect(callback ClientConnectCallback) {
	h.clientConnectCallback = callback
}

// OnClientDisconnect 注册客户端断开连接回调
// 在客户端断开连接时调用
// 用途：清理资源、更新在线状态、保存会话状态等
func (h *Hub) OnClientDisconnect(callback ClientDisconnectCallback) {
	h.clientDisconnectCallback = callback
}

// OnMessageReceived 注册消息接收回调
// 在接收到客户端消息时调用
// 用途：消息验证、业务逻辑处理、消息路由等
func (h *Hub) OnMessageReceived(callback MessageReceivedCallback) {
	h.messageReceivedCallback = callback
}

// OnError 注册错误处理回调
// 在发生错误时调用
// 用途：统一错误处理、日志记录、告警通知等
func (h *Hub) OnError(callback ErrorCallback) {
	h.errorCallback = callback
}

// OnBatchSendFailure 注册批量发送失败回调
// 在批量发送某个消息失败时调用
// 用途：记录失败日志、重试机制、告警通知等
//
// 示例：
//
//	hub.OnBatchSendFailure(func(userID string, msg *HubMessage, err error) {
//	    log.Printf("批量发送失败: userID=%s, msgID=%s, error=%v", userID, msg.ID, err)
//	})
func (h *Hub) OnBatchSendFailure(callback BatchSendFailureCallback) {
	h.batchSendFailureCallback = callback
}

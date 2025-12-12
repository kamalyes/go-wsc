/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-01 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-01 23:00:00
 * @FilePath: \go-wsc\hub_business.go
 * @Description: Hub 高阶业务函数 - 减轻业务层重复判断逻辑
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"errors"
	"time"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
)

// ============================================================================
// 高阶业务函数 - 智能消息发送（自动处理在线/离线）
// ============================================================================

// SendToUserWithOfflineCallback 智能发送消息 - 自动处理在线/离线逻辑
// 如果用户在线则发送，离线则调用回调函数（用于存储离线消息）
//
// 参数:
//   - ctx: 上下文
//   - userID: 用户ID
//   - msg: 消息
//   - offlineCallback: 用户离线时的回调函数（用于存储离线消息）
//
// 返回:
//   - isOnline: 用户是否在线
//   - err: 错误信息（仅在线发送时可能有错误）
//
// 示例:
//
//	isOnline, err := hub.SendToUserWithOfflineCallback(ctx, userID, msg, func(msg *HubMessage) {
//	   存储离线消息到数据库
//	    saveOfflineMessage(userID, msg)
//	})
func (h *Hub) SendToUserWithOfflineCallback(ctx context.Context, userID string, msg *HubMessage,
	offlineCallback func(*HubMessage)) (isOnline bool, err error) {

	// 检查用户是否在线
	isOnline, checkErr := h.IsUserOnline(userID)
	if checkErr != nil || !isOnline {
		// 用户离线，调用回调
		if offlineCallback != nil {
			offlineCallback(msg)
		}
		return false, nil
	}

	// 用户在线，发送消息（带重试）
	result := h.SendToUserWithRetry(ctx, userID, msg)
	return true, result.FinalError
}

// SendToUserWithOfflineFallback 智能发送消息 - 失败时自动降级
// 尝试发送给在线用户，如果失败（或离线）则调用离线回调
//
// 参数:
//   - ctx: 上下文
//   - userID: 用户ID
//   - msg: 消息
//   - offlineCallback: 离线或发送失败时的回调函数
//
// 返回:
//   - delivered: 消息是否成功投递（在线发送成功为true）
//   - err: 错误信息
//
// 示例:
//
//	delivered, err := hub.SendToUserWithOfflineFallback(ctx, userID, msg, func(msg *HubMessage, sendErr error) {
//	    log.Printf("消息投递失败: %v, 存储离线消息", sendErr)
//	    saveOfflineMessage(userID, msg)
//	})
func (h *Hub) SendToUserWithOfflineFallback(ctx context.Context, userID string, msg *HubMessage,
	offlineCallback func(*HubMessage, error)) (delivered bool, err error) {

	// 检查用户是否在线
	isOnline, _ := h.IsUserOnline(userID)

	if !isOnline {
		// 用户离线，直接调用回调
		if offlineCallback != nil {
			offlineCallback(msg, errorx.NewError(ErrTypeUserOffline, "user_id: %s", userID))
		}
		return false, nil
	}

	// 用户在线，尝试发送（带重试）
	result := h.SendToUserWithRetry(ctx, userID, msg)
	if result.FinalError != nil {
		// 发送失败，调用回调
		if offlineCallback != nil {
			offlineCallback(msg, result.FinalError)
		}
		return false, result.FinalError
	}

	return true, nil
}

// ============================================================================
// 批量发送 - 智能处理在线/离线
// ============================================================================

// BroadcastResult 批量发送结果统计
type BroadcastResult struct {
	Total      int              // 总用户数量
	Success    int              // 成功发送数量
	Offline    int              // 离线用户数量
	Failed     int              // 发送失败数量
	Errors     map[string]error // 错误详情 map[userID]error
	OfflineIDs []string         // 离线用户ID列表
	FailedIDs  []string         // 发送失败的用户ID列表
}

// BroadcastToUsers 批量发送消息给多个用户 - 智能处理在线/离线
//
// 参数:
//   - ctx: 上下文
//   - userIDs: 用户ID列表
//   - msg: 消息
//   - offlineCallback: 用户离线时的回调函数（可选）
//
// 返回:
//   - result: 发送结果统计
//
// 示例:
//
//	result := hub.BroadcastToUsers(ctx, []string{"user1", "user2"}, msg, func(userID string, msg *HubMessage) {
//	    saveOfflineMessage(userID, msg)
//	})
//	log.Printf("成功: %d, 离线: %d, 失败: %d", result.Success, result.Offline, result.Failed)
func (h *Hub) BroadcastToUsers(ctx context.Context, userIDs []string, msg *HubMessage,
	offlineCallback func(string, *HubMessage)) *BroadcastResult {

	result := &BroadcastResult{
		Total:      len(userIDs),
		Success:    0,
		Offline:    0,
		Failed:     0,
		Errors:     make(map[string]error),
		OfflineIDs: make([]string, 0),
		FailedIDs:  make([]string, 0),
	}

	for _, userID := range userIDs {
		isOnline, err := h.SendToUserWithOfflineCallback(ctx, userID, msg, func(m *HubMessage) {
			result.Offline++
			if offlineCallback != nil {
				offlineCallback(userID, m)
			}
		})

		if !isOnline {
			continue
		}

		if err != nil {
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, userID)
			result.Errors[userID] = err
		} else {
			result.Success++
		}
	}

	return result
}

// BroadcastToUserType 向指定类型用户批量发送 - 智能处理在线/离线
//
// 参数:
//   - ctx: 上下文
//   - userType: 用户类型
//   - msg: 消息
//   - offlineCallback: 用户离线时的回调函数（可选，对于GetOnlineUsersByType已过滤在线用户，通常不需要）
//
// 返回:
//   - result: 发送结果统计
//
// 示例:
//
//	result := hub.BroadcastToUserType(ctx, UserTypeAgent, msg, nil)
//	log.Printf("成功向 %d 个客服发送消息", result.Success)
func (h *Hub) BroadcastToUserType(ctx context.Context, userType UserType, msg *HubMessage,
	offlineCallback func(string, *HubMessage)) *BroadcastResult {

	// 获取该类型的所有在线用户
	onlineUsers, err := h.GetOnlineUsersByType(userType)
	if err != nil {
		return &BroadcastResult{
			Total:  0,
			Errors: map[string]error{"query_error": err},
		}
	}

	// 使用 BroadcastToUsers 批量发送（这些用户都是在线的）
	result := &BroadcastResult{
		Total:      len(onlineUsers),
		Success:    0,
		Offline:    0,
		Failed:     0,
		Errors:     make(map[string]error),
		OfflineIDs: make([]string, 0),
		FailedIDs:  make([]string, 0),
	}

	for _, userID := range onlineUsers {
		sendResult := h.SendToUserWithRetry(ctx, userID, msg)
		if sendResult.FinalError != nil {
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, userID)
			result.Errors[userID] = sendResult.FinalError
			// 即使在线，发送也可能失败，调用离线回调
			if offlineCallback != nil {
				offlineCallback(userID, msg)
			}
		} else {
			result.Success++
		}
	}

	return result
}

// ============================================================================
// 用户在线状态查询 - 高性能批量查询
// ============================================================================

// UserOnlineDetails 用户在线详情
type UserOnlineDetails struct {
	IsOnline     bool       // 是否在线
	HasWebSocket bool       // 是否有WebSocket连接
	HasSSE       bool       // 是否有SSE连接
	ClientID     string     // 客户端ID
	UserType     UserType   // 用户类型
	Status       UserStatus // 用户状态
	LastSeen     time.Time  // 最后活跃时间
	Client       *Client    // 客户端对象（如果在线）
}

// GetUserOnlineDetails 获取用户在线详情 - 一次查询获取所有信息
//
// 返回值包含:
//   - IsOnline: 是否在线
//   - Client: 客户端信息（如果在线）
//   - UserType: 用户类型
//   - Status: 用户状态
//   - LastSeen: 最后活跃时间
//
// 示例:
//
//	details := hub.GetUserOnlineDetails(userID)
//	if details.IsOnline {
//	    log.Printf("用户在线: Type=%s, Status=%s", details.UserType, details.Status)
//	}
func (h *Hub) GetUserOnlineDetails(userID string) *UserOnlineDetails {
	h.mutex.RLock()
	client, wsExists := h.userToClient[userID]
	h.mutex.RUnlock()

	h.sseMutex.RLock()
	_, sseExists := h.sseClients[userID]
	h.sseMutex.RUnlock()

	details := &UserOnlineDetails{
		IsOnline:     wsExists || sseExists,
		HasWebSocket: wsExists,
		HasSSE:       sseExists,
	}

	if client != nil {
		details.ClientID = client.ID
		details.UserType = client.UserType
		details.Status = client.Status
		details.LastSeen = client.LastSeen
		details.Client = client
	}

	return details
}

// BatchGetUserOnlineStatus 批量获取用户在线状态 - O(n) 一次性查询
//
// 参数:
//   - userIDs: 用户ID列表
//
// 返回:
//   - map[userID]isOnline
//
// 示例:
//
//	statuses := hub.BatchGetUserOnlineStatus([]string{"user1", "user2", "user3"})
//	for userID, isOnline := range statuses {
//	    log.Printf("%s: %v", userID, isOnline)
//	}
func (h *Hub) BatchGetUserOnlineStatus(userIDs []string) map[string]bool {
	result := make(map[string]bool, len(userIDs))

	h.mutex.RLock()
	for _, userID := range userIDs {
		_, exists := h.userToClient[userID]
		result[userID] = exists
	}
	h.mutex.RUnlock()

	// 检查SSE连接
	h.sseMutex.RLock()
	for _, userID := range userIDs {
		if !result[userID] { // 如果WebSocket不在线，检查SSE
			_, exists := h.sseClients[userID]
			if exists {
				result[userID] = true
			}
		}
	}
	h.sseMutex.RUnlock()

	return result
}

// FilterOnlineUsers 从用户列表中过滤出在线用户
//
// 参数:
//   - userIDs: 用户ID列表
//
// 返回:
//   - 在线用户ID列表
//
// 示例:
//
//	onlineUsers := hub.FilterOnlineUsers([]string{"user1", "user2", "user3"})
func (h *Hub) FilterOnlineUsers(userIDs []string) []string {
	onlineUsers := make([]string, 0, len(userIDs))

	h.mutex.RLock()
	for _, userID := range userIDs {
		if _, exists := h.userToClient[userID]; exists {
			onlineUsers = append(onlineUsers, userID)
			continue
		}
	}
	h.mutex.RUnlock()

	// 检查SSE连接
	h.sseMutex.RLock()
	for _, userID := range userIDs {
		// 避免重复添加
		alreadyAdded := false
		for _, online := range onlineUsers {
			if online == userID {
				alreadyAdded = true
				break
			}
		}
		if !alreadyAdded {
			if _, exists := h.sseClients[userID]; exists {
				onlineUsers = append(onlineUsers, userID)
			}
		}
	}
	h.sseMutex.RUnlock()

	return onlineUsers
}

// FilterOfflineUsers 从用户列表中过滤出离线用户
//
// 参数:
//   - userIDs: 用户ID列表
//
// 返回:
//   - 离线用户ID列表
//
// 示例:
//
//	offlineUsers := hub.FilterOfflineUsers([]string{"user1", "user2", "user3"})
//	for _, userID := range offlineUsers {
//	    saveOfflineMessage(userID, msg)
//	}
func (h *Hub) FilterOfflineUsers(userIDs []string) []string {
	offlineUsers := make([]string, 0, len(userIDs))
	onlineMap := h.BatchGetUserOnlineStatus(userIDs)

	for _, userID := range userIDs {
		if !onlineMap[userID] {
			offlineUsers = append(offlineUsers, userID)
		}
	}

	return offlineUsers
}

// GetOnlineUsersWithDetails 获取所有在线用户的详细信息
//
// 返回:
//   - map[userID]*UserOnlineDetails
//
// 示例:
//
//	users := hub.GetOnlineUsersWithDetails()
//	for userID, details := range users {
//	    log.Printf("%s: Type=%s, Status=%s", userID, details.UserType, details.Status)
//	}
func (h *Hub) GetOnlineUsersWithDetails() map[string]*UserOnlineDetails {
	result := make(map[string]*UserOnlineDetails)

	h.mutex.RLock()
	for userID, client := range h.userToClient {
		result[userID] = &UserOnlineDetails{
			IsOnline:     true,
			HasWebSocket: true,
			ClientID:     client.ID,
			UserType:     client.UserType,
			Status:       client.Status,
			LastSeen:     client.LastSeen,
			Client:       client,
		}
	}
	h.mutex.RUnlock()

	h.sseMutex.RLock()
	for userID := range h.sseClients {
		if details, exists := result[userID]; exists {
			// 用户同时有WebSocket和SSE连接
			details.HasSSE = true
		} else {
			// 用户只有SSE连接
			result[userID] = &UserOnlineDetails{
				IsOnline: true,
				HasSSE:   true,
			}
		}
	}
	h.sseMutex.RUnlock()

	return result
}

// CountOnlineUsersByType 统计各类型在线用户数量
//
// 返回:
//   - map[UserType]count
//
// 示例:
//
//	counts := hub.CountOnlineUsersByType()
//	log.Printf("在线客服: %d, 在线客户: %d", counts[UserTypeAgent], counts[UserTypeCustomer])
func (h *Hub) CountOnlineUsersByType() map[UserType]int {
	counts := make(map[UserType]int)

	h.mutex.RLock()
	for _, client := range h.userToClient {
		counts[client.UserType]++
	}
	h.mutex.RUnlock()

	return counts
}

// ============================================================================
// 条件过滤发送
// ============================================================================

// SendToUsersWithPredicate 向满足条件的用户发送消息
//
// 参数:
//   - ctx: 上下文
//   - predicate: 过滤条件函数
//   - msg: 消息
//
// 返回:
//   - 发送成功的数量
//
// 示例:
//
//	向所有在线且状态为忙碌的客服发送消息
//	count := hub.SendToUsersWithPredicate(ctx, func(client *Client) bool {
//	    return client.UserType == UserTypeAgent && client.Status == UserStatusBusy
//	}, msg)
func (h *Hub) SendToUsersWithPredicate(ctx context.Context, predicate func(*Client) bool, msg *HubMessage) int {
	clients := h.FilterClients(predicate)

	successCount := 0
	for _, client := range clients {
		result := h.SendToUserWithRetry(ctx, client.UserID, msg)
		if result.Success {
			successCount++
		}
	}

	return successCount
}

// ============================================================================
// 会话成员管理 - 高阶函数
// ============================================================================

// SendToGroupMembers 向群组/会话成员发送消息 - 智能处理在线/离线
//
// 参数:
//   - ctx: 上下文
//   - memberIDs: 成员ID列表
//   - msg: 消息
//   - excludeSender: 是否排除发送者（避免自己给自己发）
//   - offlineCallback: 离线回调
//
// 返回:
//   - result: 发送结果统计
//
// 示例:
//
//	向会话成员广播，排除发送者自己
//	result := hub.SendToGroupMembers(ctx, memberIDs, msg, true, func(userID string, msg *HubMessage) {
//	    saveOfflineMessage(userID, msg)
//	})
func (h *Hub) SendToGroupMembers(ctx context.Context, memberIDs []string, msg *HubMessage,
	excludeSender bool, offlineCallback func(string, *HubMessage)) *BroadcastResult {

	// 如果需要排除发送者，从列表中移除
	filteredIDs := memberIDs
	if excludeSender && msg.Sender != "" {
		filteredIDs = make([]string, 0, len(memberIDs))
		for _, id := range memberIDs {
			if id != msg.Sender {
				filteredIDs = append(filteredIDs, id)
			}
		}
	}

	// 使用 BroadcastToUsers 批量发送
	return h.BroadcastToUsers(ctx, filteredIDs, msg, offlineCallback)
}

// PartitionUsersByOnlineStatus 将用户列表分区为在线和离线两组
//
// 参数:
//   - userIDs: 用户ID列表
//
// 返回:
//   - onlineUsers: 在线用户列表
//   - offlineUsers: 离线用户列表
//
// 示例:
//
//	online, offline := hub.PartitionUsersByOnlineStatus(memberIDs)
//	向在线用户发送
//	for _, userID := range online {
//	    hub.SendToUser(ctx, userID, msg)
//	}
//	为离线用户存储消息
//	for _, userID := range offline {
//	    saveOfflineMessage(userID, msg)
//	}
func (h *Hub) PartitionUsersByOnlineStatus(userIDs []string) (onlineUsers []string, offlineUsers []string) {
	onlineMap := h.BatchGetUserOnlineStatus(userIDs)

	onlineUsers = make([]string, 0, len(userIDs))
	offlineUsers = make([]string, 0, len(userIDs))

	for _, userID := range userIDs {
		if onlineMap[userID] {
			onlineUsers = append(onlineUsers, userID)
		} else {
			offlineUsers = append(offlineUsers, userID)
		}
	}

	return onlineUsers, offlineUsers
}

// ============================================================================
// 高阶业务函数 - 用户类型筛选与批量操作
// ============================================================================

// FilterUsersByType 按用户类型过滤在线用户
//
// 参数:
//   - userType: 用户类型
//
// 返回:
//   - 该类型的所有在线用户ID列表
//
// 示例:
//
//	onlineAgents := hub.FilterUsersByType(UserTypeAgent)
//	onlineCustomers := hub.FilterUsersByType(UserTypeCustomer)
func (h *Hub) FilterUsersByType(userType UserType) []string {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	result := make([]string, 0)
	for userID, client := range h.clients {
		if client != nil && client.UserType == userType && client.Status == UserStatusOnline {
			result = append(result, userID)
		}
	}

	return result
}

// GetOnlineAgents 获取所有在线客服
// 快捷方法，等价于 FilterUsersByType(UserTypeAgent)
func (h *Hub) GetOnlineAgents() []string {
	return h.FilterUsersByType(UserTypeAgent)
}

// GetOnlineCustomers 获取所有在线客户
// 快捷方法，等价于 FilterUsersByType(UserTypeCustomer)
func (h *Hub) GetOnlineCustomers() []string {
	return h.FilterUsersByType(UserTypeCustomer)
}

// GetOnlineVisitors 获取所有在线访客
// 快捷方法，等价于 FilterUsersByType(UserTypeVisitor)
func (h *Hub) GetOnlineVisitors() []string {
	return h.FilterUsersByType(UserTypeVisitor)
}

// SelectLeastLoadedAgent 选择负载最低的在线客服
//
// 参数:
//   - getLoadFunc: 获取客服负载的函数（返回负载值，越小越优先）
//
// 返回:
//   - selectedAgentID: 选中的客服ID
//   - minLoad: 最小负载值
//   - found: 是否找到在线客服
//
// 示例:
//
//	agentID, load, found := hub.SelectLeastLoadedAgent(func(agentID string) int64 {
//	    return getAgentActiveSessionCount(agentID)
//	})
func (h *Hub) SelectLeastLoadedAgent(getLoadFunc func(string) int64) (selectedAgentID string, minLoad int64, found bool) {
	onlineAgents := h.GetOnlineAgents()
	if len(onlineAgents) == 0 {
		return "", 0, false
	}

	minLoad = int64(999999)
	for _, agentID := range onlineAgents {
		load := getLoadFunc(agentID)
		if load < minLoad {
			minLoad = load
			selectedAgentID = agentID
		}
	}

	return selectedAgentID, minLoad, selectedAgentID != ""
}

// ============================================================================
// 高阶业务函数 - 连接信息查询
// ============================================================================

// GetUserConnectionInfo 获取用户连接详细信息
//
// 返回:
//   - userID: 用户ID
//   - client: 客户端信息
//   - hasWebSocket: 是否有WebSocket连接
//   - hasSSE: 是否有SSE连接
//
// 示例:
//
//	userID, client, hasWS, hasSSE := hub.GetUserConnectionInfo(userID)
//	if hasWS {
//	    fmt.Println("用户使用WebSocket连接")
//	}
func (h *Hub) GetUserConnectionInfo(userID string) (client *Client, hasWebSocket bool, hasSSE bool) {
	h.mutex.RLock()
	client, hasWebSocket = h.userToClient[userID]
	h.mutex.RUnlock()

	h.sseMutex.RLock()
	_, hasSSE = h.sseClients[userID]
	h.sseMutex.RUnlock()

	return client, hasWebSocket, hasSSE
}

// ============================================================================
// 高阶业务函数 - 批量发送与重试
// ============================================================================

// SendToUsersWithRetry 批量发送消息并自动重试
//
// 参数:
//   - ctx: 上下文
//   - userIDs: 用户ID列表
//   - msg: 消息
//   - maxRetries: 最大重试次数
//   - offlineCallback: 离线用户回调
//
// 返回:
//   - result: 发送结果统计
//
// 示例:
//
//	result := hub.SendToUsersWithRetry(ctx, userIDs, msg, 3, func(userID string, msg *HubMessage) {
//	    saveOfflineMessage(userID, msg)
//	})
func (h *Hub) SendToUsersWithRetry(ctx context.Context, userIDs []string, msg *HubMessage,
	maxRetries int, offlineCallback func(string, *HubMessage)) *BroadcastResult {

	result := &BroadcastResult{
		Total:      len(userIDs),
		Success:    0,
		Offline:    0,
		Failed:     0,
		Errors:     make(map[string]error),
		OfflineIDs: make([]string, 0),
		FailedIDs:  make([]string, 0),
	}

	for _, userID := range userIDs {
		// 检查是否在线
		isOnline, err := h.IsUserOnline(userID)
		if err != nil || !isOnline {
			result.Offline++
			result.OfflineIDs = append(result.OfflineIDs, userID)
			if offlineCallback != nil {
				offlineCallback(userID, msg)
			}
			continue
		}

		// 发送消息（带重试）
		sendResult := h.SendToUserWithRetry(ctx, userID, msg)
		if sendResult.Success {
			result.Success++
		} else {
			result.Failed++
			result.FailedIDs = append(result.FailedIDs, userID)
			if sendResult.FinalError != nil {
				result.Errors[userID] = sendResult.FinalError
			}
		}
	}

	return result
}

// BroadcastToOnlineUsers 向所有在线用户广播消息
//
// 参数:
//   - ctx: 上下文
//   - msg: 消息
//   - userTypeFilter: 可选的用户类型过滤（nil表示所有类型）
//
// 返回:
//   - sentCount: 成功发送数量
//
// 示例:
//
//	向所有在线用户广播
//	count := hub.BroadcastToOnlineUsers(ctx, msg, nil)
//	向所有在线客服广播
//	agentType := UserTypeAgent
//	count := hub.BroadcastToOnlineUsers(ctx, msg, &agentType)
func (h *Hub) BroadcastToOnlineUsers(ctx context.Context, msg *HubMessage, userTypeFilter *UserType) int {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	sentCount := 0
	for _, client := range h.clients {
		// 类型过滤
		if userTypeFilter != nil && client.UserType != *userTypeFilter {
			continue
		}

		// 状态过滤：仅在线用户
		if client.Status != UserStatusOnline {
			continue
		}

		// 发送消息
		h.sendToClient(client, msg)
		sentCount++
	}

	return sentCount
}

// ExecuteOnOnlineUsers 对所有在线用户执行操作
//
// 参数:
//   - action: 对每个在线用户执行的操作函数
//   - userTypeFilter: 可选的用户类型过滤
//
// 返回:
//   - processedCount: 处理的用户数量
//
// 示例:
//
//	统计所有在线客服的会话数
//	agentType := UserTypeAgent
//	hub.ExecuteOnOnlineUsers(func(userID string, client *Client) {
//	    sessionCount := getAgentSessionCount(userID)
//	    fmt.Printf("Agent %s has %d sessions\n", userID, sessionCount)
//	}, &agentType)
func (h *Hub) ExecuteOnOnlineUsers(action func(userID string, client *Client), userTypeFilter *UserType) int {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	processedCount := 0
	for userID, client := range h.clients {
		// 类型过滤
		if userTypeFilter != nil && client.UserType != *userTypeFilter {
			continue
		}

		// 状态过滤：仅在线用户
		if client.Status != UserStatusOnline {
			continue
		}

		action(userID, client)
		processedCount++
	}

	return processedCount
}

// ============================================================================
// 高阶业务函数 - 在线状态聚合查询
// ============================================================================

// GetOnlineStatsSummary 获取在线状态汇总统计
//
// 返回:
//   - summary: 在线状态汇总
//
// 示例:
//
//	summary := hub.GetOnlineStatsSummary()
//	fmt.Printf("总在线: %d, 客服: %d, 客户: %d\n",
//	    summary.TotalOnline, summary.AgentCount, summary.CustomerCount)
func (h *Hub) GetOnlineStatsSummary() *OnlineStatsSummary {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	summary := &OnlineStatsSummary{
		TotalOnline:    0,
		AgentCount:     0,
		CustomerCount:  0,
		VisitorCount:   0,
		BotCount:       0,
		WebSocketCount: 0,
		SSECount:       0,
		UserTypeCounts: make(map[UserType]int),
	}

	for _, client := range h.clients {
		if client.Status != UserStatusOnline {
			continue
		}

		summary.TotalOnline++
		summary.UserTypeCounts[client.UserType]++

		switch client.UserType {
		case UserTypeAgent:
			summary.AgentCount++
		case UserTypeCustomer:
			summary.CustomerCount++
		case UserTypeVisitor:
			summary.VisitorCount++
		case UserTypeBot:
			summary.BotCount++
		}

		if client.Conn != nil {
			summary.WebSocketCount++
		}
	}

	// 统计 SSE 连接
	h.sseMutex.RLock()
	summary.SSECount = len(h.sseClients)
	h.sseMutex.RUnlock()

	return summary
}

// OnlineStatsSummary 在线状态汇总统计
type OnlineStatsSummary struct {
	TotalOnline    int              // 总在线用户数
	AgentCount     int              // 客服数量
	CustomerCount  int              // 客户数量
	VisitorCount   int              // 访客数量
	BotCount       int              // 机器人数量
	WebSocketCount int              // WebSocket连接数
	SSECount       int              // SSE连接数
	UserTypeCounts map[UserType]int // 按类型统计
}

// ============================================================================
// 高阶业务函数 - 条件筛选与查询
// ============================================================================

// FindUsersByPredicate 按条件查找在线用户
//
// 参数:
//   - predicate: 判断函数（返回true表示匹配）
//
// 返回:
//   - matchedUserIDs: 匹配的用户ID列表
//
// 示例:
//
//	查找所有空闲状态的客服
//	idleAgents := hub.FindUsersByPredicate(func(userID string, client *Client) bool {
//	    return client.UserType == UserTypeAgent && client.Status == UserStatusIdle
//	})
func (h *Hub) FindUsersByPredicate(predicate func(userID string, client *Client) bool) []string {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	result := make([]string, 0)
	for userID, client := range h.clients {
		if predicate(userID, client) {
			result = append(result, userID)
		}
	}

	return result
}

// GetClientsMapByUserType 获取指定类型的所有客户端（返回map）
//
// 参数:
//   - userType: 用户类型
//
// 返回:
//   - clients: 客户端映射表 map[userID]*Client
//
// 示例:
//
//	agents := hub.GetClientsMapByUserType(UserTypeAgent)
//	for userID, client := range agents {
//	    fmt.Printf("Agent: %s, Status: %s\n", userID, client.Status)
//	}
func (h *Hub) GetClientsMapByUserType(userType UserType) map[string]*Client {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	result := make(map[string]*Client)
	for _, client := range h.clients {
		if client.UserType == userType {
			result[client.UserID] = client
		}
	}

	return result
}

// ============================================================================
// 高阶业务函数 - 智能路由与负载均衡
// ============================================================================

// RouteMessageToAvailableAgent 智能路由消息到可用客服
//
// 参数:
//   - ctx: 上下文
//   - msg: 消息
//   - loadBalancer: 负载均衡器函数（选择最佳客服）
//
// 返回:
//   - selectedAgent: 选中的客服ID
//   - sent: 是否成功发送
//   - err: 错误信息
//
// 示例:
//
//	agent, sent, err := hub.RouteMessageToAvailableAgent(ctx, msg, func(agents []string) string {
//	    自定义负载均衡逻辑：轮询、随机、负载最低等
//	    return selectBestAgent(agents)
//	})
func (h *Hub) RouteMessageToAvailableAgent(ctx context.Context, msg *HubMessage,
	loadBalancer func([]string) string) (selectedAgent string, sent bool, err error) {

	// 获取所有在线客服
	onlineAgents := h.GetOnlineAgents()
	if len(onlineAgents) == 0 {
		return "", false, errors.New("no online agents available")
	}

	// 使用负载均衡器选择客服
	selectedAgent = loadBalancer(onlineAgents)
	if selectedAgent == "" {
		return "", false, errors.New("load balancer returned empty agent")
	}

	// 发送消息
	result := h.SendToUserWithRetry(ctx, selectedAgent, msg)
	return selectedAgent, result.Success, result.FinalError
}

// DistributeMessagesToAgents 按负载分配消息给客服团队
//
// 参数:
//   - ctx: 上下文
//   - messages: 消息列表
//   - getAgentLoad: 获取客服负载的函数
//
// 返回:
//   - distribution: 分配结果 map[agentID][]messageID
//
// 示例:
//
//	distribution := hub.DistributeMessagesToAgents(ctx, messages, func(agentID string) int64 {
//	    return getAgentActiveSessionCount(agentID)
//	})
func (h *Hub) DistributeMessagesToAgents(ctx context.Context, messages []*HubMessage,
	getAgentLoad func(string) int64) map[string][]*HubMessage {

	onlineAgents := h.GetOnlineAgents()
	if len(onlineAgents) == 0 {
		return nil
	}

	// 初始化分配结果
	distribution := make(map[string][]*HubMessage)
	for _, agentID := range onlineAgents {
		distribution[agentID] = make([]*HubMessage, 0)
	}

	// 按负载排序客服
	type agentWithLoad struct {
		agentID string
		load    int64
	}
	agentLoads := make([]agentWithLoad, len(onlineAgents))
	for i, agentID := range onlineAgents {
		agentLoads[i] = agentWithLoad{
			agentID: agentID,
			load:    getAgentLoad(agentID),
		}
	}

	// 轮询分配消息（按负载从低到高）
	for _, msg := range messages {
		// 找到负载最低的客服
		minIdx := 0
		for i := 1; i < len(agentLoads); i++ {
			if agentLoads[i].load < agentLoads[minIdx].load {
				minIdx = i
			}
		}

		// 分配消息
		agentID := agentLoads[minIdx].agentID
		distribution[agentID] = append(distribution[agentID], msg)
		agentLoads[minIdx].load++ // 增加负载
	}

	return distribution
}

// ============================================================================
// 高阶业务函数 - 会话与分组管理
// ============================================================================

// BroadcastToSessionWithExclusion 向会话广播消息（排除指定用户）
//
// 参数:
//   - ctx: 上下文
//   - memberIDs: 会话成员ID列表
//   - msg: 消息
//   - excludeUserIDs: 要排除的用户ID列表
//   - offlineCallback: 离线回调
//
// 返回:
//   - result: 广播结果
//
// 示例:
//
//	result := hub.BroadcastToSessionWithExclusion(ctx, members, msg,
//	    []string{senderID, botID}, offlineCallback)
func (h *Hub) BroadcastToSessionWithExclusion(ctx context.Context, memberIDs []string,
	msg *HubMessage, excludeUserIDs []string, offlineCallback func(string, *HubMessage)) *BroadcastResult {

	// 构建排除集合
	excludeSet := make(map[string]bool, len(excludeUserIDs))
	for _, uid := range excludeUserIDs {
		excludeSet[uid] = true
	}

	// 过滤成员列表
	filteredMembers := make([]string, 0, len(memberIDs))
	for _, uid := range memberIDs {
		if !excludeSet[uid] {
			filteredMembers = append(filteredMembers, uid)
		}
	}

	// 使用 BroadcastToUsers 批量发送
	return h.BroadcastToUsers(ctx, filteredMembers, msg, offlineCallback)
}

// MulticastToUserTypes 向多种用户类型组播消息
//
// 参数:
//   - ctx: 上下文
//   - msg: 消息
//   - userTypes: 用户类型列表
//
// 返回:
//   - sentCount: 成功发送数量
//
// 示例:
//
//	向所有客服和机器人发送系统通知
//	count := hub.MulticastToUserTypes(ctx, sysMsg, []UserType{UserTypeAgent, UserTypeBot})
func (h *Hub) MulticastToUserTypes(ctx context.Context, msg *HubMessage, userTypes []UserType) int {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	// 构建用户类型集合
	typeSet := make(map[UserType]bool, len(userTypes))
	for _, ut := range userTypes {
		typeSet[ut] = true
	}

	sentCount := 0
	for _, client := range h.clients {
		if client.Status != UserStatusOnline {
			continue
		}

		if typeSet[client.UserType] {
			h.sendToClient(client, msg)
			sentCount++
		}
	}

	return sentCount
}

// ============================================================================
// 高阶业务函数 - 批量状态操作
// ============================================================================

// UpdateUserStatus 更新用户状态（如：在线→忙碌、空闲→离开等）
//
// 参数:
//   - userID: 用户ID
//   - newStatus: 新状态
//
// 返回:
//   - updated: 是否成功更新
//   - oldStatus: 旧状态
//
// 示例:
//
//	updated, oldStatus := hub.UpdateUserStatus(agentID, UserStatusBusy)
//	if updated {
//	    fmt.Printf("Agent %s: %s -> %s\n", agentID, oldStatus, UserStatusBusy)
//	}
func (h *Hub) UpdateUserStatus(userID string, newStatus UserStatus) (updated bool, oldStatus UserStatus) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	client, exists := h.clients[userID]
	if !exists || client == nil {
		return false, UserStatusOffline
	}

	oldStatus = client.Status
	client.Status = newStatus
	client.LastSeen = time.Now()

	return true, oldStatus
}

// BatchUpdateUserStatus 批量更新用户状态
//
// 参数:
//   - userIDs: 用户ID列表
//   - newStatus: 新状态
//
// 返回:
//   - updatedCount: 成功更新的数量
//
// 示例:
//
//	将所有在线客服设置为忙碌
//	agents := hub.GetOnlineAgents()
//	count := hub.BatchUpdateUserStatus(agents, UserStatusBusy)
func (h *Hub) BatchUpdateUserStatus(userIDs []string, newStatus UserStatus) int {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	updatedCount := 0
	now := time.Now()

	for _, userID := range userIDs {
		if client, exists := h.clients[userID]; exists && client != nil {
			client.Status = newStatus
			client.LastSeen = now
			updatedCount++
		}
	}

	return updatedCount
}

// GetUsersWithStatus 获取指定状态的所有用户
//
// 参数:
//   - status: 用户状态
//   - userTypeFilter: 可选的用户类型过滤
//
// 返回:
//   - userIDs: 用户ID列表
//
// 示例:
//
//	获取所有忙碌的客服
//	agentType := UserTypeAgent
//	busyAgents := hub.GetUsersWithStatus(UserStatusBusy, &agentType)
func (h *Hub) GetUsersWithStatus(status UserStatus, userTypeFilter *UserType) []string {
	h.mutex.RLock()
	defer h.mutex.RUnlock()

	result := make([]string, 0)
	for userID, client := range h.clients {
		if client.Status != status {
			continue
		}

		if userTypeFilter != nil && client.UserType != *userTypeFilter {
			continue
		}

		result = append(result, userID)
	}

	return result
}

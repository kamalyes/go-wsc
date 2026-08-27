/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-01-30 01:20:15
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-01-30 11:20:15
 * @FilePath: \go-wsc\hub\connection.go
 * @Description: Hub 连接管理
 *   - 主动断开用户/客户端连接
 *   - 踢出客户端的断开处理
 *   - 客户端状态重置
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"runtime/debug"

	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 主动断开连接
// ============================================================================

// DisconnectUser 主动断开指定用户的所有连接（按 ctx 路由信封 appID+namespace 隔离）
// 使用 ForEachUserClientFiltered 持读锁收集匹配信封的客户端，替代 GetUserClientsMapWithLock 锁外遍历的数据竞争
func (h *Hub) DisconnectUser(ctx context.Context, userID string, reason string) error {
	appID, ns := routing.AppIDFromContext(ctx), routing.NamespaceFromContext(ctx)
	if !h.shardedRegistry.HasUser(userID, appID, ns) {
		return errorx.NewError(ErrTypeUserNotFound, userID)
	}

	// 持读锁零拷贝收集匹配信封的客户端
	var clients []*Client
	h.shardedRegistry.ForEachUserClientFiltered(userID, appID, ns, nil, func(_ string, client *Client) bool {
		clients = append(clients, client)
		return true
	})

	if len(clients) == 0 {
		return errorx.NewError(ErrTypeUserNotFound, userID)
	}

	// 并行断开所有客户端连接
	syncx.ParallelForEachSlice(clients, func(_ int, client *Client) {
		if client.Conn != nil {
			client.Conn.Close()
		}
	})
	return nil
}

// DisconnectClient 主动断开特定客户端
func (h *Hub) DisconnectClient(clientID string, reason string) error {
	client, exists := h.GetClientByIDWithLock(clientID)

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	if client.Conn != nil {
		client.Conn.Close()
	}
	return nil
}

// disconnectKickedClient 断开被踢出的客户端
// 通过 syncx.Go 异步执行断开回调，避免阻塞踢出流程
func (h *Hub) disconnectKickedClient(ctx context.Context, client *Client, reason string) {
	// 调用断开回调
	if h.clientDisconnectCallback != nil {
		syncx.Go().
			OnPanic(func(r any) {
				h.logger.ErrorContextKV(ctx, "踢出用户断开回调 panic", "panic", r, "stack", string(debug.Stack()), "client_id", client.ID)
			}).
			OnError(func(err error) {
				h.logger.ErrorContextKV(ctx, "踢出用户时断开回调执行失败",
					"client_id", client.ID,
					"user_id", client.UserID,
					"error", err,
				)
				if h.errorCallback != nil {
					_ = h.errorCallback(ctx, err, ErrorSeverityWarning)
				}
			}).
			ExecWithContext(func(execCtx context.Context) error {
				return h.clientDisconnectCallback(execCtx, client, DisconnectReasonKickOut)
			})
	}

	// 关闭连接
	h.logger.InfoContextKV(ctx, "关闭被踢用户的连接",
		"client_id", client.ID,
		"user_id", client.UserID,
		"reason", reason,
	)
	if client.Conn != nil {
		client.Conn.Close()
	}

	// 从 Hub 中移除
	h.Unregister(client)
}

// ============================================================================
// 客户端状态管理
// ============================================================================

// ResetClientStatus 重置客户端状态
func (h *Hub) ResetClientStatus(clientID string, status UserStatus) error {
	client, exists := h.GetClientByIDWithLock(clientID)

	if !exists {
		return errorx.NewError(ErrTypeClientNotFound, "client_id: %s", clientID)
	}

	client.SetStatus(status)
	return nil
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-02 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-01-02 15:19:53
 * @FilePath: \go-wsc\hub_message_status_update_test.go
 * @Description: Hub消息状态更新测试
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"context"
	"encoding/json"
	"testing"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHubUpdateMessageSendStatusRecordNotExist 测试记录不存在时的处理
func TestHubUpdateMessageSendStatusRecordNotExist(t *testing.T) {
	db := GetTestDBWithMigration(t, &MessageSendRecord{})
	ctx := context.Background()
	repo := NewMessageRecordRepository(db, nil, NewDefaultWSCLogger())
	hub := NewHub(wscconfig.Default())
	hub.SetMessageRecordRepository(repo)

	go hub.Run()
	hub.WaitForStart()
	defer hub.SafeShutdown()

	msg := createTestHubMessage(MessageTypeText)
	_, err := json.Marshal(msg)
	require.NoError(t, err)

	err = repo.UpdateStatus(ctx, msg.MessageID, MessageSendStatusSuccess, "", "")
	require.NoError(t, err) // 记录不存在时静默返回

	_, err = repo.FindByMessageID(ctx, msg.MessageID)
	assert.Error(t, err)
}

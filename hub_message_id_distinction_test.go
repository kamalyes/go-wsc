/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-12-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-12-20 12:00:00
 * @FilePath: \go-wsc\hub_message_id_distinction_test.go
 * @Description: Hub ID 和 MessageID 区分测试 - 确保不会混用
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */
package wsc

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMessageRecordIDFields 测试 MessageSendRecord 的 ID 字段正确性
func TestMessageRecordIDFields(t *testing.T) {
	businessMsgID := "biz_msg_12345"
	hubInternalID := "msg_node01_67890"

	msg := &HubMessage{
		ID:          hubInternalID, // Hub 内部ID
		MessageID:   businessMsgID, // 业务消息ID
		Sender:      "user-a",
		Receiver:    "user-b",
		MessageType: MessageTypeText,
		Content:     "test",
	}

	record := &MessageSendRecord{}
	err := record.SetMessage(msg)
	require.NoError(t, err)

	// 🔥 验证 SetMessage 正确分离两个ID
	assert.Equal(t, businessMsgID, record.MessageID, "MessageID 字段应该存储业务消息ID")
	assert.Equal(t, hubInternalID, record.HubID, "HubID 字段应该存储 Hub 内部ID")
	assert.NotEqual(t, record.MessageID, record.HubID, "两个ID字段的值必须不同")

	// 🔥 验证 GetMessage 正确还原两个ID
	retrieved, err := record.GetMessage()
	require.NoError(t, err)
	assert.Equal(t, hubInternalID, retrieved.ID, "还原的消息应该有正确的 Hub ID")
	assert.Equal(t, businessMsgID, retrieved.MessageID, "还原的消息应该有正确的业务消息ID")
}

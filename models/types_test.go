/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 00:00:00
 * @FilePath: \go-wsc\models\types_test.go
 * @Description: DistributedMessage 单元测试（日志安全取值）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDistributedMessage_LogMessageID 验证日志取 message_id 的 nil 安全
// 历史 bug：控制消息（如 client_reclaim）无 Message 体，发布路径日志
// 无条件调用 dispatch.Message.GetMessageID() 触发 nil pointer panic，
// 中断 handleRegister 导致连接只完成 Upgrade 却无读写协程（90 秒幽灵连接）
func TestDistributedMessage_LogMessageID(t *testing.T) {
	tests := []struct {
		name string
		dm   *DistributedMessage
		want string
	}{
		{
			name: "nil 消息体（控制消息）返回空串不 panic",
			dm:   &DistributedMessage{Type: OperationTypeClientReclaim, TargetID: "client-1"},
			want: "",
		},
		{
			name: "nil receiver 返回空串不 panic",
			dm:   nil,
			want: "",
		},
		{
			name: "正常消息体返回 message_id",
			dm:   &DistributedMessage{Type: OperationTypeSendMessage, Message: &HubMessage{MessageID: "m-123"}},
			want: "m-123",
		},
		{
			name: "消息体存在但 message_id 为空",
			dm:   &DistributedMessage{Type: OperationTypeSendMessage, Message: &HubMessage{}},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				assert.Equal(t, tt.want, tt.dm.LogMessageID())
			})
		})
	}
}

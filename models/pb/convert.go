/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-01-02 12:20:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-06 20:25:16
 * @FilePath: \go-wsc\models\pb\convert.go
 * @Description: HubMessage 与 protobuf 之间的转换函数
 *   基于 go-pbmo 自动转换（struct↔struct）+ google.golang.org/protobuf（二进制序列化）
 *   相比 JSON 体积减少 50%+，序列化速度提升 3-5x
 *
 * 字段映射说明（ID 后缀差异）：
 *   - HubMessage.ID          ↔ HubMessageProto.Id
 *   - HubMessage.MessageID   ↔ HubMessageProto.MessageId
 *   - HubMessage.SessionID   ↔ HubMessageProto.SessionId
 *   - HubMessage.ReplyToMsgID ↔ HubMessageProto.ReplyToMsgId
 *   - DistributedMessage.NodeID ↔ DistributedMessageProto.NodeId
 */

package wscpb

import (
	"fmt"

	"github.com/kamalyes/go-pbmo"
	"github.com/kamalyes/go-toolbox/pkg/errorx"
	"github.com/kamalyes/go-wsc/models"
	"google.golang.org/protobuf/proto"
)

// init 注册 Model↔Proto 转换器
// go-pbmo 自动处理：
//   - map[string]interface{} ↔ *structpb.Struct（fieldMapStruct）
//   - 自定义 string 类型（MessageType 等）↔ string（fieldAssignable/fieldConvertible）
//   - time.Time ↔ *timestamppb.Timestamp（自动开启）
func init() {
	// HubMessage ↔ HubMessageProto
	// 字段名差异通过 WithFieldMapping 显式映射
	pbmo.RegisterWith[models.HubMessage, HubMessageProto]().
		WithFieldMapping("ID", "Id").
		WithFieldMapping("MessageID", "MessageId").
		WithFieldMapping("SessionID", "SessionId").
		WithFieldMapping("ReplyToMsgID", "ReplyToMsgId")

	// DistributedMessage ↔ DistributedMessageProto
	// 嵌套的 Message 字段会自动递归转换（已注册 HubMessage 转换器）
	pbmo.RegisterWith[models.DistributedMessage, DistributedMessageProto]().
		WithFieldMapping("NodeID", "NodeId")
}

// ============================================================================
// 二进制序列化/反序列化（pbmo 转换 + proto.Marshal 二进制编码）
// ============================================================================

// MarshalDistributedMessage 将 DistributedMessage 序列化为 protobuf 二进制
// 相比 JSON 体积减少 50%+，序列化速度提升 3-5x
func MarshalDistributedMessage(dm *models.DistributedMessage) ([]byte, error) {
	if dm == nil {
		return nil, errorx.WrapError("distributed message is nil")
	}

	// Model → Proto（go-pbmo 自动转换）
	pbMsg, err := pbmo.ToPB[models.DistributedMessage, DistributedMessageProto](dm)
	if err != nil {
		return nil, errorx.WrapError(fmt.Sprintf("failed to convert distributed message: %v", err))
	}

	// Proto → 二进制（protobuf wire format）
	return proto.Marshal(pbMsg)
}

// UnmarshalDistributedMessage 从 protobuf 二进制反序列化 DistributedMessage
func UnmarshalDistributedMessage(data []byte) (*models.DistributedMessage, error) {
	if len(data) == 0 {
		return nil, errorx.WrapError("data is empty")
	}

	// 二进制 → Proto
	pbMsg := &DistributedMessageProto{}
	if err := proto.Unmarshal(data, pbMsg); err != nil {
		return nil, errorx.WrapError(fmt.Sprintf("failed to unmarshal distributed message: %v", err))
	}

	// Proto → Model（go-pbmo 自动转换）
	return pbmo.FromPB[DistributedMessageProto, models.DistributedMessage](pbMsg)
}

// MarshalHubMessage 将 HubMessage 序列化为 protobuf 二进制
func MarshalHubMessage(m *models.HubMessage) ([]byte, error) {
	if m == nil {
		return nil, errorx.WrapError("hub message is nil")
	}

	pbMsg, err := pbmo.ToPB[models.HubMessage, HubMessageProto](m)
	if err != nil {
		return nil, errorx.WrapError(fmt.Sprintf("failed to convert hub message: %v", err))
	}

	return proto.Marshal(pbMsg)
}

// UnmarshalHubMessage 从 protobuf 二进制反序列化 HubMessage
func UnmarshalHubMessage(data []byte) (*models.HubMessage, error) {
	if len(data) == 0 {
		return nil, errorx.WrapError("data is empty")
	}

	pbMsg := &HubMessageProto{}
	if err := proto.Unmarshal(data, pbMsg); err != nil {
		return nil, errorx.WrapError(fmt.Sprintf("failed to unmarshal hub message: %v", err))
	}

	return pbmo.FromPB[HubMessageProto, models.HubMessage](pbMsg)
}

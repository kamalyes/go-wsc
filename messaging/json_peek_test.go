/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 10:35:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 09:38:02
 * @FilePath: \go-wsc\messaging\json_peek_test.go
 * @Description: JSON 顶层字段探测测试（含对抗性输入：嵌套同名键、字符串内伪键、转义）
 */
package messaging

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJSONPeekTopLevelString(t *testing.T) {
	cases := []struct {
		name  string
		data  string
		key   string
		want  string
		found bool
	}{
		{"首字段命中", `{"message_type":"ping","message_id":"m1"}`, "message_type", "ping", true},
		{"末字段命中", `{"message_id":"m1","message_type":"ack"}`, "message_type", "ack", true},
		{"中间字段命中", `{"a":1,"message_type":"heartbeat","b":2}`, "message_type", "heartbeat", true},
		{"含空白", `{ "message_type" : "ping" }`, "message_type", "ping", true},
		{"换行缩进", "{\n  \"message_type\": \"ping\"\n}", "message_type", "ping", true},
		{"键不存在", `{"other":"x"}`, "message_type", "", false},
		{"空对象", `{}`, "message_type", "", false},

		// —— 对抗性：必须判定未命中，否则会把数据消息误路由为控制消息 ——
		{"嵌套对象内同名键", `{"data":{"message_type":"ping"},"content":"hi"}`, "message_type", "", false},
		{"数组内同名键", `{"items":[{"message_type":"ping"}]}`, "message_type", "", false},
		{"字符串值内伪键", `{"content":"\"message_type\":\"ping\"","x":1}`, "message_type", "", false},
		{"content 含键名文本", `{"content":"message_type is ping"}`, "message_type", "", false},
		{"深层嵌套", `{"a":{"b":{"message_type":"ping"}}}`, "message_type", "", false},

		// —— 非字符串值：交给完整解析 ——
		{"值为数字", `{"message_type":3}`, "message_type", "", false},
		{"值为 null", `{"message_type":null}`, "message_type", "", false},
		{"值为对象", `{"message_type":{"x":1}}`, "message_type", "", false},

		// —— 结构异常：一律未命中 ——
		{"截断", `{"message_type":"pin`, "message_type", "", false},
		{"非对象", `["message_type","ping"]`, "message_type", "", false},
		{"空输入", ``, "message_type", "", false},
		{"纯字符串", `"message_type"`, "message_type", "", false},

		// —— 转义值交给完整解析 ——
		{"值含转义引号", `{"message_type":"a\"b"}`, "message_type", "", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, found := jsonPeekTopLevelString([]byte(c.data), c.key)
			assert.Equal(t, c.found, found, "命中判定不符")
			assert.Equal(t, c.want, got, "取值不符")
		})
	}

	// 含 \u 转义的值：按字节显式构造，避免字面量在任何环节被预先解码
	t.Run("值含unicode转义", func(t *testing.T) {
		escaped := []byte(`{"message_type":"pi`)
		escaped = append(escaped, '\\', 'u', '0', '0', '6', 'e')
		escaped = append(escaped, []byte(`g"}`)...)
		require.Contains(t, escaped, byte('\\'), "输入须真的含反斜杠转义字节")

		got, found := jsonPeekTopLevelString(escaped, "message_type")
		assert.False(t, found, "含转义的值应退化为未命中，交给完整解析")
		assert.Equal(t, "", got)
	})
}

// TestJSONPeek_AgreesWithUnmarshal 探测命中时必须与 encoding/json 解析结果一致
//
// 这是防误判的核心约束：命中即必须正确，否则会错误短路控制消息分支
func TestJSONPeek_AgreesWithUnmarshal(t *testing.T) {
	docs := []string{
		`{"message_type":"ping"}`,
		`{"message_id":"m1","message_type":"ack","content":"x"}`,
		`{"message_type":"text","data":{"message_type":"ping"}}`,
		`{"content":"message_type","message_type":"text"}`,
		`{ "message_type" :  "heartbeat" , "x": [1,2,3] }`,
		`{"a":{"b":[{"c":"d"}]},"message_type":"text"}`,
	}
	for i, doc := range docs {
		t.Run(fmt.Sprintf("doc%d", i), func(t *testing.T) {
			var ref struct {
				MessageType string `json:"message_type"`
			}
			require.NoError(t, json.Unmarshal([]byte(doc), &ref), "测试文档本身须为合法 JSON")

			got, found := jsonPeekTopLevelString([]byte(doc), "message_type")
			if found {
				assert.Equal(t, ref.MessageType, got,
					"探测命中但与 encoding/json 不一致——会导致消息误路由")
			}
			// 未命中是允许的（保守退化到完整解析），故不断言 found 为真
		})
	}
}

func BenchmarkJSONPeek_vs_ProbeUnmarshal(b *testing.B) {
	// 典型数据消息：探测目标键在前部，但文档整体较大
	doc := []byte(`{"message_type":"text","message_id":"msg-000123","sender":"u-1","receiver":"u-2","content":"hello world this is a typical chat payload","create_at":"2026-10-27T05:00:00Z","app_id":"app1","namespace":"ns1","group_ids":["g1","g2"],"priority":1}`)

	b.Run("peek", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, ok := jsonPeekTopLevelString(doc, "message_type"); !ok {
				b.Fatal("未命中")
			}
		}
	})

	b.Run("probe_unmarshal", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var probe struct {
				MessageType string `json:"message_type"`
				MessageID   string `json:"message_id"`
			}
			if err := json.Unmarshal(doc, &probe); err != nil {
				b.Fatal(err)
			}
		}
	})
}

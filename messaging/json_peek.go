/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-09-22 10:35:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-26 09:38:02
 * @FilePath: \go-wsc\messaging\json_peek.go
 * @Description: JSON 顶层字段就地探测（入站控制类消息快路径，免完整反序列化）
 *
 * 动机：handleTextMessage 原先用一个 2 字段的探测结构体做 json.Unmarshal 来识别
 * 心跳/ACK，命中即返回。但 json.Unmarshal 即使只映射 2 个字段也要扫描并跳过整个文档，
 * 于是**非控制类消息（数据消息，占数据面绝大多数）要付两次完整解析**：
 * 一次探测 + 一次真正的 HubMessage 反序列化。
 *
 * 本文件提供只读的顶层字符串字段探测：单次线性扫描，零分配，不构造中间对象。
 *
 * 正确性优先于速度——探测只在**无歧义**时返回命中：
 *   - 只认深度 0（最外层对象）的键，嵌套对象里的同名键不算
 *   - 字符串内部的字节不参与键匹配（含转义处理），避免 content 里出现
 *     "message_type" 字样时误判
 *   - 任何结构异常（截断、非对象、非字符串值）一律返回未命中
 *
 * 误判方向的安全性：返回"未命中"是安全的——调用方会落到完整反序列化，
 * 且完整解析后仍会复查 MessageType 作兜底（见 handleTextMessage 的类型 switch）。
 * 返回"错误的命中"才是危险方向，故上述三条约束缺一不可。
 */
package messaging

// jsonPeekTopLevelString 就地探测 JSON 顶层对象中 key 对应的字符串值
//
// 返回 (值, true) 仅当：data 是对象、key 出现在深度 0、且其值是字符串字面量。
// 值内含转义序列时返回 ("", false)——交给完整解析处理，不在此实现反转义。
// 零分配：返回值是 data 的子切片转换，仅在命中时发生一次 string 转换。
func jsonPeekTopLevelString(data []byte, key string) (string, bool) {
	i, n := 0, len(data)

	// 跳过前导空白，要求最外层是对象
	for i < n && isJSONSpace(data[i]) {
		i++
	}
	if i >= n || data[i] != '{' {
		return "", false
	}
	i++ // 越过 '{'

	depth := 0 // 相对最外层对象内部的嵌套深度
	for i < n {
		c := data[i]

		switch {
		case isJSONSpace(c) || c == ',' || c == ':':
			i++
			continue

		case c == '{' || c == '[':
			depth++
			i++
			continue

		case c == '}' || c == ']':
			if depth == 0 {
				return "", false // 最外层对象结束，未找到
			}
			depth--
			i++
			continue

		case c == '"':
			start, end, ok := scanJSONString(data, i)
			if !ok {
				return "", false
			}
			// 仅深度 0 的字符串才可能是目标键
			if depth == 0 && string(data[start:end]) == key {
				return readJSONStringValueAfterKey(data, end+1)
			}
			i = end + 1
			continue

		default:
			// 数字/true/false/null 等标量：跳到下一个结构性字符
			i++
			continue
		}
	}
	return "", false
}

// readJSONStringValueAfterKey 读取键之后的字符串值；pos 指向键收尾引号之后
func readJSONStringValueAfterKey(data []byte, pos int) (string, bool) {
	n := len(data)
	for pos < n && isJSONSpace(data[pos]) {
		pos++
	}
	if pos >= n || data[pos] != ':' {
		return "", false
	}
	pos++
	for pos < n && isJSONSpace(data[pos]) {
		pos++
	}
	if pos >= n || data[pos] != '"' {
		return "", false // 值不是字符串（数字/对象/null）——交给完整解析
	}
	start, end, ok := scanJSONString(data, pos)
	if !ok {
		return "", false
	}
	// 含转义的值不在此处理，避免实现一套反转义
	for j := start; j < end; j++ {
		if data[j] == '\\' {
			return "", false
		}
	}
	return string(data[start:end]), true
}

// scanJSONString 扫描以 data[pos]=='"' 起始的字符串字面量
// 返回内容区间 [start, end)（不含两侧引号）与是否成功；正确跳过 \" 转义
func scanJSONString(data []byte, pos int) (start, end int, ok bool) {
	n := len(data)
	if pos >= n || data[pos] != '"' {
		return 0, 0, false
	}
	start = pos + 1
	for i := start; i < n; i++ {
		switch data[i] {
		case '\\':
			i++ // 跳过被转义字节（循环自增再 +1）
		case '"':
			return start, i, true
		}
	}
	return 0, 0, false // 未闭合
}

// isJSONSpace JSON 结构性空白
func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

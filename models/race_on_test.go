//go:build race

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-05 10:06:18
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-05 10:06:18
 * @FilePath: \go-wsc\models\race_on_test.go
 * @Description: race 探测构建标记——testing 框架无运行期探测 API，零分配断言
 *   仅在非 race 构建下有效，经 build tag 编译期区分（与 race_off_test.go 成对）
 */

package models

// raceEnabled race 探测构建置真：探测为每次锁操作注入记账分配，
// testing.AllocsPerRun 的零分配断言在该构建下不可信，相关用例跳过
const raceEnabled = true

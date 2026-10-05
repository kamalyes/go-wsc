//go:build !race

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-05 10:06:18
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-05 10:06:18
 * @FilePath: \go-wsc\models\race_off_test.go
 * @Description: 非 race 构建标记——与 race_on_test.go 成对，供零分配断言
 *   在 race 探测构建（CI 全量）下自动跳过
 */

package models

// raceEnabled 非 race 构建置假：testing.AllocsPerRun 计量可信
const raceEnabled = false

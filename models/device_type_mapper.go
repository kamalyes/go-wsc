/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-02-10 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-02-10 00:00:00
 * @FilePath: \go-wsc\models\device_type_mapper.go
 * @Description: 设备类型映射器 - 将 go-toolbox 的 DeviceType 映射到 go-wsc 的 ClientType
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package models

// DeviceType 常量（来自 go-toolbox/pkg/useragent）
const (
	DeviceTypeBot     = "bot"     // 机器人
	DeviceTypeTablet  = "tablet"  // 平板
	DeviceTypeMobile  = "mobile"  // 移动设备
	DeviceTypeDesktop = "desktop" // 桌面设备
	DeviceTypeUnknown = "unknown" // 未知设备
)

// MapDeviceTypeToClientType 将 go-toolbox 的 DeviceType 映射到 go-wsc 的 ClientType
//
// 映射规则：
//   - bot -> api (机器人视为 API 客户端)
//   - mobile -> mobile (移动设备)
//   - tablet -> mobile (平板视为移动设备)
//   - desktop -> web (桌面设备视为 Web 客户端)
//   - unknown -> web (未知设备默认为 Web 客户端)
func MapDeviceTypeToClientType(deviceType string) ClientType {
	switch deviceType {
	case DeviceTypeBot:
		return ClientTypeAPI
	case DeviceTypeMobile, DeviceTypeTablet:
		return ClientTypeMobile
	case DeviceTypeDesktop:
		return ClientTypeWeb
	case DeviceTypeUnknown:
		return ClientTypeWeb
	default:
		return ClientTypeWeb
	}
}

// MapClientTypeToDeviceType 将 go-wsc 的 ClientType 反向映射到 go-toolbox 的 DeviceType
//
// 映射规则：
//   - api -> bot
//   - mobile -> mobile
//   - desktop -> desktop
//   - web -> desktop
func MapClientTypeToDeviceType(clientType ClientType) string {
	switch clientType {
	case ClientTypeAPI:
		return DeviceTypeBot
	case ClientTypeMobile:
		return DeviceTypeMobile
	case ClientTypeDesktop:
		return DeviceTypeDesktop
	case ClientTypeWeb:
		return DeviceTypeDesktop
	default:
		return DeviceTypeUnknown
	}
}

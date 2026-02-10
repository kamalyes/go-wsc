# 设备类型映射说明

## 概述

本文档说明 `go-toolbox` 的 `DeviceType` 和 `go-wsc` 的 `ClientType` 之间的映射关系

## 背景

- **go-toolbox/useragent**: 从 User-Agent 解析出设备类型（更细粒度）
- **go-wsc**: 业务层面的客户端类型（更粗粒度）

## 映射规则

### DeviceType → ClientType

| DeviceType (go-toolbox) | ClientType (go-wsc) | 说明 |
|------------------------|---------------------|------|
| `bot`                  | `api`               | 机器人视为 API 客户端 |
| `mobile`               | `mobile`            | 移动设备 |
| `tablet`               | `mobile`            | 平板视为移动设备 |
| `desktop`              | `web`               | 桌面设备视为 Web 客户端 |
| `unknown`              | `web`               | 未知设备默认为 Web 客户端 |
| 其他                    | `web`               | 其他类型默认为 Web 客户端 |

### ClientType → DeviceType (反向映射)

| ClientType (go-wsc) | DeviceType (go-toolbox) | 说明 |
|--------------------|------------------------|------|
| `api`              | `bot`                  | API 客户端映射为机器人 |
| `mobile`           | `mobile`               | 移动设备 |
| `desktop`          | `desktop`              | 桌面设备 |
| `web`              | `desktop`              | Web 客户端映射为桌面设备 |
| 其他                | `unknown`              | 其他类型映射为未知设备 |

## 使用示例

```go
// 从 User-Agent 解析的设备类型映射到客户端类型
deviceType := "mobile" // 来自 go-toolbox/useragent
clientType := models.MapDeviceTypeToClientType(deviceType)
// clientType = ClientTypeMobile

// 客户端类型反向映射到设备类型
clientType := models.ClientTypeWeb
deviceType := models.MapClientTypeToDeviceType(clientType)
// deviceType = "desktop"
```

## 注意事项

1. **tablet 映射为 mobile**: 平板设备在业务层面视为移动设备
2. **web 和 desktop 的区别**: 
   - `ClientTypeWeb` 表示通过浏览器访问
   - `ClientTypeDesktop` 表示桌面客户端应用
   - 两者都映射到 `DeviceTypeDesktop`
3. **双向映射不完全对称**: `ClientTypeDesktop` → `desktop` → `ClientTypeWeb`

## 相关文件

- `go-wsc/models/device_type_mapper.go` - 映射函数实现
- `go-wsc/models/device_type_mapper_test.go` - 单元测试
- `go-wsc/models/enums.go` - ClientType 枚举定义
- `go-toolbox/pkg/useragent/consts.go` - DeviceType 常量定义

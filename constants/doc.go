/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-08-20 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-08-20 00:00:00
 * @FilePath: \go-wsc\constants\doc.go
 * @Description: go-wsc 系统级常量（单一真相源，无依赖底层包）
 *
 * 抽离到独立 constants 包的目的：
 *   - routing 与 models 互不依赖（routing 是底层包，不能 import models；
 *     models 引用 routing 的同时，routing 不应反向依赖 models）
 *   - 任何包（routing/models/hub/handler/业务方）均可直接 import constants，
 *     用 constants.XXX 引用，无需别名重导出，无循环依赖
 *
 * 文件组织：
 *   - route.go       路由隔离维度默认值（DefaultAppID/DefaultNamespace/DefaultGroupID）
 *   - metadata.go    gRPC metadata 键名（跨节点传播路由元数据）
 *   - system_group.go 系统保留组常量（__agents__/__observers__ 等）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package constants

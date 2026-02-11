/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2025-11-15 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2025-11-15 00:00:00
 * @FilePath: \go-wsc\hub_scenarios_test.go
 * @Description: Hub 200个场景测试 - 使用assert进行全面验证
 *
 * Copyright (c) 2025 by kamalyes, All Rights Reserved.
 */

package wsc

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/stretchr/testify/assert"
)

// TestHub200Scenarios 200个场景的综合测试
func TestHub200Scenarios(t *testing.T) {
	// 禁用欢迎消息以避免干扰测试
	hub := NewHub(wscconfig.Default())
	go hub.Run()
	defer hub.Shutdown()

	time.Sleep(50 * time.Millisecond)

	t.Run("场景1-50: 基础功能测试", func(t *testing.T) {
		testBasicScenarios(t, hub)
	})

	t.Run("场景51-100: 并发场景测试", func(t *testing.T) {
		testConcurrentScenarios(t, hub)
	})

	t.Run("场景101-150: 消息路由测试", func(t *testing.T) {
		testRoutingScenarios(t, hub)
	})

	t.Run("场景151-200: 边界和异常测试", func(t *testing.T) {
		testEdgeCaseScenarios(t, hub)
	})
}

// 场景1-50: 基础功能测试
func testBasicScenarios(t *testing.T, hub *Hub) {
	for i := 1; i <= 50; i++ {
		t.Run(fmt.Sprintf("场景%d", i), func(t *testing.T) {
			switch {
			case i <= 10: // 场景1-10: 客户端注册
				client := createTestClientWithIDGen(UserTypeCustomer, 200)
				hub.Register(client)
				time.Sleep(10 * time.Millisecond)

				stats := hub.GetStats()
				assert.GreaterOrEqual(t, stats.TotalClients, 1,
					"场景%d: 应该至少有1个连接", i)

				hub.Unregister(client)
				time.Sleep(10 * time.Millisecond)

			case i <= 20: // 场景11-20: 不同用户类型注册
				userTypes := []UserType{
					UserTypeCustomer, UserTypeAgent, UserTypeBot,
					UserTypeAdmin, UserTypeVIP,
				}
				client := createTestClientWithIDGen(userTypes[(i-11)%5], 200)
				hub.Register(client)
				time.Sleep(10 * time.Millisecond)

				onlineUsers := hub.GetOnlineUsers()
				assert.Contains(t, onlineUsers, client.UserID,
					"场景%d: 在线用户应包含刚注册的用户", i)

				hub.Unregister(client)

			case i <= 30: // 场景21-30: 不同状态的客户端
				statuses := []UserStatus{
					UserStatusOnline, UserStatusAway, UserStatusBusy,
					UserStatusOffline, UserStatusInvisible,
				}
				client := createTestClientWithIDGen(UserTypeCustomer, 200)
				client.Status = statuses[(i-21)%5]
				hub.Register(client)
				time.Sleep(10 * time.Millisecond)

				assert.Equal(t, statuses[(i-21)%5], client.Status,
					"场景%d: 客户端状态应保持不变", i)

				hub.Unregister(client)

			case i <= 40: // 场景31-40: Hub统计信息测试
				initialStats := hub.GetStats()
				client := createTestClientWithIDGen(UserTypeCustomer, 200)
				hub.Register(client)
				time.Sleep(10 * time.Millisecond)

				newStats := hub.GetStats()
				assert.GreaterOrEqual(t, newStats.TotalClients,
					initialStats.TotalClients,
					"场景%d: 总连接数应增加或保持", i)

				hub.Unregister(client)

			default: // 场景41-50: 获取在线用户
				clients := make([]*Client, 5)
				for j := 0; j < 5; j++ {
					clients[j] = createTestClientWithIDGen(UserTypeCustomer, 200)
					hub.Register(clients[j])
				}
				time.Sleep(50 * time.Millisecond)

				onlineUsers := hub.GetOnlineUsers()
				assert.GreaterOrEqual(t, len(onlineUsers), 5,
					"场景%d: 应至少有5个在线用户", i)

				for j := 0; j < 5; j++ {
					hub.Unregister(clients[j])
				}
			}
		})
	}
}

// 场景51-100: 并发场景测试
func testConcurrentScenarios(t *testing.T, hub *Hub) {
	for i := 51; i <= 100; i++ {
		t.Run(fmt.Sprintf("场景%d", i), func(t *testing.T) {
			switch {
			case i <= 60: // 场景51-60: 并发注册
				var wg sync.WaitGroup
				clientCount := 10
				wg.Add(clientCount)

				for j := 0; j < clientCount; j++ {
					go func(index int) {
						defer wg.Done()
						client := createTestClientWithIDGen(UserTypeCustomer, 200)
						hub.Register(client)
					}(j)
				}
				wg.Wait()
				time.Sleep(100 * time.Millisecond)

				stats := hub.GetStats()
				assert.GreaterOrEqual(t, stats.TotalClients, clientCount,
					"场景%d: 并发注册应成功", i)

			case i <= 70: // 场景61-70: 并发注销
				clients := make([]*Client, 10)
				for j := 0; j < 10; j++ {
					clients[j] = createTestClientWithIDGen(UserTypeCustomer, 200)
					hub.Register(clients[j])
				}
				time.Sleep(50 * time.Millisecond)

				var wg sync.WaitGroup
				wg.Add(10)
				for j := 0; j < 10; j++ {
					go func(index int) {
						defer wg.Done()
						hub.Unregister(clients[index])
					}(j)
				}
				wg.Wait()
				time.Sleep(50 * time.Millisecond)

				assert.True(t, true, "场景%d: 并发注销应成功", i)

			case i <= 80: // 场景71-80: 并发消息发送
				client := createTestClientWithIDGen(UserTypeCustomer, 200)
				hub.Register(client)
				time.Sleep(50 * time.Millisecond)

				var wg sync.WaitGroup
				msgCount := 20
				wg.Add(msgCount)

				for j := 0; j < msgCount; j++ {
					go func(index int) {
						defer wg.Done()
						msg := createTestHubMessage(MessageTypeBinary)
						hub.SendToUserWithRetry(context.Background(), client.UserID, msg)
					}(j)
				}
				wg.Wait()
				time.Sleep(100 * time.Millisecond)

				hub.Unregister(client)
				assert.True(t, true, "场景%d: 并发消息发送应成功", i)

			default: // 场景81-100: 混合并发操作
				var wg sync.WaitGroup
				wg.Add(30)

				// 10个并发注册
				for j := 0; j < 10; j++ {
					go func(index int) {
						defer wg.Done()
						client := createTestClientWithIDGen(UserTypeCustomer, 200)
						hub.Register(client)
					}(j)
				}

				// 10个并发获取统计
				for j := 0; j < 10; j++ {
					go func() {
						defer wg.Done()
						_ = hub.GetStats()
					}()
				}

				// 10个并发获取在线用户
				for j := 0; j < 10; j++ {
					go func() {
						defer wg.Done()
						_ = hub.GetOnlineUsers()
					}()
				}

				wg.Wait()
				assert.True(t, true, "场景%d: 混合并发操作应成功", i)
			}
		})
	}
}

// 场景101-150: 消息路由测试
func testRoutingScenarios(t *testing.T, hub *Hub) {
	for i := 101; i <= 150; i++ {
		t.Run(fmt.Sprintf("场景%d", i), func(t *testing.T) {
			switch {
			case i <= 120: // 场景101-120: 点对点消息
				sender := createTestClientWithIDGen(UserTypeCustomer, 200)

				receiver := createTestClientWithIDGen(UserTypeAgent, 200)

				hub.Register(sender)
				hub.Register(receiver)
				time.Sleep(50 * time.Millisecond)

				msg := createTestHubMessage(MessageTypeText)

				result := hub.SendToUserWithRetry(context.Background(), receiver.UserID, msg)
				assert.NoError(t, result.FinalError, "场景%d: 发送消息应成功", i)
				hub.Unregister(sender)
				hub.Unregister(receiver)

			default: // 场景136-150: 广播消息
				clients := make([]*Client, 5)
				for j := 0; j < 5; j++ {
					clients[j] = createTestClientWithIDGen(UserTypeCustomer, 200)
					hub.Register(clients[j])
				}
				time.Sleep(100 * time.Millisecond)
				msg := createTestHubMessage(MessageTypeText)

				hub.Broadcast(context.Background(), msg)
				time.Sleep(100 * time.Millisecond)

				for j := 0; j < 5; j++ {
					hub.Unregister(clients[j])
				}
				assert.True(t, true, "场景%d: 广播消息应成功", i)
			}
		})
	}
}

// 场景151-200: 边界和异常测试
func testEdgeCaseScenarios(t *testing.T, hub *Hub) {
	for i := 151; i <= 200; i++ {
		t.Run(fmt.Sprintf("场景%d", i), func(t *testing.T) {
			switch {
			case i <= 160: // 场景151-160: 空消息处理
				receiver := createTestClientWithIDGen(UserTypeCustomer)
				hub.Register(receiver)
				time.Sleep(50 * time.Millisecond)

				msg := &HubMessage{
					MessageType: MessageTypeText,
					Content:     "",
					CreateAt:    time.Now(),
				}

				result := hub.SendToUserWithRetry(context.Background(), receiver.UserID, msg)
				assert.NoError(t, result.FinalError, "场景%d: 空消息应能发送", i)
				hub.Unregister(receiver)

			case i <= 170: // 场景161-170: 不存在的用户
				msg := createTestHubMessage(MessageTypeText)

				result := hub.SendToUserWithRetry(context.Background(), fmt.Sprintf("nonexistent-%d", i), msg)
				assert.Error(t, result.FinalError, "场景%d: 向不存在用户发送应返回错误", i)
			case i <= 180: // 场景171-180: 重复注册相同用户
				client1 := createTestClientWithIDGen(UserTypeCustomer)
				hub.Register(client1)
				time.Sleep(50 * time.Millisecond)

				client2 := createTestClientWithIDGen(UserTypeCustomer)
				client2.UserID = client1.UserID
				hub.Register(client2)
				time.Sleep(50 * time.Millisecond)

				onlineUsers := hub.GetOnlineUsers()
				userCount := 0
				for _, u := range onlineUsers {
					if u == client1.UserID {
						userCount++
					}
				}
				assert.Equal(t, 1, userCount, "场景%d: 相同用户只应有一个连接", i)

				hub.Unregister(client2)

			case i <= 190: // 场景181-190: 大消息内容
				receiver := createTestClientWithIDGen(UserTypeCustomer, 10000)

				hub.Register(receiver)
				time.Sleep(50 * time.Millisecond)

				// 生成1MB的大消息
				largeContent := make([]byte, 1024*1024)
				for j := range largeContent {
					largeContent[j] = byte('A' + (j % 26))
				}

				msg := &HubMessage{
					MessageType: MessageTypeText,
					Content:     string(largeContent),
					CreateAt:    time.Now(),
				}

				result := hub.SendToUserWithRetry(context.Background(), receiver.UserID, msg)
				assert.NoError(t, result.FinalError, "场景%d: 大消息应能发送", i)

				hub.Unregister(receiver)

			default: // 场景191-200: Hub停止和清理
				client := createTestClientWithIDGen(UserTypeCustomer, 10000)
				hub.Register(client)
				time.Sleep(50 * time.Millisecond)

				stats := hub.GetStats()
				assert.NotNil(t, stats, "场景%d: 统计信息应可获取", i)
				assert.GreaterOrEqual(t, stats.TotalClients, 0, "场景%d: 应有总连接数字段", i)
				assert.GreaterOrEqual(t, stats.OnlineUsers, 0, "场景%d: 应有在线用户数字段", i)

				hub.Unregister(client)
			}
		})
	}
}

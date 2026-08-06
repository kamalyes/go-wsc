/**
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-03-10 17:50:22
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-03-10 17:50:22
 * @FilePath: \go-wsc\hub\heartbeat_race_test.go
 * @Description: 心跳和断开连接竞态条件测试
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */
package hub

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-wsc/repository"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
)

// TestHeartbeatAndDisconnectRace 测试心跳和断开连接的竞态条件
// 这个测试模拟了实际生产环境中的 panic 场景：
// 1. 客户端发送心跳消息
// 2. 同时客户端断开连接（关闭 SendChan）
// 3. 心跳处理尝试发送 Pong 响应到已关闭的 channel
func TestHeartbeatAndDisconnectRace(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	// 运行多次以增加触发竞态条件的概率
	for i := 0; i < 100; i++ {
		client := &Client{
			ID:            "race-test-client",
			UserID:        "race-test-user",
			UserType:      UserTypeCustomer,
			Status:        UserStatusOnline,
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			SendChan:      make(chan []byte, 10),
			Context:       context.Background(),
		}

		// 注册客户端
		hub.Register(client)
		time.Sleep(10 * time.Millisecond)

		var wg sync.WaitGroup
		wg.Add(2)

		// Goroutine 1: 发送心跳消息
		go func() {
			defer wg.Done()
			hub.handleHeartbeatMessage(client)
		}()

		// Goroutine 2: 立即断开连接
		go func() {
			defer wg.Done()
			hub.Unregister(client)
		}()

		wg.Wait()
		time.Sleep(10 * time.Millisecond)
	}

	// 如果没有 panic，测试通过
	assert.True(t, true, "测试完成，没有发生 panic")
}

// TestConcurrentHeartbeatAndUnregister 测试并发心跳和注销
func TestConcurrentHeartbeatAndUnregister(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	const numClients = 50
	const numIterations = 10

	for iter := 0; iter < numIterations; iter++ {
		clients := make([]*Client, numClients)

		// 注册多个客户端
		for i := 0; i < numClients; i++ {
			client := &Client{
				ID:            hub.idGenerator.GenerateRequestID(),
				UserID:        hub.idGenerator.GenerateRequestID(),
				UserType:      UserTypeCustomer,
				Status:        UserStatusOnline,
				LastSeen:      time.Now(),
				LastHeartbeat: time.Now(),
				SendChan:      make(chan []byte, 10),
				Context:       context.Background(),
			}
			clients[i] = client
			hub.Register(client)
		}

		time.Sleep(50 * time.Millisecond)

		var wg sync.WaitGroup

		// 并发发送心跳和注销
		for _, client := range clients {
			wg.Add(2)

			// 发送心跳
			go func(c *Client) {
				defer wg.Done()
				for j := 0; j < 5; j++ {
					hub.handleHeartbeatMessage(c)
					time.Sleep(time.Millisecond)
				}
			}(client)

			// 注销客户端
			go func(c *Client) {
				defer wg.Done()
				time.Sleep(2 * time.Millisecond)
				hub.Unregister(c)
			}(client)
		}

		wg.Wait()
		time.Sleep(50 * time.Millisecond)
	}

	assert.True(t, true, "并发测试完成，没有发生 panic")
}

// TestSendPongResponseToClosedChannel 测试向已关闭的 channel 发送 Pong
func TestSendPongResponseToClosedChannel(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "closed-channel-test",
		UserID:        "test-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 手动关闭 channel 并标记为已关闭
	client.CloseMu.Lock()
	client.MarkClosed()
	close(client.SendChan)
	client.CloseMu.Unlock()

	// 尝试发送 Pong 响应（应该返回错误而不是 panic）
	err := hub.sendPongResponse(client, time.Now())
	assert.Error(t, err, "向已关闭的 channel 发送应该返回错误")
	assert.Contains(t, err.Error(), "closed", "错误信息应该包含 'closed'")
}

// TestSendPongResponseSuccess 测试 Pong 响应成功发送及消息内容
func TestSendPongResponseSuccess(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	hub.WaitForStart()

	client := &Client{
		ID:            "pong-success-test",
		UserID:        "pong-success-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 10),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	// 发送 Pong 响应
	now := time.Now()
	err := hub.sendPongResponse(client, now)
	assert.NoError(t, err, "发送 Pong 应该成功")

	// 验证消息是否被发送到 SendChan
	select {
	case msg := <-client.SendChan:
		assert.NotNil(t, msg, "应该收到 Pong 消息")
		var pongMsg HubMessage
		err := json.Unmarshal(msg, &pongMsg)
		assert.NoError(t, err, "消息应该能反序列化")
		assert.Equal(t, MessageTypePong, pongMsg.MessageType, "消息类型应该是 Pong")
		assert.Equal(t, client.UserID, pongMsg.Receiver, "接收者应该是客户端用户ID")
		assert.Equal(t, now.Unix(), pongMsg.CreateAt.Unix(), "CreateAt 应该为传入时间")
	case <-time.After(1 * time.Second):
		t.Fatal("超时:没有收到 Pong 消息")
	}

	hub.Unregister(client)
}

// TestClientTrySendAfterClose 测试客户端关闭后的 TrySend
func TestClientTrySendAfterClose(t *testing.T) {
	client := &Client{
		ID:       "try-send-test",
		UserID:   "test-user",
		UserType: UserTypeCustomer,
		Status:   UserStatusOnline,
		LastSeen: time.Now(),
		SendChan: make(chan []byte, 10),
		Context:  context.Background(),
	}

	// 正常发送应该成功
	success := client.TrySend([]byte("test message"))
	assert.True(t, success, "正常发送应该成功")

	// 关闭 channel
	client.CloseMu.Lock()
	client.MarkClosed()
	close(client.SendChan)
	client.CloseMu.Unlock()

	// 关闭后发送应该失败（不应该 panic）
	success = client.TrySend([]byte("test message after close"))
	assert.False(t, success, "关闭后发送应该失败")
}

// TestMultipleGoroutinesSendingToSameClient 测试多个 goroutine 向同一客户端发送
func TestMultipleGoroutinesSendingToSameClient(t *testing.T) {
	hub := NewHub(wscconfig.Default())
	defer hub.Shutdown()

	go hub.Run()
	time.Sleep(100 * time.Millisecond)

	client := &Client{
		ID:            "multi-sender-test",
		UserID:        "test-user",
		UserType:      UserTypeCustomer,
		Status:        UserStatusOnline,
		LastSeen:      time.Now(),
		LastHeartbeat: time.Now(),
		SendChan:      make(chan []byte, 100),
		Context:       context.Background(),
	}

	hub.Register(client)
	time.Sleep(50 * time.Millisecond)

	var wg sync.WaitGroup
	const numSenders = 10
	const numMessages = 100

	// 启动多个发送者
	for i := 0; i < numSenders; i++ {
		wg.Add(1)
		go func(senderID int) {
			defer wg.Done()
			for j := 0; j < numMessages; j++ {
				msg := &HubMessage{
					ID:          hub.idGenerator.GenerateRequestID(),
					MessageType: MessageTypeText,
					Sender:      "system",
					Receiver:    client.UserID,
					Content:     "test message",
					CreateAt:    time.Now(),
				}
				hub.sendToClient(context.Background(), client, msg)
				time.Sleep(time.Microsecond)
			}
		}(i)
	}

	// 在发送过程中注销客户端
	time.Sleep(50 * time.Millisecond)
	hub.Unregister(client)

	wg.Wait()
	assert.True(t, true, "多发送者测试完成，没有发生 panic")
}

// TestHeartbeatRedisWorkerNoDeadlock 验证心跳 Redis worker 在「并发心跳 + 并发断开 + 关闭」下无死锁
//
// 覆盖修改后的完整路径：
//  1. handleHeartbeatMessage 投递 *Client 到 heartbeatRedisCh（chan *Client，非阻塞 send）
//  2. processHeartbeatRedisUpdates worker flush 调用 BatchSetClientsOnline（json.Marshal + Lua）
//  3. IsClosed() 过滤已断开客户端
//  4. SafeShutdown：h.cancel() → worker flush 残余 → h.wg.Wait()
//
// 使用 miniredis 提供真实 Redis 语义，确保 Lua 脚本执行路径被覆盖
// 带死锁守护：30s 未完成则判定死锁并失败
func TestHeartbeatRedisWorkerNoDeadlock(t *testing.T) {
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer redisClient.Close()

	config := wscconfig.Default().
		WithNodeInfo("127.0.0.1", 18081).
		WithHeartbeatInterval(30 * time.Second).
		WithMessageBufferSize(256)

	hub := NewHub(config)
	onlineStatusRepo := repository.NewRedisOnlineStatusRepository(redisClient, &wscconfig.OnlineStatus{
		KeyPrefix: "wsc:test:online:",
		TTL:       60 * time.Second,
	})
	hub.SetOnlineStatusRepository(onlineStatusRepo)

	go hub.Run()
	hub.WaitForStart()
	// defer SafeShutdown 本身也验证关闭路径（cancel → flush → wg.Wait）无死锁
	defer func() { _ = hub.SafeShutdown() }()

	const numClients = 60
	const heartbeatsPerClient = 10
	clients := make([]*Client, numClients)
	for i := 0; i < numClients; i++ {
		// SendChan 缓冲需 >= heartbeatsPerClient，避免 pong 阻塞 sendPongResponse
		c := &Client{
			ID:            hub.idGenerator.GenerateRequestID(),
			UserID:        hub.idGenerator.GenerateRequestID(),
			UserType:      UserTypeCustomer,
			Status:        UserStatusOnline,
			NodeID:        "test-node", // BatchSetClientsOnline 要求 NodeID 非空
			LastSeen:      time.Now(),
			LastHeartbeat: time.Now(),
			SendChan:      make(chan []byte, heartbeatsPerClient+4),
			Context:       context.Background(),
		}
		clients[i] = c
		hub.Register(c)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		var wg sync.WaitGroup

		// 并发心跳：向 heartbeatRedisCh 投递 *Client
		for _, c := range clients {
			wg.Add(1)
			go func(cl *Client) {
				defer wg.Done()
				for j := 0; j < heartbeatsPerClient; j++ {
					hub.handleHeartbeatMessage(cl)
					time.Sleep(time.Millisecond)
				}
			}(c)
		}

		// 并发注销前一半客户端：触发 worker 的 IsClosed() 过滤路径
		for i := 0; i < numClients/2; i++ {
			wg.Add(1)
			go func(cl *Client) {
				defer wg.Done()
				time.Sleep(5 * time.Millisecond)
				hub.Unregister(cl)
			}(clients[i])
		}

		wg.Wait()
		// 让 worker 的 2s ticker 至少触发一次，确保 BatchSetClientsOnline 真正执行
		time.Sleep(2500 * time.Millisecond)
	}()

	select {
	case <-done:
		// 正常完成
	case <-time.After(30 * time.Second):
		t.Fatal("检测到死锁：心跳 Redis worker 在 30s 内未完成")
	}

	// 验证在线索引确实被 worker 重建（后半部分客户端仍在线）
	online, err := hub.IsUserOnline(clients[numClients-1].UserID)
	assert.NoError(t, err)
	assert.True(t, online, "在线客户端应被心跳 worker 重建到 Redis 在线索引")
}

/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-06 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-07-06 21:30:00
 * @FilePath: \go-wsc\hub\message_handler.go
 * @Description: Hub 消息处理逻辑
 *   - 客户端读写循环（handleClientRead/handleClientWrite）
 *   - 文本/二进制消息处理
 *   - 可转发消息自动转发
 *   - 消息字段规范化
 *   - 消息接收/错误回调触发
 *   - 心跳检查
 *   - 广播/点对点消息分发
 *
 * 从 utils.go 拆分而来，职责单一：所有消息处理相关逻辑
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"encoding/json"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/protocol"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 客户端读写处理
// ============================================================================

// handleClientWrite 处理客户端消息写入
func (h *Hub) handleClientWrite(client *Client) {
	h.wg.Add(1)
	defer h.wg.Done()
	defer func() {
		h.logWithClient(logger.INFO, "客户端写入协程结束", client)
	}()

	h.logWithClient(logger.INFO, "客户端写入协程启动", client)

	for {
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				h.logWithClient(logger.INFO, "客户端发送通道关闭", client)
				return
			}

			if client.Conn != nil {
				client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := client.Conn.WriteMessage(websocket.TextMessage, message); err != nil {
					h.logWithClient(logger.ERROR, "客户端消息写入失败", client, "error", err)
					// 主动关闭连接，让读 goroutine 的 ReadMessage 立即报错退出
					// 否则读 goroutine 会卡在 IO wait 直到 TCP keepalive 超时，造成半死连接泄漏
					// （读 goroutine 退出后会触发 defer Unregister 完成清理）
					_ = client.Conn.Close()
					return
				}
			}
		case <-h.ctx.Done():
			h.logWithClient(logger.INFO, "客户端写入协程因Hub关闭而结束", client)
			return
		}
	}
}

// handleClientRead 处理客户端消息读取
func (h *Hub) handleClientRead(client *Client) {
	h.wg.Add(1)
	defer h.wg.Done()
	defer h.Unregister(client)
	defer func() {
		h.logWithClient(logger.INFO, "客户端读取协程结束", client)
	}()

	h.logWithClient(logger.INFO, "客户端读取协程启动", client)

	// 使用 client.Context（从 Hub 生命周期 h.ctx 派生的连接级 ctx，Hub 关闭时自动取消）
	reqCtx := client.Context

	for {
		messageType, data, err := client.Conn.ReadMessage()
		if err != nil {
			// Hub 正在关闭（SafeShutdown 触发），连接是被服务端主动关闭的
			// 此时读循环会拿到 "use of closed network connection"，走 ClassifyCloseError
			// 会被误判为 1006 异常断开，所以这里短路掉，单独记一条 INFO 日志
			if h.shutdown.Load() {
				h.logWithClient(logger.INFO, "服务关闭，断开客户端连接", client, "error", err.Error())
				return
			}

			// 🔍 识别断开类型和原因
			errStr := err.Error()
			closeCode, isNormal := ClassifyCloseError(err)

			// 获取关闭码描述
			codeDesc := "未知错误"
			if info, exists := WsCloseCodeMap[closeCode]; exists {
				codeDesc = info.Desc
			}

			// 根据错误类型记录不同级别的日志
			if isNormal {
				h.logWithClient(logger.INFO, "客户端正常断开", client, "close_code", closeCode, "code_desc", codeDesc)
			} else {
				// 异常断开 - 记录详细信息用于排查
				h.logWithClient(logger.WARN, "客户端异常断开", client, "close_code", closeCode, "code_desc", codeDesc, "error", errStr)
				// 记录错误到连接记录
				h.trackConnectionError(client.Context, client.ID, client.UserType, err)
			}
			return
		}

		client.SetLastSeen(time.Now())

		switch messageType {
		case websocket.TextMessage:
			h.handleTextMessage(reqCtx, client, data)
		case websocket.BinaryMessage:
			h.handleBinaryMessage(client, data)
		case websocket.CloseMessage:
			return
		case websocket.PingMessage:
			// 设置写超时，避免恶意客户端通过频繁 Ping 阻塞写 goroutine
			_ = client.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_ = client.Conn.WriteMessage(websocket.PongMessage, nil)
		}
	}
}

// handleTextMessage 处理文本消息
func (h *Hub) handleTextMessage(ctx context.Context, client *Client, data []byte) {
	var msg *HubMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		msg = NewHubMessage().
			SetSender(client.UserID).
			SetSenderType(client.UserType).
			SetContent(string(data)).
			SetMessageType(MessageTypeText)
	}

	// 规范化消息字段
	h.normalizeMessageFields(client, msg)

	// 根据消息类型进行特殊处理
	switch msg.MessageType {
	case models.MessageTypePing, models.MessageTypeHeartbeat:
		// 处理心跳/Ping消息
		h.handleHeartbeatMessage(client)
		return
	case models.MessageTypeAck:
		// ACK消息由AckManager处理
		if h.config.EnableAck && h.ackManager != nil {
			ackMsg := &protocol.AckMessage{
				MessageID: msg.MessageID,
				Status:    protocol.AckStatusConfirmed,
				Timestamp: time.Now(),
			}
			h.ackManager.ConfirmMessage(msg.MessageID, ackMsg)
		}
		return
	}

	// 源头注入路由元数据（发送方 namespace + 默认群组）
	// 下游 handleForwardableMessage / 业务回调 / SendToUserWithRetry → StoreOfflineMessage
	// 均从此 ctx + msg 信封提取 (ns, group)，保证离线消息按 ns:group:userID 维度正确隔离。
	// namespace 已在注册时归一化（handleRegister），此处直接取真实值，存储层无需兜底。
	// 与 handleBroadcast 观察者注入范式一致，全项目统一。
	// 老系统不传 GroupID 时 GetGroupIDRaw() 返回空串，判空避免 WithGroup("") 得到 []string{""}（非 nil）被误判为群组消息
	route := routing.NewRoute().WithAppID(client.GetAppID()).WithNamespace(client.Namespace)
	if gid := client.GetGroupIDRaw(); gid != "" {
		route = route.WithGroup(gid)
	}
	ctx = route.Inject(ctx)
	// 🔏 同步 ctx 路由到 msg 信封（异步队列/离线存储/跨节点投递 均可从 msg 直接恢复路由，不丢上下文）
	var msgGIDs []string
	if gid := client.GetGroupIDRaw(); gid != "" {
		msgGIDs = []string{gid}
	}
	msg.ContextWithRoute(ctx, client.GetAppID(), client.Namespace, msgGIDs)

	// 🔄 自动转发可转发类型的消息（异步执行，避免阻塞）
	// ⚠️ 必须透传 ctx：syncx.Go(ctx) 保留 trace_id + namespace，否则 handleForwardableMessage
	// 从 Background ctx 取 namespace 为空，导致 msg.Namespace 被覆盖为空 → 离线消息存到 default 维度 → 丢消息
	if models.MessageType(msg.MessageType).IsForwardableType() {
		syncx.Go(ctx).
			WithTimeout(5 * time.Second).
			OnPanic(func(r interface{}) {
				h.logger.ErrorContextKV(ctx, "转发消息panic", "panic", r, "stack", string(debug.Stack()), "message_id", msg.MessageID)
			}).
			ExecWithContext(func(ctx context.Context) error {
				return h.handleForwardableMessage(ctx, msg)
			})
		return
	}

	// 调用消息接收回调（其他类型消息交给业务层处理）
	if err := h.InvokeMessageReceivedCallback(ctx, client, msg); err != nil {
		h.logger.WarnContextKV(ctx, "消息接收回调执行失败",
			"client_id", client.ID,
			"error", err,
		)
	}
}

// handleForwardableMessage 处理可转发类型的消息（窗口消息、状态消息等）
// 这些消息无需业务层处理，框架自动转发
func (h *Hub) handleForwardableMessage(ctx context.Context, msg *HubMessage) error {
	// P2P 转发：group 不参与（nil），覆盖 handleTextMessage 注入的发送方 group
	// 发送方 group 仅用于观察者通知（handleBroadcast），离线存储必须按 P2P 维度（ns:默认组:userID）
	// 否则接收方上线时枚举自己的 group + P2P 队列，若不在发送方 group 则取不到 → 丢消息
	// 🔏 同时覆盖 ctx 和 msg 信封两处路由（ctx 供同步链路，msg 信封供异步队列/离线回放读取）
	// ⚠️ namespace 优先从 msg 信封取（handleTextMessage 已注入，syncx.Go 异步场景 ctx 可能来自 Background），
	//    msg 信封为空时 fallback 到 ctx（兼容直接调用场景，如测试）
	ns := msg.Namespace
	if ns == "" {
		ns = routing.NamespaceFromContext(ctx)
	}
	ctx = msg.ContextWithRoute(ctx, routing.AppIDFromContext(ctx), ns, nil)

	emoji := msg.MessageType.GetEmoji()

	h.logger.DebugContextKV(ctx, emoji+" 自动转发消息",
		"message_type", msg.MessageType,
		"from", msg.Sender,
		"to", msg.Receiver,
		"message_id", msg.MessageID,
	)

	// 检查接收者是否指定
	if msg.Receiver == "" {
		h.logger.WarnContextKV(ctx, "可转发消息缺少接收者",
			"message_type", msg.MessageType,
			"sender", msg.Sender,
		)
		return nil
	}

	// 使用 SendToUserWithRetry 自动转发消息
	ctx = context.WithValue(ctx, ContextKeySenderID, msg.Sender)
	result := h.SendToUserWithRetry(ctx, msg.Receiver, msg)

	if !result.Success {
		h.logger.ErrorContextKV(ctx, emoji+" 转发失败", "from", msg.Sender, "to", msg.Receiver, "error", result.FinalError)
		return result.FinalError
	}
	return nil
}

// handleBinaryMessage 处理二进制消息
func (h *Hub) handleBinaryMessage(client *Client, data []byte) {
	h.logger.DebugContextKV(client.Context, "收到二进制消息",
		"client_id", client.ID,
		"user_id", client.UserID,
		"size", len(data),
	)
}

// ============================================================================
// 回调触发方法
// ============================================================================

// InvokeMessageReceivedCallback 触发消息接收回调
func (h *Hub) InvokeMessageReceivedCallback(ctx context.Context, client *Client, msg *HubMessage) error {
	if h.messageReceivedCallback == nil {
		return nil
	}

	// 规范化消息字段（补充发送者信息等）
	h.normalizeMessageFields(client, msg)

	return h.messageReceivedCallback(ctx, client, msg)
}

// InvokeErrorCallback 触发错误处理回调
// 此方法用于统一处理各种错误
func (h *Hub) InvokeErrorCallback(ctx context.Context, err error, severity ErrorSeverity) error {
	if h.errorCallback == nil {
		return nil
	}
	return h.errorCallback(ctx, err, severity)
}

// ============================================================================
// 消息字段规范化
// ============================================================================

// normalizeMessageFields 规范化消息字段（补充缺失的字段）
func (h *Hub) normalizeMessageFields(client *Client, msg *HubMessage) {
	msg.Sender = mathx.IfEmpty(msg.Sender, client.UserID)
	msg.SenderType = mathx.IfEmpty(msg.SenderType, client.UserType)
	// 🔥 设置发送者客户端ID，用于多端同步时排除当前设备
	msg.SenderClient = mathx.IfEmpty(msg.SenderClient, client.ID)
	msg.CreateAt = mathx.IF(msg.CreateAt.IsZero(), time.Now(), msg.CreateAt)
	msg.MessageType = mathx.IfEmpty(msg.MessageType, MessageTypeText)
	// 仅在 ID 为空时才生成雪花ID，避免每条消息都调用 idGenerator（热路径 CPU 浪费）
	if msg.ID == "" {
		snowflakeId := h.idGenerator.GenerateRequestID()
		msg.ID = client.UserID + "-" + snowflakeId
	}
}

// ============================================================================
// 心跳检查
// ============================================================================

// checkHeartbeat 检查 SSE 客户端心跳超时（兜底机制）
// WebSocket 客户端由 heartbeatTimer O(1) 管理，此处仅扫描 SSE 客户端
// SSE 客户端不发送 PING，无法通过时间轮 Refresh，需定期扫描 LastSeen 判断活跃度
//
// ⚠️ 死锁防御：ForEachClientParallel 持有 shard 读锁，若在 callback 中直接调用 Unregister，
// Unregister → go handleUnregister → RemoveClient → WithShardLock，
// 同一 shard 持读锁等写锁 → 死锁
// 修复：先收集超时客户端到本地 slice（mutex 保护并发 append），遍历结束后在锁外统一调用 Unregister
func (h *Hub) checkHeartbeat() {
	start := time.Now()
	now := start

	// 并发数快照（遍历开始时的总连接数）
	totalClients := h.shardedRegistry.GetClientCount()

	// Phase 1：并行持读锁收集 SSE 超时客户端（WebSocket 由时间轮管理，跳过）
	type timeoutClient struct {
		client     *Client
		lastActive time.Time
	}
	var mu sync.Mutex
	var timeouts []timeoutClient
	var scanned int64

	h.shardedRegistry.ForEachClientParallel(0, func(_ string, client *Client) {
		// WebSocket 客户端由 heartbeatTimer O(1) 管理，跳过
		if client.ConnectionType != ConnectionTypeSSE {
			return
		}
		atomic.AddInt64(&scanned, 1)
		// 原子读时间戳（并发安全，无数据竞争）
		lastActive := client.GetLastSeen()

		// 检查是否超时
		inactiveDuration := now.Sub(lastActive)
		if inactiveDuration > h.config.ClientTimeout {
			mu.Lock()
			timeouts = append(timeouts, timeoutClient{client: client, lastActive: lastActive})
			mu.Unlock()
		}
	})

	traversalDuration := time.Since(start)

	// Phase 2：锁外批量注销（Unregister 内部走 channel 异步或 default 同步均安全）
	for _, tc := range timeouts {
		h.logger.DebugContextKV(tc.client.Context, "❤️ 检测到心跳超时，注销客户端",
			"client_id", tc.client.ID,
			"user_id", tc.client.UserID,
			"user_type", tc.client.UserType,
			"connection_type", tc.client.ConnectionType,
			"last_active", tc.lastActive,
			"inactive_duration", now.Sub(tc.lastActive).String(),
			"timeout_threshold", h.config.ClientTimeout.String(),
		)

		h.Unregister(tc.client)

		if h.heartbeatTimeoutCallback != nil {
			h.heartbeatTimeoutCallback(tc.client.ID, tc.client.UserID, tc.lastActive)
		}
	}

	totalDuration := time.Since(start)
	h.logger.DebugKV("❤️ 心跳检查完成",
		"total_clients", totalClients,
		"scanned", atomic.LoadInt64(&scanned),
		"timeouts", len(timeouts),
		"traversal_duration_ms", traversalDuration.Milliseconds(),
		"unregister_duration_ms", (totalDuration - traversalDuration).Milliseconds(),
		"total_duration_ms", totalDuration.Milliseconds(),
	)
}

// ============================================================================
// 广播处理
// ============================================================================

// handleBroadcast 处理广播消息
//
// 🔗 trace_id 恢复：广播队列异步消费，原请求 ctx 已不可用。
// msg 在入队前已通过 InjectContext 注入 trace_id，此处从 msg 恢复到 ctx，
// 保证下游 handleBroadcastMessage / handleDirectMessage / notifyObservers 日志链路串联。
func (h *Hub) handleBroadcast(msg *HubMessage) {
	ctx := msg.ContextFrom(h.ctx)

	// 🔍 通知观察者（异步，不阻塞主流程）
	// 路由来源优先级：
	//   1. msg 信封（入口层已注入，跨节点/异步队列场景时最可靠）—— 非空则直接用
	//   2. msg.Sender 的在线 client（发送者在哪个 ns/group 触发事件，就通知哪些订阅者）
	//      Observer 语义：订阅者关注"某个 ns/group 发生的事件"，因此发送者位置=路由
	//   3. 都为空 → 全局 ns+空 group（通知全局观察者）
	//
	// 🔥 群组消息（GroupIDs 非空）跳过观察者通知：
	//   SendToGroup 已在群组级别统一通知观察者（L350），此处若再通知会导致 N+1 重复
	//   （N=在线成员数：每个成员的 sendToUser → h.broadcast → handleBroadcast 都会触发一次 notifyObservers）
	//   BroadcastToGroupMembers 走 broadcastToUserIDs 不经过 handleBroadcast，无此问题
	//   仅 P2P 消息（GroupIDs 为空）和全局广播需要在此通知观察者
	//
	// ⚠️ 全局广播（BroadcastTypeGlobal 且 msg.Namespace==""）保持全局语义：
	//   不 fallback 到 sender client，避免将全局广播的观察者通知错误收窄到 sender 的 ns。
	//   全局广播投递给所有 ns 的客户端，观察者通知也应保持全局（ns="" → 仅通知全局观察者）。
	//   若收窄到 sender ns，其他 ns 的命名空间级观察者将收不到本应关注的全局事件。
	if len(msg.GroupIDs) == 0 {
		nsForObserver := msg.Namespace
		var gidsForObserver []string

		// 全局广播保持全局语义；P2P 消息 fallback 找 sender client 补齐 ns+group
		isGlobalBroadcast := msg.BroadcastType == BroadcastTypeGlobal && nsForObserver == ""
		if !isGlobalBroadcast {
			h.shardedRegistry.ForEachUserClient(msg.Sender, func(_ string, senderClient *models.Client) bool {
				if senderClient != nil {
					if nsForObserver == "" {
						nsForObserver = senderClient.Namespace
					}
					if sgid := senderClient.GetGroupIDRaw(); sgid != "" {
						gidsForObserver = []string{sgid}
					}
					return false // 取第一个在线 sender client 即停止
				}
				return true
			})
		}
		observerCtx := routing.RouteFrom(ctx).WithNamespace(nsForObserver).WithGroupIDs(gidsForObserver).Inject(ctx)
		h.notifyObservers(observerCtx, msg)
	}

	if msg.BroadcastType == BroadcastTypeGlobal {
		h.handleBroadcastMessage(ctx, msg)
		return
	}
	h.handleDirectMessage(ctx, msg)
}

// handleDirectMessage 处理点对点消息
//
// 性能：
//   - 指定 ReceiverClient 时走 GetClient O(1) 查找，避免遍历
//   - 未指定时走 ForEachUserClient 零拷贝遍历直接发送，避免 GetClientsCopyForUser 切片拷贝
//   - 消息预序列化一次，多设备复用
func (h *Hub) handleDirectMessage(ctx context.Context, msg *HubMessage) {
	// msg 路由信封由上游入口（sendToUser/handleBroadcast）已通过 InjectRoute 注入，此处直接读 msg 过滤

	// 预序列化一次（接收者多设备复用，消除循环内重复 Marshal）
	// 序列化失败时 data=nil，由 sendToClientSerialized 内部兜底
	data, _ := json.Marshal(msg)

	sent := 0
	if msg.ReceiverClient != "" {
		// 指定客户端：O(1) 查找，msgNamespace 非空时才做 namespace 匹配（防止跨 ns 串扰）
		// ⚠️ 不做 GroupIDs 系统组 vs 业务群匹配：ReceiverClient 已由发送方精准指定
		if client, ok := h.shardedRegistry.GetClient(msg.ReceiverClient); ok {
			if client != nil && (msg.Namespace == "" || client.Namespace == msg.Namespace) {
				h.sendToClientSerialized(ctx, client, msg, data)
				sent = 1
			}
		}
	} else {
		// 未指定客户端：遍历用户所有设备（ForEachUserClientFiltered 内部已按 msg.AppID/msg.Namespace 规则过滤）
		h.shardedRegistry.ForEachUserClientFiltered(msg.Receiver, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *Client) bool {
			h.sendToClientSerialized(ctx, client, msg, data)
			sent++
			return true
		})
	}

	// 📨 本地直连投递统计：Info 级保证生产可见；sent=0 是定位消息丢失的关键信号
	// （在线判定 true 但本地无连接：用户刚断线/连接漂移，跨节点路径未覆盖时会静默黑洞）
	if sent > 0 {
		// 增加消息发送统计（原子计数器，由 flushStatsCounters 定时刷写到 Redis）
		if h.statsRepo != nil {
			h.msgSentCount.Add(1)
		}
		h.logger.InfoContextKV(ctx, "📨 [投递诊断] 本地直连投递完成",
			"message_id", msg.MessageID,
			"receiver", msg.Receiver,
			"delivered_clients", sent,
			"receiver_client", msg.ReceiverClient,
		)
	} else if h.SendToUserViaSSE(msg.Receiver, msg) {
		h.logger.InfoContextKV(ctx, "📨 [投递诊断] 本地直连投递完成（SSE 通道）",
			"message_id", msg.MessageID,
			"receiver", msg.Receiver,
		)
	} else {
		h.logger.WarnContextKV(ctx, "📨 [投递诊断] 本地直连投递 0 客户端（本地无该用户连接）",
			"message_id", msg.MessageID,
			"receiver", msg.Receiver,
			"receiver_client", msg.ReceiverClient,
			"hint", "在线判定为真但本地无连接：用户刚断线或连接在其他节点，请检查跨节点路由日志",
		)
	}

	// 🔥 多端同步：P2P 消息同步给发送者的其他设备（排除当前发送设备）
	// ⚠️ 群组消息（msg.GroupIDs 非空）跳过：SendToGroup 对每个成员投递都走 handleDirectMessage，
	//    每次都同步会导致发送者其他设备收到 N 条重复（N=群组成员数）；
	//    且群组场景 excludeSender 语义已决定发送者是否收自己的消息，多端同步会与之冲突
	if msg.Sender != "" && msg.SenderClient != "" && len(msg.GroupIDs) == 0 {
		h.syncToSenderDevices(ctx, msg)
	}
}

// handleBroadcastMessage 处理广播消息
func (h *Hub) handleBroadcastMessage(ctx context.Context, msg *HubMessage) {
	// 🔏 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	// msg 可能来自跨节点 distMsg 未归一化，此处防御性兜底；namespace 保持原值（空=全局广播）
	ctx = msg.InjectRoute(ctx)

	start := time.Now()
	if h.statsRepo != nil {
		h.broadcastSentCount.Add(1)
	}

	// 预序列化消息（仅一次）
	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.ErrorContextKV(ctx, "广播消息序列化失败", "error", err)
		return
	}
	marshalDuration := time.Since(start)

	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	dataLen := len(data)

	// 并发数快照
	totalWSClients := h.shardedRegistry.GetClientCount()
	totalSSEClients := h.shardedRegistry.GetSSEClientCount()

	// 遍历所有客户端，仅投递路由匹配（namespace+group）的设备
	// ns1 的广播不会投递给 ns2 的用户，避免跨租户串扰
	// 使用并行遍历优化百万级连接广播性能（原子计数线程安全）
	var successCount int32
	var failCount int32
	var scanned int64

	wsStart := time.Now()
	h.shardedRegistry.ForEachClientFilteredParallel(0, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *Client) {
		atomic.AddInt64(&scanned, 1)
		if client.IsClosed() || client.ConnectionType == ConnectionTypeSSE {
			return
		}
		if client.TrySend(data) {
			atomic.AddInt32(&successCount, 1)
			h.trackReceiverMessageStats(client.ID, client.UserType, dataLen)
		} else {
			atomic.AddInt32(&failCount, 1)
		}
	})
	wsDuration := time.Since(wsStart)

	// 消息记录状态只更新一次（同一 msgID，无需每客户端都更新）
	if atomic.LoadInt32(&successCount) > 0 {
		h.updateMessageStatusAsync(ctx, msgID, MessageSendStatusSuccess, "", "")
	}

	if atomic.LoadInt32(&failCount) > 0 {
		h.logger.WarnContextKV(ctx, "广播消息：部分客户端发送失败",
			"success_count", atomic.LoadInt32(&successCount),
			"fail_count", atomic.LoadInt32(&failCount),
			"message_id", msg.MessageID,
		)
	}

	// SSE 客户端通过专用通道发送
	sseStart := time.Now()
	h.broadcastToSSEClients(msg)
	sseDuration := time.Since(sseStart)

	totalDuration := time.Since(start)
	h.logger.DebugContextKV(ctx, "📢 广播消息完成",
		"message_id", msg.MessageID,
		"message_type", msg.MessageType,
		"namespace", msg.Namespace,
		"data_bytes", dataLen,
		"total_ws_clients", totalWSClients,
		"total_sse_clients", totalSSEClients,
		"scanned", atomic.LoadInt64(&scanned),
		"ws_success", atomic.LoadInt32(&successCount),
		"ws_fail", atomic.LoadInt32(&failCount),
		"marshal_duration_ms", marshalDuration.Milliseconds(),
		"ws_duration_ms", wsDuration.Milliseconds(),
		"sse_duration_ms", sseDuration.Milliseconds(),
		"total_duration_ms", totalDuration.Milliseconds(),
	)
}

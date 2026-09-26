/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-07-06 00:00:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-09-22 10:52:00
 * @FilePath: \go-wsc\messaging\dispatcher.go
 * @Description: 消息分发器 —— 客户端读写泵 + 上行消息分发 + 广播扇出
 *   - 客户端读写循环（handleClientRead/handleClientWrite）
 *   - 文本/二进制消息处理（心跳/ACK 快路径、自动转发、业务回调）
 *   - 消息字段规范化（补发送者信息、雪花 ID）
 *   - 广播/点对点消息分发（准入、整形、扇出、多端同步）
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package messaging

import (
	"context"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-toolbox/pkg/json"
	"github.com/kamalyes/go-toolbox/pkg/mathx"
	"github.com/kamalyes/go-toolbox/pkg/syncx"
	"github.com/kamalyes/go-wsc/connection"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 客户端读写处理
// ============================================================================

// clientWriteBatchSize 单次唤醒最多排空的积压消息数
// （Slack 网关风格写合并：突发场景下 N 条消息共用一次 goroutine 唤醒 + 一次写超时，
// 降低高并发下的调度与 deadline 设置开销；上限防止单连接长期独占写协程饿死其他连接）
const clientWriteBatchSize = 64

// clientWriteTimeout 单条消息写入的超时时间
const clientWriteTimeout = 10 * time.Second

// handleClientWrite 处理客户端消息写入
func (m *Manager) handleClientWrite(client *models.Client) {
	m.wg.Add(1)
	defer m.wg.Done()
	defer func() {
		// 每连接 4 条生命周期日志（读写协程启动/结束），千万连接下不可忽略——降为 DEBUG
		m.logWithClient(logger.DEBUG, "客户端写入协程结束", client)
	}()

	m.logWithClient(logger.DEBUG, "客户端写入协程启动", client)

	hubCtx := m.host.Context()

	for {
		// 控制帧严格优先（双 lane 单写者）：select 前先非阻塞排空 CtrlCh，
		// 消除 select 伪随机调度导致控制帧（KickOut/ForceOffline/Ack）被数据帧插队的可能；
		// CtrlCh 为 nil（SSE/手工构造）时跳过，天然安全
		// 控制消息以 TextMessage 写出（序列化后的业务级控制消息，非 WS 协议控制帧）
		if client.CtrlCh != nil {
			for {
				select {
				case data, ok := <-client.CtrlCh:
					if !ok {
						return
					}
					if client.Conn == nil {
						continue
					}
					if err := m.writeClientMessages(client, data); err != nil {
						m.logWithClient(logger.ERROR, "控制帧写入失败", client, "error", err)
						// 主动关闭连接，让读 goroutine 的 ReadMessage 立即报错退出（语义同数据写失败）
						_ = client.Conn.Close()
						return
					}
				default:
					goto drained
				}
			}
		}
	drained:
		select {
		case message, ok := <-client.SendChan:
			if !ok {
				m.logWithClient(logger.INFO, "客户端发送通道关闭", client)
				return
			}

			if client.Conn == nil {
				continue
			}

			// 首条直写 + 积压 writev 合帧（突发 N 条：N 次 syscall 降至 2 次；见 frame_writer.go）
			if err := m.writeClientMessagesBatch(client, message); err != nil {
				m.logWithClient(logger.ERROR, "客户端消息写入失败", client, "error", err)
				// 主动关闭连接，让读 goroutine 的 ReadMessage 立即报错退出
				// 否则读 goroutine 会卡在 IO wait 直到 TCP keepalive 超时，造成半死连接泄漏
				// （读 goroutine 退出后会触发 defer Unregister 完成清理）
				_ = client.Conn.Close()
				return
			}
		case data, ok := <-client.CtrlCh:
			// 空闲唤醒：数据 lane 静默期（SendChan 空、无 pong）控制消息仍需及时写出，
			// 否则 KickOut/ForceOffline/Ack 会在 CtrlCh 滞留到下一次数据帧/pong 才被捎带处理；
			// 严格优先仍由循环顶部的非阻塞排空保证（本 case 仅兜底唤醒）
			// CtrlCh 为 nil（SSE/手工构造）时该 case 永不就绪，天然安全
			if !ok {
				return
			}
			if client.Conn == nil {
				continue
			}
			if err := m.writeClientMessages(client, data); err != nil {
				m.logWithClient(logger.ERROR, "控制帧写入失败", client, "error", err)
				_ = client.Conn.Close()
				return
			}
		case pongData := <-client.PongCh:
			// 协议级 PING 的 pong 响应（单写者：控制帧也统一由写泵写出，见 setupPingHandler）
			if client.Conn == nil {
				continue
			}
			// WriteControl 自带 deadline 参数，不依赖外层写超时
			if err := client.Conn.WriteControl(websocket.PongMessage, pongData, time.Now().Add(clientWriteTimeout)); err != nil {
				m.logWithClient(logger.ERROR, "pong 控制帧写入失败", client, "error", err)
				_ = client.Conn.Close()
				return
			}
		case <-client.DoneCh:
			// 客户端注销（closeClientChannel）——数据通道不 close，仅靠此信号退出
			m.logWithClient(logger.INFO, "客户端生命周期结束，写入协程退出", client)
			return
		case <-hubCtx.Done():
			m.logWithClient(logger.INFO, "客户端写入协程因Hub关闭而结束", client)
			return
		}
	}
}

// writeClientMessages 写入单条控制消息（不排空积压，控制帧专用）
func (m *Manager) writeClientMessages(client *models.Client, data []byte) error {
	client.Conn.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
	return client.Conn.WriteMessage(websocket.TextMessage, data)
}

// handleClientRead 处理客户端消息读取
func (m *Manager) handleClientRead(client *models.Client) {
	m.wg.Add(1)
	defer m.wg.Done()
	defer m.host.Unregister(client)
	defer func() {
		// 每连接 4 条生命周期日志（读写协程启动/结束），千万连接下不可忽略——降为 DEBUG
		m.logWithClient(logger.DEBUG, "客户端读取协程结束", client)
	}()

	m.logWithClient(logger.DEBUG, "客户端读取协程启动", client)

	// 使用 client.Context（从宿主生命周期 ctx 派生的连接级 ctx，Hub 关闭时自动取消）
	reqCtx := client.Context

	for {
		messageType, data, err := client.Conn.ReadMessage()
		if err != nil {
			// Hub 正在关闭（SafeShutdown 触发），连接是被服务端主动关闭的
			// 此时读循环会拿到 "use of closed network connection"，走 ClassifyCloseError
			// 会被误判为 1006 异常断开，所以这里短路掉，单独记一条 INFO 日志
			if m.host.IsShuttingDown() {
				m.logWithClient(logger.INFO, "服务关闭，断开客户端连接", client, "error", err.Error())
				return
			}

			// 识别断开类型和原因
			errStr := err.Error()
			closeCode, isNormal := connection.ClassifyCloseError(err)

			// 获取关闭码描述
			codeDesc := "未知错误"
			if info, exists := models.WsCloseCodeMap[closeCode]; exists {
				codeDesc = info.Desc
			}

			// 根据错误类型记录不同级别的日志
			if isNormal {
				m.logWithClient(logger.INFO, "客户端正常断开", client, "close_code", closeCode, "code_desc", codeDesc)
			} else {
				// 异常断开 - 记录详细信息用于排查
				m.logWithClient(logger.WARN, "客户端异常断开", client, "close_code", closeCode, "code_desc", codeDesc, "error", errStr)
				// 记录错误到连接记录
				m.host.TrackConnectionError(client.Context, client.ID, client.UserType, err)
			}
			return
		}

		client.SetLastSeen(time.Now())

		// 控制帧（ping/pong/close）由 gorilla 在 ReadMessage 内部经 handler 分发，
		// 不会作为 messageType 返回——协议级 PING 的保活与 pong 响应见 setupPingHandler
		switch messageType {
		case websocket.TextMessage:
			m.handleTextMessage(reqCtx, client, data)
		case websocket.BinaryMessage:
			m.handleBinaryMessage(client, data)
		}
	}
}

// handleTextMessage 处理文本消息
func (m *Manager) handleTextMessage(ctx context.Context, client *models.Client, data []byte) {
	// 高频控制类消息快路径（心跳/ACK）：
	// 心跳是连接层最高频消息（千万连接 每 30s 约 33 万 QPS），且处理只依赖 client
	// 不消费 msg 字段——仅反序列化 2 个字段的轻量探测结构，命中即返回，
	// 跳过完整 HubMessage 反序列化（全字段反射 + 时间解析 + slice 分配）。
	// json.Unmarshal 解析小结构时自动跳过其余字段，探测成本远低于完整反序列化
	var probe struct {
		MessageType models.MessageType `json:"message_type"`
		MessageID   string             `json:"message_id"`
	}
	if err := json.Unmarshal(data, &probe); err == nil {
		switch probe.MessageType {
		case models.MessageTypePing, models.MessageTypeHeartbeat:
			m.host.HandleHeartbeat(client)
			return
		case models.MessageTypeAck:
			if m.host.GetConfig().EnableAck && m.ackManager != nil {
				ackMsg := &AckMessage{
					MessageID: probe.MessageID,
					Status:    AckStatusConfirmed,
					Timestamp: time.Now(),
				}
				m.ackManager.ConfirmMessage(probe.MessageID, ackMsg)
			}
			return
		}
	}

	var msg *models.HubMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		msg = models.NewHubMessage().
			SetSender(client.UserID).
			SetSenderType(client.UserType).
			SetContent(string(data)).
			SetMessageType(models.MessageTypeText)
	}

	// 规范化消息字段
	m.normalizeMessageFields(client, msg)

	// 根据消息类型进行特殊处理
	switch msg.MessageType {
	case models.MessageTypePing, models.MessageTypeHeartbeat:
		// 处理心跳/Ping消息（快路径未命中时兜底：探测失败但完整反序列化后是心跳，如含非标字段的畸形 JSON）
		m.host.HandleHeartbeat(client)
		return
	case models.MessageTypeAck:
		// ACK消息由AckManager处理
		if m.host.GetConfig().EnableAck && m.ackManager != nil {
			ackMsg := &AckMessage{
				MessageID: msg.MessageID,
				Status:    AckStatusConfirmed,
				Timestamp: time.Now(),
			}
			m.ackManager.ConfirmMessage(msg.MessageID, ackMsg)
		}
		return
	}

	// 源头注入路由元数据（发送方 namespace + 默认群组）
	// 下游 handleForwardableMessage / 业务回调 / SendToUserWithRetry 到 StoreOfflineMessage
	// 均从此 ctx + msg 信封提取 (ns, group)，保证离线消息按 ns:group:userID 维度正确隔离。
	// namespace 已在注册时归一化（handleRegister），此处直接取真实值，存储层无需兜底。
	// 与 handleBroadcast 观察者注入范式一致，全项目统一。
	// 老系统不传 GroupID 时 GetGroupIDRaw() 返回空串，判空避免 WithGroup("") 得到 []string{""}（非 nil）被误判为群组消息
	route := routing.NewRoute().WithAppID(client.GetAppID()).WithNamespace(client.Namespace)
	if gid := client.GetGroupIDRaw(); gid != "" {
		route = route.WithGroup(gid)
	}
	ctx = route.Inject(ctx)
	// 同步 ctx 路由到 msg 信封（异步队列/离线存储/跨节点投递 均可从 msg 直接恢复路由，不丢上下文）
	var msgGIDs []string
	if gid := client.GetGroupIDRaw(); gid != "" {
		msgGIDs = []string{gid}
	}
	msg.ContextWithRoute(ctx, client.GetAppID(), client.Namespace, msgGIDs)

	// 自动转发可转发类型的消息（异步执行，避免阻塞）
	// 必须透传 ctx：syncx.Go(ctx) 保留 trace_id + namespace，否则 handleForwardableMessage
	// 从 Background ctx 取 namespace 为空，导致 msg.Namespace 被覆盖为空，离线消息存到 default 维度，丢消息
	if models.MessageType(msg.MessageType).IsForwardableType() {
		syncx.Go(ctx).
			WithTimeout(5 * time.Second).
			OnPanic(func(r interface{}) {
				m.host.GetLogger().ErrorContextKV(ctx, "转发消息panic", "panic", r, "stack", string(debug.Stack()), "message_id", msg.MessageID)
			}).
			ExecWithContext(func(ctx context.Context) error {
				return m.handleForwardableMessage(ctx, msg)
			})
		return
	}

	// 调用消息接收回调（其他类型消息交给业务层处理）
	if err := m.InvokeMessageReceivedCallback(ctx, client, msg); err != nil {
		m.host.GetLogger().WarnContextKV(ctx, "消息接收回调执行失败",
			"client_id", client.ID,
			"error", err,
		)
	}
}

// handleForwardableMessage 处理可转发类型的消息（窗口消息、状态消息等）
// 这些消息无需业务层处理，框架自动转发
func (m *Manager) handleForwardableMessage(ctx context.Context, msg *models.HubMessage) error {
	// P2P 转发：group 不参与（nil），覆盖 handleTextMessage 注入的发送方 group
	// 发送方 group 仅用于观察者通知（handleBroadcast），离线存储必须按 P2P 维度（ns:默认组:userID）
	// 否则接收方上线时枚举自己的 group + P2P 队列，若不在发送方 group 则取不到，丢消息
	// 同时覆盖 ctx 和 msg 信封两处路由（ctx 供同步链路，msg 信封供异步队列/离线回放读取）
	// namespace 优先从 msg 信封取（handleTextMessage 已注入，syncx.Go 异步场景 ctx 可能来自 Background），
	// msg 信封为空时 fallback 到 ctx（兼容直接调用场景，如测试）
	ns := msg.Namespace
	if ns == "" {
		ns = routing.NamespaceFromContext(ctx)
	}
	ctx = msg.ContextWithRoute(ctx, routing.AppIDFromContext(ctx), ns, nil)

	m.host.GetLogger().DebugContextKV(ctx, "自动转发消息",
		"message_type", msg.MessageType,
		"from", msg.Sender,
		"to", msg.Receiver,
		"message_id", msg.MessageID,
	)

	// 检查接收者是否指定
	if msg.Receiver == "" {
		m.host.GetLogger().WarnContextKV(ctx, "可转发消息缺少接收者",
			"message_type", msg.MessageType,
			"sender", msg.Sender,
		)
		return nil
	}

	// 使用 SendToUserWithRetry 自动转发消息
	ctx = context.WithValue(ctx, ContextKeySenderID, msg.Sender)
	result := m.SendToUserWithRetry(ctx, msg.Receiver, msg)

	if !result.Success {
		m.host.GetLogger().ErrorContextKV(ctx, "转发失败", "from", msg.Sender, "to", msg.Receiver, "error", result.FinalError)
		return result.FinalError
	}
	return nil
}

// handleBinaryMessage 处理二进制消息
func (m *Manager) handleBinaryMessage(client *models.Client, data []byte) {
	m.host.GetLogger().DebugContextKV(client.Context, "收到二进制消息",
		"client_id", client.ID,
		"user_id", client.UserID,
		"size", len(data),
	)
}

// ============================================================================
// 回调触发方法
// ============================================================================

// InvokeMessageReceivedCallback 触发消息接收回调
func (m *Manager) InvokeMessageReceivedCallback(ctx context.Context, client *models.Client, msg *models.HubMessage) error {
	if m.messageReceivedCallback == nil {
		return nil
	}

	// 规范化消息字段（补充发送者信息等）
	m.normalizeMessageFields(client, msg)

	return m.messageReceivedCallback(ctx, client, msg)
}

// InvokeErrorCallback 触发错误处理回调
// 此方法用于统一处理各种错误
func (m *Manager) InvokeErrorCallback(ctx context.Context, err error, severity models.ErrorSeverity) error {
	if m.errorCallback == nil {
		return nil
	}
	return m.errorCallback(ctx, err, severity)
}

// ============================================================================
// 消息字段规范化
// ============================================================================

// normalizeMessageFields 规范化消息字段（补充缺失的字段）
func (m *Manager) normalizeMessageFields(client *models.Client, msg *models.HubMessage) {
	msg.Sender = mathx.IfEmpty(msg.Sender, client.UserID)
	msg.SenderType = mathx.IfEmpty(msg.SenderType, client.UserType)
	// 设置发送者客户端ID，用于多端同步时排除当前设备
	msg.SenderClient = mathx.IfEmpty(msg.SenderClient, client.ID)
	msg.CreateAt = mathx.IF(msg.CreateAt.IsZero(), time.Now(), msg.CreateAt)
	msg.MessageType = mathx.IfEmpty(msg.MessageType, models.MessageTypeText)
	// 仅在 ID 为空时才生成雪花ID，避免每条消息都调用 idGenerator（热路径 CPU 浪费）
	if msg.ID == "" {
		snowflakeId := m.idGenerator.GenerateRequestID()
		msg.ID = client.UserID + "-" + snowflakeId
	}
}

// ============================================================================
// 广播处理
// ============================================================================

// handleBroadcast 处理广播消息
//
// trace_id 恢复：广播队列异步消费，原请求 ctx 已不可用。
// msg 在入队前已通过 InjectContext 注入 trace_id，此处从 msg 恢复到 ctx，
// 保证下游 HandleBroadcastMessage / handleDirectMessage / NotifyObservers 日志链路串联。
func (m *Manager) handleBroadcast(msg *models.HubMessage) {
	ctx := msg.ContextFrom(m.host.Context())

	// 通知观察者（异步，不阻塞主流程）
	// 路由来源优先级：
	//   1. msg 信封（入口层已注入，跨节点/异步队列场景时最可靠）—— 非空则直接用
	//   2. msg.Sender 的在线 client（发送者在哪个 ns/group 触发事件，就通知哪些订阅者）
	//      Observer 语义：订阅者关注"某个 ns/group 发生的事件"，因此发送者位置=路由
	//   3. 都为空 —— 全局 ns+空 group（通知全局观察者）
	//
	// 群组消息（GroupIDs 非空）跳过观察者通知：
	//   SendToGroup 已在群组级别统一通知观察者，此处若再通知会导致 N+1 重复
	//   （N=在线成员数：每个成员的 sendToUser 到 handleBroadcast 都会触发一次通知）
	//   BroadcastToGroupMembers 走 BroadcastToUserIDs 不经过 handleBroadcast，无此问题
	//   仅 P2P 消息（GroupIDs 为空）和全局广播需要在此通知观察者
	//
	// 全局广播（BroadcastTypeGlobal 且 msg.Namespace==""）保持全局语义：
	//   不 fallback 到 sender client，避免将全局广播的观察者通知错误收窄到 sender 的 ns。
	//   全局广播投递给所有 ns 的客户端，观察者通知也应保持全局（ns="" 仅通知全局观察者）。
	//   若收窄到 sender ns，其他 ns 的命名空间级观察者将收不到本应关注的全局事件。
	if len(msg.GroupIDs) == 0 && m.observerNotifyEnabled() {
		nsForObserver := msg.Namespace
		var gidsForObserver []string

		// 全局广播保持全局语义；P2P 消息 fallback 找 sender client 补齐 ns+group
		isGlobalBroadcast := msg.BroadcastType == models.BroadcastTypeGlobal && nsForObserver == ""
		if !isGlobalBroadcast {
			m.host.GetShardedRegistry().ForEachUserClient(msg.Sender, func(_ string, senderClient *models.Client) bool {
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
		m.NotifyObservers(observerCtx, msg)
	}

	if msg.BroadcastType == models.BroadcastTypeGlobal {
		m.HandleBroadcastMessage(ctx, msg)
		return
	}
	m.handleDirectMessage(ctx, msg)
}

// handleDirectMessage 处理点对点消息
//
// 性能：
//   - 指定 ReceiverClient 时走 GetClient O(1) 查找，避免遍历
//   - 未指定时走 ForEachUserClient 零拷贝遍历直接发送，避免 GetClientsCopyForUser 切片拷贝
//   - 消息预序列化一次，多设备复用
func (m *Manager) handleDirectMessage(ctx context.Context, msg *models.HubMessage) {
	// msg 路由信封由上游入口（sendToUser/handleBroadcast）已通过 InjectRoute 注入，此处直接读 msg 过滤

	// 预序列化一次（接收者多设备复用，消除循环内重复 Marshal）
	// 序列化失败时 data=nil，由 SendToClientSerialized 内部兜底
	data, _ := json.Marshal(msg)

	sent := 0
	if msg.ReceiverClient != "" {
		// 指定客户端：O(1) 查找，msgNamespace 非空时才做 namespace 匹配（防止跨 ns 串扰）
		// 不做 GroupIDs 系统组 vs 业务群匹配：ReceiverClient 已由发送方精准指定
		if client, ok := m.host.GetShardedRegistry().GetClient(msg.ReceiverClient); ok {
			if client != nil && (msg.Namespace == "" || client.Namespace == msg.Namespace) {
				m.SendToClientSerialized(ctx, client, msg, data)
				sent = 1
			}
		}
	} else {
		// 未指定客户端：遍历用户所有设备（ForEachUserClientFiltered 内部已按 msg.AppID/msg.Namespace 规则过滤）
		m.host.GetShardedRegistry().ForEachUserClientFiltered(msg.Receiver, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *models.Client) bool {
			m.SendToClientSerialized(ctx, client, msg, data)
			sent++
			return true
		})
	}

	// 本地直连投递统计：sent=0 是定位消息丢失的关键信号（在线判定 true 但本地无连接：
	// 用户刚断线/连接漂移，跨节点路径未覆盖时会静默黑洞）——异常路径保持 WARN 生产可见，
	// 成功路径走 DEBUG（千万连接规模下逐消息 INFO 是吞吐反模式）
	if sent > 0 {
		// 增加消息发送统计（原子计数器，由编排层定时刷写到 Redis）
		if m.host.GetStatsRepo() != nil {
			m.msgSentCount.Add(1)
		}
		m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] 本地直连投递完成",
			"message_id", msg.MessageID,
			"receiver", msg.Receiver,
			"delivered_clients", sent,
			"receiver_client", msg.ReceiverClient,
		)
	} else if m.SendToUserViaSSE(msg.Receiver, msg) {
		m.host.GetLogger().DebugContextKV(ctx, "[投递诊断] 本地直连投递完成（SSE 通道）",
			"message_id", msg.MessageID,
			"receiver", msg.Receiver,
		)
	} else {
		m.host.GetLogger().WarnContextKV(ctx, "[投递诊断] 本地直连投递 0 客户端（本地无该用户连接）",
			"message_id", msg.MessageID,
			"receiver", msg.Receiver,
			"receiver_client", msg.ReceiverClient,
			"hint", "在线判定为真但本地无连接：用户刚断线或连接在其他节点，请检查跨节点路由日志",
		)
	}

	// 多端同步：P2P 消息同步给发送者的其他设备（排除当前发送设备）
	// 群组消息（msg.GroupIDs 非空）跳过：SendToGroup 对每个成员投递都走 handleDirectMessage，
	// 每次都同步会导致发送者其他设备收到 N 条重复（N=群组成员数）；
	// 且群组场景 excludeSender 语义已决定发送者是否收自己的消息，多端同步会与之冲突
	if msg.Sender != "" && msg.SenderClient != "" && len(msg.GroupIDs) == 0 {
		m.syncToSenderDevices(ctx, msg, data)
	}
}

// HandleBroadcastMessage 处理广播消息
//
// 削峰填谷接线（与 broadcastToFiltered 同一套基座）：
//   - 准入闸门（分级水位）：拒绝时延迟队列重投（填谷）/ 转离线，拒绝不等于丢弃
//   - 出向整形（GCRA）：洪峰平滑扇出，被拒时进延迟队列；必达级跳过
//   - 重投目标 doBroadcastMessage 不重复准入（防死循环，与 broadcastToFilteredNow 同语义）
func (m *Manager) HandleBroadcastMessage(ctx context.Context, msg *models.HubMessage) {
	// 路由信封 + trace_id 同步（与所有入口共用同一套逻辑，幂等，已有不覆盖）
	// msg 可能来自跨节点 distMsg 未归一化，此处防御性兜底；namespace 保持原值（空=全局广播）
	ctx = msg.InjectRoute(ctx)

	// 准入闸门：分级水位裁决（必达级恒放行；普通/高频过载时延迟/离线路由——拒绝不等于丢弃）
	if verdict := m.host.AdmitMessage(msg, true); verdict != overload.VerdictAdmit {
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.doBroadcastMessage(c, bm)
		})
		return
	}

	// 出向整形：每条广播 1 令牌平滑扇出（必达级跳过——控制面不受数据面整形影响）
	if shaper := m.host.GetBroadcastShaper(); shaper != nil &&
		msg.ResolveGuarantee() != models.GuaranteeGuaranteed &&
		!shaper.Allow() {
		m.host.GetOverloadMetrics().RecordShaperDenied()
		m.host.DeferBroadcast(ctx, msg, func(c context.Context, bm *models.HubMessage) {
			m.doBroadcastMessage(c, bm)
		})
		return
	}

	m.doBroadcastMessage(ctx, msg)
}

// doBroadcastMessage 广播扇出（准入/整形裁决后的执行体，延迟队列重投的目标函数）
func (m *Manager) doBroadcastMessage(ctx context.Context, msg *models.HubMessage) {
	start := time.Now()
	if m.host.GetStatsRepo() != nil {
		m.broadcastSentCount.Add(1)
	}

	// 预序列化消息（仅一次，所有客户端复用）
	data, err := json.Marshal(msg)
	if err != nil {
		m.host.GetLogger().ErrorContextKV(ctx, "广播消息序列化失败", "error", err)
		return
	}
	marshalDuration := time.Since(start)

	msgID := mathx.IfNotEmpty(msg.MessageID, msg.ID)
	dataLen := len(data)

	// 送达分级在扇出循环外解析一次（循环不变量外提）：全局广播遍历全量连接，
	// 逐客户端 ResolveGuarantee（读锁 + 决策树）是百万级无谓开销，分级对同一 msg 恒定
	guarantee := msg.ResolveGuarantee()

	// 并发数快照
	registry := m.host.GetShardedRegistry()
	totalWSClients := registry.GetClientCount()
	totalSSEClients := registry.GetSSEClientCount()

	// 遍历所有客户端，仅投递路由匹配（namespace+group）的设备
	// ns1 的广播不会投递给 ns2 的用户，避免跨租户串扰
	// 使用并行遍历优化百万级连接广播性能（原子计数线程安全）
	var successCount int32
	var failCount int32
	var scanned int64

	wsStart := time.Now()
	registry.ForEachClientFilteredParallel(0, msg.AppID, msg.Namespace, msg.GroupIDs, func(_ string, client *models.Client) {
		atomic.AddInt64(&scanned, 1)
		if client.IsClosed() || client.ConnectionType == models.ConnectionTypeSSE {
			return
		}
		// 分级兜底投递：TrySend 失败按分级路由（普通/必达转离线，高频语义丢弃——拒绝不等于丢弃）
		if m.TrySendWithFallback(client, data, msg, guarantee) {
			atomic.AddInt32(&successCount, 1)
			m.host.TrackReceiverMessageStats(client.ID, client.UserType, dataLen)
		} else {
			atomic.AddInt32(&failCount, 1)
		}
	})
	wsDuration := time.Since(wsStart)

	// 消息记录状态只更新一次（同一 msgID，无需每客户端都更新；广播记录 receiver 为空）
	if atomic.LoadInt32(&successCount) > 0 {
		m.updateMessageStatusAsync(ctx, msgID, "", models.MessageSendStatusSuccess, "", "")
	}

	if atomic.LoadInt32(&failCount) > 0 {
		m.host.GetLogger().WarnContextKV(ctx, "广播消息：部分客户端发送失败",
			"success_count", atomic.LoadInt32(&successCount),
			"fail_count", atomic.LoadInt32(&failCount),
			"message_id", msg.MessageID,
		)
	}

	// SSE 客户端通过专用通道发送
	sseStart := time.Now()
	m.BroadcastToSSEClients(msg)
	sseDuration := time.Since(sseStart)

	totalDuration := time.Since(start)
	m.host.GetLogger().DebugContextKV(ctx, "广播消息完成",
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

// ============================================================================
// 日志辅助
// ============================================================================

// logWithClient 带客户端字段的 KV 日志（连接级 trace 优先，宿主 ctx 兜底）
func (m *Manager) logWithClient(level logger.LogLevel, msg string, client *models.Client, extraFields ...interface{}) {
	fields := []interface{}{
		"client_id", client.ID,
		"user_id", client.UserID,
		"user_type", client.UserType,
		"client_ip", client.ClientIP,
	}
	fields = append(fields, extraFields...)

	// 优先使用 client.Context（携带连接级 trace_id），fallback 到宿主 ctx
	ctx := client.Context
	if ctx == nil {
		ctx = m.host.Context()
	}

	log := m.host.GetLogger()
	switch level {
	case logger.INFO:
		log.InfoContextKV(ctx, msg, fields...)
	case logger.WARN:
		log.WarnContextKV(ctx, msg, fields...)
	case logger.ERROR:
		log.ErrorContextKV(ctx, msg, fields...)
	case logger.DEBUG:
		log.DebugContextKV(ctx, msg, fields...)
	}
}

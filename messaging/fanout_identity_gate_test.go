/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-22 00:16:00
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-27 10:26:55
 * @FilePath: \go-wsc\messaging\fanout_identity_gate_test.go
 * @Description: 扇出路径的逐收件人身份门与 Debug 门控等价性门
 *
 * 本文件锁定两类不变量，二者都是扇出分配优化的安全边界：
 *
 * 一、逐收件人身份（Receiver / ID 必须各不相同，MessageID 必须相同）
 *   Receiver 是 json:"receiver" 且无 omitempty，跨六个下游服务可见，属 wire contract。
 *   「一次 Marshal 多人共享」能把每收件人 12% 的分配降到零，但会让所有人收到同一个
 *   Receiver。危害不止于显示错位：ack_timer.go:118 与 node_ack_timeout.go:130 会把
 *   blob 反序列化后交给 StoreOfflineOnDeliveryFailure，后者以 msg.Receiver 作离线存储
 *   键（record_writer.go），共享 blob 会把离线消息存到错误的用户名下——投错，
 *   比显示错位严重一个量级。故本门必须长期拦住该优化
 *
 *   同时锁定 MessageID 必须共享：ACK 以 MessageID 为键（ack.go:150、dispatcher.go:283/316），
 *   而非逐收件人的 ID。若哪天 MessageID 变成逐人不同，可靠群发的 ACK 将永不收敛
 *
 * 二、Debug 日志门控与 logger 内部过滤器等价
 *   送达路径上 6 处 DebugContextKV 的 ...any 实参在调用点装箱并逃逸，占扇出全部分配
 *   53.25%，而生产与基准均跑 Info 级、这些实参构造完即弃。调用点加 IsLevelEnabled(DEBUG)
 *   门控后分配消失，前提是该谓词与 logger 自身的过滤条件严格等价，否则等于静默丢日志
 *
 * Copyright (c) 2026 by kamalyes 501893067@qq.com, All Rights Reserved.
 */

package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kamalyes/go-logger"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	"github.com/kamalyes/go-wsc/routing"
	"github.com/kamalyes/go-wsc/spi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// 一、逐收件人身份门
// ============================================================================

// collectDeliveredFrames 从每个收件人的 SendChan 取出其收到的帧
//
// SendChan 容量 16（fake_host_test.go:491），小规模收件人无需常驻排空协程。
func collectDeliveredFrames(t *testing.T, clients map[string]*models.Client) map[string][]byte {
	t.Helper()
	frames := make(map[string][]byte, len(clients))
	for uid, c := range clients {
		select {
		case raw := <-c.SendChan:
			frames[uid] = raw
		case <-time.After(2 * time.Second):
			t.Fatalf("用户 %s 未收到任何帧", uid)
		}
	}
	return frames
}

// checkPerRecipientIdentity 校验每个收件人收到的信封身份字段
//
// 返回 error 而非直接 t.Fatal，使同一套判定既能作正向门，也能用于反向验证
// （TestFanout_SharedEnvelopeFailsIdentityGate 以共享信封喂同一函数，必须报错）。
func checkPerRecipientIdentity(frames map[string][]byte) error {
	seenReceiver := make(map[string]string, len(frames))
	for uid, raw := range frames {
		var got models.HubMessage
		if err := json.Unmarshal(raw, &got); err != nil {
			return fmt.Errorf("用户 %s 的帧无法反序列化: %w", uid, err)
		}
		// 核心断言：收件人必须看到自己的 Receiver
		if got.Receiver != uid {
			return fmt.Errorf("用户 %s 收到的 receiver=%q，投递身份错位（共享信封的典型症状）", uid, got.Receiver)
		}
		// Receiver 必须在收件人之间互不相同
		if prev, dup := seenReceiver[got.Receiver]; dup {
			return fmt.Errorf("receiver=%q 同时出现在用户 %s 与 %s 的帧中，信封被共享", got.Receiver, prev, uid)
		}
		seenReceiver[got.Receiver] = uid
	}
	return nil
}

// registerMembers 注册 n 个本地在线成员，返回 (userIDs, userID->client)
func registerMembers(t *testing.T, host *fakeHost, n int) ([]string, map[string]*models.Client) {
	t.Helper()
	ids := make([]string, 0, n)
	clients := make(map[string]*models.Client, n)
	registry := host.GetShardedRegistry()
	for i := 0; i < n; i++ {
		uid := fmt.Sprintf("u-%06d", i)
		c := makeTestClient(fmt.Sprintf("c-%06d", i), uid)
		registry.AddClient(c)
		ids = append(ids, uid)
		clients[uid] = c
	}
	return ids, clients
}

// TestFanout_EachRecipientGetsOwnReceiver 多收件人扇出：每人必须收到自己的 Receiver
//
// 这是指令要求的强制门，且必须是多收件人场景——单收件人无法暴露共享信封。
func TestFanout_EachRecipientGetsOwnReceiver(t *testing.T) {
	const members = 5
	m, host := newTestManager()
	ids, clients := registerMembers(t, host, members)

	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())

	msg := makeGroupMessage("sender-identity")
	result := m.SendToGroupMembers(ctx, ids, msg, false)
	require.Equal(t, members, result.Success, "全员本地在线，应全部投递成功")

	frames := collectDeliveredFrames(t, clients)
	require.Len(t, frames, members, "每个收件人都应恰好收到一帧")
	require.NoError(t, checkPerRecipientIdentity(frames), "逐收件人身份门失败")
}

// TestDeliverToGroupReliable_IdentityAndAckKey 可靠群发：Receiver 逐人不同、MessageID 必须共享
//
// 指令强制门之二：ACK 必须在多收件人可靠群发下正确收敛。ACK 以 MessageID 为键
// （ack.go:150、dispatcher.go:283/316），因此「MessageID 在收件人之间严格相同」
// 正是 ACK 可收敛的充要条件，而逐人不同的 ID 不参与 ACK。
func TestDeliverToGroupReliable_IdentityAndAckKey(t *testing.T) {
	const members = 5
	m, host := newTestManager()
	groupRepo := newFakeGroupStore()
	host.groupRepo = groupRepo

	ids, clients := registerMembers(t, host, members)

	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs([]string{"g-identity"}).
		Inject(context.Background())
	require.NoError(t, groupRepo.AddMembers(
		context.Background(), routing.AppIDFromContext(ctx),
		constants.DefaultNamespace, "g-identity", ids))

	msg := makeGroupMessage("sender-reliable")
	// 显式设定 MessageID：真实可靠群发由入口统一赋值，全体收件人共享同一 ACK 键
	msg.MessageID = "msg-ack-shared-001"

	m.deliverToGroupReliable(ctx, msg, false)

	frames := collectDeliveredFrames(t, clients)
	require.Len(t, frames, members)
	require.NoError(t, checkPerRecipientIdentity(frames), "可靠群发的逐收件人身份门失败")

	// ACK 键必须在全体收件人间严格一致，否则 ACK 永不收敛
	ids4ack := make(map[string]struct{}, members)
	for uid, raw := range frames {
		var got models.HubMessage
		require.NoError(t, json.Unmarshal(raw, &got))
		assert.Equal(t, "msg-ack-shared-001", got.MessageID,
			"用户 %s 的 MessageID 被改写，ACK 将无法按原键收敛", uid)
		// 逐收件人 ID 应互不相同（send.go:257 以 toUserID 作前缀）
		ids4ack[got.ID] = struct{}{}
	}
	assert.Len(t, ids4ack, members, "逐收件人 ID 应互不相同（send.go:257）")
}

// TestFanout_SharedEnvelopeFailsIdentityGate 反向验证：故意共享信封，身份门必须报错
//
// 指令要求「反向验证至少一个门」。此处不改生产代码，而是直接构造「一次 Marshal
// 多人共享」会产生的帧集合，喂给与正向门完全相同的 checkPerRecipientIdentity：
// 若该函数此时仍然通过，说明正向门是空断言、拦不住共享信封优化。
func TestFanout_SharedEnvelopeFailsIdentityGate(t *testing.T) {
	const members = 5

	// 模拟共享 blob：一次序列化，Receiver 固定为第一个收件人，分发给所有人
	shared := makeGroupMessage("sender-shared")
	shared.MessageID = "msg-shared-001"
	shared.Receiver = "u-000000"
	shared.ID = "u-000000-snowflake"
	raw, err := json.Marshal(shared)
	require.NoError(t, err)

	frames := make(map[string][]byte, members)
	for i := 0; i < members; i++ {
		frames[fmt.Sprintf("u-%06d", i)] = raw // 同一份字节，全员共享
	}

	err = checkPerRecipientIdentity(frames)
	require.Error(t, err, "共享信封竟通过了身份门——正向门是空断言，拦不住 Receiver 错位")
	t.Logf("反向验证按预期失败: %v", err)
}

// ============================================================================
// 二、Debug 门控等价性门
// ============================================================================

// TestDebugGate_PredicateMatchesLoggerFilter IsLevelEnabled(DEBUG) 必须与 logger 自身过滤条件等价
//
// 等价性是构造性的，而非巧合：
//   - Logger.DebugContextKV   (logger.go:1224): if LogLevel(l.level.Load()) > DEBUG { return }
//   - Logger.IsLevelEnabled   (types.go:329):   return DEBUG >= LogLevel(l.level.Load())
//
// 「level > DEBUG 则丢弃」正是「DEBUG >= level」的否命题，两者同一谓词。
// fieldLogger 更直接：其 DebugContextKV (logger.go:1816) 字面调用
// !f.logger.IsLevelEnabled(DEBUG)，与门控是同一个函数。
//
// 本测试把该等价性钉成回归：若 go-logger 升级后二者分叉，门控就会开始静默丢日志。
func TestDebugGate_PredicateMatchesLoggerFilter(t *testing.T) {
	levels := []struct {
		level     logger.LogLevel
		wantDebug bool
	}{
		{logger.DEBUG, true},
		{logger.INFO, false},
		{logger.WARN, false},
		{logger.ERROR, false},
	}
	for _, lv := range levels {
		lg := logger.NewLogger().WithLevel(lv.level).WithShowCaller(false)
		assert.Equal(t, lv.wantDebug, lg.IsLevelEnabled(logger.DEBUG),
			"级别 %v 下 IsLevelEnabled(DEBUG) 与 DebugContextKV 的内部过滤条件不一致", lv.level)
	}
}

// debugSpy 记录实际抵达 DebugContextKV 的日志，并可独立控制 DEBUG 是否启用
//
// 嵌入 spi.Logger 以继承其余方法，仅覆写门控所依赖的两个方法。
type debugSpy struct {
	spi.Logger // 其余方法委派给真实日志器，避免 nil 方法集 panic

	debugEnabled bool

	mu          sync.Mutex
	emitted     []string
	levelChecks int // 门控咨询次数：为 0 说明调用点根本没走 IsLevelEnabled
}

func newDebugSpy(enabled bool) *debugSpy {
	return &debugSpy{
		Logger:       logger.NewLogger().WithLevel(logger.ERROR).WithShowCaller(false),
		debugEnabled: enabled,
	}
}

func (s *debugSpy) IsLevelEnabled(level logger.LogLevel) bool {
	if level == logger.DEBUG {
		s.mu.Lock()
		s.levelChecks++
		s.mu.Unlock()
		return s.debugEnabled
	}
	return s.Logger.IsLevelEnabled(level)
}

func (s *debugSpy) checks() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.levelChecks
}

func (s *debugSpy) DebugContextKV(ctx context.Context, msg string, keysAndValues ...any) {
	s.mu.Lock()
	s.emitted = append(s.emitted, msg)
	s.mu.Unlock()
}

func (s *debugSpy) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.emitted)
}

// distinct 返回去重后的日志文案及各自次数，失败时据此定位未门控的调用点
func (s *debugSpy) distinct() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]int, len(s.emitted))
	for _, msg := range s.emitted {
		out[msg]++
	}
	return out
}

// runFanoutWithSpy 以 spy 日志器跑一次多成员扇出，返回 spy
func runFanoutWithSpy(t *testing.T, enabled bool) *debugSpy {
	t.Helper()
	const members = 3
	m, host := newTestManager()
	spy := newDebugSpy(enabled)
	host.logger = spy

	ids, clients := registerMembers(t, host, members)
	ctx := routing.NewRoute().
		WithAppID("").
		WithNamespace(constants.DefaultNamespace).
		WithGroupIDs(nil).
		Inject(context.Background())

	result := m.SendToGroupMembers(ctx, ids, makeGroupMessage("sender-spy"), false)
	require.Equal(t, members, result.Success)
	collectDeliveredFrames(t, clients) // 排空，确保投递链路跑完

	return spy
}

// TestDebugGate_EmitsWhenDebugEnabled DEBUG 开启时门控不得吞掉日志
//
// 这是门控改动的核心风险面：若 IsLevelEnabled 判反或写错，诊断日志会在真正需要时消失。
func TestDebugGate_EmitsWhenDebugEnabled(t *testing.T) {
	spy := runFanoutWithSpy(t, true)
	assert.Positive(t, spy.count(),
		"DEBUG 已启用，扇出路径却未产生任何 Debug 日志——门控把日志吞掉了")
}

// TestDebugGate_ElidesWhenDebugDisabled DEBUG 关闭时门控必须完全短路
//
// 反面：若门控失效，DebugContextKV 仍被调用，...any 实参在调用点的装箱分配照旧发生，
// 优化即归零（这正是优化前的状态：53.25% 的扇出分配来自这些被丢弃的实参）。
func TestDebugGate_ElidesWhenDebugDisabled(t *testing.T) {
	spy := runFanoutWithSpy(t, false)
	t.Logf("门控咨询 IsLevelEnabled(DEBUG) 共 %d 次", spy.checks())
	if n := spy.count(); n != 0 {
		t.Errorf("DEBUG 已关闭，DebugContextKV 仍被调用 %d 次——以下调用点未加级别门控，"+
			"其 ...any 实参仍在调用点装箱后被丢弃：", n)
		for msg, c := range spy.distinct() {
			t.Errorf("  × %d 次: %s", c, msg)
		}
	}
}

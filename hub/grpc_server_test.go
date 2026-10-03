/*
 * @Author: kamalyes 501893067@qq.com
 * @Date: 2026-10-03 11:05:07
 * @LastEditors: kamalyes 501893067@qq.com
 * @LastEditTime: 2026-10-03 11:05:07
 * @FilePath: \go-wsc\hub\grpc_server_test.go
 * @Description: 节点间 gRPC 服务端测试 —— 16 个入口方法与生命周期
 *
 * 覆盖：
 *   - NodeService 全部 handler（SendToUser/CheckUsersOnline/BroadcastGroup/
 *     NotifyObservers/KickUser/Ping/BatchDispatch/dispatchBatchItem）：
 *     经 NewGRPCServer(hub) 直调（不绑端口），真实 Hub + 分片注册表
 *   - 路由信封隔离语义（appID/namespace/groupIDs 经 incoming metadata 恢复）
 *   - resolveAdvertisedGRPCAddr 纯函数表驱动（通配地址转换，与
 *     cluster_dispatch_test.go 的 TestResolveAdvertisedGRPCAddr 互补）
 *   - Start/Stop 冒烟：空闲端口启动 + 真实 dial Ping 往返 + 端口占用失败分支
 *   - InitNodeGRPC/wireNodeEvictionCleanup/startNodeGRPC/stopNodeGRPC 生命周期
 *     （miniredis 真实链路：注册/发现/幽灵节点淘汰联动回收连接）
 *
 * 与既有测试的关系：
 *   - cluster_dispatch_test.go 覆盖 dispatchViaGRPC（客户端侧路由）与
 *     resolveAdvertisedGRPCAddr 的通配场景，此处不重复
 *   - grpc_delivery_result_test.go 覆盖 SendToUser 的过载拒绝分支
 *     （admission ResourceExhausted），此处补在线投递/全离线两主分支
 *
 * Copyright (c) 2026 by kamalyes, All Rights Reserved.
 */

package hub

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kamalyes/go-cachex"
	wscconfig "github.com/kamalyes/go-config/pkg/wsc"
	"github.com/kamalyes/go-toolbox/pkg/netx"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	wscpb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/routing"
)

// ============================================================================
// 构造 helper
// ============================================================================

// newGRPCServerTestHub 构造服务端 handler 测试用 Hub：
// 真实注册表/域管理器齐备（NewHub 全量组装），观察者索引启用
// （NotifyObservers 用例依赖 EnableObserver），node-grpc 未启用（直调 handler 不依赖网络）
func newGRPCServerTestHub() *Hub {
	return NewHub(&wscconfig.WSC{
		ClientTimeout:  time.Minute,
		EnableObserver: true,
	})
}

// newServerTestClient 构造带缓冲 SendChan 的普通客户端并注册进分片注册表
// （AppID/Namespace 取 NewClient 默认值：DefaultAppID/DefaultNamespace）
func newServerTestClient(hub *Hub, id, userID string) *models.Client {
	c := models.NewClient(id, userID, models.UserTypeCustomer).
		WithSendChan(make(chan []byte, 4))
	hub.shardedRegistry.AddClient(c)
	return c
}

// newServerTestGroupClient 构造指定 appID + 群组成员客户端：
// AddClient 时建立 (appID, groupID) 群组本地反向索引（BroadcastGroup 成员定位依赖）
func newServerTestGroupClient(hub *Hub, id, userID, appID string, groupIDs ...string) *models.Client {
	c := models.NewClient(id, userID, models.UserTypeCustomer).
		WithSendChan(make(chan []byte, 4))
	c.AppID = appID
	c.GroupIDs = groupIDs
	hub.shardedRegistry.AddClient(c)
	return c
}

// mustMarshalServerMessage 序列化 HubMessage 为 gRPC 请求体（失败即终止）
func mustMarshalServerMessage(t *testing.T, msg *models.HubMessage) []byte {
	t.Helper()
	raw, err := wscpb.MarshalHubMessage(msg)
	require.NoError(t, err)
	return raw
}

// newServerRouteCtx 构造携带路由信封（appID/namespace/groupIDs）的 incoming
// metadata ctx，模拟真实 gRPC 链路中经 RestoreFromIncomingMetadata 的恢复来源
func newServerRouteCtx(appID, namespace string, groupIDs ...string) context.Context {
	pairs := []string{constants.MetadataKeyAppID, appID}
	if namespace != "" {
		pairs = append(pairs, constants.MetadataKeyNamespace, namespace)
	}
	if len(groupIDs) > 0 {
		pairs = append(pairs, constants.MetadataKeyGroupIDs, strings.Join(groupIDs, ","))
	}
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pairs...))
}

// ============================================================================
// SendToUser —— 点对点投递
// ============================================================================

// TestGRPCServerSendToUserOnlineDelivery 本地在线投递分支：
// 用户在线 → 预序列化消息直投 SendChan，响应 Success/UserOnline 双 true
func TestGRPCServerSendToUserOnlineDelivery(t *testing.T) {
	hub := newGRPCServerTestHub()
	c := newServerTestClient(hub, "c-s2u-1", "u-s2u")

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetReceiver("u-s2u").
		SetContent("grpc send online")
	msg.AppID = constants.DefaultAppID
	msg.Namespace = constants.DefaultNamespace

	resp, err := NewGRPCServer(hub).SendToUser(context.Background(),
		&wscpb.SendToUserRequest{UserId: "u-s2u", MessageData: mustMarshalServerMessage(t, msg)})

	require.NoError(t, err)
	assert.True(t, resp.GetSuccess(), "在线投递应成功")
	assert.True(t, resp.GetUserOnline(), "用户在本节点在线")
	assert.Empty(t, resp.GetError())

	// 客户端收到一帧预序列化投递（所有设备复用同一份字节）
	select {
	case data := <-c.SendChan:
		assert.NotEmpty(t, data)
	default:
		t.Fatal("在线客户端应收到投递帧")
	}

	// 非法消息体（空数据反序列化失败）→ InvalidArgument
	_, err = NewGRPCServer(hub).SendToUser(context.Background(),
		&wscpb.SendToUserRequest{UserId: "u-s2u"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "非法消息体应返回 InvalidArgument")
}

// TestGRPCServerSendToUserAllOffline 全离线分支：
// 用户不在线 → 业务响应 Success=false/UserOnline=false（非 RPC 错误，
// 发送方据此触发重路由决策）；在线但投递全拒（连接已关闭）→ ResourceExhausted
func TestGRPCServerSendToUserAllOffline(t *testing.T) {
	hub := newGRPCServerTestHub()
	srv := NewGRPCServer(hub)

	msg := models.NewHubMessage().SetMessageType(models.MessageTypeText).SetReceiver("u-gone")
	msg.AppID = constants.DefaultAppID

	// 全离线：扑空是业务响应（selfHeal 在 onlineStatusRepo 未注入时 nil-safe 降级）
	resp, err := srv.SendToUser(context.Background(),
		&wscpb.SendToUserRequest{UserId: "u-gone", MessageData: mustMarshalServerMessage(t, msg)})
	require.NoError(t, err, "用户不在线应为业务响应而非 RPC 错误")
	assert.False(t, resp.GetSuccess())
	assert.False(t, resp.GetUserOnline())
	assert.Equal(t, "用户不在线", resp.GetError())

	// 在线但全部拒绝（客户端已标记关闭）：delivered==0 → ResourceExhausted
	closed := newServerTestClient(hub, "c-s2u-closed", "u-closed")
	closed.MarkClosed()
	_, err = srv.SendToUser(context.Background(),
		&wscpb.SendToUserRequest{UserId: "u-closed", MessageData: mustMarshalServerMessage(t, msg)})
	assert.Equal(t, codes.ResourceExhausted, status.Code(err), "在线客户端全拒投递应返回 ResourceExhausted")
}

// ============================================================================
// CheckUsersOnline —— 批量在线探测
// ============================================================================

// TestGRPCServerCheckUsersOnline 批量在线检查：混合在线/离线 + 路由信封隔离
// （其他 appID 信封下同名用户判离线，跨 app 不串扰）
func TestGRPCServerCheckUsersOnline(t *testing.T) {
	hub := newGRPCServerTestHub()
	newServerTestClient(hub, "c-chk-1", "u-chk-online")

	req := &wscpb.CheckUsersOnlineRequest{UserIds: []string{"u-chk-online", "u-chk-offline"}}
	srv := NewGRPCServer(hub)

	// 无路由 ctx：appID 退化 DefaultAppID，默认信封客户端命中
	resp, err := srv.CheckUsersOnline(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"u-chk-online": true, "u-chk-offline": false}, resp.GetOnlineUsers())

	// 信封隔离：ctx 携带其他 appID 路由（无 incoming metadata 时 Restore 保持原路由）
	otherAppCtx := routing.NewRoute().
		WithAppID("app-other").
		WithNamespace("ns-other").
		Inject(context.Background())
	resp, err = srv.CheckUsersOnline(otherAppCtx, req)
	require.NoError(t, err)
	assert.False(t, resp.GetOnlineUsers()["u-chk-online"], "跨 app 信封不应命中 DefaultAppID 客户端")
	assert.False(t, resp.GetOnlineUsers()["u-chk-offline"])
}

// ============================================================================
// BroadcastGroup —— 群组广播
// ============================================================================

// TestGRPCServerBroadcastGroup 群组广播三分支：
// 本地反向索引成员扇出 / 排除发送者 / 空 groupID 短路（Delivered=0 不触发成员查询）
func TestGRPCServerBroadcastGroup(t *testing.T) {
	hub := newGRPCServerTestHub()
	const (
		appID   = "app-bc"
		groupID = "g-srv"
	)
	m1 := newServerTestGroupClient(hub, "c-bc-1", "u-bc-1", appID, groupID)
	m2 := newServerTestGroupClient(hub, "c-bc-2", "u-bc-2", appID, groupID)
	outsider := newServerTestGroupClient(hub, "c-bc-3", "u-bc-3", appID, "g-other")

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetContent("group broadcast")
	msg.AppID = appID
	raw := mustMarshalServerMessage(t, msg)
	srv := NewGRPCServer(hub)

	// 群组成员扇出：路由信封（appID+单群组ID）经 incoming metadata 恢复
	resp, err := srv.BroadcastGroup(newServerRouteCtx(appID, "", groupID),
		&wscpb.BroadcastGroupRequest{MessageData: raw})
	require.NoError(t, err)
	assert.EqualValues(t, 2, resp.GetDelivered(), "两名本地成员均应投递")
	for _, c := range []*models.Client{m1, m2} {
		select {
		case data := <-c.SendChan:
			assert.NotEmpty(t, data)
		default:
			t.Fatalf("群组成员 %s 未收到广播", c.ID)
		}
	}
	select {
	case <-outsider.SendChan:
		t.Fatal("非目标群组成员不应收到广播")
	default:
	}

	// 排除发送者：多端同步场景仅投递其余成员
	resp, err = srv.BroadcastGroup(newServerRouteCtx(appID, "", groupID),
		&wscpb.BroadcastGroupRequest{MessageData: raw, ExcludeSender: true, SenderId: "u-bc-1"})
	require.NoError(t, err)
	assert.EqualValues(t, 1, resp.GetDelivered(), "排除发送者后仅一名成员投递")
	select {
	case <-m1.SendChan:
		t.Fatal("被排除的发送者不应收到广播")
	default:
	}
	select {
	case <-m2.SendChan:
	default:
		t.Fatal("未排除的成员应收到广播")
	}

	// 空 groupID：短路返回 Delivered=0（不查群组成员）
	resp, err = srv.BroadcastGroup(newServerRouteCtx(appID, ""),
		&wscpb.BroadcastGroupRequest{MessageData: raw})
	require.NoError(t, err)
	assert.Zero(t, resp.GetDelivered(), "空 groupID 应短路返回 0")

	// 非法消息体 → InvalidArgument
	_, err = srv.BroadcastGroup(newServerRouteCtx(appID, "", groupID),
		&wscpb.BroadcastGroupRequest{})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ============================================================================
// NotifyObservers —— 观察者通知
// ============================================================================

// TestGRPCServerNotifyObservers 观察者通知：命名空间级观察者命中 /
// 命名空间不匹配不通知 / 非法消息体 InvalidArgument
func TestGRPCServerNotifyObservers(t *testing.T) {
	hub := newGRPCServerTestHub()
	srv := NewGRPCServer(hub)

	// 命名空间级观察者（观察 ns-obs 下所有群组）
	obs := models.NewClient("obs-ns-1", "u-obs-1", models.UserTypeObserver).
		WithSendChan(make(chan []byte, 1))
	obs.Namespace = "ns-obs"
	hub.shardedRegistry.AddClient(obs)

	msg := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetContent("observer notify")
	msg.AppID = constants.DefaultAppID
	msg.Namespace = "ns-obs" // 消息体信封优先（InjectRoute 不覆盖已有有效值）

	resp, err := srv.NotifyObservers(context.Background(),
		&wscpb.NotifyObserversRequest{MessageData: mustMarshalServerMessage(t, msg)})
	require.NoError(t, err)
	assert.EqualValues(t, 1, resp.GetNotified(), "命名空间观察者应被通知")
	select {
	case data := <-obs.SendChan:
		assert.NotEmpty(t, data, "观察者应收到带旁路标记的投递")
	default:
		t.Fatal("观察者未收到通知")
	}

	// 命名空间不匹配：观察者不在目标命名空间
	other := models.NewHubMessage().
		SetMessageType(models.MessageTypeText).
		SetContent("observer notify other ns")
	other.AppID = constants.DefaultAppID
	other.Namespace = "ns-other"
	resp, err = srv.NotifyObservers(context.Background(),
		&wscpb.NotifyObserversRequest{MessageData: mustMarshalServerMessage(t, other)})
	require.NoError(t, err)
	assert.Zero(t, resp.GetNotified(), "命名空间不匹配的观察者不应被通知")

	// 非法消息体 → InvalidArgument
	_, err = srv.NotifyObservers(context.Background(), &wscpb.NotifyObserversRequest{})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

// ============================================================================
// KickUser —— 跨节点踢人
// ============================================================================

// TestGRPCServerKickUser 踢人三分支：在线踢下线（异步注销终态可见）/
// 离线幂等（Success=true + 0 连接）/ 路由信封隔离（跨 app 不误踢）
func TestGRPCServerKickUser(t *testing.T) {
	hub := newGRPCServerTestHub()
	srv := NewGRPCServer(hub)

	// 在线踢下线：Success + 连接数 1，注册表异步清空
	newServerTestClient(hub, "c-kick", "u-kick")
	resp, err := srv.KickUser(context.Background(),
		&wscpb.KickUserRequest{UserId: "u-kick", Reason: "test-kick"})
	require.NoError(t, err)
	assert.True(t, resp.GetSuccess())
	assert.EqualValues(t, 1, resp.GetKickedConnections())
	require.Eventually(t, func() bool {
		_, ok := hub.shardedRegistry.GetClient("c-kick")
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "被踢连接应从注册表移除（异步注销）")

	// 离线幂等：用户已不在线 = 踢出目标已达成（Success=true + 0 连接）
	resp, err = srv.KickUser(context.Background(), &wscpb.KickUserRequest{UserId: "u-kick"})
	require.NoError(t, err)
	assert.True(t, resp.GetSuccess(), "离线踢人应为幂等成功")
	assert.Zero(t, resp.GetKickedConnections())

	// 信封隔离校验：其他 appID 信封下同名用户不被误踢（远端 kick 仅踢本节点匹配信封连接）
	newServerTestClient(hub, "c-kick-2", "u-kick-2")
	otherAppCtx := routing.NewRoute().
		WithAppID("app-other").
		WithNamespace("ns-other").
		Inject(context.Background())
	resp, err = srv.KickUser(otherAppCtx, &wscpb.KickUserRequest{UserId: "u-kick-2"})
	require.NoError(t, err)
	assert.True(t, resp.GetSuccess(), "信封内无连接亦为幂等成功")
	assert.Zero(t, resp.GetKickedConnections(), "跨 app 信封不应踢出 DefaultAppID 连接")
	if _, ok := hub.shardedRegistry.GetClient("c-kick-2"); !ok {
		t.Fatal("跨 app 信封的在线校验失败：连接不应被移除")
	}
}

// ============================================================================
// Ping —— 节点健康检查
// ============================================================================

// TestGRPCServerPing 健康检查：回显节点 ID / 活跃连接数（原子计数）/
// 健康位随停机状态翻转
func TestGRPCServerPing(t *testing.T) {
	hub := newGRPCServerTestHub()
	newServerTestClient(hub, "c-ping-1", "u-ping-1")
	newServerTestClient(hub, "c-ping-2", "u-ping-2")

	resp, err := NewGRPCServer(hub).Ping(context.Background(), &wscpb.PingRequest{NodeId: "caller"})
	require.NoError(t, err)
	assert.Equal(t, hub.GetNodeID(), resp.GetNodeId())
	assert.EqualValues(t, 2, resp.GetActiveConnections(), "活跃连接数应与注册表计数一致")
	assert.True(t, resp.GetHealthy(), "非停机状态节点健康")

	// 停机后健康位置 false（IsShutdown 语义，不触发完整 SafeShutdown 链路）
	hub.shutdown.Store(true)
	resp, err = NewGRPCServer(hub).Ping(context.Background(), &wscpb.PingRequest{})
	require.NoError(t, err)
	assert.False(t, resp.GetHealthy(), "停机中节点应上报不健康")
}

// ============================================================================
// BatchDispatch / dispatchBatchItem —— 微批合帧投递
// ============================================================================

// TestGRPCServerBatchDispatchMixed 多 item 混合操作的结果归集：
// 路由信封内嵌于指令体（写入 incoming metadata 复用单指令处理器），
// 结果与请求 items 一一对应、单条失败不中断整批
func TestGRPCServerBatchDispatchMixed(t *testing.T) {
	hub := newGRPCServerTestHub()
	srv := NewGRPCServer(hub)

	// u-bd-send 在线两设备；u-bd-miss 离线；u-bd-kick 待踢；
	// g-batch 两成员（含待排除的发送者）；ns-batch 命名空间观察者
	newServerTestClient(hub, "c-bd-1", "u-bd-send")
	newServerTestClient(hub, "c-bd-2", "u-bd-send")
	newServerTestGroupClient(hub, "c-bd-g1", "u-bd-g1", constants.DefaultAppID, "g-batch")
	newServerTestGroupClient(hub, "c-bd-g2", "u-bd-g2", constants.DefaultAppID, "g-batch")
	kickTarget := newServerTestClient(hub, "c-bd-kick", "u-bd-kick")
	obs := models.NewClient("obs-bd", "u-obs-bd", models.UserTypeObserver).
		WithSendChan(make(chan []byte, 1))
	obs.Namespace = "ns-batch"
	hub.shardedRegistry.AddClient(obs)

	sendMsg := models.NewHubMessage().SetMessageType(models.MessageTypeText).SetReceiver("u-bd-send")
	sendMsg.AppID = constants.DefaultAppID
	sendMsg.Namespace = constants.DefaultNamespace
	missMsg := models.NewHubMessage().SetMessageType(models.MessageTypeText).SetReceiver("u-bd-miss")
	missMsg.AppID = constants.DefaultAppID
	groupMsg := models.NewHubMessage().SetMessageType(models.MessageTypeText).SetContent("batch group")
	groupMsg.AppID = constants.DefaultAppID
	obsMsg := models.NewHubMessage().SetMessageType(models.MessageTypeText).SetContent("batch obs")
	obsMsg.AppID = constants.DefaultAppID
	obsMsg.Namespace = "ns-batch"

	items := []*wscpb.DispatchItem{
		{ // 在线用户投递：Success/UserOnline 双 true
			Operation:    wscpb.DispatchOperation_DISPATCH_SEND_MESSAGE,
			AppId:        constants.DefaultAppID,
			Namespace:    constants.DefaultNamespace,
			TargetUserId: "u-bd-send",
			MessageData:  mustMarshalServerMessage(t, sendMsg),
		},
		{ // 离线用户：Success=false + Error 回传（发送端逐条降级依据）
			Operation:    wscpb.DispatchOperation_DISPATCH_SEND_MESSAGE,
			AppId:        constants.DefaultAppID,
			TargetUserId: "u-bd-miss",
			MessageData:  mustMarshalServerMessage(t, missMsg),
		},
		{ // 踢人：Count=踢出连接数
			Operation:    wscpb.DispatchOperation_DISPATCH_KICK_USER,
			TargetUserId: "u-bd-kick",
			Reason:       "batch-kick",
		},
		{ // 单群组广播（排除发送者）：Count=投递数
			Operation:     wscpb.DispatchOperation_DISPATCH_GROUP_BROADCAST,
			AppId:         constants.DefaultAppID,
			GroupIds:      []string{"g-batch"},
			MessageData:   mustMarshalServerMessage(t, groupMsg),
			ExcludeSender: true,
			SenderId:      "u-bd-g1",
		},
		{ // 观察者通知：Count=通知数
			Operation:   wscpb.DispatchOperation_DISPATCH_OBSERVER_NOTIFY,
			Namespace:   "ns-batch",
			MessageData: mustMarshalServerMessage(t, obsMsg),
		},
		{ // 未知操作类型：单条失败不中断整批，Error 回传
			Operation: wscpb.DispatchOperation_DISPATCH_OPERATION_UNSPECIFIED,
		},
	}

	// 生产路径 gRPC 调用必带 incoming metadata，指令体的路由信封才会被
	// dispatchBatchItem 写入 metadata（复用下游处理器的 Restore 逻辑）
	batchCtx := metadata.NewIncomingContext(context.Background(), metadata.New(nil))

	resp, err := srv.BatchDispatch(batchCtx, &wscpb.BatchDispatchRequest{Items: items})
	require.NoError(t, err)
	results := resp.GetResults()
	require.Len(t, results, len(items), "结果应与请求 items 一一对应")

	assert.True(t, results[0].GetSuccess(), "在线用户投递应成功")
	assert.True(t, results[0].GetUserOnline())
	assert.Empty(t, results[0].GetError())

	assert.False(t, results[1].GetSuccess(), "离线用户应回传失败")
	assert.False(t, results[1].GetUserOnline())
	assert.Equal(t, "用户不在线", results[1].GetError())

	assert.True(t, results[2].GetSuccess(), "踢人应成功")
	assert.EqualValues(t, 1, results[2].GetCount(), "应踢出一条连接")
	require.Eventually(t, func() bool {
		_, ok := hub.shardedRegistry.GetClient(kickTarget.ID)
		return !ok
	}, 2*time.Second, 10*time.Millisecond, "批量踢人目标应被移除")

	assert.True(t, results[3].GetSuccess(), "群组广播应成功")
	assert.EqualValues(t, 1, results[3].GetCount(), "排除发送者后应投递一名成员")

	assert.True(t, results[4].GetSuccess(), "观察者通知应成功")
	assert.EqualValues(t, 1, results[4].GetCount())

	assert.NotEmpty(t, results[5].GetError(), "未知操作类型应回传错误")
	assert.Contains(t, results[5].GetError(), "未知操作类型")
}

// ============================================================================
// resolveAdvertisedGRPCAddr —— 纯函数表驱动
// ============================================================================

// TestResolveAdvertisedGRPCAddrTable 表驱动验证监听地址 → 注册地址转换：
// 显式 host 原样返回 / 通配 host 替换私网 IP / 解析失败原样返回
// （通配场景与 cluster_dispatch_test.go 的 TestResolveAdvertisedGRPCAddr 互补，
// 此处补全 SplitHostPort 错误与显式 IPv6 分支）
func TestResolveAdvertisedGRPCAddrTable(t *testing.T) {
	privateIP, ipErr := netx.GetPrivateIP()

	tests := []struct {
		name     string
		listen   string
		expected string // 期望注册地址（通配行在无私网 IP 时动态跳过）
	}{
		{"显式IPv4原样返回", "10.0.0.1:50052", "10.0.0.1:50052"},
		{"显式IPv6原样返回", "[2001:db8::1]:50052", "[2001:db8::1]:50052"},
		{"主机名原样返回", "grpc.wsc.local:50052", "grpc.wsc.local:50052"},
		{"解析失败原样返回", "no-port", "no-port"},
		{"裸端口通配替换私网IP", ":50052", ""},
		{"IPv4通配替换私网IP", "0.0.0.0:50052", ""},
		{"IPv6通配替换私网IP", "[::]:50052", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveAdvertisedGRPCAddr(tt.listen)
			if tt.expected != "" {
				assert.Equal(t, tt.expected, got)
				return
			}
			// 通配 host 行：测试环境无私网 IP 时原样返回（与生产降级一致）
			if ipErr != nil {
				assert.Equal(t, tt.listen, got, "无私网 IP 时原样返回")
				return
			}
			assert.Equal(t, privateIP+portSuffix(t, tt.listen), got,
				"通配地址应替换为本机私网 IP，端口保留")
		})
	}
}

// portSuffix 提取地址的端口部分（:port）
func portSuffix(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	return ":" + port
}

// ============================================================================
// Start / Stop —— 服务端启停
// ============================================================================

// TestGRPCServerStartStopSmoke 启停冒烟：空闲端口 Start + 真实 dial Ping 往返 +
// Stop 幂等；端口被占用时 Start 失败（不绑端口场景由直调 handler 用例覆盖）
func TestGRPCServerStartStopSmoke(t *testing.T) {
	hub := newGRPCServerTestHub()
	srv := NewGRPCServer(hub)

	// 端口被占用：Start 失败
	blocker, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = blocker.Close() }()
	_, blockedPort, err := net.SplitHostPort(blocker.Addr().String())
	require.NoError(t, err)
	err = srv.Start(context.Background(), "127.0.0.1:"+blockedPort)
	require.Error(t, err, "端口被占用时 Start 应失败")

	// 空闲端口：Start 成功并真实提供服务
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := probe.Addr().String()
	require.NoError(t, probe.Close())
	require.NoError(t, srv.Start(context.Background(), addr))
	defer srv.Stop()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	pingResp, err := wscpb.NewNodeServiceClient(conn).Ping(context.Background(), &wscpb.PingRequest{})
	require.NoError(t, err, "服务端应可接受真实 RPC 往返")
	assert.Equal(t, hub.GetNodeID(), pingResp.GetNodeId())
	assert.True(t, pingResp.GetHealthy())

	// Stop 幂等（server 已构造时重复 GracefulStop 安全）
	srv.Stop()
	assert.NotPanics(t, srv.Stop)
}

// ============================================================================
// 节点 gRPC 生命周期（InitNodeGRPC / wireNodeEvictionCleanup / start/stopNodeGRPC）
// ============================================================================

// TestNodeGRPCLifecycle 节点 gRPC 组件生命周期（miniredis 真实链路）：
//   - 未启用 / PubSub 未设置：InitNodeGRPC 跳过（降级 Redis PubSub 模式）
//   - 启用 + PubSub：注册表/服务端引用/连接池/微批分派器三件套创建
//   - 淘汰联动：注册表剔除幽灵节点时回收连接池中该 addr 的连接
//   - startNodeGRPC：绑定随机端口 + 实际地址回填 + 注册到 Redis
//   - stopNodeGRPC：注销节点 + 停服务端 + 释放包级映射
func TestNodeGRPCLifecycle(t *testing.T) {
	// 未启用 node-grpc：跳过初始化
	hubOff := newGRPCServerTestHub()
	hubOff.InitNodeGRPC()
	assert.Nil(t, hubOff.nodeRegistry, "未启用 node-grpc 时不初始化集群组件")

	// 启用但 PubSub 未设置：节点发现依赖 Redis，跳过初始化
	hubNoPS := NewHub(&wscconfig.WSC{
		ClientTimeout: time.Minute,
		NodeGRPC:      &wscconfig.NodeGRPC{Enabled: true},
	})
	hubNoPS.InitNodeGRPC()
	assert.Nil(t, hubNoPS.nodeRegistry, "PubSub 未设置时无法启用节点 gRPC 通信")
	assert.Nil(t, hubNoPS.grpcClientPool)

	// 启用 + miniredis PubSub：三件套创建 + 服务端引用登记
	mr := miniredis.RunT(t)
	redisClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer func() { _ = redisClient.Close() }()

	hub := NewHub(&wscconfig.WSC{
		ClientTimeout: time.Minute,
		NodeGRPC:      &wscconfig.NodeGRPC{Enabled: true, Host: "127.0.0.1", Port: 0},
	})
	hub.pubsub = cachex.NewPubSub(redisClient)
	defer func() { _ = hub.pubsub.Close() }()
	hub.InitNodeGRPC()

	require.NotNil(t, hub.nodeRegistry)
	require.NotNil(t, hub.grpcClientPool)
	require.NotNil(t, hub.grpcBatchDispatcher)
	_, ok := grpcServers.Load(hub)
	assert.True(t, ok, "InitNodeGRPC 应登记包级映射中的服务端引用")

	// 淘汰联动回收：连接池先建一条到 mock 节点的连接 → 本地登记幽灵节点 →
	// Register 触发 refreshNodes 剔除（Redis 无此节点）→ 联动回收其 gRPC 连接
	mock := &dispatchMockServer{}
	mockAddr, stopMock := startDispatchMockServer(t, mock)
	defer stopMock()
	_, err := hub.grpcClientPool.GetClient(mockAddr)
	require.NoError(t, err)
	require.EqualValues(t, 1, hub.grpcClientPool.ConnectionCount())

	hub.wireNodeEvictionCleanup()
	hub.nodeRegistry.SetNodeAddr("node-ghost", mockAddr)

	// 启动 gRPC 服务端：配置端口 0（随机端口）→ 绑定后回填实际监听地址并注册 Redis
	hub.startNodeGRPC()

	// 注册异步执行（syncx.Go + 5s 超时）：轮询直到 Redis 出现本节点条目、
	// 幽灵节点被剔除并联动回收连接（miniredis HGet 缺失时返回空串）
	grpcKey := hub.config.NodeGRPC.GetNodeGRPCKey()
	require.Eventually(t, func() bool {
		return mr.HGet(grpcKey, hub.GetNodeID()) != "" && hub.grpcClientPool.ConnectionCount() == 0
	}, 3*time.Second, 20*time.Millisecond, "注册到 Redis 且幽灵节点连接被联动回收")

	// 实际监听地址回填：注册表返回的地址 = 服务端 listener 实际绑定地址
	v, ok := grpcServers.Load(hub)
	require.True(t, ok)
	started := v.(*GRPCServer)
	require.NotNil(t, started.listener)
	advertised, ok := hub.nodeRegistry.GetNodeAddr(hub.GetNodeID())
	require.True(t, ok)
	assert.Equal(t, started.listener.Addr().String(), advertised,
		"端口 0 绑定后应回填实际监听地址")

	// 停止：注销 Redis 条目 + 释放包级映射 + 服务端停止
	hub.stopNodeGRPC()
	_, exists := grpcServers.Load(hub)
	assert.False(t, exists, "stopNodeGRPC 应释放包级映射中的服务端引用")
	assert.Empty(t, mr.HGet(grpcKey, hub.GetNodeID()), "停止后 Redis 节点条目应已注销")
}

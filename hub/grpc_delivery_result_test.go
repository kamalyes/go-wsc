package hub

import (
	"context"
	"testing"
	"time"

	"github.com/kamalyes/go-wsc/cluster"
	"github.com/kamalyes/go-wsc/constants"
	"github.com/kamalyes/go-wsc/models"
	pb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/kamalyes/go-wsc/overload"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPCSendToUserReportsAdmissionRefusal(t *testing.T) {
	h := newDispatchTestHub("")
	defer h.grpcClientPool.Close()
	c := models.NewClient("c", "u", models.UserTypeCustomer).WithSendChan(make(chan []byte, 2))
	c.AppID = constants.DefaultAppID
	c.Namespace = constants.DefaultNamespace
	h.shardedRegistry.AddClient(c)
	msg := models.NewHubMessage().SetMessageType(models.MessageTypeNotice).SetReceiver("u")
	msg.AppID = c.AppID
	msg.Namespace = c.Namespace
	raw, err := pb.MarshalHubMessage(msg)
	require.NoError(t, err)
	gate := h.admission.Load()
	gate.ForceLevel(overload.LevelReadOnly)
	response, err := NewGRPCServer(h).SendToUser(context.Background(), &pb.SendToUserRequest{UserId: "u", MessageData: raw})
	require.Equal(t, codes.ResourceExhausted, status.Code(err))
	require.Nil(t, response)
	require.Empty(t, c.SendChan)
	gate.ForceLevel(overload.LevelNormal)
	response, err = NewGRPCServer(h).SendToUser(context.Background(), &pb.SendToUserRequest{UserId: "u", MessageData: raw})
	require.NoError(t, err)
	require.True(t, response.GetSuccess())
	require.Len(t, c.SendChan, 1)
}

func TestAdmissionReplacementRetainsPendingFrames(t *testing.T) {
	h := newDispatchTestHub("")
	defer h.grpcClientPool.Close()
	c := models.NewClient("c", "u", models.UserTypeCustomer).WithSendChan(make(chan []byte, 2))
	c.SetPendingCounter(&h.pendingMessages)
	require.True(t, c.TrySend([]byte("x")))
	g := overload.NewAdmissionGate(0, 0, 0)
	h.SetOverloadPolicy(g, nil, nil, nil)
	defer g.Stop()
	require.EqualValues(t, 1, g.Backlog())
	c.MarkClosed()
	require.Zero(t, g.Backlog())
}

func TestCrossNodeRefusalIsNotSuccessOrUserMiss(t *testing.T) {
	target := newDispatchTestHub("")
	defer target.grpcClientPool.Close()
	c := models.NewClient("c", "u", models.UserTypeCustomer).WithSendChan(make(chan []byte, 1))
	c.AppID = constants.DefaultAppID
	c.Namespace = constants.DefaultNamespace
	target.shardedRegistry.AddClient(c)
	target.admission.Load().ForceLevel(overload.LevelReadOnly)
	addr, stop := startDispatchMockServer(t, NewGRPCServer(target))
	defer stop()
	source := newDispatchTestHub(addr)
	defer source.grpcClientPool.Close()
	msg := models.NewHubMessage().SetMessageType(models.MessageTypeNotice).SetReceiver("u")
	msg.AppID = c.AppID
	msg.Namespace = c.Namespace
	opts := cluster.ClusterDispatchOptions{Operation: models.OperationTypeSendMessage, TargetUserID: "u", TargetNodeIDs: []string{"node-b"}}
	result := source.dispatchViaGRPC(context.Background(), msg, opts)
	require.Zero(t, result.grpcDelivered)
	require.Empty(t, result.userMissNodes)
	require.Empty(t, result.pubsubFallback)
	require.Equal(t, []string{"node-b"}, result.rejectedNodes)
	require.Equal(t, codes.ResourceExhausted, status.Code(source.routeToCluster(context.Background(), msg, opts)))
	raw, err := pb.MarshalHubMessage(msg)
	require.NoError(t, err)
	item := source.buildDispatchItem(context.Background(), opts, raw, "")
	dispatcher := cluster.NewGRPCBatchDispatcher(source.grpcClientPool, time.Millisecond, 10)
	defer dispatcher.Close()
	require.Equal(t, cluster.OutcomeRejected, dispatcher.Submit(context.Background(), addr, item))
	source.grpcBatchDispatcher = dispatcher
	require.Equal(t, codes.ResourceExhausted, status.Code(source.routeToCluster(context.Background(), msg, opts)))
}

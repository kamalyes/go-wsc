package cluster

import (
	"testing"

	"github.com/kamalyes/go-wsc/models"
	pb "github.com/kamalyes/go-wsc/models/pb"
	"github.com/stretchr/testify/require"
)

func TestBatchOnlineRefusalPreservesIndex(t *testing.T) {
	require.Equal(t, OutcomeRejected, classifyOutcome(models.OperationTypeSendMessage, &pb.DispatchResult{UserOnline: true, Success: false, Error: "admission rejected"}))
	require.Equal(t, OutcomeUserMiss, classifyOutcome(models.OperationTypeSendMessage, &pb.DispatchResult{}))
	require.Equal(t, OutcomeDelivered, classifyOutcome(models.OperationTypeSendMessage, &pb.DispatchResult{Success: true, UserOnline: true}))
}

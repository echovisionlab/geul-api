package filemedia

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestManageBulkMediaDeliveryRejectsOversizedRequestUnit(t *testing.T) {
	t.Parallel()
	fileIDs := make([]string, MaxMediaDeliveryBatchSize+1)
	for index := range fileIDs {
		fileIDs[index] = uuid.NewString()
	}
	_, err := NewConnectHandler((&Service{})).GetBulkMediaDeliveries(context.Background(), connect.NewRequest(&managev1.GetBulkMediaDeliveriesRequest{FileIds: fileIDs}))
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

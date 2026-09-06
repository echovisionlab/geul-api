//go:build integration

package label

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestLabelListReturnsPublishedLabelsOnlyIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	seedExternalKratosIdentityWithTraits(t, db, adminID, "Label visibility admin")
	ctx := artistIntegrationAdminCtx(adminID)
	service := newLabelIntegrationService(t, db, adminID, &recordingArtistFileDeleter{})
	suffix := integrationTestUUID()

	publishedID := createIntegrationLabel(t, service, ctx, "Published label "+suffix, "published-label-"+suffix)
	draftID := createIntegrationLabel(t, service, ctx, "Draft label "+suffix, "draft-label-"+suffix)
	_, err := service.PublishLabel(ctx, connect.NewRequest(&managev1.PublishLabelRequest{Id: publishedID}))
	require.NoError(t, err)

	response, err := service.ListLabels(context.Background(), connect.NewRequest(&managev1.ListLabelsRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 100},
		Filters: []*commonv1.FilterSpec{{
			Field: "search",
			Op:    commonv1.FilterOp_FILTER_OP_ILIKE,
			Value: suffix,
		}},
	}))
	require.NoError(t, err)
	require.Equal(t, int32(1), response.Msg.GetPagination().GetTotal())
	require.Len(t, response.Msg.Labels, 1)
	require.Equal(t, publishedID, response.Msg.Labels[0].Id)
	require.NotEqual(t, draftID, response.Msg.Labels[0].Id)
}

package audience_test

import (
	"math"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/audience"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
)

func TestListSegmentsForAuthenticatedAccessHasNoMoreBeyondTheLastPage(t *testing.T) {
	h := newHarness(t)
	h.store.SeedSegment(audience.Segment{Name: "Filtered", Type: audience.SegmentMembersByFilter})
	handler := audience.NewConnectHandler(h.service)
	for name, test := range map[string]struct {
		pagination *commonv1.PaginationRequest
		hasMore    bool
	}{
		"first page":     {&commonv1.PaginationRequest{Limit: 1}, false},
		"largest offset": {&commonv1.PaginationRequest{Limit: 100, Offset: math.MaxInt32 - 10}, false},
	} {
		response, err := handler.ListSegmentsForAuthenticatedAccess(h.admin, connect.NewRequest(&managev1.ListSegmentsForAuthenticatedAccessRequest{Pagination: test.pagination}))
		require.NoError(t, err, name)
		require.EqualValues(t, 1, response.Msg.GetPagination().GetTotal(), name)
		require.Equal(t, test.hasMore, response.Msg.GetPagination().GetHasMore(), name)
	}
}

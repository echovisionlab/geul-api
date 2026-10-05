//go:build integration

package referencecatalog_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/testutil"
	commonv1 "github.com/echovisionlab/geul-event-contracts/gen/api/common/v1"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestListMapPlacesAdminChecksCurrentGlobalPermissionIntegration(t *testing.T) {
	stack := testutil.PrepareOryIntegrationTest(t)
	ctx, identityID, _, spiceDB := mapPlaceAuditActor(t, stack, policyv1.Role.Admin())
	service := mapPlaceServiceForTest(t, stack.DB, "https://cdn.example.com", spiceDB)
	name := "Admin list place " + testutil.IntegrationUUID()
	created, err := service.CreateMapPlace(ctx, connect.NewRequest(&managev1.CreateMapPlaceRequest{Name: name, Address: "1 Admin Road", Lat: 0, Lng: 0}))
	require.NoError(t, err)
	request := connect.NewRequest(&managev1.ListMapPlacesAdminRequest{
		Pagination: &commonv1.PaginationRequest{Limit: 1},
		Filters:    []*commonv1.FilterSpec{{Field: "search", Op: commonv1.FilterOp_FILTER_OP_ILIKE, Value: name}},
	})
	listed, err := service.ListMapPlacesAdmin(ctx, request)
	require.NoError(t, err)
	require.Equal(t, int32(1), listed.Msg.Pagination.Total)
	require.Len(t, listed.Msg.Places, 1)
	require.Equal(t, created.Msg.Id, listed.Msg.Places[0].Id)
	// Keep the original request principal while changing live SpiceDB authority.
	for _, role := range []policyv1.RoleID{policyv1.Role.Author(), policyv1.Role.User()} {
		setMapPlaceAuditActorRole(t, spiceDB, identityID, role)
		_, err = service.ListMapPlacesAdmin(ctx, request)
		require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	}
	setMapPlaceAuditActorRole(t, spiceDB, identityID, policyv1.Role.Admin())
	_, err = service.ListMapPlacesAdmin(ctx, request)
	require.NoError(t, err)
	_, err = service.ListMapPlacesAdmin(context.Background(), request)
	require.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
}

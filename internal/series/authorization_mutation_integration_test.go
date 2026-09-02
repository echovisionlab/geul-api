//go:build integration

package series

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func TestSeriesAuthorizationMutationsAreSynchronousIntegration(t *testing.T) {
	db := newServiceIntegrationDB(t)
	stack := testutil.SetupOryStack(t)
	adminID := integrationTestUUID()
	managerID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series authorization admin")
	seedSeriesActor(t, db, managerID, "Series authorization manager")
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	service := newSeriesIntegrationService(t, db, adminID, &recordingSeriesFileDeleter{})

	created, err := service.CreateSeries(seriesIntegrationAdminCtx(adminID), connect.NewRequest(&managev1.CreateSeriesRequest{Title: "Synchronous Series"}))
	require.NoError(t, err)
	seriesID := created.Msg.Id
	requireSeriesManagerRelation(t, service, adminID, seriesID, true)

	added, err := service.AddSeriesManager(seriesIntegrationAdminCtx(adminID), connect.NewRequest(&managev1.AddSeriesManagerRequest{
		SeriesId: seriesID, MemberId: integrationMemberID(managerID),
	}))
	require.NoError(t, err)
	require.True(t, added.Msg.Success)
	requireSeriesManagerRelation(t, service, managerID, seriesID, true)

	idempotentAdd, err := service.AddSeriesManager(seriesIntegrationAdminCtx(adminID), connect.NewRequest(&managev1.AddSeriesManagerRequest{
		SeriesId: seriesID, MemberId: integrationMemberID(managerID),
	}))
	require.NoError(t, err)
	require.True(t, idempotentAdd.Msg.Success)

	removed, err := service.RemoveSeriesManager(seriesIntegrationAdminCtx(adminID), connect.NewRequest(&managev1.RemoveSeriesManagerRequest{
		SeriesId: seriesID, MemberId: integrationMemberID(managerID),
	}))
	require.NoError(t, err)
	require.True(t, removed.Msg.Success)
	requireSeriesManagerRelation(t, service, managerID, seriesID, false)

	// Deletion snapshots and removes every current Series relationship, not only
	// the policy edge. Leave an active Manager edge in place for that assertion.
	_, err = service.AddSeriesManager(seriesIntegrationAdminCtx(adminID), connect.NewRequest(&managev1.AddSeriesManagerRequest{
		SeriesId: seriesID, MemberId: integrationMemberID(managerID),
	}))
	require.NoError(t, err)

	deleted, err := service.DeleteSeries(seriesIntegrationAdminCtx(adminID), connect.NewRequest(&managev1.DeleteSeriesRequest{Id: seriesID}))
	require.NoError(t, err)
	require.True(t, deleted.Msg.Success)
	requireSeriesManagerRelation(t, service, adminID, seriesID, false)
	requireSeriesManagerRelation(t, service, managerID, seriesID, false)
}

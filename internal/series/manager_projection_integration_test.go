//go:build integration

package series

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	managev1 "github.com/echovisionlab/geul-event-contracts/gen/api/manage/v1"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

// Series manager membership is a product projection owned by the series
// service. Losing the account identity must not erase that durable
// attribution, while the removed identity must immediately lose SpiceDB
// authority to list or mutate the series.
func TestSeriesManagerProjectionSurvivesIdentityDisappearanceIntegration(t *testing.T) {
	stack := testutil.SetupOryStack(t)
	db := newServiceIntegrationDB(t)
	adminID := integrationTestUUID()
	identityID := integrationTestUUID()
	seedSeriesActor(t, db, adminID, "Series relation reader")
	seedSeriesActor(t, db, identityID, "Former Series manager")
	memberID := integrationMemberID(identityID)
	seriesID := seedSeriesRow(
		t, db, "Durable Series manager", "durable-series-manager-"+identityID,
		managev1.SeriesStatus_SERIES_STATUS_DRAFT.String(),
	)

	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	formerSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	formerActor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	policy, err := policyv1.PostSeries.TouchPolicy(seriesID)
	require.NoError(t, err)
	touchManager, err := policyv1.PostSeries.TouchManager(seriesID, formerActor)
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.ApplyRelationships(t.Context(), policy, touchManager)
	require.NoError(t, err)
	require.NoError(t, db.Exec(
		`INSERT INTO series_manager (series_id, member_id, created_at, updated_at)
		 VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`, seriesID, memberID,
	).Error)

	runtime := seriesadapter.NewRuntime(db, "", newOGRefresherForTest(db, ""))
	service := NewSeriesService(
		db,
		runtime,
		runtime,
		runtime,
		stack.SpiceDBClient,
		testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en")),
		seriesIntegrationMenuTargets(),
		seriesadapter.PostAccess{},
		seriesadapter.NewMemberSummaries(db, ""),
	)

	// Identity deletion removes its authorization relationships but leaves the
	// product's series_manager attribution row intact.
	_, err = stack.SpiceDBClient.DeleteAllAccountIdentityRelationships(t.Context(), formerSubject)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`DELETE FROM kratos.identities WHERE id = ?::uuid`, identityID).Error)

	formerCtx := auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(identityID),
		MemberID:      auth.MemberID(memberID),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
	})
	response, err := service.ListMySeries(formerCtx, connect.NewRequest(&managev1.ListMySeriesRequest{}))
	require.NoError(t, err)
	require.Empty(t, response.Msg.Series, "removed SpiceDB authority must stop ListMySeries effectiveness")

	adminCtx := seriesIntegrationAdminCtx(adminID)
	detail, err := service.GetSeriesWithManagers(adminCtx, connect.NewRequest(&managev1.GetSeriesWithManagersRequest{Id: seriesID}))
	require.NoError(t, err)
	require.Len(t, detail.Msg.Managers, 1)
	require.Equal(t, memberID, detail.Msg.Managers[0].MemberId)
	listed, err := service.ListSeriesManagers(adminCtx, connect.NewRequest(&managev1.ListSeriesManagersRequest{SeriesId: seriesID}))
	require.NoError(t, err)
	require.Len(t, listed.Msg.Managers, 1)
	require.Equal(t, memberID, listed.Msg.Managers[0].MemberId)

	nextTitle := "Former manager cannot edit"
	_, err = service.UpdateSeries(formerCtx, connect.NewRequest(&managev1.UpdateSeriesRequest{Id: seriesID, Title: &nextTitle}))
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, err = service.RemoveSeriesManager(adminCtx, connect.NewRequest(&managev1.RemoveSeriesManagerRequest{
		SeriesId: seriesID,
		MemberId: memberID,
	}))
	require.NoError(t, err)
	var count int64
	require.NoError(t, db.Table("series_manager").Where("series_id = ? AND member_id = ?", seriesID, memberID).Count(&count).Error)
	require.Zero(t, count)
}

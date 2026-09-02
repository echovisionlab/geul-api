//go:build integration

package series

import (
	"context"
	"testing"

	"gorm.io/gorm"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func newSeriesIntegrationService(
	t *testing.T,
	db *gorm.DB,
	adminID string,
	fileDeleter *recordingSeriesFileDeleter,
) *SeriesService {
	t.Helper()
	_ = fileDeleter
	stack := testutil.SetupOryStack(t)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	runtime := seriesadapter.NewRuntime(db, "", newOGRefresherForTest(db, ""))
	return NewSeriesService(
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
}

func seriesIntegrationAdminCtx(id string) context.Context {
	return auth.WithUser(context.Background(), &auth.UserInfo{
		IdentityID:    auth.IdentityID(id),
		MemberID:      auth.MemberID(integrationMemberID(id)),
		SessionID:     auth.SessionID(integrationTestUUID()),
		Authenticated: true,
	})
}

func addSeriesManagerRelation(t *testing.T, service *SeriesService, identityID, seriesID string) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	mutation, err := policyv1.PostSeries.TouchManager(seriesID, actor)
	require.NoError(t, err)
	_, err = service.spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func removeSeriesManagerRelation(t *testing.T, service *SeriesService, identityID, seriesID string) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	mutation, err := policyv1.PostSeries.DeleteManager(seriesID, actor)
	require.NoError(t, err)
	_, err = service.spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func requireSeriesManagerRelation(t *testing.T, service *SeriesService, identityID, seriesID string, want bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := policyv1.PostSeries.Manage(seriesID)
	require.NoError(t, err)
	got, err := service.spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

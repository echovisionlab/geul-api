//go:build integration

package series

import (
	"context"
	"testing"

	"gorm.io/gorm"

	seriesadapter "github.com/echovisionlab/geul-api/internal/adapters/series"
	"github.com/echovisionlab/geul-api/internal/auth"
	apitelemetry "github.com/echovisionlab/geul-api/internal/telemetry"
	"github.com/echovisionlab/geul-api/internal/testutil"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
	"github.com/stretchr/testify/require"
)

func newSeriesIntegrationService(
	t *testing.T,
	db *gorm.DB,
	adminID string,
	fileDeleter *recordingSeriesFileDeleter,
) seriesIntegrationService {
	t.Helper()
	_ = fileDeleter
	stack := testutil.SetupOryStack(t)
	adminSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(adminID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.SyncAccountIdentityGlobalRole(t.Context(), adminSubject, policyv1.Role.Admin())
	require.NoError(t, err)
	runtime := seriesadapter.NewRuntime(db, "", newOGRefresherForTest(db, ""))
	return newSeriesIntegrationHandler(t, Deps{
		DB: db, SpiceDB: stack.SpiceDBClient,
		Identity: testutil.NewPostIdentityManager(testutil.PostIntegrationIdentity(adminID, "en")),
		Media:    runtime, OG: runtime, Reads: runtime, MenuTargets: seriesIntegrationMenuTargets(),
		Posts: seriesadapter.PostAccess{}, Members: seriesadapter.NewMemberSummaries(db, ""),
		Audit: apitelemetry.NewDurableWriter(db),
	})
}

// seriesIntegrationService serves the Series RPCs and exposes the use cases
// other adapters call.
type seriesIntegrationService struct {
	*ConnectHandler
	domain  *Service
	spiceDB *auth.SpiceDBClient
}

func newSeriesIntegrationHandler(t *testing.T, deps Deps) seriesIntegrationService {
	t.Helper()
	service := newTestService(t, deps)
	return seriesIntegrationService{ConnectHandler: NewConnectHandler(service), domain: service, spiceDB: service.spiceDB}
}

func seriesIntegrationAdminCtx(t *testing.T, id string) context.Context {
	t.Helper()
	return testutil.NewAuditContext(t, id, testutil.PostIntegrationMemberID(id))
}

func addSeriesManagerRelation(t *testing.T, service seriesIntegrationService, identityID, seriesID string) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	mutation, err := policyv1.PostSeries.TouchManager(seriesID, actor)
	require.NoError(t, err)
	_, err = service.spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func removeSeriesManagerRelation(t *testing.T, service seriesIntegrationService, identityID, seriesID string) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	mutation, err := policyv1.PostSeries.DeleteManager(seriesID, actor)
	require.NoError(t, err)
	_, err = service.spiceDB.ApplyRelationships(t.Context(), mutation)
	require.NoError(t, err)
}

func requireSeriesManagerRelation(t *testing.T, service seriesIntegrationService, identityID, seriesID string, want bool) {
	t.Helper()
	actor, err := policyv1.NewAccountIdentityActor(identityID)
	require.NoError(t, err)
	can, err := policyv1.PostSeries.Manage(seriesID)
	require.NoError(t, err)
	got, err := service.spiceDB.CheckActorCan(t.Context(), actor, can)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

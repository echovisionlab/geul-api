//go:build integration

package series_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/echovisionlab/geul-api/internal/auth"
	"github.com/echovisionlab/geul-api/internal/model"
	"github.com/echovisionlab/geul-api/internal/series"

	"github.com/echovisionlab/geul-api/internal/testutil/orytest"
	"github.com/echovisionlab/geul-api/internal/testutil/pgtest"
	policyv1 "github.com/echovisionlab/geul-event-contracts/gen/api/policy/v1"
)

func requireManager(t *testing.T, spiceDB *auth.SpiceDBClient, identityID, seriesID string, want bool) {
	t.Helper()
	got, err := spiceDB.CheckActorCan(t.Context(), must(policyv1.NewAccountIdentityActor(identityID)), must(policyv1.PostSeries.Manage(seriesID)))
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func memberContext(t *testing.T, identityID string) context.Context {
	t.Helper()
	return principalContext(t, identityID, orytest.PostIntegrationMemberID(identityID))
}

// The SpiceDB schema gives the policy edge and the manager edge their
// meaning; the batches a mutation writes, and the snapshot a deletion
// removes, are only checked against it here. The manager targets are real
// Members linked to Kratos Identities.
func TestSeriesRelationshipsFollowItsMutationsIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewIntegrationDB(t)
	stack := orytest.SetupOryStack(t)
	adminID, managerID := orytest.IntegrationUUID(), orytest.IntegrationUUID()
	orytest.SeedPostIntegrationIdentity(t, db, adminID, "Series authorization admin")
	orytest.SeedPostIntegrationIdentity(t, db, managerID, "Series authorization manager")
	orytest.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	service := newIntegrationService(t, db, integrationDeps{spiceDB: stack.SpiceDBClient, client: stack.SpiceDBClient})
	ctx := memberContext(t, adminID)

	created, err := service.CreateSeries(ctx, series.CreateSeriesCommand{Title: "Synchronous Series"})
	require.NoError(t, err)
	seriesID := created.Series.ID
	requireManager(t, stack.SpiceDBClient, adminID, seriesID, true)

	managerMemberID := orytest.PostIntegrationMemberID(managerID)
	require.NoError(t, service.AddSeriesManager(ctx, series.AddSeriesManagerCommand{SeriesID: seriesID, MemberID: managerMemberID}))
	requireManager(t, stack.SpiceDBClient, managerID, seriesID, true)
	require.NoError(t, service.AddSeriesManager(ctx, series.AddSeriesManagerCommand{SeriesID: seriesID, MemberID: managerMemberID}))
	require.NoError(t, service.RemoveSeriesManager(ctx, series.RemoveSeriesManagerCommand{SeriesID: seriesID, MemberID: managerMemberID}))
	requireManager(t, stack.SpiceDBClient, managerID, seriesID, false)

	// Deletion snapshots and removes every current relationship, not only the
	// policy edge; an active manager edge stays for that assertion.
	require.NoError(t, service.AddSeriesManager(ctx, series.AddSeriesManagerCommand{SeriesID: seriesID, MemberID: managerMemberID}))
	require.NoError(t, service.DeleteSeries(ctx, series.DeleteSeriesCommand{ID: seriesID}))
	requireManager(t, stack.SpiceDBClient, adminID, seriesID, false)
	requireManager(t, stack.SpiceDBClient, managerID, seriesID, false)
}

// Series manager attribution is a product record: losing the Identity keeps
// it, while the removed Identity loses its SpiceDB authority at once and the
// retired attribution is cleared without a relationship write.
func TestSeriesManagerProjectionSurvivesIdentityDisappearanceIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewIntegrationDB(t)
	stack := orytest.SetupOryStack(t)
	adminID, identityID := orytest.IntegrationUUID(), orytest.IntegrationUUID()
	orytest.SeedPostIntegrationIdentity(t, db, adminID, "Series relation reader")
	orytest.SeedPostIntegrationIdentity(t, db, identityID, "Former Series manager")
	orytest.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	memberID := orytest.PostIntegrationMemberID(identityID)
	seriesID := seedSeriesRow(t, db, "Durable Series manager", "durable-series-manager-"+identityID, model.SeriesStatusDraft)
	formerActor := must(policyv1.NewAccountIdentityActor(identityID))
	_, err := stack.SpiceDBClient.ApplyRelationships(t.Context(),
		must(policyv1.PostSeries.TouchPolicy(seriesID)), must(policyv1.PostSeries.TouchManager(seriesID, formerActor)))
	require.NoError(t, err)
	require.NoError(t, db.Exec(`INSERT INTO series_manager (series_id, member_id) VALUES (?, ?)`, seriesID, memberID).Error)
	service := newIntegrationService(t, db, integrationDeps{spiceDB: stack.SpiceDBClient, client: stack.SpiceDBClient})

	formerSubject, err := auth.NewAccountIdentitySubject(auth.IdentityID(identityID))
	require.NoError(t, err)
	_, err = stack.SpiceDBClient.DeleteAllAccountIdentityRelationships(t.Context(), formerSubject)
	require.NoError(t, err)
	require.NoError(t, db.Exec(`DELETE FROM kratos.identities WHERE id = ?::uuid`, identityID).Error)

	formerCtx := memberContext(t, identityID)
	mine, err := service.ListMySeries(formerCtx, series.ListMySeriesQuery{})
	require.NoError(t, err)
	require.Empty(t, mine.Items, "removed SpiceDB authority stops ListMySeries")
	_, err = service.UpdateSeries(formerCtx, series.UpdateSeriesCommand{ID: seriesID, Title: text("Former manager cannot edit")})
	require.Equal(t, connect.CodeNotFound, codeOf(err))

	adminCtx := memberContext(t, adminID)
	managers, err := service.ListSeriesManagers(adminCtx, series.ListSeriesManagersQuery{SeriesID: seriesID})
	require.NoError(t, err)
	require.Len(t, managers, 1)
	require.Equal(t, memberID, managers[0].MemberID)
	require.NoError(t, service.RemoveSeriesManager(adminCtx, series.RemoveSeriesManagerCommand{SeriesID: seriesID, MemberID: memberID}))
	requireCount(t, db, 0, "series_manager", "series_id = ? AND member_id = ?", seriesID, memberID)
}

// A mutation that waited on the Series root lock authorizes as of when it
// proceeds: authority revoked while it waited answers not found, and the
// change never commits.
func TestSeriesMutationRechecksAuthorityAfterOwningRowLockIntegration(t *testing.T) {
	t.Parallel()
	db := orytest.NewConcurrentPostIntegrationDB(t)
	stack := orytest.SetupOryStack(t)
	adminID, managerID := orytest.IntegrationUUID(), orytest.IntegrationUUID()
	orytest.SeedPostIntegrationIdentity(t, db, adminID, "Series authority admin")
	orytest.SeedPostIntegrationIdentity(t, db, managerID, "Series authority manager")
	orytest.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.Admin())
	service := newIntegrationService(t, db, integrationDeps{spiceDB: stack.SpiceDBClient, client: stack.SpiceDBClient})
	seriesID := seedSeriesRow(t, db, "Authority Series", "authority-series-"+managerID, model.SeriesStatusDraft)
	managerActor := must(policyv1.NewAccountIdentityActor(managerID))
	_, err := stack.SpiceDBClient.ApplyRelationships(t.Context(),
		must(policyv1.PostSeries.TouchPolicy(seriesID)), must(policyv1.PostSeries.TouchManager(seriesID, managerActor)))
	require.NoError(t, err)

	waitBehindRootLock := func(revoke func(), run func() error) error {
		t.Helper()
		blocker := db.Begin()
		require.NoError(t, blocker.Error)
		t.Cleanup(func() { _ = blocker.Rollback().Error })
		require.NoError(t, blocker.Exec(`SELECT id FROM series WHERE id = ? FOR UPDATE`, seriesID).Error)
		done := make(chan error, 1)
		go func() { done <- run() }()
		pgtest.RequireLockWaiters(t, db, 1)
		revoke()
		require.NoError(t, blocker.Commit().Error)
		return <-done
	}

	err = waitBehindRootLock(func() {
		_, err := stack.SpiceDBClient.ApplyRelationships(t.Context(), must(policyv1.PostSeries.DeleteManager(seriesID, managerActor)))
		require.NoError(t, err)
	}, func() error {
		_, err := service.UpdateSeries(memberContext(t, managerID), series.UpdateSeriesCommand{ID: seriesID, Title: text("Must not commit after revoke")})
		return err
	})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	var title string
	require.NoError(t, db.Raw(`SELECT title FROM series_translation WHERE entity_id = ? AND locale = 'en'`, seriesID).Scan(&title).Error)
	require.Equal(t, "Authority Series", title)

	err = waitBehindRootLock(func() {
		orytest.GrantIntegrationGlobalRole(t, stack.SpiceDBClient, adminID, policyv1.Role.User())
	}, func() error {
		return service.DeleteSeries(memberContext(t, adminID), series.DeleteSeriesCommand{ID: seriesID})
	})
	require.Equal(t, connect.CodeNotFound, codeOf(err))
	requireCount(t, db, 1, "series", "id = ?", seriesID)
}
